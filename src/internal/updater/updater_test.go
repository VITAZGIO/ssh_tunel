package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Строковое сравнение сказало бы, что 1.10.0 «меньше» 1.9.0 — на этом
// ломается большинство самодельных проверок обновлений, поэтому случай
// вынесен в тест первым.
func TestCompareСравниваетЧислами(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.0", 1},
		{"1.9.0", "1.10.0", -1},
		{"v1.2.0", "1.2.0", 0},
		{"1.2.0", "v1.2.1", -1},
		{"1.2.0-beta1", "1.2.0", 0},
		{"dev", "1.2.0", unknownVersion},
		{"", "1.2.0", unknownVersion},
		{"1.2.0", "не версия", unknownVersion},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, ожидалось %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAssetForВыбираетФайлПоСистеме(t *testing.T) {
	rel := Release{Assets: []Asset{
		{Name: "ssh_tunnel.exe"}, {Name: "ssh_tunnel_linux"},
		{Name: "ssh_tunnel_linux_arm64"}, {Name: "ssh_tunnel.apk"},
	}}
	cases := []struct{ goos, goarch, want string }{
		{"windows", "amd64", "ssh_tunnel.exe"},
		{"linux", "amd64", "ssh_tunnel_linux"},
		{"linux", "arm64", "ssh_tunnel_linux_arm64"},
		{"android", "arm64", "ssh_tunnel.apk"},
	}
	for _, tc := range cases {
		a, ok := AssetFor(rel, tc.goos, tc.goarch)
		if !ok || a.Name != tc.want {
			t.Errorf("%s/%s: получено %q (ok=%v), ожидалось %q", tc.goos, tc.goarch, a.Name, ok, tc.want)
		}
	}
	if _, ok := AssetFor(Release{}, "windows", "amd64"); ok {
		t.Error("у релиза без файлов не должно находиться подходящего")
	}
}

// Ответ GitHub разбираем из подставного сервера, а не из настоящей сети:
// тест не должен зависеть ни от интернета, ни от лимита запросов.
func TestCheckРазбираетОтветИКеширует(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v1.10.0","name":"1.10.0",
			"html_url":"https://example.invalid/release",
			"assets":[{"name":"ssh_tunnel_linux","browser_download_url":"https://example.invalid/f","size":10}]}`))
	}))
	defer srv.Close()
	restore := useTestServer(t, srv.URL, "1.9.0")

	res, err := Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Newer || res.Latest != "1.10.0" || res.Installed != "1.9.0" {
		t.Errorf("получено %+v, ожидалась новая версия 1.10.0", res)
	}
	if _, err := Check(context.Background()); err != nil {
		t.Fatalf("повторный Check: %v", err)
	}
	if calls != 1 {
		t.Errorf("к GitHub ушло %d запросов, ожидался один (кеш)", calls)
	}
	restore()
}

func TestCheckОтличаетЛимитОтОтсутствияОбновлений(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	restore := useTestServer(t, srv.URL, "1.9.0")
	defer restore()

	if _, err := Check(context.Background()); err != ErrRateLimited {
		t.Errorf("получено %v, ожидалось ErrRateLimited", err)
	}
}

func TestCheckНеМолчитПриНедоступномGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // сервер закрыт — соединиться будет некуда
	restore := useTestServer(t, addr, "1.9.0")
	defer restore()

	if _, err := Check(context.Background()); err == nil {
		t.Error("недоступный GitHub должен быть ошибкой, а не «у вас последняя версия»")
	}
}

func TestDownloadНеЗатираетСуществующийФайл(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("новая сборка"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "ssh_tunnel.exe"), []byte("старое"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := Download(context.Background(), srv.URL, "ssh_tunnel.exe")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if filepath.Base(path) != "ssh_tunnel (1).exe" {
		t.Errorf("файл сохранён как %q, ожидалось «ssh_tunnel (1).exe»", filepath.Base(path))
	}
	old, _ := os.ReadFile(filepath.Join(dir, "ssh_tunnel.exe"))
	if string(old) != "старое" {
		t.Error("существующий файл затёрт")
	}
}

// useTestServer подменяет адрес GitHub и версию сборки на время теста и
// сбрасывает кеш, чтобы соседние тесты не влияли друг на друга.
func useTestServer(t *testing.T, url, version string) func() {
	t.Helper()
	oldURL, oldVer := releasesURLFor, Version
	releasesURLFor, Version = url, version
	resetCache()
	return func() {
		releasesURLFor, Version = oldURL, oldVer
		resetCache()
	}
}

func resetCache() {
	cacheMu.Lock()
	cached, cachedAt = nil, time.Time{}
	cacheMu.Unlock()
}
