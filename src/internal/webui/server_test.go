package webui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/tunnel"
)

// withHost — тестовый помощник: конфиг по умолчанию с адресом сервера у
// активного профиля.
func withHost(host string) config.Config {
	cfg := config.Default()
	p := cfg.Active()
	p.Host = host
	cfg.SetProfile(p)
	return cfg
}

// Страница настроек шлёт полный документ целиком (весь список серверов и
// общие настройки), а не один плоский набор полей, как раньше. Но верхнего
// уровня это по-прежнему касается: переключателей системного прокси и
// переменных среды в форме нет, и раньше отсутствие ключа в JSON молча их
// выключало. Внешне всё выглядело исправно: туннель поднимался, каналы
// считались, тест скорости шёл через сервер, а браузер продолжал ходить
// напрямую.
func TestSaveKeepsFieldsMissingInRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := withHost("203.0.113.10")
	if !cfg.SysProxy || !cfg.SetEnvVars {
		t.Fatal("в настройках по умолчанию системный прокси должен быть включён")
	}
	active := cfg.Active()

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Профиль шлётся целиком — иначе непереданные поля (JSON не мержит
	// вложенные объекты внутри массива) обнулились бы. А вот sysProxy и
	// setEnvVars — самого верхнего уровня, их в запросе намеренно нет.
	body := fmt.Sprintf(`{"profiles":[{"id":%q,"name":"Сервер 1","host":"203.0.113.10",
	          "user":"tunnel","keyPath":"/k","sshPort":22,
	          "socksPort":1080,"httpPort":1081,"poolSize":4,
	          "localViaTunnel":false,"directHosts":[],"filterMode":"all","filterApps":[]}],
	          "activeProfile":%q,"verbose":false,"autoStart":false}`, active.ID, active.ID)

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
	if resp.Config.Active().User != "tunnel" {
		t.Errorf("пользователь не сохранился: %q", resp.Config.Active().User)
	}
}

// В открытом режиме панель пускает без ключа, но только из локальной сети и
// только если запрос не прилетел со стороннего сайта.
func TestOpenLocalAccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := withHost("203.0.113.10")

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

	cfg := withHost("203.0.113.10")
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

// Добавление, переключение и удаление серверов через API — то же самое, что
// делают кнопки «+», «Выбрать этот сервер» и «Удалить» в панели.
func TestProfileEndpoints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := withHost("203.0.113.10")
	firstID := cfg.Active().ID
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Добавить.
	rec := httptest.NewRecorder()
	s.handleProfileAdd(rec, httptest.NewRequest(http.MethodPost, "/api/profile/add",
		strings.NewReader(`{"name":"Амстердам","flag":"🇳🇱"}`)))
	var addResp struct {
		OK      bool           `json:"ok"`
		Profile config.Profile `json:"profile"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &addResp); err != nil || addResp.Error != "" {
		t.Fatalf("не удалось добавить сервер: %v (%s)", err, rec.Body.String())
	}
	if addResp.Profile.Name != "Амстердам" || addResp.Profile.Flag != "🇳🇱" {
		t.Errorf("новый сервер собрался неправильно: %+v", addResp.Profile)
	}
	if s.app.Config().ActiveProfile != firstID {
		t.Error("добавление сервера не должно само переключать активный")
	}

	// Выбрать.
	rec = httptest.NewRecorder()
	body := fmt.Sprintf(`{"id":%q}`, addResp.Profile.ID)
	s.handleProfileSelect(rec, httptest.NewRequest(http.MethodPost, "/api/profile/select", strings.NewReader(body)))
	if s.app.Config().ActiveProfile != addResp.Profile.ID {
		t.Errorf("выбор сервера не сработал: %s", rec.Body.String())
	}

	// Удалить активный — активным должен снова стать первый.
	rec = httptest.NewRecorder()
	s.handleProfileRemove(rec, httptest.NewRequest(http.MethodPost, "/api/profile/remove", strings.NewReader(body)))
	if s.app.Config().ActiveProfile != firstID {
		t.Errorf("после удаления активного сервера активным должен стать оставшийся: %s", rec.Body.String())
	}
	if len(s.app.Config().Profiles) != 1 {
		t.Errorf("ожидался один оставшийся сервер, получилось %d", len(s.app.Config().Profiles))
	}

	// Последний сервер не удаляется.
	rec = httptest.NewRecorder()
	body = fmt.Sprintf(`{"id":%q}`, firstID)
	s.handleProfileRemove(rec, httptest.NewRequest(http.MethodPost, "/api/profile/remove", strings.NewReader(body)))
	var rmResp struct {
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rmResp)
	if rmResp.Error == "" {
		t.Error("последний сервер не должен удаляться")
	}
}

// Отказ подключения по SSH должен приходить со стабильным кодом причины
// (ТЗ-13, internal/tunnel.ConnErrorKind), а не только сырым текстом — по
// нему страница выбирает переведённое сообщение вместо "ssh: handshake
// failed: ssh: unable to authenticate" на экране.
func TestHandleStartReportsConnErrorKind(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)

	keyPath := filepath.Join(tmp, "id_test")
	if _, _, err := ensureKey(keyPath); err != nil {
		t.Fatal(err)
	}

	// Порт, на котором заведомо никто не слушает — TCP сразу отвечает RST.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()

	cfg := withHost("127.0.0.1")
	p := cfg.Active()
	p.KeyPath = keyPath
	port, _ := strconv.Atoi(portStr)
	p.SSHPort = port
	cfg.SetProfile(p)

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := httptest.NewRecorder()
	s.handleStart(rec, httptest.NewRequest(http.MethodPost, "/api/start", nil))
	var resp struct {
		Error     string `json:"error"`
		ErrorKind string `json:"errorKind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не разобрался: %v (%s)", err, rec.Body.String())
	}
	if resp.Error == "" {
		t.Fatal("подключение к закрытому порту должно провалиться")
	}
	if resp.ErrorKind != string(tunnel.ConnErrorRefused) {
		t.Fatalf("ожидал errorKind=%q, получил %q (error=%q)",
			tunnel.ConnErrorRefused, resp.ErrorKind, resp.Error)
	}
}

// Самопроверка работает и без поднятого туннеля: адрес недоступен (порт
// закрыт), поэтому первый же сетевой шаг проваливается быстро, а не висит —
// сама глубина цепочки уже проверена отдельно в internal/tunnel.
func TestSelfCheckEndpoint(t *testing.T) {
	cfg := withHost("127.0.0.1")
	p := cfg.Active()
	p.SSHPort = 1 // закрытый порт — «отказано в соединении» почти сразу
	cfg.SetProfile(p)

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := httptest.NewRecorder()
	s.handleSelfCheck(rec, httptest.NewRequest(http.MethodGet, "/api/selfcheck", nil))

	var resp struct {
		Steps []struct {
			Name    string `json:"name"`
			OK      bool   `json:"ok"`
			Skipped bool   `json:"skipped"`
			Code    string `json:"code"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("не разобрал ответ: %v, тело: %s", err, rec.Body.String())
	}
	if len(resp.Steps) == 0 {
		t.Fatal("шагов не пришло")
	}
	if resp.Steps[0].Name != "dns" || !resp.Steps[0].OK {
		t.Errorf("шаг DNS для 127.0.0.1 обязан пройти сразу: %+v", resp.Steps[0])
	}
	var sawFailure bool
	for _, s := range resp.Steps {
		if !s.OK && !s.Skipped {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("закрытый порт должен был провалить один из шагов")
	}
}
