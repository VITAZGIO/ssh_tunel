// Пакет mobile — единственное, что видит приложение на Kotlin.
//
// Связка Go и Java умеет передавать только простые вещи: числа, строки, булевы
// значения, массивы байт и типы, объявленные здесь же. Поэтому вся настройка
// принимается плоскими параметрами, а обратная связь идёт через интерфейс
// Callbacks, который реализуется на стороне Kotlin.
package mobile

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"

	"sshtunnel/android/core"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/tunnel"
)

// Callbacks — то, что Go просит сделать у приложения.
type Callbacks interface {
	// Protect помечает сокет как идущий мимо туннеля.
	//
	// Без этого соединение с сервером ушло бы в собственный туннель и
	// зациклилось: система заворачивает внутрь весь трафик, включая наш.
	// Реализация на Kotlin — VpnService.protect(fd).
	Protect(fd int) bool

	// OnState сообщает о смене состояния, чтобы экран показал её человеку.
	OnState(state string, detail string)

	// OnLog отдаёт строку для журнала соединений.
	OnLog(line string)

	// ResolveLocal разрешает имя средствами системы, минуя туннель.
	// Возвращает адреса через запятую или пустую строку, если не вышло.
	//
	// Своими силами Go этого на Android не может: файла /etc/resolv.conf там
	// нет, а системный резолвер доступен только через Java.
	ResolveLocal(name string) string
}

// Tunnel — то, что приложение включает и выключает.
type Tunnel struct {
	mu sync.Mutex

	cb     Callbacks
	cfg    tunnel.Config
	direct *routing.DirectList

	core   *tunnel.Tunnel
	engine *core.Engine
	bus    *events.Bus
	stop   chan struct{}
}

// NewTunnel создаёт выключенный туннель.
//
// Имя не New намеренно: в Java «new» — служебное слово, и связка Go с Java
// переименовала бы функцию во что-то нечитаемое.
func NewTunnel() *Tunnel { return &Tunnel{} }

// SetCallbacks задаёт обратную связь. Вызывается до Configure.
func (t *Tunnel) SetCallbacks(cb Callbacks) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cb = cb
}

// Configure принимает настройки.
//
// keyPath — путь к файлу закрытого ключа: приложение сохраняет его в свою
// папку само, потому что права на файл ключа задаёт тоже оно.
// directHosts — список «всегда напрямую», через запятую или с новой строки.
func (t *Tunnel) Configure(
	host string, sshPort int, user string, keyPath string,
	knownHostsPath string, poolSize int,
	directHosts string, localViaTunnel bool,
) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("не задан адрес сервера")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("не задано имя пользователя")
	}
	if sshPort <= 0 {
		sshPort = 22
	}
	if poolSize <= 0 {
		poolSize = 4
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.direct = routing.NewDirectList(routing.SplitEntries(directHosts))
	t.cfg = tunnel.Config{
		Host:           host,
		SSHPort:        sshPort,
		User:           user,
		KeyPath:        keyPath,
		KnownHostsPath: knownHostsPath,
		PoolSize:       poolSize,
		Direct:         t.direct,
		LocalViaTunnel: localViaTunnel,
		ProtectSocket:  t.protect,

		// Локальных прокси на телефоне нет: соединения приходят из пакетов,
		// а не с портов 1080 и 1081.
		SocksAddr: "",
		HTTPAddr:  "",
	}
	return nil
}

// protect помечает сокет через приложение. Ошибка пометки — не повод рвать
// соединение: на некоторых устройствах вызов возвращает false и при этом всё
// работает. Зато молча зациклиться было бы гораздо хуже, поэтому пишем в лог.
func (t *Tunnel) protect(network, address string, c syscall.RawConn) error {
	cb := t.callbacks()
	if cb == nil {
		return nil
	}
	var ok bool
	err := c.Control(func(fd uintptr) { ok = cb.Protect(int(fd)) })
	if err != nil {
		return err
	}
	if !ok {
		cb.OnLog(fmt.Sprintf("не удалось пометить сокет до %s", address))
	}
	return nil
}

func (t *Tunnel) callbacks() Callbacks {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cb
}

// Start поднимает туннель и сетевой стек поверх дескриптора от VpnService.
//
// tunFD должен быть получен через detachFd(): дальше им распоряжается Go и
// закрывает его сам. Если отдать getFd(), дескриптор закроют дважды.
func (t *Tunnel) Start(tunFD int, mtu int) error {
	t.mu.Lock()
	if t.core != nil {
		t.mu.Unlock()
		return fmt.Errorf("туннель уже запущен")
	}
	cfg, direct, cb := t.cfg, t.direct, t.cb
	t.mu.Unlock()

	if cfg.Host == "" {
		return fmt.Errorf("сначала нужно задать настройки")
	}
	if mtu <= 0 {
		mtu = 1500
	}

	bus := events.NewBus()
	tun := tunnel.New(cfg, bus)
	if err := tun.Start(); err != nil {
		return err
	}

	pool, err := core.NewFakePool(fakeNet)
	if err != nil {
		tun.Stop()
		return err
	}

	dns := &core.DNS{
		Pool: pool,
		// Мимо туннеля идут те же имена, что и на компьютере: локальная сеть
		// и то, что человек внёс в список сам.
		Direct: func(name string) bool {
			if !cfg.LocalViaTunnel && routing.IsLocalHost(name) {
				return true
			}
			return direct != nil && direct.Match(name)
		},
		Local: func(name string) ([]net.IP, error) {
			if cb == nil {
				return nil, fmt.Errorf("нет резолвера")
			}
			return parseIPs(cb.ResolveLocal(name))
		},
	}

	eng, err := core.Start(tunFD, uint32(mtu), &core.Handler{
		Core:    tun,
		Resolve: pool.Resolver(),
		DNS:     dns,
	})
	if err != nil {
		tun.Stop()
		return err
	}

	stop := make(chan struct{})
	t.mu.Lock()
	t.core, t.engine, t.bus, t.stop = tun, eng, bus, stop
	t.mu.Unlock()

	go t.forwardEvents(bus, stop, cb)
	return nil
}

// forwardEvents перекладывает события ядра на экран приложения.
func (t *Tunnel) forwardEvents(bus *events.Bus, stop <-chan struct{}, cb Callbacks) {
	if cb == nil {
		return
	}
	ch, unsub := bus.Subscribe()
	defer unsub()

	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Kind {
			case events.KindState:
				cb.OnState(ev.State, ev.Detail)
			case events.KindLog:
				cb.OnLog(ev.Text)
			case events.KindConn:
				cb.OnLog(describeConn(ev))
			}
		}
	}
}

// describeConn превращает событие о соединении в строку для журнала.
// Пометка «напрямую» важна человеку: она означает не сбой, а сработавшее
// правило — например, адрес из локальной сети.
func describeConn(ev events.Event) string {
	line := ev.Target
	if ev.Direct {
		line += " — напрямую"
	}
	if ev.DNSLeak {
		line += " — адрес известен заранее"
	}
	if ev.Failed && ev.Error != "" {
		line += " — ошибка: " + ev.Error
	}
	return line
}

// Stop останавливает всё и освобождает дескриптор.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	tun, eng, stop := t.core, t.engine, t.stop
	t.core, t.engine, t.bus, t.stop = nil, nil, nil, nil
	t.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if eng != nil {
		eng.Close()
	}
	if tun != nil {
		tun.Stop()
	}
}

// State — состояние одним словом, для экрана.
func (t *Tunnel) State() string {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun == nil {
		return "stopped"
	}
	return tun.State()
}

// StatsJSON — счётчики для экрана: байты, соединения, живые каналы.
func (t *Tunnel) StatsJSON() string {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun == nil {
		return "{}"
	}
	b, err := json.Marshal(tun.Stats())
	if err != nil {
		return "{}"
	}
	return string(b)
}

// fakeNet — подсеть подставных адресов. 198.18.0.0/15 зарезервирована под
// тесты сетевого оборудования: настоящих сайтов там нет, спутать не с чем.
const fakeNet = "198.18.0.0/15"

// FakeNet отдаёт подсеть приложению: её надо добавить в маршруты туннеля,
// иначе соединения на подставные адреса до нас не дойдут.
func FakeNet() string { return fakeNet }

func parseIPs(list string) ([]net.IP, error) {
	var out []net.IP
	for _, part := range strings.Split(list, ",") {
		if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("имя не разрешилось")
	}
	return out, nil
}
