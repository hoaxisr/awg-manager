#include "firc/ifwatch.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/checkroute.h"
#include "firc/duration.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/mark.h"

typedef struct watched_if {
    char name[16];
    firc_if_track_t track;
    int64_t since_unix;
    bool tunnel;
    bool seen;
    bool needs_global;
    bool needs_route;
    bool in_flight;
    bool routed;
    uint32_t gen;
    bool pulled;
    int64_t pulled_ms;
    bool nudged;
    int64_t nudged_ms;
    int64_t due_ms;
    firc_checkroute_t route;
    firc_logrep_t said;
    firc_logrep_t route_failed;
} watched_if_t;

typedef struct watched_group_cand {
    firc_if_track_t track;
    int64_t since_unix;
    uint32_t fail_streak;
    uint32_t pass_streak;
    bool last_link_ok;
    bool init_link;
} watched_group_cand_t;

typedef struct watched_group_sent_cand {
    char iface[16];
    uint32_t mark;
    char dns_server[64];
    uint16_t dns_port;
    uint32_t dns_mark;
    bool skip;
} watched_group_sent_cand_t;

typedef struct watched_group {
    firc_id_t id;
    char name[64];
    char names[FIRC_GROUP_IFACES_MAX][16];
    size_t n;
    size_t active;
    bool blocked;
    bool seen;
    firc_group_check_mode_t check_mode;
    char check_url[513];
    int64_t interval_ms;
    bool custom_interval;
    uint32_t timeout_ms;
    uint32_t fail_threshold;
    uint32_t recover_threshold;
    uint32_t probe_gen;
    bool check_in_flight;
    bool has_result;           /* at least one probe result was applied since the last reset */
    bool confirm_pending;      /* the next probe is a 3 s confirm (failure or recovery streak) */
    bool recheck_after_flight; /* a link came up while a probe was in flight: probe again on its result */
    int64_t check_due_ms;
    int64_t check_sent_ms;
    firc_id_t leader_id;
    uint32_t leader_gen;
    watched_group_sent_cand_t sent_cands[FIRC_GROUP_IFACES_MAX];
    size_t n_sent_cands;
    watched_group_cand_t cand[FIRC_GROUP_IFACES_MAX];
    firc_logrep_t all_dead;
    firc_logrep_t move_failed;
} watched_group_t;

struct firc_ifwatch {
    firc_ifwatch_deps_t d;
    watched_if_t ifs[FIRC_IFWATCH_MAX_IFS];
    size_t n_ifs;
    watched_group_t groups[FIRC_MARK_MAX_GROUPS];
    size_t n_groups;
    int64_t interval_ms;
    int timer;
    uint32_t next_gen;
    firc_logrep_t full;
    firc_logrep_t guard;
};

static const firc_if_track_t k_unknown = {FIRC_IF_UNKNOWN, 0, -1, false, ""};
static const char k_all_dead[] = "every interface of its list is dead; its traffic goes nowhere until one is alive";

static int64_t now_ms(const firc_ifwatch_t *w) {
    if (w->d.now_ms != NULL) { return w->d.now_ms(); }
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static watched_if_t *find_if(firc_ifwatch_t *w, const char *name) {
    for (size_t i = 0; i < w->n_ifs; i++) {
        if (strcmp(w->ifs[i].name, name) == 0) { return &w->ifs[i]; }
    }
    return NULL;
}

static watched_group_t *find_group(firc_ifwatch_t *w, firc_id_t id) {
    for (size_t i = 0; i < w->n_groups; i++) {
        if (firc_id_equal(w->groups[i].id, id)) { return &w->groups[i]; }
    }
    return NULL;
}

static void say_state(watched_if_t *wi, firc_if_step_t s, bool counted) {
    if (s == FIRC_IF_CHANGED) {
        wi->since_unix = (int64_t)time(NULL);
        if (wi->track.state == FIRC_IF_DEAD) {
            firc_logrep_fail(&wi->said, FIRC_LOG_WARN, NULL, "interface %s is dead: %s", wi->name, wi->track.error);
        } else if (wi->track.state == FIRC_IF_ALIVE && !firc_logrep_ok(&wi->said, NULL, "interface %s", wi->name)) {
            FIRC_INFO("interface %s is alive", wi->name);
        }
        return;
    }
    if (wi->track.state == FIRC_IF_DEAD && (counted || firc_logrep_due(&wi->said, UINT64_MAX))) {
        firc_logrep_fail_for_quiet(&wi->said, UINT64_MAX, NULL, "interface %s is dead: %s", wi->name,
                                   wi->track.error);
    }
}

static bool tunnel_known(firc_ifwatch_t *w, const watched_if_t *wi) {
    return w->d.tunnel_known != NULL && w->d.tunnel_known(w->d.ud, wi->name);
}

static bool tunnel_still_starting(firc_ifwatch_t *w, const watched_if_t *wi, int64_t now) {
    int64_t since = 0;
    return wi->tunnel && w->d.tunnel_starting != NULL &&
           w->d.tunnel_starting(w->d.ud, wi->name, &since) && now - since < FIRC_IFWATCH_TUNNEL_START_MS;
}

static void tunnel_step(firc_ifwatch_t *w, watched_if_t *wi, int64_t now) {
    bool link = w->d.link_up == NULL || w->d.link_up(w->d.ud, wi->name);
    if (tunnel_still_starting(w, wi, now)) {
        if (wi->track.state != FIRC_IF_UNKNOWN) {
            firc_if_track_init(&wi->track, now);
            say_state(wi, FIRC_IF_CHANGED, false);
        }
        return;
    }
    bool up = link && (w->d.tunnel_down == NULL || !w->d.tunnel_down(w->d.ud, wi->name));
    say_state(wi, firc_if_track_tunnel(&wi->track, up, now), false);
}

static void classify(firc_ifwatch_t *w, watched_if_t *wi, int64_t now) {
    wi->tunnel = tunnel_known(w, wi);
    wi->gen = ++w->next_gen;
    wi->in_flight = false;
    wi->due_ms = now;
    if (wi->tunnel) {
        tunnel_step(w, wi, now);
    } else if (w->d.link_up != NULL && !w->d.link_up(w->d.ud, wi->name)) {
        say_state(wi, firc_if_track_link(&wi->track, false, now), true);
    }
}

static watched_if_t *add_if(firc_ifwatch_t *w, const char *name, int64_t now) {
    if (w->n_ifs == FIRC_IFWATCH_MAX_IFS) {
        firc_logrep_fail_for_quiet(&w->full, 0, NULL, "interface lists name more than %d interfaces; %s is not checked",
                                   FIRC_IFWATCH_MAX_IFS, name);
        return NULL;
    }
    watched_if_t *wi = &w->ifs[w->n_ifs++];
    memset(wi, 0, sizeof(*wi));
    snprintf(wi->name, sizeof(wi->name), "%s", name);
    firc_if_track_init(&wi->track, now);
    wi->since_unix = (int64_t)time(NULL);
    classify(w, wi, now);
    return wi;
}

static bool ensure_route(firc_ifwatch_t *w, watched_if_t *wi) {
    if (wi->routed || w->d.rtnl == NULL) { return true; }
    firc_err_t err = firc_checkroute_apply(w->d.rtnl, w->d.start_table, wi->name, &wi->route);
    wi->routed = err == FIRC_OK;
    if (wi->routed) {
        firc_logrep_reset(&wi->route_failed);
    } else if (err == FIRC_ERR_LIMIT) {
        firc_logrep_fail_for(&wi->route_failed, 0, NULL,
                             "interface %s cannot be checked: no mark field or table is left (%zu interfaces are checked)",
                             wi->name, w->n_ifs);
    } else {
        firc_logrep_fail(&wi->route_failed, FIRC_LOG_WARN, NULL, "interface %s cannot be checked: its check route: %s",
                         wi->name, firc_err_str(err));
    }
    return wi->routed;
}

static int64_t effective_group_interval_ms(const firc_ifwatch_t *w, uint32_t interval_sec) {
    if (interval_sec > 0) { return (int64_t)interval_sec * 1000; }
    if (w != NULL && w->interval_ms > 0) { return w->interval_ms; }
    return (int64_t)FIRC_GROUP_CHECK_DEFAULT_INTERVAL_SEC * 1000;
}

static watched_group_t *group_rec(firc_ifwatch_t *w, const firc_group_t *g, int64_t now) {
    watched_group_t *wg = find_group(w, g->id);
    bool is_new = (wg == NULL);
    if (wg == NULL) {
        if (w->n_groups == FIRC_MARK_MAX_GROUPS) { return NULL; }
        wg = &w->groups[w->n_groups++];
        memset(wg, 0, sizeof(*wg));
        wg->id = g->id;
    }
    snprintf(wg->name, sizeof(wg->name), "%s", g->name != NULL ? g->name : "");
    size_t k = firc_group_n_ifaces(g);
    bool same_ifaces = !is_new && wg->n == k;
    for (size_t e = 0; same_ifaces && e < k; e++) {
        same_ifaces = strcmp(wg->names[e], firc_group_iface_at(g, e)) == 0;
    }
    const char *url = g->check.url != NULL ? g->check.url : "";
    bool custom_intv = g->check.interval_sec > 0;
    int64_t intv_ms = effective_group_interval_ms(w, g->check.interval_sec);
    uint32_t timeout_ms = g->check.timeout_ms > 0 ? g->check.timeout_ms : FIRC_GROUP_CHECK_DEFAULT_TIMEOUT_MS;
    uint32_t fail_thr = g->check.fail_threshold > 0 ? g->check.fail_threshold : FIRC_GROUP_CHECK_DEFAULT_FAIL_THRESHOLD;
    uint32_t rec_thr = g->check.recover_threshold > 0 ? g->check.recover_threshold
                                                      : FIRC_GROUP_CHECK_DEFAULT_RECOVER_THRESHOLD;
    bool same_check = !is_new && wg->check_mode == g->check.mode && strcmp(wg->check_url, url) == 0 &&
                      wg->timeout_ms == timeout_ms && wg->fail_threshold == fail_thr &&
                      wg->recover_threshold == rec_thr;

    if (!same_ifaces) {
        char keep[16] = "";
        if (wg->n > 0) { snprintf(keep, sizeof(keep), "%s", wg->names[wg->active]); }
        wg->n = k;
        wg->active = 0;
        wg->blocked = false;
        for (size_t e = 0; e < k; e++) {
            snprintf(wg->names[e], sizeof(wg->names[e]), "%s", firc_group_iface_at(g, e));
            if (keep[0] != '\0' && strcmp(wg->names[e], keep) == 0) { wg->active = e; }
        }
    }
    if (!same_ifaces || !same_check) {
        wg->check_mode = g->check.mode;
        snprintf(wg->check_url, sizeof(wg->check_url), "%s", url);
        wg->interval_ms = intv_ms;
        wg->custom_interval = custom_intv;
        wg->timeout_ms = timeout_ms;
        wg->fail_threshold = fail_thr;
        wg->recover_threshold = rec_thr;
        if (++wg->probe_gen == 0) { wg->probe_gen = 1; }
        wg->check_in_flight = false;
        wg->leader_id = (firc_id_t){{0}};
        wg->leader_gen = 0;
        wg->n_sent_cands = 0;
        wg->has_result = false;
        wg->confirm_pending = false;
        wg->recheck_after_flight = false;
        wg->check_due_ms = now;
        int64_t unix_now = (int64_t)time(NULL);
        for (size_t e = 0; e < k; e++) {
            firc_if_track_init(&wg->cand[e].track, now);
            wg->cand[e].since_unix = unix_now;
            wg->cand[e].fail_streak = 0;
            wg->cand[e].pass_streak = 0;
            wg->cand[e].last_link_ok = false;
            wg->cand[e].init_link = false;
        }
    } else {
        wg->custom_interval = custom_intv;
        if (wg->interval_ms != intv_ms) {
            wg->interval_ms = intv_ms;
            if (!wg->check_in_flight && wg->check_due_ms > now + wg->interval_ms) {
                wg->check_due_ms = now + wg->interval_ms;
            }
        }
    }
    return wg;
}

static void tracks_of(firc_ifwatch_t *w, const watched_group_t *wg, int64_t now, const firc_if_track_t **e) {
    for (size_t i = 0; i < wg->n; i++) {
        watched_if_t *wi = find_if(w, wg->names[i]);
        if (wi == NULL) { wi = add_if(w, wg->names[i], now); }
        if (wi != NULL) {
            wi->seen = true;
            if (wg->check_mode == FIRC_GROUP_CHECK_NONE) {
                wi->needs_global = true;
                if (!wi->tunnel) { wi->needs_route = true; }
            }
        }
        e[i] = wi != NULL ? &wi->track : &k_unknown;
    }
}

static void group_tracks_of(firc_ifwatch_t *w, const watched_group_t *wg, int64_t now, const firc_if_track_t **e) {
    if (wg->check_mode == FIRC_GROUP_CHECK_NONE) {
        tracks_of(w, wg, now, e);
        return;
    }
    for (size_t i = 0; i < wg->n; i++) { e[i] = &wg->cand[i].track; }
}

static void reconcile(firc_ifwatch_t *w, int64_t now) {
    for (size_t i = 0; i < w->n_ifs; i++) {
        w->ifs[i].seen = false;
        w->ifs[i].needs_global = false;
        w->ifs[i].needs_route = false;
    }
    for (size_t i = 0; i < w->n_groups; i++) { w->groups[i].seen = false; }
    size_t n = w->d.n_groups != NULL ? w->d.n_groups(w->d.ud) : 0;
    for (size_t i = 0; i < n; i++) {
        const firc_group_t *g = w->d.group_at(w->d.ud, i);
        if (g == NULL || !g->enable || firc_group_n_ifaces(g) < 2) { continue; }
        watched_group_t *wg = group_rec(w, g, now);
        if (wg == NULL) { continue; }
        wg->seen = true;
        for (size_t e = 0; e < wg->n; e++) {
            watched_if_t *wi = find_if(w, wg->names[e]);
            if (wi != NULL) { wi->seen = true; }
        }
    }
    size_t kept = 0;
    for (size_t i = 0; i < w->n_groups; i++) {
        if (w->groups[i].seen) { w->groups[kept++] = w->groups[i]; }
    }
    w->n_groups = kept;
    kept = 0;
    for (size_t i = 0; i < w->n_ifs; i++) {
        if (w->ifs[i].seen) {
            w->ifs[kept++] = w->ifs[i];
        } else if (w->ifs[i].routed && w->d.rtnl != NULL) {
            (void)firc_checkroute_remove(w->d.rtnl, &w->ifs[i].route);
        }
    }
    w->n_ifs = kept;
    if (w->n_ifs < FIRC_IFWATCH_MAX_IFS) { firc_logrep_reset(&w->full); }
    for (size_t i = 0; i < w->n_groups; i++) {
        const firc_if_track_t *e[FIRC_GROUP_IFACES_MAX];
        tracks_of(w, &w->groups[i], now, e);
        for (size_t j = 0; j < w->groups[i].n; j++) {
            watched_if_t *wi = find_if(w, w->groups[i].names[j]);
            if (wi == NULL) { continue; }
            if (w->groups[i].check_mode == FIRC_GROUP_CHECK_NONE) {
                wi->needs_global = true;
                if (!wi->tunnel) { wi->needs_route = true; }
            } else {
                wi->needs_route = true;
            }
        }
    }
    for (size_t i = 0; i < w->n_ifs; i++) {
        if (w->ifs[i].routed && !w->ifs[i].needs_route && w->d.rtnl != NULL) {
            (void)firc_checkroute_remove(w->d.rtnl, &w->ifs[i].route);
            w->ifs[i].routed = false;
        }
    }
}

static int64_t spread_ms(const firc_ifwatch_t *w, const watched_if_t *wi) {
    int64_t slot = (int64_t)(wi - w->ifs) * 1000;
    return w->interval_ms > 0 ? slot % w->interval_ms : 0;
}

static bool end_all_dead(watched_group_t *wg) {
    if (wg->all_dead.count == 0) { return false; }
    firc_logrep_reset(&wg->all_dead);
    FIRC_INFO("group %s is back on %s", wg->name, wg->names[wg->active]);
    return true;
}

static firc_err_t apply(firc_ifwatch_t *w, watched_group_t *wg, firc_ifchoice_t c) {
    size_t from = wg->active;
    firc_err_t err = w->d.move != NULL ? w->d.move(w->d.ud, wg->id, c.active, c.all_dead) : FIRC_OK;
    if (err != FIRC_OK && err != FIRC_ERR_NOENT) {
        firc_logrep_fail_for(&wg->move_failed, FIRC_IFWATCH_MOVE_ERROR_MS, NULL, "group %s: the move to %s failed: %s",
                             wg->name, wg->names[c.active], firc_err_str(err));
        return err;
    }
    firc_logrep_reset(&wg->move_failed);
    wg->active = c.active;
    wg->blocked = c.all_dead;
    firc_err_t done = err == FIRC_ERR_NOENT ? FIRC_ERR_AGAIN : FIRC_OK;
    if (c.all_dead) {
        firc_logrep_fail_for(&wg->all_dead, FIRC_IFCHOOSE_ALL_DEAD_ERROR_MS, NULL, "group %s: %s", wg->name, k_all_dead);
        return done;
    }
    if (done != FIRC_OK) {
        firc_logrep_reset(&wg->all_dead);
        return done;
    }
    if (end_all_dead(wg)) { return FIRC_OK; }
    const watched_if_t *wi = wg->check_mode == FIRC_GROUP_CHECK_NONE ? find_if(w, wg->names[from]) : NULL;
    const firc_if_track_t *ft =
        wg->check_mode != FIRC_GROUP_CHECK_NONE ? &wg->cand[from].track : (wi != NULL ? &wi->track : NULL);
    if (c.active > from || (c.active != from && ft != NULL && ft->state == FIRC_IF_DEAD)) {
        FIRC_WARN("group %s moved from %s to %s: %s", wg->name, wg->names[from], wg->names[c.active],
                  ft != NULL ? ft->error : "");
    } else if (c.active < from) {
        FIRC_INFO("group %s moved back from %s to %s", wg->name, wg->names[from], wg->names[c.active]);
    }
    return FIRC_OK;
}

static bool is_cand_link_ok(firc_ifwatch_t *w, const char *name, const watched_if_t *wi, int64_t now,
                            const char **why);

static firc_ifchoice_t choose_group_checked(firc_ifwatch_t *w, const watched_group_t *wg,
                                            const firc_if_track_t *const *e, int64_t now) {
    size_t cur = wg->active < wg->n ? wg->active : 0;
    firc_ifchoice_t c = {cur, false};
    if (wg->n == 0) { return c; }
    size_t first_alive = wg->n, first_unknown = wg->n;
    for (size_t i = 0; i < wg->n; i++) {
        if (e[i]->state == FIRC_IF_ALIVE && first_alive == wg->n) { first_alive = i; }
        if (e[i]->state == FIRC_IF_UNKNOWN && first_unknown == wg->n) { first_unknown = i; }
    }
    if (first_alive == wg->n && first_unknown == wg->n) {
        if (wg->check_mode == FIRC_GROUP_CHECK_KEENDNS && w != NULL) {
            /* Fail-open for KeenDNS Cloud: never blackhole traffic if any candidate link/tunnel is healthy. */
            if (is_cand_link_ok(w, wg->names[cur], find_if(w, wg->names[cur]), now, NULL)) {
                c.active = cur;
                c.all_dead = false;
                return c;
            }
            for (size_t i = 0; i < wg->n; i++) {
                if (is_cand_link_ok(w, wg->names[i], find_if(w, wg->names[i]), now, NULL)) {
                    c.active = i;
                    c.all_dead = false;
                    return c;
                }
            }
        }
        c.all_dead = true;
        return c;
    }
    if (e[cur]->state == FIRC_IF_DEAD) {
        c.active = first_alive < wg->n ? first_alive : first_unknown;
        return c;
    }
    if (wg->blocked) {
        if (first_alive < wg->n) { c.active = first_alive; }
        return c;
    }
    for (size_t i = 0; i < cur; i++) {
        if (e[i]->state == FIRC_IF_ALIVE) {
            c.active = i;
            break;
        }
    }
    return c;
}

static firc_ifchoice_t choose(firc_ifwatch_t *w, const watched_group_t *wg, const firc_if_track_t *const *e,
                              int64_t now) {
    if (wg->check_mode != FIRC_GROUP_CHECK_NONE) { return choose_group_checked(w, wg, e, now); }
    firc_ifchoice_t c = firc_ifchoose(e, wg->n, wg->active, now);
    if (!wg->blocked || c.all_dead) { return c; }
    for (size_t i = 0; i < wg->n; i++) {
        if (e[i]->state == FIRC_IF_ALIVE) { return c; }
    }
    return (firc_ifchoice_t){wg->active, true};
}

static void choose_all(firc_ifwatch_t *w, int64_t now) {
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        const firc_if_track_t *e[FIRC_GROUP_IFACES_MAX];
        group_tracks_of(w, wg, now, e);
        firc_ifchoice_t c = choose(w, wg, e, now);
        if (c.active != wg->active || c.all_dead != wg->blocked) {
            (void)apply(w, wg, c);
        } else if (c.all_dead && firc_logrep_due(&wg->all_dead, FIRC_IFCHOOSE_ALL_DEAD_ERROR_MS)) {
            firc_logrep_fail_for(&wg->all_dead, FIRC_IFCHOOSE_ALL_DEAD_ERROR_MS, NULL, "group %s: %s", wg->name,
                                 k_all_dead);
        }
    }
}

firc_ifwatch_t *firc_ifwatch_new(const firc_ifwatch_deps_t *d) {
    firc_ifwatch_t *w = calloc(1, sizeof(*w));
    if (w == NULL) { return NULL; }
    w->d = *d;
    w->interval_ms = 30000;
    return w;
}

static void on_tick(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_ifwatch_tick(ud);
}

firc_err_t firc_ifwatch_start(firc_ifwatch_t *w) {
    if (w->d.loop == NULL || w->timer != 0) { return FIRC_ERR_STATE; }
    return firc_loop_add_timer(w->d.loop, FIRC_IFWATCH_TICK_MS, FIRC_IFWATCH_TICK_MS, on_tick, w, &w->timer);
}

void firc_ifwatch_free(firc_ifwatch_t *w) {
    if (w == NULL) { return; }
    if (w->timer != 0 && w->d.loop != NULL) { (void)firc_loop_del_timer(w->d.loop, w->timer); }
    for (size_t i = 0; i < w->n_ifs; i++) {
        if (w->ifs[i].routed && w->d.rtnl != NULL) { (void)firc_checkroute_remove(w->d.rtnl, &w->ifs[i].route); }
    }
    free(w);
}

void firc_ifwatch_configure(firc_ifwatch_t *w, const firc_app_config_t *a) {
    int64_t was = w->interval_ms;
    w->interval_ms = a->check.interval / FIRC_DURATION_MS;
    if (w->interval_ms <= 0) { w->interval_ms = 30000; }
    if (was == w->interval_ms) { return; }
    int64_t now = now_ms(w);
    for (size_t i = 0; i < w->n_ifs; i++) {
        watched_if_t *wi = &w->ifs[i];
        if (!wi->in_flight && wi->due_ms > now + w->interval_ms) { wi->due_ms = now + w->interval_ms; }
    }
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE || wg->custom_interval) { continue; }
        wg->interval_ms = w->interval_ms;
        if (!wg->check_in_flight && wg->check_due_ms > now + wg->interval_ms) {
            wg->check_due_ms = now + wg->interval_ms;
        }
    }
}

static bool is_cand_link_ok(firc_ifwatch_t *w, const char *name, const watched_if_t *wi, int64_t now,
                            const char **why) {
    if (w->d.link_up != NULL && !w->d.link_up(w->d.ud, name)) {
        if (why != NULL) { *why = "the link is down or gone"; }
        return false;
    }
    if (wi != NULL && wi->tunnel && !tunnel_still_starting(w, wi, now) &&
        w->d.tunnel_down != NULL && w->d.tunnel_down(w->d.ud, name)) {
        if (why != NULL) { *why = "the tunnel carries no traffic"; }
        return false;
    }
    return true;
}

static void cand_mark_dead(watched_group_t *wg, size_t idx, const char *why, int64_t now) {
    watched_group_cand_t *c = &wg->cand[idx];
    c->pass_streak = 0;
    c->fail_streak = wg->fail_threshold;
    c->track.suspect = false;
    c->track.clean_since_ms = -1;
    snprintf(c->track.error, sizeof(c->track.error), "%s", why != NULL ? why : "the link is down or gone");
    if (c->track.state != FIRC_IF_DEAD) {
        c->track.state = FIRC_IF_DEAD;
        c->track.since_ms = now;
        c->since_unix = (int64_t)time(NULL);
    }
}

static const firc_group_t *find_group_spec(const firc_ifwatch_t *w, firc_id_t id) {
    size_t n = w->d.n_groups != NULL ? w->d.n_groups(w->d.ud) : 0;
    for (size_t i = 0; i < n; i++) {
        const firc_group_t *g = w->d.group_at != NULL ? w->d.group_at(w->d.ud, i) : NULL;
        if (g != NULL && firc_id_equal(g->id, id)) { return g; }
    }
    return NULL;
}

static void sync_group_cand_links(firc_ifwatch_t *w, watched_group_t *wg, int64_t now) {
    for (size_t e = 0; e < wg->n; e++) {
        watched_if_t *wi = find_if(w, wg->names[e]);
        const char *why = NULL;
        bool ok = is_cand_link_ok(w, wg->names[e], wi, now, &why);
        watched_group_cand_t *c = &wg->cand[e];
        if (!c->init_link) {
            c->init_link = true;
            c->last_link_ok = ok;
        } else if (ok && !c->last_link_ok) {
            c->last_link_ok = true;
            if (wi != NULL && wi->routed && w->d.rtnl != NULL) {
                (void)firc_checkroute_refresh(w->d.rtnl, &wi->route);
            }
            if (wg->check_in_flight) {
                wg->recheck_after_flight = true;
            } else if (wg->check_due_ms > now) {
                wg->check_due_ms = now;
            }
        } else {
            c->last_link_ok = ok;
        }
        if (!ok) { cand_mark_dead(wg, e, why, now); }
    }
}

static void build_group_check_job(firc_ifwatch_t *w, const watched_group_t *wg, int64_t now,
                                  firc_ifcheck_group_job_t *job) {
    memset(job, 0, sizeof(*job));
    job->group_id = wg->id;
    job->gen = wg->probe_gen;
    job->mode = wg->check_mode;
    snprintf(job->url, sizeof(job->url), "%s", wg->check_url);
    job->timeout_ms = wg->timeout_ms;
    job->n_cands = wg->n;
    const firc_group_t *g = find_group_spec(w, wg->id);
    for (size_t e = 0; e < wg->n; e++) {
        firc_ifcheck_group_cand_t *jc = &job->cands[e];
        snprintf(jc->iface, sizeof(jc->iface), "%s", wg->names[e]);
        watched_if_t *wi = find_if(w, wg->names[e]);
        const char *why = NULL;
        if (!is_cand_link_ok(w, wg->names[e], wi, now, &why)) {
            jc->skip = true;
            snprintf(jc->skip_error, sizeof(jc->skip_error), "%s", why ? why : "the link is down or gone");
            continue;
        }
        if (wi == NULL || !ensure_route(w, wi) || (w->d.rtnl != NULL && wi->route.mark == 0)) {
            jc->skip = true;
            snprintf(jc->skip_error, sizeof(jc->skip_error), "no check route for interface");
            continue;
        }
        jc->mark = wi->route.mark;
        if (w->d.resolve_for != NULL) {
            char dsrv[64] = "";
            uint16_t dport = 0;
            bool mark_dns = false;
            if (w->d.resolve_for(w->d.ud, g, wi->name, wi->tunnel, dsrv, sizeof(dsrv), &dport, &mark_dns)) {
                snprintf(jc->dns_server, sizeof(jc->dns_server), "%s", dsrv);
                jc->dns_port = dport;
                jc->dns_mark = mark_dns ? jc->mark : 0;
            }
        }
    }
}

static void record_sent_cands(watched_group_t *wg, const firc_ifcheck_group_job_t *job) {
    size_t n = job->n_cands <= FIRC_GROUP_IFACES_MAX ? job->n_cands : FIRC_GROUP_IFACES_MAX;
    wg->n_sent_cands = n;
    for (size_t e = 0; e < n; e++) {
        const firc_ifcheck_group_cand_t *jc = &job->cands[e];
        watched_group_sent_cand_t *sc = &wg->sent_cands[e];
        memset(sc, 0, sizeof(*sc));
        snprintf(sc->iface, sizeof(sc->iface), "%s", jc->iface);
        sc->mark = jc->mark;
        snprintf(sc->dns_server, sizeof(sc->dns_server), "%s", jc->dns_server);
        sc->dns_port = jc->dns_port;
        sc->dns_mark = jc->dns_mark;
        sc->skip = jc->skip;
    }
}

static bool sent_cand_matches(const watched_group_sent_cand_t *sc, const firc_ifcheck_group_cand_t *jc) {
    if (strcmp(sc->iface, jc->iface) != 0 || sc->skip != jc->skip) { return false; }
    if (jc->skip) { return true; }
    return sc->mark == jc->mark && sc->dns_port == jc->dns_port && sc->dns_mark == jc->dns_mark &&
           strcmp(sc->dns_server, jc->dns_server) == 0;
}

static bool group_job_compatible(const watched_group_t *peer, const firc_ifcheck_group_job_t *job) {
    if (peer->n_sent_cands == 0 || peer->check_mode != job->mode || peer->timeout_ms != job->timeout_ms ||
        strcmp(peer->check_url, job->url) != 0) {
        return false;
    }
    for (size_t e = 0; e < job->n_cands; e++) {
        bool covered = false;
        for (size_t k = 0; k < peer->n_sent_cands; k++) {
            if (sent_cand_matches(&peer->sent_cands[k], &job->cands[e])) {
                covered = true;
                break;
            }
        }
        if (!covered) { return false; }
    }
    return true;
}

/* A leader waiting to recheck went out before a link came up: its result is stale for a newcomer. */
static bool leader_covers_job(const watched_group_t *ld, const firc_ifcheck_group_job_t *job) {
    if (!ld->check_in_flight || ld->recheck_after_flight || !firc_id_equal(ld->leader_id, ld->id) ||
        ld->leader_gen != ld->probe_gen) {
        return false;
    }
    return group_job_compatible(ld, job);
}

/* Cheap pre-filter, before a job (netlink, routes, resolvers) is built for wg. */
static bool may_share_probe(const watched_group_t *wg, const watched_group_t *peer) {
    return peer != wg && peer->check_mode == wg->check_mode && peer->timeout_ms == wg->timeout_ms &&
           peer->interval_ms == wg->interval_ms && peer->n_sent_cands >= wg->n &&
           strcmp(peer->check_url, wg->check_url) == 0;
}

static watched_group_t *find_covering_leader(firc_ifwatch_t *w, const watched_group_t *self,
                                             const firc_ifcheck_group_job_t *job) {
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *ld = &w->groups[i];
        if (ld != self && leader_covers_job(ld, job)) { return ld; }
    }
    return NULL;
}

static watched_group_t *find_phase_leader(firc_ifwatch_t *w, const watched_group_t *wg,
                                          const firc_ifcheck_group_job_t *job, int64_t now) {
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *ld = &w->groups[i];
        if (ld != wg && ld->check_sent_ms == now && ld->interval_ms == wg->interval_ms && leader_covers_job(ld, job)) {
            return ld;
        }
    }
    return NULL;
}

static bool has_phase_leader(const firc_ifwatch_t *w, const watched_group_t *wg, int64_t now) {
    for (size_t i = 0; i < w->n_groups; i++) {
        const watched_group_t *ld = &w->groups[i];
        if (ld->check_in_flight && ld->check_sent_ms == now && firc_id_equal(ld->leader_id, ld->id) &&
            may_share_probe(wg, ld)) {
            return true;
        }
    }
    return false;
}

static bool has_iface(const watched_group_t *wg, const char *name) {
    for (size_t k = 0; k < wg->n; k++) {
        if (strcmp(wg->names[k], name) == 0) { return true; }
    }
    return false;
}

/* Every interface of `small` is in `big`. */
static bool ifaces_within(const watched_group_t *small, const watched_group_t *big) {
    for (size_t e = 0; e < small->n; e++) {
        if (!has_iface(big, small->names[e])) { return false; }
    }
    return true;
}

/* A group with no result yet (new rule, edited check, restart) takes the candidate state and schedule of an idle
 * group with the very same candidates, so it joins that group's phase instead of probing out of phase. Only an
 * identical set: a wider group's DEAD can rest on a peer this group does not have (its own guard would hold). */
static bool adopt_peer_schedule(firc_ifwatch_t *w, watched_group_t *wg, const firc_ifcheck_group_job_t *job) {
    for (size_t i = 0; i < w->n_groups; i++) {
        const watched_group_t *pr = &w->groups[i];
        if (!may_share_probe(wg, pr) || pr->n != wg->n || !ifaces_within(wg, pr) || !pr->has_result ||
            pr->check_in_flight || pr->fail_threshold != wg->fail_threshold ||
            pr->recover_threshold != wg->recover_threshold || !group_job_compatible(pr, job)) {
            continue;
        }
        for (size_t e = 0; e < wg->n; e++) {
            for (size_t k = 0; k < pr->n; k++) {
                if (strcmp(pr->names[k], wg->names[e]) == 0) {
                    wg->cand[e] = pr->cand[k];
                    break;
                }
            }
        }
        wg->has_result = true;
        wg->confirm_pending = pr->confirm_pending;
        wg->check_due_ms = pr->check_due_ms;
        record_sent_cands(wg, job);
        return true;
    }
    return false;
}

static void follow_leader(watched_group_t *wg, const watched_group_t *ld, const firc_ifcheck_group_job_t *job) {
    wg->check_in_flight = true;
    wg->recheck_after_flight = false;
    wg->check_sent_ms = ld->check_sent_ms;
    wg->leader_id = ld->id;
    wg->leader_gen = ld->probe_gen;
    record_sent_cands(wg, job);
}

static void tick_group_checks(firc_ifwatch_t *w, int64_t now) {
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE) { continue; }
        sync_group_cand_links(w, wg, now);
        if (wg->check_in_flight && now - wg->check_sent_ms >= FIRC_IFWATCH_LOST_MS) {
            wg->check_in_flight = false;
            /* A new gen: the lost probe's late answer must not pass for the next one's. */
            if (firc_id_equal(wg->leader_id, wg->id) && ++wg->probe_gen == 0) { wg->probe_gen = 1; }
        }
        if (wg->check_in_flight && !firc_id_equal(wg->leader_id, wg->id)) {
            const watched_group_t *ld = find_group(w, wg->leader_id);
            if (ld == NULL || !ld->check_in_flight || !firc_id_equal(ld->leader_id, ld->id) ||
                ld->probe_gen != wg->leader_gen) {
                wg->check_in_flight = false;
                if (wg->check_due_ms > now) { wg->check_due_ms = now; }
            }
        }
    }

    bool queued_leader_now = false;
    for (size_t target_n = FIRC_GROUP_IFACES_MAX; target_n >= 2; target_n--) {
        for (size_t i = 0; i < w->n_groups; i++) {
            watched_group_t *wg = &w->groups[i];
            if (wg->check_mode == FIRC_GROUP_CHECK_NONE || wg->n != target_n) { continue; }
            if (wg->check_in_flight || now < wg->check_due_ms) { continue; }
            if (w->d.queue_group == NULL) {
                wg->check_due_ms = now + wg->interval_ms;
                continue;
            }
            firc_ifcheck_group_job_t job;
            build_group_check_job(w, wg, now, &job);
            const watched_group_t *ld = find_covering_leader(w, wg, &job);
            if (ld != NULL) {
                follow_leader(wg, ld, &job);
                continue;
            }
            if (!wg->has_result && adopt_peer_schedule(w, wg, &job)) { continue; }
            if (w->d.queue_group(w->d.ud, &job) != FIRC_OK) {
                wg->check_due_ms = now + FIRC_IFWATCH_TICK_MS;
                continue;
            }
            wg->check_in_flight = true;
            wg->recheck_after_flight = false;
            wg->check_sent_ms = now;
            wg->leader_id = wg->id;
            wg->leader_gen = wg->probe_gen;
            record_sent_cands(wg, &job);
            queued_leader_now = true;
        }
    }

    if (!queued_leader_now) { return; }
    /* Pull an idle group into a probe queued now only when its own probe is near (a quarter of its interval):
     * two leaders out of phase never double its rate, and a 3 s confirm is never brought forward. */
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE || wg->check_in_flight || !wg->has_result ||
            wg->confirm_pending || now >= wg->check_due_ms || wg->check_due_ms - now > wg->interval_ms / 4 ||
            !has_phase_leader(w, wg, now)) {
            continue;
        }
        firc_ifcheck_group_job_t job;
        build_group_check_job(w, wg, now, &job);
        const watched_group_t *ld = find_phase_leader(w, wg, &job, now);
        if (ld != NULL) { follow_leader(wg, ld, &job); }
    }
}

void firc_ifwatch_tick(firc_ifwatch_t *w) {
    int64_t now = now_ms(w);
    reconcile(w, now);
    for (size_t i = 0; i < w->n_ifs; i++) {
        watched_if_t *wi = &w->ifs[i];
        bool is_tun = tunnel_known(w, wi);
        if (!wi->needs_global) {
            if (is_tun != wi->tunnel) {
                wi->tunnel = is_tun;
                wi->in_flight = false;
            }
            continue;
        }
        if (is_tun != wi->tunnel) {
            firc_if_track_init(&wi->track, now);
            classify(w, wi, now);
            if (wi->tunnel) { continue; }
        } else if (wi->tunnel) {
            tunnel_step(w, wi, now);
            continue;
        }
        if (wi->in_flight && now - wi->due_ms >= FIRC_IFWATCH_LOST_MS) { wi->in_flight = false; }
        if (wi->in_flight || now < wi->due_ms) { continue; }
        if (!ensure_route(w, wi)) {
            wi->due_ms = now + w->interval_ms;
            continue;
        }
        if (w->d.queue == NULL || w->d.queue(w->d.ud, wi->name, wi->route.mark, wi->gen) != FIRC_OK) {
            wi->due_ms = now + FIRC_IFWATCH_TICK_MS;
            continue;
        }
        wi->in_flight = true;
        wi->due_ms = now;
    }
    tick_group_checks(w, now);
    choose_all(w, now);
}

void firc_ifwatch_results(firc_ifwatch_t *w, const firc_ifcheck_result_t *r, size_t n) {
    int64_t now = now_ms(w);
    const firc_ifcheck_result_t *guard = NULL;
    bool any_passed = false;
    char names[FIRC_IFCHECK_BATCH * 18] = "";
    size_t at = 0;
    for (size_t i = 0; i < n; i++) {
        if (r[i].control_failed && (guard == NULL || (r[i].control_ran && !guard->control_ran))) { guard = &r[i]; }
        any_passed = any_passed || r[i].passed;
        if (r[i].control_failed && !r[i].control_ran && at < sizeof(names)) {
            at += (size_t)snprintf(names + at, sizeof(names) - at, "%s%s", at > 0 ? ", " : "", r[i].iface);
        }
        watched_if_t *wi = find_if(w, r[i].iface);
        if (wi == NULL || wi->tunnel || r[i].gen != wi->gen) { continue; }
        wi->in_flight = false;
        firc_if_step_t s = firc_if_track_check(&wi->track, r[i].passed, r[i].control_failed, r[i].error, now);
        wi->due_ms = now + (s == FIRC_IF_CONFIRM ? FIRC_IFCHOOSE_CONFIRM_MS : w->interval_ms + spread_ms(w, wi));
        say_state(wi, s, true);
    }
    if (guard != NULL && guard->control_ran) {
        firc_logrep_fail(&w->guard, FIRC_LOG_WARN, NULL, "the interface check fails, even through the WAN: %s",
                         guard->error);
    } else if (guard != NULL && any_passed) {
        firc_logrep_fail(&w->guard, FIRC_LOG_WARN, NULL, "cannot check %s: %s", names, guard->error);
    } else if (guard != NULL) {
        firc_logrep_fail(&w->guard, FIRC_LOG_WARN, NULL, "the interface check cannot run: %s", guard->error);
    } else if (n > 0) {
        (void)firc_logrep_ok(&w->guard, NULL, "the interface check");
    }
    choose_all(w, now);
}

/* After a regular (non-confirm) result, move the next probe onto the phase of an idle group that probes the same
 * thing (one set within the other), by at most half an interval and once: groups that drifted apart through
 * confirm rounds or were created at different times meet again, and the wider one then leads the shared probe. */
static void align_with_peer_phase(const firc_ifwatch_t *w, watched_group_t *wg, int64_t now) {
    const int64_t iv = wg->interval_ms;
    if (iv <= 0) { return; }
    const int64_t target = wg->check_due_ms;
    for (size_t i = 0; i < w->n_groups; i++) {
        const watched_group_t *pr = &w->groups[i];
        if (pr == wg || pr->check_mode != wg->check_mode || pr->interval_ms != iv || pr->timeout_ms != wg->timeout_ms ||
            !pr->has_result || pr->check_in_flight || pr->confirm_pending || strcmp(pr->check_url, wg->check_url) != 0 ||
            !(ifaces_within(wg, pr) || ifaces_within(pr, wg))) {
            continue;
        }
        const int64_t pd = pr->check_due_ms;
        int64_t k;
        if (pd >= target) {
            k = -((pd - target + iv / 2) / iv);
        } else {
            k = (target - pd + iv / 2) / iv;
        }
        int64_t next = pd + k * iv;
        if (next <= now) { next += iv; }
        wg->check_due_ms = next;
        return;
    }
}

static void apply_group_result_to_watched(firc_ifwatch_t *w, watched_group_t *wg, const firc_ifcheck_group_result_t *r,
                                          int64_t now) {
    wg->check_in_flight = false;
    const firc_ifcheck_group_cand_result_t *matched[FIRC_GROUP_IFACES_MAX];
    bool wg_any_passed = false;
    for (size_t e = 0; e < wg->n; e++) {
        matched[e] = NULL;
        for (size_t k = 0; k < r->n_cands; k++) {
            if (strcmp(r->cands[k].iface, wg->names[e]) == 0) {
                matched[e] = &r->cands[k];
                break;
            }
        }
        if (matched[e] != NULL && matched[e]->passed) { wg_any_passed = true; }
    }

    bool need_confirm = false;
    for (size_t e = 0; e < wg->n; e++) {
        watched_group_cand_t *c = &wg->cand[e];
        const watched_if_t *wi = find_if(w, wg->names[e]);
        const char *down_why = NULL;
        if (!is_cand_link_ok(w, wg->names[e], wi, now, &down_why)) {
            cand_mark_dead(wg, e, down_why, now);
            continue;
        }
        const firc_ifcheck_group_cand_result_t *cr = matched[e];
        if (cr == NULL) { continue; }
        bool skip_e = (e < wg->n_sent_cands) && wg->sent_cands[e].skip;
        bool cand_guarded = cr->passed ? false : (cr->guarded || !wg_any_passed || skip_e);
        firc_if_state_t prev = c->track.state;
        if (cr->passed) {
            c->fail_streak = 0;
            c->pass_streak++;
            c->track.suspect = false;
            c->track.error[0] = '\0';
            if (c->track.clean_since_ms < 0) { c->track.clean_since_ms = now; }
            if (prev == FIRC_IF_UNKNOWN) {
                c->track.state = FIRC_IF_ALIVE;
                c->track.since_ms = now;
                c->since_unix = (int64_t)time(NULL);
            } else if (prev == FIRC_IF_DEAD) {
                if (c->pass_streak >= wg->recover_threshold) {
                    c->track.state = FIRC_IF_ALIVE;
                    c->track.since_ms = now;
                    c->since_unix = (int64_t)time(NULL);
                    FIRC_INFO("group %s: %s recovered after %u passes", wg->name, wg->names[e],
                              (unsigned)c->pass_streak);
                } else {
                    need_confirm = true;
                }
            }
        } else if (!cand_guarded) {
            c->pass_streak = 0;
            c->fail_streak++;
            c->track.clean_since_ms = -1;
            snprintf(c->track.error, sizeof(c->track.error), "%s", cr->error);
            if (prev != FIRC_IF_DEAD) {
                if (c->fail_streak >= wg->fail_threshold) {
                    c->track.suspect = false;
                    c->track.state = FIRC_IF_DEAD;
                    c->track.since_ms = now;
                    c->since_unix = (int64_t)time(NULL);
                    FIRC_WARN("group %s: %s probe failed (%u/%u): %s", wg->name, wg->names[e],
                              (unsigned)c->fail_streak, (unsigned)wg->fail_threshold, c->track.error);
                } else {
                    c->track.suspect = true;
                    need_confirm = true;
                }
            }
        }
    }
    wg->has_result = true;
    wg->confirm_pending = need_confirm;
    int64_t delay = wg->interval_ms;
    if (need_confirm && FIRC_IFCHOOSE_CONFIRM_MS < delay) { delay = FIRC_IFCHOOSE_CONFIRM_MS; }
    wg->check_due_ms = now + delay;
    if (wg->recheck_after_flight) {
        wg->recheck_after_flight = false;
        wg->check_due_ms = now;
    } else if (!need_confirm) {
        align_with_peer_phase(w, wg, now);
    }
}

void firc_ifwatch_group_results(firc_ifwatch_t *w, const firc_ifcheck_group_result_t *r) {
    if (w == NULL || r == NULL) { return; }
    watched_group_t *leader = find_group(w, r->group_id);
    if (leader == NULL || leader->check_mode == FIRC_GROUP_CHECK_NONE || !leader->check_in_flight ||
        !firc_id_equal(leader->leader_id, r->group_id) || r->gen != leader->probe_gen ||
        r->n_cands != leader->n) {
        return;
    }
    int64_t now = now_ms(w);
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE || !wg->check_in_flight ||
            !firc_id_equal(wg->leader_id, r->group_id) || wg->leader_gen != r->gen) {
            continue;
        }
        apply_group_result_to_watched(w, wg, r, now);
    }
    choose_all(w, now);
}

static void pull(const firc_ifwatch_t *w, watched_if_t *wi, int64_t now, bool *done, int64_t *done_ms) {
    bool may_pull = !*done || now - *done_ms >= w->interval_ms;
    if (!wi->in_flight && !wi->track.suspect && may_pull && wi->due_ms > now) {
        wi->due_ms = now;
        *done = true;
        *done_ms = now;
    }
}

void firc_ifwatch_nudge(firc_ifwatch_t *w, const char *name) {
    watched_if_t *wi = find_if(w, name);
    if (wi != NULL) { pull(w, wi, now_ms(w), &wi->nudged, &wi->nudged_ms); }
}

void firc_ifwatch_link(firc_ifwatch_t *w, const char *name, bool up) {
    watched_if_t *wi = find_if(w, name);
    if (wi == NULL) { return; }
    int64_t now = now_ms(w);
    if (up) {
        if (wi->routed && w->d.rtnl != NULL) { (void)firc_checkroute_refresh(w->d.rtnl, &wi->route); }
        bool unblock_keendns = false;
        for (size_t i = 0; i < w->n_groups; i++) {
            watched_group_t *wg = &w->groups[i];
            if (wg->check_mode == FIRC_GROUP_CHECK_NONE) { continue; }
            for (size_t e = 0; e < wg->n; e++) {
                if (strcmp(wg->names[e], name) == 0) {
                    wg->cand[e].last_link_ok = true;
                    wg->cand[e].init_link = true;
                    if (wg->check_in_flight) {
                        wg->recheck_after_flight = true;
                    } else if (wg->check_due_ms > now) {
                        wg->check_due_ms = now;
                    }
                    if (wg->check_mode == FIRC_GROUP_CHECK_KEENDNS && wg->blocked) { unblock_keendns = true; }
                }
            }
        }
        if (unblock_keendns) { choose_all(w, now); }
        if (wi->tunnel) { return; }
        pull(w, wi, now, &wi->pulled, &wi->pulled_ms);
        return;
    }
    if (tunnel_still_starting(w, wi, now)) { return; }
    firc_if_step_t st = firc_if_track_link(&wi->track, false, now);
    if (wi->needs_global) { say_state(wi, st, true); }
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE) { continue; }
        for (size_t e = 0; e < wg->n; e++) {
            if (strcmp(wg->names[e], name) == 0) {
                wg->cand[e].last_link_ok = false;
                wg->cand[e].init_link = true;
                cand_mark_dead(wg, e, "the link is down or gone", now);
            }
        }
    }
    choose_all(w, now);
}

void firc_ifwatch_refresh(firc_ifwatch_t *w, const char *name) {
    for (size_t i = 0; i < w->n_ifs; i++) {
        const char *n = w->ifs[i].name;
        if (name != NULL && strcmp(n, name) != 0) { continue; }
        char copy[16];
        snprintf(copy, sizeof(copy), "%s", n);
        firc_ifwatch_link(w, copy, w->d.link_up == NULL || w->d.link_up(w->d.ud, copy));
    }
}

firc_ifchoice_t firc_ifwatch_pick(firc_ifwatch_t *w, const firc_group_t *g) {
    firc_ifchoice_t c = {0, false};
    if (w == NULL || g == NULL || firc_group_n_ifaces(g) < 2) { return c; }
    int64_t now = now_ms(w);
    watched_group_t *wg = group_rec(w, g, now);
    if (wg == NULL) { return c; }
    const firc_if_track_t *e[FIRC_GROUP_IFACES_MAX];
    tracks_of(w, wg, now, e);
    group_tracks_of(w, wg, now, e);
    c = choose(w, wg, e, now);
    wg->active = c.active;
    wg->blocked = c.all_dead;
    if (!c.all_dead) {
        (void)end_all_dead(wg);
    } else if (wg->all_dead.count == 0) {
        firc_logrep_fail_for(&wg->all_dead, FIRC_IFCHOOSE_ALL_DEAD_ERROR_MS, NULL, "group %s: %s", wg->name, k_all_dead);
    }
    return c;
}

bool firc_ifwatch_view(firc_ifwatch_t *w, const char *name, firc_if_view_t *out) {
    const watched_if_t *wi = w != NULL ? find_if(w, name) : NULL;
    if (wi == NULL) { return false; }
    out->state = wi->track.state;
    out->since_unix = wi->since_unix;
    snprintf(out->error, sizeof(out->error), "%s", wi->track.error);
    return true;
}

bool firc_ifwatch_group_view(firc_ifwatch_t *w, firc_id_t id, const char *name, firc_if_view_t *out) {
    if (w == NULL || name == NULL || out == NULL) { return false; }
    const watched_group_t *wg = find_group(w, id);
    if (wg != NULL && wg->check_mode != FIRC_GROUP_CHECK_NONE) {
        for (size_t e = 0; e < wg->n; e++) {
            if (strcmp(wg->names[e], name) == 0) {
                out->state = wg->cand[e].track.state;
                out->since_unix = wg->cand[e].since_unix;
                snprintf(out->error, sizeof(out->error), "%s", wg->cand[e].track.error);
                return true;
            }
        }
    }
    return firc_ifwatch_view(w, name, out);
}

firc_err_t firc_ifwatch_move_up(firc_ifwatch_t *w, firc_id_t id) {
    watched_group_t *wg = find_group(w, id);
    if (wg == NULL) { return FIRC_ERR_NOENT; }
    if (wg->active == 0 && !wg->blocked) { return FIRC_OK; }
    const firc_if_track_t *e[FIRC_GROUP_IFACES_MAX];
    group_tracks_of(w, wg, now_ms(w), e);
    if (!firc_ifchoose_first_alive(e, wg->n)) { return FIRC_ERR_STATE; }
    return apply(w, wg, (firc_ifchoice_t){0, false});
}
