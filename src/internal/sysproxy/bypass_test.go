package sysproxy

import (
	"strings"
	"testing"
)

// В ProxyServer идёт один адрес на все протоколы.
//
// Это не вкусовщина, а починка живой поломки: с разбивкой вида
// "http=...;https=...;socks=..." браузеры уводили часть соединений на запись
// "socks=", трактовали её как SOCKS4 и разрешали имя хоста сами, на этой
// машине. Веб-версия телеграма не открывалась при полностью рабочем туннеле, а
// в журнале программы такого соединения не было вовсе.
func TestWinProxyServerHasNoPerSchemeParts(t *testing.T) {
	got := winProxyServer("127.0.0.1:1081")

	if got != "127.0.0.1:1081" {
		t.Fatalf("ProxyServer = %q, а должен быть просто адрес", got)
	}
	if strings.ContainsAny(got, "=;") {
		t.Fatalf("ProxyServer = %q — разбивка по протоколам вернулась", got)
	}
}

// Локальные адреса обязаны идти мимо прокси, иначе перестают открываться
// домашние сервисы и наше собственное окно настроек.
func TestWinProxyOverrideKeepsLocalOut(t *testing.T) {
	got := winProxyOverride(true)

	for _, want := range []string{"<local>", "127.0.0.1", "192.168.*", "172.31.*"} {
		if !strings.Contains(got, want) {
			t.Errorf("в списке обхода нет %q: %s", want, got)
		}
	}
}

// Сети в записи CIDR WinINET не понимает — их отбрасываем, иначе весь список
// обхода он может счесть испорченным. Ядро такие сети учитывает само.
func TestWinExtraDropsCIDR(t *testing.T) {
	got := winExtra([]string{"10.8.0.0/16", "corp.example", ".corp.local", "  "})

	want := []string{"corp.example", "*.corp.local"}
	if len(got) != len(want) {
		t.Fatalf("получилось %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("получилось %v, ожидалось %v", got, want)
		}
	}
}
