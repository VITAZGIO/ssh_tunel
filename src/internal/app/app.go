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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"vpstunnel/internal/config"
	"vpstunnel/internal/events"
	"vpstunnel/internal/speedtest"
	"vpstunnel/internal/sysproxy"
	"vpstunnel/internal/tunnel"
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
}

func New(cfg config.Config) *App {
	return &App{
		Bus: events.NewBus(),
		cfg: cfg,
		sys: sysproxy.NewManager(config.Dir()),
	}
}

func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) SetConfig(cfg config.Config) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return errors.New("останови туннель, прежде чем менять настройки")
	}
	a.cfg = cfg
	a.mu.Unlock()
	return cfg.Save()
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
		if err := a.sys.Enable(httpAddr, socksAddr, cfg.SetEnvVars); err != nil {
			a.Bus.Warnf("Не удалось включить системный прокси: %v. Туннель работает, но приложения надо настроить вручную.", err)
		} else {
			a.mu.Lock()
			a.sysOn = true
			a.mu.Unlock()
			a.Bus.Infof("Системный прокси включён: http=%s, socks=%s", httpAddr, socksAddr)
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
// VPS, значит трафик действительно идёт через сервер.
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
