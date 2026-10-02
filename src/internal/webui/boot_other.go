//go:build !linux && !windows

// Автозапуск при старте системы поддержан только на Linux (systemd) и
// Windows (реестр) — на остальных системах панель просто прячет галочку.
package webui

func platformBootSupported() bool                                        { return false }
func platformBootEnabled() bool                                          { return false }
func platformBootLinger() bool                                           { return false }
func platformUnitPath() string                                           { return "" }
func platformSetBoot(enable bool, password string, flags []string) error { return nil }
