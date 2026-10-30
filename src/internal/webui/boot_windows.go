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
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValueName = "ssh_tunnel"

// Сюда Диспетчер задач (вкладка «Автозагрузка») пишет, включена ли запись:
// первый байт нечётный — выключена, и Windows её молча пропускает.
const startupApprovedPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`

func platformBootSupported() bool { return true }

// runEntryExe — путь к программе из записи автозагрузки (в кавычках или без,
// с ключами или без).
func runEntryExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
		return strings.Trim(cmd, `"`)
	}
	if i := strings.IndexByte(cmd, ' '); i > 0 {
		return cmd[:i]
	}
	return cmd
}

// runEntry — путь из нашей записи автозагрузки; ok=false — записи нет.
func runEntry() (exe string, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	if err != nil {
		return "", false
	}
	return runEntryExe(v), true
}

func isSelf(exe string) bool {
	self, err := os.Executable()
	return err == nil && strings.EqualFold(filepath.Clean(exe), filepath.Clean(self))
}

func disabledInTaskManager() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(runValueName)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// platformBootEnabled — запись есть и запускает именно эту программу. Запись,
// оставшаяся от другой копии (VPN-версия, старый файл в «Загрузках»), не в
// счёт: галочка была бы включена, а запускалось бы что-то другое или ничего.
func platformBootEnabled() bool {
	exe, ok := runEntry()
	return ok && isSelf(exe)
}

func platformBootProblem() string {
	exe, ok := runEntry()
	if !ok {
		return ""
	}
	if !isSelf(exe) {
		if _, err := os.Stat(exe); err != nil {
			return "missing"
		}
		return "other"
	}
	if disabledInTaskManager() {
		return "taskmgr"
	}
	return ""
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
	// Если запись когда-то выключили в Диспетчере задач, Windows так и будет
	// её пропускать. Галочку здесь ставят, чтобы запускалось, — снимаем запрет.
	if a, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedPath, registry.SET_VALUE); err == nil {
		_ = a.DeleteValue(runValueName)
		a.Close()
	}
	return nil
}
