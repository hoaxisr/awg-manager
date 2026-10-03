#ifndef SUSANIN_NEVER_H
#define SUSANIN_NEVER_H

#include "config.h"

#include <time.h>

/*
 * vpn_never — список направлений, которые ВСЕГДА идут напрямую (never VPN).
 * Домены резолвятся в A-записи; IP/CIDR берутся как есть. Все записи
 * попадают в ipset susanin_never, для которого в цепочке SUSANIN стоит
 * RETURN до правил маркировки. Изменение файла подхватывается на лету.
 */

typedef struct vpn_never vpn_never;

vpn_never *vn_new(void);
void vn_free(vpn_never *v);
void vn_mark_dirty(vpn_never *v);
int vn_changed(vpn_never *v, const susanin_config *cfg);
int vn_refresh(vpn_never *v, const susanin_config *cfg);

/* 1, если адрес недавно был в susanin_never (его не учим в VPN). */
int vn_is_recently_never(const char *ip, time_t now);

#endif
