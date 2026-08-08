//go:build !windows

// Заглушки, чтобы весь остальной код собирался и тестировался на Linux и macOS.
// Нативное окно существует только в версии для Windows — она и есть цель.
package nativeui

import "errors"

type Options struct {
	Title    string
	URL      string
	Width    int
	Height   int
	Running  func() bool
	Toggle   func()
	OnQuit   func()
	DataPath string
}

func Run(opts Options) error {
	return errors.New("нативное окно поддерживается только на Windows")
}

func AlreadyRunning(title string) bool { return false }
func WebView2Installed() bool          { return false }
func SetStatus(text string)            {}
