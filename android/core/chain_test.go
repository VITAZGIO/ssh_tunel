//go:build linux

package core

// Цепочка целиком, как она будет работать на телефоне:
//
//	приложение → tun (то, что даёт VpnService) → сетевой стек → ядро туннеля
//	           → SSH direct-tcpip → сервер → сайт
//
// Здесь всё настоящее: пакеты формирует ядро Linux, SSH-сервер поднят
// по-настоящему, соединение до цели открывает он же. Подделан только адрес
// назначения — ровно так и будет работать fake-IP.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sshtunnel/internal/routing"
	"sshtunnel/internal/tunnel"
)

var chainSeq int32

// startChain поднимает туннель, цель и стек на своей подсети.
// Возвращает адрес, на который тесту надо стучаться вместо настоящего.
func startChain(t *testing.T, pool int, resolve Resolver) (*tunnel.Tunnel, *testSSHServer, *Stats, string, int) {
	t.Helper()
	tun, srv := startTunnel(t, pool)
	target := httpTarget(t)

	n := byte(atomic.AddInt32(&chainSeq, 1) + 100)
	dev, err := OpenTun(fmt.Sprintf("tunand%d", n),
		net.IPv4(198, 18, n, 1), net.CIDRMask(24, 32), 1500)
	if err != nil {
		t.Skipf("tun-устройство недоступно: %v", err)
	}
	st := &Stats{}
	eng, err := Start(dev.FD, 1500, &Handler{Core: tun, Stats: st, Resolve: resolve})
	if err != nil {
		t.Fatalf("стек не поднялся: %v", err)
	}
	t.Cleanup(eng.Close)

	return tun, srv, st, fmt.Sprintf("198.18.%d.77", n), target.Port
}

// Заглушка fake-IP: выдуманный адрес → настоящее имя цели.
func toLocalhost(ip string) (string, bool) {
	if strings.HasPrefix(ip, "198.18.") {
		return "127.0.0.1", true
	}
	return "", false
}

func TestЦепочкаЦеликом(t *testing.T) {
	tun, srv, st, peer, port := startChain(t, 2, toLocalhost)

	// Обычный HTTP-запрос на несуществующий адрес. Про туннель клиент ничего
	// не знает — ровно как приложение на телефоне.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/hello", peer, port))
	if err != nil {
		t.Fatalf("запрос не прошёл: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "привет от") {
		t.Fatalf("пришло не то: %q", body)
	}

	tcpOpen, _, targets := st.Snapshot()
	if tcpOpen == 0 {
		t.Fatal("стек не передал наверх ни одного соединения")
	}

	// Главное: трафик действительно прошёл через SSH, а не мимо него.
	if got := srv.channels.Load(); got == 0 {
		t.Fatal("сервер не открывал соединений — трафик пошёл мимо туннеля")
	}
	if got := tun.Stats().Total; got == 0 {
		t.Fatal("туннель не зафиксировал соединение")
	}
	t.Logf("прошло через стек: %v, каналов через сервер: %d", targets, srv.channels.Load())
}

// Объём: проверка, что стек не разваливается на большом ответе и MTU выбран
// верно. Потеря даже одного байта здесь означала бы битые страницы на телефоне.
func TestБольшойОтвет(t *testing.T) {
	_, _, _, peer, port := startChain(t, 4, toLocalhost)

	client := &http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/big", peer, port))
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("чтение тела: %v", err)
	}
	elapsed := time.Since(start)

	const want = 512 * 1024
	if got != want {
		t.Fatalf("получено %d Б вместо %d — данные потерялись по дороге", got, want)
	}
	t.Logf("512 КБ через tun + стек + SSH за %v (%.1f МБ/с)",
		elapsed.Round(time.Millisecond), float64(got)/elapsed.Seconds()/1024/1024)
}

// Список «всегда напрямую» должен работать и на телефоне: перечисленное в нём
// идёт мимо сервера, даже когда туннель включён на всё.
func TestСписокНапрямую(t *testing.T) {
	// Имя не восстанавливаем: правило должно сработать по адресу.
	tun, srv, _, peer, port := startChain(t, 1, func(string) (string, bool) { return "", false })
	tun.SetDirect(routing.NewDirectList([]string{"198.18.0.0/15"}))

	before := srv.channels.Load()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", peer, port), 5*time.Second)
	if err == nil {
		conn.Close()
	}

	if got := srv.channels.Load() - before; got != 0 {
		t.Fatalf("через сервер ушло %d соединений, а адрес в списке «всегда напрямую»", got)
	}
}
