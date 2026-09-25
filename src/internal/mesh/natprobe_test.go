package mesh

// Проверка NAT на настоящем meshd (STUN на случайных портах) и имитации NAT:
// простого, жёсткого и с фильтрацией.

import (
	"bufio"
	"context"
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

// startMeshdSTUN — meshd с проверкой NAT на случайных портах и входом для
// панели; возвращает адрес, сокет панели и порты проверки.
func startMeshdSTUN(t *testing.T) (string, string, []int) {
	t.Helper()
	bin, err := buildMeshdOnce()
	if err != nil {
		t.Skipf("не удалось собрать meshd: %v", err)
	}
	dir, err := os.MkdirTemp("", "mn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-state", filepath.Join(dir, "state.json"),
		"-admin", sock, "-stun", "0,0")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	type result struct {
		addr  string
		ports []int
	}
	found := make(chan result, 1)
	go func() {
		var r result
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if i := strings.Index(line, "проверка NAT: UDP "); i >= 0 {
				for _, p := range strings.Split(line[i+len("проверка NAT: UDP "):], ",") {
					n, _ := strconv.Atoi(strings.TrimSpace(p))
					r.ports = append(r.ports, n)
				}
			}
			if i := strings.Index(line, "слушает "); i >= 0 {
				r.addr = strings.TrimSpace(line[i+len("слушает "):])
				found <- r
			}
		}
	}()
	select {
	case r := <-found:
		if len(r.ports) != 2 {
			t.Fatalf("meshd не открыл порты проверки NAT: %v", r.ports)
		}
		return r.addr, sock, r.ports
	case <-time.After(10 * time.Second):
		t.Fatal("meshd не поднялся")
		return "", "", nil
	}
}

// fakeNAT — имитация NAT поверх настоящих UDP-сокетов.
//
//	hard       — «жёсткий»: под каждого собеседника свой внешний сокет (порт);
//	restricted — пускает ответы только с тех адрес:порт, куда уже писали.
type fakeNAT struct {
	hard, restricted bool

	mu       sync.Mutex
	socks    map[string]net.PacketConn
	sent     map[string]bool
	in       chan natPacket
	deadline time.Time
	closed   chan struct{}
}

type natPacket struct {
	b    []byte
	from net.Addr
}

func newFakeNAT(hard, restricted bool) *fakeNAT {
	return &fakeNAT{hard: hard, restricted: restricted, socks: map[string]net.PacketConn{},
		sent: map[string]bool{}, in: make(chan natPacket, 64), closed: make(chan struct{})}
}

func (f *fakeNAT) sock(dst string) (net.PacketConn, error) {
	key := ""
	if f.hard {
		key = dst
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.socks[key]; ok {
		return s, nil
	}
	s, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f.socks[key] = s
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := s.ReadFrom(buf)
			if err != nil {
				return
			}
			select {
			case f.in <- natPacket{append([]byte(nil), buf[:n]...), from}:
			case <-f.closed:
				return
			}
		}
	}()
	return s, nil
}

func (f *fakeNAT) WriteTo(b []byte, dst net.Addr) (int, error) {
	s, err := f.sock(dst.String())
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	f.sent[dst.String()] = true
	f.mu.Unlock()
	return s.WriteTo(b, dst)
}

func (f *fakeNAT) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		f.mu.Lock()
		d := f.deadline
		f.mu.Unlock()
		var timeout <-chan time.Time
		if !d.IsZero() {
			timeout = time.After(time.Until(d))
		}
		select {
		case p := <-f.in:
			f.mu.Lock()
			allowed := !f.restricted || f.sent[p.from.String()]
			f.mu.Unlock()
			if !allowed {
				continue // NAT выкинул: сюда не писали
			}
			return copy(b, p.b), p.from, nil
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-f.closed:
			return 0, nil, net.ErrClosed
		}
	}
}

func (f *fakeNAT) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	for _, s := range f.socks {
		s.Close()
	}
	return nil
}

func (f *fakeNAT) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1} }
func (f *fakeNAT) SetDeadline(t time.Time) error {
	return f.SetReadDeadline(t)
}
func (f *fakeNAT) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	f.deadline = t
	f.mu.Unlock()
	return nil
}
func (f *fakeNAT) SetWriteDeadline(time.Time) error { return nil }

func probeWith(t *testing.T, ports []int, listen func(string) (net.PacketConn, error)) NATInfo {
	t.Helper()
	return ProbeNAT(context.Background(), ProbeEnv{Server: "127.0.0.1", Ports: ports, Listen: listen,
		Timeout: 300 * time.Millisecond})
}

func TestNATБезNAT(t *testing.T) {
	_, _, ports := startMeshdSTUN(t)
	n := probeWith(t, ports, nil)
	if !n.UDP || n.Mapping != "independent" || n.Filtering != "open" || !n.PortPreserved || n.PublicIP != "127.0.0.1" {
		t.Fatalf("без NAT: %+v", n)
	}
}

func TestNATЖёсткий(t *testing.T) {
	_, _, ports := startMeshdSTUN(t)
	n := probeWith(t, ports, func(string) (net.PacketConn, error) { return newFakeNAT(true, true), nil })
	if !n.UDP || n.Mapping != "dependent" || n.Filtering != "restricted" || n.PortPreserved {
		t.Fatalf("жёсткий NAT: %+v", n)
	}
}

func TestNATПростойСФильтрацией(t *testing.T) {
	_, _, ports := startMeshdSTUN(t)
	n := probeWith(t, ports, func(string) (net.PacketConn, error) { return newFakeNAT(false, true), nil })
	if !n.UDP || n.Mapping != "independent" || n.Filtering != "restricted" {
		t.Fatalf("простой NAT с фильтрацией: %+v", n)
	}
}

func TestNATUDPНеПроходит(t *testing.T) {
	// Свободный порт, где никто не отвечает.
	pc, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	n := probeWith(t, []int{port, port}, nil)
	if n.UDP || n.Mapping != "" {
		t.Fatalf("UDP не проходит, а получено %+v", n)
	}
}

// Устройство само проверяет NAT после подключения, и результат видит панель.
func TestNATОтчётДоходитДоПанели(t *testing.T) {
	addr, sock, _ := startMeshdSTUN(t)
	c := startClient(t, addr, Config{Key: NewKey(), Name: "ноутбук", Via: "127.0.0.1"})
	waitFor(t, func() bool { return c.Status().NAT != nil }, "проверка NAT не прошла")
	if n := c.Status().NAT; !n.UDP || n.Mapping != "independent" {
		t.Fatalf("у устройства: %+v", n)
	}
	admin := adminClient(sock)
	waitFor(t, func() bool {
		st := getState(t, admin)
		return len(st.Networks) == 1 && len(st.Networks[0].Devices) == 1 && st.Networks[0].Devices[0].NAT != nil
	}, "панель не увидела результат проверки NAT")
	d := getState(t, admin).Networks[0].Devices[0]
	if !d.NAT.UDP || d.NAT.Mapping != "independent" || d.NAT.PublicIP != "127.0.0.1" {
		t.Fatalf("в панели: %+v", d.NAT)
	}
}

// Мусор на порт проверки не валит meshd.
func TestNATМусорНеРоняетСервер(t *testing.T) {
	_, _, ports := startMeshdSTUN(t)
	pc, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer pc.Close()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ports[0]}
	junk := [][]byte{{}, {1, 2, 3}, make([]byte, 28), append(stunRequest([12]byte{}, false)[:24], 0xff, 0xff, 0xff, 0xff)}
	bad := stunRequest([12]byte{}, false)
	bad[23] = 200 // длина атрибута больше пакета
	junk = append(junk, bad)
	for _, j := range junk {
		pc.WriteTo(j, dst)
	}
	if n := probeWith(t, ports, nil); !n.UDP {
		t.Fatal("после мусора сервер перестал отвечать")
	}
}
