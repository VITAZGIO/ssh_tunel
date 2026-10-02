package app

import (
	"time"

	"sshtunnel/internal/tunnel"
)

// Сколько и как часто StartOnLaunch пробует подключиться. Переменные, а не
// константы, — чтобы тест не ждал минутами.
var (
	launchRetryFor   = 3 * time.Minute
	launchRetryEvery = 5 * time.Second
)

// StartOnLaunch поднимает туннель сразу после запуска программы (галочка
// «Подключаться сразу при запуске»).
//
// При автозапуске программа стартует вместе со входом в систему, и сети в
// этот момент часто ещё нет: Wi-Fi подключается через несколько секунд после
// рабочего стола. Одна попытка тогда заканчивалась «автозапуск не удался», и
// туннель так и оставался выключенным до ручного «Подключить». Поэтому
// пробуем ещё несколько минут.
//
// Сдаёмся сразу, если дело в ключе или имени пользователя (ожиданием это не
// чинится), и тихо выходим, если за это время человек сам нажал «Подключить»
// или «Отключить»: решение уже за ним.
func (a *App) StartOnLaunch() {
	deadline := time.Now().Add(launchRetryFor)
	for attempt := 1; ; attempt++ {
		err := a.Start()
		if err == nil || a.Running() {
			if attempt > 1 {
				a.Bus.Infof("Автозапуск: подключились с %d-й попытки", attempt)
			}
			return
		}
		if tunnel.IsAuthError(err) || time.Now().After(deadline) {
			a.Bus.Errorf("Автозапуск не удался: %v", err)
			return
		}
		if attempt == 1 {
			a.Bus.Warnf("Автозапуск: пока не подключиться (%v) — возможно, сеть ещё не поднялась, пробую снова", err)
		}

		a.mu.Lock()
		gen := a.gen
		a.mu.Unlock()
		time.Sleep(launchRetryEvery)
		a.mu.Lock()
		touched := a.gen != gen || a.running || a.transitioning
		a.mu.Unlock()
		if touched {
			return
		}
	}
}
