package tunnel

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunel/internal/events"
	"sshtunel/internal/routing"
)

// Тесты поднимают настоящий SSH-сервер прямо в процессе и прогоняют через него
// все четыре протокола, которые понимает локальный прокси. Так проверяется, что
// туннель реально передаёт данные в обе стороны, а не только компилируется.

// ---------- тестовый SSH-сервер ----------

type testSSHServer struct {
	addr     string
	hostKey  ssh.Signer
	ln       net.Listener
	closeCh  chan struct{}
	rejectTo string // если цель совпала — отказать (проверка обработки ошибок)

	// channels считает открытые через сервер соединения — по нему видно,
	// действительно ли трафик пошёл через туннель.
	channels atomic.Int64
}

func newTestSSHServer(t *testing.T, clientPub ssh.PublicKey) *testSSHServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s := &testSSHServer{
		addr:    ln.Addr().String(),
		hostKey: signer,
		ln:      ln,
		closeCh: make(chan struct{}),
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), clientPub.Marshal()) {
				return nil, fmt.Errorf("чужой ключ")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(t, c, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testSSHServer) handle(t *testing.T, c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	// Отвечаем на keepalive, иначе клиент решит, что соединение мертво.
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "поддерживается только direct-tcpip")
			continue
		}
		var payload struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &payload); err != nil {
			nc.Reject(ssh.ConnectionFailed, "битый запрос")
			continue
		}
		s.channels.Add(1)
		target := net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port)))
		if s.rejectTo != "" && target == s.rejectTo {
			nc.Reject(ssh.ConnectionFailed, "administratively prohibited")
			continue
		}
		remote, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			remote.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go func() {
			defer ch.Close()
			defer remote.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(remote, ch); done <- struct{}{} }()
			go func() { io.Copy(ch, remote); done <- struct{}{} }()
			<-done
		}()
	}
}

// ---------- вспомогательное ----------

func writeTestKey(t *testing.T, dir string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pemEncode(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return path, sshPub
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// echoServer отвечает на HTTP-запрос известным телом и умеет отдавать много
// данных — чтобы проверить перекачку, а не только установление соединения.
func echoServer(t *testing.T) *net.TCPAddr {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "привет от %s", r.Host)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("A"), 1024)
		for i := 0; i < 512; i++ { // 512 КБ — больше любого буфера в коде
			w.Write(chunk)
		}
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr)
}

func startTunnel(t *testing.T, poolSize int) (*Tunnel, string, string, *testSSHServer) {
	t.Helper()
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)

	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)

	socksAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	httpAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	tun := New(Config{
		Host:           host,
		SSHPort:        sshPort,
		User:           "test",
		KeyPath:        keyPath,
		SocksAddr:      socksAddr,
		HTTPAddr:       httpAddr,
		PoolSize:       poolSize,
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
		// Цели в тестах поднимаются на 127.0.0.1, а локальные адреса в
		// обычном режиме идут мимо туннеля. Здесь проверяется сам туннель,
		// поэтому локальную сеть намеренно заворачиваем в него.
		LocalViaTunnel: true,
	}, events.NewBus())

	if err := tun.Start(); err != nil {
		t.Fatalf("туннель не запустился: %v", err)
	}
	t.Cleanup(tun.Stop)
	return tun, socksAddr, httpAddr, srv
}

// ---------- SOCKS5 ----------

func socks5Connect(t *testing.T, proxy, host string, port int, useDomain bool) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	// Приветствие: только метод "без авторизации".
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("прокси не принял анонимный доступ: %v", resp)
	}

	req := []byte{0x05, 0x01, 0x00}
	if useDomain {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	} else {
		ip := net.ParseIP(host).To4()
		if ip == nil {
			t.Fatalf("ожидался IPv4, получено %q", host)
		}
		req = append(req, 0x01)
		req = append(req, ip...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("SOCKS5 отказал, код %d", reply[1])
	}
	return c
}

func TestSOCKS5ByIP(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	c := socks5Connect(t, socksAddr, target.IP.String(), target.Port, false)
	defer c.Close()
	assertHTTPBody(t, c, target.String(), "/hello", "привет от ")
}

func TestSOCKS5ByDomain(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	// "localhost" резолвит уже сервер на той стороне туннеля — это и есть
	// режим без утечки DNS.
	c := socks5Connect(t, socksAddr, "localhost", target.Port, true)
	defer c.Close()
	assertHTTPBody(t, c, "localhost", "/hello", "привет от localhost")
}

func TestSOCKS5LargeTransfer(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 2)
	target := echoServer(t)

	c := socks5Connect(t, socksAddr, target.IP.String(), target.Port, false)
	defer c.Close()

	fmt.Fprintf(c, "GET /big HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.String())
	body := readHTTPBody(t, c)
	if len(body) != 512*1024 {
		t.Fatalf("получено %d байт вместо %d — перекачка теряет данные", len(body), 512*1024)
	}
}

// TestSOCKS5UploadThenDownload проверяет, что оба направления доживают до
// конца: клиент дописывает тело, закрывает свою сторону на запись и ждёт
// ответ. Именно этот сценарий раньше ломался на полуслове.
func TestSOCKS5UploadThenDownload(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	c := socks5Connect(t, socksAddr, target.IP.String(), target.Port, false)
	defer c.Close()

	payload := strings.Repeat("данные", 5000)
	fmt.Fprintf(c, "POST /echo HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		target.String(), len(payload), payload)

	body := readHTTPBody(t, c)
	if string(body) != payload {
		t.Fatalf("ответ не совпал: отправлено %d байт, получено %d", len(payload), len(body))
	}
}

// ---------- SOCKS4 / SOCKS4a ----------

func socks4Connect(t *testing.T, proxy, host string, port int, use4a bool) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	req := []byte{0x04, 0x01}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if use4a {
		req = append(req, 0, 0, 0, 1) // 0.0.0.x — признак SOCKS4a
	} else {
		ip := net.ParseIP(host).To4()
		if ip == nil {
			t.Fatalf("ожидался IPv4, получено %q", host)
		}
		req = append(req, ip...)
	}
	req = append(req, "vitaz"...) // USERID
	req = append(req, 0)
	if use4a {
		req = append(req, host...)
		req = append(req, 0)
	}
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 8)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x5A {
		t.Fatalf("SOCKS4 отказал, код 0x%X", reply[1])
	}
	return c
}

func TestSOCKS4(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	c := socks4Connect(t, socksAddr, target.IP.String(), target.Port, false)
	defer c.Close()
	assertHTTPBody(t, c, target.String(), "/hello", "привет от ")
}

func TestSOCKS4a(t *testing.T) {
	_, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	c := socks4Connect(t, socksAddr, "localhost", target.Port, true)
	defer c.Close()
	assertHTTPBody(t, c, "localhost", "/hello", "привет от localhost")
}

// ---------- HTTP-прокси ----------

// TestHTTPConnect — это ровно тот путь, которым ходит Claude Code и всё
// остальное на Node.js: переменная HTTPS_PROXY, метод CONNECT.
func TestHTTPConnect(t *testing.T) {
	_, _, httpAddr, _ := startTunnel(t, 1)
	target := echoServer(t)

	c, err := net.Dial("tcp", httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	fmt.Fprintf(c, "CONNECT localhost:%d HTTP/1.1\r\nHost: localhost:%d\r\n\r\n", target.Port, target.Port)
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("CONNECT не удался: %q", strings.TrimSpace(line))
	}
	for { // дочитать пустую строку после заголовков
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(l) == "" {
			break
		}
	}

	fmt.Fprintf(c, "GET /hello HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	body := readHTTPBodyFrom(t, br)
	if !strings.Contains(string(body), "привет от localhost") {
		t.Fatalf("неожиданный ответ: %q", string(body))
	}
}

// TestHTTPConnectEarlyData проверяет, что данные, присланные клиентом сразу
// за заголовком CONNECT (так делает TLS), не теряются в буфере.
func TestHTTPConnectEarlyData(t *testing.T) {
	_, _, httpAddr, _ := startTunnel(t, 1)
	target := echoServer(t)

	c, err := net.Dial("tcp", httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Запрос отправляется ОДНИМ куском вместе с заголовком CONNECT.
	fmt.Fprintf(c, "CONNECT localhost:%d HTTP/1.1\r\nHost: localhost:%d\r\n\r\n"+
		"GET /hello HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n",
		target.Port, target.Port)

	all, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(all), "привет от localhost") {
		t.Fatalf("данные, отправленные сразу за CONNECT, потерялись: %q", string(all))
	}
}

func TestHTTPForward(t *testing.T) {
	_, _, httpAddr, _ := startTunnel(t, 1)
	target := echoServer(t)

	c, err := net.Dial("tcp", httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Прокси-форма запроса: полный URL в строке запроса.
	fmt.Fprintf(c, "GET http://localhost:%d/hello HTTP/1.1\r\nHost: localhost:%d\r\nProxy-Connection: keep-alive\r\nConnection: close\r\n\r\n",
		target.Port, target.Port)
	body := readHTTPBody(t, c)
	if !strings.Contains(string(body), "привет от") {
		t.Fatalf("неожиданный ответ: %q", string(body))
	}
}

// ---------- поведение при сбоях ----------

func TestFailedTargetReportsError(t *testing.T) {
	tun, socksAddr, _, _ := startTunnel(t, 1)
	_ = tun

	c, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.Write([]byte{0x05, 0x01, 0x00})
	io.ReadFull(c, make([]byte, 2))

	// Порт 1 на localhost заведомо закрыт.
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1}
	req = binary.BigEndian.AppendUint16(req, 1)
	c.Write(req)

	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatalf("прокси не ответил на неудачное соединение: %v", err)
	}
	if reply[1] == 0x00 {
		t.Fatal("прокси отрапортовал успех для заведомо закрытого порта")
	}
}

// TestPoolSurvivesDeadLink проверяет главное свойство пула: если одно
// SSH-соединение умерло, трафик продолжает ходить через остальные.
func TestPoolSurvivesDeadLink(t *testing.T) {
	tun, socksAddr, _, _ := startTunnel(t, 3)
	target := echoServer(t)

	if !tun.WaitReady(3, 5*time.Second) {
		t.Fatal("пул не поднялся целиком")
	}
	// Убиваем одно соединение из пула, как это делает разрыв связи.
	tun.links[0].set(nil)

	for i := 0; i < 6; i++ {
		c := socks5Connect(t, socksAddr, target.IP.String(), target.Port, false)
		assertHTTPBody(t, c, target.String(), "/hello", "привет от ")
		c.Close()
	}
}

func TestStatsCounted(t *testing.T) {
	tun, socksAddr, _, _ := startTunnel(t, 1)
	target := echoServer(t)

	c := socks5Connect(t, socksAddr, target.IP.String(), target.Port, false)
	fmt.Fprintf(c, "GET /big HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.String())
	readHTTPBody(t, c)
	c.Close()

	// Дать перекачке дописать счётчики.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tun.Stats().BytesDown > 500*1024 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := tun.Stats()
	if s.BytesDown < 500*1024 {
		t.Fatalf("счётчик принятых байт не растёт: %d", s.BytesDown)
	}
	if s.Total == 0 {
		t.Fatal("счётчик соединений не растёт")
	}
}

// ---------- helpers ----------

func assertHTTPBody(t *testing.T, c net.Conn, host, path, want string) {
	t.Helper()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	body := readHTTPBody(t, c)
	if !strings.Contains(string(body), want) {
		t.Fatalf("ожидал в ответе %q, получил %q", want, string(body))
	}
}

func readHTTPBody(t *testing.T, r io.Reader) []byte {
	t.Helper()
	return readHTTPBodyFrom(t, bufio.NewReader(r))
}

func readHTTPBodyFrom(t *testing.T, br *bufio.Reader) []byte {
	t.Helper()
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("не смог прочитать HTTP-ответ через туннель: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не смог дочитать тело ответа: %v", err)
	}
	return body
}

// ---------- разделение трафика по программам ----------

// Главная проверка фильтра: соединение исключённой программы обязано пойти
// НАПРЯМУЮ, минуя сервер. Считаем каналы на тестовом SSH-сервере — если они
// не выросли, значит трафик действительно прошёл мимо туннеля.
func TestPolicySendsExcludedAppDirect(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	target := echoServer(t)

	tun.SetPolicy(routing.New(routing.ModeExcept, []string{"steam.exe"}))

	before := srv.channels.Load()

	conn, direct, err := tun.dialFor("steam.exe", target.String())
	if err != nil {
		t.Fatalf("прямое соединение не открылось: %v", err)
	}
	defer conn.Close()
	if !direct {
		t.Fatal("исключённая программа помечена как идущая через туннель")
	}
	if got := srv.channels.Load(); got != before {
		t.Fatalf("сервер открыл %d новых каналов — трафик всё-таки пошёл через туннель", got-before)
	}
	assertHTTPBody(t, conn, target.String(), "/hello", "привет от ")
}

// Обратный случай: не исключённая программа должна идти через сервер.
func TestPolicyKeepsOtherAppsInTunnel(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	target := echoServer(t)

	tun.SetPolicy(routing.New(routing.ModeExcept, []string{"steam.exe"}))

	before := srv.channels.Load()

	conn, direct, err := tun.dialFor("chrome.exe", target.String())
	if err != nil {
		t.Fatalf("соединение через туннель не открылось: %v", err)
	}
	defer conn.Close()
	if direct {
		t.Fatal("обычная программа выпущена мимо туннеля")
	}
	if got := srv.channels.Load(); got != before+1 {
		t.Fatalf("сервер открыл %d каналов вместо одного — трафик пошёл не туда", got-before)
	}
	assertHTTPBody(t, conn, target.String(), "/hello", "привет от ")
}

// Правила меняются на ходу: одна и та же программа до и после смены режима
// должна идти разными путями без переподключения туннеля.
func TestPolicyChangesWithoutRestart(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	target := echoServer(t)

	before := srv.channels.Load()
	c1, direct1, err := tun.dialFor("steam.exe", target.String())
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	if direct1 || srv.channels.Load() != before+1 {
		t.Fatal("до включения правил всё должно идти через туннель")
	}

	tun.SetPolicy(routing.New(routing.ModeOnly, []string{"chrome.exe"}))

	before = srv.channels.Load()
	c2, direct2, err := tun.dialFor("steam.exe", target.String())
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
	if !direct2 {
		t.Fatal("после включения правил программа не ушла напрямую")
	}
	if srv.channels.Load() != before {
		t.Fatal("после включения правил трафик всё ещё идёт через туннель")
	}
}

// ---------- Локальная сеть ----------

// Локальная цель не должна уходить в туннель даже в режиме «всё через
// сервер»: сервер искал бы такой адрес в своей сети, и домашние сервисы
// (роутер, NAS, Home Assistant) перестали бы открываться.
func TestLocalTargetGoesDirect(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	target := echoServer(t) // 127.0.0.1:порт — заведомо локальный адрес
	tun.SetLocalViaTunnel(false)

	before := srv.channels.Load()

	conn, direct, err := tun.dialFor("chrome.exe", target.String())
	if err != nil {
		t.Fatalf("прямое соединение до локального адреса не открылось: %v", err)
	}
	defer conn.Close()
	if !direct {
		t.Fatal("локальный адрес помечен как идущий через туннель")
	}
	if got := srv.channels.Load(); got != before {
		t.Fatalf("сервер открыл %d каналов — локальный адрес всё-таки ушёл в туннель", got-before)
	}
	assertHTTPBody(t, conn, target.String(), "/hello", "привет от ")
}

// Обратная сторона того же правила: не-локальные адреса при выключенном
// LocalViaTunnel обязаны по-прежнему идти через сервер. Соединение до
// несуществующего имени не установится, но канал на сервере откроется — по
// счётчику видно, что маршрут выбран верно.
func TestRemoteTargetStillGoesThroughTunnel(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	tun.SetLocalViaTunnel(false)

	before := srv.channels.Load()

	conn, direct, _ := tun.dialFor("chrome.exe", "example.invalid:80")
	if conn != nil {
		conn.Close()
	}
	if direct {
		t.Fatal("внешний адрес выпущен мимо туннеля")
	}
	if got := srv.channels.Load(); got != before+1 {
		t.Fatalf("сервер открыл %d каналов вместо одного — внешний адрес пошёл не туда", got-before)
	}
}

// Пользовательский список «всегда напрямую» перекрывает режим «всё через
// туннель»: сюда попадают чужие сети (mesh-VPN, рабочий VPN), про которые
// программа знать не может.
func TestDirectListSkipsTunnel(t *testing.T) {
	tun, _, _, srv := startTunnel(t, 1)
	tun.SetDirect(routing.NewDirectList([]string{"example.invalid", "203.0.113.0/24"}))

	for _, target := range []string{"example.invalid:80", "203.0.113.9:443"} {
		before := srv.channels.Load()
		conn, direct, _ := tun.dialFor("chrome.exe", target)
		if conn != nil {
			conn.Close()
		}
		if !direct {
			t.Errorf("%s: цель из списка помечена как идущая через туннель", target)
		}
		if got := srv.channels.Load(); got != before {
			t.Errorf("%s: сервер открыл %d каналов — цель из списка ушла в туннель",
				target, got-before)
		}
	}
}
