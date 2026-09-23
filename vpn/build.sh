#!/usr/bin/env bash
# Собирает ssh_tunnel_vpn.exe — версию для Windows с режимом VPN — и кладёт
# рядом с остальными файлами в ../releases/windows. Запускать из папки vpn.
#
# Отдельно от src/build.sh, потому что этому модулю нужен Go 1.26 (сетевой
# стек gvisor), а основному хватает 1.22.
set -euo pipefail

cd "$(dirname "$0")"
OUT="../releases/windows"
mkdir -p "$OUT"
export CGO_ENABLED=0

VERSION="${VERSION:-${GITHUB_REF_NAME:-dev}}"
LDVERSION="-X sshtunnel/internal/updater.Version=${VERSION}"
echo "Версия сборки: $VERSION"

# Драйвер Wintun — от авторов WireGuard, подписан их сертификатом. В
# репозитории он не лежит (готовым файлам там не место), поэтому скачивается
# с официального сайта. Сумма зашита сюда: подменённый по дороге архив сборку
# остановит, а не уедет в exe.
WINTUN_VERSION="0.14.1"
WINTUN_SHA256="07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
DLL="winvpn/wintun/wintun.dll"

if [ ! -f "$DLL" ] || [ "$(head -c 2 "$DLL")" != "MZ" ]; then
  echo "Скачиваю Wintun $WINTUN_VERSION..."
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL -o "$tmp/wintun.zip" "https://www.wintun.net/builds/wintun-${WINTUN_VERSION}.zip"
  got="$(sha256sum "$tmp/wintun.zip" | cut -d' ' -f1)"
  if [ "$got" != "$WINTUN_SHA256" ]; then
    echo "Контрольная сумма Wintun не совпала!" >&2
    echo "  ожидалась: $WINTUN_SHA256" >&2
    echo "  получена:  $got" >&2
    exit 1
  fi
  mkdir -p "$(dirname "$DLL")"
  unzip -p "$tmp/wintun.zip" wintun/bin/amd64/wintun.dll > "$DLL"
fi

echo "Проверяю тесты..."
go vet ./...
GOOS=windows GOARCH=amd64 go vet ./...
go test ./...
( cd ../android/core && go test ./... )

# Манифест с requireAdministrator и иконка — секцией ресурсов в exe, как и у
# обычной версии (см. src/build.sh).
echo "Вшиваю иконку и манифест..."
go run github.com/akavel/rsrc@v0.10.2 \
  -manifest cmd/ssh_tunnel_vpn/app.manifest \
  -ico ../src/internal/nativeui/icon.ico \
  -arch amd64 \
  -o cmd/ssh_tunnel_vpn/rsrc_windows_amd64.syso

echo "Windows: VPN..."
GOOS=windows GOARCH=amd64 go build \
  -ldflags="-s -w -H windowsgui $LDVERSION" \
  -o "$OUT/ssh_tunnel_vpn.exe" ./cmd/ssh_tunnel_vpn

ls -lh "$OUT/ssh_tunnel_vpn.exe"
echo "Готово."
