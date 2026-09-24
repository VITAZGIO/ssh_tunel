package vpnlayer

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"sshtunnel/android/core"
	"sshtunnel/internal/events"
)

// Настоящая проверка маршрутов и защиты от петли. Сеть самой машины трогать
// нельзя, поэтому тест перезапускает себя в отдельном сетевом пространстве
// имён (CLONE_NEWNET): там свой lo, свой «провайдер» (dummy-интерфейс с
// маршрутом по умолчанию) и наш TUN. Нужны root и /dev/net/tun — без них
// тест пропускается.
const netnsChildEnv = "VPNLAYER_NETNS_CHILD"

func TestLinuxRoutingInNetns(t *testing.T) {
	if os.Getenv(netnsChildEnv) == "1" {
		runNetnsChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("нужен root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("нет /dev/net/tun")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxRoutingInNetns$", "-test.v")
	cmd.Env = append(os.Environ(), netnsChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("отдельное сетевое пространство недоступно: %v", err)
		}
		t.Fatalf("проверка в отдельном пространстве не прошла: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

type recordingCore struct {
	mu      sync.Mutex
	targets []string
	got     chan string
}

func (c *recordingCore) ServeConn(conn net.Conn, target string, byIP bool) {
	c.mu.Lock()
	c.targets = append(c.targets, target)
	c.mu.Unlock()
	conn.Close()
	select {
	case c.got <- target:
	default:
	}
}

func runNetnsChild(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}

	// «Провайдер»: второй TUN с адресом и маршрутом по умолчанию. Пакеты в
	// него никто не читает — нам важно лишь, в какое устройство они уходят.
	uplinkTun, err := core.OpenTun("uplink0", net.IPv4(10, 99, 0, 2), net.CIDRMask(24, 32), 1500)
	if err != nil {
		t.Fatalf("uplink: %v", err)
	}
	defer syscall.Close(uplinkTun.FD)
	up, err := netlink.LinkByName("uplink0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: up.Attrs().Index, Gw: net.ParseIP("10.99.0.1")}); err != nil {
		t.Fatalf("маршрут по умолчанию: %v", err)
	}

	s := newSys(events.NewBus())
	s.refresh()
	if !s.hasUplink() || s.up4 != "uplink0" {
		t.Fatalf("настоящая сеть не найдена: %q", s.up4)
	}

	dev, err := s.open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := &recordingCore{got: make(chan string, 8)}
	eng, err := core.StartDevice(dev, &core.Handler{Core: rec})
	if err != nil {
		t.Fatalf("стек: %v", err)
	}
	defer eng.Close()
	if err := s.configureRoutes(); err != nil {
		t.Fatalf("маршруты: %v", err)
	}

	// После наших маршрутов настоящая сеть должна остаться прежней: наши
	// /1 в выбор маршрута по умолчанию не попадают.
	if s.refresh() {
		t.Fatalf("после маршрутов в TUN «настоящая сеть» сменилась на %q", s.up4)
	}

	// Обычная программа: соединение уходит в TUN и доходит до ядра.
	c, err := net.DialTimeout("tcp", "203.0.113.10:80", 3*time.Second)
	if err == nil {
		c.Close()
	}
	select {
	case got := <-rec.got:
		if got != "203.0.113.10:80" {
			t.Fatalf("в ядро пришло %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("соединение обычной программы не попало в TUN (ошибка dial: %v)", err)
	}

	// Свой сокет, привязанный к настоящей сети, в TUN попадать не должен:
	// иначе соединение с сервером ушло бы само в себя.
	d := net.Dialer{Timeout: 700 * time.Millisecond, Control: s.protect}
	if c, err := d.Dial("tcp", "203.0.113.11:80"); err == nil {
		c.Close()
	}
	select {
	case got := <-rec.got:
		t.Fatalf("защищённый сокет попал в TUN: %q", got)
	case <-time.After(700 * time.Millisecond):
	}

	s.close()
}

func TestParseAddrsОтбрасываетЗаглушкиИСебя(t *testing.T) {
	got := parseAddrs([]string{"127.0.0.53", "198.18.0.53", "192.168.1.1", "fe80::1%eth0", "мусор", "2001:db8::1"})
	want := []netip.Addr{netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("2001:db8::1")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseAddrs = %v, ожидалось %v", got, want)
	}
}

func TestNameservers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(path, []byte("# comment\nnameserver 10.0.0.1\nsearch lan\nnameserver 127.0.0.53\n"), 0o644)
	got := nameservers(path)
	if len(got) != 1 || got[0] != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("nameservers = %v", got)
	}
}
