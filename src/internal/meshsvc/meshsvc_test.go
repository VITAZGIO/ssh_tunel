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
	script := InstallScript()
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
	if !strings.HasPrefix(line, `restrict,port-forwarding,permitopen="127.0.0.1:47831" ssh-ed25519 AAAAC3Nza `) {
		t.Fatalf("строка: %q", line)
	}
	if strings.ContainsAny(strings.TrimPrefix(line, `restrict,port-forwarding,permitopen="127.0.0.1:47831" `), "\"\n") {
		t.Fatalf("в строке кавычки или перевод строки: %q", line)
	}
}

func TestUplinkUnit(t *testing.T) {
	u := UplinkUnit("de.example.com", 2222, "/etc/ssh_tunnel_panel/uplink", "/etc/ssh_tunnel_panel/uplink_known_hosts")
	for _, want := range []string{"-p 2222", "-L 127.0.0.1:47831:127.0.0.1:47831", "meshlink@de.example.com", "ExitOnForwardFailure=yes"} {
		if !strings.Contains(u, want) {
			t.Errorf("в юните нет %q:\n%s", want, u)
		}
	}
}
