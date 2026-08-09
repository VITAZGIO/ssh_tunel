<div align="center">

# ssh_tunel

**Your own VPN on your own server — over plain SSH, which nobody blocks.**

A single file. No installer, no administrator rights.
Your ISP sees one encrypted SSH connection to a server abroad — not the
contents, not the list of sites.

[![License: MIT](https://img.shields.io/badge/License-MIT-4c8dff)](LICENSE)
[![Version](https://img.shields.io/badge/version-1.0.0-4c8dff)](https://github.com/VITAZGIO/ssh_tunel/releases)
[![Windows](https://img.shields.io/badge/Windows-10%20%7C%2011-2de2ff)](https://github.com/VITAZGIO/ssh_tunel/releases/latest)
[![Linux](https://img.shields.io/badge/Linux-amd64%20%7C%20arm64-2de2ff)](https://github.com/VITAZGIO/ssh_tunel/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.22+-2de2ff)](src)

[🇷🇺 Русский](README.md) · 🇬🇧 English

### Download

| System | File | |
|---|---|---|
| **Windows** | [**ssh_tunel.exe**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunel.exe) | window with a button, tray icon |
| **Linux** | [**ssh_tunel-linux**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunel-linux) | console + web interface, systemd service |

The console build for Windows, the ARM build and the checksums are on the
[release page](https://github.com/VITAZGIO/ssh_tunel/releases/latest).

<img src="docs/screenshot.png" width="300" alt="main screen">
<img src="docs/screenshot-filter.png" width="300" alt="per-app filter">

</div>

---

## What this is

WireGuard and OpenVPN are recognised by their very first packet and get blocked
wholesale. SSH has no such signature: it is a working tool that administrators
and developers all over the world use every single day, and switching it off
entirely means breaking half of the working infrastructure.

`ssh_tunel` opens an SSH connection to **your** VPS and runs a local proxy on
top of it. Browsers and other applications then reach the internet through that
server, while your ISP only sees an encrypted stream to a single address.

No third-party servers, no subscriptions, no accounts: all you need is a VPS you
can log into over SSH.

### Key features

- 🔌 **Three protocols at once** — SOCKS5, SOCKS4/4a and HTTP CONNECT. Different
  programs speak different things, and all three are needed.
- 🎯 **Per-application split tunnelling** — everything goes through the tunnel,
  or only the applications you pick, or everything except them. Rules apply on
  the fly, without reconnecting.
- 🧩 **Works where the system proxy cannot** — Node.js, Python, Go and everything
  built on them (Claude Code, npm, pip, curl) read only environment variables,
  and the program sets them.
- ♻️ **Survives dropped links** — a pool of SSH connections with liveness checks
  and automatic reconnect. A laptop waking from sleep does not break the tunnel.
- 🛟 **Restores your settings on any exit**, including a crash: the internet does
  not disappear after you quit.
- 🔑 **Verifies the server key** — a man-in-the-middle swapping the server out
  will not pass unnoticed.
- 📈 **Measures the real throughput** — a multi-stream speed test through the
  tunnel.
- 👀 **Shows who is going online** — a live list of programs and addresses, with
  a mark when a DNS lookup went around the tunnel.

> A detailed technical write-up is in [ARCHITECTURE.md](docs/ARCHITECTURE.md)
> (in Russian).

---

## How it works (short version)

```
  application
      │  SOCKS4/4a/5 (1080)   HTTP CONNECT (1081)
      ▼
  ssh_tunel  ──── encrypted SSH (22) ────►  your VPS  ──►  internet
```

1. The application thinks it is talking to an ordinary proxy on its own machine.
2. `ssh_tunel` parses the request, learns the destination and opens a
   `direct-tcpip` channel to it over SSH.
3. Host names are resolved by the VPS, not by your computer — so the ISP does
   not see your DNS lookups either.

---

## Requirements

**Your machine:**
- Windows 10 / 11, or Linux (amd64 / arm64)
- nothing else: the binary is self-contained, no runtime, no installer

**Your server:**
- any VPS you can reach over SSH — that is all. Nothing has to be installed on
  it: the proxy runs on your machine, the server only passes connections
  through.

---

## Windows

1. Download [**ssh_tunel.exe**](https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunel.exe)
   and put it in a permanent folder.
2. Run it. Windows will warn about an unknown publisher — «More info» → «Run
   anyway» (the binary is not signed with a certificate: that costs money and
   requires a legal entity).
3. Open the settings (the gear), enter the VPS address and the user name.
   No key yet? Click the question mark next to «Private key» — there is a
   ready-made command that creates a key and uploads it to the server.
4. Save, go back, press the round button.

The close button hides the program in the tray, the tunnel keeps running. To
quit for real: right-click the tray icon → «Exit».

There is a console build as well — `ssh_tunel-cli.exe`, everything through flags.

---

## Linux

For servers and workstations, amd64 and arm64.

```bash
# download and make executable
curl -LO https://github.com/VITAZGIO/ssh_tunel/releases/latest/download/ssh_tunel-linux
chmod +x ssh_tunel-linux

# set the server once
./ssh_tunel-linux -host YOUR_VPS -user root -save

# run: tunnel only...
./ssh_tunel-linux

# ...or with the web interface
./ssh_tunel-linux -web
```

The web interface is the very same one the Windows build shows in its window,
served on `127.0.0.1:47821`. The port is deliberately unusual so it does not
collide with whatever is already running on a server. The address, together with
an access token, is printed at startup; without the token the interface does not
answer. It does not expose itself to the outside — that needs the separate
`-web-listen` flag, and the program warns you about the risk.

Wire the proxy into the current shell (curl, git, apt, docker, pip, npm):

```bash
source ~/.config/ssh_tunel/proxy.env
```

As a systemd service — see [packaging/linux](packaging/linux):

```bash
./packaging/linux/install.sh ./ssh_tunel-linux
systemctl --user enable --now ssh_tunel
```

---

## Configuration

Settings live in `%APPDATA%\ssh_tunel\config.json` on Windows and in
`~/.config/ssh_tunel/config.json` on Linux. The GUI writes the same file the
flags do.

| Key | Meaning |
|---|---|
| `host` | Address of your VPS. |
| `sshPort` | SSH port, `22` by default. |
| `user` | User to log in as. |
| `keyPath` | Private key. Detected automatically among `id_ed25519`, `id_ecdsa`, `id_rsa`. |
| `socksPort` | Local SOCKS4/4a/5 port, `1080` by default. |
| `httpPort` | Local HTTP proxy port, `1081` by default. |
| `poolSize` | Number of parallel SSH connections. More streams — higher throughput on a long link. |
| `filterMode` | `all`, `only` or `except` — the split-tunnelling mode. |
| `filterApps` | The list of applications that mode applies to. |

Linux flags: `-host`, `-sshport`, `-user`, `-key`, `-port`, `-httpport`,
`-pool`, `-filter`, `-apps`, `-sysproxy`, `-setenv`, `-save`, `-env`, `-web`,
`-web-listen`, `-v`.

---

## Security

- **The server key is checked.** On the first connection its fingerprint is
  remembered (TOFU) in `known_hosts`; if it changes later, the program refuses
  to connect instead of quietly talking to whoever answered.
- **Host names are resolved on the VPS**, so DNS queries do not leak to the ISP.
  Connections that arrive already resolved (the application looked the name up
  itself) are marked in the log.
- **The system proxy is restored on every exit**, including a crash — there is a
  saved snapshot and a recovery pass at the next start. Losing the internet
  because a program died is not acceptable.
- **The local web interface is protected by a random token** and an `Origin`
  check, on a random port: an arbitrary page open in your browser must not be
  able to reach `127.0.0.1` and switch the tunnel off or read the settings.
- **No administrator rights.** Everything is written under the current user.

What the tunnel deliberately does not do: hide the *fact* that you use SSH. Your
ISP sees an SSH connection to one address abroad — see
[SECURITY.md](docs/SECURITY.md) (in Russian) for the full picture.

---

## What the tunnel does not cover

- Programs with their own network stack that ignore both the system proxy and
  the environment variables — some games, some messengers.
- UDP entirely: SSH forwards TCP only. Browsers fall back to TCP by themselves
  when QUIC is unavailable.
- DNS lookups made by applications that resolve names on their own, before they
  ever talk to the proxy. Such connections are marked in the log.

---

## Building from source

Go 1.22 or newer is the only requirement — no C compiler, no external libraries.

```bash
cd src
go test ./... -race     # check that everything works
./build.sh              # build everything into ../releases
```

```
src/          source code (a Go module)
packaging/    systemd service and installer for Linux
docs/         architecture, security, troubleshooting
```

Binaries are not kept in the repository — they are published in
[releases](https://github.com/VITAZGIO/ssh_tunel/releases) so the history does
not swell with megabytes.

---

## Contributing

Pull requests and issues are welcome. Please do not commit personal data (server
addresses, keys, `config.json`, `known_hosts`) — they are already in
`.gitignore`.

## License

[MIT](LICENSE) © 2026 Vitaliy ([VITAZGIO](https://github.com/VITAZGIO)).
Developed together with Claude (Anthropic).

Take it, change it, use it however you like, including in your own projects. The
only condition is to keep the license text and the attribution.

The software is provided as is, without warranty. It is meant for reaching your
own server; how you use it is your responsibility.
