#!/usr/bin/env bash
# Ставит ssh_tunnel-linux в /usr/local/bin и готовит службу для текущего
# пользователя. Права администратора нужны только чтобы скопировать файл.
set -euo pipefail

BIN="${1:-./ssh_tunnel-linux}"
if [ ! -f "$BIN" ]; then
  echo "Не нашёл $BIN — укажи путь к скачанному файлу первым аргументом." >&2
  exit 1
fi

echo "Копирую в /usr/local/bin (нужен sudo)..."
sudo install -m 755 "$BIN" /usr/local/bin/ssh_tunnel-linux

echo "Готовлю службу для пользователя $USER..."
mkdir -p ~/.config/systemd/user
install -m 644 "$(dirname "$0")/ssh_tunnel.service" ~/.config/systemd/user/ssh_tunnel.service
systemctl --user daemon-reload

cat <<MSG

Готово. Дальше:

  1. Задай сервер (один раз):
       ssh_tunnel-linux -host ТВОЙ_СЕРВЕР -user root -save

  2. Включи службу:
       systemctl --user enable --now ssh_tunnel

  3. Адрес веб-интерфейса (с ключом) появится в журнале:
       journalctl --user -u ssh_tunnel -n 20

Чтобы служба работала и без входа в систему:
       sudo loginctl enable-linger $USER
MSG
