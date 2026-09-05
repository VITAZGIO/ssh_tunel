# Установка на Linux

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

Сборка под ARM и контрольные суммы — на
[странице релиза](https://github.com/VITAZGIO/ssh_tunel/releases/latest).

---

## Команды под свою систему

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

---

## Автозапуск при включении машины

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

---

## Веб-интерфейс

Тот же самый, что и в версии для Windows, на постоянном порту `47821`. Порт
выбран нестандартным, чтобы не столкнуться с тем, что уже занято на сервере.

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

---

## Прокси в оболочке

Подключить прокси в текущую оболочку (curl, git, apt, docker, pip, npm):

```bash
source ~/.config/ssh_tunnel/proxy.env
```

Как служба systemd — [packaging/linux](../packaging/linux):

```bash
./packaging/linux/install.sh ./ssh_tunnel_linux --lan   # без --lan — только с самой машины
systemctl --user enable --now ssh_tunnel
```

---

Заводишь новый сервер под туннель? Смотри
[первую настройку сервера](SERVER_SETUP.md).
