package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sshtunnel/internal/app"
)

// Экспорт — импорт целиком: файл, который получает друг, должен собрать ему
// рабочий сервер с тем же ключом, без ручной правки config.json.
func TestExportImportRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)

	keyPath := filepath.Join(tmp, "id_test")
	keyBody := "-----BEGIN OPENSSH PRIVATE KEY-----\nfakekeyfakekey\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(keyBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := withHost("203.0.113.10")
	p := cfg.Active()
	p.KeyPath = keyPath
	p.Name = "Амстердам"
	p.Flag = "NL"
	cfg.SetProfile(p)

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Экспорт.
	rec := httptest.NewRecorder()
	body := `{"id":"` + p.ID + `","includeKey":true}`
	s.handleProfileExport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/export", strings.NewReader(body)))
	var expResp struct {
		OK       bool   `json:"ok"`
		Filename string `json:"filename"`
		Data     string `json:"data"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &expResp); err != nil || expResp.Error != "" {
		t.Fatalf("экспорт не удался: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(expResp.Data, "fakekeyfakekey") {
		t.Error("ключ не попал в экспорт")
	}
	if expResp.Filename != "ssh_tunnel-server.json" {
		// Кириллица в имени схлопывается в дефисы — так и задумано, но
		// проверим хотя бы расширение и префикс.
		if !strings.HasPrefix(expResp.Filename, "ssh_tunnel-") || !strings.HasSuffix(expResp.Filename, ".json") {
			t.Errorf("странное имя файла: %q", expResp.Filename)
		}
	}

	// Импорт в "чистую" вторую программу — как будто у друга.
	tmp2 := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp2)
	t.Setenv("APPDATA", tmp2)
	cfg2 := withHost("") // пустой профиль по умолчанию
	s2, err := NewOn(app.New(cfg2), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	rec = httptest.NewRecorder()
	s2.handleProfileImport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/import", strings.NewReader(expResp.Data)))
	var impResp struct {
		OK          bool   `json:"ok"`
		KeyImported bool   `json:"keyImported"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &impResp); err != nil || impResp.Error != "" {
		t.Fatalf("импорт не удался: %v (%s)", err, rec.Body.String())
	}
	if !impResp.KeyImported {
		t.Error("ключ не импортировался")
	}

	newCfg := s2.app.Config()
	if len(newCfg.Profiles) != 2 { // тот, что был по умолчанию, плюс импортированный
		t.Fatalf("ожидалось 2 профиля после импорта, получилось %d", len(newCfg.Profiles))
	}
	active := newCfg.Active()
	if active.Host != "203.0.113.10" || active.Name != "Амстердам" || active.Flag != "NL" {
		t.Errorf("импортированный сервер не стал активным как надо: %+v", active)
	}
	if newCfg.ActiveProfile != active.ID {
		t.Error("импорт должен сделать новый сервер активным")
	}

	imported, err := os.ReadFile(active.KeyPath)
	if err != nil {
		t.Fatalf("не могу прочитать импортированный ключ: %v", err)
	}
	if string(imported) != keyBody {
		t.Errorf("содержимое ключа исказилось при импорте:\n%q\nхотели:\n%q", imported, keyBody)
	}
}

// Конфиг, выданный панелью на VPS (ТЗ-10), несёт поля версии 2 — они должны
// дойти до профиля при импорте и вернуться назад при повторном экспорте
// (ТЗ-12: по ним экран решает, показывать ли «Этот сервер выдан панелью»).
func TestImportCarriesPanelFields(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)
	cfg := withHost("")
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	doc := `{"sshTunnelExport":2,"name":"Ноутбук","host":"203.0.113.10","sshPort":22,
		"user":"tun_0123456789abcdef","socksPort":1080,"httpPort":1081,"poolSize":4,
		"filterMode":"all","keyIncluded":false,
		"panel":"https://panel.example.com/","clientId":"tun_0123456789abcdef",
		"deviceName":"Ноутбук"}`

	rec := httptest.NewRecorder()
	s.handleProfileImport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/import", strings.NewReader(doc)))
	var impResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &impResp); err != nil || impResp.Error != "" {
		t.Fatalf("импорт не удался: %v (%s)", err, rec.Body.String())
	}

	active := s.app.Config().Active()
	if active.Panel != "https://panel.example.com/" {
		t.Errorf("Panel не сохранился при импорте: %q", active.Panel)
	}
	if active.ClientID != "tun_0123456789abcdef" {
		t.Errorf("ClientID не сохранился при импорте: %q", active.ClientID)
	}
	if active.DeviceName != "Ноутбук" {
		t.Errorf("DeviceName не сохранился при импорте: %q", active.DeviceName)
	}

	// Повторный экспорт того же профиля не должен потерять эти поля.
	rec = httptest.NewRecorder()
	body := `{"id":"` + active.ID + `","includeKey":false}`
	s.handleProfileExport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/export", strings.NewReader(body)))
	var expResp struct {
		Data  string `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &expResp); err != nil || expResp.Error != "" {
		t.Fatalf("экспорт не удался: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(expResp.Data, `"panel": "https://panel.example.com/"`) {
		t.Errorf("повторный экспорт должен сохранить поле panel: %s", expResp.Data)
	}
}

// Сервер, настроенный руками (или конфиг версии 1), не несёт полей панели —
// импорт не должен на этом падать, а профиль остаётся с пустыми полями:
// строка «выдано панелью» на экране в этом случае просто не появляется.
func TestImportWithoutPanelFieldsLeavesThemEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)
	cfg := withHost("")
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	doc := `{"sshTunnelExport":1,"name":"Свой сервер","host":"203.0.113.10","sshPort":22,
		"user":"tunnel","socksPort":1080,"httpPort":1081,"poolSize":4,"filterMode":"all",
		"keyIncluded":false}`

	rec := httptest.NewRecorder()
	s.handleProfileImport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/import", strings.NewReader(doc)))
	var impResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &impResp); err != nil || impResp.Error != "" {
		t.Fatalf("импорт не удался: %v (%s)", err, rec.Body.String())
	}

	active := s.app.Config().Active()
	if active.Panel != "" || active.ClientID != "" || active.DeviceName != "" {
		t.Errorf("конфиг без полей панели не должен их придумывать: %+v", active)
	}
}

// Файл без ключа — тоже валидный сценарий: например, у друга уже есть свой
// ключ на сервере, и делиться нужно только адресом и настройками фильтра.
func TestImportWithoutKey(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)

	cfg := withHost("203.0.113.10")
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	body := `{"sshTunnelExport":1,"name":"Без ключа","host":"198.51.100.9","sshPort":22,
	          "user":"tunnel","socksPort":1080,"httpPort":1081,"poolSize":4,"filterMode":"all",
	          "keyIncluded":false}`
	rec := httptest.NewRecorder()
	s.handleProfileImport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/import", strings.NewReader(body)))
	var resp struct {
		OK          bool   `json:"ok"`
		KeyImported bool   `json:"keyImported"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error != "" {
		t.Fatalf("импорт без ключа не удался: %v (%s)", err, rec.Body.String())
	}
	if resp.KeyImported {
		t.Error("ключа в файле не было — keyImported должен быть false")
	}
	active := s.app.Config().Active()
	if active.Host != "198.51.100.9" {
		t.Errorf("адрес не импортировался: %q", active.Host)
	}
	// Путь к ключу должен остаться тем, что подставил AddProfile по
	// умолчанию (DetectKeyPath), а не потеряться и не стать пустым.
	if active.KeyPath == "" {
		t.Error("путь к ключу не должен быть пустым даже без импорта самого ключа")
	}
}

func TestImportRejectsGarbage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)

	cfg := withHost("203.0.113.10")
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := httptest.NewRecorder()
	s.handleProfileImport(rec, httptest.NewRequest(http.MethodPost, "/api/profile/import", strings.NewReader(`{"foo":"bar"}`)))
	var resp struct {
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Error("файл без адреса сервера должен быть отклонён с понятной ошибкой")
	}
}
