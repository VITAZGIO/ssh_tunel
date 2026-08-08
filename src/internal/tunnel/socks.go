package tunnel

import (
	"fmt"
	"io"
	"net"
	"strconv"
)

// Локальный SOCKS-сервер понимает и SOCKS5 (RFC 1928), и SOCKS4/4a. Обе версии
// нужны на практике: разные приложения Windows ходят разными, причём по одному
// и тому же порту, и сервер, понимающий только SOCKS5, молча рвал соединения
// от SOCKS4-клиентов.
//
// Версия протокола — первый байт, по нему и разводим.
func (t *Tunnel) handleSOCKS(conn net.Conn) {
	var ver [1]byte
	if _, err := io.ReadFull(conn, ver[:]); err != nil {
		conn.Close()
		return
	}
	switch ver[0] {
	case 0x05:
		t.handleSOCKS5(conn)
	case 0x04:
		t.handleSOCKS4(conn)
	default:
		// Частый случай: в поле SOCKS-прокси прописали адрес, а приложение шлёт
		// туда обычный HTTP-запрос. Подсказываем, вместо того чтобы молчать.
		t.bus.Warnf("На SOCKS-порт пришёл не SOCKS-запрос (первый байт %d). Для HTTP-прокси используй порт %s", ver[0], portOf(t.cfg.HTTPAddr))
		conn.Close()
	}
}

func (t *Tunnel) handleSOCKS5(conn net.Conn) {
	buf := make([]byte, 262)

	// Приветствие: NMETHODS, METHODS... (VER уже прочитан).
	if _, err := io.ReadFull(conn, buf[:1]); err != nil {
		conn.Close()
		return
	}
	nMethods := int(buf[0])
	if nMethods > 0 {
		if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
			conn.Close()
			return
		}
	}
	// Отвечать методом, которого клиент не предлагал, нельзя: строгие клиенты
	// на это рвут соединение.
	noAuth := false
	for i := 0; i < nMethods; i++ {
		if buf[i] == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		conn.Write([]byte{0x05, 0xFF})
		conn.Close()
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		conn.Close()
		return
	}

	// Запрос: VER CMD RSV ATYP ADDR PORT
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		conn.Close()
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 { // поддерживаем только CONNECT
		writeSOCKS5Reply(conn, 0x07)
		conn.Close()
		return
	}

	var host string
	byIP := true
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			conn.Close()
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // доменное имя — приложение доверило резолв нам, утечки DNS нет
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			conn.Close()
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:l]); err != nil {
			conn.Close()
			return
		}
		host = string(buf[:l])
		byIP = false
	case 0x04: // IPv6
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			conn.Close()
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		writeSOCKS5Reply(conn, 0x08)
		conn.Close()
		return
	}

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		conn.Close()
		return
	}
	port := int(buf[0])<<8 | int(buf[1])

	t.serve(request{
		conn:     conn,
		target:   net.JoinHostPort(host, strconv.Itoa(port)),
		proto:    "socks5",
		byIP:     byIP,
		writeErr: func(error) { writeSOCKS5Reply(conn, 0x05) },
		writeOK:  func(c net.Conn) error { return writeSOCKS5Reply(c, 0x00) },
	})
}

func (t *Tunnel) handleSOCKS4(conn net.Conn) {
	// CMD(1) DSTPORT(2) DSTIP(4) USERID(...\0) [DOMAIN(...\0) — если SOCKS4a]
	head := make([]byte, 7)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return
	}
	if head[0] != 0x01 { // только CONNECT
		writeSOCKS4Reply(conn, 0x5B)
		conn.Close()
		return
	}
	port := int(head[1])<<8 | int(head[2])

	if err := skipUntilNull(conn); err != nil { // USERID, нам он не нужен
		conn.Close()
		return
	}

	var host string
	proto := "socks4"
	byIP := true
	// SOCKS4a: адрес вида 0.0.0.x (x≠0) означает, что дальше идёт имя хоста —
	// так клиент просит прокси самому резолвить имя.
	if head[3] == 0 && head[4] == 0 && head[5] == 0 && head[6] != 0 {
		name, err := readUntilNull(conn)
		if err != nil {
			conn.Close()
			return
		}
		host, proto, byIP = name, "socks4a", false
	} else {
		host = net.IP(head[3:7]).String()
	}

	t.serve(request{
		conn:     conn,
		target:   net.JoinHostPort(host, strconv.Itoa(port)),
		proto:    proto,
		byIP:     byIP,
		writeErr: func(error) { writeSOCKS4Reply(conn, 0x5B) },
		writeOK:  func(c net.Conn) error { return writeSOCKS4Reply(c, 0x5A) },
	})
}

func skipUntilNull(r io.Reader) error {
	var b [1]byte
	for i := 0; i < 256; i++ { // защита от бесконечного мусора
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return err
		}
		if b[0] == 0 {
			return nil
		}
	}
	return fmt.Errorf("строка без завершающего нуля")
}

func readUntilNull(r io.Reader) (string, error) {
	var out []byte
	var b [1]byte
	for len(out) < 255 {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return string(out), nil
		}
		out = append(out, b[0])
	}
	return "", fmt.Errorf("слишком длинное имя хоста")
}

func writeSOCKS5Reply(conn net.Conn, code byte) error {
	// BND.ADDR/BND.PORT нулевые — клиентам этого достаточно для CONNECT.
	_, err := conn.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func writeSOCKS4Reply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{0x00, code, 0, 0, 0, 0, 0, 0})
	return err
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return p
}
