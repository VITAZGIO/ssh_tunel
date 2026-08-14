# Первая настройка сервера

Что сделать с чистым сервером сразу после того, как хостер прислал пароль от
`root`, чтобы его не увели в первую же ночь.

Рассчитано на Debian 11/12 и Ubuntu 22.04/24.04 — то есть на образы, которые
даёт почти любой хостинг. Всё делается один раз, дальше про сервер можно
забыть: обновления безопасности он будет ставить сам.

> Порядок важен. Сначала ключ, потом всё остальное — иначе можно закрыть
> вход паролем и остаться снаружи.

---

## Шаг 1. Закинуть на сервер свой ключ

Это делается **со своего компьютера**, а не на сервере.

Нет ключа — создать (Enter на все вопросы, пароль на ключ по желанию):

```bash
ssh-keygen -t ed25519
```

Положить его на сервер (спросит пароль от сервера — последний раз):

```bash
ssh-copy-id root@АДРЕС_СЕРВЕРА
```

В Windows то же самое одной строкой в PowerShell:

```powershell
ssh-keygen -t ed25519 -f $env:USERPROFILE\.ssh\id_ed25519 -N '""'; type $env:USERPROFILE\.ssh\id_ed25519.pub | ssh root@АДРЕС_СЕРВЕРА "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys"
```

Проверить, что вход по ключу работает — пароль спрашивать не должен:

```bash
ssh root@АДРЕС_СЕРВЕРА
```

Спросил пароль — дальше не ходи, разберись с ключом. Скрипт ниже это проверяет
и сам откажется работать, но лучше убедиться заранее.

---

## Шаг 2. Один скрипт

Зайти на сервер по SSH и вставить целиком. Наверху три строки, которые можно
поправить под себя, — остальное трогать не надо.

```bash
cat > /root/harden.sh <<'ENDOFSCRIPT'
#!/usr/bin/env bash
# Базовая защита свежего сервера: ключи вместо паролей, firewall, бан за
# перебор, автоматические обновления безопасности.
set -euo pipefail

# ─── что настроить ────────────────────────────────────────────────────────
NEW_USER="admin"            # обычный пользователь с sudo; пусто = не создавать
SSH_PORT=22                 # менять только если понимаешь зачем (см. документ)
TIMEZONE="Europe/Moscow"    # пусто = не трогать
# ──────────────────────────────────────────────────────────────────────────

say()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m[!] %s\033[0m\n' "$*"; }
die()  { printf '\n\033[1;31mОШИБКА: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "запускать от root"
command -v apt-get >/dev/null || die "скрипт рассчитан на Debian и Ubuntu"

# ─── Проверка ключа. Самое важное: без неё можно отрезать себе вход ────────
say "Проверяю, что вход по ключу настроен"
[ -s /root/.ssh/authorized_keys ] || die \
"в /root/.ssh/authorized_keys пусто.
   Сначала со своего компьютера: ssh-copy-id root@АДРЕС_СЕРВЕРА
   Отключать пароли, не имея ключа, нельзя — останешься снаружи."
KEYS=$(grep -cv '^[[:space:]]*\(#\|$\)' /root/.ssh/authorized_keys || true)
echo "ключей найдено: $KEYS"
[ "$KEYS" -gt 0 ] || die "в authorized_keys только пустые строки и комментарии"

export DEBIAN_FRONTEND=noninteractive

say "Обновляю систему"
apt-get update -qq
apt-get -y -qq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade

say "Ставлю firewall, защиту от перебора и автообновления"
apt-get -y -qq install ufw fail2ban unattended-upgrades iproute2 ca-certificates
# Нужен fail2ban, чтобы читать журнал systemd. Есть не во всех репозиториях —
# если нет, обойдёмся, jail просто будет читать файл журнала.
apt-get -y -qq install python3-systemd 2>/dev/null || true

# ─── Обычный пользователь вместо root для повседневной работы ─────────────
if [ -n "$NEW_USER" ]; then
  say "Пользователь $NEW_USER"
  if id -u "$NEW_USER" >/dev/null 2>&1; then
    echo "уже есть, пропускаю создание"
  else
    adduser --disabled-password --gecos "" "$NEW_USER"
  fi
  usermod -aG sudo "$NEW_USER"
  install -d -m 700 -o "$NEW_USER" -g "$NEW_USER" "/home/$NEW_USER/.ssh"
  install -m 600 -o "$NEW_USER" -g "$NEW_USER" \
    /root/.ssh/authorized_keys "/home/$NEW_USER/.ssh/authorized_keys"
  # sudo без пароля: пароля у пользователя нет вообще, вход только по ключу,
  # и запрос пароля превратился бы в тупик.
  echo "$NEW_USER ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$NEW_USER"
  chmod 440 "/etc/sudoers.d/90-$NEW_USER"
fi

# ─── SSH: только ключи ────────────────────────────────────────────────────
say "Настраиваю SSH"
cp -n /etc/ssh/sshd_config /etc/ssh/sshd_config.before-harden 2>/dev/null || true

if grep -q '^Include /etc/ssh/sshd_config.d/\*.conf' /etc/ssh/sshd_config; then
  mkdir -p /etc/ssh/sshd_config.d
  # Имя начинается с 00 не для красоты: OpenSSH берёт ПЕРВОЕ найденное
  # значение каждой настройки, а файлы читаются по алфавиту. Образы Ubuntu на
  # многих хостингах кладут туда 50-cloud-init.conf с
  # "PasswordAuthentication yes" — с именем 99-... наш файл проиграл бы ему,
  # и вход по паролю остался бы включён, хотя скрипт отчитался бы об успехе.
  SSHD_CONF=/etc/ssh/sshd_config.d/00-hardening.conf
  : > "$SSHD_CONF"
else
  # Старая система без Include — дописываем в основной файл, убрав прошлый
  # хвост от этого же скрипта, чтобы повторный запуск не плодил копии.
  SSHD_CONF=/etc/ssh/sshd_config
  sed -i '/^# --- ssh_tunel hardening ---$/,$d' "$SSHD_CONF"
  echo "# --- ssh_tunel hardening ---" >> "$SSHD_CONF"
fi

# По той же причине (первое значение выигрывает) гасим все прежние объявления
# этих настроек, где бы они ни лежали. Иначе строка из образа хостинга тихо
# перебила бы нашу.
DIRECTIVES='PasswordAuthentication|PermitRootLogin|KbdInteractiveAuthentication'
DIRECTIVES="$DIRECTIVES|ChallengeResponseAuthentication|PubkeyAuthentication"
DIRECTIVES="$DIRECTIVES|PermitEmptyPasswords|AllowTcpForwarding|Port|X11Forwarding"
# Свой файл на этот момент ещё пуст (или из него только что вырезан прошлый
# блок), поэтому проходим по всем файлам без исключений — иначе в режиме без
# Include старый "Port 22" из основного файла остался бы выше нашего и выиграл.
for f in /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf; do
  [ -f "$f" ] || continue
  sed -ri "s/^([[:space:]]*($DIRECTIVES)[[:space:]])/# выключено ssh_tunel: \\1/I" "$f"
done

cat >> "$SSHD_CONF" <<EOF
Port $SSH_PORT
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitEmptyPasswords no
MaxAuthTries 3
LoginGraceTime 30
X11Forwarding no
ClientAliveInterval 60
ClientAliveCountMax 3

# Проброс TCP нужен для SSH-туннеля (ssh -D, ssh -L и ssh_tunel).
# Многие "гайды по безопасности" его выключают — тогда туннель не работает.
AllowTcpForwarding yes
EOF

sshd -t || die "конфиг SSH не прошёл проверку, ничего не перезапускаю"

# На Ubuntu 22.10+ SSH поднимается через сокет systemd, и порт задаётся там, а
# не в sshd_config. Без этого смена порта молча не сработала бы — а firewall
# уже открыл бы только новый порт. Это и есть типовой способ потерять сервер.
if systemctl is-enabled ssh.socket >/dev/null 2>&1; then
  mkdir -p /etc/systemd/system/ssh.socket.d
  printf '[Socket]\nListenStream=\nListenStream=%s\n' "$SSH_PORT" \
    > /etc/systemd/system/ssh.socket.d/port.conf
  systemctl daemon-reload
  systemctl restart ssh.socket
fi
systemctl restart ssh 2>/dev/null || systemctl restart sshd

sleep 1
ss -tln | grep -q ":$SSH_PORT " || die \
"SSH не слушает порт $SSH_PORT. Firewall НЕ включён, вход по паролю мог уже
   отключиться — чини прямо в этой сессии, не закрывая её:
     rm -f /etc/ssh/sshd_config.d/00-hardening.conf && systemctl restart ssh"
echo "SSH слушает порт $SSH_PORT"

# ─── Firewall: наружу открыт только SSH ───────────────────────────────────
say "Включаю firewall"
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow "$SSH_PORT"/tcp comment 'SSH' >/dev/null
ufw --force enable >/dev/null
ufw status verbose

# ─── Бан за перебор ───────────────────────────────────────────────────────
say "Настраиваю fail2ban"
cat > /etc/fail2ban/jail.d/sshd.local <<EOF
[sshd]
enabled = true
port    = $SSH_PORT
backend = systemd
maxretry = 3
findtime = 10m
bantime  = 1h
EOF
systemctl enable --now fail2ban >/dev/null 2>&1 || true
systemctl restart fail2ban || true
sleep 1
fail2ban-client status sshd >/dev/null 2>&1 \
  || warn "fail2ban не поднялся — не критично, проверь потом: fail2ban-client status sshd"

# ─── Обновления безопасности сами ─────────────────────────────────────────
say "Включаю автоматические обновления безопасности"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true

# ─── Сетевые настройки ядра ───────────────────────────────────────────────
say "Настройки ядра"
cat > /etc/sysctl.d/99-hardening.conf <<'EOF'
net.ipv4.tcp_syncookies = 1
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.all.accept_source_route = 0
net.ipv6.conf.all.accept_redirects = 0
kernel.dmesg_restrict = 1
EOF
sysctl --system >/dev/null

if [ -n "$TIMEZONE" ]; then
  timedatectl set-timezone "$TIMEZONE" 2>/dev/null || warn "часовой пояс не изменён"
fi

# ─── Итог ─────────────────────────────────────────────────────────────────
IP=$(hostname -I | awk '{print $1}')
say "Готово"
echo "SSH-порт:        $SSH_PORT"
echo "Вход по паролю:  выключен"
echo "Root по паролю:  выключен (по ключу — можно)"
if [ -n "$NEW_USER" ]; then
  echo "Пользователь:    $NEW_USER (sudo, вход по тому же ключу)"
fi
echo
echo "Кто слушает наружу:"
ss -tlnp | grep -v '127.0.0.1\|\[::1\]' || true
echo
printf '\033[1;33mНЕ ЗАКРЫВАЙ ЭТУ СЕССИЮ.\033[0m Открой второе окно и проверь вход:\n'
echo "  ssh -p $SSH_PORT root@$IP"
if [ -n "$NEW_USER" ]; then
  echo "  ssh -p $SSH_PORT $NEW_USER@$IP"
fi
echo "Зашло — можно закрывать. Не зашло — чини из этой сессии."
ENDOFSCRIPT
bash /root/harden.sh
```

---

## Что сделал скрипт из шага 2

| Что | Зачем |
|---|---|
| Обновил систему | Дыры, закрытые месяц назад, ломают чаще всего |
| Отключил вход по паролю | Главное. Перебор паролей — 99% всех попыток взлома сервера |
| Оставил root только по ключу | Логиниться root'ом по ключу можно, паролем — нет |
| Создал пользователя с sudo | Повседневная работа не от root |
| Включил firewall | Наружу открыт один порт SSH. Всё, что случайно поднимется на сервере, снаружи не видно |
| Поставил fail2ban | Банит на час после трёх неудачных попыток |
| Включил автообновления | Обновления безопасности ставятся сами, без тебя |
| Разрешил проброс TCP | Нужно для `ssh_tunel`, `ssh -D` и `ssh -L`. Многие гайды это ломают |

Отдельно про последнюю строчку: типовые «чеклисты по харденингу» советуют
`AllowTcpForwarding no`. После такого туннель перестаёт работать, а сообщение об
ошибке выглядит как проблема на стороне клиента. Здесь оно явно включено.

---

## Проверить перед тем, как закрыть сессию

**Не закрывая** текущее окно, открой второе и зайди заново:

```bash
ssh root@АДРЕС_СЕРВЕРА
ssh admin@АДРЕС_СЕРВЕРА
```

Зашло без пароля — всё в порядке. Заодно можно посмотреть:

```bash
ufw status verbose          # активен, входящие запрещены, открыт SSH
fail2ban-client status sshd # джейл поднят
ss -tlnp                    # кто слушает; наружу должен торчать только sshd
```

---

## Шаг 3. Отдельный пользователь только для туннеля

Необязательно, но правильно. Сейчас `ssh_tunel` ходит на сервер под `root` — то
есть ключ, который лежит на твоём компьютере, открывает полный доступ к серверу.
Утёк ноутбук — утёк сервер.

Смысл этого шага: завести пользователя, который **умеет ровно одно — пробрасывать
соединения**. Ни выполнить команду, ни забрать файл, ни зайти в оболочку этим
ключом нельзя. Даже если ключ украдут, максимум, что с ним сделают, — походят в
интернет через твой сервер.

Вставить на сервере целиком:

```bash
cat > /root/tunnel-user.sh <<'ENDOFSCRIPT'
#!/usr/bin/env bash
# Пользователь, которому можно только пробрасывать соединения.
set -euo pipefail

# ─── что настроить ────────────────────────────────────────────────────────
TUNNEL_USER="tunnel"
TUNNEL_KEY=""        # публичный ключ для туннеля ("ssh-ed25519 AAAA... ").
                     # Пусто = взять те же ключи, что у root. Отдельный ключ
                     # лучше: тогда компрометация одного не тянет за собой
                     # второй.
BLOCK_INTERNALS=1    # 1 = запретить этому пользователю обращаться к самому
                     # серверу и к приватным сетям хостера
# ──────────────────────────────────────────────────────────────────────────

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mОШИБКА: %s\033[0m\n' "$*" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || die "запускать от root"

say "Создаю пользователя $TUNNEL_USER"
id -u "$TUNNEL_USER" >/dev/null 2>&1 || \
  useradd -m -d "/home/$TUNNEL_USER" -s /usr/sbin/nologin "$TUNNEL_USER"
usermod -s /usr/sbin/nologin "$TUNNEL_USER"
usermod -G "" "$TUNNEL_USER"          # ни одной дополнительной группы
# Звёздочка, а не восклицательный знак: '!' означает "учётка заблокирована", и
# с ним PAM может отказать даже во входе по ключу. '*' — пароль не подойдёт
# никогда, ключ работает.
usermod -p '*' "$TUNNEL_USER"

say "Ставлю ключ с ограничениями"
install -d -m 700 -o "$TUNNEL_USER" -g "$TUNNEL_USER" "/home/$TUNNEL_USER/.ssh"
AK="/home/$TUNNEL_USER/.ssh/authorized_keys"
if [ -z "$TUNNEL_KEY" ] && [ ! -s /root/.ssh/authorized_keys ]; then
  die "ключей нет ни в TUNNEL_KEY, ни у root — вписать публичный ключ в TUNNEL_KEY"
fi
{
  if [ -n "$TUNNEL_KEY" ]; then printf '%s\n' "$TUNNEL_KEY"; else cat /root/.ssh/authorized_keys; fi
} | grep -E '^(ssh-|ecdsa-|sk-)' \
  | sed 's/^/restrict,port-forwarding /' > "$AK"
[ -s "$AK" ] || die "не нашёл ни одного ключа — заполни TUNNEL_KEY"
chown "$TUNNEL_USER:$TUNNEL_USER" "$AK"; chmod 600 "$AK"
echo "ключей записано: $(wc -l < "$AK")"

# restrict выключает всё сразу (сессии, агент, X11, туннели, ~/.ssh/rc), после
# чего port-forwarding возвращает ровно одну нужную возможность.

say "Ограничиваю его в настройках SSH"
if grep -q '^Include /etc/ssh/sshd_config.d/\*.conf' /etc/ssh/sshd_config; then
  MATCH_CONF=/etc/ssh/sshd_config.d/01-tunnel-user.conf
  : > "$MATCH_CONF"
else
  # На старых системах без Include блок дописывается в конец основного файла:
  # Match действует до конца файла, поэтому он обязан быть последним.
  MATCH_CONF=/etc/ssh/sshd_config
  sed -i "/^# --- ssh_tunel: $TUNNEL_USER ---\$/,\$d" "$MATCH_CONF"
  echo "# --- ssh_tunel: $TUNNEL_USER ---" >> "$MATCH_CONF"
fi
cat >> "$MATCH_CONF" <<EOF
Match User $TUNNEL_USER
    PermitTTY no
    AllowAgentForwarding no
    PermitTunnel no
    X11Forwarding no
    AllowTcpForwarding yes
    ForceCommand /usr/sbin/nologin
EOF
sshd -t || die "конфиг SSH не прошёл проверку"
systemctl restart ssh 2>/dev/null || systemctl restart sshd

if [ "$BLOCK_INTERNALS" = 1 ]; then
  say "Закрываю ему доступ к самому серверу"
  cat > /usr/local/sbin/tunnel-fw.sh <<EOF
#!/bin/sh
# Пробрасывать соединения можно куда угодно, кроме самого сервера и приватных
# сетей: иначе через туннель достаётся то, что слушает на 127.0.0.1 (базы,
# админки), и метаданные облака на 169.254.169.254.
#
# Правила только для TCP: DNS по UDP должен продолжать работать, иначе туннель
# перестанет разрешать имена. Для DNS по TCP есть отдельное исключение.
set -e
U="$TUNNEL_USER"
for T in iptables ip6tables; do
  \$T -N SSHTUNNEL 2>/dev/null || \$T -F SSHTUNNEL
  \$T -A SSHTUNNEL -p tcp --dport 53 -j RETURN
done
for N in 127.0.0.0/8 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16; do
  iptables -A SSHTUNNEL -p tcp -d \$N -j REJECT
done
for N in ::1/128 fc00::/7 fe80::/10; do
  ip6tables -A SSHTUNNEL -p tcp -d \$N -j REJECT
done
for T in iptables ip6tables; do
  \$T -C OUTPUT -m owner --uid-owner "\$U" -j SSHTUNNEL 2>/dev/null || \
    \$T -I OUTPUT 1 -m owner --uid-owner "\$U" -j SSHTUNNEL
done
EOF
  chmod +x /usr/local/sbin/tunnel-fw.sh
  cat > /etc/systemd/system/tunnel-fw.service <<'EOF'
[Unit]
Description=Ограничения сети для пользователя туннеля
After=network-online.target ufw.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/tunnel-fw.sh

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now tunnel-fw.service
  iptables -L SSHTUNNEL -n | head -3 || true
fi

say "Готово"
echo "В настройках ssh_tunel поменяй пользователя: root -> $TUNNEL_USER"
echo "Ключ остаётся тот же, если TUNNEL_KEY был пустым."
echo
echo "Проверить со своего компьютера — проброс должен работать:"
echo "  ssh -N -D 1080 $TUNNEL_USER@$(hostname -I | awk '{print $1}')"
echo "а команда — нет:"
echo "  ssh $TUNNEL_USER@$(hostname -I | awk '{print $1}') id     # This account is currently not available."
ENDOFSCRIPT
bash /root/tunnel-user.sh
```

После этого в программе меняешь **Пользователь SSH** с `root` на `tunnel` и
нажимаешь «Сохранить» → переподключиться. Больше ничего.

### Что этот пользователь может и чего не может

Проверено на настоящем `sshd` 9.6 (Ubuntu 24.04):

| Действие | Результат |
|---|---|
| Проброс порта (`ssh -L`), то есть работа `ssh_tunel` | **работает** |
| SOCKS-прокси (`ssh -D`) | **работает** |
| Выполнить команду: `ssh tunnel@сервер id` | `This account is currently not available.` |
| Интерактивная оболочка | `PTY allocation request failed` |
| Скачать файл через `scp`/`sftp` | обрывается, файл не передаётся |
| Вход по паролю | пароля нет вообще (`*` в `/etc/shadow`) |
| Права `sudo`, группы | никаких, ни одной дополнительной группы |
| Достучаться до сервисов самого сервера (`127.0.0.1`) | запрещено правилом firewall |
| Достучаться до метаданных облака (`169.254.169.254`) | запрещено там же |
| Разрешение имён (DNS) для самого туннеля | **работает** — правила только для TCP |

Последние три строки — про `BLOCK_INTERNALS=1`. Без него проброс на
`127.0.0.1` сервера остаётся возможен, и это не теория: у меня в проверке
через такой туннель открылась страница локального сервиса сервера. Если на
сервере крутится база или админка на localhost — оставляй запрет включённым.

### Можно ли теперь запретить root по SSH совсем

Да, если пользователь с `sudo` из шага 2 создан и вход им проверен:

```bash
sed -i 's/^PermitRootLogin .*/PermitRootLogin no/' /etc/ssh/sshd_config.d/00-hardening.conf
sshd -t && systemctl restart ssh
```

Проверь вход `admin@сервер` **вторым окном** до того, как закроешь текущее.

---

## Если всё-таки заблокировался

Не паникуй, сервер не потерян. У любого хостера в панели есть **консоль**
(VNC, «Console», «Recovery»): это как монитор и клавиатура, воткнутые в машину
напрямую, SSH там не нужен. Заходишь root'ом по паролю от хостера и чинишь:

```bash
# вернуть вход по паролю
rm -f /etc/ssh/sshd_config.d/00-hardening.conf
cp /etc/ssh/sshd_config.before-harden /etc/ssh/sshd_config
systemctl restart ssh

# выключить firewall
ufw disable

# снять бан со своего адреса
fail2ban-client set sshd unbanip ТВОЙ_АДРЕС
```

Пароль root от хостера скрипт намеренно не удаляет и не блокирует — иначе
консоль восстановления оказалась бы бесполезной.

---

## Что дальше, по желанию

**Менять ли SSH-порт.** От целенаправленной атаки не спасает: порт находится
сканированием за минуту. От массовых ботов — избавляет почти полностью, в логах
станет тихо. Если решил менять, в скрипте есть `SSH_PORT`; после этого
подключаться надо с `-p НОВЫЙ_ПОРТ`, а в `ssh_tunel` указать его в поле
«SSH-порт». **Сначала проверь вход на новом порту вторым окном** и только потом
закрывай сессию.

**Дополнительные порты.** Если поднимешь на сервере что-то ещё, открывать по
одному:

```bash
ufw allow 443/tcp comment 'что это'
```

Для самого `ssh_tunel` открывать ничего не нужно: он ходит только по SSH.

**Ключ с паролем.** `ssh-keygen` спрашивает пароль на ключ. С паролем украденный
файл ключа бесполезен, без пароля — это готовый вход на сервер. На Windows
пароль будет спрашиваться при каждом подключении, если не пользоваться
ssh-agent, — поэтому решай сам.

**Чего делать не надо:** ставить «панели управления» и веб-морды ради удобства.
Каждая такая штука — это новый порт наружу и новый повод её обновлять.
