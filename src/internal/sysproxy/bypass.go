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

// noProxyList — значение для NO_PROXY/no_proxy.
func noProxyList(bypassLocal bool) string {
	parts := append([]string{}, alwaysBypass...)
	if bypassLocal {
		parts = append(parts, localNets...)
		parts = append(parts, localSuffixes...)
	}
	return strings.Join(parts, ",")
}

// winProxyOverride — значение для ProxyOverride в реестре.
//
// WinINET не понимает CIDR, только шаблоны с '*', поэтому 172.16/12
// приходится расписывать по одной подсети. "<local>" означает имена без точек
// ("homeassistant") — их WinINET исключает сам.
func winProxyOverride(bypassLocal bool) string {
	parts := []string{"<local>", "localhost", "127.0.0.1", "::1", "*.local"}
	if bypassLocal {
		parts = append(parts, "*.lan", "*.home", "*.internal",
			"10.*", "192.168.*", "169.254.*")
		for i := 16; i <= 31; i++ {
			parts = append(parts, fmt.Sprintf("172.%d.*", i))
		}
	}
	return strings.Join(parts, ";")
}

// gnomeIgnoreHosts — значение org.gnome.system.proxy ignore-hosts: список
// строк в синтаксисе GVariant.
func gnomeIgnoreHosts(bypassLocal bool) string {
	parts := []string{"localhost", "127.0.0.0/8", "::1"}
	if bypassLocal {
		parts = append(parts, localNets...)
		parts = append(parts, localSuffixes...)
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = "'" + p + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
