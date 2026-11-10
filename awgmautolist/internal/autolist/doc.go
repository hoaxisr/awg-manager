// Package autolist — сателлит-демон awg-manager: находит сайты, которые не
// открываются напрямую, подтверждает их активными пробами и предлагает
// пользователю (или автоматически добавляет) правила маршрутизации через
// sing-box router.
//
// Принцип «рядом, не внутри»: основной демон awg-manager не изменяется,
// интеграция только через его публичные поверхности —
//   - GET  /api/singbox/clash/connections  (живые соединения sing-box),
//   - GET  /api/logs?bucket=singbox        (dial-ошибки движка),
//   - GET/POST /api/singbox/router/rules/* и /staging (управление правилами),
//   - Authorization: Bearer <apiKey>       (см. internal/auth/middleware.go).
//
// Поведение включает пороги отказов, очередь проб с ограничениями, вердикты
// blocked/reachable/unknown и удаление правил после восстановления доступа.
// Пакетные стратегии наблюдаются через AF_PACKET на LAN bridge с kernel BPF;
// маршрутная атрибуция выполняется по Clash API sing-box. Firewall не меняется.
package autolist

// Версия сателлита, печатается в --version и в журнале при старте.
const Version = "0.1.0"
