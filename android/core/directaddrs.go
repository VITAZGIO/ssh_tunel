package core

// Настоящие адреса имён, которые идут мимо туннеля.
//
// Имени из списка «всегда напрямую» DNS отвечает настоящим адресом, а не
// подставным. Дальше приложение идёт с соединением на этот адрес — и если он
// публичный, пакеты всё равно попадают в наш стек (маршрут по умолчанию ведёт
// в туннель). По голому адресу правило по имени уже не сработает, и соединение
// ушло бы через сервер. Поэтому выданные адреса запоминаются: соединение на
// такой адрес возвращается к имени, и ядро видит, что его положено вести
// напрямую.

import (
	"net"
	"sync"
	"time"
)

// directAddrsTTL — сколько помнить адрес. Приложения держат ответ DNS у себя
// дольше, чем мы просим, поэтому запас большой.
const directAddrsTTL = 30 * time.Minute

// directAddrsMax — предел, чтобы память не росла без конца.
const directAddrsMax = 4096

type directEntry struct {
	name    string
	expires time.Time
}

// DirectAddrs помнит, какому имени достался настоящий адрес.
type DirectAddrs struct {
	mu     sync.Mutex
	byAddr map[string]directEntry
	now    func() time.Time
}

func NewDirectAddrs() *DirectAddrs {
	return &DirectAddrs{byAddr: make(map[string]directEntry), now: time.Now}
}

// Add запоминает адреса, выданные имени.
func (m *DirectAddrs) Add(name string, ips []net.IP) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if len(m.byAddr)+len(ips) > directAddrsMax {
		for k, e := range m.byAddr {
			if now.After(e.expires) {
				delete(m.byAddr, k)
			}
		}
		if len(m.byAddr)+len(ips) > directAddrsMax {
			m.byAddr = make(map[string]directEntry)
		}
	}
	for _, ip := range ips {
		m.byAddr[ip.String()] = directEntry{name: name, expires: now.Add(directAddrsTTL)}
	}
}

// Name возвращает имя, которому был выдан этот адрес.
func (m *DirectAddrs) Name(ip string) (string, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byAddr[parsed.String()]
	if !ok || m.now().After(e.expires) {
		return "", false
	}
	return e.name, true
}

// ChainResolvers опрашивает источники по очереди: первый, кто узнал адрес,
// и отвечает.
func ChainResolvers(rs ...Resolver) Resolver {
	return func(ip string) (string, bool) {
		for _, r := range rs {
			if r == nil {
				continue
			}
			if name, ok := r(ip); ok {
				return name, true
			}
		}
		return "", false
	}
}
