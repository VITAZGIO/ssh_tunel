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

	// FilterMode и FilterApps — какие программы пускать через туннель:
	//   all    — все;
	//   only   — только перечисленные, остальные напрямую;
	//   except — все, кроме перечисленных.
	FilterMode string   `json:"filterMode"`
	FilterApps []string `json:"filterApps"`

	// DirectHosts — адреса, сети и имена, которые всегда идут напрямую,
	// помимо встроенного списка локальных диапазонов. Сюда вписывают чужие
	// сети: mesh-VPN, рабочий VPN, самодельный WireGuard.
	DirectHosts []string `json:"directHosts"`

	// LocalViaTunnel — вести ли локальную сеть (192.168.x.x, домашние имена)
	// через сервер. По умолчанию false: такие адреса идут напрямую, иначе
	// домашние сервисы становятся недоступны при включённом туннеле.
	// Включать имеет смысл только чтобы дотянуться до внутренней сети самого
	// сервера.
	LocalViaTunnel bool `json:"localViaTunnel"`

	// Verbose — подробный лог (включая закрытие соединений).
	Verbose bool `json:"verbose"`
	// AutoStart — при запуске GUI сразу поднимать туннель.
	AutoStart bool `json:"autoStart"`
}

func Default() Config {
	return Config{
		SSHPort: 22,
		// Не root: у пользователя, заведённого только для туннеля, нет прав
		// ни на что, кроме проброса соединений. Если такого пользователя на
		// сервере нет, его создаст команда из подсказки у поля с ключом.
		User:       "tunnel",
		KeyPath:    DetectKeyPath(),
		SocksPort:  1080,
		HTTPPort:   1081,
		PoolSize:   4,
		SysProxy:   true,
		SetEnvVars: true,
		FilterMode: "all",
	}
}

// Dir — папка с настройками: %APPDATA%\ssh_tunnel на Windows,
// ~/.config/ssh_tunnel на остальных системах.
func Dir() string {
	return filepath.Join(baseDir(), "ssh_tunnel")
}

func baseDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return base
}

// migrateOldDir переносит настройки из папки прежнего названия. Без этого
// после переименования программы человек потерял бы адрес сервера и путь к
// ключу и решил бы, что всё сломалось.
func migrateOldDir() {
	newDir := Dir()
	if _, err := os.Stat(newDir); err == nil {
		return // уже переехали
	}
	// Программа успела дважды сменить имя: vpstunnel -> ssh_tunel -> ssh_tunnel.
	// Настройки надо подобрать от любого из прежних, начиная с самого свежего.
	for _, old := range []string{"ssh_tunel", "vpstunnel"} {
		oldDir := filepath.Join(baseDir(), old)
		if _, err := os.Stat(oldDir); err != nil {
			continue
		}
		if os.Rename(oldDir, newDir) == nil {
			return
		}
	}
}

func Path() string { return filepath.Join(Dir(), "config.json") }

// DetectKeyPath ищет ключ SSH в домашней папке ТЕКУЩЕГО пользователя.
//
// Путь не зашит и не угадывается по имени: домашняя папка берётся у системы,
// поэтому на любом компьютере и под любым пользователем он свой. Из
// стандартных имён берётся первое существующее — у людей встречаются и
// ed25519, и ecdsa, и старый rsa.
func DetectKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".ssh")
	names := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	// Ничего нет — показываем путь, по которому ключ появится после создания.
	return filepath.Join(dir, "id_ed25519")
}

func KnownHostsPath() string { return filepath.Join(Dir(), "known_hosts") }

// Load читает конфиг с диска. Отсутствие файла — не ошибка: возвращаются
// значения по умолчанию.
func Load() Config {
	migrateOldDir()
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
		c.KeyPath = DetectKeyPath()
	}
	switch c.FilterMode {
	case "only", "except":
	default:
		c.FilterMode = "all"
	}
	// На системах, где трогать общесистемные настройки нечем (macOS и прочее),
	// оставлять эти галочки включёнными бессмысленно.
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		c.SysProxy = false
		c.SetEnvVars = false
	}
}
