// Мастер настройки VPS: то же самое, что раньше делалось руками по
// docs/SERVER_SETUP.md — положить на сервер свой ключ, прогнать harden.sh
// (шаг 2) и tunnel-user.sh (шаг 3), — но по SSH с паролем root, одной
// кнопкой, с построчным выводом в интерфейс вместо копирования команд в
// терминал.
//
// Пароль root используется только внутри runVpsSetup и функций, которым он
// передан явным параметром: он не сохраняется на диск, не публикуется в
// events.Bus и не возвращается наружу HTTP-обработчиком — тот же приём, что и
// пароль sudo для автозапуска (boot.go).
//
// Порядок шагов — как в документе, а не упрощённый: сначала ключ, потом
// проверка, что вход по нему действительно работает, и только после этого
// harden.sh отключает пароли. Если проверка не прошла, скрипты не
// запускаются вовсе — сервер остаётся как был, со входом по паролю.
package webui

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/tunnel"
)

// vpsSetupParams — то, что вводится в мастере настройки VPS.
type vpsSetupParams struct {
	Host            string
	Port            int
	User            string
	Password        string
	KeyPath         string
	InstallPanel    bool
	InstallUDPRelay bool
}

const vpsDialTimeout = 20 * time.Second

// runVpsSetup выполняет всю настройку и публикует ход построчно в bus.
// Ошибка на любом шаге останавливает всё, что после него, — но если она
// случилась до успешной проверки входа по ключу, пароли на сервере остаются
// включёнными: полунастроенного состояния без доступа не бывает.
func runVpsSetup(bus *events.Bus, p vpsSetupParams) (err error) {
	defer func() {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		bus.VpsSetupDone(msg)
	}()

	if strings.TrimSpace(p.Host) == "" || p.Port <= 0 || strings.TrimSpace(p.User) == "" {
		return errors.New("не заполнены адрес, порт или пользователь")
	}
	if p.Password == "" {
		return errors.New("не введён пароль root")
	}

	pub, _, err := ensureKey(p.KeyPath)
	if err != nil {
		return fmt.Errorf("не удалось подготовить свой SSH-ключ: %w", err)
	}
	signer, err := loadSigner(p.KeyPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать свой SSH-ключ: %w", err)
	}

	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	bus.VpsSetupLine("connect", fmt.Sprintf("Подключаюсь к %s по паролю…", addr))
	pwClient, err := vpsDial(p.User, addr, ssh.Password(p.Password), bus)
	if err != nil {
		return fmt.Errorf("не удалось подключиться по паролю: %w", classifySSHErr(err))
	}
	defer pwClient.Close()
	bus.VpsSetupLine("connect", "Подключение по паролю установлено")

	bus.VpsSetupLine("key", "Кладу на сервер открытый ключ этого компьютера")
	if err := installPubKey(pwClient, pub); err != nil {
		return fmt.Errorf("не удалось положить ключ на сервер: %w", err)
	}
	bus.VpsSetupLine("key", "Ключ записан в authorized_keys")

	bus.VpsSetupLine("key", "Проверяю вход по ключу — без этого отключать пароли нельзя")
	keyClient, err := vpsDial(p.User, addr, ssh.PublicKeys(signer), bus)
	if err != nil {
		return fmt.Errorf(
			"ключ положен, но вход по нему не удался — пароли на сервере НЕ отключены (%w)",
			classifySSHErr(err))
	}
	defer keyClient.Close()
	bus.VpsSetupLine("key", "Вход по ключу работает")

	bus.VpsSetupLine("harden", "Запускаю базовую защиту сервера (harden.sh)")
	hardenScript, err := renderHarden(hardenParams{NewUser: "admin", SSHPort: p.Port, Timezone: ""})
	if err != nil {
		return err
	}
	if err := runRemoteScript(keyClient, hardenScript, func(line string) { bus.VpsSetupLine("harden", line) }); err != nil {
		return fmt.Errorf("harden.sh завершился с ошибкой: %w", err)
	}
	bus.VpsSetupLine("harden", "harden.sh готово")

	bus.VpsSetupLine("tunnel-user", "Завожу отдельного пользователя для туннеля (tunnel-user.sh)")
	tunnelScript, err := renderTunnelUser(tunnelUserParams{TunnelUser: "tunnel", TunnelKey: "", BlockInternals: 1})
	if err != nil {
		return err
	}
	if err := runRemoteScript(keyClient, tunnelScript, func(line string) { bus.VpsSetupLine("tunnel-user", line) }); err != nil {
		return fmt.Errorf("tunnel-user.sh завершился с ошибкой: %w", err)
	}
	bus.VpsSetupLine("tunnel-user", "tunnel-user.sh готово")

	if p.InstallPanel {
		bus.VpsSetupLine("panel", "Ставлю веб-панель Cockpit")
		panelAddr, panelPass, perr := installPanel(keyClient, p.Host, func(line string) { bus.VpsSetupLine("panel", line) })
		if perr != nil {
			bus.VpsSetupLine("panel", "Веб-панель не установлена: "+perr.Error())
		} else {
			bus.VpsSetupLine("panel", fmt.Sprintf(
				"Панель: %s   пользователь: vpsadmin   пароль (одноразовый, спросит сменить при входе): %s",
				panelAddr, panelPass))
		}
	}

	if p.InstallUDPRelay {
		bus.VpsSetupLine("udprelay", "Ставлю ретранслятор UDP (звонки, игры, QUIC через туннель)")
		if err := installUDPRelay(keyClient, func(line string) { bus.VpsSetupLine("udprelay", line) }); err != nil {
			bus.VpsSetupLine("udprelay", "Ретранслятор UDP не установлен: "+err.Error())
		} else {
			bus.VpsSetupLine("udprelay", "Готово — включи «Пробрасывать UDP через сервер» в настройках сервера")
		}
	}

	return nil
}

// vpsDial — то же самое, что ssh.Dial, но с ограничением по времени на само
// рукопожатие: сервер, который принял TCP-соединение и замолчал, не должен
// подвесить мастер навсегда (тот же приём, что tunnel.dialSSH).
func vpsDial(user, addr string, auth ssh.AuthMethod, bus *events.Bus) (*ssh.Client, error) {
	cb := hostkey.Callback(config.KnownHostsPath(), func(host, fp string) {
		bus.VpsSetupLine("connect", fmt.Sprintf("Ключ сервера %s запомнен: %s", host, fp))
	})
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: cb,
		Timeout:         vpsDialTimeout,
	}
	conn, err := net.DialTimeout("tcp", addr, vpsDialTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(vpsDialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		c.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func loadSigner(keyPath string) (ssh.Signer, error) {
	path, err := expandHome(keyPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

// installPubKey кладёт открытый ключ в authorized_keys, если его там ещё нет
// — повторный запуск мастера не плодит дубликаты.
func installPubKey(client *ssh.Client, pubLine string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	cmd := fmt.Sprintf(
		`set -e; mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys `+
			`&& chmod 600 ~/.ssh/authorized_keys `+
			`&& grep -qxF %s ~/.ssh/authorized_keys || printf '%%s\n' %s >> ~/.ssh/authorized_keys`,
		shQuote(pubLine), shQuote(pubLine))
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// runRemoteScript запускает script как `bash -s`, отдавая стандартный вывод и
// вывод ошибок построчно в onLine по мере появления — а не одной простынёй
// после завершения.
func runRemoteScript(client *ssh.Client, script string, onLine func(string)) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	lw := &lineWriter{emit: onLine}
	session.Stdout = lw
	session.Stderr = lw

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	if err := session.Start("bash -s"); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, script); err != nil {
		stdin.Close()
		return err
	}
	stdin.Close()
	err = session.Wait()
	lw.Flush()
	return err
}

// lineWriter режет поток байт на строки и отдаёт их по одной. stdout и stderr
// сессии пишут в один и тот же lineWriter из разных горутин — отсюда мьютекс.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimRight(w.buf[:i], "\r"))
		w.buf = w.buf[i+1:]
		if line != "" {
			w.emit(line)
		}
	}
	return len(p), nil
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// installPanel — необязательный шаг (см. ТЗ на веб-панель): ставит Cockpit,
// штатную веб-панель администрирования для Debian/Ubuntu (systemd, journalctl,
// сеть, диски, обновления — из браузера), заводит для входа в неё отдельного
// пользователя vpsadmin с одноразовым паролем. В документе harden.sh
// советует не ставить веб-панели ради самой безопасности сервера — поэтому
// это отдельная явная галочка, выключенная по умолчанию, а не часть
// обязательных шагов.
// installUDPRelay ставит ретранслятор UDP (см. sshtunnel/cmd/udprelay,
// sshtunnel/internal/udprelay): SSH умеет пробрасывать только TCP, поэтому
// звонки, игры и QUIC без него не проходят через туннель. Ретранслятор
// компилируется прямо на сервере из исходника, который мастер сюда
// закладывает, — так не нужно ни кросс-компилировать бинарник на компьютере
// человека, ни подбирать архитектуру сервера. Слушает только 127.0.0.1:
// отдельно открывать порт в файрволе не нужно, достучаться до него можно
// только через уже прошедший SSH-аутентификацию туннель.
func installUDPRelay(client *ssh.Client, onLine func(string)) error {
	src, err := udpRelayServerSource()
	if err != nil {
		return fmt.Errorf("не нашёл исходник ретранслятора: %w", err)
	}

	const marker = "EOF_UDPRELAY_SOURCE_39fa2c"
	script := fmt.Sprintf(`set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
command -v go >/dev/null || { echo "ставлю Go"; apt-get -y -qq install golang-go >/dev/null; }
mkdir -p /root/udprelay
cat > /root/udprelay/main.go <<'%s'
%s
%s
cd /root/udprelay
go build -o /usr/local/bin/udprelay main.go
echo "ретранслятор собран"

cat > /etc/systemd/system/udprelay.service <<'EOF_UNIT'
[Unit]
Description=ssh_tunnel - ретранслятор UDP (только 127.0.0.1)
After=network.target

[Service]
ExecStart=/usr/local/bin/udprelay
Restart=always
RestartSec=2
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF_UNIT
systemctl daemon-reload
systemctl enable --now udprelay.service
echo "udprelay запущен"
`, marker, src, marker)

	return runRemoteScript(client, script, onLine)
}

func installPanel(client *ssh.Client, host string, onLine func(string)) (addr, password string, err error) {
	const script = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
command -v apt-get >/dev/null || { echo "панель ставится только на Debian/Ubuntu" >&2; exit 1; }
apt-get -y -qq install cockpit >/dev/null
id -u vpsadmin >/dev/null 2>&1 || useradd -m -s /bin/bash vpsadmin
usermod -aG sudo vpsadmin
echo "vpsadmin ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/90-vpsadmin
chmod 440 /etc/sudoers.d/90-vpsadmin
systemctl enable --now cockpit.socket >/dev/null
if command -v ufw >/dev/null; then ufw allow 9090/tcp comment cockpit-panel >/dev/null || true; fi
echo "cockpit установлен"
`
	if err := runRemoteScript(client, script, onLine); err != nil {
		return "", "", fmt.Errorf("установка cockpit: %w", err)
	}

	pass, err := randomPassword()
	if err != nil {
		return "", "", err
	}
	session, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(fmt.Sprintf("vpsadmin:%s\n", pass))
	out, err := session.CombinedOutput("chpasswd && chage -d 0 vpsadmin")
	if err != nil {
		return "", "", fmt.Errorf("не удалось задать пароль панели: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return fmt.Sprintf("https://%s:9090", host), pass, nil
}

func randomPassword() (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// classifySSHErr переводит частые ошибки golang.org/x/crypto/ssh в понятный
// текст — вместо "ssh: handshake failed: ssh: unable to authenticate...".
func classifySSHErr(err error) error {
	msg := err.Error()
	switch {
	case tunnel.IsAuthError(err):
		return errors.New("неверный пароль (или пользователь)")
	case strings.Contains(msg, "connection refused"):
		return errors.New("сервер отказал в соединении — проверь адрес и порт")
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "timed out"):
		return errors.New("сервер не ответил вовремя — проверь адрес, порт и что сервер включён")
	case strings.Contains(msg, "no route to host"):
		return errors.New("сервер недоступен по сети")
	default:
		var changed *hostkey.ErrChanged
		if errors.As(err, &changed) {
			return changed
		}
		return err
	}
}
