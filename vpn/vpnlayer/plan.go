// Пакет vpnlayer — режим VPN для Windows и Linux: виртуальный сетевой адаптер
// (Wintun на Windows, TUN на Linux), в который система отдаёт весь трафик, и
// сетевой стек поверх него, который превращает пакеты обратно в соединения и
// отдаёт их в тот же туннель, что и прокси-режим.
//
// Раскладка: layer.go — общая часть (стек, DNS, переключение туннеля),
// sys_windows.go и sys_linux.go — всё, что делается средствами самой ОС:
// адаптер, адреса, маршруты, DNS системы, привязка своих сокетов к настоящей
// сети. В этом файле — решения, не зависящие от ОС: какие адреса и маршруты
// ставить, какой интерфейс считать «настоящим». Они проверяются тестами на
// любой системе.
package vpnlayer

import (
	"net"
	"net/netip"
	"strconv"
)

const (
	// adapterName — как адаптер называется в «Сетевых подключениях» Windows.
	// На Linux имя интерфейса ограничено 15 символами и принято короткое —
	// там свой linuxIfName.
	adapterName = "ssh_tunnel"
	// adapterMTU — пакеты дальше адаптера не уходят (соединения собираются
	// заново у нас), поэтому обычный размер Ethernet — безопасный выбор.
	adapterMTU = 1500
	// fakeNet — подставные адреса для имён, как на Android (mobile.FakeNet).
	fakeNet = "198.18.128.0/17"
)

var (
	// Адреса адаптера. 198.18.0.0/15 зарезервирован под тесты сетевого
	// оборудования: настоящих сайтов там нет, спутать не с чем.
	adapterPrefix4 = netip.MustParsePrefix("198.18.0.1/15")
	adapterPrefix6 = netip.MustParsePrefix("fd00:c0de:7075::1/64")

	// dnsAddr — наш DNS. Отдельный адрес, не адрес самого адаптера: пакет на
	// собственный адрес система обработала бы сама и нам бы не отдала.
	dnsAddr = netip.MustParseAddr("198.18.0.53")

	// Весь интернет двумя половинками, а не одним маршрутом 0.0.0.0/0: так
	// наши маршруты всегда точнее маршрута по умолчанию настоящей сети, и
	// метрики сравнивать не приходится. Своя локальная подсеть (роутер,
	// принтер) остаётся ещё точнее и идёт мимо адаптера.
	routes4 = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/1"),
		netip.MustParsePrefix("128.0.0.0/1"),
	}
	// IPv6 заворачиваем всегда, даже если сервер его не умеет: иначе
	// соединения по шестой версии уходили бы мимо туннеля — утечка.
	routes6 = []netip.Prefix{
		netip.MustParsePrefix("::/1"),
		netip.MustParsePrefix("8000::/1"),
	}
)

// defaultRoute — маршрут по умолчанию одной из сетевых карт.
type defaultRoute struct {
	luid    uint64
	ifIndex uint32
	// metric — метрика маршрута плюс метрика интерфейса: так её считает
	// сама Windows, выбирая, куда слать.
	metric uint32
}

// pickDefault выбирает интерфейс, через который Windows ходила бы в интернет
// без нас: маршрут по умолчанию с наименьшей метрикой, кроме нашего адаптера.
func pickDefault(routes []defaultRoute, ours uint64) (defaultRoute, bool) {
	var best defaultRoute
	found := false
	for _, r := range routes {
		if r.luid == ours || r.ifIndex == 0 {
			continue
		}
		if !found || r.metric < best.metric {
			best, found = r, true
		}
	}
	return best, found
}

// fallbackDNS — на случай, если у настоящей сети не нашлось ни одного
// DNS-сервера (так бывает на странно настроенных сетях).
var fallbackDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}

// pickDNSServer решает, куда на самом деле слать запрос, который резолвер Go
// собрался отправить на requested. Резолвер берёт список серверов из
// настроек всех адаптеров, включая наш, — а спросить наш адрес значило бы
// спросить самих себя по кругу. Поэтому: сервер настоящей сети — как есть,
// всё остальное — на первый сервер настоящей сети.
func pickDNSServer(requested string, physical []netip.Addr) string {
	if len(physical) == 0 {
		physical = fallbackDNS
	}
	if ap, err := netip.ParseAddrPort(requested); err == nil {
		for _, a := range physical {
			if a == ap.Addr().Unmap() {
				return ap.String()
			}
		}
	}
	return netip.AddrPortFrom(physical[0], 53).String()
}

// isServerTarget — соединение ведёт на сам SSH-сервер. Такое соединение,
// пришедшее из адаптера, означает петлю: наш собственный сокет почему-то не
// удалось привязать к настоящей сети. Вести его в туннель нельзя — он
// поедет сам в себя.
func isServerTarget(target string, serverIPs []netip.Addr, sshPort int) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port != strconv.Itoa(sshPort) {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, s := range serverIPs {
		if s == addr.Unmap() {
			return true
		}
	}
	return false
}
