package winsvc

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/webui"
)

func TestEndpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadEndpoint(dir); err == nil {
		t.Fatal("файла нет, а ошибки нет")
	}
	want := Endpoint{Addr: "127.0.0.1:5555", Token: "abc", Version: "v1", PID: 42}
	if err := WriteEndpoint(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEndpoint(dir)
	if err != nil || got != want {
		t.Fatalf("прочитали %+v, %v", got, err)
	}
	RemoveEndpoint(dir)
	if _, err := os.Stat(EndpointPath(dir)); !os.IsNotExist(err) {
		t.Fatal("service.json не удалился")
	}
}

func TestRunEntryExe(t *testing.T) {
	for in, want := range map[string]string{
		`"C:\Program Files\ssh_tunnel\ssh_tunnel_vpn.exe" -tray`: `C:\Program Files\ssh_tunnel\ssh_tunnel_vpn.exe`,
		`"C:\Users\Иван\Downloads\ssh_tunnel_vpn.exe"`:           `C:\Users\Иван\Downloads\ssh_tunnel_vpn.exe`,
		`C:\tools\ssh_tunnel_vpn.exe -tray`:                      `C:\tools\ssh_tunnel_vpn.exe`,
		`  C:\tools\x.exe  `:                                     `C:\tools\x.exe`,
	} {
		if got := runEntryExe(in); got != want {
			t.Errorf("runEntryExe(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// fakeService — настоящий веб-интерфейс программы, как у службы, и его
// service.json в папке dir.
func fakeService(t *testing.T, dir string) *webui.Server {
	t.Helper()
	srv, _ := fakeServiceApp(t, dir)
	return srv
}

func fakeServiceApp(t *testing.T, dir string) (*webui.Server, *app.App) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // config.Save — не в настоящую папку
	a := app.New(config.Default())
	srv, err := webui.New(a)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(srv.Close)
	if err := WriteEndpoint(dir, Endpoint{Addr: srv.Addr(), Token: srv.Token(), Version: "test"}); err != nil {
		t.Fatal(err)
	}
	return srv, a
}

func get(t *testing.T, url string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Страница ходит к службе со своим ключом — прокси подставляет ключ службы.
// Чужой ключ и чужой сайт не проходят.
func TestProxyForwardsWithServiceToken(t *testing.T) {
	dir := t.TempDir()
	fakeService(t, dir)
	p := NewProxy(dir, nil)
	front := httptest.NewServer(p)
	defer front.Close()

	code, body := get(t, front.URL+"/api/status?t="+p.Token, map[string]string{"Origin": front.URL})
	if code != 200 || !strings.Contains(body, `"state"`) {
		t.Fatalf("статус через прокси: %d %s", code, body)
	}
	if code, _ := get(t, front.URL+"/api/status", map[string]string{"X-Token": p.Token}); code != 200 {
		t.Fatalf("ключ в заголовке: %d", code)
	}
	if code, _ := get(t, front.URL+"/api/status?t=чужой", nil); code != 403 {
		t.Fatalf("чужой ключ: %d, ожидался 403", code)
	}
	if code, _ := get(t, front.URL+"/api/status?t="+p.Token, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("чужой сайт: %d, ожидался 403", code)
	}
	if code, body := get(t, front.URL+"/", nil); code != 200 || !strings.Contains(body, "<html") {
		t.Fatalf("страница: %d", code)
	}
}

// Служба перезапустилась на другом порту с другим ключом — открытое окно
// продолжает работать, ничего не перезагружая.
func TestProxyFollowsServiceRestart(t *testing.T) {
	dir := t.TempDir()
	first := fakeService(t, dir)
	p := NewProxy(dir, nil)
	front := httptest.NewServer(p)
	defer front.Close()
	if code, _ := get(t, front.URL+"/api/status?t="+p.Token, nil); code != 200 {
		t.Fatalf("до перезапуска: %d", code)
	}

	first.Close()
	time.Sleep(10 * time.Millisecond) // другое время изменения файла
	fakeService(t, dir)
	// Первый запрос может попасть ещё в старый адрес — прокси тогда забывает
	// его, и следующий идёт уже в новый.
	code, _ := get(t, front.URL+"/api/status?t="+p.Token, nil)
	if code != 200 {
		code, _ = get(t, front.URL+"/api/status?t="+p.Token, nil)
	}
	if code != 200 {
		t.Fatalf("после перезапуска службы: %d", code)
	}
}

// Поток событий проходит насквозь и сразу, без буферизации.
func TestProxyStreamsEvents(t *testing.T) {
	dir := t.TempDir()
	_, a := fakeServiceApp(t, dir)
	p := NewProxy(dir, nil)
	front := httptest.NewServer(p)
	defer front.Close()

	// Служба шлёт заголовки вместе с первым событием — пусть оно будет.
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.Bus.Infof("привет из службы")
	}()
	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(front.URL + "/events?t=" + p.Token)
		if err != nil {
			got <- "ошибка: " + err.Error()
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var e struct{ Text string }
			json.Unmarshal([]byte(line), &e)
			if e.Text == "привет из службы" {
				got <- e.Text
				return
			}
		}
		got <- "поток оборвался"
	}()
	select {
	case v := <-got:
		if v != "привет из службы" {
			t.Fatal(v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("событие не дошло через прокси")
	}
}

// Запросы, которым нужен рабочий стол пользователя, прокси выполняет сам —
// служба их не видит.
func TestProxyLocalHandlers(t *testing.T) {
	dir := t.TempDir()
	fakeService(t, dir)
	called := false
	p := NewProxy(dir, map[string]http.HandlerFunc{
		"/api/pickfile": func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Write([]byte(`{"path":"C:\\x.exe"}`))
		},
	})
	front := httptest.NewServer(p)
	defer front.Close()
	if code, body := get(t, front.URL+"/api/pickfile?t="+p.Token, nil); code != 200 || !called {
		t.Fatalf("локальный обработчик не вызван: %d %s", code, body)
	}
	called = false
	if code, _ := get(t, front.URL+"/api/pickfile?t=чужой", nil); code != 403 || called {
		t.Fatal("локальный обработчик доступен без ключа")
	}
}

func TestEndpointPathInDir(t *testing.T) {
	if got := EndpointPath(filepath.Join("a", "b")); got != filepath.Join("a", "b", "service.json") {
		t.Fatal(got)
	}
}
