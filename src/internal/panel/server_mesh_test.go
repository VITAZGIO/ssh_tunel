package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapacityЧерезПанель(t *testing.T) {
	reloads := fakeSSHD(t, nil)
	os.WriteFile(SSHDPath, []byte("Port 22\n"), 0o600)
	s, pass := newTestServer(t)
	st, err := OpenSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := s.WithSettings(st).Handler()
	cookies := loggedInCookies(t, h, pass)

	rec, body := doJSON(t, h, http.MethodGet, "/api/settings/capacity", nil, cookies)
	if rec.Code != http.StatusOK || body["maxDevices"] != float64(DefaultMaxDevices) || body["wanted"] != "120:30:240" {
		t.Fatalf("GET: %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodPost, "/api/settings/capacity", map[string]int{"maxDevices": 0}, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("0 устройств принято: %d %v", rec.Code, body)
	}

	rec, body = doJSON(t, h, http.MethodPost, "/api/settings/capacity", map[string]int{"maxDevices": 50}, cookies)
	if rec.Code != http.StatusOK || body["maxDevices"] != float64(50) {
		t.Fatalf("POST: %d %v", rec.Code, body)
	}
	if st.Get().MaxDevices != 50 || *reloads != 1 {
		t.Fatalf("настройка %d, перечитываний sshd %d", st.Get().MaxDevices, *reloads)
	}
	if data, _ := os.ReadFile(SSHDPath); !strings.Contains(string(data), "MaxStartups 200:30:400") {
		t.Fatalf("sshd_config:\n%s", data)
	}
}

func TestCapacityНеСохраняетсяПриОшибкеSSHD(t *testing.T) {
	fakeSSHD(t, os.ErrInvalid)
	os.WriteFile(SSHDPath, []byte("Port 22\n"), 0o600)
	s, pass := newTestServer(t)
	st, _ := OpenSettings(filepath.Join(t.TempDir(), "settings.json"))
	h := s.WithSettings(st).Handler()
	rec, _ := doJSON(t, h, http.MethodPost, "/api/settings/capacity", map[string]int{"maxDevices": 50}, loggedInCookies(t, h, pass))
	if rec.Code != http.StatusBadRequest || st.Get().MaxDevices != 0 {
		t.Fatalf("код %d, в настройках %d", rec.Code, st.Get().MaxDevices)
	}
}

func TestMeshРучкиЧерезПанель(t *testing.T) {
	s, pass := newTestServer(t)
	sys := newFakeMeshSystem()
	m := newTestMesh(t, sys, fakeDownAdmin{}, "main.example", 22)
	h := s.WithMesh(m).Handler()

	for _, path := range []string{"/api/mesh/state", "/api/mesh/role", "/api/mesh/links/create", "/api/mesh/join"} {
		if rec, _ := doJSON(t, h, http.MethodPost, path, map[string]string{}, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s без входа: %d", path, rec.Code)
		}
	}
	cookies := loggedInCookies(t, h, pass)

	rec, body := doJSON(t, h, http.MethodGet, "/api/mesh/state", nil, cookies)
	if rec.Code != http.StatusOK || body["effectiveRole"] != "" {
		t.Fatalf("состояние: %d %v", rec.Code, body)
	}
	rec, body = doJSON(t, h, http.MethodPost, "/api/mesh/role", map[string]string{"role": "что-то"}, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестная роль принята: %v", body)
	}
	rec, _ = doJSON(t, h, http.MethodPost, "/api/mesh/role", map[string]string{"role": "main", "name": "Москва"}, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("роль главного: %d", rec.Code)
	}
	eventually(t, "установка не закончилась", func() bool { j := m.jobSnapshot(); return j != nil && !j.Running })

	rec, body = doJSON(t, h, http.MethodPost, "/api/mesh/links/create", map[string]string{"name": "Германия"}, cookies)
	code, _ := body["code"].(string)
	if rec.Code != http.StatusOK || !strings.HasPrefix(code, invitePrefix) {
		t.Fatalf("код подключения: %d %v", rec.Code, body)
	}
	rec, body = doJSON(t, h, http.MethodGet, "/api/mesh/state", nil, cookies)
	servers, _ := body["servers"].([]any)
	if rec.Code != http.StatusOK || body["role"] != "main" || len(servers) != 2 {
		t.Fatalf("после добавления сервера: %v", body)
	}
	rec, body = doJSON(t, h, http.MethodPost, "/api/mesh/join", map[string]string{"code": "мусор"}, cookies)
	if rec.Code != http.StatusBadRequest || body["error"] == "" {
		t.Fatalf("мусорный код принят: %d %v", rec.Code, body)
	}
}
