package meshsvc

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSourceMatchesCmd — копия исходника meshd не должна разъехаться с
// оригиналом: сервер, собранный из копии, должен быть тем же, что тестируется.
func TestSourceMatchesCmd(t *testing.T) {
	original, err := os.ReadFile("../../cmd/meshd/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if Source != string(original) {
		t.Fatal("internal/meshsvc/meshd.go.txt разошёлся с cmd/meshd/main.go — " +
			"скопируй актуальный main.go поверх meshd.go.txt")
	}
}

func TestInstallScriptParses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("нет bash")
	}
	script := InstallScript("v1.5.0-mesh1")
	for _, want := range []string{"47830:47831", AdminSocket, "RuntimeDirectory=meshd", UplinkUnitName} {
		if !strings.Contains(script, want) {
			t.Errorf("в скрипте нет %q", want)
		}
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestLinkAuthorizedKeyТолькоПроброс(t *testing.T) {
	line := LinkAuthorizedKey("ssh-ed25519 AAAAC3Nza root@nl  \n", `Нидерланды "x" nl-1`)
	const opts = `restrict,port-forwarding,permitopen="127.0.0.1:47831",command="/usr/sbin/nologin" `
	if !strings.HasPrefix(line, opts+"ssh-ed25519 AAAAC3Nza ") {
		t.Fatalf("строка: %q", line)
	}
	if strings.ContainsAny(strings.TrimPrefix(line, opts), "\"\n") {
		t.Fatalf("в строке кавычки или перевод строки: %q", line)
	}
}

func TestUplinkUnit(t *testing.T) {
	u := UplinkUnit("de.example.com", 2222, "/etc/ssh_tunnel_panel/uplink", "/etc/ssh_tunnel_panel/uplink_known_hosts", true)
	for _, want := range []string{"-p 2222", "-L 127.0.0.1:47831:127.0.0.1:47831", "meshlink@de.example.com",
		"ExitOnForwardFailure=yes", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(u, want) {
			t.Errorf("в юните нет %q:\n%s", want, u)
		}
	}
}

func TestKnownHostsLine(t *testing.T) {
	if got := KnownHostsLine("de.example.com", 22, "ssh-ed25519 AAAA root@de"); got != "de.example.com ssh-ed25519 AAAA" {
		t.Errorf("порт 22: %q", got)
	}
	if got := KnownHostsLine("203.0.113.7", 2222, "ssh-ed25519 AAAA"); got != "[203.0.113.7]:2222 ssh-ed25519 AAAA" {
		t.Errorf("другой порт: %q", got)
	}
}

func TestInstallScriptКачаетТуЖеВерсию(t *testing.T) {
	for version, want := range map[string]string{
		"v1.5.0":            "releases/download/v1.5.0/$BIN",
		"v1.5.0-mesh1":      "releases/download/v1.5.0-mesh1/$BIN",
		"dev":               "releases/latest/download/$BIN",
		"":                  "releases/latest/download/$BIN",
		"v1.5.0; rm -rf /":  "releases/latest/download/$BIN",
		"v1.5.0\"$(reboot)": "releases/latest/download/$BIN",
	} {
		if s := InstallScript(version); !strings.Contains(s, want) {
			t.Errorf("версия %q: в скрипте нет %q", version, want)
		}
	}
}
