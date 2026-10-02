//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestNeedsRoot(t *testing.T) {
	for args, want := range map[string]bool{
		"-web -web-lan":                    true,
		"":                                 true,
		"-host x -user tunnel -save":       false,
		"-env":                             false,
		"--help":                           false,
		"-h":                               false,
		"-web-listen 127.0.0.1:47821 -web": true,
	} {
		if got := needsRoot(strings.Fields(args)); got != want {
			t.Errorf("needsRoot(%q) = %v, ожидалось %v", args, got, want)
		}
	}
}

// Служба поднимает панель с теми же флагами, что и запущенная программа, —
// и только с ними: адрес сервера и прочее лежат в настройках.
func TestWebFlags(t *testing.T) {
	cases := map[string][]string{
		"-web -web-lan":                         {"-web", "-web-lan"},
		"--web":                                 {"-web"},
		"-web -web-listen 0.0.0.0:9000 -host x": {"-web", "-web-listen", "0.0.0.0:9000"},
		"-web-listen=127.0.0.1:1 -web":          {"-web-listen=127.0.0.1:1", "-web"},
		"-host x":                               {"-web"},
	}
	for in, want := range cases {
		if got := webFlags(strings.Fields(in)); !reflect.DeepEqual(got, want) {
			t.Errorf("webFlags(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestVPNUnitText(t *testing.T) {
	u := vpnUnitText("/usr/local/bin/ssh_tunnel_vpn_linux", []string{"-web", "-web-lan"}, "XDG_CONFIG_HOME=/home/vitaz/.config")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/ssh_tunnel_vpn_linux -web -web-lan\n",
		"Environment=XDG_CONFIG_HOME=/home/vitaz/.config\n",
		"WantedBy=multi-user.target", // системная, не пользовательская
		"Restart=always",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("в службе нет %q:\n%s", want, u)
		}
	}
	if q := vpnUnitText("/opt/my dir/x", nil, "HOME=/root"); !strings.Contains(q, `ExecStart="/opt/my dir/x"`) {
		t.Errorf("путь с пробелом без кавычек:\n%s", q)
	}
}

// ~/.config, созданная прежними версиями от root, возвращается
// пользователю; чужие папки внутри не трогаются.
func TestOwnBase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("нужен root, чтобы менять владельца")
	}
	base := filepath.Join(t.TempDir(), ".config")
	if err := os.MkdirAll(filepath.Join(base, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	ownBase(base, 1000, 1000)
	owner := func(p string) uint32 {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Sys().(*syscall.Stat_t).Uid
	}
	if owner(base) != 1000 {
		t.Fatal("~/.config осталась за root")
	}
	if owner(filepath.Join(base, "other")) != 0 {
		t.Fatal("ownBase полезла внутрь ~/.config")
	}
	// Ещё не существующая — создаётся сразу пользовательской.
	fresh := filepath.Join(t.TempDir(), "new", ".config")
	ownBase(fresh, 1000, 1000)
	if owner(fresh) != 1000 {
		t.Fatal("новая ~/.config создана от root")
	}
}
