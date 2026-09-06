package panel

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName = "ssh_tunnel_panel_session"
	sessionTTL = 12 * time.Hour
)

type session struct {
	username   string
	mustChange bool
	expires    time.Time
}

// sessionManager держит активные сессии в памяти. Перезапуск панели выкидывает
// всех — для панели, которая правит саму же машину, это не проблема, а
// разумное поведение по умолчанию.
type sessionManager struct {
	mu   sync.Mutex
	byID map[string]session
}

func newSessionManager() *sessionManager {
	return &sessionManager{byID: map[string]session{}}
}

func (m *sessionManager) create(username string, mustChange bool) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	m.mu.Lock()
	m.byID[id] = session{username: username, mustChange: mustChange, expires: time.Now().Add(sessionTTL)}
	m.mu.Unlock()
	return id, nil
}

func (m *sessionManager) get(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return session{}, false
	}
	if time.Now().After(s.expires) {
		delete(m.byID, id)
		return session{}, false
	}
	return s, true
}

// clearMustChange снимает пометку «обязательно смени пароль» у всех сессий
// пользователя, как только он это делает — без этого пришлось бы просить
// перелогиниться сразу после смены пароля.
func (m *sessionManager) clearMustChange(username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.byID {
		if s.username == username {
			s.mustChange = false
			m.byID[id] = s
		}
	}
}

func (m *sessionManager) destroy(id string) {
	m.mu.Lock()
	delete(m.byID, id)
	m.mu.Unlock()
}

// setCookie кладёт токен сессии в HttpOnly-cookie. Secure ставится, когда
// запрос пришёл по TLS — сам ли терминировала его панель (autocert) или это
// сказал обратный прокси заголовком X-Forwarded-Proto.
func setCookie(w http.ResponseWriter, r *http.Request, id string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func cookieValue(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// isSecure — пришёл ли запрос по https, напрямую или через обратный прокси.
// Заголовку X-Forwarded-Proto доверяем: за ним стоит nginx/Caddy, а не браузер
// пользователя — подделать его снаружи можно, только если прокси сам его не
// перезаписывает, что для типичной настройки reverse proxy не так.
func isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
