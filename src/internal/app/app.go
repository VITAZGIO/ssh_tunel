// Package app связывает вместе туннель и системные настройки прокси. И
// консольная версия, и версия с окном пользуются одним и тем же кодом — иначе
// они неизбежно разъезжаются в поведении.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/speedtest"
	"sshtunnel/internal/sysproxy"
	"sshtunnel/internal/tunnel"
)

type App struct {
	Bus *events.Bus

	mu       sync.Mutex
	cfg      config.Config
	tun      *tunnel.Tunnel
	sys      *sysproxy.Manager
	sysOn    bool
	running  bool
	proxyURL string

	speedRunning atomic.Bool

	// policy живёт отдельно от туннеля: правила фильтра меняются на ходу,
	// без переподключения.
	policy *routing.Policy
	direct *routing.DirectList

	// seenApps — программы, которые уже ходили через прокси. Нужны, чтобы в
	// настройках можно было выбрать их из списка, а не вспоминать имена.
	seenMu   sync.Mutex
	seenApps map[string]struct{}
}

func New(cfg config.Config) *App {
	a := &App{
		Bus:      events.NewBus(),
		cfg:      cfg,
		sys:      sysproxy.NewManager(config.Dir()),
		policy:   routing.New(routing.Mode(cfg.FilterMode), cfg.FilterApps),
		direct:   routing.NewDirectList(cfg.DirectHosts),
		seenApps: map[string]struct{}{},
	}
	go a.collectSeenApps()
	return a
}

// collectSeenApps запоминает имена программ из событий соединений.
func (a *App) collectSeenApps() {
	ch, _ := a.Bus.Subscribe()
	for e := range ch {
		if e.Kind != events.KindConn || e.Process == "" {
			continue
		}
		name := routing.Normalize(e.Process)
		a.seenMu.Lock()
		if len(a.seenApps) < 200 {
			a.seenApps[name] = struct{}{}
		}
		a.seenMu.Unlock()
	}
}

// SeenApps — отсортированный список программ, замеченных за этот запуск.
func (a *App) SeenApps() []string {
	a.seenMu.Lock()
	out := make([]string, 0, len(a.seenApps))
	for n := range a.seenApps {
		out = append(out, n)
	}
	a.seenMu.Unlock()
	sort.Strings(out)
	return out
}

func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SetConfig сохраняет настройки. Правила фильтра применяются сразу, даже на
// работающем туннеле — ради них останавливаться незачем. Остальное (адрес
// сервера, порты, число каналов) вступит в силу при следующем подключении, о
// чём сообщается в возвращаемом тексте.
func (a *App) SetConfig(cfg config.Config) (string, error) {
	a.mu.Lock()
	old, running := a.cfg, a.running
	a.cfg = cfg
	tun := a.tun
	a.mu.Unlock()

	a.policy.Set(routing.Mode(cfg.FilterMode), cfg.FilterApps)
	a.direct.Set(cfg.DirectHosts)
	if tun != nil {
		tun.SetPolicy(a.policy)
		tun.SetDirect(a.direct)
		tun.SetLocalViaTunnel(cfg.LocalViaTunnel)
	}

	if err := cfg.Save(); err != nil {
		return "", err
	}

	if running && connectionSettingsChanged(old, cfg) {
		return "часть настроек вступит в силу после переподключения", nil
	}
	return "", nil
}

// connectionSettingsChanged — поменялось ли то, ради чего надо переподключаться.
func connectionSettingsChanged(a, b config.Config) bool {
	return a.Host != b.Host || a.SSHPort != b.SSHPort || a.User != b.User ||
		a.KeyPath != b.KeyPath || a.SocksPort != b.SocksPort ||
		a.HTTPPort != b.HTTPPort || a.PoolSize != b.PoolSize ||
		a.SysProxy != b.SysProxy || a.SetEnvVars != b.SetEnvVars ||
		a.LocalViaTunnel != b.LocalViaTunnel
}

func (a *App) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// RecoverStaleProxy чинит настройки, оставшиеся от аварийно завершённого
// прошлого запуска. Вызывать при старте, до всего остального.
func (a *App) RecoverStaleProxy() {
	if a.sys.RecoverStale() {
		a.Bus.Warnf("Обнаружены настройки прокси от прошлого запуска (программа завершилась аварийно) — вернул как было")
	}
}

func (a *App) Start() error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return errors.New("туннель уже запущен")
	}
	cfg := a.cfg
	a.mu.Unlock()

	if cfg.Host == "" {
		return errors.New("не указан адрес сервера")
	}

	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.SocksPort))
	httpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HTTPPort))

	tun := tunnel.New(tunnel.Config{
		Host:           cfg.Host,
		SSHPort:        cfg.SSHPort,
		User:           cfg.User,
		KeyPath:        cfg.KeyPath,
		SocksAddr:      socksAddr,
		HTTPAddr:       httpAddr,
		PoolSize:       cfg.PoolSize,
		KnownHostsPath: config.KnownHostsPath(),
		Verbose:        cfg.Verbose,
		Policy:         a.policy,
		Direct:         a.direct,
		LocalViaTunnel: cfg.LocalViaTunnel,
	}, a.Bus)

	if err := tun.Start(); err != nil {
		return err
	}

	a.mu.Lock()
	a.tun = tun
	a.running = true
	a.proxyURL = "http://" + httpAddr
	a.mu.Unlock()

	if cfg.SysProxy {
		if err := a.sys.Enable(httpAddr, socksAddr, cfg.SetEnvVars, !cfg.LocalViaTunnel, cfg.DirectHosts); err != nil {
			a.Bus.Warnf("Не удалось включить системный прокси: %v. Туннель работает, но приложения надо настроить вручную.", err)
		} else {
			a.mu.Lock()
			a.sysOn = true
			a.mu.Unlock()
			a.Bus.Infof("Системный прокси включён: %s (SOCKS доступен на %s)", httpAddr, socksAddr)
			if cfg.SetEnvVars {
				a.Bus.Infof("HTTPS_PROXY прописан в переменные среды — программы вроде Claude Code подхватят его при следующем запуске")
			}
		}
	}
	return nil
}

func (a *App) Stop() {
	a.mu.Lock()
	tun, sysOn := a.tun, a.sysOn
	a.tun, a.running, a.sysOn = nil, false, false
	a.mu.Unlock()

	if sysOn {
		if err := a.sys.Disable(); err != nil {
			a.Bus.Errorf("Не удалось вернуть настройки прокси: %v. Проверь: Параметры → Сеть и Интернет → Прокси-сервер", err)
		} else {
			a.Bus.Infof("Системный прокси выключен, трафик идёт как обычно")
		}
	}
	if tun != nil {
		tun.Stop()
	}
}

func (a *App) Stats() events.Stats {
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun == nil {
		return events.Stats{}
	}
	return tun.Stats()
}

func (a *App) State() string {
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun == nil {
		return events.StateStopped
	}
	return tun.State()
}

// CheckIP спрашивает у внешнего сервиса, каким адресом мы выходим в интернет,
// причём ЧЕРЕЗ туннель. Это и есть проверка "работает ли": если вернулся адрес
// сервера, значит трафик действительно идёт через него.
func (a *App) CheckIP() (string, error) {
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun == nil {
		return "", errors.New("туннель не запущен")
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				return tun.Dial(network, addr)
			},
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("не удалось проверить адрес: %w", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	ip := string(buf[:n])
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("сервис вернул что-то странное: %q", ip)
	}
	return ip, nil
}

// SpeedTest меряет реальную пропускную способность туннеля. В отличие от
// счётчиков на главном экране, которые показывают текущий трафик, тест сам
// нагружает канал и показывает потолок.
//
// Одновременно тест может идти только один: два теста мешали бы друг другу и
// показали бы обоим заниженный результат.
func (a *App) SpeedTest() (speedtest.Result, error) {
	a.mu.Lock()
	tun, cfg := a.tun, a.cfg
	a.mu.Unlock()
	if tun == nil {
		return speedtest.Result{}, errors.New("туннель не запущен")
	}
	if !a.speedRunning.CompareAndSwap(false, true) {
		return speedtest.Result{}, errors.New("тест скорости уже идёт")
	}
	defer a.speedRunning.Store(false)

	a.Bus.Infof("Тест скорости запущен")
	res, err := speedtest.Run(context.Background(), speedtest.Options{
		Dial: tun.Dial,
		// Тот же замер мимо туннеля. Без него цифра «через туннель» не с чем
		// сравнить, и вопрос «медленно из-за туннеля или интернет такой»
		// остаётся без ответа.
		DirectDial: tun.DialDirect,
		// Потоков столько же, сколько соединений в пуле: меряем ровно то,
		// чем пользуются приложения.
		Streams: cfg.PoolSize,
		OnProgress: func(phase string, mbps float64) {
			a.Bus.Speed(phase, mbps, false)
		},
	})
	if err != nil {
		a.Bus.Speed("", 0, true)
		a.Bus.Warnf("Тест скорости не удался: %v", err)
		return res, err
	}
	a.Bus.Speed("", 0, true)
	a.Bus.Infof("Тест скорости: приём %.1f Мбит/с, отдача %.1f Мбит/с", res.DownMbps, res.UpMbps)
	if res.DirectDownMbps > 0 {
		a.Bus.Infof("Тот же файл мимо туннеля: %.1f Мбит/с — %s", res.DirectDownMbps, res.Verdict)
	}
	return res, nil
}

// ProxyURL — адрес HTTP-прокси для подсказок в интерфейсе.
func (a *App) ProxyURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.proxyURL
}

func (a *App) EnvHint() []string {
	cfg := a.Config()
	return sysproxy.EnvHint(net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HTTPPort)))
}
