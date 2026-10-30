package tunnel

// Прогон на по-настоящему плохой сети: задержка и потери пакетов задаются
// снаружи через tc netem, тест только подключается и гоняет запросы и пишет,
// сколько удалось. Обычным `go test` не запускается — нужна переменная
// SSHTUNNEL_NETEM=1 и сетевое пространство с испорченной петлёй, например:
//
//	ip netns add weak
//	ip netns exec weak ip link set lo up
//	ip netns exec weak tc qdisc add dev lo root netem delay 300ms 100ms loss 15%
//	SSHTUNNEL_NETEM=1 ip netns exec weak go test -run TestNetem -v ./internal/tunnel/
//
// Пользуется только тем, что есть у туннеля снаружи (New, Start, Dial, Stop),
// поэтому один и тот же файл можно прогнать и на старой версии — для
// сравнения «до/после».

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/events"
)

func TestNetemWeakNetwork(t *testing.T) {
	if os.Getenv("SSHTUNNEL_NETEM") == "" {
		t.Skip("только под tc netem: SSHTUNNEL_NETEM=1")
	}
	rounds, _ := strconv.Atoi(os.Getenv("SSHTUNNEL_NETEM_ROUNDS"))
	if rounds <= 0 {
		rounds = 6
	}

	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)
	web := echoServer(t)
	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)

	var startOK, reqOK, reqFail, reconnects int
	var startTimes []string
	for r := 0; r < rounds; r++ {
		bus := events.NewBus()
		tun := New(Config{
			Host:           host,
			SSHPort:        sshPort,
			User:           "test",
			KeyPath:        keyPath,
			PoolSize:       4,
			KnownHostsPath: filepath.Join(dir, "known_hosts"),
			LocalViaTunnel: true,
		}, bus)
		start := time.Now()
		err := tun.Start()
		if err != nil {
			t.Logf("раунд %d: не подключился за %v: %v", r+1, time.Since(start).Round(time.Millisecond), err)
			continue
		}
		startOK++
		startTimes = append(startTimes, time.Since(start).Round(100*time.Millisecond).String())

		// Минута работы: запрос каждые 2 секунды.
		end := time.Now().Add(time.Minute)
		for time.Now().Before(end) {
			if fetchHello(tun, web) == nil {
				reqOK++
			} else {
				reqFail++
			}
			time.Sleep(2 * time.Second)
		}
		for _, e := range bus.History() {
			if strings.Contains(e.Text, "переподключаюсь") {
				reconnects++
			}
		}
		tun.Stop()
	}
	t.Logf("ИТОГ: подключений %d из %d (время: %s); запросов успешно %d, с ошибкой %d; переподключений слотов %d",
		startOK, rounds, strings.Join(startTimes, " "), reqOK, reqFail, reconnects)
}

func fetchHello(tun *Tunnel, web *net.TCPAddr) error {
	c, err := tun.Dial("tcp", web.String())
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(c, "GET /hello HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("код %d", resp.StatusCode)
	}
	return nil
}
