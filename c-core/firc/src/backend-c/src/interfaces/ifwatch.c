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
    uint32_t timeout_ms;
    uint32_t fail_threshold;
    uint32_t recover_threshold;
    uint32_t probe_gen;
    bool check_in_flight;
    int64_t check_due_ms;
    int64_t check_sent_ms;
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
    uint32_t intv_s = g->check.interval_sec > 0 ? g->check.interval_sec : FIRC_GROUP_CHECK_DEFAULT_INTERVAL_SEC;
    int64_t intv_ms = (int64_t)intv_s * 1000;
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
        wg->timeout_ms = timeout_ms;
        wg->fail_threshold = fail_thr;
        wg->recover_threshold = rec_thr;
        if (++wg->probe_gen == 0) { wg->probe_gen = 1; }
        wg->check_in_flight = false;
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
    } else if (wg->interval_ms != intv_ms) {
        wg->interval_ms = intv_ms;
        if (!wg->check_in_flight && wg->check_due_ms > now + wg->interval_ms) {
            wg->check_due_ms = now + wg->interval_ms;
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
            if (!wg->check_in_flight && wg->check_due_ms > now) { wg->check_due_ms = now; }
        } else {
            c->last_link_ok = ok;
        }
        if (!ok) { cand_mark_dead(wg, e, why, now); }
    }
}

static void tick_group_checks(firc_ifwatch_t *w, int64_t now) {
    for (size_t i = 0; i < w->n_groups; i++) {
        watched_group_t *wg = &w->groups[i];
        if (wg->check_mode == FIRC_GROUP_CHECK_NONE) { continue; }
        sync_group_cand_links(w, wg, now);
        if (wg->check_in_flight && now - wg->check_sent_ms >= FIRC_IFWATCH_LOST_MS) { wg->check_in_flight = false; }
        if (wg->check_in_flight || now < wg->check_due_ms) { continue; }
        if (w->d.queue_group == NULL) {
            wg->check_due_ms = now + wg->interval_ms;
            continue;
        }
        firc_ifcheck_group_job_t job;
        memset(&job, 0, sizeof(job));
        job.group_id = wg->id;
        job.gen = wg->probe_gen;
        job.mode = wg->check_mode;
        snprintf(job.url, sizeof(job.url), "%s", wg->check_url);
        job.timeout_ms = wg->timeout_ms;
        job.n_cands = wg->n;
        const firc_group_t *g = find_group_spec(w, wg->id);
        for (size_t e = 0; e < wg->n; e++) {
            firc_ifcheck_group_cand_t *jc = &job.cands[e];
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
        if (w->d.queue_group(w->d.ud, &job) != FIRC_OK) {
            wg->check_due_ms = now + FIRC_IFWATCH_TICK_MS;
            continue;
        }
        wg->check_in_flight = true;
        wg->check_sent_ms = now;
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

void firc_ifwatch_group_results(firc_ifwatch_t *w, const firc_ifcheck_group_result_t *r) {
    if (w == NULL || r == NULL) { return; }
    watched_group_t *wg = find_group(w, r->group_id);
    if (wg == NULL || wg->check_mode == FIRC_GROUP_CHECK_NONE || r->gen != wg->probe_gen ||
        r->n_cands != wg->n) {
        return;
    }
    int64_t now = now_ms(w);
    wg->check_in_flight = false;
    bool need_confirm = false;
    for (size_t e = 0; e < wg->n; e++) {
        const firc_ifcheck_group_cand_result_t *cr = &r->cands[e];
        watched_group_cand_t *c = &wg->cand[e];
        const watched_if_t *wi = find_if(w, wg->names[e]);
        const char *down_why = NULL;
        if (!is_cand_link_ok(w, wg->names[e], wi, now, &down_why)) {
            cand_mark_dead(wg, e, down_why, now);
            continue;
        }
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
        } else if (!cr->guarded) {
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
    int64_t delay = wg->interval_ms;
    if (need_confirm && FIRC_IFCHOOSE_CONFIRM_MS < delay) { delay = FIRC_IFCHOOSE_CONFIRM_MS; }
    wg->check_due_ms = now + delay;
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
                    if (!wg->check_in_flight && wg->check_due_ms > now) { wg->check_due_ms = now; }
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
