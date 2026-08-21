//go:build linux

package core

// Проверка всей цепочки на настоящих пакетах: ядро Linux открывает соединение
// на адрес, которого не существует, пакеты попадают в tun, наш стек собирает из
// них соединение и отдаёт наверх — туда, где на Android будет SSH-туннель.

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// directCore — подставка вместо настоящего ядра: соединяется напрямую.
// Нужна, чтобы проверять сам стек отдельно от SSH.
type directCore struct{}

func (directCore) ServeConn(conn net.Conn, target string, byIP bool) {
	defer conn.Close()
	remote, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, remote); done <- struct{}{} }()
	<-done
}

// echoServer — то, что на телефоне будет настоящим сайтом.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						c.Write([]byte("ответ:" + string(buf[:n])))
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

var tunSeq int32

// startEngine поднимает отдельный интерфейс на своей подсети 198.18.N.0/24 и
// возвращает адрес, на который тесту надо стучаться. Общая подсеть на все тесты
// не годится: маршрут может достаться интерфейсу от прошлого теста.
func startEngine(t *testing.T, h *Handler) (peer string) {
	t.Helper()
	n := byte(atomic.AddInt32(&tunSeq, 1))
	name := fmt.Sprintf("tunlab%d", n)
	// 198.18.0.0/15 — диапазон, зарезервированный под сетевые тесты. Именно его
	// используют под fake-IP: настоящих сайтов там нет, спутать не с чем.
	tun, err := OpenTun(name, net.IPv4(198, 18, n, 1), net.CIDRMask(24, 32), 1500)
	if err != nil {
		t.Skipf("tun-устройство недоступно: %v", err)
	}
	eng, err := Start(tun.FD, 1500, h)
	if err != nil {
		t.Fatalf("стек не поднялся: %v", err)
	}
	t.Cleanup(eng.Close)
	_ = tun
	return fmt.Sprintf("198.18.%d.77", n)
}

// Главная проверка: пакеты превращаются обратно в соединение, и наверх приходит
// тот адрес, к которому обращалось приложение.
func TestПакетыСтановятсяСоединением(t *testing.T) {
	ln := echoServer(t)
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	st := &Stats{}
	h := &Handler{
		Core:  directCore{},
		Stats: st,
		// Заглушка fake-IP: любой выдуманный адрес ведёт на наш echo-сервер.
		Resolve: func(ip string) (string, bool) {
			if strings.HasPrefix(ip, "198.18.") {
				return "127.0.0.1", true
			}
			return "", false
		},
	}
	peer := startEngine(t, h)

	target := net.JoinHostPort(peer, port)
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		t.Fatalf("соединение через tun не установилось: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("привет")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if got := string(buf[:n]); got != "ответ:привет" {
		t.Fatalf("данные исказились: %q", got)
	}

	tcpOpen, _, _, targets := st.Snapshot()
	if tcpOpen != 1 {
		t.Fatalf("стек передал наверх %d соединений, ожидалось 1", tcpOpen)
	}
	want := net.JoinHostPort(peer, port)
	if len(targets) == 0 || (targets[0] != "127.0.0.1:"+port) {
		t.Fatalf("наверх пришёл адрес %v, а приложение шло на %s", targets, want)
	}
	t.Logf("соединение прошло: приложение → tun → стек → %s → echo", targets[0])
}

// UDP отбрасывается, и клиент должен узнать об этом сразу, а не по таймауту:
// от этого зависит, откатится ли браузер с QUIC на TCP.
func TestUDPОтбрасываетсяБыстро(t *testing.T) {
	st := &Stats{}
	peer := startEngine(t, &Handler{Core: directCore{}, Stats: st})

	conn, err := net.Dial("udp", net.JoinHostPort(peer, "443"))
	if err != nil {
		t.Fatalf("udp-сокет: %v", err)
	}
	defer conn.Close()

	start := time.Now()
	if _, err := conn.Write([]byte("это был бы QUIC")); err != nil {
		t.Fatalf("отправка: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	_, err = conn.Read(buf)
	elapsed := time.Since(start)

	_, udpDrop, _, _ := st.Snapshot()
	t.Logf("UDP: отброшено=%d, ответ через %v, ошибка=%v", udpDrop, elapsed, err)
	if udpDrop == 0 {
		t.Fatal("пакет UDP не дошёл до обработчика")
	}
	if err == nil {
		t.Fatal("на UDP пришёл ответ, хотя переносить его через SSH нечем")
	}
}
