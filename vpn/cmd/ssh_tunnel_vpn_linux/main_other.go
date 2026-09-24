//go:build !linux

// Эта версия — только для Linux; для Windows есть ssh_tunnel_vpn.exe.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ssh_tunnel_vpn_linux работает только на Linux")
	os.Exit(1)
}
