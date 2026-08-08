package tunnel

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

// HTTP-прокси — вторая точка входа наряду с SOCKS, и именно она закрывает две
// дыры, которых SOCKS не закрывал.
//
//  1. Программы вне экосистемы Windows. Node.js, Python, Go, curl и всё, что на
//     них написано (в том числе Claude Code), системный прокси Windows из
//     реестра НЕ читают вовсе — они смотрят на переменные среды
//     HTTP_PROXY/HTTPS_PROXY и понимают там только схему http://. Без этого
//     прокси такие программы идут в интернет напрямую, мимо туннеля.
//
//  2. Утечка DNS. В методе CONNECT клиент передаёт ИМЯ хоста
//     ("CONNECT api.anthropic.com:443"), а не адрес. Значит имя разрешает VPS,
//     и провайдер не видит DNS-запросов. При SOCKS многие приложения резолвят
//     имя сами, до обращения к прокси, и такой запрос уходит через провайдера.
func (t *Tunnel) handleHTTP(conn net.Conn) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		conn.Close()
		return
	}

	if req.Method == http.MethodConnect {
		t.httpConnect(conn, br, req)
		return
	}
	t.httpForward(conn, br, req)
}

// httpConnect обслуживает HTTPS: клиент просит открыть TCP до host:port, а
// дальше сам гоняет по нему TLS. Содержимое мы не видим и не трогаем.
func (t *Tunnel) httpConnect(conn net.Conn, br *bufio.Reader, req *http.Request) {
	target := req.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}

	t.serve(request{
		conn:   conn,
		target: target,
		proto:  "http-connect",
		byIP:   isIPHost(target),
		// Читаем через br, а не через conn: клиент мог прислать начало
		// TLS-рукопожатия сразу за заголовком CONNECT, и эти байты уже лежат
		// в буфере bufio — при чтении напрямую из conn они бы потерялись.
		src: br,
		writeErr: func(err error) {
			fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\nvpstunnel: %s\r\n", shortErr(err))
		},
		writeOK: func(c net.Conn) error {
			_, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
			return err
		},
	})
}

// httpForward обслуживает обычный http:// — здесь запрос приходит целиком, и
// его надо переписать из «прокси-формы» (GET http://site/path) в обычную
// (GET /path) перед отправкой на сервер.
func (t *Tunnel) httpForward(conn net.Conn, br *bufio.Reader, req *http.Request) {
	defer conn.Close()

	if req.URL == nil || req.URL.Host == "" {
		io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
		return
	}
	target := req.URL.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "80")
	}

	proc, pid := lookupProcess(conn)
	remote, err := t.Dial("tcp", target)
	if err != nil {
		t.bus.Publish(eventConn(proc, pid, target, "http", false, err))
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nvpstunnel: %s\r\n", shortErr(err))
		return
	}
	defer remote.Close()

	t.bus.Publish(eventConn(proc, pid, target, "http", isIPHost(target), nil))
	t.stats.active.Add(1)
	t.stats.total.Add(1)
	defer t.stats.active.Add(-1)

	// Hop-by-hop заголовки адресованы прокси, а не серверу, дальше не идут.
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	if err := req.Write(remote); err != nil {
		return
	}
	// Остальное (тело, следующие запросы пайплайна, ответ) гоняем без разбора,
	// читая клиента через br — в нём мог остаться хвост.
	t.pump(conn, br, remote)
}

func isIPHost(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	return net.ParseIP(h) != nil
}
