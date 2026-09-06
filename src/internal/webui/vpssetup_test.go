package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/events"
)

func mustEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// ---------- поддельный SSH-сервер администратора ----------
//
// Настоящих apt-get/systemctl/ufw в тестах, конечно, нет — сервер просто
// узнаёт по содержимому команды, какой из шагов (положить ключ, harden.sh,
// tunnel-user.sh, cockpit) до него дошёл, и отвечает заранее заданным
// построчным выводом и кодом выхода. Ключевая проверка — не то, что делает
// настоящий bash, а то, что runVpsSetup обращается к серверу в правильном
// порядке, с правильно подставленными параметрами, и не идёт дальше, если
// предыдущий шаг не удался.

type execCall struct {
	cmd   string
	stdin string
}

type fakeAdminServer struct {
	addr     string
	password string

	mu           sync.Mutex
	keyLine      string
	keyInstalled bool
	acceptKey    bool // разрешить ли вход по ключу вообще (проверка шага 6 ТЗ)
	calls        []execCall
	hardenExit   uint32
	tunnelExit   uint32
}

func newFakeAdminServer(t *testing.T, password string) *fakeAdminServer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(mustEd25519(t))
	if err != nil {
		t.Fatal(err)
	}

	s := &fakeAdminServer{password: password, acceptKey: true}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) != s.password {
				return nil, fmt.Errorf("неверный пароль")
			}
			return &ssh.Permissions{}, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.acceptKey || !s.keyInstalled {
				return nil, fmt.Errorf("ключ не подходит")
			}
			line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if line != s.keyLine {
				return nil, fmt.Errorf("чужой ключ")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(c, cfg)
		}
	}()
	return s
}

func (s *fakeAdminServer) handleConn(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
}

func (s *fakeAdminServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		if req.WantReply {
			req.Reply(true, nil)
		}
		cmd := decodeExecCommand(req.Payload)
		s.runExec(ch, cmd)
		return
	}
}

func decodeExecCommand(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}

func sendExit(ch ssh.Channel, code uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], code)
	ch.SendRequest("exit-status", false, b[:])
	ch.Close()
}

var keyLineRe = regexp.MustCompile(`'([^']*)'`)

// drainStdin сливает стандартный вход команды в фоне и закрывает возвращённый
// канал, когда клиент дописал и закрыл запись (или канал оборвался). Кому
// это важно (chpasswd) — дожидается закрытия канала перед ответом; кому нет
// — просто не ждёт, поведение то же самое, что и раньше.
func drainStdin(ch ssh.Channel) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, ch)
		close(done)
	}()
	return done
}

func (s *fakeAdminServer) runExec(ch ssh.Channel, cmd string) {
	defer ch.Close()

	if cmd == "bash -s" {
		data, _ := io.ReadAll(ch)
		s.handleScript(ch, string(data))
		return
	}

	// Большинство команд стандартный вход не читают — не ждём его закрытия,
	// просто сливаем в фоне, если что-то всё же придёт. Исключение —
	// chpasswd ниже: там клиент правда пишет и явно закрывает запись, и это
	// важно дождаться перед ответом (см. комментарий там же).
	stdinDrained := drainStdin(ch)

	s.mu.Lock()
	s.calls = append(s.calls, execCall{cmd: cmd})
	s.mu.Unlock()

	switch {
	case strings.Contains(cmd, "authorized_keys"):
		if m := keyLineRe.FindStringSubmatch(cmd); len(m) > 1 {
			s.mu.Lock()
			s.keyLine = m[1]
			s.keyInstalled = true
			s.mu.Unlock()
		}
		sendExit(ch, 0)
	case strings.Contains(cmd, "journalctl -u ssh_tunnel_panel"):
		fmt.Fprintln(ch, `Первый запуск: создан пользователь "admin" с одноразовым паролем: TESTPASS`)
		sendExit(ch, 0)
	case strings.HasPrefix(cmd, "chpasswd"):
		// В отличие от остальных команд здесь клиент действительно пишет в
		// stdin (пароль) и сам закрывает запись, когда дописал. Раньше эта
		// ветка сразу слала exit-status и закрывала канал (общий фоновый
		// io.Copy выше просто отбрасывал байты параллельно) — оттого
		// изредка обгоняла ещё не дописанные клиентом данные, и
		// session.Run на его стороне возвращал ошибку записи вместо кода
		// выхода. Дожидаемся EOF по стандартному входу явно, прежде чем
		// отвечать, — тот самый фоновый io.Copy выше это и делает, просто
		// нужно на него дождаться, а не гонять его в фоне бесконтрольно.
		<-stdinDrained
		sendExit(ch, 0)
	default:
		fmt.Fprintf(ch.Stderr(), "неизвестная команда: %s\n", cmd)
		sendExit(ch, 1)
	}
}

func (s *fakeAdminServer) handleScript(ch ssh.Channel, script string) {
	s.mu.Lock()
	s.calls = append(s.calls, execCall{cmd: "bash -s", stdin: script})
	hardenExit, tunnelExit := s.hardenExit, s.tunnelExit
	s.mu.Unlock()

	switch {
	case strings.Contains(script, "NEW_USER="):
		fmt.Fprintln(ch, "harden: строка первая")
		fmt.Fprintln(ch, "harden: строка вторая")
		sendExit(ch, hardenExit)
	case strings.Contains(script, "TUNNEL_USER="):
		fmt.Fprintln(ch, "tunnel-user: строка первая")
		sendExit(ch, tunnelExit)
	case strings.Contains(script, "ssh_tunnel_panel"):
		fmt.Fprintln(ch, "панель запущена")
		sendExit(ch, 0)
	case strings.Contains(script, "udprelay"):
		fmt.Fprintln(ch, "udprelay собран")
		sendExit(ch, 0)
	default:
		fmt.Fprintln(ch.Stderr(), "неизвестный скрипт")
		sendExit(ch, 1)
	}
}

func (s *fakeAdminServer) callsSnapshot() []execCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]execCall(nil), s.calls...)
}

func (s *fakeAdminServer) hasScriptContaining(sub string) bool {
	for _, c := range s.callsSnapshot() {
		if c.cmd == "bash -s" && strings.Contains(c.stdin, sub) {
			return true
		}
	}
	return false
}

func (s *fakeAdminServer) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return host, port
}

// ---------- сами тесты ----------

func testKeyPath(t *testing.T) string {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return filepath.Join(t.TempDir(), "id_ed25519")
}

// collectEvents собирает все события шины за время работы fn.
func collectEvents(t *testing.T, bus *events.Bus, fn func()) []events.Event {
	t.Helper()
	ch, unsub := bus.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	<-done
	var out []events.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestRunVpsSetupFullFlow(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t), InstallPanel: true, InstallUDPRelay: true,
	}

	var runErr error
	evs := collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("runVpsSetup вернул ошибку: %v", runErr)
	}

	if !srv.hasScriptContaining(fmt.Sprintf("SSH_PORT=%d", port)) {
		t.Error("harden.sh не получил правильный SSH_PORT")
	}
	// NEW_USER пустой: отдельного sudo-пользователя автоматический мастер не
	// заводит, вход и так уже под root по ключу.
	if !srv.hasScriptContaining(`NEW_USER=""`) {
		t.Error("harden.sh должен получить пустой NEW_USER")
	}
	if !srv.hasScriptContaining(`TUNNEL_USER="tunnel"`) {
		t.Error("tunnel-user.sh не запустился")
	}
	if !srv.hasScriptContaining("ssh_tunnel_panel") {
		t.Error("установка панели не запросилась, хотя InstallPanel=true")
	}
	// Ставим свою панель, а не постороннюю системную консоль: та открывала бы
	// наружу вход по паролю к учётке с полным sudo — ровно то, что harden.sh
	// только что закрыл.
	if srv.hasScriptContaining("cockpit") || srv.hasScriptContaining("vpsadmin") {
		t.Error("мастер ставит постороннюю панель вместо ssh_tunnel_panel")
	}
	if !srv.hasScriptContaining("udprelay") {
		t.Error("установка ретранслятора UDP не запросилась, хотя InstallUDPRelay=true")
	}

	var sawDone bool
	var panelLineFound bool
	for _, e := range evs {
		if e.Kind != events.KindVpsSetup {
			continue
		}
		if strings.Contains(e.Text, params.Password) || strings.Contains(e.Error, params.Password) {
			t.Fatalf("пароль root попал в событие: %+v", e)
		}
		if e.Stage == "panel" && strings.Contains(e.Text, "TESTPASS") {
			panelLineFound = true
		}
		if e.Done {
			sawDone = true
			if e.Failed {
				t.Fatalf("итоговое событие сообщает о неудаче: %s", e.Error)
			}
		}
	}
	if !sawDone {
		t.Fatal("не пришло итоговое событие VpsSetupDone")
	}
	if !panelLineFound {
		t.Error("одноразовый пароль панели не пришёл в поток событий")
	}
}

func TestRunVpsSetupWrongPassword(t *testing.T) {
	srv := newFakeAdminServer(t, "correct-pass")
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "wrong-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	evs := collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка при неверном пароле")
	}
	if len(srv.callsSnapshot()) != 0 {
		t.Error("сервер получил команды, хотя пароль не подошёл")
	}
	for _, e := range evs {
		if strings.Contains(e.Text, params.Password) || strings.Contains(e.Error, params.Password) {
			t.Fatalf("пароль root попал в событие: %+v", e)
		}
	}
}

// Ключ положен, но вход по нему почему-то не работает (сервер отклоняет) —
// harden.sh не должен запуститься вовсе: иначе пароли отключились бы, а
// подтверждённого рабочего входа так и не появилось бы.
func TestRunVpsSetupKeyLoginFailsStopsBeforeHarden(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	srv.acceptKey = false
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка проверки входа по ключу")
	}
	if srv.hasScriptContaining("NEW_USER=") {
		t.Fatal("harden.sh запустился, хотя вход по ключу не подтверждён")
	}
}

// harden.sh закончился с ошибкой — tunnel-user.sh запускать нельзя.
func TestRunVpsSetupHardenFailureStopsBeforeTunnelUser(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	srv.hardenExit = 1
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка harden.sh")
	}
	if srv.hasScriptContaining("TUNNEL_USER=") {
		t.Fatal("tunnel-user.sh запустился после неудачного harden.sh")
	}
}

func TestLineWriterSplitsAcrossWrites(t *testing.T) {
	var lines []string
	lw := &lineWriter{emit: func(s string) { lines = append(lines, s) }}
	lw.Write([]byte("perva"))
	lw.Write([]byte("ya line\nvtoraya\nchast' tret'"))
	lw.Write([]byte("yey stroki\n"))
	lw.Write([]byte("bez perevoda stroki v kontce"))
	lw.Flush()

	want := []string{"pervaya line", "vtoraya", "chast' tret'yey stroki", "bez perevoda stroki v kontce"}
	if len(lines) != len(want) {
		t.Fatalf("получилось %v, ожидалось %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("получилось %v, ожидалось %v", lines, want)
		}
	}
}
