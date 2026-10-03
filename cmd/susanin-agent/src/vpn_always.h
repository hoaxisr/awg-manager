#ifndef SUSANIN_VPN_ALWAYS_H
#define SUSANIN_VPN_ALWAYS_H

#include <stddef.h>

#include "config.h"

/*
 * vpn_always — список доменов, которые всегда маршрутизируются через VPN.
 *
 * Демон читает файл (vpn_always_file), резолвит A-записи каждого домена
 * через локальный DNS (или vpn_always_dns) и добавляет полученные IP в
 * ipset-наборы susanin_ok_tcp / susanin_ok_udp (timeout 0, без истечения).
 * Любое новое TCP/UDP-соединение LAN->этот IP получает mark_ok и уходит в
 * VPN-таблицу. Домен можно убрать из файла — IP будет выпинен при следующем
 * обновлении. Изменение файла подхватывается без перезапуска демона.
 */

typedef struct vpn_always vpn_always;

vpn_always *va_new(void);
void va_free(vpn_always *v);

/* 1, если mtime/size файла изменились с прошлого refresh (дешёвый stat).
 * Вызывать можно часто; cfg нужен для пути к файлу. */
int va_changed(vpn_always *v, const susanin_config *cfg);

/* Пометить: ipset-наборы могли быть очищены (fail-open / пересоздание) —
 * при следующем refresh пины будут добавлены заново, даже если список доменов
 * не менялся. Вызывается из engine при flush и при re-provisioning. */
void va_mark_dirty(vpn_always *v);

/*
 * Один цикл обслуживания:
 *  - перечитывает файл списка;
 *  - резолвит домены, чьё время перепроверки наступило;
 *  - добавляет/убирает IP в ipset ok (tcp+udp);
 *  Возвращает 1, если остались необработанные домены (бюджет времени
 *  исчерпан) — стоит позвать refresh снова вскоре; 0 если всё обработано.
 * При недоступном туннеле вызывающий должен НЕ звать refresh (fail-open).
 */
int va_refresh(vpn_always *v, const susanin_config *cfg);

/* Shared DNS helpers (also used by vpn_never). */
int va_dns_query(const char *server, const char *domain, char ips[][16],
                 int max, int timeout_ms);
void va_pick_resolver(const susanin_config *cfg, char *out, size_t n);

#endif
