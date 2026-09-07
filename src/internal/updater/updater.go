// Package updater — проверка обновлений по странице релизов GitHub и
// скачивание готового файла новой версии. Задача описана в
// docs/UPDATE_SPEC.md; здесь её общая для Windows, Linux и панели часть.
//
// Автоматической установки тут нет намеренно: на Windows программа не может
// переписать саму себя, пока она запущена, а на Linux файл обычно лежит в
// /usr/local/bin и требует root. Обновление скачивается в «Загрузки», а
// заменяет файл человек.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version — версия этой сборки. Проставляется линковщиком
// (-X sshtunnel/internal/updater.Version=v1.2.0) из build.sh; при сборке
// руками остаётся "dev", и тогда сравнивать не с чем — панель честно скажет,
// что версия неизвестна, но последний релиз всё равно покажет.
var Version = "dev"

// releasesURL — публичная ручка GitHub: последний НЕ черновой и не
// предрелизный выпуск репозитория.
const releasesURL = "https://api.github.com/repos/VITAZGIO/ssh_tunel/releases/latest"

// releasesURLFor — тот же адрес, но переменной: тесты подставляют сюда свой
// httptest-сервер, чтобы не ходить в настоящую сеть и не тратить лимит
// запросов GitHub.
var releasesURLFor = releasesURL

// Release — то немногое, что нам нужно от ответа GitHub.
type Release struct {
	Tag    string  `json:"tag_name"`
	Name   string  `json:"name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset — один файл, приложенный к релизу.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Result — ответ на «проверить обновления» в том виде, в каком его показывает
// интерфейс.
type Result struct {
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	// Newer — на GitHub лежит версия новее установленной.
	Newer bool `json:"newer"`
	// Ahead — установлена версия новее опубликованной (сборка из исходников).
	Ahead bool `json:"ahead"`
	// Unknown — версию установленной сборки определить не удалось.
	Unknown bool   `json:"unknown"`
	URL     string `json:"url"`
	// Asset — файл этого релиза, подходящий текущей системе (может быть пуст).
	AssetName string `json:"assetName"`
	AssetURL  string `json:"assetUrl"`
}

var (
	cacheMu  sync.Mutex
	cached   *Release
	cachedAt time.Time
	cacheTTL = time.Hour
	// httpClient — свой, а не http.DefaultClient: у проверки обновлений свой
	// таймаут, и менять его глобально для всей программы нельзя.
	httpClient = &http.Client{Timeout: 10 * time.Second}
)

// ErrRateLimited — GitHub временно отказывает из-за числа запросов с этого
// адреса. Отдельная ошибка, потому что показывать её нужно не так, как
// «нет сети», и уж точно не как «у вас последняя версия».
var ErrRateLimited = errors.New("GitHub временно ограничил число запросов — попробуй позже")

// Check возвращает результат сравнения установленной версии с последним
// релизом. Ответ GitHub кешируется на час: анонимный лимит — 60 запросов в
// час на адрес, а кнопку можно нажимать сколько угодно.
func Check(ctx context.Context) (Result, error) {
	rel, err := latest(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Installed: Version,
		Latest:    strings.TrimPrefix(rel.Tag, "v"),
		URL:       rel.URL,
	}
	if a, ok := AssetFor(*rel, runtime.GOOS, runtime.GOARCH); ok {
		res.AssetName, res.AssetURL = a.Name, a.URL
	}
	switch cmp := Compare(Version, rel.Tag); {
	case cmp == unknownVersion:
		res.Unknown = true
	case cmp < 0:
		res.Newer = true
	case cmp > 0:
		res.Ahead = true
	}
	return res, nil
}

func latest(ctx context.Context) (*Release, error) {
	cacheMu.Lock()
	if cached != nil && time.Since(cachedAt) < cacheTTL {
		rel := *cached
		cacheMu.Unlock()
		return &rel, nil
	}
	cacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURLFor, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ssh_tunnel/"+Version+" ("+runtime.GOOS+")")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось связаться с GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub ответил %s", resp.Status)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("не разобрал ответ GitHub: %w", err)
	}
	if rel.Tag == "" {
		return nil, errors.New("GitHub не назвал версию последнего релиза")
	}

	cacheMu.Lock()
	cached, cachedAt = &rel, time.Now()
	cacheMu.Unlock()
	return &rel, nil
}

// unknownVersion — метка «сравнивать не с чем» для Compare: версия сборки не
// похожа на номер (например "dev").
const unknownVersion = -100

// Compare сравнивает две версии вида MAJOR.MINOR.PATCH (ведущая "v"
// необязательна) покомпонентно как числа: 1.10.0 новее 1.9.0, хотя как
// строка он «меньше» — на этом обычно и ломаются самодельные проверки.
// Возвращает -1, 0, 1 или unknownVersion, если разобрать нечего.
func Compare(a, b string) int {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return unknownVersion
	}
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func parse(v string) ([3]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if i := strings.IndexAny(v, "-+"); i >= 0 { // 1.2.0-beta1 → 1.2.0
		v = v[:i]
	}
	if v == "" {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// AssetFor выбирает файл релиза для этой системы: на Windows — .exe (архив
// нужен браузеру, а мы качаем сами), на Linux — файл под свою архитектуру.
func AssetFor(rel Release, goos, goarch string) (Asset, bool) {
	want := ""
	switch goos {
	case "windows":
		want = "ssh_tunnel.exe"
	case "linux":
		if goarch == "arm64" {
			want = "ssh_tunnel_linux_arm64"
		} else {
			want = "ssh_tunnel_linux"
		}
	case "android":
		want = "ssh_tunnel.apk"
	}
	for _, a := range rel.Assets {
		if a.Name == want {
			return a, true
		}
	}
	return Asset{}, false
}

// Download скачивает файл релиза в папку загрузок и возвращает путь к нему.
// Существующий файл не затирается — рядом появляется имя с номером, как это
// делает браузер: перезаписать чужой файл молча хуже, чем положить второй.
func Download(ctx context.Context, url, filename string) (string, error) {
	if url == "" {
		return "", errors.New("нечего скачивать: у релиза нет файла для этой системы")
	}
	dir := DownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ssh_tunnel/"+Version+" ("+runtime.GOOS+")")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("не удалось скачать файл: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub ответил %s", resp.Status)
	}

	path := freeName(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// freeName подбирает свободное имя в папке: file.exe, file (1).exe и так
// далее.
func freeName(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	path := filepath.Join(dir, name)
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return path
}

// DownloadsDir — папка «Загрузки» текущего пользователя. XDG_DOWNLOAD_DIR
// учитывается: в русской системе папка может называться «Загрузки», и класть
// файл в несуществующую Downloads было бы сюрпризом.
func DownloadsDir() string {
	if d := strings.TrimSpace(os.Getenv("XDG_DOWNLOAD_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return os.TempDir()
	}
	downloads := filepath.Join(home, "Downloads")
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(filepath.Join(home, "Загрузки")); err == nil && st.IsDir() {
			return filepath.Join(home, "Загрузки")
		}
	}
	return downloads
}
