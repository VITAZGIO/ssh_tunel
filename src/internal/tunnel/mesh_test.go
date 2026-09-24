package tunnel

// Сеть устройств целиком: два туннеля (два «устройства») через тестовый
// SSH-сервер, настоящий meshd, собранный из cmd/meshd, и соединение от
// одного устройства к службе другого по имени «b.mesh» — через SOCKS, как это
// делает браузер, и через ServeConn, как это делает стек режима VPN.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/events"
	"sshtunnel/internal/mesh"
)

var buildMeshdOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "meshd-build")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "meshd")
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/meshd").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("сборка meshd: %v: %s", err, out)
	}
	return bin, nil
})

func startTestMeshd(t *testing.T) string {
	t.Helper()
	bin, err := buildMeshdOnce()
	if err != nil {
		t.Skipf("не удалось собрать meshd: %v", err)
	}
	// Порт выбирает сам meshd (:0) и сообщает в журнале — см. тот же приём
	// в internal/mesh.
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-state", filepath.Join(t.TempDir(), "state.json"))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if i := strings.Index(sc.Text(), "слушает "); i >= 0 {
				found <- strings.TrimSpace(sc.Text()[i+len("слушает "):])
			}
		}
	}()
	select {
	case addr := <-found:
		return addr
	case <-time.After(10 * time.Second):
		t.Fatal("meshd не поднялся")
		return ""
	}
}

func startMeshTunnel(t *testing.T, srv *testSSHServer, keyPath string, m mesh.Config) (*Tunnel, string) {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	tun := New(Config{
		Host: host, SSHPort: sshPort, User: "test", KeyPath: keyPath,
		SocksAddr: socksAddr, PoolSize: 1,
		KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts"),
		Mesh:           &m,
	}, events.NewBus())
	if err := tun.Start(); err != nil {
		t.Fatalf("туннель не запустился: %v", err)
	}
	t.Cleanup(tun.Stop)
	return tun, socksAddr
}

func TestСетьУстройствЧерезТуннель(t *testing.T) {
	meshd := startTestMeshd(t)
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)

	key := mesh.NewKey()
	a, socksA := startMeshTunnel(t, srv, keyPath, mesh.Config{
		Key: key, DeviceID: mesh.NewDeviceID(), Name: "a", AllowIncoming: true, Addr: meshd,
	})
	startMeshTunnel(t, srv, keyPath, mesh.Config{
		Key: key, DeviceID: mesh.NewDeviceID(), Name: "b", AllowIncoming: true, Addr: meshd,
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := a.Mesh(); m != nil {
			if _, ok := m.Resolve("b.mesh"); ok {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("устройство a не увидело b")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Служба на устройстве b слушает его 127.0.0.1 — в тесте это тот же
	// процесс, но достаётся она только через meshd.
	target := echoServer(t)

	// Как браузер: SOCKS с именем.
	c := socks5Connect(t, socksA, "b.mesh", target.Port, true)
	assertHTTPBody(t, c, "b.mesh", "/hello", "привет от b.mesh")
	c.Close()

	// Как стек режима VPN: соединение на адрес устройства.
	bIP, _ := a.Mesh().Resolve("b")
	app, ours := net.Pipe()
	defer app.Close()
	go a.ServeConn(ours, net.JoinHostPort(bIP.String(), strconv.Itoa(target.Port)), true)
	assertHTTPBody(t, app, "b.mesh", "/hello", "привет от b.mesh")
}
