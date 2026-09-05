// ssh_tunnel — приложение с окном. Запускается двойным щелчком, никаких команд
// и флагов: всё настраивается в окне и запоминается.
//
// Собирается с -H windowsgui, поэтому чёрного окна консоли за ним не
// появляется. Окно — настоящее окно Windows со своей иконкой и местом в
// панели задач; внутри него рисует WebView2 (движок Edge, встроенный в
// систему). Крестик прячет окно в трей, чтобы туннель не рвался от случайного
// закрытия; выход — через меню у значка рядом с часами.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/shutdown"
	"sshtunnel/internal/webui"
)

const windowTitle = "ssh_tunnel"

func main() {
	// Единственный флаг — на случай, если окно почему-то не открывается:
	// тогда программа печатает адрес, который можно открыть браузером.
	noWindow := flag.Bool("nowindow", false, "не открывать окно, только напечатать адрес")
	flag.Parse()

	// Вторая копия не нужна: она всё равно не займёт уже занятые порты.
	// Вместо неё показываем окно той, что уже работает.
	if !*noWindow && nativeui.AlreadyRunning(windowTitle) {
		return
	}

	cfg := config.Load()
	// В окне этих переключателей нет: без них туннелем никто не пользуется сам
	// собой, и человек видел бы «подключено» при неработающем интернете.
	// Управлять ими можно только в консольной версии флагами.
	if runtime.GOOS == "windows" {
		cfg.SysProxy = true
		cfg.SetEnvVars = true
	}
	a := app.New(cfg)
	a.RecoverStaleProxy()

	srv, err := webui.New(a)
	if err != nil {
		fatal("Не удалось запустить интерфейс: " + err.Error())
	}
	go srv.Serve()

	// Прокси обязан сняться при любом завершении, иначе система продолжит
	// слать трафик на порт, который уже никто не слушает, и интернет пропадёт.
	go func() {
		<-shutdown.OnExit(func() { a.Stop() })
		os.Exit(0)
	}()

	go showStateInTray(a)

	if cfg.AutoStart && cfg.Active().Host != "" {
		go func() {
			time.Sleep(300 * time.Millisecond) // дать окну подписаться на события
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
			"Обычно он уже есть в Windows 10 и 11 вместе с Edge. Если его нет,\n" +
			"поставь «Microsoft Edge WebView2 Runtime» с сайта Microsoft.\n\n" +
			"Пока его нет, интерфейс можно открыть в браузере — запусти так:\n" +
			"ssh_tunnel.exe -nowindow")
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
		OnQuit: a.Stop,
	})
	if err != nil {
		fatal(err.Error())
	}
}

// showStateInTray держит подсказку у значка в трее в актуальном состоянии,
// чтобы состояние было видно, не открывая окно.
func showStateInTray(a *app.App) {
	ch, _ := a.Bus.Subscribe()
	names := map[string]string{
		events.StateStopped:      "отключено",
		events.StateConnecting:   "подключаюсь",
		events.StateConnected:    "защищено",
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

// fatal показывает сообщение так, чтобы его увидели и без консоли.
func fatal(msg string) {
	showMessage(msg)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
