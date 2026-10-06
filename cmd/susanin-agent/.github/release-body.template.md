Susanin.Keenetic {{TAG}}

### Установка (первый раз)
```sh
opkg update && opkg install ca-certificates ipset iptables conntrack
wget -qO- https://raw.githubusercontent.com/R17a/Susanin.Keenetic/main/install.sh | sh
```
Установщик скачивает готовый архив под вашу архитектуру, определяет LAN/VPN и
(при наличии) OpenConnect, запрашивает подтверждение и запускает демон.
Существующие `susanin.conf`, `vpn_always.txt`, `vpn_never.txt` и кэш сохраняются.

### Обновление
```sh
sh /opt/susanin/tools/susanin.sh update {{TAG}}
```
`update` печатает `installed → target`, делает бэкап `susanin.conf` и
`susanin.state` в `/opt/susanin/var/backup/`, заменяет бинарь и скрипты,
**не трогая** `etc/` и `var/`, затем перезапускает демон.
Версию после обновления видно первой строкой в `susanin-agent status`.

### Удаление
```sh
sh /opt/susanin/tools/susanin.sh uninstall
sh /opt/susanin/tools/susanin.sh uninstall --purge
```

### Что нового
<!-- CHANGELOG -->
### Диагностика

Если что-то работает не так — сначала запустите диагностику: проверит окружение,
настройки и списки, выдаст рекомендации:
```sh
sh /opt/susanin/tools/diagnose.sh
```
Для отправки полного отчёта (логи, состояние) — соберите и приложите вывод:
```sh
sh /opt/susanin/tools/report.sh
# или без установки:
wget -qO- https://raw.githubusercontent.com/R17a/Susanin.Keenetic/{{TAG}}/tools/report.sh | sh
```
Отчёт сохраняется в `/opt/susanin/var/report.txt`.

### Сборки
- архивы: `susanin-keenetic-deploy-mipsel.tar.gz`, `…-mips.tar.gz`,
  `…-aarch64.tar.gz`, `…-armv7.tar.gz`, `…-x86_64.tar.gz` и `SHA256SUMS`;
- контейнер: `ghcr.io/r17a/susanin.keenetic:{{TAG}}` (+`latest`).

**Проверено**: mipsel — Keenetic Viva, KeeneticOS 5.1.4–5.1.5, Entware
(+ клиент OpenConnect). Остальные архитектуры собраны и приложены готовыми
архивами, но на живом железе не тестировались — сообщайте об ошибках в Issues.
