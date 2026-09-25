package meshsvc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// ServerInfo — что побочный сервер рассказывает о себе главному.
type ServerInfo struct {
	ID    string
	Name  string
	Host  string
	App   string
	Addrs []string
}

// LinkState — состояние связи побочного сервера с главным.
type LinkState struct {
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitempty"`
	Error     string    `json:"error,omitempty"`
	// LastPing — когда главный последний раз проверял связь (присылал ping).
	LastPing time.Time `json:"lastPing,omitempty"`
}

// ServerLink держит управляющее соединение побочного сервера с meshd
// главного. Оно идёт на свой же 127.0.0.1:47831 — это SSH-проброс к главному
// (служба UplinkUnitName), так что соединение само по себе проверяет, что
// проброс жив.
type ServerLink struct {
	Addr string // по умолчанию 127.0.0.1:Port
	Info ServerInfo

	mu    sync.Mutex
	state LinkState
}

// State — текущее состояние связи.
func (l *ServerLink) State() LinkState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *ServerLink) set(f func(*LinkState)) {
	l.mu.Lock()
	f(&l.state)
	l.mu.Unlock()
}

// Run держит связь, пока не отменят ctx: при обрыве переподключается с
// растущей паузой (до 30 секунд).
func (l *ServerLink) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := l.session(ctx)
		if ctx.Err() != nil {
			break
		}
		l.set(func(s *LinkState) {
			*s = LinkState{Error: explainLinkError(err)}
		})
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	l.set(func(s *LinkState) { *s = LinkState{} })
}

type linkMsg struct {
	Op     string   `json:"op"`
	V      int      `json:"v,omitempty"`
	Device string   `json:"device,omitempty"`
	Name   string   `json:"name,omitempty"`
	Host   string   `json:"host,omitempty"`
	App    string   `json:"app,omitempty"`
	Addrs  []string `json:"addrs,omitempty"`
	Error  string   `json:"error,omitempty"`
	T      int64    `json:"t,omitempty"`
}

var errNoAnswer = errors.New("главный сервер закрыл соединение")

func (l *ServerLink) session(ctx context.Context) error {
	addr := l.Addr
	if addr == "" {
		addr = "127.0.0.1:" + strconv.Itoa(Port)
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	enc := json.NewEncoder(conn)
	var wmu sync.Mutex
	write := func(m linkMsg) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return enc.Encode(m)
	}
	if err := write(linkMsg{Op: "server", V: 2, Device: l.Info.ID, Name: l.Info.Name,
		Host: l.Info.Host, App: l.Info.App, Addrs: l.Info.Addrs}); err != nil {
		return err
	}

	r := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var m linkMsg
	if err := readLinkMsg(r, &m); err != nil {
		return err
	}
	if m.Op == "error" {
		return errors.New(m.Error)
	}
	now := time.Now()
	l.set(func(s *LinkState) { *s = LinkState{Connected: true, Since: now} })

	for {
		// Главный шлёт ping раз в 15 секунд: полторы минуты тишины —
		// связь мертва, даже если TCP об этом ещё не знает.
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if err := readLinkMsg(r, &m); err != nil {
			return err
		}
		switch m.Op {
		case "ping":
			l.set(func(s *LinkState) { s.LastPing = time.Now() })
			if err := write(linkMsg{Op: "pong", T: m.T}); err != nil {
				return err
			}
		case "error":
			return errors.New(m.Error)
		}
	}
}

func readLinkMsg(r *bufio.Reader, m *linkMsg) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if len(line) == 0 {
			return errNoAnswer
		}
		return err
	}
	*m = linkMsg{}
	return json.Unmarshal(line, m)
}

// explainLinkError — ошибка связи по-человечески.
func explainLinkError(err error) string {
	var op *net.OpError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &op) && op.Op == "dial":
		return "проброс к главному серверу не поднят — служба " + UplinkUnitName + " не подключилась"
	case errors.Is(err, errNoAnswer):
		return "главный сервер закрыл соединение — на нём не запущен meshd или связь оборвалась"
	}
	return err.Error()
}
