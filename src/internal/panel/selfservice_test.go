package panel

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// exeSuffix — на Windows собранной программе нужно расширение .exe.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func selfPost(t *testing.T, h http.Handler, path string, body any) (int, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestСозданиеПользователейИзПрограммы(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("vpn.example", 22, "")
	creator, err := s.clients.CreateClient("ПК", DeviceWindows)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := s.clients.CreateClient("Телефон", DeviceAndroid)
	s.clients.Update(creator.ID, func(c *Client) { c.Groups = []string{"Мои"} })
	caller := creator.UID
	s.connUID = func(*http.Request) (int, bool) { return caller, true }
	h := s.SelfServiceHandler()

	// Без галки — нельзя.
	if code, out := selfPost(t, h, "/v1/create", map[string]string{"password": "x", "name": "a"}); code != http.StatusForbidden {
		t.Fatalf("без права создавать: %d %v", code, out)
	}

	// Галка ставится только с паролем.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"`+creator.ID+`","canCreate":true,"password":""}`))
	s.handleClientCreator(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("право создавать включилось без пароля")
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"`+creator.ID+`","canCreate":true,"password":"длинный-пароль"}`))
	s.handleClientCreator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("право не включилось: %s", rec.Body)
	}

	if code, out := selfPost(t, h, "/v1/whoami", map[string]string{}); code != 200 || out["canCreate"] != true {
		t.Fatalf("whoami: %d %v", code, out)
	}
	if code, _ := selfPost(t, h, "/v1/create", map[string]string{"password": "неверный", "name": "a"}); code != http.StatusForbidden {
		t.Fatal("пустили с неверным паролем")
	}
	code, out := selfPost(t, h, "/v1/create", map[string]string{"password": "длинный-пароль", "name": "Ноутбук", "deviceType": "linux"})
	if code != 200 || !strings.Contains(out["config"].(string), "vpn.example") {
		t.Fatalf("не создался: %d %v", code, out)
	}
	made, _ := s.clients.Get(out["id"].(string))
	if made.CanCreate || made.CreatedBy != creator.ID || len(made.Groups) != 1 || made.Groups[0] != "Мои" {
		t.Fatalf("новый клиент получил не те права: %+v", made)
	}

	// Обычный клиент (без галки) — нельзя даже с паролем создателя.
	caller = plain.UID
	if code, _ := selfPost(t, h, "/v1/create", map[string]string{"password": "длинный-пароль", "name": "b"}); code != http.StatusForbidden {
		t.Fatal("обычный клиент смог создать пользователя")
	}
	// Незнакомый uid — нельзя.
	caller = 99999
	if code, _ := selfPost(t, h, "/v1/whoami", map[string]string{}); code != http.StatusForbidden {
		t.Fatal("незнакомый uid узнан")
	}
	// Не JSON (форма из браузера) — не принимается.
	req = httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader("password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("форма принята: %d", rec.Code)
	}
}

func TestЛимитСозданных(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("vpn.example", 22, "")
	creator, _ := s.clients.CreateClient("ПК", DeviceWindows)
	s.clients.Update(creator.ID, func(c *Client) { c.CanCreate = true })
	rec := httptest.NewRecorder()
	s.handleClientCreator(rec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"id":"`+creator.ID+`","canCreate":true,"password":"длинный-пароль"}`)))
	s.connUID = func(*http.Request) (int, bool) { return creator.UID, true }
	h := s.SelfServiceHandler()
	for i := 0; i < maxCreatedPerCreator; i++ {
		if code, out := selfPost(t, h, "/v1/create", map[string]string{"password": "длинный-пароль", "name": "d"}); code != 200 {
			t.Fatalf("%d: %v", i, out)
		}
	}
	if code, _ := selfPost(t, h, "/v1/create", map[string]string{"password": "длинный-пароль", "name": "d"}); code == 200 {
		t.Fatal("лимит не сработал")
	}
}

func TestВладелецСокетаПанель(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:B868 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 1
   1: 0100007F:9C40 0100007F:B868 01 00000000:00000000 00:00000000 00000000  1007        0 2
`
	sshd := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}
	panel := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 47208}
	if uid, ok := socketUID(table, sshd, panel); !ok || uid != 1007 {
		t.Fatalf("uid = %d %v", uid, ok)
	}
	if _, ok := socketUID(table, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5}, panel); ok {
		t.Fatal("нашёлся чужой сокет")
	}
}

func TestПолитикаИзНастроек(t *testing.T) {
	a := AccessSettings{Enabled: true, Groups: []string{"Мои", "Гости"},
		Rules: []AccessRule{{From: "Мои", To: GroupAll}, {From: "Гости", To: GroupServers, Ports: "80, 443"}}}
	if err := a.clean(); err != nil {
		t.Fatal(err)
	}
	if a.Rules[1].Ports != "80,443" {
		t.Fatalf("порты: %q", a.Rules[1].Ports)
	}
	bad := AccessSettings{Groups: []string{"Мои"}, Rules: []AccessRule{{From: "Мои", To: "Нет такой"}}}
	if bad.clean() == nil {
		t.Fatal("правило с неизвестной группой принято")
	}
	p := buildPolicy(a, []Client{{Username: "tun_a", Groups: []string{"Мои"}}, {Username: "tun_b"}})
	if !p.Enabled || len(p.Users) != 1 || p.Users["tun_a"][0] != "Мои" || len(p.Rules) != 2 {
		t.Fatalf("политика: %+v", p)
	}
}
