// Package config — настройки приложения и их хранение на диске, чтобы не
// вводить одну и ту же команду с флагами каждый раз.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

type Config struct {
	Host    string `json:"host"`
	SSHPort int    `json:"sshPort"`
	User    string `json:"user"`
	KeyPath string `json:"keyPath"`

	SocksPort int `json:"socksPort"`
	HTTPPort  int `json:"httpPort"`

	// PoolSize — сколько параллельных SSH-соединений держать. Одно соединение
	// упирается в одно TCP-окно и в один поток шифрования, поэтому несколько
	// заметно поднимают скорость на дальнем канале.
	PoolSize int `json:"poolSize"`

	// SysProxy — прописывать ли системный прокси Windows (реестр).
	SysProxy bool `json:"sysProxy"`
	// SetEnvVars — прописывать ли HTTPS_PROXY/HTTP_PROXY/NO_PROXY в переменные
	// среды пользователя. Нужно для программ на Node.js/Python/Go (Claude Code,
	// npm, pip, curl), которые системный прокси Windows не читают вовсе.
	SetEnvVars bool `json:"setEnvVars"`

	// Verbose — подробный лог (включая закрытие соединений).
	Verbose bool `json:"verbose"`
	// AutoStart — при запуске GUI сразу поднимать туннель.
	AutoStart bool `json:"autoStart"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		SSHPort:    22,
		User:       "root",
		KeyPath:    filepath.Join(home, ".ssh", "id_ed25519"),
		SocksPort:  1080,
		HTTPPort:   1081,
		PoolSize:   4,
		SysProxy:   true,
		SetEnvVars: true,
	}
}

// Dir — папка с настройками: %APPDATA%\vpstunnel на Windows,
// ~/.config/vpstunnel на остальных системах.
func Dir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "vpstunnel")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func KnownHostsPath() string { return filepath.Join(Dir(), "known_hosts") }

// Load читает конфиг с диска. Отсутствие файла — не ошибка: возвращаются
// значения по умолчанию.
func Load() Config {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg) // битый конфиг не должен ронять запуск
	cfg.normalize()
	return cfg
}

func (c *Config) Save() error {
	c.normalize()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o600)
}

// normalize чинит заведомо нерабочие значения, которые могли прийти из
// руками правленного JSON или из формы настроек.
func (c *Config) normalize() {
	if c.SSHPort <= 0 || c.SSHPort > 65535 {
		c.SSHPort = 22
	}
	if c.SocksPort <= 0 || c.SocksPort > 65535 {
		c.SocksPort = 1080
	}
	if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
		c.HTTPPort = 1081
	}
	if c.HTTPPort == c.SocksPort { // иначе второй слушатель не поднимется
		c.HTTPPort = c.SocksPort + 1
	}
	if c.PoolSize < 1 {
		c.PoolSize = 1
	}
	if c.PoolSize > 16 {
		c.PoolSize = 16
	}
	if c.User == "" {
		c.User = "root"
	}
	if c.KeyPath == "" {
		home, _ := os.UserHomeDir()
		c.KeyPath = filepath.Join(home, ".ssh", "id_ed25519")
	}
	if runtime.GOOS != "windows" {
		// На не-Windows системный прокси и переменные среды мы не трогаем.
		c.SysProxy = false
		c.SetEnvVars = false
	}
}
