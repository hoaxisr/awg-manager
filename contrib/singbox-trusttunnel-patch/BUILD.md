# sing-box (hoaxisr/amnezia-box): TrustTunnel outbound

Порт протокола [TrustTunnel](https://trusttunnel.org/) из
[xchacha20-poly1305/sing-trusttunnel](https://github.com/xchacha20-poly1305/sing-trusttunnel)
и [qr243vbi/sing-box](https://github.com/qr243vbi/sing-box) (upstream PR SagerNet/sing-box#3831 закрыт).

## Файлы в amnezia-box

| Файл | Назначение |
|------|------------|
| `option/trusttunnel.go` | JSON-опции outbound/inbound |
| `protocol/trusttunnel/outbound.go` | `with_trusttunnel_outbound` |
| `include/trusttunnel_outbound.go` | регистрация outbound |
| `include/trusttunnel_outbound_stub.go` | stub без тега |
| `constant/proxy.go` | `TypeTrustTunnel` |
| `include/registry.go` | `registerTrustTunnelOutbound` |
| `release/DEFAULT_BUILD_TAGS` | `+with_trusttunnel_outbound` |
| `go.mod` | `github.com/xchacha20-poly1305/sing-trusttunnel v0.2.0` |

## Сборка (Entware)

Тег `with_trusttunnel_outbound` включён в `DEFAULT_BUILD_TAGS` (mipsel + aarch64 с `with_quic`).
На **mips-3.4** (`DEFAULT_BUILD_TAGS_OTHERS`) тега нет — HTTP/3 недоступен без QUIC.

```bash
# в amnezia-box, после push — CI release-entware.yml по тегу awg-v*
git tag awg-v1.14.0-beta.14-awgm.11
git push origin awg-v1.14.0-beta.14-awgm.11
```

## awg-manager

1. `./scripts/regen-embedded.sh 1.14.0-beta.14-awgm.11` (или зеркало `repo.hoaxisr.ru`)
2. Импорт: `tt://`, `trustunnel.ru/connect/?d=`, TOML `[endpoint]` — `internal/singbox/vlink/trusttunnel*.go`
3. Feature gate: `with_trusttunnel_outbound` в `internal/singbox/config.go`

## Проверка бинаря

```bash
sing-box version | grep trusttunnel
sing-box check -C /path/to/config.d   # outbound type trusttunnel, quic: true
```
