# qWDTT monolith (SpaceNeuroX) + Keenetic

Сборка **official monolith** из [SpaceNeuroX/proxy-turn-vk-android](https://github.com/SpaceNeuroX/proxy-turn-vk-android) с флагами qWDTT 1.4 APK (`+dirty`):

| Флаг | Назначение |
|------|------------|
| `-listen-direct` | WRAP UDP без DTLS — WG ~26 Mbit (peer `:56002`) |
| `-listen-raw` | Raw TUN — ~59 Mbit down на amd64 official |
| `-no-nat` / `-nat-if` | NAT на роутере (awg-manager) |
| `-wg-iface` | OpkgTun для NDMS |

База: коммит **`afe989b`** (SpaceNeuroX v1.4 + RAW downlink pacer) + keenetic flags через `apply-keenetic.py`.

Upstream v1.4 уже содержит `-listen-direct`, `-listen-raw`, `rawRouter` и `pacer.go` — отдельные `server_raw.go`/`server_direct.go` не нужны.

БД сервера: **`passwords.json`** в `-config-dir` (awg-manager пишет его перед стартом).

```bash
WDTT_SERVER_SOURCE=qwdtt-monolith bash scripts/build-wdtt-server.sh
```

Для A/B на VPS amd64:

```bash
WDTT_SERVER_SOURCE=qwdtt-monolith bash scripts/build-wdtt-server-amd64.sh
```
