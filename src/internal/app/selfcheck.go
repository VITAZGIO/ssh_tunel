package app

import (
	"context"

	"sshtunnel/internal/config"
	"sshtunnel/internal/tunnel"
)

// SelfCheck прогоняет цепочку самопроверки (см. tunnel.RunSelfCheck) для
// текущего активного сервера. Соединение для проверки отдельное от уже
// поднятого пула — работает и объясняет, где именно не так, даже когда
// туннель выключен.
func (a *App) SelfCheck(ctx context.Context) []tunnel.CheckStep {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	active := cfg.Active()

	return tunnel.RunSelfCheck(ctx, tunnel.SelfCheckOptions{
		Config: tunnel.Config{
			Host:           active.Host,
			SSHPort:        active.SSHPort,
			User:           active.User,
			KeyPath:        active.KeyPath,
			KnownHostsPath: config.KnownHostsPath(),
		},
	})
}
