// Package panel — веб-панель, которая ставится НА сам VPS и управляет
// подключёнными к нему клиентами. В отличие от internal/webui (окно на
// компьютере пользователя, доступное только с петлевого адреса), эта панель
// смотрит в интернет, поэтому вход защищён логином и паролем, а не токеном в
// адресе.
package panel

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// User — учётная запись входа в панель. Пароль хранится только хешем —
// bcrypt сам добавляет соль и медленно проверяет, что и нужно от пароля,
// который смотрит в интернет.
type User struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
	// MustChangePassword — стоит после первого запуска, когда пароль
	// сгенерирован программой и напечатан в журнал. Снимается после того, как
	// человек задаст свой пароль через /api/change-password.
	MustChangePassword bool `json:"mustChangePassword"`
}

// Store — пользователи панели, на диске одним JSON-файлом. Панель работает
// от root, поэтому файл может лежать в системной папке и не быть доступным
// на чтение никому, кроме root.
type Store struct {
	path string

	mu    sync.Mutex
	users map[string]User
}

// OpenStore читает пользователей из path, создавая пустой файл, если его ещё
// нет. dir создаётся с правами 0700, файл — с правами 0600: внутри лежат
// хеши паролей, и права должны быть строже, чем у обычных настроек.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("не могу создать папку для %s: %w", path, err)
	}
	s := &Store{path: path, users: map[string]User{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	var list []User
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("не могу разобрать %s: %w", path, err)
	}
	for _, u := range list {
		s.users[strings.ToLower(u.Username)] = u
	}
	return s, nil
}

// Empty — правда, если пользователей ещё нет. По этому признаку программа
// при первом запуске заводит учётку с одноразовым паролем.
func (s *Store) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users) == 0
}

// Verify сверяет пароль. Возвращает пользователя и true, если пароль верный.
// Само по себе Verify не защищает от перебора — это забота вызывающего кода
// (см. loginLimiter), здесь только сравнение хеша.
func (s *Store) Verify(username, password string) (User, bool) {
	s.mu.Lock()
	u, ok := s.users[strings.ToLower(username)]
	s.mu.Unlock()
	if !ok {
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

// CreateWithPassword заводит пользователя с уже готовым паролем. Используется
// один раз — при первом запуске, когда программа сама придумывает одноразовый
// пароль.
func (s *Store) CreateWithPassword(username, password string, mustChange bool) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.users[strings.ToLower(username)] = User{
		Username:           username,
		Hash:               string(hash),
		MustChangePassword: mustChange,
	}
	s.mu.Unlock()
	return s.save()
}

// SetPassword меняет пароль существующего пользователя и снимает флаг
// обязательной смены.
func (s *Store) SetPassword(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	key := strings.ToLower(username)
	s.mu.Lock()
	u, ok := s.users[key]
	if !ok {
		s.mu.Unlock()
		return errors.New("такого пользователя нет")
	}
	u.Hash = string(hash)
	u.MustChangePassword = false
	s.users[key] = u
	s.mu.Unlock()
	return s.save()
}

// save должен вызываться уже без удержания s.mu — иначе он бы блокировался
// сам на себе.
func (s *Store) save() error {
	s.mu.Lock()
	list := make([]User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
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

// GenerateOnePassword придумывает одноразовый пароль для первого входа:
// достаточно длинный, чтобы не подобрать перебором за то время, что он
// провисит в журнале, и без символов, которые легко перепутать при
// переписывании вручную (0/O, 1/l/I).
func GenerateOnePassword() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz"
	const length = 16
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}
