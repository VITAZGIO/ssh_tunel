//go:build !windows

package procinfo

// Process — запущенная программа для списка выбора.
type Process struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// List на не-Windows не реализован: список нужен ради выбора программ в
// настройках, а это возможность только Windows-версии.
func List() []Process { return nil }
