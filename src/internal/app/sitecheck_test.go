package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// tlsServer поднимает сервер с самоподписанным сертификатом на нужное имя и
// возвращает его адрес.
func tlsServer(t *testing.T, name string) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				// Рукопожатие идёт лениво, поэтому подталкиваем его чтением.
				c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// Сайт открывается — проверка обязана это увидеть.
//
// Сертификат самоподписанный, поэтому проверка подписи не пройдёт, но нам
// важно другое: сообщение должно говорить «ответил», а не «не открылось
// соединение». Разница ровно та, ради которой проверка и написана.
func TestProbeReachesServer(t *testing.T) {
	addr := tlsServer(t, "example.test")
	dial := func(network, target string) (net.Conn, error) {
		return net.Dial(network, addr)
	}

	c := probe(dial, "Тест", "example.test:443")
	if strings.Contains(c.Why, "не открылось соединение") {
		t.Fatalf("живой сервер принят за недоступный: %s", c.Why)
	}
}

// Туннель не смог открыть соединение — это должно быть видно отдельной
// формулировкой, а не свалено в одну кучу с отказом сайта.
func TestProbeReportsDialFailure(t *testing.T) {
	dial := func(network, target string) (net.Conn, error) {
		return nil, errors.New("нет живого SSH-соединения с сервером")
	}

	c := probe(dial, "Тест", "example.test:443")
	if c.OK {
		t.Fatal("недоступный сайт помечен как рабочий")
	}
	if !strings.Contains(c.Why, "не открылось соединение") {
		t.Fatalf("непонятная причина: %s", c.Why)
	}
}

// Вывод одной строкой — то, что человек прочитает первым, и он обязан
// различать три случая.
func TestSitesVerdict(t *testing.T) {
	all := []SiteCheck{{OK: true}, {OK: true}}
	none := []SiteCheck{{OK: false}, {OK: false}}
	some := []SiteCheck{{OK: true}, {OK: false}}

	if !strings.Contains(SitesVerdict(all), "браузере") {
		t.Error("всё работает — надо отправить искать в браузере")
	}
	if !strings.Contains(SitesVerdict(none), "туннеле") {
		t.Error("не работает ничего — надо указать на туннель")
	}
	if !strings.Contains(SitesVerdict(some), "сервер") {
		t.Error("работает часть — надо указать на сервер")
	}
	if SitesVerdict(nil) != "" {
		t.Error("пустой список не должен ничего утверждать")
	}
}
