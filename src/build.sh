#!/usr/bin/env bash
# Собирает готовые файлы для всех поддерживаемых систем и раскладывает их по
# ../releases. Запускать из папки src.
set -euo pipefail

cd "$(dirname "$0")"
OUT="../releases"
mkdir -p "$OUT/windows" "$OUT/linux"

# Собираем без cgo. Внешних библиотек у программы нет, зато статический файл
# запускается везде одинаково: и на Alpine с musl вместо glibc, и на старых
# дистрибутивах, где glibc слишком древняя для того, с чем слинковались бы на
# сборочной машине.
export CGO_ENABLED=0

echo "Проверяю тесты..."
go test ./... >/dev/null

echo "Собираю иконку..."
go run tools/mkicon/main.go internal/nativeui/icon-source.png internal/nativeui/icon.ico

# Иконка и манифест попадают в exe отдельной секцией ресурсов. Файл .syso
# линковщик Go подхватывает автоматически, если он лежит рядом с main-пакетом.
# Порядок важен: манифест получает ID 1, группа иконок — ID 2, и именно на
# двойку ссылается код в internal/nativeui.
echo "Вшиваю иконку и манифест..."
go run github.com/akavel/rsrc@v0.10.2 \
  -manifest cmd/ssh_tunnel/app.manifest \
  -ico internal/nativeui/icon.ico \
  -arch amd64 \
  -o cmd/ssh_tunnel/rsrc_windows_amd64.syso

echo "Windows: окно..."
GOOS=windows GOARCH=amd64 go build \
  -ldflags="-s -w -H windowsgui" \
  -o "$OUT/windows/ssh_tunnel.exe" ./cmd/ssh_tunnel

# Консольная версия для Windows в релиз не идёт: окно умеет всё то же самое, а
# лишний файл на странице скачивания только сбивает с толку. Кому нужна —
# собирается одной командой:
#   GOOS=windows go build -o ssh_tunnel_cli.exe ./cmd/ssh_tunnel_cli

# Для Linux собираем две архитектуры: обычные серверы и ARM (Raspberry Pi,
# облачные ARM-машины, домашние мини-серверы).
echo "Linux: amd64..."
GOOS=linux GOARCH=amd64 go build \
  -ldflags="-s -w" \
  -o "$OUT/linux/ssh_tunnel_linux" ./cmd/ssh_tunnel_linux

echo "Linux: arm64..."
GOOS=linux GOARCH=arm64 go build \
  -ldflags="-s -w" \
  -o "$OUT/linux/ssh_tunnel_linux_arm64" ./cmd/ssh_tunnel_linux

# Архив для Windows. Браузеры почти не придираются к .zip, тогда как
# неподписанный .exe многие помечают как опасный уже при скачивании. Внутри тот
# же файл — это способ отдать его человеку без спора с браузером.
echo "Windows: архив..."
if command -v zip >/dev/null 2>&1; then
  ( cd "$OUT/windows" && rm -f ssh_tunnel_windows.zip && zip -q ssh_tunnel_windows.zip ssh_tunnel.exe )
elif command -v python3 >/dev/null 2>&1; then
  ( cd "$OUT/windows" && rm -f ssh_tunnel_windows.zip && python3 -m zipfile -c ssh_tunnel_windows.zip ssh_tunnel.exe )
else
  echo "  нет ни zip, ни python3 — архив не собран"
fi

# Файлы прошлых сборок могли остаться от предыдущих версий и прежних имён —
# иначе они попадут в контрольные суммы, а в релиз нет.
rm -f "$OUT/windows/ssh_tunnel_cli.exe" "$OUT/windows/ssh_tunel.exe" "$OUT/windows/ssh_tunnel-cli.exe" \
      "$OUT/windows/ssh_tunel-cli.exe" "$OUT/linux/ssh_tunel-linux" "$OUT/linux/ssh_tunnel-linux" \
      "$OUT/linux/ssh_tunel-linux-arm64" "$OUT/linux/ssh_tunnel-linux-arm64"

( cd "$OUT" && sha256sum windows/* linux/* > SHA256SUMS.txt )

echo
ls -lh "$OUT"/windows/* "$OUT"/linux/*
echo "Готово."
