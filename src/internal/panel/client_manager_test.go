package panel

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestClientManager(t *testing.T) (*ClientManager, *fakeProvisioner) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvisioner()
	return NewClientManager(store, prov), prov
}

func TestCreateClientProvisionsUserAndKey(t *testing.T) {
	m, prov := newTestClientManager(t)

	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidUsername(c.Username) {
		t.Fatalf("клиенту присвоено некорректное имя пользователя: %q", c.Username)
	}
	if c.ID != c.Username {
		t.Fatalf("ID клиента должен совпадать с unix-именем: %q != %q", c.ID, c.Username)
	}
	if c.State != StateActive {
		t.Fatalf("новый клиент должен быть активным, получил %q", c.State)
	}
	if c.PrivateKey == "" {
		t.Fatal("создание клиента должно вернуть приватный ключ")
	}
	if !prov.groupEnsured {
		t.Fatal("создание клиента должно подготовить группу sshtunnel")
	}
	if !prov.hasUser(c.Username) {
		t.Fatal("создание клиента должно завести unix-пользователя")
	}

	akLine := prov.users[c.Username]
	if !strings.HasPrefix(akLine, "restrict,port-forwarding ") {
		t.Fatalf("строка authorized_keys должна начинаться с restrict,port-forwarding: %q", akLine)
	}
	if !strings.HasSuffix(akLine, " "+c.ID) {
		t.Fatalf("строка authorized_keys должна оканчиваться id клиента: %q", akLine)
	}
	if !strings.Contains(akLine, c.PublicKey) {
		t.Fatalf("строка authorized_keys должна содержать открытый ключ клиента: %q", akLine)
	}
}

func TestCreateClientRejectsBadInput(t *testing.T) {
	m, _ := newTestClientManager(t)

	if _, err := m.CreateClient("", DeviceLinux); err == nil {
		t.Fatal("пустое имя должно быть ошибкой")
	}
	if _, err := m.CreateClient("  ", DeviceLinux); err == nil {
		t.Fatal("имя из одних пробелов должно быть ошибкой")
	}
	if _, err := m.CreateClient(strings.Repeat("a", maxNameLength+1), DeviceLinux); err == nil {
		t.Fatal("слишком длинное имя должно быть ошибкой")
	}
	if _, err := m.CreateClient("Телефон", DeviceType("bogus")); err == nil {
		t.Fatal("неизвестный тип устройства должен быть ошибкой")
	}
}

func TestCreateClientRollsBackOnStoreFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvisioner()
	m := NewClientManager(store, prov)

	// Ломаем путь хранилища так, чтобы save() гарантированно не сработал —
	// подменяем его на директорию, куда файл не запишется.
	store.path = dir // это директория, а не файл: os.Rename поверх неё упадёт

	if _, err := m.CreateClient("Ноутбук", DeviceLinux); err == nil {
		t.Fatal("ожидал ошибку сохранения в хранилище")
	}
	if len(prov.users) != 0 {
		t.Fatalf("после отката пользователь не должен остаться в системе: %v", prov.users)
	}
	if len(prov.deleted) != 1 {
		t.Fatalf("откат должен вызвать удаление пользователя ровно один раз, вызвал %d раз", len(prov.deleted))
	}
}

func TestDeleteClientRemovesUserAndRecord(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("Телефон", DeviceAndroid)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.DeleteClient(c.ID); err != nil {
		t.Fatal(err)
	}
	if prov.hasUser(c.Username) {
		t.Fatal("после удаления пользователь не должен остаться в системе")
	}
	if _, ok := m.store.Get(c.ID); ok {
		t.Fatal("после удаления записи в хранилище быть не должно")
	}
	if len(m.List()) != 0 {
		t.Fatal("список клиентов должен быть пуст после удаления единственного клиента")
	}
}

func TestDeleteUnknownClientIsNotAnError(t *testing.T) {
	m, _ := newTestClientManager(t)
	if err := m.DeleteClient("tun_0000000000000000"); err != nil {
		t.Fatalf("удаление несуществующего клиента не должно быть ошибкой, получил: %v", err)
	}
}

func TestListHidesPrivateKey(t *testing.T) {
	m, _ := newTestClientManager(t)
	if _, err := m.CreateClient("Ноутбук", DeviceWindows); err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("ожидал одного клиента, получил %d", len(list))
	}
	if list[0].PrivateKey != "" {
		t.Fatal("список клиентов не должен содержать приватный ключ")
	}
	if list[0].PublicKey == "" {
		t.Fatal("открытый ключ в списке быть должен")
	}
}
