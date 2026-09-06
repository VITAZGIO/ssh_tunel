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
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/events"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/procinfo"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/udprelay"
)

// defaultUDPRelayAddr — куда стучаться, если Config.UDPRelayAddr не задан:
// тот же адрес, на котором cmd/udprelay слушает без флагов.
const defaultUDPRelayAddr = "127.0.0.1:47830"

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

	// DialTimeout — сколько ждать соединения с сервером, считая рукопожатие.
	// Ноль означает пятнадцать секунд.
	DialTimeout time.Duration

	// Policy решает по имени программы, вести соединение через сервер или
	// выпустить напрямую. nil означает «всё через туннель».
	Policy *routing.Policy

	// Direct — собственный список адресов и сетей, которые всегда идут
	// напрямую. nil означает «список пуст».
	Direct *routing.DirectList

	// ProtectSocket вызывается для каждого сокета, который открывает сама
	// программа: соединения с сервером и прямых соединений мимо туннеля.
	//
	// Нужно это только на Android и там обязательно. Система заворачивает в
	// туннель весь трафик, включая наш собственный, — соединение с сервером
	// пошло бы внутрь самого себя и зациклилось. VpnService.protect() помечает
	// сокет как «этот мимо», и обращение к нему разрывает петлю.
	//
	// nil означает «ничего помечать не надо» — так на Windows и Linux.
	ProtectSocket func(network, address string, c syscall.RawConn) error

	// LocalViaTunnel — вести ли в туннель и адреса локальной сети.
	//
	// По умолчанию (false) 192.168.x.x, домашние имена и прочая локальная
	// сеть идут напрямую: сервер всё равно искал бы такой адрес у себя.
	// Включать это стоит ровно в одном случае — когда нужна внутренняя сеть
	// САМОГО сервера, а не своя.
	LocalViaTunnel bool

	// UDPRelayEnabled — пробрасывать ли UDP (звонки, игры, QUIC) через
	// ретранслятор на сервере (см. sshtunnel/internal/udprelay,
	// sshtunnel/cmd/udprelay) вместо молчаливого отказа. По умолчанию
	// выключено: пока ретранслятор на сервере не установлен, включать нечего
	// — соединение до него просто не поднимется, и UDP продолжит
	// отбрасываться, как и раньше.
	UDPRelayEnabled bool
	// UDPRelayAddr — где на сервере слушает ретранслятор (его собственный
	// localhost). Пусто — значение по умолчанию, то же, что и у
	// cmd/udprelay без флагов.
	UDPRelayAddr string
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

	// kickMu и kick — сигнал «пересобрать пул немедленно», см. Kick.
	kickMu sync.Mutex
	kick   chan struct{}

	// udpRelayMu и udpRelayClient — соединение до ретранслятора UDP на
	// сервере, поднимается по требованию и переживает до тех пор, пока не
	// оборвётся само (см. UDPRelay).
	udpRelayMu     sync.Mutex
	udpRelayClient *udprelay.Client
}

type stats struct {
	// pingMs — задержка до сервера, миллисекунды.
	pingMs atomic.Int64

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

// SetDirect меняет список «всегда напрямую» на ходу, как и правила по
// программам: переподключаться ради него не нужно.
func (t *Tunnel) SetDirect(d *routing.DirectList) {
	t.mu.Lock()
	t.cfg.Direct = d
	t.mu.Unlock()
}

// currentKick отдаёт текущий канал сигнала «пересобрать пул» — создаёт его
// при первом обращении.
func (t *Tunnel) currentKick() <-chan struct{} {
	t.kickMu.Lock()
	defer t.kickMu.Unlock()
	if t.kick == nil {
		t.kick = make(chan struct{})
	}
	return t.kick
}

// Kick форсирует немедленное переподключение всего пула — например, когда
// телефон сменил сеть (Wi-Fi ↔ мобильная) и ждать таймера проверки живости
// незачем: старые сокеты после смены сети почти наверняка уже мертвы.
//
// Все слоты помечаются оборванными тем же способом, что использует
// keepLinkAlive при настоящем обрыве связи, — отдельного пути переподключения
// заводить не пришлось. Безопасно вызывать в любой момент, включая случай,
// когда туннель ещё не запущен.
func (t *Tunnel) Kick() {
	t.kickMu.Lock()
	old := t.kick
	t.kick = make(chan struct{})
	t.kickMu.Unlock()
	if old != nil {
		close(old)
	}
	for _, l := range t.snapLinks() {
		l.set(nil)
	}
}

func (t *Tunnel) State() string {
	s, _ := t.state.Load().(string)
	return s
}

func (t *Tunnel) setState(s, detail string) {
	t.state.Store(s)
	t.bus.State(s, detail)
}

// reportDialErr переводит ошибку dial() в состояние туннеля. Для
// *ConnError — по разобранному коду причины (ErrorKind), который экран с
// I18N переводит сам; для смены ключа сервера (hostkey.ErrChanged) и любого
// другого случая — как раньше, текстом ошибки без изменений.
func (t *Tunnel) reportDialErr(s string, err error) {
	var ce *ConnError
	if errors.As(err, &ce) {
		t.state.Store(s)
		t.bus.StateErr(s, ce.Message, string(ce.Kind))
		return
	}
	t.setState(s, err.Error())
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
		t.reportDialErr(events.StateError, err)
		t.cancel()
		return err
	}

	n := t.cfg.PoolSize
	if n < 1 {
		n = 1
	}
	links := make([]*link, n)
	for i := range links {
		links[i] = &link{}
	}
	links[0].set(first)
	t.mu.Lock()
	t.links = links
	t.mu.Unlock()

	for i := range links {
		t.wg.Add(1)
		go t.keepLinkAlive(links[i], i)
	}

	if err := t.startListeners(); err != nil {
		t.Stop()
		return err
	}

	t.setState(events.StateConnected, fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.SSHPort))
	t.wg.Add(1)
	go t.publishStats()
	t.wg.Add(1)
	go t.pingLoop()
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
			return fmt.Errorf("не могу занять локальный адрес %s (%s): %w%s",
				s.addr, s.name, err, whoHolds(s.addr))
		}
		t.listeners = append(t.listeners, ln)
		t.wg.Add(1)
		go t.acceptLoop(ln, s.handler)
	}
	return nil
}

// pingLoop меряет задержку до сервера.
//
// Keepalive для этого не годится: он ходит раз в двадцать секунд, и число на
// экране было бы почти всегда несвежим. Свой замер стоит копейки — служебный
// запрос без нагрузки по уже открытому соединению, — зато показывает связь
// такой, какая она сейчас.
func (t *Tunnel) pingLoop() {
	defer t.wg.Done()

	for {
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(4 * time.Second):
		}

		links := t.snapLinks()
		if len(links) == 0 {
			t.stats.pingMs.Store(0)
			continue
		}
		client := links[0].get()
		if client == nil {
			t.stats.pingMs.Store(0)
			continue
		}

		start := time.Now()
		done := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()

		select {
		case err := <-done:
			if err != nil {
				t.stats.pingMs.Store(0)
				continue
			}
			ms := time.Since(start).Milliseconds()
			if ms < 1 {
				ms = 1 // ноль означает «нет замера», а не «мгновенно»
			}
			t.stats.pingMs.Store(ms)
		case <-time.After(5 * time.Second):
			t.stats.pingMs.Store(0)
		case <-t.ctx.Done():
			return
		}
	}
}

// whoHolds пытается назвать программу, которая уже держит этот порт.
//
// Без этого сообщение об ошибке заставляет гадать: своя же копия висит в трее
// или посторонняя программа заняла порт. Ответ у системы есть, и спросить его
// стоит ровно один раз — в момент, когда занять порт не вышло.
func whoHolds(addr string) string {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ""
	}
	name, pid := procinfo.Lookup(uint16(port))
	if name == "" {
		return " — возможно, программа уже запущена"
	}
	return fmt.Sprintf(" — порт занят программой %s (%d)", name, pid)
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

// stopGrace — сколько ждать фоновые горутины при остановке.
//
// Ждать их бесконечно нельзя: горутина может стоять в попытке достучаться до
// сервера, которого нет. Слушатели к этому моменту уже закрыты, а соединения
// разорваны, поэтому опоздавшая горутина ничего не сломает — она увидит
// отменённый контекст и выйдет сама, просто чуть позже.
const stopGrace = 3 * time.Second

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
	for _, l := range t.snapLinks() {
		l.set(nil)
	}

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopGrace):
	}

	t.mu.Lock()
	t.links = nil
	t.mu.Unlock()

	t.udpRelayMu.Lock()
	relay := t.udpRelayClient
	t.udpRelayClient = nil
	t.udpRelayMu.Unlock()
	if relay != nil {
		relay.Shutdown()
	}

	t.setState(events.StateStopped, "")
}

// UDPRelay отдаёт клиента ретранслятора UDP, поднимая соединение до него по
// требованию — и заново, если прежнее оборвалось. nil, если функция
// выключена в настройках или соединение сейчас поднять не удалось (сеть,
// ретранслятор не установлен на сервере) — вызывающий код в этом случае
// просто продолжает вести себя так, будто UDP не поддерживается вовсе.
func (t *Tunnel) UDPRelay() *udprelay.Client {
	if !t.cfg.UDPRelayEnabled {
		return nil
	}

	t.udpRelayMu.Lock()
	defer t.udpRelayMu.Unlock()

	if t.udpRelayClient != nil {
		select {
		case <-t.udpRelayClient.Done():
			t.udpRelayClient = nil // прежнее соединение оборвалось — поднимем новое ниже
		default:
			return t.udpRelayClient
		}
	}

	addr := t.cfg.UDPRelayAddr
	if addr == "" {
		addr = defaultUDPRelayAddr
	}
	conn, err := t.Dial("tcp", addr)
	if err != nil {
		t.bus.Warnf("UDP через туннель недоступен: %v", err)
		return nil
	}
	t.udpRelayClient = udprelay.NewClient(conn)
	return t.udpRelayClient
}

// snapLinks отдаёт срез пула под замком.
//
// Пул создаётся при старте и обнуляется при остановке, а читают его счётчики и
// замер задержки — то есть из других горутин и в любой момент. Без этого
// остановка и обращение к пулу пересекались бы на одном поле.
func (t *Tunnel) snapLinks() []*link {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.links
}

func (t *Tunnel) dialTimeout() time.Duration {
	if t.cfg.DialTimeout > 0 {
		return t.cfg.DialTimeout
	}
	return 15 * time.Second
}

func (t *Tunnel) clientConfig() *ssh.ClientConfig {
	cb := hostkey.Callback(t.cfg.KnownHostsPath, func(host, fp string) {
		t.bus.Infof("Ключ сервера %s запомнен: %s", host, fp)
	})
	return &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(t.signer)},
		HostKeyCallback: cb,
		Timeout:         t.dialTimeout(),
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
	client, err := t.dialSSH(addr)
	if err != nil {
		var changed *hostkey.ErrChanged
		if errors.As(err, &changed) {
			return nil, changed // отдельный понятный текст, см. hostkey — не трогаем
		}
		wrapped := fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
		return nil, classifyConnError(wrapped)
	}
	return client, nil
}

// dialSSH — то же, что ssh.Dial, но соединение открывается своим диалером.
// Иначе некуда вставить пометку сокета, без которой на Android соединение с
// сервером ушло бы в собственный туннель.
func (t *Tunnel) dialSSH(addr string) (*ssh.Client, error) {
	cfg := t.clientConfig()
	conn, err := t.directDialer(cfg.Timeout).Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	// У рукопожатия своего срока нет: Timeout в настройках ограничивает только
	// установку TCP-соединения. Сервер, который соединение принял и замолчал —
	// а именно так выглядит фильтрация у провайдера и мобильный интернет на
	// одном делении, — подвесил бы этот вызов навсегда. Вместе с ним повисает и
	// остановка туннеля, которая ждёт эту горутину: на телефоне это выглядит
	// как намертво зависшая кнопка.
	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Срок снимаем: дальше по этому соединению идут переносы данных без всякого
	// ограничения по времени, и общий дедлайн оборвал бы их через пятнадцать
	// секунд после подключения.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		c.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// directDialer — диалер для соединений, которые программа открывает сама.
// На Android каждое такое соединение должно быть помечено как идущее мимо
// туннеля, иначе оно вернётся в него же.
func (t *Tunnel) directDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: t.cfg.ProtectSocket}
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
		kickCh := t.currentKick()

		client := l.get()
		if client == nil {
			c, err := t.dial()
			if err != nil {
				if t.ctx.Err() != nil {
					return
				}
				if idx == 0 {
					t.reportDialErr(events.StateReconnecting, err)
				}
				select {
				case <-t.ctx.Done():
					return
				case <-time.After(backoff):
				case <-kickCh:
					// Сеть точно сменилась — прежняя пауза уже не про
					// текущие условия, начинаем заново с минимальной.
					backoff = time.Second
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

		// Ждём либо остановки, либо момента следующей проверки, либо сигнала
		// пересобрать пул немедленно (Kick уже пометил этот слот оборванным —
		// следующий круг цикла сразу уйдёт на переподключение).
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(20 * time.Second):
		case <-kickCh:
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
	links := t.snapLinks()
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
	links := t.snapLinks()
	healthy := 0
	for _, l := range links {
		if l.get() != nil {
			healthy++
		}
	}
	return events.Stats{
		BytesUp:   t.stats.up.Load(),
		BytesDown: t.stats.down.Load(),
		Active:    t.stats.active.Load(),
		Total:     t.stats.total.Load(),
		Links:     len(links),
		Healthy:   healthy,
		PingMs:    t.stats.pingMs.Load(),
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
