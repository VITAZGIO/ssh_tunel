//go:build !windows

package filedialog

import "errors"

// ErrCancelled — пользователь закрыл диалог, ничего не выбрав.
var ErrCancelled = errors.New("выбор отменён")

// PickExecutable — системный диалог есть только в версии для Windows.
func PickExecutable() (string, error) {
	return "", errors.New("выбор файла поддерживается только на Windows")
}
