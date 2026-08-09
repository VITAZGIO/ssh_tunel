# Готовые сборки

## Windows

| Файл | Что это |
|---|---|
| [`windows/ssh_tunel.exe`](windows/ssh_tunel.exe) | версия с окном, запускается двойным щелчком |
| [`windows/ssh_tunel-cli.exe`](windows/ssh_tunel-cli.exe) | консольная версия, управляется флагами |

## Linux

| Файл | Что это |
|---|---|
| [`linux/ssh_tunel-linux`](linux/ssh_tunel-linux) | обычные серверы и компьютеры (amd64) |
| [`linux/ssh_tunel-linux-arm64`](linux/ssh_tunel-linux-arm64) | ARM: Raspberry Pi, ARM-облако, мини-серверы |

Служба systemd и установщик — в [../packaging/linux](../packaging/linux).

## Проверить целостность

Контрольные суммы всех файлов лежат в `SHA256SUMS.txt`.

Windows:

```powershell
Get-FileHash .\ssh_tunel.exe -Algorithm SHA256
```

Linux:

```bash
sha256sum -c SHA256SUMS.txt
```

Всё собрано из `../src` командой `./build.sh`. Ни установки, ни дополнительных
библиотек, ни прав администратора не требуется.
