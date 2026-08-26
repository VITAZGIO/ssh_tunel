package tunnel

import (
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"sshtunnel/internal/events"
	"sshtunnel/internal/procinfo"
	"sshtunnel/internal/routing"
)

const syscallECONNRESET = syscall.ECONNRESET

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// bufSize — размер буфера перекачки. io.Copy по умолчанию берёт 32 КБ; на
// канале с задержкой это лишние переключения и системные вызовы, 64 КБ
// заметно ровнее ложатся на SSH-пакеты и окно канала.
const bufSize = 64 * 1024

var bufPool = make(chan []byte, 64)

func getBuf() []byte {
	select {
	case b := <-bufPool:
		return b
	default:
		return make([]byte, bufSize)
	}
}

func putBuf(b []byte) {
	select {
	case bufPool <- b:
	default:
	}
}

// copyBuf — то же, что io.Copy, но буфер действительно тот, что дали.
//
// Через io.CopyBuffer это не работает, и незаметно: тот сначала спрашивает у
// сторон io.WriterTo/io.ReaderFrom и, если находит, переданный буфер просто
// выбрасывает. Здесь такие стороны есть всегда — и net.TCPConn, и bufio.Reader
// умеют оба интерфейса, — так что весь пул буферов лежал без дела, а перекачка
// шла чужими кусками по 32 КБ, каждый раз заново выделенными на соединение.
func copyBuf(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total, werr
			}
			if w != n {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}

// request описывает разобранный запрос от локального приложения, независимо
// от того, каким протоколом он пришёл.
type request struct {
	conn   net.Conn
	target string // host:port, куда хочет попасть приложение
	proto  string // socks4 / socks4a / socks5 / http-connect
	byIP   bool   // приложение прислало готовый IP => DNS резолвил сам

	// src — откуда читать данные клиента. Обычно это сам conn, но если
	// протокол разбирался через bufio, часть байтов клиента уже лежит в его
	// буфере, и читать надо именно оттуда, иначе они потеряются.
	src io.Reader

	writeErr func(error)
	writeOK  func(net.Conn) error
}

// serve — общая для всех протоколов часть: определить процесс, открыть
// соединение через SSH, ответить клиенту и перекачать данные в обе стороны.
func (t *Tunnel) serve(r request) {
	defer r.conn.Close()

	proc, pid := lookupProcess(r.conn)

	remote, direct, err := t.dialFor(proc, r.target)
	if err != nil {
		t.bus.Publish(eventConn(proc, pid, r.target, r.proto, r.byIP, direct, err))
		if r.writeErr != nil {
			r.writeErr(err)
		}
		return
	}
	defer remote.Close()

	if r.writeOK != nil {
		if err := r.writeOK(r.conn); err != nil {
			return
		}
	}

	t.bus.Publish(eventConn(proc, pid, r.target, r.proto, r.byIP, direct, nil))

	t.stats.active.Add(1)
	t.stats.total.Add(1)
	defer t.stats.active.Add(-1)

	src := r.src
	if src == nil {
		src = r.conn
	}
	t.pump(r.conn, src, remote)

	if t.cfg.Verbose {
		t.bus.Infof("%s → %s: закрыто", displayProc(proc), r.target)
	}
}

// pump перекачивает данные в обе стороны и ждёт завершения ОБОИХ направлений.
// Если выйти по первому, второе направление обрывается на полуслове — именно
// это раньше давало ERR_CONNECTION_RESET на середине страницы.
//
// local — соединение с приложением (нужно для закрытия на запись),
// localSrc — поток чтения от приложения (может быть буфером поверх local).
func (t *Tunnel) pump(local net.Conn, localSrc io.Reader, remote net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		buf := getBuf()
		n, _ := copyBuf(remote, localSrc, buf)
		putBuf(buf)
		t.stats.up.Add(n)
		closeWrite(remote)
		done <- struct{}{}
	}()
	go func() {
		buf := getBuf()
		n, _ := copyBuf(local, remote, buf)
		putBuf(buf)
		t.stats.down.Add(n)
		closeWrite(local)
		done <- struct{}{}
	}()

	<-done
	<-done
}

// closeWrite сообщает другой стороне "я больше не пишу", не разрывая приём.
// Многие протоколы на этом и держатся: клиент дописал запрос и ждёт ответ.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	// Если полузакрытие недоступно, мягко ограничим время дочитывания, чтобы
	// соединение не висело вечно.
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
}

// dialFor открывает соединение до цели тем путём, который положен этой
// программе по правилам: через сервер или напрямую с этого компьютера.
//
// Прямое соединение — не «отказ туннеля», а осознанный обход: так работает
// режим, в котором выбранные приложения намеренно ходят мимо, и так же
// обрабатывается локальная сеть.
func (t *Tunnel) dialFor(process, target string) (net.Conn, bool, error) {
	if t.useTunnel(process) && !t.localDirect(target) && !t.listedDirect(target) {
		c, err := t.Dial("tcp", target)
		return c, false, err
	}
	d := t.directDialer(15 * time.Second)
	c, err := d.Dial("tcp", target)
	return c, true, err
}

// localDirect — цель в локальной сети и её незачем вести через сервер.
// Проверяется раньше правил по программам: даже в режиме «всё через туннель»
// домашний NAS должен оставаться доступным.
func (t *Tunnel) localDirect(target string) bool {
	if t.localViaTunnel.Load() {
		return false
	}
	return routing.IsLocalTarget(target)
}

// listedDirect — цель попала в пользовательский список «всегда напрямую».
// Это про чужие сети, о которых программа знать не может: mesh-VPN, рабочий
// VPN, самодельный WireGuard.
func (t *Tunnel) listedDirect(target string) bool {
	t.mu.RLock()
	d := t.cfg.Direct
	t.mu.RUnlock()
	return d.Match(target)
}

func eventConn(proc string, pid int, target, proto string, byIP, direct bool, err error) events.Event {
	e := events.Event{
		Kind: events.KindConn, Process: proc, PID: pid,
		Target: target, Proto: proto, DNSLeak: byIP, Direct: direct,
	}
	if err != nil {
		e.Failed = true
		e.Error = shortErr(err)
	}
	return e
}

func lookupProcess(c net.Conn) (string, int) {
	addr, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return "", 0
	}
	return procinfo.Lookup(uint16(addr.Port))
}

func displayProc(p string) string {
	if p == "" {
		return "приложение"
	}
	return p
}

// shortErr убирает из ошибки техническую обвязку — в интерфейсе нужен смысл,
// а не внутренности библиотеки.
func shortErr(err error) string {
	s := err.Error()
	switch {
	case containsAny(s, "connection refused"):
		return "сервер отказал в соединении"
	case containsAny(s, "no such host", "Name or service not known"):
		return "имя не разрешилось"
	case containsAny(s, "i/o timeout", "timeout"):
		return "таймаут"
	case containsAny(s, "administratively prohibited"):
		return "сервер запретил исходящее соединение"
	}
	if i := strings.LastIndex(s, ": "); i > 0 && len(s)-i < 60 {
		return s[i+2:]
	}
	return s
}
