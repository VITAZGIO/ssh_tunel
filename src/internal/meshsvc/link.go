package meshsvc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
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
	// MeshIP/MeshHost — адрес и имя этого сервера в сети устройств.
	MeshIP   string `json:"meshIp,omitempty"`
	MeshHost string `json:"meshHost,omitempty"`
}

// ServerLink держит управляющее соединение побочного сервера с meshd
// главного. Оно идёт на свой же 127.0.0.1:47831 — это SSH-проброс к главному
// (служба UplinkUnitName), так что соединение само по себе проверяет, что
// проброс жив.
type ServerLink struct {
	Addr string // по умолчанию 127.0.0.1:Port
	Info ServerInfo

	// Allow решает, пускать ли устройство сети на порт этого сервера
	// (см. сервер как узел сети в cmd/meshd). nil — не пускать никуда.
	// Непустая строка — объяснение отказа для устройства.
	Allow func(port int) (bool, string)
	// LocalHost — куда вести такие соединения; пусто — 127.0.0.1.
	LocalHost string

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
	Net    string   `json:"net,omitempty"`
	Call   string   `json:"call,omitempty"`
	OK     bool     `json:"ok,omitempty"`
	Port   int      `json:"port,omitempty"`
	IP     string   `json:"ip,omitempty"`
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
	ip, host := m.IP, m.Host
	l.set(func(s *LinkState) { *s = LinkState{Connected: true, Since: now, MeshIP: ip, MeshHost: host} })

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
		case "incoming":
			go l.incoming(m.Call, m.Port)
		case "error":
			return errors.New(m.Error)
		}
	}
}

// incoming — устройство сети хочет на порт этого сервера: подключаемся к
// нему здесь и отвечаем главному встречным соединением (как устройство).
func (l *ServerLink) incoming(call string, port int) {
	addr := l.Addr
	if addr == "" {
		addr = "127.0.0.1:" + strconv.Itoa(Port)
	}
	back, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return
	}
	reply := linkMsg{Op: "accept", Net: "@relay", Device: l.Info.ID, Call: call}
	var local net.Conn
	ok, why := false, "сервер не пускает устройства сети"
	if l.Allow != nil && port > 0 && port <= 65535 && port != Port {
		ok, why = l.Allow(port)
	}
	if ok {
		host := l.LocalHost
		if host == "" {
			host = "127.0.0.1"
		}
		local, err = net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
		if err != nil {
			ok, why = false, "на сервере на порту "+strconv.Itoa(port)+" ничего не слушает"
		}
	} else if why == "" {
		why = "сервер не пускает устройства сети на порт " + strconv.Itoa(port)
	}
	reply.OK = ok
	if !ok {
		reply.Error = why
	}
	back.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(back).Encode(reply); err != nil || !ok {
		back.Close()
		if local != nil {
			local.Close()
		}
		return
	}
	back.SetWriteDeadline(time.Time{})
	pipe(back, local)
}

// pipe перекачивает данные в обе стороны, пока одна не закроется.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// PortAllowed — открыт ли порт по списку вида "22,80,8000-8100" (пусто —
// все). Тот же разбор, что и в meshd.
func PortAllowed(spec string, port int) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := a, error(nil)
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 == nil && err2 == nil && port >= a && port <= b {
			return true
		}
	}
	return false
}

// ValidPorts проверяет список портов из панели.
func ValidPorts(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a < 1 || a > 65535 {
			return errors.New("неверный список портов: «" + strings.TrimSpace(part) + "» — пример: 22, 80, 8000-8100")
		}
		if isRange {
			b, err := strconv.Atoi(strings.TrimSpace(hi))
			if err != nil || b < a || b > 65535 {
				return errors.New("неверный список портов: «" + strings.TrimSpace(part) + "» — пример: 22, 80, 8000-8100")
			}
		}
	}
	return nil
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
