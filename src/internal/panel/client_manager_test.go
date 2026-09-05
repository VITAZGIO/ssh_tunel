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

func TestFreezeRemovesKeyAndKillsSessions(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Freeze(c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.store.Get(c.ID)
	if got.State != StateFrozen {
		t.Fatalf("состояние должно стать frozen, получил %q", got.State)
	}
	if HasClientKey(prov.users[c.Username], c.ID) {
		t.Fatal("после заморозки ключа не должно быть в authorized_keys")
	}
	found := false
	for _, k := range prov.killed {
		if k == c.Username {
			found = true
		}
	}
	if !found {
		t.Fatal("заморозка должна оборвать сессии клиента")
	}

	// Повторная заморозка уже замороженного клиента — не ошибка и не
	// повторное действие.
	if err := m.Freeze(c.ID); err != nil {
		t.Fatalf("повторная заморозка не должна быть ошибкой: %v", err)
	}
}

func TestUnfreezeRestoresKey(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Freeze(c.ID); err != nil {
		t.Fatal(err)
	}

	if err := m.Unfreeze(c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.store.Get(c.ID)
	if got.State != StateActive {
		t.Fatalf("состояние должно вернуться в active, получил %q", got.State)
	}
	if !HasClientKey(prov.users[c.Username], c.ID) {
		t.Fatal("после разморозки ключ должен снова быть в authorized_keys")
	}

	// Повторная разморозка уже активного клиента — не ошибка.
	if err := m.Unfreeze(c.ID); err != nil {
		t.Fatalf("повторная разморозка не должна быть ошибкой: %v", err)
	}
}

func TestDisconnectKillsSessionsWithoutTouchingKey(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("Телефон", DeviceAndroid)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Disconnect(c.ID); err != nil {
		t.Fatal(err)
	}
	if !HasClientKey(prov.users[c.Username], c.ID) {
		t.Fatal("отключение не должно трогать ключ клиента")
	}
	got, _ := m.store.Get(c.ID)
	if got.State != StateActive {
		t.Fatalf("отключение не должно менять состояние, получил %q", got.State)
	}
	found := false
	for _, k := range prov.killed {
		if k == c.Username {
			found = true
		}
	}
	if !found {
		t.Fatal("отключение должно оборвать сессии клиента")
	}
}

func TestSyncTrafficAccumulatesAndSurvivesReadError(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvisioner()
	acc := newFakeAccountant()
	m := NewClientManager(store, prov).WithTraffic(acc)

	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := acc.added[c.ID]; !ok {
		t.Fatal("создание клиента должно завести правило учёта трафика")
	}

	acc.setCounter(c.ID, RawCounter{RxBytes: 1000, TxBytes: 500})
	if err := m.SyncTraffic(acc); err != nil {
		t.Fatal(err)
	}
	got, _ := m.store.Get(c.ID)
	if got.RxBytes != 1000 || got.TxBytes != 500 {
		t.Fatalf("после первой синхронизации ожидал rx=1000 tx=500, получил rx=%d tx=%d",
			got.RxBytes, got.TxBytes)
	}

	// nftables недоступен на этом тике — накопленные значения не должны
	// обнулиться.
	acc.failRead = true
	if err := m.SyncTraffic(acc); err == nil {
		t.Fatal("ожидал ошибку чтения счётчиков")
	}
	got, _ = m.store.Get(c.ID)
	if got.RxBytes != 1000 || got.TxBytes != 500 {
		t.Fatalf("накопленные значения не должны меняться при ошибке чтения: rx=%d tx=%d",
			got.RxBytes, got.TxBytes)
	}

	acc.failRead = false
	acc.setCounter(c.ID, RawCounter{RxBytes: 1500, TxBytes: 900})
	if err := m.SyncTraffic(acc); err != nil {
		t.Fatal(err)
	}
	got, _ = m.store.Get(c.ID)
	if got.RxBytes != 1500 || got.TxBytes != 900 {
		t.Fatalf("после роста счётчика ожидал rx=1500 tx=900, получил rx=%d tx=%d",
			got.RxBytes, got.TxBytes)
	}
}

func TestDeleteClientRemovesTrafficRule(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvisioner()
	acc := newFakeAccountant()
	m := NewClientManager(store, prov).WithTraffic(acc)

	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteClient(c.ID); err != nil {
		t.Fatal(err)
	}
	if len(acc.removed) != 1 || acc.removed[0] != c.ID {
		t.Fatalf("удаление клиента должно убрать его правило учёта трафика, получил %v", acc.removed)
	}
}

func TestSyncOnlineTracksSessionsAndLastSeen(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("Ноутбук", DeviceLinux)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := prov.UID(c.Username)
	if err != nil {
		t.Fatal(err)
	}

	root := makeFakeProc(t, map[int]struct {
		uid  int
		comm string
	}{500: {uid: uid, comm: "sshd"}})

	if err := m.SyncOnline(root); err != nil {
		t.Fatal(err)
	}
	got, _ := m.store.Get(c.ID)
	if got.Sessions != 1 {
		t.Fatalf("ожидал 1 сессию, получил %d", got.Sessions)
	}
	if got.LastSeenAt.IsZero() {
		t.Fatal("LastSeenAt должен обновиться при появлении сессии")
	}
	firstSeen := got.LastSeenAt

	// Сессия пропала — Sessions должен обнулиться, а LastSeenAt остаться от
	// последнего наблюдения.
	emptyRoot := t.TempDir()
	if err := m.SyncOnline(emptyRoot); err != nil {
		t.Fatal(err)
	}
	got, _ = m.store.Get(c.ID)
	if got.Sessions != 0 {
		t.Fatalf("ожидал 0 сессий, получил %d", got.Sessions)
	}
	if !got.LastSeenAt.Equal(firstSeen) {
		t.Fatalf("LastSeenAt не должен меняться, пока клиент не в сети: было %v, стало %v",
			firstSeen, got.LastSeenAt)
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
