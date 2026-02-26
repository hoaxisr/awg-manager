# Changelog

All notable changes to this project will be documented in this file.

## [2.2.6] - 2026-02-19

### Исправлено
- **Туннели не запускались при PPPoE/IPoE/L2TP/PPTP-подключении** — система ошибочно сообщала «нет доступного WAN», даже если интерфейс провайдера работал. Причина: при загрузке физический WAN (ISP) скрывался из трекера, и данные о связи с вышестоящим протоколом терялись
- **«All WANs excluded» при единственном провайдере через PPPoE/IPoE** — после переустановки или перезагрузки программа считала, что все подключения исключены, хотя ни одно не было исключено в настройках
- **Исключение вышестоящего протокола больше не блокирует физический WAN** — если пользователь исключил PPPoE/IPoE, а ISP (Static IP / DHCP) остаётся доступным, туннели теперь корректно используют ISP
- **«Остановка» вместо «Запуск» при включении туннеля** — во время запуска на короткое время отображался неверный статус. Исправлен порядок инициализации: NDMS-интерфейс активируется до запуска процесса
- **Не появлялась кнопка «Сохранить» при изменении времени хранения логов** — изменение значения в поле не активировало кнопку сохранения
- **Кнопки маршрутов туннелей меняли размер** — при исключении/включении WAN-интерфейсов выпадающие списки в секции «Маршруты туннелей» дёргались по ширине
- **Поле status в JSON-конфигурации туннеля** — при обновлении туннеля поле `status` в файле могло сохранять неверное значение вместо штатного `"stopped"`

### Добавлено
- **Политики доступа** — новая секция на странице маршрутизации для управления доступом устройств через туннели
- Диалог подтверждения при удалении туннелей и политик (вместо системного `confirm`)

### Изменено
- Карточки «Статус WAN» и «Исключённые WAN» объединены в одну на странице маршрутизации

---

## [Unreleased]

### Added
- **Ping Check monitoring** - automatic tunnel health checks
  - HTTP 204 and ICMP ping connectivity checks through tunnels
  - Automatic OpkgTun interface down on OS 5.x when tunnel loses connectivity
  - Per-tunnel customizable settings with global defaults
  - Ring buffer logging (2 hours retention, memory-only)
  - WAN event handling (pause on WAN down, resume on WAN up)
  - Dead tunnel detection blocks autostart until recovery
  - Dedicated monitoring page with status grid and filterable logs
  - Visual "DEAD" indicator on tunnel cards
- Settings page with ping check configuration and defaults editor
- Settings schema v2 with server port/interface and pingCheck configuration
- API endpoints: `/api/pingcheck/status`, `/api/pingcheck/logs`, `/api/pingcheck/check-now`
- API endpoints: `/api/settings/get`, `/api/settings/update`
- Unit tests for settings migration, log buffer, and API handlers

### Changed
- Settings migrated from port file to unified `settings.json`
- Server port and interface now configurable via settings (was CLI flags)

## [1.0.0] - Previous Release

- Initial release with tunnel management, testing, and auto-start features
