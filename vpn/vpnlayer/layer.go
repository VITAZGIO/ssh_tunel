//go:build windows || linux

package vpnlayer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"sshtunnel/android/core"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/tunnel"
	"sshtunnel/internal/udprelay"
)

// Layer — режим VPN, подключаемый к app.App (реализует app.NetLayer).
//
// Адаптер поднимается один раз на подключение и переживает переход на
// запасной сервер: меняется только туннель, в который он ведёт.
type Layer struct {
	bus *events.Bus
	sys *sys

	// current — туннель, куда сейчас идут соединения из адаптера.
	current atomic.Pointer[tunnel.Tunnel]

	mu      sync.Mutex
	direct  *routing.DirectList
	engine  *core.Engine
	up      bool
	unwatch func()
	servers []netip.Addr
	sshPort int
}

// New готовит слой. Адаптер появится только при подключении.
func New(bus *events.Bus) *Layer {
	return &Layer{bus: bus, sys: newSys(bus)}
}

// Prepare вызывается перед созданием каждого туннеля.
func (l *Layer) Prepare(cfg *tunnel.Config) {
	// Локальные прокси в режиме VPN не нужны: всё приходит из адаптера. И
	// не занимать порты 1080/1081 полезно — обычная версия может быть
	// запущена рядом.
	cfg.SocksAddr, cfg.HTTPAddr = "", ""
	cfg.ProtectSocket = l.sys.protect
	cfg.Resolver = l.resolver()
	cfg.TunProcessRules = true

	l.mu.Lock()
	l.direct = cfg.Direct
	l.mu.Unlock()
}

// Attach поднимает адаптер или переключает уже поднятый на новый туннель.
func (l *Layer) Attach(tun *tunnel.Tunnel, p config.Profile) error {
	// До блокировки: поиск адреса может занять секунды, а под блокировкой
	// ждали бы и все соединения из адаптера.
	servers := l.lookupServer(p.Host)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.current.Store(tun)
	l.servers, l.sshPort = servers, p.SSHPort
	if l.up {
		return nil
	}
	if err := l.bringUp(); err != nil {
		l.bringDown()
		l.current.Store(nil)
		return err
	}
	l.bus.Infof("VPN включён: весь трафик системы идёт через адаптер %s", l.sys.name())
	return nil
}

// Detach снимает адаптер: вместе с ним исчезают его маршруты, и система
// сразу возвращается к обычной сети.
func (l *Layer) Detach() {
	l.mu.Lock()
	defer l.mu.Unlock()
	wasUp := l.up
	l.bringDown()
	l.current.Store(nil)
	if wasUp {
		l.bus.Infof("VPN выключен, сеть работает как обычно")
	}
}

func (l *Layer) bringUp() error {
	l.up = true
	l.sys.refresh()
	if !l.sys.hasUplink() {
		return errors.New("не нашёл подключение к интернету — не к чему привязать туннель")
	}

	dev, err := l.sys.open()
	if err != nil {
		return err
	}
	if err := l.startStack(dev); err != nil {
		return err
	}
	if err := l.sys.configure(); err != nil {
		return err
	}

	unwatch, err := l.sys.watch(l.networkChanged)
	if err != nil {
		l.bus.Warnf("Не получится следить за сменой сети: %v", err)
	} else {
		l.unwatch = unwatch
	}
	l.sys.flushDNS()
	return nil
}

func (l *Layer) bringDown() {
	if !l.up {
		return
	}
	l.up = false
	if l.unwatch != nil {
		l.unwatch()
		l.unwatch = nil
	}
	// Сначала адаптер, потом всё остальное (запрет DNS, настройки DNS
	// системы): наоборот на долю секунды открылось бы окно, когда DNS ещё
	// смотрит в адаптер, а защиты уже нет.
	if l.engine != nil {
		l.engine.Close() // закрывает и устройство
		l.engine = nil
	}
	l.sys.close()
	l.sys.flushDNS()
}

func (l *Layer) startStack(dev core.Device) error {
	pool, err := core.NewFakePool(fakeNet)
	if err != nil {
		dev.Close()
		return err
	}
	stats := &core.Stats{}
	resolver := l.resolver()

	dns := &core.DNS{
		Pool:  pool,
		Stats: stats,
		// Мимо туннеля — те же имена, что и в режиме прокси: локальная
		// сеть и список «всегда напрямую».
		Direct: func(name string) bool {
			if t := l.current.Load(); t != nil && !t.LocalViaTunnel() && routing.IsLocalHost(name) {
				return true
			}
			l.mu.Lock()
			d := l.direct
			l.mu.Unlock()
			return d != nil && d.Match(name)
		},
		Local: func(name string) ([]net.IP, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			addrs, err := resolver.LookupIP(ctx, "ip4", name)
			if err != nil {
				return nil, err
			}
			// Приложение придёт уже с адресом, а не с именем, — ядро должно
			// узнать его и не повести через сервер (см. tunnel.LearnDirect).
			if t := l.current.Load(); t != nil {
				t.LearnDirect(name, addrs)
			}
			return addrs, nil
		},
	}

	engine, err := core.StartDevice(dev, &core.Handler{
		Core:    (*coreSwitch)(l),
		Resolve: pool.Resolver(),
		DNS:     dns,
		Stats:   stats,
		Log:     func(line string) { l.bus.Infof("%s", line) },
		UDPRelay: func() *udprelay.Client {
			if t := l.current.Load(); t != nil {
				return t.UDPRelay()
			}
			return nil
		},
	})
	if err != nil {
		return err // StartDevice уже закрыл устройство
	}
	l.engine = engine
	return nil
}

// resolver — резолвер для своих нужд программы: спрашивает DNS-сервер
// настоящей сети через привязанный к ней сокет. Системный спрашивать нельзя:
// пока VPN включён, он смотрит в наш же адаптер.
func (l *Layer) resolver() *net.Resolver {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: l.sys.protect}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, pickDNSServer(address, l.sys.dnsServers()))
		},
	}
}

// networkChanged — сменилась настоящая сеть: старые SSH-соединения привязаны
// к прежней карте и почти наверняка мертвы, пересобираем пул сразу.
func (l *Layer) networkChanged() {
	l.bus.Infof("Сеть сменилась — переподключаюсь к серверу")
	if t := l.current.Load(); t != nil {
		t.Kick()
	}
}

// lookupServer узнаёт адреса сервера — чтобы распознать петлю (см.
// isServerTarget). Спрашивает настоящий DNS, как и сам туннель.
func (l *Layer) lookupServer(host string) []netip.Addr {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a.Unmap()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := l.resolver().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips
}

// coreSwitch отдаёт соединения из адаптера текущему туннелю.
type coreSwitch Layer

func (c *coreSwitch) ServeConn(conn net.Conn, target string, byIP bool) {
	l := (*Layer)(c)
	t := l.current.Load()
	if t == nil {
		conn.Close()
		return
	}
	l.mu.Lock()
	servers, port := l.servers, l.sshPort
	l.mu.Unlock()
	if byIP && isServerTarget(target, servers, port) {
		conn.Close()
		return
	}
	t.ServeConn(conn, target, byIP)
}
