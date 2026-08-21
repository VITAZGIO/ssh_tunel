package core

// Настоящий SSH-сервер прямо в процессе теста — чтобы проверять цепочку
// целиком, а не по кусочкам. Устроен так же, как сервер в тестах основного
// модуля: понимает direct-tcpip и сам открывает соединение до цели.
//
// Дублирование здесь осознанное: вспомогательный код тестов из чужого пакета
// не импортируется, а поднимать ради этого настоящий sshd в сборке — хуже.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/events"
	"sshtunnel/internal/tunnel"
)

type testSSHServer struct {
	addr     string
	channels atomic.Int64 // сколько соединений сервер открыл по нашей просьбе
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

	s := &testSSHServer{addr: ln.Addr().String()}

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
			go s.handle(c, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testSSHServer) handle(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
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
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
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

// startTunnel поднимает настоящее ядро туннеля до тестового сервера.
func startTunnel(t *testing.T, poolSize int) (*tunnel.Tunnel, *testSSHServer) {
	t.Helper()
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)

	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)

	tun := tunnel.New(tunnel.Config{
		Host:           host,
		SSHPort:        sshPort,
		User:           "test",
		KeyPath:        keyPath,
		SocksAddr:      fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		HTTPAddr:       fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		PoolSize:       poolSize,
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
		// Цели в тестах живут на 127.0.0.1, а локальные адреса в обычном режиме
		// идут мимо туннеля. Здесь проверяется сам туннель, поэтому локальную
		// сеть намеренно заворачиваем в него.
		LocalViaTunnel: true,
	}, events.NewBus())

	if err := tun.Start(); err != nil {
		t.Fatalf("туннель не запустился: %v", err)
	}
	t.Cleanup(tun.Stop)
	if !tun.WaitReady(1, 10*time.Second) {
		t.Fatal("туннель не поднялся")
	}
	return tun, srv
}

// httpTarget — «сайт в интернете», до которого соединение открывает сервер.
func httpTarget(t *testing.T) *net.TCPAddr {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "привет от %s", r.Host)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("A"), 1024)
		for i := 0; i < 512; i++ { // 512 КБ — больше любого буфера по дороге
			w.Write(chunk)
		}
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
