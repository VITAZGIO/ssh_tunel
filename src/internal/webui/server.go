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
	return &Server{app: a, token: hex.EncodeToString(tok), ln: ln}, nil
}

// URL — адрес, который надо открыть в браузере.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", s.ln.Addr().String(), s.token)
}

func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/events", s.guard(s.handleEvents))
	mux.HandleFunc("/api/status", s.guard(s.handleStatus))
	mux.HandleFunc("/api/start", s.guard(s.handleStart))
	mux.HandleFunc("/api/stop", s.guard(s.handleStop))
	mux.HandleFunc("/api/config", s.guard(s.handleConfig))
	mux.HandleFunc("/api/checkip", s.guard(s.handleCheckIP))
	mux.HandleFunc("/api/speedtest", s.guard(s.handleSpeedTest))
	mux.HandleFunc("/api/processes", s.guard(s.handleProcesses))
	mux.HandleFunc("/api/pickfile", s.guard(s.handlePickFile))
	mux.HandleFunc("/api/openterminal", s.guard(s.handleOpenTerminal))
	mux.HandleFunc("/api/scannet", s.guard(s.handleScanNet))
	mux.HandleFunc("/api/checksites", s.guard(s.handleCheckSites))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(s.ln)
}

func (s *Server) Close() { s.ln.Close() }

// guard пропускает только запросы с правильным токеном и без чужого Origin.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "запрещено", http.StatusForbidden)
			return
		}
		// Origin ставит браузер, подделать его со страницы нельзя. Свой
		// собственный Origin разрешаем, чужой — нет.
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.HasSuffix(origin, s.ln.Addr().String()) {
				http.Error(w, "запрещено", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
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

// handleCheckSites отвечает на вопрос «это туннель не может или программа
// ходит мимо него»: открывает несколько сайтов своими руками, через туннель.
func (s *Server) handleCheckSites(w http.ResponseWriter, r *http.Request) {
	checks, err := s.app.CheckSites()
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"sites":   checks,
		"verdict": app.SitesVerdict(checks),
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
