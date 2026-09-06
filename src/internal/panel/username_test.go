package panel

import "testing"

func TestValidUsername(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"tun_0123456789abcdef", true},
		{"tun_0123456789ABCDEF", false}, // не hex в нижнем регистре
		{"tun_short", false},
		{"tun_0123456789abcdef0", false}, // на символ длиннее
		{"tunnel", false},
		{"", false},
		{"../../etc/passwd", false},
		{"tun_0123456789abcdef; rm -rf /", false},
		{"tun_0123456789abcdef\n", false},
	}
	for _, c := range cases {
		if got := ValidUsername(c.name); got != c.ok {
			t.Errorf("ValidUsername(%q) = %v, хочу %v", c.name, got, c.ok)
		}
	}
}

func TestGenerateUsernameIsValidAndUnique(t *testing.T) {
	a, err := GenerateUsername()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidUsername(a) {
		t.Fatalf("сгенерированное имя не проходит собственную проверку: %q", a)
	}
	b, err := GenerateUsername()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("два вызова подряд не должны совпасть")
	}
}

func TestClientIDMatchesUsername(t *testing.T) {
	u, err := GenerateUsername()
	if err != nil {
		t.Fatal(err)
	}
	if ClientID(u) != u {
		t.Fatalf("ClientID должен совпадать с именем пользователя: %q != %q", ClientID(u), u)
	}
}
