package vpnlayer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"sshtunnel/android/core"
	"sshtunnel/internal/events"
	"sshtunnel/vpn/internal/wfp"
)

// sys — всё, что режим VPN делает средствами Windows: адаптер Wintun, его
// адреса и маршруты, запрет DNS мимо адаптера и привязка своих сокетов к
// настоящей сети (той, через которую Windows ходила бы в интернет без нас).
type sys struct {
	bus *events.Bus

	mu   sync.RWMutex
	ours uint64 // LUID нашего адаптера; 0, пока его нет
	idx4 uint32
	idx6 uint32
	dns  []netip.Addr

	dev   *device
	guard *wfp.Guard
}

func newSys(bus *events.Bus) *sys { return &sys{bus: bus} }

func (s *sys) name() string { return adapterName }

// refresh заново находит настоящую сеть. Возвращает true, если она сменилась
// (перешли с Wi-Fi на кабель, переподключились к другой точке).
func (s *sys) refresh() bool {
	s.mu.RLock()
	ours := s.ours
	s.mu.RUnlock()

	r4, ok4 := findDefault(windows.AF_INET, ours)
	r6, _ := findDefault(windows.AF_INET6, ours)

	var dns []netip.Addr
	if ok4 {
		dns, _ = winipcfg.LUID(r4.luid).DNS()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.idx4 != r4.ifIndex || s.idx6 != r6.ifIndex
	s.idx4, s.idx6, s.dns = r4.ifIndex, r6.ifIndex, dns
	return changed
}

func (s *sys) hasUplink() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idx4 != 0
}

func (s *sys) dnsServers() []netip.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dns
}

// findDefault ищет маршрут по умолчанию с наименьшей метрикой, кроме нашего.
func findDefault(family winipcfg.AddressFamily, ours uint64) (defaultRoute, bool) {
	table, err := winipcfg.GetIPForwardTable2(family)
	if err != nil {
		return defaultRoute{}, false
	}
	var routes []defaultRoute
	for i := range table {
		row := &table[i]
		if row.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		iface, err := row.InterfaceLUID.IPInterface(family)
		if err != nil || !iface.Connected {
			continue
		}
		routes = append(routes, defaultRoute{
			luid:    uint64(row.InterfaceLUID),
			ifIndex: row.InterfaceIndex,
			metric:  row.Metric + iface.Metric,
		})
	}
	return pickDefault(routes, ours)
}

// protect привязывает сокет к настоящей сети (IP_UNICAST_IF): Windows отправит
// его пакеты через эту карту, не глядя на наши маршруты в адаптер.
func (s *sys) protect(network, address string, c syscall.RawConn) error {
	s.mu.RLock()
	idx4, idx6 := s.idx4, s.idx6
	s.mu.RUnlock()
	if idx4 == 0 && idx6 == 0 {
		s.refresh()
		s.mu.RLock()
		idx4, idx6 = s.idx4, s.idx6
		s.mu.RUnlock()
	}
	var serr error
	err := c.Control(func(fd uintptr) {
		h := windows.Handle(fd)
		switch network {
		case "tcp4", "udp4":
			if idx4 != 0 {
				serr = bindSocket4(h, idx4)
			}
		case "tcp6", "udp6":
			if idx6 != 0 {
				serr = bindSocket6(h, idx6)
			} else if idx4 != 0 {
				// У настоящей сети нет IPv6 — такой сокет не должен уйти
				// в наш адаптер, где IPv6 есть. Пусть лучше не соединится.
				serr = fmt.Errorf("у настоящей сети нет IPv6")
			}
		}
	})
	if err != nil {
		return err
	}
	return serr
}

func bindSocket4(h windows.Handle, ifIndex uint32) error {
	const ipUnicastIf = 31
	// Для IPv4 Windows ждёт номер интерфейса в сетевом порядке байт.
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], ifIndex)
	v := *(*uint32)(unsafe.Pointer(&b[0]))
	return windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, int(v))
}

func bindSocket6(h windows.Handle, ifIndex uint32) error {
	const ipv6UnicastIf = 31
	return windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(ifIndex))
}

// open загружает драйвер и создаёт адаптер.
func (s *sys) open() (core.Device, error) {
	if err := loadWintun(); err != nil {
		return nil, err
	}
	dev, err := openDevice()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.dev, s.ours = dev, uint64(dev.luid)
	s.mu.Unlock()
	return dev, nil
}

// configure назначает адаптеру адреса, маршруты, DNS и наименьшую метрику —
// так Windows считает его главной сетью, — и ставит запрет DNS мимо него.
func (s *sys) configure() error {
	luid := s.dev.luid
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
		err := configureFamily(luid, f.family, f.prefix, f.routes, f.next)
		// «Element not found» на IPv6 — на компьютере выключен IPv6 (галочка
		// в свойствах адаптера или DisabledComponents в реестре): у адаптера
		// просто нет IPv6-интерфейса. Работаем по IPv4, как WireGuard.
		if f.family == windows.AF_INET6 && errors.Is(err, windows.ERROR_NOT_FOUND) {
			s.bus.Warnf("IPv6 на этом компьютере выключен — VPN работает только по IPv4. " +
				"Если IPv6 выключен лишь у адаптера VPN, а у сетевой карты включён, IPv6-соединения пойдут мимо туннеля.")
			continue
		}
		if err != nil {
			return err
		}
	}
	if err := luid.SetDNS(windows.AF_INET, []netip.Addr{dnsAddr}, nil); err != nil {
		return fmt.Errorf("DNS адаптера: %w", err)
	}

	// Без запрета DNS мимо адаптера провайдер видел бы запросы и мог бы
	// подсунуть заглушку. Не встал запрет — работать всё равно можно, но
	// человек должен об этом знать.
	guard, err := wfp.EnableDNSGuard(uint64(luid))
	if err != nil {
		s.bus.Warnf("Не удалось запретить DNS мимо VPN (%v): возможна утечка DNS-запросов провайдеру", err)
	} else {
		s.guard = guard
	}
	return nil
}

// configureFamily — адрес, маршруты и метрика адаптера для одного семейства
// адресов (IPv4 или IPv6).
func configureFamily(luid winipcfg.LUID, family winipcfg.AddressFamily, prefix netip.Prefix, routes []netip.Prefix, next netip.Addr) error {
	name := "IPv4"
	if family == windows.AF_INET6 {
		name = "IPv6"
	}
	if err := luid.SetIPAddressesForFamily(family, []netip.Prefix{prefix}); err != nil {
		return fmt.Errorf("%s, адрес %s: %w", name, prefix, err)
	}
	var rd []*winipcfg.RouteData
	for _, r := range routes {
		rd = append(rd, &winipcfg.RouteData{Destination: r, NextHop: next, Metric: 0})
	}
	if err := luid.SetRoutesForFamily(family, rd); err != nil {
		return fmt.Errorf("%s, маршруты: %w", name, err)
	}
	ipif, err := luid.IPInterface(family)
	if err != nil {
		return fmt.Errorf("%s, интерфейс адаптера: %w", name, err)
	}
	ipif.UseAutomaticMetric = false
	ipif.Metric = 0
	ipif.NLMTU = adapterMTU
	ipif.DadTransmits = 0
	ipif.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	if err := ipif.Set(); err != nil {
		return fmt.Errorf("%s, метрика адаптера: %w", name, err)
	}
	return nil
}

// close убирает то, что не исчезает вместе с адаптером. Сам адаптер к этому
// моменту уже закрыт стеком, а с ним Windows убрала и адреса, и маршруты.
func (s *sys) close() {
	s.guard.Close()
	s.guard = nil
	s.mu.Lock()
	s.dev, s.ours = nil, 0
	s.mu.Unlock()
}

// watch зовёт onChange, когда меняется настоящая сеть. Остановить — вызвать
// возвращённую функцию.
func (s *sys) watch(onChange func()) (func(), error) {
	var mu sync.Mutex
	var timer *time.Timer
	cb, err := winipcfg.RegisterRouteChangeCallback(func(_ winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		if route == nil || route.DestinationPrefix.PrefixLength != 0 {
			return
		}
		// Смена сети — это пачка уведомлений подряд. Разбираемся, когда
		// всё утихнет, а не на каждое.
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(700*time.Millisecond, func() {
			if s.refresh() {
				onChange()
			}
		})
	})
	if err != nil {
		return nil, err
	}
	return func() {
		cb.Unregister()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}, nil
}

var procFlushCache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// flushDNS забывает ответы, полученные до включения (или после выключения):
// иначе браузер ещё какое-то время ходил бы по старым адресам мимо нашего
// DNS — или, наоборот, по подставным, которых больше нет.
func (s *sys) flushDNS() {
	if procFlushCache.Find() == nil {
		procFlushCache.Call()
	}
}
