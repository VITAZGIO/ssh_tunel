package udprelay

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// SessionTTL — если по сессии дольше этого времени не было ни одного кадра
// ни в одну сторону, клиент сам её забывает и шлёт FIN. Ретранслятор следит
// за тем же самым независимо (см. src/cmd/udprelay) — небольшое рассогласование
// между двумя таймерами не страшно, каждая сторона просто наводит порядок
// у себя.
const SessionTTL = 60 * time.Second

var errUnknownSession = errors.New("udprelay: сессия уже закрыта")

// Client мультиплексирует произвольное число UDP-сессий через одно
// TCP-соединение до ретранслятора на сервере. Как получить это соединение
// (обычно — через уже поднятый SSH-туннель, tun.Dial("tcp","127.0.0.1:порт"))
// решает вызывающий код: Client самим SSH не занимается.
type Client struct {
	conn    net.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	sessions map[uint32]*session
	nextID   atomic.Uint32

	closed    chan struct{}
	closeOnce sync.Once
}

type session struct {
	deliver func(data []byte, fromHost string, fromPort uint16)
	lastUse atomic.Int64 // unix-нано
}

// NewClient начинает обслуживать conn. Закрыть его должен вызывающий код —
// через Close() у последней сессии смысла нет, соединение общее на весь
// туннель; когда оно рвётся (сервер, сеть), это видно через Done().
func NewClient(conn net.Conn) *Client {
	c := &Client{
		conn:     conn,
		sessions: make(map[uint32]*session),
		closed:   make(chan struct{}),
	}
	go c.readLoop()
	go c.expireLoop()
	return c
}

// Open заводит новую сессию: deliver вызывается на каждый входящий кадр по
// ней — из горутины чтения, поэтому не должен блокироваться надолго.
func (c *Client) Open(deliver func(data []byte, fromHost string, fromPort uint16)) uint32 {
	id := c.nextID.Add(1)
	s := &session{deliver: deliver}
	s.lastUse.Store(time.Now().UnixNano())
	c.mu.Lock()
	c.sessions[id] = s
	c.mu.Unlock()
	return id
}

// Send отправляет датаграмму по уже открытой сессии на host:port.
func (c *Client) Send(id uint32, host string, port uint16, data []byte) error {
	c.mu.Lock()
	s, ok := c.sessions[id]
	c.mu.Unlock()
	if !ok {
		return errUnknownSession
	}
	s.lastUse.Store(time.Now().UnixNano())

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(c.conn, Frame{SessionID: id, Host: host, Port: port, Data: data})
}

// Close завершает одну сессию и, если получится, сообщает об этом
// ретранслятору кадром FIN — чтобы тот не ждал там тайм-аута впустую.
func (c *Client) Close(id uint32) {
	c.mu.Lock()
	_, ok := c.sessions[id]
	delete(c.sessions, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	c.writeMu.Lock()
	WriteFrame(c.conn, Frame{SessionID: id, FIN: true})
	c.writeMu.Unlock()
}

// Done закрывается, когда соединение до ретранслятора оборвалось (сервер,
// сеть, туннель упал). Вызывающий код решает, поднимать ли новое.
func (c *Client) Done() <-chan struct{} { return c.closed }

// Shutdown закрывает соединение до ретранслятора немедленно — например, при
// остановке всего туннеля, когда дожидаться естественного обрыва незачем.
func (c *Client) Shutdown() {
	c.conn.Close()
	<-c.closed
}

func (c *Client) readLoop() {
	defer c.closeOnce.Do(func() { close(c.closed) })
	defer c.conn.Close()
	for {
		f, err := ReadFrame(c.conn)
		if err != nil {
			return
		}
		c.mu.Lock()
		s, ok := c.sessions[f.SessionID]
		if ok && f.FIN {
			delete(c.sessions, f.SessionID)
		}
		c.mu.Unlock()
		if !ok {
			continue // сессия у нас уже закрыта — запоздавший кадр, не страшно
		}
		if f.FIN {
			continue
		}
		s.lastUse.Store(time.Now().UnixNano())
		s.deliver(f.Data, f.Host, f.Port)
	}
}

func (c *Client) expireLoop() {
	t := time.NewTicker(SessionTTL / 4)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			cutoff := time.Now().Add(-SessionTTL).UnixNano()
			var stale []uint32
			c.mu.Lock()
			for id, s := range c.sessions {
				if s.lastUse.Load() < cutoff {
					stale = append(stale, id)
				}
			}
			c.mu.Unlock()
			for _, id := range stale {
				c.Close(id)
			}
		}
	}
}

func formatPort(p uint16) string { return strconv.Itoa(int(p)) }

// JoinHostPort — то же самое, что net.JoinHostPort, но принимает порт числом:
// удобно для вызывающего кода на приёмной стороне (deliver).
func JoinHostPort(host string, port uint16) string {
	return net.JoinHostPort(host, formatPort(port))
}
