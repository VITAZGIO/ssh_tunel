package tunnel

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"sshtunnel/internal/events"
)

// fakeTimeoutErr — реализует net.Error с Timeout()==true, как настоящая
// ошибка таймаута из net.Dialer, без реального сетевого стека.
type fakeTimeoutErr struct{ msg string }

func (e *fakeTimeoutErr) Error() string   { return e.msg }
func (e *fakeTimeoutErr) Timeout() bool   { return true }
func (e *fakeTimeoutErr) Temporary() bool { return true }

func TestClassifyConnErrorAuth(t *testing.T) {
	err := fmt.Errorf("не удалось подключиться к 203.0.113.10:22: %w",
		errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"))
	ce := classifyConnError(err)
	if ce.Kind != ConnErrorAuth {
		t.Fatalf("ожидал ConnErrorAuth, получил %q", ce.Kind)
	}
	if ce.Message == "" || ce.Message == err.Error() {
		t.Fatalf("сообщение должно быть переведено на понятный текст, получил %q", ce.Message)
	}
}

func TestClassifyConnErrorRefusedByErrno(t *testing.T) {
	err := fmt.Errorf("не удалось подключиться к 203.0.113.10:22: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
	ce := classifyConnError(err)
	if ce.Kind != ConnErrorRefused {
		t.Fatalf("ожидал ConnErrorRefused, получил %q", ce.Kind)
	}
}

func TestClassifyConnErrorRefusedBySubstring(t *testing.T) {
	err := errors.New("dial tcp 203.0.113.10:22: connect: connection refused")
	ce := classifyConnError(err)
	if ce.Kind != ConnErrorRefused {
		t.Fatalf("ожидал ConnErrorRefused, получил %q", ce.Kind)
	}
}

func TestClassifyConnErrorNoResponseByTimeout(t *testing.T) {
	err := fmt.Errorf("не удалось подключиться к 203.0.113.10:22: %w", &fakeTimeoutErr{msg: "dial tcp: i/o timeout"})
	ce := classifyConnError(err)
	if ce.Kind != ConnErrorNoResponse {
		t.Fatalf("ожидал ConnErrorNoResponse, получил %q", ce.Kind)
	}
}

func TestClassifyConnErrorNoResponseBySubstring(t *testing.T) {
	cases := []string{
		"dial tcp: lookup bad.example: no such host",
		"dial tcp 10.0.0.1:22: connect: no route to host",
		"dial tcp 10.0.0.1:22: connect: network is unreachable",
	}
	for _, msg := range cases {
		ce := classifyConnError(errors.New(msg))
		if ce.Kind != ConnErrorNoResponse {
			t.Errorf("%q: ожидал ConnErrorNoResponse, получил %q", msg, ce.Kind)
		}
	}
}

func TestClassifyConnErrorOtherKeepsOriginalMessage(t *testing.T) {
	err := errors.New("ssh: some unusual protocol error we don't recognize")
	ce := classifyConnError(err)
	if ce.Kind != ConnErrorOther {
		t.Fatalf("ожидал ConnErrorOther, получил %q", ce.Kind)
	}
	if ce.Message != err.Error() {
		t.Fatalf("для ConnErrorOther текст должен остаться как есть: %q != %q", ce.Message, err.Error())
	}
}

// Проверка сквозным путём — от Start() с настоящим SSH-рукопожатием до
// готового ConnError: сервер принимает соединения, но отвергает наш ключ,
// потому что тестовый сервер настроен на другой открытый ключ (та же
// картина, что и с клиентом, которого заморозили или удалили в панели).
func TestStartReportsConnErrorOnAuthFailure(t *testing.T) {
	dir := t.TempDir()
	_, wrongPub := writeTestKey(t, dir) // ключ, который сервер ждёт
	keyPath, _ := writeTestKey(t, dir)  // ключ, который реально используем — другой
	srv := newTestSSHServer(t, wrongPub)

	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	tun := New(Config{
		Host: host, SSHPort: sshPort, User: "test", KeyPath: keyPath,
		SocksAddr: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		HTTPAddr:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		PoolSize:  1, KnownHostsPath: filepath.Join(dir, "known_hosts"),
	}, events.NewBus())

	err = tun.Start()
	if err == nil {
		tun.Stop()
		t.Fatal("подключение с чужим ключом должно провалиться")
	}
	var ce *ConnError
	if !errors.As(err, &ce) {
		t.Fatalf("ожидал *ConnError, получил %T: %v", err, err)
	}
	if ce.Kind != ConnErrorAuth {
		t.Fatalf("ожидал ConnErrorAuth, получил %q", ce.Kind)
	}
}

// То же самое, но порт вообще ничей — сразу TCP RST.
func TestStartReportsConnErrorOnRefused(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeTestKey(t, dir)
	port := freePort(t) // слушателя на нём уже нет — freePort сам его закрывает

	tun := New(Config{
		Host: "127.0.0.1", SSHPort: port, User: "test", KeyPath: keyPath,
		SocksAddr: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		HTTPAddr:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		PoolSize:  1, KnownHostsPath: filepath.Join(dir, "known_hosts"),
	}, events.NewBus())

	err := tun.Start()
	if err == nil {
		tun.Stop()
		t.Fatal("подключение к закрытому порту должно провалиться")
	}
	var ce *ConnError
	if !errors.As(err, &ce) {
		t.Fatalf("ожидал *ConnError, получил %T: %v", err, err)
	}
	if ce.Kind != ConnErrorRefused {
		t.Fatalf("ожидал ConnErrorRefused, получил %q", ce.Kind)
	}
}

func TestConnErrorUnwrap(t *testing.T) {
	base := errors.New("connection refused")
	ce := classifyConnError(base)
	if !errors.Is(ce, base) {
		t.Fatal("ConnError должен разворачиваться до исходной ошибки через errors.Is")
	}
}
