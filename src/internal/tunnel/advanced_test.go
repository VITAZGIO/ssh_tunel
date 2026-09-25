package tunnel

// Тонкая настройка соединения: шифр, протокол до сервера, частота проверки
// связи и прокси для локальной сети.

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/events"
)

func TestSSHCiphers(t *testing.T) {
	if got := sshCiphers("")[0]; got != "aes128-gcm@openssh.com" {
		t.Errorf("по умолчанию первым %q", got)
	}
	if got := sshCiphers("aes")[0]; got != "aes128-gcm@openssh.com" {
		t.Errorf("aes: первым %q", got)
	}
	chacha := sshCiphers("chacha")
	if chacha[0] != "chacha20-poly1305@openssh.com" {
		t.Errorf("chacha: первым %q", chacha[0])
	}
	// Выбор только меняет порядок: запасные шифры остаются.
	for _, pref := range []string{"", "aes", "chacha"} {
		if n := len(sshCiphers(pref)); n != 5 {
			t.Errorf("%q: шифров %d, ожидалось 5", pref, n)
		}
	}
}

func TestSSHNetwork(t *testing.T) {
	for in, want := range map[string]string{"": "tcp", "4": "tcp4", "6": "tcp6", "x": "tcp"} {
		if got := sshNetwork(in); got != want {
			t.Errorf("sshNetwork(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestLANSource(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":      true,
		"192.168.1.20":   true,
		"10.0.0.5":       true,
		"172.20.3.4":     true,
		"169.254.10.1":   true,
		"::1":            true,
		"fd12:3456::1":   true,
		"fe80::1":        true,
		"8.8.8.8":        false,
		"100.64.1.2":     false, // общий адрес оператора — чужие абоненты
		"172.32.0.1":     false,
		"2001:db8::1":    false,
		"::ffff:1.2.3.4": false,
	}
	for ip, want := range cases {
		a := &net.TCPAddr{IP: net.ParseIP(ip), Port: 5555}
		if got := lanSource(a); got != want {
			t.Errorf("lanSource(%s) = %v, ожидалось %v", ip, got, want)
		}
	}
}

func TestKeepAliveПоУмолчанию(t *testing.T) {
	if got := New(Config{}, events.NewBus()).keepAliveEvery(); got != 20*time.Second {
		t.Errorf("по умолчанию %v", got)
	}
	if got := New(Config{KeepAlive: 7 * time.Second}, events.NewBus()).keepAliveEvery(); got != 7*time.Second {
		t.Errorf("заданное %v", got)
	}
}

// Настоящее подключение с выбранным шифром и по IPv4, прокси открыт для
// локальной сети: соединение с 127.0.0.1 проходит.
func TestТуннельСТонкойНастройкой(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)
	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)
	port := freePort(t)

	tun := New(Config{
		Host: host, SSHPort: sshPort, User: "test", KeyPath: keyPath,
		SocksAddr:      fmt.Sprintf("0.0.0.0:%d", port),
		PoolSize:       1,
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
		LocalViaTunnel: true,
		Cipher:         "chacha",
		IPVersion:      "4",
		KeepAlive:      5 * time.Second,
		DialTimeout:    5 * time.Second,
		LANAccess:      true,
	}, events.NewBus())
	if err := tun.Start(); err != nil {
		t.Fatalf("туннель не запустился: %v", err)
	}
	t.Cleanup(tun.Stop)

	target := echoServer(t)
	c := socks5Connect(t, fmt.Sprintf("127.0.0.1:%d", port), target.IP.String(), target.Port, false)
	defer c.Close()
	assertHTTPBody(t, c, target.String(), "/hello", "привет от ")
}
