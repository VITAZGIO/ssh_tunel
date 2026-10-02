package winsvc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// InstallDir — куда служба ставит свою копию exe. Program Files, а не папка,
// откуда запустили: служба работает от имени SYSTEM, и её файл не должен
// лежать там, где его может подменить обычная программа пользователя (в
// «Загрузках» это может кто угодно). К тому же скачанный файл новой версии
// иначе было бы не положить поверх: exe работающей службы занят.
func InstallDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "ssh_tunnel")
}

// InstalledExe — путь к копии exe, которую запускает служба.
func InstalledExe() string { return filepath.Join(InstallDir(), "ssh_tunnel_vpn.exe") }

// SamePath — один и тот же файл (регистр в путях Windows не важен).
func SamePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// openService открывает службу с минимальными правами — так проверять и
// запускать её может и окно без прав администратора (см. serviceDACL).
func openService(access uint32) (*mgr.Service, func(), error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, err
	}
	name, _ := windows.UTF16PtrFromString(Name)
	h, err := windows.OpenService(scm, name, access)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return nil, nil, err
	}
	s := &mgr.Service{Name: Name, Handle: h}
	return s, func() { s.Close(); windows.CloseServiceHandle(scm) }, nil
}

// Installed — служба установлена.
func Installed() bool {
	_, done, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	done()
	return true
}

// Running — служба установлена и работает.
func Running() bool {
	s, done, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer done()
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}

// Start запускает службу, если она стоит. Без прав администратора тоже
// можно: установка разрешает это вошедшим в систему (см. serviceDACL).
func Start() error {
	s, done, err := openService(windows.SERVICE_QUERY_STATUS | windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer done()
	if st, err := s.Query(); err == nil && (st.State == svc.Running || st.State == svc.StartPending) {
		return nil
	}
	err = s.Start()
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}

// InstallOptions — с чем ставить службу.
type InstallOptions struct {
	// Profile и AppData — домашняя папка и AppData\Roaming пользователя.
	// Служба работает от имени SYSTEM, у которого свои, пустые: без них она
	// не нашла бы ни настроек, ни ключей.
	Profile, AppData string
	// Start — запустить сразу после установки.
	Start bool
}

// Install ставит службу или обновляет уже стоящую: копирует свой exe в
// Program Files, прописывает автоматический запуск при включении компьютера и
// перезапуск при сбое. Нужны права администратора.
func Install(opt InstallOptions) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if opt.Profile == "" || opt.AppData == "" {
		return errors.New("не знаю, чьи настройки брать службе")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("нет доступа к службам Windows (нужны права администратора): %w", err)
	}
	defer m.Disconnect()

	existing, err := m.OpenService(Name)
	if err == nil {
		// Работающая служба держит свой exe — останавливаем, прежде чем
		// класть новый.
		stopAndWait(existing, 20*time.Second)
	}

	target := InstalledExe()
	if !SamePath(self, target) {
		if err := copyExe(self, target); err != nil {
			if existing != nil {
				existing.Close()
			}
			return fmt.Errorf("не удалось скопировать программу в %s: %w", InstallDir(), err)
		}
	}

	args := []string{"-service", "-profile", opt.Profile, "-appdata", opt.AppData}
	cfg := mgr.Config{
		DisplayName:  DisplayName,
		Description:  Description,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		// Сети при включении компьютера может ещё не быть — служба сама
		// подождёт её (app.StartOnLaunch), отложенный старт не нужен: чем
		// раньше она поднимется, тем раньше компьютер виден в сети.
	}
	var s *mgr.Service
	if existing != nil {
		s = existing
		cur, err := s.Config()
		if err != nil {
			s.Close()
			return err
		}
		cur.DisplayName, cur.Description = cfg.DisplayName, cfg.Description
		cur.StartType, cur.ErrorControl = cfg.StartType, cfg.ErrorControl
		cur.BinaryPathName = commandLine(target, args)
		if err := s.UpdateConfig(cur); err != nil {
			s.Close()
			return fmt.Errorf("не удалось обновить службу: %w", err)
		}
	} else {
		s, err = m.CreateService(Name, target, cfg, args...)
		if err != nil {
			return fmt.Errorf("не удалось создать службу: %w", err)
		}
	}
	defer s.Close()

	// Упала — поднять снова: через 5 секунд, потом через 10 и 30. Счётчик
	// сбрасывается через сутки без сбоев.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*60*60)
	if err := allowUsersToStart(s.Handle); err != nil {
		// Не смертельно: окно просто не сможет само поднять остановленную
		// службу, а при включении компьютера её и так запустит Windows.
		fmt.Fprintln(os.Stderr, "права на службу:", err)
	}
	if opt.Start {
		if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return fmt.Errorf("служба установлена, но не запустилась: %w", err)
		}
	}
	return nil
}

// Uninstall останавливает и удаляет службу. Копия exe в Program Files
// остаётся: её может занимать открытое окно, а лишний файл ничему не мешает.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("нет доступа к службам Windows (нужны права администратора): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return nil // и так нет
	}
	defer s.Close()
	stopAndWait(s, 20*time.Second)
	if err := s.Delete(); err != nil {
		return fmt.Errorf("не удалось удалить службу: %w", err)
	}
	return nil
}

func stopAndWait(s *mgr.Service, timeout time.Duration) {
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return
	}
	_, _ = s.Control(svc.Stop)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// copyExe кладёт exe в Program Files. Прежний файл может быть занят — его
// держит открытое окно, запущенное из этой папки. Занятый exe Windows не даёт
// перезаписать, но даёт переименовать: старый уходит в .old (окно доработает
// на нём до перезапуска), новый ложится на его место.
func copyExe(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	old := dst + ".old"
	os.Remove(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return nil
}

// commandLine — строка запуска службы с кавычками там, где они нужны (путь
// с пробелами: «Program Files», «C:\Users\Иван Петров»).
func commandLine(exe string, args []string) string {
	parts := []string{syscall.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

// serviceDACL — кому что можно делать со службой. Как у служб по умолчанию
// (SYSTEM и администраторам — всё, остальным — смотреть состояние), плюс
// вошедшим в систему (IU) — запускать (RP). Окну без прав администратора это
// нужно, чтобы поднять службу, если её остановили. Останавливать (WP) обычным
// пользователям по-прежнему нельзя.
const serviceDACL = "D:" +
	"(A;;CCLCSWRPWPDTLOCRRC;;;SY)" +
	"(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" +
	"(A;;CCLCSWRPLOCRRC;;;IU)" +
	"(A;;CCLCSWLOCRRC;;;SU)"

func allowUsersToStart(h windows.Handle) error {
	sd, err := windows.SecurityDescriptorFromString(serviceDACL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// RemoveOldTask убирает задачу Планировщика «ssh_tunnel VPN», которой
// автозапуск был устроен в одной из промежуточных сборок: служба её
// полностью заменяет, а вместе они запускали бы программу дважды.
func RemoveOldTask() {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
	cmd := exec.Command(exe, "/Delete", "/TN", "ssh_tunnel VPN", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	_ = cmd.Run()
}

// OldTaskExists — та самая задача ещё стоит (значит, автозапуск был включён
// и его надо перенести на службу).
func OldTaskExists() bool {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
	cmd := exec.Command(exe, "/Query", "/TN", "ssh_tunnel VPN")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.Run() == nil
}
