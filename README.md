# AWG Manager

Simple web interface for managing AmneziaWG VPN tunnels on Keenetic routers.

## Features

- Create and manage AmneziaWG tunnels
- Import WireGuard/AmneziaWG .conf configurations
- Start, stop, restart tunnels
- Test connectivity, IP, and speed through tunnels
- Auto-start tunnels on boot
- **Ping Check monitoring** - automatic tunnel health checks with failover
- Tokyo Night theme (dark/light)

## Tech Stack

- **Backend:** Go (single binary)
- **Frontend:** SvelteKit 2.0 + TypeScript
- **Package:** IPK for Entware (Keenetic)

## Build

### Requirements

- Go 1.21+
- Node.js 18+
- npm

### Build IPK package

```bash
# For mipsel (MT7621 - Giga, Ultra, Viva, etc.)
./scripts/build-ipk.sh mipsel-3.4

# For aarch64 (MT7986 - Peak, Hopper, etc.)
./scripts/build-ipk.sh aarch64-3.10
```

Output: `dist/awg-manager_VERSION_ARCH-kn.ipk`

### Build components separately

```bash
# Backend only
./scripts/build-backend.sh mipsle

# Frontend only
./scripts/build-frontend.sh
```

## Installation

```bash
# Copy IPK to router
scp dist/awg-manager_*.ipk root@router:/tmp/

# Install on router
opkg install /tmp/awg-manager_*.ipk
```

Access web interface at: `http://router-ip/awg-manager/`

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/tunnels/list` | GET | List all tunnels |
| `/api/tunnels/get?id=` | GET | Get tunnel details |
| `/api/tunnels/create` | POST | Create tunnel |
| `/api/tunnels/update?id=` | POST | Update tunnel |
| `/api/tunnels/delete?id=` | POST | Delete tunnel |
| `/api/control/start?id=` | POST | Start tunnel |
| `/api/control/stop?id=` | POST | Stop tunnel |
| `/api/control/restart?id=` | POST | Restart tunnel |
| `/api/control/toggle-enabled?id=` | POST | Toggle auto-start |
| `/api/status/get?id=` | GET | Get tunnel status |
| `/api/status/all` | GET | Get all tunnels status |
| `/api/test/ip?id=` | GET | Test IP through tunnel |
| `/api/test/connectivity?id=` | GET | Test connectivity |
| `/api/test/speed/start?id=` | POST | Start speed test |
| `/api/test/speed/status?testId=` | GET | Get speed test status |
| `/api/test/speed/stop?testId=` | POST | Stop speed test |
| `/api/import/conf` | POST | Import .conf file |
| `/api/system/info` | GET | System information |
| `/api/settings/get` | GET | Get application settings |
| `/api/settings/update` | POST | Update application settings |
| `/api/pingcheck/status` | GET | Get ping check status for all tunnels |
| `/api/pingcheck/logs` | GET | Get ping check logs (optional `?tunnelId=`) |
| `/api/pingcheck/check-now` | POST | Trigger immediate check on all tunnels |

## Ping Check (Tunnel Monitoring)

Automatic health monitoring for VPN tunnels. When enabled, periodically checks connectivity through each tunnel and takes action when tunnels become unreachable.

### How it works

1. **Check Methods:**
   - **HTTP 204** (default) - requests `http://connectivitycheck.gstatic.com/generate_204` through tunnel
   - **ICMP Ping** - pings configured IP (default: 8.8.8.8) through tunnel interface

2. **Failure Detection:**
   - Configurable fail threshold (default: 3 consecutive failures)
   - Tunnel marked as "dead" after threshold reached
   - On **Keenetic OS 5.x**: OpkgTun interface automatically disabled for route failover

3. **Recovery:**
   - Dead tunnels checked at longer intervals (default: 120s vs 45s)
   - Auto-recovery when connectivity restored
   - Dead tunnels blocked from auto-start until recovered

### Configuration

Enable in **Settings → Мониторинг туннелей**. Default settings:

| Parameter | Default | Description |
|-----------|---------|-------------|
| Method | HTTP 204 | Check method (http/icmp) |
| Target | 8.8.8.8 | ICMP ping target |
| Interval | 45s | Check interval for healthy tunnels |
| Dead Interval | 120s | Check interval for dead tunnels |
| Fail Threshold | 3 | Failures before marking dead |

Per-tunnel overrides available in tunnel settings.

## Data Storage

All data stored in `/opt/etc/awg-manager/`:
- `settings.json` - Application settings (server, auth, ping check)
- `tunnels/*.json` - Tunnel metadata
- `*.conf` - WireGuard-format configs

Logs: `/opt/var/log/awg-manager.log`
Ping check logs: In-memory only (2 hour retention)

## Dependencies

- `amneziawg-tools` - AmneziaWG userspace tools
- `curl` - For testing
- `lighttpd` - Web server (reverse proxy)

## License

MIT
