package panel

import (
	"errors"
	"testing"
)

// systemctl is-enabled отвечает не только словом, но и кодом возврата:
// "disabled" приходит вместе с ненулевым кодом. Ошибка выполнения сама по
// себе не должна означать «управлять нечем» — сначала смотрим на слово.
func TestAutostartРазбираетОтветыSystemctl(t *testing.T) {
	cases := []struct {
		out           string
		err           error
		wantSupported bool
		wantEnabled   bool
	}{
		{"enabled\n", nil, true, true},
		{"disabled\n", errors.New("exit status 1"), true, false},
		{"masked\n", errors.New("exit status 1"), true, false},
		{"", errNoSystemctl, false, false},
		{"Failed to get unit file state for ssh_tunnel_panel.service: No such file or directory\n",
			errors.New("exit status 1"), false, false},
	}
	orig := runSystemctl
	defer func() { runSystemctl = orig }()

	for _, tc := range cases {
		runSystemctl = func(args ...string) (string, error) { return tc.out, tc.err }
		got := Autostart()
		if got.Supported != tc.wantSupported || got.Enabled != tc.wantEnabled {
			t.Errorf("ответ %q: получено %+v, ожидалось supported=%v enabled=%v",
				tc.out, got, tc.wantSupported, tc.wantEnabled)
		}
	}
}

func TestSetAutostartВыбираетГлагол(t *testing.T) {
	orig := runSystemctl
	defer func() { runSystemctl = orig }()

	var gotArgs []string
	runSystemctl = func(args ...string) (string, error) {
		gotArgs = args
		return "", nil
	}

	if err := SetAutostart(true); err != nil {
		t.Fatalf("SetAutostart(true): %v", err)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "enable" || gotArgs[1] != systemdUnitName {
		t.Errorf("включение вызвало systemctl %v", gotArgs)
	}

	if err := SetAutostart(false); err != nil {
		t.Fatalf("SetAutostart(false): %v", err)
	}
	if gotArgs[0] != "disable" {
		t.Errorf("выключение вызвало systemctl %v", gotArgs)
	}
}

// Текст ошибки от systemctl должен доходить до человека: «не получилось» без
// причины на экране панели бесполезно.
func TestSetAutostartПереноситТекстОшибки(t *testing.T) {
	orig := runSystemctl
	defer func() { runSystemctl = orig }()
	runSystemctl = func(args ...string) (string, error) {
		return "Failed to enable unit: Unit file does not exist.", errors.New("exit status 1")
	}
	err := SetAutostart(true)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if want := "Unit file does not exist"; !contains(err.Error(), want) {
		t.Errorf("ошибка %q не содержит %q", err, want)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
