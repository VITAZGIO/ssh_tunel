#!/usr/bin/env bash
# Ставит ssh_tunnel_panel как системную службу (root) на Debian/Ubuntu.
#
#   install.sh ./ssh_tunnel_panel                     панель за nginx/Caddy,
#                                                      слушает 127.0.0.1:47823
#   install.sh ./ssh_tunnel_panel --lan                панель на всех интерфейсах,
#                                                      порт 47823 открывается в ufw
#   install.sh ./ssh_tunnel_panel --domain=panel.example.com
#                                                      свой HTTPS с автосертификатом
#                                                      Let's Encrypt, порты 80 и 443
#
# С --domain адрес для SSH-подключения клиентов панель берёт из того же
# домена — отдельно указывать не нужно. Без --domain (--lan или обычный
# режим за реверс-прокси) конфиги для новых клиентов не соберутся, пока не
# добавишь --ssh-host=АДРЕС_СЕРВЕРА:
#
#   install.sh ./ssh_tunnel_panel --lan --ssh-host=203.0.113.10
#
# Панель работает от root — иначе она не сможет заводить пользователей и
# управлять доступом клиентов. Юнит намеренно сужает ей права на всё
# остальное (см. packaging/panel/ssh_tunnel_panel.service).
set -euo pipefail

BIN="./ssh_tunnel_panel"
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

if [ ! -f "$BIN" ]; then
  echo "Не нашёл $BIN — укажи путь к скачанному файлу первым аргументом." >&2
  exit 1
fi
if [ "$MODE" = "domain" ] && [ -z "$DOMAIN" ]; then
  echo "--domain требует значения: --domain=panel.example.com" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "Нужен root: запусти через sudo." >&2
  exit 1
fi

echo "Копирую в /usr/local/bin..."
install -m 755 "$BIN" /usr/local/bin/ssh_tunnel_panel

echo "Готовлю папку с настройками..."
install -d -m 700 /etc/ssh_tunnel_panel

echo "Ставлю службу..."
UNIT=/etc/systemd/system/ssh_tunnel_panel.service
install -m 644 "$(dirname "$0")/ssh_tunnel_panel.service" "$UNIT"

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
