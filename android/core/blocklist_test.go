package core

import (
	"strings"
	"testing"
)

// Разбор списка: формат hosts (первое поле — IP) и "одно имя в строке"
// перемешаны в одном источнике, как это бывает при склейке нескольких
// списков в один файл, — оба должны разобраться.
func TestParseBlockListTextФорматыHostsИОдноИмяВСтроке(t *testing.T) {
	text := `# комментарий целиком
0.0.0.0 ads.example.com
127.0.0.1 tracker.example.net another.example.net # инлайн-комментарий

   plain.example.org
`
	got := ParseBlockListText(strings.NewReader(text))
	want := []string{
		"ads.example.com",
		"tracker.example.net", "another.example.net",
		"plain.example.org",
	}
	if len(got) != len(want) {
		t.Fatalf("получилось %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("получилось %v, ожидалось %v", got, want)
		}
	}
}

func TestBlockListMatchТочноеИмяИПоддомены(t *testing.T) {
	b := NewBlockList([]string{"ads.example.com", "tracker.net"}, nil)

	blocked := []string{"ads.example.com", "ADS.EXAMPLE.COM", "x.ads.example.com", "tracker.net", "a.b.tracker.net"}
	for _, name := range blocked {
		if !b.Match(name) {
			t.Errorf("Match(%q) = false, ожидалось true", name)
		}
	}

	notBlocked := []string{"example.com", "notads.example.com", "example.net", "tracker.net.evil.com"}
	for _, name := range notBlocked {
		if b.Match(name) {
			t.Errorf("Match(%q) = true, ожидалось false", name)
		}
	}
}

// Исключение перекрывает блокировку на любом уровне вложенности, даже когда
// в блок-листе есть более конкретная запись.
func TestBlockListИсключениеПеребиваетБлокировку(t *testing.T) {
	b := NewBlockList(
		[]string{"ads.example.com"},
		[]string{"example.com"},
	)
	if b.Match("ads.example.com") {
		t.Error("исключение не сработало для поддомена")
	}
	if b.Match("example.com") {
		t.Error("исключение не сработало для самого домена")
	}
}

func TestBlockListEmptyИNil(t *testing.T) {
	var nilList *BlockList
	if !nilList.Empty() {
		t.Error("nil-список считается непустым")
	}
	if nilList.Match("ads.example.com") {
		t.Error("nil-список что-то заблокировал")
	}

	empty := NewBlockList(nil, nil)
	if !empty.Empty() {
		t.Error("пустой список считается непустым")
	}
}

// Set полностью заменяет содержимое — старые записи не должны просачиваться.
func TestBlockListSetЗаменяетСписокЦеликом(t *testing.T) {
	b := NewBlockList([]string{"old.example.com"}, nil)
	if !b.Match("old.example.com") {
		t.Fatal("исходная запись не сработала")
	}
	b.Set([]string{"new.example.com"}, nil)
	if b.Match("old.example.com") {
		t.Error("старая запись осталась после Set")
	}
	if !b.Match("new.example.com") {
		t.Error("новая запись не применилась")
	}
}
