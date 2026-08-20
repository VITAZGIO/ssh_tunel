package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Проверка сайтов — ответ на вопрос «это туннель не может или программа ходит
// мимо него».
//
// Вопрос не праздный: жалоба почти всегда звучит как «сайт не открывается», и
// по ней невозможно понять, где именно оборвалось. Браузер мог вообще не пойти
// в туннель — его настройки прокси перебивает и расширение, и встроенный
// «ускоритель», и остатки прошлой такой же программы. Снаружи это выглядит
// ровно так же, как отказ на стороне сервера.
//
// Проверка идёт своими руками, через туннель, мимо любых системных настроек.
// Открылось здесь, а в браузере нет — значит дело в браузере, и дальше искать
// надо там.

// SiteCheck — один сайт и что с ним.
type SiteCheck struct {
	Name string `json:"name"`
	Host string `json:"host"`
	OK   bool   `json:"ok"`
	MS   int64  `json:"ms"`
	Why  string `json:"why"`
}

// checkSites — то, что проверяется. Список короткий намеренно: он должен
// отвечать на вопрос за пять секунд, а не обходить весь интернет.
//
// Три первых — то, ради чего туннель и нужен. Четвёртый работает почти везде
// и служит опорой: если не открылся и он, дело не в блокировках, а в самом
// туннеле.
var checkSites = []struct{ name, host string }{
	{"Claude", "claude.ai:443"},
	{"YouTube", "www.youtube.com:443"},
	{"Telegram", "web.telegram.org:443"},
	{"Google — опорная точка", "www.google.com:443"},
}

// CheckSites открывает каждый сайт через туннель и говорит по каждому, вышло
// или нет.
func (a *App) CheckSites() ([]SiteCheck, error) {
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun == nil {
		return nil, errors.New("туннель не запущен")
	}

	out := make([]SiteCheck, len(checkSites))
	var wg sync.WaitGroup
	for i, site := range checkSites {
		wg.Add(1)
		go func(i int, name, host string) {
			defer wg.Done()
			out[i] = probe(tun.Dial, name, host)
		}(i, site.name, site.host)
	}
	wg.Wait()

	// Сначала неудачи: главное человек должен увидеть в первой строке.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return !out[i].OK
		}
		return false
	})
	return out, nil
}

// probeTimeout — сколько ждать один сайт. Пятнадцать секунд, как у остальных
// проверок: меньше — и медленный канал попадёт в «не работает» без причины.
const probeTimeout = 15 * time.Second

// probe доводит дело до рукопожатия TLS, а не ограничивается установкой
// соединения. Разница существенная: TCP до сайта открывается и там, где ответ
// подменён, а вот назваться нужным именем подменённая сторона не может.
func probe(dial func(network, target string) (net.Conn, error), name, host string) SiteCheck {
	c := SiteCheck{Name: name, Host: host}
	start := time.Now()

	conn, err := dial("tcp", host)
	if err != nil {
		c.Why = "не открылось соединение: " + short(err)
		return c
	}
	defer conn.Close()

	server, _, err := net.SplitHostPort(host)
	if err != nil {
		server = host
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	tlsConn := tls.Client(conn, &tls.Config{ServerName: server})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		c.Why = "соединение открылось, но сайт не ответил: " + short(err)
		return c
	}
	tlsConn.Close()

	c.OK = true
	c.MS = time.Since(start).Milliseconds()
	c.Why = "открывается через туннель"
	return c
}

// short убирает из ошибки служебные подробности: человеку нужна суть, а не
// адрес и номер порта, которые он и так видит в строке рядом.
func short(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && i < len(s)-2 {
		s = s[i+2:]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// SitesVerdict — вывод одной строкой, который и надо прочитать первым.
func SitesVerdict(checks []SiteCheck) string {
	bad := 0
	for _, c := range checks {
		if !c.OK {
			bad++
		}
	}
	switch {
	case len(checks) == 0:
		return ""
	case bad == 0:
		return "Через туннель открывается всё. Если в браузере не открывается — " +
			"дело в браузере: он ходит мимо туннеля. Проверь расширения и его " +
			"собственные настройки прокси."
	case bad == len(checks):
		return "Не открывается ничего, включая опорную точку. Дело не в " +
			"блокировках, а в самом туннеле или в сервере."
	default:
		return "Часть сайтов не открывается через туннель — значит их не пускает " +
			"уже сервер, а не твой компьютер."
	}
}
