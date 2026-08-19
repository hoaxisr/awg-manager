# wt-client (qWDTT) — патч awg-manager

Порт **rawtun**-клиента из qWDTT 1.4 APK в исходники SpaceNeuroX `go_client` v1.4.0.

## Что добавляет патч

| Файл | Назначение |
|------|------------|
| `protocol_raw.go` | `GETCONF_RAW` / `RAWCONF` |
| `raw_session.go` | `RunRawSession` — TURN + WRAP без DTLS |
| `tun_fd_linux.go` | `-tun-fd-sock` + plain IP fd (как APK Android) |
| `raw_tun.go` | TUN fallback: `CreateTUN(-tun-name)` для dev без OpkgTun |
| `group_raw.go` | `WorkerGroupRaw` |
| `main_rawtun.go` | `-mode rawtun` entrypoint |
| `flags_compat.go` | `-vk-auth-mode`, `-fingerprint` (awg-manager) |
| `apply-main-patch.py` | правки `main.go` (rawtun branch) |

**Не патчим:** `dispatcher.go` — в APK тот же upstream chunk RR (`chunkSizeFor`, без flow-hash).

**Не переносим:** `-listen-direct`, `-dns`.

## Протокол Raw

```
Client → Server: GETCONF_RAW:deviceID|password   (только worker #1)
Server → Client: RAWCONF:10.70.66.x|1.1.1.1|1300
Workers 2..N:    AUTH:deviceID|password           (relay привязка без ответа)
```

Транспорт: VK TURN → UDP relay → WRAP/RTP AEAD → VPS `-listen-raw`.

## Сборка

```bash
# Linux / WSL Ubuntu (не docker-desktop WSL!)
./scripts/build-keenetic-test.sh r19 aarch64-3.10
```

Или по шагам:

```bash
./scripts/build-wdtt-client.sh
./scripts/build-wdtt-server.sh
python3 scripts/update-wdtt-pins.py
# залить build/wdtt/* на repo.hoaxisr.ru/wt/ — иначе пин указывает в пустоту
./scripts/build-ipk.sh 2.16.5+r19 aarch64-3.10
```

В IPK бинари не кладутся: awg-manager скачивает их с зеркала по пину `internal/wdtt/install.go` и сверяет SHA256. Бинарь, оказавшийся в `/opt/bin` мимо этого пути, установленным не считается — UI предложит поставить пин.

## awg-manager

- `-mode rawtun` при `ConnMode: raw`
- `-peer` = адрес VPS `:listen-raw` (например `87.x.x.x:56013`)
- Пароль = WRAP key (HKDF от `-password`)

Флаги awg-manager:

| awg-manager | клиент |
|-------------|--------|
| `-vk-auth-mode vkcalls` | `-vk-auth anonymous -vk-anon-path vkcalls` |
| `-fingerprint chrome` | принимается, игнорируется |

## Проверка на роутере

1. Сервер VPS: `-listen-raw :56013`, пароль в unit.
2. Клиент: `-mode rawtun -peer VPS:56013 -password … -vk …`
3. В логе: `RAWCONF|10.70.66.x|…`, TUN `wdtturn0` up.
4. Пинг через туннель (маршрутизацию на Keenetic настраивает awg-manager отдельно).
