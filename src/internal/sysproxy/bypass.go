package sysproxy

import (
	"fmt"
	"strings"
)

// Списки исключений: куда система НЕ должна ходить через прокси.
//
// Помимо петлевого адреса сюда попадает вся локальная сеть. Без этого
// обращение к 192.168.1.50 уходило бы в туннель, и сервер искал бы этот адрес
// у себя — домашние сервисы (роутер, NAS, Home Assistant) переставали бы
// открываться при включённом туннеле.
//
// Ядро принимает такое же решение самостоятельно (см. routing.IsLocalHost),
// поэтому списки — не единственная защита, а способ не гонять запрос до
// прокси вообще.

// alwaysBypass — то, что мимо прокси всегда: свой же компьютер и mDNS-имена,
// которые за пределами своей сети не значат ничего.
var alwaysBypass = []string{"localhost", "127.0.0.1", "::1"}

// localNets — приватные диапазоны из RFC 1918 плюс link-local.
var localNets = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
}

// localSuffixes — доменные суффиксы домашних и внутренних сетей.
var localSuffixes = []string{".local", ".lan", ".home", ".internal"}

// cgnatNet — 100.64.0.0/10: адреса mesh-VPN (NetBird, Tailscale). В NO_PROXY
// запись CIDR понимают и Go, и curl, поэтому диапазон указывается как есть.
// В ProxyOverride его нет: WinINET понимает только шаблоны, и пришлось бы
// перечислять 64 подсети — ядро всё равно решает этот случай само.
const cgnatNet = "100.64.0.0/10"

// noProxyList — значение для NO_PROXY/no_proxy.
func noProxyList(bypassLocal bool, extra ...string) string {
	parts := append([]string{}, alwaysBypass...)
	if bypassLocal {
		parts = append(parts, localNets...)
		parts = append(parts, cgnatNet)
		parts = append(parts, localSuffixes...)
	}
	parts = append(parts, extra...)
	return strings.Join(parts, ",")
}

// winExtra оставляет из пользовательского списка то, что WinINET способен
// понять: сети в записи CIDR он не поддерживает, а ядро их и так учитывает.
func winExtra(extra []string) []string {
	out := make([]string, 0, len(extra))
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e == "" || strings.Contains(e, "/") {
			continue
		}
		if strings.HasPrefix(e, ".") {
			e = "*" + e // ".corp.local" -> "*.corp.local"
		}
		out = append(out, e)
	}
	return out
}

// winProxyOverride — значение для ProxyOverride в реестре.
//
// WinINET не понимает CIDR, только шаблоны с '*', поэтому 172.16/12
// приходится расписывать по одной подсети. "<local>" означает имена без точек
// ("homeassistant") — их WinINET исключает сам.
func winProxyOverride(bypassLocal bool, extra ...string) string {
	parts := []string{"<local>", "localhost", "127.0.0.1", "::1", "*.local"}
	if bypassLocal {
		parts = append(parts, "*.lan", "*.home", "*.internal",
			"10.*", "192.168.*", "169.254.*")
		for i := 16; i <= 31; i++ {
			parts = append(parts, fmt.Sprintf("172.%d.*", i))
		}
	}
	parts = append(parts, winExtra(extra)...)
	return strings.Join(parts, ";")
}

// gnomeIgnoreHosts — значение org.gnome.system.proxy ignore-hosts: список
// строк в синтаксисе GVariant.
func gnomeIgnoreHosts(bypassLocal bool, extra ...string) string {
	parts := []string{"localhost", "127.0.0.0/8", "::1"}
	if bypassLocal {
		parts = append(parts, localNets...)
		parts = append(parts, cgnatNet)
		parts = append(parts, localSuffixes...)
	}
	parts = append(parts, extra...)
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = "'" + p + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
