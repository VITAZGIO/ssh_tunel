# Web panel for a VPS (ssh_tunnel_panel)

A separate program. Installed **on the server itself** and adds clients with
a button — no console, no manual `useradd`, no editing `authorized_keys`.

## How it's different from the local panel

`ssh_tunnel_linux -web` is the program's window on the client's own
computer: it manages only its own connection, reachable by a token in the
address and only from that machine.

`ssh_tunnel_panel` is a separate program on the VPS itself. It doesn't
connect anywhere on its own — it manages **who is allowed to connect to this
server**: it adds and removes clients, each with its own unix user and its
own key, and shows traffic and who's currently online. It faces the
internet, so the login is a username and password, not a token in a link.

If you only need one computer you trust, and you're happy setting it up
yourself, the panel isn't necessary — [manual setup](SERVER_SETUP.md) is
enough. The panel earns its keep once there's more than one client (your own
several devices, or you're handing out access to other people) and adding
each one by hand over SSH gets old.

## The panel runs as root — and that's worth understanding

To add and remove system users, the panel needs root. That's not an
oversight — part of its job is simply impossible without it. The systemd
unit narrows everything else it can touch (`PrivateTmp`,
`ProtectSystem=true` — `/usr` and `/boot` read-only, `NoNewPrivileges`,
kernel-settings protection), but the fact that "a process facing the
internet runs as root" doesn't go away. Three practical consequences follow:

1. **Password** — change the one-time password to your own, long and
   unique, right after the first login. The panel throttles brute-forcing
   itself (a growing delay, a lockout after several failures), but that's
   not a substitute for a real password.
2. **Domain and certificate** — don't leave the panel as bare HTTP on a port
   open to the internet. Either your own domain with TLS through a reverse
   proxy, or the built-in auto-certificate (below) — never bare HTTP without
   a domain and certificate.
3. **Address restriction**, where possible — a firewall or reverse proxy
   that only lets the panel's port through from your own address or a VPN.
   The panel needs no special flags for this — it's handled by whatever
   sits in front of it (ufw, nginx, Tailscale/NetBird).

## One-command install

One block — paste it whole on a fresh server as root. Nothing to download
ahead of time: it fetches the right binary for the server's architecture
from GitHub itself, installs the service, and prints the panel's address
and password.

**Option 1 — no domain of your own.** The panel listens only on
`127.0.0.1:47823`; there's no need to open it to the outside — reach it
through an SSH tunnel, or add a domain and nginx/Caddy later (see "Domain
and TLS" below):

```bash
curl -fsSL https://raw.githubusercontent.com/VITAZGIO/ssh_tunel/main/packaging/panel/install.sh \
  | sudo bash -s -- --lan --ssh-host=THIS_SERVERS_ADDRESS
```

**Option 2 — with your own domain.** The domain must already have an A
record pointing at this server — then the panel gets its own Let's Encrypt
certificate and is reachable over HTTPS right away:

```bash
curl -fsSL https://raw.githubusercontent.com/VITAZGIO/ssh_tunel/main/packaging/panel/install.sh \
  | sudo bash -s -- --domain=panel.example.com
```

Either way, the command prints the panel's address and the one-time
password at the end — you can also look them up any time with:

```bash
journalctl -u ssh_tunnel_panel -n 50
```

Already have your own reverse proxy (nginx/Caddy) and want the panel behind
it without an open port — a third option, without `--lan` and without
`--domain` (this is the default mode, see "Domain and TLS" below):

```bash
curl -fsSL https://raw.githubusercontent.com/VITAZGIO/ssh_tunel/main/packaging/panel/install.sh \
  | sudo bash -s -- --ssh-host=THIS_SERVERS_ADDRESS
```

Already downloaded the binary yourself (say, built it from source) — pass
its path as the first argument after `--`:

```bash
sudo bash install.sh ./ssh_tunnel_panel --lan --ssh-host=SERVER_ADDRESS
```

## First login

On first launch, if there are no users yet, the panel creates an `admin`
account with a random one-time password itself and prints it to the log. It
will require changing that password right after the first login — it isn't
stored anywhere else, and if it's lost, the only way out is to delete
`/etc/ssh_tunnel_panel/users.json` and restart the service
(`systemctl restart ssh_tunnel_panel`): the panel will create a fresh
one-time account, same as on first launch.

## Domain and TLS

Two paths, both supported without reinstalling — just different flags:

**Behind nginx/Caddy (default).** The panel listens only on
`127.0.0.1:47823`; the certificate and domain are the reverse proxy's job.
A typical nginx config:

```nginx
server {
    listen 443 ssl;
    server_name panel.example.com;
    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:47823;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

The `X-Forwarded-Proto` header matters: it's how the panel knows the
connection arrived over HTTPS, and sets the session cookie with the
`Secure` flag.

**Built-in auto-certificate**, if there's no separate reverse proxy on the
machine:

```bash
sudo bash install.sh ./ssh_tunnel_panel --domain=panel.example.com
```

The panel obtains and renews its own Let's Encrypt certificate
(`golang.org/x/crypto/acme/autocert`), listening on `:443` for the panel and
`:80` only for the domain check Let's Encrypt requires. The domain must
already point at the server via an A record before you run this — otherwise
getting a certificate will fail.

## Changing the port

The port is set with the `-listen` flag (default `127.0.0.1:47823`). Edit
`ExecStart` in `/etc/systemd/system/ssh_tunnel_panel.service`:

```bash
sudo sed -i 's|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_panel -listen 127.0.0.1:8443|' \
  /etc/systemd/system/ssh_tunnel_panel.service
sudo systemctl daemon-reload
sudo systemctl restart ssh_tunnel_panel
```

With `--domain` the port doesn't matter — the panel always listens on `:80`
and `:443`.

## The address clients connect to

Separate from the panel's own address: clients connect over SSH to the
server, not to the web panel. With `--domain` the panel takes this address
from the same domain automatically. Without `--domain` (`--lan` or the
default reverse-proxy mode), set it explicitly at install time:

```bash
sudo bash install.sh ./ssh_tunnel_panel --lan --ssh-host=203.0.113.10
```

Without this address the panel will still create a client, but won't be
able to build a ready config for it — it will just report that no address
is set.

## Adding your first client

1. Open the panel, log in, change the one-time password.
2. On the main screen — "+ Add client": a device name ("Laptop", "Phone")
   and type (Windows, Linux, Android).
3. Once created, the panel shows a ready config right away: a QR code for a
   phone, a "Copy" button for pasting into the panel on a computer, and
   "Download file" — plus the address, port, user, and key as plain text if
   you'd rather type them in by hand. This is the only moment the panel
   shows the private key on its own — after that it's only revealed again
   through that specific client's "Show settings" button.
4. On the new device — import that config (scanning the QR code, pasting
   from the clipboard, or opening the downloaded file with the "Import"
   button, depending on the platform).

From there, the client list shows traffic, the number of active sessions,
and the time of the last connection; buttons let you freeze (temporarily
deny login without removing the key from the panel's storage), disconnect
(kill current sessions without touching the key), and delete (erase the
user and key for good).

## Removing the panel

```bash
sudo systemctl disable --now ssh_tunnel_panel
sudo rm /etc/systemd/system/ssh_tunnel_panel.service
sudo systemctl daemon-reload
sudo rm /usr/local/bin/ssh_tunnel_panel
```

This stops the panel and removes it, but leaves already-created clients
alone — their unix users and keys in `authorized_keys` keep working, and the
sshd restrictions (`Match Group sshtunnel` in `/etc/ssh/sshd_config`,
between the `# BEGIN ssh_tunnel_panel` and `# END ssh_tunnel_panel` lines)
stay in effect. To remove everything:

```bash
# Delete all of the panel's clients (replace tun_* with real names from
# `getent passwd | grep tun_` if you only want to remove some):
for u in $(getent passwd | cut -d: -f1 | grep '^tun_'); do
  sudo userdel -r "$u"
done

# Remove the restriction block from sshd_config (it's between these two
# lines) and the group:
sudo sed -i '/# BEGIN ssh_tunnel_panel/,/# END ssh_tunnel_panel/d' /etc/ssh/sshd_config
sudo systemctl reload ssh
sudo groupdel sshtunnel

# The panel's own data (panel users, client list, certificate cache):
sudo rm -rf /etc/ssh_tunnel_panel
```

## Flags

| Flag | Default | What it does |
|---|---|---|
| `-listen` | `127.0.0.1:47823` | address the panel listens on without `-domain` |
| `-domain` | — | turns on HTTPS with a built-in auto-certificate for this domain |
| `-ssh-host` | taken from `-domain` | address clients use to connect to the server over SSH |
| `-ssh-port` | `22` | the server's SSH port — goes into the client config |
| `-public-url` | built from `-domain` | the panel's own address — goes into the client config |
| `-data-dir` | `/etc/ssh_tunnel_panel` | folder with accounts, clients, and the certificate cache |
