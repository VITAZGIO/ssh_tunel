package app

// Автовыбор самого быстрого сервера и переход на запасной. Оба поведения
// живут за одним переключателем (config.Config.AutoPickFastest): выключен —
// всё как раньше, подключение только к ActiveProfile, без сюрпризов для тех,
// кто выбрал сервер вручную.
//
// Порядок кандидатов считается один раз, при подключении, а не постоянно:
// скакать между серверами на ходу хуже, чем работать на чуть более медленном
// (так и сформулировано в задаче). Возврат на исходный сервер — только
// вручную (SwitchProfile) или при следующем Start(): effectiveProfile нигде
// не сохраняется в конфиг.

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/tunnel"
)

// errNotConnected — подключение состоялось, но пока мы его фиксировали,
// кто-то нажал «стоп» (или перехватил failover) раньше нас.
var errNotConnected = errors.New("остановлено")

// candidateLatencyBudget — сколько всего времени тратим на замер откликов
// перед подключением. Не привязан к latencyTimeout*attempts specifically:
// замеры идут параллельно, так что это скорее общий предохранитель.
const candidateLatencyBudget = 6 * time.Second

// failoverGrace — сколько ждать в состоянии "переподключение", прежде чем
// считать сервер упавшим и переходить на запасной. Меньше — риск переключиться
// из-за короткого моргания связи; больше — долгое ожидание там, где запасной
// поднялся бы сразу. Переменная, а не константа: тесты подставляют короткий
// срок, чтобы не ждать двадцать секунд по-настоящему.
var failoverGrace = 20 * time.Second

// connectCandidates строит порядок серверов для подключения. Без автовыбора
// или при одном сервере — только активный профиль, ровно как раньше.
func (a *App) connectCandidates(cfg config.Config) []config.Profile {
	active := cfg.Active()
	if active.Host == "" {
		return nil
	}
	if !cfg.AutoPickFastest || len(cfg.Profiles) < 2 {
		return []config.Profile{active}
	}

	ctx, cancel := context.WithTimeout(context.Background(), candidateLatencyBudget)
	defer cancel()
	results := measureAllLatencies(ctx, cfg.Profiles)
	ranked := rankByLatency(cfg.Profiles, results)

	out := make([]config.Profile, 0, len(ranked))
	for _, p := range ranked {
		if p.Host != "" {
			out = append(out, p)
		}
	}
	return out
}

// connectFrom пробует кандидатов по очереди начиная с idx, пока один не
// поднимется. Ошибка из-за ключа или имени пользователя останавливает попытки
// совсем: другой сервер её не чинит, а незаметно подключиться не туда — хуже
// честной ошибки (см. tunnel.IsAuthError).
func (a *App) connectFrom(cfg config.Config, candidates []config.Profile, idx int, gen int64) error {
	var lastErr error
	for i := idx; i < len(candidates); i++ {
		p := candidates[i]
		tun, socksAddr, httpAddr := a.buildTunnel(cfg, p)
		err := tun.Start()
		if err != nil {
			lastErr = err
			if tunnel.IsAuthError(err) {
				break
			}
			continue
		}

		a.mu.Lock()
		if a.gen != gen {
			// Пока подключались, кто-то нажал «стоп» или перехватил
			// failover — поднятое никому не нужно.
			a.mu.Unlock()
			tun.Stop()
			return errNotConnected
		}
		a.tun = tun
		a.running = true
		a.proxyURL = "http://" + httpAddr
		a.effectiveProfile = p.ID
		a.mu.Unlock()

		if i > 0 {
			a.Bus.Warnf("сервер %q не поднялся, перешли на запасной: %s", candidates[0].Name, p.Name)
		} else if idx > 0 {
			a.Bus.Infof("подключились к запасному серверу: %s", p.Name)
		}

		a.enableSysProxy(cfg, p, httpAddr, socksAddr)
		go a.watchFailover(cfg, candidates, i, gen)
		return nil
	}
	return lastErr
}

func (a *App) buildTunnel(cfg config.Config, p config.Profile) (tun *tunnel.Tunnel, socksAddr, httpAddr string) {
	socksAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.SocksPort))
	httpAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.HTTPPort))
	tun = tunnel.New(tunnel.Config{
		Host:           p.Host,
		SSHPort:        p.SSHPort,
		User:           p.User,
		KeyPath:        p.KeyPath,
		SocksAddr:      socksAddr,
		HTTPAddr:       httpAddr,
		PoolSize:       p.PoolSize,
		KnownHostsPath: config.KnownHostsPath(),
		Verbose:        cfg.Verbose,
		Policy:         a.policy,
		Direct:         a.direct,
		LocalViaTunnel: p.LocalViaTunnel,
	}, a.Bus)
	return tun, socksAddr, httpAddr
}

func (a *App) enableSysProxy(cfg config.Config, p config.Profile, httpAddr, socksAddr string) {
	if !cfg.SysProxy {
		return
	}
	if err := a.sys.Enable(httpAddr, socksAddr, cfg.SetEnvVars, !p.LocalViaTunnel, p.DirectHosts); err != nil {
		a.Bus.Warnf("Не удалось включить системный прокси: %v. Туннель работает, но приложения надо настроить вручную.", err)
		return
	}
	a.mu.Lock()
	a.sysOn = true
	a.mu.Unlock()
	a.Bus.Infof("Системный прокси включён: %s (SOCKS доступен на %s)", httpAddr, socksAddr)
	if cfg.SetEnvVars {
		a.Bus.Infof("HTTPS_PROXY прописан в переменные среды — программы вроде Claude Code подхватят его при следующем запуске")
	}
}

// watchFailover следит за состоянием только что поднятого туннеля
// candidates[idx]. Если он надолго застревает в "переподключение" — считаем
// его упавшим и переходим на следующего по отклику кандидата, о чём пишем в
// журнал. Дальше запасных нет — просто перестаёт наблюдать: keepalive внутри
// самого туннеля продолжает пытаться сам, как и без автовыбора.
func (a *App) watchFailover(cfg config.Config, candidates []config.Profile, idx int, gen int64) {
	if idx+1 >= len(candidates) {
		return
	}
	ch, unsub := a.Bus.Subscribe()
	defer unsub()

	var reconnectingSince time.Time
	for ev := range ch {
		a.mu.Lock()
		current := a.gen == gen
		a.mu.Unlock()
		if !current {
			return
		}
		if ev.Kind != events.KindState {
			continue
		}
		switch ev.State {
		case events.StateConnected:
			reconnectingSince = time.Time{}
		case events.StateReconnecting:
			if reconnectingSince.IsZero() {
				reconnectingSince = time.Now()
			}
			if time.Since(reconnectingSince) < failoverGrace {
				continue
			}
			a.failoverTo(cfg, candidates, idx, gen)
			return
		}
	}
}

func (a *App) failoverTo(cfg config.Config, candidates []config.Profile, idx int, gen int64) {
	a.mu.Lock()
	if a.gen != gen || a.transitioning {
		a.mu.Unlock()
		return
	}
	a.transitioning = true
	tun := a.tun
	a.tun, a.running, a.effectiveProfile = nil, false, ""
	a.gen++
	newGen := a.gen
	a.mu.Unlock()

	a.Bus.Warnf("сервер %q не отвечает — переходим на запасной: %s", candidates[idx].Name, candidates[idx+1].Name)
	if tun != nil {
		tun.Stop()
	}

	err := a.connectFrom(cfg, candidates, idx+1, newGen)
	a.mu.Lock()
	a.transitioning = false
	a.mu.Unlock()
	if err != nil {
		a.Bus.Errorf("не удалось перейти на запасной сервер: %v", err)
	}
}
