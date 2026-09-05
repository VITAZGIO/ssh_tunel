// ssh_tunnel для Linux — версия для серверов и рабочих станций.
//
// Одна и та же программа умеет два режима работы:
//
//	ssh_tunnel_linux              — просто поднимает туннель и пишет журнал
//	                               в стандартный вывод (годится для systemd);
//	ssh_tunnel_linux -web         — плюс веб-интерфейс, тот же самый, что и в
//	                               версии для Windows.
//
// Веб-интерфейс по умолчанию слушает 127.0.0.1:47821 — порт выбран
// нестандартным намеренно, чтобы не столкнуться с чем-нибудь привычным вроде
// 8080 или 3000, которые на сервере почти всегда уже заняты.
//
// Наружу интерфейс сам по себе не выставляется: за настройки прокси и за
// туннель отвечает тот, кто может им управлять, поэтому по умолчанию доступ
// только с самой машины. Флаг -web-lan открывает панель для локальной сети —
// по адресу машины и без ключа в ссылке, как у домашних сервисов; с публичных
// адресов ключ по-прежнему обязателен. Совсем произвольный адрес задаётся
// через -web-listen.
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
	"sshtunnel/internal/webui"
)

// Порт веб-интерфейса. Нестандартный, чтобы не конфликтовать с тем, что уже
// крутится на сервере. Постоянный: ссылку на панель кладут в закладки, и она
// не должна меняться от запуска к запуску.
const (
	defaultWebAddr = "127.0.0.1:47821"
	lanWebAddr     = "0.0.0.0:47821"
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
	flag.BoolVar(&cfg.SysProxy, "sysproxy", cfg.SysProxy, "настраивать прокси рабочего стола (GNOME), если он есть")
	flag.BoolVar(&cfg.SetEnvVars, "setenv", cfg.SetEnvVars, "писать файл proxy.env с переменными окружения")
	flag.StringVar(&cfg.FilterMode, "filter", cfg.FilterMode,
		"какие программы вести через туннель: all, only, except")
	apps := flag.String("apps", strings.Join(cfg.FilterApps, ","),
		"список программ для -filter через запятую")
	flag.BoolVar(&cfg.LocalViaTunnel, "local-via-tunnel", cfg.LocalViaTunnel,
		"вести через сервер и локальную сеть (по умолчанию она идёт напрямую)")
	direct := flag.String("direct", strings.Join(cfg.DirectHosts, ","),
		"адреса и сети, которые всегда идут напрямую (через запятую)")
	flag.BoolVar(&cfg.Verbose, "v", cfg.Verbose, "подробный журнал")

	web := flag.Bool("web", false, "включить веб-интерфейс")
	webLAN := flag.Bool("web-lan", false,
		"открыть панель для локальной сети: по адресу машины и без ключа в ссылке")
	webAddr := flag.String("web-listen", "",
		"свой адрес веб-интерфейса (по умолчанию "+defaultWebAddr+", с -web-lan — "+lanWebAddr+")")
	save := flag.Bool("save", false, "сохранить настройки и выйти")
	printEnv := flag.Bool("env", false, "напечатать строки для подключения прокси в оболочке и выйти")
	flag.Parse()

	cfg.FilterApps = splitApps(*apps)
	cfg.DirectHosts = routing.SplitEntries(*direct)

	if *printEnv {
		for _, line := range envLines(cfg) {
			fmt.Println(line)
		}
		return
	}
	if *save {
		if err := cfg.Save(); err != nil {
			fatal("не удалось сохранить настройки: %v", err)
		}
		fmt.Println("Настройки сохранены в", config.Path())
		return
	}
	if cfg.Host == "" {
		usage()
		os.Exit(1)
	}

	a := app.New(cfg)
	a.RecoverStaleProxy()
	go printEvents(a.Bus, cfg.Verbose)

	if *web {
		addr := *webAddr
		if addr == "" {
			addr = defaultWebAddr
			if *webLAN {
				addr = lanWebAddr
			}
		}

		newServer := webui.NewOn
		if *webLAN {
			newServer = webui.NewOpenLocalOn
		}
		srv, err := newServer(a, addr)
		if err != nil {
			fatal("%v", err)
		}
		go srv.Serve()

		fmt.Println("\nВеб-интерфейс:", srv.URL())
		switch {
		case *webLAN:
			a.Bus.Warnf("Панель открыта для локальной сети (%s): управлять туннелем может "+
				"любой, кто до неё дотянется. Для чужой сети так не оставляй.", addr)
			fmt.Println("(открыт для локальной сети — ключ не нужен)")
		case !strings.HasPrefix(addr, "127.") && !strings.HasPrefix(addr, "localhost"):
			a.Bus.Warnf("Веб-интерфейс открыт по сети (%s). Доступ защищён только ключом в адресе — "+
				"не оставляй его так в чужой сети.", addr)
			fmt.Println("(ключ в адресе обязателен — без него интерфейс не отвечает)")
		default:
			fmt.Println("(ключ в адресе обязателен — без него интерфейс не отвечает)")
		}
	}

	fmt.Printf("\nПодключаюсь к %s:%d...\n", cfg.Host, cfg.SSHPort)
	if err := a.Start(); err != nil {
		// С веб-интерфейсом выходить нельзя: настройки правятся как раз через
		// него, и человек остался бы без единственного способа починить.
		if !*web {
			fatal("%v", err)
		}
		a.Bus.Errorf("%v", err)
		fmt.Println("\nТуннель не поднялся. Поправь настройки в веб-интерфейсе и нажми «Подключить».")
	}

	fmt.Printf(`
  SOCKS4/SOCKS5  127.0.0.1:%d
  HTTP-прокси    127.0.0.1:%d

Подключить прокси в текущую оболочку:
  source %s

Готово. Ctrl+C — выйти.

`, cfg.SocksPort, cfg.HTTPPort, envFilePath())

	<-shutdown.OnExit(func() {
		fmt.Println("\nЗавершаю...")
		a.Stop()
	})
	time.Sleep(150 * time.Millisecond) // дать событиям выхода допечататься
}

func envFilePath() string {
	return config.Dir() + "/proxy.env"
}

func envLines(cfg config.Config) []string {
	url := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)
	return []string{
		"export http_proxy=" + url,
		"export https_proxy=" + url,
		"export HTTP_PROXY=" + url,
		"export HTTPS_PROXY=" + url,
		"export no_proxy=localhost,127.0.0.1,::1",
		"export NO_PROXY=localhost,127.0.0.1,::1",
	}
}

// printEvents печатает компактный журнал: по строке на соединение, без
// повторов одного и того же хоста подряд.
func printEvents(bus *events.Bus, verbose bool) {
	ch, _ := bus.Subscribe()

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
				proc = "программа"
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
				fmt.Printf("%s  %s\n", ts, e.Text)
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Ошибка: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Printf(`ssh_tunnel — SSH-туннель до своего сервера (SOCKS4/5 + HTTP-прокси)

Первый запуск:
  ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user root -save

Дальше достаточно:
  ssh_tunnel_linux              # только туннель, журнал в вывод
  ssh_tunnel_linux -web         # плюс веб-интерфейс на %s
  ssh_tunnel_linux -web -web-lan  # панель по адресу машины, без ключа в ссылке

Полезное:
  -env        напечатать строки для подключения прокси в оболочке
  -filter     all | only | except — какие программы вести через туннель
  -apps       список программ для -filter через запятую
  -direct     адреса и сети, которые всегда идут напрямую (через запятую)
  -pool       число SSH-соединений, больше — выше скорость (по умолчанию 4)
  -v          подробный журнал

Все флаги: ssh_tunnel_linux -h
`, defaultWebAddr)
}
