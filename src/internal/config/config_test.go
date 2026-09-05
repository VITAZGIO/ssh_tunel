package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Старые конфиги, сохранённые до появления AutoConnect, не должны молча
// переставать подключаться сами: JSON без этого ключа даёт AutoConnect == nil,
// а это должно читаться как «включено», а не как «выключено».
func TestAutoConnectEnabledDefaultsWhenUnset(t *testing.T) {
	var cfg Config
	if !cfg.AutoConnectEnabled() {
		t.Error("AutoConnect не задан явно — должен считаться включённым")
	}

	on, off := true, false
	cfg.AutoConnect = &on
	if !cfg.AutoConnectEnabled() {
		t.Error("AutoConnect = true должен давать true")
	}
	cfg.AutoConnect = &off
	if cfg.AutoConnectEnabled() {
		t.Error("AutoConnect = false должен давать false")
	}
}

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
			active := cfg.Active()
			if active.Host != "203.0.113.10" {
				t.Errorf("адрес сервера потерялся при переезде из %q: %q", oldName, active.Host)
			}
			if active.User != "tunnel" {
				t.Errorf("пользователь потерялся при переезде из %q: %q", oldName, active.User)
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

	if cfg := Load(); cfg.Active().Host != "новый" {
		t.Errorf("старые настройки затёрли новые: %q", cfg.Active().Host)
	}
}

// Файл, сохранённый до появления нескольких серверов, — плоский, без ключа
// "profiles". Он должен превратиться в единственный профиль, а не потеряться.
func TestMigrateLegacyToSingleProfile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("APPDATA", base)

	legacy := `{"host":"203.0.113.10","user":"tunnel","sshPort":2222,"poolSize":6,"verbose":true}`
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if len(cfg.Profiles) != 1 {
		t.Fatalf("ожидался один профиль, получилось %d", len(cfg.Profiles))
	}
	active := cfg.Active()
	if active.Host != "203.0.113.10" || active.SSHPort != 2222 || active.PoolSize != 6 {
		t.Errorf("данные сервера потерялись при миграции: %+v", active)
	}
	if !cfg.Verbose {
		t.Error("настройка verbose (общая для программы) потерялась при миграции")
	}
	if cfg.ActiveProfile != active.ID {
		t.Error("ActiveProfile не указывает на мигрировавший профиль")
	}
}

// Добавление и удаление серверов: список не должен опустеть, а активным
// после удаления текущего должен становиться кто-то из оставшихся.
func TestAddRemoveProfile(t *testing.T) {
	cfg := Default()
	first := cfg.Profiles[0]

	second := cfg.AddProfile("Амстердам", "🇳🇱")
	if len(cfg.Profiles) != 2 {
		t.Fatalf("ожидалось 2 профиля, получилось %d", len(cfg.Profiles))
	}
	if second.Name != "Амстердам" || second.Flag != "🇳🇱" {
		t.Errorf("новый профиль собрался неправильно: %+v", second)
	}

	cfg.ActiveProfile = second.ID
	if !cfg.RemoveProfile(second.ID) {
		t.Fatal("не удалось удалить второй профиль")
	}
	if cfg.ActiveProfile != first.ID {
		t.Errorf("после удаления активного профиля активным должен стать оставшийся: %q != %q",
			cfg.ActiveProfile, first.ID)
	}
	if cfg.RemoveProfile(first.ID) {
		t.Error("последний профиль удалять нельзя — подключаться будет не к чему")
	}
}
