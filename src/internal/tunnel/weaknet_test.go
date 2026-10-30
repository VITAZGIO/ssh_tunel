package tunnel

// Плохая сеть в миниатюре: между клиентом и тестовым SSH-сервером стоит
// посредник, который задерживает каждый кусок данных, рвёт первые соединения
// посреди рукопожатия или замораживает ответы сервера. Ровно то, что на
// мобильном интернете в одно деление ломало подключение: «сервер не ответил
// вовремя» в проверке и «handshake failed: EOF» после подключения.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/events"
)

// badLink — TCP-посредник с испорченной связью.
type badLink struct {
	addr   string
	target string

	delay     time.Duration // задержка каждого куска в обе стороны
	dropFirst atomic.Int64  // сколько первых соединений оборвать посреди рукопожатия
	frozen    atomic.Bool   // ответы сервера не доходят, пока true
	conns     atomic.Int64
}

func newBadLink(t *testing.T, target string, delay time.Duration) *badLink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &badLink{addr: ln.Addr().String(), target: target, delay: delay}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return b
}

func (b *badLink) serve(client net.Conn) {
	n := b.conns.Add(1)
	server, err := net.Dial("tcp", b.target)
	if err != nil {
		client.Close()
		return
	}
	drop := n <= b.dropFirst.Load()
	// Клиент → сервер.
	go b.pipe(server, client, false, false)
	// Сервер → клиент: тут и обрыв, и заморозка.
	b.pipe(client, server, drop, true)
}

func (b *badLink) pipe(dst, src net.Conn, drop, fromServer bool) {
	defer dst.Close()
	defer src.Close()
	// Задержка — как у настоящего канала: каждый кусок приходит через delay
	// после отправки, а не после предыдущего куска (иначе куски копились бы
	// в очереди и «пинг» рос бы с каждым).
	type chunk struct {
		data []byte
		at   time.Time
	}
	q := make(chan chunk, 1024)
	go func() {
		defer close(q)
		for {
			buf := make([]byte, 32*1024)
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			q <- chunk{buf[:n], time.Now().Add(b.delay)}
		}
	}()
	for c := range q {
		time.Sleep(time.Until(c.at))
		for fromServer && b.frozen.Load() {
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := dst.Write(c.data); err != nil {
			return
		}
		if drop {
			// Версию сервера клиент получил — рвём посреди обмена ключами,
			// со сбросом, как это делает фильтр у провайдера.
			if tc, ok := dst.(*net.TCPConn); ok {
				tc.SetLinger(0)
			}
			return
		}
	}
}

// tunnelVia — туннель, который ходит к серверу через посредника.
func tunnelVia(t *testing.T, poolSize int, dialTimeout time.Duration, delay time.Duration) (*Tunnel, *badLink) {
	t.Helper()
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)
	bl := newBadLink(t, srv.addr, delay)

	host, portStr, _ := net.SplitHostPort(bl.addr)
	sshPort, _ := strconv.Atoi(portStr)
	tun := New(Config{
		Host:           host,
		SSHPort:        sshPort,
		User:           "test",
		KeyPath:        keyPath,
		PoolSize:       poolSize,
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
		DialTimeout:    dialTimeout,
		KeepAlive:      200 * time.Millisecond,
	}, events.NewBus())
	return tun, bl
}

func loadTestSigner(t *testing.T, tun *Tunnel) {
	t.Helper()
	key, err := os.ReadFile(tun.cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	tun.signer = signer
}

// Медленный, но отвечающий сервер дожидаемся. Раньше срок шёл на всё
// рукопожатие целиком, и при шести обменах по 0,6 с вход не укладывался в
// срок, хотя сервер ни разу не замолчал дольше секунды.
func TestSlowServerIsWaitedFor(t *testing.T) {
	tun, _ := tunnelVia(t, 1, time.Second, 300*time.Millisecond)
	loadTestSigner(t, tun)

	start := time.Now()
	c, err := tun.dialSSH(net.JoinHostPort(tun.cfg.Host, strconv.Itoa(tun.cfg.SSHPort)))
	if err != nil {
		t.Fatalf("медленный сервер не дождались: %v", err)
	}
	c.Close()
	if d := time.Since(start); d < time.Second {
		t.Fatalf("рукопожатие прошло за %v — задержка не сработала, тест ничего не проверяет", d)
	}
}

// Обрыв посреди первого рукопожатия не должен превращаться в ошибку для
// человека: туннель сам пробует ещё раз.
func TestStartRetriesDroppedHandshake(t *testing.T) {
	tun, bl := tunnelVia(t, 1, 2*time.Second, 0)
	bl.dropFirst.Store(1)

	if err := tun.Start(); err != nil {
		t.Fatalf("туннель не пережил один обрыв рукопожатия: %v", err)
	}
	defer tun.Stop()
	if n := bl.conns.Load(); n < 2 {
		t.Fatalf("попыток подключения %d, ожидалось не меньше 2", n)
	}
}

// Если рвётся каждая попытка, ошибка понятная («связь оборвалась»), а не
// «handshake failed: EOF», и попыток ограниченное число.
func TestDroppedHandshakeIsExplained(t *testing.T) {
	tun, bl := tunnelVia(t, 1, 2*time.Second, 0)
	bl.dropFirst.Store(1000)

	err := tun.Start()
	if err == nil {
		tun.Stop()
		t.Fatal("туннель «подключился» к серверу, который рвёт каждое соединение")
	}
	var ce *ConnError
	if !errors.As(err, &ce) || ce.Kind != ConnErrorDropped {
		t.Fatalf("ошибка %v (%T), ожидался ConnError вида dropped", err, err)
	}
	if n := bl.conns.Load(); n != firstDialAttempts {
		t.Fatalf("попыток %d, ожидалось %d", n, firstDialAttempts)
	}
}

func shrinkKeepalive(t *testing.T) {
	r, m, rt := keepaliveReply, keepaliveMisses, keepaliveRetry
	keepaliveReply, keepaliveMisses, keepaliveRetry = 300*time.Millisecond, 3, 100*time.Millisecond
	t.Cleanup(func() { keepaliveReply, keepaliveMisses, keepaliveRetry = r, m, rt })
}

// Короткое замирание связи (ответ на keepalive опоздал) не рвёт соединение:
// раньше хватало одного опоздания, и закачки обрывались вместе с ним.
func TestKeepaliveSurvivesShortStall(t *testing.T) {
	shrinkKeepalive(t)
	tun, bl := tunnelVia(t, 1, 2*time.Second, 0)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	defer tun.Stop()
	before := tun.snapLinks()[0].get()

	time.Sleep(300 * time.Millisecond)
	bl.frozen.Store(true)
	time.Sleep(500 * time.Millisecond) // дольше одного keepaliveReply, меньше трёх
	bl.frozen.Store(false)
	time.Sleep(time.Second)

	if after := tun.snapLinks()[0].get(); after != before {
		t.Fatal("живое соединение закрыто из-за одного опоздавшего ответа")
	}
	if n := bl.conns.Load(); n != 1 {
		t.Fatalf("переподключений не должно быть, соединений %d", n)
	}
}

// А совсем замолчавшее соединение всё-таки признаётся мёртвым.
func TestKeepaliveDropsDeadLink(t *testing.T) {
	shrinkKeepalive(t)
	tun, bl := tunnelVia(t, 1, 2*time.Second, 0)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	defer tun.Stop()
	before := tun.snapLinks()[0].get()

	bl.frozen.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tun.snapLinks()[0].get() != before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("мёртвое соединение так и не признано мёртвым")
}

// Дополнительные слоты не стучатся в сервер, пока основной не поднят.
func TestSecondarySlotsWaitForPrimary(t *testing.T) {
	tun, bl := tunnelVia(t, 4, 2*time.Second, 0)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	defer tun.Stop()
	if !tun.WaitReady(4, 10*time.Second) {
		t.Fatalf("пул не поднялся: %+v", tun.Stats())
	}

	// Сервер «пропал»: все соединения рвутся при подключении.
	bl.dropFirst.Store(1 << 30)
	for _, l := range tun.snapLinks() {
		l.set(nil)
	}
	start := bl.conns.Load()
	time.Sleep(1500 * time.Millisecond)
	// Основной слот за это время делает 1–2 попытки (пауза 1–2 с), а не
	// четыре слота по столько же.
	if n := bl.conns.Load() - start; n > 2 {
		t.Fatalf("за 1,5 с без сервера %d попыток подключения — слоты стучатся все разом", n)
	}
}

