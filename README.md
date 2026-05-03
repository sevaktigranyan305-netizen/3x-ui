[English](/README.md) | [فارسی](/README.fa_IR.md) | [العربية](/README.ar_EG.md) | [中文](/README.zh_CN.md) | [Español](/README.es_ES.md) | [Русский](/README.ru_RU.md)

# 3x-ui

**3x-ui (v2rayVN fork)** is the admin panel for the [v2rayVN fork
stack](#the-v2rayvn-fork-stack). It is a soft fork of
[MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) that adds a per-client
**virtual IPv4** (`vnetIp`) IPAM on top of VLESS inbounds, and teaches the
share-link generator to emit `vless://…&vnetIp=…` URLs that our Android
client ([v2rayVN](https://github.com/sevaktigranyan305-netizen/v2rayNG)) and
CLI clients ([Xray-core
fork](https://github.com/sevaktigranyan305-netizen/Xray-core)) understand.

The panel itself is visually identical to upstream 3x-ui — the only UI change
is an extra **Virtual network** section on the VLESS inbound editor.

## The v2rayVN fork stack

| Component | Role | Repository |
|---|---|---|
| **Xray-core (fork)** | Server / core with L3 virtualnet (VLESS-as-VPN) | https://github.com/sevaktigranyan305-netizen/Xray-core |
| **3x-ui (fork)** | Admin panel with per-client `vnetIp` IPAM | *this repo* |
| **AndroidLibXrayLite (fork)** | gomobile bindings producing `libv2ray.aar` | https://github.com/sevaktigranyan305-netizen/AndroidLibXrayLite |
| **v2rayVN (Android client)** | Android VPN app built on top of `libv2ray.aar` | https://github.com/sevaktigranyan305-netizen/v2rayNG |

## What's different from upstream 3x-ui

| | Upstream 3x-ui | 3x-ui (v2rayVN fork) |
|---|---|---|
| Multi-protocol inbound management (VLESS, VMess, Trojan, Shadowsocks…) | ✓ | ✓ |
| REALITY / XHTTP / XUDP / sniffing / routing rules in the UI | ✓ | ✓ |
| Subscription URLs, traffic accounting, expiry, Telegram bot | ✓ | ✓ |
| **Per-inbound `virtualNetwork` toggle** (enable VLESS-as-VPN) | — | ✓ |
| **Per-client virtual IPv4 allocation** (`vnetIp`) with IPAM persisted across panel restarts | — | ✓ |
| **Share-link generator emits `&vnetIp=…`** so QR-scanned phones bind on the right IP | — | ✓ |
| **JSON subscription** nests `virtualNetwork` inside `settings` so clients see it as a normal VLESS outbound option | — | ✓ |
| Bundled Xray binary | upstream XTLS/Xray-core | [sevaktigranyan305-netizen/Xray-core](https://github.com/sevaktigranyan305-netizen/Xray-core) — needed for the server-side L3 gVisor stack |
| Trimmed release matrix | Linux + Windows panel builds | Linux-only (server-side is the only place this panel ever runs) |

Everything else — language files, plugin hooks, panel theme, DB schema
migrations — is kept as close to upstream as possible so future upstream
syncs are cheap.

## Quick start

> The installer script is drop-in compatible with upstream's and works on a
> fresh Ubuntu / Debian / CentOS / Fedora / RHEL box. It installs the panel
> as a systemd service, opens firewall ports, generates a default admin user
> and prints the login URL at the end.

```bash
bash <(curl -Ls https://raw.githubusercontent.com/sevaktigranyan305-netizen/3x-ui/main/install.sh)
```

Then follow the on-screen prompts:

1. Set the panel admin username / password.
2. Set the panel port (default `2053`).
3. Set the panel URL path (default random 10 chars).

Log in at `http://<server-ip>:<port>/<path>` with the credentials you just
set.

### Update to the latest release

```bash
x-ui update
```

### Uninstall

```bash
x-ui uninstall
```

### Docker

A `Dockerfile` is included in the repo. Build it yourself:

```bash
git clone https://github.com/sevaktigranyan305-netizen/3x-ui.git
cd 3x-ui
docker build -t 3x-ui-vnet .

docker run -d --name 3x-ui \
    --restart=unless-stopped \
    --network=host \
    -v /etc/x-ui:/etc/x-ui \
    -v /root/cert:/root/cert \
    3x-ui-vnet
```

Mount `/etc/x-ui` to keep the SQLite DB (users, inbounds, IPAM assignments,
traffic counters) persistent across container restarts.

## Configuring a VLESS L3 inbound

1. In the panel, **Inbounds → Add Inbound**.
2. **Protocol = VLESS**, port = `443`, network = `tcp`, security = `reality`
   (or `tls` / `xhttp` — any regular VLESS transport works).
3. Add one or more clients — just UUID + e-mail / remark, same as stock.
4. Scroll to the **Virtual network** section and flip the **Enable** toggle.
   Set the subnet (default `10.0.0.0/24`).
5. Save. The panel stores the inbound and starts allocating `vnetIp`s for
   each client from the subnet.

### How the IPAM works

- The first usable address of the subnet is the **gateway** (e.g. `10.0.0.1`
  for `10.0.0.0/24`). It is reserved — clients never get this address.
- The first client gets `10.0.0.2`, the next `10.0.0.3`, and so on — the
  lowest free address above the gateway is always picked.
- Allocations are persisted in the panel DB (`virtualnet_assignments` table)
  and in an Xray-core-readable JSON file
  (`/etc/x-ui/virtualnet-ipam-<subnet>.json`) that the core reads at startup
  so a returning client always gets the same IP.
- When you delete a client from the panel (or remove their UUID from the
  inbound JSON by hand), their slot is released and reclaimed by the next
  new client.
- A `/24` subnet gives you up to **253 simultaneous clients** (256 − network
  − broadcast − gateway). Use a `/23` or `/22` if you need more.

### Getting the QR to the phone

- Inbound list → client row → **More info → QR code**.
- The QR encodes a `vless://…` URL with four extra query parameters:
  - `vnet=1` — enable virtualNetwork on the outbound.
  - `vnetSubnet=10.0.0.0%2F24` — subnet, URL-encoded.
  - `vnetIp=10.0.0.N` — this client's pre-allocated IP.
  - `vnetDefaultRoute=1` — all traffic through VPN (default; change to `0`
    in the inbound JSON for split-tunnel).
- Stock v2rayNG clients silently ignore the `vnet*` params and connect as a
  plain VLESS proxy.
- Our [v2rayVN](https://github.com/sevaktigranyan305-netizen/v2rayNG) picks
  up the params and automatically switches into VPN mode on connect.

### JSON subscription URL

If you rely on 3x-ui's JSON subscription format (`/sub_json/<id>`) rather
than sharing one-off QR codes, the v2rayVN fork nests `virtualNetwork`
inside the outbound's `settings` block (upstream 3x-ui put it at the
outbound root, which stock Xray-core rejects at parse time). v2rayVN and
the fork's CLI clients both understand the nested layout.

## CLI helper

The `x-ui` command (installed into `/usr/bin/x-ui`) is upstream with one
small addition:

```bash
x-ui update          # upgrades panel + bundled Xray core in one step
x-ui status          # show systemd status + panel URL
x-ui settings        # reset admin password / port / URL path
x-ui log             # tail panel logs
x-ui banlog          # tail Xray access / error logs
```

## Upgrading from upstream 3x-ui

If you already run the upstream panel and want to switch to the fork:

1. **Stop** the upstream panel: `systemctl stop x-ui`.
2. Back up `/etc/x-ui/x-ui.db` (the panel DB with your inbounds and users).
3. Run the fork's installer (see Quick start above). It drops a new
   `/etc/x-ui/x-ui.db` if none exists — but if you restored your backup
   first it picks up your existing data.
4. Open each VLESS inbound and decide whether to enable `virtualNetwork`.
   Existing inbounds with `virtualNetwork.enabled = false` (the default)
   behave exactly like they did on upstream.

No DB schema change; the `virtualnet_assignments` table is created lazily on
first use.

## Building from source

```bash
# Go 1.26+, Node 22+
git clone https://github.com/sevaktigranyan305-netizen/3x-ui.git
cd 3x-ui

# Download and vendor the fork's Xray-core binary + geoip / geosite dat
# files into bin/. This is the step that pins the server to the fork.
bash DockerInit.sh

# Build the panel binary.
go build -o x-ui main.go

# Run locally for development (defaults to port 2053).
./x-ui
```

Release tarballs bundle the panel binary + matching fork Xray-core +
geoip.dat / geosite.dat per architecture. They are produced by
[`.github/workflows/release.yml`](.github/workflows/release.yml) on every
tag push.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Inbound with `virtualNetwork.enabled = true` refuses to start; panel shows "xray failed to start" | Stock (upstream) Xray binary installed instead of the fork's. | `x-ui update`, or re-run `DockerInit.sh` if you built from source. |
| Client's QR code has no `vnetIp=…` param | The inbound does not have `virtualNetwork.enabled = true`, or the client was added before you enabled virtualnet. | Edit the inbound → **Virtual network → Enable** → Save. The IPAM re-runs against the current client list. |
| Two clients ended up on the same `vnetIp` | Impossible via the panel, but if you edited `/etc/x-ui/x-ui.db` by hand you can break invariants. | Stop panel, `rm /etc/x-ui/virtualnet-ipam-*.json`, start panel — it rebuilds from the DB. |
| `vnetIp` appears in the database but *not* in the QR share-link | Old panel version before the WebSocket-broadcast fix (pre-v1.0.6). | Update the panel. |
| Android client connects, TUN comes up, but `ping 10.0.0.1` times out | Server firewall blocking ICMP on the loopback rewrite. | `iptables -I INPUT -i lo -p icmp -j ACCEPT`, or use the `--accept-redirects` sysctl. |
| Peer-to-peer between two phones doesn't work | Both clients must be **simultaneously** connected to the panel for P2P. | Reconnect one, it will appear in the other's reachable set. |

## Credits

- Upstream [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) — the panel,
  including the entire web UI, Telegram bot, DB schema, install scripts,
  language files. GPL-3.0.
- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — the upstream of our
  [core fork](https://github.com/sevaktigranyan305-netizen/Xray-core), which
  this panel bundles.
- [alireza0](https://github.com/alireza0/) — upstream 3x-ui contributor.

## Acknowledgements

- [Iran v2ray rules](https://github.com/chocolate4u/Iran-v2ray-rules) — Iran
  routing rules bundled in the panel's rule-set picker.
- [Russia v2ray rules](https://github.com/runetfreedom/russia-v2ray-rules-dat) —
  Russia routing rules bundled in the panel's rule-set picker.

## License

[GPL-3.0](./LICENSE) — same as upstream 3x-ui.
