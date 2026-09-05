#!/usr/bin/env bash
# Ставит ssh_tunnel_linux в /usr/local/bin и готовит службу для текущего
# пользователя. Права администратора нужны только чтобы скопировать файл.
#
#   install.sh ./ssh_tunnel_linux          панель только с этой машины
#   install.sh ./ssh_tunnel_linux --lan    панель по адресу машины, без ключа
set -euo pipefail

BIN="./ssh_tunnel_linux"
LAN=0
for arg in "$@"; do
  case "$arg" in
    --lan) LAN=1 ;;
    *) BIN="$arg" ;;
  esac
done

if [ ! -f "$BIN" ]; then
  echo "Не нашёл $BIN — укажи путь к скачанному файлу первым аргументом." >&2
  exit 1
fi

echo "Копирую в /usr/local/bin (нужен sudo)..."
sudo install -m 755 "$BIN" /usr/local/bin/ssh_tunnel_linux

echo "Готовлю службу для пользователя $USER..."
mkdir -p ~/.config/systemd/user
install -m 644 "$(dirname "$0")/ssh_tunnel.service" ~/.config/systemd/user/ssh_tunnel.service
if [ "$LAN" = 1 ]; then
  sed -i 's|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_linux -web -web-lan|' \
    ~/.config/systemd/user/ssh_tunnel.service
fi
systemctl --user daemon-reload

if [ "$LAN" = 1 ]; then
  IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
  WEB_STEP="Панель откроется по адресу машины на постоянном порту:
       http://${IP:-АДРЕС_МАШИНЫ}:47821"
else
  WEB_STEP="Адрес веб-интерфейса (с ключом) появится в журнале:
       journalctl --user -u ssh_tunnel -n 20"
fi

cat <<MSG

Готово. Дальше:

  1. Задай сервер (один раз):
       ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user root -save

  2. Включи службу:
       systemctl --user enable --now ssh_tunnel

  3. $WEB_STEP

Чтобы служба работала и без входа в систему:
       sudo loginctl enable-linger $USER
MSG
