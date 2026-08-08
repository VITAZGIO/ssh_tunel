package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func testKey(t *testing.T) sshPublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := newPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func addr(t *testing.T) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", "87.58.210.143:22")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Первое подключение должно запомнить ключ, второе — принять его молча.
func TestLearnsThenVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testKey(t)

	learned := 0
	cb := Callback(path, func(string, string) { learned++ })

	if err := cb("87.58.210.143:22", addr(t), key); err != nil {
		t.Fatalf("первое подключение должно проходить: %v", err)
	}
	if learned != 1 {
		t.Fatalf("ключ должен был запомниться ровно один раз, было %d", learned)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл known_hosts не создан: %v", err)
	}

	if err := cb("87.58.210.143:22", addr(t), key); err != nil {
		t.Fatalf("повторное подключение с тем же ключом должно проходить: %v", err)
	}
	if learned != 1 {
		t.Fatalf("ключ запомнился повторно (%d) — значит проверка не работает", learned)
	}
}

// Подмена ключа — это либо переустановка сервера, либо перехват. Молча
// принимать нельзя: именно от этого защищает отказ от InsecureIgnoreHostKey.
func TestRejectsChangedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := Callback(path, nil)

	if err := cb("87.58.210.143:22", addr(t), testKey(t)); err != nil {
		t.Fatalf("первое подключение должно проходить: %v", err)
	}

	err := cb("87.58.210.143:22", addr(t), testKey(t)) // другой ключ
	if err == nil {
		t.Fatal("подмена ключа сервера прошла незамеченной")
	}
	var changed *ErrChanged
	if !errors.As(err, &changed) {
		t.Fatalf("ожидалась ошибка ErrChanged, получена %T: %v", err, err)
	}
	if changed.KnownHosts != path {
		t.Errorf("в сообщении должен быть путь к known_hosts, а там %q", changed.KnownHosts)
	}
}

// Другой сервер (другой адрес) должен запоминаться отдельно, а не считаться
// подменой.
func TestDifferentHostLearnedSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := Callback(path, nil)

	if err := cb("87.58.210.143:22", addr(t), testKey(t)); err != nil {
		t.Fatal(err)
	}
	other, _ := net.ResolveTCPAddr("tcp", "1.2.3.4:22")
	if err := cb("1.2.3.4:22", other, testKey(t)); err != nil {
		t.Fatalf("новый сервер должен просто запомниться: %v", err)
	}
}
