// ssh_tunnel_cli — консольная версия. Запускается флагами, пишет компактный
// лог в окно. Тем, кому нужны кнопки, а не команды, предназначен ssh_tunnel.exe
// с обычным окном — логика у них общая.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/shutdown"
)

func main() {
	cfg := config.Load()
	// Флаги правят активный сервер: у консольной версии одна вкладка, и это
	// она. Несколько серверов заводятся и переключаются уже в веб-панели.
	p := cfg.Active()

	flag.StringVar(&p.Host, "host", p.Host, "адрес сервера")
	flag.IntVar(&p.SSHPort, "sshport", p.SSHPort, "SSH-порт сервера")
	flag.StringVar(&p.User, "user", p.User, "пользователь SSH")
	flag.StringVar(&p.KeyPath, "key", p.KeyPath, "путь к приватному ключу")
	flag.IntVar(&p.SocksPort, "port", p.SocksPort, "локальный порт SOCKS4/SOCKS5")
	flag.IntVar(&p.HTTPPort, "httpport", p.HTTPPort, "локальный порт HTTP-прокси")
	flag.IntVar(&p.PoolSize, "pool", p.PoolSize, "сколько SSH-соединений держать (больше — быстрее)")
	flag.BoolVar(&cfg.SysProxy, "sysproxy", cfg.SysProxy, "прописывать системный прокси Windows")
	flag.BoolVar(&cfg.SetEnvVars, "setenv", cfg.SetEnvVars, "прописывать HTTPS_PROXY в переменные среды (нужно для Claude Code, npm, pip)")
	flag.StringVar(&p.FilterMode, "filter", p.FilterMode,
		"какие программы вести через туннель: all (все), only (только указанные), except (все, кроме указанных)")
	apps := flag.String("apps", strings.Join(p.FilterApps, ","),
		"список программ для -filter через запятую, например steam.exe,discord.exe")
	flag.BoolVar(&p.LocalViaTunnel, "local-via-tunnel", p.LocalViaTunnel,
		"вести через сервер и локальную сеть (по умолчанию она идёт напрямую)")
	direct := flag.String("direct", strings.Join(p.DirectHosts, ","),
		"адреса и сети, которые всегда идут напрямую (через запятую)")
	flag.BoolVar(&cfg.Verbose, "v", cfg.Verbose, "подробный лог")
	save := flag.Bool("save", false, "сохранить указанные настройки как значения по умолчанию и выйти")
	flag.Parse()

	p.FilterApps = splitApps(*apps)
	p.DirectHosts = routing.SplitEntries(*direct)
	cfg.SetProfile(p)

	if p.Host == "" {
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

	fmt.Printf("Подключаюсь к %s:%d...\n", p.Host, p.SSHPort)
	if err := a.Start(); err != nil {
		fmt.Println("\nОшибка:", err)
		waitEnter()
		os.Exit(1)
	}

	fmt.Printf(`
  SOCKS4/SOCKS5  127.0.0.1:%d
  HTTP-прокси    127.0.0.1:%d

Готово. Держи окно открытым, пока нужен туннель. Ctrl+C — выйти.

`, p.SocksPort, p.HTTPPort)

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
	fmt.Print(`ssh_tunnel — SSH-туннель до своего сервера (SOCKS4/5 + HTTP-прокси)

Использование:
  ssh_tunnel_cli -host <IP сервера> [-user root] [-key путь-к-ключу]

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
  -direct     адреса и сети, которые всегда идут напрямую (через запятую)
  -sysproxy   прописывать системный прокси Windows (по умолчанию да)
  -setenv     прописывать HTTPS_PROXY в переменные среды (по умолчанию да)
  -save       запомнить настройки, чтобы дальше запускать без флагов
  -v          подробный лог

Пример первого запуска:
  ssh_tunnel_cli -host ТВОЙ_СЕРВЕР -user root -save

Дальше достаточно просто:
  ssh_tunnel_cli
`)
	waitEnter()
}

func waitEnter() {
	fmt.Println("\nНажми Enter для выхода...")
	fmt.Scanln()
}
