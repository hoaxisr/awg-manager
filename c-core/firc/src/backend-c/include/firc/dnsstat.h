#ifndef FIRC_DNSSTAT_H
#define FIRC_DNSSTAT_H

/* Loop thread only. Every call accepts a NULL store and does nothing. Times are monotonic seconds. */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip_addr.h"
#include "firc/id.h"

#define FIRC_DNSSTAT_KEYS 16384
#define FIRC_DNSSTAT_HOUR 3600
#define FIRC_DNSSTAT_HOURS 24

typedef struct firc_dnsstat firc_dnsstat_t;

typedef struct {
    uint32_t queries, blocked;
} firc_dnsstat_count_t;

/* NULL when max_keys is 0 or the allocation fails. */
firc_dnsstat_t *firc_dnsstat_new(size_t max_keys);
void firc_dnsstat_free(firc_dnsstat_t *s);

/* Counts one query of a folded name without the trailing dot; when full, the key with the fewest queries in the
 * window goes (ties: asked longest ago). A NULL client or an empty name is not counted. */
void firc_dnsstat_note(firc_dnsstat_t *s, const char *name, const firc_ip_t *client, firc_id_t owner, bool blocked,
                       int64_t now_mono);
/* Expires the hours that left the window and drops the keys with nothing left in it. */
void firc_dnsstat_sweep(firc_dnsstat_t *s, int64_t now_mono);

/* Every query of the window, evicted keys included. */
firc_dnsstat_count_t firc_dnsstat_totals(const firc_dnsstat_t *s, int64_t now_mono);
/* Of those, the queries for names under .arpa. */
firc_dnsstat_count_t firc_dnsstat_totals_arpa(const firc_dnsstat_t *s, int64_t now_mono);
/* True when the name a visit gave in name_slot is "arpa" or ends in ".arpa". */
bool firc_dnsstat_name_is_arpa(const firc_dnsstat_t *s, uint32_t name_slot);

/* Distinct names held, and the bound of a name slot. */
size_t firc_dnsstat_names(const firc_dnsstat_t *s);
size_t firc_dnsstat_name_slots(const firc_dnsstat_t *s);
size_t firc_dnsstat_keys(const firc_dnsstat_t *s);

/* Returns false to stop. */
typedef bool (*firc_dnsstat_visit_fn)(void *ud, uint32_t name_slot, const char *name, const firc_ip_t *client,
                                      firc_dnsstat_count_t c, int64_t last_mono, firc_id_t owner, bool last_blocked);
/* Visits every key with a query in the window, in no order. */
void firc_dnsstat_each(const firc_dnsstat_t *s, int64_t now_mono, firc_dnsstat_visit_fn fn, void *ud);

/* Longest occupied run in either index. Tests only. */
size_t firc_dnsstat_probe_max_for_test(const firc_dnsstat_t *s);

#endif /* FIRC_DNSSTAT_H */
