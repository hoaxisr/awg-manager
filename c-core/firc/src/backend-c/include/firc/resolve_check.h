#ifndef FIRC_RESOLVE_CHECK_H
#define FIRC_RESOLVE_CHECK_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/fakeip.h"
#include "firc/models.h"

/* True if server parses and any of its entries lies inside pool (a fake address, unreachable).
   hit (may be NULL) gets that entry as firc_resolver_addr_format writes it. */
bool firc_resolve_server_in_pool(const char *server, const firc_fakeip_t *pool, char *hit, size_t hit_cap);

/* The first group whose server lies inside the pool, or NULL; hit as above. */
const firc_group_t *firc_resolve_groups_in_pool(firc_group_t *const *groups, size_t n,
                                                const firc_fakeip_t *pool, char *hit, size_t hit_cap);

#endif
