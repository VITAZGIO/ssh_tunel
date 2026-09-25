//go:build linux

package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// fakePinger — «устройство», которое отвечает через delay, если адрес ok.
type fakePinger struct {
	match netip.Prefix
	ok    netip.Addr
	delay time.Duration
}

func (f fakePinger) PingMatch(dst netip.Addr) bool { return f.match.Contains(dst) }
func (f fakePinger) Ping(dst netip.Addr) (time.Duration, error) {
	if dst != f.ok {
		time.Sleep(50 * time.Millisecond)
		return 0, errors.New("нет ответа")
	}
	time.Sleep(f.delay)
	return f.delay, nil
}

func inetSum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func echoRequest(id, seq uint16, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = 8
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	copy(b[8:], payload)
	binary.BigEndian.PutUint16(b[2:], inetSum(b))
	return b
}

// readEcho ждёт эхо-ответ с нужным id от адреса from.
func readEcho(c net.PacketConn, from string, id uint16, wait time.Duration) ([]byte, bool) {
	c.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 2000)
	for {
		n, addr, err := c.ReadFrom(buf)
		if err != nil {
			return nil, false
		}
		b := buf[:n]
		if addr.String() == from && n >= 8 && b[0] == 0 && binary.BigEndian.Uint16(b[4:]) == id {
			return append([]byte(nil), b...), true
		}
	}
}

func TestPingСетиУстройств(t *testing.T) {
	// Адрес известен после запуска, а обработчик нужен до него.
	var okp atomic.Pointer[netip.Addr]
	h := &Handler{Core: directCore{}, Ping: func() Pinger {
		ok := okp.Load()
		if ok == nil {
			return nil
		}
		return fakePinger{match: netip.PrefixFrom(*ok, 24).Masked(), ok: *ok, delay: 60 * time.Millisecond}
	}}
	peer := startEngine(t, h)
	ok := netip.MustParseAddr(peer)
	okp.Store(&ok)
	c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		t.Skipf("нет сырых сокетов: %v", err)
	}
	defer c.Close()

	payload := []byte("ping-сети-устройств-0123456789")
	start := time.Now()
	c.WriteTo(echoRequest(4242, 7, payload), &net.IPAddr{IP: net.ParseIP(peer)})
	b, got := readEcho(c, peer, 4242, 3*time.Second)
	if !got {
		t.Fatal("эхо-ответ не пришёл")
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("ответ пришёл раньше, чем ответило устройство: %v", el)
	}
	if binary.BigEndian.Uint16(b[6:]) != 7 || !bytes.Equal(b[8:], payload) {
		t.Fatalf("ответ не совпал с запросом: % x", b)
	}
	if inetSum(b) != 0 {
		t.Fatal("контрольная сумма ICMP неверна")
	}

	// Устройство не ответило — ответа нет.
	other := netip.AddrFrom4([4]byte{ok.As4()[0], ok.As4()[1], ok.As4()[2], 99}).String()
	c.WriteTo(echoRequest(4343, 1, payload), &net.IPAddr{IP: net.ParseIP(other)})
	if _, got := readEcho(c, other, 4343, 700*time.Millisecond); got {
		t.Fatal("ответ за устройство, которое не ответило")
	}

	// TCP через тот же стек по-прежнему работает (перехват не мешает).
	ln := echoServer(t)
	defer ln.Close()
}
