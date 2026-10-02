//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// Автозапуск VPN-версии на Linux — системная служба systemd. Обычной версии
// хватает пользовательской (systemctl --user), но VPN нужен root, а
// пользовательские службы работают без него. Служба берёт настройки из той же
// папки, что и запущенная сейчас программа (XDG_CONFIG_HOME): под sudo — из
// домашней папки пользователя, под root — из /root.

const (
	vpnUnitName = "ssh_tunnel_vpn.service"
	vpnUnitPath = "/etc/systemd/system/" + vpnUnitName
)

type systemBoot struct {
	webFlags []string
}

func newSystemBoot(args []string) systemBoot { return systemBoot{webFlags: webFlags(args)} }

func (systemBoot) Enabled() bool {
	out, _ := exec.Command("systemctl", "is-enabled", vpnUnitName).Output()
	return strings.TrimSpace(string(out)) == "enabled"
}

func (b systemBoot) Set(enable bool) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("здесь нет systemd — автозапуск заводится вручную (для Alpine — см. docs/VPN.md)")
	}
	if os.Geteuid() != 0 {
		return errors.New("для автозапуска VPN нужен root — запусти программу через sudo")
	}
	if !enable {
		return systemctl("disable", vpnUnitName)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не могу определить путь к самой программе: %w", err)
	}
	if err := os.WriteFile(vpnUnitPath, []byte(vpnUnitText(exe, b.webFlags, configEnv())), 0o644); err != nil {
		return fmt.Errorf("не удалось записать %s: %w", vpnUnitPath, err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	// Только enable, без --now: адаптер и порт панели сейчас держит этот
	// процесс, вторая копия рядом не поднялась бы. Служба запустится при
	// следующей загрузке — или сразу, если закрыть программу и выполнить
	// systemctl start ssh_tunnel_vpn.
	return systemctl("enable", vpnUnitName)
}

// configEnv — откуда службе брать настройки: та же папка, что у этого
// процесса. Под sudo это XDG_CONFIG_HOME пользователя (его выставляет
// main), под root — домашняя папка root: у системной службы нет $HOME.
func configEnv() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return "XDG_CONFIG_HOME=" + x
	}
	home := os.Getenv("HOME")
	if home == "" {
		if u, err := user.Current(); err == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		home = "/root"
	}
	return "HOME=" + home
}

func vpnUnitText(exe string, flags []string, env string) string {
	cmd := quoteArg(exe)
	for _, f := range flags {
		cmd += " " + quoteArg(f)
	}
	return `# Служба ssh_tunnel VPN. Создана самой программой по галочке
# «Запускать при старте системы»; снять галочку — служба выключится.
[Unit]
Description=ssh_tunnel VPN — весь трафик через собственный сервер
Documentation=https://github.com/VITAZGIO/ssh_tunel
After=network-online.target systemd-resolved.service
Wants=network-online.target

[Service]
Type=simple
Environment=` + quoteArg(env) + `
ExecStart=` + cmd + `
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
`
}

// webFlags — с какими флагами панели запущена программа: служба поднимет её
// так же (только в домашнюю сеть или только на этой машине — как было).
func webFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "web", "web-lan":
			out = append(out, "-"+strings.TrimLeft(a, "-"))
		case "web-listen":
			out = append(out, "-"+strings.TrimLeft(a, "-"))
			if !hasValue && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		}
	}
	if len(out) == 0 {
		out = []string{"-web"}
	}
	return out
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}
