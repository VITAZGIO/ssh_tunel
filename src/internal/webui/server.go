// Package webui — окно программы. Внутри это маленький HTTP-сервер на
// 127.0.0.1 и страница, которую браузер открывает в режиме приложения (без
// адресной строки и вкладок), так что для пользователя это обычное окно.
//
// Почему так, а не «настоящее» окно на Win32: интерфейс получается один и тот
// же на всех системах, его можно открыть и проверить прямо при разработке, и
// он не тянет ни C-компилятор, ни внешние библиотеки — exe остаётся одним
// самодостаточным файлом.
//
// Безопасность. Сервер слушает только петлевой адрес, но этого мало: любая
// открытая в браузере страница тоже может слать запросы на 127.0.0.1 и,
// например, выключить туннель или прочитать настройки. Поэтому при запуске
// генерируется случайный токен, он же передаётся в адресе окна, и без него
// API не отвечает. Плюс проверяется заголовок Origin — чтобы запрос не мог
// прийти со стороннего сайта.
//
// Режим «открыт для локальной сети» (OpenLocal) снимает требование токена для
// тех, кто пришёл с адреса локальной сети: панель тогда открывается просто по
// адресу машины, как у домашних сервисов вроде Home Assistant. Проверка Origin
// при этом остаётся и делает основную работу — именно она отсекает чужой сайт,
// открытый во вкладке браузера. Запросы с публичных адресов без токена не
// проходят в любом режиме.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/filedialog"
	"sshtunnel/internal/procinfo"
	"sshtunnel/internal/sysproxy"
)

//go:embed assets/*
var assets embed.FS

type Server struct {
	app   *App
	token string
	ln    net.Listener
	// openLocal — пускать без токена тех, кто пришёл из локальной сети.
	openLocal bool
	// bootFlags — с какими флагами программа должна подняться при загрузке
	// машины. Записываются в файл службы по галочке «Запускать при старте
	// системы», чтобы после перезагрузки панель работала в том же режиме, а
	// не в угаданном.
	bootFlags []string

	// vpsSetup — идёт ли сейчас настройка VPS. Мастер занимает минуту-две и
	// трогает sshd на сервере; запускать второй одновременно с первым — верный
	// способ всё перепутать, поэтому второй запрос отклоняется, пока первый
	// не закончился.
	vpsSetup struct {
		mu      sync.Mutex
		running bool
	}
}

// App — то, что интерфейс умеет делать с программой.
type App = app.App

// New поднимает интерфейс на случайном свободном порту петлевого адреса —
// так окно не конфликтует ни с другими программами, ни со вторым запуском.
func New(a *App) (*Server, error) {
	return NewOn(a, "127.0.0.1:0")
}

// NewOn поднимает интерфейс на заданном адресе. Нужен серверной версии, где
// адрес должен быть постоянным, иначе его не открыть в браузере.
func NewOn(a *App, addr string) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("не могу занять адрес %s: %w", addr, err)
	}
	s := &Server{app: a, token: hex.EncodeToString(tok), ln: ln, bootFlags: []string{"-web"}}
	// Порт 0 означает «любой свободный» — такой адрес в файл службы писать
	// нечего, он всё равно будет другим при следующем запуске.
	if !strings.HasSuffix(addr, ":0") {
		s.bootFlags = append(s.bootFlags, "-web-listen", addr)
	}
	return s, nil
}

// NewOpenLocalOn — то же, но без ключа в адресе для гостей из локальной сети.
// Панель открывается по адресу машины и постоянному порту, как домашние
// сервисы: ссылку можно положить в закладки, она не меняется от запуска к
// запуску.
func NewOpenLocalOn(a *App, addr string) (*Server, error) {
	s, err := NewOn(a, addr)
	if err != nil {
		return nil, err
	}
	s.openLocal = true
	s.bootFlags = append([]string{"-web", "-web-lan"}, s.bootFlags[1:]...)
	return s, nil
}

// URL — адрес, который надо открыть в браузере.
func (s *Server) URL() string {
	addr := s.ln.Addr().String()
	if s.openLocal {
		return fmt.Sprintf("http://%s/", displayAddr(addr))
	}
	return fmt.Sprintf("http://%s/?t=%s", addr, s.token)
}

// displayAddr заменяет 0.0.0.0 на реальный адрес машины в локальной сети:
// «0.0.0.0» в браузер не вставишь, а ради этого адреса всё и затевалось.
func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
		return addr
	}
	if local := localIP(); local != "" {
		return net.JoinHostPort(local, port)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// localIP — первый адрес машины в локальной сети. Их может быть несколько
// (docker, wireguard), поэтому предпочитаем обычные домашние и офисные сети.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := n.IP.To4()
		if ip == nil || ip.IsLoopback() || !ip.IsPrivate() {
			continue
		}
		return ip.String()
	}
	return ""
}

func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/events", s.guard(s.handleEvents))
	mux.HandleFunc("/api/status", s.guard(s.handleStatus))
	mux.HandleFunc("/api/start", s.guard(s.handleStart))
	mux.HandleFunc("/api/stop", s.guard(s.handleStop))
	mux.HandleFunc("/api/config", s.guard(s.handleConfig))
	mux.HandleFunc("/api/profile/add", s.guard(s.handleProfileAdd))
	mux.HandleFunc("/api/profile/remove", s.guard(s.handleProfileRemove))
	mux.HandleFunc("/api/profile/select", s.guard(s.handleProfileSelect))
	mux.HandleFunc("/api/profile/export", s.guard(s.handleProfileExport))
	mux.HandleFunc("/api/profile/import", s.guard(s.handleProfileImport))
	mux.HandleFunc("/api/checkip", s.guard(s.handleCheckIP))
	mux.HandleFunc("/api/speedtest", s.guard(s.handleSpeedTest))
	mux.HandleFunc("/api/processes", s.guard(s.handleProcesses))
	mux.HandleFunc("/api/pickfile", s.guard(s.handlePickFile))
	mux.HandleFunc("/api/openterminal", s.guard(s.handleOpenTerminal))
	mux.HandleFunc("/api/genkey", s.guard(s.handleGenKey))
	mux.HandleFunc("/api/bootstart", s.guard(s.handleBootStart))
	mux.HandleFunc("/api/scannet", s.guard(s.handleScanNet))
	mux.HandleFunc("/api/vpssetup/start", s.guard(s.handleVpsSetupStart))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(s.ln)
}

func (s *Server) Close() { s.ln.Close() }

// guard пропускает только свои запросы: с правильным токеном либо, в открытом
// режиме, из локальной сети — и в любом случае без чужого Origin.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.allowed(r) {
			http.Error(w, "запрещено", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) allowed(r *http.Request) bool {
	// Origin ставит браузер, подделать его со страницы нельзя: это и есть
	// защита от чужого сайта, открытого в соседней вкладке.
	if !sameOrigin(r) {
		return false
	}
	tok := r.Header.Get("X-Token")
	if tok == "" {
		tok = r.URL.Query().Get("t")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1 {
		return true
	}
	return s.openLocal && isLocalClient(r.RemoteAddr)
}

// sameOrigin сверяет Origin с адресом, по которому пришёл сам запрос. Так
// проверка работает при любом адресе панели — 127.0.0.1, имя машины или адрес
// в локальной сети, — а не только при том, на котором она слушает.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Заголовка нет — запрос пришёл не со страницы: curl, приложение.
		// Такие проходят по токену, отдельная проверка тут ничего не даёт.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// isLocalClient — пришёл ли запрос из локальной сети: домашней, офисной или
// mesh-VPN (100.64.0.0/10 — NetBird, Tailscale). Публичные адреса сюда не
// попадают, им токен нужен всегда.
func isLocalClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
	}
	return false
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

type statusResp struct {
	State    string        `json:"state"`
	Running  bool          `json:"running"`
	Config   config.Config `json:"config"`
	Stats    events.Stats  `json:"stats"`
	SysProxy string        `json:"sysProxy"`
	EnvHint  []string      `json:"envHint"`
	ProxyURL string        `json:"proxyUrl"`
	// OS — на чём крутится сама программа (runtime.GOOS). Страница одна и та
	// же на Windows и на сервере под Linux, а команды создания ключа у них
	// разные (PowerShell против bash) — переключаются по этому полю.
	OS string `json:"os"`
	// SeenApps — программы, замеченные за этот запуск: из них удобно
	// собирать список фильтра, не вспоминая имена вручную.
	SeenApps []string `json:"seenApps"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, statusResp{
		State:    s.app.State(),
		Running:  s.app.Running(),
		Config:   s.app.Config(),
		Stats:    s.app.Stats(),
		SysProxy: sysproxy.Current(),
		EnvHint:  s.app.EnvHint(),
		ProxyURL: s.app.ProxyURL(),
		SeenApps: s.app.SeenApps(),
		OS:       runtime.GOOS,
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Start(); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.app.Stop()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, s.app.Config())
		return
	}
	// Начинаем с текущих настроек, а не с пустой структуры: страница шлёт
	// только те поля, что показаны на экране, и отсутствующий ключ означает
	// «не трогай», а не «выключи». Пустая структура молча гасила системный
	// прокси и переменные среды — туннель работал, но трафик шёл мимо него.
	cfg := s.app.Config()
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал настройки: " + err.Error()})
		return
	}
	note, err := s.app.SetConfig(cfg)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "config": s.app.Config(), "note": note})
}

// handleProfileAdd заводит новый сервер — как открыть вкладку «+» в браузере.
// Активным он не становится сам: подключаться к нему или нет — решает
// отдельная кнопка «Выбрать этот сервер».
func (s *Server) handleProfileAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Flag string `json:"flag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	p, err := s.app.AddProfile(req.Name, req.Flag)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "profile": p, "config": s.app.Config()})
}

// handleProfileRemove закрывает вкладку сервера. Последний сервер удалить
// нельзя — подключаться будет не к чему.
func (s *Server) handleProfileRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	note, err := s.app.RemoveProfile(req.ID)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "note": note, "config": s.app.Config()})
}

// handleProfileSelect делает сервер активным — это то, к чему подключается
// «Подключить» на главном экране. Если в этот момент был поднят туннель
// прежнего сервера, он останавливается: продолжать молча работать со старым
// адресом после явного переключения было бы неожиданно.
func (s *Server) handleProfileSelect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	note, err := s.app.SwitchProfile(req.ID)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "note": note, "config": s.app.Config()})
}

// handleScanNet отвечает на вопрос «что сломается, если включить туннель»:
// перечисляет сети этого компьютера и говорит по каждой, пойдёт ли она мимо
// туннеля.
func (s *Server) handleScanNet(w http.ResponseWriter, r *http.Request) {
	checks := s.app.ScanNetworks()
	writeJSON(w, map[string]any{
		"nets":     checks,
		"problems": app.Problems(checks),
	})
}

func (s *Server) handleCheckIP(w http.ResponseWriter, r *http.Request) {
	ip, err := s.app.CheckIP()
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"ip": ip})
}

// handleSpeedTest запускает измерение. Оно занимает около двадцати секунд, а
// ход показывается через поток событий, поэтому здесь только итог.
func (s *Server) handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	res, err := s.app.SpeedTest()
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, res)
}

// handleProcesses отдаёт список запущенных программ — из него выбираются
// приложения для фильтра, как в диспетчере задач.
func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"processes": procinfo.List()})
}

// handlePickFile показывает системный диалог выбора программы. Отмена — не
// ошибка, поэтому возвращается пустой путь без сообщения.
func (s *Server) handlePickFile(w http.ResponseWriter, r *http.Request) {
	path, err := filedialog.PickExecutable()
	if errors.Is(err, filedialog.ErrCancelled) {
		writeJSON(w, map[string]string{"path": ""})
		return
	}
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handleOpenTerminal открывает PowerShell рядом с окном. Саму команду туда
// подставить нельзя: любой способ «напечатать за пользователя» либо выполняет
// её сразу, либо ломается от раскладки и задержек. Поэтому команда кладётся в
// буфер обмена, а человеку остаётся вставить её и нажать Enter — так он ещё и
// видит, что именно выполняет.
func (s *Server) handleOpenTerminal(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeJSON(w, map[string]string{"error": "поддерживается только на Windows"})
		return
	}
	cmd := exec.Command("cmd", "/c", "start", "", "powershell", "-NoExit",
		"-Command", "Write-Host 'Вставь команду (Ctrl+V) и нажми Enter' -ForegroundColor Cyan")
	if err := cmd.Start(); err != nil {
		writeJSON(w, map[string]string{"error": "не удалось открыть PowerShell: " + err.Error()})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleGenKey — «одна кнопка» вместо двух шагов из терминала: создаёт SSH-
// ключ по указанному пути, если его там ещё нет, и в любом случае отдаёт
// открытую часть. Дальше страница сама подставляет её в готовую команду для
// сервера, так что человеку остаётся только вставить эту команду там.
func (s *Server) handleGenKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyPath string `json:"keyPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	path := strings.TrimSpace(req.KeyPath)
	if path == "" {
		path = config.DetectKeyPath()
	}
	pub, created, err := ensureKey(path)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "keyPath": path, "pubKey": pub, "created": created})
}

// handleBootStart — галочка «Запускать при старте системы». GET отдаёт, как
// сейчас обстоят дела, POST включает или выключает. Пароль, если он вообще
// понадобился, живёт ровно на время одной команды sudo: он не сохраняется, не
// попадает в журнал и не возвращается обратно на страницу.
func (s *Server) handleBootStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, currentBootState())
		return
	}
	var req struct {
		Enabled  bool   `json:"enabled"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	err := applyBoot(req.Enabled, req.Password, s.bootFlags)
	if errors.Is(err, errNeedRoot) {
		// Служба к этому моменту уже включена — не хватает только права
		// стартовать без входа в систему. Так и говорим, вместе с просьбой
		// ввести пароль.
		writeJSON(w, map[string]any{"needRoot": true, "state": currentBootState()})
		return
	}
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "state": currentBootState()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": currentBootState()})
}

// handleVpsSetupStart запускает мастер настройки VPS в фоне и сразу отвечает
// — ход работы (построчный вывод скриптов, ошибки, готовность) смотрят через
// уже существующий поток /events, событиями events.KindVpsSetup. Пароль root
// уходит прямо в runVpsSetup и не возвращается в ответе ни в каком виде.
func (s *Server) handleVpsSetupStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host         string `json:"host"`
		Port         int    `json:"port"`
		User         string `json:"user"`
		Password     string `json:"password"`
		KeyPath      string `json:"keyPath"`
		InstallPanel bool   `json:"installPanel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}

	s.vpsSetup.mu.Lock()
	if s.vpsSetup.running {
		s.vpsSetup.mu.Unlock()
		writeJSON(w, map[string]string{"error": "настройка сервера уже идёт"})
		return
	}
	s.vpsSetup.running = true
	s.vpsSetup.mu.Unlock()

	keyPath := strings.TrimSpace(req.KeyPath)
	if keyPath == "" {
		keyPath = config.DetectKeyPath()
	}
	port := req.Port
	if port <= 0 {
		port = 22
	}
	params := vpsSetupParams{
		Host:         strings.TrimSpace(req.Host),
		Port:         port,
		User:         strings.TrimSpace(req.User),
		Password:     req.Password,
		KeyPath:      keyPath,
		InstallPanel: req.InstallPanel,
	}
	go func() {
		defer func() {
			s.vpsSetup.mu.Lock()
			s.vpsSetup.running = false
			s.vpsSetup.mu.Unlock()
		}()
		runVpsSetup(s.app.Bus, params)
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleEvents — поток событий в окно (Server-Sent Events). Проще вебсокетов
// и переподключается сам, если окно перезагрузили.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "поток не поддерживается", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.app.Bus.Subscribe()
	defer unsub()

	var mu sync.Mutex
	send := func(e events.Event) bool {
		data, err := json.Marshal(e)
		if err != nil {
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Сначала отдаём историю — иначе окно, открытое позже старта, показывало
	// бы пустой лог.
	for _, e := range s.app.Bus.History() {
		if !send(e) {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if !send(e) {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
