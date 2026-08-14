# Сюда попадают собранные файлы

Папка намеренно пустая: готовые сборки лежат не в репозитории, а на
[странице релизов](https://github.com/VITAZGIO/ssh_tunel/releases) — иначе
каждая пересборка добавляла бы к истории десятки мегабайт.

## Скачать готовое

[Последний релиз](https://github.com/VITAZGIO/ssh_tunel/releases/latest):

| Файл | Для чего |
|---|---|
| `ssh_tunnel.exe` | Windows, версия с окном |
| `ssh_tunnel_linux` | Linux amd64 — обычные серверы и компьютеры |
| `ssh_tunnel_linux_arm64` | Linux ARM — Raspberry Pi, ARM-облако |
| `ssh_tunnel_windows.zip` | тот же exe в архиве |
| `SHA256SUMS.txt` | контрольные суммы |

Ссылки, которые всегда ведут на свежую версию:

```
https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.exe
https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
```

Архив с exe нужен не для экономии места (он почти ничего не экономит), а ради
браузеров: неподписанный `.exe` многие помечают как опасный прямо при
скачивании, а к `.zip` не придираются. Внутри тот же файл.

## Собрать самому

```bash
cd src
./build.sh
```

Файлы появятся здесь, в `windows/` и `linux/`. В git они не попадут — так
задано в `.gitignore`.

Консольная версия для Windows в релиз не входит, но собирается отдельно:

```bash
cd src && GOOS=windows go build -o ../releases/windows/ssh_tunnel_cli.exe ./cmd/ssh_tunnel_cli
```

## Проверить целостность

Windows:

```powershell
Get-FileHash .\ssh_tunnel.exe -Algorithm SHA256
```

Linux:

```bash
sha256sum -c SHA256SUMS.txt
```
