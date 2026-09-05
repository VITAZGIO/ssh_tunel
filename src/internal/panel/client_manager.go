package panel

import (
	"fmt"
	"strings"
	"time"
)

// ClientManager — единственная точка, где панель заводит и удаляет клиентов.
// Сама бизнес-логика (что делать в каком порядке, как называть ошибки)
// живёт здесь и проверяется тестами с поддельным Provisioner; настоящие
// системные вызовы — только в provision.go.
type ClientManager struct {
	store       *ClientStore
	provisioner Provisioner
}

func NewClientManager(store *ClientStore, provisioner Provisioner) *ClientManager {
	return &ClientManager{store: store, provisioner: provisioner}
}

// maxNameLength — предел для имени устройства. Само по себе имя нигде не
// подставляется в шелл или в путь (unix-пользователь у клиента — отдельная,
// сгенерированная панелью строка, см. username.go), но чрезмерно длинная
// строка в списке клиентов ломает вёрстку не хуже, чем в любом другом месте,
// где текст пришёл от человека.
const maxNameLength = 80

// CreateClient заводит нового клиента целиком: unix-пользователя, пару
// ключей, запись в authorized_keys и в хранилище панели. При любой ошибке
// после создания пользователя пытается откатить уже сделанные системные
// изменения — незавершённый клиент, застрявший в системе без записи в
// хранилище, было бы сложнее найти и убрать вручную, чем ошибку сейчас.
func (m *ClientManager) CreateClient(name string, deviceType DeviceType) (Client, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Client{}, fmt.Errorf("имя клиента не может быть пустым")
	}
	if len([]rune(name)) > maxNameLength {
		return Client{}, fmt.Errorf("имя клиента длиннее %d символов", maxNameLength)
	}
	if !validDeviceType(deviceType) {
		return Client{}, fmt.Errorf("неизвестный тип устройства: %q", deviceType)
	}

	if err := m.provisioner.EnsureGroup(); err != nil {
		return Client{}, fmt.Errorf("не могу подготовить группу %s: %w", sshdGroup, err)
	}

	username, err := GenerateUsername()
	if err != nil {
		return Client{}, fmt.Errorf("не могу придумать имя пользователя: %w", err)
	}
	// ValidUsername здесь — не паранойя ради стиля, а последняя граница
	// перед системным вызовом: даже если GenerateUsername когда-нибудь
	// начнёт врать, дальше это имя не пройдёт.
	if !ValidUsername(username) {
		return Client{}, fmt.Errorf("сгенерированное имя не прошло проверку: %q", username)
	}
	clientID := ClientID(username)

	pubKey, privKey, err := generateKeyPair()
	if err != nil {
		return Client{}, fmt.Errorf("не могу создать ключ: %w", err)
	}
	akLine, err := AuthorizedKeysLine(pubKey, clientID)
	if err != nil {
		return Client{}, fmt.Errorf("не могу собрать строку authorized_keys: %w", err)
	}

	if err := m.provisioner.CreateUser(username, akLine); err != nil {
		return Client{}, fmt.Errorf("не могу завести пользователя: %w", err)
	}

	c := Client{
		ID:         clientID,
		Name:       name,
		DeviceType: deviceType,
		CreatedAt:  time.Now(),
		State:      StateActive,
		Username:   username,
		PublicKey:  pubKey,
		PrivateKey: privKey,
	}
	if err := m.store.Put(c); err != nil {
		// Пользователь в системе уже есть, а записи о нём нет — оставлять
		// так нельзя: со следующего перезапуска панель о нём не узнает
		// вообще, а useradd на то же имя больше не даст повторить попытку.
		_ = m.provisioner.DeleteUser(username)
		return Client{}, fmt.Errorf("пользователь создан, но не сохранился в хранилище "+
			"(система приведена обратно в исходное состояние): %w", err)
	}
	return c, nil
}

// DeleteClient убивает живые сессии клиента, удаляет его unix-пользователя
// вместе с домашним каталогом и стирает запись в хранилище. Если клиента с
// таким id нет — не ошибка: результат ровно тот, которого добивались.
func (m *ClientManager) DeleteClient(id string) error {
	c, ok := m.store.Get(id)
	if !ok {
		return nil
	}
	if err := m.provisioner.DeleteUser(c.Username); err != nil {
		return fmt.Errorf("не могу удалить пользователя %s: %w", c.Username, err)
	}
	return m.store.Delete(id)
}

// List отдаёт клиентов без приватных ключей — этот список идёт прямо в
// ответ /api/clients, а приватный ключ показывается только отдельной
// ручкой по явному действию человека (ТЗ-10).
func (m *ClientManager) List() []Client {
	list := m.store.List()
	for i := range list {
		list[i].PrivateKey = ""
	}
	return list
}
