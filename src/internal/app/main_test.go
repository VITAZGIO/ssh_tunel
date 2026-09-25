package app

import (
	"os"
	"testing"
)

// TestMain уводит папку настроек во временную. Тесты поднимают SSH-серверы на
// случайных портах, и программа запоминает их ключи в known_hosts из папки
// настроек. С настоящей папкой ключ сервера из прошлого прогона на том же
// порту выглядел бы подменой — тест падал бы через раз, — а заодно тесты
// засоряли бы настройки человека, который их запускает.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ssh_tunnel-app-test")
	if err != nil {
		panic(err)
	}
	// os.UserConfigDir: XDG_CONFIG_HOME на Linux, AppData на Windows, HOME на
	// macOS.
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("AppData", dir)
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
