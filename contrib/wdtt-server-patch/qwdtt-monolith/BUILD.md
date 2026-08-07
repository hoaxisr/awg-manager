# qWDTT monolith (SpaceNeuroX) + Keenetic

Сборка **official monolith** из [SpaceNeuroX/proxy-turn-vk-android](https://github.com/SpaceNeuroX/proxy-turn-vk-android) с флагами qWDTT 1.4 APK (`+dirty`):

| Флаг | Назначение |
|------|------------|
| `-listen-direct` | WRAP UDP без DTLS — WG ~26 Mbit (peer `:56002`) |
| `-listen-raw` | Raw TUN — ~59 Mbit down на amd64 official |
| `-no-nat` / `-nat-if` | NAT на роутере (awg-manager) |
| `-wg-iface` | OpkgTun для NDMS |

База: коммит **`2dd5d37f18a0`** (go build info APK qWDTT 1.4) + `server_direct.go` + `server_raw.go` + `apply-keenetic.py`.

БД сервера: **`passwords.json`** в `-config-dir` (awg-manager пишет его перед стартом).

```bash
WDTT_SERVER_SOURCE=qwdtt-monolith bash scripts/build-wdtt-server.sh
```

Для A/B на VPS amd64:

```bash
WDTT_SERVER_SOURCE=qwdtt-monolith bash scripts/build-wdtt-server-amd64.sh
```
