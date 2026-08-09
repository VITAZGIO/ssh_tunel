// Package tunnel — ядро: пул SSH-соединений до сервера и локальные прокси-серверы
// (SOCKS4/4a, SOCKS5, HTTP CONNECT), которые пробрасывают через него трафик.
//
// Почему пул, а не одно соединение, как было раньше:
//   - одно TCP-соединение — это одно окно перегрузки; на дальнем канале
//     с задержкой в десятки миллисекунд оно и есть главный потолок скорости;
//   - библиотека x/crypto/ssh сериализует запись пакетов одним мьютексом на
//     соединение, то есть все каналы делят один поток шифрования;
//   - если единственное соединение умирает, умирает вообще всё.
//
// Пул решает все три пункта сразу: соединения независимы, живучесть каждого
// отслеживается keepalive-ами, мёртвые переподключаются в фоне с backoff.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunel/internal/events"
	"sshtunel/internal/hostkey"
	"sshtunel/internal/routing"
)

type Config struct {
	Host    string
	SSHPort int
	User    string
	KeyPath string

	SocksAddr string // 127.0.0.1:1080
	HTTPAddr  string // 127.0.0.1:1081

	PoolSize       int
	KnownHostsPath string
	Verbose        bool

	// Policy решает по имени программы, вести соединение через сервер или
	// выпустить напрямую. nil означает «всё через туннель».
	Policy *routing.Policy

	// LocalViaTunnel — вести ли в туннель и адреса локальной сети.
	//
	// По умолчанию (false) 192.168.x.x, домашние имена и прочая локальная
	// сеть идут напрямую: сервер всё равно искал бы такой адрес у себя.
	// Включать это стоит ровно в одном случае — когда нужна внутренняя сеть
	// САМОГО сервера, а не своя.
	LocalViaTunnel bool
}

var ErrNotConnected = errors.New("нет живого SSH-соединения с сервером")

// link — один слот пула: SSH-соединение плюс горутина, которая его чинит.
type link struct {
	mu     sync.RWMutex
	client *ssh.Client
}

func (l *link) get() *ssh.Client {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.client
}

func (l *link) set(c *ssh.Client) {
	l.mu.Lock()
	old := l.client
	l.client = c
	l.mu.Unlock()
	if old != nil && old != c {
		old.Close()
	}
}

type Tunnel struct {
	cfg Config
	bus *events.Bus

	signer ssh.Signer
	links  []*link
	rr     atomic.Uint64 // счётчик для раздачи соединений по кругу

	listeners []net.Listener
	stats     stats
	mu        sync.RWMutex // защищает изменяемую часть настроек (правила)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	state atomic.Value // string

	// localViaTunnel меняется на ходу из настроек, поэтому atomic, а не поле
	// в cfg: перезапускать туннель ради галочки не надо.
	localViaTunnel atomic.Bool
}

type stats struct {
	up     atomic.Int64
	down   atomic.Int64
	active atomic.Int64
	total  atomic.Int64
}

func New(cfg Config, bus *events.Bus) *Tunnel {
	t := &Tunnel{cfg: cfg, bus: bus}
	t.localViaTunnel.Store(cfg.LocalViaTunnel)
	t.state.Store(events.StateStopped)
	return t
}

// SetLocalViaTunnel переключает обработку локальной сети без перезапуска.
func (t *Tunnel) SetLocalViaTunnel(v bool) { t.localViaTunnel.Store(v) }

func (t *Tunnel) State() string {
	s, _ := t.state.Load().(string)
	return s
}

func (t *Tunnel) setState(s, detail string) {
	t.state.Store(s)
	t.bus.State(s, detail)
}

// Start поднимает пул и локальные слушатели. Возвращает ошибку только если
// стартовать вообще не получилось (нет ключа, занят порт, сервер не отвечает);
// после успешного старта обрывы связи лечатся сами, без ошибки наружу.
func (t *Tunnel) Start() error {
	if t.State() != events.StateStopped {
		return errors.New("туннель уже запущен")
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.setState(events.StateConnecting, "")

	key, err := os.ReadFile(t.cfg.KeyPath)
	if err != nil {
		t.setState(events.StateError, "нет доступа к ключу")
		return fmt.Errorf("не могу прочитать приватный ключ %s: %w", t.cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.setState(events.StateError, "плохой ключ")
		return fmt.Errorf("ключ %s повреждён, зашифрован паролем или не в том формате: %w", t.cfg.KeyPath, err)
	}
	t.signer = signer

	// Первое соединение поднимаем синхронно: если сервер недоступен или ключ
	// не подходит, пользователь должен узнать об этом сразу, а не из лога.
	first, err := t.dial()
	if err != nil {
		t.setState(events.StateError, "не удалось подключиться")
		t.cancel()
		return err
	}

	n := t.cfg.PoolSize
	if n < 1 {
		n = 1
	}
	t.links = make([]*link, n)
	for i := range t.links {
		t.links[i] = &link{}
	}
	t.links[0].set(first)

	for i := range t.links {
		t.wg.Add(1)
		go t.keepLinkAlive(t.links[i], i)
	}

	if err := t.startListeners(); err != nil {
		t.Stop()
		return err
	}

	t.setState(events.StateConnected, fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.SSHPort))
	t.wg.Add(1)
	go t.publishStats()
	return nil
}

func (t *Tunnel) startListeners() error {
	type srv struct {
		addr    string
		name    string
		handler func(net.Conn)
	}
	want := []srv{
		{t.cfg.SocksAddr, "SOCKS4/5", t.handleSOCKS},
		{t.cfg.HTTPAddr, "HTTP", t.handleHTTP},
	}
	for _, s := range want {
		if s.addr == "" {
			continue
		}
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			return fmt.Errorf("не могу занять локальный адрес %s (%s): %w — возможно, программа уже запущена", s.addr, s.name, err)
		}
		t.listeners = append(t.listeners, ln)
		t.wg.Add(1)
		go t.acceptLoop(ln, s.handler)
	}
	return nil
}

func (t *Tunnel) acceptLoop(ln net.Listener, handle func(net.Conn)) {
	defer t.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Закрытый слушатель — это штатная остановка, а не сбой.
			if t.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go handle(conn)
	}
}

// Stop гасит слушатели и пул. Безопасно вызывать повторно.
func (t *Tunnel) Stop() {
	if t.cancel == nil {
		return
	}
	t.cancel()
	for _, ln := range t.listeners {
		ln.Close()
	}
	t.listeners = nil
	for _, l := range t.links {
		l.set(nil)
	}
	t.wg.Wait()
	t.links = nil
	t.setState(events.StateStopped, "")
}

func (t *Tunnel) clientConfig() *ssh.ClientConfig {
	cb := hostkey.Callback(t.cfg.KnownHostsPath, func(host, fp string) {
		t.bus.Infof("Ключ сервера %s запомнен: %s", host, fp)
	})
	return &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(t.signer)},
		HostKeyCallback: cb,
		Timeout:         15 * time.Second,
		Config: ssh.Config{
			// Порядок задаёт предпочтения клиента. AES-GCM первым, потому что
			// на любом современном процессоре есть AES-NI и он заметно быстрее
			// программного chacha20; chacha оставлена запасной для серверов
			// без поддержки GCM.
			Ciphers: []string{
				"aes128-gcm@openssh.com",
				"aes256-gcm@openssh.com",
				"chacha20-poly1305@openssh.com",
				"aes128-ctr",
				"aes256-ctr",
			},
		},
	}
}

func (t *Tunnel) dial() (*ssh.Client, error) {
	addr := net.JoinHostPort(t.cfg.Host, fmt.Sprint(t.cfg.SSHPort))
	client, err := ssh.Dial("tcp", addr, t.clientConfig())
	if err != nil {
		var changed *hostkey.ErrChanged
		if errors.As(err, &changed) {
			return nil, changed // отдельный понятный текст, см. hostkey
		}
		return nil, fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	return client, nil
}

// keepLinkAlive держит один слот пула живым: шлёт keepalive, замечает смерть
// соединения и переподключается с нарастающей паузой.
//
// Без keepalive обрыв связи (заснул ноутбук, моргнул Wi-Fi, NAT выкинул сессию)
// раньше не замечался вообще: программа продолжала принимать соединения и
// молча их ломать, а выглядело это как "интернет пропал".
func (t *Tunnel) keepLinkAlive(l *link, idx int) {
	defer t.wg.Done()

	backoff := time.Second
	for {
		if t.ctx.Err() != nil {
			return
		}

		client := l.get()
		if client == nil {
			c, err := t.dial()
			if err != nil {
				if t.ctx.Err() != nil {
					return
				}
				if idx == 0 {
					t.setState(events.StateReconnecting, err.Error())
				}
				select {
				case <-t.ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			l.set(c)
			backoff = time.Second
			if t.State() == events.StateReconnecting {
				t.setState(events.StateConnected, fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.SSHPort))
				t.bus.Infof("Связь с сервером восстановлена")
			}
			continue
		}

		// Ждём либо остановки, либо момента следующей проверки.
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}

		if t.ctx.Err() != nil {
			return
		}
		// Пустой глобальный запрос — стандартный способ проверить, жив ли
		// канал. Сервер обязан ответить (пусть и отказом), а вот если
		// соединение мертво, здесь будет ошибка.
		if client != l.get() {
			continue // соединение уже заменили, проверять нечего
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.bus.Warnf("SSH-соединение #%d оборвалось (%v) — переподключаюсь", idx+1, err)
				l.set(nil)
			}
		case <-time.After(15 * time.Second):
			t.bus.Warnf("SSH-соединение #%d не отвечает — переподключаюсь", idx+1)
			l.set(nil)
		case <-t.ctx.Done():
			return
		}
	}
}

// dialWait — сколько ждать живого соединения, если в этот момент весь пул
// переподключается. Без ожидания короткий разрыв связи превращался бы в пачку
// мгновенных ошибок в браузере, хотя через секунду всё уже работает.
const dialWait = 8 * time.Second

// Dial открывает соединение до target через SSH. Ходит по пулу: если слот
// мёртв, пробует следующий, поэтому обрыв одного соединения не виден снаружи.
func (t *Tunnel) Dial(network, target string) (net.Conn, error) {
	deadline := time.Now().Add(dialWait)
	for {
		conn, err, hadLink := t.tryDial(network, target)
		if hadLink {
			return conn, err
		}
		// Живых соединений нет вообще — ждём, пока фоновые горутины поднимут
		// хотя бы одно.
		if time.Now().After(deadline) {
			return nil, ErrNotConnected
		}
		select {
		case <-t.ctx.Done():
			return nil, ErrNotConnected
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tryDial делает один проход по пулу. hadLink=false означает, что ни одного
// живого соединения не нашлось и есть смысл подождать.
func (t *Tunnel) tryDial(network, target string) (conn net.Conn, err error, hadLink bool) {
	links := t.links
	if len(links) == 0 {
		return nil, ErrNotConnected, false
	}
	var lastErr error
	for i := 0; i < len(links); i++ {
		idx := int(t.rr.Add(1)-1) % len(links)
		client := links[idx].get()
		if client == nil {
			continue
		}
		hadLink = true
		c, err := client.Dial(network, target)
		if err == nil {
			return c, nil, true
		}
		lastErr = err
		// Ошибка уровня соединения (а не "хост недоступен") означает, что
		// слот пора чинить: помечаем мёртвым, keepalive-горутина переподключит.
		if isLinkDead(err) {
			links[idx].set(nil)
			continue
		}
		return nil, err, true
	}
	if !hadLink {
		return nil, ErrNotConnected, false
	}
	if lastErr == nil {
		lastErr = ErrNotConnected
	}
	return nil, lastErr, true
}

// SetPolicy меняет правила на ходу — останавливать туннель ради этого не надо.
func (t *Tunnel) SetPolicy(p *routing.Policy) {
	t.mu.Lock()
	t.cfg.Policy = p
	t.mu.Unlock()
}

// useTunnel — вести ли соединение этой программы через сервер.
func (t *Tunnel) useTunnel(process string) bool {
	t.mu.RLock()
	p := t.cfg.Policy
	t.mu.RUnlock()
	if p == nil {
		return true
	}
	return p.UseTunnel(process)
}

// WaitReady ждёт, пока в пуле поднимется не меньше n соединений. Нужно тестам
// и автозапуску, чтобы не дёргать туннель раньше времени.
func (t *Tunnel) WaitReady(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if t.Stats().Healthy >= n {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return t.Stats().Healthy >= n
}

func isLinkDead(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscallECONNRESET) ||
		containsAny(err.Error(),
			"use of closed network connection",
			"connection lost",
			"broken pipe",
			"EOF",
		)
}

func (t *Tunnel) Stats() events.Stats {
	healthy := 0
	for _, l := range t.links {
		if l.get() != nil {
			healthy++
		}
	}
	return events.Stats{
		BytesUp:   t.stats.up.Load(),
		BytesDown: t.stats.down.Load(),
		Active:    t.stats.active.Load(),
		Total:     t.stats.total.Load(),
		Links:     len(t.links),
		Healthy:   healthy,
	}
}

func (t *Tunnel) publishStats() {
	defer t.wg.Done()
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-tk.C:
			s := t.Stats()
			t.bus.Publish(events.Event{Kind: events.KindStats, Stats: &s})
		}
	}
}
