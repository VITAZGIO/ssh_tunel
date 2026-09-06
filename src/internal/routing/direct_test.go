package routing

import "testing"

func TestDirectListMatches(t *testing.T) {
	d := NewDirectList([]string{
		"100.64.0.0/10", // сеть в записи CIDR
		"25.*",          // шаблон вместо /8
		"10.8.*",        // вместо /16
		"192.168.77.*",  // вместо /24
		"203.0.113.7",   // одиночный адрес
		"nas.mydomain.com",
		".netbird.cloud", // любое имя с окончанием
		"*.corp.example", // привычная запись того же
	})

	in := []string{
		"100.104.1.5:443", "100.64.0.0", "100.127.255.255",
		"25.1.2.3:80", "10.8.9.10", "192.168.77.4:8080",
		"203.0.113.7:22",
		"nas.mydomain.com:445", "NAS.MyDomain.com",
		"box.netbird.cloud:443", "git.corp.example",
	}
	for _, target := range in {
		if !d.Match(target) {
			t.Errorf("Match(%q) = false, ожидалось true", target)
		}
	}

	out := []string{
		"100.63.255.255", "100.128.0.1", // границы CGNAT снаружи
		"26.1.2.3", "10.9.0.1", "192.168.78.4", // соседние сети
		"203.0.113.8:22",            // соседний адрес
		"nas.mydomain.com.evil.net", // не тот же хост
		"netbird.cloud.evil.net", "corp.example.evil.net",
		"youtube.com:443", "8.8.8.8",
	}
	for _, target := range out {
		if d.Match(target) {
			t.Errorf("Match(%q) = true, ожидалось false", target)
		}
	}
}

// Домены: точное имя, поддомены через ".имя" и через "*.имя" — это одна и та
// же запись, и она не должна перехватывать сам домен без отдельной строки.
func TestDirectListDomainPatterns(t *testing.T) {
	d := NewDirectList([]string{
		"vitazgio.ru",     // точное имя
		".sub.example.ru", // поддомены через точку
		"*.wild.example",  // та же запись через звёздочку
	})

	in := []string{
		"vitazgio.ru:443", "VITAZGIO.RU",
		"a.sub.example.ru", "a.b.sub.example.ru:22",
		"a.wild.example", "a.b.wild.example:443",
	}
	for _, target := range in {
		if !d.Match(target) {
			t.Errorf("Match(%q) = false, ожидалось true", target)
		}
	}

	out := []string{
		"notvitazgio.ru", "vitazgio.ru.evil.net",
		"sub.example.ru",     // сам поддоменный узел без записи как точного имени
		"evilsub.example.ru", // похожее имя без точки-разделителя не должно совпасть
		"wild.example",       // сам домен без отдельной записи
		"evilwild.example",
	}
	for _, target := range out {
		if d.Match(target) {
			t.Errorf("Match(%q) = true, ожидалось false", target)
		}
	}
}

// Пустой список не должен ничего перехватывать — иначе опечатка в настройках
// молча выпускала бы трафик мимо туннеля.
func TestDirectListEmpty(t *testing.T) {
	for _, d := range []*DirectList{nil, NewDirectList(nil), NewDirectList([]string{"", "   "})} {
		if !d.Empty() {
			t.Error("список считается непустым")
		}
		if d.Match("youtube.com:443") || d.Match("192.168.1.1") {
			t.Error("пустой список что-то перехватил")
		}
	}
}

// Список правится на ходу, как и правила по программам.
func TestDirectListSet(t *testing.T) {
	d := NewDirectList([]string{"10.8.0.0/16"})
	if !d.Match("10.8.0.1") {
		t.Fatal("исходное правило не работает")
	}
	d.Set([]string{"172.30.0.0/16"})
	if d.Match("10.8.0.1") {
		t.Error("старое правило осталось после замены списка")
	}
	if !d.Match("172.30.1.2:443") {
		t.Error("новое правило не применилось")
	}
}

func TestSplitEntries(t *testing.T) {
	got := SplitEntries(" 100.64.0.0/10, 25.*\n.netbird.cloud;10.8.*  \t")
	want := []string{"100.64.0.0/10", "25.*", ".netbird.cloud", "10.8.*"}
	if len(got) != len(want) {
		t.Fatalf("получилось %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("получилось %v, ожидалось %v", got, want)
		}
	}
}

// Адреса mesh-VPN (NetBird, Tailscale) должны считаться локальными и без
// пользовательского списка: 100.64.0.0/10 входит во встроенные правила.
func TestMeshVPNIsLocalByDefault(t *testing.T) {
	for _, h := range []string{"100.104.1.5", "100.64.0.1", "100.127.0.1",
		"host.ts.net", "box.netbird.cloud"} {
		if !IsLocalHost(h) {
			t.Errorf("IsLocalHost(%q) = false, ожидалось true", h)
		}
	}
	// Публичная половина сотой сети (там же живёт часть AWS) — не локальная.
	for _, h := range []string{"100.20.1.1", "100.63.255.255", "100.128.0.1"} {
		if IsLocalHost(h) {
			t.Errorf("IsLocalHost(%q) = true, ожидалось false", h)
		}
	}
}
