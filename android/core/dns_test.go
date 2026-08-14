//go:build linux

package core

// Проверка подставных адресов на настоящих запросах: резолвер системы
// спрашивает имя через tun, получает выдуманный адрес, идёт на него — и наверх
// должно уйти имя, а не адрес. Именно на этом держится то, что имена сайтов не
// видны в сети и что правила по именам продолжают работать.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// recordingCore запоминает, что именно пришло наверх, и соединяется напрямую.
type recordingCore struct {
	mu      sync.Mutex
	targets []string
	byIP    []bool
	dialTo  string // куда на самом деле вести соединение
}

func (c *recordingCore) ServeConn(conn net.Conn, target string, byIP bool) {
	defer conn.Close()
	c.mu.Lock()
	c.targets = append(c.targets, target)
	c.byIP = append(c.byIP, byIP)
	c.mu.Unlock()

	remote, err := net.DialTimeout("tcp", c.dialTo, 5*time.Second)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, remote); done <- struct{}{} }()
	<-done
}

func (c *recordingCore) seen() ([]string, []bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.targets...), append([]bool(nil), c.byIP...)
}

var dnsSeq int32

func TestИмяДоходитДоЯдра(t *testing.T) {
	target := httpTarget(t)

	n := byte(atomic.AddInt32(&dnsSeq, 1) + 150)
	dev, err := OpenTun(fmt.Sprintf("tundns%d", n),
		net.IPv4(198, 18, n, 1), net.CIDRMask(24, 32), 1500)
	if err != nil {
		t.Skipf("tun-устройство недоступно: %v", err)
	}

	// Пул выдаёт адреса из той же подсети, что и интерфейс, — иначе ответ
	// приведёт приложение туда, куда нет маршрута.
	pool, err := NewFakePool(fmt.Sprintf("198.18.%d.0/24", n))
	if err != nil {
		t.Fatal(err)
	}

	core := &recordingCore{dialTo: fmt.Sprintf("127.0.0.1:%d", target.Port)}
	st := &Stats{}
	eng, err := Start(dev.FD, 1500, &Handler{
		Core:    core,
		Stats:   st,
		Resolve: pool.Resolver(),
		DNS:     &DNS{Pool: pool},
	})
	if err != nil {
		t.Fatalf("стек: %v", err)
	}
	t.Cleanup(eng.Close)

	dnsAddr := fmt.Sprintf("198.18.%d.53:53", n)
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return net.Dial("udp", dnsAddr)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addrs, err := resolver.LookupHost(ctx, "пример.тест")
	if err != nil {
		// Имя с не-ASCII буквами резолвер отвергнет ещё до запроса.
		addrs, err = resolver.LookupHost(ctx, "example.test")
		if err != nil {
			t.Fatalf("имя не разрешилось: %v", err)
		}
	}
	if len(addrs) == 0 {
		t.Fatal("ответ пустой")
	}
	fake := addrs[0]
	if !strings.HasPrefix(fake, fmt.Sprintf("198.18.%d.", n)) {
		t.Fatalf("выдан адрес %s, а ожидался подставной из нашей подсети", fake)
	}
	t.Logf("имя example.test получило подставной адрес %s", fake)

	// Теперь идём на этот адрес обычным запросом.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/hello", fake, target.Port))
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "привет от") {
		t.Fatalf("пришло не то: %q", body)
	}

	targets, byIP := core.seen()
	if len(targets) == 0 {
		t.Fatal("наверх не пришло ни одного соединения")
	}
	want := fmt.Sprintf("example.test:%d", target.Port)
	if targets[0] != want {
		t.Fatalf("наверх ушло %q, а должно было имя %q", targets[0], want)
	}
	if byIP[0] {
		t.Fatal("соединение помечено как «адрес известен заранее» — имя потерялось")
	}

	_, _, dnsAsked, _ := st.Snapshot()
	t.Logf("запросов DNS обслужено: %d, наверх ушло: %s", dnsAsked, targets[0])
}

// Запрос AAAA должен получать пустой, но успешный ответ: иначе приложение
// будет ждать адрес IPv6, которого через туннель всё равно не будет.
func TestОтветНаAAAAПустой(t *testing.T) {
	pool, err := NewFakePool("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	d := &DNS{Pool: pool}

	reply, err := d.Answer(buildQuery(t, "example.test", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("ответ не построился: %v", err)
	}

	var p dnsmessage.Parser
	h, err := p.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	if h.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("код ответа %v, ожидался успех", h.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	answers, err := p.AllAnswers()
	if err != nil && err != dnsmessage.ErrSectionDone {
		t.Fatal(err)
	}
	if len(answers) != 0 {
		t.Fatalf("в ответе %d записей, ожидался пустой", len(answers))
	}
}

// Имя, которому положено идти мимо туннеля, подставным адресом отвечать нельзя:
// соединение ушло бы на сервер вместо домашней сети. Без локального резолвера
// честнее вернуть отказ.
func TestИмяМимоТуннеляБезРезолвераОтклоняется(t *testing.T) {
	pool, err := NewFakePool("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	d := &DNS{
		Pool:   pool,
		Direct: func(name string) bool { return strings.HasSuffix(name, ".home") },
	}

	reply, err := d.Answer(buildQuery(t, "nas.home", dnsmessage.TypeA))
	if err != nil {
		t.Fatalf("ответ не построился: %v", err)
	}
	var p dnsmessage.Parser
	h, err := p.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	if h.RCode == dnsmessage.RCodeSuccess {
		t.Fatal("на локальное имя выдан успешный ответ с подставным адресом")
	}
}

func TestПулПомнитВыданное(t *testing.T) {
	pool, err := NewFakePool("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	first := pool.Get("example.test")
	again := pool.Get("example.test")
	if !first.Equal(again) {
		t.Fatalf("одному имени выданы разные адреса: %s и %s", first, again)
	}
	name, ok := pool.Name(first.String())
	if !ok || name != "example.test" {
		t.Fatalf("адрес %s не привёл обратно к имени: %q, %v", first, name, ok)
	}
	other := pool.Get("second.test")
	if other.Equal(first) {
		t.Fatal("двум именам достался один адрес")
	}
}

func buildQuery(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 42, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name + "."),
		Type:  typ,
		Class: dnsmessage.ClassINET,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}
