package tunnel

// SOCKS5 UDP ASSOCIATE (RFC 1928, раздел 7): приложение просит завести
// локальный UDP-адрес, куда можно слать датаграммы вперемешку с разными
// адресатами — прокси относит их куда сказано и приносит ответы обратно.
// Здесь это и есть точка входа для UDP через SSH: пробрасывать датаграммы
// самим по себе SSH не умеет, поэтому дальше используется ретранслятор на
// сервере (см. sshtunnel/internal/udprelay) — то же самое, что и на Android.
//
// Ассоциация живёт, пока не закроется TCP-соединение, на котором пришёл
// запрос ASSOCIATE, — это прямо оговорено в RFC 1928 и одновременно самый
// простой способ узнать, что приложение больше не собирается слать UDP:
// отдельного кадра "стоп" в самом SOCKS5 для этого нет.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
)

func (t *Tunnel) handleSOCKS5UDPAssociate(conn net.Conn) {
	relay := t.UDPRelay()
	if relay == nil {
		// Выключено в настройках либо ретранслятор недоступен — то же самое,
		// что видит клиент, если бы прокси вообще не умел ASSOCIATE.
		writeSOCKS5Reply(conn, 0x01)
		conn.Close()
		return
	}

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		writeSOCKS5Reply(conn, 0x01)
		conn.Close()
		return
	}

	local, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP.To4() == nil {
		pc.Close()
		writeSOCKS5Reply(conn, 0x01)
		conn.Close()
		return
	}
	reply := make([]byte, 0, 10)
	reply = append(reply, 0x05, 0x00, 0x00, 0x01)
	reply = append(reply, local.IP.To4()...)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(local.Port))
	reply = append(reply, portBuf[:]...)
	if _, err := conn.Write(reply); err != nil {
		pc.Close()
		conn.Close()
		return
	}

	// clientAddr — куда слать ответы: обновляется по адресу, с которого
	// последний раз пришла датаграмма от приложения (обычно он один и тот же
	// на всё время ассоциации).
	var clientAddr atomic.Pointer[net.UDPAddr]

	sessionID := relay.Open(func(data []byte, fromHost string, fromPort uint16) {
		addr := clientAddr.Load()
		if addr == nil {
			return
		}
		pc.WriteToUDP(encodeSOCKS5UDP(fromHost, fromPort, data), addr)
	})

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientAddr.Store(from)
			host, port, data, err := decodeSOCKS5UDP(buf[:n])
			if err != nil {
				continue // битый пакет от приложения — просто игнорируем этот
			}
			relay.Send(sessionID, host, port, data)
		}
	}()

	// Держим TCP-соединение открытым и ждём, пока приложение его само не
	// закроет, — ровно так по RFC 1928 завершается ассоциация. Сам поток
	// оттуда не нужен: команда ASSOCIATE — это разовый запрос-ответ, дальше
	// разговор идёт только по UDP.
	io.Copy(io.Discard, conn)

	pc.Close()
	conn.Close()
	relay.Close(sessionID)
	<-readDone
}

// encodeSOCKS5UDP заворачивает датаграмму в заголовок запроса/ответа UDP
// ASSOCIATE: RSV(2) FRAG(1) ATYP ADDR PORT DATA. FRAG всегда 0 — фрагментацию
// не поддерживаем, как и почти все клиенты её не используют.
func encodeSOCKS5UDP(host string, port uint16, data []byte) []byte {
	out := make([]byte, 0, 10+len(data))
	out = append(out, 0, 0, 0)
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, 0x01)
			out = append(out, v4...)
		} else {
			out = append(out, 0x04)
			out = append(out, ip.To16()...)
		}
	} else {
		n := len(host)
		if n > 255 {
			n = 255
		}
		out = append(out, 0x03, byte(n))
		out = append(out, host[:n]...)
	}
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	out = append(out, portBuf[:]...)
	out = append(out, data...)
	return out
}

// decodeSOCKS5UDP разбирает тот же заголовок в обратную сторону.
func decodeSOCKS5UDP(pkt []byte) (host string, port uint16, data []byte, err error) {
	if len(pkt) < 4 {
		return "", 0, nil, fmt.Errorf("udp associate: короткий пакет")
	}
	if pkt[2] != 0 {
		return "", 0, nil, fmt.Errorf("udp associate: фрагментация не поддерживается")
	}
	atyp := pkt[3]
	rest := pkt[4:]
	switch atyp {
	case 0x01:
		if len(rest) < 4 {
			return "", 0, nil, fmt.Errorf("udp associate: короткий IPv4")
		}
		host = net.IP(rest[:4]).String()
		rest = rest[4:]
	case 0x03:
		if len(rest) < 1 {
			return "", 0, nil, fmt.Errorf("udp associate: нет длины имени")
		}
		n := int(rest[0])
		rest = rest[1:]
		if len(rest) < n {
			return "", 0, nil, fmt.Errorf("udp associate: короткое имя")
		}
		host = string(rest[:n])
		rest = rest[n:]
	case 0x04:
		if len(rest) < 16 {
			return "", 0, nil, fmt.Errorf("udp associate: короткий IPv6")
		}
		host = net.IP(rest[:16]).String()
		rest = rest[16:]
	default:
		return "", 0, nil, fmt.Errorf("udp associate: неизвестный ATYP %d", atyp)
	}
	if len(rest) < 2 {
		return "", 0, nil, fmt.Errorf("udp associate: нет порта")
	}
	port = binary.BigEndian.Uint16(rest[:2])
	data = append([]byte(nil), rest[2:]...)
	return host, port, data, nil
}
