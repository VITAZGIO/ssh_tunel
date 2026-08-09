// vpstunnel-cli — консольная версия. Запускается флагами, пишет компактный
// лог в окно. Тем, кому нужны кнопки, а не команды, предназначен vpstunnel.exe
// с обычным окном — логика у них общая.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sshtunel/internal/app"
	"sshtunel/internal/config"
	"sshtunel/internal/events"
	"sshtunel/internal/shutdown"
)

func main() {
	cfg := config.Load()

	flag.StringVar(&cfg.Host, "host", cfg.Host, "адрес сервера")
	flag.IntVar(&cfg.SSHPort, "sshport", cfg.SSHPort, "SSH-порт сервера")
	flag.StringVar(&cfg.User, "user", cfg.User, "пользователь SSH")
	flag.StringVar(&cfg.KeyPath, "key", cfg.KeyPath, "путь к приватному ключу")
	flag.IntVar(&cfg.SocksPort, "port", cfg.SocksPort, "локальный порт SOCKS4/SOCKS5")
	flag.IntVar(&cfg.HTTPPort, "httpport", cfg.HTTPPort, "локальный порт HTTP-прокси")
	flag.IntVar(&cfg.PoolSize, "pool", cfg.PoolSize, "сколько SSH-соединений держать (больше — быстрее)")
	flag.BoolVar(&cfg.SysProxy, "sysproxy", cfg.SysProxy, "прописывать системный прокси Windows")
	flag.BoolVar(&cfg.SetEnvVars, "setenv", cfg.SetEnvVars, "прописывать HTTPS_PROXY в переменные среды (нужно для Claude Code, npm, pip)")
	flag.StringVar(&cfg.FilterMode, "filter", cfg.FilterMode,
		"какие программы вести через туннель: all (все), only (только указанные), except (все, кроме указанных)")
	apps := flag.String("apps", strings.Join(cfg.FilterApps, ","),
		"список программ для -filter через запятую, например steam.exe,discord.exe")
	flag.BoolVar(&cfg.LocalViaTunnel, "local-via-tunnel", cfg.LocalViaTunnel,
		"вести через сервер и локальную сеть (по умолчанию она идёт напрямую)")
	flag.BoolVar(&cfg.Verbose, "v", cfg.Verbose, "подробный лог")
	save := flag.Bool("save", false, "сохранить указанные настройки как значения по умолчанию и выйти")
	flag.Parse()

	cfg.FilterApps = splitApps(*apps)

	if cfg.Host == "" {
		usage()
		os.Exit(1)
	}
	if *save {
		if err := cfg.Save(); err != nil {
			fmt.Println("Не удалось сохранить настройки:", err)
			os.Exit(1)
		}
		fmt.Println("Настройки сохранены в", config.Path())
		return
	}

	a := app.New(cfg)
	a.RecoverStaleProxy()

	go printEvents(a.Bus, cfg.Verbose)

	fmt.Printf("Подключаюсь к %s:%d...\n", cfg.Host, cfg.SSHPort)
	if err := a.Start(); err != nil {
		fmt.Println("\nОшибка:", err)
		waitEnter()
		os.Exit(1)
	}

	fmt.Printf(`
  SOCKS4/SOCKS5  127.0.0.1:%d
  HTTP-прокси    127.0.0.1:%d

Готово. Держи окно открытым, пока нужен туннель. Ctrl+C — выйти.

`, cfg.SocksPort, cfg.HTTPPort)

	<-shutdown.OnExit(func() {
		fmt.Println("\nЗавершаю...")
		a.Stop()
	})
	time.Sleep(150 * time.Millisecond) // дать событиям выхода допечататься
}

// printEvents печатает компактный лог. Одна строка на соединение:
//
//	00:26:48  chrome.exe        → www.youtube.com:443
//	00:26:50  Discord.exe       → 162.159.128.233:443   DNS мимо туннеля
//
// Закрытия соединений в обычном режиме не печатаются вовсе: раньше половина
// вывода была строками "соединение закрыто штатно", в которых нет информации.
func printEvents(bus *events.Bus, verbose bool) {
	ch, _ := bus.Subscribe()

	// Одна страница открывает десятки соединений к одному хосту. Повторы в
	// пределах короткого окна сворачиваем, иначе лог не читается.
	type seen struct {
		at time.Time
		n  int
	}
	recent := map[string]*seen{}

	for e := range ch {
		ts := e.Time.Format("15:04:05")
		switch e.Kind {
		case events.KindConn:
			key := e.Process + "|" + e.Target
			if s, ok := recent[key]; ok && time.Since(s.at) < 5*time.Second && !e.Failed {
				s.n++
				s.at = time.Now()
				continue
			}
			recent[key] = &seen{at: time.Now(), n: 1}
			if len(recent) > 500 {
				recent = map[string]*seen{}
			}

			proc := e.Process
			if proc == "" {
				proc = "приложение"
			}
			if e.Failed {
				fmt.Printf("%s  %-18s ✗ %s — %s\n", ts, trim(proc, 18), e.Target, e.Error)
				continue
			}
			note := ""
			switch {
			case e.Direct:
				note = "   мимо туннеля (по правилам)"
			case e.DNSLeak:
				note = "   DNS мимо туннеля"
			}
			fmt.Printf("%s  %-18s → %s%s\n", ts, trim(proc, 18), e.Target, note)

		case events.KindLog:
			switch e.Level {
			case "error":
				fmt.Printf("%s  [!] %s\n", ts, e.Text)
			case "warn":
				fmt.Printf("%s  [~] %s\n", ts, e.Text)
			default:
				if verbose || !strings.HasPrefix(e.Text, "Ключ сервера") {
					fmt.Printf("%s  %s\n", ts, e.Text)
				}
			}

		case events.KindState:
			switch e.State {
			case events.StateConnected:
				fmt.Printf("%s  Соединение с сервером установлено (%s)\n", ts, e.Detail)
			case events.StateReconnecting:
				fmt.Printf("%s  [~] Связь потеряна, переподключаюсь: %s\n", ts, e.Detail)
			}
		}
	}
}

// splitApps разбирает список программ из флага. Пустые куски выбрасываются,
// чтобы запятая в конце не превращалась в безымянную запись.
func splitApps(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func usage() {
	fmt.Print(`ssh_tunel — SSH-туннель до своего сервера (SOCKS4/5 + HTTP-прокси)

Использование:
  vpstunnel-cli -host <IP сервера> [-user root] [-key путь-к-ключу]

Основные флаги:
  -host       адрес сервера (обязателен при первом запуске)
  -user       пользователь SSH (по умолчанию root)
  -key        приватный ключ (по умолчанию ~/.ssh/id_ed25519)
  -port       порт SOCKS4/SOCKS5 (по умолчанию 1080)
  -httpport   порт HTTP-прокси (по умолчанию 1081)
  -pool       число SSH-соединений, больше — выше скорость (по умолчанию 4)
  -filter     какие программы вести через туннель:
              all — все (по умолчанию)
              only — только указанные в -apps
              except — все, кроме указанных в -apps
  -apps       список программ для -filter через запятую
  -sysproxy   прописывать системный прокси Windows (по умолчанию да)
  -setenv     прописывать HTTPS_PROXY в переменные среды (по умолчанию да)
  -save       запомнить настройки, чтобы дальше запускать без флагов
  -v          подробный лог

Пример первого запуска:
  ssh_tunel-cli -host ТВОЙ_СЕРВЕР -user root -save

Дальше достаточно просто:
  vpstunnel-cli
`)
	waitEnter()
}

func waitEnter() {
	fmt.Println("\nНажми Enter для выхода...")
	fmt.Scanln()
}
