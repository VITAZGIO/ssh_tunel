package mesh

// Прямые соединения на настоящем meshd: оба устройства на этой машине, STUN
// на случайных портах. Для meshd они выглядят как два устройства за одним
// «NAT» 127.0.0.1 — пробивание идёт по-настоящему, по UDP.

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) add(_, text string) {
	l.mu.Lock()
	l.lines = append(l.lines, text)
	l.mu.Unlock()
}

func (l *logBuf) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func startClientLog(t *testing.T, addr string, cfg Config) (*Client, *logBuf) {
	t.Helper()
	cfg.Addr = addr
	cfg.DeviceID = NewDeviceID()
	cfg.NoNATProbe = true
	lb := &logBuf{}
	c := New(cfg, directDial, lb.add)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	waitFor(t, func() bool { return c.Status().State == "online" }, "клиент "+cfg.Name+" не подключился")
	return c, lb
}

func peerStatus(c *Client, ip string) Peer {
	for _, p := range c.Status().Peers {
		if p.IP.String() == ip {
			return p
		}
	}
	return Peer{}
}

func waitLong(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(what)
}

func TestПрямоеСоединение(t *testing.T) {
	addr, sock, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "ноутбук", Via: "127.0.0.1", AllowIncoming: true})
	b, blog := startClientLog(t, addr, Config{Key: key, Name: "пк", Via: "127.0.0.1", AllowIncoming: true})
	bIP := b.Status().Self.IP.String()
	aIP := a.Status().Self.IP.String()
	waitFor(t, func() bool { _, ok := a.Resolve("pk"); return ok }, "ноутбук не увидел пк")
	port := echoService(t, "пк")
	target := net.JoinHostPort(bIP, strconv.Itoa(port))

	// Первое соединение — через сервер, прямой путь ищется в фоне.
	conn, err := a.DialPeer(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, conn, "раз"); got != "пк: раз" {
		t.Fatalf("через сервер: %q", got)
	}
	waitLong(t, 10*time.Second, func() bool { return peerStatus(a, bIP).Direct && peerStatus(b, aIP).Direct },
		"прямое соединение не установилось")

	// Следующее — напрямую.
	conn, err = a.DialPeer(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, conn, "два"); got != "пк: два" {
		t.Fatalf("напрямую: %q", got)
	}
	if !blog.has("напрямую → порт " + strconv.Itoa(port)) {
		t.Fatal("соединение пошло не напрямую")
	}
	waitFor(t, func() bool { return peerStatus(a, bIP).DirectRTTMs > 0 }, "нет задержки прямого пути")

	// Панель видит прямое соединение.
	admin := adminClient(sock)
	waitLong(t, 20*time.Second, func() bool {
		_, d, ok := findDev(getState(t, admin), "ноутбук")
		return ok && len(d.Direct) == 1 && d.Direct[0].IP == bIP
	}, "панель не увидела прямое соединение")
}

// Много данных в обе стороны по прямому пути и обратно — без потерь.
func TestПрямоеСоединениеБольшиеДанные(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1", AllowIncoming: true})
	b, _ := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", AllowIncoming: true})
	bIP := b.Status().Self.IP.String()
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "a не увидел b")

	// Эхо-служба: возвращает всё, что пришло.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.(*net.TCPConn).CloseWrite() }()
		}
	}()
	target := net.JoinHostPort(bIP, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	a.DialPeer(target) // запускает поиск прямого пути
	waitLong(t, 10*time.Second, func() bool { return peerStatus(a, bIP).Direct }, "прямое соединение не установилось")

	conn, err := a.DialPeer(target)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4<<20)
	rand.Read(data)
	go func() {
		conn.Write(data)
		conn.(interface{ CloseWrite() error }).CloseWrite()
	}()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	got, err := io.ReadAll(conn)
	conn.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("вернулось %d из %d байт: %v", len(got), len(data), err)
	}
}

// NAT, которые не пробить (жёсткие с фильтрацией у обоих): соединения
// продолжают работать через сервер.
func TestПрямойПутьНеПробитьИдёмЧерезСервер(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	hard := func(string) (net.PacketConn, error) { return newFakeNAT(true, true), nil }
	a, alog := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1", ProbeListen: hard})
	b, _ := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", AllowIncoming: true, ProbeListen: hard})
	bIP := b.Status().Self.IP.String()
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "a не увидел b")
	port := echoService(t, "b")
	target := net.JoinHostPort(bIP, strconv.Itoa(port))
	for i := 0; i < 2; i++ {
		conn, err := a.DialPeer(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := ask(t, conn, "эй"); got != "b: эй" {
			t.Fatalf("ответ %q", got)
		}
	}
	waitLong(t, 15*time.Second, func() bool { return alog.has("не нашёлся") }, "попытка не закончилась")
	if peerStatus(a, bIP).Direct {
		t.Fatal("прямое соединение сквозь жёсткий NAT?")
	}
	conn, err := a.DialPeer(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, conn, "ещё"); got != "b: ещё" {
		t.Fatalf("после неудачи: %q", got)
	}
}

// Выключатель: с NoDirect всё идёт через сервер, и ответная сторона не
// откликается на предложения.
func TestПрямыеВыключены(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1"})
	b, blog := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", AllowIncoming: true, NoDirect: true})
	bIP := b.Status().Self.IP.String()
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "a не увидел b")
	port := echoService(t, "b")
	conn, err := a.DialPeer(net.JoinHostPort(bIP, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	ask(t, conn, "x")
	time.Sleep(2 * time.Second)
	if peerStatus(a, bIP).Direct || blog.has("прямое соединение") {
		t.Fatal("прямое соединение при выключенных прямых")
	}
}

// Чужой ключ не пройдёт: отпечаток сверяется с присланным через сервер.
func TestПрямоеЧужойКлючОтвергнут(t *testing.T) {
	_, fp1, _ := p2pCert()
	cert2, _, _ := p2pCert()
	p := &p2p{}
	cfg := p.clientTLS(fp1)
	if err := cfg.VerifyPeerCertificate(cert2.Certificate, nil); err == nil {
		t.Fatal("чужой ключ принят")
	}
	cert1raw, fp, _ := p2pCert()
	if err := p.clientTLS(fp).VerifyPeerCertificate(cert1raw.Certificate, nil); err != nil {
		t.Fatalf("свой ключ отвергнут: %v", err)
	}
}

// Прямой путь ищется сам, как только устройства в сети, — без единого
// соединения между ними. Оба начинают одновременно («ничья»): остаётся одно
// соединение, и видят его оба.
func TestПрямоеСоединениеСамо(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1", AllowIncoming: true})
	b, _ := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", AllowIncoming: true})
	aIP, bIP := a.Status().Self.IP.String(), b.Status().Self.IP.String()
	waitLong(t, 15*time.Second, func() bool { return peerStatus(a, bIP).Direct && peerStatus(b, aIP).Direct },
		"прямое соединение само не установилось")
	// И держится: ни одна сторона не закрыла его в пользу своего.
	time.Sleep(2 * time.Second)
	if !peerStatus(a, bIP).Direct || !peerStatus(b, aIP).Direct {
		t.Fatal("прямое соединение пропало после «ничьей»")
	}
}

// Свой локальный адрес находится и без списка интерфейсов (как на Android 11+).
func TestЛокальныйАдресЧерезМаршрут(t *testing.T) {
	c := New(Config{}, nil, nil)
	p := &p2p{c: c, stun: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}}
	ip, ok := p.routeIP()
	if !ok || !ip.IsLoopback() {
		t.Fatalf("адрес в сторону 127.0.0.1: %v %v", ip, ok)
	}
}

// Замеры задержки не копят незакрытые потоки: у QUIC лимит одновременных
// потоков (100), и раньше примерно через 100 замеров (≈25 минут) прямое
// соединение «пропадало».
func TestПрямоеСоединениеДолгаяЖизнь(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1", AllowIncoming: true})
	b, _ := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", AllowIncoming: true})
	bIP := b.Status().Self.IP.String()
	waitLong(t, 15*time.Second, func() bool { return peerStatus(a, bIP).Direct }, "прямое соединение не установилось")
	p := a.direct()
	for i := 0; i < 250; i++ {
		if !p.measure(bIP) {
			t.Fatalf("замер %d не прошёл — соединение сброшено", i+1)
		}
	}
	// И обычные соединения, в том числе отказы, тоже не копятся.
	for i := 0; i < 150; i++ {
		c, err := p.dialDirect(bIP, 1) // порт 1 никто не слушает
		if c != nil || err == nil {
			t.Fatalf("соединение %d: ожидался отказ, получено %v %v", i+1, c, err)
		}
	}
	if !peerStatus(a, bIP).Direct {
		t.Fatal("прямое соединение пропало")
	}
}

// ping до устройства: через сервер (прямые выключены), по прямому пути, до
// себя и до адреса, которого нет в сети.
func TestPingУстройства(t *testing.T) {
	addr, _, _ := startMeshdSTUN(t)
	key := NewKey()
	a, _ := startClientLog(t, addr, Config{Key: key, Name: "a", Via: "127.0.0.1", NoDirect: true})
	b, _ := startClientLog(t, addr, Config{Key: key, Name: "b", Via: "127.0.0.1", NoDirect: true})
	bIP := b.Status().Self.IP
	if rtt, err := a.Ping(bIP); err != nil || rtt <= 0 {
		t.Fatalf("через сервер: %v %v", rtt, err)
	}
	if _, err := a.Ping(a.Status().Self.IP); err != nil {
		t.Fatalf("до себя: %v", err)
	}
	start := time.Now()
	if _, err := a.Ping(netip.MustParseAddr("198.19.0.200")); err == nil {
		t.Fatal("ответил несуществующий адрес")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("ожидание ответа не ограничено")
	}

	c, _ := startClientLog(t, addr, Config{Key: key, Name: "c", Via: "127.0.0.1"})
	d, _ := startClientLog(t, addr, Config{Key: key, Name: "d", Via: "127.0.0.1"})
	dIP := d.Status().Self.IP
	waitLong(t, 15*time.Second, func() bool { return peerStatus(c, dIP.String()).Direct }, "прямое соединение не установилось")
	if rtt, err := c.Ping(dIP); err != nil || rtt <= 0 {
		t.Fatalf("напрямую: %v %v", rtt, err)
	}
}
