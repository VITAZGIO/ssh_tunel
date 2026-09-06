package tunnel

// Сквозная проверка UDPRelay(): настоящий (тестовый) SSH-сервер плюс
// настоящий бинарник ретранслятора (sshtunnel/cmd/udprelay), собранный из
// тех же исходников, что уйдут на сервер. Дальше — обычный клиент этого
// пакета, tun.Dial через пул и сам UDPRelay().

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

var buildUDPRelayOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "udprelay-build")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "udprelay")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "sshtunnel/cmd/udprelay")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("сборка ретранслятора: %v: %s", err, out)
	}
	return bin, nil
})

func startUDPRelayProcess(t *testing.T) string {
	t.Helper()
	bin, err := buildUDPRelayOnce()
	if err != nil {
		t.Skipf("не удалось собрать ретранслятор: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cmd := exec.Command(bin, "-listen", addr)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

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

func echoUDP(t *testing.T) *net.UDPAddr {
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

func TestUDPRelayСквозьПулИДоРетранслятора(t *testing.T) {
	relayAddr := startUDPRelayProcess(t)
	target := echoUDP(t)

	tun, _, _, _ := startTunnel(t, 2)
	tun.cfg.UDPRelayEnabled = true
	tun.cfg.UDPRelayAddr = relayAddr
	if !tun.WaitReady(2, 5*time.Second) {
		t.Fatal("пул не поднялся")
	}

	client := tun.UDPRelay()
	if client == nil {
		t.Fatal("UDPRelay() вернул nil, хотя включён и ретранслятор поднят")
	}

	got := make(chan string, 1)
	id := client.Open(func(data []byte, fromHost string, fromPort uint16) {
		got <- string(data)
	})
	if err := client.Send(id, target.IP.String(), uint16(target.Port), []byte("привет через пул")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "привет через пул" {
			t.Fatalf("получено %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ответ не пришёл")
	}

	// Повторный вызов отдаёт тот же клиент — соединение до ретранслятора не
	// поднимается заново на каждую UDP-сессию.
	if again := tun.UDPRelay(); again != client {
		t.Error("UDPRelay() поднял новое соединение вместо переиспользования старого")
	}
}

// Выключенная настройка — UDPRelay всегда nil, независимо от того, поднят ли
// вообще какой-то ретранслятор.
func TestUDPRelayВыключенВозвращаетNil(t *testing.T) {
	tun, _, _, _ := startTunnel(t, 1)
	if !tun.WaitReady(1, 5*time.Second) {
		t.Fatal("пул не поднялся")
	}
	if client := tun.UDPRelay(); client != nil {
		t.Fatal("UDPRelay() должен быть nil, когда UDPRelayEnabled=false")
	}
}
