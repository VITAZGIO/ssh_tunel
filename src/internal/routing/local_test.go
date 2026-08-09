package routing

import "testing"

func TestIsLocalHost(t *testing.T) {
	local := []string{
		"192.168.1.50", "192.168.0.1", "10.0.0.5", "10.255.255.254",
		"172.16.0.1", "172.20.10.3", "172.31.255.255",
		"127.0.0.1", "127.0.0.53", "169.254.1.1",
		"::1", "[::1]", "fd00::1", "fe80::1",
		"homeassistant", "nas", "router",
		"homeassistant.local", "HomeAssistant.LOCAL", "nas.local.",
		"printer.lan", "pi.home", "git.internal", "box.localdomain",
		"ha.home.arpa",
	}
	for _, h := range local {
		if !IsLocalHost(h) {
			t.Errorf("IsLocalHost(%q) = false, ожидалось true", h)
		}
	}

	remote := []string{
		"youtube.com", "www.google.com", "api.anthropic.com",
		"8.8.8.8", "1.1.1.1", "172.32.0.1", "172.15.0.1",
		"11.0.0.1", "192.169.0.1", "2606:4700::1111",
		"example.localhost.evil.com", "", "   ",
	}
	for _, h := range remote {
		if IsLocalHost(h) {
			t.Errorf("IsLocalHost(%q) = true, ожидалось false", h)
		}
	}
}

func TestIsLocalTarget(t *testing.T) {
	cases := map[string]bool{
		"192.168.1.50:8123":  true,
		"homeassistant:8123": true,
		"nas.local:445":      true,
		"[::1]:8080":         true,
		"youtube.com:443":    false,
		"142.250.185.78:443": false,
		"192.168.1.50":       true, // без порта тоже разбираем
		"youtube.com":        false,
	}
	for target, want := range cases {
		if got := IsLocalTarget(target); got != want {
			t.Errorf("IsLocalTarget(%q) = %v, ожидалось %v", target, got, want)
		}
	}
}
