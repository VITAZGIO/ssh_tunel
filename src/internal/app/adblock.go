package app

import (
	"errors"

	"sshtunnel/internal/config"
)

// AdBlocker — блокировка рекламы и слежки по DNS. Её умеет только слой VPN:
// в режиме прокси имена разрешают сами программы, перехватывать нечего.
type AdBlocker interface {
	// SetAdBlock применяет настройки (вкл/выкл, исключения) и перечитывает
	// сохранённый список. Работает и на поднятом адаптере.
	SetAdBlock(cfg config.Config)
	// UpdateAdBlockLists скачивает списки и сохраняет их; возвращает число имён.
	UpdateAdBlockLists(sources string) (int, error)
}

func (a *App) adBlocker() AdBlocker {
	a.mu.Lock()
	defer a.mu.Unlock()
	ab, _ := a.net.(AdBlocker)
	return ab
}

// AdBlockAvailable — есть ли блокировка рекламы в этой версии программы.
func (a *App) AdBlockAvailable() bool { return a.adBlocker() != nil }

// UpdateAdBlockLists скачивает списки по кнопке в настройках и сразу
// применяет их.
func (a *App) UpdateAdBlockLists(sources string) (int, error) {
	ab := a.adBlocker()
	if ab == nil {
		return 0, errors.New("блокировка рекламы есть только в режиме VPN")
	}
	n, err := ab.UpdateAdBlockLists(sources)
	if err != nil {
		return 0, err
	}
	ab.SetAdBlock(a.Config())
	return n, nil
}

// AdBlocked — сколько запросов имён заблокировано с подключения; 0, если
// блокировки в этой версии нет.
func (a *App) AdBlocked() int {
	if c, ok := a.adBlocker().(interface{ AdBlocked() int }); ok {
		return c.AdBlocked()
	}
	return 0
}
