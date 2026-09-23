package winvpn

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// physical следит за настоящей сетью компьютера — той, через которую Windows
// ходила бы в интернет без нас. К ней привязываются все сокеты, которые
// открывает сама программа: соединения с сервером, прямые соединения «мимо
// туннеля» и DNS-запросы для них. Иначе они ушли бы в наш же адаптер и
// зациклились.
type physical struct {
	mu   sync.RWMutex
	ours uint64 // LUID нашего адаптера; 0, пока его нет
	idx4 uint32
	idx6 uint32
	dns  []netip.Addr
}

// refresh заново находит настоящую сеть. Возвращает true, если она сменилась
// (перешли с Wi-Fi на кабель, переподключились к другой точке).
func (p *physical) refresh() bool {
	p.mu.RLock()
	ours := p.ours
	p.mu.RUnlock()

	r4, ok4 := findDefault(windows.AF_INET, ours)
	r6, _ := findDefault(windows.AF_INET6, ours)

	var dns []netip.Addr
	if ok4 {
		dns, _ = winipcfg.LUID(r4.luid).DNS()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	changed := p.idx4 != r4.ifIndex || p.idx6 != r6.ifIndex
	p.idx4, p.idx6, p.dns = r4.ifIndex, r6.ifIndex, dns
	return changed
}

func (p *physical) setOurs(luid uint64) {
	p.mu.Lock()
	p.ours = luid
	p.mu.Unlock()
}

func (p *physical) indexes() (uint32, uint32) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.idx4, p.idx6
}

func (p *physical) dnsServers() []netip.Addr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dns
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
func (p *physical) protect(network, address string, c syscall.RawConn) error {
	idx4, idx6 := p.indexes()
	if idx4 == 0 && idx6 == 0 {
		p.refresh()
		idx4, idx6 = p.indexes()
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

// resolver — резолвер для своих нужд программы: спрашивает DNS-сервер
// настоящей сети через привязанный к ней сокет. Системный спрашивать нельзя:
// пока VPN включён, он смотрит в наш же адаптер.
func (p *physical) resolver() *net.Resolver {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: p.protect}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, pickDNSServer(address, p.dnsServers()))
		},
	}
}

// watch зовёт onChange, когда меняется настоящая сеть. Остановить — вызвать
// возвращённую функцию.
func (p *physical) watch(onChange func()) (func(), error) {
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
			if p.refresh() {
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
