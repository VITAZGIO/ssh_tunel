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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

//go:embed vpsassets/*.tmpl vpsassets/udprelay_server.go.txt
var vpsScripts embed.FS

var (
	hardenTmpl     = template.Must(template.ParseFS(vpsScripts, "vpsassets/harden.sh.tmpl"))
	tunnelUserTmpl = template.Must(template.ParseFS(vpsScripts, "vpsassets/tunnel-user.sh.tmpl"))
)

// udpRelayServerSource — исходник ретранслятора UDP (sshtunnel/cmd/udprelay),
// один в один: этот файл собирается прямо на сервере, а не пересылается
// готовым бинарником, поэтому нужен именно исходный код. Копия, а не прямой
// go:embed из cmd/udprelay — go:embed не умеет "../.." в пути; синхронность
// с оригиналом проверяет TestUDPRelayServerSourceMatchesCmd в vpssetup_test.go.
func udpRelayServerSource() (string, error) {
	data, err := vpsScripts.ReadFile("vpsassets/udprelay_server.go.txt")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

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

// ---------------------------------------------------------------------------
// Ручной путь: те же самые скрипты, но одним текстом для копирования в
// терминал. Нужен, когда вход root по паролю на сервере запрещён вовсе и
// мастер настройки подключиться не может. Важно, что текст собирается из тех
// же шаблонов vpsassets/*.tmpl, что и автоматический путь: разойтись они не
// могут по построению.

// usernameRe — обычные правила для имени пользователя в Linux: только то, что
// само по себе не может разорвать двойные кавычки в "что настроить" скрипта.
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// timezoneRe — имя зоны IANA (Europe/Amsterdam, Etc/UTC): буквы, цифры,
// слэш, подчёркивание, дефис и плюс.
var timezoneRe = regexp.MustCompile(`^[A-Za-z0-9_/+-]{0,64}$`)

// vpsScript собирает готовое сообщение для вставки на свежем сервере: оба
// шага подряд, с уже подставленными значениями полей.
func vpsScript(newUser string, sshPort int, timezone, tunnelUser, tunnelKey string) (string, error) {
	harden, err := renderHarden(hardenParams{NewUser: newUser, SSHPort: sshPort, Timezone: timezone})
	if err != nil {
		return "", err
	}
	tunnel, err := renderTunnelUser(tunnelUserParams{
		TunnelUser: tunnelUser, TunnelKey: tunnelKey, BlockInternals: 1,
	})
	if err != nil {
		return "", err
	}
	return "cat > /root/harden.sh <<'ENDOFSCRIPT'\n" + harden + "ENDOFSCRIPT\nbash /root/harden.sh\n\n" +
		"cat > /root/tunnel-user.sh <<'ENDOFSCRIPT'\n" + tunnel + "ENDOFSCRIPT\nbash /root/tunnel-user.sh\n", nil
}

func (s *Server) handleVPSScript(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewUser    string `json:"newUser"`
		SSHPort    string `json:"sshPort"`
		Timezone   string `json:"timezone"`
		TunnelUser string `json:"tunnelUser"`
		TunnelKey  string `json:"tunnelKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}

	newUser := strings.TrimSpace(req.NewUser)
	if newUser == "" {
		newUser = "admin"
	}
	if !usernameRe.MatchString(newUser) {
		writeJSON(w, map[string]string{"error": "имя пользователя: только латиница, цифры, «-» и «_», с буквы"})
		return
	}

	tunnelUser := strings.TrimSpace(req.TunnelUser)
	if tunnelUser == "" {
		tunnelUser = "tunnel"
	}
	if !usernameRe.MatchString(tunnelUser) {
		writeJSON(w, map[string]string{"error": "имя пользователя туннеля: только латиница, цифры, «-» и «_», с буквы"})
		return
	}

	port := strings.TrimSpace(req.SSHPort)
	if port == "" {
		port = "22"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		writeJSON(w, map[string]string{"error": "SSH-порт: число от 1 до 65535"})
		return
	}

	tz := strings.TrimSpace(req.Timezone)
	if !timezoneRe.MatchString(tz) {
		writeJSON(w, map[string]string{"error": "часовой пояс: буквы, цифры, «/», «_», «-» (например Europe/Amsterdam)"})
		return
	}

	key := strings.TrimSpace(req.TunnelKey)
	if strings.ContainsAny(key, "\"`\n") {
		writeJSON(w, map[string]string{"error": "открытый ключ содержит недопустимые символы"})
		return
	}

	script, err := vpsScript(newUser, n, tz, tunnelUser, key)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "script": script})
}
