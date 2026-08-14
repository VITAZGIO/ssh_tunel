package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Программа дважды меняла имя, и вместе с ним — папку настроек. Человек не
// должен из-за этого потерять адрес сервера и путь к ключу: старая папка
// переезжает под новое имя при первом запуске.
func TestMigrateFromOldNames(t *testing.T) {
	for _, oldName := range []string{"ssh_tunel", "vpstunnel"} {
		t.Run(oldName, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", base)
			t.Setenv("APPDATA", base)

			oldDir := filepath.Join(base, oldName)
			if err := os.MkdirAll(oldDir, 0o700); err != nil {
				t.Fatal(err)
			}
			want := `{"host":"203.0.113.10","user":"tunnel"}`
			if err := os.WriteFile(filepath.Join(oldDir, "config.json"), []byte(want), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg := Load()
			if cfg.Host != "203.0.113.10" {
				t.Errorf("адрес сервера потерялся при переезде из %q: %q", oldName, cfg.Host)
			}
			if cfg.User != "tunnel" {
				t.Errorf("пользователь потерялся при переезде из %q: %q", oldName, cfg.User)
			}
			if _, err := os.Stat(Dir()); err != nil {
				t.Errorf("новая папка не появилась: %v", err)
			}
		})
	}
}

// Если настройки уже лежат под новым именем, старую папку трогать нельзя.
func TestMigrateKeepsExisting(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("APPDATA", base)

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"host":"новый"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(base, "ssh_tunel")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "config.json"), []byte(`{"host":"старый"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if cfg := Load(); cfg.Host != "новый" {
		t.Errorf("старые настройки затёрли новые: %q", cfg.Host)
	}
}
