package panel

import (
	"fmt"
	"sync"
)

// fakeProvisioner — Provisioner в памяти для тестов client_manager.go и
// server.go: никаких настоящих useradd/usermod/userdel, только учёт того,
// что было вызвано и с какими аргументами, — этого достаточно, чтобы
// проверить бизнес-логику отдельно от реальных системных вызовов
// (см. provision.go, там же комментарий о том, зачем разделены эти два
// слоя).
type fakeProvisioner struct {
	mu sync.Mutex

	groupEnsured    bool
	users           map[string]string // username -> authorized_keys line
	killed          []string
	deleted         []string
	failCreateUser  string // если совпадает с username, CreateUser вернёт ошибку
	failEnsureGroup bool
}

func newFakeProvisioner() *fakeProvisioner {
	return &fakeProvisioner{users: map[string]string{}}
}

func (f *fakeProvisioner) EnsureGroup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failEnsureGroup {
		return fmt.Errorf("группу создать не вышло")
	}
	f.groupEnsured = true
	return nil
}

func (f *fakeProvisioner) CreateUser(username, pubKeyLine string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !ValidUsername(username) {
		return fmt.Errorf("некорректное имя: %q", username)
	}
	if username == f.failCreateUser {
		return fmt.Errorf("подставная ошибка создания пользователя")
	}
	if _, exists := f.users[username]; exists {
		return fmt.Errorf("пользователь %s уже существует", username)
	}
	f.users[username] = pubKeyLine
	return nil
}

func (f *fakeProvisioner) DeleteUser(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[username]; !ok {
		return fmt.Errorf("пользователь %s не найден", username)
	}
	delete(f.users, username)
	f.deleted = append(f.deleted, username)
	return nil
}

func (f *fakeProvisioner) KillSessions(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, username)
	return nil
}

func (f *fakeProvisioner) WriteAuthorizedKeys(username, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[username] = content
	return nil
}

func (f *fakeProvisioner) ReadAuthorizedKeys(username string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[username], nil
}

func (f *fakeProvisioner) hasUser(username string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[username]
	return ok
}
