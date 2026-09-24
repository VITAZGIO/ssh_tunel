// Пакет mobile — единственное, что видит приложение на Kotlin.
//
// Связка Go и Java умеет передавать только простые вещи: числа, строки, булевы
// значения, массивы байт и типы, объявленные здесь же. Поэтому вся настройка
// принимается плоскими параметрами, а обратная связь идёт через интерфейс
// Callbacks, который реализуется на стороне Kotlin.
package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"sshtunnel/android/core"
	"sshtunnel/internal/events"
	"sshtunnel/internal/mesh"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/share"
	"sshtunnel/internal/speedtest"
	"sshtunnel/internal/tunnel"
)

// Callbacks — то, что Go просит сделать у приложения.
type Callbacks interface {
	// Protect помечает сокет как идущий мимо туннеля.
	//
	// Без этого соединение с сервером ушло бы в собственный туннель и
	// зациклилось: система заворачивает внутрь весь трафик, включая наш.
	// Реализация на Kotlin — VpnService.protect(fd).
	Protect(fd int) bool

	// OnState сообщает о смене состояния, чтобы экран показал её человеку.
	//
	// errorKind — стабильный код причины отказа подключения (ТЗ-13,
	// tunnel.ConnErrorKind: "auth", "no_response", "refused", "other"),
	// заполнен только при state=="error"/"reconnecting" и только если
	// причина распознана. Экран переводит по нему текст через свой словарь
	// строк вместо показа сырого detail — разбор самой ошибки один раз
	// сделан в общем коде (internal/tunnel), сюда приходит уже готовый код.
	// Пустая строка — либо не ошибка, либо смена ключа сервера
	// (hostkey.ErrChanged): тот текст в detail не трогаем, показываем как
	// есть.
	OnState(state string, detail string, errorKind string)

	// OnLog отдаёт строку для журнала соединений.
	OnLog(line string)

	// ResolveLocal разрешает имя средствами системы, минуя туннель.
	// Возвращает адреса через запятую или пустую строку, если не вышло.
	//
	// Своими силами Go этого на Android не может: файла /etc/resolv.conf там
	// нет, а системный резолвер доступен только через Java.
	ResolveLocal(name string) string
}

// Tunnel — то, что приложение включает и выключает.
type Tunnel struct {
	mu sync.Mutex

	cb     Callbacks
	cfg    tunnel.Config
	direct *routing.DirectList
	block  *core.BlockList

	core   *tunnel.Tunnel
	engine *core.Engine
	stack  *core.Stats
	bus    *events.Bus
	stop   chan struct{}

	// gen растёт на каждой остановке.
	//
	// Подключение занимает секунды, и всё это время человек может передумать и
	// нажать «стоп». Раньше такое нажатие проходило вхолостую — гасить было
	// ещё нечего, — а поднявшееся следом ядро оставалось работать, и выключить
	// его было уже нечем. Поколение отвечает на вопрос «пока я подключался,
	// меня не выключили?».
	gen int64
}

// NewTunnel создаёт выключенный туннель.
//
// Имя не New намеренно: в Java «new» — служебное слово, и связка Go с Java
// переименовала бы функцию во что-то нечитаемое.
func NewTunnel() *Tunnel { return &Tunnel{} }

// SetCallbacks задаёт обратную связь. Вызывается до Configure.
func (t *Tunnel) SetCallbacks(cb Callbacks) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cb = cb
}

// Configure принимает настройки.
//
// keyPath — путь к файлу закрытого ключа: приложение сохраняет его в свою
// папку само, потому что права на файл ключа задаёт тоже оно.
// directHosts — список «всегда напрямую», через запятую или с новой строки.
//
// adBlockEnabled включает блокировку рекламы и слежки. adBlockListPath —
// путь к файлу со списком имён, по одному в строке (см. UpdateBlockLists —
// именно она готовит этот файл на телефоне); если файла ещё нет, блокировка
// включается, но пока ничего не блокирует. adBlockAllowlist — свои
// исключения, через запятую или с новой строки, как и directHosts.
func (t *Tunnel) Configure(
	host string, sshPort int, user string, keyPath string,
	knownHostsPath string, poolSize int,
	directHosts string, localViaTunnel bool,
	adBlockEnabled bool, adBlockListPath string, adBlockAllowlist string,
	udpRelayEnabled bool,
) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("не задан адрес сервера")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("не задано имя пользователя")
	}
	if sshPort <= 0 {
		sshPort = 22
	}
	if poolSize <= 0 {
		poolSize = 4
	}

	var block *core.BlockList
	if adBlockEnabled {
		var blocked []string
		if data, err := os.ReadFile(adBlockListPath); err == nil {
			blocked = strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		block = core.NewBlockList(blocked, routing.SplitEntries(adBlockAllowlist))
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Список один на всё время жизни: его же держат ядро и DNS уже
	// поднятого стека. Новый объект они бы не увидели — после слива
	// (Resume) продолжал бы действовать прежний список.
	if t.direct == nil {
		t.direct = routing.NewDirectList(nil)
	}
	t.direct.Set(routing.SplitEntries(directHosts))
	t.block = block
	t.cfg = tunnel.Config{
		Host:            host,
		SSHPort:         sshPort,
		User:            user,
		KeyPath:         keyPath,
		KnownHostsPath:  knownHostsPath,
		PoolSize:        poolSize,
		Direct:          t.direct,
		LocalViaTunnel:  localViaTunnel,
		ProtectSocket:   t.protect,
		UDPRelayEnabled: udpRelayEnabled,

		// Локальных прокси на телефоне нет: соединения приходят из пакетов,
		// а не с портов 1080 и 1081.
		SocksAddr: "",
		HTTPAddr:  "",
	}
	return nil
}

// ConfigureMesh задаёт сеть устройств (см. sshtunnel/internal/mesh).
// Вызывается после Configure и до StartCore: Configure собирает настройки
// заново и сеть сбрасывает. deviceID — постоянный id телефона, его хранит
// приложение. Пустой key при enabled — ошибка: ключ сети приложение
// создаёт само (NewMeshKey) или получает импортом сервера.
func (t *Tunnel) ConfigureMesh(enabled bool, key, name, deviceID string, incoming bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !enabled {
		t.cfg.Mesh = nil
		return nil
	}
	key, name, deviceID = strings.TrimSpace(key), strings.TrimSpace(name), strings.TrimSpace(deviceID)
	if key == "" {
		return fmt.Errorf("не задан ключ сети устройств")
	}
	if deviceID == "" {
		return fmt.Errorf("не задан id устройства")
	}
	if name == "" {
		name = "телефон"
	}
	t.cfg.Mesh = &mesh.Config{Key: key, DeviceID: deviceID, Name: name, AllowIncoming: incoming}
	return nil
}

// NewMeshKey — ключ новой сети устройств.
func NewMeshKey() string { return mesh.NewKey() }

// NewDeviceID — постоянный id устройства для сети устройств.
func NewDeviceID() string { return mesh.NewDeviceID() }

// MeshStatusJSON — состояние сети устройств для экрана:
// {"state":"online","self":{...},"peers":[...]} или {"state":"off"}.
func (t *Tunnel) MeshStatusJSON() string {
	t.mu.Lock()
	tun, cfg := t.core, t.cfg
	t.mu.Unlock()
	st := mesh.Status{State: "off"}
	if cfg.Mesh != nil {
		st.State = "stopped"
	}
	if tun != nil {
		if m := tun.Mesh(); m != nil {
			st = m.Status()
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return `{"state":"off"}`
	}
	return string(b)
}

// protect помечает сокет через приложение. Ошибка пометки — не повод рвать
// соединение: на некоторых устройствах вызов возвращает false и при этом всё
// работает. Зато молча зациклиться было бы гораздо хуже, поэтому пишем в лог.
func (t *Tunnel) protect(network, address string, c syscall.RawConn) error {
	cb := t.callbacks()
	if cb == nil {
		return nil
	}
	var ok bool
	err := c.Control(func(fd uintptr) { ok = cb.Protect(int(fd)) })
	if err != nil {
		return err
	}
	if !ok {
		cb.OnLog(fmt.Sprintf("не удалось пометить сокет до %s", address))
	}
	return nil
}

func (t *Tunnel) callbacks() Callbacks {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cb
}

// StartCore поднимает только туннель по SSH, без сетевого стека.
//
// Разделено на два шага ради одной проверки: прежде чем создавать интерфейс,
// надо узнать, умеет ли сервер IPv6. От этого зависит, объявлять ли его
// телефону. Объявить и не суметь — худшее из возможного: приложения дружно
// уходят в шестую версию, и не работает вообще ничего.
func (t *Tunnel) StartCore() error {
	t.mu.Lock()
	if t.core != nil {
		t.mu.Unlock()
		return fmt.Errorf("туннель уже запущен")
	}
	cfg, cb, gen := t.cfg, t.cb, t.gen
	t.mu.Unlock()

	if cfg.Host == "" {
		return fmt.Errorf("сначала нужно задать настройки")
	}

	bus := events.NewBus()
	stop := make(chan struct{})
	ch, unsub := bus.Subscribe()
	go t.forwardEvents(ch, unsub, stop, cb)

	tun := tunnel.New(cfg, bus)
	if err := tun.Start(); err != nil {
		close(stop)
		return err
	}

	t.mu.Lock()
	if t.gen != gen {
		// Пока мы подключались, нажали «стоп». Поднятое ядро никому не нужно —
		// гасим и уходим, не запоминая его.
		t.mu.Unlock()
		close(stop)
		go tun.Stop()
		return fmt.Errorf("остановлено")
	}
	t.core, t.bus, t.stop = tun, bus, stop
	t.mu.Unlock()

	if cb != nil {
		cb.OnState(tun.State(), fmt.Sprintf("%s:%d", cfg.Host, cfg.SSHPort), "")
	}
	return nil
}

// ServerHasIPv6 спрашивает у сервера, может ли он открыть соединение по IPv6.
//
// Проверка идёт через сам туннель, на общедоступный DNS-сервер Google: если
// сервер сумел — значит шестая версия у него работает, и её можно объявлять
// телефону. Если нет, интерфейс останется четвёртой версии, и приложения
// пойдут по ней.
func (t *Tunnel) ServerHasIPv6() bool {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun == nil {
		return false
	}
	if !tun.WaitReady(1, 15*time.Second) {
		return false
	}
	c, err := tun.Dial("tcp", "[2001:4860:4860::8888]:53")
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// StartStack достраивает сетевой стек поверх дескриптора от VpnService.
//
// tunFD должен быть получен через detachFd(): дальше им распоряжается Go и
// закрывает его сам. Если отдать getFd(), дескриптор закроют дважды.
func (t *Tunnel) StartStack(tunFD int, mtu int) error {
	t.mu.Lock()
	tun, block, cb := t.core, t.block, t.cb
	t.mu.Unlock()

	if tun == nil {
		return fmt.Errorf("сначала нужно поднять туннель")
	}
	if mtu <= 0 {
		mtu = 1500
	}

	pool, err := core.NewFakePool(fakeNet)
	if err != nil {
		return err
	}

	stackStats := &core.Stats{}

	dns := &core.DNS{
		Pool: pool,
		// Мимо туннеля идут те же имена, что и на компьютере: локальная сеть
		// и то, что человек внёс в список сам.
		//
		// Правила читаем в момент запроса, а не запоминаем при старте: стек
		// переживает слив и Resume, а настройки к тому времени могли смениться.
		Direct: func(name string) bool {
			t.mu.Lock()
			direct, localViaTunnel := t.direct, t.cfg.LocalViaTunnel
			t.mu.Unlock()
			if !localViaTunnel && routing.IsLocalHost(name) {
				return true
			}
			return direct != nil && direct.Match(name)
		},
		Local: func(name string) ([]net.IP, error) {
			if cb == nil {
				return nil, fmt.Errorf("нет резолвера")
			}
			ips, err := parseIPs(cb.ResolveLocal(name))
			if err == nil {
				// Приложение придёт уже с адресом, а не с именем, —
				// ядро должно узнать его и не повести через сервер.
				tun.LearnDirect(name, ips)
			}
			return ips, err
		},
		// Block проверяется раньше Direct и раньше Pool.Get — см. dns.go.
		Block: block,
		// Имена сети устройств («ноутбук.mesh») — настоящими адресами.
		Static: func(name string) ([]net.IP, bool) {
			return mesh.StaticDNS(tun.Mesh(), name)
		},
		Stats: stackStats,
	}

	eng, err := core.Start(tunFD, uint32(mtu), &core.Handler{
		Core:    tun,
		Resolve: pool.Resolver(),
		DNS:     dns,
		Stats:   stackStats,
		Log: func(line string) {
			if cb != nil {
				cb.OnLog(line)
			}
		},
		// UDPRelay — тот же клиент, что и на компьютере (см.
		// tunnel.Tunnel.UDPRelay): звонит по требованию, кеширует и сам
		// переподключается. Значение проверяется на каждый новый поток, не
		// один раз, — поэтому здесь достаточно функции-обёртки без вызова.
		UDPRelay: tun.UDPRelay,
	})
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.engine, t.stack = eng, stackStats
	t.mu.Unlock()
	return nil
}

// forwardEvents перекладывает события ядра на экран приложения.
func (t *Tunnel) forwardEvents(ch <-chan events.Event, unsub func(), stop <-chan struct{}, cb Callbacks) {
	defer unsub()
	if cb == nil {
		return
	}

	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Kind {
			case events.KindState:
				cb.OnState(ev.State, ev.Detail, ev.ErrorKind)
			case events.KindLog:
				cb.OnLog(ev.Text)
			case events.KindConn:
				cb.OnLog(describeConn(ev))
			}
		}
	}
}

// describeConn превращает событие о соединении в строку для журнала.
// Пометка «напрямую» важна человеку: она означает не сбой, а сработавшее
// правило — например, адрес из локальной сети.
func describeConn(ev events.Event) string {
	line := ev.Target
	if ev.Direct {
		line += " — напрямую"
	}
	if ev.DNSLeak {
		line += " — адрес известен заранее"
	}
	if ev.Failed && ev.Error != "" {
		line += " — ошибка: " + ev.Error
	}
	return line
}

// Drain — мягкая остановка: связь с сервером рвётся немедленно, но сетевой
// стек и интерфейс VpnService остаются на месте и ведут трафик НАПРЯМУЮ, мимо
// сервера, ещё seconds секунд.
//
// Ради чего. Закрытие интерфейса VPN рвёт разом все сокеты всех приложений,
// и узнают они об этом не сразу: мессенджер продолжает слать в мёртвый сокет
// до собственного таймаута. Отсюда «выключил VPN — интернет пропал». Живой
// интерфейс, водящий напрямую, эту дыру закрывает. А если человек за это
// время включит VPN обратно (Resume), интерфейс даже не дрогнет — и ни одно
// приложение ничего не заметит, перезапускать их не придётся.
//
// Дескриптором распоряжается сторона Android: она же и решает, когда всё
// закончится, вызывая Stop. Здесь только разрыв с сервером и переключение
// маршрутизации на прямую.
func (t *Tunnel) Drain(seconds int) {
	t.mu.Lock()
	tun := t.core
	if tun != nil && seconds > 0 {
		t.cfg.DrainTimeout = time.Duration(seconds) * time.Second
		tun.SetDrainTimeout(t.cfg.DrainTimeout)
	}
	t.mu.Unlock()
	if tun == nil {
		return
	}
	tun.Drain()
}

// Draining — идёт ли сейчас слив. По нему сторона Android решает, поднимать
// туннель заново (Resume) или запускать всё с нуля.
func (t *Tunnel) Draining() bool {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	return tun != nil && tun.Draining()
}

// UpdateRouting применяет правила «всегда напрямую» и «локальную сеть через
// сервер» к уже поднятому туннелю. Нужно перед Resume: возобновление
// переиспользует и ядро, и стек, и без этого действовали бы правила, с
// которыми туннель поднимали в прошлый раз.
func (t *Tunnel) UpdateRouting(directHosts string, localViaTunnel bool) {
	t.mu.Lock()
	if t.direct == nil {
		t.direct = routing.NewDirectList(nil)
	}
	t.direct.Set(routing.SplitEntries(directHosts))
	t.cfg.Direct = t.direct
	t.cfg.LocalViaTunnel = localViaTunnel
	tun, direct := t.core, t.direct
	t.mu.Unlock()
	if tun != nil {
		tun.SetDirect(direct)
		tun.SetLocalViaTunnel(localViaTunnel)
	}
}

// Resume возвращает к жизни туннель, который был в сливе. Интерфейс VPN при
// этом не пересоздаётся, поэтому уже открытые сокеты приложений продолжают
// работать — просто снова через сервер.
func (t *Tunnel) Resume() error {
	t.mu.Lock()
	tun, cfg, cb := t.core, t.cfg, t.cb
	t.mu.Unlock()
	if tun == nil {
		return fmt.Errorf("туннель не запущен")
	}
	if err := tun.Resume(); err != nil {
		return err
	}
	if cb != nil {
		cb.OnState(tun.State(), fmt.Sprintf("%s:%d", cfg.Host, cfg.SSHPort), "")
	}
	return nil
}

// Stop останавливает всё и освобождает дескриптор.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	tun, eng, stop := t.core, t.engine, t.stop
	t.core, t.engine, t.bus, t.stop, t.stack = nil, nil, nil, nil, nil
	t.gen++
	t.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if eng != nil {
		eng.Close()
	}
	if tun != nil {
		tun.Stop()
	}
}

// NetworkChanged сообщает, что телефон сменил сеть (Wi-Fi ↔ мобильная).
//
// Вызывается из ConnectivityManager.NetworkCallback на стороне Kotlin. Без
// этого сигнала обрыв замечался бы только по таймеру проверки живости — до
// двадцати секунд паузы при каждом переключении сети. Пул пересобирается
// сразу: старые сокеты, помеченные VpnService.protect() под прежнюю сеть,
// после смены почти наверняка уже нерабочие, ждать их естественной смерти
// незачем.
func (t *Tunnel) NetworkChanged() {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun != nil {
		tun.Kick()
	}
}

// SelfCheck прогоняет ту же цепочку самопроверки, что и на компьютере (см.
// sshtunnel/internal/tunnel.RunSelfCheck), и отдаёт результат строкой JSON:
// {"steps":[{"name":"dns","ok":true,"code":"resolved","detail":"..."}, ...]}.
// Соединение для проверки отдельное от уже поднятого пула — работает и
// объясняет причину, даже когда туннель выключен. Configure нужно вызвать
// заранее: отсюда берутся адрес, пользователь и путь к ключу.
func (t *Tunnel) SelfCheck() string {
	t.mu.Lock()
	cfg := t.cfg
	t.mu.Unlock()
	if cfg.Host == "" {
		return `{"error":"сначала нужно задать настройки"}`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	steps := tunnel.RunSelfCheck(ctx, tunnel.SelfCheckOptions{Config: cfg})

	b, err := json.Marshal(struct {
		Steps []tunnel.CheckStep `json:"steps"`
	}{Steps: steps})
	if err != nil {
		return `{"error":"не удалось разобрать результат"}`
	}
	return string(b)
}

// SpeedTest мерит скорость через туннель — тот же тест, что в окне на
// компьютере. Возвращает результат строкой JSON.
//
// Идёт он секунд двадцать, поэтому вызывать только из отдельного потока.
func (t *Tunnel) SpeedTest() string {
	t.mu.Lock()
	tun, cb := t.core, t.cb
	t.mu.Unlock()
	if tun == nil {
		return `{"error":"туннель выключен"}`
	}

	res, err := speedtest.Run(context.Background(), speedtest.Options{
		Dial: tun.Dial,
		OnProgress: func(phase string, mbps float64) {
			if cb == nil {
				return
			}
			what := "приём"
			if phase == "up" {
				what = "отдача"
			}
			cb.OnLog(fmt.Sprintf("тест скорости, %s: %.1f Мбит/с", what, mbps))
		},
	})
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	b, err := json.Marshal(res)
	if err != nil {
		return `{"error":"не удалось разобрать результат"}`
	}
	return string(b)
}

// State — состояние одним словом, для экрана.
func (t *Tunnel) State() string {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun == nil {
		return "stopped"
	}
	return tun.State()
}

// StatsJSON — счётчики для экрана: байты, соединения, живые каналы, а также
// то, что стек отверг. Последнее важно человеку не меньше: по нему видно, что
// приложение ломится туда, куда через SSH хода нет.
func (t *Tunnel) StatsJSON() string {
	t.mu.Lock()
	tun, st := t.core, t.stack
	t.mu.Unlock()
	if tun == nil {
		return "{}"
	}

	out := struct {
		events.Stats
		UDPDropped int `json:"udpDropped"`
		DNSAsked   int `json:"dnsAsked"`
		V6Blocked  int `json:"v6Blocked"`
		AdsBlocked int `json:"adsBlocked"`
	}{Stats: tun.Stats()}

	if st != nil {
		_, out.UDPDropped, out.DNSAsked, out.V6Blocked, out.AdsBlocked = st.Counts()
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Диапазон 198.18.0.0/15 зарезервирован под тесты сетевого оборудования:
// настоящих сайтов там нет, спутать не с чем. Внутри него три роли, и они
// намеренно не пересекаются.
const (
	// fakeNet — подставные адреса, которые выдаются вместо настоящих.
	fakeNet = "198.18.128.0/17"

	// dnsAddr — наш DNS-сервер.
	//
	// Взят отдельным адресом, а не адресом самого интерфейса, и это важно:
	// пакет на собственный адрес система может обработать сама и не отдать в
	// туннель вовсе. Тогда имена перестают разрешаться у всех приложений,
	// кроме тех, что носят адреса в себе, — а выглядит это так, будто не
	// работает половина интернета.
	dnsAddr = "198.18.0.53"
)

// ParsedConfig — конфиг сервера, разобранный из текста (файл экспорта или
// содержимое QR-кода) в форму, понятную Kotlin: gomobile умеет отдавать
// только простые типы, поэтому списки полей (FilterApps, DirectHosts)
// приходят строками, по одному значению на строку.
type ParsedConfig struct {
	Name           string
	Flag           string
	Host           string
	SshPort        int
	User           string
	PoolSize       int
	FilterMode     string
	FilterApps     string
	DirectHosts    string
	LocalViaTunnel bool
	KeyIncluded    bool
	KeyContents    string
	// Panel/DeviceName — заполнены, только если конфиг выдан веб-панелью на
	// VPS (internal/share, поля версии 2). У сервера, настроенного руками,
	// Panel пустой — по нему экран настроек решает, показывать ли строку
	// «Этот сервер выдан панелью».
	Panel      string
	DeviceName string
	// MeshKey — ключ сети устройств на этом сервере; пусто — сети нет.
	MeshKey string
}

// ParseConfig разбирает текст, вставленный из буфера обмена или считанный из
// QR-кода, в ParsedConfig. Тот же формат, что использует экспорт/импорт
// сервера в панели на компьютере (internal/share), версии 1 и 2 — оба
// читаются одинаково. Ошибка возвращается такой, какую можно показать
// человеку на экране как есть — она уже на русском.
func ParseConfig(text string) (*ParsedConfig, error) {
	doc, err := share.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	return &ParsedConfig{
		Name: doc.Name, Flag: doc.Flag, Host: doc.Host, SshPort: doc.SSHPort, User: doc.User,
		PoolSize:       doc.PoolSize,
		FilterMode:     doc.FilterMode,
		FilterApps:     strings.Join(doc.FilterApps, "\n"),
		DirectHosts:    strings.Join(doc.DirectHosts, "\n"),
		LocalViaTunnel: doc.LocalViaTunnel,
		KeyIncluded:    doc.KeyIncluded,
		KeyContents:    doc.KeyContents,
		Panel:          doc.Panel,
		DeviceName:     doc.DeviceName,
		MeshKey:        doc.MeshKey,
	}, nil
}

// BuildConfig собирает тот же текст, что ParseConfig разбирает обратно —
// нужен экрану настроек для кнопки «Экспортировать сервер» (копия в буфер
// обмена и в файл того же формата, что использует и панель на компьютере).
// Поля flat: gomobile не умеет передавать структуры аргументом, только
// плоский набор скаляров, как и во всех остальных функциях этого пакета.
func BuildConfig(name, flag, host string, sshPort int, user string, poolSize int,
	filterMode, filterApps, directHosts string, localViaTunnel, keyIncluded bool,
	keyContents, panel, deviceName, meshKey string) (string, error) {
	doc := share.Doc{
		Name: name, Flag: flag, Host: host, SSHPort: sshPort, User: user,
		SocksPort: 1080, HTTPPort: 1081, PoolSize: poolSize,
		FilterMode:     filterMode,
		FilterApps:     splitNonEmpty(filterApps),
		DirectHosts:    splitNonEmpty(directHosts),
		LocalViaTunnel: localViaTunnel,
		KeyIncluded:    keyIncluded,
		KeyContents:    keyContents,
		Panel:          panel,
		DeviceName:     deviceName,
		MeshKey:        meshKey,
	}
	data, err := share.Build(doc)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func splitNonEmpty(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// UpdateBlockLists загружает списки блокировки рекламы и слежки по адресам
// или путям из sourcesText (через запятую или с новой строки, вперемешку
// http(s)-ссылки и локальные файлы), сохраняет объединённый и очищенный от
// повторов список имён на телефон по пути cachePath — чтобы дальше
// блокировка работала и без сети — и отдаёт результат строкой JSON:
// {"count":N} при успехе, {"error":"..."} при неудаче.
//
// Вызывается только по явному нажатию кнопки в настройках, никогда сама по
// себе: список не должен меняться без ведома человека, а обновление по
// расписанию — это ещё и сетевой запрос, который на телефоне не бесплатен.
func UpdateBlockLists(sourcesText string, cachePath string) string {
	sources := routing.SplitEntries(sourcesText)
	if len(sources) == 0 {
		return `{"error":"не указан ни один список"}`
	}

	seen := make(map[string]struct{})
	var names []string
	var lastErr error
	fetchedAny := false
	for _, src := range sources {
		list, err := core.FetchBlockListSource(src)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src, err)
			continue
		}
		fetchedAny = true
		for _, n := range list {
			n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
			if n == "" {
				continue
			}
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			names = append(names, n)
		}
	}
	if !fetchedAny {
		return fmt.Sprintf(`{"error":%q}`, lastErr.Error())
	}

	if err := os.WriteFile(cachePath, []byte(strings.Join(names, "\n")), 0o600); err != nil {
		return fmt.Sprintf(`{"error":%q}`, "не удалось сохранить список: "+err.Error())
	}
	return fmt.Sprintf(`{"count":%d}`, len(names))
}

// FakeNet отдаёт подсеть подставных адресов.
func FakeNet() string { return fakeNet }

// DNSAddr — адрес, который надо объявить телефону как DNS-сервер.
func DNSAddr() string { return dnsAddr }

func parseIPs(list string) ([]net.IP, error) {
	var out []net.IP
	for _, part := range strings.Split(list, ",") {
		if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("имя не разрешилось")
	}
	return out, nil
}
