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
	mu      sync.Mutex
	tcpOpen int
	udpDrop int
	targets []string
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

// Snapshot — копия счётчиков без гонок.
func (s *Stats) Snapshot() (tcpOpen, udpDrop int, targets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tcpOpen, s.udpDrop, append([]string(nil), s.targets...)
}

// Handler — мост между сетевым стеком и ядром туннеля.
type Handler struct {
	Core    Core
	Resolve Resolver
	Stats   *Stats
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

	// UDP: SSH переносить его не умеет, поэтому здесь он заканчивается.
	//
	// Ответ «не обработано» — не небрежность, а способ заставить стек послать
	// ICMP «порт недоступен». Если промолчать, отправитель ждёт до таймаута:
	// браузер вместо быстрого отката с QUIC на TCP выдаёт ошибку. С отказом
	// он переключается за доли миллисекунды.
	s.SetTransportProtocolHandler(udp.ProtocolNumber,
		func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			h.Stats.udp()
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

func (e *Engine) Close() {
	if e.stack != nil {
		e.stack.Close()
	}
	if e.dev != nil {
		e.dev.Close()
	}
}
