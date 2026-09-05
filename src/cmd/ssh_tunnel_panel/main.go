// ssh_tunnel_panel — веб-панель, которая ставится НА сам VPS (а не на
// компьютер клиента, как internal/webui) и управляет подключёнными к нему
// клиентами. Панель заводит и удаляет клиентов сама (unix-пользователь,
// пара ключей, ограниченная запись в authorized_keys — см.
// internal/panel/client_manager.go): консоль на сервере для этого не нужна.
//
// Панель смотрит в интернет, поэтому:
//   - слушает постоянный порт (по умолчанию 47823), на который можно повесить
//     домен — в отличие от internal/webui со случайным портом и токеном в
//     адресе;
//   - вход защищён логином, паролем (bcrypt) и сессией в cookie, а не
//     токеном в URL: адрес панели может увидеть кто угодно, ссылки в
//     закладках, истории браузера и логах прокси утекают легко;
//   - при переборе пароля включается растущая задержка и блокировка после
//     нескольких неудач подряд (internal/panel/limiter.go);
//   - при первом запуске, если пользователей ещё нет, программа сама заводит
//     учётку с одноразовым паролем и печатает его в журнал — заводить
//     пользователя руками было бы отдельным шагом, который легко забыть
//     сделать безопасно.
//
// Домен и TLS поддержаны двумя путями:
//   - по умолчанию панель слушает локальный адрес, а сертификат и домен —
//     забота nginx/Caddy перед ней (обычная связка reverse proxy);
//   - флагом -domain включается встроенный автосертификат Let's Encrypt
//     (golang.org/x/crypto/acme/autocert) — годится, если отдельного
//     реверс-прокси на машине нет и панель должна сама говорить по HTTPS.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"sshtunnel/internal/panel"
)

// defaultListen — только петлевой адрес: по умолчанию перед панелью должен
// стоять nginx/Caddy, который и держит домен с TLS. Наружу без обратного
// прокси панель отдаётся либо явным -listen 0.0.0.0:PORT, либо -domain
// (тогда порт вообще не имеет значения — используются :80 и :443).
const defaultListen = "127.0.0.1:47823"

func main() {
	listen := flag.String("listen", defaultListen, "адрес, который слушает панель")
	domain := flag.String("domain", "", "домен панели — включает встроенный автосертификат "+
		"Let's Encrypt и HTTPS на :443/:80; без флага панель отдаёт обычный HTTP и ждёт "+
		"TLS-терминацию на nginx/Caddy перед собой")
	dataDir := flag.String("data-dir", defaultDataDir(), "папка с учётными записями панели "+
		"и, при -domain, кешем сертификатов")
	flag.Parse()

	store, err := panel.OpenStore(filepath.Join(*dataDir, "users.json"))
	if err != nil {
		fatal("не могу открыть хранилище пользователей: %v", err)
	}
	if store.Empty() {
		if err := createFirstUser(store); err != nil {
			fatal("не могу завести первого пользователя: %v", err)
		}
	}

	if err := panel.EnsureSSHDRestrictions(); err != nil {
		fatal("не могу подготовить ограничения sshd для клиентов панели: %v", err)
	}
	clientStore, err := panel.OpenClientStore(filepath.Join(*dataDir, "clients.json"))
	if err != nil {
		fatal("не могу открыть хранилище клиентов: %v", err)
	}
	accountant := panel.NewNFTAccountant()
	if err := accountant.EnsureTables(); err != nil {
		// Не фатально: без nftables панель работает без цифр трафика (см.
		// TrafficAccountant в internal/panel/nft.go).
		log.Printf("учёт трафика недоступен: %v", err)
	}
	clients := panel.NewClientManager(clientStore, panel.NewSystemProvisioner()).
		WithTraffic(accountant).
		WithWarnf(func(format string, args ...any) { log.Printf(format, args...) })
	go syncClientsLoop(clients, accountant)

	srv := panel.NewServer(store, clients)
	handler := srv.Handler()

	if *domain != "" {
		if err := serveWithAutocert(handler, *domain, filepath.Join(*dataDir, "certs")); err != nil {
			fatal("%v", err)
		}
		return
	}

	log.Printf("Панель слушает http://%s/ (без своего TLS — жду reverse proxy перед собой)", *listen)
	if err := http.ListenAndServe(*listen, handler); err != nil {
		fatal("%v", err)
	}
}

// createFirstUser заводит единственную учётку "admin" с одноразовым паролем
// и требованием сменить его при первом входе. Пароль печатается в журнал
// (то есть в стандартный вывод — под systemd это journalctl) и нигде больше
// не сохраняется: перечитать его после первого входа уже нельзя.
func createFirstUser(store *panel.Store) error {
	const username = "admin"
	password, err := panel.GenerateOnePassword()
	if err != nil {
		return err
	}
	if err := store.CreateWithPassword(username, password, true); err != nil {
		return err
	}
	log.Printf("=================================================================")
	log.Printf("Первый запуск: создан пользователь %q с одноразовым паролем.", username)
	log.Printf("Логин:  %s", username)
	log.Printf("Пароль: %s", password)
	log.Printf("Панель потребует сменить его сразу после входа. Пароль больше нигде")
	log.Printf("не сохранён — если он потерян, удали файл пользователей и перезапусти панель.")
	log.Printf("=================================================================")
	return nil
}

// serveWithAutocert поднимает HTTPS на :443 с сертификатом, который
// autocert сам получает и продлевает у Let's Encrypt, плюс HTTP на :80 —
// он нужен и для ACME HTTP-01 challenge, и чтобы вежливо перенаправлять
// обычные http-запросы на https.
func serveWithAutocert(handler http.Handler, domain, cacheDir string) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("не могу создать папку кеша сертификатов: %w", err)
	}
	mgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache(cacheDir),
	}

	httpSrv := &http.Server{
		Addr:              ":80",
		Handler:           mgr.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP-обработчик ACME на :80 упал: %v", err)
		}
	}()

	httpsSrv := &http.Server{
		Addr:              ":443",
		Handler:           handler,
		TLSConfig:         mgr.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("Панель слушает https://%s/ (сертификат Let's Encrypt получается автоматически)", domain)
	if err := httpsSrv.ListenAndServeTLS("", ""); err != nil {
		return fmt.Errorf("https-сервер: %w", err)
	}
	return nil
}

// syncClientsLoop обновляет счётчики трафика и число живых сессий клиентов
// раз в syncInterval. Ошибки (нет nftables, /proc недоступен) пишутся в
// журнал один раз за сбой и не останавливают цикл — список клиентов в этом
// случае просто перестаёт обновлять соответствующие цифры, а не ломает
// работу панели целиком (см. пакет internal/panel, TrafficAccountant).
func syncClientsLoop(clients *panel.ClientManager, accountant panel.TrafficAccountant) {
	const syncInterval = 15 * time.Second
	tick := time.NewTicker(syncInterval)
	defer tick.Stop()
	for range tick.C {
		if err := clients.SyncTraffic(accountant); err != nil {
			log.Printf("не удалось обновить трафик клиентов: %v", err)
		}
		if err := clients.SyncOnline(panel.ProcRoot); err != nil {
			log.Printf("не удалось обновить список подключённых клиентов: %v", err)
		}
	}
}

func defaultDataDir() string {
	return "/etc/ssh_tunnel_panel"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Ошибка: "+format+"\n", args...)
	os.Exit(1)
}
