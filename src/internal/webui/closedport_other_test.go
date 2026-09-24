//go:build !unix

package webui

import (
	"net"
	"testing"
)

// closedPort — свободный порт, который тут же закрыли. На unix — надёжнее,
// см. closedport_unix_test.go.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
