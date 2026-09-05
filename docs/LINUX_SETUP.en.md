# Installing on Linux

For servers and workstations, amd64 and arm64. The binary is built statically:
it needs no libraries and no particular glibc version — it runs the same on
Debian, Fedora, Arch, and even on Alpine with musl.

```bash
# download and make executable
curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux

# set the server once
./ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save

# run: tunnel only...
./ssh_tunnel_linux

# ...or with the web interface
./ssh_tunnel_linux -web

# ...or with the panel open to your home network
./ssh_tunnel_linux -web -web-lan
```

The ARM build and the checksums are on the
[release page](https://github.com/VITAZGIO/ssh_tunel/releases/latest).

---

## Commands for your distribution

The program itself is identical everywhere — what differs is how you install
`curl`, how you open a port in the firewall, and how a service is set up. Open
your system:

<details>
<summary><b>Ubuntu · Debian · Linux Mint · Raspberry Pi OS</b></summary>

```bash
sudo apt update && sudo apt install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

The panel opens at `http://MACHINE_ADDRESS:47821`. If ufw is on, let only your
home network in:

```bash
sudo ufw allow from 192.168.0.0/16 to any port 47821 proto tcp
```

Autostart — the "Start at system boot" checkbox in the panel itself.

On a Raspberry Pi and other ARM machines the file is named
`ssh_tunnel_linux_arm64`.
</details>

<details>
<summary><b>Proxmox VE</b></summary>

Proxmox is Debian, but you normally work there as `root` and with no user
session. So the service goes in as a **system** one rather than a user one, and
the autostart checkbox in the panel will not help here — it targets
`systemctl --user`.

```bash
apt update && apt install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user root -save
```

The system service (starts at host boot, before any login):

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

Settings then live in `/root/.config/ssh_tunnel/`. If the Proxmox firewall is on
(Datacenter → Firewall), port `47821` has to be allowed there, in the Proxmox web
UI — `ufw` plays no part in it.
</details>

<details>
<summary><b>Fedora · RHEL · CentOS Stream · Rocky · AlmaLinux</b></summary>

```bash
sudo dnf install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

The firewall here is `firewalld`, not `ufw`:

```bash
sudo firewall-cmd --permanent --add-port=47821/tcp
sudo firewall-cmd --reload
```

SELinux does not get in the way: the program touches neither system directories
nor other people's ports — everything of its own stays in the user's home.
</details>

<details>
<summary><b>Arch · Manjaro · EndeavourOS</b></summary>

```bash
sudo pacman -S --needed curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

There is no firewall by default at all — if you installed one, open port `47821`
there. Everything else is as on Ubuntu, same systemd.
</details>

<details>
<summary><b>openSUSE</b></summary>

```bash
sudo zypper install -y curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
sudo install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save
ssh_tunnel_linux -web -web-lan
```

The port is opened through `firewalld`, as on Fedora:

```bash
sudo firewall-cmd --permanent --add-port=47821/tcp && sudo firewall-cmd --reload
```
</details>

<details>
<summary><b>Alpine Linux</b></summary>

The one system on this list with **no systemd** — it runs OpenRC. The program
works fine (the binary is static, musl is no obstacle), but autostart is set up
differently, and the checkbox in the panel will not do it.

```sh
apk add curl

curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunnel_linux
chmod +x ssh_tunnel_linux
install -m 755 ssh_tunnel_linux /usr/local/bin/ssh_tunnel_linux

ssh_tunnel_linux -host YOUR_SERVER -user tunnel -save
```

The OpenRC service:

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

## Starting at boot

At the bottom of the settings there are two checkboxes side by side:

- **"Start at system boot"** — the program writes the systemd unit itself,
  enables it, and allows it to run without the user logging in. That last part
  needs administrator rights: if the system asks for a password, a field for it
  appears right there — the password is used once and stored nowhere.
- **"Connect immediately on launch"** — the tunnel comes up on its own as soon
  as the program starts, no need to press "Connect" in the panel. On by
  default — that's the ordinary behavior; turn it off if you'd rather start the
  tunnel by hand.

Together they give you "turn the machine on, the tunnel is already up": the
first checkbox brings the program up at boot, the second connects it right
away.

If Docker is found on the machine, a third checkbox appears a little further
down — turn on autostart for Docker too, so that after a reboot the containers
and the tunnel come up together.

This works anywhere systemd is present. The exceptions are Proxmox under `root`
and Alpine: there the service is set up by hand, with the commands above.

---

## Web interface

The very same one the Windows build shows in its window, served on the fixed
port `47821`. The port is deliberately unusual so it does not collide with
whatever is already running on a server.

By default the panel listens on `127.0.0.1` only and the address carries an
access token, printed at startup. Nothing reaches it from the network, and no
page that happens to be open in your browser can either.

The **`-web-lan`** flag turns it into a home service like Home Assistant: fixed
address, no token, opened straight at the machine's address —

```
http://192.168.1.203:47821
```

Only your own side gets in: local network addresses (`192.168.x.x`, `10.x.x.x`,
`172.16–31.x.x`) and mesh VPNs (`100.64.x.x`). A public address still needs the
token, and the `Origin` check keeps a foreign site in the next browser tab from
pressing anything on your behalf. Anyone on that network can control the tunnel —
usually the point at home, not something to leave on in someone else's network.

---

## Proxy in your shell

Wire the proxy into the current shell (curl, git, apt, docker, pip, npm):

```bash
source ~/.config/ssh_tunnel/proxy.env
```

As a systemd service — see [packaging/linux](../packaging/linux):

```bash
./packaging/linux/install.sh ./ssh_tunnel_linux --lan   # without --lan: local machine only
systemctl --user enable --now ssh_tunnel
```

---

Setting up a new server for the tunnel? See
[first-time server setup](SERVER_SETUP.md) (Russian only for now).
