// ssh_tunnel_vpn_linux — версия для Linux с режимом VPN: через туннель идёт
// весь трафик системы, а не только программы, настроенные на прокси. Флаги,
// журнал и веб-интерфейс — те же, что у ssh_tunnel_linux (internal/linuxcli).
//
// Нужен root: создавать сетевые устройства и менять маршруты ядро позволяет
// только ему. Запускать через sudo — настройки при этом берутся из домашней
// папки того, кто запустил, и остаются общими с обычной версией.
package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"sshtunnel/internal/app"
	"sshtunnel/internal/events"
	"sshtunnel/internal/linuxcli"
	"sshtunnel/internal/updater"
	"sshtunnel/vpn/vpnlayer"
)

func main() {
	uid, gid, ok := useSudoUserConfig()
	if ok {
		defer giveBack(uid, gid)
		giveBack(uid, gid) // вдруг прошлый запуск упал и оставил файлы root'у
	}
	updater.VPN = true
	linuxcli.Run(linuxcli.Options{
		Name: "sudo ssh_tunnel_vpn_linux",
		NetLayer: func(bus *events.Bus) app.NetLayer {
			return vpnlayer.New(bus)
		},
	})
}

// useSudoUserConfig: под sudo домашняя папка — /root, и настройки оказались
// бы отдельными от обычной версии. Берём папку того, кто вызвал sudo.
func useSudoUserConfig() (uid, gid int, ok bool) {
	name := os.Getenv("SUDO_USER")
	if os.Geteuid() != 0 || name == "" || name == "root" || os.Getenv("XDG_CONFIG_HOME") != "" {
		return 0, 0, false
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(u.HomeDir, ".config"))
	return uid, gid, true
}

// giveBack возвращает владельца файлам настроек: иначе после запуска под
// sudo обычная версия не смогла бы их сохранить.
func giveBack(uid, gid int) {
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "ssh_tunnel")
	filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err == nil {
			os.Lchown(path, uid, gid)
		}
		return nil
	})
}
