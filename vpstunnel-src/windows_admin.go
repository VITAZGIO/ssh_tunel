//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW     = shell32.NewProc("ShellExecuteW")
)

// relaunchElevatedImpl перезапускает текущий exe с запросом UAC ("runas"),
// передавая те же аргументы командной строки плюс -elevated (просто маркер,
// на логику не влияет, оставлен для будущей отладки).
func relaunchElevatedImpl() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := strings.Join(os.Args[1:], " ")

	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(args)
	cwd, _ := syscall.UTF16PtrFromString("")

	// SW_SHOWNORMAL = 1
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(cwd)),
		1,
	)
	// ShellExecute возвращает значение >32 при успехе.
	if ret <= 32 {
		return fmt.Errorf("ShellExecute вернул код %d (пользователь отклонил запрос UAC?)", ret)
	}
	return nil
}

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// setWindowsSystemProxyImpl прописывает системный SOCKS-прокси в реестр
// (тот же эффект, что и ручная настройка через Панель управления -> Свойства
// обозревателя -> Подключения -> Настройка сети -> Дополнительно -> Sockets).
// Работает с текущим пользователем (HKCU) — не требует перезапуска explorer.
func setWindowsSystemProxyImpl(socksAddr string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	// ProxyServer в формате "socks=host:port" — задействует ТОЛЬКО Socks-поле,
	// HTTP/HTTPS/FTP остаются пустыми (то, из-за чего мы вручную мучились —
	// галка "один прокси для всех протоколов" тут просто не выставляется).
	proxyValue := "socks=" + socksAddr
	if err := k.SetStringValue("ProxyServer", proxyValue); err != nil {
		return err
	}
	// Пустая строка исключений — ничего не обходит прокси намеренно.
	_ = k.SetStringValue("ProxyOverride", "")

	notifySettingsChanged()
	return nil
}

func clearWindowsSystemProxyImpl() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	notifySettingsChanged()
	return nil
}

// notifySettingsChanged говорит Windows/Edge/Chrome перечитать настройки
// прокси немедленно, без ручного перезапуска браузера.
func notifySettingsChanged() {
	wininet := syscall.NewLazyDLL("wininet.dll")
	proc := wininet.NewProc("InternetSetOptionW")
	const INTERNET_OPTION_SETTINGS_CHANGED = 39
	const INTERNET_OPTION_REFRESH = 37
	proc.Call(0, INTERNET_OPTION_SETTINGS_CHANGED, 0, 0)
	proc.Call(0, INTERNET_OPTION_REFRESH, 0, 0)
}
