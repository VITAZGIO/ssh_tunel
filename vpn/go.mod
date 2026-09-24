// Режим VPN для Windows (ssh_tunnel_vpn.exe) — отдельным модулем, как и ядро
// Android: ему нужны сетевой стек gvisor, драйвер Wintun и Go 1.26, а обычному
// ssh_tunnel.exe всё это ни к чему. Путь начинается с sshtunnel/, поэтому
// пакеты internal/ основного модуля отсюда доступны.
module sshtunnel/vpn

go 1.26.3

replace sshtunnel => ../src

replace sshtunnel/android/core => ../android/core

require (
	github.com/vishvananda/netlink v1.3.1
	github.com/xjasonlyu/tun2socks/v2 v2.7.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	golang.zx2c4.com/wireguard/windows v1.1.1
	sshtunnel v0.0.0
	sshtunnel/android/core v0.0.0-00010101000000-000000000000
)

require (
	github.com/google/btree v1.1.3 // indirect
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20260701204157-69c2d17aea96 // indirect
)
