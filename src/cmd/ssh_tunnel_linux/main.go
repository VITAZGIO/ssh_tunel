// ssh_tunnel для Linux — версия для серверов и рабочих станций: прокси
// SOCKS4/5 и HTTP, журнал в вывод, по флагу -web — веб-интерфейс. Вся
// программа — в internal/linuxcli: её же использует версия с режимом VPN.
package main

import "sshtunnel/internal/linuxcli"

func main() {
	linuxcli.Run(linuxcli.Options{Name: "ssh_tunnel_linux"})
}
