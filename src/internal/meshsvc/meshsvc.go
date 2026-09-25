// Пакет meshsvc — всё, что нужно, чтобы поставить и настроить службу сети
// устройств (sshtunnel/cmd/meshd) на сервере: скрипт установки, юнит systemd,
// исключение в брандмауэре и юнит «побочного» сервера, который пробрасывает
// свой meshd к главному. Им пользуются и мастер настройки VPS на компьютере
// (по SSH), и панель на самом сервере (локально) — чтобы не разъезжаться.
package meshsvc

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Source — исходник meshd, один в один с cmd/meshd/main.go (go:embed не
// умеет "../.." в пути; синхронность проверяет тест). Нужен, чтобы собрать
// meshd прямо на сервере, если готового файла со страницы релиза нет.
//
//go:embed meshd.go.txt
var Source string

const (
	// Port — где meshd слушает на 127.0.0.1.
	Port = 47831
	// AdminSocket — вход для панели (HTTP по unix-сокету).
	AdminSocket = "/run/meshd/admin.sock"
	// UplinkUnitName — служба побочного сервера: проброс к главному.
	UplinkUnitName = "meshd-uplink.service"
	// LinkUser — пользователь на главном сервере, под которым к нему
	// подключаются побочные: ему разрешён только проброс до meshd.
	LinkUser = "meshlink"
)

// Unit — юнит systemd для meshd.
func Unit() string {
	return `[Unit]
Description=ssh_tunnel - сеть устройств (только 127.0.0.1)
After=network.target

[Service]
ExecStart=/usr/local/bin/meshd -state /var/lib/meshd/state.json -admin ` + AdminSocket + `
Restart=always
RestartSec=2
DynamicUser=yes
StateDirectory=meshd
RuntimeDirectory=meshd
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
`
}

// FirewallExemption дописывается в конец установки ретранслятора UDP и meshd.
// Брандмауэр пользователя туннеля (tunnel-user.sh, «закрыть доступ к самому
// серверу») запрещает ему соединения с 127.0.0.1 — а обе службы слушают как
// раз там. В свежем tunnel-user.sh исключение уже есть; на серверах,
// настроенных раньше, его добавляем и в действующие правила, и в сам скрипт,
// чтобы пережило перезагрузку.
const FirewallExemption = `
if iptables -L SSHTUNNEL -n >/dev/null 2>&1; then
  iptables -C SSHTUNNEL -p tcp -d 127.0.0.1 --dport 47830:47831 -j RETURN 2>/dev/null || \
    iptables -I SSHTUNNEL 1 -p tcp -d 127.0.0.1 --dport 47830:47831 -j RETURN
fi
if [ -f /usr/local/sbin/tunnel-fw.sh ] && ! grep -q 47831 /usr/local/sbin/tunnel-fw.sh; then
  sed -i 's|^for N in 127.0.0.0/8|iptables -A SSHTUNNEL -p tcp -d 127.0.0.1 --dport 47830:47831 -j RETURN\nfor N in 127.0.0.0/8|' /usr/local/sbin/tunnel-fw.sh
  echo "брандмауэр: службам ssh_tunnel на 127.0.0.1 открыт доступ через туннель"
fi
`

// releaseTag — метка релиза, из которого качать meshd той же версии, что и
// программа, которая его ставит. Иначе панель новой версии могла бы
// поставить старый meshd из «последнего релиза» — без нужных ей
// возможностей. Сборка без версии (dev) и всё непохожее на метку — ""
// («последний релиз»).
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$`)

// downloadURL — откуда качать meshd для этой версии ($BIN подставит скрипт).
func downloadURL(version string) string {
	if releaseTag.MatchString(version) {
		return "https://github.com/VITAZGIO/ssh_tunel/releases/download/" + version + "/$BIN"
	}
	return "https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/$BIN"
}

// InstallScript — установка или обновление meshd версии version (версия
// программы, которая ставит): готовый файл со страницы этого релиза, а если
// его нет — сборка на сервере из Source. Побочный проброс к главному (если
// был) останавливается: на одном сервере — что-то одно.
func InstallScript(version string) string {
	const marker = "EOF_MESHD_SOURCE_7c41d0"
	return fmt.Sprintf(`set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

BIN=""
case "$(uname -m)" in
  x86_64)        BIN=meshd ;;
  aarch64|arm64) BIN=meshd_arm64 ;;
esac
GOT=0
if [ -n "$BIN" ] && command -v curl >/dev/null; then
  if curl -fsSL -o /usr/local/bin/meshd.new "%s"; then
    chmod +x /usr/local/bin/meshd.new
    # Старый meshd (до входа для панели) не знает -version: такой не годится.
    if /usr/local/bin/meshd.new -version >/dev/null 2>&1; then
      mv /usr/local/bin/meshd.new /usr/local/bin/meshd
      GOT=1
      echo "meshd скачан: версия $(/usr/local/bin/meshd -version)"
    else
      echo "скачанный meshd слишком старый"
    fi
  fi
fi

if [ "$GOT" != 1 ]; then
  rm -f /usr/local/bin/meshd.new
  echo "готового файла нет — собираю на сервере"
  if ! command -v go >/dev/null; then
    # Go из пакетов весит сотни мегабайт: на маленьком диске он заполнил бы
    # его целиком, и сервер перестал бы работать вовсе.
    FREE_MB=$(df -Pm /usr | awk 'NR==2 {print $4}')
    if [ "${FREE_MB:-0}" -lt 800 ]; then
      echo "Для сборки нужен Go, а на диске свободно только ${FREE_MB} МБ (нужно от 800)." >&2
      echo "Освободи место или поставь версию программы, для которой есть готовый meshd." >&2
      exit 1
    fi
    echo "ставлю Go"; apt-get -y -qq install golang-go >/dev/null
  fi
  mkdir -p /root/meshd
  cat > /root/meshd/main.go <<'%s'
%s
%s
  cd /root/meshd
  go build -o /usr/local/bin/meshd main.go
  echo "meshd собран"
fi

systemctl disable --now %s >/dev/null 2>&1 || true
cat > /etc/systemd/system/meshd.service <<'EOF_UNIT'
%s
EOF_UNIT
systemctl daemon-reload
systemctl enable meshd.service
systemctl restart meshd.service
echo "meshd запущен"
# Проверка NAT (сможет ли устройство соединяться с другими напрямую) —
# UDP 3478-3479. Открываем, только если брандмауэр ufw включён.
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow 3478:3479/udp comment 'ssh_tunnel meshd: проверка NAT' >/dev/null && echo "брандмауэр: открыт UDP 3478-3479 для проверки NAT"
fi
`, downloadURL(version), marker, Source, marker, UplinkUnitName, strings.TrimRight(Unit(), "\n")) + FirewallExemption
}

// UplinkUnit — служба побочного сервера: держит SSH-соединение с главным и
// пробрасывает свой 127.0.0.1:47831 на meshd главного. Устройства, которые
// подключены к побочному серверу, так попадают в сеть главного — сами они
// ничего об этом не знают. Работает на обычном клиенте OpenSSH: он и так
// есть на любом сервере, а переподключается systemd.
//
// strict — ключи главного сервера уже записаны в knownHosts (они приходят в
// коде подключения): чужой сервер под его адресом не примется. Без них
// ключ запоминается при первом подключении.
func UplinkUnit(mainHost string, mainPort int, keyPath, knownHosts string, strict bool) string {
	check := "accept-new"
	if strict {
		check = "yes"
	}
	return `[Unit]
Description=ssh_tunnel - сеть устройств: проброс к главному серверу ` + mainHost + `
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/ssh -N -T \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o BatchMode=yes -o StrictHostKeyChecking=` + check + ` -o UserKnownHostsFile=` + knownHosts + ` \
  -i ` + keyPath + ` -p ` + strconv.Itoa(mainPort) + ` \
  -L 127.0.0.1:` + strconv.Itoa(Port) + `:127.0.0.1:` + strconv.Itoa(Port) + ` \
  ` + LinkUser + `@` + mainHost + `
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}

// LinkKeyOptions — ограничения ключа побочного сервера в authorized_keys:
// только проброс и только до meshd, ни оболочки, ни чего-то ещё. Команда
// принудительная на случай, если кто-то всё же попросит сессию.
var LinkKeyOptions = `restrict,port-forwarding,permitopen="127.0.0.1:` + strconv.Itoa(Port) + `",command="/usr/sbin/nologin"`

// LinkAuthorizedKey — строка authorized_keys пользователя meshlink на главном
// сервере для ключа побочного сервера name.
func LinkAuthorizedKey(pubKey, name string) string {
	pubKey = strings.TrimSpace(pubKey)
	fields := strings.Fields(pubKey)
	if len(fields) >= 2 {
		pubKey = fields[0] + " " + fields[1]
	}
	return LinkKeyOptions + " " + pubKey + " " + sanitizeComment(name)
}

// LinkSSHDBlock — ограничения для пользователя meshlink в sshd_config:
// вторая линия защиты после опций в authorized_keys (их можно забыть при
// ручной правке файла, а этот блок — нет). Проброс только «к себе» (-L) и
// только до meshd; обратный проброс, сессии, агент — закрыты.
func LinkSSHDBlock() string {
	return "Match User " + LinkUser + "\n" +
		"    AllowTcpForwarding local\n" +
		"    PermitOpen 127.0.0.1:" + strconv.Itoa(Port) + "\n" +
		"    PermitListen none\n" +
		"    AllowStreamLocalForwarding no\n" +
		"    AllowAgentForwarding no\n" +
		"    X11Forwarding no\n" +
		"    PermitTunnel no\n" +
		"    PermitTTY no\n" +
		"    ForceCommand /usr/sbin/nologin\n"
}

// KnownHostsLine — строка known_hosts для ключа hostKey ("тип ключ")
// сервера host:port в том виде, в каком её ищет ssh.
func KnownHostsLine(host string, port int, hostKey string) string {
	name := host
	if port != 22 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	f := strings.Fields(hostKey)
	if len(f) >= 2 {
		hostKey = f[0] + " " + f[1]
	}
	return name + " " + hostKey
}

// sanitizeComment — имя сервера в комментарии ключа: без пробелов и кавычек,
// чтобы строку authorized_keys нельзя было сломать.
func sanitizeComment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "server"
	}
	return "meshlink:" + b.String()
}
