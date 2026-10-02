// ssh_tunnel_vpn — та же программа с окном, что и ssh_tunnel.exe, но трафик
// заворачивается в туннель не системным прокси, а виртуальным сетевым
// адаптером (Wintun). Через туннель идёт всё, что ходит в сеть, — в том числе
// программы, которые про прокси не знают.
//
// Создавать адаптер Windows разрешает только администраторам, поэтому ядро
// (туннель, адаптер, сеть устройств) работает одним из двух способов:
//
//   - службой Windows (галочка «Запускать при старте системы», см.
//     service_windows.go): поднимается при включении компьютера, ещё до входа
//     в систему, а окно — обычная программа без прав администратора, которая
//     показывает интерфейс службы (client_windows.go). Так устроены NetBird и
//     Tailscale: к компьютеру можно подключиться по RDP прямо с экрана ввода
//     пароля;
//   - в самом процессе окна (inproc_windows.go), если службы нет: тогда окно
//     запускается с правами администратора, через окно UAC.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"sshtunnel/internal/config"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/updater"
	"sshtunnel/vpn/internal/winsvc"
)

const windowTitle = "ssh_tunnel VPN"

func main() {
	noWindow := flag.Bool("nowindow", false, "не открывать окно, только напечатать адрес")
	tray := flag.Bool("tray", false, "запуститься свёрнутым в трей (так окно стартует при входе в систему)")
	service := flag.Bool("service", false, "работать службой Windows (так программу запускает сама Windows)")
	profile := flag.String("profile", "", "для -service: домашняя папка пользователя, чьи настройки брать")
	appdata := flag.String("appdata", "", "для -service: папка AppData\\Roaming этого пользователя")
	install := flag.Bool("install-service", false, "установить или обновить службу (нужны права администратора)")
	uninstall := flag.Bool("uninstall-service", false, "удалить службу (нужны права администратора)")
	relaunch := flag.Bool("relaunch", false, "после -uninstall-service снова открыть окно")
	waitInstance := flag.Bool("wait-instance", false, "дождаться, пока закроется прежняя копия программы")
	flag.Parse()

	updater.VPN = true

	switch {
	case *service:
		runService(*profile, *appdata)
		return
	case *install:
		os.Exit(cmdInstall())
	case *uninstall:
		os.Exit(cmdUninstall(*relaunch))
	}

	// Одна копия на компьютер — общая с обычным ssh_tunnel.exe: два туннеля
	// сразу друг другу бы только мешали. Если уже запущен любой, показываем его.
	if !*noWindow {
		if *waitInstance {
			if !nativeui.WaitSingleInstance(20 * time.Second) {
				return
			}
		} else if nativeui.AlreadyRunning(windowTitle) {
			return
		}
	}

	if winsvc.Installed() {
		runClient(*tray, *noWindow)
		return
	}

	if !winsvc.Elevated() {
		// Службы нет — ядро будет работать в процессе окна, а адаптер требует
		// прав администратора. Перезапускаемся через окно UAC; новая копия
		// дождётся, пока эта выйдет и освободит место.
		args := []string{"-wait-instance"}
		if *tray {
			args = append(args, "-tray")
		}
		if *noWindow {
			args = append(args, "-nowindow")
		}
		self, _ := os.Executable()
		if _, err := winsvc.RunElevated(self, args, false); err != nil {
			fatal("Режиму VPN нужны права администратора: без них Windows не даёт\n" +
				"создать сетевой адаптер.\n\n" +
				"Подтверди запрос Windows при запуске. Чтобы он больше не появлялся,\n" +
				"включи в настройках «Запускать при старте системы» — программа\n" +
				"установит себя службой.")
		}
		return
	}

	// Автозапуск, включённый прежними версиями (запись в реестре или задача
	// Планировщика), Windows для программы с правами администратора не
	// выполняла. Раз его включали — переносим на службу.
	if migrateOldAutostart() {
		runClient(*tray, *noWindow)
		return
	}

	runInProcess(*tray, *noWindow)
}

// migrateOldAutostart ставит службу вместо автозапуска прежних версий.
// true — служба поставлена и запущена, дальше окно работает с ней.
func migrateOldAutostart() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if !winsvc.RunEntryPointsTo(self) && !winsvc.OldTaskExists() {
		return false
	}
	// Служба нужна, чтобы всё поднималось само, — значит, и подключаться ей
	// надо сразу.
	if cfg := config.Load(); !cfg.AutoStart {
		cfg.AutoStart = true
		_ = cfg.Save()
	}
	if err := installService(true); err != nil {
		fmt.Fprintln(os.Stderr, "перенос автозапуска на службу:", err)
		return false
	}
	return true
}

// installService ставит (или обновляет) службу для текущего пользователя и
// окно в его автозагрузку. Вызывается в процессе с правами администратора.
func installService(start bool) error {
	err := winsvc.Install(winsvc.InstallOptions{
		Profile: os.Getenv("USERPROFILE"),
		AppData: os.Getenv("APPDATA"),
		Start:   start,
	})
	if err != nil {
		return err
	}
	winsvc.RemoveOldTask()
	if err := winsvc.SetGUIAutostart(winsvc.InstalledExe()); err != nil {
		fmt.Fprintln(os.Stderr, "окно в автозагрузку:", err)
	}
	return nil
}

// cmdInstall — ssh_tunnel_vpn.exe -install-service: запускается окном через
// UAC, когда надо обновить службу новой версией файла.
func cmdInstall() int {
	if err := installService(true); err != nil {
		showMessage("Не удалось установить службу:\n\n"+err.Error(), mbIconError)
		return 1
	}
	return 0
}

// cmdUninstall — ssh_tunnel_vpn.exe -uninstall-service: снимает галочку
// «Запускать при старте системы». relaunch — потом снова открыть окно (уже
// без службы, ядро в процессе окна).
func cmdUninstall(relaunch bool) int {
	if err := winsvc.Uninstall(); err != nil {
		showMessage("Не удалось удалить службу:\n\n"+err.Error(), mbIconError)
		return 1
	}
	_ = winsvc.RemoveGUIAutostart()
	winsvc.RemoveOldTask()
	if relaunch {
		startSelf("-wait-instance")
	}
	return 0
}

const (
	mbIconError    = 0x00000010
	mbIconQuestion = 0x00000020
	mbYesNo        = 0x00000004
	idYes          = 6
)

func showMessage(msg string, flags uint32) int32 {
	title, _ := windows.UTF16PtrFromString(windowTitle)
	text, _ := windows.UTF16PtrFromString(msg)
	r, _ := windows.MessageBox(0, text, title, flags)
	return r
}

func fatal(msg string) {
	showMessage(msg, mbIconError)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
