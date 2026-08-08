#!/usr/bin/env bash
# Собирает оба exe под Windows и кладёт их в ../releases вместе с контрольными
# суммами. Запускать из папки src.
set -euo pipefail

cd "$(dirname "$0")"
OUT="../releases"
mkdir -p "$OUT"

echo "Проверяю тесты..."
go test ./... >/dev/null

echo "Рисую иконку..."
go run tools/mkicon/main.go internal/nativeui/icon.ico

# Иконка и манифест попадают в exe отдельной секцией ресурсов. Файл .syso
# линковщик Go подхватывает автоматически, если он лежит рядом с main-пакетом.
# Порядок важен: манифест получает ID 1, группа иконок — ID 2, и именно на
# двойку ссылается код в internal/nativeui.
echo "Вшиваю иконку и манифест..."
go run github.com/akavel/rsrc@v0.10.2 \
  -manifest cmd/vpstunnel/app.manifest \
  -ico internal/nativeui/icon.ico \
  -arch amd64 \
  -o cmd/vpstunnel/rsrc_windows_amd64.syso

echo "Собираю vpstunnel.exe (окно)..."
GOOS=windows GOARCH=amd64 go build \
  -ldflags="-s -w -H windowsgui" \
  -o "$OUT/vpstunnel.exe" ./cmd/vpstunnel

echo "Собираю vpstunnel-cli.exe (консоль)..."
GOOS=windows GOARCH=amd64 go build \
  -ldflags="-s -w" \
  -o "$OUT/vpstunnel-cli.exe" ./cmd/vpstunnel-cli

( cd "$OUT" && sha256sum vpstunnel.exe vpstunnel-cli.exe > SHA256SUMS.txt )

echo
ls -lh "$OUT"/*.exe
echo "Готово."
