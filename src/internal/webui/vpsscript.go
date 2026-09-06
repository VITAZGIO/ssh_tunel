// Скрипты первой настройки сервера — те же самые, что в docs/SERVER_SETUP.md
// (шаг 2 — harden.sh, шаг 3 — tunnel-user.sh), только не для ручной вставки в
// терминал, а для автоматического запуска мастером настройки VPS. Тела
// скриптов размечены как text/template с теми же тремя настраиваемыми
// параметрами, что в документе (NEW_USER/SSH_PORT/TIMEZONE и
// TUNNEL_USER/TUNNEL_KEY/BLOCK_INTERNALS) — остальной текст, порядок шагов и
// все проверки не менялись ни на строку.
package webui

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed vpsassets/*.tmpl
var vpsScripts embed.FS

var (
	hardenTmpl     = template.Must(template.ParseFS(vpsScripts, "vpsassets/harden.sh.tmpl"))
	tunnelUserTmpl = template.Must(template.ParseFS(vpsScripts, "vpsassets/tunnel-user.sh.tmpl"))
)

// hardenParams — параметры шага 2 (docs/SERVER_SETUP.md, "Шаг 2. Один
// скрипт"). NewUser и Timezone мастер не спрашивает — берутся значения по
// умолчанию из документа: пользователь "admin", часовой пояс не трогается.
// SSHPort — тот же порт, на который мастер уже подключился: сервер продолжит
// слушать его же, переподключаться на другой порт не придётся.
type hardenParams struct {
	NewUser  string
	SSHPort  int
	Timezone string
}

func renderHarden(p hardenParams) (string, error) {
	var buf bytes.Buffer
	if err := hardenTmpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("harden.sh: %w", err)
	}
	return buf.String(), nil
}

// tunnelUserParams — параметры шага 3 (docs/SERVER_SETUP.md, "Шаг 3.
// Отдельный пользователь только для туннеля"). TunnelKey оставляем пустым —
// скрипт сам возьмёт ключи из /root/.ssh/authorized_keys, куда мастер кладёт
// открытый ключ этого компьютера ещё до запуска harden.sh.
type tunnelUserParams struct {
	TunnelUser     string
	TunnelKey      string
	BlockInternals int
}

func renderTunnelUser(p tunnelUserParams) (string, error) {
	var buf bytes.Buffer
	if err := tunnelUserTmpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("tunnel-user.sh: %w", err)
	}
	return buf.String(), nil
}
