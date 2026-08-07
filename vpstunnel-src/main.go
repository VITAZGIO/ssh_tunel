// vpstunnel.exe — простой инструмент для прогона трафика Windows через
// SSH-туннель до удалённого сервера (SOCKS5), без внешних программ (ssh.exe/
// putty не нужны — весь SSH-клиент встроен). Дополнительно умеет включать/
// выключать системный прокси Windows (это покрывает браузеры и большинство
// приложений, которые используют системные сетевые настройки WinINET/WinHTTP —
// то есть заметно больше, чем «только браузер», но не 100% всех программ:
// то, что открывает свои сокеты напрямую (игры, некоторые desktop-клиенты),
// системный прокси не тронет).
//
// Использование:
//   vpstunnel.exe -host 87.58.210.143 -user root -key C:\Users\vitaz\.ssh\id_ed25519
//
// Флаги:
//   -host   адрес VPS
//   -user   пользователь SSH (по умолчанию root)
//   -key    путь к приватному ключу (по умолчанию ~/.ssh/id_ed25519)
//   -port   локальный порт SOCKS5 (по умолчанию 1080)
//   -sysproxy  включить системный прокси Windows на время работы (true/false,
//              по умолчанию true) — при выходе (Ctrl+C) прокси выключается сам
//
// Требует прав администратора ТОЛЬКО если включён -sysproxy (для правки
// системных настроек прокси всем пользователям; если запускать без него —
// админ не нужен, просто поднимется локальный SOCKS5-порт).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	flagHost     = flag.String("host", "", "адрес VPS (обязательно)")
	flagUser     = flag.String("user", "root", "пользователь SSH")
	flagKey      = flag.String("key", "", "путь к приватному ключу (по умолчанию ~/.ssh/id_ed25519)")
	flagSSHPort  = flag.Int("sshport", 22, "SSH-порт сервера")
	flagLocal    = flag.Int("port", 1080, "локальный порт SOCKS5")
	flagSysProxy = flag.Bool("sysproxy", true, "включить системный прокси Windows на время работы")
	flagElevate  = flag.Bool("elevated", false, "(внутренний флаг, не указывать вручную)")
)

func main() {
	flag.Parse()

	if *flagHost == "" {
		fmt.Println("Использование: vpstunnel.exe -host <IP сервера> [-user root] [-key путь] [-port 1080] [-sysproxy=true]")
		fmt.Println("\nПример:")
		fmt.Println(`  vpstunnel.exe -host 87.58.210.143 -user root -key C:\Users\vitaz\.ssh\id_ed25519`)
		waitEnterIfWindows()
		os.Exit(1)
	}

	if *flagKey == "" {
		home, _ := os.UserHomeDir()
		*flagKey = filepath.Join(home, ".ssh", "id_ed25519")
	}

	// Если нужен системный прокси, а прав администратора нет — перезапускаемся
	// с запросом UAC (только на Windows).
	if *flagSysProxy && runtime.GOOS == "windows" && !isElevated() {
		fmt.Println("Нужны права администратора для системного прокси — запрашиваю...")
		if err := relaunchElevated(); err != nil {
			log.Fatalf("Не удалось перезапуститься с правами администратора: %v\nПопробуй запустить .exe вручную через 'Запуск от имени администратора', либо добавь флаг -sysproxy=false", err)
		}
		return // текущий (неповышенный) процесс завершается, работает новый
	}

	key, err := os.ReadFile(*flagKey)
	if err != nil {
		log.Fatalf("Не могу прочитать приватный ключ %s: %v", *flagKey, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		log.Fatalf("Ключ повреждён или не тот формат (%s): %v", *flagKey, err)
	}

	config := &ssh.ClientConfig{
		User:            *flagUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // см. README: для полной строгости заменить на known_hosts
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", *flagHost, *flagSSHPort)
	fmt.Printf("Подключаюсь к %s по SSH...\n", addr)

	client, err := dialSSHWithRetry(addr, config)
	if err != nil {
		log.Fatalf("Не удалось подключиться: %v", err)
	}
	fmt.Println("SSH-соединение установлено.")

	listenAddr := fmt.Sprintf("127.0.0.1:%d", *flagLocal)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Не могу занять локальный порт %d: %v", *flagLocal, err)
	}
	fmt.Printf("SOCKS5-прокси поднят на %s\n", listenAddr)

	// Ловим Ctrl+C / закрытие консоли, чтобы аккуратно выключить системный прокси.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sysProxyOn := false
	if *flagSysProxy && runtime.GOOS == "windows" {
		if err := setWindowsSystemProxy(listenAddr); err != nil {
			fmt.Printf("Внимание: не удалось включить системный прокси автоматически: %v\n", err)
			fmt.Println("SOCKS5 всё равно работает — можно настроить прокси в приложениях вручную.")
		} else {
			sysProxyOn = true
			fmt.Println("Системный прокси Windows включён (socks=" + listenAddr + ").")
		}
	}

	cleanup := func() {
		if sysProxyOn {
			if err := clearWindowsSystemProxy(); err != nil {
				fmt.Printf("Не удалось выключить системный прокси автоматически: %v\n", err)
				fmt.Println(`Выключи вручную: Панель управления -> Свойства обозревателя -> Подключения -> Настройка сети`)
			} else {
				fmt.Println("Системный прокси Windows выключен.")
			}
		}
	}

	go func() {
		<-sigCh
		fmt.Println("\nЗавершение...")
		cleanup()
		os.Exit(0)
	}()

	fmt.Println("Готово. Держи это окно открытым, пока нужен туннель. Ctrl+C — выйти и вернуть обычный трафик.")

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn, client)
	}
}

// dialSSHWithRetry — несколько попыток на случай моргания сети при старте.
func dialSSHWithRetry(addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		client, err := ssh.Dial("tcp", addr, config)
		if err == nil {
			return client, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return nil, lastErr
}

// ---------- Встроенный SOCKS-сервер (без внешних зависимостей) ----------
// Поддерживает и SOCKS5 (RFC 1928, CONNECT без авторизации), и SOCKS4/4a —
// на практике оказалось, что системный прокси Windows (или что-то в цепочке
// приложений) обращается именно по SOCKS4, а не SOCKS5, хотя порт тот же.
// Раньше сервер понимал только SOCKS5 и молча рвал любое SOCKS4-соединение —
// отсюда были обрывы "сразу", ещё до захода на любую страницу.

func handleConnection(conn net.Conn, sshClient *ssh.Client) {
	defer conn.Close()
	remoteAddrStr := conn.RemoteAddr().String()

	verBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, verBuf); err != nil {
		logf("[%s] обрыв до получения первого байта: %v", remoteAddrStr, err)
		return
	}

	switch verBuf[0] {
	case 0x05:
		handleSOCKS5(conn, sshClient, remoteAddrStr)
	case 0x04:
		handleSOCKS4(conn, sshClient, remoteAddrStr)
	default:
		logf("[%s] неизвестная версия протокола (%d) — не SOCKS4 и не SOCKS5", remoteAddrStr, verBuf[0])
	}
}

func handleSOCKS4(conn net.Conn, sshClient *ssh.Client, remoteAddrStr string) {
	// Формат запроса SOCKS4/4a (версия уже прочитана в handleConnection):
	// CMD(1) DSTPORT(2) DSTIP(4) USERID(...\0) [DOMAIN(...\0) — если SOCKS4a]
	head := make([]byte, 7)
	if _, err := io.ReadFull(conn, head); err != nil {
		logf("[%s] SOCKS4: обрыв при чтении заголовка запроса: %v", remoteAddrStr, err)
		return
	}
	cmd := head[0]
	targetPort := int(head[1])<<8 | int(head[2])
	dstIP := net.IP(head[3:7])

	if cmd != 0x01 { // поддерживаем только CONNECT
		logf("[%s] SOCKS4: неподдерживаемая команда (cmd=%d)", remoteAddrStr, cmd)
		writeSOCKS4Reply(conn, 0x5B) // request rejected/failed
		return
	}

	// USERID — читаем до нулевого байта, значение не используем (авторизация не нужна).
	if err := readUntilNull(conn); err != nil {
		logf("[%s] SOCKS4: обрыв при чтении USERID: %v", remoteAddrStr, err)
		return
	}

	var targetHost string
	// SOCKS4a: если IP имеет вид 0.0.0.x (x != 0), следом идёт доменное имя
	// вместо реального адреса — так клиенты передают DNS-имя через прокси,
	// когда сами резолвить его не могут/не хотят.
	isSocks4a := head[3] == 0 && head[4] == 0 && head[5] == 0 && head[6] != 0
	if isSocks4a {
		domain, err := readStringUntilNull(conn)
		if err != nil {
			logf("[%s] SOCKS4a: обрыв при чтении домена: %v", remoteAddrStr, err)
			return
		}
		targetHost = domain
	} else {
		targetHost = dstIP.String()
	}

	target := fmt.Sprintf("%s:%d", targetHost, targetPort)
	proxyToTarget(conn, sshClient, remoteAddrStr, target, func(ok bool) {
		if ok {
			writeSOCKS4Reply(conn, 0x5A) // request granted
		} else {
			writeSOCKS4Reply(conn, 0x5B)
		}
	})
}

func readUntilNull(r io.Reader) error {
	b := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
		if b[0] == 0 {
			return nil
		}
	}
}

func readStringUntilNull(r io.Reader) (string, error) {
	var out []byte
	b := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return string(out), nil
		}
		out = append(out, b[0])
	}
}

func writeSOCKS4Reply(conn net.Conn, code byte) {
	// VN(0) REP(code) DSTPORT(0) DSTIP(0) — большинству клиентов этого достаточно.
	conn.Write([]byte{0x00, code, 0, 0, 0, 0, 0, 0})
}

func handleSOCKS5(conn net.Conn, sshClient *ssh.Client, remoteAddrStr string) {
	buf := make([]byte, 262)

	// Greeting: NMETHODS, METHODS... (VER уже прочитан в handleConnection)
	if _, err := io.ReadFull(conn, buf[:1]); err != nil {
		logf("[%s] обрыв на приветствии: %v", remoteAddrStr, err)
		return
	}
	nMethods := int(buf[0])
	if nMethods > 0 {
		if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
			logf("[%s] обрыв при чтении списка методов авторизации: %v", remoteAddrStr, err)
			return
		}
	}
	// Проверяем, что клиент реально предложил "без авторизации" (0x00) —
	// раньше мы отвечали 0x00 не глядя, а строгие клиенты рвут соединение,
	// если сервер "выбрал" метод, которого не было в списке предложенных.
	noAuthOffered := false
	for i := 0; i < nMethods; i++ {
		if buf[i] == 0x00 {
			noAuthOffered = true
			break
		}
	}
	if !noAuthOffered {
		logf("[%s] клиент не предложил анонимный доступ (методы: %v) — отвечаю отказом", remoteAddrStr, buf[:nMethods])
		conn.Write([]byte{0x05, 0xFF})
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		logf("[%s] не смог отправить ответ на приветствие: %v", remoteAddrStr, err)
		return
	}

	// Запрос: VER CMD RSV ATYP ADDR PORT
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		logf("[%s] обрыв при чтении запроса CONNECT: %v", remoteAddrStr, err)
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 { // поддерживаем только CONNECT
		logf("[%s] неподдерживаемая команда (cmd=%d) — отвечаю отказом", remoteAddrStr, buf[1])
		writeSOCKSReply(conn, 0x07)
		return
	}

	var targetHost string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		targetHost = net.IP(buf[:4]).String()
	case 0x03: // domain name
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:l]); err != nil {
			return
		}
		targetHost = string(buf[:l])
	case 0x04: // IPv6
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		targetHost = net.IP(buf[:16]).String()
	default:
		writeSOCKSReply(conn, 0x08)
		return
	}

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	targetPort := int(buf[0])<<8 | int(buf[1])
	target := fmt.Sprintf("%s:%d", targetHost, targetPort)

	proxyToTarget(conn, sshClient, remoteAddrStr, target, func(ok bool) {
		if ok {
			writeSOCKSReply(conn, 0x00)
		} else {
			writeSOCKSReply(conn, 0x05)
		}
	})
}

// proxyToTarget — общая для SOCKS4 и SOCKS5 часть: подключается к цели ЧЕРЕЗ
// SSH (весь трафик реально выходит с VPS), сообщает результат через
// writeReply(true/false), и, если получилось, перекачивает данные в обе
// стороны, дожидаясь завершения ОБОИХ направлений (иначе браузер получает
// ERR_CONNECTION_RESET на середине страницы — так и было до этого фикса).
func proxyToTarget(conn net.Conn, sshClient *ssh.Client, remoteAddrStr, target string, writeReply func(ok bool)) {
	remote, err := sshClient.Dial("tcp", target)
	if err != nil {
		logf("[%s] -> %s: сервер не смог подключиться через SSH: %v", remoteAddrStr, target, err)
		writeReply(false)
		return
	}
	defer remote.Close()
	logf("[%s] -> %s: подключение через VPS установлено", remoteAddrStr, target)

	writeReply(true)

	done := make(chan struct{}, 2)
	go func() {
		n, err := io.Copy(remote, conn)
		if err != nil {
			logf("[%s] -> %s: направление комп->сайт оборвалось после %d байт: %v", remoteAddrStr, target, n, err)
		}
		if tc, ok := remote.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, err := io.Copy(conn, remote)
		if err != nil {
			logf("[%s] -> %s: направление сайт->комп оборвалось после %d байт: %v", remoteAddrStr, target, n, err)
		}
		if tc, ok := conn.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	logf("[%s] -> %s: соединение закрыто штатно", remoteAddrStr, target)
}

func logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

func writeSOCKSReply(conn net.Conn, code byte) {
	// BND.ADDR/BND.PORT нулевые — большинству клиентов этого достаточно.
	conn.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

// ---------- Windows-специфика (системный прокси, права администратора) ----------

func isElevated() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	_, err := os.Open(`\\.\PHYSICALDRIVE0`)
	return err == nil
}

func relaunchElevated() error {
	// Реализация в windows_admin.go (build-tag windows) через ShellExecute "runas".
	return relaunchElevatedImpl()
}

func setWindowsSystemProxy(socksAddr string) error {
	return setWindowsSystemProxyImpl(socksAddr)
}

func clearWindowsSystemProxy() error {
	return clearWindowsSystemProxyImpl()
}

func waitEnterIfWindows() {
	if runtime.GOOS == "windows" {
		fmt.Println("\nНажми Enter для выхода...")
		fmt.Scanln()
	}
}

var _ = strings.TrimSpace // (заглушка, чтобы избежать unused import при правках)
var _ = errors.New
