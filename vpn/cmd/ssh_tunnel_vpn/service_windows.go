package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/internal/winsvc"
	"sshtunnel/vpn/vpnlayer"
)

// runService — ssh_tunnel_vpn.exe -service, его запускает сама Windows при
// включении компьютера. Окна и рабочего стола у службы нет: её интерфейс
// показывает обычная копия программы (см. client_windows.go).
func runService(profile, appdata string) {
	// Служба работает от имени SYSTEM, у которого свои пустые папки.
	// Настройки, ключи и известные серверы — пользователя, который ставил
	// службу: подставляем его папки, и весь остальной код (config.Dir, поиск
	// ключа, «Загрузки» для обновлений) находит их сам.
	if profile != "" {
		os.Setenv("USERPROFILE", profile)
	}
	if appdata != "" {
		os.Setenv("APPDATA", appdata)
	}
	vpnlayer.WintunDir = filepath.Join(winsvc.InstallDir(), "wintun")

	if err := svc.Run(winsvc.Name, &service{}); err != nil {
		fmt.Fprintln(os.Stderr, "служба:", err)
		os.Exit(1)
	}
}

type service struct{}

func (*service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	dir := config.Dir()
	e, err := newEngine()
	if err != nil {
		writeServiceLog(dir, "не запустился интерфейс: "+err.Error())
		return true, 1
	}
	go logToFile(dir, e.a.Bus)
	// Галочку автозапуска из службы не снять: служба удаляет себя только из
	// окна, через окно UAC (см. clientBoot).
	webui.SetBootControl(serviceBoot{})

	if err := winsvc.WriteEndpoint(dir, winsvc.Endpoint{
		Addr: e.srv.Addr(), Token: e.srv.Token(), Version: updater.Version, PID: os.Getpid(),
	}); err != nil {
		writeServiceLog(dir, "не удалось записать service.json: "+err.Error())
	}
	// Удалённо включённый компьютер должен появиться в сети, когда бы ни
	// поднялась его сеть, — поэтому ждём её без срока.
	e.autoConnect(0, 0)

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			winsvc.RemoveEndpoint(dir)
			e.a.StopNow()
			e.srv.Close()
			return false, 0
		}
	}
	return false, 0
}

// serviceBoot — галочка автозапуска глазами самой службы: раз служба
// работает, автозапуск включён.
type serviceBoot struct{}

func (serviceBoot) Enabled() bool { return true }
func (serviceBoot) Set(enable bool) error {
	if enable {
		return nil
	}
	return fmt.Errorf("автозапуск выключается в окне программы")
}

// Журнал службы — файлом в папке настроек: окна у службы нет, а понять,
// почему VPN не поднялся при включении компьютера, иногда надо.
const serviceLogMax = 1 << 20

func serviceLogPath(dir string) string { return filepath.Join(dir, "service.log") }

func writeServiceLog(dir, line string) {
	f, err := os.OpenFile(serviceLogPath(dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

func logToFile(dir string, bus *events.Bus) {
	// Старый журнал не копим бесконечно: перевалил за мегабайт — начинаем
	// заново, прежний остаётся рядом одной копией.
	path := serviceLogPath(dir)
	if st, err := os.Stat(path); err == nil && st.Size() > serviceLogMax {
		os.Rename(path, path+".old")
	}
	writeServiceLog(dir, "служба запущена, версия "+updater.Version)
	ch, _ := bus.Subscribe()
	for e := range ch {
		switch e.Kind {
		case events.KindLog:
			writeServiceLog(dir, e.Level+": "+e.Text)
		case events.KindState:
			writeServiceLog(dir, "состояние: "+e.State+" "+e.Detail)
		}
	}
}
