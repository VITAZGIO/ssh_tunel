package winsvc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

// Proxy — посредник между страницей в окне и веб-интерфейсом службы (reverse
// proxy: принимает запросы и пересылает их дальше). Зачем он:
//   - адрес и ключ службы меняются при каждом её перезапуске — прокси берёт
//     свежие из service.json, и открытое окно не ломается;
//   - некоторые запросы должны выполниться на рабочем столе пользователя
//     (выбор файла, открытие PowerShell), а у службы его нет, — их прокси
//     отдаёт обработчикам в процессе окна (Local).
//
// У страницы свой ключ (Token), не ключ службы: служба о нём не знает, а
// прокси подставляет её текущий.
type Proxy struct {
	Dir   string // папка настроек, где лежит service.json
	Token string
	// Local — запросы, которые выполняет само окно, а не служба.
	Local map[string]http.HandlerFunc

	mu    sync.Mutex
	ep    Endpoint
	epMod time.Time

	rp *httputil.ReverseProxy
}

// NewProxy готовит прокси со случайным ключом страницы.
func NewProxy(dir string, local map[string]http.HandlerFunc) *Proxy {
	tok := make([]byte, 16)
	rand.Read(tok)
	p := &Proxy{Dir: dir, Token: hex.EncodeToString(tok), Local: local}
	p.rp = &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		FlushInterval: -1, // поток событий (/events) — без задержек
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.Forget()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "служба ssh_tunnel VPN не отвечает: " + err.Error()})
		},
	}
	return p
}

// Endpoint — где сейчас служба. Файл перечитывается, только если изменился.
func (p *Proxy) Endpoint() Endpoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, err := os.Stat(EndpointPath(p.Dir)); err == nil && !st.ModTime().Equal(p.epMod) {
		if ep, err := ReadEndpoint(p.Dir); err == nil {
			p.ep, p.epMod = ep, st.ModTime()
		}
	}
	return p.ep
}

// Forget — перечитать service.json при следующем запросе, даже если время
// изменения файла то же (служба могла перезапуститься в ту же секунду).
func (p *Proxy) Forget() {
	p.mu.Lock()
	p.epMod = time.Time{}
	p.mu.Unlock()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Сама страница открыта всем, кто знает адрес (как и у службы), а всё
	// остальное — только со своим ключом и не с чужого сайта.
	if r.URL.Path != "/" {
		if !p.allowed(r) {
			http.Error(w, "запрещено", http.StatusForbidden)
			return
		}
		if h, ok := p.Local[r.URL.Path]; ok {
			h(w, r)
			return
		}
	}
	p.rp.ServeHTTP(w, r)
}

// rewrite отправляет запрос службе с её текущим ключом вместо ключа окна.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	ep := p.Endpoint()
	pr.SetURL(&url.URL{Scheme: "http", Host: ep.Addr})
	q := pr.Out.URL.Query()
	if q.Has("t") {
		q.Set("t", ep.Token)
		pr.Out.URL.RawQuery = q.Encode()
	}
	pr.Out.Header.Set("X-Token", ep.Token)
	// Origin страницы — адрес окна, а не службы. Проверку на чужой сайт уже
	// сделал allowed, служба пусть проверяет ключ.
	pr.Out.Header.Del("Origin")
}

func (p *Proxy) allowed(r *http.Request) bool {
	// Origin ставит браузер, подделать его со страницы нельзя: это и есть
	// защита от чужого сайта, открытого в соседней вкладке.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	tok := r.Header.Get("X-Token")
	if tok == "" {
		tok = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(p.Token)) == 1
}
