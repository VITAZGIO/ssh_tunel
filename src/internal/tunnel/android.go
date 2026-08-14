package tunnel

// Точка входа для Android.
//
// На компьютере соединение приходит от SOCKS или HTTP-прокси, и по локальному
// порту можно узнать программу-владельца. На телефоне соединение собирается из
// IP-пакетов, владельца по порту не определить — да и не нужно: какие
// приложения заворачивать, решает сама система (VpnService.addAllowedApplication),
// и то, что дошло до нас, уже отфильтровано.
//
// Поэтому здесь тот же путь, что и в serve(), минус определение процесса и
// минус правила по программам. Правила «локальная сеть напрямую» и «всегда
// напрямую» остаются: они про адрес назначения, а не про программу.

import (
	"net"
	"time"
)

// ServeConn обслуживает готовое соединение от приложения на телефоне:
// открывает встречное через сервер, пишет событие в лог, считает байты
// и перекачивает данные в обе стороны. Соединение закрывается само.
//
// target — куда шло приложение (имя хоста, если его удалось восстановить,
// иначе адрес), byIP — пришёл готовый адрес, то есть имя разрешалось не нами.
func (t *Tunnel) ServeConn(conn net.Conn, target string, byIP bool) {
	defer conn.Close()

	remote, direct, err := t.dialForTun(target)
	if err != nil {
		t.bus.Publish(eventConn("", 0, target, "tun", byIP, direct, err))
		return
	}
	defer remote.Close()

	t.bus.Publish(eventConn("", 0, target, "tun", byIP, direct, nil))

	t.stats.active.Add(1)
	t.stats.total.Add(1)
	defer t.stats.active.Add(-1)

	t.pump(conn, conn, remote)
}

// dialForTun — решение «через сервер или напрямую» для трафика с телефона.
// Отличается от dialFor только тем, что не спрашивает про программу.
func (t *Tunnel) dialForTun(target string) (net.Conn, bool, error) {
	if !t.localDirect(target) && !t.listedDirect(target) {
		c, err := t.Dial("tcp", target)
		return c, false, err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	c, err := d.Dial("tcp", target)
	return c, true, err
}
