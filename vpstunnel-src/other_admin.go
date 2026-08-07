//go:build !windows

package main

import "fmt"

func relaunchElevatedImpl() error {
	return fmt.Errorf("повышение прав поддерживается только на Windows")
}

func setWindowsSystemProxyImpl(socksAddr string) error {
	return fmt.Errorf("системный прокси Windows недоступен на этой ОС")
}

func clearWindowsSystemProxyImpl() error {
	return fmt.Errorf("системный прокси Windows недоступен на этой ОС")
}
