//go:build windows

package main

import "golang.org/x/sys/windows"

// showMessage показывает обычное окно с сообщением. Без него ошибка запуска в
// версии с -H windowsgui была бы вообще не видна: консоли нет, писать некуда.
func showMessage(msg string) {
	title, _ := windows.UTF16PtrFromString("ssh_tunnel")
	text, _ := windows.UTF16PtrFromString(msg)
	const mbIconError = 0x00000010
	windows.MessageBox(0, text, title, mbIconError)
}
