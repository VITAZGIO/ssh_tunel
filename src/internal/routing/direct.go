package routing

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// Свой список «всегда напрямую» — для сетей, о которых программа знать не
// может.
//
// Встроенных правил (RFC 1918, петлевые, link-local, CGNAT) хватает для
// домашней сети и для mesh-VPN вроде NetBird и Tailscale. Но сетей бывает
// больше: Hamachi раздаёт адреса из 25.0.0.0/8, корпоративный VPN — из любого
// диапазона, у самодельного WireGuard адреса какие угодно. Вести такие адреса
// через сервер бессмысленно ровно по той же причине, что и домашние: сервер
// будет искать их у себя.
//
// Поэтому список задаёт пользователь, а формат принимается любой, каким
// человек естественно напишет адрес:
//
//	100.64.0.0/10      сеть в записи CIDR
//	25.*, 10.8.*       то же самое шаблоном (превращается в /8, /16, /24)
//	100.104.1.5        одиночный адрес
//	nas.mydomain.com   конкретное имя
//	.netbird.cloud     любое имя с таким окончанием
//	*.corp.local       то же самое, привычной записью

// DirectList потокобезопасен: список правится в настройках, не останавливая
// туннель.
type DirectList struct {
	mu       sync.RWMutex
	ips      []net.IP
	nets     []*net.IPNet
	names    []string
	suffixes []string
}

func NewDirectList(entries []string) *DirectList {
	d := &DirectList{}
	d.Set(entries)
	return d
}

func (d *DirectList) Set(entries []string) {
	var (
		ips      []net.IP
		nets     []*net.IPNet
		names    []string
		suffixes []string
	)
	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		e = strings.Trim(e, "[]")
		if e == "" {
			continue
		}
		if n := parseNet(e); n != nil {
			nets = append(nets, n)
			continue
		}
		if ip := net.ParseIP(e); ip != nil {
			ips = append(ips, ip)
			continue
		}
		// Имя: "*.corp.local" и ".corp.local" — одно и то же правило.
		if s, ok := strings.CutPrefix(e, "*"); ok {
			e = s
		}
		if strings.HasPrefix(e, ".") {
			suffixes = append(suffixes, e)
			continue
		}
		names = append(names, strings.TrimSuffix(e, "."))
	}

	d.mu.Lock()
	d.ips, d.nets, d.names, d.suffixes = ips, nets, names, suffixes
	d.mu.Unlock()
}

// Empty — список пуст, проверять нечего.
func (d *DirectList) Empty() bool {
	if d == nil {
		return true
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.ips)+len(d.nets)+len(d.names)+len(d.suffixes) == 0
}

// Match — попадает ли цель ("host" или "host:port") под одно из правил.
func (d *DirectList) Match(target string) bool {
	if d == nil {
		return false
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if ip := net.ParseIP(h); ip != nil {
		for _, x := range d.ips {
			if x.Equal(ip) {
				return true
			}
		}
		for _, n := range d.nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	for _, n := range d.names {
		if h == n {
			return true
		}
	}
	for _, s := range d.suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// parseNet понимает и CIDR, и шаблон со звёздочкой: "10.8.*" человек напишет
// куда охотнее, чем "10.8.0.0/16".
func parseNet(e string) *net.IPNet {
	if strings.Contains(e, "/") {
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil
		}
		return n
	}
	if !strings.HasSuffix(e, ".*") {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(e, ".*"), ".")
	if len(parts) < 1 || len(parts) > 3 {
		return nil
	}
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	bits := 0
	switch {
	case strings.Count(e, ".") == 1: // "25.*"
		bits = 8
	case strings.Count(e, ".") == 2: // "10.8.*"
		bits = 16
	default: // "192.168.1.*"
		bits = 24
	}
	_, n, err := net.ParseCIDR(fmt.Sprintf("%s/%d", strings.Join(parts, "."), bits))
	if err != nil {
		return nil
	}
	return n
}

// SplitEntries разбирает то, что человек ввёл одной строкой: через запятую,
// пробел или с новой строки — как получится, так и разберём.
func SplitEntries(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(f))
	for _, v := range f {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
