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
