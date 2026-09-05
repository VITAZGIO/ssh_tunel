package panel

import (
	"embed"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

// Server — веб-панель управления сервером. В отличие от internal/webui она
// не полагается на то, что до неё дотянется только владелец машины: доступ
// разрешает только сессия, полученная логином и паролем.
type Server struct {
	store    *Store
	sessions *sessionManager
	limiter  *loginLimiter
	clients  *ClientManager

	// startedAt — с какого момента считать время работы панели на экране
	// статуса.
	startedAt time.Time
}

func NewServer(store *Store, clients *ClientManager) *Server {
	return &Server{
		store:     store,
		sessions:  newSessionManager(),
		limiter:   newLoginLimiter(),
		clients:   clients,
		startedAt: time.Now(),
	}
}

// Handler собирает маршруты в http.Handler — вызывающий код сам решает, как
// его подавать: напрямую (autocert) или за обратным прокси.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/session", s.handleSessionInfo)
	mux.HandleFunc("/api/change-password", s.handleChangePassword)
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/clients", s.requireAuth(s.handleClients))
	mux.HandleFunc("/api/clients/create", s.requireAuth(s.handleClientCreate))
	mux.HandleFunc("/api/clients/delete", s.requireAuth(s.handleClientDelete))
	mux.HandleFunc("/api/clients/freeze", s.requireAuth(s.handleClientFreeze))
	mux.HandleFunc("/api/clients/unfreeze", s.requireAuth(s.handleClientUnfreeze))
	mux.HandleFunc("/api/clients/disconnect", s.requireAuth(s.handleClientDisconnect))
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// currentSession достаёт сессию текущего запроса, если она есть и не
// истекла.
func (s *Server) currentSession(r *http.Request) (session, bool) {
	return s.sessions.get(cookieValue(r))
}

// requireAuth пускает дальше только с действующей сессией. Если у
// пользователя ещё стоит обязательная смена пароля — на любой другой ручке,
// кроме logout и change-password, отвечаем отдельной ошибкой: страница по
// ней покажет форму смены пароля вместо остального интерфейса.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.currentSession(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth_required"})
			return
		}
		if sess.mustChange {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "must_change_password"})
			return
		}
		next(w, r)
	}
}

// limiterKey сочетает логин и адрес — так подбор пароля к одной учётке с
// разных мест и перебор чужих логинов с одного адреса тормозятся одинаково.
func limiterKey(r *http.Request, username string) string {
	return strings.ToLower(username) + "|" + clientIP(r)
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}

	key := limiterKey(r, req.Username)
	if locked, remaining := s.limiter.Before(key); locked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":            "locked",
			"retryAfterSecond": int(remaining.Seconds()) + 1,
		})
		return
	}

	u, ok := s.store.Verify(req.Username, req.Password)
	if !ok {
		s.limiter.Fail(key)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_credentials"})
		return
	}
	s.limiter.Success(key)

	id, err := s.sessions.create(u.Username, u.MustChangePassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	setCookie(w, r, id, sessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"username":   u.Username,
		"mustChange": u.MustChangePassword,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if id := cookieValue(r); id != "" {
		s.sessions.destroy(id)
	}
	clearCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSessionInfo — есть ли сейчас действующий вход, и не пора ли сменить
// пароль. Страница спрашивает это при загрузке, чтобы решить, какой экран
// показать: логин, смену пароля или обычную панель.
func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      sess.username,
		"mustChange":    sess.mustChange,
	})
}

// handleChangePassword работает и для обычной смены пароля из настроек, и
// для обязательной смены после первого входа по одноразовому паролю — в
// обоих случаях нужна действующая сессия, но requireAuth сюда не годится: он
// как раз блокирует запросы с mustChange, а этой ручке они и нужны.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, ok := s.currentSession(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth_required"})
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password_too_short"})
		return
	}
	if _, ok := s.store.Verify(sess.username, req.CurrentPassword); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_credentials"})
		return
	}
	if err := s.store.SetPassword(sess.username, req.NewPassword); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	s.sessions.clearMustChange(sess.username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type statusResp struct {
	OS      string `json:"os"`
	Uptime  int64  `json:"uptimeSeconds"`
	Version string `json:"version"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResp{
		OS:      runtime.GOOS,
		Uptime:  int64(time.Since(s.startedAt).Seconds()),
		Version: "0.1.0",
	})
}

// handleClients отдаёт список клиентов без приватных ключей — их видно
// только по отдельной ручке для конкретного клиента (ТЗ-10).
func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	list := s.clients.List()
	if list == nil {
		list = []Client{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": list})
}

// handleClientCreate заводит нового клиента: unix-пользователя, пару
// ключей, запись в authorized_keys — по одной кнопке в панели, без
// консоли. Приватный ключ в ответе присутствует только здесь, сразу при
// создании; дальше он виден лишь по отдельному запросу конкретного клиента.
func (s *Server) handleClientCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name       string `json:"name"`
		DeviceType string `json:"deviceType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	c, err := s.clients.CreateClient(req.Name, DeviceType(req.DeviceType))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "client": c})
}

// handleClientDelete убирает unix-пользователя вместе с домашним каталогом,
// обрывает его живые сессии и стирает запись в хранилище панели.
func (s *Server) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if err := s.clients.DeleteClient(req.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClientFreeze вынимает ключ клиента из authorized_keys и обрывает
// его живые сессии — заблокированный клиент не может подключиться заново
// сам, в отличие от handleClientDisconnect.
func (s *Server) handleClientFreeze(w http.ResponseWriter, r *http.Request) {
	s.handleClientAction(w, r, s.clients.Freeze)
}

// handleClientUnfreeze возвращает ключ клиента в authorized_keys.
func (s *Server) handleClientUnfreeze(w http.ResponseWriter, r *http.Request) {
	s.handleClientAction(w, r, s.clients.Unfreeze)
}

// handleClientDisconnect обрывает живые сессии клиента, не трогая его
// ключ — клиент может подключиться заново сам сразу же.
func (s *Server) handleClientDisconnect(w http.ResponseWriter, r *http.Request) {
	s.handleClientAction(w, r, s.clients.Disconnect)
}

// handleClientAction — общий каркас для ручек, которые принимают только id
// клиента и не возвращают ничего, кроме успеха или ошибки.
func (s *Server) handleClientAction(w http.ResponseWriter, r *http.Request, action func(id string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if err := action(req.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
