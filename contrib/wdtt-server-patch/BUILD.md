# wdtt-server patch: `-no-nat` + `panel.db` в `-config-dir` для Keenetic / awg-manager

Патчи к [ildarmaga/wdtt](https://github.com/ildarmaga/wdtt) (`server/cmd`):

## no-nat.patch

- **`-no-nat`** — не трогать iptables/nft и `ip_forward` (NAT на роутере делает awg-manager).
- **`-nat-if eth3`** — явный WAN для встроенного MASQUERADE (если `-no-nat` не задан).
- **cleanup** — снятие правил `WDTT_MANAGED` и таблиц `nft wdtt` при остановке.

## panel-db.patch

- **`panel.db` в `-config-dir`** — `{config-dir}/panel.db` вместо жёсткого `/etc/wdtt/panel.db`.
- **автосоздание** — SQLite и таблицы `wdtt_*` при первом запуске (headless без веб-панели).
- Нужно для GETCONF: без panel.db сервер отвечает NOCONF.

## Сборка (Entware arm64)

```bash
git clone https://github.com/ildarmaga/wdtt.git
cd wdtt
git apply /path/to/no-nat.patch
git apply /path/to/panel-db.patch
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o wdtt-server-linux-arm64 ./server/cmd
```

Релиз для awg-manager: **`0.1.5-awgm`** → `http://repo.hoaxisr.ru/wt/server/0.1.5-awgm/`

SHA256 и имена файлов — в `internal/wdtt/install.go`.
