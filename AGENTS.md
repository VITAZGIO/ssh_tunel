# ssh_tunnel — карта проекта для ИИ-агентов

Прочитай этот файл перед правками — он экономит на разведке по коду. Подробности
архитектуры и протоколов — в `docs/ARCHITECTURE.md`, их читать не обязательно,
если правка локальная (см. таблицу ниже).

## Что это

Go-программа: поднимает пул SSH-соединений до сервера пользователя и
разворачивает поверх него локальный SOCKS4/4a/5 + HTTP CONNECT прокси. Три
платформы из одного ядра: Windows/Linux (`src/`) и Android (`android/`).

## Где что лежит — начинай отсюда, не с Glob/Grep по всему репо

| Нужно поправить | Файл(ы) |
|---|---|
| Веб-интерфейс: вёрстка/CSS | `src/internal/webui/assets/index.html`, `.../style.css` |
| Веб-интерфейс: поведение (JS) | `src/internal/webui/assets/app.js` |
| HTTP-роуты, API окна | `src/internal/webui/server.go` |
| Логика самого туннеля (SSH-пул, каналы) | `src/internal/tunnel/tunnel.go` |
| SOCKS4/4a/5 | `src/internal/tunnel/socks.go` |
| Перекладывание байт между соединениями | `src/internal/tunnel/relay.go` |
| Конфиг (поля, чтение/запись) | `src/internal/config/config.go` |
| Точки входа (main для каждой ОС) | `src/cmd/ssh_tunnel` (Windows-окно), `src/cmd/ssh_tunnel_cli`, `src/cmd/ssh_tunnel_linux` |
| Системный прокси (реестр/netsh на Windows, аналог на Linux) | `src/internal/sysproxy/sysproxy_{windows,linux}.go` |
| Иконка/меню в трее (Windows) | `src/internal/nativeui/` |
| Тест скорости | `src/internal/speedtest/speedtest.go` |
| Android: VPN-сервис, ядро на Go | `android/core/`, `android/app/src/main/kotlin/.../TunnelService.kt` |
| Сборка релизов (Windows/Linux) | `src/build.sh` |
| CI (Android APK) | `.github/workflows/` |

Не читай `docs/ARCHITECTURE.md`, `docs/SERVER_SETUP.md`, `docs/TROUBLESHOOTING.md`,
`README.md`/`README.en.md` целиком ради мелкой правки — это документация для
людей, не карта кода. Открывай их только если меняешь протокол/архитектуру или
пользователь прямо просит обновить документацию.

## Важно про веб-интерфейс

`index.html` — только разметка (~290 строк). CSS и JS — отдельные файлы
(`style.css`, `app.js`), их отдаёт `server.go` через `/style.css` и `/app.js`
(embed, см. `handleAsset` в `server.go`). При добавлении нового статического
файла в `assets/` — не забудь завести для него роут в `Serve()`, иначе браузер
получит 404 (см. `handleIndex`, который отвечает только на `/`).

## Сборка и тесты

```bash
cd src
go build ./...          # быстрая проверка компиляции
go test ./...            # все тесты
go vet ./...
./build.sh                # полная сборка релизов (Windows+Linux) в ../releases
```

Android — отдельно, тестируется/собирается только в CI (`android/core` требует
`/dev/net/tun`, `go test` локально частично пропускается):
```bash
cd android/core && go vet ./... && go test ./...
```

## Конвенции

- Комментарии в коде — по-русски, только там, где объясняют неочевидное решение
  или обходной путь (см. существующий стиль в `tunnel.go`, `webui/server.go`).
  Не описывай, что делает код — только зачем сделано именно так.
- Новую страницу/раздел веб-интерфейса — размечать в `index.html`, стили в
  `style.css`, поведение в `app.js`. Не возвращай инлайновые `<style>`/`<script>`
  обратно в `index.html`.
- Не трогай `packaging/`, `releases/` руками — это результат `build.sh` и CI.
