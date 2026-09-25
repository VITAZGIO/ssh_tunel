package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Settings — настройки самой панели, которые меняются из интерфейса: ёмкость
// сервера и роль в сети устройств. Лежат рядом с пользователями и клиентами
// одним JSON-файлом.
type Settings struct {
	// MaxDevices — сколько устройств сервер должен выдерживать одновременно
	// (см. capacity.go). Ноль — значение по умолчанию.
	MaxDevices int `json:"maxDevices,omitempty"`

	Mesh MeshSettings `json:"mesh"`
}

// Роли сервера в сети устройств.
const (
	MeshRoleOff       = ""
	MeshRoleMain      = "main"
	MeshRoleSecondary = "secondary"
)

// MeshSettings — роль сервера в сети устройств (см. mesh.go).
type MeshSettings struct {
	// Role — "" (выключено), "main" или "secondary".
	Role string `json:"role,omitempty"`

	// Name — как этот сервер подписан на схеме сети и в ключе у главного.
	Name string `json:"name,omitempty"`

	// MainHost/MainPort — куда подключается побочный сервер (SSH главного),
	// LinkID — под каким id главный его знает.
	MainHost string `json:"mainHost,omitempty"`
	MainPort int    `json:"mainPort,omitempty"`
	LinkID   string `json:"linkId,omitempty"`

	// Host — адрес этого сервера, как его вводят устройства: по нему главный
	// узнаёт, какие устройства пришли через побочный сервер.
	Host string `json:"host,omitempty"`

	// Links — побочные серверы, которым главный разрешил подключаться.
	Links []MeshLink `json:"links,omitempty"`

	// ServerAccessOff — не пускать устройства сети к самому этому серверу
	// (по умолчанию пускать: у сервера свой адрес и имя .mesh, соединение на
	// них ведёт на его 127.0.0.1:ПОРТ). ServerPorts — какие порты открыты
	// ("22,80,8000-8100"; пусто — все).
	ServerAccessOff bool   `json:"serverAccessOff,omitempty"`
	ServerPorts     string `json:"serverPorts,omitempty"`
}

// MeshLink — побочный сервер, подключённый к главному.
type MeshLink struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PublicKey string    `json:"publicKey"`
	Added     time.Time `json:"added"`
}

// SettingsStore — Settings на диске.
type SettingsStore struct {
	path string

	mu sync.Mutex
	s  Settings
}

// OpenSettings читает настройки из path. Файла ещё нет — пустые настройки,
// то есть значения по умолчанию.
func OpenSettings(path string) (*SettingsStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("не могу создать папку для %s: %w", path, err)
	}
	st := &SettingsStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, &st.s); err != nil {
		return nil, fmt.Errorf("не могу разобрать %s: %w", path, err)
	}
	return st, nil
}

// Get — копия текущих настроек.
func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.s
	s.Mesh.Links = append([]MeshLink(nil), st.s.Mesh.Links...)
	return s
}

// Update меняет настройки и сразу пишет их на диск. Ошибка fn — ничего не
// меняется.
func (st *SettingsStore) Update(fn func(*Settings) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.s
	next.Mesh.Links = append([]MeshLink(nil), st.s.Mesh.Links...)
	if err := fn(&next); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.s = next
	return nil
}
