package winsvc

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// Окно при входе в систему — обычной записью в автозагрузке пользователя
// (HKCU\...\Run). С ядром в службе окну права администратора не нужны, и
// Windows запускает его отсюда без капризов. Имя записи то же, что у обычного
// ssh_tunnel.exe: одновременно работает только одна копия программы, и
// автозапуск VPN заменяет автозапуск прокси.

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "ssh_tunnel"
)

// SetGUIAutostart — запускать окно exe при входе, сразу в трей.
func SetGUIAutostart(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runValueName, syscall.EscapeArg(exe)+" -tray")
}

// RemoveGUIAutostart убирает окно из автозагрузки.
func RemoveGUIAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// RunEntryPointsTo — запись автозагрузки запускает exe. Так узнаётся
// автозапуск, включённый прежними версиями VPN-программы (их Windows молча
// не запускала), — чтобы перенести его на службу.
func RunEntryPointsTo(exe string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	if err != nil {
		return false
	}
	return SamePath(runEntryExe(v), exe)
}
