package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// openVPNServiceWindow — на компьютере работает служба VPN-версии
// (ssh_tunnel_vpn, галочка «Запускать при старте системы» там): туннель уже
// поднят ею. Второй — прокси поверх VPN — только мешал бы, поэтому вместо
// себя открываем окно VPN-версии. true — открыли, этому процессу делать
// нечего.
func openVPNServiceWindow() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString("ssh_tunnel_vpn")
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil || st.CurrentState != uint32(svc.Running) {
		return false
	}
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	exe := filepath.Join(pf, "ssh_tunnel", "ssh_tunnel_vpn.exe")
	if _, err := os.Stat(exe); err != nil {
		return false
	}
	return exec.Command(exe).Start() == nil
}
