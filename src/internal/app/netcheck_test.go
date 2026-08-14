package app

import (
	"net"
	"testing"

	"sshtunnel/internal/routing"
)

func mk(name, cidr string) foundNet {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return foundNet{Name: name, IP: ip, Mask: n.Mask}
}

func TestClassifyNetworks(t *testing.T) {
	direct := routing.NewDirectList([]string{"198.18.0.0/15"})

	checks := classify([]foundNet{
		mk("Ethernet", "192.168.3.81/24"), // офис
		mk("VMware", "192.168.113.1/24"),  // виртуалки
		mk("Hyper-V", "172.27.64.1/20"),   // виртуалки
		mk("happ-tun", "172.18.0.1/30"),   // VPN-адаптер
		mk("NetBird", "100.104.1.5/16"),   // mesh-VPN
		mk("Wi-Fi", "169.254.38.219/16"),  // без адреса
		mk("Corp", "198.18.5.7/24"),       // из списка пользователя
		mk("Weird", "25.30.40.50/8"),      // Hamachi — сломается
		mk("Public", "203.0.113.5/24"),    // публичная сеть — сломается
	}, direct)

	want := map[string]bool{ // cidr -> ok
		"192.168.3.0/24":   true,
		"192.168.113.0/24": true,
		"172.27.64.0/20":   true,
		"172.18.0.0/30":    true,
		"100.104.0.0/16":   true,
		"169.254.0.0/16":   true,
		"198.18.5.0/24":    true,
		"25.0.0.0/8":       false,
		"203.0.113.0/24":   false,
	}
	if len(checks) != len(want) {
		t.Fatalf("получено %d сетей, ожидалось %d: %+v", len(checks), len(want), checks)
	}
	for _, c := range checks {
		w, ok := want[c.CIDR]
		if !ok {
			t.Errorf("неожиданная сеть %s", c.CIDR)
			continue
		}
		if c.OK != w {
			t.Errorf("%s (%s): получено ok=%v, ожидалось %v (%s)", c.CIDR, c.Name, c.OK, w, c.Why)
		}
	}

	// Проблемные должны идти первыми — их человек и должен увидеть сразу.
	if checks[0].OK {
		t.Error("проблемные сети не подняты наверх списка")
	}
	if got := Problems(checks); got != "25.0.0.0/8, 203.0.113.0/24" && got != "203.0.113.0/24, 25.0.0.0/8" {
		t.Errorf("строка для вставки в поле неверная: %q", got)
	}
}

// Одинаковые сети на нескольких адаптерах не должны дублироваться в списке.
func TestClassifyDeduplicates(t *testing.T) {
	checks := classify([]foundNet{
		mk("Ethernet", "192.168.1.10/24"),
		mk("Wi-Fi", "192.168.1.11/24"),
	}, nil)
	if len(checks) != 1 {
		t.Fatalf("ожидалась одна сеть, получено %d: %+v", len(checks), checks)
	}
}
