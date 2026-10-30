// Рукопожатие SSH на плохой сети.
//
// Вход по SSH — это около шести обменов «туда-обратно» с сервером: версии,
// обмен ключами, запрос службы, два шага проверки ключа. На мобильном
// интернете в одно деление каждый обмен идёт секунду-две, а потерянный пакет
// TCP перепосылает через 1, 2, 4, 8 секунд. Общий срок на всё рукопожатие
// (раньше 10–15 секунд) на такой сети истекал посреди проверки ключа: сервер
// честно отвечал, а клиент сам бросал трубку. В логе сервера это выглядело как
// «Connection closed by authenticating user … [preauth]», у человека — как
// «сервер не ответил вовремя», хотя другие VPN на той же сети работали.
//
// Поэтому срок здесь двойной: «тишина» — сколько можно ждать без единого байта
// от сервера (это и есть признак, что сервер пропал), и «потолок» — сколько
// может длиться всё рукопожатие, пока данные идут. Пока сервер отвечает, хоть
// и медленно, мы ждём.
package tunnel

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// defaultDialTimeout — сколько ждать установки TCP-соединения и сколько
// терпеть полную тишину от сервера во время рукопожатия. 20 секунд — с
// запасом на три-четыре подряд потерянных пакета (1+2+4+8 с перепосылок TCP).
const defaultDialTimeout = 20 * time.Second

// handshakeCeiling — потолок на всё рукопожатие, пока данные идут. Меньше
// LoginGraceTime сервера (60 с на нашем VPS, 120 с по умолчанию в OpenSSH):
// пусть сдаётся клиент с понятной ошибкой, а не сервер молча обрывает вход.
const handshakeCeiling = 50 * time.Second

// wireConn — TCP-соединение с сервером, которое помнит, когда от сервера
// последний раз пришли данные. Во время рукопожатия каждый пришедший байт
// отодвигает срок; после — отметка нужна проверке связи: соединение, по
// которому прямо сейчас идут данные, мёртвым не считается, даже если ответ на
// keepalive застрял в очереди за ними.
type wireConn struct {
	net.Conn
	lastRead atomic.Int64 // UnixNano

	// Пока идёт рукопожатие: тишина и потолок; нулевой потолок — не идёт.
	// Под замком: иначе Read, начавшийся до конца рукопожатия, мог бы
	// поставить срок уже после того, как его сняли, и через idle убить живое
	// соединение.
	mu      sync.Mutex
	idle    time.Duration
	ceiling time.Time
}

func newWireConn(c net.Conn) *wireConn {
	w := &wireConn{Conn: c}
	w.lastRead.Store(time.Now().UnixNano())
	return w
}

func (w *wireConn) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if n > 0 {
		now := time.Now()
		w.lastRead.Store(now.UnixNano())
		w.mu.Lock()
		if !w.ceiling.IsZero() {
			d := now.Add(w.idle)
			if d.After(w.ceiling) {
				d = w.ceiling
			}
			_ = w.Conn.SetDeadline(d)
		}
		w.mu.Unlock()
	}
	return n, err
}

// readWithin — приходили ли данные от сервера за последние d.
func (w *wireConn) readWithin(d time.Duration) bool {
	if w == nil {
		return false
	}
	return time.Since(time.Unix(0, w.lastRead.Load())) < d
}

// sshHandshakeWire проводит рукопожатие и вход по уже открытому conn со
// сроками «тишина/потолок» (см. начало файла). Отмена ctx обрывает
// рукопожатие сразу — иначе «Отключить» ждало бы до конца срока.
func sshHandshakeWire(ctx context.Context, conn net.Conn, addr string, cfg *ssh.ClientConfig, idle time.Duration) (*ssh.Client, *wireConn, error) {
	w := newWireConn(conn)
	ceiling := handshakeCeiling
	if ceiling < idle {
		ceiling = idle
	}
	start := time.Now()
	w.idle = idle
	w.ceiling = start.Add(ceiling)
	if err := conn.SetDeadline(start.Add(idle)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(w, addr, cfg)
	stop()
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	// Рукопожатие позади: срок снимаем, дальше по соединению идут данные без
	// ограничения по времени — за его жизнью следит проверка связи.
	w.mu.Lock()
	w.ceiling = time.Time{}
	err = conn.SetDeadline(time.Time{})
	w.mu.Unlock()
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return ssh.NewClient(c, chans, reqs), w, nil
}

// isTransientDialErr — сбой, который на плохой сети лечится повтором:
// соединение оборвалось или затихло посреди рукопожатия. Отказ по ключу,
// смена ключа сервера, закрытый порт и неразрешённое имя повтором не лечатся.
func isTransientDialErr(err error) bool {
	if err == nil {
		return false
	}
	// Уже разобранная ошибка dial(): её Error() — русский текст для человека,
	// по нему причину не узнать, смотрим на вид.
	var ce *ConnError
	if errors.As(err, &ce) {
		switch ce.Kind {
		case ConnErrorDropped:
			return true
		case ConnErrorNoResponse:
			return isTimeout(ce.Err)
		}
		return false
	}
	if IsAuthError(err) || isRefused(err) {
		return false
	}
	return isDropped(err) || isTimeout(err)
}

// isDropped — соединение оборвали посреди рукопожатия: сервер или кто-то по
// дороге закрыл его (EOF) или сбросил (RST). Именно так выглядит «ssh:
// handshake failed: EOF» на слабой сети.
func isDropped(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscallECONNRESET) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "forcibly closed") || // Windows: WSAECONNRESET
		strings.Contains(msg, "broken pipe") ||
		// Сброс посреди рукопожатия x/crypto/ssh иногда отдаёт так: сам
		// закрыл соединение после ошибки записи, и чтение видит уже закрытое.
		// Отмену рукопожатия нами же (Stop) отсекает ctx в sshHandshakeWire.
		strings.Contains(msg, "use of closed network connection")
}

func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "timed out")
}

// jitter возвращает d, случайно растянутое или сжатое до ±50 %. Без этого все
// слоты пула (и все устройства после общего обрыва) стучатся в сервер
// одновременно и мешают друг другу на узком канале.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int63n(int64(d)))
}
