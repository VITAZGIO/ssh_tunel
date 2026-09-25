// Пакет mesh — клиент «сети устройств»: твои устройства (ноутбук, телефон,
// домашний сервер) видят друг друга по постоянным адресам 198.19.x.y и
// именам «имя.mesh» — как в NetBird или Tailscale, только через собственный
// сервер. Серверная сторона — sshtunnel/cmd/meshd, протокол описан там.
//
// Клиент ходит к meshd через уже поднятый SSH-туннель (Dial ядра до
// 127.0.0.1:47831 на сервере), поэтому ему не нужны ни белый адрес, ни проброс
// портов на роутере: достаточно того, что устройство видит сервер.
package mesh

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAddr — где на сервере слушает meshd (его собственный localhost).
const DefaultAddr = "127.0.0.1:47831"

// Suffix — окончание имён устройств: «ноутбук.mesh».
const Suffix = ".mesh"

// Range — адреса устройств. Из диапазона тестов сетевого оборудования
// (198.18.0.0/15): с настоящими сетями он не пересекается, а его первую
// половину уже занимают подставные адреса DNS.
var Range = netip.MustParsePrefix("198.19.0.0/16")

// Config — настройки сети устройств для одного сервера.
type Config struct {
	// Key — ключ сети: устройства с одинаковым ключом видят друг друга.
	Key string
	// DeviceID — постоянный id этого устройства (см. NewDeviceID): по нему
	// сервер узнаёт устройство и выдаёт ему тот же адрес.
	DeviceID string
	// Name — имя устройства, из него получается имя «.mesh».
	Name string
	// AllowIncoming — пускать ли соединения от других устройств к своим
	// службам (на 127.0.0.1 этого устройства).
	AllowIncoming bool
	// Addr — адрес meshd на сервере. Пусто — DefaultAddr.
	Addr string

	// То, что устройство рассказывает о себе: видно в панели на сервере.
	// Всё необязательное.
	Platform   string // windows, linux, android
	OS         string // версия системы, если известна
	AppVersion string // версия ssh_tunnel
	Mode       string // proxy или vpn
	Via        string // через какой сервер подключено устройство
	Hostname   string // имя компьютера

	// Проверка NAT (см. natprobe.go) идёт на сервер Via мимо туннеля.
	// ProbeListen открывает такой UDP-сокет (на Android и в режиме VPN — с
	// пометкой «мимо VPN»); nil — обычный. ProbeResolver ищет адрес сервера
	// мимо туннеля; nil — системный. NoNATProbe — не проверять (тесты).
	ProbeListen   func(network string) (net.PacketConn, error)
	ProbeResolver *net.Resolver
	NoNATProbe    bool
}

// NewKey придумывает ключ новой сети.
func NewKey() string { return randomString(24) }

// NewDeviceID придумывает id устройства.
func NewDeviceID() string { return randomString(16) }

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Peer — устройство сети.
type Peer struct {
	Name     string     `json:"name"`
	Host     string     `json:"host"`
	IP       netip.Addr `json:"ip"`
	Platform string     `json:"platform,omitempty"`
	Via      string     `json:"via,omitempty"`
	Online   bool       `json:"online"`
	LastSeen int64      `json:"lastSeen,omitempty"`
	Self     bool       `json:"self,omitempty"`
}

// Status — то, что показывается человеку.
type Status struct {
	// State: "connecting", "online", "error".
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	Self  Peer   `json:"self"`
	Peers []Peer `json:"peers"`
	// NAT — результат проверки NAT этого устройства, если она прошла.
	NAT *NATInfo `json:"nat,omitempty"`
}

// Dialer открывает соединение через туннель (tunnel.Tunnel.Dial).
type Dialer func(network, addr string) (net.Conn, error)

// Client держит управляющее соединение с meshd и обслуживает вызовы в обе
// стороны.
type Client struct {
	cfg  Config
	dial Dialer
	log  func(level, text string)

	// LocalDial соединяется со службой на этом устройстве для входящего
	// вызова. Можно подменить в тестах.
	LocalDial func(ctx context.Context, port int) (net.Conn, error)

	mu     sync.Mutex
	status Status
}

// New готовит клиента. log получает строки для журнала ("info", "warn").
func New(cfg Config, dial Dialer, log func(level, text string)) *Client {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if log == nil {
		log = func(string, string) {}
	}
	c := &Client{cfg: cfg, dial: dial, log: log, status: Status{State: "connecting"}}
	c.LocalDial = func(ctx context.Context, port int) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	return c
}

// wire — строка протокола; зеркало msg из cmd/meshd.
type wire struct {
	Op       string     `json:"op"`
	V        int        `json:"v,omitempty"`
	Net      string     `json:"net,omitempty"`
	Device   string     `json:"device,omitempty"`
	Name     string     `json:"name,omitempty"`
	To       string     `json:"to,omitempty"`
	Port     int        `json:"port,omitempty"`
	Call     string     `json:"call,omitempty"`
	OK       bool       `json:"ok,omitempty"`
	Error    string     `json:"error,omitempty"`
	IP       string     `json:"ip,omitempty"`
	Host     string     `json:"host,omitempty"`
	From     string     `json:"from,omitempty"`
	FromHost string     `json:"fromHost,omitempty"`
	Peers    []wirePeer `json:"peers,omitempty"`

	Platform string `json:"platform,omitempty"`
	OS       string `json:"os,omitempty"`
	App      string `json:"app,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Via      string `json:"via,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	T        int64  `json:"t,omitempty"`

	Stun []int    `json:"stun,omitempty"`
	NAT  *NATInfo `json:"nat,omitempty"`
}

type wirePeer struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Platform string `json:"platform,omitempty"`
	Via      string `json:"via,omitempty"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen,omitempty"`
}

const protoVersion = 2

func send(conn net.Conn, m wire) error {
	m.V = protoVersion
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	return json.NewEncoder(conn).Encode(m)
}

// Run держит управляющее соединение, пока жив ctx, и переподключается при
// обрывах с нарастающей паузой.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			break
		}
		c.setError(err)
		if time.Since(start) > time.Minute {
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
	c.mu.Lock()
	c.status = Status{State: "connecting"}
	c.mu.Unlock()
}

func (c *Client) setError(err error) {
	text := "связь с сервером сети потеряна"
	if err != nil {
		text = err.Error()
	}
	c.mu.Lock()
	wasOnline := c.status.State == "online"
	c.status.State, c.status.Error = "error", text
	for i := range c.status.Peers {
		c.status.Peers[i].Online = false
	}
	c.mu.Unlock()
	if wasOnline {
		c.log("warn", "Сеть устройств: "+text)
	}
}

// session — одно управляющее соединение от подключения до обрыва.
func (c *Client) session(ctx context.Context) error {
	conn, err := c.dial("tcp", c.cfg.Addr)
	if err != nil {
		return fmt.Errorf("сервис сети устройств на сервере недоступен (не установлен?): %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	hello := wire{
		Op: "hello", Net: c.cfg.Key, Device: c.cfg.DeviceID, Name: c.cfg.Name,
		Platform: c.cfg.Platform, OS: c.cfg.OS, App: c.cfg.AppVersion,
		Mode: c.cfg.Mode, Via: c.cfg.Via, Hostname: c.cfg.Hostname,
	}
	if err := send(conn, hello); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var welcome wire
	if err := readLine(r, &welcome); err != nil {
		return fmt.Errorf("сервис сети устройств не ответил: %w", err)
	}
	if welcome.Op == "error" {
		return errors.New(welcome.Error)
	}
	if welcome.Op != "welcome" {
		return fmt.Errorf("неожиданный ответ сервера: %q", welcome.Op)
	}
	self, _ := netip.ParseAddr(welcome.IP)
	c.mu.Lock()
	c.status = Status{
		State: "online",
		Self:  Peer{Name: c.cfg.Name, Host: welcome.Host, IP: self, Online: true, Self: true},
	}
	c.mu.Unlock()
	c.setPeers(welcome.Peers)
	c.log("info", fmt.Sprintf("Сеть устройств: это устройство — %s%s (%s)", welcome.Host, Suffix, welcome.IP))

	// Пинг держит соединение живым через NAT и даёт серверу знать, что мы
	// на месте.
	var wmu sync.Mutex

	// Проверка NAT — один раз на подключение: сеть сменилась — подключение
	// пересобирается, и проверка идёт заново.
	if len(welcome.Stun) > 0 && c.cfg.Via != "" && !c.cfg.NoNATProbe {
		go func() {
			info := ProbeNAT(ctx, ProbeEnv{Server: c.cfg.Via, Ports: welcome.Stun,
				Listen: c.cfg.ProbeListen, Resolver: c.cfg.ProbeResolver})
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			c.status.NAT = &info
			c.mu.Unlock()
			c.log("info", "Сеть устройств, проверка прямых соединений: "+natSummary(info))
			wmu.Lock()
			send(conn, wire{Op: "nat", NAT: &info})
			wmu.Unlock()
		}()
	}
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				wmu.Lock()
				err := send(conn, wire{Op: "ping"})
				wmu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var m wire
		if err := readLine(r, &m); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("связь с сервером сети потеряна")
		}
		switch m.Op {
		case "peers":
			c.setPeers(m.Peers)
		case "incoming":
			go c.incoming(m)
		case "ping":
			// Сервер меряет задержку: вернуть его метку времени как есть.
			wmu.Lock()
			err := send(conn, wire{Op: "pong", T: m.T})
			wmu.Unlock()
			if err != nil {
				return errors.New("связь с сервером сети потеряна")
			}
		}
	}
}

func readLine(r *bufio.Reader, v any) error {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}

func (c *Client) setPeers(list []wirePeer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	peers := make([]Peer, 0, len(list))
	for _, p := range list {
		ip, err := netip.ParseAddr(p.IP)
		if err != nil {
			continue
		}
		self := ip == c.status.Self.IP
		peers = append(peers, Peer{
			Name: p.Name, Host: p.Host, IP: ip, Online: p.Online, LastSeen: p.LastSeen,
			Platform: p.Platform, Via: p.Via, Self: self,
		})
		if self {
			// Устройство могли переименовать в панели — имя берём из списка.
			c.status.Self.Name, c.status.Self.Host = p.Name, p.Host
		}
	}
	c.status.Peers = peers
}

// Status — копия текущего состояния.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.status
	st.Peers = append([]Peer(nil), c.status.Peers...)
	return st
}

// Match — ведёт ли цель (host:port) в сеть устройств.
func (c *Client) Match(target string) bool {
	host := hostOf(target)
	if strings.HasSuffix(strings.ToLower(host), Suffix) {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && Range.Contains(a.Unmap())
}

// Resolve — адрес устройства по имени («ноутбук.mesh» или «ноутбук»).
func (c *Client) Resolve(name string) (netip.Addr, bool) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSuffix(name, ".")), Suffix)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.status.Peers {
		if p.Host == name {
			return p.IP, true
		}
	}
	return netip.Addr{}, false
}

// StaticDNS — ответ DNS на имя сети устройств для стека режима VPN (см.
// core.DNS.Static). ours=false — имя не из сети устройств. c может быть nil
// (сеть выключена или не подключена): тогда имена .mesh — «нет такого».
func StaticDNS(c *Client, name string) (ips []net.IP, ours bool) {
	if !strings.HasSuffix(strings.ToLower(strings.TrimSuffix(name, ".")), Suffix) {
		return nil, false
	}
	if c == nil {
		return nil, true
	}
	if ip, ok := c.Resolve(name); ok {
		return []net.IP{net.IP(ip.AsSlice())}, true
	}
	return nil, true
}

func hostOf(target string) string {
	if h, _, err := net.SplitHostPort(target); err == nil {
		return h
	}
	return target
}

// DialPeer соединяется с устройством сети. target — «имя.mesh:порт» или
// «198.19.x.y:порт».
func (c *Client) DialPeer(target string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("неверный порт %q", portStr)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		var ok bool
		if ip, ok = c.Resolve(host); !ok {
			return nil, fmt.Errorf("в сети устройств нет %q", host)
		}
	}
	ip = ip.Unmap()

	c.mu.Lock()
	self := c.status.Self.IP
	c.mu.Unlock()
	if ip == self {
		// Сам к себе — незачем гонять через сервер.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.LocalDial(ctx, port)
	}

	conn, err := c.dial("tcp", c.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("сервис сети устройств недоступен: %w", err)
	}
	if err := send(conn, wire{Op: "dial", Net: c.cfg.Key, Device: c.cfg.DeviceID, To: ip.String(), Port: port}); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	line, err := readRawLine(conn)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("сервис сети устройств не ответил: %w", err)
	}
	var resp wire
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp.Op != "connected" {
		conn.Close()
		if resp.Error == "" {
			resp.Error = "соединение не состоялось"
		}
		return nil, errors.New(resp.Error)
	}
	return conn, nil
}

// readRawLine читает строку по байту: после неё идут сырые данные, и забрать
// их в буфер значило бы потерять.
func readRawLine(conn net.Conn) ([]byte, error) {
	var line []byte
	var b [1]byte
	for len(line) < 4096 {
		if _, err := conn.Read(b[:]); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return line, nil
		}
		line = append(line, b[0])
	}
	return nil, errors.New("слишком длинная строка")
}

// incoming — другое устройство хочет соединиться с нашей службой.
func (c *Client) incoming(m wire) {
	reject := func(text string) {
		conn, err := c.dial("tcp", c.cfg.Addr)
		if err != nil {
			return
		}
		send(conn, wire{Op: "accept", Net: c.cfg.Key, Device: c.cfg.DeviceID, Call: m.Call, OK: false, Error: text})
		conn.Close()
	}
	if !c.cfg.AllowIncoming {
		reject("устройство не принимает входящие соединения")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	local, err := c.LocalDial(ctx, m.Port)
	cancel()
	if err != nil {
		reject(fmt.Sprintf("на устройстве ничего не слушает порт %d", m.Port))
		return
	}
	conn, err := c.dial("tcp", c.cfg.Addr)
	if err != nil {
		local.Close()
		return
	}
	if err := send(conn, wire{Op: "accept", Net: c.cfg.Key, Device: c.cfg.DeviceID, Call: m.Call, OK: true}); err != nil {
		conn.Close()
		local.Close()
		return
	}
	c.log("info", fmt.Sprintf("Сеть устройств: %s%s → порт %d этого устройства", m.FromHost, Suffix, m.Port))
	pump(conn, local)
}

func pump(a, b net.Conn) {
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
