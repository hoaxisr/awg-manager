#ifndef FIRC_KEENETIC_RESOLVERS_H
#define FIRC_KEENETIC_RESOLVERS_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"
#include "firc/keenetic_rci.h"
#include "firc/resolver_addr.h"

typedef struct firc_kn_iface_resolvers {
    char iface[16];
    size_t n;
    firc_resolver_addr_t servers[FIRC_RESOLVE_MAX_SERVERS];
} firc_kn_iface_resolvers_t;

typedef struct firc_kn_resolver_map {
    firc_kn_iface_resolvers_t *items;
    size_t n;
    size_t dropped;
    size_t unknown_ids;
} firc_kn_resolver_map_t;

firc_err_t firc_kn_resolver_map_parse(const char *name_server_json, const firc_kn_id_map_t *ids,
                                      firc_kn_resolver_map_t *out);
bool firc_kn_resolver_map_equal(const firc_kn_resolver_map_t *a, const firc_kn_resolver_map_t *b);
void firc_kn_resolver_map_free(firc_kn_resolver_map_t *m);

/* Fetches the id map for a refresh (real in the daemon, a fake in tests). */
typedef firc_err_t (*firc_kn_id_map_fetch_fn)(void *ud, firc_kn_id_map_t *out);

/* Runs on the refresher's thread; must only post to the loop. */
typedef void (*firc_kn_resolvers_changed_fn)(void *ud);

typedef struct firc_kn_resolvers firc_kn_resolvers_t;

firc_kn_resolvers_t *firc_kn_resolvers_start(const char *base_url, unsigned refresh_secs,
                                             firc_kn_resolvers_changed_fn on_change, void *ud);
void firc_kn_resolvers_stop(firc_kn_resolvers_t *r);
/* Takes m's arrays and zeroes *m. */
void firc_kn_resolvers_swap(firc_kn_resolvers_t *r, firc_kn_resolver_map_t *m);
size_t firc_kn_resolvers_for(firc_kn_resolvers_t *r, const char *kernel_iface, firc_resolver_addr_t *out,
                             size_t cap);
size_t firc_kn_resolvers_list(firc_kn_resolvers_t *r, firc_kn_iface_resolvers_t *out, size_t cap);

/* One refresh from an already-read name-server body; a failed id fetch keeps the last good list. */
firc_err_t firc_kn_resolvers_refresh_from(firc_kn_resolvers_t *r, const char *name_server_json,
                                          firc_kn_id_map_fetch_fn fetch_ids, void *ud);

#endif
