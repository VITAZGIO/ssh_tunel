package app

import (
	"net"
	"sort"
	"strings"

	"sshtunnel/internal/routing"
)

// Проверка сетей — ответ на вопрос «что сломается, если включить туннель».
//
// Спрашивать это у человека («какие у вас там сети?») бессмысленно: адреса
// офисной сети и корпоративного VPN он обычно не знает. Зато их знает его же
// компьютер — достаточно перечислить сетевые адаптеры и сверить каждую сеть с
// правилами, по которым программа выпускает трафик напрямую.

// NetCheck — одна найденная сеть и вердикт по ней.
type NetCheck struct {
	Name string `json:"name"` // имя адаптера, как его называет система
	CIDR string `json:"cidr"` // сеть целиком, а не адрес компьютера
	OK   bool   `json:"ok"`   // true — трафик туда пойдёт мимо туннеля
	Why  string `json:"why"`  // объяснение человеку
}

// ScanNetworks смотрит адаптеры этого компьютера и говорит по каждой сети,
// останется ли она доступной при включённом туннеле.
func (a *App) ScanNetworks() []NetCheck {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var found []foundNet
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			n, ok := addr.(*net.IPNet)
			if !ok || n.IP == nil {
				continue
			}
			found = append(found, foundNet{Name: ifc.Name, IP: n.IP, Mask: n.Mask})
		}
	}
	return classify(found, a.direct)
}

type foundNet struct {
	Name string
	IP   net.IP
	Mask net.IPMask
}

// classify отделён от перечисления адаптеров, чтобы его можно было проверить
// тестом на выдуманных сетях: настоящие адаптеры на машине с тестами не те,
// что у пользователя.
func classify(nets []foundNet, direct *routing.DirectList) []NetCheck {
	seen := make(map[string]bool)
	out := make([]NetCheck, 0, len(nets))

	for _, n := range nets {
		ipnet := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
		cidr := ipnet.String()
		if cidr == "<nil>" || seen[cidr] {
			continue
		}
		seen[cidr] = true

		c := NetCheck{Name: n.Name, CIDR: cidr}
		switch {
		case n.IP.IsLinkLocalUnicast():
			// 169.254.x и fe80:: — адаптеры без настроенного адреса. Сетью в
			// человеческом смысле это не является, но и не мешает.
			c.OK, c.Why = true, "адаптер без адреса, не мешает"
		case routing.IsLocalHost(n.IP.String()):
			c.OK, c.Why = true, "локальная сеть, идёт мимо туннеля"
		case direct.Match(n.IP.String()):
			c.OK, c.Why = true, "в списке «всегда напрямую»"
		default:
			c.OK, c.Why = false, "пойдёт через сервер — если это рабочая сеть, добавь её"
		}
		out = append(out, c)
	}

	// Сначала проблемные, дальше по имени: человек должен увидеть главное
	// в первой строке, а не искать его глазами.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return !out[i].OK
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Problems — сети из проверки, которые стоит добавить в список «всегда
// напрямую», одной строкой, готовой для вставки в поле.
func Problems(checks []NetCheck) string {
	var bad []string
	for _, c := range checks {
		if !c.OK {
			bad = append(bad, c.CIDR)
		}
	}
	return strings.Join(bad, ", ")
}
