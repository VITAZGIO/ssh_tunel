package webui

// TestUDPRelayServerSourceMatchesCmd — единственная защита от рассинхрона:
// vpsassets/udprelay_server.go.txt существует лишь потому, что go:embed не
// умеет "../.." в пути и не может встроить sshtunnel/cmd/udprelay/main.go
// напрямую. Это копия, и если кто-то поправит оригинал, не тронув копию (или
// наоборот), сервер, который мастер ставит на VPS, тихо разъедется с тем,
// что собирается и тестируется в этом репозитории. Тест сравнивает их
// побайтово и падает при любом расхождении.

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"sshtunnel/internal/meshsvc"
)

func TestUDPRelayServerSourceMatchesCmd(t *testing.T) {
	embedded, err := udpRelayServerSource()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("../../cmd/udprelay/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if embedded != string(original) {
		t.Fatal("vpsassets/udprelay_server.go.txt разошёлся с cmd/udprelay/main.go — " +
			"скопируй актуальный main.go поверх vpsassets/udprelay_server.go.txt")
	}
}

// Скрипты установки служб собираются из кусков (исходник, юнит systemd,
// правка брандмауэра) — bash -n ловит сломанные кавычки и heredoc раньше,
// чем их увидит настоящий сервер.
func TestInstallScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("нет bash")
	}
	for name, build := range map[string]func() (string, error){
		"udprelay": udpRelayInstallScript,
		"meshd":    func() (string, error) { return meshsvc.InstallScript("v1.5.0"), nil },
	} {
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(script, "47830:47831") {
			t.Errorf("%s: нет исключения в брандмауэре", name)
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", name, err, out)
		}
	}
}
