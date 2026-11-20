#include "firc/tunwatch.h"

#include <string.h>

#include "firc/log.h"
#include "firc/logrep.h"

static long long minutes(int64_t ms)
{
    return (long long)(ms / 60000);
}

void firc_tunwatch_see(firc_tunwatch_t *w, const char *device, firc_tun_status_t st, int64_t now_ms)
{
    if (st == FIRC_TUN_ST_OFF) {
        memset(w, 0, sizeof *w);
        return;
    }
    if (st == FIRC_TUN_ST_UP) {
        if (w->alarmed) {
            firc_log_from(FIRC_LOG_INFO, device, "recovered: a node works again after %lld min without one",
                          minutes(now_ms - w->bad_since));
        }
        memset(w, 0, sizeof *w);
        return;
    }
    if (!w->bad) {
        w->bad = true;
        w->bad_since = now_ms;
        return;
    }
    int64_t down = now_ms - w->bad_since;
    bool due = w->alarmed ? now_ms - w->said >= (int64_t)FIRC_LOGREP_REMIND_MS : down >= FIRC_TUNWATCH_AFTER_MS;
    if (!due) {
        return;
    }
    const char *what = st == FIRC_TUN_ST_BAD_CONFIG    ? "its configuration has been refused"
                       : st == FIRC_TUN_ST_UPLINK_DOWN ? "its uplink has been down"
                       : st == FIRC_TUN_ST_WAITING     ? "it has had no nodes to use"
                       : st == FIRC_TUN_ST_ROUTER      ? "its router interface has not been ready"
                                                       : "no node has worked";
    firc_log_from(FIRC_LOG_ERROR, device, "%s for %lld min; groups on it fail closed", what, minutes(down));
    w->alarmed = true;
    w->said = now_ms;
}
