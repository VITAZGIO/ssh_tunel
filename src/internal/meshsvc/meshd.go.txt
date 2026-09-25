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
//	управление: → {"op":"hello","net":КЛЮЧ,"device":ID,"name":ИМЯ,
//	               "platform":..,"os":..,"app":..,"mode":..,"via":..}
//	            ← {"op":"welcome","ip":..,"host":..,"peers":[..]}
//	            дальше ← peers / incoming / pong / ping, → ping / pong
//	            (сервер шлёт ping с меткой времени и по pong меряет задержку)
//	вызов:      → {"op":"dial","net":..,"device":..,"to":IP,"port":N}
//	            ← {"op":"connected"} и дальше сырые байты — или {"op":"error"}
//	ответ:      → {"op":"accept","net":..,"device":..,"call":ID,"ok":true}
//	            и дальше сырые байты
//	сервер:     → {"op":"server","device":ID,"name":..,"host":..,"addrs":[..]}
//	            ← {"op":"welcome"}, дальше ping / pong, как у устройств
//
// Несколько серверов. На «побочном» сервере meshd не запущен: его
// 127.0.0.1:47831 — это SSH-проброс (ssh -L) к meshd «главного», поэтому
// устройства, подключённые к любому из серверов, оказываются в одной сети.
// Сам побочный сервер (его панель) держит здесь управляющее соединение
// "server" — чтобы главный видел его на схеме и знал качество связи с ним.
//
// Для панели на сервере (ssh_tunnel_panel) есть отдельный вход — HTTP по
// unix-сокету (флаг -admin): список устройств с характеристиками и качеством
// связи, переименование, удаление. Сокет доступен только владельцу службы и
// root: через SSH-туннель до него не дотянуться, в отличие от порта.
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
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	protoVersion = 2
	// version — версия самого meshd, её видно в панели.
	version = "2"

	maxLine          = 4096
	maxNetworks      = 1000
	maxDevicesPerNet = 1024
	maxPendingCalls  = 256
	maxRelays        = 64

	// callTimeout — сколько ждать, пока вызванное устройство откроет встречное
	// соединение.
	callTimeout = 10 * time.Second
	// ctrlIdle — управляющее соединение без единого ping дольше этого
	// считается мёртвым (клиент шлёт ping раз в 25 секунд).
	ctrlIdle = 90 * time.Second
	// forgetAfter — устройство, которое не появлялось столько времени,
	// забывается, и его адрес может достаться новому.
	forgetAfter = 180 * 24 * time.Hour
	// pingEvery — как часто сервер сам меряет задержку до устройства.
	pingEvery = 15 * time.Second
	// maxField — предел для строк, которые устройство рассказывает о себе.
	maxField = 64
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

	// Что устройство рассказывает о себе в hello (всё необязательное).
	Platform string `json:"platform,omitempty"` // windows, linux, android
	OS       string `json:"os,omitempty"`       // версия системы, если известна
	App      string `json:"app,omitempty"`      // версия ssh_tunnel
	Mode     string `json:"mode,omitempty"`     // proxy или vpn
	Via      string `json:"via,omitempty"`      // через какой сервер пришло
	Hostname string `json:"hostname,omitempty"` // имя компьютера

	// Addrs — адреса побочного сервера (op "server"): по ним панель узнаёт,
	// какие устройства пришли через него.
	Addrs []string `json:"addrs,omitempty"`

	// T — метка времени (наносекунды) в ping/pong для замера задержки.
	T int64 `json:"t,omitempty"`
}

type peer struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen,omitempty"`
	Platform string `json:"platform,omitempty"`
	Via      string `json:"via,omitempty"`
}

// ---------- состояние ----------

type device struct {
	ID string `json:"id"`
	// Name — как устройство назвало себя само; Alias — как его переименовали
	// в панели. Показывается Alias, если он есть: иначе переименование
	// жило бы до первого переподключения устройства.
	Name      string    `json:"name"`
	Alias     string    `json:"alias,omitempty"`
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	FirstSeen time.Time `json:"firstSeen,omitempty"`
	LastSeen  time.Time `json:"lastSeen"`
	Platform  string    `json:"platform,omitempty"`
	OS        string    `json:"os,omitempty"`
	App       string    `json:"app,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Via       string    `json:"via,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Sessions  int64     `json:"sessions,omitempty"`
	BytesUp   int64     `json:"bytesUp,omitempty"`
	BytesDown int64     `json:"bytesDown,omitempty"`

	ctrl        *ctrlConn
	connectedAt time.Time
	up, down    atomic.Int64 // трафик за время жизни процесса, поверх Bytes*
	activeCalls atomic.Int64
	link        linkStats
}

// displayName — имя для людей: переименование из панели главнее.
func (d *device) displayName() string {
	if d.Alias != "" {
		return d.Alias
	}
	return d.Name
}

// linkStats — качество связи с устройством по замерам ping/pong. Под s.mu.
type linkStats struct {
	sent, got int
	last      time.Duration
	avg       float64 // миллисекунды, скользящее среднее
	jitter    float64 // миллисекунды, скользящее среднее отклонения
}

func (l *linkStats) add(rtt time.Duration) {
	ms := float64(rtt) / float64(time.Millisecond)
	if l.got == 0 {
		l.avg = ms
	} else {
		l.jitter = 0.7*l.jitter + 0.3*math.Abs(ms-float64(l.last)/float64(time.Millisecond))
		l.avg = 0.7*l.avg + 0.3*ms
	}
	l.last = rtt
	l.got++
}

// loss — доля пропавших замеров, в процентах. Последний отправленный ping
// мог ещё не вернуться — его не считаем.
func (l *linkStats) loss() float64 {
	if l.sent <= 1 {
		return 0
	}
	lost := l.sent - 1 - l.got
	if lost < 0 {
		lost = 0
	}
	return 100 * float64(lost) / float64(l.sent-1)
}

// quality — оценка связи словом, для значка в панели.
func (l *linkStats) quality() string {
	switch {
	case l.got == 0:
		return "unknown"
	case l.loss() > 20 || l.avg > 400:
		return "poor"
	case l.loss() > 5 || l.avg > 150:
		return "fair"
	case l.avg > 60:
		return "good"
	default:
		return "excellent"
	}
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

// relay — побочный сервер, подключённый к этому. В памяти: после
// перезапуска он переподключится сам.
type relay struct {
	ID, Name, Host, App string
	Addrs               []string
	connectedAt         time.Time
	ctrl                *ctrlConn
	link                linkStats
}

type server struct {
	mu      sync.Mutex
	nets    map[string]*network // по хешу ключа
	calls   map[string]*call
	relays  map[string]*relay
	path    string
	saveMu  sync.Mutex
	dirty   chan struct{}
	started time.Time
}

func newServer(path string) *server {
	s := &server{
		nets:    map[string]*network{},
		calls:   map[string]*call{},
		relays:  map[string]*relay{},
		path:    path,
		dirty:   make(chan struct{}, 1),
		started: time.Now(),
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
	// Живые счётчики трафика — поверх сохранённых: на диск идёт сумма.
	type saved struct {
		d        *device
		up, down int64
	}
	var restore []saved
	for _, n := range s.nets {
		for _, d := range n.Devices {
			restore = append(restore, saved{d, d.BytesUp, d.BytesDown})
			d.BytesUp += d.up.Load()
			d.BytesDown += d.down.Load()
		}
	}
	data, err := json.MarshalIndent(s.nets, "", " ")
	for _, r := range restore {
		r.d.BytesUp, r.d.BytesDown = r.up, r.down
	}
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
		p := peer{Name: d.displayName(), Host: d.Host, IP: d.IP, Online: d.ctrl != nil,
			Platform: d.Platform, Via: d.Via}
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
	if m.Op == "server" {
		s.relayLink(conn, m)
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
	now := time.Now()
	d.Name = name
	d.Host = uniqueHostLocked(n, m.Device, d.displayName())
	if d.FirstSeen.IsZero() {
		d.FirstSeen = now
	}
	d.LastSeen = now
	// Необязательные сведения: старый клиент их не шлёт — прежние не стираем.
	for dst, src := range map[*string]string{
		&d.Platform: m.Platform, &d.OS: m.OS, &d.App: m.App,
		&d.Mode: m.Mode, &d.Via: m.Via, &d.Hostname: m.Hostname,
	} {
		if v := clip(src); v != "" {
			*dst = v
		}
	}
	d.Sessions++
	d.connectedAt = now
	d.link = linkStats{}
	d.ctrl = c
	c.send(msg{Op: "welcome", V: protoVersion, IP: d.IP, Host: d.Host, Peers: peersLocked(n)})
	broadcastLocked(n)
	s.mu.Unlock()
	s.markDirty()

	s.keepAlive(conn, c, func(f func(*linkStats)) {
		s.mu.Lock()
		if d.ctrl == c {
			f(&d.link)
		}
		s.mu.Unlock()
	})

	s.mu.Lock()
	if d.ctrl == c {
		d.ctrl = nil
		d.LastSeen = time.Now()
		broadcastLocked(n)
	}
	s.mu.Unlock()
	s.markDirty()
}

// keepAlive обслуживает управляющее соединение, пока оно живо: отвечает на
// ping и сам меряет задержку. link даёт доступ к замерам под блокировкой —
// или не вызывает f вовсе, если соединение уже сменилось новым.
//
// Замер: сервер шлёт ping с меткой времени, другая сторона возвращает её в
// pong. Старые клиенты незнакомое сообщение пропускают — у них задержка
// просто остаётся неизвестной.
func (s *server) keepAlive(conn net.Conn, c *ctrlConn, link func(func(*linkStats))) {
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		send := func() {
			link(func(l *linkStats) { l.sent++ })
			c.send(msg{Op: "ping", T: time.Now().UnixNano()})
		}
		send()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				send()
			}
		}
	}()

	r := bufio.NewReaderSize(conn, maxLine)
	for {
		conn.SetReadDeadline(time.Now().Add(ctrlIdle))
		line, err := r.ReadSlice('\n')
		if err != nil {
			return
		}
		var in msg
		if json.Unmarshal(line, &in) != nil {
			continue
		}
		switch in.Op {
		case "ping":
			c.send(msg{Op: "pong", T: in.T})
		case "pong":
			if in.T > 0 {
				rtt := time.Since(time.Unix(0, in.T))
				if rtt >= 0 && rtt < time.Minute {
					link(func(l *linkStats) { l.add(rtt) })
				}
			}
		}
	}
}

// relayLink — управляющее соединение побочного сервера.
func (s *server) relayLink(conn net.Conn, m msg) {
	defer conn.Close()
	id := clip(m.Device)
	if id == "" {
		writeMsg(conn, msg{Op: "error", Error: "нет id сервера"})
		return
	}
	c := newCtrlConn(conn)
	defer c.close()
	r := &relay{ID: id, Name: clip(m.Name), Host: clip(m.Host), App: clip(m.App), connectedAt: time.Now(), ctrl: c}
	for _, a := range m.Addrs {
		if v := clip(a); v != "" && len(r.Addrs) < 8 {
			r.Addrs = append(r.Addrs, v)
		}
	}

	s.mu.Lock()
	if old := s.relays[id]; old != nil {
		old.ctrl.conn.Close()
	} else if len(s.relays) >= maxRelays {
		s.mu.Unlock()
		writeMsg(conn, msg{Op: "error", Error: "подключено слишком много серверов"})
		return
	}
	s.relays[id] = r
	s.mu.Unlock()
	log.Printf("подключился сервер %q (%s)", r.Name, r.Host)
	c.send(msg{Op: "welcome", V: protoVersion})

	s.keepAlive(conn, c, func(f func(*linkStats)) {
		s.mu.Lock()
		if s.relays[id] == r {
			f(&r.link)
		}
		s.mu.Unlock()
	})

	s.mu.Lock()
	if s.relays[id] == r {
		delete(s.relays, id)
	}
	s.mu.Unlock()
	log.Printf("сервер %q отключился", r.Name)
}

// clip обрезает строку, которую устройство прислало о себе, до разумной.
func clip(v string) string {
	v = strings.TrimSpace(v)
	if r := []rune(v); len(r) > maxField {
		v = string(r[:maxField])
	}
	return v
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
	// Трафик — обоим устройствам: вызывающему «отдано» то, что ушло к
	// вызванному, и наоборот.
	from.activeCalls.Add(1)
	target.activeCalls.Add(1)
	defer from.activeCalls.Add(-1)
	defer target.activeCalls.Add(-1)
	splice(conn, peerConn, &from.up, &target.down, &target.up, &from.down)
	s.markDirty()
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
// Счётчики: aUp/bDown — то, что пришло от a и ушло к b; bUp/aDown — обратно.
func splice(a, b net.Conn, aUp, bDown, bUp, aDown *atomic.Int64) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn, c1, c2 *atomic.Int64) {
		io.Copy(dst, countingReader{src, c1, c2})
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(b, a, aUp, bDown)
	go cp(a, b, bUp, aDown)
	<-done
	<-done
	a.Close()
	b.Close()
}

type countingReader struct {
	r      io.Reader
	c1, c2 *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.c1.Add(int64(n))
		c.c2.Add(int64(n))
	}
	return n, err
}

// ---------- вход для панели (HTTP по unix-сокету) ----------

// adminDevice — устройство глазами панели.
type adminDevice struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Alias       string  `json:"alias,omitempty"`
	Display     string  `json:"display"`
	Host        string  `json:"host"`
	IP          string  `json:"ip"`
	Online      bool    `json:"online"`
	Platform    string  `json:"platform,omitempty"`
	OS          string  `json:"os,omitempty"`
	App         string  `json:"app,omitempty"`
	Mode        string  `json:"mode,omitempty"`
	Via         string  `json:"via,omitempty"`
	Hostname    string  `json:"hostname,omitempty"`
	FirstSeen   int64   `json:"firstSeen,omitempty"`
	LastSeen    int64   `json:"lastSeen,omitempty"`
	ConnectedAt int64   `json:"connectedAt,omitempty"`
	Sessions    int64   `json:"sessions"`
	RTTMs       float64 `json:"rttMs,omitempty"`
	RTTAvgMs    float64 `json:"rttAvgMs,omitempty"`
	JitterMs    float64 `json:"jitterMs,omitempty"`
	LossPct     float64 `json:"lossPct"`
	Quality     string  `json:"quality"`
	BytesUp     int64   `json:"bytesUp"`
	BytesDown   int64   `json:"bytesDown"`
	ActiveCalls int64   `json:"activeCalls"`
}

type adminNetwork struct {
	ID      string        `json:"id"`
	Devices []adminDevice `json:"devices"`
}

// adminServer — подключённый побочный сервер глазами панели.
type adminServer struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Addrs       []string `json:"addrs,omitempty"`
	App         string   `json:"app,omitempty"`
	ConnectedAt int64    `json:"connectedAt"`
	RTTMs       float64  `json:"rttMs,omitempty"`
	RTTAvgMs    float64  `json:"rttAvgMs,omitempty"`
	JitterMs    float64  `json:"jitterMs,omitempty"`
	LossPct     float64  `json:"lossPct"`
	Quality     string   `json:"quality"`
}

type adminState struct {
	Version   string         `json:"version"`
	StartedAt int64          `json:"startedAt"`
	Networks  []adminNetwork `json:"networks"`
	Servers   []adminServer  `json:"servers"`
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func (s *server) adminState() adminState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := adminState{Version: version, StartedAt: s.started.Unix()}
	for id, n := range s.nets {
		an := adminNetwork{ID: id}
		for _, d := range n.Devices {
			ad := adminDevice{
				ID: d.ID, Name: d.Name, Alias: d.Alias, Display: d.displayName(),
				Host: d.Host, IP: d.IP, Online: d.ctrl != nil,
				Platform: d.Platform, OS: d.OS, App: d.App, Mode: d.Mode, Via: d.Via,
				Hostname: d.Hostname, FirstSeen: unixOrZero(d.FirstSeen), LastSeen: unixOrZero(d.LastSeen),
				Sessions:  d.Sessions,
				BytesUp:   d.BytesUp + d.up.Load(),
				BytesDown: d.BytesDown + d.down.Load(),
				Quality:   "unknown",
			}
			if d.ctrl != nil {
				ad.ConnectedAt = unixOrZero(d.connectedAt)
				ad.ActiveCalls = d.activeCalls.Load()
				ad.Quality = d.link.quality()
				ad.LossPct = round1(d.link.loss())
				if d.link.got > 0 {
					ad.RTTMs = round1(float64(d.link.last) / float64(time.Millisecond))
					ad.RTTAvgMs = round1(d.link.avg)
					ad.JitterMs = round1(d.link.jitter)
				}
			}
			an.Devices = append(an.Devices, ad)
		}
		sort.Slice(an.Devices, func(i, j int) bool { return an.Devices[i].IP < an.Devices[j].IP })
		st.Networks = append(st.Networks, an)
	}
	sort.Slice(st.Networks, func(i, j int) bool { return len(st.Networks[i].Devices) > len(st.Networks[j].Devices) })
	st.Servers = []adminServer{}
	for _, r := range s.relays {
		as := adminServer{
			ID: r.ID, Name: r.Name, Host: r.Host, Addrs: r.Addrs, App: r.App,
			ConnectedAt: unixOrZero(r.connectedAt),
			Quality:     r.link.quality(),
			LossPct:     round1(r.link.loss()),
		}
		if r.link.got > 0 {
			as.RTTMs = round1(float64(r.link.last) / float64(time.Millisecond))
			as.RTTAvgMs = round1(r.link.avg)
			as.JitterMs = round1(r.link.jitter)
		}
		st.Servers = append(st.Servers, as)
	}
	sort.Slice(st.Servers, func(i, j int) bool { return st.Servers[i].Name < st.Servers[j].Name })
	return st
}

// rename задаёт устройству имя из панели. Пустое — вернуть его собственное.
func (s *server) rename(netID, devID, alias string) error {
	alias = clip(alias)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.nets[netID]
	if n == nil || n.Devices[devID] == nil {
		return errors.New("нет такого устройства")
	}
	d := n.Devices[devID]
	d.Alias = alias
	d.Host = uniqueHostLocked(n, devID, d.displayName())
	broadcastLocked(n)
	s.markDirty()
	return nil
}

// forget убирает устройство из сети: его адрес освобождается. Если оно
// сейчас на связи — соединение рвётся; вернуться оно сможет, но уже как новое.
func (s *server) forget(netID, devID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.nets[netID]
	if n == nil || n.Devices[devID] == nil {
		return errors.New("нет такого устройства")
	}
	d := n.Devices[devID]
	delete(n.Devices, devID)
	if d.ctrl != nil {
		d.ctrl.conn.Close()
		d.ctrl = nil
	}
	broadcastLocked(n)
	s.markDirty()
	return nil
}

func (s *server) adminHandler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
	type req struct {
		Net    string `json:"net"`
		Device string `json:"device"`
		Alias  string `json:"alias"`
	}
	decode := func(r *http.Request) (req, error) {
		var q req
		if r.Method != http.MethodPost {
			return q, errors.New("нужен POST")
		}
		err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&q)
		return q, err
	}
	mux.HandleFunc("/v1/state", func(w http.ResponseWriter, r *http.Request) {
		reply(w, s.adminState(), nil)
	})
	mux.HandleFunc("/v1/rename", func(w http.ResponseWriter, r *http.Request) {
		q, err := decode(r)
		if err == nil {
			err = s.rename(q.Net, q.Device, q.Alias)
		}
		reply(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("/v1/forget", func(w http.ResponseWriter, r *http.Request) {
		q, err := decode(r)
		if err == nil {
			err = s.forget(q.Net, q.Device)
		}
		reply(w, map[string]bool{"ok": true}, err)
	})
	return mux
}

// serveAdmin слушает unix-сокет для панели. Права 0600: достучаться может
// только владелец службы и root — клиентам туннеля (другие пользователи
// системы) он недоступен даже через проброс unix-сокетов в SSH.
func (s *server) serveAdmin(path string) error {
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	log.Printf("вход для панели: %s", path)
	go (&http.Server{Handler: s.adminHandler(), ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
	return nil
}

func randomID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func main() {
	listen := flag.String("listen", "127.0.0.1:47831", "адрес, на котором слушать (только localhost)")
	state := flag.String("state", "/var/lib/meshd/state.json", "где хранить адреса устройств")
	admin := flag.String("admin", "", "unix-сокет для панели на сервере (пусто — выключен)")
	showVersion := flag.Bool("version", false, "напечатать версию и выйти")
	flag.Parse()
	if *showVersion {
		os.Stdout.WriteString(version + "\n")
		return
	}

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("неверный адрес %q: %v", *listen, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		log.Fatalf("слушать можно только localhost, а не %q: снаружи сервис доступен быть не должен", host)
	}

	s := newServer(*state)
	go s.saveLoop()
	if *admin != "" {
		if err := s.serveAdmin(*admin); err != nil {
			log.Printf("вход для панели не открылся (%v) — сеть работает, но панель её не увидит", err)
		}
	}

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
