#!/usr/bin/env bash
# Собирает оба exe под Windows и кладёт их в ../releases вместе с контрольными
# суммами. Запускать из папки src.
set -euo pipefail

cd "$(dirname "$0")"
OUT="../releases"
mkdir -p "$OUT"

echo "Проверяю тесты..."
go test ./... >/dev/null

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
