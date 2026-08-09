//go:build linux

// Системные настройки прокси для Linux.
//
// Здесь нет одного места, как реестр в Windows, поэтому делается два дела:
//
//  1. Всегда пишется файл с переменными окружения. Его достаточно подключить
//     («source»), чтобы через туннель пошли curl, git, apt, docker, pip, npm и
//     всё остальное консольное. На сервере это и есть главный способ.
//
//  2. Если рядом графическая среда GNOME (или производная), настройки прокси
//     дополнительно прописываются через gsettings — тогда браузеры и
//     графические приложения подхватывают их сами, как в Windows.
//
// Прежние значения gsettings сохраняются и возвращаются при выходе, как и в
// Windows-версии: затирать чужие настройки нельзя.
package sysproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type snapshot struct {
	GSettings map[string]string `json:"gsettings"`
	Applied   bool              `json:"applied"`
}

type Manager struct {
	dir       string
	statePath string
	envPath   string
	snap      *snapshot
}

func NewManager(configDir string) *Manager {
	return &Manager{
		dir:       configDir,
		statePath: filepath.Join(configDir, "sysproxy-restore.json"),
		envPath:   filepath.Join(configDir, "proxy.env"),
	}
}

// gsettings, которые описывают прокси в GNOME.
var gsettingsKeys = []string{
	"org.gnome.system.proxy mode",
	"org.gnome.system.proxy.http host",
	"org.gnome.system.proxy.http port",
	"org.gnome.system.proxy.https host",
	"org.gnome.system.proxy.https port",
	"org.gnome.system.proxy.socks host",
	"org.gnome.system.proxy.socks port",
	"org.gnome.system.proxy ignore-hosts",
}

func (m *Manager) Enable(httpAddr, socksAddr string, setEnv, bypassLocal bool, extra []string) error {
	if m.snap != nil {
		return nil
	}
	snap := &snapshot{GSettings: map[string]string{}}

	if err := m.writeEnvFile(httpAddr, bypassLocal, extra); err != nil {
		return err
	}

	if hasDesktopProxy() {
		for _, k := range gsettingsKeys {
			if v, err := gsettingsGet(k); err == nil {
				snap.GSettings[k] = v
			}
		}
		hHost, hPort := splitHostPort(httpAddr)
		sHost, sPort := splitHostPort(socksAddr)
		set := [][2]string{
			{"org.gnome.system.proxy.http host", hHost},
			{"org.gnome.system.proxy.http port", hPort},
			{"org.gnome.system.proxy.https host", hHost},
			{"org.gnome.system.proxy.https port", hPort},
			{"org.gnome.system.proxy.socks host", sHost},
			{"org.gnome.system.proxy.socks port", sPort},
			{"org.gnome.system.proxy ignore-hosts", gnomeIgnoreHosts(bypassLocal, extra...)},
			{"org.gnome.system.proxy mode", "'manual'"},
		}
		for _, kv := range set {
			gsettingsSet(kv[0], kv[1])
		}
		snap.Applied = true
	}

	m.snap = snap
	m.saveState()
	return nil
}

// writeEnvFile кладёт рядом с настройками готовый файл для подключения в
// оболочке. Пишется всегда: на сервере это единственный рабочий способ, а на
// рабочем столе — запасной.
func (m *Manager) writeEnvFile(httpAddr string, bypassLocal bool, extra []string) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	url := "http://" + httpAddr
	noProxy := noProxyList(bypassLocal, extra...)
	body := fmt.Sprintf(`# Создано ssh_tunel. Подключить в текущую оболочку:
#   source %s
# Отключить: unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY no_proxy NO_PROXY
export http_proxy=%s
export https_proxy=%s
export HTTP_PROXY=%s
export HTTPS_PROXY=%s
export no_proxy=%s
export NO_PROXY=%s
`, m.envPath, url, url, url, url, noProxy, noProxy)
	return os.WriteFile(m.envPath, []byte(body), 0o600)
}

func (m *Manager) Disable() error {
	if m.snap == nil {
		return nil
	}
	err := restore(m.snap)
	m.snap = nil
	os.Remove(m.statePath)
	os.Remove(m.envPath)
	return err
}

func restore(snap *snapshot) error {
	if !snap.Applied {
		return nil
	}
	for k, v := range snap.GSettings {
		gsettingsSet(k, v)
	}
	return nil
}

// RecoverStale возвращает настройки, оставшиеся от аварийно завершённого
// прошлого запуска.
func (m *Manager) RecoverStale() bool {
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return false
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		os.Remove(m.statePath)
		return false
	}
	restore(&snap)
	os.Remove(m.statePath)
	os.Remove(m.envPath)
	return true
}

func (m *Manager) saveState() {
	if m.snap == nil {
		return
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return
	}
	if data, err := json.Marshal(m.snap); err == nil {
		os.WriteFile(m.statePath, data, 0o600)
	}
}

// EnvFilePath — путь к файлу с переменными окружения, чтобы показать его
// пользователю.
func (m *Manager) EnvFilePath() string { return m.envPath }

// Current показывает, что сейчас прописано в системе.
func Current() string {
	if !hasDesktopProxy() {
		return "переменные окружения (графической среды нет)"
	}
	mode, err := gsettingsGet("org.gnome.system.proxy mode")
	if err != nil {
		return "неизвестно"
	}
	mode = strings.Trim(strings.TrimSpace(mode), "'")
	if mode != "manual" {
		return "выключен"
	}
	host, _ := gsettingsGet("org.gnome.system.proxy.http host")
	port, _ := gsettingsGet("org.gnome.system.proxy.http port")
	return "включён: " + strings.Trim(strings.TrimSpace(host), "'") + ":" + strings.TrimSpace(port)
}

// hasDesktopProxy — есть ли смысл трогать настройки графической среды. На
// сервере без рабочего стола gsettings либо отсутствует, либо пишет в пустоту.
func hasDesktopProxy() bool {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	_, err := exec.LookPath("gsettings")
	return err == nil
}

func gsettingsGet(key string) (string, error) {
	parts := strings.SplitN(key, " ", 2)
	out, err := exec.Command("gsettings", "get", parts[0], parts[1]).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gsettingsSet(key, value string) {
	parts := strings.SplitN(key, " ", 2)
	exec.Command("gsettings", "set", parts[0], parts[1], value).Run()
}

func splitHostPort(addr string) (string, string) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "0"
	}
	host := addr[:i]
	port := addr[i+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return addr, "0"
	}
	return "'" + host + "'", port
}

// EnvHint — строки для ручного подключения в оболочке.
func EnvHint(httpAddr string) []string {
	url := "http://" + httpAddr
	return []string{
		"export HTTPS_PROXY=" + url,
		"export HTTP_PROXY=" + url,
		"export NO_PROXY=" + noProxyList(true),
	}
}
