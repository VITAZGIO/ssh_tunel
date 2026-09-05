package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureKeyCreatesThenReuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "id_ed25519")

	pub1, created, err := ensureKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("первый вызов должен был создать ключ")
	}
	if !strings.HasPrefix(pub1, "ssh-ed25519 ") {
		t.Errorf("неожиданный формат открытого ключа: %q", pub1)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("приватный ключ: права %v, ошибка %v", st, err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	pub2, created, err := ensureKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("второй вызов не должен пересоздавать ключ")
	}
	if pub2 != pub1 {
		t.Errorf("открытый ключ поменялся между вызовами: %q != %q", pub1, pub2)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("содержимое приватного ключа изменилось при повторном вызове")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("нет домашней папки в этом окружении")
	}
	got, err := expandHome("~/.ssh/id_ed25519")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".ssh", "id_ed25519")
	if got != want {
		t.Errorf("expandHome = %q, ожидалось %q", got, want)
	}
	if got, _ := expandHome("/absolute/path"); got != "/absolute/path" {
		t.Errorf("абсолютный путь не должен меняться: %q", got)
	}
}
