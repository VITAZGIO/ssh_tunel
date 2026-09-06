// Самопроверка — семь шагов подряд, одинаковых по смыслу на компьютере и на
// Android: имя разрешается в адрес, порт SSH отвечает, ключ принят сервером,
// проброс работает, DNS через туннель отвечает, известные сайты открываются,
// внешний адрес совпадает с адресом сервера. Первая же неудача останавливает
// цепочку — дальше проверять бессмысленно, а оставшиеся шаги помечаются
// пропущенными, чтобы экран сразу показал, докуда дошло.
//
// Соединение для проверки отдельное от уже поднятого пула (если он вообще
// есть) и закрывается по завершении — экран работает и при выключенном
// туннеле, ничего не трогая в его состоянии.
//
// Текст на экране собирает сам интерфейс (два языка на компьютере, один на
// Android — как и everywhere else в этом проекте): Go отдаёт машиночитаемый
// Code и, где уместно, Detail с сырыми данными (адрес, миллисекунды, имя
// сайта), а не готовое предложение.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/hostkey"
)

// Имена шагов — в этом порядке они и идут.
const (
	StepDNS      = "dns"
	StepPort     = "port"
	StepKey      = "key"
	StepForward  = "forward"
	StepDNSTun   = "dns_tunnel"
	StepSites    = "sites"
	StepExternal = "external_ip"
)

var stepOrder = []string{StepDNS, StepPort, StepKey, StepForward, StepDNSTun, StepSites, StepExternal}

// CheckStep — один шаг самопроверки.
type CheckStep struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"`
	// Code — машиночитаемая причина. У успешного шага тоже есть code
	// (например "resolved"), не только у неудачного.
	Code string `json:"code,omitempty"`
	// Detail — то, что не переводится: адрес, задержка, имя сайта.
	Detail string `json:"detail,omitempty"`
}

// SelfCheckOptions — настройки сервера плюс необязательные цели проверки.
// Цели можно переопределить (тесты подставляют локальные адреса вместо
// интернета); по умолчанию берутся значения из selfCheckDefaults().
type SelfCheckOptions struct {
	Config

	// DialTimeout на каждый сетевой шаг. Ноль — 10 секунд.
	DialTimeout time.Duration

	// ProbeAddr — куда сервер должен пробросить тестовое соединение (шаг 4).
	ProbeAddr string
	// DNSAddr — DNS-сервер, доступный через туннель (шаг 5).
	DNSAddr string
	// DNSProbeName — какое имя спросить у него.
	DNSProbeName string
	// KnownSites — какие сайты открывать на шаге 6 (порт 443 всегда).
	KnownSites []string
	// ExternalIPURL — сервис, отдающий текущий внешний адрес (шаг 7).
	ExternalIPURL string
}

func (o *SelfCheckOptions) setDefaults() {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 10 * time.Second
	}
	if o.ProbeAddr == "" {
		o.ProbeAddr = "1.1.1.1:443"
	}
	if o.DNSAddr == "" {
		o.DNSAddr = "1.1.1.1:53"
	}
	if o.DNSProbeName == "" {
		o.DNSProbeName = "example.com."
	}
	if o.KnownSites == nil {
		o.KnownSites = []string{"ya.ru", "google.com", "cloudflare.com"}
	}
	if o.ExternalIPURL == "" {
		o.ExternalIPURL = "https://api.ipify.org"
	}
}

// appendStep добавляет шаг; если он провалился, оставшиеся по порядку шаги
// дописываются как пропущенные. Возвращает true, если цепочку пора
// остановить, — это и есть логика "первая неудача останавливает цепочку",
// проверяемая в тестах отдельно от сети (см. selfcheck_test.go).
func appendStep(steps *[]CheckStep, s CheckStep) bool {
	*steps = append(*steps, s)
	if s.OK {
		return false
	}
	done := make(map[string]bool, len(*steps))
	for _, done1 := range *steps {
		done[done1.Name] = true
	}
	for _, name := range stepOrder {
		if !done[name] {
			*steps = append(*steps, CheckStep{Name: name, Skipped: true})
		}
	}
	return true
}

// RunSelfCheck прогоняет цепочку целиком.
func RunSelfCheck(ctx context.Context, opt SelfCheckOptions) []CheckStep {
	opt.setDefaults()
	var steps []CheckStep

	// 1. Имя разрешается в адрес.
	resolved := []string{opt.Host}
	if net.ParseIP(opt.Host) == nil {
		ctx1, cancel := context.WithTimeout(ctx, opt.DialTimeout)
		addrs, err := net.DefaultResolver.LookupHost(ctx1, opt.Host)
		cancel()
		if err != nil {
			appendStep(&steps, CheckStep{Name: StepDNS, Code: "resolve_failed", Detail: err.Error()})
			return steps
		}
		resolved = addrs
	}
	appendStep(&steps, CheckStep{Name: StepDNS, OK: true, Code: "resolved", Detail: strings.Join(resolved, ", ")})

	// 2. Порт SSH отвечает.
	addr := net.JoinHostPort(opt.Host, strconv.Itoa(opt.SSHPort))
	dialer := &net.Dialer{Timeout: opt.DialTimeout, Control: opt.ProtectSocket}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if appendStep(&steps, CheckStep{Name: StepPort, Code: classifyDialCode(err), Detail: err.Error()}) {
			return steps
		}
	}
	portMs := time.Since(start).Milliseconds()
	appendStep(&steps, CheckStep{Name: StepPort, OK: true, Code: "responded", Detail: fmt.Sprintf("%dms", portMs)})

	// 3. Ключ принят сервером.
	client, err := sshHandshake(conn, addr, opt)
	if err != nil {
		var changed *hostkey.ErrChanged
		code := classifyDialCode(err)
		if errors.As(err, &changed) {
			code = "host_key_changed"
		}
		appendStep(&steps, CheckStep{Name: StepKey, Code: code, Detail: err.Error()})
		return steps
	}
	defer client.Close()
	appendStep(&steps, CheckStep{Name: StepKey, OK: true, Code: "accepted"})

	// 4. Проброс работает: тестовое соединение через туннель.
	fstart := time.Now()
	fc, err := dialThroughClient(client, opt.DialTimeout, "tcp", opt.ProbeAddr)
	if err != nil {
		appendStep(&steps, CheckStep{Name: StepForward, Code: "forward_failed", Detail: err.Error()})
		return steps
	}
	fc.Close()
	appendStep(&steps, CheckStep{
		Name: StepForward, OK: true, Code: "ok",
		Detail: fmt.Sprintf("%dms", time.Since(fstart).Milliseconds()),
	})

	// 5. DNS через туннель отвечает.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(_ context.Context, network, _ string) (net.Conn, error) {
			return dialThroughClient(client, opt.DialTimeout, network, opt.DNSAddr)
		},
	}
	ctx5, cancel := context.WithTimeout(ctx, opt.DialTimeout)
	_, err = resolver.LookupHost(ctx5, opt.DNSProbeName)
	cancel()
	if err != nil {
		appendStep(&steps, CheckStep{Name: StepDNSTun, Code: "no_answer", Detail: err.Error()})
		return steps
	}
	appendStep(&steps, CheckStep{Name: StepDNSTun, OK: true, Code: "ok"})

	// 6. Несколько известных сайтов открываются — время ответа у каждого.
	var siteDetail []string
	anySite := false
	for _, site := range opt.KnownSites {
		s := time.Now()
		c, err := dialThroughClient(client, opt.DialTimeout, "tcp", net.JoinHostPort(site, "443"))
		if err != nil {
			siteDetail = append(siteDetail, site+":fail")
			continue
		}
		c.Close()
		anySite = true
		siteDetail = append(siteDetail, fmt.Sprintf("%s:%dms", site, time.Since(s).Milliseconds()))
	}
	if !anySite {
		appendStep(&steps, CheckStep{Name: StepSites, Code: "all_failed", Detail: strings.Join(siteDetail, ", ")})
		return steps
	}
	appendStep(&steps, CheckStep{Name: StepSites, OK: true, Code: "ok", Detail: strings.Join(siteDetail, ", ")})

	// 7. Внешний адрес совпадает с адресом сервера.
	ip, err := fetchExternalIPThroughClient(client, opt.DialTimeout, opt.ExternalIPURL)
	if err != nil {
		appendStep(&steps, CheckStep{Name: StepExternal, Code: "check_failed", Detail: err.Error()})
		return steps
	}
	matches := false
	for _, r := range resolved {
		if r == ip {
			matches = true
			break
		}
	}
	code := "mismatch"
	if matches {
		code = "matches"
	}
	appendStep(&steps, CheckStep{Name: StepExternal, OK: matches, Code: code, Detail: ip})
	return steps
}

func sshHandshake(conn net.Conn, addr string, opt SelfCheckOptions) (*ssh.Client, error) {
	key, err := os.ReadFile(opt.KeyPath)
	if err != nil {
		conn.Close()
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		conn.Close()
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            opt.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostkey.Callback(opt.KnownHostsPath, nil),
		Timeout:         opt.DialTimeout,
	}
	if err := conn.SetDeadline(time.Now().Add(opt.DialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// dialThroughClient — client.Dial с ограничением по времени: сам метод его не
// принимает, а зависшая проверка не должна подвесить экран самопроверки.
func dialThroughClient(client *ssh.Client, timeout time.Duration, network, addr string) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := client.Dial(network, addr)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("не ответил за %s", timeout)
	}
}

func fetchExternalIPThroughClient(client *ssh.Client, timeout time.Duration, url string) (string, error) {
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialThroughClient(client, timeout, network, addr)
			},
			DisableKeepAlives: true,
		},
	}
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	ip := strings.TrimSpace(string(buf[:n]))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("сервис вернул не адрес: %q", ip)
	}
	return ip, nil
}
