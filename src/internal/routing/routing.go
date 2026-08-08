// Package routing решает, вести соединение через туннель или напрямую.
//
// Это возможно потому, что для каждого соединения мы знаем программу, которая
// его открыла: приложение приходит с локального порта, а Windows по этому
// порту говорит владельца сокета. Дальше остаётся только сверить имя со
// списком.
//
// Три режима:
//
//	all    — всё через туннель (обычное поведение);
//	only   — через туннель идут ТОЛЬКО перечисленные программы,
//	         остальные выходят напрямую;
//	except — через туннель идёт всё, КРОМЕ перечисленных.
//
// Чего это не может, и об этом честно сказано в интерфейсе: программы,
// которые вообще не обращаются к прокси (часть игр, приложения со своим
// сетевым стеком), туннель не видит — ни включить их в него, ни исключить
// нельзя, они и так идут мимо.
package routing

import (
	"strings"
	"sync"
)

type Mode string

const (
	ModeAll    Mode = "all"
	ModeOnly   Mode = "only"
	ModeExcept Mode = "except"
)

// Policy потокобезопасна: правила можно менять, не останавливая туннель.
type Policy struct {
	mu   sync.RWMutex
	mode Mode
	apps map[string]struct{}
}

func New(mode Mode, apps []string) *Policy {
	p := &Policy{}
	p.Set(mode, apps)
	return p
}

func (p *Policy) Set(mode Mode, apps []string) {
	set := make(map[string]struct{}, len(apps))
	for _, a := range apps {
		if n := normalize(a); n != "" {
			set[n] = struct{}{}
		}
	}
	p.mu.Lock()
	p.mode = validMode(mode)
	p.apps = set
	p.mu.Unlock()
}

func (p *Policy) Mode() Mode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// UseTunnel отвечает на единственный вопрос: гнать это соединение через
// сервер или выпустить напрямую.
//
// process — имя программы; пустая строка означает, что определить не удалось
// (сокет уже закрылся, или процесс чужого пользователя). Неизвестные
// намеренно ведём через туннель: неожиданная утечка мимо туннеля хуже, чем
// лишнее приложение внутри него.
func (p *Policy) UseTunnel(process string) bool {
	p.mu.RLock()
	mode, apps := p.mode, p.apps
	p.mu.RUnlock()

	if mode == ModeAll || len(apps) == 0 {
		// В режиме "только выбранные" с пустым списком через туннель не пошло
		// бы вообще ничего — это выглядело бы как поломка, поэтому такой
		// список считаем не заданным.
		return true
	}

	name := normalize(process)
	if name == "" {
		return true
	}
	_, listed := apps[name]

	switch mode {
	case ModeOnly:
		return listed
	case ModeExcept:
		return !listed
	default:
		return true
	}
}

func validMode(m Mode) Mode {
	switch m {
	case ModeOnly, ModeExcept:
		return m
	default:
		return ModeAll
	}
}

// normalize приводит имя к сравнимому виду: без пути, в нижнем регистре и
// обязательно с .exe — человек в списке легко напишет "chrome" вместо
// "chrome.exe", и это должно работать.
func normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if i := strings.LastIndexAny(s, `\/`); i >= 0 {
		s = s[i+1:]
	}
	if !strings.HasSuffix(s, ".exe") {
		s += ".exe"
	}
	return s
}

// Normalize — то же самое, но наружу: интерфейс приводит введённые имена к
// одному виду, чтобы в списке не появлялось двух записей об одной программе.
func Normalize(s string) string { return normalize(s) }
