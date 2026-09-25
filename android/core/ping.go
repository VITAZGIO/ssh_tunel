package core

// ping к адресам, которые обслуживает сама программа (сеть устройств,
// 198.19.x.y).
//
// ping — это ICMP «эхо-запрос»: не соединение, а одиночный пакет «ты тут?».
// Сетевой стек его не передаёт никуда, поэтому такие пакеты перехватываются
// здесь, до стека: программа спрашивает само устройство (по прямому пути или
// через сервер) и, когда оно ответило, возвращает системе «эхо-ответ». Время
// в ping — настоящее: ответ уходит ровно тогда, когда устройство ответило.
// Не ответило — ответа нет, и ping честно пишет «превышен интервал».

import (
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core/device"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// Pinger — кто отвечает за ping на свои адреса (см. mesh.Client).
type Pinger interface {
	// PingMatch — свой ли адрес; вызывается на каждый ICMP-пакет, должна
	// быть быстрой.
	PingMatch(dst netip.Addr) bool
	// Ping ждёт ответа устройства; ошибка — не ответило.
	Ping(dst netip.Addr) (time.Duration, error)
}

// maxPings — сколько ping ждут ответа одновременно; лишние отбрасываются.
const maxPings = 64

// pingDevice — устройство, которое перехватывает ping к своим адресам по
// дороге в стек. Остальные пакеты идут дальше как есть.
type pingDevice struct {
	device.Device
	pinger func() Pinger
	disp   stack.NetworkDispatcher
	busy   atomic.Int32
}

func (p *pingDevice) Attach(d stack.NetworkDispatcher) {
	p.disp = d
	if d == nil {
		p.Device.Attach(nil)
		return
	}
	p.Device.Attach(p)
}

func (p *pingDevice) DeliverNetworkPacket(proto tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	if proto == header.IPv4ProtocolNumber && p.takePing(pkt) {
		return
	}
	p.disp.DeliverNetworkPacket(proto, pkt)
}

func (p *pingDevice) DeliverLinkPacket(proto tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	p.disp.DeliverLinkPacket(proto, pkt)
}

// takePing забирает эхо-запрос к своему адресу. Смотрит только заголовки —
// пакет целиком копируется, лишь если это действительно наш ping.
func (p *pingDevice) takePing(pkt *stack.PacketBuffer) bool {
	hdr, ok := pkt.Data().PullUp(header.IPv4MinimumSize)
	if !ok {
		return false
	}
	ip := header.IPv4(hdr)
	if ip.Protocol() != uint8(header.ICMPv4ProtocolNumber) || ip.More() || ip.FragmentOffset() != 0 {
		return false
	}
	pg := p.pinger()
	if pg == nil {
		return false
	}
	dst := netip.AddrFrom4(ip.DestinationAddress().As4())
	if !pg.PingMatch(dst) {
		return false
	}
	ihl := int(ip.HeaderLength())
	full, ok := pkt.Data().PullUp(ihl + header.ICMPv4MinimumSize)
	if !ok || ihl < header.IPv4MinimumSize || header.ICMPv4(full[ihl:]).Type() != header.ICMPv4Echo {
		return false
	}
	v := pkt.ToView()
	req := append([]byte(nil), v.AsSlice()...)
	v.Release()
	reply := echoReply(req)
	if reply == nil {
		return true // битый запрос — молча выбрасываем
	}
	if p.busy.Add(1) > maxPings {
		p.busy.Add(-1)
		return true
	}
	go func() {
		defer p.busy.Add(-1)
		if _, err := pg.Ping(dst); err != nil {
			return // не ответило — пусть ping покажет «превышен интервал»
		}
		p.write(reply)
	}()
	return true
}

// write отдаёт готовый IP-пакет системе.
func (p *pingDevice) write(b []byte) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(b)})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	var list stack.PacketBufferList
	list.PushBack(pkt)
	p.Device.WritePackets(list)
	pkt.DecRef()
}

// echoReply собирает эхо-ответ на эхо-запрос req (IPv4 целиком): адреса
// меняются местами, тип — «ответ», данные те же. nil — запрос битый.
func echoReply(req []byte) []byte {
	if len(req) < header.IPv4MinimumSize {
		return nil
	}
	ip := header.IPv4(req)
	ihl, total := int(ip.HeaderLength()), int(ip.TotalLength())
	if ihl < header.IPv4MinimumSize || total > len(req) || total < ihl+header.ICMPv4MinimumSize {
		return nil
	}
	icmpReq := req[ihl:total]
	out := make([]byte, header.IPv4MinimumSize+len(icmpReq))
	oip := header.IPv4(out)
	oip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(out)),
		ID:          ip.ID(),
		TTL:         64,
		Protocol:    uint8(header.ICMPv4ProtocolNumber),
		SrcAddr:     ip.DestinationAddress(),
		DstAddr:     ip.SourceAddress(),
	})
	oip.SetChecksum(^oip.CalculateChecksum())
	ic := header.ICMPv4(out[header.IPv4MinimumSize:])
	copy(ic, icmpReq)
	ic.SetType(header.ICMPv4EchoReply)
	ic.SetCode(0)
	ic.SetChecksum(0)
	ic.SetChecksum(^checksum.Checksum(ic, 0))
	return out
}
