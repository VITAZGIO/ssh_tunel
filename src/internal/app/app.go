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

	// gen растёт на каждой Stop() и на каждом переходе на запасной сервер —
	// фоновый наблюдатель failover (см. failover.go) по нему узнаёт, что его
	// подключение больше не актуально, и не мешает тому, что произошло после.
	gen int64
	// transitioning — идёт подключение или переход на запасной сервер прямо
	// сейчас. Отдельно от running: во время перехода тун временно nil, но
	// второй Start() при этом всё равно не ко двору.
	transitioning bool
	// effectiveProfile — какой профиль реально подключён сейчас; может
	// отличаться от cfg.ActiveProfile после автовыбора самого быстрого или
	// перехода на запасной. Не сохраняется в конфиг: следующий Start() снова
	// начинает с ActiveProfile.
	effectiveProfile string

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
	active := cfg.Active()
	a := &App{
		Bus:      events.NewBus(),
		cfg:      cfg,
		sys:      sysproxy.NewManager(config.Dir()),
		policy:   routing.New(routing.Mode(active.FilterMode), active.FilterApps),
		direct:   routing.NewDirectList(active.DirectHosts),
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

	active := cfg.Active()
	a.policy.Set(routing.Mode(active.FilterMode), active.FilterApps)
	a.direct.Set(active.DirectHosts)
	if tun != nil {
		tun.SetPolicy(a.policy)
		tun.SetDirect(a.direct)
		tun.SetLocalViaTunnel(active.LocalViaTunnel)
	}

	if err := cfg.Save(); err != nil {
		return "", err
	}

	if running && connectionSettingsChanged(old, cfg) {
		return "часть настроек вступит в силу после переподключения", nil
	}
	return "", nil
}

// SwitchProfile переключает активный сервер. Работающий туннель принадлежит
// прежнему серверу, поэтому его надо остановить — переподключаться к новому
// адресу молча, без нажатия «Подключить», было бы неожиданно.
func (a *App) SwitchProfile(id string) (string, error) {
	a.mu.Lock()
	if _, ok := a.cfg.ProfileByID(id); !ok {
		a.mu.Unlock()
		return "", fmt.Errorf("сервер с id %q не найден", id)
	}
	a.cfg.ActiveProfile = id
	cfg := a.cfg
	wasRunning := a.running
	a.mu.Unlock()

	if err := cfg.Save(); err != nil {
		return "", err
	}
	if wasRunning {
		a.Stop()
		return "туннель прежнего сервера остановлен — нажми «Подключить», чтобы поднять новый", nil
	}
	return "", nil
}

// AddProfile заводит новый сервер и сразу сохраняет его в списке — activным
// он не становится сам: выбор сервера остаётся отдельным явным действием.
func (a *App) AddProfile(name, flag string) (config.Profile, error) {
	a.mu.Lock()
	p := a.cfg.AddProfile(name, flag)
	cfg := a.cfg
	a.mu.Unlock()
	if err := cfg.Save(); err != nil {
		return config.Profile{}, err
	}
	return p, nil
}

// UpdateProfile перезаписывает один профиль (по его ID) и сохраняет — не
// трогая ни туннель, ни политику фильтра: используется при импорте, когда
// правится необязательно тот сервер, что сейчас подключён.
func (a *App) UpdateProfile(p config.Profile) error {
	a.mu.Lock()
	a.cfg.SetProfile(p)
	cfg := a.cfg
	a.mu.Unlock()
	return cfg.Save()
}

// RemoveProfile удаляет сервер из списка. Если это был активный (и, значит,
// возможно, подключённый прямо сейчас) — туннель останавливается: он остался
// бы работать по адресу, которого в настройках больше нет.
func (a *App) RemoveProfile(id string) (string, error) {
	a.mu.Lock()
	wasActive := a.cfg.ActiveProfile == id
	ok := a.cfg.RemoveProfile(id)
	cfg := a.cfg
	wasRunning := a.running
	a.mu.Unlock()
	if !ok {
		return "", errors.New("этот сервер удалить нельзя — он либо не найден, либо последний из оставшихся")
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	if wasActive && wasRunning {
		a.Stop()
		return "удалённый сервер был подключён — туннель остановлен", nil
	}
	return "", nil
}

// connectionSettingsChanged — поменялось ли у активного сервера то, ради чего
// надо переподключаться. Правки в неактивном профиле (другая вкладка) сюда не
// попадают: он всё равно ни к чему сейчас не подключён.
func connectionSettingsChanged(a, b config.Config) bool {
	newActive := b.Active()
	oldActive, _ := a.ProfileByID(newActive.ID)
	return oldActive.Host != newActive.Host || oldActive.SSHPort != newActive.SSHPort ||
		oldActive.User != newActive.User || oldActive.KeyPath != newActive.KeyPath ||
		oldActive.SocksPort != newActive.SocksPort || oldActive.HTTPPort != newActive.HTTPPort ||
		oldActive.PoolSize != newActive.PoolSize || oldActive.LocalViaTunnel != newActive.LocalViaTunnel ||
		a.SysProxy != b.SysProxy || a.SetEnvVars != b.SetEnvVars
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

// Start поднимает туннель. Без автовыбора (cfg.AutoPickFastest) ведёт себя
// как раньше — подключается к ActiveProfile. С автовыбором и несколькими
// серверами сначала меряет отклик у всех и подключается к тому, что ответил
// быстрее; если он не поднимется, по очереди пробует остальных — см.
// failover.go.
func (a *App) Start() error {
	a.mu.Lock()
	if a.running || a.transitioning {
		a.mu.Unlock()
		return errors.New("туннель уже запущен")
	}
	a.transitioning = true
	cfg := a.cfg
	a.gen++
	gen := a.gen
	a.mu.Unlock()

	candidates := a.connectCandidates(cfg)
	if len(candidates) == 0 {
		a.mu.Lock()
		a.transitioning = false
		a.mu.Unlock()
		return errors.New("не указан адрес сервера")
	}

	err := a.connectFrom(cfg, candidates, 0, gen)
	a.mu.Lock()
	a.transitioning = false
	a.mu.Unlock()
	return err
}

func (a *App) Stop() {
	a.mu.Lock()
	tun, sysOn := a.tun, a.sysOn
	a.tun, a.running, a.sysOn = nil, false, false
	a.effectiveProfile = ""
	// Отменяет любой фоновый переход на запасной сервер, который мог быть в
	// процессе: connectFrom проверяет gen перед тем, как зафиксировать успех,
	// и сам остановит то, что успел поднять.
	a.gen++
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

// EffectiveProfileID — какой сервер реально подключён сейчас. Пусто, если
// туннель не работает. Отличается от Config().ActiveProfile после автовыбора
// самого быстрого сервера или перехода на запасной.
func (a *App) EffectiveProfileID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effectiveProfile
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
		// Потоков столько же, сколько соединений в пуле: меряем ровно то,
		// чем пользуются приложения.
		Streams: cfg.Active().PoolSize,
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
	return sysproxy.EnvHint(net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Active().HTTPPort)))
}
