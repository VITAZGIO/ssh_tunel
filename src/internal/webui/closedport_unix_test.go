//go:build unix

package webui

import (
	"syscall"
	"testing"
)

// closedPort — порт на 127.0.0.1, который гарантированно отвечает отказом:
// сокет к нему привязан, но не слушает. Занятый так порт не достанется
// никому другому — в отличие от «взять свободный и закрыть», когда параллельно
// идущие тесты других пакетов успевали занять его своим сервером, и вместо
// отказа приходил тайм-аут.
func closedPort(t *testing.T) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return sa.(*syscall.SockaddrInet4).Port
}
