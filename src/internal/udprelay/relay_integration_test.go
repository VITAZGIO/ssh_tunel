package udprelay

// Сквозная проверка: настоящий бинарник ретранслятора (src/cmd/udprelay),
// собранный из тех же исходников, что уйдут на сервер, и клиентский Client
// из этого пакета — если они разъедутся в понимании протокола, именно этот
// тест это заметит, а не отдельные юнит-тесты кодека по обе стороны.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// buildRelay компилирует src/cmd/udprelay один раз на весь пакет тестов —
// иначе каждый тест собирал бы бинарник заново.
var buildRelayOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "udprelay-build")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "udprelay")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/udprelay")
	cmd.Dir = mustWD()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("сборка ретранслятора: %v: %s", err, out)
	}
	return bin, nil
})

func mustWD() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}

func startRelay(t *testing.T) string {
	t.Helper()
	bin, err := buildRelayOnce()
	if err != nil {
		t.Skipf("не удалось собрать ретранслятор: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // отдаём адрес процессу — окно гонки короткое и для теста не важно

	cmd := exec.Command(bin, "-listen", addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// Ждём, пока порт реально откроется — процессу нужно время подняться.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ретранслятор не поднялся")
	return ""
}

// echoUDPServer отвечает тем же, что получил, — простая цель для проверки
// сквозного пути туда и обратно.
func echoUDPServer(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func TestClientИРетрансляторРеальныйКруг(t *testing.T) {
	relayAddr := startRelay(t)
	target := echoUDPServer(t)

	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := NewClient(conn)

	replies := make(chan string, 4)
	id := client.Open(func(data []byte, fromHost string, fromPort uint16) {
		replies <- string(data)
	})

	if err := client.Send(id, target.IP.String(), uint16(target.Port), []byte("привет")); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-replies:
		if got != "привет" {
			t.Fatalf("получено %q, ожидалось %q", got, "привет")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ответ не пришёл")
	}
}

// Несколько сессий одновременно через одно TCP-соединение до ретранслятора
// не должны путать датаграммы между собой.
func TestClientМножествоСессийНеПутаютсяМеждуСобой(t *testing.T) {
	relayAddr := startRelay(t)
	target := echoUDPServer(t)

	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := NewClient(conn)

	const n = 20
	results := make([]chan string, n)
	ids := make([]uint32, n)
	for i := 0; i < n; i++ {
		i := i
		results[i] = make(chan string, 1)
		ids[i] = client.Open(func(data []byte, fromHost string, fromPort uint16) {
			results[i] <- string(data)
		})
	}
	for i := 0; i < n; i++ {
		msg := fmt.Sprintf("сообщение-%d", i)
		if err := client.Send(ids[i], target.IP.String(), uint16(target.Port), []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("сообщение-%d", i)
		select {
		case got := <-results[i]:
			if got != want {
				t.Errorf("сессия %d: получено %q, ожидалось %q", i, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("сессия %d: ответ не пришёл", i)
		}
	}
}

// FIN должен закрыть сессию на стороне ретранслятора — после него UDP-сокет
// освобождается и повторный кадр по тому же (уже забытому клиентом)
// идентификатору ведёт себя как новая сессия, а не мешает старым данным.
func TestClientCloseОсвобождаетСессиюНемедленно(t *testing.T) {
	relayAddr := startRelay(t)
	target := echoUDPServer(t)

	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := NewClient(conn)

	got := make(chan string, 2)
	id := client.Open(func(data []byte, fromHost string, fromPort uint16) { got <- string(data) })
	if err := client.Send(id, target.IP.String(), uint16(target.Port), []byte("раз")); err != nil {
		t.Fatal(err)
	}
	<-got
	client.Close(id)

	// Сессии с этим id для клиента больше не существует — Send обязан отказать.
	if err := client.Send(id, target.IP.String(), uint16(target.Port), []byte("два")); err == nil {
		t.Fatal("ожидалась ошибка отправки в закрытую сессию")
	}
}
