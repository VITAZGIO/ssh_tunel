// vpstunnel — версия с окном. Запускается двойным щелчком, без команд и
// флагов: всё настраивается в окне и запоминается.
//
// Собирается с -H windowsgui, поэтому чёрного окна консоли за ней не
// появляется. Само окно — это страница, открытая браузером в режиме
// приложения (без адресной строки и вкладок).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"vpstunnel/internal/app"
	"vpstunnel/internal/config"
	"vpstunnel/internal/shutdown"
	"vpstunnel/internal/webui"
)

func main() {
	// Единственный флаг: не открывать окно самому, а напечатать адрес. Нужен,
	// если браузер не находится или окно хочется открыть вручную.
	noWindow := flag.Bool("nowindow", false, "не открывать окно, только напечатать адрес")
	flag.Parse()

	cfg := config.Load()
	a := app.New(cfg)
	a.RecoverStaleProxy()

	srv, err := webui.New(a)
	if err != nil {
		fatal("Не удалось запустить интерфейс: " + err.Error())
	}

	go func() {
		if err := srv.Serve(); err != nil {
			// Сервер падает только при закрытии — это штатный выход.
			_ = err
		}
	}()

	// Прокси обязан сняться при любом закрытии окна, иначе система продолжит
	// слать трафик на порт, который уже никто не слушает, и интернет пропадёт.
	go func() {
		<-shutdown.OnExit(func() { a.Stop() })
		os.Exit(0)
	}()

	if cfg.AutoStart && cfg.Host != "" {
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
	} else if err := openWindow(url); err != nil {
		fatal("Не удалось открыть окно программы.\n\n" +
			"Открой этот адрес в браузере вручную:\n" + url)
	}

	// Процесс живёт, пока не закроют его явно. Закрытие окна браузера мы не
	// отслеживаем специально: пользователь может закрыть вкладку, но захотеть
	// оставить туннель — выход делается кнопкой в трее браузера или Ctrl+C.
	select {}
}

// openWindow пытается открыть страницу в режиме приложения — так окно выглядит
// как обычная программа, без адресной строки, вкладок и закладок. Если ни
// одного подходящего браузера нет, открываем как обычную ссылку.
func openWindow(url string) error {
	arg := "--app=" + url + " --window-size=1000,760"
	for _, exe := range appModeBrowsers() {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		cmd := exec.Command(exe, "--app="+url, "--window-size=1000,760")
		if err := cmd.Start(); err == nil {
			return nil
		}
	}
	_ = arg
	return openDefault(url)
}

func appModeBrowsers() []string {
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		pf86 := os.Getenv("ProgramFiles(x86)")
		local := os.Getenv("LOCALAPPDATA")
		return []string{
			filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		}
	case "darwin":
		return []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	default:
		return []string{"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser"}
	}
}

func openDefault(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// fatal показывает сообщение так, чтобы его увидели и без консоли.
func fatal(msg string) {
	showMessage(msg)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
