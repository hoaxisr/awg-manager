# AWG Manager

> [Russian](README.md)

> A web interface for managing AmneziaWG VPN tunnels on Keenetic routers.
Experimental support for Sing-box has been added (vless tcp, hysteria, trojan, etc.)

> **Disclaimer:** AWG Manager is an independent open-source project, not affiliated with [Amnezia.org](https://amnezia.org) or [SagerNet](https://github.com/SagerNet/sing-box) (Sing-box), and is not an official product of either. The program is in a state of perpetual BETA.

![awgm-showcase](https://raw.githubusercontent.com/hoaxisr/awg-manager/develop/scripts/dev/awgm-showcase.webp)

---

## Features

- Manage AmneziaWG/Sing-box tunnels from a browser
- Add, remove, and monitor peers
- Speed test with real-time display
- Traffic graph with 1h / 3h / 24h periods
- Create AWG servers on the router
- DNS routing through tunnels, with support for NDMS system WireGuard interfaces and the Sing-box rule system
- Real-time connection status view
- Works on Keenetic routers with Entware (OPKG)

---

## Requirements

- A Keenetic router with Entware support and the WireGuard component installed

---

## Installation (stable version)

curl is not required — the installation uses the built-in busybox wget (useful on devices with little internal storage):

```sh
opkg update && opkg upgrade
wget -qO- http://repo.hoaxisr.ru/install.sh | sh
```

Alternative via GitHub (HTTPS): curl is needed, since the firmware's busybox wget may not support HTTPS:
```sh
opkg install curl
curl -sL https://raw.githubusercontent.com/hoaxisr/awg-manager/develop/scripts/install.sh | sh
```

After installation, the web interface is available at the router's address, usually on port 2222.

---

## Uninstallation

```sh
opkg remove awg-manager
rm -rf /opt/etc/awg-manager /opt/etc/awg-manager.pre-restore-*
```

`awg-manager.pre-restore-*` are copies of the data directory left behind by restoring from a backup in versions before 2.19.10.

---

## MCP server (access for AI agents)

awg-manager can expose its functions to AI agents (Claude Code, Cursor, Claude Desktop) over the MCP protocol.

1. Settings → "MCP server" → enable.
2. "Create key" — the key is shown only once; ready-made snippets for clients are provided there as well.
3. Connect: `claude mcp add --transport http awg-manager http://<router>:2222/mcp --header "Authorization: Bearer <key>"`.
   Via KeenDNS: `https://<domain>/mcp`.

Access is by key only (even if web interface authentication is turned off). Agents can view status, logs, tunnels, and routes and make safe changes (starting/stopping tunnels, DNS/static/client routes, sing-box). They can also analyze how a domain or address is routed, check the external IP through a tunnel, view proxies, subscriptions, server groups, and sing-box router rules, enable and disable subscriptions, and manage the clients of the built-in WG server. A key can be issued as read-only — then any tool that changes the router will be refused. Deleting tunnels, backups, updates, and file access are not available via MCP. Deleting a route list (DNS or static) is available but irreversible — the list cannot be restored via MCP, so the client flags these tools as destructive and usually asks for confirmation.

Before issuing a key, keep in mind:

- **The key is the endpoint's only protection from the outside.** Limiting failed attempts works only for clients on the local network: behind the KeenDNS reverse proxy, all remote clients arrive from the proxy's own address (127.0.0.1), so counting by IP would not protect anything there — on the contrary, any remote client could lock out everyone else with a couple of wrong keys. That is why the limit is not applied at all to loopback requests, and protection against brute force from outside comes from the key length (256 bits): store the key like a password and revoke it as soon as it is no longer needed.
- **The key gives access to VPN private keys.** The `export_tunnel_config` tool returns the entire tunnel configuration, including `PrivateKey`. This means anyone who has the MCP key (and the agent you gave it to) can export the private keys of all tunnels on the router. Issue the key only to clients you trust as much as the router itself.

---

## About the project

AWG Manager was created as an independent tool for managing AmneziaWG/Sing-box tunnels directly on the router, without the CLI.

The project is **not affiliated with Amnezia.org**, and is not developed or supported by the Amnezia team. AmneziaWG is used as a transport protocol.
The project is **not affiliated with SagerNet**, and is not developed or supported by the SagerNet team. Sing-box is used as a transport protocol.

---

## Community

Telegram: [@awgmanager](https://t.me/awgmanager) (in Russian)

---

## Support the project

If you have a few spare shekels, enjoyed having your questions go unanswered, are ready to solve problems on your own, and appreciated the "not a day without a new bug" approach, then:

You can share the wealth and give us a chance to breed new problems where everything worked just fine yesterday:

**USDT / ETH:** `0x7eae43b82157f2e4ea233eddf5d9ce19a1064f04`

**USDT / Tron:** `TDisGwxj2AopFzT2VQ9JwY6QDyjChUP5EA`

**Boosty:** https://boosty.to/awgm_hoaxisr/donate (in Russian)

**YooMoney:** https://yoomoney.ru/fundraise/1GF36UHR07L.260312 (in Russian)

**Or any amount:** https://yoomoney.ru/to/4100119477098112/0 (in Russian)

---

## Useful links

Install and manage an AmneziaWG server - https://github.com/bivlked/amneziawg-installer

Another way to manage an AmneziaWG server - https://github.com/pumbaX/awg-multi-script

Docker image of an AmneziaWG server - https://github.com/AYastrebov/docker-amneziawg

Project documentation - https://awgm.hoaxisr.ru/install/
