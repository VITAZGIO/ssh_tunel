//go:build windows || linux

package vpnlayer

import (
	"path/filepath"

	"sshtunnel/android/core"
	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/routing"
)

// app находит блокировку приведением типа: без этой строки опечатка в
// сигнатуре молча выключила бы её.
var _ app.AdBlocker = (*Layer)(nil)

// Блокировка рекламы и слежки — та же, что на телефоне (core.BlockList):
// на запрос имени из списка адаптер отвечает отказом, и до сети он не доходит.

// adBlockFile — скачанный список, рядом с настройками программы.
func adBlockFile() string { return filepath.Join(config.Dir(), "adblock.list") }

// SetAdBlock применяет настройки (реализует app.AdBlocker). Действует и на
// поднятом адаптере: новые запросы имён сразу проходят через новый список.
func (l *Layer) SetAdBlock(cfg config.Config) {
	if !cfg.AdBlockEnabled {
		l.block.Set(nil, nil)
	} else {
		l.block.Set(core.LoadBlockListFile(adBlockFile()), routing.SplitEntries(cfg.AdBlockAllowlist))
	}
	l.mu.Lock()
	up := l.up
	l.mu.Unlock()
	// Кеш DNS системы помнит уже разрешённые имена — без сброса реклама,
	// показанная минуту назад, ещё какое-то время грузилась бы.
	if up {
		l.sys.flushDNS()
	}
}

// UpdateAdBlockLists скачивает списки и сохраняет их (реализует app.AdBlocker).
func (l *Layer) UpdateAdBlockLists(sources string) (int, error) {
	n, err := core.UpdateBlockListFile(routing.SplitEntries(sources), adBlockFile())
	if err == nil {
		l.bus.Infof("Блокировка рекламы: загружено имён — %d", n)
	}
	return n, err
}

// AdBlocked — сколько запросов имён отвергнуто с подъёма адаптера (для
// строки «Заблокировано» в настройках, как на телефоне).
func (l *Layer) AdBlocked() int {
	l.mu.Lock()
	st := l.stats
	l.mu.Unlock()
	if st == nil {
		return 0
	}
	_, _, _, _, n := st.Counts()
	return n
}
