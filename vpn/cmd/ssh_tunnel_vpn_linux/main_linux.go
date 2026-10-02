// ssh_tunnel_vpn_linux — версия для Linux с режимом VPN: через туннель идёт
// весь трафик системы, а не только программы, настроенные на прокси. Флаги,
// журнал и веб-интерфейс — те же, что у ssh_tunnel_linux (internal/linuxcli).
//
// Нужен root: создавать сетевые устройства и менять маршруты ядро позволяет
// только ему. Запускать через sudo — настройки при этом берутся из домашней
// папки того, кто запустил, и остаются общими с обычной версией.
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"sshtunnel/internal/app"
	"sshtunnel/internal/events"
	"sshtunnel/internal/linuxcli"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/vpnlayer"
)

func main() {
	// Без root VPN не поднять: ядро не даст создать сетевое устройство. Раньше
	// программа всё равно открывала панель, и туннель молча не подключался —
	// лучше сразу сказать, что не так. Сохранить настройки и показать справку
	// можно и без root.
	if os.Geteuid() != 0 && needsRoot(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "Режиму VPN нужны права root: создавать сетевое устройство ядро разрешает только ему.")
		fmt.Fprintln(os.Stderr, "Запусти так:  sudo ssh_tunnel_vpn_linux "+strings.Join(os.Args[1:], " "))
		os.Exit(1)
	}

	uid, gid, ok := useSudoUserConfig()
	if !ok {
		uid, gid, ok = configDirOwner()
	}
	if ok {
		defer giveBack(uid, gid)
		giveBack(uid, gid) // вдруг прошлый запуск упал и оставил файлы root'у
	}
	updater.VPN = true
	// Галочка «Запускать при старте системы» — системная служба: обычной
	// версии хватает пользовательской, а VPN нужен root (см. boot_linux.go).
	webui.SetBootControl(newSystemBoot(os.Args[1:]))
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
	base := filepath.Join(u.HomeDir, ".config")
	os.Setenv("XDG_CONFIG_HOME", base)
	ownBase(base, uid, gid)
	return uid, gid, true
}

// ownBase — папка ~/.config должна принадлежать пользователю. Если её ещё не
// было, её создала бы сама программа — от root (прежние версии так и
// делали), и потом ни обычная версия, ни systemd пользователя не могли бы
// ничего в ней создать («mkdir ~/.config/systemd: permission denied»).
// Чиним и уже испорченную: только саму папку, без захода внутрь — там могут
// лежать чужие настройки.
func ownBase(base string, uid, gid int) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return
	}
	if st, err := os.Stat(base); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid == 0 {
			os.Chown(base, uid, gid)
		}
	}
}

// needsRoot — будет ли программа поднимать VPN. -save, -env и справка
// только пишут или печатают настройки, им root не нужен.
func needsRoot(args []string) bool {
	for _, a := range args {
		switch strings.TrimLeft(a, "-") {
		case "save", "env", "h", "help":
			return false
		}
	}
	return true
}

// configDirOwner — служба systemd работает от root, но с XDG_CONFIG_HOME
// пользователя (см. docs/VPN.md, автозапуск на Linux): настройки общие с
// обычной версией. Файлы, которые служба там создаст или перепишет, должны
// остаться его, иначе обычная версия не сможет их сохранить.
func configDirOwner() (uid, gid int, ok bool) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if os.Geteuid() != 0 || base == "" {
		return 0, 0, false
	}
	st, err := os.Stat(base)
	if err != nil {
		return 0, 0, false
	}
	sys, isUnix := st.Sys().(*syscall.Stat_t)
	if !isUnix || sys.Uid == 0 {
		return 0, 0, false
	}
	return int(sys.Uid), int(sys.Gid), true
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
