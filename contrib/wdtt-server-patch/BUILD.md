# wdtt-server patch: `-no-nat` + `panel.db` + `-wg-iface` для Keenetic / awg-manager

Патчи к [ildarmaga/wdtt](https://github.com/ildarmaga/wdtt), тег **`v1.4.62`** (`server/cmd`).
Порядок наложения: `no-nat.patch` → `panel-db.patch` → `wg-iface.patch`.

## no-nat.patch

- **`-no-nat`** — не трогать iptables/nft и `ip_forward` (NAT на роутере делает awg-manager).
- **`-nat-if eth3`** — явный WAN для встроенного MASQUERADE (если `-no-nat` не задан).
- **cleanup** — снятие правил `WDTT_MANAGED` и таблиц `nft wdtt` при остановке.

## panel-db.patch

- **`panel.db` в `-config-dir`** — `{config-dir}/panel.db` вместо жёсткого `/etc/wdtt/panel.db`.
- **автосоздание** — SQLite и таблицы `wdtt_*` при первом запуске (headless без веб-панели).
- Нужно для GETCONF: без panel.db сервер отвечает NOCONF.

## wg-iface.patch

- **`-wg-iface opkgtun90`** — имя userspace WireGuard-интерфейса (по умолчанию `wdtt0`).
- Нужен для регистрации WDTT в NDMS как `OpkgTun90..99`: NAT/LAN/policy через `ip nat` и ACL роутера, как у managed WireGuard.

## Сборка (Entware arm64)

```bash
git clone --branch v1.4.62 https://github.com/ildarmaga/wdtt.git
cd wdtt
git apply /path/to/no-nat.patch
git apply /path/to/panel-db.patch
git apply /path/to/wg-iface.patch
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o wdtt-server-linux-arm64 ./server/cmd
```

Только `arm64`: апстримовый `pkg/paneldb` тянет `modernc.org/sqlite` →
`modernc.org/libc`, который не поддерживает `mips`/`mipsle`. Собрать
wdtt-server под `mipsel-3.4` и `mips-3.4` невозможно.

Релиз для awg-manager: **`0.1.6-awgm`** → `http://repo.hoaxisr.ru/wt/server/0.1.6-awgm/`

SHA256 и имена файлов — в `internal/wdtt/install.go`.
