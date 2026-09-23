// ssh_tunnel_vpn — та же программа с окном, что и ssh_tunnel.exe, но трафик
// заворачивается в туннель не системным прокси, а виртуальным сетевым
// адаптером (Wintun). Через туннель идёт всё, что ходит в сеть, — в том числе
// программы, которые про прокси не знают.
//
// Цена — права администратора: создавать сетевые адаптеры и менять маршруты
// Windows разрешает только им. Поэтому это отдельный exe, а обычный
// ssh_tunnel.exe остаётся лёгким и без запроса UAC.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/shutdown"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/winvpn"
)

const windowTitle = "ssh_tunnel VPN"

func main() {
	noWindow := flag.Bool("nowindow", false, "не открывать окно, только напечатать адрес")
	flag.Parse()

	if !windows.GetCurrentProcessToken().IsElevated() {
		fatal("Режиму VPN нужны права администратора: без них Windows не даёт\n" +
			"создать сетевой адаптер.\n\n" +
			"Запусти ssh_tunnel_vpn.exe правой кнопкой → «Запуск от имени администратора».")
	}

	// Одна копия на компьютер — общая с обычным ssh_tunnel.exe: два туннеля
	// сразу друг другу бы только мешали. Если уже запущен любой, показываем его.
	if !*noWindow && nativeui.AlreadyRunning(windowTitle) {
		return
	}

	updater.WindowsAsset = "ssh_tunnel_vpn.exe"

	cfg := config.Load()
	a := app.New(cfg)
	a.SetNetLayer(winvpn.New(a.Bus))
	// Системный прокси мог остаться от аварийно закрытого ssh_tunnel.exe —
	// вместе с VPN он только мешал бы.
	a.RecoverStaleProxy()

	srv, err := webui.New(a)
	if err != nil {
		fatal("Не удалось запустить интерфейс: " + err.Error())
	}
	go srv.Serve()

	// Адаптер снимается при любом завершении. Даже если процесс упадёт, Windows
	// уберёт адаптер и фильтры сама — они привязаны к процессу.
	go func() {
		<-shutdown.OnExit(func() { a.StopNow() })
		os.Exit(0)
	}()

	go showStateInTray(a)

	if cfg.AutoStart && cfg.Active().Host != "" {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := a.Start(); err != nil {
				a.Bus.Errorf("Автозапуск не удался: %v", err)
			}
		}()
	}

	url := srv.URL()
	if *noWindow {
		fmt.Println(url)
		select {}
	}

	if !nativeui.WebView2Installed() {
		fatal("Не найден компонент WebView2, на котором рисуется окно.\n\n" +
			"Поставь «Microsoft Edge WebView2 Runtime» с сайта Microsoft\n" +
			"или запусти так: ssh_tunnel_vpn.exe -nowindow")
	}

	err = nativeui.Run(nativeui.Options{
		Title:    windowTitle,
		URL:      url,
		Width:    420,
		Height:   700,
		DataPath: config.Dir(),
		Running:  a.Running,
		Toggle: func() {
			if a.Running() {
				a.Stop()
				return
			}
			if err := a.Start(); err != nil {
				a.Bus.Errorf("%v", err)
			}
		},
		OnQuit: a.StopNow,
	})
	if err != nil {
		fatal(err.Error())
	}
}

func showStateInTray(a *app.App) {
	ch, _ := a.Bus.Subscribe()
	names := map[string]string{
		events.StateStopped:      "VPN выключен",
		events.StateConnecting:   "подключаюсь",
		events.StateConnected:    "VPN включён",
		events.StateReconnecting: "связь потеряна",
		events.StateError:        "ошибка",
	}
	for e := range ch {
		if e.Kind != events.KindState {
			continue
		}
		if name, ok := names[e.State]; ok {
			nativeui.SetStatus(name)
		}
	}
}

func fatal(msg string) {
	title, _ := windows.UTF16PtrFromString(windowTitle)
	text, _ := windows.UTF16PtrFromString(msg)
	const mbIconError = 0x00000010
	windows.MessageBox(0, text, title, mbIconError)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
