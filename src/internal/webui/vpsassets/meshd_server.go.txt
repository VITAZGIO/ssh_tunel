// Команда meshd — «сеть устройств» для ssh_tunnel: сервис на сервере,
// который связывает твои устройства (ноутбук, телефон, домашний сервер) в одну
// сеть, как NetBird или Tailscale, — только через твой собственный сервер.
//
// Как это устроено. Каждое устройство держит к meshd управляющее соединение и
// получает постоянный адрес из 198.19.0.0/16 и имя вида «ноутбук.mesh».
// Когда одно устройство хочет соединиться с другим, оно просит meshd; тот
// через управляющее соединение зовёт второе устройство, оно открывает
// встречное соединение, и meshd сшивает их вместе. Весь трафик идёт через
// сервер — зато не нужны ни белые адреса, ни проброс портов на роутерах: оба
// устройства и так ходят к серверу.
//
// Слушает ТОЛЬКО 127.0.0.1: достучаться до него можно лишь через уже
// прошедший SSH-аутентификацию туннель (direct-tcpip до этого же localhost).
// Устройства разных людей разделены ключом сети: видят друг друга только
// те, у кого ключ одинаковый. Сервер хранит не сам ключ, а его хеш.
//
// Протокол — по строке JSON в начале каждого соединения:
//
//	управление: → {"op":"hello","net":КЛЮЧ,"device":ID,"name":ИМЯ}
//	            ← {"op":"welcome","ip":..,"host":..,"peers":[..]}
//	            дальше ← peers / incoming / pong, → ping
//	вызов:      → {"op":"dial","net":..,"device":..,"to":IP,"port":N}
//	            ← {"op":"connected"} и дальше сырые байты — или {"op":"error"}
//	ответ:      → {"op":"accept","net":..,"device":..,"call":ID,"ok":true}
//	            и дальше сырые байты
//
// Клиентская сторона — пакет sshtunnel/internal/mesh. Этот файл нарочно
// самодостаточен (только стандартная библиотека): мастер настройки VPS может
// собрать его прямо на сервере одной командой "go build".
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	protoVersion = 1

	maxLine          = 4096
	maxNetworks      = 1000
	maxDevicesPerNet = 1024
	maxPendingCalls  = 256

	// callTimeout — сколько ждать, пока вызванное устройство откроет встречное
	// соединение.
	callTimeout = 10 * time.Second
	// ctrlIdle — управляющее соединение без единого ping дольше этого
	// считается мёртвым (клиент шлёт ping раз в 25 секунд).
	ctrlIdle = 90 * time.Second
	// forgetAfter — устройство, которое не появлялось столько времени,
	// забывается, и его адрес может достаться новому.
	forgetAfter = 180 * 24 * time.Hour
)

// meshRange — адреса устройств. Диапазон 198.18.0.0/15 зарезервирован под
// тесты сетевого оборудования: с настоящими сетями он не пересекается, а
// первую его половину (198.18/16) уже занимают подставные адреса DNS.
var meshRange = netip.MustParsePrefix("198.19.0.0/16")

// ---------- сообщения ----------

type msg struct {
	Op       string `json:"op"`
	V        int    `json:"v,omitempty"`
	Net      string `json:"net,omitempty"`
	Device   string `json:"device,omitempty"`
	Name     string `json:"name,omitempty"`
	To       string `json:"to,omitempty"`
	Port     int    `json:"port,omitempty"`
	Call     string `json:"call,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Error    string `json:"error,omitempty"`
	IP       string `json:"ip,omitempty"`
	Host     string `json:"host,omitempty"`
	From     string `json:"from,omitempty"`
	FromHost string `json:"fromHost,omitempty"`
	Peers    []peer `json:"peers,omitempty"`
}

type peer struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen,omitempty"`
}

// ---------- состояние ----------

type device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Host     string    `json:"host"`
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"lastSeen"`

	ctrl *ctrlConn
}

type network struct {
	Devices map[string]*device `json:"devices"`
}

type call struct {
	net    string
	target string // id вызванного устройства
	answer chan answer
}

type answer struct {
	conn net.Conn // nil — устройство отказалось
	err  string
}

type server struct {
	mu     sync.Mutex
	nets   map[string]*network // по хешу ключа
	calls  map[string]*call
	path   string
	saveMu sync.Mutex
	dirty  chan struct{}
}

func newServer(path string) *server {
	s := &server{
		nets:  map[string]*network{},
		calls: map[string]*call{},
		path:  path,
		dirty: make(chan struct{}, 1),
	}
	s.load()
	return s
}

func netID(key string) string {
	h := sha256.Sum256([]byte("ssh_tunnel mesh v1\x00" + key))
	return hex.EncodeToString(h[:])
}

func (s *server) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var nets map[string]*network
	if err := json.Unmarshal(data, &nets); err != nil {
		log.Printf("состояние %s повреждено, начинаю с чистого: %v", s.path, err)
		return
	}
	now := time.Now()
	for id, n := range nets {
		if n == nil || n.Devices == nil {
			continue
		}
		for did, d := range n.Devices {
			if now.Sub(d.LastSeen) > forgetAfter {
				delete(n.Devices, did)
			}
		}
		s.nets[id] = n
	}
}

// saveLoop пишет состояние на диск не чаще раза в секунду.
func (s *server) saveLoop() {
	for range s.dirty {
		time.Sleep(time.Second)
		s.save()
	}
}

func (s *server) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *server) save() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	data, err := json.MarshalIndent(s.nets, "", " ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("не удалось сохранить состояние: %v", err)
		return
	}
	os.Rename(tmp, s.path)
}

// peersLocked — список устройств сети для рассылки. Под s.mu.
func peersLocked(n *network) []peer {
	out := make([]peer, 0, len(n.Devices))
	for _, d := range n.Devices {
		p := peer{Name: d.Name, Host: d.Host, IP: d.IP, Online: d.ctrl != nil}
		if !d.LastSeen.IsZero() {
			p.LastSeen = d.LastSeen.Unix()
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// broadcastLocked рассылает всем подключённым устройствам сети свежий
// список. Под s.mu.
func broadcastLocked(n *network) {
	peers := peersLocked(n)
	for _, d := range n.Devices {
		if d.ctrl != nil {
			d.ctrl.send(msg{Op: "peers", Peers: peers})
		}
	}
}

// allocIPLocked выдаёт свободный адрес в сети. Под s.mu.
func allocIPLocked(n *network) (string, bool) {
	used := map[string]bool{}
	for _, d := range n.Devices {
		used[d.IP] = true
	}
	a := meshRange.Addr().Next() // .0.0 — адрес сети, пропускаем
	for meshRange.Contains(a) {
		b := a.As4()
		if b[3] != 0 && b[3] != 255 && !used[a.String()] {
			return a.String(), true
		}
		a = a.Next()
	}
	return "", false
}

// hostLabel делает из имени устройства имя для DNS: латиница, цифры и
// дефисы. Кириллица переводится в латиницу.
func hostLabel(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			dash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya", 'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g",
}

// uniqueHostLocked — hostLabel(name), а при совпадении с чужим — с номером.
func uniqueHostLocked(n *network, self, name string) string {
	base := hostLabel(name)
	if base == "" {
		base = "device"
	}
	taken := func(h string) bool {
		for id, d := range n.Devices {
			if id != self && d.Host == h {
				return true
			}
		}
		return false
	}
	h := base
	for i := 2; taken(h); i++ {
		h = base + "-" + itoa(i)
	}
	return h
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// ---------- соединения ----------

// ctrlConn — управляющее соединение устройства. Пишется из отдельной
// горутины через очередь: иначе одно медленное устройство держало бы общую
// блокировку, пока ему шлют список, и тормозило бы всех остальных.
type ctrlConn struct {
	conn net.Conn
	out  chan msg
	once sync.Once
}

func newCtrlConn(conn net.Conn) *ctrlConn {
	c := &ctrlConn{conn: conn, out: make(chan msg, 64)}
	go func() {
		enc := json.NewEncoder(conn)
		for m := range c.out {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if enc.Encode(m) != nil {
				conn.Close()
			}
		}
	}()
	return c
}

// send не ждёт: очередь переполнена — устройство не успевает читать, и
// соединение с ним закрывается (оно переподключится само).
func (c *ctrlConn) send(m msg) {
	defer func() { recover() }() // очередь уже закрыта
	select {
	case c.out <- m:
	default:
		c.conn.Close()
	}
}

func (c *ctrlConn) close() {
	c.once.Do(func() { close(c.out) })
}

func writeMsg(conn net.Conn, m msg) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	return json.NewEncoder(conn).Encode(m)
}

// readFirst читает первую строку соединения, не заглядывая дальше неё:
// после неё могут идти сырые байты, и их нельзя проглотить в буфер.
func readFirst(conn net.Conn) (msg, error) {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	var line []byte
	var b [1]byte
	for len(line) < maxLine {
		if _, err := conn.Read(b[:]); err != nil {
			return msg{}, err
		}
		if b[0] == '\n' {
			var m msg
			err := json.Unmarshal(line, &m)
			return m, err
		}
		line = append(line, b[0])
	}
	return msg{}, errors.New("слишком длинная строка")
}

func (s *server) handle(conn net.Conn) {
	m, err := readFirst(conn)
	if err != nil {
		conn.Close()
		return
	}
	if m.Net == "" || m.Device == "" || len(m.Device) > 128 {
		writeMsg(conn, msg{Op: "error", Error: "нет ключа сети или id устройства"})
		conn.Close()
		return
	}
	switch m.Op {
	case "hello":
		s.control(conn, m)
	case "dial":
		s.dial(conn, m)
	case "accept":
		s.accept(conn, m)
	default:
		writeMsg(conn, msg{Op: "error", Error: "неизвестная операция"})
		conn.Close()
	}
}

func (s *server) control(conn net.Conn, m msg) {
	defer conn.Close()
	id := netID(m.Net)
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = "устройство"
	}
	if len([]rune(name)) > 64 {
		name = string([]rune(name)[:64])
	}
	c := newCtrlConn(conn)
	defer c.close()

	s.mu.Lock()
	n := s.nets[id]
	if n == nil {
		if len(s.nets) >= maxNetworks {
			s.mu.Unlock()
			writeMsg(conn, msg{Op: "error", Error: "на сервере слишком много сетей"})
			return
		}
		n = &network{Devices: map[string]*device{}}
		s.nets[id] = n
	}
	d := n.Devices[m.Device]
	if d == nil {
		if len(n.Devices) >= maxDevicesPerNet {
			s.mu.Unlock()
			writeMsg(conn, msg{Op: "error", Error: "в сети слишком много устройств"})
			return
		}
		ip, ok := allocIPLocked(n)
		if !ok {
			s.mu.Unlock()
			writeMsg(conn, msg{Op: "error", Error: "кончились адреса"})
			return
		}
		d = &device{ID: m.Device, IP: ip}
		n.Devices[m.Device] = d
	}
	if d.ctrl != nil {
		// То же устройство подключилось заново (сменилась сеть) — старое
		// соединение уже не нужно.
		d.ctrl.conn.Close()
	}
	d.Name = name
	d.Host = uniqueHostLocked(n, m.Device, name)
	d.LastSeen = time.Now()
	d.ctrl = c
	c.send(msg{Op: "welcome", V: protoVersion, IP: d.IP, Host: d.Host, Peers: peersLocked(n)})
	broadcastLocked(n)
	s.mu.Unlock()
	s.markDirty()

	r := bufio.NewReaderSize(conn, maxLine)
	for {
		conn.SetReadDeadline(time.Now().Add(ctrlIdle))
		line, err := r.ReadSlice('\n')
		if err != nil {
			break
		}
		var in msg
		if json.Unmarshal(line, &in) != nil {
			continue
		}
		if in.Op == "ping" {
			c.send(msg{Op: "pong"})
		}
	}

	s.mu.Lock()
	if d.ctrl == c {
		d.ctrl = nil
		d.LastSeen = time.Now()
		broadcastLocked(n)
	}
	s.mu.Unlock()
	s.markDirty()
}

func (s *server) dial(conn net.Conn, m msg) {
	id := netID(m.Net)
	fail := func(text string) {
		writeMsg(conn, msg{Op: "error", Error: text})
		conn.Close()
	}
	if m.Port <= 0 || m.Port > 65535 {
		fail("неверный порт")
		return
	}

	s.mu.Lock()
	n := s.nets[id]
	if n == nil || n.Devices[m.Device] == nil {
		s.mu.Unlock()
		fail("это устройство не в сети — сначала нужно управляющее соединение")
		return
	}
	from := n.Devices[m.Device]
	var target *device
	for _, d := range n.Devices {
		if d.IP == m.To || d.Host == m.To {
			target = d
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		fail("нет такого устройства в сети")
		return
	}
	if target.ctrl == nil {
		s.mu.Unlock()
		fail("устройство " + target.Host + " сейчас не в сети")
		return
	}
	if len(s.calls) >= maxPendingCalls {
		s.mu.Unlock()
		fail("сервер занят, попробуй ещё раз")
		return
	}
	callID := randomID()
	cl := &call{net: id, target: target.ID, answer: make(chan answer, 1)}
	s.calls[callID] = cl
	target.ctrl.send(msg{Op: "incoming", Call: callID, From: from.IP, FromHost: from.Host, Port: m.Port})
	s.mu.Unlock()

	var got answer
	timedOut := false
	select {
	case got = <-cl.answer:
	case <-time.After(callTimeout):
		timedOut = true
	}
	s.mu.Lock()
	delete(s.calls, callID)
	s.mu.Unlock()
	if timedOut {
		// Ответ мог успеть прийти между таймаутом и удалением вызова —
		// тогда его соединение никому не нужно.
		select {
		case late := <-cl.answer:
			if late.conn != nil {
				late.conn.Close()
			}
		default:
		}
		fail("устройство не ответило")
		return
	}
	if got.conn == nil {
		if got.err == "" {
			got.err = "устройство отказало в соединении"
		}
		fail(got.err)
		return
	}
	peerConn := got.conn
	if err := writeMsg(conn, msg{Op: "connected"}); err != nil {
		conn.Close()
		peerConn.Close()
		return
	}
	splice(conn, peerConn)
}

func (s *server) accept(conn net.Conn, m msg) {
	id := netID(m.Net)
	s.mu.Lock()
	cl := s.calls[m.Call]
	if cl == nil || cl.net != id || cl.target != m.Device {
		s.mu.Unlock()
		conn.Close()
		return
	}
	delete(s.calls, m.Call)
	s.mu.Unlock()

	if !m.OK {
		// Устройство отказалось (порт закрыт, входящие выключены) — говорим
		// вызывающему сразу, а не по таймауту.
		conn.Close()
		cl.answer <- answer{err: m.Error}
		return
	}
	cl.answer <- answer{conn: conn}
}

// splice перекладывает байты в обе стороны и ждёт, пока закончат обе.
func splice(a, b net.Conn) {
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

func randomID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func main() {
	listen := flag.String("listen", "127.0.0.1:47831", "адрес, на котором слушать (только localhost)")
	state := flag.String("state", "/var/lib/meshd/state.json", "где хранить адреса устройств")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("неверный адрес %q: %v", *listen, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		log.Fatalf("слушать можно только localhost, а не %q: снаружи сервис доступен быть не должен", host)
	}

	s := newServer(*state)
	go s.saveLoop()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("не могу занять %s: %v", *listen, err)
	}
	log.Printf("meshd слушает %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.handle(conn)
	}
}
