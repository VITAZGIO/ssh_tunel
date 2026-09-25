package mesh

// Прямые соединения между устройствами (p2p), минуя сервер.
//
// Как в NetBird и Tailscale: соединение сразу идёт через сервер (meshd), а
// параллельно устройства пробуют найти прямой путь.
//
//  1. У каждого устройства один UDP-сокет для прямых соединений. Через него
//     же оно спрашивает у сервера (STUN), каким адресом видно снаружи, и
//     раз в 20 секунд повторяет — чтобы NAT не закрыл «дырку».
//  2. Устройство A посылает через meshd устройству B предложение: свои
//     адреса-кандидаты (внешний и локальные) и отпечаток ключа. B отвечает
//     тем же. meshd идёт по SSH — подменить предложение по дороге нельзя.
//  3. Оба одновременно шлют друг другу короткие UDP-пакеты («пробивание»):
//     каждый NAT видит исходящий пакет и пропускает встречный.
//  4. Как только путь нашёлся, A открывает по нему QUIC (шифрование TLS 1.3,
//     потоки, контроль скорости). Ключи проверяются по отпечаткам из
//     предложения: чужое устройство не встанет посередине.
//  5. Каждое соединение сети устройств — отдельный поток QUIC. Пропал прямой
//     путь — новые соединения снова идут через сервер.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	p2pALPN = "sshtunnel-mesh-p2p"
	// Пакеты пробивания начинаются с нулевого байта: QUIC так никогда не
	// начинается (у него всегда взведён бит 0x40), и транспорт отдаёт их нам.
	punchPing = 1
	punchPong = 2
	// p2pRetry — не чаще, чем раз в столько, пробовать прямой путь к тому же
	// устройству: неудачное пробивание стоит пакетов и времени.
	p2pRetry = 45 * time.Second
	// punchFor — сколько длится одна попытка пробивания.
	punchFor = 8 * time.Second
)

var punchMagic = [4]byte{0, 'p', '2', 'p'}

// directWire — строка отчёта p2pstat для meshd.
type directWire struct {
	IP    string  `json:"ip"`
	RTTMs float64 `json:"rttMs,omitempty"`
}

type p2p struct {
	c      *Client
	send   func(wire) error
	pc     net.PacketConn
	tr     *quic.Transport
	ln     *quic.Listener
	cert   tls.Certificate
	fp     string
	stun   *net.UDPAddr
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	public   netip.AddrPort
	peers    map[string]*p2pPeer
	attempts map[string]*p2pAttempt
	stunWait map[[12]byte]chan netip.AddrPort
}

type p2pPeer struct {
	conn    quic.Connection
	rtt     time.Duration
	lastTry time.Time
}

type p2pAttempt struct {
	id        [16]byte
	peerIP    string
	initiator bool
	fp        string
	cands     []*net.UDPAddr
	answered  chan struct{} // ответ на предложение пришёл (у инициатора)
	path      chan net.Addr // первый подтверждённый путь
	done      chan struct{}
	once      sync.Once
	finished  sync.Once
}

func (a *p2pAttempt) finish() { a.finished.Do(func() { close(a.done) }) }

var quicConfig = &quic.Config{
	HandshakeIdleTimeout: 5 * time.Second,
	MaxIdleTimeout:       30 * time.Second,
	KeepAlivePeriod:      10 * time.Second,
}

// startP2P открывает сокет прямых соединений и начинает слушать.
func startP2P(ctx context.Context, c *Client, send func(wire) error, listen func(string) (net.PacketConn, error), stun *net.UDPAddr) (*p2p, error) {
	// quic-go жалуется в журнал, что не смог увеличить буфер сокета, —
	// на скорость прямых соединений между устройствами это почти не влияет.
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	if listen == nil {
		listen = func(network string) (net.PacketConn, error) { return net.ListenPacket(network, ":0") }
	}
	pc, err := listen("udp4")
	if err != nil {
		return nil, err
	}
	cert, fp, err := p2pCert()
	if err != nil {
		pc.Close()
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &p2p{
		c: c, send: send, pc: pc, cert: cert, fp: fp, stun: stun, ctx: pctx, cancel: cancel,
		peers: map[string]*p2pPeer{}, attempts: map[string]*p2pAttempt{},
		stunWait: map[[12]byte]chan netip.AddrPort{},
	}
	p.tr = &quic.Transport{Conn: pc}
	p.ln, err = p.tr.Listen(p.serverTLS(), quicConfig)
	if err != nil {
		cancel()
		pc.Close()
		return nil, err
	}
	go p.readLoop()
	go p.acceptLoop()
	go p.stunLoop()
	go p.healthLoop()
	return p, nil
}

func (p *p2p) close() {
	p.cancel()
	p.mu.Lock()
	for _, peer := range p.peers {
		if peer.conn != nil {
			peer.conn.CloseWithError(0, "")
		}
	}
	for _, a := range p.attempts {
		a.finish()
	}
	p.mu.Unlock()
	p.ln.Close()
	p.tr.Close()
	p.pc.Close()
}

// ---------- ключи ----------

// p2pCert — свежий ключ на каждое подключение к сети: хранить его незачем,
// отпечаток и так едет через meshd в каждом предложении.
func p2pCert() (tls.Certificate, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, keyFP(pub), nil
}

func keyFP(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])
}

func certFP(raw [][]byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("нет сертификата")
	}
	cert, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("не тот ключ")
	}
	return keyFP(pub), nil
}

// peerFP — отпечаток ключа собеседника в установленном соединении.
func peerFP(conn quic.Connection) (string, error) {
	certs := conn.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("нет сертификата")
	}
	pub, ok := certs[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("не тот ключ")
	}
	return keyFP(pub), nil
}

func (p *p2p) serverTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.cert},
		NextProtos:   []string{p2pALPN},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
		// Кто пришёл, проверяем в acceptLoop: отпечаток должен совпасть с
		// тем, что устройство прислало в предложении через meshd.
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			fp, err := certFP(raw)
			if err != nil {
				return err
			}
			if !p.expecting(fp) {
				return errors.New("неизвестное устройство")
			}
			return nil
		},
	}
}

func (p *p2p) clientTLS(expect string) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{p.cert},
		NextProtos:         []string{p2pALPN},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // цепочки нет — проверяем отпечаток ниже
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			fp, err := certFP(raw)
			if err != nil {
				return err
			}
			if fp != expect {
				return errors.New("ключ собеседника не совпал с присланным через сервер")
			}
			return nil
		},
	}
}

func (p *p2p) expecting(fp string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.attempts {
		if !a.initiator && a.fp == fp {
			return true
		}
	}
	return false
}

// ---------- UDP: STUN и пробивание ----------

func (p *p2p) readLoop() {
	buf := make([]byte, 1500)
	for {
		n, from, err := p.tr.ReadNonQUICPacket(p.ctx, buf)
		if err != nil {
			return
		}
		b := buf[:n]
		switch {
		case n >= 20 && binary.BigEndian.Uint16(b[0:2]) == stunBindResp:
			var id [12]byte
			copy(id[:], b[8:20])
			p.mu.Lock()
			ch := p.stunWait[id]
			p.mu.Unlock()
			if ch != nil {
				if ap, ok := stunParse(b, id); ok {
					select {
					case ch <- ap:
					default:
					}
				}
			}
		case n >= 21 && [4]byte(b[0:4]) == punchMagic:
			var id [16]byte
			copy(id[:], b[5:21])
			p.punchPacket(b[4], id, from)
		}
	}
}

func (p *p2p) punchPacket(typ byte, id [16]byte, from net.Addr) {
	p.mu.Lock()
	a := p.attempts[string(id[:])]
	p.mu.Unlock()
	if a == nil {
		return // чужое или устаревшее — не отвечаем, чтобы не быть отражателем
	}
	if typ == punchPing {
		p.tr.WriteTo(punchPacket(punchPong, id), from)
	}
	select {
	case a.path <- from:
	default:
	}
}

func punchPacket(typ byte, id [16]byte) []byte {
	b := make([]byte, 21)
	copy(b, punchMagic[:])
	b[4] = typ
	copy(b[5:], id[:])
	return b
}

// stunQuery узнаёт внешний адрес этого сокета.
func (p *p2p) stunQuery() (netip.AddrPort, error) {
	var id [12]byte
	rand.Read(id[:])
	ch := make(chan netip.AddrPort, 1)
	p.mu.Lock()
	p.stunWait[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.stunWait, id)
		p.mu.Unlock()
	}()
	req := stunRequest(id, false)
	for try := 0; try < 3; try++ {
		if _, err := p.tr.WriteTo(req, p.stun); err != nil {
			return netip.AddrPort{}, err
		}
		select {
		case ap := <-ch:
			return ap, nil
		case <-time.After(800 * time.Millisecond):
		case <-p.ctx.Done():
			return netip.AddrPort{}, p.ctx.Err()
		}
	}
	return netip.AddrPort{}, errors.New("сервер не ответил по UDP")
}

func (p *p2p) stunLoop() {
	for {
		if ap, err := p.stunQuery(); err == nil {
			p.mu.Lock()
			p.public = ap
			p.mu.Unlock()
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
	}
}

// candidates — адреса, по которым к этому устройству можно попробовать
// достучаться: внешний (через NAT) и локальные (если собеседник в той же сети).
func (p *p2p) candidates() []string {
	var out []string
	p.mu.Lock()
	if p.public.IsValid() {
		out = append(out, p.public.String())
	}
	p.mu.Unlock()
	la, ok := p.pc.LocalAddr().(*net.UDPAddr)
	if !ok {
		return out
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok || !ip.Unmap().Is4() || !ip.IsPrivate() || Range.Contains(ip.Unmap()) {
			continue
		}
		out = append(out, netip.AddrPortFrom(ip.Unmap(), uint16(la.Port)).String())
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func parseCands(list []string) []*net.UDPAddr {
	var out []*net.UDPAddr
	for _, s := range list {
		if ap, err := netip.ParseAddrPort(s); err == nil && ap.Addr().Is4() {
			out = append(out, net.UDPAddrFromAddrPort(ap))
		}
		if len(out) >= 16 {
			break
		}
	}
	return out
}

// ---------- сигнализация через meshd ----------

// connect начинает попытку прямого пути к устройству, если её не было
// недавно. Не ждёт: соединение, ради которого вызвали, уже идёт через сервер.
func (p *p2p) connect(peerIP string) {
	p.mu.Lock()
	peer := p.peers[peerIP]
	if peer == nil {
		peer = &p2pPeer{}
		p.peers[peerIP] = peer
	}
	busy := peer.conn != nil || time.Since(peer.lastTry) < p2pRetry
	for _, a := range p.attempts {
		if a.peerIP == peerIP {
			busy = true
		}
	}
	if busy {
		p.mu.Unlock()
		return
	}
	peer.lastTry = time.Now()
	a := p.newAttemptLocked(peerIP, true)
	p.mu.Unlock()

	if err := p.send(wire{Op: "p2p", To: peerIP, Call: hex.EncodeToString(a.id[:]), Cands: p.candidates(), FP: p.fp}); err != nil {
		p.dropAttempt(a)
		return
	}
	go func() {
		select {
		case <-a.answered:
			p.punch(a)
		case <-time.After(6 * time.Second):
			p.dropAttempt(a)
		case <-p.ctx.Done():
		}
	}()
}

func (p *p2p) newAttemptLocked(peerIP string, initiator bool) *p2pAttempt {
	a := &p2pAttempt{peerIP: peerIP, initiator: initiator,
		answered: make(chan struct{}), path: make(chan net.Addr, 4), done: make(chan struct{})}
	rand.Read(a.id[:])
	p.attempts[string(a.id[:])] = a
	return a
}

func (p *p2p) dropAttempt(a *p2pAttempt) {
	a.finish()
	p.mu.Lock()
	delete(p.attempts, string(a.id[:]))
	p.mu.Unlock()
}

// signal — предложение или ответ от другого устройства (op p2p).
func (p *p2p) signal(m wire) {
	raw, err := hex.DecodeString(m.Call)
	if err != nil || len(raw) != 16 || m.From == "" {
		return
	}
	var id [16]byte
	copy(id[:], raw)
	// Свой адрес — до p.mu: Status сама заглядывает в p2p (порядок замков
	// всегда c.mu → p.mu).
	self := p.c.Status().Self.IP.String()

	p.mu.Lock()
	if a := p.attempts[string(id[:])]; a != nil && a.initiator {
		// Ответ на наше предложение.
		p.mu.Unlock()
		if !m.OK || m.FP == "" {
			p.dropAttempt(a)
			return
		}
		a.once.Do(func() {
			p.mu.Lock()
			a.fp, a.cands = m.FP, parseCands(m.Cands)
			p.mu.Unlock()
			close(a.answered)
		})
		return
	}
	// Встречное предложение, пока мы сами пробуем к нему же: побеждает
	// устройство с меньшим адресом, иначе каждый ждал бы своего.
	for _, a := range p.attempts {
		if a.peerIP == m.From && a.initiator {
			if self < m.From {
				p.mu.Unlock()
				p.send(wire{Op: "p2p", To: m.From, Call: m.Call, OK: false})
				return
			}
		}
	}
	if peer := p.peers[m.From]; peer != nil && peer.conn != nil {
		// Прямой путь уже есть, а собеседник его потерял — старый закроем,
		// строим заново.
		peer.conn.CloseWithError(0, "")
		peer.conn = nil
	}
	a := &p2pAttempt{id: id, peerIP: m.From, fp: m.FP, cands: parseCands(m.Cands),
		answered: make(chan struct{}), path: make(chan net.Addr, 4), done: make(chan struct{})}
	p.attempts[string(id[:])] = a
	p.mu.Unlock()

	if err := p.send(wire{Op: "p2p", To: m.From, Call: m.Call, Cands: p.candidates(), FP: p.fp, OK: true}); err != nil {
		p.dropAttempt(a)
		return
	}
	go p.punch(a)
}

// punch — пробивание: шлём пакеты на все адреса собеседника, пока путь не
// найдётся. Инициатор по найденному пути открывает QUIC, второй ждёт его.
func (p *p2p) punch(a *p2pAttempt) {
	defer p.dropAttempt(a)
	ping := punchPacket(punchPing, a.id)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(punchFor)
	p.mu.Lock()
	cands := append([]*net.UDPAddr(nil), a.cands...)
	p.mu.Unlock()
	for _, c := range cands {
		p.tr.WriteTo(ping, c)
	}
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-a.done:
			return
		case <-deadline:
			p.c.log("info", "Сеть устройств: прямой путь к "+a.peerIP+" не нашёлся — соединения идут через сервер")
			return
		case <-tick.C:
			for _, c := range cands {
				p.tr.WriteTo(ping, c)
			}
		case path := <-a.path:
			if !a.initiator {
				continue // ждём QUIC от инициатора (acceptLoop), пинги продолжаются
			}
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			conn, err := p.tr.Dial(ctx, path, p.clientTLS(a.fp), quicConfig)
			cancel()
			if err != nil {
				continue // попробуем по следующему подтверждённому пути
			}
			p.adopt(a.peerIP, conn)
			return
		}
	}
}

func (p *p2p) acceptLoop() {
	for {
		conn, err := p.ln.Accept(p.ctx)
		if err != nil {
			return
		}
		fp, err := peerFP(conn)
		if err != nil {
			conn.CloseWithError(1, "")
			continue
		}
		p.mu.Lock()
		var match *p2pAttempt
		for _, a := range p.attempts {
			if !a.initiator && a.fp == fp {
				match = a
			}
		}
		p.mu.Unlock()
		if match == nil {
			conn.CloseWithError(1, "")
			continue
		}
		p.adopt(match.peerIP, conn)
		match.finish()
	}
}

// adopt — прямое соединение установлено: с этого момента новые соединения к
// этому устройству идут по нему.
func (p *p2p) adopt(peerIP string, conn quic.Connection) {
	p.mu.Lock()
	peer := p.peers[peerIP]
	if peer == nil {
		peer = &p2pPeer{}
		p.peers[peerIP] = peer
	}
	if peer.conn != nil && peer.conn != conn {
		peer.conn.CloseWithError(0, "")
	}
	peer.conn = conn
	p.mu.Unlock()
	p.c.log("info", fmt.Sprintf("Сеть устройств: прямое соединение с %s (%s)", peerIP, conn.RemoteAddr()))
	go p.serve(peerIP, conn)
	p.measure(peerIP)
	p.report()
}

func (p *p2p) drop(peerIP string, conn quic.Connection) {
	p.mu.Lock()
	peer := p.peers[peerIP]
	gone := peer != nil && peer.conn == conn
	if gone {
		peer.conn = nil
		peer.rtt = 0
	}
	p.mu.Unlock()
	conn.CloseWithError(0, "")
	if gone {
		p.c.log("info", "Сеть устройств: прямое соединение с "+peerIP+" пропало — дальше через сервер")
		p.report()
	}
}

// serve принимает потоки от собеседника: каждый — соединение к службе этого
// устройства (или замер задержки, порт 0).
func (p *p2p) serve(peerIP string, conn quic.Connection) {
	for {
		str, err := conn.AcceptStream(p.ctx)
		if err != nil {
			p.drop(peerIP, conn)
			return
		}
		go p.handleStream(peerIP, conn, str)
	}
}

// Заголовок потока: версия (1), порт (2 байта). Ответ — 1 байт.
const (
	streamOK       = 0
	streamRefused  = 1
	streamNoListen = 2
)

func (p *p2p) handleStream(peerIP string, conn quic.Connection, str quic.Stream) {
	var hdr [3]byte
	str.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(str, hdr[:]); err != nil || hdr[0] != 1 {
		str.CancelRead(0)
		str.Close()
		return
	}
	str.SetReadDeadline(time.Time{})
	port := int(binary.BigEndian.Uint16(hdr[1:3]))
	if port == 0 { // замер задержки
		str.Write([]byte{streamOK})
		str.Close()
		return
	}
	if !p.c.cfg.AllowIncoming {
		str.Write([]byte{streamRefused})
		str.Close()
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	local, err := p.c.LocalDial(ctx, port)
	cancel()
	if err != nil {
		str.Write([]byte{streamNoListen})
		str.Close()
		return
	}
	str.Write([]byte{streamOK})
	p.c.log("info", fmt.Sprintf("Сеть устройств: %s напрямую → порт %d этого устройства", peerIP, port))
	pump(streamConn{str, conn}, local)
}

// dialDirect — соединение с портом устройства по прямому пути. nil, nil —
// прямого пути нет (идти через сервер).
func (p *p2p) dialDirect(peerIP string, port int) (net.Conn, error) {
	p.mu.Lock()
	peer := p.peers[peerIP]
	var conn quic.Connection
	if peer != nil {
		conn = peer.conn
	}
	p.mu.Unlock()
	if conn == nil {
		return nil, nil
	}
	str, err := p.openStream(conn, port)
	if err != nil {
		p.drop(peerIP, conn)
		return nil, nil
	}
	var status [1]byte
	str.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(str, status[:]); err != nil {
		str.CancelRead(0)
		str.Close()
		return nil, nil
	}
	str.SetReadDeadline(time.Time{})
	switch status[0] {
	case streamRefused:
		str.Close()
		return nil, errors.New("устройство не принимает входящие соединения")
	case streamNoListen:
		str.Close()
		return nil, fmt.Errorf("на устройстве ничего не слушает порт %d", port)
	}
	return streamConn{str, conn}, nil
}

func (p *p2p) openStream(conn quic.Connection, port int) (quic.Stream, error) {
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	defer cancel()
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	hdr := []byte{1, byte(port >> 8), byte(port)}
	if _, err := str.Write(hdr); err != nil {
		str.CancelRead(0)
		str.Close()
		return nil, err
	}
	return str, nil
}

// measure — задержка прямого пути (пустой поток туда и обратно).
func (p *p2p) measure(peerIP string) bool {
	p.mu.Lock()
	peer := p.peers[peerIP]
	var conn quic.Connection
	if peer != nil {
		conn = peer.conn
	}
	p.mu.Unlock()
	if conn == nil {
		return false
	}
	start := time.Now()
	str, err := p.openStream(conn, 0)
	if err == nil {
		var b [1]byte
		str.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.ReadFull(str, b[:])
		str.Close()
	}
	if err != nil {
		p.drop(peerIP, conn)
		return false
	}
	rtt := time.Since(start)
	p.mu.Lock()
	if peer.conn == conn {
		peer.rtt = rtt
	}
	p.mu.Unlock()
	return true
}

func (p *p2p) healthLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		p.mu.Lock()
		var ips []string
		for ip, peer := range p.peers {
			if peer.conn != nil {
				ips = append(ips, ip)
			}
		}
		p.mu.Unlock()
		for _, ip := range ips {
			p.measure(ip)
		}
		p.report()
	}
}

// report — сервер узнаёт, с кем у устройства прямые соединения (для панели).
func (p *p2p) report() {
	p.mu.Lock()
	list := []directWire{}
	for ip, peer := range p.peers {
		if peer.conn != nil {
			list = append(list, directWire{IP: ip, RTTMs: float64(peer.rtt.Microseconds()) / 1000})
		}
	}
	p.mu.Unlock()
	p.send(wire{Op: "p2pstat", Direct: list})
}

// direct — прямой ли путь к устройству и его задержка.
func (p *p2p) direct(peerIP string) (bool, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[peerIP]; peer != nil && peer.conn != nil {
		return true, peer.rtt
	}
	return false, 0
}

// streamConn — поток QUIC как net.Conn.
type streamConn struct {
	quic.Stream
	conn quic.Connection
}

func (s streamConn) LocalAddr() net.Addr  { return s.conn.LocalAddr() }
func (s streamConn) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }
func (s streamConn) CloseWrite() error    { return s.Stream.Close() }
func (s streamConn) Close() error {
	s.Stream.CancelRead(0)
	return s.Stream.Close()
}
