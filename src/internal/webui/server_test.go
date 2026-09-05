package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
)

// Страница настроек шлёт только те поля, которые показаны на экране.
// Переключателей системного прокси и переменных среды там нет, и раньше они
// приходили пустыми — то есть выключались при каждом «Сохранить». Внешне всё
// выглядело исправно: туннель поднимался, каналы считались, тест скорости шёл
// через сервер, а браузер продолжал ходить напрямую.
func TestSaveKeepsFieldsMissingInRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := config.Default()
	cfg.Host = "203.0.113.10"
	if !cfg.SysProxy || !cfg.SetEnvVars {
		t.Fatal("в настройках по умолчанию системный прокси должен быть включён")
	}

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Ровно то, что шлёт страница: полей sysProxy и setEnvVars в ней нет.
	body := `{"host":"203.0.113.10","user":"tunnel","keyPath":"/k","sshPort":22,
	          "socksPort":1080,"httpPort":1081,"poolSize":4,
	          "verbose":false,"autoStart":false,"localViaTunnel":false,
	          "directHosts":[],"filterMode":"all","filterApps":[]}`

	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body)))

	var resp struct {
		Config config.Config `json:"config"`
		Error  string        `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не разобрался: %v (%s)", err, rec.Body.String())
	}
	if resp.Error != "" {
		t.Fatalf("сохранение вернуло ошибку: %s", resp.Error)
	}

	if !resp.Config.SysProxy {
		t.Error("системный прокси выключился при сохранении — трафик пойдёт мимо туннеля")
	}
	if !resp.Config.SetEnvVars {
		t.Error("переменные среды выключились при сохранении — Claude Code, npm и curl пойдут мимо")
	}
	// И то, что страница действительно прислала, должно примениться.
	if resp.Config.User != "tunnel" {
		t.Errorf("пользователь не сохранился: %q", resp.Config.User)
	}
}

// В открытом режиме панель пускает без ключа, но только из локальной сети и
// только если запрос не прилетел со стороннего сайта.
func TestOpenLocalAccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := config.Default()
	cfg.Host = "203.0.113.10"

	closed, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Close()

	open, err := NewOpenLocalOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()

	cases := []struct {
		name   string
		srv    *Server
		remote string
		origin string
		want   bool
	}{
		{"локальная сеть без ключа", open, "192.168.1.50:5000", "", true},
		{"сама машина без ключа", open, "127.0.0.1:5000", "", true},
		{"свой Origin", open, "192.168.1.50:5000", "http://192.168.1.203:47821", true},
		{"чужой сайт во вкладке", open, "192.168.1.50:5000", "http://evil.example", false},
		{"публичный адрес без ключа", open, "203.0.113.7:5000", "", false},
		{"обычный режим без ключа", closed, "127.0.0.1:5000", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			r.Host = "192.168.1.203:47821"
			r.RemoteAddr = c.remote
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if got := c.srv.allowed(r); got != c.want {
				t.Errorf("доступ = %v, ожидалось %v", got, c.want)
			}
		})
	}
}

// Ключ работает всегда — и в обычном режиме, и с публичного адреса.
func TestTokenAlwaysWorks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := config.Default()
	cfg.Host = "203.0.113.10"
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	r := httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil)
	r.RemoteAddr = "203.0.113.7:5000"
	if !s.allowed(r) {
		t.Error("запрос с правильным ключом не прошёл")
	}
}
