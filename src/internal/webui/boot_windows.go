//go:build windows

// Автозапуск на Windows — запись в реестре текущего пользователя
// (HKCU\...\Run), тот же механизм, которым автозагрузку себе прописывают
// большинство обычных программ. Никакого UAC и пароля не требуется: ключ
// целиком принадлежит текущему пользователю, права администратора ему не
// нужны — в отличие от Linux, где то же самое (работа без входа в систему)
// требует root.
//
// Исключение — версия с режимом VPN: программу с правами администратора
// Windows из Run не запускает, ей нужна задача в Планировщике (см.
// boot_task.go).
package webui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"sshtunnel/internal/updater"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValueName = "ssh_tunnel"

func platformBootSupported() bool { return true }

func platformBootEnabled() bool {
	if updater.VPN {
		return bootTaskExists()
	}
	_, ok := runValue()
	return ok
}

// platformBootLinger — понятие чисто Linux/systemd (служба без входа в
// систему); на Windows автозапуск через реестр и так срабатывает при входе
// пользователя без отдельной ручки, поэтому всегда false.
func platformBootLinger() bool { return false }

// platformBootTask — автозапуск идёт через Планировщик заданий, а не через
// реестр. Странице это нужно, чтобы не обещать «без прав администратора».
func platformBootTask() bool { return updater.VPN }

// platformUnitPath — файла службы на Windows нет, показывать нечего.
func platformUnitPath() string { return "" }

// platformSetBoot включает или выключает автозапуск. password и flags не
// нужны: путь к самой программе достаточен как команда запуска, а права
// пользователя на собственный HKCU и так хватает.
func platformSetBoot(enable bool, _ string, _ []string) error {
	if updater.VPN {
		return setBootTask(enable)
	}
	if !enable {
		return deleteRunValue()
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

// platformRepairBoot чинит автозапуск, прописанный прежними версиями. До
// задачи в Планировщике VPN-версия, как и обычная, записывала себя в Run — и
// при входе в систему Windows её молча пропускала. Такую запись переделываем в
// задачу, чтобы галочку не пришлось снимать и ставить заново. Заодно задача
// перенаправляется на exe, если его переложили в другую папку.
func platformRepairBoot() error {
	if !updater.VPN {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if v, ok := runValue(); ok && samePath(v, exe) {
		return setBootTask(true)
	}
	if bootTaskExists() && !bootTaskPointsTo(exe) {
		return setBootTask(true)
	}
	return nil
}

// setBootTask создаёт (или пересоздаёт) задачу автозапуска либо удаляет её.
func setBootTask(enable bool) error {
	if !enable {
		if bootTaskExists() {
			if out, err := schtasks("/Delete", "/TN", bootTaskName, "/F"); err != nil {
				return fmt.Errorf("не удалось удалить задачу автозапуска: %s", errText(out, err))
			}
		}
		return deleteOwnRunValue()
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("задачу автозапуска может создать только программа, запущенная от имени администратора")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не могу определить путь к самой программе: %w", err)
	}
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("не могу узнать, кто вошёл в систему: %w", err)
	}

	// schtasks читает описание задачи только из файла: кладём его во
	// временную папку и сразу после регистрации удаляем.
	f, err := os.CreateTemp("", "ssh_tunnel_task_*.xml")
	if err != nil {
		return fmt.Errorf("не удалось подготовить задачу автозапуска: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.Write(utf16LE(bootTaskXML(sid, exe)))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return fmt.Errorf("не удалось подготовить задачу автозапуска: %v", errors.Join(werr, cerr))
	}

	if out, err := schtasks("/Create", "/TN", bootTaskName, "/XML", path, "/F"); err != nil {
		return fmt.Errorf("не удалось создать задачу автозапуска: %s", errText(out, err))
	}

	// Запись в Run больше не нужна: если её оставила прежняя версия
	// VPN-программы, она всё равно не срабатывала. А если её оставил обычный
	// ssh_tunnel.exe, при входе в систему он бы наперегонки с VPN занимал
	// единственное место (одновременно работает только одна копия) — и сеть
	// устройств могла бы не подняться.
	return deleteRunValue()
}

func bootTaskExists() bool {
	_, err := schtasks("/Query", "/TN", bootTaskName)
	return err == nil
}

// bootTaskPointsTo — запускает ли задача именно этот exe. Если ответ schtasks
// не удалось разобрать, считаем, что нет: лишнее пересоздание задачи ничего
// не ломает, а задача на несуществующий файл оставила бы без автозапуска.
func bootTaskPointsTo(exe string) bool {
	out, err := schtasks("/Query", "/TN", bootTaskName, "/XML", "ONE")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(decodeConsole(out)), strings.ToLower(exe))
}

func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// schtasks запускает schtasks.exe без мелькающего чёрного окна консоли:
// программа собрана как оконная, и у дочерней консольной утилиты иначе
// появилось бы своё окно.
func schtasks(args ...string) ([]byte, error) {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.CombinedOutput()
}

// errText — понятная причина ошибки schtasks: его собственное сообщение (оно
// на языке системы), а если его нет — хотя бы код выхода.
func errText(out []byte, err error) string {
	if msg := strings.TrimSpace(decodeConsole(out)); msg != "" {
		return msg
	}
	return err.Error()
}

// decodeConsole переводит вывод консольной утилиты в нормальную строку.
// Windows пишет его в кодировке OEM (на русской системе — CP866), и как
// UTF-8 кириллица превратилась бы в кашу.
func decodeConsole(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const cpOEM = 1 // CP_OEMCP — текущая OEM-кодировка системы
	n, err := windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	u := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), &u[0], n)
	if err != nil || n == 0 {
		return string(b)
	}
	return windows.UTF16ToString(u[:n])
}

func runValue() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	return v, err == nil
}

func deleteRunValue() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("не удалось открыть раздел автозагрузки в реестре: %w", err)
	}
	defer k.Close()
	if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("не удалось выключить автозапуск: %w", err)
	}
	return nil
}

// deleteOwnRunValue убирает запись в Run, только если она ведёт на этот exe.
// Чужую — обычного ssh_tunnel.exe — при выключении автозапуска VPN не трогаем:
// человек мог включить её сам.
func deleteOwnRunValue() error {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if v, ok := runValue(); ok && samePath(v, exe) {
		return deleteRunValue()
	}
	return nil
}

// samePath сравнивает команду из Run (в кавычках) с путём к exe. Регистр в
// путях Windows не важен.
func samePath(cmd, exe string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(cmd), `"`), exe)
}
