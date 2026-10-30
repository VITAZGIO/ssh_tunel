package routing

import (
	"fmt"
	"net"
	"slices"
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
//	home.vitazgio.ru   имя и все его поддомены (sync.home.vitazgio.ru)
//	.netbird.cloud     только поддомены, без самого имени
//	*.corp.local       то же самое, привычной записью
//	https://site.ru/   ссылка из браузера — берётся имя (см. CleanEntry)
//
// Имя покрывает и поддомены: человек вписывает свой домен и ждёт, что
// откроются все сайты на нём. Домашние сервисы живут как раз так —
// https://<имя>.home.vitazgio.ru, — и правило «только само имя» их не
// касалось: они уходили через сервер и не открывались.

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
		e := CleanEntry(raw)
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
		e = strings.TrimSuffix(e, ".")
		names = append(names, e)
		suffixes = append(suffixes, "."+e)
	}

	d.mu.Lock()
	d.ips, d.nets, d.names, d.suffixes = ips, nets, names, suffixes
	d.mu.Unlock()
}

// CleanEntry приводит запись к виду, который понимают правила. Человек
// часто вставляет адрес из строки браузера — "https://vitazgio.ru/" или
// "home.vitazgio.ru:8443": такая запись не совпала бы ни с одним
// соединением, и правило молча не работало бы. Схему, путь и порт у имени
// отбрасываем; сети в записи CIDR и адреса IPv6 не трогаем.
func CleanEntry(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if _, rest, ok := strings.Cut(e, "://"); ok {
		e = rest
		// После схемы идёт адрес из ссылки: всё от первого '/' — путь.
		if i := strings.IndexAny(e, "/?#"); i >= 0 {
			e = e[:i]
		}
	}
	if e == "" || parseNet(e) != nil {
		return e
	}
	if h, _, err := net.SplitHostPort(e); err == nil {
		e = h
	}
	e = strings.Trim(e, "[]")
	if net.ParseIP(e) != nil {
		return e
	}
	// Имя: путь без схемы ("vitazgio.ru/cloud") и точка в конце.
	if i := strings.IndexAny(e, "/?#"); i >= 0 {
		e = e[:i]
	}
	return strings.TrimSuffix(e, ".")
}

// CleanEntries — CleanEntry для всего списка, без пустых записей. Нужен там,
// где список уходит из программы как есть: в исключения системного прокси.
func CleanEntries(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e = CleanEntry(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Names — точные имена из списка: их можно разрешить заранее. Адреса под
// ними нужны там, где соединение приходит уже с адресом, а не с именем (см.
// tunnel). Записи вида ".corp.local" сюда не входят: поддомены заранее не
// перечислить.
func (d *DirectList) Names() []string {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return slices.Clone(d.names)
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
