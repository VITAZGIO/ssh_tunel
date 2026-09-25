package panel

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/meshsvc"
)

// systemMesh — MeshSystem поверх systemd, bash и учётных записей системы.
// Требует root, как и вся панель.
type systemMesh struct{}

// NewSystemMesh — настоящая MeshSystem.
func NewSystemMesh() MeshSystem { return systemMesh{} }

// unitDir — куда пишутся юниты. Переменная — для тестов.
var unitDir = "/etc/systemd/system"

func (systemMesh) UnitState(unit string) UnitState {
	out, _ := runSystemctl("show", unit, "--property=LoadState,ActiveState,SubState,UnitFileState")
	return parseUnitState(out)
}

// parseUnitState разбирает вывод systemctl show.
func parseUnitState(out string) UnitState {
	props := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			props[k] = v
		}
	}
	st := UnitState{
		Installed: props["LoadState"] == "loaded",
		Active:    props["ActiveState"] == "active",
		Enabled:   strings.HasPrefix(props["UnitFileState"], "enabled"),
	}
	if st.Installed {
		st.Detail = props["ActiveState"]
		if sub := props["SubState"]; sub != "" && sub != props["ActiveState"] {
			st.Detail += " (" + sub + ")"
		}
	}
	return st
}

// scriptCommand — чем выполнить скрипт установки. Панель работает службой
// systemd с ProtectSystem (/usr только на чтение), а скрипт кладёт meshd в
// /usr/local/bin и может ставить пакеты. Поэтому под systemd он запускается
// отдельной временной службой (systemd-run) — без ограничений панели, но
// с выводом прямо сюда. Панель, запущенная руками, выполняет его сама.
func scriptCommand() *exec.Cmd {
	if os.Getenv("INVOCATION_ID") != "" {
		if path, err := exec.LookPath("systemd-run"); err == nil {
			unit := "ssh_tunnel_mesh_install_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			return exec.Command(path, "--quiet", "--wait", "--pipe", "--collect",
				"--unit="+unit, "--setenv=HOME=/root", "--", "/bin/bash", "-s")
		}
	}
	return exec.Command("bash", "-s")
}

func (systemMesh) RunScript(script string, onLine func(string)) error {
	cmd := scriptCommand()
	cmd.Stdin = strings.NewReader(script)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			onLine(sc.Text())
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Run()
	pw.Close()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("скрипт установки завершился с ошибкой: %w", err)
	}
	return nil
}

func (systemMesh) InstallUnit(name, text string) error {
	if err := os.WriteFile(filepath.Join(unitDir, name), []byte(text), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", name}, {"restart", name}} {
		if out, err := runSystemctl(args...); err != nil {
			return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), strings.TrimSpace(out))
		}
	}
	return nil
}

func (s systemMesh) DisableUnit(unit string) error {
	if !s.UnitState(unit).Installed {
		return nil
	}
	if out, err := runSystemctl("disable", "--now", unit); err != nil {
		return fmt.Errorf("systemctl disable %s: %s", unit, strings.TrimSpace(out))
	}
	return nil
}

func (systemMesh) UnitLog(unit string, n int) []string {
	out, err := exec.Command("journalctl", "-u", unit, "-n", fmt.Sprint(n),
		"--no-pager", "--output=short-iso", "--no-hostname").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" && !strings.HasPrefix(l, "-- ") {
			lines = append(lines, l)
		}
	}
	return lines
}

func (systemMesh) EnsureLinkUser() error {
	if _, err := user.Lookup(meshsvc.LinkUser); err == nil {
		return nil
	}
	if err := runSystem("useradd", "--system", "--create-home",
		"--home-dir", "/var/lib/"+meshsvc.LinkUser,
		"--shell", "/usr/sbin/nologin", meshsvc.LinkUser); err != nil {
		return err
	}
	// Как у клиентов панели: пароля нет вовсе, но учётка не заблокирована —
	// заблокированную sshd не пустит и по ключу.
	return runSystem("usermod", "-p", "*", meshsvc.LinkUser)
}

func (systemMesh) WriteLinkKeys(lines []string) error {
	u, err := user.Lookup(meshsvc.LinkUser)
	if err != nil {
		if len(lines) == 0 {
			return nil
		}
		return fmt.Errorf("нет пользователя %s: %w", meshsvc.LinkUser, err)
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	return writeAuthorizedKeysFile(u, content)
}

func (systemMesh) KillLinkSessions() error {
	if _, err := user.Lookup(meshsvc.LinkUser); err != nil {
		return nil
	}
	return killUserSessions(meshsvc.LinkUser)
}

func (systemMesh) EnsureLinkSSHD() error {
	current, err := os.ReadFile(SSHDPath)
	if err != nil {
		return fmt.Errorf("не могу прочитать %s: %w", SSHDPath, err)
	}
	updated, changed := ensureLinkBlock(string(current))
	if !changed {
		return nil
	}
	return applySSHDConfig(updated)
}

func (systemMesh) HostKeys() []string {
	var keys []string
	for _, t := range []string{"ed25519", "ecdsa", "rsa"} {
		data, err := os.ReadFile("/etc/ssh/ssh_host_" + t + "_key.pub")
		if err != nil {
			continue
		}
		if f := strings.Fields(string(data)); len(f) >= 2 {
			keys = append(keys, f[0]+" "+f[1])
		}
	}
	return keys
}

func (systemMesh) LocalAddrs() []string {
	return localAddrs()
}

// localAddrs — адреса интерфейсов сервера, кроме служебных.
func localAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || !ipn.IP.IsGlobalUnicast() {
			continue
		}
		out = append(out, ipn.IP.String())
		if len(out) >= 8 {
			break
		}
	}
	return out
}

const (
	linkMarkerBegin = "# BEGIN ssh_tunnel_panel meshlink (не редактируй руками — правь через панель)"
	linkMarkerEnd   = "# END ssh_tunnel_panel meshlink"
)

// ensureLinkBlock — как ensureMatchBlock, но для пользователя meshlink; блок
// обновляется, если его содержимое устарело.
func ensureLinkBlock(config string) (string, bool) {
	block := linkMarkerBegin + "\n" + meshsvc.LinkSSHDBlock() + linkMarkerEnd + "\n"
	if i := strings.Index(config, linkMarkerBegin); i >= 0 {
		j := strings.Index(config[i:], linkMarkerEnd)
		if j < 0 {
			return config, false
		}
		end := i + j + len(linkMarkerEnd)
		if end < len(config) && config[end] == '\n' {
			end++
		}
		if config[i:end] == block {
			return config, false
		}
		return config[:i] + block + config[end:], true
	}
	out := config
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	return out + block, true
}
