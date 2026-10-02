package main

import (
	"fmt"
	"os"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/shutdown"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/internal/winsvc"
)

// runInProcess — ядро в процессе окна, без службы. Процесс уже с правами
// администратора.
func runInProcess(tray, noWindow bool) {
	e, err := newEngine()
	if err != nil {
		fatal("Не удалось запустить интерфейс: " + err.Error())
	}
	a := e.a
	// Системный прокси мог остаться от аварийно закрытого ssh_tunnel.exe —
	// вместе с VPN он только мешал бы.
	a.RecoverStaleProxy()
	webui.SetBootControl(&inprocBoot{a: a})

	// Адаптер снимается при любом завершении. Даже если процесс упадёт, Windows
	// уберёт адаптер и фильтры сама — они привязаны к процессу.
	go func() {
		<-shutdown.OnExit(func() { a.StopNow() })
		os.Exit(0)
	}()

	go showStateInTray(a)
	e.autoConnect(300*time.Millisecond, 3*time.Minute)

	url := e.srv.URL()
	if noWindow {
		fmt.Println(url)
		select {}
	}
	runWindow(url, tray, a.Running, toggleApp(a), a.StopNow)
}

func toggleApp(a *app.App) func() {
	return func() {
		if a.Running() {
			a.Stop()
			return
		}
		if err := a.Start(); err != nil {
			a.Bus.Errorf("%v", err)
		}
	}
}

func runWindow(url string, tray bool, running func() bool, toggle, onQuit func()) {
	if !nativeui.WebView2Installed() {
		fatal("Не найден компонент WebView2, на котором рисуется окно.\n\n" +
			"Поставь «Microsoft Edge WebView2 Runtime» с сайта Microsoft\n" +
			"или запусти так: ssh_tunnel_vpn.exe -nowindow")
	}
	err := nativeui.Run(nativeui.Options{
		Title:       windowTitle,
		URL:         url,
		Width:       420,
		Height:      700,
		DataPath:    config.Dir(),
		Running:     running,
		Toggle:      toggle,
		OnQuit:      onQuit,
		StartHidden: tray,
	})
	if err != nil {
		fatal(err.Error())
	}
}

// inprocBoot — галочка «Запускать при старте системы», пока службы нет.
// Включение ставит службу и передаёт ей работу: этот процесс гасит своё ядро
// (адаптер один на компьютер), служба поднимает своё, а окно перезапускается
// уже как окно службы.
type inprocBoot struct{ a *app.App }

func (b *inprocBoot) Enabled() bool { return winsvc.Installed() }

func (b *inprocBoot) Set(enable bool) error {
	if !enable {
		return winsvc.RemoveGUIAutostart()
	}
	// Служба нужна, чтобы при включении компьютера всё поднялось само, —
	// значит, и подключаться ей надо сразу.
	cfg := b.a.Config()
	if !cfg.AutoStart {
		cfg.AutoStart = true
		if _, err := b.a.SetConfig(cfg); err != nil {
			return err
		}
	}
	if err := installService(false); err != nil {
		return err
	}
	b.a.Bus.Infof("Служба установлена — передаю ей работу, окно перезапустится")
	go func() {
		// Дать странице получить ответ.
		time.Sleep(700 * time.Millisecond)
		b.a.StopNow()
		if err := winsvc.Start(); err != nil {
			showMessage("Служба установлена, но не запустилась:\n\n"+err.Error(), mbIconError)
		}
		startExe(winsvc.InstalledExe(), "-wait-instance")
		nativeui.Quit()
	}()
	return nil
}
