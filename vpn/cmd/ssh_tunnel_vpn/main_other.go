//go:build !windows

// Режим VPN с адаптером Wintun есть только на Windows. На Linux и Android
// свои версии; этот файл нужен лишь для того, чтобы модуль собирался и
// проверялся везде.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ssh_tunnel_vpn работает только на Windows")
	os.Exit(1)
}
