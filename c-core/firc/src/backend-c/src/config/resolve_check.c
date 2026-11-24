#include "firc/resolve_check.h"

#include "firc/resolver_addr.h"

#include <string.h>

bool firc_resolve_server_in_pool(const char *server, const firc_fakeip_t *pool, char *hit, size_t hit_cap)
{
    if (hit != NULL && hit_cap > 0) { hit[0] = '\0'; }
    if (server == NULL || server[0] == '\0' || pool == NULL) { return false; }
    firc_resolver_list_t l;
    if (firc_resolver_list_parse(server, &l, NULL, 0) != FIRC_RESOLVER_LIST_OK) { return false; }
    for (size_t i = 0; i < l.n; i++) {
        firc_resolver_addr_t shown;
        memset(&shown, 0, sizeof(shown));
        shown.port = FIRC_RESOLVER_DEFAULT_PORT;
        if (l.servers[i].doh) {
            if (!firc_resolver_url_literal(l.urls[i], &shown.ip)) { continue; }
        } else {
            shown = l.servers[i];
        }
        if (firc_fakeip_overlaps(pool, &shown.ip, (uint8_t)(shown.ip.len * 8))) {
            if (hit != NULL) { (void)firc_resolver_addr_format(&shown, hit, hit_cap); }
            return true;
        }
    }
    return false;
}

const firc_group_t *firc_resolve_groups_in_pool(firc_group_t *const *groups, size_t n,
                                                const firc_fakeip_t *pool, char *hit, size_t hit_cap)
{
    for (size_t i = 0; i < n; i++) {
        if (groups[i] != NULL && firc_resolve_server_in_pool(groups[i]->resolve.server, pool, hit, hit_cap)) {
            return groups[i];
        }
    }
    return NULL;
}
