package vpnlayer

import (
	"net/netip"
	"testing"
)

func TestPickDefaultИсключаетСвойАдаптерИБеретМеньшуюМетрику(t *testing.T) {
	routes := []defaultRoute{
		{luid: 1, ifIndex: 11, metric: 50},  // Wi-Fi
		{luid: 2, ifIndex: 12, metric: 25},  // кабель
		{luid: 99, ifIndex: 13, metric: 0},  // наш адаптер
		{luid: 3, ifIndex: 0, metric: 1},    // мусорная запись без индекса
		{luid: 4, ifIndex: 14, metric: 100}, // что-то ещё
	}
	got, ok := pickDefault(routes, 99)
	if !ok || got.ifIndex != 12 {
		t.Fatalf("выбран %+v, ok=%v; ожидался кабель (индекс 12)", got, ok)
	}
	if _, ok := pickDefault([]defaultRoute{{luid: 99, ifIndex: 13}}, 99); ok {
		t.Error("кроме своего адаптера ничего нет — выбирать нечего")
	}
}

func TestPickDNSServerНеСпрашиваетСамогоСебя(t *testing.T) {
	router := netip.MustParseAddr("192.168.1.1")
	isp := netip.MustParseAddr("10.0.0.2")
	phys := []netip.Addr{router, isp}

	if got := pickDNSServer("10.0.0.2:53", phys); got != "10.0.0.2:53" {
		t.Errorf("сервер настоящей сети должен остаться: %s", got)
	}
	if got := pickDNSServer("198.18.0.53:53", phys); got != "192.168.1.1:53" {
		t.Errorf("наш адрес нельзя спрашивать, ожидался роутер: %s", got)
	}
	if got := pickDNSServer("garbage", nil); got != "1.1.1.1:53" {
		t.Errorf("без серверов — запасной: %s", got)
	}
}

func TestIsServerTarget(t *testing.T) {
	servers := []netip.Addr{netip.MustParseAddr("203.0.113.5")}
	cases := map[string]bool{
		"203.0.113.5:22":          true,
		"203.0.113.5:443":         false,
		"203.0.113.6:22":          false,
		"example.com:22":          false,
		"[::ffff:203.0.113.5]:22": true,
	}
	for target, want := range cases {
		if got := isServerTarget(target, servers, 22); got != want {
			t.Errorf("isServerTarget(%q) = %v, ожидалось %v", target, got, want)
		}
	}
}

func TestМаршрутыНеПерекрываютDNSАдаптера(t *testing.T) {
	if !adapterPrefix4.Contains(dnsAddr) {
		t.Fatal("DNS-адрес должен лежать в подсети адаптера, иначе до него не дойдёт")
	}
	if adapterPrefix4.Addr() == dnsAddr {
		t.Fatal("DNS-адрес не должен совпадать с адресом адаптера")
	}
	fake := netip.MustParsePrefix(fakeNet)
	if !adapterPrefix4.Contains(fake.Addr()) || fake.Contains(dnsAddr) {
		t.Fatal("подставные адреса — в подсети адаптера, но не на месте DNS")
	}
}
