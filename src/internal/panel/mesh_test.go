package panel

// Сеть устройств в панели: главный и побочный сервер на настоящем meshd
// (собран из cmd/meshd), система — подделка. Проброс между серверами здесь
// не нужен: побочный подключается к meshd напрямую, для протокола это одно
// и то же.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/mesh"
	"sshtunnel/internal/meshsvc"
)

type fakeMeshSystem struct {
	mu        sync.Mutex
	units     map[string]UnitState
	unitText  map[string]string
	scripts   []string
	linkUser  bool
	linkSSHD  bool
	linkKeys  []string
	kills     int
	scriptErr error
	addrs     []string
}

func newFakeMeshSystem() *fakeMeshSystem {
	return &fakeMeshSystem{units: map[string]UnitState{}, unitText: map[string]string{}}
}

func (f *fakeMeshSystem) UnitState(unit string) UnitState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.units[unit]
}

func (f *fakeMeshSystem) RunScript(script string, onLine func(string)) error {
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	err := f.scriptErr
	if err == nil {
		f.units[meshdUnit] = UnitState{Installed: true, Active: true, Enabled: true}
	}
	f.mu.Unlock()
	onLine("meshd запущен")
	return err
}

func (f *fakeMeshSystem) InstallUnit(name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unitText[name] = text
	f.units[name] = UnitState{Installed: true, Active: true, Enabled: true}
	return nil
}

func (f *fakeMeshSystem) DisableUnit(unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.units[unit]; ok {
		st.Active, st.Enabled = false, false
		f.units[unit] = st
	}
	return nil
}

func (f *fakeMeshSystem) UnitLog(string, int) []string {
	return []string{"строка журнала"}
}

func (f *fakeMeshSystem) EnsureLinkUser() error {
	f.mu.Lock()
	f.linkUser = true
	f.mu.Unlock()
	return nil
}

func (f *fakeMeshSystem) WriteLinkKeys(lines []string) error {
	f.mu.Lock()
	f.linkKeys = append([]string(nil), lines...)
	f.mu.Unlock()
	return nil
}

func (f *fakeMeshSystem) KillLinkSessions() error {
	f.mu.Lock()
	f.kills++
	f.mu.Unlock()
	return nil
}

func (f *fakeMeshSystem) EnsureLinkSSHD() error {
	f.mu.Lock()
	f.linkSSHD = true
	f.mu.Unlock()
	return nil
}

func (f *fakeMeshSystem) HostKeys() []string {
	return []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMainHostKey"}
}

func (f *fakeMeshSystem) LocalAddrs() []string { return f.addrs }

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

func startTestMeshd(t *testing.T) (addr, sock string) {
	t.Helper()
	bin, err := buildMeshdOnce()
	if err != nil {
		t.Skipf("не удалось собрать meshd: %v", err)
	}
	dir, err := os.MkdirTemp("", "pm")
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
	return addr, sock
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newTestMesh(t *testing.T, sys MeshSystem, admin meshAdmin, host string, port int) *MeshManager {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewMeshManager(st, sys, admin, dir, "9.9.9").WithSSH(host, port)
	// Имена в тесте не разрешаются по-настоящему.
	m.lookup = func(_ context.Context, h string) ([]string, error) {
		switch h {
		case "main.example":
			return []string{"198.51.100.1"}, nil
		case "de.example":
			return []string{"203.0.113.7"}, nil
		}
		return nil, fmt.Errorf("нет такого имени")
	}
	t.Cleanup(m.Stop)
	return m
}

func findServer(st MeshState, id string) (MeshServer, bool) {
	for _, s := range st.Servers {
		if s.ID == id {
			return s, true
		}
	}
	return MeshServer{}, false
}

func TestСетьГлавныйИПобочный(t *testing.T) {
	addr, sock := startTestMeshd(t)
	ctx := context.Background()

	mainSys := newFakeMeshSystem()
	mainSys.addrs = []string{"198.51.100.1"}
	main := newTestMesh(t, mainSys, meshsvc.NewAdmin(sock), "main.example", 2222)

	// Главный: установка meshd и вход для побочных.
	if err := main.SetMain("Москва"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "установка не закончилась", func() bool {
		j := main.jobSnapshot()
		return j != nil && !j.Running
	})
	if j := main.jobSnapshot(); j.Error != "" || len(mainSys.scripts) != 1 ||
		!strings.Contains(mainSys.scripts[0], "meshd.service") {
		t.Fatalf("установка: %+v, скриптов %d", j, len(mainSys.scripts))
	}
	if !mainSys.linkUser || !mainSys.linkSSHD {
		t.Fatal("вход для побочных серверов не подготовлен")
	}

	// Код подключения для побочного сервера.
	link, code, err := main.CreateLink("Германия", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := decodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Host != "main.example" || inv.Port != 2222 || inv.ID != link.ID || len(inv.HostKeys) != 1 {
		t.Fatalf("код подключения: %+v", inv)
	}
	if len(mainSys.linkKeys) != 1 || !strings.HasPrefix(mainSys.linkKeys[0], meshsvc.LinkKeyOptions+" ssh-ed25519 ") {
		t.Fatalf("ключ побочного на главном: %q", mainSys.linkKeys)
	}
	if _, _, err := main.CreateLink("германия", "", 0); err == nil {
		t.Fatal("второй сервер с тем же именем принят")
	}

	// Побочный: вставили код.
	secSys := newFakeMeshSystem()
	secSys.addrs = []string{"203.0.113.7"}
	secSys.units[meshdUnit] = UnitState{Installed: true, Active: true, Enabled: true}
	sec := newTestMesh(t, secSys, nil, "de.example", 22)
	sec.linkAddr = addr
	if err := sec.Join("  "+code[:20]+"\n"+code[20:]+"\n", ""); err != nil {
		t.Fatal(err)
	}
	if secSys.units[meshdUnit].Active {
		t.Fatal("свой meshd на побочном не остановлен — он занял бы порт проброса")
	}
	unit := secSys.unitText[meshsvc.UplinkUnitName]
	for _, want := range []string{"meshlink@main.example", "-p 2222", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("в юните проброса нет %q:\n%s", want, unit)
		}
	}
	keyData, err := os.ReadFile(filepath.Join(sec.linkDir(), "id_ed25519"))
	if err != nil || string(keyData) != inv.Key {
		t.Fatalf("ключ на побочном: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(sec.linkDir(), "id_ed25519")); info.Mode().Perm() != 0o600 {
		t.Fatalf("права ключа %v", info.Mode())
	}
	known, _ := os.ReadFile(filepath.Join(sec.linkDir(), "known_hosts"))
	if !strings.HasPrefix(string(known), "[main.example]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMainHostKey") {
		t.Fatalf("known_hosts: %q", known)
	}

	// Главный видит побочный сервер и качество связи с ним.
	eventually(t, "главный не видит побочный сервер", func() bool {
		s, ok := findServer(main.State(ctx), link.ID)
		return ok && s.Online && s.Link != nil && s.Link.Quality != "unknown"
	})
	secState := sec.State(ctx)
	if secState.EffectiveRole != MeshRoleSecondary || secState.LinkInfo == nil || !secState.LinkInfo.Connected ||
		secState.MainHost != "main.example" {
		t.Fatalf("побочный о себе: %+v", secState)
	}

	// Устройства: одно подключено к главному, другое — через побочный.
	key := mesh.NewKey()
	for _, dev := range []struct{ name, via string }{{"ноутбук", "198.51.100.1"}, {"телефон", "de.example"}} {
		c := mesh.New(mesh.Config{Addr: addr, Key: key, DeviceID: mesh.NewDeviceID(), Name: dev.name, Via: dev.via},
			func(n, a string) (net.Conn, error) { return net.Dial(n, a) }, nil)
		cctx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)
		go c.Run(cctx)
	}
	var devs []MeshDevice
	eventually(t, "устройства не появились", func() bool {
		st := main.State(ctx)
		if len(st.Networks) != 1 || len(st.Networks[0].Devices) != 2 {
			return false
		}
		devs = st.Networks[0].Devices
		return devs[0].Online && devs[1].Online
	})
	byName := map[string]MeshDevice{}
	for _, d := range devs {
		byName[d.Name] = d
	}
	if byName["ноутбук"].Server != "main" || byName["телефон"].Server != link.ID {
		t.Fatalf("через какой сервер: ноутбук=%q телефон=%q (побочный %q)",
			byName["ноутбук"].Server, byName["телефон"].Server, link.ID)
	}

	// Переименование из панели.
	netID := main.State(ctx).Networks[0].ID
	if err := main.Rename(ctx, netID, byName["телефон"].ID, "Телефон мамы"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "переименование не видно", func() bool {
		for _, d := range main.State(ctx).Networks[0].Devices {
			if d.ID == byName["телефон"].ID {
				return d.Display == "Телефон мамы"
			}
		}
		return false
	})

	// Отключили побочный сервер на главном — ключ убран, соединения порваны.
	if err := main.DeleteLink(link.ID); err != nil {
		t.Fatal(err)
	}
	if len(mainSys.linkKeys) != 0 || mainSys.kills != 1 {
		t.Fatalf("после удаления: ключей %d, обрывов %d", len(mainSys.linkKeys), mainSys.kills)
	}
	if _, ok := findServer(main.State(ctx), link.ID); ok {
		// В тесте соединение идёт мимо SSH, поэтому само не рвётся:
		// сервер может остаться в списке как «подключившийся сам».
		if s, _ := findServer(main.State(ctx), link.ID); s.Known {
			t.Fatal("удалённый сервер всё ещё числится добавленным")
		}
	}

	// Выключили сеть на главном.
	if err := main.Disable(); err != nil {
		t.Fatal(err)
	}
	if st := main.State(ctx); st.Role != MeshRoleOff || st.Meshd.Active {
		t.Fatalf("после выключения: роль %q, meshd %+v", st.Role, st.Meshd)
	}
}

func TestMeshdОтМастераСчитаетсяГлавным(t *testing.T) {
	sys := newFakeMeshSystem()
	sys.units[meshdUnit] = UnitState{Installed: true, Active: true}
	m := newTestMesh(t, sys, fakeDownAdmin{}, "", 22)
	st := m.State(context.Background())
	if st.Role != MeshRoleOff || st.EffectiveRole != MeshRoleMain || st.AdminErr == "" {
		t.Fatalf("состояние: %+v", st)
	}
	if len(st.Servers) != 1 || !st.Servers[0].Main {
		t.Fatalf("серверы: %+v", st.Servers)
	}
}

type fakeDownAdmin struct{}

func (fakeDownAdmin) State(context.Context) (meshsvc.State, error) {
	return meshsvc.State{}, meshsvc.ErrNotRunning
}
func (fakeDownAdmin) Rename(context.Context, string, string, string) error {
	return meshsvc.ErrNotRunning
}
func (fakeDownAdmin) Forget(context.Context, string, string) error { return meshsvc.ErrNotRunning }
func (fakeDownAdmin) SetSelf(context.Context, string, bool, string) error {
	return meshsvc.ErrNotRunning
}
func (fakeDownAdmin) ForgetServer(context.Context, string) error { return meshsvc.ErrNotRunning }

func TestКодПодключенияПроверяется(t *testing.T) {
	for _, code := range []string{"", "что-то", invitePrefix + "!!!", invitePrefix + "e30"} {
		if _, err := decodeInvite(code); err == nil {
			t.Errorf("код %q принят", code)
		}
	}
	_, priv, err := generateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"evil host", "-oProxyCommand=x", "a\nb", ""} {
		code := encodeInvite(meshInvite{V: 1, ID: "x", Host: host, Port: 22, Key: priv})
		if _, err := decodeInvite(code); err == nil {
			t.Errorf("адрес %q принят", host)
		}
	}
	good := encodeInvite(meshInvite{V: 1, ID: "x", Host: "de.example.com", Port: 22, Key: priv})
	if _, err := decodeInvite(good); err != nil {
		t.Fatal(err)
	}
}

func TestCreateLinkТолькоНаГлавном(t *testing.T) {
	m := newTestMesh(t, newFakeMeshSystem(), fakeDownAdmin{}, "main.example", 22)
	if _, _, err := m.CreateLink("Германия", "", 0); err == nil {
		t.Fatal("сервер добавлен без роли «главный»")
	}
}

func TestEnsureLinkBlock(t *testing.T) {
	cfg := "Port 22\n\n" + sshdMatchBlock()
	out, changed := ensureLinkBlock(cfg)
	if !changed || !strings.Contains(out, "Match User "+meshsvc.LinkUser) || !strings.HasPrefix(out, cfg) {
		t.Fatalf("блок не добавлен:\n%s", out)
	}
	if again, changed := ensureLinkBlock(out); changed || again != out {
		t.Fatal("повторный вызов что-то поменял")
	}
	// Устаревшее содержимое заменяется, без второй копии.
	old := strings.Replace(out, "PermitListen none", "PermitListen any", 1)
	fixed, changed := ensureLinkBlock(old)
	if !changed || fixed != out {
		t.Fatalf("устаревший блок не обновлён:\n%s", fixed)
	}
}

// С настоящим sshd, если он есть: блок проходит проверку, и для meshlink
// действуют именно наши ограничения.
func TestLinkBlockНастоящийSSHD(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sshd_config")
	base := "Port 2222\n"
	if err := os.WriteFile(cfgPath, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sshdCheck(cfgPath); err != nil {
		t.Skipf("sshd недоступен: %v", err)
	}
	cfg, _ := ensureLinkBlock(base)
	os.WriteFile(cfgPath, []byte(cfg), 0o644)
	if err := sshdCheck(cfgPath); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sshd", "-T", "-f", cfgPath, "-C", "user="+meshsvc.LinkUser+",host=x,addr=203.0.113.7").CombinedOutput()
	if err != nil {
		t.Skipf("sshd -T: %v %s", err, out)
	}
	for _, want := range []string{
		"allowtcpforwarding local", "permitopen 127.0.0.1:47831", "permitlisten none",
		"forcecommand /usr/sbin/nologin", "permittty no", "allowstreamlocalforwarding no",
	} {
		if !strings.Contains(string(out), "\n"+want+"\n") {
			t.Errorf("для meshlink не действует %q", want)
		}
	}
}
