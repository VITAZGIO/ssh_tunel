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

// DeviceType — на чём стоит клиентское приложение. Влияет только на подпись
// и значок в списке клиентов панели.
type DeviceType string

const (
	DeviceWindows DeviceType = "windows"
	DeviceLinux   DeviceType = "linux"
	DeviceAndroid DeviceType = "android"
)

func validDeviceType(t DeviceType) bool {
	switch t {
	case DeviceWindows, DeviceLinux, DeviceAndroid:
		return true
	}
	return false
}

// ClientState — состояние клиента в панели. StateFrozen и StateDisabled
// заводятся здесь же, хотя переключают их только в ТЗ-09: состояние — часть
// формата хранения, и лучше сразу описать его целиком, чем потом раздвигать
// уже сохранённые файлы клиентов.
type ClientState string

const (
	// StateActive — обычный клиент: ключ в authorized_keys, подключается
	// когда захочет.
	StateActive ClientState = "active"
	// StateFrozen — ключ вынут из authorized_keys, живые сессии оборваны.
	// Отличие от StateDisabled чисто в намерении и в тексте на экране: обе
	// технически работают одинаково (нет ключа — не зайти), но замороженный
	// клиент явно «на паузе», а не временно недоступен.
	StateFrozen ClientState = "frozen"
	// StateDisabled — сессии оборваны, но ключ на месте: клиент может
	// подключиться заново сам, без участия панели.
	StateDisabled ClientState = "disabled"
)

// Client — один клиент панели: устройство со своим unix-пользователем.
type Client struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	DeviceType DeviceType  `json:"deviceType"`
	CreatedAt  time.Time   `json:"createdAt"`
	State      ClientState `json:"state"`

	// Username — unix-пользователь клиента, всегда равен ID (см.
	// username.go), хранится отдельным полем для наглядности файла на диске.
	Username string `json:"username"`

	// PublicKey — открытый ключ клиента в чистом виде ("ssh-ed25519 AAAA..."),
	// без опций restrict,port-forwarding и без комментария: они собираются
	// заново функцией AuthorizedKeysLine, когда нужно записать файл на
	// сервере.
	PublicKey string `json:"publicKey"`

	// PrivateKey хранится, пока клиента не удалили — иначе панели нечего
	// было бы показать по кнопке «Показать настройки» (ТЗ-10) в другой раз,
	// не при самом создании клиента. Права на файл хранилища — 0600
	// (см. NewClientStore), как и на файл пользователей панели.
	PrivateKey string `json:"privateKey"`
}

// ClientStore — клиенты панели одним JSON-файлом, рядом с users.json.
// Никакой базы данных: клиентов у одной панели предполагаются десятки, не
// миллионы, и то же решение уже принято для пользователей панели
// (store.go) и для настроек локальной программы (internal/config).
type ClientStore struct {
	path string

	mu      sync.Mutex
	clients map[string]Client
}

func OpenClientStore(path string) (*ClientStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("не могу создать папку для %s: %w", path, err)
	}
	s := &ClientStore{path: path, clients: map[string]Client{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return s, nil
	}
	var list []Client
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("не могу разобрать %s: %w", path, err)
	}
	for _, c := range list {
		s.clients[c.ID] = c
	}
	return s, nil
}

func (s *ClientStore) Get(id string) (Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	return c, ok
}

// List возвращает клиентов, отсортированных по дате создания — новые
// первыми, чтобы только что созданный клиент сразу оказывался наверху
// списка в панели.
func (s *ClientStore) List() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		list = append(list, c)
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].CreatedAt.After(list[j-1].CreatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return list
}

func (s *ClientStore) Put(c Client) error {
	s.mu.Lock()
	s.clients[c.ID] = c
	s.mu.Unlock()
	return s.save()
}

func (s *ClientStore) Delete(id string) error {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
	return s.save()
}

func (s *ClientStore) save() error {
	s.mu.Lock()
	list := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		list = append(list, c)
	}
	s.mu.Unlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
