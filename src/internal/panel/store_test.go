package panel

import (
	"path/filepath"
	"testing"
)

func TestStoreCreateAndVerify(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Empty() {
		t.Fatal("новое хранилище должно быть пустым")
	}
	if err := s.CreateWithPassword("admin", "hunter22", true); err != nil {
		t.Fatal(err)
	}
	if s.Empty() {
		t.Fatal("после создания пользователя хранилище не должно быть пустым")
	}

	if _, ok := s.Verify("admin", "wrong"); ok {
		t.Fatal("неверный пароль не должен проходить")
	}
	u, ok := s.Verify("Admin", "hunter22")
	if !ok {
		t.Fatal("верный пароль должен проходить, логин без учёта регистра")
	}
	if !u.MustChangePassword {
		t.Fatal("свежесозданный пользователь должен требовать смены пароля")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")

	s1, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.CreateWithPassword("admin", "firstpass", true); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Empty() {
		t.Fatal("пользователь должен пережить перезапуск")
	}
	if _, ok := s2.Verify("admin", "firstpass"); !ok {
		t.Fatal("пароль должен читаться после перезапуска")
	}
}

func TestStoreSetPasswordClearsMustChange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWithPassword("admin", "onetime", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("admin", "mynewpassword"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("admin", "onetime"); ok {
		t.Fatal("старый пароль не должен работать после смены")
	}
	u, ok := s.Verify("admin", "mynewpassword")
	if !ok {
		t.Fatal("новый пароль должен работать")
	}
	if u.MustChangePassword {
		t.Fatal("после смены пароля флаг обязательной смены должен сняться")
	}
}

func TestGenerateOnePasswordIsRandomAndLong(t *testing.T) {
	a, err := GenerateOnePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateOnePassword()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("два вызова не должны совпасть")
	}
	if len(a) < 12 {
		t.Fatalf("пароль слишком короткий: %q", a)
	}
}
