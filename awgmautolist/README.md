# awgmautolist 0.10.0-1

Отдельный демон для Keenetic/Entware. AF_PACKET наблюдает реальные LAN-потоки,
проверяет существующее покрытие и исключения, выполняет direct/WAN/tunnel-пробы
и только затем предлагает или создаёт правила через AWGM Router API.

## Наблюдение

- По умолчанию: TCP/443 и DNS/53 для имён.
- При включённом UDP: STUN/TURN на любых UDP-портах. Произвольный UDP-протокол
  не становится кандидатом, потому что универсальной активной UDP-пробы нет.
- TCP-поводы: SYN/TLS без ответа, RST и остановка ответа не более чем на 30 КиБ.
  TCP candidate требует пассивно полученное DNS/SNI-имя; unnamed TCP не создаёт
  IP-only карточку или правило. DNS hints изолируются по клиенту и IP, а apex/www
  объединяются через registered zone. IP-only остаётся допустимым для UDP STUN/TURN.
- Clash connections, журналы sing-box и conntrack не используются как evidence.
  Read-only runtime API может использоваться для фактического selector/outbound.

Coverage использует отдельный AWGM always-current index активного applied slot
(tproxy или fakeip-tun). Полная generation собирается из активных rule-set references
и выбранных exclusions; warm hit/negative выполняются в памяти без JSON/SRS reads и
без `sing-box rule-set match` subprocesses. Ошибка или неполнота generation fail-closed:
candidate и write не создаются. Старый `/rulesets/match` не используется как fallback.

DNS для IP-покрытия — один выбранный numeric-IP сервер sing-box, простые A/AAAA через
UDP, DoH или DoT. Системный и AWGM resolver не вызываются. Name-only coverage/exclusion
проверяется до DNS; затем observed IP может проверяться параллельно с A/AAAA.

Повторные suspects для queued/active/уже решённого candidate отбрасываются на входе,
до bounded queue. Переполнение по-прежнему видно в `queueDropped` и журнале.

## Правила

Демон владеет только inline rule-set `awgm-autolist` и одним безусловным
owner-правилом. Домены записываются как registered zone (`www.example.co.uk` →
`example.co.uk`), а активная проба сохраняет исходный SNI/IP. IP-only UDP цель получает
`/32` или `/128` без port/network: один подтверждённый blocked порт покрывает весь IP.
Для TCP такие CIDR fallback-правила не создаются.

AutoAdd доступен только при включённой tunnel-пробе и verdict `reachable`.
AutoOff отсутствует. Удаление записей выполняется пользователем. Firewall, NDMS
и kernel routes демон напрямую не изменяет.

## Файлы

```text
Бинарник:       /opt/bin/awgmautolist
Настройки:      /opt/etc/awgmautolist.json
State/history:  /opt/etc/awgmautolist-state.json
Init:           /opt/etc/init.d/S99awgmautolist
Журнал:         /tmp/awgmautolist.log
```

Журнал находится только в tmpfs, ограничен 5 МиБ и усекается без ротации.

## Запуск

```sh
export AWGM_API_KEY="ключ AWGM"
/opt/bin/awgmautolist \
  -router http://127.0.0.1:2222 \
  -config /opt/etc/awgmautolist.json \
  -listen 127.0.0.1:2226
```

Loopback API содержит только operational status/config и действия кандидатов;
диагностических endpoints и ручной `/probe` нет.

## Сборка

```sh
go mod vendor
./build-ipk.sh all
```

Создаются статические IPK для `aarch64-3.10`, `mipsel-3.4` и `mips-3.4`.
Пакеты используют opkg-compatible ustar и проверяются на AppleDouble entries.

Техническое задание и подробный план находятся в независимом каталоге
`/Users/admin/src/awgm-mini/TECHNICAL_SPEC.md` и `IMPLEMENTATION_PLAN.md`.
