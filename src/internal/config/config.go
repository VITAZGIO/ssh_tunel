// Package config — настройки приложения и их хранение на диске, чтобы не
// вводить одну и ту же команду с флагами каждый раз.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Profile — один сервер: всё, что относится именно к нему, а не к программе
// в целом. Их может быть несколько — как вкладки в браузере, — и переключение
// между ними не трогает язык интерфейса или общие галочки автозапуска.
type Profile struct {
	ID string `json:"id"`
	// Name — подпись на вкладке. Либо название выбранного из списка города
	// («Амстердам»), либо то, что человек вписал сам.
	Name string `json:"name"`
	// Flag — эмодзи флага страны у выбранного города. Пусто — показывать
	// вместо флага логотип программы: так помечен сервер с произвольным
	// именем, для которого страну никто не выбирал.
	Flag string `json:"flag,omitempty"`

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
	LocalViaTunnel bool `json:"localViaTunnel"`

	// Panel/ClientID/DeviceName заполняются только при импорте конфига,
	// выданного веб-панелью на VPS (internal/share, поля версии 2 формата
	// обмена). У сервера, настроенного руками, Panel остаётся пустым — по
	// нему экран настроек решает, показывать ли строку «Этот сервер выдан
	// панелью».
	Panel      string `json:"panel,omitempty"`
	ClientID   string `json:"clientId,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
}

// Config — вся программа целиком: список серверов и настройки, общие для
// всех них (язык интерфейса, автозапуск, системный прокси).
type Config struct {
	// Profiles — серверы, между которыми можно переключаться. Пустым не
	// бывает: normalize() следит, чтобы в нём был хотя бы один.
	Profiles []Profile `json:"profiles"`
	// ActiveProfile — id сервера, который сейчас используется для
	// подключения. Переключение между вкладками в настройках его не меняет —
	// только нажатие «Выбрать этот сервер».
	ActiveProfile string `json:"activeProfile"`

	// SysProxy — прописывать ли системный прокси Windows (реестр).
	SysProxy bool `json:"sysProxy"`
	// SetEnvVars — прописывать ли HTTPS_PROXY/HTTP_PROXY/NO_PROXY в переменные
	// среды пользователя. Нужно для программ на Node.js/Python/Go (Claude Code,
	// npm, pip, curl), которые системный прокси Windows не читают вовсе.
	SetEnvVars bool `json:"setEnvVars"`

	// Verbose — подробный лог (включая закрытие соединений).
	Verbose bool `json:"verbose"`
	// AutoStart — при запуске GUI (Windows) сразу поднимать туннель, без
	// нажатия на кнопку. К консольной версии для Linux отношения не имеет —
	// там за это отвечает AutoConnect.
	AutoStart bool `json:"autoStart"`

	// AutoConnect — соединяться сразу при запуске программы (ssh_tunnel_linux
	// с флагом -web): без этого пришлось бы каждый раз самому нажимать
	// «Подключить» в панели после перезапуска. Указатель, а не bool: nil
	// означает «не задано явно», и тогда действует прежнее поведение —
	// программа подключается сразу, как было до появления этой настройки.
	AutoConnect *bool `json:"autoConnect,omitempty"`

	// Language — язык интерфейса панели: "ru" или "en".
	Language string `json:"language"`
	// ShowServerPicker — показывать ли на главном экране виджет с текущим
	// сервером и быстрым переключением. Имеет смысл только когда серверов
	// больше одного — при одном сервере поле хранится, но панель его прячет.
	ShowServerPicker bool `json:"showServerPicker"`
}

// AutoConnectEnabled — эффективное значение AutoConnect с учётом отсутствия
// настройки в старых конфигах (см. комментарий у поля).
func (c Config) AutoConnectEnabled() bool {
	return c.AutoConnect == nil || *c.AutoConnect
}

// Active — сервер, который сейчас используется для подключения. Если
// ActiveProfile почему-то не указывает ни на один известный профиль (файл
// правили руками, id устарел), возвращается первый — так подключаться есть
// куда, а не в никуда.
func (c Config) Active() Profile {
	for _, p := range c.Profiles {
		if p.ID == c.ActiveProfile {
			return p
		}
	}
	if len(c.Profiles) > 0 {
		return c.Profiles[0]
	}
	return defaultProfile(1)
}

// ProfileByID ищет профиль по id.
func (c Config) ProfileByID(id string) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// SetProfile заменяет профиль с тем же id (по ключу p.ID). Профиля с таким id
// нет — ничего не делает: добавление нового сервера идёт через AddProfile, а
// не через подмену несуществующего.
func (c *Config) SetProfile(p Profile) {
	for i := range c.Profiles {
		if c.Profiles[i].ID == p.ID {
			c.Profiles[i] = p
			return
		}
	}
}

// AddProfile добавляет новый сервер и возвращает его.
func (c *Config) AddProfile(name, flag string) Profile {
	p := defaultProfile(len(c.Profiles) + 1)
	if name != "" {
		p.Name = name
	}
	p.Flag = flag
	c.Profiles = append(c.Profiles, p)
	return p
}

// RemoveProfile удаляет сервер. Последний удалить нельзя — программе всегда
// нужен хотя бы один, иначе подключаться попросту не к чему. Если удаляли
// активный, активным становится первый из оставшихся.
func (c *Config) RemoveProfile(id string) bool {
	if len(c.Profiles) <= 1 {
		return false
	}
	out := c.Profiles[:0]
	for _, p := range c.Profiles {
		if p.ID != id {
			out = append(out, p)
		}
	}
	if len(out) == len(c.Profiles) {
		return false // такого id и не было
	}
	c.Profiles = out
	if c.ActiveProfile == id {
		c.ActiveProfile = c.Profiles[0].ID
	}
	return true
}

func Default() Config {
	p := defaultProfile(1)
	return Config{
		Profiles:      []Profile{p},
		ActiveProfile: p.ID,
		SysProxy:      true,
		SetEnvVars:    true,
		Language:      "ru",
	}
}

func defaultProfile(n int) Profile {
	name := "Сервер " + itoa(n)
	return Profile{
		ID:   newProfileID(),
		Name: name,
		// Не root: у пользователя, заведённого только для туннеля, нет прав
		// ни на что, кроме проброса соединений. Если такого пользователя на
		// сервере нет, его создаст команда из подсказки у поля с ключом.
		User:       "tunnel",
		KeyPath:    DetectKeyPath(),
		SSHPort:    22,
		SocksPort:  1080,
		HTTPPort:   1081,
		PoolSize:   4,
		FilterMode: "all",
	}
}

func newProfileID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand практически никогда не подводит, но id обязан быть хоть
		// каким-то: при сбое берём то, что успело записаться (нули), это не
		// нарушит уникальность в пределах одного процесса.
	}
	return "p_" + hex.EncodeToString(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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

// legacyConfig — формат файла настроек до появления нескольких серверов:
// один плоский набор полей вместо списка профилей. Нужен только для чтения
// старых config.json при первом запуске новой версии.
type legacyConfig struct {
	Host           string   `json:"host"`
	SSHPort        int      `json:"sshPort"`
	User           string   `json:"user"`
	KeyPath        string   `json:"keyPath"`
	SocksPort      int      `json:"socksPort"`
	HTTPPort       int      `json:"httpPort"`
	PoolSize       int      `json:"poolSize"`
	SysProxy       bool     `json:"sysProxy"`
	SetEnvVars     bool     `json:"setEnvVars"`
	FilterMode     string   `json:"filterMode"`
	FilterApps     []string `json:"filterApps"`
	DirectHosts    []string `json:"directHosts"`
	LocalViaTunnel bool     `json:"localViaTunnel"`
	Verbose        bool     `json:"verbose"`
	AutoStart      bool     `json:"autoStart"`
	AutoConnect    *bool    `json:"autoConnect,omitempty"`
}

// migrateLegacy собирает Config из файла старого формата: одного сервера
// хватало на всю программу, теперь он становится первым (и единственным)
// профилем, а настройки самой программы переезжают как были.
func migrateLegacy(data []byte) Config {
	var lc legacyConfig
	_ = json.Unmarshal(data, &lc)
	p := defaultProfile(1)
	p.Host, p.User, p.KeyPath = lc.Host, lc.User, lc.KeyPath
	p.SSHPort, p.SocksPort, p.HTTPPort, p.PoolSize = lc.SSHPort, lc.SocksPort, lc.HTTPPort, lc.PoolSize
	p.FilterMode, p.FilterApps = lc.FilterMode, lc.FilterApps
	p.DirectHosts, p.LocalViaTunnel = lc.DirectHosts, lc.LocalViaTunnel

	return Config{
		Profiles:      []Profile{p},
		ActiveProfile: p.ID,
		SysProxy:      lc.SysProxy,
		SetEnvVars:    lc.SetEnvVars,
		Verbose:       lc.Verbose,
		AutoStart:     lc.AutoStart,
		AutoConnect:   lc.AutoConnect,
		Language:      "ru",
	}
}

// Load читает конфиг с диска. Отсутствие файла — не ошибка: возвращаются
// значения по умолчанию.
func Load() Config {
	migrateOldDir()
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}

	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return cfg // битый конфиг не должен ронять запуск
	}
	if _, hasProfiles := raw["profiles"]; hasProfiles {
		_ = json.Unmarshal(data, &cfg)
	} else {
		cfg = migrateLegacy(data)
	}
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
	if len(c.Profiles) == 0 {
		c.Profiles = []Profile{defaultProfile(1)}
	}
	for i := range c.Profiles {
		c.Profiles[i].normalize(i + 1)
	}
	if _, ok := c.ProfileByID(c.ActiveProfile); !ok {
		c.ActiveProfile = c.Profiles[0].ID
	}
	if c.Language != "en" {
		c.Language = "ru"
	}
	// На системах, где трогать общесистемные настройки нечем (macOS и прочее),
	// оставлять эти галочки включёнными бессмысленно.
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		c.SysProxy = false
		c.SetEnvVars = false
	}
}

func (p *Profile) normalize(n int) {
	if p.ID == "" {
		p.ID = newProfileID()
	}
	if p.Name == "" {
		p.Name = "Сервер " + itoa(n)
	}
	if p.SSHPort <= 0 || p.SSHPort > 65535 {
		p.SSHPort = 22
	}
	if p.SocksPort <= 0 || p.SocksPort > 65535 {
		p.SocksPort = 1080
	}
	if p.HTTPPort <= 0 || p.HTTPPort > 65535 {
		p.HTTPPort = 1081
	}
	if p.HTTPPort == p.SocksPort { // иначе второй слушатель не поднимется
		p.HTTPPort = p.SocksPort + 1
	}
	if p.PoolSize < 1 {
		p.PoolSize = 1
	}
	if p.PoolSize > 16 {
		p.PoolSize = 16
	}
	if p.User == "" {
		p.User = "root"
	}
	if p.KeyPath == "" {
		p.KeyPath = DetectKeyPath()
	}
	switch p.FilterMode {
	case "only", "except":
	default:
		p.FilterMode = "all"
	}
}
