#!/usr/bin/env bash
# Ставит ssh_tunnel_panel как системную службу (root) на Debian/Ubuntu — одной
# командой на чистом сервере, без похода в браузер за файлами:
#
#   curl -fsSL https://raw.githubusercontent.com/VITAZGIO/ssh_tunel/main/packaging/panel/install.sh \
#     | sudo bash
#
#   curl -fsSL https://raw.githubusercontent.com/VITAZGIO/ssh_tunel/main/packaging/panel/install.sh \
#     | sudo bash -s -- --domain=panel.example.com
#
# Скрипт сам скачивает бинарь под архитектуру сервера (amd64/arm64) со
# страницы последнего релиза — путь к уже скачанному файлу первым
# аргументом по-прежнему работает, если бинарь уже есть под рукой:
#
#   sudo bash install.sh ./ssh_tunnel_panel --lan
#
# Единица systemd не читается из соседнего файла (при запуске через
# curl | bash соседних файлов просто нет) — она встроена в этот скрипт
# ниже, см. install_unit().
#
#   install.sh                          панель за nginx/Caddy,
#                                        слушает 127.0.0.1:47823
#   install.sh --lan                     панель на всех интерфейсах,
#                                        порт 47823 открывается в ufw
#   install.sh --domain=panel.example.com
#                                        свой HTTPS с автосертификатом
#                                        Let's Encrypt, порты 80 и 443
#
# С --domain адрес для SSH-подключения клиентов панель берёт из того же
# домена — отдельно указывать не нужно. Без --domain (--lan или обычный
# режим за реверс-прокси) конфиги для новых клиентов не соберутся, пока не
# добавишь --ssh-host=АДРЕС_СЕРВЕРА:
#
#   install.sh --lan --ssh-host=203.0.113.10
#
# Панель работает от root — иначе она не сможет заводить пользователей и
# управлять доступом клиентов. Юнит намеренно сужает ей права на всё
# остальное (см. install_unit() ниже).
set -euo pipefail

REPO="VITAZGIO/ssh_tunel"

BIN=""
MODE="local"   # local | lan | domain
DOMAIN=""
SSH_HOST=""
for arg in "$@"; do
  case "$arg" in
    --lan) MODE="lan" ;;
    --domain=*) MODE="domain"; DOMAIN="${arg#--domain=}" ;;
    --ssh-host=*) SSH_HOST="${arg#--ssh-host=}" ;;
    *) BIN="$arg" ;;
  esac
done

if [ "$MODE" = "domain" ] && [ -z "$DOMAIN" ]; then
  echo "--domain требует значения: --domain=panel.example.com" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "Нужен root: запусти через sudo." >&2
  exit 1
fi

# Бинарь: либо уже скачан и путь дан аргументом, либо тащим сами со страницы
# последнего релиза под архитектуру этого сервера.
if [ -n "$BIN" ]; then
  if [ ! -f "$BIN" ]; then
    echo "Не нашёл $BIN — проверь путь к файлу." >&2
    exit 1
  fi
else
  case "$(uname -m)" in
    x86_64)          ASSET="ssh_tunnel_panel" ;;
    aarch64|arm64)   ASSET="ssh_tunnel_panel_arm64" ;;
    *) echo "Нет готовой сборки под $(uname -m) — собери бинарь сам и укажи путь первым аргументом." >&2
       exit 1 ;;
  esac
  command -v curl >/dev/null || { echo "Нужен curl." >&2; exit 1; }
  echo "Скачиваю $ASSET..."
  BIN="$(mktemp)"
  curl -fsSL -o "$BIN" "https://github.com/$REPO/releases/latest/download/$ASSET"
  chmod +x "$BIN"
fi

echo "Копирую в /usr/local/bin..."
install -m 755 "$BIN" /usr/local/bin/ssh_tunnel_panel

echo "Готовлю папку с настройками..."
install -d -m 700 /etc/ssh_tunnel_panel

UNIT=/etc/systemd/system/ssh_tunnel_panel.service

# install_unit — та же единица, что раньше лежала отдельным файлом
# ssh_tunnel_panel.service. Встроена сюда, чтобы install.sh работал сам по
# себе через curl | bash, без похода за вторым файлом.
#
# Панели нужен root ровно для одного — управлять системными учётными
# записями и правами клиентов; со всем остальным она может быть настолько
# ограничена, насколько это позволяет systemd для процесса с UID 0. Границы
# выбраны по тому, что панель обязана делать, а не по принципу «чем строже,
# тем лучше»: useradd пишет в /etc/passwd и /etc/shadow, ключи клиентов
# ложатся в /home/tun_*/.ssh, поэтому ProtectSystem=strict и
# ProtectHome=true здесь стояли бы только для вида — с ними панель не может
# завести ни одного клиента и падает на первой же кнопке. Read-only
# остаются /usr и /boot: туда ей писать незачем.
install_unit() {
cat > "$UNIT" <<'UNITEOF'
[Unit]
Description=ssh_tunnel_panel — веб-панель управления сервером и клиентами
Documentation=https://github.com/VITAZGIO/ssh_tunel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/ssh_tunnel_panel
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=true
ProtectHome=false
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
UNITEOF
}

echo "Ставлю службу..."
install_unit

EXTRA=""
if [ -n "$SSH_HOST" ]; then EXTRA=" -ssh-host $SSH_HOST"; fi

case "$MODE" in
  lan)
    sed -i "s|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_panel -listen 0.0.0.0:47823$EXTRA|" "$UNIT"
    ;;
  domain)
    sed -i "s|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_panel -domain $DOMAIN$EXTRA|" "$UNIT"
    ;;
  local)
    if [ -n "$EXTRA" ]; then
      sed -i "s|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_panel$EXTRA|" "$UNIT"
    fi
    ;;
esac

systemctl daemon-reload
systemctl enable --now ssh_tunnel_panel

if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
  case "$MODE" in
    lan)    ufw allow 47823/tcp comment 'ssh_tunnel_panel' >/dev/null ;;
    domain) ufw allow 80/tcp comment 'ssh_tunnel_panel (ACME)' >/dev/null
            ufw allow 443/tcp comment 'ssh_tunnel_panel' >/dev/null ;;
    local)  : ;; # за nginx/Caddy — порт панели наружу не открывается вовсе
  esac
fi

echo "Жду, пока панель запишет в журнал одноразовый пароль..."
sleep 2

ADDR="http://127.0.0.1:47823/"
case "$MODE" in
  lan)    IP="$(hostname -I 2>/dev/null | awk '{print $1}')"; ADDR="http://${IP:-АДРЕС_МАШИНЫ}:47823/" ;;
  domain) ADDR="https://$DOMAIN/" ;;
esac

cat <<MSG

Готово. Служба ssh_tunnel_panel запущена.

  Адрес панели:  $ADDR
MSG

if [ "$MODE" = "local" ]; then
  cat <<MSG
  (слушает 127.0.0.1:47823 — заведи в nginx/Caddy проксирование на этот адрес
   и свой сертификат для домена панели)
MSG
fi

if [ "$MODE" != "domain" ] && [ -z "$SSH_HOST" ]; then
  cat <<MSG

Внимание: адрес для SSH-подключения клиентов не задан. Пока не перезапустишь
службу с -ssh-host=АДРЕС_СЕРВЕРА (поправь ExecStart в $UNIT и
"systemctl daemon-reload && systemctl restart ssh_tunnel_panel"), конфиги
для новых клиентов не соберутся.
MSG
fi

echo
echo "Логин и одноразовый пароль для первого входа — из журнала службы:"
echo
journalctl -u ssh_tunnel_panel --no-pager -n 50 | grep -A3 "Первый запуск" || \
  echo "  (не нашёл в последних строчках журнала — смотри: journalctl -u ssh_tunnel_panel)"
echo
echo "Панель потребует сменить этот пароль сразу после входа."
