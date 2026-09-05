<div align="center">

# ssh_tunnel

**Пропускает трафик приложений через твой собственный Linux-сервер по обычному SSH.**

Один файл, без установки, без прав администратора.
Учебный проект: показывает, как из штатного механизма SSH собирается рабочий
локальный прокси.

[![Лицензия: MIT](https://img.shields.io/badge/лицензия-MIT-4c8dff)](LICENSE)
[![Версия](https://img.shields.io/badge/версия-1.0.0-4c8dff)](https://github.com/VITAZGIO/ssh_tunel/releases)
[![Windows](https://img.shields.io/badge/Windows-10%20%7C%2011-2de2ff)](https://github.com/VITAZGIO/ssh_tunel/releases/latest)
[![Linux](https://img.shields.io/badge/Linux-amd64%20%7C%20arm64-2de2ff)](https://github.com/VITAZGIO/ssh_tunel/releases/latest)
[![Android](https://img.shields.io/badge/Android-8.0+-2de2ff)](https://github.com/VITAZGIO/ssh_tunel/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.22+-2de2ff)](src)

🇷🇺 Русский · [🇬🇧 English](README.en.md)

### Скачать

| Система | Файл | |
|---|---|---|
| **Windows** | [**ssh_tunnel.exe**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.exe) | окно с кнопкой, значок у часов |
| **Linux** | [**ssh_tunnel_linux**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux) | консоль + веб-интерфейс, служба systemd — [команды под свою систему](#команды-под-свою-систему) |
| **Android** | [**ssh_tunnel.apk**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.apk) | VPN-подключение, кнопка в шторке |

Сборка под ARM и контрольные суммы — на
[странице релиза](https://github.com/VITAZGIO/ssh_tunel/releases/latest).

Заводишь **новый VPS** под сервер — это не файл для скачивания, а порядок
действий: [первая настройка сервера](docs/SERVER_SETUP.md) — ключи, firewall,
отдельный пользователь `tunnel`.

<img src="docs/screenshot.png" width="300" alt="главный экран">
<img src="docs/screenshot_filter.png" width="300" alt="выбор программ">

</div>

---

## Что это

У SSH есть штатная возможность — проброс TCP-соединений (`direct-tcpip`, то же
самое, что делает `ssh -D`). `ssh_tunnel` поднимает SSH-соединение с **твоим**
сервером и разворачивает поверх него локальный прокси: приложение обращается к
прокси на своей же машине, а соединение до нужного адреса открывает сервер.

Зачем это бывает нужно: посмотреть, как сервис отвечает с адреса твоего сервера,
дотянуться до внутренней сети, где ты и так работаешь по SSH, отладить работу
приложения через прокси, дать программе выход в сеть с постоянного адреса.

Никаких чужих серверов, подписок и учётных записей: нужен только сервер, к
которому у тебя есть доступ по SSH.

Проект сделан в образовательных целях — чтобы разобраться, как устроены SOCKS,
HTTP CONNECT, каналы SSH и определение процесса по сокету. Как ты его
используешь и не нарушает ли это правил твоего провайдера, сервиса или
законодательства — твоя ответственность.

### Ключевые возможности

- 🔌 **Три протокола сразу** — SOCKS5, SOCKS4/4a и HTTP CONNECT. Разные
  программы умеют разное, и нужны все три.
- 🎯 **Разделение трафика по программам** — через туннель идёт всё, только
  выбранные приложения, или всё кроме выбранных. Правила меняются на ходу,
  без переподключения.
- 🏠 **Локальная сеть остаётся доступной** — роутер, NAS, Home Assistant и
  прочее по адресам `192.168.x.x` или по имени идут напрямую, а не через
  сервер. Mesh-VPN (NetBird, Tailscale) тоже: их адреса `100.64.x.x` учтены.
  Для остальных сетей есть свой список «всегда напрямую».
- 🧩 **Работает там, где системный прокси бессилен** — Node.js, Python, Go и всё
  на них (Claude Code, npm, pip, curl) читают только переменные окружения,
  и программа их прописывает.
- ♻️ **Переживает обрывы связи** — пул SSH-соединений с проверкой живости и
  переподключением. Заснувший ноутбук не ломает туннель.
- 🛟 **Возвращает настройки как было** — при любом закрытии, включая аварийное.
  Интернет после выхода не пропадает.
- 🔑 **Проверяет ключ сервера** — подмена сервера по дороге не пройдёт незаметно.
- 📈 **Меряет реальную скорость** — тест в несколько потоков через туннель.
- 👀 **Видно, кто ходит в интернет** — живой список программ и адресов, с
  пометкой, если DNS-запрос ушёл мимо туннеля.

> Подробное техническое описание — в [ARCHITECTURE.md](docs/ARCHITECTURE.md).

---

## Как это устроено (кратко)

```
  приложение
      │  SOCKS4/4a/5 (1080)   HTTP CONNECT (1081)
      ▼
  ssh_tunnel  ──── зашифрованный SSH (22) ────►  твой сервер  ──►  сеть
```

1. Приложение думает, что говорит с обычным прокси на своей же машине.
2. Программа разбирает запрос, узнаёт адрес назначения и открывает до него
   канал `direct-tcpip` через SSH.
3. Имя хоста разрешает сервер, а не твой компьютер: соединение выходит в сеть
   с адреса сервера целиком, включая DNS-запрос.

---

## Что нужно

**На твоей машине:**
- Windows 10 / 11 либо Linux (amd64 / arm64)
- больше ничего: файл самодостаточный, без установщика и без библиотек

**На сервере:**
- любой Linux-сервер с доступом по SSH — свой или арендованный, разницы нет.
  Ставить на него ничего не надо: прокси разворачивается на твоей машине,
  сервер только пропускает через себя соединения.

Сервер только что арендован и настроен «как из коробки»? Первым делом стоит
закрыть вход по паролю и включить firewall — готовый набор команд одним блоком
лежит в [первой настройке сервера](docs/SERVER_SETUP.md).

---

## Windows

1. Скачай [**ssh_tunnel.exe**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.exe)
   и положи в постоянную папку.

   Браузер может обозвать файл опасным — программа не подписана сертификатом
   (это стоит денег и требует юрлица), а к неподписанным и малоизвестным файлам
   он придирается по умолчанию. Чтобы не спорить с браузером, скачай через
   PowerShell — заодно файл не получит метку «загружено из интернета»:

   ```powershell
   curl.exe -fL --ssl-no-revoke -o "$env:USERPROFILE\Desktop\ssh_tunnel.exe" https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.exe
   Get-FileHash "$env:USERPROFILE\Desktop\ssh_tunnel.exe" -Algorithm SHA256
   ```

   Вторая команда печатает контрольную сумму — сверь её с `SHA256SUMS.txt` на
   странице релиза. Это надёжнее любой подписи: ты проверяешь именно тот файл,
   который скачал.
2. Запусти двойным щелчком. Если файл всё же скачан браузером, Windows
   предупредит о неизвестном издателе — «Подробнее» → «Выполнить в любом
   случае».
3. Открой настройки (шестерёнка), укажи адрес сервера и пользователя.
   Ключа ещё нет? Нажми знак вопроса у поля «Приватный ключ» — там готовая
   команда, которая создаст ключ и положит его на сервер.
4. Сохрани, вернись назад, нажми круглую кнопку.

Крестик прячет программу в трей, туннель продолжает работать. Выйти совсем —
правый щелчок по значку у часов → «Выход».

Нужна консольная версия для Windows — она есть в исходниках, собирается одной
командой: `GOOS=windows go build -o ssh_tunnel_cli.exe ./cmd/ssh_tunnel_cli`.

---

## Linux

Для серверов и рабочих станций, amd64 и arm64. Файл собран статически: ему не
нужны ни библиотеки, ни определённая версия glibc — он одинаково запускается на
Debian, Fedora, Arch и даже на Alpine с musl.

```bash
# скачать и сделать исполняемым
curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux

# задать сервер один раз
./ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save

# запустить: только туннель...
./ssh_tunnel_linux

# ...или сразу с веб-интерфейсом
./ssh_tunnel_linux -web

# ...или с панелью, открытой для домашней сети
./ssh_tunnel_linux -web -web-lan
```

### Команды под свою систему

Сама программа везде одна и та же — отличается только то, чем ставится `curl`,
чем открывается порт в firewall и как заводится служба. Разверни свою систему:

<details>
<summary><b>Ubuntu · Debian · Linux Mint · Raspberry Pi OS</b></summary>

```bash
sudo apt update && sudo apt install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

Панель откроется на `http://АДРЕС_МАШИНЫ:47821`. Если включён ufw, пустить в неё
только домашнюю сеть:

```bash
sudo ufw allow from 192.168.0.0/16 to any port 47821 proto tcp
```

Автозапуск — галочкой «Запускать при старте системы» прямо в панели.

На Raspberry Pi и других ARM-машинах имя файла другое:
`ssh_tunnel_linux_arm64`.
</details>

<details>
<summary><b>Proxmox VE</b></summary>

Proxmox — это Debian, но работают там обычно под `root` и без пользовательской
сессии. Поэтому служба ставится **системная**, а не пользовательская, и галочка
автозапуска в панели тут не поможет — она рассчитана на `systemctl --user`.

```bash
apt update && apt install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user root -save
```

Системная служба (запускается при загрузке хоста, до всякого входа):

```bash
tee /etc/systemd/system/ssh_tunnel.service >/dev/null <<'EOF'
[Unit]
Description=ssh_tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ssh_tunnel_linux -web -web-lan
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ssh_tunnel
```

Настройки при этом лежат в `/root/.config/ssh_tunnel/`. Если включён firewall
Proxmox (Datacenter → Firewall), порт `47821` надо разрешить там же, в веб-морде
Proxmox, — `ufw` в нём не участвует.
</details>

<details>
<summary><b>Fedora · RHEL · CentOS Stream · Rocky · AlmaLinux</b></summary>

```bash
sudo dnf install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

Firewall здесь `firewalld`, а не `ufw`:

```bash
sudo firewall-cmd --permanent --add-port=47821/tcp
sudo firewall-cmd --reload
```

SELinux программе не мешает: она не лезет ни в системные папки, ни в чужие
порты — всё своё держит в домашней папке пользователя.
</details>

<details>
<summary><b>Arch · Manjaro · EndeavourOS</b></summary>

```bash
sudo pacman -S --needed curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

Firewall по умолчанию не стоит вовсе — если поставил свой, порт `47821` открывай
в нём. Всё остальное как в Ubuntu, systemd тот же.
</details>

<details>
<summary><b>openSUSE</b></summary>

```bash
sudo zypper install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

Порт открывается через `firewalld`, как в Fedora:

```bash
sudo firewall-cmd --permanent --add-port=47821/tcp && sudo firewall-cmd --reload
```
</details>

<details>
<summary><b>Alpine Linux</b></summary>

Единственная система из списка, где **нет systemd** — там OpenRC. Программа
работает (файл статический, musl ей не помеха), но автозапуск заводится иначе,
и галочка в панели его не сделает.

```sh
apk add curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host ТВОЙ_СЕРВЕР -user tunnel -save
```

Служба OpenRC:

```sh
cat > /etc/init.d/ssh_tunnel <<'EOF'
#!/sbin/openrc-run
command="/usr/local/bin/ssh_tunnel_linux"
command_args="-web -web-lan"
command_background=true
pidfile="/run/ssh_tunnel.pid"
depend() { need net; }
EOF

chmod +x /etc/init.d/ssh_tunnel
rc-update add ssh_tunnel default
rc-service ssh_tunnel start
```
</details>

### Автозапуск при включении машины

В панели, внизу настроек, — две галочки рядом:

- **«Запускать при старте системы»** — программа сама положит файл службы
  systemd, включит её и разрешит ей работать без входа пользователя в систему.
  Последнее требует прав администратора: если система попросит пароль, прямо
  там появится поле для него — пароль используется один раз и никуда не
  сохраняется.
- **«Подключаться сразу при запуске»** — туннель поднимется сам, как только
  программа стартует, без нажатия «Подключить» в панели. Включена по
  умолчанию — это и есть обычное поведение; выключи, если хочешь запускать
  туннель вручную.

Вместе они и дают то самое «включил сервер — туннель уже поднят»: первая
галочка поднимает саму программу при загрузке, вторая — сразу подключает её.

Если на машине найден Docker, чуть ниже появляется третья галочка — включить
автозапуск и ему, чтобы после перезагрузки контейнеры и туннель поднимались
вместе.

Работает это на всём, где есть systemd. Исключения — Proxmox под `root` и
Alpine: там служба ставится руками, команды выше.

Веб-интерфейс — тот же самый, что и в версии для Windows, на постоянном порту
`47821`. Порт выбран нестандартным, чтобы не столкнуться с тем, что уже занято
на сервере.

По умолчанию панель слушает только `127.0.0.1`, а в адресе нужен ключ доступа —
он печатается при запуске. Так до неё не дотянуться ни из сети, ни со страницы,
случайно открытой в браузере.

Флаг **`-web-lan`** превращает её в домашний сервис вроде Home Assistant:
адрес постоянный, ключ не нужен, открывается прямо по адресу машины —

```
http://192.168.1.203:47821
```

Пускает при этом только своих: адреса локальной сети (`192.168.x.x`, `10.x.x.x`,
`172.16–31.x.x`) и mesh-VPN (`100.64.x.x`). С публичного адреса ключ по-прежнему
обязателен, а проверка `Origin` не даёт чужому сайту, открытому в соседней
вкладке, что-нибудь нажать за тебя. Управлять туннелем сможет любой в этой сети —
для домашней это обычно и нужно, для чужой так оставлять не стоит.

Подключить прокси в текущую оболочку (curl, git, apt, docker, pip, npm):

```bash
source ~/.config/ssh_tunnel/proxy.env
```

Как служба systemd — [packaging/linux](packaging/linux):

```bash
./packaging/linux/install.sh ./ssh_tunnel_linux --lan   # без --lan — только с самой машины
systemctl --user enable --now ssh_tunnel
```

---

## Android

Android 8.0 и новее. Устроено иначе, чем на компьютере, и это принципиально:
локального прокси, который надо прописывать в настройках, здесь нет.

Приложение поднимает **VPN-подключение** средствами самой системы, забирает
из неё сырые IP-пакеты и разбирает их своим сетевым стеком, а наружу выпускает
всё тем же SSH-соединением. Приложениям настраивать нечего — они просто ходят
в интернет, а система отдаёт их трафик нам.

1. Скачай [**ssh_tunnel.apk**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel.apk).
2. Открой файл. Android спросит разрешение ставить приложения из этого
   источника — оно нужно один раз.
3. Шестерёнка → адрес сервера, пользователь `tunnel`, закрытый ключ. Ключ
   вставляется целиком, вместе со строками `-----BEGIN...` и `-----END...`.
   После сохранения поле очищается: на экране ему не место.
4. Кнопка на главном экране. Первый раз система спросит подтверждение
   VPN-подключения — это её собственный запрос, не наш.

Ключ для телефона делай **отдельный**, а не тот же, что на компьютере: тогда
потеря одного устройства не тянет за собой второе.

```bash
ssh-keygen -t ed25519 -f ~/.ssh/phone -C "phone"
```

Открытую часть (`phone.pub`) добавь на сервере в
`/home/tunnel/.ssh/authorized_keys` — с теми же ограничениями, что и остальные,
см. [docs/SERVER_SETUP.md](docs/SERVER_SETUP.md).

**Что есть:** выбор приложений (какие вести через туннель, какие мимо) —
отбором занимается сама система; кнопка в шторке быстрых настроек; журнал
соединений; тест скорости; задержка до сервера.

**Чего нет:** UDP. Через SSH он не проходит, поэтому звонки и игры, которым
нужен UDP, надо выносить в исключения — они пойдут напрямую. Веб и
мессенджеры работают: приложение отбивает UDP сразу, и они за доли секунды
переключаются на TCP.

---

## Настройки

Лежат в `%APPDATA%\ssh_tunnel\config.json` на Windows и в
`~/.config/ssh_tunnel/config.json` на Linux. Окно и флаги пишут один и тот же файл.

| Ключ | Назначение |
|---|---|
| `host` | Адрес твоего сервера. |
| `sshPort` | Порт SSH, по умолчанию `22`. |
| `user` | Пользователь для входа. |
| `keyPath` | Приватный ключ. Находится сам среди `id_ed25519`, `id_ecdsa`, `id_rsa`. |
| `socksPort` | Локальный порт SOCKS4/4a/5, по умолчанию `1080`. |
| `httpPort` | Локальный порт HTTP-прокси, по умолчанию `1081`. |
| `poolSize` | Сколько параллельных SSH-соединений. Больше потоков — выше скорость на длинном канале. |
| `filterMode` | `all`, `only` или `except` — режим разделения трафика. |
| `filterApps` | Список программ, к которым режим применяется. |
| `localViaTunnel` | Вести ли через сервер и локальную сеть. По умолчанию `false` — она идёт напрямую. |
| `directHosts` | Свой список адресов и сетей, которые всегда идут напрямую: `100.64.0.0/10`, `10.8.*`, `.netbird.cloud`. |

Флаги версии для Linux: `-host`, `-sshport`, `-user`, `-key`, `-port`,
`-httpport`, `-pool`, `-filter`, `-apps`, `-direct`, `-local-via-tunnel`,
`-sysproxy`, `-setenv`, `-save`, `-env`, `-web`, `-web-lan`, `-web-listen`, `-v`.

---

## Безопасность

- **Ключ сервера проверяется.** При первом подключении его отпечаток
  запоминается (TOFU) в `known_hosts`; если потом он изменится, программа
  откажется соединяться, а не будет молча говорить с тем, кто ответил.
- **Имена разрешает сервер**, поэтому запрос уходит целиком через туннель.
  Соединения, пришедшие уже с адресом (приложение разрешило имя само),
  помечаются в журнале: они выходят наружу не полностью через сервер.
- **Системный прокси возвращается при любом выходе**, включая аварийный: есть
  сохранённый снимок и восстановление при следующем запуске. Остаться без
  интернета из-за того, что программа упала, недопустимо.
- **Локальный веб-интерфейс защищён случайным токеном** и проверкой `Origin`, на
  случайном порту: посторонняя страница, открытая в браузере, не должна иметь
  возможности достучаться до `127.0.0.1` и выключить туннель или прочитать
  настройки.
- **Прав администратора не требуется.** Всё пишется под текущим пользователем.

Чего программа не делает: она не маскирует сама себя. Со стороны это обычное
SSH-соединение с твоим сервером — со всеми свойствами обычного SSH-соединения,
не больше и не меньше. Полная картина — в [SECURITY.md](docs/SECURITY.md).

---

## Чего туннель не покрывает

- Программы со своим сетевым стеком, игнорирующие и системный прокси, и
  переменные окружения — часть игр, некоторые мессенджеры.
- UDP целиком: SSH пробрасывает только TCP. Браузеры при недоступности QUIC
  откатываются на TCP сами.
- DNS-запросы приложений, которые резолвят имена сами, до обращения к прокси.
  Такие соединения помечаются в журнале.
- Локальную сеть — намеренно: адреса `192.168.x.x`, `10.x.x.x`, имена без точек
  и суффиксы `.local`/`.lan`/`.home` идут напрямую, иначе домашние сервисы
  перестали бы открываться. Нужно наоборот (внутренняя сеть самого сервера) —
  галочка «Локальную сеть тоже вести через сервер» в настройках.

---

## Сборка из исходников

Нужен только Go 1.22 или новее — ни компилятора C, ни внешних библиотек.

```bash
cd src
go test ./... -race     # проверить, что всё работает
./build.sh              # собрать всё в ../releases
```

```
src/          исходный код (модуль Go)
android/      приложение (Kotlin) и сетевой стек к нему (Go)
packaging/    служба systemd и установщик для Linux
docs/         архитектура, безопасность, диагностика
```

Приложение для Android локально не собрать: нужны Android SDK и NDK. Оно
собирается на серверах GitHub — workflow `android` на каждый пуш и `release`
при выпуске. Ключ подписи заводится один раз, см.
[docs/ANDROID_SIGNING.md](docs/ANDROID_SIGNING.md).

Готовые файлы в репозитории не лежат — они публикуются в
[релизах](https://github.com/VITAZGIO/ssh_tunel/releases), чтобы история не
пухла от бинарников.

---

## Вклад в проект

PR и issue приветствуются. Пожалуйста, не коммить личные данные (адрес сервера,
ключи, `config.json`, `known_hosts`) — они уже в `.gitignore`.

## Лицензия

[MIT](LICENSE) © 2026 Vitaliy ([VITAZGIO](https://github.com/VITAZGIO)).
Разработано в паре с Claude (Anthropic).
