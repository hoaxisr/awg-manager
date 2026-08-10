# wdtt-server patch: `-no-nat` + `panel.db` + `-wg-iface` для Keenetic / awg-manager

Патчи к [ildarmaga/wdtt](https://github.com/ildarmaga/wdtt), тег **`v1.4.62`** (`server/cmd`).
Порядок наложения: `no-nat.patch` → `panel-db.patch` → `wg-iface.patch` → `no-wipe.patch` → `raw-listen.patch` + копирование `server_raw.go`.

## Raw (qWDTT 1.4)

Клиент и сервер qWDTT 1.4 используют:

| Сторона | Флаг | Пример |
|---------|------|--------|
| **Клиент** (wt-client / qWDTT) | `-mode rawtun` | peer = `host:56003` |
| **Сервер** (wdtt-server) | `-listen-raw 0.0.0.0:56003` | отдельный UDP-порт, **не** тот же что `-listen` |

awg-manager передаёт эти флаги из UI (режим Raw). **Не** используйте устаревший `-relay-mode raw`.

Публичный GitHub [SpaceNeuroX/proxy-turn-vk-android](https://github.com/SpaceNeuroX/proxy-turn-vk-android) на теге `v1.4.0` **не содержал** `-listen-raw` в git — реализация портирована в awg-manager (`server_raw.go` + `raw-listen.patch`) по бинарнику APK qWDTT 1.4.

### Сервер с Raw из APK (только VPS amd64)

```bash
bash scripts/extract-wdtt-from-apk.sh
# build/wdtt/wdtt-server-linux-amd64-qwdtt-1.4.0 — -listen-raw, -dns (см. contrib/qwdtt-apk/README.md)
```

На **Keenetic arm64** соберите через `scripts/build-wdtt-server.sh` (ildarmaga + `server_raw.go`).

Протокол Raw (портировано из APK):

| Шаг | Формат |
|-----|--------|
| Client → Server | `GETCONF_RAW:deviceID\|password` |
| Server → Client | `RAWCONF:clientIP\|dns\|mtu` |
| Далее | IPv4-пакеты через WRAP/UDP |

### Сборка бинарников для релиза awg-manager

```bash
# Клиент (arm64 + mips*) — SpaceNeuroX go_client
bash scripts/build-wdtt-client.sh

# Сервер (arm64) — qWDTT monolith + Keenetic (по умолчанию)
WDTT_SERVER_SOURCE=qwdtt-monolith bash scripts/build-wdtt-server.sh

# Legacy: ildarmaga + server_raw.go (медленный raw vs official)
WDTT_SERVER_SOURCE=ildarmaga bash scripts/build-wdtt-server.sh
```

После сборки — SHA256 и размер в `internal/wdtt/install.go`, заливка на `repo.hoaxisr.ru/wt/`.

Версия релиза awg-manager: **`1.4.0-awgm`** (клиент + server `0.2.0-awgm` или единый префикс — см. `install.go`).

## no-nat.patch

- **`-no-nat`** — не трогать iptables/nft и `ip_forward` (NAT на роутере делает awg-manager).
- **`-nat-if eth3`** — явный WAN для встроенного MASQUERADE (если `-no-nat` не задан).
- **cleanup** — снятие правил `WDTT_MANAGED` и таблиц `nft wdtt` при остановке.

## panel-db.patch

- **`panel.db` в `-config-dir`** — `{config-dir}/panel.db` вместо жёсткого `/etc/wdtt/panel.db`.
- **автосоздание** — SQLite и таблицы `wdtt_*` при первом запуске (headless без веб-панели).
- Нужно для GETCONF: без panel.db сервер отвечает NOCONF.

## no-wipe.patch

- **`initDB` не зовёт `saveDB()` после неудачной загрузки** — `saveDB()` делает
  полный `DELETE`+`INSERT` из памяти, поэтому одна сбойная загрузка (битый
  `-wal`, залипший lock) навсегда уносила всех клиентов, оставляя только
  главный пароль (issue #679).

## wg-iface.patch

- **`-wg-iface opkgtun90`** — имя userspace WireGuard-интерфейса (по умолчанию `wdtt0`).
- Нужен для регистрации WDTT в NDMS как `OpkgTun17..49`: NAT/LAN/policy через `ip nat` и ACL роутера, как у managed WireGuard.

## getNextIP (qwdtt-monolith / apply-keenetic.py)

- **`10.66.0.1` пропускается** при выдаче IP клиенту — это адрес шлюза OpkgTun на Keenetic (`DefaultWdttServerGatewayAddr`).
- Без пропуска первый клиент получал `.1`, трафик не форвардился и NAT не срабатывал.

## Сборка ildarmaga (Entware arm64)

```bash
git clone --branch v1.4.62 https://github.com/ildarmaga/wdtt.git
cd wdtt
git apply /path/to/no-nat.patch
git apply /path/to/panel-db.patch
git apply /path/to/wg-iface.patch
git apply /path/to/no-wipe.patch
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o wdtt-server-linux-arm64 ./server/cmd
```

Только `arm64`: апстримовый `pkg/paneldb` тянет `modernc.org/sqlite` →
`modernc.org/libc`, который не поддерживает `mips`/`mipsle`. Собрать
wdtt-server под `mipsel-3.4` и `mips-3.4` невозможно.

Релиз для awg-manager: см. `PinnedClientVersion` / `PinnedServerVersion` в `internal/wdtt/install.go`.

SHA256 и имена файлов — в `internal/wdtt/install.go` (обновляются после `scripts/build-wdtt-*.sh`).
