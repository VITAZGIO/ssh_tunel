// Package udprelay — протокол "UDP через SSH".
//
// Замысел: SSH умеет пробрасывать только TCP (direct-tcpip). Чтобы провести
// через него UDP (звонки, игры, QUIC), на сервере поднимается маленький
// ретранслятор (см. src/cmd/udprelay) — слушает только 127.0.0.1, никакого
// нового порта наружу не открывается. Клиент открывает до него ОДНО
// TCP-соединение через уже поднятый SSH-туннель (обычный direct-tcpip до
// 127.0.0.1:порт на сервере) и мультиплексирует через него сколько угодно
// UDP-сессий этим простым кадровым протоколом.
//
// Кадр (все целые — big-endian):
//
//	uint32 длина        — всего, что идёт дальше
//	uint32 sessionID     — присваивает клиент, ретранслятор просто эхом
//	                       возвращает его в ответных кадрах той же сессии
//	uint8  flags         — бит 0: FIN, сессию можно забыть немедленно
//	если не FIN:
//	  uint8  addrType     — 1 = IPv4, 4 = IPv6, 3 = имя
//	  ...адрес...         — 4, 16 или (1+N) байт соответственно
//	  uint16 port
//	  ...полезная нагрузка — сама датаграмма, до конца кадра
//
// Разрешение имён (addrType=3) сознательно возможно: так DNS для этих
// адресов происходит на сервере, а не утекает с телефона или компьютера
// напрямую — как и для остального трафика через этот тунель.
//
// Границы протокола (сознательно НЕ делаем):
//   - никаких входящих соединений извне: сессию всегда открывает клиент,
//     ретранслятор никогда не проявляет инициативы;
//   - никакой отдельной аутентификации кадров — единственная граница
//     доступа - то, что до ретранслятора вообще можно достучаться, только
//     пройдя SSH-аутентификацию (порт слушает исключительно 127.0.0.1);
//   - не мультиплексируем один сеанс поверх нескольких SSH-каналов — одно
//     TCP-соединение до ретранслятора на весь туннель, этого достаточно;
//   - никакого контроля перегрузки сверх базовых пределов (см.
//     src/cmd/udprelay) — это не система QoS.
package udprelay

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
)

// MaxFrameBody — предел на размер кадра без заголовка длины. С запасом над
// обычным пределом UDP (65507 байт для IPv4), но не бесконечный: иначе одна
// испорченная или злонамеренная сторона могла бы вынудить выделить
// неограниченную память под тело кадра.
const MaxFrameBody = 65*1024 + 32

const (
	flagFIN = 1 << 0

	addrNone   = 0
	addrIPv4   = 1
	addrDomain = 3
	addrIPv6   = 4
)

// Frame — один кадр протокола.
type Frame struct {
	SessionID uint32
	// FIN — сессию можно забыть немедленно, дальше по ней ничего не будет.
	// Host/Port/Data при FIN не несут смысла и игнорируются.
	FIN  bool
	Host string
	Port uint16
	Data []byte
}

// WriteFrame пишет кадр в w целиком или не пишет вовсе.
func WriteFrame(w io.Writer, f Frame) error {
	body, err := marshalBody(f)
	if err != nil {
		return err
	}
	if len(body) > MaxFrameBody {
		return errors.New("udprelay: кадр больше предела")
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

func marshalBody(f Frame) ([]byte, error) {
	buf := make([]byte, 0, 24+len(f.Data))
	var sid [4]byte
	binary.BigEndian.PutUint32(sid[:], f.SessionID)
	buf = append(buf, sid[:]...)

	var flags byte
	if f.FIN {
		flags |= flagFIN
	}
	buf = append(buf, flags)
	if f.FIN {
		buf = append(buf, addrNone)
		return buf, nil
	}

	if ip := net.ParseIP(f.Host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append(buf, addrIPv4)
			buf = append(buf, v4...)
		} else {
			buf = append(buf, addrIPv6)
			buf = append(buf, ip.To16()...)
		}
	} else {
		if len(f.Host) == 0 || len(f.Host) > 255 {
			return nil, errors.New("udprelay: неподходящая длина имени")
		}
		buf = append(buf, addrDomain, byte(len(f.Host)))
		buf = append(buf, f.Host...)
	}

	var port [2]byte
	binary.BigEndian.PutUint16(port[:], f.Port)
	buf = append(buf, port[:]...)
	buf = append(buf, f.Data...)
	return buf, nil
}

// ReadFrame читает один кадр целиком из r, блокируясь до полного прихода.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameBody {
		return Frame{}, errors.New("udprelay: кадр больше предела")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Frame{}, err
	}
	return unmarshalBody(body)
}

func unmarshalBody(body []byte) (Frame, error) {
	if len(body) < 5 {
		return Frame{}, errors.New("udprelay: кадр короче заголовка")
	}
	f := Frame{SessionID: binary.BigEndian.Uint32(body[:4])}
	flags := body[4]
	f.FIN = flags&flagFIN != 0
	rest := body[5:]
	if f.FIN {
		return f, nil
	}
	if len(rest) < 1 {
		return Frame{}, errors.New("udprelay: нет типа адреса")
	}
	addrType := rest[0]
	rest = rest[1:]
	switch addrType {
	case addrIPv4:
		if len(rest) < 4 {
			return Frame{}, errors.New("udprelay: короткий IPv4")
		}
		f.Host = net.IP(rest[:4]).String()
		rest = rest[4:]
	case addrIPv6:
		if len(rest) < 16 {
			return Frame{}, errors.New("udprelay: короткий IPv6")
		}
		f.Host = net.IP(rest[:16]).String()
		rest = rest[16:]
	case addrDomain:
		if len(rest) < 1 {
			return Frame{}, errors.New("udprelay: нет длины имени")
		}
		n := int(rest[0])
		rest = rest[1:]
		if n == 0 || len(rest) < n {
			return Frame{}, errors.New("udprelay: битое имя")
		}
		f.Host = string(rest[:n])
		rest = rest[n:]
	default:
		return Frame{}, errors.New("udprelay: неизвестный тип адреса")
	}
	if len(rest) < 2 {
		return Frame{}, errors.New("udprelay: нет порта")
	}
	f.Port = binary.BigEndian.Uint16(rest[:2])
	f.Data = append([]byte(nil), rest[2:]...)
	return f, nil
}
