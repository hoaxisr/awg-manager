# Susanin.Keenetic — руководство пользователя

Кратко: программа смотрит, какие соединения «не работают» из-за блокировок,
пробует их через VPN, запоминает рабочие и держит их в VPN. Если VPN недоступен —
всё идёт напрямую. Есть списки «всегда через VPN» и «всегда напрямую».

Подробности и устройство — в [README.md](README.md); установка/обновление —
в [DEPLOY.md](DEPLOY.md).

## Быстрый старт

```sh
# 1. Нужные пакеты Entware
opkg update && opkg install ca-certificates ipset iptables conntrack

# 2. Установка (одной строкой)
wget -qO- https://raw.githubusercontent.com/R17a/Susanin.Keenetic/main/install.sh | sh

# 3. Проверка
sh /opt/susanin/tools/susanin.sh status
```

Установщик сам определит архитектуру, LAN и VPN, спросит подтверждение и
запустит демон. `susanin.conf`, `vpn_always.txt` и `vpn_never.txt` при
обновлении не перезаписываются.

## Управление

| Действие | Команда |
|---|---|
| Состояние | `sh /opt/susanin/tools/susanin.sh status` |
| Запуск / стоп / рестарт | `… start` / `… stop` / `… restart` |
| Перечитать конфиг | `… reload` |
| Заново найти LAN/VPN | `… rescan` |
| Лог (последние N строк) | `… log 100` |
| Убрать адрес из кэша | `… forget <ip>` |
| Добавить в VPN вручную | `… add <ip> tcp test` |
| Диагностика | `sh /opt/susanin/tools/diagnose.sh` |
| Отчёт для Issue | `sh /opt/susanin/tools/report.sh` |

## Настройка

Файл `/opt/susanin/etc/susanin.conf`. Открытые вопросы и примеры полей — в
[README.md](README.md). Часто трогают:

- `egress_interface` — VPN-интерфейс(ы); несколько через запятую (фейловер);
- `lan_interfaces`, `lan_subnets` — трафик каких сетей анализировать;
- `fast_syn_min_op=1` — быстрее детект «SYN без ответа» (больше ложных);
- `soft_interval=1s` — быстрее детект «заглохшего» потока (по умолчанию 1s);
- `ok_evict_misses=3` — сколько «сбоев» подряд до снятия из VPN-кэша;
- `promo_per_min=30` — лимит новых проверок через VPN в минуту;
- `learn_exclude_ports` — порты, где отключено быстрое обучение (скан-шум);
- `disk_mode=normal|soft` — сколько писать на носитель.

После правки: `sh /opt/susanin/tools/susanin.sh reload` (или `rescan`, если
меняли сетевые поля). Изменения `vpn_always.txt`/`vpn_never.txt` подхватываются
сами, перезапуск не нужен.

## Типовые задачи

**Домен всегда через VPN.** Добавьте строку в `/opt/susanin/etc/vpn_always.txt`:

```
example.com          # только сам домен
*.example.com        # домен и все поддомены (для CDN)
192.0.2.0/24         # диапазон
```

**Домен всегда напрямую (никогда в VPN).** Строка в
`/opt/susanin/etc/vpn_never.txt` (тот же формат, включая `*.example.com`).
Такой адрес не попадёт в VPN-кэш и не будет обучен; если добавлен позже —
уже открытые VPN-потоки разрываются, клиент переподключается напрямую.

**Адрес ошибочно в VPN.** `sh /opt/susanin/tools/susanin.sh forget <ip>`.

**Сменили или удалили VPN.** `sh /opt/susanin/tools/susanin.sh rescan` — заново
найдёт LAN/VPN, обновит конфиг и применит без полной перезагрузки.

**Несколько VPN (фейловер).** В конфиге:

```
egress_interface=nwg0,nwg1
egress_address=10.8.1.1,10.8.1.2
```

Активен один туннель; при обрыве трафик автоматически идёт через следующий
живой, а если живых нет — напрямую.

**Экономия носителя.** При установке во внутреннюю память включится
`disk_mode=soft`: лог не ведётся, бэкапов нет, состояние сохраняется раз в
`soft_state_interval` (12 часов).

## Обновление и удаление

```sh
sh /opt/susanin/tools/susanin.sh update v0.3.10  # версия (актуальную см. в Releases)
sh /opt/susanin/tools/susanin.sh uninstall        # снять, конфиг сохранить
sh /opt/susanin/tools/susanin.sh uninstall --purge # удалить всё
```

Если что-то не работает — см. [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
