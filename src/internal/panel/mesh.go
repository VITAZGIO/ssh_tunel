package panel

// Сеть устройств на сервере: панель ставит и настраивает meshd, показывает
// подключённые устройства и связывает несколько серверов в одну сеть.
//
// Роли сервера:
//   - главный (main) — здесь работает meshd, к нему сходятся все устройства и
//     все побочные серверы; здесь видно всю сеть;
//   - побочный (secondary) — meshd здесь не нужен: его 127.0.0.1:47831 — это
//     SSH-проброс к главному (служба meshsvc.UplinkUnitName). Устройства,
//     подключённые к побочному серверу, ничего об этом не знают и попадают в
//     ту же сеть, что и подключённые к главному;
//   - выключено.
//
// Как подключить побочный сервер. На главном «Добавить сервер» — панель
// заводит для него пару ключей и выдаёт код подключения: адрес и SSH-ключи
// главного, закрытый ключ побочного. На побочном этот код вставляется один
// раз — панель сама пишет ключи и поднимает проброс. Ключ побочного на
// главном ограничен пробросом до meshd (meshsvc.LinkKeyOptions и
// meshsvc.LinkSSHDBlock) — ни оболочки, ни других адресов.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// MeshSystem — всё, что сеть устройств делает с системой. Настоящая
// реализация — systemMesh (mesh_system.go), в тестах — подделка.
type MeshSystem interface {
	// UnitState — состояние службы systemd.
	UnitState(unit string) UnitState
	// RunScript выполняет скрипт bash от root, построчно отдавая вывод.
	RunScript(script string, onLine func(string)) error
	// InstallUnit пишет юнит, включает его и перезапускает.
	InstallUnit(name, text string) error
	// DisableUnit останавливает и выключает службу (нет её — не ошибка).
	DisableUnit(unit string) error
	// UnitLog — последние строки журнала службы.
	UnitLog(unit string, n int) []string

	// EnsureLinkUser заводит пользователя meshsvc.LinkUser, если его нет.
	EnsureLinkUser() error
	// WriteLinkKeys заменяет его authorized_keys (пользователя нет — не ошибка
	// для пустого списка).
	WriteLinkKeys(lines []string) error
	// KillLinkSessions рвёт соединения побочных серверов: после удаления
	// ключа уже открытые соединения сами не закроются.
	KillLinkSessions() error
	// EnsureLinkSSHD дописывает в sshd_config ограничения для meshlink.
	EnsureLinkSSHD() error

	// HostKeys — открытые SSH-ключи этого сервера ("тип ключ").
	HostKeys() []string
	// LocalAddrs — адреса этого сервера.
	LocalAddrs() []string
}

// UnitState — состояние службы systemd.
type UnitState struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Enabled   bool   `json:"enabled"`
	Detail    string `json:"detail,omitempty"`
}

// meshAdmin — вход meshd для панели (meshsvc.Admin; в тестах — подделка).
type meshAdmin interface {
	State(ctx context.Context) (meshsvc.State, error)
	Rename(ctx context.Context, netID, device, alias string) error
	Forget(ctx context.Context, netID, device string) error
}

const meshdUnit = "meshd.service"

// MeshManager — сеть устройств в панели.
type MeshManager struct {
	settings *SettingsStore
	sys      MeshSystem
	admin    meshAdmin
	dataDir  string
	version  string

	// sshHost/sshPort — адрес этого сервера, как его вводят устройства.
	sshHost string
	sshPort int

	// lookup — поиск адресов по имени (подменяется в тестах).
	lookup func(ctx context.Context, host string) ([]string, error)
	// linkAddr — куда побочный сервер подключается к главному; пустой —
	// meshsvc.Port на 127.0.0.1, то есть проброс (подменяется в тестах).
	linkAddr string

	mu         sync.Mutex
	job        *meshJob
	link       *meshsvc.ServerLink
	linkCancel context.CancelFunc
	resolved   map[string]resolvedHost
}

type resolvedHost struct {
	addrs []string
	until time.Time
}

// NewMeshManager собирает сеть устройств панели.
func NewMeshManager(settings *SettingsStore, sys MeshSystem, admin meshAdmin, dataDir, version string) *MeshManager {
	return &MeshManager{
		settings: settings, sys: sys, admin: admin, dataDir: dataDir, version: version,
		lookup:   net.DefaultResolver.LookupHost,
		resolved: map[string]resolvedHost{},
	}
}

// WithSSH задаёт адрес этого сервера для устройств.
func (m *MeshManager) WithSSH(host string, port int) *MeshManager {
	m.sshHost, m.sshPort = host, port
	return m
}

// Start — при запуске панели: побочный сервер сразу держит связь с главным.
func (m *MeshManager) Start() {
	if s := m.settings.Get().Mesh; s.Role == MeshRoleSecondary {
		m.startLink(s)
	}
}

// Stop — при остановке панели.
func (m *MeshManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLinkLocked()
}

// ---------- задания (установка и обновление) ----------

// meshJob — долгое действие (установка meshd), за которым следит интерфейс.
type meshJob struct {
	Kind     string    `json:"kind"`
	Running  bool      `json:"running"`
	Lines    []string  `json:"lines"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

const maxJobLines = 300

var errJobRunning = errors.New("уже идёт установка — дождись её окончания")

func (m *MeshManager) runJob(kind string, fn func(onLine func(string)) error) error {
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	job := &meshJob{Kind: kind, Running: true, Started: time.Now()}
	m.job = job
	m.mu.Unlock()

	onLine := func(line string) {
		m.mu.Lock()
		job.Lines = append(job.Lines, line)
		if len(job.Lines) > maxJobLines {
			job.Lines = job.Lines[len(job.Lines)-maxJobLines:]
		}
		m.mu.Unlock()
	}
	go func() {
		err := fn(onLine)
		m.mu.Lock()
		job.Running = false
		job.Finished = time.Now()
		if err != nil {
			job.Error = err.Error()
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *MeshManager) jobSnapshot() *meshJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	j := *m.job
	j.Lines = append([]string(nil), m.job.Lines...)
	return &j
}

// ---------- роли ----------

// SetMain делает сервер главным: ставит (или обновляет) meshd и готовит вход
// для побочных серверов.
func (m *MeshManager) SetMain(name string) error {
	name = cleanName(name)
	if err := m.settings.Update(func(s *Settings) error {
		s.Mesh.Role = MeshRoleMain
		if name != "" {
			s.Mesh.Name = name
		}
		return nil
	}); err != nil {
		return err
	}
	m.mu.Lock()
	m.stopLinkLocked()
	m.mu.Unlock()
	return m.runJob("install", m.installMain)
}

// Reinstall — обновить meshd до версии из релиза.
func (m *MeshManager) Reinstall() error {
	if m.settings.Get().Mesh.Role != MeshRoleMain && !m.sys.UnitState(meshdUnit).Installed {
		return errors.New("сначала выбери роль «главный сервер»")
	}
	return m.runJob("install", m.installMain)
}

func (m *MeshManager) installMain(onLine func(string)) error {
	onLine("Ставлю meshd — службу сети устройств")
	if err := m.sys.RunScript(meshsvc.InstallScript(), onLine); err != nil {
		return err
	}
	onLine("Готовлю вход для побочных серверов (пользователь " + meshsvc.LinkUser + ")")
	if err := m.syncLinks(); err != nil {
		return fmt.Errorf("вход для побочных серверов: %w", err)
	}
	onLine("Готово: сеть устройств работает")
	return nil
}

// syncLinks приводит пользователя meshlink и его ключи к списку в настройках.
func (m *MeshManager) syncLinks() error {
	links := m.settings.Get().Mesh.Links
	if err := m.sys.EnsureLinkUser(); err != nil {
		return err
	}
	if err := m.sys.EnsureLinkSSHD(); err != nil {
		return err
	}
	lines := make([]string, 0, len(links))
	for _, l := range links {
		lines = append(lines, meshsvc.LinkAuthorizedKey(l.PublicKey, l.Name))
	}
	return m.sys.WriteLinkKeys(lines)
}

// Disable выключает сеть устройств на этом сервере. Данные meshd (адреса
// устройств) и список побочных серверов остаются — включишь снова, всё
// вернётся как было.
func (m *MeshManager) Disable() error {
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	m.stopLinkLocked()
	m.mu.Unlock()

	var errs []error
	errs = append(errs, m.sys.DisableUnit(meshdUnit), m.sys.DisableUnit(meshsvc.UplinkUnitName))
	// Ключи побочных серверов убираем: пускать их больше некуда.
	errs = append(errs, m.sys.WriteLinkKeys(nil), m.sys.KillLinkSessions())
	if err := m.settings.Update(func(s *Settings) error {
		s.Mesh.Role = MeshRoleOff
		return nil
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ---------- побочные серверы (на главном) ----------

// meshInvite — код подключения побочного сервера.
type meshInvite struct {
	V        int      `json:"v"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Key      string   `json:"key"`
	HostKeys []string `json:"hostKeys,omitempty"`
}

const invitePrefix = "sshtunnel-link1:"

func encodeInvite(inv meshInvite) string {
	data, _ := json.Marshal(inv)
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

func decodeInvite(code string) (meshInvite, error) {
	var inv meshInvite
	code = strings.Join(strings.Fields(code), "") // переносы строк при копировании
	if !strings.HasPrefix(code, invitePrefix) {
		return inv, errors.New("это не код подключения сервера — он начинается с " + invitePrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, invitePrefix))
	if err != nil {
		return inv, errors.New("код подключения повреждён — скопируй его целиком")
	}
	if err := json.Unmarshal(data, &inv); err != nil {
		return inv, errors.New("код подключения повреждён — скопируй его целиком")
	}
	if inv.ID == "" || inv.Key == "" || validHost(inv.Host) != nil || inv.Port <= 0 || inv.Port > 65535 {
		return inv, errors.New("в коде подключения не хватает данных")
	}
	if _, err := ssh.ParsePrivateKey([]byte(inv.Key)); err != nil {
		return inv, errors.New("в коде подключения испорчен ключ")
	}
	return inv, nil
}

// validHost — адрес сервера пойдёт в юнит systemd и known_hosts: только
// имя или адрес, без пробелов и спецсимволов.
func validHost(h string) error {
	if h == "" || len(h) > 253 {
		return errors.New("нужен адрес сервера")
	}
	for _, r := range h {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == ':' || r == '_'
		if !ok {
			return fmt.Errorf("в адресе сервера недопустимый символ %q", r)
		}
	}
	if strings.HasPrefix(h, "-") {
		return errors.New("адрес сервера не может начинаться с «-»")
	}
	return nil
}

// cleanName — имя сервера для людей.
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 48 {
		s = string(r[:48])
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateLink заводит побочный сервер и возвращает код подключения для него.
// host/port — адрес ЭТОГО (главного) сервера, куда будет подключаться
// побочный; пустой — тот, что задан панели при запуске.
func (m *MeshManager) CreateLink(name, host string, port int) (MeshLink, string, error) {
	if m.settings.Get().Mesh.Role != MeshRoleMain {
		return MeshLink{}, "", errors.New("подключать другие серверы может только главный")
	}
	name = cleanName(name)
	if name == "" {
		return MeshLink{}, "", errors.New("дай серверу имя — например, по стране: «Германия»")
	}
	if host == "" {
		host = m.sshHost
	}
	if port == 0 {
		port = m.sshPort
	}
	if port == 0 {
		port = 22
	}
	if err := validHost(host); err != nil {
		return MeshLink{}, "", errors.New("укажи адрес этого сервера — по нему к нему подключится побочный")
	}
	if port < 1 || port > 65535 {
		return MeshLink{}, "", errors.New("неверный порт SSH")
	}
	pub, priv, err := generateKeyPair()
	if err != nil {
		return MeshLink{}, "", err
	}
	link := MeshLink{ID: randomHex(8), Name: name, PublicKey: pub, Added: time.Now().UTC()}
	if err := m.settings.Update(func(s *Settings) error {
		for _, l := range s.Mesh.Links {
			if strings.EqualFold(l.Name, name) {
				return fmt.Errorf("сервер с именем «%s» уже есть", name)
			}
		}
		if len(s.Mesh.Links) >= 32 {
			return errors.New("слишком много серверов")
		}
		s.Mesh.Links = append(s.Mesh.Links, link)
		return nil
	}); err != nil {
		return MeshLink{}, "", err
	}
	if err := m.syncLinks(); err != nil {
		return link, "", fmt.Errorf("сервер добавлен, но ключ не записан: %w", err)
	}
	code := encodeInvite(meshInvite{
		V: 1, ID: link.ID, Name: name, Host: host, Port: port, Key: priv, HostKeys: m.sys.HostKeys(),
	})
	return link, code, nil
}

// DeleteLink отключает побочный сервер: ключ убирается, соединение рвётся.
func (m *MeshManager) DeleteLink(id string) error {
	found := false
	if err := m.settings.Update(func(s *Settings) error {
		out := s.Mesh.Links[:0]
		for _, l := range s.Mesh.Links {
			if l.ID == id {
				found = true
				continue
			}
			out = append(out, l)
		}
		s.Mesh.Links = out
		return nil
	}); err != nil {
		return err
	}
	if !found {
		return errors.New("нет такого сервера")
	}
	if err := m.syncLinks(); err != nil {
		return err
	}
	// Рвутся все побочные — остальные переподключатся сами за секунды.
	return m.sys.KillLinkSessions()
}

// ---------- побочный сервер ----------

func (m *MeshManager) linkDir() string { return filepath.Join(m.dataDir, "meshlink") }

// Join делает сервер побочным по коду подключения с главного. selfHost —
// адрес этого сервера, как его вводят устройства (по нему главный поймёт,
// какие устройства пришли через него).
func (m *MeshManager) Join(code, selfHost string) error {
	inv, err := decodeInvite(code)
	if err != nil {
		return err
	}
	if selfHost == "" {
		selfHost = m.sshHost
	}
	if selfHost != "" {
		if err := validHost(selfHost); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	m.mu.Unlock()

	dir := m.linkDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(inv.Key), 0o600); err != nil {
		return err
	}
	var known []string
	for _, k := range inv.HostKeys {
		if len(strings.Fields(k)) >= 2 {
			known = append(known, meshsvc.KnownHostsLine(inv.Host, inv.Port, k))
		}
	}
	knownPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownPath, []byte(strings.Join(known, "\n")+"\n"), 0o600); err != nil {
		return err
	}

	// На одном сервере — что-то одно: свой meshd занял бы порт проброса.
	if err := m.sys.DisableUnit(meshdUnit); err != nil {
		return err
	}
	unit := meshsvc.UplinkUnit(inv.Host, inv.Port, keyPath, knownPath, len(known) > 0)
	if err := m.sys.InstallUnit(meshsvc.UplinkUnitName, unit); err != nil {
		return err
	}
	var s MeshSettings
	if err := m.settings.Update(func(st *Settings) error {
		st.Mesh.Role = MeshRoleSecondary
		st.Mesh.Name = inv.Name
		st.Mesh.MainHost = inv.Host
		st.Mesh.MainPort = inv.Port
		st.Mesh.LinkID = inv.ID
		st.Mesh.Host = selfHost
		s = st.Mesh
		return nil
	}); err != nil {
		return err
	}
	m.startLink(s)
	return nil
}

func (m *MeshManager) startLink(s MeshSettings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLinkLocked()
	host := s.Host
	if host == "" {
		host = m.sshHost
	}
	link := &meshsvc.ServerLink{Addr: m.linkAddr, Info: meshsvc.ServerInfo{
		ID: s.LinkID, Name: s.Name, Host: host, App: m.version, Addrs: m.sys.LocalAddrs(),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	m.link, m.linkCancel = link, cancel
	go link.Run(ctx)
}

func (m *MeshManager) stopLinkLocked() {
	if m.linkCancel != nil {
		m.linkCancel()
	}
	m.link, m.linkCancel = nil, nil
}

// ---------- состояние для интерфейса ----------

// MeshDevice — устройство с тем, через какой сервер оно подключено.
type MeshDevice struct {
	meshsvc.Device
	// Server — id сервера из MeshState.Servers, через который пришло
	// устройство ("main" — этот сервер).
	Server string `json:"server"`
}

// MeshNetwork — сеть с устройствами.
type MeshNetwork struct {
	ID      string       `json:"id"`
	Devices []MeshDevice `json:"devices"`
}

// MeshServer — сервер на схеме сети.
type MeshServer struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Host   string          `json:"host,omitempty"`
	Main   bool            `json:"main,omitempty"`
	Online bool            `json:"online"`
	Known  bool            `json:"known"` // добавлен в панели (а не подключился сам)
	Added  time.Time       `json:"added,omitempty"`
	Link   *meshsvc.Server `json:"link,omitempty"`
}

// MeshState — всё, что показывает вкладка «Сеть устройств».
type MeshState struct {
	Role          string    `json:"role"`
	EffectiveRole string    `json:"effectiveRole"`
	Name          string    `json:"name"`
	Meshd         UnitState `json:"meshd"`
	Uplink        UnitState `json:"uplink"`
	Job           *meshJob  `json:"job,omitempty"`
	SSHHost       string    `json:"sshHost,omitempty"`
	SSHPort       int       `json:"sshPort,omitempty"`

	// Главный.
	Version   string        `json:"version,omitempty"`
	StartedAt int64         `json:"startedAt,omitempty"`
	Servers   []MeshServer  `json:"servers"`
	Networks  []MeshNetwork `json:"networks"`
	AdminErr  string        `json:"adminError,omitempty"`

	// Побочный.
	MainHost string             `json:"mainHost,omitempty"`
	MainPort int                `json:"mainPort,omitempty"`
	SelfHost string             `json:"selfHost,omitempty"`
	LinkInfo *meshsvc.LinkState `json:"link,omitempty"`
	Log      []string           `json:"log,omitempty"`
}

// State собирает состояние сети для интерфейса.
func (m *MeshManager) State(ctx context.Context) MeshState {
	s := m.settings.Get().Mesh
	st := MeshState{
		Role:    s.Role,
		Name:    s.Name,
		Meshd:   m.sys.UnitState(meshdUnit),
		Uplink:  m.sys.UnitState(meshsvc.UplinkUnitName),
		Job:     m.jobSnapshot(),
		SSHHost: m.sshHost,
		SSHPort: m.sshPort,

		Servers:  []MeshServer{},
		Networks: []MeshNetwork{},
	}
	st.EffectiveRole = s.Role
	// meshd поставлен мастером настройки VPS, а в панели роль не выбрана —
	// по сути это уже главный сервер.
	if s.Role == MeshRoleOff && st.Meshd.Active {
		st.EffectiveRole = MeshRoleMain
	}

	switch st.EffectiveRole {
	case MeshRoleMain:
		m.fillMain(ctx, &st, s)
	case MeshRoleSecondary:
		st.MainHost, st.MainPort, st.SelfHost = s.MainHost, s.MainPort, s.Host
		m.mu.Lock()
		if m.link != nil {
			ls := m.link.State()
			st.LinkInfo = &ls
		}
		m.mu.Unlock()
		st.Log = m.sys.UnitLog(meshsvc.UplinkUnitName, 15)
	}
	return st
}

func (m *MeshManager) fillMain(ctx context.Context, st *MeshState, s MeshSettings) {
	name := s.Name
	if name == "" {
		name = "Главный сервер"
	}
	st.Servers = append(st.Servers, MeshServer{ID: "main", Name: name, Host: m.sshHost, Main: true, Online: st.Meshd.Active, Known: true})

	ms, err := m.admin.State(ctx)
	if err != nil {
		st.AdminErr = err.Error()
	}
	st.Version, st.StartedAt = ms.Version, ms.StartedAt

	connected := map[string]meshsvc.Server{}
	for _, r := range ms.Servers {
		connected[r.ID] = r
	}
	for _, l := range s.Links {
		srv := MeshServer{ID: l.ID, Name: l.Name, Known: true, Added: l.Added}
		if r, ok := connected[l.ID]; ok {
			r := r
			srv.Online, srv.Link, srv.Host = true, &r, r.Host
			delete(connected, l.ID)
		}
		st.Servers = append(st.Servers, srv)
	}
	// Подключились сами, без кода из панели (настроены руками).
	for _, r := range ms.Servers {
		if _, ok := connected[r.ID]; ok {
			r := r
			st.Servers = append(st.Servers, MeshServer{ID: r.ID, Name: r.Name, Host: r.Host, Online: true, Link: &r})
		}
	}

	matcher := m.serverMatcher(ctx, st.Servers)
	for _, n := range ms.Networks {
		mn := MeshNetwork{ID: n.ID, Devices: make([]MeshDevice, 0, len(n.Devices))}
		for _, d := range n.Devices {
			mn.Devices = append(mn.Devices, MeshDevice{Device: d, Server: matcher(d.Via)})
		}
		st.Networks = append(st.Networks, mn)
	}
}

// serverMatcher — по адресу сервера из профиля устройства (via) узнаёт, через
// какой сервер оно подключено. Устройство вводит адрес по-своему (имя или
// IP), поэтому сравниваются адреса после разрешения имён. Не узналось —
// считаем, что через главный.
func (m *MeshManager) serverMatcher(ctx context.Context, servers []MeshServer) func(via string) string {
	type cand struct {
		id    string
		addrs map[string]bool
	}
	var cands []cand
	for _, s := range servers {
		set := map[string]bool{}
		add := func(h string) {
			if h == "" {
				return
			}
			set[strings.ToLower(h)] = true
			for _, a := range m.resolve(ctx, h) {
				set[a] = true
			}
		}
		add(s.Host)
		if s.Main {
			for _, a := range m.sys.LocalAddrs() {
				set[a] = true
			}
		}
		if s.Link != nil {
			for _, a := range s.Link.Addrs {
				add(a)
			}
		}
		cands = append(cands, cand{s.ID, set})
	}
	return func(via string) string {
		if via == "" {
			return "main"
		}
		keys := append([]string{strings.ToLower(via)}, m.resolve(ctx, via)...)
		// Сначала побочные: адрес главного устройство вводит чаще, но
		// совпадение с побочным — более точный ответ.
		for i := len(cands) - 1; i >= 0; i-- {
			for _, k := range keys {
				if cands[i].addrs[k] {
					return cands[i].id
				}
			}
		}
		return "main"
	}
}

// resolve — адреса имени с кешем на 10 минут; IP возвращается как есть.
func (m *MeshManager) resolve(ctx context.Context, host string) []string {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}
	}
	m.mu.Lock()
	r, ok := m.resolved[host]
	m.mu.Unlock()
	if ok && time.Now().Before(r.until) {
		return r.addrs
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := m.lookup(lctx, host)
	if err != nil {
		addrs = nil
	}
	m.mu.Lock()
	if len(m.resolved) > 512 {
		clear(m.resolved)
	}
	m.resolved[host] = resolvedHost{addrs: addrs, until: time.Now().Add(10 * time.Minute)}
	m.mu.Unlock()
	return addrs
}

// Rename переименовывает устройство (пустое имя — вернуть его собственное).
func (m *MeshManager) Rename(ctx context.Context, netID, device, alias string) error {
	return m.admin.Rename(ctx, netID, device, alias)
}

// Forget убирает устройство из сети.
func (m *MeshManager) Forget(ctx context.Context, netID, device string) error {
	return m.admin.Forget(ctx, netID, device)
}
