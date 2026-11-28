# awgmautolist

Отдельный демон для Keenetic/Entware. Он наблюдает реальные LAN-потоки через
AF_PACKET, проверяет существующую маршрутизацию и доступность целей напрямую и
через выбранный туннель, а затем предлагает или создаёт правила через AWGM Router API.

## Первоначальная настройка

Для запуска рабочего engine нужен явно выбранный туннель. Пока он не выбран,
сателлит находится в `setupRequired`: capture, пробы и запись правил не работают,
но status/config API остаётся доступным для мастера настройки.

В панели предлагается запустить wizard:

1. выбрать туннель и режим наблюдения — только сайты или сайты + STUN/TURN (UDP);
2. выбрать DNS — `dns.final` или конкретный поддерживаемый numeric-IP сервер;
3. выбрать ручное подтверждение либо автодобавление;
4. при автодобавлении отдельно принять риски и заполнить исключения.

Ручной режим рекомендуется. AutoAdd может самостоятельно инициировать подключения,
раскрывать сайтам IP VPN-сервера и направлять в туннель нежелательные ресурсы.
Перед его включением показываются предупреждения о зарегистрированных зонах,
UDP-правилах на весь IP, дополнительном трафике и нагрузке. Исключения можно
пропустить только с отдельным подтверждением риска.

Режима «первый доступный туннель» нет. Старые конфигурации с пустым
`targetInterface` переводятся в `setupRequired`; существующие правила при этом не
переключаются автоматически.

## Наблюдение и кандидаты

- По умолчанию наблюдаются TCP/443 и DNS/53 для имён.
- При включённом UDP добавляются STUN/TURN на любых UDP-портах. Произвольный UDP
  не становится кандидатом, поскольку универсальной активной UDP-пробы нет.
- TCP-поводы: SYN/TLS без ответа, RST и остановка ответа не более чем на 30 КиБ.
- TCP candidate требует пассивно полученное DNS/SNI-имя; unnamed TCP не создаёт
  карточку или правило. DNS hints изолируются по клиенту и IP.
- Clash connections, журналы sing-box и conntrack не используются как evidence.
  Read-only runtime API применяется для фактического selector/outbound.

Повторные suspects для queued/active/уже решённого candidate отбрасываются до
bounded queue. Переполнение видно в `queueDropped` и журнале.

## Ошибки и списки в UI

Кандидат попадает в «Добавленные» только после успешной записи и обратного
чтения подтверждающего правила. Любая ошибка Add оставляет его кандидатом с
красной рамкой; подробная причина доступна в «Подробности проверки» и журнале.
Повторное добавление идемпотентно завершает частично выполненную операцию.

«Кандидаты», «Добавленные» и «История» загружаются со стороны backend страницами
по 128 записей. Кнопка «Показать ещё» использует стабильный cursor; поиск также
выполняется на backend. Ограничений хранения в 512 карточек/событий нет, silent trim
не выполняется. Operational queue, таймауты и настраиваемый лимит добавлений в час
сохраняются.

Логи доступны в «Инструменты → Журнал» с источником `autolist`. Панель читает
существующий `/tmp/awgmautolist.log` порциями без отдельного транспорта; при ошибках
проб в журнал попадают доступные `TimeDNS`, `TimeConnect` и `TimeTLS`.

## Coverage

AWGM хранит always-current index активного applied slot (`router`/tproxy или
`fakeip`). Generation собирается из активных rule-set references и выбранных
исключений; warm match выполняется в памяти без повторных JSON/SRS reads и без
`sing-box rule-set match` subprocesses.

`indexed-snapshot` возвращает только `generation`, `slot`, обычные правила и
`skipped_rule_sets`; полное содержимое rule-sets по нему не передаётся. Совпадения
проверяются отдельным `indexed-match`. Смена generation запускает повтор всей
проверки, ошибка generation запрещает создание candidate/write. Старый
`/rulesets/match` fallback отсутствует.

DNS для IP-покрытия — один выбранный numeric-IP сервер sing-box: UDP, DoH или DoT.
Системный и AWGM resolver не вызываются. Сначала проверяются name-only
coverage/exclusion, затем observed IP и A/AAAA-адреса.

## Правила

Демон владеет только inline rule-set `awgm-autolist` и одним безусловным
owner-правилом. Домены записываются как registered zone (`www.example.co.uk` →
`example.co.uk`), активная проба сохраняет исходный SNI/IP. IP-only UDP цель получает
`/32` или `/128` без port/network: один подтверждённый blocked порт покрывает весь IP.
Для TCP такие CIDR fallback-правила не создаются.

AutoAdd доступен только при включённой tunnel-пробе и verdict `reachable`.
AutoOff отсутствует: записи удаляются пользователем. Firewall, NDMS и kernel routes
демон напрямую не изменяет.

## Файлы и миграция

```text
Бинарник:       /opt/bin/awgmautolist
Каталог:        /opt/etc/awgmautolist/ (переопределяется AWGM_AUTOLIST_DIR)
Настройки:      /opt/etc/awgmautolist/config.json
State:          /opt/etc/awgmautolist/state.json
Legacy history: /opt/etc/awgmautolist/history.json
Intent/ключ:    /opt/etc/awgmautolist/enabled, /opt/etc/awgmautolist/key
Init:           /opt/etc/init.d/S99awgmautolist
Лог-буфер:      /tmp/awgmautolist.log
```

При обновлении старые flat config/state/history/key/enabled/awgmautolist.conf однократно переносятся
в рабочий каталог. Существующие новые файлы не перезаписываются. После миграции
используются только файлы рабочего каталога: постоянных fallback-чтений из
`/opt/etc/awgmautolist.{json,key,enabled}` нет. При свежем standalone-запуске каталог
создаётся автоматически.

Лог-буфер находится в tmpfs, ограничен 5 МиБ и усекается без ротации.

## Запуск и operational API

```sh
export AWGM_API_KEY="ключ AWGM"
/opt/bin/awgmautolist \
  -router http://127.0.0.1:2222 \
  -config /opt/etc/awgmautolist/config.json \
  -listen 127.0.0.1:2226
```

Адрес панели `-router` необязателен: явный флаг → `AWGM_ROUTER_URL` → `server.port`
из `/opt/etc/awg-manager/settings.json` → `http://127.0.0.1:2222`.

Loopback API доступен только на loopback и содержит:

- `GET /status` — operational state и `setupRequired`, без полных списков;
- `GET/POST /config` — сохранённая конфигурация и её изменение;
- `POST /list` — страницы candidates/history с фильтром состояния, поиском и cursor;
- действия кандидатов и очистка истории.

Диагностического ручного `/probe` нет.

## Сборка

```sh
go mod vendor
./build-ipk.sh all
```

Создаются статические IPK для `aarch64-3.10`, `mipsel-3.4` и `mips-3.4`.
Пакеты используют opkg-compatible ustar и проверяются на AppleDouble entries.
