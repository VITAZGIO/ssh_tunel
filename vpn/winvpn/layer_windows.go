package winvpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"sshtunnel/android/core"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/tunnel"
	"sshtunnel/internal/udprelay"
	"sshtunnel/vpn/internal/wfp"
)

// Layer — режим VPN, подключаемый к app.App (реализует app.NetLayer).
//
// Адаптер поднимается один раз на подключение и переживает переход на
// запасной сервер: меняется только туннель, в который он ведёт.
type Layer struct {
	bus  *events.Bus
	phys *physical

	// current — туннель, куда сейчас идут соединения из адаптера.
	current atomic.Pointer[tunnel.Tunnel]

	mu      sync.Mutex
	direct  *routing.DirectList
	dev     *device
	engine  *core.Engine
	guard   *wfp.Guard
	unwatch func()
	servers []netip.Addr
	sshPort int
}

// New готовит слой. Адаптер появится только при подключении.
func New(bus *events.Bus) *Layer {
	return &Layer{bus: bus, phys: &physical{}}
}

// Prepare вызывается перед созданием каждого туннеля.
func (l *Layer) Prepare(cfg *tunnel.Config) {
	// Локальные прокси в режиме VPN не нужны: всё приходит из адаптера. И
	// не занимать порты 1080/1081 полезно — обычный ssh_tunnel.exe может
	// быть запущен рядом.
	cfg.SocksAddr, cfg.HTTPAddr = "", ""
	cfg.ProtectSocket = l.phys.protect
	cfg.Resolver = l.phys.resolver()
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
	if l.dev != nil {
		return nil
	}
	if err := l.up(); err != nil {
		l.down()
		l.current.Store(nil)
		return err
	}
	l.bus.Infof("VPN включён: весь трафик Windows идёт через адаптер %s", adapterName)
	return nil
}

// Detach снимает адаптер: вместе с ним исчезают его маршруты, и Windows
// сразу возвращается к обычной сети.
func (l *Layer) Detach() {
	l.mu.Lock()
	defer l.mu.Unlock()
	wasUp := l.dev != nil
	l.down()
	l.current.Store(nil)
	if wasUp {
		l.bus.Infof("VPN выключен, сеть работает как обычно")
	}
}

func (l *Layer) up() error {
	l.phys.refresh()
	if idx4, _ := l.phys.indexes(); idx4 == 0 {
		return errors.New("не нашёл подключение к интернету — не к чему привязать туннель")
	}

	if err := loadWintun(); err != nil {
		return err
	}
	dev, err := openDevice()
	if err != nil {
		return err
	}
	l.dev = dev
	l.phys.setOurs(uint64(dev.luid))

	if err := l.startStack(); err != nil {
		return err
	}
	if err := configureAdapter(dev.luid); err != nil {
		return fmt.Errorf("настроить адаптер: %w", err)
	}

	// Без запрета DNS мимо адаптера провайдер видел бы запросы и мог бы
	// подсунуть заглушку. Не встал запрет — работать всё равно можно, но
	// человек должен об этом знать.
	guard, err := wfp.EnableDNSGuard(uint64(dev.luid))
	if err != nil {
		l.bus.Warnf("Не удалось запретить DNS мимо VPN (%v): возможна утечка DNS-запросов провайдеру", err)
	} else {
		l.guard = guard
	}

	unwatch, err := l.phys.watch(l.networkChanged)
	if err != nil {
		l.bus.Warnf("Не получится следить за сменой сети: %v", err)
	} else {
		l.unwatch = unwatch
	}

	flushDNSCache()
	return nil
}

func (l *Layer) down() {
	if l.unwatch != nil {
		l.unwatch()
		l.unwatch = nil
	}
	// Сначала адаптер, потом запрет DNS: наоборот на долю секунды открылось
	// бы окно, когда DNS ещё смотрит в адаптер, а запрета уже нет.
	if l.engine != nil {
		l.engine.Close() // закрывает и устройство
		l.engine, l.dev = nil, nil
	} else if l.dev != nil {
		l.dev.Close()
		l.dev = nil
	}
	if l.guard != nil {
		l.guard.Close()
		l.guard = nil
	}
	l.phys.setOurs(0)
	flushDNSCache()
}

func (l *Layer) startStack() error {
	pool, err := core.NewFakePool(fakeNet)
	if err != nil {
		return err
	}
	stats := &core.Stats{}
	resolver := l.phys.resolver()

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

	engine, err := core.StartDevice(l.dev, &core.Handler{
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
		// StartDevice уже закрыл устройство.
		l.dev = nil
		return err
	}
	l.engine = engine
	return nil
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
	ips, err := l.phys.resolver().LookupNetIP(ctx, "ip", host)
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

// configureAdapter назначает адаптеру адреса, маршруты, DNS и наименьшую
// метрику — так Windows считает его главной сетью.
func configureAdapter(luid winipcfg.LUID) error {
	families := []struct {
		family winipcfg.AddressFamily
		prefix netip.Prefix
		routes []netip.Prefix
		next   netip.Addr
	}{
		{windows.AF_INET, adapterPrefix4, routes4, netip.IPv4Unspecified()},
		{windows.AF_INET6, adapterPrefix6, routes6, netip.IPv6Unspecified()},
	}
	for _, f := range families {
		if err := luid.SetIPAddressesForFamily(f.family, []netip.Prefix{f.prefix}); err != nil {
			return fmt.Errorf("адрес %s: %w", f.prefix, err)
		}
		var rd []*winipcfg.RouteData
		for _, r := range f.routes {
			rd = append(rd, &winipcfg.RouteData{Destination: r, NextHop: f.next, Metric: 0})
		}
		if err := luid.SetRoutesForFamily(f.family, rd); err != nil {
			return fmt.Errorf("маршруты: %w", err)
		}
		ipif, err := luid.IPInterface(f.family)
		if err != nil {
			return err
		}
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
		ipif.NLMTU = adapterMTU
		ipif.DadTransmits = 0
		ipif.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		if err := ipif.Set(); err != nil {
			return fmt.Errorf("метрика адаптера: %w", err)
		}
	}
	return luid.SetDNS(windows.AF_INET, []netip.Addr{dnsAddr}, nil)
}

var procFlushCache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// flushDNSCache забывает ответы, полученные до включения (или после
// выключения): иначе браузер ещё какое-то время ходил бы по старым адресам
// мимо нашего DNS — или, наоборот, по подставным, которых больше нет.
func flushDNSCache() {
	if procFlushCache.Find() == nil {
		procFlushCache.Call()
	}
}
