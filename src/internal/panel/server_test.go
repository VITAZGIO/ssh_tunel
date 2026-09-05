package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateWithPassword("admin", "onetimepass", true); err != nil {
		t.Fatal(err)
	}
	clientStore, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	clients := NewClientManager(clientStore, newFakeProvisioner())
	return NewServer(store, clients), "onetimepass"
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any, cookies []*http.Cookie) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestLoginRequiresPasswordChangeThenAllowsStatus(t *testing.T) {
	s, pass := newTestServer(t)
	h := s.Handler()

	// До входа статус недоступен.
	rec, _ := doJSON(t, h, http.MethodGet, "/api/status", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидал 401 без сессии, получил %d", rec.Code)
	}

	// Неверный пароль.
	rec, body := doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": "wrong"}, nil)
	if rec.Code != http.StatusUnauthorized || body["error"] != "bad_credentials" {
		t.Fatalf("ожидал bad_credentials, получил %d %v", rec.Code, body)
	}

	// Верный пароль — сессия создаётся, но помечена mustChange.
	rec, body = doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": pass}, nil)
	if rec.Code != http.StatusOK || body["mustChange"] != true {
		t.Fatalf("ожидал успешный вход с mustChange, получил %d %v", rec.Code, body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("логин должен ставить cookie сессии")
	}

	// Пока пароль не сменили, /api/status закрыт.
	rec, body = doJSON(t, h, http.MethodGet, "/api/status", nil, cookies)
	if rec.Code != http.StatusForbidden || body["error"] != "must_change_password" {
		t.Fatalf("ожидал must_change_password, получил %d %v", rec.Code, body)
	}

	// Смена пароля с неверным текущим паролем.
	rec, body = doJSON(t, h, http.MethodPost, "/api/change-password",
		map[string]string{"currentPassword": "wrong", "newPassword": "brandnewpass"}, cookies)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидал 401 на неверный текущий пароль, получил %d %v", rec.Code, body)
	}

	// Успешная смена пароля.
	rec, body = doJSON(t, h, http.MethodPost, "/api/change-password",
		map[string]string{"currentPassword": pass, "newPassword": "brandnewpass"}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("ожидал успешную смену пароля, получил %d %v", rec.Code, body)
	}

	// Теперь статус доступен той же сессией.
	rec, _ = doJSON(t, h, http.MethodGet, "/api/status", nil, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидал доступ к статусу после смены пароля, получил %d", rec.Code)
	}

	// И список клиентов пуст.
	rec, body = doJSON(t, h, http.MethodGet, "/api/clients", nil, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидал доступ к списку клиентов, получил %d", rec.Code)
	}
	clients, _ := body["clients"].([]any)
	if len(clients) != 0 {
		t.Fatalf("список клиентов должен быть пуст, получил %v", clients)
	}

	// Новый пароль работает при новом логине.
	rec, body = doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": "brandnewpass"}, nil)
	if rec.Code != http.StatusOK || body["mustChange"] != false {
		t.Fatalf("ожидал вход новым паролем без mustChange, получил %d %v", rec.Code, body)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	s, pass := newTestServer(t)
	h := s.Handler()

	rec, _ := doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": pass}, nil)
	cookies := rec.Result().Cookies()

	rec, _ = doJSON(t, h, http.MethodPost, "/api/logout", map[string]string{}, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout должен отвечать 200, получил %d", rec.Code)
	}

	rec, _ = doJSON(t, h, http.MethodGet, "/api/session", nil, cookies)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["authenticated"] == true {
		t.Fatal("после logout сессия не должна считаться действующей")
	}
}

// TestLoginLockoutAfterRepeatedFailures проверяет реакцию /api/login на уже
// заблокированный ключ. Сама блокировка после серии неудач подряд —
// поведение loginLimiter, оно проверено отдельно в limiter_test.go; здесь же
// неудачи заводятся напрямую в лимитер, а не через lockAfter реальных HTTP-
// запросов подряд — иначе тест шёл бы почти минуту из-за растущей задержки
// между попытками, которая и есть защита от перебора.
func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()

	key := limiterKey(httptest.NewRequest(http.MethodPost, "/api/login", nil), "admin")
	for i := 0; i < lockAfter; i++ {
		s.limiter.Fail(key)
	}

	rec, body := doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": "wrong"}, nil)
	if rec.Code != http.StatusTooManyRequests || body["error"] != "locked" {
		t.Fatalf("ожидал блокировку после %d неудач, получил %d %v", lockAfter, rec.Code, body)
	}
}

// loggedInCookies логинится и сразу меняет одноразовый пароль — большинству
// тестов клиентской ручки сама смена пароля не важна, а без неё requireAuth
// не пускает дальше logout/change-password.
func loggedInCookies(t *testing.T, h http.Handler, pass string) []*http.Cookie {
	t.Helper()
	rec, _ := doJSON(t, h, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": pass}, nil)
	cookies := rec.Result().Cookies()
	rec, body := doJSON(t, h, http.MethodPost, "/api/change-password",
		map[string]string{"currentPassword": pass, "newPassword": "brandnewpass1"}, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("не удалось сменить одноразовый пароль в тестовой обвязке: %d %v", rec.Code, body)
	}
	return cookies
}

func TestClientCreateListDelete(t *testing.T) {
	s, pass := newTestServer(t)
	h := s.Handler()
	cookies := loggedInCookies(t, h, pass)

	rec, body := doJSON(t, h, http.MethodPost, "/api/clients/create",
		map[string]string{"name": "Ноутбук", "deviceType": "linux"}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("ожидал успешное создание клиента, получил %d %v", rec.Code, body)
	}
	client, _ := body["client"].(map[string]any)
	if client == nil {
		t.Fatalf("ответ должен содержать клиента: %v", body)
	}
	id, _ := client["id"].(string)
	if id == "" {
		t.Fatal("у созданного клиента должен быть id")
	}
	if client["privateKey"] == nil || client["privateKey"] == "" {
		t.Fatal("ответ на создание должен содержать приватный ключ")
	}

	rec, body = doJSON(t, h, http.MethodGet, "/api/clients", nil, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидал 200 от списка клиентов, получил %d", rec.Code)
	}
	list, _ := body["clients"].([]any)
	if len(list) != 1 {
		t.Fatalf("ожидал одного клиента в списке, получил %d: %v", len(list), list)
	}
	first, _ := list[0].(map[string]any)
	if first["privateKey"] != nil && first["privateKey"] != "" {
		t.Fatal("список клиентов не должен содержать приватный ключ")
	}

	rec, body = doJSON(t, h, http.MethodPost, "/api/clients/delete",
		map[string]string{"id": id}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("ожидал успешное удаление клиента, получил %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodGet, "/api/clients", nil, cookies)
	list, _ = body["clients"].([]any)
	if len(list) != 0 {
		t.Fatalf("после удаления список клиентов должен быть пуст, получил %v", list)
	}
}

func TestClientCreateRequiresAuth(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	rec, _ := doJSON(t, h, http.MethodPost, "/api/clients/create",
		map[string]string{"name": "Ноутбук", "deviceType": "linux"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без сессии ожидал 401, получил %d", rec.Code)
	}
}

func TestClientCreateRejectsBadDeviceType(t *testing.T) {
	s, pass := newTestServer(t)
	h := s.Handler()
	cookies := loggedInCookies(t, h, pass)

	rec, body := doJSON(t, h, http.MethodPost, "/api/clients/create",
		map[string]string{"name": "Штука", "deviceType": "amiga"}, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("некорректный тип устройства должен отдавать 400, получил %d %v", rec.Code, body)
	}
}

func TestClientFreezeUnfreezeDisconnect(t *testing.T) {
	s, pass := newTestServer(t)
	h := s.Handler()
	cookies := loggedInCookies(t, h, pass)

	rec, body := doJSON(t, h, http.MethodPost, "/api/clients/create",
		map[string]string{"name": "Ноутбук", "deviceType": "linux"}, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("не удалось создать клиента: %d %v", rec.Code, body)
	}
	client := body["client"].(map[string]any)
	id := client["id"].(string)

	rec, body = doJSON(t, h, http.MethodPost, "/api/clients/freeze", map[string]string{"id": id}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("заморозка должна пройти успешно, получил %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodGet, "/api/clients", nil, cookies)
	list, _ := body["clients"].([]any)
	if len(list) != 1 {
		t.Fatalf("ожидал одного клиента, получил %d", len(list))
	}
	if state := list[0].(map[string]any)["state"]; state != "frozen" {
		t.Fatalf("состояние в списке должно быть frozen, получил %v", state)
	}

	rec, body = doJSON(t, h, http.MethodPost, "/api/clients/unfreeze", map[string]string{"id": id}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("разморозка должна пройти успешно, получил %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodPost, "/api/clients/disconnect", map[string]string{"id": id}, cookies)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("отключение должно пройти успешно, получил %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodGet, "/api/clients", nil, cookies)
	list, _ = body["clients"].([]any)
	if state := list[0].(map[string]any)["state"]; state != "active" {
		t.Fatalf("после отключения состояние должно остаться active, получил %v", state)
	}
}

func TestClientActionsRequireAuth(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, path := range []string{"/api/clients/freeze", "/api/clients/unfreeze", "/api/clients/disconnect"} {
		rec, _ := doJSON(t, h, http.MethodPost, path, map[string]string{"id": "tun_0000000000000000"}, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s без сессии должен отдавать 401, получил %d", path, rec.Code)
		}
	}
}
