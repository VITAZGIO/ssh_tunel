//go:build linux

package webui

import (
	"strings"
	"testing"
)

// В файл службы должны попасть ровно те флаги, с которыми панель работает
// сейчас. Ошибка здесь тихая и обидная: после перезагрузки программа
// поднимется в другом режиме — например, без -web-lan, — и панель просто
// перестанет открываться по адресу машины.
func TestUnitTextKeepsFlags(t *testing.T) {
	got := unitText("/usr/local/bin/ssh_tunnel_linux",
		[]string{"-web", "-web-lan", "-web-listen", "0.0.0.0:47821"})

	want := "ExecStart=/usr/local/bin/ssh_tunnel_linux -web -web-lan -web-listen 0.0.0.0:47821"
	if !strings.Contains(got, want) {
		t.Errorf("нет строки запуска %q в:\n%s", want, got)
	}
	if !strings.Contains(got, "WantedBy=default.target") {
		t.Error("без WantedBy служба не включится в автозапуск")
	}
}

func TestUnitTextQuotesPathWithSpaces(t *testing.T) {
	got := unitText("/home/vitaz/my programs/ssh_tunnel_linux", []string{"-web"})
	want := `ExecStart="/home/vitaz/my programs/ssh_tunnel_linux" -web`
	if !strings.Contains(got, want) {
		t.Errorf("путь с пробелом не взят в кавычки:\n%s", got)
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"Failed to connect to bus\nи ещё строка": "Failed to connect to bus",
		"  одна строка  ":                        "одна строка",
		"":                                       "",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}
