package webui

import (
	"strings"
	"testing"
)

func TestVPSScriptSubstitution(t *testing.T) {
	out := vpsScript("admin2", "2222", "Europe/Amsterdam", "tun", "ssh-ed25519 AAAAtest key")

	for _, want := range []string{
		`NEW_USER="admin2"`,
		"SSH_PORT=2222",
		`TIMEZONE="Europe/Amsterdam"`,
		`TUNNEL_USER="tun"`,
		`TUNNEL_KEY="ssh-ed25519 AAAAtest key"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated script", want)
		}
	}
	for _, mustNotHave := range []string{
		`NEW_USER="admin"`,
		"SSH_PORT=22\n",
		`TIMEZONE="Europe/Moscow"`,
		`TUNNEL_USER="tunnel"`,
		`TUNNEL_KEY=""`,
	} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("default value %q was not replaced", mustNotHave)
		}
	}
	if !strings.Contains(out, "bash /root/harden.sh") || !strings.Contains(out, "bash /root/tunnel-user.sh") {
		t.Error("both scripts should run at the end of their block")
	}
}
