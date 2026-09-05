package panel

import (
	"fmt"
	"strings"
	"time"
)

// ClientManager — единственная точка, где панель заводит, удаляет и
// переключает состояние клиентов. Сама бизнес-логика (что делать в каком
// порядке, как называть ошибки) живёт здесь и проверяется тестами с
// поддельными Provisioner и TrafficAccountant; настоящие системные вызовы —
// только в provision.go и nft.go.
type ClientManager struct {
	store       *ClientStore
	provisioner Provisioner
	// traffic может быть nil (например, в старых тестах ТЗ-08) — тогда
	// заведение и удаление клиента просто не трогает nftables вовсе, без
	// вызовов nil-указателя. Отсутствие самого nftables в системе отдельно
	// обрабатывает WarnAccountingFailure ниже: это разные вещи — "правила
	// учёта не нужны" и "нужны, но nft недоступен".
	traffic TrafficAccountant
	// warnf получает некритичные ошибки (не удалось завести правило учёта
	// трафика, не удалось прочитать /proc и т.п.) — по умолчанию no-op,
	// cmd/ssh_tunnel_panel подставляет log.Printf, чтобы они были видны в
	// журнале, не прерывая работу панели.
	warnf func(format string, args ...any)
}

func NewClientManager(store *ClientStore, provisioner Provisioner) *ClientManager {
	return &ClientManager{store: store, provisioner: provisioner, warnf: func(string, ...any) {}}
}

// WithTraffic подключает учёт трафика — отдельным шагом, а не параметром
// NewClientManager, потому что он не нужен части тестов (ТЗ-08) и не всегда
// доступен в проде (nftables может быть не установлен).
func (m *ClientManager) WithTraffic(t TrafficAccountant) *ClientManager {
	m.traffic = t
	return m
}

// WithWarnf подключает журналирование некритичных ошибок.
func (m *ClientManager) WithWarnf(f func(format string, args ...any)) *ClientManager {
	m.warnf = f
	return m
}

func (m *ClientManager) warn(format string, args ...any) {
	if m.warnf != nil {
		m.warnf(format, args...)
	}
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

	uid, err := m.provisioner.UID(username)
	if err != nil {
		_ = m.provisioner.DeleteUser(username)
		return Client{}, fmt.Errorf("пользователь создан, но не удалось узнать его uid "+
			"(система приведена обратно в исходное состояние): %w", err)
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
		UID:        uid,
	}
	if err := m.store.Put(c); err != nil {
		// Пользователь в системе уже есть, а записи о нём нет — оставлять
		// так нельзя: со следующего перезапуска панель о нём не узнает
		// вообще, а useradd на то же имя больше не даст повторить попытку.
		_ = m.provisioner.DeleteUser(username)
		return Client{}, fmt.Errorf("пользователь создан, но не сохранился в хранилище "+
			"(система приведена обратно в исходное состояние): %w", err)
	}

	// Правило учёта трафика — по возможности: его отсутствие (нет nftables,
	// или ещё не выполнен EnsureTables) не должно мешать завести клиента,
	// только оставляет его без цифр трафика (см. TrafficAccountant).
	if m.traffic != nil {
		if err := m.traffic.AddClient(clientID, uid); err != nil {
			m.warn("не удалось завести правило учёта трафика для %s: %v", clientID, err)
		}
	}
	return c, nil
}

// DeleteClient убивает живые сессии клиента, удаляет его unix-пользователя
// вместе с домашним каталогом, убирает его правила учёта трафика и стирает
// запись в хранилище. Если клиента с таким id нет — не ошибка: результат
// ровно тот, которого добивались.
func (m *ClientManager) DeleteClient(id string) error {
	c, ok := m.store.Get(id)
	if !ok {
		return nil
	}
	if err := m.provisioner.DeleteUser(c.Username); err != nil {
		return fmt.Errorf("не могу удалить пользователя %s: %w", c.Username, err)
	}
	if m.traffic != nil {
		if err := m.traffic.RemoveClient(c.ID); err != nil {
			m.warn("не удалось убрать правило учёта трафика для %s: %v", c.ID, err)
		}
	}
	return m.store.Delete(id)
}

// Freeze вынимает ключ клиента из его authorized_keys (сам ключ остаётся в
// хранилище панели — см. Client.PrivateKey) и обрывает живые сессии.
// Счётчики трафика не трогаются: заморозка не сбрасывает историю.
func (m *ClientManager) Freeze(id string) error {
	c, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("клиент не найден")
	}
	if c.State == StateFrozen {
		return nil
	}
	content, err := m.provisioner.ReadAuthorizedKeys(c.Username)
	if err != nil {
		return fmt.Errorf("не могу прочитать authorized_keys: %w", err)
	}
	if err := m.provisioner.WriteAuthorizedKeys(c.Username, RemoveClientKey(content, c.ID)); err != nil {
		return fmt.Errorf("не могу обновить authorized_keys: %w", err)
	}
	if err := m.provisioner.KillSessions(c.Username); err != nil {
		m.warn("не удалось оборвать сессии %s при заморозке: %v", c.Username, err)
	}
	c.State = StateFrozen
	return m.store.Put(c)
}

// Unfreeze возвращает ключ клиента в authorized_keys. Само по себе не
// поднимает никаких сессий — просто снова разрешает клиенту подключиться.
func (m *ClientManager) Unfreeze(id string) error {
	c, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("клиент не найден")
	}
	if c.State != StateFrozen {
		return nil
	}
	akLine, err := AuthorizedKeysLine(c.PublicKey, c.ID)
	if err != nil {
		return fmt.Errorf("не могу собрать строку authorized_keys: %w", err)
	}
	content, err := m.provisioner.ReadAuthorizedKeys(c.Username)
	if err != nil {
		return fmt.Errorf("не могу прочитать authorized_keys: %w", err)
	}
	if HasClientKey(content, c.ID) {
		// Уже на месте — ничего добавлять не нужно, только поправить
		// состояние (могло разойтись, если предыдущая попытка разморозки
		// упала уже после записи файла).
		c.State = StateActive
		return m.store.Put(c)
	}
	newContent := strings.TrimRight(content, "\n")
	if newContent != "" {
		newContent += "\n"
	}
	newContent += akLine + "\n"
	if err := m.provisioner.WriteAuthorizedKeys(c.Username, newContent); err != nil {
		return fmt.Errorf("не могу обновить authorized_keys: %w", err)
	}
	c.State = StateActive
	return m.store.Put(c)
}

// Disconnect обрывает живые сессии клиента, не трогая его ключ — в отличие
// от Freeze, клиент может тут же подключиться заново сам. Состояние клиента
// не меняется: это разовое действие, а не режим.
func (m *ClientManager) Disconnect(id string) error {
	c, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("клиент не найден")
	}
	return m.provisioner.KillSessions(c.Username)
}

// SyncTraffic обновляет накопленные счётчики трафика всех клиентов текущими
// показаниями nftables. Ошибка чтения (например, nftables не установлен)
// возвращается наружу для журнала, но не трогает уже накопленные значения —
// список клиентов при этом продолжает показывать то, что накопилось раньше.
func (m *ClientManager) SyncTraffic(accountant TrafficAccountant) error {
	if accountant == nil {
		return nil
	}
	counters, err := accountant.ReadCounters()
	if err != nil {
		return err
	}
	for _, c := range m.store.List() {
		raw, ok := counters[c.ID]
		if !ok {
			continue
		}
		c.LastRawRx, c.RxBytes = AccumulateCounter(c.LastRawRx, c.RxBytes, raw.RxBytes)
		c.LastRawTx, c.TxBytes = AccumulateCounter(c.LastRawTx, c.TxBytes, raw.TxBytes)
		if err := m.store.Put(c); err != nil {
			m.warn("не удалось сохранить трафик клиента %s: %v", c.ID, err)
		}
	}
	return nil
}

// SyncOnline пересчитывает число живых сессий каждого клиента обходом
// /proc и обновляет время последнего подключения для тех, у кого сессии
// только что появились.
func (m *ClientManager) SyncOnline(procRoot string) error {
	for _, c := range m.store.List() {
		n, err := CountUserSessions(procRoot, c.UID)
		if err != nil {
			return err
		}
		wasOffline := c.Sessions == 0
		c.Sessions = n
		if n > 0 && wasOffline {
			c.LastSeenAt = time.Now()
		}
		if err := m.store.Put(c); err != nil {
			m.warn("не удалось сохранить состояние подключения клиента %s: %v", c.ID, err)
		}
	}
	return nil
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
