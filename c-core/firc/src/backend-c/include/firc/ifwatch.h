#ifndef FIRC_IFWATCH_H
#define FIRC_IFWATCH_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/id.h"
#include "firc/ifcheck.h"
#include "firc/ifchoose.h"
#include "firc/loop.h"
#include "firc/models.h"
#include "firc/rtnl.h"

#define FIRC_IFWATCH_MAX_IFS 16
#define FIRC_IFWATCH_TICK_MS 1000
#define FIRC_IFWATCH_LOST_MS 120000
#define FIRC_IFWATCH_MOVE_ERROR_MS 60000
#define FIRC_IFWATCH_TUNNEL_START_MS 30000

typedef struct firc_ifwatch_deps {
    firc_loop_t *loop;
    firc_rtnl_t *rtnl;
    uint32_t start_table;
    size_t (*n_groups)(void *ud);
    const firc_group_t *(*group_at)(void *ud, size_t i);
    firc_err_t (*move)(void *ud, firc_id_t id, size_t to, bool blocked);
    bool (*link_up)(void *ud, const char *name);
    bool (*tunnel_known)(void *ud, const char *dev);
    bool (*tunnel_down)(void *ud, const char *dev);
    bool (*tunnel_starting)(void *ud, const char *dev, int64_t *since_ms);
    firc_err_t (*queue)(void *ud, const char *name, uint32_t mark, uint32_t gen);
    void *ud;
    int64_t (*now_ms)(void);
} firc_ifwatch_deps_t;

typedef struct firc_ifwatch firc_ifwatch_t;

/* Watches the interfaces of every list of two or more; d copied, every callback on the loop thread. */
firc_ifwatch_t *firc_ifwatch_new(const firc_ifwatch_deps_t *d);

/* Takes the check routes back; NULL-safe. */
void firc_ifwatch_free(firc_ifwatch_t *w);

/* A tick every FIRC_IFWATCH_TICK_MS on d.loop. */
firc_err_t firc_ifwatch_start(firc_ifwatch_t *w);

/* Takes the check interval from a; a shorter one brings due checks forward. */
void firc_ifwatch_configure(firc_ifwatch_t *w, const firc_app_config_t *a);

/* What the timer does: follow the groups and tunnels, queue due checks, choose. */
void firc_ifwatch_tick(firc_ifwatch_t *w);

/* A round of check results; guarded failures conclude nothing. */
void firc_ifwatch_results(firc_ifwatch_t *w, const firc_ifcheck_result_t *r, size_t n);

/* A link event; down is dead at once, up brings a regular check forward, at most once per interval. */
void firc_ifwatch_link(firc_ifwatch_t *w, const char *name, bool up);

/* Something changed on name outside the kernel (an NDM layer event): its regular check comes forward, at most once
 * per interval; an unknown name is left alone. */
void firc_ifwatch_nudge(firc_ifwatch_t *w, const char *name);

/* Reads the links again (NULL: every one) and writes their check routes again. */
void firc_ifwatch_refresh(firc_ifwatch_t *w, const char *name);

/* Where a group being enabled starts; remembered as its active entry. */
firc_ifchoice_t firc_ifwatch_pick(firc_ifwatch_t *w, const firc_group_t *g);

/* False when name is in no list of two or more. */
bool firc_ifwatch_view(firc_ifwatch_t *w, const char *name, firc_if_view_t *out);

/* The button: NOENT unknown group, STATE first entry not alive, AGAIN the group is not routed (the entry is kept
 * for its next enable), or the move's own error. */
firc_err_t firc_ifwatch_move_up(firc_ifwatch_t *w, firc_id_t id);

#endif
