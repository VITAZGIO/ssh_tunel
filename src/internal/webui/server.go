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
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"vpstunnel/internal/app"
	"vpstunnel/internal/config"
	"vpstunnel/internal/events"
	"vpstunnel/internal/sysproxy"
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

func New(a *App) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	// Порт 0 — система сама выдаст свободный, чтобы окно не конфликтовало
	// с другими программами и со вторым запуском.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("не могу открыть окно программы: %w", err)
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
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал настройки: " + err.Error()})
		return
	}
	if err := s.app.SetConfig(cfg); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "config": s.app.Config()})
}

func (s *Server) handleCheckIP(w http.ResponseWriter, r *http.Request) {
	ip, err := s.app.CheckIP()
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"ip": ip})
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
