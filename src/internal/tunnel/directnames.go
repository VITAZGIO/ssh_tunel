package tunnel

// Имена из «всегда напрямую» — заранее в адреса.
//
// Правило по имени срабатывает, только когда соединение приходит с именем:
// через HTTP-прокси, SOCKS с удалённым DNS или через подставной адрес в
// режиме VPN. Но часто приходит голый адрес:
//
//   - браузер разрешил имя сам, по DNS-over-HTTPS («безопасный DNS» в Chrome,
//     Edge, Firefox, Яндекс Браузере), — наш DNS имени даже не видел;
//   - адрес лежал в кеше программы с тех пор, как правила ещё не было;
//   - программа говорит с SOCKS адресами (SOCKS4, socks5 без «h»).
//
// Тогда в списке, где записано имя, адрес не узнавался, и соединение уходило
// через сервер — «всегда напрямую» для имён не работало. Поэтому, пока
// туннель жив, имена из списка разрешаются заранее, настоящим DNS, и их
// адреса запоминаются так же, как выданные нашим DNS (см. LearnDirect).
//
// Цена: другой сайт на том же адресе (общий хостинг, CDN) тоже пойдёт
// напрямую. Для имён, которые человек сам вписал в список, это приемлемо.

import (
	"context"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// directRefresh — как часто переспрашивать адреса. Срок памяти (learnedTTL)
// заметно больше, так что между проверками адрес не забывается.
const directRefresh = 10 * time.Minute

// fakeNet — подставные адреса режима VPN (RFC 2544, в интернете их нет).
// Если резолвер вдруг ответил ими, значит, он смотрел в наш же DNS, —
// запомнить такой адрес как «напрямую» значило бы сломать чужие соединения.
var fakeNet = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}

// lookupIP — разрешение имени; переменная, чтобы тесты обходились без DNS.
var lookupIP = func(ctx context.Context, r *net.Resolver, name string) ([]net.IP, error) {
	return r.LookupIP(ctx, "ip", name)
}

type directNames struct {
	once sync.Once
	kick chan struct{}
	mu   sync.Mutex
	seen map[string]string // имя -> адреса, о которых уже писали в журнал
}

func (n *directNames) ch() chan struct{} {
	n.once.Do(func() { n.kick = make(chan struct{}, 1) })
	return n.kick
}

// kickDirectNames — список поменялся: разрешить имена сейчас, не дожидаясь
// очередной проверки.
func (t *Tunnel) kickDirectNames() {
	select {
	case t.dnames.ch() <- struct{}{}:
	default:
	}
}

// keepDirectResolved живёт, пока жив туннель.
//
// На телефоне не нужен: там приложения не обходят наш DNS, а свой резолвер
// у Go в процессе VpnService не работает (нет /etc/resolv.conf).
func (t *Tunnel) keepDirectResolved(ctx context.Context) {
	if runtime.GOOS == "android" {
		return
	}
	tick := time.NewTicker(directRefresh)
	defer tick.Stop()
	for {
		t.resolveDirectNames(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.dnames.ch():
		}
	}
}

func (t *Tunnel) resolveDirectNames(ctx context.Context) {
	t.mu.RLock()
	d, r := t.cfg.Direct, t.cfg.Resolver
	t.mu.RUnlock()
	if r == nil {
		r = net.DefaultResolver
	}
	for _, name := range d.Names() {
		if strings.HasSuffix(name, ".mesh") {
			continue // имена сети устройств — не про интернет
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := lookupIP(lctx, r, name)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			continue // имени нет или DNS молчит — проверим в следующий раз
		}
		ips = slices.DeleteFunc(ips, func(ip net.IP) bool {
			return fakeNet.Contains(ip) || ip.IsUnspecified()
		})
		if len(ips) == 0 {
			continue
		}
		t.LearnDirect(name, ips)
		t.logDirectName(name, ips)
	}
}

// logDirectName пишет в журнал, какие адреса пойдут напрямую, — только при
// первом разрешении и при смене адресов, а не каждые десять минут.
func (t *Tunnel) logDirectName(name string, ips []net.IP) {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	slices.Sort(s)
	list := strings.Join(s, ", ")

	n := &t.dnames
	n.mu.Lock()
	if n.seen == nil {
		n.seen = make(map[string]string)
	}
	same := n.seen[name] == list
	n.seen[name] = list
	n.mu.Unlock()
	if !same && t.bus != nil {
		t.bus.Infof("Всегда напрямую: %s — %s", name, list)
	}
}
