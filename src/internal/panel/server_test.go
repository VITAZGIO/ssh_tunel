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
	return NewServer(store), "onetimepass"
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
