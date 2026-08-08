//go:build windows

// Package sysproxy включает и выключает системные настройки прокси Windows.
//
// Главное отличие от прошлой версии: прежние настройки СОХРАНЯЮТСЯ и потом
// восстанавливаются точь-в-точь. Раньше выход просто ставил ProxyEnable=0 —
// то есть затирал настройки, которые у пользователя могли быть до нас, а если
// программа падала или окно консоли закрывали крестиком, прокси вообще
// оставался включённым и интернет пропадал совсем: система продолжала слать
// весь трафик на порт, который уже никто не слушает.
//
// Поэтому здесь три вещи:
//   - снимок прежнего состояния пишется на диск, а не только в память;
//   - при старте снимок с прошлого запуска обнаруживается и применяется;
//   - восстановление идемпотентно.
package sysproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	envKey           = `Environment`
)

// snapshot — то, что нужно вернуть на место при выходе.
type snapshot struct {
	ProxyEnable   uint64 `json:"proxyEnable"`
	ProxyServer   string `json:"proxyServer"`
	ProxyOverride string `json:"proxyOverride"`
	HadServer     bool   `json:"hadServer"`
	HadOverride   bool   `json:"hadOverride"`

	EnvHTTP     string `json:"envHttp"`
	EnvHTTPS    string `json:"envHttps"`
	EnvNo       string `json:"envNo"`
	HadEnvHTTP  bool   `json:"hadEnvHttp"`
	HadEnvHTTPS bool   `json:"hadEnvHttps"`
	HadEnvNo    bool   `json:"hadEnvNo"`

	EnvApplied bool `json:"envApplied"`
}

type Manager struct {
	statePath string
	snap      *snapshot
}

func NewManager(configDir string) *Manager {
	return &Manager{statePath: filepath.Join(configDir, "sysproxy-restore.json")}
}

// Enable прописывает системный прокси и переменные среды.
//
// httpAddr указывается и для http, и для https: WinINET умеет CONNECT, а через
// него имя хоста уходит на VPS и не светится в DNS-запросах провайдера.
// socksAddr прописывается дополнительно — для приложений, которые ходят
// только по SOCKS.
func (m *Manager) Enable(httpAddr, socksAddr string, setEnv bool) error {
	if m.snap != nil {
		return nil // уже включено
	}
	snap := &snapshot{}

	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("нет доступа к настройкам Internet Settings: %w", err)
	}
	defer k.Close()

	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		snap.ProxyEnable = v
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		snap.ProxyServer, snap.HadServer = v, true
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		snap.ProxyOverride, snap.HadOverride = v, true
	}

	value := fmt.Sprintf("http=%s;https=%s;socks=%s", httpAddr, httpAddr, socksAddr)
	if err := k.SetStringValue("ProxyServer", value); err != nil {
		return err
	}
	// Локальные адреса обязаны идти мимо прокси. Раньше здесь была пустая
	// строка — то есть даже обращение к 127.0.0.1 система пыталась гнать через
	// прокси; это ломает локальные серверы разработки и наше собственное окно
	// настроек.
	if err := k.SetStringValue("ProxyOverride", "<local>;localhost;127.0.0.1;::1;*.local"); err != nil {
		return err
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}

	if setEnv {
		if err := m.applyEnv(snap, httpAddr); err != nil {
			// Переменные среды — приятное дополнение, но не повод отменять
			// уже включённый системный прокси.
			snap.EnvApplied = false
		} else {
			snap.EnvApplied = true
		}
	}

	m.snap = snap
	m.saveState()
	notifyWinINET()
	broadcastEnvChange()
	return nil
}

// applyEnv прописывает HTTP_PROXY/HTTPS_PROXY/NO_PROXY в переменные среды
// пользователя. Именно это лечит 403 в локально установленном Claude Code:
// Node.js системный прокси Windows не читает и без этих переменных идёт
// напрямую, получая отказ по географии.
//
// Важно: переменные среды подхватываются только процессами, запущенными ПОСЛЕ
// изменения. Уже открытый терминал их не увидит.
func (m *Manager) applyEnv(snap *snapshot, httpAddr string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if v, _, err := k.GetStringValue("HTTP_PROXY"); err == nil {
		snap.EnvHTTP, snap.HadEnvHTTP = v, true
	}
	if v, _, err := k.GetStringValue("HTTPS_PROXY"); err == nil {
		snap.EnvHTTPS, snap.HadEnvHTTPS = v, true
	}
	if v, _, err := k.GetStringValue("NO_PROXY"); err == nil {
		snap.EnvNo, snap.HadEnvNo = v, true
	}

	url := "http://" + httpAddr
	if err := k.SetStringValue("HTTP_PROXY", url); err != nil {
		return err
	}
	if err := k.SetStringValue("HTTPS_PROXY", url); err != nil {
		return err
	}
	return k.SetStringValue("NO_PROXY", "localhost,127.0.0.1,::1")
}

// Disable возвращает всё как было.
func (m *Manager) Disable() error {
	if m.snap == nil {
		return nil
	}
	err := restore(m.snap)
	m.snap = nil
	os.Remove(m.statePath)
	notifyWinINET()
	broadcastEnvChange()
	return err
}

func restore(snap *snapshot) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.SetDWordValue("ProxyEnable", uint32(snap.ProxyEnable)); err != nil {
		return err
	}
	if snap.HadServer {
		k.SetStringValue("ProxyServer", snap.ProxyServer)
	} else {
		k.DeleteValue("ProxyServer")
	}
	if snap.HadOverride {
		k.SetStringValue("ProxyOverride", snap.ProxyOverride)
	} else {
		k.DeleteValue("ProxyOverride")
	}

	if snap.EnvApplied {
		if ek, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
			restoreEnv(ek, "HTTP_PROXY", snap.EnvHTTP, snap.HadEnvHTTP)
			restoreEnv(ek, "HTTPS_PROXY", snap.EnvHTTPS, snap.HadEnvHTTPS)
			restoreEnv(ek, "NO_PROXY", snap.EnvNo, snap.HadEnvNo)
			ek.Close()
		}
	}
	return nil
}

func restoreEnv(k registry.Key, name, value string, had bool) {
	if had {
		k.SetStringValue(name, value)
	} else {
		k.DeleteValue(name)
	}
}

// RecoverStale вызывается при старте: если от прошлого запуска остался снимок,
// значит программа завершилась аварийно и настройки не вернулись. Возвращает
// true, если пришлось чинить.
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
	notifyWinINET()
	broadcastEnvChange()
	return true
}

func (m *Manager) saveState() {
	if m.snap == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return
	}
	if data, err := json.Marshal(m.snap); err == nil {
		os.WriteFile(m.statePath, data, 0o600)
	}
}

// Current показывает, что сейчас прописано в системе — для окна диагностики.
func Current() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE)
	if err != nil {
		return "неизвестно"
	}
	defer k.Close()
	on, _, _ := k.GetIntegerValue("ProxyEnable")
	srv, _, _ := k.GetStringValue("ProxyServer")
	if on == 0 {
		return "выключен"
	}
	if srv == "" {
		return "включён"
	}
	return "включён: " + srv
}

// notifyWinINET просит Windows и браузеры перечитать настройки прокси сразу,
// без перезапуска.
func notifyWinINET() {
	wininet := syscall.NewLazyDLL("wininet.dll")
	proc := wininet.NewProc("InternetSetOptionW")
	const optionSettingsChanged = 39
	const optionRefresh = 37
	proc.Call(0, optionSettingsChanged, 0, 0)
	proc.Call(0, optionRefresh, 0, 0)
}

// broadcastEnvChange рассылает WM_SETTINGCHANGE, чтобы новые процессы (и
// проводник, который их порождает) увидели изменённые переменные среды.
func broadcastEnvChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	env, _ := syscall.UTF16PtrFromString("Environment")
	var result uintptr
	proc.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 1000,
		uintptr(unsafe.Pointer(&result)))
}

// EnvHint — строки, которые пользователь может скопировать себе в терминал,
// если не хочет менять переменные среды глобально.
func EnvHint(httpAddr string) []string {
	url := "http://" + httpAddr
	return []string{
		`$env:HTTPS_PROXY="` + url + `"`,
		`$env:HTTP_PROXY="` + url + `"`,
		`$env:NO_PROXY="localhost,127.0.0.1"`,
	}
}

var _ = strings.TrimSpace
