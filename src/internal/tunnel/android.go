package tunnel

// Точка входа для сетевого стека: Android и режим VPN на Windows.
//
// Здесь соединение собрано из IP-пакетов, а не пришло от SOCKS или
// HTTP-прокси. На Android владельца по порту не определить — да и не нужно:
// какие приложения заворачивать, решает сама система
// (VpnService.addAllowedApplication). На Windows владелец виден по порту так же,
// как у прокси, и правила по программам включаются через
// Config.TunProcessRules. Правила «локальная сеть напрямую» и «всегда
// напрямую» действуют всегда: они про адрес назначения, а не про программу.

import (
	"net"
)

// ServeConn обслуживает готовое соединение от приложения на телефоне:
// открывает встречное через сервер, пишет событие в лог, считает байты
// и перекачивает данные в обе стороны. Соединение закрывается само.
//
// target — куда шло приложение (имя хоста, если его удалось восстановить,
// иначе адрес), byIP — пришёл готовый адрес, то есть имя разрешалось не нами.
func (t *Tunnel) ServeConn(conn net.Conn, target string, byIP bool) {
	defer conn.Close()

	var proc string
	var pid int
	if t.cfg.TunProcessRules {
		// Адрес приложения у соединения из стека — его собственный сокет,
		// поэтому владелец находится по порту так же, как у прокси.
		proc, pid = lookupProcess(conn)
	}

	remote, direct, err := t.dialFor(proc, target)
	if err != nil {
		t.bus.Publish(eventConn(proc, pid, target, "tun", byIP, direct, err))
		return
	}
	defer remote.Close()

	t.bus.Publish(eventConn(proc, pid, target, "tun", byIP, direct, nil))

	t.stats.active.Add(1)
	t.stats.total.Add(1)
	defer t.stats.active.Add(-1)

	t.pump(conn, conn, remote)
}
