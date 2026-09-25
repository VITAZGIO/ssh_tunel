package meshsvc

// Связь побочного сервера с главным и клиент входа для панели — на
// настоящем meshd, собранном из cmd/meshd.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// startMeshd — meshd на свободном порту со входом для панели.
func startMeshd(t *testing.T) (addr, sock string) {
	t.Helper()
	bin, err := buildMeshdOnce()
	if err != nil {
		t.Skipf("не удалось собрать meshd: %v", err)
	}
	dir, err := os.MkdirTemp("", "ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "a.sock")
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-state", filepath.Join(dir, "state.json"), "-admin", sock)
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
	case addr = <-found:
	case <-time.After(10 * time.Second):
		t.Fatal("meshd не поднялся")
	}
	// Сокет появляется чуть позже порта.
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return addr, sock
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestПобочныйСерверВиденНаГлавном(t *testing.T) {
	addr, sock := startMeshd(t)
	admin := NewAdmin(sock)
	ctx := context.Background()

	link := &ServerLink{Addr: addr, Info: ServerInfo{
		ID: "de1", Name: "Германия", Host: "de.example.com", App: "1.2.3", Addrs: []string{"203.0.113.7"},
	}}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { link.Run(lctx); close(done) }()

	var srv Server
	waitFor(t, "главный не увидел побочный сервер с замером связи", func() bool {
		st, err := admin.State(ctx)
		if err != nil || len(st.Servers) != 1 {
			return false
		}
		srv = st.Servers[0]
		return srv.Quality != "unknown"
	})
	if srv.ID != "de1" || srv.Name != "Германия" || srv.Host != "de.example.com" ||
		srv.App != "1.2.3" || len(srv.Addrs) != 1 || srv.Addrs[0] != "203.0.113.7" || srv.ConnectedAt == 0 {
		t.Fatalf("сервер глазами панели: %+v", srv)
	}
	if st := link.State(); !st.Connected || st.LastPing.IsZero() {
		t.Fatalf("состояние связи на побочном: %+v", st)
	}

	// Тот же сервер переподключился — запись одна, а не две.
	link2 := &ServerLink{Addr: addr, Info: link.Info}
	l2ctx, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go link2.Run(l2ctx)
	waitFor(t, "второе подключение не заменило первое", func() bool { return link2.State().Connected })
	if st, _ := admin.State(ctx); len(st.Servers) != 1 {
		t.Fatalf("серверов %d, ожидался один", len(st.Servers))
	}
	cancel()
	<-done
	cancel2()
	waitFor(t, "отключившийся сервер остался в списке", func() bool {
		st, err := admin.State(ctx)
		return err == nil && len(st.Servers) == 0
	})
}

func TestСвязьБезПроброса(t *testing.T) {
	// Порт, на котором никто не слушает: проброса к главному нет.
	link := &ServerLink{Addr: "127.0.0.1:1", Info: ServerInfo{ID: "x"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go link.Run(ctx)
	waitFor(t, "ошибка не объяснена", func() bool {
		return strings.Contains(link.State().Error, UplinkUnitName)
	})
}

func TestAdminНеЗапущен(t *testing.T) {
	_, err := NewAdmin(filepath.Join(t.TempDir(), "нет.sock")).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrNotRunning.Error()) {
		t.Fatalf("ожидалось %v, получено %v", ErrNotRunning, err)
	}
}
