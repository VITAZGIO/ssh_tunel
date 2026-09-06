// Package events — общая шина событий между ядром туннеля и интерфейсом
// (консольным или графическим). Ядро ничего не знает про то, кто и как будет
// показывать события: оно просто публикует их сюда, а CLI/GUI подписываются.
package events

import (
	"fmt"
	"sync"
	"time"
)

type Kind string

const (
	KindState Kind = "state" // сменилось состояние туннеля
	KindConn  Kind = "conn"  // приложение открыло соединение через туннель
	KindLog   Kind = "log"   // произвольное сообщение (ошибка, предупреждение)
	KindStats Kind = "stats" // периодическая статистика трафика
	KindSpeed Kind = "speed" // ход и результат теста скорости
)

// Состояния туннеля.
const (
	StateStopped      = "stopped"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateError        = "error"
)

// Stats — сводка по трафику. Скорости считаются интерфейсом из разницы
// счётчиков между двумя событиями, поэтому здесь только абсолютные значения.
type Stats struct {
	BytesUp   int64 `json:"bytesUp"`
	BytesDown int64 `json:"bytesDown"`
	Active    int64 `json:"active"`  // сейчас открыто соединений
	Total     int64 `json:"total"`   // всего обслужено с момента старта
	Links     int   `json:"links"`   // размер пула SSH-соединений
	Healthy   int   `json:"healthy"` // сколько из них живы

	// PingMs — задержка до сервера в миллисекундах: сколько идёт туда и
	// обратно служебный запрос по уже открытому SSH-соединению. Ноль означает,
	// что замера ещё не было.
	PingMs int64 `json:"pingMs,omitempty"`
}

type Event struct {
	Kind Kind      `json:"kind"`
	Time time.Time `json:"time"`

	// KindState
	State  string `json:"state,omitempty"`
	Detail string `json:"detail,omitempty"`
	// ErrorKind — стабильный код причины для state=="error"/"reconnecting"
	// (internal/tunnel.ConnErrorKind: "auth", "no_response", "refused",
	// "hostkey_changed", "other"). Пустая строка — переход состояния не
	// связан с ошибкой подключения (обычное "connecting"/"connected").
	// Detail при этом остаётся русским текстом того же события — для
	// потребителей без своего словаря (консоль); экран с I18N (веб-панель,
	// Android) выбирает текст по ErrorKind, а не по Detail.
	ErrorKind string `json:"errorKind,omitempty"`

	// KindConn
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Target  string `json:"target,omitempty"`
	Proto   string `json:"proto,omitempty"` // socks4 / socks4a / socks5 / http
	// DNSLeak=true означает, что приложение прислало нам уже готовый IP-адрес,
	// то есть DNS-запрос оно сделало само — в обход туннеля.
	DNSLeak bool `json:"dnsLeak,omitempty"`
	// Direct=true — соединение намеренно пущено мимо туннеля по правилам
	// фильтра, а не из-за сбоя.
	Direct bool   `json:"direct,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`

	// KindLog
	Level string `json:"level,omitempty"` // info / warn / error
	Text  string `json:"text,omitempty"`

	// KindStats
	Stats *Stats `json:"stats,omitempty"`

	// KindSpeed
	Phase string  `json:"phase,omitempty"` // down / up
	Mbps  float64 `json:"mbps,omitempty"`
	Done  bool    `json:"done,omitempty"`
}

// Speed сообщает ход теста скорости: какое направление меряется сейчас и
// сколько получилось. Done=true — тест завершён.
func (b *Bus) Speed(phase string, mbps float64, done bool) {
	b.Publish(Event{Kind: KindSpeed, Phase: phase, Mbps: mbps, Done: done})
}

// Bus раздаёт события подписчикам. Публикация никогда не блокируется: если
// подписчик не успевает читать, его события просто теряются — лог важен, но не
// настолько, чтобы из-за него вставал проброс трафика.
type Bus struct {
	mu      sync.RWMutex
	subs    map[chan Event]struct{}
	history []Event
	maxHist int
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{}), maxHist: 300}
}

// Subscribe возвращает канал событий и функцию отписки. Канал буферизованный;
// переполнение означает потерю событий, а не остановку публикующего.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// History отдаёт последние события — нужно, чтобы окно, открытое позже старта,
// не показывало пустой лог.
func (b *Bus) History() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, len(b.history))
	copy(out, b.history)
	return out
}

func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.Lock()
	// Статистика и ход теста скорости идут часто и в истории не нужны:
	// иначе они вытеснят из неё осмысленные сообщения.
	if e.Kind != KindStats && e.Kind != KindSpeed {
		b.history = append(b.history, e)
		if len(b.history) > b.maxHist {
			b.history = b.history[len(b.history)-b.maxHist:]
		}
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *Bus) State(state, detail string) {
	b.Publish(Event{Kind: KindState, State: state, Detail: detail})
}

// StateErr — то же самое, но с кодом причины ошибки подключения (см.
// ErrorKind), для переходов в StateError/StateReconnecting.
func (b *Bus) StateErr(state, detail, errorKind string) {
	b.Publish(Event{Kind: KindState, State: state, Detail: detail, ErrorKind: errorKind})
}

func (b *Bus) Infof(format string, args ...any)  { b.logf("info", format, args...) }
func (b *Bus) Warnf(format string, args ...any)  { b.logf("warn", format, args...) }
func (b *Bus) Errorf(format string, args ...any) { b.logf("error", format, args...) }

func (b *Bus) logf(level, format string, args ...any) {
	b.Publish(Event{Kind: KindLog, Level: level, Text: fmt.Sprintf(format, args...)})
}
