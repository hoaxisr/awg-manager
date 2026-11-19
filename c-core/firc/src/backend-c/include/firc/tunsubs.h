#ifndef FIRC_TUNSUBS_H
#define FIRC_TUNSUBS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/loop.h"
#include "firc/subhdr.h"
#include "firc/subinfo.h"
#include "firc/tunnels.h"
#include "firc/tunnodes.h"

typedef struct firc_tunsubs firc_tunsubs_t;

typedef struct {
    char url_hash[17];
    int64_t last_ok_ms, last_try_ms;
    char error[160];
    bool fetching;
    size_t nodes;
    firc_subinfo_t info;
} firc_tun_sub_state_t;

/* Loop thread only, like every call below; fetches run on a thread of its own. changed(ud) runs on the
   loop thread when a body some wanted pair uses changed. NULL when the worker cannot start. */
firc_tunsubs_t *firc_tunsubs_new(firc_loop_t *loop, const char *cache_dir, void (*changed)(void *ud), void *ud);

/* The subscriptions to keep fresh: every one of every enabled tunnel, keyed by (url, uplink identity) and
   fetched with the mark of one of its tunnels, marks[i] for t->t[i]. A new pair takes its cached body at
   once and is fetched now unless it is manual-only and cached. */
void firc_tunsubs_want(firc_tunsubs_t *s, const firc_tunnels_t *t, const uint32_t *marks);

/* Removes every cache file no subscription of any tunnel in all (enabled or not) is keyed to. */
void firc_tunsubs_sweep(firc_tunsubs_t *s, const firc_tunnels_t *all);

/* Queues every pair whose interval has passed and that is neither queued nor fetching. */
void firc_tunsubs_due(firc_tunsubs_t *s);

/* Queues every subscription of tunnel_id now; FIRC_ERR_NOENT when it has none wanted. */
firc_err_t firc_tunsubs_refresh(firc_tunsubs_t *s, const char *tunnel_id);

/* The held bodies of the wanted pairs; valid until the next call into s. */
const firc_tun_body_t *firc_tunsubs_bodies(const firc_tunsubs_t *s, size_t *n);

/* The held bodies of t's subscriptions as fetched through uplink_of's uplink and with the headers of uplink_of's
   source of the same id, enabled or not, each with mark 0 and the hsig of t's source, at most cap of them; for
   display only, valid until the next call into s. */
size_t firc_tunsubs_held(const firc_tunsubs_t *s, const firc_tunnel_t *uplink_of, const firc_tunnel_t *t,
                         firc_tun_body_t *out, size_t cap);

/* Fills out for the subscription url of a tunnel fetching with mark under the headers signature hsig; returns 1,
   or 0 when none is wanted. */
size_t firc_tunsubs_state(const firc_tunsubs_t *s, const char *url, uint32_t mark, uint64_t hsig,
                          firc_tun_sub_state_t *out);

/* The headers every subscription starts from; every pair's next fetch sends the new set, its key unchanged. */
void firc_tunsubs_set_defaults(firc_tunsubs_t *s, const firc_subhdr_set_t *defaults);

/* The headers every subscription starts from; NULL for a NULL s. */
const firc_subhdr_set_t *firc_tunsubs_defaults(const firc_tunsubs_t *s);

void firc_tunsubs_advance_for_test(firc_tunsubs_t *s, int64_t ms); /* test seam: moves its clock forward */

/* Joins the fetch thread; results still on their way to the loop are dropped. NULL-safe. */
void firc_tunsubs_free(firc_tunsubs_t *s);

#endif
