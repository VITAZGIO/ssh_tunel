//go:build windows

// Автозапуск на Windows — запись в реестре текущего пользователя
// (HKCU\...\Run), тот же механизм, которым автозагрузку себе прописывают
// большинство обычных программ. Никакого UAC и пароля не требуется: ключ
// целиком принадлежит текущему пользователю, права администратора ему не
// нужны — в отличие от Linux, где то же самое (работа без входа в систему)
// требует root.
package webui

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValueName = "ssh_tunnel"

func platformBootSupported() bool { return true }

func platformBootEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	return err == nil
}

// platformBootLinger — понятие чисто Linux/systemd (служба без входа в
// систему); на Windows автозапуск через реестр и так срабатывает при входе
// пользователя без отдельной ручки, поэтому всегда false.
func platformBootLinger() bool { return false }

// platformUnitPath — файла службы на Windows нет, показывать нечего.
func platformUnitPath() string { return "" }

// platformSetBoot включает или выключает автозапуск. password и flags не
// нужны: путь к самой программе достаточен как команда запуска, а права
// пользователя на собственный HKCU и так хватает.
func platformSetBoot(enable bool, _ string, _ []string) error {
	if !enable {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("не удалось открыть раздел автозагрузки в реестре: %w", err)
		}
		defer k.Close()
		if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("не удалось выключить автозапуск: %w", err)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не могу определить путь к самой программе: %w", err)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("не удалось открыть раздел автозагрузки в реестре: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(runValueName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("не удалось включить автозапуск: %w", err)
	}
	return nil
}
