package app

import (
	"errors"

	"sshtunnel/internal/serverusers"
)

// ServerUsers вызывает вход создания пользователей на сервере, к которому
// сейчас подключён туннель (см. serverusers): op — "whoami" или "create".
func (a *App) ServerUsers(op string, body any) (map[string]any, error) {
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun == nil {
		return nil, errors.New("сначала подключись к серверу, на котором заводить пользователя")
	}
	return serverusers.Call(tun.Dial, op, body)
}
