# Susanin.Keenetic — развёртывание на роутер

> Важно: на Keenetic с Entware всё ставится на флешку/USB, префикс `/opt`.
> Каталог `/tmp` — маленький (tmpfs), его для установки не используем.

## Что в архиве
- `susanin-agent.mipsel` — статический бинарь (ELF MIPS32) — демон.
- `datapath.sh` — управление правилами (iptables+ipset).
- `S94susanin` — init-скрипт автозапуска демона.
- `config.example.conf` — конфиг (на роутере — `/opt/susanin/etc/susanin.conf`).
- `vpn_always.txt` — готовый список доменов «всегда через VPN»; при установке
  копируется как `/opt/susanin/etc/vpn_always.txt` **только если файла нет**
  (существующий список никогда не перезаписывается, в т.ч. при update).
- `install.sh` — установщик (онлайн: скачивает архив под архитектуру;
  офлайн: запускается прямо из распакованного архива). Существующий
  `susanin.conf` не перезаписывает (для перезаписи `--force`).
- `update.sh` / `uninstall.sh` — обновление (конфиг и state сохраняются) и
  удаление (`--purge` — целиком).
- `report.sh` — сборщик диагностики для багрепортов (отчёт сохраняется в
  `/opt/susanin/var/report.txt`); ставится как `/opt/susanin/tools/report.sh`.
- `install.sh` — установщик одной строкой (скачивает архив под архитектуру).
- `DEPLOY.md` — этот файл.

> В релизах v0.3.0+ публикуются отдельные архивы на архитектуру:
> `susanin-keenetic-deploy-<mipsel|mips|aarch64|armv7|x86_64>.tar.gz` и
> файл контрольных сумм `SHA256SUMS`. Бинарь внутри архива называется
> `susanin-agent` (без суффикса архитектуры).

## Установка в один шаг

Зависимости Entware (сертификаты — чтобы `wget` работал по HTTPS с GitHub;
`ipset`/`iptables`/`conntrack` — для работы самой программы):
```sh
opkg update && opkg install ca-certificates ipset iptables conntrack
```

Онлайн, прямо на роутере (скачает архив под архитектуру). На Entware `curl`
обычно нет — используйте `wget`:
```sh
wget -qO- https://raw.githubusercontent.com/R17a/Susanin.Keenetic/main/install.sh \
  | sh -s -- --yes
```
Либо `opkg install curl` и затем `curl -fsSL ... | sh -s -- --yes`.

Офлайн — из распакованного архива:
```sh
cd /opt/sp
sh install.sh --yes
```

Подтверждения (`[y/N]`) можно давать двумя способами: интерактивно ответить
`y`/`yes` либо заранее передать `--yes` (`-y`) и не отвечать ни на один вопрос.
Скрипт сам создаст `/opt/susanin/{bin,etc,var,tools}` и разложит файлы:
- `/opt/susanin/bin/susanin-agent`
- `/opt/susanin/tools/datapath.sh`
- `/opt/susanin/etc/susanin.conf`
- `/opt/etc/init.d/S94susanin`

После установки:
```sh
/opt/susanin/bin/susanin-agent version
sh /opt/susanin/tools/datapath.sh up
sh /opt/susanin/tools/datapath.sh status
SUSANIN_CONF=/opt/susanin/etc/susanin.conf /opt/susanin/bin/susanin-agent run
```

## Обновление

```sh
sh /opt/susanin/tools/susanin.sh update v0.3.10
```

`update` печатает `installed → target`, сохраняет `susanin.conf` и
`susanin.state` (бэкап в `/opt/susanin/var/backup/`), заменяет бинарь и скрипты,
**не трогая** `etc/` и `var/`, затем перезапускает демон. Версию после
обновления видно первой строкой в `susanin-agent status`.

## Ручная раскладка (если без скрипта)
| Откуда (в архиве) | Куда на роутере |
|---|---|
| `susanin-agent.mipsel` | `/opt/susanin/bin/susanin-agent` |
| `datapath.sh` | `/opt/susanin/tools/datapath.sh` |
| `config.example.conf` | `/opt/susanin/etc/susanin.conf` |
| `S94susanin` | `/opt/etc/init.d/S94susanin` |
```sh
mkdir -p /opt/susanin/bin /opt/susanin/etc /opt/susanin/var /opt/susanin/tools
chmod +x /opt/susanin/bin/susanin-agent /opt/susanin/tools/datapath.sh /opt/etc/init.d/S94susanin
```

## Остановить / откатить
```sh
sh /opt/susanin/tools/datapath.sh down    # снять правила Susanin (как было)
```
`datapath.sh up` перед изменениями сохраняет бэкап в `/opt/susanin/var/datapath-<дата>/`.

## Автозапуск демона (после успешного теста)
```sh
/opt/etc/init.d/S94susanin start
/opt/etc/init.d/S94susanin stop
```

## Важно: приоритеты подключений Keenetic

Keenetic умеет сам заворачивать устройства/сегменты в VPN (Web → «Приоритеты
подключений», политика вида «Амнезия VPN» → `Wireguard0`). Это **независимый от
Susanin** механизм: правила NDM идут с приоритетом 100–107, а Susanin — 2000/2001.
Для привязанного клиента весь трафик уйдёт в VPN, и `vpn_always`/`vpn_never` на
него **не повлияют**.

У каждого клиента должен быть один «решающий» механизм:
- адаптивно через Susanin → у клиента/сегмента **не должно быть никакой политики**
  (системная «по умолчанию»). Это про **любую** политику, как бы она ни называлась
  и что бы ни делала (в т.ч. «только напрямую») — любая назначенная политика
  перекрывает Susanin;
- «всё и всегда через VPN» → можно оставить политику NDM, но `vpn_never` не сработает;
- не смешивать оба для одного клиента.

Проверка привязок:
```sh
ndmc -c "show running-config" | grep -i "ip policy"
```
Признак в conntrack: `mark=2684xxxxx` (`0xffffaXX`) — клиент под политикой Keenetic;
`mark=0` или `536870912` (`0x20000000`) — работает Susanin.
