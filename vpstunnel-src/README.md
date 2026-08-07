# vpstunnel — SSH-туннель (SOCKS4/SOCKS5) + системный прокси Windows

## Что это
Один exe-файл: встроенный SSH-клиент поднимает SOCKS-прокси (понимает и
SOCKS4/4a, и SOCKS5 — оба нужны, разные приложения Windows используют
разные версии протокола), сам просит права администратора и сам
прописывает системный прокси Windows в реестр.

## Как собрать (на машине с нормальным доступом в интернет — не в
ограниченной песочнице)

```bash
go mod tidy      # подтянет golang.org/x/crypto и x/sys, создаст go.sum
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o vpstunnel.exe .
```

Готовый `vpstunnel.exe` появится в этой же папке.

## Как запускать (на Windows)

```powershell
.\vpstunnel.exe -host 87.58.210.143 -user root -key C:\Users\vitaz\.ssh\id_ed25519
```

Флаги: -host (обязательный), -user (по умолчанию root), -key (по
умолчанию ~/.ssh/id_ed25519), -port (локальный SOCKS-порт, по умолчанию
1080), -sysproxy (включать ли системный прокси Windows, по умолчанию true).

## Файлы
- `main.go` — вся логика: SSH-клиент, встроенный SOCKS4/SOCKS5-сервер,
  проброс трафика через SSH-каналы
- `windows_admin.go` (build tag: windows) — UAC-запрос прав через
  ShellExecute("runas"), запись системного прокси в реестр
  (HKCU\...\Internet Settings), уведомление Windows о смене настроек
- `other_admin.go` (build tag: !windows) — заглушки для сборки/линтинга
  на не-Windows системах

## Известные особенности (не баги, так и задумано)
- Системный прокси Windows покрывает браузеры и приложения, использующие
  системные сетевые настройки (WinINET/WinHTTP) — это больше, чем
  "только браузер", но не 100% всего (игры и часть desktop-приложений
  со своим сетевым стеком проксирование обходят)
- HostKeyCallback сейчас `InsecureIgnoreHostKey()` — для полной строгости
  стоит заменить на проверку через known_hosts
