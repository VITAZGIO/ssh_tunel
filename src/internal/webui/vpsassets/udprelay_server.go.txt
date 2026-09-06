// Команда udprelay — маленький сервер-ретранслятор UDP для ssh_tunnel,
// который ставится на сервер (см. мастер настройки VPS в webui).
//
// Слушает ТОЛЬКО 127.0.0.1: достучаться до него можно исключительно через уже
// прошедший SSH-аутентификацию туннель (клиент открывает обычный direct-tcpip
// до этого же localhost), никакого нового порта наружу не появляется, и как
// открытым ретранслятором им воспользоваться нельзя — снаружи он попросту не
// виден. Отдельной аутентификации кадров поэтому нет: граница доступа — сам
// факт, что до порта вообще можно достучаться.
//
// Протокол — см. пакет sshtunnel/internal/udprelay, где он описан подробно.
// Здесь код протокола продублирован нарочно: этот файл предназначен для
// компиляции ПРЯМО НА СЕРВЕРЕ, сам по себе, одной командой "go build", без
// остального модуля и без сети для go mod download — поэтому ни одного
// внешнего или внутреннего импорта, кроме стандартной библиотеки.
//
// Границы: сессию всегда открывает клиент, входящих соединений извне не
// бывает; на сессию — один сокет UDP с таймером бездействия; ограничение на
// число сессий и на число одновременных TCP-соединений — чтобы одна
// испорченная сторона не расплодила сокеты без предела. Это не система QoS
// и не NAT-трансляция общего назначения — только зеркало клиентских кадров
// в настоящие датаграммы и обратно.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- протокол (копия sshtunnel/internal/udprelay, см. её комментарии) ----------

const (
	maxFrameBody = 65*1024 + 32
	flagFIN      = 1 << 0
	addrNone     = 0
	addrIPv4     = 1
	addrDomain   = 3
	addrIPv6     = 4
)

type frame struct {
	sessionID uint32
	fin       bool
	host      string
	port      uint16
	data      []byte
}

func writeFrame(w io.Writer, f frame) error {
	body, err := marshalBody(f)
	if err != nil {
		return err
	}
	if len(body) > maxFrameBody {
		return errors.New("кадр больше предела")
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

func marshalBody(f frame) ([]byte, error) {
	buf := make([]byte, 0, 24+len(f.data))
	var sid [4]byte
	binary.BigEndian.PutUint32(sid[:], f.sessionID)
	buf = append(buf, sid[:]...)
	var flags byte
	if f.fin {
		flags |= flagFIN
	}
	buf = append(buf, flags)
	if f.fin {
		buf = append(buf, addrNone)
		return buf, nil
	}
	if ip := net.ParseIP(f.host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append(buf, addrIPv4)
			buf = append(buf, v4...)
		} else {
			buf = append(buf, addrIPv6)
			buf = append(buf, ip.To16()...)
		}
	} else {
		if len(f.host) == 0 || len(f.host) > 255 {
			return nil, errors.New("неподходящая длина имени")
		}
		buf = append(buf, addrDomain, byte(len(f.host)))
		buf = append(buf, f.host...)
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], f.port)
	buf = append(buf, port[:]...)
	buf = append(buf, f.data...)
	return buf, nil
}

func readFrame(r io.Reader) (frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameBody {
		return frame{}, errors.New("кадр больше предела")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return frame{}, err
	}
	return unmarshalBody(body)
}

func unmarshalBody(body []byte) (frame, error) {
	if len(body) < 5 {
		return frame{}, errors.New("кадр короче заголовка")
	}
	f := frame{sessionID: binary.BigEndian.Uint32(body[:4])}
	flags := body[4]
	f.fin = flags&flagFIN != 0
	rest := body[5:]
	if f.fin {
		return f, nil
	}
	if len(rest) < 1 {
		return frame{}, errors.New("нет типа адреса")
	}
	addrType := rest[0]
	rest = rest[1:]
	switch addrType {
	case addrIPv4:
		if len(rest) < 4 {
			return frame{}, errors.New("короткий IPv4")
		}
		f.host = net.IP(rest[:4]).String()
		rest = rest[4:]
	case addrIPv6:
		if len(rest) < 16 {
			return frame{}, errors.New("короткий IPv6")
		}
		f.host = net.IP(rest[:16]).String()
		rest = rest[16:]
	case addrDomain:
		if len(rest) < 1 {
			return frame{}, errors.New("нет длины имени")
		}
		n := int(rest[0])
		rest = rest[1:]
		if n == 0 || len(rest) < n {
			return frame{}, errors.New("битое имя")
		}
		f.host = string(rest[:n])
		rest = rest[n:]
	default:
		return frame{}, errors.New("неизвестный тип адреса")
	}
	if len(rest) < 2 {
		return frame{}, errors.New("нет порта")
	}
	f.port = binary.BigEndian.Uint16(rest[:2])
	f.data = append([]byte(nil), rest[2:]...)
	return f, nil
}

// ---------- сервер ----------

const (
	// maxSessionsPerConn — предел сессий на одно TCP-соединение.
	maxSessionsPerConn = 512
	// idleTimeout — сессия без единой датаграммы в любую сторону дольше
	// этого времени закрывается сама.
	idleTimeout = 60 * time.Second
	// maxConns — предел одновременных TCP-соединений до ретранслятора.
	// Клиент держит одно на весь туннель; несколько бывает на время
	// переподключения, пока старое ещё не закрылось.
	maxConns = 8
)

type serverSession struct {
	pc *net.UDPConn
}

type conn struct {
	nc      net.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	sessions map[uint32]*serverSession
}

func (c *conn) writeFrameLocked(f frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.nc, f)
}

func (c *conn) handle() {
	defer c.closeAll()
	for {
		f, err := readFrame(c.nc)
		if err != nil {
			return
		}
		if f.fin {
			c.closeSession(f.sessionID)
			continue
		}

		c.mu.Lock()
		s, ok := c.sessions[f.sessionID]
		if !ok {
			if len(c.sessions) >= maxSessionsPerConn {
				c.mu.Unlock()
				continue // предел сессий — молча дропаем кадр, не рвём соединение
			}
			pc, lErr := net.ListenUDP("udp", nil)
			if lErr != nil {
				c.mu.Unlock()
				continue
			}
			s = &serverSession{pc: pc}
			c.sessions[f.sessionID] = s
			c.mu.Unlock()
			go c.readSession(f.sessionID, s)
		} else {
			c.mu.Unlock()
		}

		addr, rErr := net.ResolveUDPAddr("udp", net.JoinHostPort(f.host, strconv.Itoa(int(f.port))))
		if rErr != nil {
			continue
		}
		// Продлевает срок бездействия и на отправку: сессия, по которой
		// только шлют и не получают ответа, не должна протухнуть раньше
		// времени — то же самое, что и обновление срока при чтении ниже.
		s.pc.SetReadDeadline(time.Now().Add(idleTimeout))
		s.pc.WriteTo(f.data, addr) //nolint:errcheck — одна неудачная датаграмма не повод рвать сессию
	}
}

// readSession читает ответы одной сессии и заворачивает их обратно в кадры.
// SetReadDeadline и есть весь механизм тайм-аута бездействия: отдельный
// сборщик мусора по времени не нужен.
func (c *conn) readSession(id uint32, s *serverSession) {
	buf := make([]byte, 65535)
	for {
		s.pc.SetReadDeadline(time.Now().Add(idleTimeout))
		n, from, err := s.pc.ReadFrom(buf)
		if err != nil {
			c.closeSession(id)
			return
		}
		host, portStr, splitErr := net.SplitHostPort(from.String())
		if splitErr != nil {
			continue
		}
		port, _ := strconv.Atoi(portStr)
		out := frame{sessionID: id, host: host, port: uint16(port), data: buf[:n]}
		if err := c.writeFrameLocked(out); err != nil {
			c.closeSession(id)
			return
		}
	}
}

func (c *conn) closeSession(id uint32) {
	c.mu.Lock()
	s, ok := c.sessions[id]
	delete(c.sessions, id)
	c.mu.Unlock()
	if ok {
		s.pc.Close()
	}
}

func (c *conn) closeAll() {
	c.mu.Lock()
	ss := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	for _, s := range ss {
		s.pc.Close()
	}
	c.nc.Close()
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	addr := flag.String("listen", "127.0.0.1:47830", "адрес, на котором слушать (только localhost)")
	flag.Parse()

	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !isLoopback(host) {
		log.Fatalf("отказываюсь слушать не на localhost: %s", *addr)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("udprelay слушает %s", ln.Addr())

	// int32, а не типизированный atomic.Int32: этот файл собирается прямо на
	// сервере командой "go build" в один файл, часто совсем старым Go из
	// пакетов дистрибутива (atomic.Int32 появился только в Go 1.19).
	var active int32
	for {
		nc, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		if atomic.LoadInt32(&active) >= maxConns {
			nc.Close()
			continue
		}
		atomic.AddInt32(&active, 1)
		c := &conn{nc: nc, sessions: make(map[uint32]*serverSession)}
		go func() {
			defer atomic.AddInt32(&active, -1)
			c.handle()
		}()
	}
}
