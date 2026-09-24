package mesh

// Сквозная проверка: настоящий бинарник meshd (src/cmd/meshd), собранный из
// тех же исходников, что уйдут на сервер, и клиенты из этого пакета. В жизни
// клиенты ходят к meshd через SSH-туннель; здесь — напрямую по TCP, для
// протокола это одно и то же.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var buildMeshdOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "meshd-build")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "meshd")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/meshd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("сборка meshd: %v: %s", err, out)
	}
	return bin, nil
})

func startMeshd(t *testing.T) string {
	t.Helper()
	bin, err := buildMeshdOnce()
	if err != nil {
		t.Skipf("не удалось собрать meshd: %v", err)
	}
	return runMeshd(t, bin, filepath.Join(t.TempDir(), "state.json"))
}

// runMeshd запускает meshd на свободном порту, который тот выбирает сам (:0),
// и узнаёт адрес из его журнала. «Взять свободный порт и закрыть» здесь не
// годится: пока meshd стартует, порт успевает занять тест другого пакета.
func runMeshd(t *testing.T, bin, state string) string {
	t.Helper()
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-state", state)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if i := strings.Index(sc.Text(), "слушает "); i >= 0 {
				found <- strings.TrimSpace(sc.Text()[i+len("слушает "):])
			}
		}
	}()
	select {
	case addr := <-found:
		return addr
	case <-time.After(10 * time.Second):
		t.Fatal("meshd не поднялся")
		return ""
	}
}

func directDial(network, addr string) (net.Conn, error) { return net.Dial(network, addr) }

// echoService — «служба на устройстве»: отвечает на строку «имя: строка».
func echoService(t *testing.T, name string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, name+": "+line)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func startClient(t *testing.T, addr string, cfg Config) *Client {
	t.Helper()
	cfg.Addr = addr
	if cfg.DeviceID == "" {
		cfg.DeviceID = NewDeviceID()
	}
	c := New(cfg, directDial, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	waitFor(t, func() bool { return c.Status().State == "online" }, "клиент "+cfg.Name+" не подключился")
	return c
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(what)
}

func ask(t *testing.T, conn net.Conn, text string) string {
	t.Helper()
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, text+"\n")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("ответ не пришёл: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestДваУстройстваВидятИСоединяютсяПоИмени(t *testing.T) {
	addr := startMeshd(t)
	key := NewKey()
	laptop := startClient(t, addr, Config{Key: key, Name: "Мой ноутбук", AllowIncoming: true})
	server := startClient(t, addr, Config{Key: key, Name: "Домашний сервер", AllowIncoming: true})

	waitFor(t, func() bool {
		_, ok := laptop.Resolve("domashniy-server.mesh")
		return ok
	}, "ноутбук не увидел сервер в списке")

	st := server.Status()
	if st.Self.Host != "domashniy-server" || !Range.Contains(st.Self.IP) {
		t.Fatalf("сервер получил %+v", st.Self)
	}
	if laptop.Status().Self.IP == st.Self.IP {
		t.Fatal("два устройства получили один адрес")
	}

	port := echoService(t, "server")
	for _, target := range []string{
		"domashniy-server.mesh:" + strconv.Itoa(port),
		net.JoinHostPort(st.Self.IP.String(), strconv.Itoa(port)),
	} {
		if !laptop.Match(target) {
			t.Fatalf("Match(%q) = false", target)
		}
		conn, err := laptop.DialPeer(target)
		if err != nil {
			t.Fatalf("DialPeer(%q): %v", target, err)
		}
		if got := ask(t, conn, "привет"); got != "server: привет" {
			t.Fatalf("через %q ответили %q", target, got)
		}
	}
}

func TestРазныеКлючиНеВидятДругДруга(t *testing.T) {
	addr := startMeshd(t)
	a := startClient(t, addr, Config{Key: NewKey(), Name: "a", AllowIncoming: true})
	b := startClient(t, addr, Config{Key: NewKey(), Name: "b", AllowIncoming: true})

	time.Sleep(200 * time.Millisecond)
	if _, ok := a.Resolve("b.mesh"); ok {
		t.Fatal("устройство чужой сети попало в список")
	}
	bIP := b.Status().Self.IP
	if _, err := a.DialPeer(net.JoinHostPort(bIP.String(), "22")); err == nil {
		// Адреса в разных сетях могут совпасть — тогда это сам a, и ему
		// честно ответит его собственный порт. Проверяем, что не b.
		if bIP != a.Status().Self.IP {
			t.Fatal("удалось дозвониться до устройства чужой сети")
		}
	}
}

func TestВходящиеВыключены(t *testing.T) {
	addr := startMeshd(t)
	key := NewKey()
	a := startClient(t, addr, Config{Key: key, Name: "a", AllowIncoming: true})
	startClient(t, addr, Config{Key: key, Name: "b", AllowIncoming: false})
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "b не появился в списке")

	_, err := a.DialPeer("b.mesh:22")
	if err == nil || !strings.Contains(err.Error(), "не принимает") {
		t.Fatalf("ожидался отказ, получено %v", err)
	}
}

func TestПортЗакрыт(t *testing.T) {
	addr := startMeshd(t)
	key := NewKey()
	a := startClient(t, addr, Config{Key: key, Name: "a", AllowIncoming: true})
	startClient(t, addr, Config{Key: key, Name: "b", AllowIncoming: true})
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "b не появился в списке")

	// Свободный порт, на котором ничего не слушает.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	_, err := a.DialPeer("b.mesh:" + strconv.Itoa(port))
	if err == nil || !strings.Contains(err.Error(), "ничего не слушает") {
		t.Fatalf("ожидалась понятная ошибка, получено %v", err)
	}
}

func TestУстройствоНеВСети(t *testing.T) {
	addr := startMeshd(t)
	key := NewKey()
	a := startClient(t, addr, Config{Key: key, Name: "a", AllowIncoming: true})

	bCfg := Config{Key: key, Name: "b", AllowIncoming: true, DeviceID: NewDeviceID(), Addr: addr}
	b := New(bCfg, directDial, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)
	waitFor(t, func() bool { return b.Status().State == "online" }, "b не подключился")
	cancel()
	waitFor(t, func() bool {
		for _, p := range a.Status().Peers {
			if p.Host == "b" && !p.Online {
				return true
			}
		}
		return false
	}, "a не узнал, что b ушёл")

	_, err := a.DialPeer("b.mesh:22")
	if err == nil || !strings.Contains(err.Error(), "не в сети") {
		t.Fatalf("ожидалось «не в сети», получено %v", err)
	}
}

// Адрес устройства постоянный: переподключилось — тот же адрес.
func TestАдресПостоянный(t *testing.T) {
	addr := startMeshd(t)
	key, id := NewKey(), NewDeviceID()
	cfg := Config{Key: key, Name: "a", DeviceID: id, Addr: addr}

	first := New(cfg, directDial, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go first.Run(ctx)
	waitFor(t, func() bool { return first.Status().State == "online" }, "не подключился")
	ip := first.Status().Self.IP
	cancel()

	second := startClient(t, addr, cfg)
	if got := second.Status().Self.IP; got != ip {
		t.Fatalf("после переподключения адрес %s, был %s", got, ip)
	}
}

func TestСамКСебеБезСервера(t *testing.T) {
	addr := startMeshd(t)
	a := startClient(t, addr, Config{Key: NewKey(), Name: "a", AllowIncoming: true})
	port := echoService(t, "a")
	conn, err := a.DialPeer(net.JoinHostPort(a.Status().Self.IP.String(), strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, conn, "эхо"); got != "a: эхо" {
		t.Fatalf("ответ %q", got)
	}
}

func TestMatch(t *testing.T) {
	c := New(Config{}, directDial, nil)
	cases := map[string]bool{
		"laptop.mesh:22":   true,
		"LAPTOP.MESH:22":   true,
		"198.19.0.5:80":    true,
		"198.18.0.5:80":    false,
		"example.com:443":  false,
		"10.0.0.1:22":      false,
		"mesh.example:443": false,
	}
	for target, want := range cases {
		if got := c.Match(target); got != want {
			t.Errorf("Match(%q) = %v, ожидалось %v", target, got, want)
		}
	}
}
