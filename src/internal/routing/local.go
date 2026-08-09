package routing

import (
	"net"
	"strings"
)

// Локальная сеть — отдельное правило, не зависящее от списка программ.
//
// Соединение до 192.168.x.x или до домашнего NAS по имени не имеет смысла
// вести через сервер: сервер попытается открыть этот адрес В СВОЕЙ сети. В
// лучшем случае это таймаут, в худшем — попадание на чужое устройство в
// подсети хостера, потому что приватные диапазоны у всех одинаковые.
//
// Поэтому такие цели по умолчанию идут напрямую, в любом режиме фильтра.
// Обратное поведение (дотянуться до внутренней сети САМОГО сервера — законная
// задача для SSH) включается отдельной настройкой.

// localSuffixes — доменные суффиксы, которые по соглашению существуют только
// внутри своей сети: mDNS-имена (.local) и то, что раздаёт домашний роутер.
var localSuffixes = []string{
	".local", ".lan", ".home", ".internal", ".localdomain", ".home.arpa",
}

// IsLocalHost отвечает, находится ли цель заведомо в локальной сети.
//
// Правил три:
//   - литеральный адрес из приватного, петлевого или link-local диапазона
//     (10/8, 172.16/12, 192.168/16, 127/8, 169.254/16, ::1, fc00::/7, fe80::/10);
//   - имя без единой точки ("homeassistant", "nas") — такие имена разрешает
//     только локальный DNS или NetBIOS, снаружи они не значат ничего;
//   - имя с одним из локальных суффиксов.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")      // литерал IPv6 приходит в скобках
	h = strings.TrimSuffix(h, ".") // абсолютное имя: "nas.local."
	if h == "" {
		return false
	}

	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified()
	}

	if !strings.Contains(h, ".") {
		return true
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// IsLocalTarget — то же самое для адреса вида "host:port", как он приходит от
// SOCKS и от HTTP CONNECT.
func IsLocalTarget(target string) bool {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	return IsLocalHost(host)
}
