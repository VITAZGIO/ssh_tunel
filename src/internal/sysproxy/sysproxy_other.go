//go:build !windows && !linux

// На не-Windows системах системный прокси не трогаем: программа там нужна
// только для разработки и тестов, а способ настройки прокси в каждом рабочем
// столе свой. Заглушки позволяют собирать и тестировать весь остальной код.
package sysproxy

type Manager struct{}

func NewManager(configDir string) *Manager { return &Manager{} }

func (m *Manager) Enable(httpAddr, socksAddr string, setEnv, bypassLocal bool, extra []string) error {
	return nil
}
func (m *Manager) Disable() error     { return nil }
func (m *Manager) RecoverStale() bool { return false }

func Current() string { return "не поддерживается на этой ОС" }

func EnvHint(httpAddr string) []string {
	url := "http://" + httpAddr
	return []string{
		`export HTTPS_PROXY=` + url,
		`export HTTP_PROXY=` + url,
		`export NO_PROXY=` + noProxyList(true),
	}
}
