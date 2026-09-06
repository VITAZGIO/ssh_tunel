// Прототип слоя, которого не хватает для Android.
//
// На Windows и Linux система сама направляет приложения в наш прокси. В Android
// такой настройки нет: VpnService отдаёт файловый дескриптор, в который сыплются
// сырые IP-пакеты. Здесь они превращаются обратно в соединения и уходят в наше
// существующее ядро.
package core

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core/device"
	"github.com/xjasonlyu/tun2socks/v2/core/device/fdbased"
	"github.com/xjasonlyu/tun2socks/v2/core/option"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"sshtunnel/internal/udprelay"
)

// Core — то, чем на самом деле является наш internal/tunnel.Tunnel.
//
// Важно, что это не просто «открой соединение»: внутри применяются правила
// маршрутизации, пишется лог и считаются байты. Соединение отдаётся целиком,
// потому что перекачкой данных занимается тоже ядро.
type Core interface {
	ServeConn(conn net.Conn, target string, byIP bool)
}

// Resolver превращает адрес назначения обратно в имя хоста.
//
// Это задел под fake-IP: приложение спрашивает DNS, мы отдаём выдуманный адрес
// из 198.18.0.0/15 и запоминаем пару. Когда придёт соединение на этот адрес,
// наверх уходит имя, а не адрес, — и его резолвит сервер, как на компьютере.
type Resolver func(ip string) (host string, ok bool)

// Stats — счётчики для проверок.
type Stats struct {
	mu        sync.Mutex
	tcpOpen   int
	udpDrop   int
	dnsAsked  int
	v6Blocked int
	blocked   int
	targets   []string

	// seenUDP помнит, о каких адресах уже сообщили: QUIC шлёт пакеты пачками,
	// и без этого журнал состоял бы из одной строки, повторённой сто раз.
	seenUDP map[string]bool
}

func (s *Stats) tcp(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tcpOpen++
	s.targets = append(s.targets, target)
}

func (s *Stats) udp() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.udpDrop++
}

func (s *Stats) v6() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v6Blocked++
}

func (s *Stats) dns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dnsAsked++
}

func (s *Stats) block() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked++
}

// Snapshot — копия счётчиков без гонок.
func (s *Stats) Snapshot() (tcpOpen, udpDrop, dnsAsked int, targets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tcpOpen, s.udpDrop, s.dnsAsked, append([]string(nil), s.targets...)
}

// Counts — счётчики для показа человеку.
func (s *Stats) Counts() (tcpOpen, udpDrop, dnsAsked, v6Blocked, adsBlocked int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tcpOpen, s.udpDrop, s.dnsAsked, s.v6Blocked, s.blocked
}

// Handler — мост между сетевым стеком и ядром туннеля.
type Handler struct {
	Core    Core
	Resolve Resolver
	Stats   *Stats

	// DNS отвечает на запросы приложений. Если не задан, запросы к 53-му порту
	// отбиваются вместе с остальным UDP.
	DNS *DNS

	// Log показывает человеку то, что отвергнуто. Без этого отказы невидимы:
	// приложение просто «не работает», а причина остаётся только в счётчике.
	Log func(line string)

	// UDPRelay отдаёт клиента проброса UDP через сервер (см.
	// sshtunnel/internal/udprelay) — вызывается заново на каждый новый поток,
	// потому что сама функция уже кеширует и переподключает соединение (см.
	// tunnel.Tunnel.UDPRelay). nil, либо когда сам вызов вернул nil, — UDP
	// кроме DNS по-прежнему отбрасывается, как и раньше.
	UDPRelay func() *udprelay.Client
}

func (h *Handler) logf(format string, a ...any) {
	if h.Log != nil {
		h.Log(fmt.Sprintf(format, a...))
	}
}

// Engine — собранный стек поверх дескриптора от VpnService.
type Engine struct {
	dev   device.Device
	stack *stack.Stack
}

// Start поднимает стек на готовом дескрипторе. На Android сюда приходит fd,
// который вернул VpnService.Builder.establish(); в тестах — дескриптор обычного
// tun-устройства, что с точки зрения кода одно и то же.
//
// Порядок здесь принципиален: обработчики протоколов ставятся ДО создания
// сетевой карты. Если поставить их после, карта уже начинает разбирать пакеты,
// и получается гонка — детектор её ловит, это проверено.
func Start(fd int, mtu uint32, h *Handler) (*Engine, error) {
	if h == nil || h.Core == nil {
		return nil, errors.New("нужен обработчик с ядром туннеля")
	}
	if h.Stats == nil {
		h.Stats = &Stats{}
	}

	dev, err := fdbased.Open(strconv.Itoa(fd), mtu, 0)
	if err != nil {
		return nil, fmt.Errorf("открыть устройство: %w", err)
	}

	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol, ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol,
			icmp.NewProtocol4, icmp.NewProtocol6,
		},
	})

	fail := func(format string, a ...any) (*Engine, error) {
		s.Close()
		dev.Close()
		return nil, fmt.Errorf(format, a...)
	}

	// Настройки буферов и алгоритмов TCP берём готовые: они подобраны под
	// работу поверх туннеля, а не поверх настоящей сетевой карты.
	if err := option.WithDefault()(s); err != nil {
		return fail("настройки стека: %w", err)
	}

	// TCP: каждое соединение отдаём наверх, в ядро туннеля.
	tcpFwd := tcp.NewForwarder(s, 0, 2048, func(r *tcp.ForwarderRequest) {
		// Адрес назначения забираем до Complete: тот освобождает запрос,
		// и обращение к ID после него роняет процесс.
		id := r.ID()

		// Соединения IPv6 ведём через сервер наравне с остальными.
		//
		// Сначала мы их отвергали, рассчитывая, что приложение попробует IPv4.
		// На живом телефоне выяснилось, что не пробует: YouTube упирался в
		// отказ и переставал делать что бы то ни было. Пусть лучше сервер сам
		// откроет соединение — SSH умеет и шестую версию.
		if id.LocalAddress.Len() == 16 {
			h.Stats.v6()
		}

		var wq waiter.Queue
		ep, epErr := r.CreateEndpoint(&wq)
		if epErr != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		go h.serveTCP(gonet.NewTCPConn(&wq, ep), id)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP: единственное, что мы по нему обслуживаем, — это DNS. Всё остальное
	// через SSH пронести нечем, и здесь оно заканчивается.
	//
	// Ответ «не обработано» — не небрежность, а способ заставить стек послать
	// ICMP «порт недоступен». Если промолчать, отправитель ждёт до таймаута:
	// браузер вместо быстрого отката с QUIC на TCP выдаёт ошибку. С отказом
	// он переключается за доли миллисекунды.
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		var wq waiter.Queue
		ep, epErr := r.CreateEndpoint(&wq)
		if epErr != nil {
			return false
		}
		go h.serveDNS(gonet.NewUDPConn(&wq, ep))
		return true
	})

	// Остальной UDP — через ретранслятор на сервере, если включено (см.
	// UDPRelay). Отдельный forwarder, а не общий с DNS: тому всегда нужен
	// serveDNS, а этому — только когда клиент ретранслятора вообще поднят.
	udpRelayFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		relay := h.UDPRelay()
		if relay == nil {
			return false
		}
		var wq waiter.Queue
		ep, epErr := r.CreateEndpoint(&wq)
		if epErr != nil {
			return false
		}
		go h.serveUDPRelay(gonet.NewUDPConn(&wq, ep), r.ID(), relay)
		return true
	})

	s.SetTransportProtocolHandler(udp.ProtocolNumber,
		func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			if id.LocalPort == 53 && h.DNS != nil {
				return udpFwd.HandlePacket(id, pkt)
			}
			if h.UDPRelay != nil && udpRelayFwd.HandlePacket(id, pkt) {
				return true
			}
			h.Stats.udp()
			h.logUDP(id)
			return false
		})

	nicID := s.NextNICID()
	if tErr := s.CreateNIC(nicID, dev); tErr != nil {
		return fail("создать сетевую карту: %s", tErr)
	}
	// Адрес назначения у пакетов чужой — стек должен принимать всё подряд
	// и уметь отвечать с адресов, которые ему не принадлежат.
	if tErr := s.SetPromiscuousMode(nicID, true); tErr != nil {
		return fail("режим приёма всего: %s", tErr)
	}
	if tErr := s.SetSpoofing(nicID, true); tErr != nil {
		return fail("ответы с чужих адресов: %s", tErr)
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	return &Engine{dev: dev, stack: s}, nil
}

// logUDP сообщает о каждом новом адресе, куда ломился UDP, — но только один
// раз про каждый. Иначе журнал состоял бы из одной строки, повторённой сто раз.
func (h *Handler) logUDP(id stack.TransportEndpointID) {
	where := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))

	h.Stats.mu.Lock()
	if h.Stats.seenUDP == nil {
		h.Stats.seenUDP = make(map[string]bool)
	}
	// Предел на случай, если приложение перебирает адреса без конца.
	fresh := !h.Stats.seenUDP[where] && len(h.Stats.seenUDP) < 60
	if fresh {
		h.Stats.seenUDP[where] = true
	}
	h.Stats.mu.Unlock()

	if !fresh {
		return
	}
	what := "UDP"
	if id.LocalPort == 443 {
		what = "QUIC"
	}
	h.logf("%s %s — через SSH не проходит", what, where)
}

// serveTCP восстанавливает адрес назначения и передаёт соединение в ядро.
func (h *Handler) serveTCP(conn net.Conn, id stack.TransportEndpointID) {
	host := id.LocalAddress.String()
	byIP := true
	if h.Resolve != nil {
		if name, ok := h.Resolve(host); ok {
			// Имя восстановлено из таблицы fake-IP: дальше его разрешит сервер,
			// как и на компьютере. Наружу DNS-запрос не уходит.
			host, byIP = name, false
		}
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(id.LocalPort)))
	h.Stats.tcp(target)
	h.Core.ServeConn(conn, target, byIP)
}

// serveDNS обслуживает один запрос и закрывает соединение: DNS по UDP
// устроен как «вопрос — ответ», держать канал открытым незачем.
func (h *Handler) serveDNS(conn net.Conn) {
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	// 512 байт — предел обычного запроса по UDP; с EDNS бывает больше,
	// поэтому берём с запасом.
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	h.Stats.dns()

	reply, err := h.DNS.Answer(buf[:n])
	if err != nil {
		return
	}
	conn.Write(reply)
}

// udpRelayIdle — сколько ждать следующей датаграммы от приложения, прежде
// чем закрыть сессию: сама сессия долгоживущая (звонок, игра), в отличие от
// одноразового DNS-запроса.
const udpRelayIdle = 60 * time.Second

// serveUDPRelay ведёт один UDP-поток приложения через ретранслятор на
// сервере, пока тот не замолчит дольше udpRelayIdle или соединение до
// ретранслятора не оборвётся.
func (h *Handler) serveUDPRelay(conn net.Conn, id stack.TransportEndpointID, relay *udprelay.Client) {
	defer conn.Close()

	host := id.LocalAddress.String()
	if h.Resolve != nil {
		if name, ok := h.Resolve(host); ok {
			// Имя восстановлено из таблицы fake-IP — как и для TCP: дальше
			// его разрешит сам сервер, а не телефон.
			host = name
		}
	}
	port := id.LocalPort

	sessionID := relay.Open(func(data []byte, fromHost string, fromPort uint16) {
		conn.Write(data)
	})
	defer relay.Close(sessionID)

	buf := make([]byte, 65535)
	for {
		conn.SetReadDeadline(time.Now().Add(udpRelayIdle))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if err := relay.Send(sessionID, host, port, buf[:n]); err != nil {
			return
		}
	}
}

func (e *Engine) Close() {
	if e.stack != nil {
		e.stack.Close()
	}
	if e.dev != nil {
		e.dev.Close()
	}
}
