package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestПраваВыключеныВсёМожно(t *testing.T) {
	var p policy
	if !p.allowed("tun_a", "tun_b", 22) || !p.visible("tun_a", "tun_b") {
		t.Fatal("без политики должно быть разрешено всё")
	}
}

func TestПраваГруппыИПорты(t *testing.T) {
	p := policy{
		Enabled: true,
		Users: map[string][]string{
			"tun_pc":     {"Мои"},
			"tun_phone":  {"Мои"},
			"tun_tv":     {"Дом"},
			"tun_guest":  {"Гости"},
			"tun_guest2": {"Гости"},
		},
		Rules: []policyRule{
			{From: "Мои", To: "*"},
			{From: "Дом", To: "@servers", Ports: "53"},
			{From: "Гости", To: "Дом", Ports: "8080"},
		},
	}
	cases := []struct {
		from, to string
		port     int
		want     bool
	}{
		{"tun_pc", "tun_tv", 22, true},
		{"tun_pc", serversGroup, 22, true},
		{"tun_pc", "tun_phone", 0, true},
		{"tun_tv", "tun_pc", 22, false},
		{"tun_tv", serversGroup, 53, true},
		{"tun_tv", serversGroup, 22, false},
		{"tun_guest", "tun_tv", 8080, true},
		{"tun_guest", "tun_tv", 0, false}, // «на любой порт» — нет
		{"tun_guest", "tun_pc", 8080, false},
		{"meshlink", "tun_pc", 22, false}, // не в политике — только «*»
		{serversGroup, "tun_pc", 22, false},
	}
	for _, c := range cases {
		if got := p.allowed(c.from, c.to, c.port); got != c.want {
			t.Errorf("%s → %s:%d = %v, ждали %v", c.from, c.to, c.port, got, c.want)
		}
	}
	if !p.visible("tun_tv", "tun_pc") {
		t.Error("телевизор должен видеть ПК: ПК к нему ходить может")
	}
	if p.visible("tun_guest", "tun_guest2") {
		t.Error("гости не должны видеть друг друга")
	}
	if p.visible("tun_guest", serversGroup) {
		t.Error("гость не должен видеть серверы")
	}
}

func TestПраваПроверка(t *testing.T) {
	bad := []policy{
		{Rules: []policyRule{{From: "", To: "*"}}},
		{Rules: []policyRule{{From: "@servers", To: "*"}}},
		{Rules: []policyRule{{From: "*", To: "*", Ports: "99999"}}},
	}
	for i, p := range bad {
		if p.validate() == nil {
			t.Errorf("политика %d должна быть отвергнута", i)
		}
	}
}

func TestПраваИспорченныйФайлЗакрываетВсё(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "policy.json"), []byte("{испорчено"), 0o600)
	s := newServer(filepath.Join(dir, "state.json"))
	if !s.policy.Enabled || s.policy.allowed("tun_a", "tun_b", 22) {
		t.Fatal("испорченный файл прав должен закрывать доступ")
	}
}

func TestПраваСохраняются(t *testing.T) {
	dir := t.TempDir()
	s := newServer(filepath.Join(dir, "state.json"))
	p := policy{Enabled: true, Users: map[string][]string{"tun_a": {"Мои"}}, Rules: []policyRule{{From: "Мои", To: "*"}}}
	if err := s.setPolicy(p); err != nil {
		t.Fatal(err)
	}
	s2 := newServer(filepath.Join(dir, "state.json"))
	if !s2.policy.Enabled || !s2.policy.allowed("tun_a", "tun_b", 1) || s2.policy.allowed("tun_b", "tun_a", 1) {
		t.Fatalf("после перезапуска права другие: %+v", s2.policy)
	}
}

func TestВладелецСокета(t *testing.T) {
	// 127.0.0.1:40000 → 127.0.0.1:47831 у uid 1005, и встречный сокет meshd.
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:BA77 0100007F:9C40 01 00000000:00000000 00:00000000 00000000   997        0 1 1 0000000000000000 20 4 30 10 -1
   1: 0100007F:9C40 0100007F:BA77 01 00000000:00000000 00:00000000 00000000  1005        0 2 1 0000000000000000 20 4 30 10 -1
`
	sshd := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}
	meshd := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 47735}
	uid, ok := socketUID(table, sshd, meshd)
	if !ok || uid != "1005" {
		t.Fatalf("uid = %q %v, ждали 1005", uid, ok)
	}
	if _, ok := socketUID(table, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, meshd); ok {
		t.Fatal("нашёлся несуществующий сокет")
	}
}

// TestПраваНаСоединениях — meshd сам проверяет права: запрещённый вызов
// получает отказ, разрешённый доходит до адресата, а запрещённого
// устройства нет в списке.
func TestПраваНаСоединениях(t *testing.T) {
	s := newServer("")
	var mu sync.Mutex
	users := map[string]string{} // локальный адрес клиента → пользователь
	s.connUser = func(c net.Conn) string {
		mu.Lock()
		defer mu.Unlock()
		return users[c.RemoteAddr().String()]
	}
	s.policy = policy{Enabled: true,
		Users: map[string][]string{"tun_pc": {"Мои"}},
		Rules: []policyRule{{From: "Мои", To: "*"}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	open := func(user string, m msg) (net.Conn, *bufio.Reader) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		mu.Lock()
		users[c.LocalAddr().String()] = user
		mu.Unlock()
		writeMsg(c, m)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		return c, bufio.NewReader(c)
	}
	read := func(r *bufio.Reader) msg {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m msg
		json.Unmarshal(line, &m)
		return m
	}
	_, pcR := open("tun_pc", msg{Op: "hello", Net: "k", Device: "pc", Name: "pc"})
	pcW := read(pcR)
	_, tvR := open("tun_tv", msg{Op: "hello", Net: "k", Device: "tv", Name: "tv"})
	tvW := read(tvR)
	if tvW.Op != "welcome" || pcW.Op != "welcome" {
		t.Fatal("нет welcome")
	}
	for _, p := range tvW.Peers {
		if p.Platform == "server" {
			t.Error("телевизору не разрешены серверы, а он их видит")
		}
	}

	// Телевизор → ПК: нельзя.
	_, r := open("tun_tv", msg{Op: "dial", Net: "k", Device: "tv", To: pcW.IP, Port: 22})
	if m := read(r); m.Op != "error" {
		t.Fatalf("телевизора пустили к ПК: %+v", m)
	}
	// Чужой id: пользователь гостя звонит от имени ПК — отказ.
	_, r = open("tun_tv", msg{Op: "dial", Net: "k", Device: "pc", To: tvW.IP, Port: 22})
	if m := read(r); m.Op != "error" {
		t.Fatalf("пустили звонок от чужого имени: %+v", m)
	}
	// ПК → телевизор: вызов доходит до телевизора.
	open("tun_pc", msg{Op: "dial", Net: "k", Device: "pc", To: tvW.IP, Port: 22})
	for {
		m := read(tvR)
		if m.Op == "incoming" {
			break
		}
	}
}
