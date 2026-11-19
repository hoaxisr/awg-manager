#include "firc/app.h"

#include <errno.h>
#include <ifaddrs.h>
#include <linux/if.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <net/if.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "firc/keenetic_rci.h"
#include "firc/mark.h"
#include "firc/json.h"
#include "firc/utf8.h"
#include "firc/hash.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/match.h"
#include "firc/dnat.h"
#include "firc/mss.h"
#include "firc/redir.h"
#include "firc/pool_reject.h"
#include "firc/ifacename.h"
#include "firc/rand.h"
#include "firc/taprules.h"
#include "firc/autolistcheck.h"
#include "firc/autolistrules.h"
#include "firc/autolistsvc.h"
#include "firc/ifcheck.h"
#include "firc/regdomain.h"

#define AL_ADDS_PER_HOUR 30
#define AL_RECHECK_HOSTS 32
#define AL_PTR_PER_MINUTE 30
#include "firc/tapwindow.h"
#include "firc/events.h"
#include "firc/recall.h"
#include "firc/netfilter_cleaner.h"
#include "firc/nfcommit.h"
#include "firc/purge.h"
#include "firc/rulesnap.h"
#include "firc/sub_fetch.h"
#include "firc/sub_worker.h"
#include "firc/subparse.h"

#define FIRC_IFACE_NAME_CAP 16

/* a flow flush a pass must carry before it runs */
typedef struct nf_flush {
    uint32_t value; /* firc_mark_group_value(field) */
    firc_id_t owner;
    char owner_s[FIRC_ID_STR_LEN];
    char iface[64];
    int dev;
    uint64_t ticket;
    bool narrowed; /* owner's selector narrowed: paid even if routed again the same way */
    bool sweep;
    firc_nf_prefixes_t close;
    firc_nf_prefixes_t lost;
} nf_flush_t;

typedef enum { FLUSH_TEARDOWN, FLUSH_NARROWED, FLUSH_SWEEP } flush_kind_t;

typedef enum { WD_FLUSH, WD_UNCOND, WD_SWEEP } withdraw_kind_t;

typedef struct ct_wait {
    uint32_t value;
    firc_id_t owner;
    char owner_s[FIRC_ID_STR_LEN];
    char iface[64];
    uint8_t dev;
    int acks;
    uint32_t gen;
    withdraw_kind_t kind;
    bool sweep_lost;
    bool forget;
    uint64_t due_ms;
} ct_wait_t;

#define FIRC_APP_CLOSE_WAIT_MS 1000

typedef struct nf_held {
    firc_ipset_to_link_t *link;
    firc_id_t owner;
    uint64_t ticket;
} nf_held_t;

typedef struct set_rec {
    firc_id_t id;
    uint8_t family;
    bool live;
    bool failed;
    firc_err_t failed_err;
    firc_nf_print_t print;
    firc_nf_print_t failed_print;
} set_rec_t;

typedef struct set_job {
    firc_id_t id;
    uint8_t family;
    char live[FIRC_SET_NAME_MAX];
    char scratch[FIRC_SET_NAME_MAX];
    firc_nf_prefixes_t list;
    firc_nf_print_t print;
    bool done;
    firc_err_t err;
} set_job_t;

struct firc_app {
    firc_lograte_t said_snapshot;
    bool owed_full_sweep; /* a failed-to-report full pass asked for one more; cleared by the next landed report */
    firc_config_t *cfg;
    firc_app_config_t saved; /* what firc.conf holds; cfg->app is what the daemon RUNS, read at run time */
    firc_ipt_t *ipt4;
    firc_ipt_t *ipt6;
    firc_rtnl_t *rtnl;
    firc_ct_t *ct;
    firc_setnl_t *sets;
    set_rec_t *set_recs;
    size_t n_set_recs;
    size_t cap_set_recs;
    char *sets_swept;
    bool sweep_again;
    bool sweep_said;
    int set_retry_timer;
    uint32_t set_retry_ms;
    bool set_retry_due;
    firc_id_t *set_left;
    size_t n_set_left;
    size_t cap_set_left;
    firc_fakeip_t *pool;                /* borrowed, nullable; loop thread only */
    firc_loop_t *loop;                  /* borrowed, nullable */
    firc_dnsproxy_t *proxy;             /* borrowed, nullable */
    /* a pass copies pool_snap into pass_snap at its start, so a mid-pass refresh never changes its view */
    pthread_mutex_t snap_mu;
    firc_fakeip_snapshot_t *pool_snap;      /* owned */
    const firc_fakeip_snapshot_t *pass_snap; /* what the running pass reads */
    firc_fakeip_snapshot_t **retired;
    size_t n_retired;
    firc_dns_pipeline_t *pipeline;
    firc_resolve_router_t *router;   /* borrowed, nullable */
    firc_kn_resolvers_t *resolvers;  /* borrowed, nullable */
    uint32_t start_idx;
    bool running;

    firc_ruleset_t **rulesets;
    size_t n_rulesets;
    size_t cap_rulesets;

    firc_port_remap_t *port_remap; /* borrowed, nullable (main() owns it) */

    /* recursive: entry points can grow into nesting; lock/unlock is a no-op pair until a committer starts */
    pthread_mutex_t nf_mu;
    bool nf_mu_ready;
    uint64_t nf_enters; /* acquisitions of nf_mu through app_nf_enter, loop thread's only; test seam */
    firc_nfcommit_t *committer;
    uint64_t nf_change; /* moved on under nf_mu by every mutation that asks for a pass; a pass reads it once, at lock time */
    nf_flush_t *flushes; /* loop thread only, at most one entry per mark field (FIRC_MARK_MAX_GROUPS) */
    size_t n_flushes;
    size_t cap_flushes;
    ct_wait_t *waits;
    size_t n_waits;
    size_t cap_waits;
    int wait_timer;
    uint64_t wait_timer_at;
    int owed[FIRC_KTCP_DEVICES];
    uint32_t child_gen[FIRC_KTCP_DEVICES];
    nf_held_t *held;
    size_t n_held;
    size_t cap_held;

    firc_sub_worker_t *sub_worker;
    uint64_t sync_seq_counter; /* monotonic across the app, not per list */
    firc_app_sync_listener_fn sync_listener;
    void *sync_listener_ud;
    bool sync_listener_muted; /* raised for the length of firc_app_sync_list_now, a test seam with no stream watching it */
    const char *config_path; /* borrowed; NULL means an applied sync writes no file */
    const char *config_version;
    /* _Atomic/relaxed: committer reads it per pass while the loop writes it; a plain bool here is a data race */
    _Atomic bool tap_open;
    int64_t tap_ends_at;
    int64_t tap_started_ms; /* monotonic, for how long a stopped capture ran */
    int64_t tap_ends_mono; /* the real deadline, on a clock no NTP step moves; tap_ends_at is for printing only */
    int64_t tap_seconds; /* FIRC_CAPTURE_SECONDS, or a test's */
    uint64_t (*mono_ms)(void); /* a failed group's retry spacing */
    int64_t (*wall_s)(void);
    int retry_timer; /* the loop timer armed for the earliest due retry, 0 when none */
    uint64_t retry_timer_at;
    uint64_t retry_timer_delay;
    char tap_token[FIRC_TAP_TOKEN_CHARS + 1];
    int tap_timer; /* 0 when none: the deadline, armed on the loop */
    /* [0] new-connection socket, [1] ClientHello socket; loop-thread state, no lock needed */
    firc_nflog_t *(*tap_nflog_open)(uint16_t group, uint16_t copy_range);
    unsigned (*tap_iface_index)(const char *name);
    struct tap_reader {
        firc_app_t *app;
        firc_nflog_t *sock;
        int which;
    } tap_reader[2];
    char **tap_lan; /* capture's LAN interfaces, copied from app.link at start; read under nf_mu, like tap_open */
    size_t n_tap_lan;
    firc_tap_report_t *tap_report;
    firc_capture_last_t tap_last;
    bool tap_has_last;
    firc_tap_pool_t tap_pool;
    /* counted by the committer, folded into the report by the loop; atomic since the loop must not take nf_mu here */
    _Atomic uint32_t tap_failed_passes;
    uint32_t tap_failed_passes_seen; /* loop thread only */
    firc_devsel_policy_fn tap_policy;
    firc_devsel_device_fn tap_device;
    void *tap_resolver_ud;
    _Atomic bool al_on;
    char **al_lan;
    size_t n_al_lan;
    firc_autolist_svc_t *al_svc;
    const char *al_why_off;
    int64_t al_added[AL_ADDS_PER_HOUR];
    size_t al_added_at;
    bool al_cap_said;
    firc_al_cands_t *al_cands;
    firc_al_probes_t *al_probes;
    firc_autolist_check_t *al_check;
    uint64_t al_check_gen;
    char al_check_host[FIRC_AUTOLIST_NAME_MAX];
    bool al_check_recheck;
    bool al_check_by_domain;
    size_t al_recheck_from;
    int al_tick_timer;
    int al_recheck_timer;
    uint64_t al_recheck_ms;
    int64_t al_checked_at;
    bool al_wan_warning;
    bool al_wan_url_said;
    uint64_t al_switched_off, al_switched_on;
    firc_autolist_probe_fn al_probe;
    firc_ptr_t *al_ptr;
    int64_t al_ptr_sent[AL_PTR_PER_MINUTE];
    size_t al_ptr_sent_at;
    firc_app_tunnel_down_fn tunnel_down;
    void *tunnel_down_ud;
    bool dns_down;
    firc_app_ifaces_t ifaces;
    firc_app_dnsstats_fn dnsstats;
    void *dnsstats_ud;
    firc_ruleset_lookup_t lookup; /* filled after the rulesets exist; read on the loop thread when a selector renders */

    bool pass_refused;
    uint16_t ktcp_ports[FIRC_KTCP_DEVICES];
    char tun_names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP];
    firc_ktcp_image_t pass_image;
    firc_ktcp_image_t last_image;
    firc_xt_acl_t acl_cached, pass_skip, last_skip;
    bool acl_known, acl_fail_said, acl_all_said;
    firc_ktcp_off_t pass_off, last_off;
    firc_app_ktcp_sink_t ktcp_sink;
    bool ktcp_lack_said, ktcp_cap_said, redir_refused_said;
    bool redir_off, redir_staged;
    bool mss_off[2], mss_staged[2], mss_refused_said, refused_v6;
    const char *refused_table;
    firc_ip_t reject_base[2]; /* the pool prefixes, for the FORWARD barrier */
    uint8_t reject_len[2];
    bool reject_has[2];
};

/* every entry point that mutates the registry or drives an iptables engine must hold this */
static void app_nf_enter(firc_app_t *app);
static bool ktcp_redirected(const firc_app_t *app, int dev);
static void app_nf_leave(firc_app_t *app);
static void on_tap_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud);
static void tap_reader_down(firc_app_t *app);
static void on_tap_deadline(firc_loop_t *loop, void *ud);
static firc_err_t capture_end(firc_app_t *app, const char *token, const char *how);
static void autolist_stop(firc_app_t *app);
static void report_committed(firc_app_t *app, uint64_t gen, bool full, uint64_t ticket, firc_ktcp_image_t *image,
                             const firc_xt_acl_t *skip, firc_ktcp_off_t off, bool sets_failed);
static const firc_fakeip_snapshot_t *rules_snapshot(void *ud);
static void pool_forget_group_id(firc_app_t *app, firc_id_t gid);
static void firc_app_request_sync_missing_unlocked(firc_app_t *app); /* needed from inside app_nf_enter below */

firc_err_t firc_app_refresh_pool_snapshot(firc_app_t *app) {
    if (app == NULL) { return FIRC_ERR_INVAL; }
    firc_fakeip_snapshot_t *fresh = firc_fakeip_snapshot_take(app->pool);
    if (fresh == NULL && app->pool != NULL) { return FIRC_ERR_NOMEM; }

    pthread_mutex_lock(&app->snap_mu);
    firc_fakeip_snapshot_t *old = app->pool_snap;
    app->pool_snap = fresh;
    bool in_use = old != NULL && old == app->pass_snap;
    if (in_use) {
        firc_fakeip_snapshot_t **grown =
            realloc(app->retired, (app->n_retired + 1) * sizeof(*grown));
        if (grown == NULL) { /* can't remember it: keep the old one current and drop the fresh copy */
            app->pool_snap = old;
            pthread_mutex_unlock(&app->snap_mu);
            firc_fakeip_snapshot_free(fresh);
            return FIRC_ERR_NOMEM;
        }
        app->retired = grown;
        app->retired[app->n_retired++] = old;
        old = NULL;
    }
    pthread_mutex_unlock(&app->snap_mu);
    firc_fakeip_snapshot_free(old);
    return FIRC_OK;
}

/* a group edit never writes a chain without its chunk rules: a pass reads its own snapshot */
static const firc_fakeip_snapshot_t *rules_snapshot(void *ud) {
    return firc_app_rules_snapshot(ud);
}

const firc_fakeip_snapshot_t *firc_app_rules_snapshot(firc_app_t *app) {
    pthread_mutex_lock(&app->snap_mu);
    const firc_fakeip_snapshot_t *s = app->pass_snap != NULL ? app->pass_snap : app->pool_snap;
    pthread_mutex_unlock(&app->snap_mu);
    return s;
}

static uint64_t pass_snapshot_begin(firc_app_t *app) {
    pthread_mutex_lock(&app->snap_mu);
    app->pass_snap = app->pool_snap;
    uint64_t gen = firc_fakeip_snapshot_gen(app->pass_snap);
    pthread_mutex_unlock(&app->snap_mu);
    return gen;
}

static void pass_snapshot_end(firc_app_t *app) {
    pthread_mutex_lock(&app->snap_mu);
    app->pass_snap = NULL;
    firc_fakeip_snapshot_t **retired = app->retired;
    size_t n = app->n_retired;
    app->retired = NULL;
    app->n_retired = 0;
    pthread_mutex_unlock(&app->snap_mu);
    for (size_t i = 0; i < n; i++) { firc_fakeip_snapshot_free(retired[i]); }
    free(retired);
}

void firc_app_nf_enter(firc_app_t *app) { app_nf_enter(app); }
void firc_app_nf_leave(firc_app_t *app) { app_nf_leave(app); }

static void app_nf_enter(firc_app_t *app) {
    if (!app->nf_mu_ready) { return; }
    firc_nfcommit_interrupt(app->committer);
    pthread_mutex_lock(&app->nf_mu);
    app->nf_enters++;
}

static void app_nf_leave(firc_app_t *app) {
    if (!app->nf_mu_ready) { return; }
    pthread_mutex_unlock(&app->nf_mu);
}

static bool tunnel_dev_of(void *ud, const char *iface);

static firc_ruleset_deps_t ruleset_deps(firc_app_t *app) {
    firc_ruleset_deps_t deps = {
        .ipt4 = app->ipt4,
        .ipt6 = app->ipt6,
        .rtnl = app->rtnl,
        .pool = app->pool,
        .snap = rules_snapshot,
        .snap_ud = app,
        .chain_prefix = app->cfg->app.netfilter.iptables.chain_prefix,
        .start_idx = app->start_idx,
        .lookup = &app->lookup,
        .tunnel_dev = tunnel_dev_of,
        .tunnel_ud = app,
    };
    return deps;
}

static firc_err_t rulesets_push(firc_app_t *app, firc_ruleset_t *rs) {
    if (app->n_rulesets == app->cap_rulesets) {
        size_t new_cap = app->cap_rulesets ? app->cap_rulesets * 2 : 8;
        firc_ruleset_t **na = realloc(app->rulesets, new_cap * sizeof(*na));
        if (!na) { return FIRC_ERR_NOMEM; }
        app->rulesets = na;
        app->cap_rulesets = new_cap;
    }
    app->rulesets[app->n_rulesets++] = rs;
    return FIRC_OK;
}

static void rulesets_remove_at(firc_app_t *app, size_t idx) {
    for (size_t i = idx; i + 1 < app->n_rulesets; i++) {
        app->rulesets[i] = app->rulesets[i + 1];
    }
    app->n_rulesets--;
}

/* never a full pass: it would move jumps someone put above ours */
static void app_request_more_locked(firc_app_t *app) {
    app->nf_change++;
    firc_nfcommit_request_more(app->committer);
}

static bool flush_queued_for(const firc_app_t *app, firc_id_t owner) {
    for (size_t i = 0; i < app->n_flushes; i++) {
        if (firc_id_equal(app->flushes[i].owner, owner)) { return true; }
    }
    for (size_t i = 0; i < app->n_waits; i++) {
        if (firc_id_equal(app->waits[i].owner, owner)) { return true; }
    }
    return false;
}

bool firc_app_stale_groups(const firc_app_t *app, firc_app_field_kept_fn kept, void *ud, uint32_t only_field,
                           firc_app_stale_groups_t *out) {
    memset(out, 0, sizeof(*out));
    size_t n = app->n_rulesets;
    if (n == 0) { return true; }
    out->v = calloc(n, sizeof(*out->v));
    out->ids = calloc(n, sizeof(*out->ids));
    if (out->v == NULL || out->ids == NULL) {
        firc_app_stale_groups_free(out);
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        const firc_group_t *g = rs != NULL ? firc_ruleset_group(rs) : NULL;
        if (g == NULL) { continue; }
        firc_stale_group_t *e = &out->v[out->n];
        firc_id_format(g->id, out->ids[out->n]);
        e->id = out->ids[out->n];
        e->field = firc_ruleset_mark_field(rs);
        e->inexact = firc_ruleset_has_devices(rs);
        e->field_kept = kept(ud, g, e->id, e->field);
        firc_ct_chunk_t *subnets = NULL;
        bool wanted = only_field == 0 || e->field == only_field;
        if (wanted && !firc_stale_group_subnets(g, e->field, e->field_kept, &subnets, &e->n_subnets)) {
            firc_app_stale_groups_free(out);
            return false;
        }
        e->subnets = subnets;
        out->n++;
    }
    return true;
}

void firc_app_stale_groups_free(firc_app_stale_groups_t *t) {
    for (size_t i = 0; t->v != NULL && i < t->n; i++) { free((void *)t->v[i].subnets); }
    free(t->v);
    free(t->ids);
    memset(t, 0, sizeof(*t));
}

static bool list_not_loaded(void *ud, const firc_group_t *g, const char *id, uint32_t field) {
    (void)ud;
    (void)id;
    (void)field;
    return g->list != NULL && !g->list->has_body_hash;
}

static void sweep_stale_field(firc_app_t *app, uint32_t value, const char *who) {
    if (app->ct == NULL || app->pool == NULL) { return; }
    firc_app_stale_groups_t t;
    if (!firc_app_stale_groups(app, list_not_loaded, NULL, value, &t)) {
        FIRC_WARN("group %s: no memory to check its flows for a prefix it no longer routes; none were touched", who);
        return;
    }
    size_t dropped = 0;
    firc_err_t err = firc_stale_field_sweep(app->ct, app->pool, t.v, t.n, FIRC_MARK_GROUP_MASK, value, &dropped);
    firc_app_stale_groups_free(&t);
    if (dropped > 0) { FIRC_INFO("group %s: dropped %zu flow(s) to a prefix it no longer routes", who, dropped); }
    if (err != FIRC_OK) {
        FIRC_WARN("group %s: the check of its flows for a prefix it no longer routes did not finish: %s", who,
                  firc_err_str(err));
    }
}

static bool prefixes_add_v4(firc_nf_prefixes_t *to, const firc_nf_prefix_t *p, size_t n) {
    return firc_nf_prefixes_join(to, p, n, 4) == FIRC_OK;
}

typedef struct {
    firc_nf_prefix_t *v;
    size_t n, cap;
    bool ok;
} chunk_walk_t;

static void chunk_prefix(void *ud, const char *group_id, unsigned family, const firc_ip_t *base, uint8_t prefix) {
    chunk_walk_t *w = ud;
    (void)group_id;
    if (family != FIRC_FAM_V4 || !w->ok) { return; }
    if (w->n == w->cap) {
        size_t cap = w->cap != 0 ? w->cap * 2 : 8;
        firc_nf_prefix_t *v = realloc(w->v, cap * sizeof(*v));
        if (v == NULL) {
            w->ok = false;
            return;
        }
        w->v = v;
        w->cap = cap;
    }
    firc_nf_prefix_t p = {.family = 4, .cidr = prefix};
    memcpy(p.addr, base->b, 4);
    w->v[w->n++] = p;
}

static int tun_dev(const firc_app_t *app, const char *iface) {
    if (iface == NULL || iface[0] == '\0') { return -1; }
    for (int d = 0; d < FIRC_KTCP_DEVICES; d++) {
        if (strcmp(app->tun_names[d], iface) == 0) { return d; }
    }
    return -1;
}

static int group_tunnel(const firc_app_t *app, const firc_ruleset_t *rs) {
    return tun_dev(app, firc_ruleset_active_iface(rs));
}

static bool tunnel_dev_of(void *ud, const char *iface) { return tun_dev(ud, iface) >= 0; }

static void close_capture(const firc_app_t *app, const firc_ruleset_t *rs, firc_nf_prefixes_t *out) {
    out->v = NULL;
    out->n = 0;
    const firc_group_t *g = firc_ruleset_group(rs);
    if (group_tunnel(app, rs) < 0) { return; }
    char id[FIRC_ID_STR_LEN];
    firc_id_format(g->id, id);
    chunk_walk_t w = {NULL, 0, 0, true};
    firc_fakeip_walk_chunks(app->pool, id, chunk_prefix, &w);
    firc_nf_prefixes_t subs = {NULL, 0};
    bool ok = w.ok && prefixes_add_v4(out, w.v, w.n) && firc_ruleset_prefixes(rs, &subs) == FIRC_OK &&
              prefixes_add_v4(out, subs.v, subs.n);
    free(w.v);
    firc_nf_prefixes_clear(&subs);
    if (!ok) { FIRC_WARN("group %s: no memory to name every redirected flow to close; the rest end on their own", id); }
}

static void lost_capture(const firc_app_t *app, const firc_ruleset_t *rs, const firc_nf_prefixes_t *was,
                         const firc_nf_prefixes_t *now, firc_nf_prefixes_t *out) {
    out->v = NULL;
    out->n = 0;
    const firc_group_t *g = firc_ruleset_group(rs);
    if (group_tunnel(app, rs) < 0) { return; }
    firc_nf_prefixes_t lost = {NULL, 0};
    if (firc_nf_prefixes_lost(was, now, &lost) != FIRC_OK || !prefixes_add_v4(out, lost.v, lost.n)) {
        char id[FIRC_ID_STR_LEN];
        firc_id_format(g->id, id);
        FIRC_WARN("group %s: no memory to name the redirected flows of a lost prefix; they end on their own", id);
    }
    firc_nf_prefixes_clear(&lost);
}

static int send_close(firc_app_t *app, int dev, const firc_nf_prefixes_t *p) {
    if (dev < 0 || p->n == 0 || app->ktcp_sink.close == NULL) { return 0; }
    int sent = app->ktcp_sink.close((uint8_t)dev, p->v, p->n, app->ktcp_sink.ud);
    return sent > 0 ? sent : 0;
}

static void pay_ct(firc_app_t *app, uint32_t value, const char *who, bool sweep, bool forget) {
    if (sweep) {
        sweep_stale_field(app, value, who);
    } else {
        firc_ipset_to_link_flush_mark(app->ct, value, who);
    }
    if (forget) { firc_rtnl_forget_mark_field(app->rtnl, who); }
}

static void on_wait_timer(firc_loop_t *loop, void *ud);

static void waits_rearm(firc_app_t *app) {
    uint64_t earliest = 0;
    for (size_t i = 0; i < app->n_waits; i++) {
        if (earliest == 0 || app->waits[i].due_ms < earliest) { earliest = app->waits[i].due_ms; }
    }
    if (app->wait_timer != 0 && earliest == app->wait_timer_at) { return; }
    if (app->wait_timer != 0) { (void)firc_loop_del_timer(app->loop, app->wait_timer); }
    app->wait_timer = 0;
    app->wait_timer_at = 0;
    if (earliest == 0 || app->loop == NULL) { return; }
    uint64_t now = app->mono_ms();
    uint64_t delay = earliest > now ? earliest - now : 1;
    int id = 0;
    if (firc_loop_add_timer(app->loop, delay, 0, on_wait_timer, app, &id) != FIRC_OK) {
        FIRC_WARN("could not arm the deadline of a close; its flush is paid with the next ack or pass");
        return;
    }
    app->wait_timer = id;
    app->wait_timer_at = earliest;
}

static void wait_pay_at(firc_app_t *app, size_t i) {
    ct_wait_t w = app->waits[i];
    memmove(&app->waits[i], &app->waits[i + 1], (app->n_waits - i - 1) * sizeof(*app->waits));
    app->n_waits--;
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, w.owner);
    const char *now = rs != NULL ? firc_ruleset_active_iface(rs) : NULL;
    if (w.kind == WD_FLUSH && rs != NULL && firc_ruleset_routed(rs) && now != NULL && strcmp(now, w.iface) == 0 &&
        firc_ruleset_mark_field(rs) == w.value) {
        FIRC_DEBUG("group %s routes through %s again: its flows are its own, not flushed", w.owner_s, w.iface);
        if (w.sweep_lost) { sweep_stale_field(app, w.value, w.owner_s); }
        return;
    }
    pay_ct(app, w.value, w.owner_s, w.kind == WD_SWEEP, w.forget && rs == NULL && !flush_queued_for(app, w.owner));
}

static bool wait_takes_forget(firc_app_t *app, firc_id_t owner) {
    for (size_t i = app->n_waits; i > 0; i--) {
        if (firc_id_equal(app->waits[i - 1].owner, owner)) {
            app->waits[i - 1].forget = true;
            return true;
        }
    }
    return false;
}

static void owe(firc_app_t *app, int dev, int acks, uint32_t gen) {
    if (dev >= 0 && dev < FIRC_KTCP_DEVICES && gen == app->child_gen[dev]) { app->owed[dev] += acks; }
}

static void waits_due(firc_app_t *app) {
    uint64_t now = app->mono_ms();
    for (size_t i = 0; i < app->n_waits;) {
        if (app->waits[i].due_ms <= now) {
            owe(app, app->waits[i].dev, app->waits[i].acks, app->waits[i].gen);
            wait_pay_at(app, i);
        } else {
            i++;
        }
    }
    waits_rearm(app);
}

static void on_wait_timer(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_app_t *app = ud;
    app->wait_timer = 0;
    app->wait_timer_at = 0;
    waits_due(app);
}

static bool wait_add(firc_app_t *app, const char *iface, int dev, int acks, uint32_t value, firc_id_t owner,
                     const char *who, withdraw_kind_t kind, bool sweep_lost, bool forget) {
    if (app->n_waits == app->cap_waits) {
        size_t cap = app->cap_waits != 0 ? app->cap_waits * 2 : 8;
        ct_wait_t *grown = realloc(app->waits, cap * sizeof(*grown));
        if (grown == NULL) { return false; }
        app->waits = grown;
        app->cap_waits = cap;
    }
    ct_wait_t *w = &app->waits[app->n_waits++];
    *w = (ct_wait_t){.value = value, .owner = owner, .dev = (uint8_t)dev, .acks = acks, .gen = app->child_gen[dev],
                     .kind = kind, .sweep_lost = sweep_lost,
                     .forget = forget, .due_ms = app->mono_ms() + FIRC_APP_CLOSE_WAIT_MS};
    snprintf(w->owner_s, sizeof(w->owner_s), "%s", who);
    snprintf(w->iface, sizeof(w->iface), "%s", iface != NULL ? iface : "");
    waits_rearm(app);
    return true;
}

static void withdraw(firc_app_t *app, const char *iface, int dev, uint32_t value, firc_id_t owner, const char *who,
                     withdraw_kind_t kind, bool sweep_lost, bool forget, const firc_nf_prefixes_t *a,
                     const firc_nf_prefixes_t *b) {
    int sent = send_close(app, dev, a) + (b != NULL ? send_close(app, dev, b) : 0);
    if (sent == 0 || !wait_add(app, iface, dev, sent, value, owner, who, kind, sweep_lost, forget)) {
        if (sent > 0) { owe(app, dev, sent, app->child_gen[dev]); }
        bool handed = forget && wait_takes_forget(app, owner);
        pay_ct(app, value, who, kind == WD_SWEEP, forget && !handed);
    }
}

static void flush_entry_clear(nf_flush_t *e) {
    firc_nf_prefixes_clear(&e->close);
    firc_nf_prefixes_clear(&e->lost);
}

static void note_close_lost(const char *who) {
    FIRC_WARN("group %s: no memory to add to the redirected flows to close; some end on their own", who);
}

/* a field already queued keeps its entry (and interface) with the new ticket, so A->B->A leaves flows where they were */
static void flush_queue_add(firc_app_t *app, uint32_t value, const firc_group_t *g, const char *iface,
                            flush_kind_t kind, const firc_nf_prefixes_t *close) {
    uint64_t ticket = ++app->nf_change;
    bool narrowed = kind == FLUSH_NARROWED, sweep = kind == FLUSH_SWEEP;
    char who[FIRC_ID_STR_LEN];
    firc_id_format(g->id, who);
    for (size_t i = 0; i < app->n_flushes; i++) {
        nf_flush_t *e = &app->flushes[i];
        if (e->value == value) {
            e->ticket = ticket;
            e->narrowed = e->narrowed || narrowed; /* never cleared by a merge */
            e->sweep = e->sweep || sweep;
            if (!prefixes_add_v4(sweep ? &e->lost : &e->close, close->v, close->n)) { note_close_lost(who); }
            return;
        }
    }
    if (app->n_flushes == app->cap_flushes) {
        size_t cap = app->cap_flushes != 0 ? app->cap_flushes * 2 : 8;
        nf_flush_t *grown = realloc(app->flushes, cap * sizeof(*grown));
        if (grown == NULL) { /* early rather than never, as a teardown flush always did */
            FIRC_WARN("no memory to queue the flush of group %s; flushing now", who);
            withdraw(app, iface, tun_dev(app, iface), value, g->id, who,
                     sweep ? WD_SWEEP : narrowed ? WD_UNCOND : WD_FLUSH, false, false, close, NULL);
            return;
        }
        app->flushes = grown;
        app->cap_flushes = cap;
    }
    nf_flush_t *e = &app->flushes[app->n_flushes++];
    e->value = value;
    e->owner = g->id;
    snprintf(e->owner_s, sizeof(e->owner_s), "%s", who);
    snprintf(e->iface, sizeof(e->iface), "%s", iface != NULL ? iface : "");
    e->dev = tun_dev(app, iface);
    e->ticket = ticket;
    e->narrowed = narrowed;
    e->sweep = sweep;
    e->close = (firc_nf_prefixes_t){NULL, 0};
    e->lost = (firc_nf_prefixes_t){NULL, 0};
    if (!prefixes_add_v4(sweep ? &e->lost : &e->close, close->v, close->n)) { note_close_lost(who); }
}

static void release_link(firc_ipset_to_link_t *link, const firc_ipset_to_link_t *newer) {
    firc_err_t err = firc_ipset_to_link_release(link, newer);
    if (err != FIRC_OK) { FIRC_WARN("a withdrawn group's ip rules and routes: %s", firc_err_str(err)); }
}

static firc_err_t hold_routing(firc_app_t *app, firc_ruleset_t *rs) {
    firc_id_t owner = firc_ruleset_group(rs)->id;
    firc_ipset_to_link_t *link = firc_ruleset_disable_hold(rs);
    if (link == NULL) { return FIRC_OK; }
    if (app->n_held == app->cap_held) {
        size_t cap = app->cap_held != 0 ? app->cap_held * 2 : 8;
        nf_held_t *grown = realloc(app->held, cap * sizeof(*grown));
        if (grown == NULL) {
            release_link(link, NULL);
            return FIRC_ERR_NOMEM;
        }
        app->held = grown;
        app->cap_held = cap;
    }
    app->held[app->n_held++] = (nf_held_t){link, owner, ++app->nf_change};
    return FIRC_OK;
}

static void release_held(firc_app_t *app, uint64_t ticket, const firc_id_t *owner, const firc_ipset_to_link_t *newer) {
    size_t kept = 0;
    for (size_t i = 0; i < app->n_held; i++) {
        if (owner != NULL ? !firc_id_equal(app->held[i].owner, *owner) : app->held[i].ticket > ticket) {
            app->held[kept++] = app->held[i];
            continue;
        }
        release_link(app->held[i].link, newer);
    }
    app->n_held = kept;
}

/* an owner routed again the same way (A->B->A) holds its field again, so its flows are skipped unpaid */
static void pay_flushes(firc_app_t *app, uint64_t ticket) {
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (!firc_ruleset_take_flush(rs)) { continue; }
        char who[FIRC_ID_STR_LEN];
        firc_id_format(firc_ruleset_group(rs)->id, who);
        firc_nf_prefixes_t close;
        close_capture(app, rs, &close);
        withdraw(app, firc_ruleset_active_iface(rs), group_tunnel(app, rs), firc_ruleset_mark_field(rs),
                 firc_ruleset_group(rs)->id, who, WD_UNCOND, false, false, &close, NULL);
        firc_nf_prefixes_clear(&close);
    }
    size_t kept = 0;
    for (size_t i = 0; i < app->n_flushes; i++) {
        nf_flush_t e = app->flushes[i];
        if (e.ticket > ticket) {
            app->flushes[kept++] = e;
            continue;
        }
        firc_ruleset_t *rs = firc_app_find_group_by_id(app, e.owner);
        const char *now = rs != NULL ? firc_ruleset_active_iface(rs) : NULL;
        bool again = rs != NULL && firc_ruleset_routed(rs) && now != NULL && strcmp(now, e.iface) == 0 &&
                     firc_ruleset_mark_field(rs) == e.value;
        if (again && !e.narrowed) {
            if (e.sweep) {
                withdraw(app, e.iface, e.dev, e.value, e.owner, e.owner_s, WD_SWEEP, false, false, &e.lost, NULL);
            } else {
                FIRC_DEBUG("group %s routes through %s again: its flows are its own, not flushed", e.owner_s, e.iface);
            }
            flush_entry_clear(&e);
            continue;
        }
        bool joined = prefixes_add_v4(&e.close, e.lost.v, e.lost.n);
        withdraw(app, e.iface, e.dev, e.value, e.owner, e.owner_s, e.narrowed ? WD_UNCOND : WD_FLUSH, e.sweep, rs == NULL,
                 &e.close, joined ? NULL : &e.lost);
        flush_entry_clear(&e);
    }
    app->n_flushes = kept;
    release_held(app, ticket, NULL, NULL);
}

static uint64_t real_mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static int64_t real_wall_s(void) {
    return (int64_t)time(NULL);
}

static bool retry_pending(firc_ruleset_t *rs) {
    return firc_ruleset_group(rs)->enable && !firc_ruleset_runtime_enabled(rs) &&
           firc_ruleset_retry(rs)->backoff_ms != 0;
}

static void on_retry_timer(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_app_t *app = ud;
    app->retry_timer = 0; /* a one-shot: the loop retired it */
    app->retry_timer_at = 0;
    (void)firc_app_retry_groups(app, NULL); /* re-arms for what is left */
}

/* keeps one loop timer armed for the earliest due retry; makes a try happen even if nothing else arrives */
static void retry_rearm(firc_app_t *app) {
    uint64_t earliest = 0;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (!retry_pending(rs)) { continue; }
        uint64_t at = firc_ruleset_retry(rs)->at_ms;
        if (earliest == 0 || at < earliest) { earliest = at; }
    }
    if (app->retry_timer != 0 && earliest == app->retry_timer_at) { return; }
    if (app->retry_timer != 0) { (void)firc_loop_del_timer(app->loop, app->retry_timer); }
    app->retry_timer = 0;
    app->retry_timer_at = 0;
    if (earliest == 0 || app->loop == NULL || !app->running) { return; } /* nothing is tried before the app runs */
    uint64_t now = app->mono_ms();
    uint64_t delay = earliest > now ? earliest - now : 1; /* 0 would disarm a timerfd */
    int id = 0;
    if (firc_loop_add_timer(app->loop, delay, 0, on_retry_timer, app, &id) != FIRC_OK) {
        FIRC_WARN("could not arm the retry of a group that failed to come up; the next link "
                  "event or completed pass tries it");
        return;
    }
    app->retry_timer = id;
    app->retry_timer_at = earliest;
    app->retry_timer_delay = delay;
}

/* 1s after the first failure, doubling to 60s; fresh (not a retry's own failure) restarts the run at 1s */
static void retry_note_failure(firc_app_t *app, firc_ruleset_t *rs, bool fresh) {
    firc_ruleset_retry_t *r = firc_ruleset_retry(rs);
    if (fresh) { *r = (firc_ruleset_retry_t){0}; }
    uint32_t next = r->backoff_ms == 0 ? FIRC_APP_RETRY_MIN_MS : r->backoff_ms * 2u;
    r->backoff_ms = next < FIRC_APP_RETRY_MAX_MS ? next : FIRC_APP_RETRY_MAX_MS;
    r->at_ms = app->mono_ms() + r->backoff_ms;
    retry_rearm(app);
}

static void retry_note_success(firc_app_t *app, firc_ruleset_t *rs) {
    const char *name = firc_ruleset_group(rs)->name;
    (void)firc_logrep_ok(&firc_ruleset_retry(rs)->said, NULL, "group \"%s\"", name != NULL ? name : "");
    *firc_ruleset_retry(rs) = (firc_ruleset_retry_t){0};
    retry_rearm(app);
}

#define FIRC_APP_GROUP_ERROR_AFTER_MS (5u * 60u * 1000u)

static void group_failed(firc_app_t *app, firc_ruleset_t *rs, const char *step, firc_err_t err, bool fresh,
                         char *why, size_t why_len) {
    const char *name = firc_ruleset_group(rs)->name;
    char line[256];
    snprintf(line, sizeof(line), "group \"%s\": %s: %s", name != NULL ? name : "", step != NULL ? step : "enable",
             firc_err_str(err));
    retry_note_failure(app, rs, fresh);
    firc_logrep_fail_for(&firc_ruleset_retry(rs)->said, FIRC_APP_GROUP_ERROR_AFTER_MS, NULL,
                         "%s; trying again, spaced up to every %u s, until it comes up", line,
                         (unsigned)(FIRC_APP_RETRY_MAX_MS / 1000u));
    if (why != NULL && why_len > 0) { snprintf(why, why_len, "%s", line); }
}

static firc_err_t app_group_up_ex(firc_app_t *app, firc_ruleset_t *rs, char *why, size_t why_len,
                                  bool retry) {
    const firc_group_t *g = firc_ruleset_group(rs);
    const char *step = NULL;
    firc_ifchoice_t at = app->ifaces.pick != NULL ? app->ifaces.pick(app->ifaces.ud, g) : (firc_ifchoice_t){0, false};
    firc_ruleset_set_start(rs, at.active, at.all_dead);
    firc_err_t err = firc_ruleset_enable(rs, &step);
    if (err != FIRC_OK) {
        group_failed(app, rs, step, err, !retry, why, why_len);
    } else {
        retry_note_success(app, rs);
        if (firc_ruleset_routed(rs)) { release_held(app, 0, &g->id, firc_ruleset_link(rs)); }
    }
    app_request_more_locked(app);
    return err;
}

static firc_err_t app_group_up(firc_app_t *app, firc_ruleset_t *rs, char *why, size_t why_len) {
    return app_group_up_ex(app, rs, why, why_len, false);
}

/* an rtnetlink error is logged and returned, but no caller stops on it: the next enable deletes a stale rule first */
static firc_err_t app_group_down(firc_app_t *app, firc_ruleset_t *rs) {
    const firc_group_t *g = firc_ruleset_group(rs);
    bool had_chain = firc_ruleset_routed(rs);
    uint32_t value = firc_ruleset_mark_field(rs);
    firc_nf_prefixes_t close = {NULL, 0};
    if (had_chain && value != 0) { close_capture(app, rs, &close); }
    char was[FIRC_IFACE_NAME_CAP];
    snprintf(was, sizeof(was), "%s", firc_ruleset_active_iface(rs));
    firc_err_t err = had_chain && app->committer != NULL ? hold_routing(app, rs) : firc_ruleset_disable(rs);
    if (err != FIRC_OK) {
        FIRC_WARN("group \"%s\": teardown: %s", g->name != NULL ? g->name : "", firc_err_str(err));
    }
    if (had_chain) {
        /* queued even when it keeps its routing: pay_flushes skips it if the group comes back on the same interface/field */
        if (value != 0) { flush_queue_add(app, value, g, was, FLUSH_TEARDOWN, &close); }
        app_request_more_locked(app);
    }
    firc_nf_prefixes_clear(&close);
    return err;
}

/* newly routed: its pairs are uncommitted, so their next answer waits for a pass whose snapshot stages the chain */
static void hold_if_newly_routed(firc_app_t *app, firc_ruleset_t *rs, bool was_routed) {
    if (was_routed || app->pool == NULL || !firc_ruleset_routed(rs)) { return; }
    char id[FIRC_ID_STR_LEN];
    firc_id_format(firc_ruleset_group(rs)->id, id);
    if (firc_fakeip_uncommit_group(app->pool, id) > 0) { firc_app_pool_changed(app); }
}

/* the flush waits for the pass that writes the narrower chain, or the old one would mark the next packet again */
static void note_narrowed(firc_app_t *app, firc_ruleset_t *rs) {
    uint32_t value = firc_ruleset_mark_field(rs);
    if (value == 0 || !firc_ruleset_routed(rs)) { return; }
    const firc_group_t *g = firc_ruleset_group(rs);
    firc_nf_prefixes_t close;
    close_capture(app, rs, &close);
    if (app->committer != NULL) {
        flush_queue_add(app, value, g, firc_ruleset_active_iface(rs), FLUSH_NARROWED, &close);
    } else {
        char who[FIRC_ID_STR_LEN];
        firc_id_format(g->id, who);
        withdraw(app, firc_ruleset_active_iface(rs), group_tunnel(app, rs), value, g->id, who, WD_UNCOND, false, false,
                 &close, NULL);
    }
    firc_nf_prefixes_clear(&close);
}

typedef struct {
    firc_nf_prefixes_t p;
    bool routed;
    bool ok;
} subnets_before_t;

static subnets_before_t subnets_before(const firc_ruleset_t *rs) {
    subnets_before_t b = {{NULL, 0}, firc_ruleset_routed(rs), true};
    if (b.routed) { b.ok = firc_ruleset_prefixes(rs, &b.p) == FIRC_OK; }
    return b;
}

static bool queue_field_sweep(firc_app_t *app, firc_ruleset_t *rs, const firc_nf_prefixes_t *lost) {
    uint32_t value = firc_ruleset_mark_field(rs);
    if (value == 0 || !firc_ruleset_routed(rs)) { return false; }
    const firc_group_t *g = firc_ruleset_group(rs);
    if (app->committer != NULL) {
        flush_queue_add(app, value, g, firc_ruleset_active_iface(rs), FLUSH_SWEEP, lost);
        return true;
    }
    char who[FIRC_ID_STR_LEN];
    firc_id_format(g->id, who);
    withdraw(app, firc_ruleset_active_iface(rs), group_tunnel(app, rs), value, g->id, who, WD_SWEEP, false, false, lost,
             NULL);
    return false;
}

static void note_subnets_shrunk(firc_app_t *app, firc_ruleset_t *rs, subnets_before_t *b) {
    firc_nf_prefixes_t now = {NULL, 0}, lost = {NULL, 0};
    bool both = b->routed && firc_ruleset_routed(rs);
    bool got = both && b->ok && firc_ruleset_prefixes(rs, &now) == FIRC_OK;
    bool shrunk = both && (!got || firc_nf_prefixes_narrow(&b->p, &now));
    if (shrunk && got) { lost_capture(app, rs, &b->p, &now, &lost); }
    if (shrunk && !got && group_tunnel(app, rs) >= 0) {
        char id[FIRC_ID_STR_LEN];
        firc_id_format(firc_ruleset_group(rs)->id, id);
        FIRC_WARN("group %s: no memory to read the prefixes it lost; their redirected flows end on their own", id);
    }
    firc_nf_prefixes_clear(&now);
    firc_nf_prefixes_clear(&b->p);
    b->routed = false;
    if (shrunk) { (void)queue_field_sweep(app, rs, &lost); }
    firc_nf_prefixes_clear(&lost);
}

/* kept across a teardown so the enable after it can compare; ok false (OOM) counts as a narrowing */
typedef struct {
    firc_nf_devices_t dev;
    bool ok;
} devices_before_t;

static devices_before_t devices_before(const firc_ruleset_t *rs) {
    devices_before_t b = {{0}, true};
    const firc_nf_devices_t *d = firc_ruleset_devices(rs);
    if (d != NULL) { b.ok = firc_nf_devices_copy(&b.dev, d) == FIRC_OK; }
    return b;
}

/* a host table collected since the render can move a device with no edit, so a later refresh would compare equal */
static bool devices_narrowed_since(const firc_ruleset_t *rs, const devices_before_t *b) {
    const firc_nf_devices_t *now = firc_ruleset_devices(rs);
    if (now == NULL) { return false; }
    return !b->ok || firc_nf_devices_narrows(&b->dev, now);
}

static void republish_or_log(firc_app_t *app) {
    firc_err_t err = firc_app_republish_dns_snapshot(app);
    if (err != FIRC_OK) { FIRC_ERROR("failed to republish DNS-matching snapshot: %s", firc_err_str(err)); }
}

/* drops the snapshot before building the next one: holding both has OOM-killed the daemon on a large list before */
static void release_dns_snapshot(firc_app_t *app) {
    if (app->pipeline != NULL) { firc_dns_pipeline_set_snapshot(app->pipeline, NULL); }
}

/* without this a DELETE mid-sync leaves the socket held forever: the worker's result is dropped for a gone owner */
static void sync_end_for_removed_list(firc_app_t *app, firc_id_t owner, firc_group_list_t *l,
                                      const char *why) {
    if (app->sync_listener == NULL) { return; }
    l->sync_state = FIRC_SUB_SYNC_ERROR;
    snprintf(l->sync_error, sizeof(l->sync_error), "%s", why);
    app->sync_listener(app->sync_listener_ud, owner, l, true);
}

/* an idle list has no stream open; handle_get_sync_events answers one with its terminal event directly */
static void end_sync_in_flight(firc_app_t *app, firc_group_t *g, const char *why) {
    if (g->list == NULL) { return; }
    if (g->list->sync_state != FIRC_SUB_SYNC_QUEUED && g->list->sync_state != FIRC_SUB_SYNC_FETCHING) {
        return;
    }
    sync_end_for_removed_list(app, g->id, g->list, why);
}

/* a firmware-listed resolver inside firc's own pool routes nowhere; filtered here, the one place both are held */
static size_t drop_pool_addrs(firc_resolver_addr_t *arr, size_t n, const firc_fakeip_t *pool) {
    if (pool == NULL) { return n; }
    size_t kept = 0;
    for (size_t i = 0; i < n; i++) {
        if (firc_fakeip_overlaps(pool, &arr[i].ip, (uint8_t)(arr[i].ip.len * 8u))) { continue; }
        arr[kept++] = arr[i];
    }
    return kept;
}

void firc_app_republish_resolve_routes(firc_app_t *app) {
    if (app == NULL || app->router == NULL) { return; }
    size_t n = app->n_rulesets;
    firc_resolve_input_t *in = n ? calloc(n, sizeof(*in)) : NULL;
    firc_resolver_addr_t (*fw)[FIRC_RESOLVE_MAX_SERVERS] = n ? calloc(n, sizeof(*fw)) : NULL;
    if (n != 0 && (in == NULL || fw == NULL)) {
        free(in);
        free(fw);
        FIRC_WARN("failed to rebuild the group resolver routes: out of memory");
        return;
    }
    for (size_t i = 0; i < n; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        const firc_group_t *g = firc_ruleset_group(rs);
        uint32_t field = firc_ruleset_mark_field(rs); /* shifted, 0 when not in the kernel */
        const char *active = firc_ruleset_active_iface(rs);
        in[i].group = g;
        in[i].iface = active;
        in[i].mark = (field != 0 && firc_ruleset_runtime_enabled(rs) && g->enable) ? (field | FIRC_MARK_HANDLED) : 0;
        bool blocked = false;
        (void)firc_ruleset_active(rs, &blocked);
        in[i].blackhole = blocked || firc_iface_is_sink(active);
        in[i].v6_route = firc_ruleset_has_iface_route(rs, AF_INET6);
        in[i].ktcp = ktcp_redirected(app, group_tunnel(app, rs)) && app->last_off == FIRC_KTCP_OFF_NONE;
        in[i].firmware = fw[i];
        in[i].n_firmware = firc_kn_resolvers_for(app->resolvers, active, fw[i], FIRC_RESOLVE_MAX_SERVERS);
        in[i].n_firmware = drop_pool_addrs(fw[i], in[i].n_firmware, app->pool);
    }
    firc_resolve_table_t t;
    firc_err_t err = firc_resolve_table_build(in, n, firc_resolve_router_table(app->router), &t);
    free(in);
    free(fw);
    if (err != FIRC_OK) {
        FIRC_WARN("failed to rebuild the group resolver routes: %s", firc_err_str(err));
        return;
    }
    /* computed before the publish frees the old table; on OOM nothing is closed, sockets just stay on the old route */
    const firc_resolve_table_t *prev = firc_resolve_router_table(app->router);
    size_t n_prev = prev != NULL ? prev->n : 0;
    firc_id_t *stale = n_prev ? calloc(2 * n_prev, sizeof(*stale)) : NULL;
    size_t n_stale = stale != NULL ? firc_resolve_table_stale(prev, &t, stale, n_prev) : 0;
    size_t n_gone = stale != NULL ? firc_resolve_table_gone(prev, &t, stale + n_prev, n_prev) : 0;
    firc_resolve_router_publish(app->router, &t);
    for (size_t i = 0; app->proxy != NULL && i < n_stale; i++) {
        firc_dnsproxy_close_group_pools(app->proxy, stale[i]);
    }
    for (size_t i = 0; app->proxy != NULL && i < n_gone; i++) {
        firc_dnsproxy_forget_group(app->proxy, stale[n_prev + i]);
    }
    free(stale);
}

const firc_fakeip_t *firc_app_pool(const firc_app_t *app) {
    return app != NULL ? app->pool : NULL;
}

void firc_app_group_resolver(const firc_app_t *app, const firc_group_t *g, firc_app_resolver_info_t *out) {
    memset(out, 0, sizeof(*out));
    firc_resolve_route_t r;
    r.gen = 0;
    if (app != NULL && firc_resolve_router_route(app->router, g->id, &r)) {
        out->source = r.source;
        if (firc_resolve_route_usable(&r)) { /* a route with no mark lists no servers: queries go to the common upstream */
            out->n_servers = r.n_servers;
            memcpy(out->servers, r.servers, sizeof(out->servers));
        }
    } else { /* no router: not published yet */
        out->source = g->resolve.tunnel ? FIRC_RESOLVE_SOURCE_NONE : FIRC_RESOLVE_SOURCE_OFF;
    }
    if (app != NULL) { out->wall_s = app->wall_s(); }
    if (app != NULL && app->proxy != NULL && out->n_servers > 0) {
        firc_dnsproxy_group_health(app->proxy, g->id, r.gen, &out->health);
    }
}

static bool group_in_view(const firc_group_t *g, void *ud) {
    return firc_ruleset_in_view(firc_app_find_group_by_id(ud, g->id));
}

/* the DNS-matching snapshot alone; a capture reads this same snapshot fresh from the pipeline, no second holder */
static firc_err_t republish_dns_matching_only(firc_app_t *app) {
    /* while running, the view is the groups actually in it (a failed enable is left out); before, the config is the view */
    firc_ruleset_snapshot_t *snap = app->running ? firc_ruleset_snapshot_build_where(app->cfg, group_in_view, app)
                                                 : firc_ruleset_snapshot_build(app->cfg);
    if (snap == NULL) { return FIRC_ERR_NOMEM; }
    if (app->pipeline != NULL) {
        firc_dns_pipeline_set_snapshot(app->pipeline, snap);
    } else {
        firc_ruleset_snapshot_free(snap);
    }
    return FIRC_OK;
}

/* resolve routes read the kernel's marks, not the snapshot: a just-enabled group needs its route either way */
firc_err_t firc_app_republish_dns_snapshot(firc_app_t *app) {
    firc_err_t err = republish_dns_matching_only(app);
    firc_app_republish_resolve_routes(app);
    return err;
}

firc_app_t *firc_app_create(const firc_app_deps_t *deps) {
    firc_app_t *app = calloc(1, sizeof(*app));
    if (!app) { return NULL; }
    app->cfg = deps->cfg;
    app->ipt4 = deps->ipt4;
    app->ipt6 = deps->ipt6;
    app->rtnl = deps->rtnl;
    app->ct = deps->ct;
    app->sets = deps->sets;
    app->router = deps->router;
    app->resolvers = deps->resolvers;
    app->pool = deps->pool;
    /* pool prefixes never change; copied so a pass reads no live pool */
    for (unsigned fam = 0; fam < 2; fam++) {
        app->reject_has[fam] = deps->pool != NULL &&
            firc_fakeip_pool_prefix(deps->pool, fam == 0 ? FIRC_FAM_V4 : FIRC_FAM_V6, &app->reject_base[fam], &app->reject_len[fam]);
    }
    app->loop = deps->loop;
    app->tap_nflog_open = deps->nflog_open != NULL ? deps->nflog_open : firc_nflog_open;
    app->tap_iface_index = deps->iface_index != NULL ? deps->iface_index : if_nametoindex;
    app->tap_seconds = deps->capture_seconds > 0 ? deps->capture_seconds : FIRC_CAPTURE_SECONDS;
    app->mono_ms = deps->mono_ms != NULL ? deps->mono_ms : real_mono_ms;
    app->wall_s = deps->wall_s != NULL ? deps->wall_s : real_wall_s;
    app->proxy = deps->proxy;
    app->pool_snap = firc_fakeip_snapshot_take(deps->pool); /* so a pass before anything is issued still has a consistent view */
    pthread_mutex_init(&app->snap_mu, NULL);
    app->pipeline = deps->pipeline;
    app->config_path = deps->config_path;
    app->config_version = deps->config_version;
    app->start_idx = deps->start_idx;
    if (app->loop != NULL && (app->al_ptr = firc_ptr_new(app->loop, 3000)) == NULL) {
        firc_app_destroy(app);
        return NULL;
    }

    firc_ruleset_deps_t rdeps = ruleset_deps(app);
    for (size_t i = 0; i < app->cfg->n_groups; i++) {
        firc_ruleset_t *rs = firc_ruleset_new(app->cfg->groups[i], &rdeps);
        if (!rs || rulesets_push(app, rs) != FIRC_OK) {
            firc_ruleset_free(rs);
            firc_app_destroy(app);
            return NULL;
        }
    }
    if (firc_app_config_copy(&app->saved, &app->cfg->app) != FIRC_OK) {
        firc_app_destroy(app);
        return NULL;
    }
    return app;
}

static firc_err_t firc_app_stage_pool_reject_one(firc_app_t *app, unsigned fam) {
    firc_ipt_t *engine = fam == 0 ? app->ipt4 : app->ipt6;
    if (engine == NULL || !app->reject_has[fam]) { return FIRC_OK; }
    return firc_pool_reject_build_rules(engine, app->cfg->app.netfilter.iptables.chain_prefix, &app->reject_base[fam],
                                        app->reject_len[fam]);
}

static firc_err_t stop_step(void *ud) {
    firc_app_t *app = ud;
    firc_ipt_t *engines[] = {app->ipt4, app->ipt6};
    firc_err_t first = FIRC_OK;
    for (unsigned i = 0; i < 2; i++) {
        if (engines[i] == NULL) { continue; }
        firc_ipt_discard(engines[i]);
        firc_err_t err = firc_netfilter_register_base_chains(engines[i], NULL);
        if (err == FIRC_OK) { err = firc_app_stage_pool_reject_one(app, i); }
        if (err == FIRC_OK) {
            err = firc_ipt_commit_full(engines[i]);
        } else {
            firc_ipt_discard(engines[i]);
        }
        if (err != FIRC_OK && first == FIRC_OK) { first = err; }
    }
    return first;
}

static firc_err_t write_stop_state(firc_app_t *app) {
    firc_err_t err = firc_netfilter_retry_races(stop_step, app);
    if (err != FIRC_OK) {
        FIRC_WARN("netfilter not cleaned at stop (%s): the groups keep their ip rules and routes, so a rewritten "
                  "packet still leaves by its group; the next start sweeps them", firc_err_str(err));
    }
    return err;
}

void firc_app_destroy(firc_app_t *app) {
    if (!app) { return; }
    firc_app_stop_netfilter_committer(app); /* nothing below is safe while a rebuild can still read the registry */
    if (app->retry_timer != 0) { (void)firc_loop_del_timer(app->loop, app->retry_timer); }
    app->retry_timer = 0;
    if (app->set_retry_timer != 0) { (void)firc_loop_del_timer(app->loop, app->set_retry_timer); }
    app->set_retry_timer = 0;
    firc_app_stop_list_worker(app); /* before the config it reports about goes: a result it posts could reach a loop whose app no longer exists */
    (void)firc_app_capture_stop(app, NULL);
    autolist_stop(app);
    firc_ptr_free(app->al_ptr);
    app->al_ptr = NULL;
    firc_err_t stopped = write_stop_state(app); /* no pass will run again, so write directly; before the flushes, so a flushed flow meets no chain to re-mark it */
    (void)firc_purge_sets(app->sets, app->cfg->app.netfilter.iptables.chain_prefix, NULL);
    for (size_t i = 0; stopped != FIRC_OK && i < app->n_held; i++) { firc_ipset_to_link_free(app->held[i].link); }
    if (stopped != FIRC_OK) { app->n_held = 0; }
    memset(&app->ktcp_sink, 0, sizeof(app->ktcp_sink));
    pay_flushes(app, UINT64_MAX); /* before the groups go, so an owner routed again is still recognised */
    while (app->n_waits > 0) { wait_pay_at(app, 0); }
    if (app->wait_timer != 0) { (void)firc_loop_del_timer(app->loop, app->wait_timer); }
    app->wait_timer = 0;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        if (stopped == FIRC_OK) {
            firc_ruleset_disable(app->rulesets[i]); /* pool is saved/restored, same names/addrs/marks, so in-flight flows stay correct across a restart */
        }
        firc_ruleset_free(app->rulesets[i]);
    }
    free(app->rulesets);
    free(app->flushes);
    free(app->waits);
    free(app->held);
    free(app->set_recs);
    free(app->sets_swept);
    free(app->set_left);
    firc_fakeip_snapshot_free(app->pool_snap);
    app->pool_snap = NULL;
    for (size_t i = 0; i < app->n_retired; i++) { firc_fakeip_snapshot_free(app->retired[i]); }
    free(app->retired);
    firc_ktcp_image_free(&app->pass_image);
    firc_ktcp_image_free(&app->last_image);
    pthread_mutex_destroy(&app->snap_mu);
    firc_app_config_clear(&app->saved);
    free(app);
}

void firc_app_set_running(firc_app_t *app, bool running) {
    app->running = running;
}
bool firc_app_is_running(const firc_app_t *app) { return app->running; }

/* enabled as a mutation would, chain by the committer's first full pass, so nothing here writes iptables directly */
firc_err_t firc_app_start_groups(firc_app_t *app, const firc_tunnels_t *ts) {
    app_nf_enter(app);
    firc_tunnels_names(ts, app->tun_names);
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        const char *step = NULL;
        firc_err_t err = firc_ruleset_enable(rs, &step);
        if (err != FIRC_OK) { group_failed(app, rs, step, err, true, NULL, 0); }
    }
    app->running = true;
    retry_rearm(app); /* the failures above could not arm it before running */
    firc_nfcommit_request_more(app->committer); /* no-op in the daemon (committer starts after this); a caller with one running still gets a pass */
    firc_err_t err = firc_app_republish_dns_snapshot(app); /* also republishes resolve routes */
    app_nf_leave(app);
    if (err != FIRC_OK) { FIRC_ERROR("failed to publish the DNS view: %s", firc_err_str(err)); }
    return err;
}

size_t firc_app_user_group_count(const firc_app_t *app) {
    return app->n_rulesets;
}

firc_ruleset_t *firc_app_user_group_at(const firc_app_t *app, size_t idx) {
    return idx < app->n_rulesets ? app->rulesets[idx] : NULL;
}

firc_ruleset_t *firc_app_find_group_by_id(const firc_app_t *app, firc_id_t id) {
    for (size_t i = 0; i < app->n_rulesets; i++) {
        if (firc_id_equal(firc_ruleset_group(app->rulesets[i])->id, id)) { return app->rulesets[i]; }
    }
    return NULL;
}

/* rolls the add back on failure; only while running and only for a single add */
static firc_err_t activate_group_at(firc_app_t *app, size_t idx, char *why, size_t why_len) {
    firc_ruleset_t *rs = app->rulesets[idx];
    firc_err_t err = app_group_up(app, rs, why, why_len); /* carries the prefixes already */
    if (err == FIRC_OK) { hold_if_newly_routed(app, rs, false); } /* defensive: an id re-added after a delete may have parked mappings */
    if (err != FIRC_OK) {
        rulesets_remove_at(app, idx); /* the enable rolled itself back; the next pass drops its chain */
        firc_ruleset_free(rs);
        /* no snapshot release: it borrows nothing of this group, built before the group existed */
        end_sync_in_flight(app, app->cfg->groups[idx], "group removed");
        firc_config_remove_group_by_index(app->cfg, idx);
        retry_rearm(app); /* the failure armed a try for a group that is gone */
    }
    return err;
}

/* activate false adds without enabling: a batch adds every group first, activates them afterwards */
static firc_err_t firc_app_add_group_unlocked(firc_app_t *app, firc_group_t *group, bool activate,
                                              char *why, size_t why_len) {
    firc_err_t err = FIRC_OK; /* every refusal below frees group; in a replace it may carry a list whose sync-in-flight stream ends here */
    for (size_t i = 0; err == FIRC_OK && i < app->cfg->n_groups; i++) {
        if (firc_id_equal(app->cfg->groups[i]->id, group->id)) { err = FIRC_ERR_EXIST; }
    }
    if (err == FIRC_OK) {
        err = firc_group_check_rule_ids(group);
        if (err == FIRC_ERR_EXIST) { err = FIRC_ERR_INVAL; }
    }
    if (err == FIRC_OK) { err = firc_config_add_group(app->cfg, group); }
    if (err != FIRC_OK) {
        end_sync_in_flight(app, group, "group removed");
        firc_group_free(group);
        return err;
    }
    size_t group_idx = app->cfg->n_groups - 1;

    firc_ruleset_deps_t rdeps = ruleset_deps(app);
    firc_ruleset_t *rs = firc_ruleset_new(app->cfg->groups[group_idx], &rdeps);
    err = rs != NULL ? rulesets_push(app, rs) : FIRC_ERR_NOMEM;
    if (err != FIRC_OK) {
        firc_ruleset_free(rs);
        end_sync_in_flight(app, group, "group removed");
        firc_config_remove_group_by_index(app->cfg, group_idx);
        return err;
    }

    if (app->running && activate) {
        err = activate_group_at(app, app->n_rulesets - 1, why, why_len);
        if (err != FIRC_OK) { return err; }
    }
    if (activate) { /* a batch republishes once and asks for missing lists once, in firc_app_replace_groups; a single add (POST /groups) does both here */
        republish_or_log(app);
        if (group->list != NULL) { firc_app_request_sync_missing_unlocked(app); }
    }
    return FIRC_OK;
}

firc_err_t firc_app_add_group_why(firc_app_t *app, firc_group_t *group, char *why, size_t why_len) {
    app_nf_enter(app);
    firc_err_t r = firc_app_add_group_unlocked(app, group, true, why, why_len);
    app_nf_leave(app);
    return r;
}

firc_err_t firc_app_add_group(firc_app_t *app, firc_group_t *group) {
    return firc_app_add_group_why(app, group, NULL, 0);
}

/* asked of the kernel each call, on the loop that owns app->rtnl, so it is never stale; blackhole needs no link */
static bool iface_link_up(const firc_app_t *app, const char *iface) {
    if (firc_iface_is_sink(iface)) { return true; }
    if (app->rtnl == NULL || iface == NULL) { return false; }
    firc_link_info_t li;
    bool found = false;
    if (firc_rtnl_link_by_name(app->rtnl, iface, &li, &found) != FIRC_OK) { return false; }
    return found && li.up;
}

bool firc_app_group_live(const firc_app_t *app, firc_id_t id, const char **reason) {
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    const char *why = NULL;
    bool blocked = false;
    const char *active = NULL;
    if (rs != NULL) {
        (void)firc_ruleset_active(rs, &blocked);
        active = firc_ruleset_active_iface(rs);
    }
    if (rs != NULL && !firc_ruleset_group(rs)->enable) {
        why = "disabled";
    } else if (rs == NULL || !firc_ruleset_runtime_enabled(rs)) {
        why = "not-enabled";
    } else if (app->dns_down) {
        why = "dns-down";
    } else if (!firc_ruleset_routed(rs)) {
        why = "no-interface";
    } else if (blocked) {
        why = "no-interface";
    } else if (app->tunnel_down != NULL && app->tunnel_down(app->tunnel_down_ud, active)) {
        why = "tunnel-down";
    } else if (!iface_link_up(app, active)) {
        why = "no-interface"; /* routing itself does not read the link; the table's blackhole default routes nowhere either way */
    } else {
        /* no false live: before the first pass, or after 5s of failures, the chain may not be in the kernel */
        firc_nfcommit_health_t h;
        firc_app_netfilter_health(app, &h);
        if (h.failing || h.first_pending || firc_ruleset_pass(rs) == FIRC_RULESET_PASS_LEFT_OUT ||
            firc_ruleset_sets_stale(rs)) {
            why = "not-written";
        }
    }
    if (reason != NULL) { *reason = why; }
    return why == NULL;
}

static bool retry_due(const firc_app_t *app, firc_ruleset_t *rs, const char *iface, uint64_t now) {
    const firc_group_t *g = firc_ruleset_group(rs);
    if (!app->running || !retry_pending(rs) || now < firc_ruleset_retry(rs)->at_ms) { return false; }
    return iface == NULL || firc_group_has_iface(g, iface);
}

bool firc_app_set_retry_armed_for_test(const firc_app_t *app) { return app->set_retry_timer != 0; }

bool firc_app_retry_timer_for_test(const firc_app_t *app, uint64_t *delay_ms) {
    if (delay_ms != NULL) { *delay_ms = app->retry_timer != 0 ? app->retry_timer_delay : 0; }
    return app->retry_timer != 0;
}

/* the due check runs first, without the lock, so nothing-due does not interrupt a pass in flight */
bool firc_app_retry_groups(firc_app_t *app, const char *iface) {
    if (app == NULL) { return false; }
    uint64_t now = app->mono_ms();
    bool any = false;
    for (size_t i = 0; i < app->n_rulesets && !any; i++) { any = retry_due(app, app->rulesets[i], iface, now); }
    if (!any) {
        retry_rearm(app); /* a timer that fired early is armed again for when it is due */
        return false;
    }
    app_nf_enter(app);
    bool came_up = false;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (!retry_due(app, rs, iface, now)) { continue; }
        if (app_group_up_ex(app, rs, NULL, 0, true) == FIRC_OK) {
            hold_if_newly_routed(app, rs, false);
            came_up = true;
        }
    }
    if (came_up) { republish_or_log(app); }
    app_nf_leave(app);
    return came_up;
}

void firc_app_netfilter_health(const firc_app_t *app, firc_nfcommit_health_t *out) {
    firc_nfcommit_health(app != NULL ? app->committer : NULL, out);
}

/* without the lock: firc_app_sync_group is this plus the lock, for a caller not already inside it */
static firc_err_t sync_group_ruleset_locked(firc_app_t *app, firc_ruleset_t *rs) {
    bool chain_changed = false;
    subnets_before_t before = subnets_before(rs);
    firc_err_t r = firc_ruleset_sync(rs, &chain_changed);
    /* asked for even on error: a failed devices render already handed the link new prefixes the next sync would miss */
    if (chain_changed) { app_request_more_locked(app); }
    if (firc_ruleset_devices_narrowed(rs)) { note_narrowed(app, rs); } /* a render that narrowed resets its flows exactly as a narrowing edit does */
    if (!chain_changed) { before.routed = false; }
    note_subnets_shrunk(app, rs, &before);
    return r;
}

firc_err_t firc_app_sync_group(firc_app_t *app, firc_ruleset_t *rs) {
    app_nf_enter(app);
    firc_err_t r = sync_group_ruleset_locked(app, rs);
    app_nf_leave(app);
    return r;
}

static int rule_ptr_cmp(const void *a, const void *b) {
    uintptr_t x = (uintptr_t) * (firc_rule_t *const *)a;
    uintptr_t y = (uintptr_t) * (firc_rule_t *const *)b;
    return x < y ? -1 : x > y;
}

static firc_rule_t **sorted_rules(firc_rule_t *const *v, size_t n) {
    firc_rule_t **s = malloc((n > 0 ? n : 1) * sizeof(*s));
    if (s == NULL) { return NULL; }
    if (n > 0) {
        memcpy(s, v, n * sizeof(*s));
        qsort(s, n, sizeof(*s), rule_ptr_cmp);
    }
    return s;
}

static bool rule_among(const firc_rule_t *r, firc_rule_t *const *v, size_t n, firc_rule_t **sorted) {
    if (sorted != NULL) { return bsearch(&r, sorted, n, sizeof(*sorted), rule_ptr_cmp) != NULL; }
    for (size_t i = 0; i < n; i++) {
        if (v[i] == r) { return true; }
    }
    return false;
}

static bool rules_listed_twice(firc_rule_t *const *v, size_t n) {
    firc_rule_t **s = sorted_rules(v, n);
    bool twice = false;
    for (size_t i = 1; i < n; i++) {
        if (s != NULL ? s[i] == s[i - 1] : rule_among(v[i], v, i, NULL)) {
            twice = true;
            break;
        }
    }
    free(s);
    return twice;
}

static void free_rules_not_in(firc_rule_t **loser, size_t n_loser, firc_rule_t *const *keep, size_t n_keep,
                              bool may_repeat) {
    firc_rule_t **sk = sorted_rules(keep, n_keep);
    firc_rule_t **sl = may_repeat ? sorted_rules(loser, n_loser) : NULL;
    firc_rule_t **walk = sl != NULL ? sl : loser;
    for (size_t i = 0; i < n_loser; i++) {
        if (may_repeat && (sl != NULL ? i > 0 && sl[i] == sl[i - 1] : rule_among(loser[i], loser, i, NULL))) {
            continue;
        }
        if (!rule_among(walk[i], keep, n_keep, sk)) { firc_rule_free(walk[i]); }
    }
    free(sl);
    free(sk);
    free(loser);
}

firc_err_t firc_app_set_hand_rules(firc_app_t *app, firc_ruleset_t *rs, firc_group_t *next, const char *save_path,
                                   const char *version, bool *unsaved) {
    *unsaved = false;
    firc_group_t *g = firc_ruleset_group_mut(rs);
    if (rules_listed_twice(next->rules, next->n_rules)) {
        free_rules_not_in(next->rules, next->n_rules, g->rules, g->n_rules, true);
        return FIRC_ERR_INVAL;
    }
    firc_rule_t **old = g->rules;
    size_t n_old = g->n_rules;
    app_nf_enter(app);
    g->rules = next->rules;
    g->n_rules = next->n_rules;
    firc_err_t err = firc_ruleset_runtime_enabled(rs) ? sync_group_ruleset_locked(app, rs) : FIRC_OK;
    if (err != FIRC_OK) {
        g->rules = old;
        g->n_rules = n_old;
        firc_err_t again = sync_group_ruleset_locked(app, rs);
        if (again != FIRC_OK) {
            FIRC_ERROR("group \"%s\": rules put back, group not synced: %s", g->name != NULL ? g->name : "",
                       firc_err_str(again));
        }
    }
    republish_or_log(app);
    app_nf_leave(app);
    if (err != FIRC_OK) {
        free_rules_not_in(next->rules, next->n_rules, old, n_old, false);
        return err;
    }
    free_rules_not_in(old, n_old, g->rules, g->n_rules, false);
    if (save_path == NULL) { return FIRC_OK; }
    err = firc_app_save_groups(app, save_path, version != NULL ? version : "");
    if (err != FIRC_OK) { *unsaved = true; }
    return err;
}

static bool id_among(firc_id_t id, firc_group_t *const *keep, size_t n_keep) {
    for (size_t i = 0; i < n_keep; i++) {
        if (keep[i] != NULL && firc_id_equal(keep[i]->id, id)) { return true; }
    }
    return false;
}

/* keep: groups about to be re-added under the same ids, whose mappings must survive the replace */
static void clear_groups_keeping(firc_app_t *app, firc_group_t *const *keep, size_t n_keep) {
    release_dns_snapshot(app); /* the snapshot borrows the lists freed below */
    /* forgetting a group may rebuild tables on the spot, walking the ruleset list, so the list is emptied first */
    size_t n_gone = 0;
    firc_id_t *gone = app->n_rulesets ? calloc(app->n_rulesets, sizeof(*gone)) : NULL;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        const firc_group_t *g = firc_ruleset_group(app->rulesets[i]);
        (void)app_group_down(app, app->rulesets[i]);
        if (!id_among(g->id, keep, n_keep)) {
            if (g->list != NULL) {
                sync_end_for_removed_list(app, g->id, g->list, "group removed");
            }
            if (gone != NULL) { gone[n_gone++] = g->id; }
        }
        firc_ruleset_free(app->rulesets[i]);
    }
    app->n_rulesets = 0;
    firc_config_clear_groups(app->cfg);
    for (size_t i = 0; i < n_gone; i++) { pool_forget_group_id(app, gone[i]); }
    free(gone);
    republish_or_log(app);
    retry_rearm(app);
}

static void firc_app_clear_groups_unlocked(firc_app_t *app) {
    clear_groups_keeping(app, NULL, 0);
}


void firc_app_clear_groups(firc_app_t *app) {
    app_nf_enter(app);
    firc_app_clear_groups_unlocked(app);
    app_nf_leave(app);
}

/* the same list under new metadata: moving the arena across avoids a refetch */
static void carry_list(firc_group_list_t *nw, firc_group_list_t *old) {
    /* sync_seq is carried for every same-url arrival, or a Save mid-sync strands the stream at "fetching" */
    nw->sync_state = old->sync_state;
    nw->sync_seq = old->sync_seq;
    nw->sync_progress = old->sync_progress;
    memcpy(nw->sync_error, old->sync_error, sizeof(nw->sync_error));

    firc_sub_rules_free(&nw->rules);
    firc_sub_rules_move(&nw->rules, &old->rules);
    memcpy(nw->body_hash, old->body_hash, sizeof(nw->body_hash));
    nw->has_body_hash = old->has_body_hash;
    nw->last_check = old->last_check;
    nw->failed_fetches = old->failed_fetches;
    nw->last_update = old->last_update; /* the daemon's, never a body's */
    firc_sub_apply_overrides(nw, &nw->rules);

    old->has_body_hash = false;
    memset(old->body_hash, 0, sizeof(old->body_hash));
    old->sync_state = FIRC_SUB_SYNC_IDLE;
    old->last_check = 0;
}

static bool same_url(const firc_group_list_t *a, const firc_group_list_t *b) {
    return strcmp(a->url != NULL ? a->url : "", b->url != NULL ? b->url : "") == 0;
}

/* carried into arrival's list if the url is the same; otherwise the stream watching live's list ends */
static void hand_over_list(firc_app_t *app, firc_group_t *live, firc_group_t *arrival) {
    if (live->list == NULL) { return; }
    if (arrival->list == NULL) {
        sync_end_for_removed_list(app, live->id, live->list, "list removed");
    } else if (same_url(arrival->list, live->list)) {
        carry_list(arrival->list, live->list);
    } else {
        end_sync_in_flight(app, live, "list url changed");
    }
}

firc_err_t firc_app_replace_groups(firc_app_t *app, firc_group_t **groups, size_t n) {
    /* one acquisition for the whole swap, or a committer pass could land between clear() and add() */
    app_nf_enter(app);
    size_t n_was = 0; /* who was routed before the swap; taken before anything moves, so an OOM here applies nothing */
    firc_id_t *was = app->n_rulesets != 0 ? calloc(app->n_rulesets, sizeof(*was)) : NULL;
    if (app->n_rulesets != 0 && was == NULL) {
        app_nf_leave(app);
        for (size_t i = 0; i < n; i++) { firc_group_free(groups[i]); }
        free(groups);
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < app->n_rulesets; i++) {
        if (firc_ruleset_routed(app->rulesets[i])) { was[n_was++] = firc_ruleset_group(app->rulesets[i])->id; }
    }
    size_t n_narrowed = 0; /* groups the swap keeps with a narrower selector, read by id before it moves old selectors out */
    firc_id_t *narrowed = n != 0 ? calloc(n, sizeof(*narrowed)) : NULL;
    /* each routed group's devices copy, to compare the Save's render with, by the same index as was */
    devices_before_t *was_dev = n_was != 0 ? calloc(n_was, sizeof(*was_dev)) : NULL;
    subnets_before_t *was_sub = n_was != 0 ? calloc(n_was, sizeof(*was_sub)) : NULL;
    if ((n != 0 && narrowed == NULL) || (n_was != 0 && (was_dev == NULL || was_sub == NULL))) {
        free(narrowed);
        free(was_dev);
        free(was_sub);
        free(was);
        app_nf_leave(app);
        for (size_t i = 0; i < n; i++) { firc_group_free(groups[i]); }
        free(groups);
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < n; i++) {
        firc_ruleset_t *old = firc_app_find_group_by_id(app, groups[i]->id);
        if (old != NULL && firc_ruleset_routed(old) &&
            firc_devsel_spec_narrows(&firc_ruleset_group(old)->devices, &groups[i]->devices)) {
            narrowed[n_narrowed++] = groups[i]->id;
        }
    }
    for (size_t w = 0; w < n_was; w++) {
        firc_ruleset_t *old = firc_app_find_group_by_id(app, was[w]);
        was_dev[w] = devices_before(old);
        was_sub[w] = subnets_before(old);
    }
    release_dns_snapshot(app); /* before the gate moves any arena or anything is freed: the snapshot borrows the lists */
    for (size_t i = 0; i < n; i++) {
        for (size_t j = 0; j < app->cfg->n_groups; j++) {
            if (firc_id_equal(app->cfg->groups[j]->id, groups[i]->id)) {
                hand_over_list(app, app->cfg->groups[j], groups[i]);
                break;
            }
        }
    }
    clear_groups_keeping(app, groups, n);
    firc_err_t err = FIRC_OK;
    size_t i = 0;
    for (; i < n; i++) {
        err = firc_app_add_group_unlocked(app, groups[i], false, NULL, 0); /* takes ownership, even on failure */
        if (err != FIRC_OK) { break; }
    }
    /* every added group is activated regardless of the one before it; one that fails stays config-only, not-enabled */
    if (app->running) {
        for (size_t k = 0; k < app->n_rulesets; k++) {
            firc_ruleset_t *rs = app->rulesets[k];
            bool was_routed = false;
            size_t at = 0;
            for (size_t w = 0; w < n_was && !was_routed; w++) {
                was_routed = firc_id_equal(was[w], firc_ruleset_group(rs)->id);
                at = w;
            }
            if (app_group_up(app, rs, NULL, 0) == FIRC_OK) {
                hold_if_newly_routed(app, rs, was_routed);
                bool narrows = was_routed && devices_narrowed_since(rs, &was_dev[at]);
                for (size_t w = 0; w < n_narrowed && !narrows; w++) {
                    narrows = firc_id_equal(narrowed[w], firc_ruleset_group(rs)->id);
                }
                if (narrows) { note_narrowed(app, rs); }
            }
            if (was_routed) { note_subnets_shrunk(app, rs, &was_sub[at]); }
        }
    }
    for (size_t w = 0; w < n_was; w++) {
        firc_nf_devices_clear(&was_dev[w].dev);
        firc_nf_prefixes_clear(&was_sub[w].p);
    }
    free(was_dev);
    free(was_sub);
    free(was);
    free(narrowed);
    for (size_t j = i + 1; j < n; j++) {
        end_sync_in_flight(app, groups[j], "group removed");
        firc_group_free(groups[j]);
    }
    republish_or_log(app);
    /* a failed replace does not restore the old set: a group it could not add is gone with its list */
    firc_app_request_sync_missing_unlocked(app);
    app_nf_leave(app);
    free(groups);
    return err;
}

/* moves fields from from into into in place, preserving into's address and id; frees the emptied from shell */
static void group_move_into(firc_app_t *app, firc_group_t *into, firc_group_t *from) {
    free(into->name);
    into->name = from->name;
    free(into->iface);
    into->iface = from->iface;
    for (size_t i = 0; i < into->n_iface_rest; i++) { free(into->iface_rest[i]); }
    memcpy(into->iface_rest, from->iface_rest, sizeof(into->iface_rest));
    into->n_iface_rest = from->n_iface_rest;
    into->enable = from->enable;
    for (size_t i = 0; i < into->n_rules; i++) { firc_rule_free(into->rules[i]); }
    free(into->rules);
    into->rules = from->rules;
    into->n_rules = from->n_rules;
    firc_devsel_spec_clear(&into->devices);
    into->devices = from->devices;
    free(into->resolve.server);
    into->resolve = from->resolve;
    into->block = from->block;
    if (into->list != NULL) { /* hand_over_list: after it, into's list is either carried into from's (blank) or finished with */
        hand_over_list(app, into, from);
        firc_group_list_free(into->list);
    }
    into->list = from->list;
    free(from); /* the shell: everything it held has moved */
}

firc_err_t firc_app_update_group(firc_app_t *app, firc_id_t id, firc_group_t *built) {
    app_nf_enter(app);
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    if (rs == NULL) {
        app_nf_leave(app);
        firc_group_free(built);
        return FIRC_ERR_NOENT;
    }
    firc_group_t *live = firc_ruleset_group_mut(rs);
    bool was_routed = firc_ruleset_routed(rs);
    bool narrowed = was_routed && firc_devsel_spec_narrows(&live->devices, &built->devices); /* read before the move takes the old selector away */
    devices_before_t before = devices_before(rs);
    subnets_before_t sub_before = subnets_before(rs);
    if (firc_ruleset_runtime_enabled(rs)) {
        (void)app_group_down(app, rs); /* an rtnetlink error does not stop the update; the enable below deletes a stale rule before adding its own */
    }
    release_dns_snapshot(app); /* it borrows the list freed by the move */
    group_move_into(app, live, built);
    firc_app_request_sync_missing_unlocked(app); /* a new list, or a new url */
    /* runs whenever the daemon runs, not only when it was before; the move stands whether this enable fails or not */
    if (app->running && app_group_up(app, rs, NULL, 0) == FIRC_OK) {
        hold_if_newly_routed(app, rs, was_routed);
        if (narrowed || (was_routed && devices_narrowed_since(rs, &before))) { note_narrowed(app, rs); }
    }
    note_subnets_shrunk(app, rs, &sub_before);
    firc_nf_devices_clear(&before.dev);
    firc_err_t snap_err = republish_dns_matching_only(app); /* after the enable: before it this group was not in the view */
    if (snap_err != FIRC_OK) {
        FIRC_ERROR("failed to republish DNS-matching snapshot: %s", firc_err_str(snap_err));
    }
    firc_app_republish_resolve_routes(app); /* after the enable: the mark is the kernel's now */
    app_nf_leave(app);
    return FIRC_OK;
}

/* a subnet rule is part of the chain, so its enable/type needs the kernel told; a name rule needs only the snapshot */
static bool is_subnet_type(const char *type) {
    return strcmp(type, FIRC_RULE_SUBNET) == 0 || strcmp(type, FIRC_RULE_SUBNET6) == 0;
}

/* "" and NULL are the same spec everywhere a proto/ports pair is compared */
static bool spec_eq(const char *a, const char *b) { return strcmp(a != NULL ? a : "", b != NULL ? b : "") == 0; }

/* whether some OTHER rule shares idx's text/proto/ports, the only case an override needs list_type to tell apart */
static bool rule_has_twin(const firc_sub_rules_t *rs, size_t idx) {
    const char *text = firc_sub_rules_text(rs, idx);
    const char *proto = firc_sub_rules_proto(rs, idx), *ports = firc_sub_rules_ports(rs, idx);
    for (size_t j = 0; j < rs->n; j++) {
        if (j != idx && strcmp(firc_sub_rules_text(rs, j), text) == 0 &&
            spec_eq(firc_sub_rules_proto(rs, j), proto) && spec_eq(firc_sub_rules_ports(rs, j), ports)) {
            return true;
        }
    }
    return false;
}

/* a later PATCH finding no twin must still land on this entry, or the edit silently reverts at the next sync */
static bool has_typed_override(const firc_group_list_t *l, const char *text, const char *given,
                               const char *proto, const char *ports) {
    for (size_t i = 0; i < l->n_overrides; i++) {
        const firc_sub_override_t *o = l->overrides[i];
        if (strcmp(o->rule, text) == 0 && spec_eq(o->list_type, given) && spec_eq(o->proto, proto) &&
            spec_eq(o->ports, ports)) {
            return true;
        }
    }
    return false;
}

firc_err_t firc_app_patch_list_rules(firc_app_t *app, firc_id_t id, const firc_list_rule_edit_t *edits,
                                     size_t n, char *msg, size_t msg_len) {
    app_nf_enter(app);
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    firc_group_list_t *l = rs != NULL ? firc_ruleset_group_mut(rs)->list : NULL;
    if (l == NULL) {
        app_nf_leave(app);
        return FIRC_ERR_NOENT;
    }
    if (n == 0) {
        app_nf_leave(app);
        return FIRC_OK;
    }
    /* pass one resolves every edit before any is applied: the rule's index and, for a type edit, the override to store */
    size_t *at = calloc(n, sizeof(*at));
    const char **override = calloc(n, sizeof(*override));
    if (at == NULL || override == NULL) {
        free(at);
        free(override);
        app_nf_leave(app);
        return FIRC_ERR_NOMEM;
    }
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; err == FIRC_OK && i < n; i++) {
        if (!firc_sub_rules_find_id(&l->rules, edits[i].rule, &at[i])) {
            err = FIRC_ERR_NOENT;
            break;
        }
        if (edits[i].type == NULL) { continue; }
        const char *text = firc_sub_rules_text(&l->rules, at[i]);
        const char *proto = firc_sub_rules_proto(&l->rules, at[i]),
                   *ports = firc_sub_rules_ports(&l->rules, at[i]);
        const char *why = NULL;
        if (!firc_rule_spec_is_usable(edits[i].type, text, proto, ports, &why)) {
            snprintf(msg, msg_len, "rule \"%.*s\" (type \"%.*s\"): %.90s",
                     firc_utf8_clamp(text, 120), text, firc_utf8_clamp(edits[i].type, 20),
                     edits[i].type, why != NULL ? why : "unusable");
            err = FIRC_ERR_INVAL;
            break;
        }
        /* an override exists to disagree with the list: a type equal to the one the list gave is stored as "", clearing it */
        const char *given = firc_sub_rules_list_type(&l->rules, at[i]);
        override[i] = strcmp(given, edits[i].type) == 0 ? "" : edits[i].type;
    }
    if (err != FIRC_OK) { /* refused before anything changed */
        free(at);
        free(override);
        app_nf_leave(app);
        return err;
    }

    bool chain_touched = false; /* pass two can only fail for memory; whatever it applied before that is published below like a whole edit */
    for (size_t i = 0; i < n; i++) {
        const char *text = firc_sub_rules_text(&l->rules, at[i]);
        const char *given = firc_sub_rules_list_type(&l->rules, at[i]);
        const char *proto = firc_sub_rules_proto(&l->rules, at[i]),
                   *ports = firc_sub_rules_ports(&l->rules, at[i]);
        /* keyed by text+spec alone, except a twin of a different type: then the list's own type joins the key too */
        if (edits[i].has_enable || override[i] != NULL) {
            bool keyed_by_type =
                rule_has_twin(&l->rules, at[i]) || has_typed_override(l, text, given, proto, ports);
            firc_sub_rule_key_t key = {text, keyed_by_type ? given : NULL, proto, ports};
            err = firc_group_list_set_override(l, &key, override[i],
                                               edits[i].has_enable ? &edits[i].enable : NULL);
            if (err != FIRC_OK) { break; }
        }
        if (is_subnet_type(firc_sub_rules_type(&l->rules, at[i]))) { chain_touched = true; }
        if (edits[i].has_enable) { l->rules.v[at[i]].enable = edits[i].enable; }
        if (edits[i].type != NULL) { firc_sub_rules_set_type(&l->rules, at[i], edits[i].type); }
        if (is_subnet_type(firc_sub_rules_type(&l->rules, at[i]))) { chain_touched = true; }
    }
    free(at);
    free(override);
    /* the arena's text did not move, so borrowed text stays valid; the matcher rebuilds since it was built by type */
    release_dns_snapshot(app);
    republish_or_log(app);
    if (chain_touched && firc_ruleset_runtime_enabled(rs)) {
        firc_err_t serr = sync_group_ruleset_locked(app, rs);
        if (err == FIRC_OK) { err = serr; }
    }
    app_nf_leave(app);
    return err;
}

uint64_t firc_app_nf_enters_for_test(const firc_app_t *app) { return app->nf_enters; }
uint64_t firc_app_nf_passes_for_test(const firc_app_t *app) {
    return app->committer ? firc_nfcommit_passes(app->committer) : 0;
}

uint64_t firc_app_nf_last_completed_pass_for_test(const firc_app_t *app) {
    return app->committer ? firc_nfcommit_last_completed_pass(app->committer) : 0;
}

uint64_t firc_app_nf_change_for_test(const firc_app_t *app) { return app->nf_change; }
void firc_app_pay_flushes_for_test(firc_app_t *app, uint64_t ticket) { pay_flushes(app, ticket); }

/* forgets a group that is gone: its mappings, its chunks (parked for the quarantine window) */
static void pool_forget_group_id(firc_app_t *app, firc_id_t gid) {
    char id[FIRC_ID_STR_LEN];
    firc_id_format(gid, id);
    /* the mark field goes back too, but not while a flush of its flows is still queued: pay_flushes forgets it then */
    if (!flush_queued_for(app, gid)) { firc_rtnl_forget_mark_field(app->rtnl, id); }
    if (app->pool == NULL) { return; }
    firc_fakeip_drop_group(app->pool, id, (int64_t)time(NULL));
    firc_app_pool_changed(app);
}


static void firc_app_remove_group_by_index_unlocked(firc_app_t *app, size_t idx) {
    if (idx >= app->n_rulesets) { return; }
    /* out of the kernel before the config: ip rules/routes now, the chain with the next pass */
    if (firc_ruleset_runtime_enabled(app->rulesets[idx])) {
        (void)app_group_down(app, app->rulesets[idx]);
    }
    firc_group_t *g = app->cfg->groups[idx];
    firc_id_t gid = g->id;
    firc_ruleset_free(app->rulesets[idx]);
    rulesets_remove_at(app, idx);
    release_dns_snapshot(app); /* it borrows the list freed next */
    if (g->list != NULL) { sync_end_for_removed_list(app, gid, g->list, "group removed"); }
    firc_config_remove_group_by_index(app->cfg, idx);
    pool_forget_group_id(app, gid); /* after the list is consistent: see clear_groups_keeping */
    republish_or_log(app);
    retry_rearm(app); /* a failed group gone takes its timer with it */
}

void firc_app_remove_group_by_index(firc_app_t *app, size_t idx) {
    app_nf_enter(app);
    firc_app_remove_group_by_index_unlocked(app, idx);
    app_nf_leave(app);
}

static bool firc_app_remove_group_by_id_unlocked(firc_app_t *app, firc_id_t id) {
    for (size_t i = 0; i < app->n_rulesets; i++) {
        if (firc_id_equal(firc_ruleset_group(app->rulesets[i])->id, id)) {
            firc_app_remove_group_by_index_unlocked(app, i);
            return true;
        }
    }
    return false;
}

bool firc_app_remove_group_by_id(firc_app_t *app, firc_id_t id) {
    app_nf_enter(app);
    bool r = firc_app_remove_group_by_id_unlocked(app, id);
    app_nf_leave(app);
    return r;
}

static uint64_t sync_mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000 + (uint64_t)ts.tv_nsec / 1000000;
}

/* the seq answers three questions (exists, url unchanged, no newer request); checked here and nowhere else */
static bool sync_seq_is_current(const firc_group_list_t *list, const firc_sub_event_t *ev) {
    return ev->seq == list->sync_seq;
}

/* unflattened: a server refusal names its HTTP status, the four reasons call for four operator actions */
static void sync_set_error(firc_group_list_t *list, firc_err_t err, long http_status, const char *why) {
    list->sync_state = FIRC_SUB_SYNC_ERROR;
    if (why != NULL) {
        snprintf(list->sync_error, sizeof(list->sync_error), "%s", why);
    } else if (http_status != 0) {
        snprintf(list->sync_error, sizeof(list->sync_error), "%s: HTTP %ld", firc_err_str(err),
                 http_status);
    } else {
        snprintf(list->sync_error, sizeof(list->sync_error), "%s", firc_err_str(err));
    }
}

static void sync_set_idle(firc_group_list_t *list) {
    list->sync_state = FIRC_SUB_SYNC_IDLE;
    list->sync_error[0] = '\0';
}

/* runs once at the apply of a changed list, not on every rebuild: this is the only place that sees every line */
static void warn_unusable_list_lines(const char *owner_name, const firc_sub_rules_t *rules) {
    size_t unusable = 0;
    const char *first_why = "", *first_type = "", *first_rule = "";
    for (size_t i = 0; i < rules->n; i++) {
        if (!firc_sub_rules_enable(rules, i)) { continue; }
        const char *type = firc_sub_rules_type(rules, i);
        const char *text = firc_sub_rules_text(rules, i);
        const char *proto = firc_sub_rules_proto(rules, i), *ports = firc_sub_rules_ports(rules, i);
        const char *why = NULL;
        if (!firc_rule_spec_is_usable(type, text, proto, ports, &why)) {
            if (unusable == 0) {
                first_why = why ? why : "unusable";
                first_type = type;
                first_rule = text;
            }
            unusable++;
        }
    }
    if (unusable > 0) {
        FIRC_WARN("group \"%s\": %zu line(s) give a rule the daemon cannot use and that "
                  "routes nothing, the first being %s (type \"%s\", rule \"%s\")",
                  owner_name, unusable, first_why, first_type, first_rule);
    }
}

static firc_err_t firc_app_request_sync_unlocked(firc_app_t *app, firc_id_t id); /* for "ask again" on a stale UNCHANGED */
static firc_group_list_t *find_list(firc_app_t *app, firc_id_t id, const char **name); /* defined below */

/* provisional flips false->true only at the next republish; without this call it would never clear */
static void republish_if_first_list(firc_app_t *app, bool first_list) {
    if (!first_list) { return; }
    release_dns_snapshot(app); /* before the new one is built, as on the path that actually moved rules */
    republish_or_log(app);
}

static void sweep_after_first_load(firc_app_t *app, firc_id_t id) {
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    static const firc_nf_prefixes_t none = {NULL, 0};
    if (rs != NULL && queue_field_sweep(app, rs, &none)) { app_request_more_locked(app); }
}

/* the app-touching half of a sync; ev is not freed here, its rules may be moved out; the lock is the caller's */
static firc_err_t firc_app_apply_sync_result_unlocked(firc_app_t *app, firc_sub_event_t *ev,
                                                      int64_t now_unix, bool *out_changed) {
    *out_changed = false;

    const char *name = NULL;
    firc_group_list_t *list = find_list(app, ev->group_id, &name);
    if (list == NULL) { return FIRC_ERR_NOENT; }
    if (!sync_seq_is_current(list, ev)) {
        FIRC_DEBUG("\"%s\": a superseded result (seq %llu, latest %llu) is dropped", name,
                   (unsigned long long)ev->seq, (unsigned long long)list->sync_seq);
        return FIRC_OK;
    }

    /* told BEFORE the work runs: what follows is the seconds somebody watches a progress bar for */
    list->sync_progress.applying = true;
    if (app->sync_listener != NULL && !app->sync_listener_muted) {
        app->sync_listener(app->sync_listener_ud, ev->group_id, list, false);
    }

    uint64_t refresh_ms = 0, compare_ms = 0, rebuild_ms = 0, snapshot_ms = 0, save_ms = 0;
    firc_err_t ret = FIRC_OK;

    switch (ev->result) {
    case FIRC_SUB_RESULT_ERROR:
        /* set even on failure: firc_sub_is_due reads it with failed_fetches to pace the retry */
        sync_set_error(list, ev->err, ev->http_status, ev->why);
        list->last_check = (uint32_t)now_unix;
        if (list->failed_fetches < UINT8_MAX) { list->failed_fetches++; }
        FIRC_WARN("group \"%s\": its list was not synced: %s", name, list->sync_error);
        ret = ev->err;
        break;

    case FIRC_SUB_RESULT_UNCHANGED:
        /* refused with no body hash: storing it would wrongly claim hand-given rules came from bytes */
        if (!list->has_body_hash) {
            FIRC_DEBUG("\"%s\": unchanged against a hash this list no longer holds; "
                       "re-requesting",
                       name);
            list->last_check = (uint32_t)now_unix;
            sync_set_idle(list);
            firc_err_t qerr = firc_app_request_sync_unlocked(app, ev->group_id);
            if (qerr != FIRC_OK) { /* not this sync's failure to report; firc_sub_is_due comes back to it at the interval */
                FIRC_WARN("\"%s\": could not ask for the parse it needs: %s", name,
                          firc_err_str(qerr));
            }
            break;
        }
        FIRC_DEBUG("\"%s\": the list is the same bytes as last time, not parsed", name);
        memcpy(list->body_hash, ev->hash, sizeof(list->body_hash));
        list->has_body_hash = true;
        list->last_check = (uint32_t)now_unix;
        sync_set_idle(list);
        break;

    case FIRC_SUB_RESULT_PARSED: {
        if (ev->dropped > 0) {
            FIRC_WARN("group \"%s\": %zu entr(y/ies) of its list are neither a name, an address nor "
                      "a regex as written, and were dropped",
                      name, ev->dropped);
        }
        if (ev->unconstrained > 0) {
            FIRC_WARN("group \"%s\": %zu name(s) of its list came with a protocol or port that a name "
                      "rule cannot keep, and route every protocol and port",
                      name, ev->unconstrained);
        }
        uint64_t t0 = sync_mono_ms();
        /* carries the old arena's ids/types/enables across by rule text; the parser allocated fresh ids for everything */
        firc_err_t rerr = firc_sub_rules_inherit(&ev->rules, &list->rules);
        uint64_t t1 = sync_mono_ms();
        refresh_ms = t1 - t0;

        bool same = false;
        firc_err_t cerr = rerr;
        if (rerr == FIRC_OK) {
            firc_sub_apply_overrides(list, &ev->rules); /* before the comparison, or a changed override reads as "the list has not moved" */
            cerr = firc_sub_same_rules_checked(&list->rules, &ev->rules, &same); /* could not tell is not a change: don't replace rules on an allocation failure */
        }
        compare_ms = sync_mono_ms() - t1;
        if (cerr != FIRC_OK) {
            sync_set_error(list, cerr, 0, NULL);
            ret = cerr;
            break;
        }
        if (same) {
            bool first_list = !list->has_body_hash;
            firc_sub_rules_free(&ev->rules);
            memcpy(list->body_hash, ev->hash, sizeof(list->body_hash));
            list->has_body_hash = true;
            list->last_check = (uint32_t)now_unix;
            sync_set_idle(list);
            republish_if_first_list(app, first_list);
            if (first_list) { sweep_after_first_load(app, ev->group_id); }
            break;
        }
        bool first_load = !list->has_body_hash;

        firc_sub_rules_t prev_rules = list->rules; /* kept aside for the rollback below; the list holds the new ones from here */
        uint32_t prev_last_check = list->last_check;
        uint32_t prev_last_update = list->last_update;
        list->rules = ev->rules; /* moved: the event owns nothing now */
        firc_sub_rules_init(&ev->rules);
        list->last_check = (uint32_t)now_unix;
        list->last_update = (uint32_t)now_unix;

        warn_unusable_list_lines(name, &list->rules); /* once per changed list, the one place that sees every line */

        release_dns_snapshot(app); /* before anything new is built: holding both was the transition the router could not survive. Old rules stay until the resync succeeds, for the rollback. */
        uint64_t t2 = sync_mono_ms();
        firc_ruleset_t *group_rs = firc_app_find_group_by_id(app, ev->group_id);
        firc_err_t err = group_rs != NULL ? sync_group_ruleset_locked(app, group_rs) : FIRC_ERR_NOENT;
        uint64_t t3 = sync_mono_ms();
        rebuild_ms = t3 - t2;
        if (err != FIRC_OK) {
            firc_sub_rules_free(&list->rules);
            list->rules = prev_rules;
            list->last_check = prev_last_check;
            list->last_update = prev_last_update;
            firc_err_t rollback_err = group_rs != NULL ? sync_group_ruleset_locked(app, group_rs) : FIRC_ERR_NOENT;
            if (rollback_err != FIRC_OK) {
                FIRC_ERROR("failed to rollback the group's ruleset: %s", firc_err_str(rollback_err));
            }
            republish_or_log(app); /* the rule-set released above, from the rules kept */
            snapshot_ms = sync_mono_ms() - t3;
            sync_set_error(list, err, 0, NULL);
            *out_changed = true; /* a change was attempted, even though rolled back */
            ret = err;
            break;
        }
        firc_sub_rules_free(&prev_rules);

        /* set BEFORE the republish: snapshot.c reads has_body_hash on the live group at build time */
        memcpy(list->body_hash, ev->hash, sizeof(list->body_hash));
        list->has_body_hash = true;
        if (first_load) { sweep_after_first_load(app, ev->group_id); }
        republish_or_log(app);
        uint64_t t4 = sync_mono_ms();
        snapshot_ms = t4 - t3;

        sync_set_idle(list);
        *out_changed = true;

        /* written here and nowhere else now: the sync route has no save parameter to gate it on */
        if (app->config_path != NULL) {
            firc_err_t serr = firc_app_save_groups(app, app->config_path,
                                                   app->config_version != NULL ? app->config_version
                                                                               : "");
            if (serr != FIRC_OK) {
                FIRC_ERROR("failed to save the groups file: %s", firc_err_str(serr));
            }
            save_ms = sync_mono_ms() - t4;
        }
        break;
    }
    }

    list->sync_progress.applying = false;
    if (ev->result == FIRC_SUB_RESULT_PARSED) { /* tools/bench/run_sub_cost.sh sums this format */
        FIRC_DEBUG("%s: sync applied: refresh %llu ms, compare %llu ms, "
                   "rebuild %llu ms, snapshot %llu ms, save %llu ms, rules %zu",
                   name, (unsigned long long)refresh_ms, (unsigned long long)compare_ms,
                   (unsigned long long)rebuild_ms, (unsigned long long)snapshot_ms,
                   (unsigned long long)save_ms, list->rules.n);
    }
    return ret;
}

firc_err_t firc_app_apply_sync_result(firc_app_t *app, firc_sub_event_t *ev, int64_t now_unix,
                                      bool *out_changed) {
    app_nf_enter(app);
    firc_err_t r = firc_app_apply_sync_result_unlocked(app, ev, now_unix, out_changed);
    app_nf_leave(app);
    return r;
}

/* no lock but for a RESULT: STARTED/PROGRESS write only the list's own sync fields, which no pass reads */
void firc_app_on_sync_event(firc_app_t *app, firc_sub_event_t *ev, int64_t now_unix) {
    if (ev == NULL) { return; }
    if (app == NULL) {
        firc_sub_event_free(ev);
        return;
    }
    const char *name = NULL;
    firc_group_list_t *list = find_list(app, ev->group_id, &name);
    if (list == NULL) { /* deleted while the job ran; the worker could not have known */
        char idbuf[FIRC_ID_STR_LEN];
        firc_id_format(ev->group_id, idbuf);
        FIRC_DEBUG("sync event for %s, which is gone: dropped", idbuf);
        firc_sub_event_free(ev);
        return;
    }
    if (!sync_seq_is_current(list, ev)) {
        FIRC_DEBUG("\"%s\": a superseded sync event (seq %llu, latest %llu) is dropped", name,
                   (unsigned long long)ev->seq, (unsigned long long)list->sync_seq);
        firc_sub_event_free(ev);
        return;
    }

    switch (ev->kind) {
    case FIRC_SUB_EV_STARTED:
        list->sync_state = FIRC_SUB_SYNC_FETCHING;
        memset(&list->sync_progress, 0, sizeof(list->sync_progress));
        break;
    case FIRC_SUB_EV_PROGRESS:
        list->sync_progress.stage = ev->stage;
        list->sync_progress.bytes = ev->bytes;
        list->sync_progress.total = ev->total;
        list->sync_progress.lines = ev->lines;
        list->sync_progress.applying = false;
        break;
    case FIRC_SUB_EV_RESULT: {
        bool changed = false;
        app_nf_enter(app);
        firc_err_t err = firc_app_apply_sync_result_unlocked(app, ev, now_unix, &changed);
        app_nf_leave(app);
        if (err != FIRC_OK && ev->result != FIRC_SUB_RESULT_ERROR) {
            FIRC_WARN("\"%s\": sync failed: %s", name, firc_err_str(err));
        }
        break;
    }
    }

    if (app->sync_listener != NULL) {
        app->sync_listener(app->sync_listener_ud, ev->group_id, list, ev->kind == FIRC_SUB_EV_RESULT);
    }
    firc_sub_event_free(ev);
}

void firc_app_set_sync_listener(firc_app_t *app, firc_app_sync_listener_fn fn, void *ud) {
    app->sync_listener = fn;
    app->sync_listener_ud = ud;
}

/* NULL when no group has id, or it holds no list */
static firc_group_list_t *find_list(firc_app_t *app, firc_id_t id, const char **name) {
    for (size_t i = 0; i < app->cfg->n_groups; i++) {
        firc_group_t *g = app->cfg->groups[i];
        if (!firc_id_equal(g->id, id)) { continue; }
        if (name != NULL) { *name = g->name != NULL ? g->name : ""; }
        return g->list;
    }
    return NULL;
}

static firc_err_t firc_app_request_sync_unlocked(firc_app_t *app, firc_id_t id) {
    if (app->sub_worker == NULL) { return FIRC_ERR_STATE; }
    firc_group_list_t *l = find_list(app, id, NULL);
    if (l == NULL) { return FIRC_ERR_NOENT; }
    if (l->url == NULL || l->url[0] == '\0') { return FIRC_ERR_INVAL; }

    firc_sub_job_t job;
    memset(&job, 0, sizeof(job));
    job.group_id = id;
    if (firc_strset(&job.url, l->url) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    job.has_hash = l->has_body_hash;
    memcpy(job.expected_hash, l->body_hash, sizeof(job.expected_hash));
    job.seq = ++app->sync_seq_counter;

    firc_err_t err = firc_sub_worker_enqueue(app->sub_worker, &job); /* takes job.url on every return, error ones included */
    if (err != FIRC_OK) { return err; } /* FIRC_ERR_LIMIT: FIRC_SUB_WORKER_CAPACITY distinct ids already waiting; the list is left exactly as it was */

    l->sync_seq = job.seq;
    l->sync_state = FIRC_SUB_SYNC_QUEUED;
    l->sync_error[0] = '\0';
    memset(&l->sync_progress, 0, sizeof(l->sync_progress));
    return FIRC_OK;
}

/* this, the url change and the two sweeps below take no lock: they write only a list's own fields, no pass reads */
firc_err_t firc_app_request_sync(firc_app_t *app, firc_id_t id) {
    return firc_app_request_sync_unlocked(app, id);
}

firc_err_t firc_app_set_list_url(firc_app_t *app, firc_id_t id, const char *url) {
    /* id checked first: 400 "cannot take this url" would be a lie when there is no such list at all */
    firc_group_list_t *l = find_list(app, id, NULL);
    if (l == NULL) {
        return FIRC_ERR_NOENT;
    }
    if (url == NULL || !firc_sub_url_is_supported(url)) {
        return FIRC_ERR_INVAL;
    }
    if (strcmp(l->url != NULL ? l->url : "", url) == 0) {
        return FIRC_OK;
    }
    if (firc_strset(&l->url, url) != FIRC_OK) {
        return FIRC_ERR_NOMEM;
    }
    /* the body hash is kept deliberately: it hashes the list, not the address */
    if (app->config_path != NULL) {
        firc_err_t serr = firc_app_save_groups(app, app->config_path,
                                               app->config_version != NULL ? app->config_version
                                                                           : "");
        if (serr != FIRC_OK) {
            FIRC_ERROR("failed to save the groups file: %s", firc_err_str(serr));
        }
    }
    return FIRC_OK;
}

static void log_request_failure(firc_id_t id, const char *name, firc_err_t err) {
    char idbuf[FIRC_ID_STR_LEN];
    firc_id_format(id, idbuf);
    FIRC_WARN("list of %s (\"%s\") was not queued for a sync: %s", idbuf, name,
              firc_err_str(err));
}

/* has_body_hash is the filter, not last_check; skips QUEUED/FETCHING, unlike the due sweep, which must not */
static void request_if_missing(firc_app_t *app, firc_id_t id, const char *name, bool enable,
                               const firc_group_list_t *l) {
    if (!enable || l->url == NULL || l->url[0] == '\0' || l->has_body_hash) { return; }
    if (l->sync_state == FIRC_SUB_SYNC_QUEUED || l->sync_state == FIRC_SUB_SYNC_FETCHING) { return; }
    firc_err_t err = firc_app_request_sync_unlocked(app, id);
    if (err != FIRC_OK) { log_request_failure(id, name, err); }
}

static void firc_app_request_sync_missing_unlocked(firc_app_t *app) {
    for (size_t i = 0; i < app->cfg->n_groups; i++) {
        firc_group_t *g = app->cfg->groups[i];
        if (g->list == NULL) { continue; }
        request_if_missing(app, g->id, g->name != NULL ? g->name : "", g->enable, g->list);
    }
}

void firc_app_request_sync_missing(firc_app_t *app) {
    firc_app_request_sync_missing_unlocked(app);
}

void firc_app_request_sync_due(firc_app_t *app, int64_t now_unix) {
    for (size_t i = 0; i < app->cfg->n_groups; i++) {
        firc_group_t *g = app->cfg->groups[i];
        if (g->list == NULL || !firc_sub_is_due(g->enable, g->list, now_unix)) { continue; } /* not skipped when QUEUED/FETCHING */
        firc_err_t err = firc_app_request_sync_unlocked(app, g->id);
        if (err != FIRC_OK) { log_request_failure(g->id, g->name != NULL ? g->name : "", err); }
    }
}

typedef struct sync_capture {
    firc_sub_event_t *result; /* the last RESULT the job emitted, owned */
} sync_capture_t;

static void sync_capture_emit(void *ud, firc_sub_event_t *ev) {
    sync_capture_t *cap = ud;
    if (ev->kind != FIRC_SUB_EV_RESULT) {
        firc_sub_event_free(ev); /* nobody is watching a synchronous job */
        return;
    }
    firc_sub_event_free(cap->result);
    cap->result = ev;
}

static firc_err_t firc_app_sync_list_now_unlocked(firc_app_t *app, firc_id_t id,
                                                  int64_t now_unix,
                                                  const char *url_override,
                                                  bool *out_changed) {
    *out_changed = false;

    firc_group_list_t *list = find_list(app, id, NULL);
    if (list == NULL) { return FIRC_ERR_NOENT; }
    const char *fetch_url = (url_override != NULL && url_override[0] != '\0') ? url_override
                                                                             : list->url;
    if (fetch_url == NULL || fetch_url[0] == '\0') { return FIRC_ERR_INVAL; }

    firc_sub_job_t job;
    memset(&job, 0, sizeof(job));
    job.group_id = id;
    if (firc_strset(&job.url, fetch_url) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    job.has_hash = list->has_body_hash;
    memcpy(job.expected_hash, list->body_hash, sizeof(job.expected_hash));
    job.seq = ++app->sync_seq_counter;

    sync_capture_t cap = {.result = NULL};
    firc_sub_job_run(&job, NULL, sync_capture_emit, &cap);
    free(job.url);
    if (cap.result == NULL) { return FIRC_ERR_NOMEM; } /* a job that could not allocate its result says nothing, rather than a start with no end */

    bool was_fetch_error = cap.result->result == FIRC_SUB_RESULT_ERROR;
    firc_err_t result_err = cap.result->err;

    /* the url the fetch used becomes the list's before the apply, so the rollback below can restore it */
    bool url_changed = strcmp(list->url != NULL ? list->url : "", fetch_url) != 0;
    char *prev_url = NULL;
    if (url_changed && !was_fetch_error) {
        char *new_url = NULL;
        if (firc_strset(&new_url, fetch_url) != FIRC_OK) {
            firc_sub_event_free(cap.result);
            return FIRC_ERR_NOMEM;
        }
        prev_url = list->url;
        list->url = new_url;
    }

    list->sync_seq = job.seq; /* the latest thing asked of this list, so its answer is the one to take */
    bool changed = false;
    /* listener-silent deliberately: this path hands the answer back as a return value, not an event */
    app->sync_listener_muted = true;
    firc_err_t err = firc_app_apply_sync_result_unlocked(app, cap.result, now_unix, &changed);
    app->sync_listener_muted = false;
    firc_sub_event_free(cap.result);

    if (err != FIRC_OK) {
        if (prev_url != NULL) {
            free(list->url);
            list->url = prev_url;
        }
        *out_changed = changed;
        if (was_fetch_error) { /* one code for "could not be fetched", whatever stopped it; the list's own state says which, unflattened */
            return result_err == FIRC_ERR_NOMEM ? FIRC_ERR_NOMEM : FIRC_ERR_UPSTREAM;
        }
        return err;
    }
    free(prev_url);
    *out_changed = changed || url_changed;
    return FIRC_OK;
}

firc_err_t firc_app_sync_list_now(firc_app_t *app, firc_id_t id, int64_t now_unix,
                                  const char *url_override, bool *out_changed) {
    app_nf_enter(app);
    firc_err_t r = firc_app_sync_list_now_unlocked(app, id, now_unix, url_override,
                                                           out_changed);
    app_nf_leave(app);
    return r;
}

typedef struct posted_sync_event {
    firc_app_t *app;
    firc_sub_event_t *ev;
} posted_sync_event_t;

static void on_posted_sync_event(firc_loop_t *loop, void *ud) {
    (void)loop;
    posted_sync_event_t *p = ud;
    firc_app_on_sync_event(p->app, p->ev, (int64_t)time(NULL));
    free(p);
}

/* file-scope, not app fields: the drop runs at firc_loop_destroy, after firc_app_destroy, with no app left */
static _Atomic size_t g_sync_events_posted;
static _Atomic size_t g_sync_results_posted;
static _Atomic size_t g_sync_events_dropped;

void firc_app_sync_event_counts_for_test(size_t *out_posted, size_t *out_results,
                                         size_t *out_dropped) {
    if (out_posted != NULL) { *out_posted = atomic_load(&g_sync_events_posted); }
    if (out_results != NULL) { *out_results = atomic_load(&g_sync_results_posted); }
    if (out_dropped != NULL) { *out_dropped = atomic_load(&g_sync_events_dropped); }
}

/* a result the loop never read carries a rules arena, freed here rather than left to the wrapper's own free */
static void drop_posted_sync_event(void *ud) {
    posted_sync_event_t *p = ud;
    firc_sub_event_free(p->ev);
    free(p);
    atomic_fetch_add(&g_sync_events_dropped, 1);
}

/* worker thread; reads nothing of the app's but the loop it was created with */
static void sync_worker_emit(void *ud, firc_sub_event_t *ev) {
    firc_app_t *app = ud;
    posted_sync_event_t *p = malloc(sizeof(*p));
    if (p == NULL || app->loop == NULL) {
        free(p);
        firc_sub_event_free(ev);
        return;
    }
    p->app = app;
    p->ev = ev;
    bool is_result = ev->kind == FIRC_SUB_EV_RESULT; /* read before the post: a successful post may free ev before this call returns */
    if (firc_loop_post_with_drop(app->loop, on_posted_sync_event, p, drop_posted_sync_event) ==
        FIRC_OK) {
        if (is_result) { atomic_fetch_add(&g_sync_results_posted, 1); }
        atomic_fetch_add(&g_sync_events_posted, 1);
    } else { /* a failed post leaves the list QUEUED/FETCHING; the due sweep asks again */
        firc_sub_event_free(ev);
        free(p);
    }
}

firc_err_t firc_app_start_list_worker(firc_app_t *app) {
    if (app->sub_worker != NULL) { return FIRC_ERR_STATE; }
    if (app->loop == NULL) { return FIRC_ERR_STATE; } /* a worker that can fetch but not report is worse than none */

    app->sub_worker = firc_sub_worker_new(FIRC_SUB_WORKER_CAPACITY, sync_worker_emit, app);
    if (app->sub_worker == NULL) { return FIRC_ERR_NOMEM; }

    firc_err_t err = firc_sub_worker_start(app->sub_worker);
    if (err != FIRC_OK) {
        firc_sub_worker_free(app->sub_worker);
        app->sub_worker = NULL;
        return err;
    }
    FIRC_DEBUG("list worker started");
    return FIRC_OK;
}

void firc_app_stop_list_worker(firc_app_t *app) {
    if (app == NULL || app->sub_worker == NULL) { return; }
    firc_sub_worker_free(app->sub_worker); /* raises the cancel token, ends the transfer in flight, and joins */
    app->sub_worker = NULL;
}

static bool all_digits(const char *p, const char *end) {
    if (p == end) { return false; }
    for (; p < end; p++) {
        if (*p < '0' || *p > '9') { return false; }
    }
    return true;
}

static bool is_radio(const char *name) {
    if (strncmp(name, "ra", 2) != 0) { return false; }
    const char *p = name + 2;
    if (*p == 'i') { p++; }
    const char *end = p + strlen(p);
    const char *dot = strchr(p, '.');
    if (dot == NULL) { return all_digits(p, end); }
    return all_digits(p, dot) && all_digits(dot + 1, end);
}

static bool iface_listed(const char *name) {
    return strcmp(name, "lo") != 0 && strcmp(name, "ezcfg0") != 0 && !is_radio(name);
}

bool firc_iface_listed_for_test(const char *name) { return iface_listed(name); }

static bool named_with_number(const char *name, const char *prefix) {
    size_t n = strlen(prefix);
    return strncmp(name, prefix, n) == 0 && all_digits(name + n, name + strlen(name));
}

static firc_iface_role_t iface_role(const char *name, unsigned flags, bool inet, const firc_tunnels_t *ts) {
    if (firc_tunnels_by_device(ts, name) != NULL) { return FIRC_IFACE_TUNNEL; }
    if (inet) { return FIRC_IFACE_WAN; }
    if ((flags & IFF_POINTOPOINT) != 0) { return FIRC_IFACE_TUNNEL; }
    if (named_with_number(name, "br")) { return FIRC_IFACE_LAN; }
    return FIRC_IFACE_OTHER;
}

firc_iface_role_t firc_iface_role_for_test(const char *name, unsigned flags, bool inet, const firc_tunnels_t *ts) {
    return iface_role(name, flags, inet, ts);
}

const char *firc_iface_role_name(firc_iface_role_t role) {
    switch (role) {
    case FIRC_IFACE_TUNNEL: return "tunnel";
    case FIRC_IFACE_WAN: return "wan";
    case FIRC_IFACE_LAN: return "lan";
    case FIRC_IFACE_OTHER: break;
    }
    return "other";
}

firc_err_t firc_app_list_interfaces(const firc_kn_aliases_t *aliases, const firc_tunnels_t *ts, firc_iface_info_t **out,
                                    size_t *out_n) {
    *out = NULL;
    *out_n = 0;
    static const firc_kn_aliases_t none = {0};
    if (aliases == NULL) { aliases = &none; }

    struct ifaddrs *ifap;
    if (getifaddrs(&ifap) != 0) { return firc_err_from_errno(errno); }

    size_t cap = 0;
    size_t n = 0;
    firc_iface_info_t *arr = NULL;

    for (struct ifaddrs *p = ifap; p != NULL; p = p->ifa_next) {
        bool already = false;
        for (size_t i = 0; i < n; i++) {
            if (strcmp(arr[i].id, p->ifa_name) == 0) {
                already = true;
                break;
            }
        }
        if (already) { continue; }

        if (!iface_listed(p->ifa_name)) { continue; }

        if (n == cap) {
            size_t new_cap = cap ? cap * 2 : 8;
            firc_iface_info_t *na = realloc(arr, new_cap * sizeof(*na));
            if (!na) {
                free(arr);
                freeifaddrs(ifap);
                return FIRC_ERR_NOMEM;
            }
            arr = na;
            cap = new_cap;
        }
        memset(&arr[n], 0, sizeof(arr[n]));
        snprintf(arr[n].id, sizeof(arr[n].id), "%s", p->ifa_name);
        const char *description = firc_kn_aliases_lookup(aliases, arr[n].id);
        if (description) { snprintf(arr[n].name, sizeof(arr[n].name), "%.63s", description); }
        const char *ndms_name = firc_kn_aliases_ndms_name(aliases, arr[n].id);
        if (ndms_name) { snprintf(arr[n].ndms_name, sizeof(arr[n].ndms_name), "%.63s", ndms_name); }
        arr[n].role = iface_role(arr[n].id, p->ifa_flags, firc_kn_aliases_inet(aliases, arr[n].id), ts);
        if (aliases->n > 0 && arr[n].role != FIRC_IFACE_TUNNEL && !firc_kn_aliases_known(aliases, arr[n].id)) {
            continue;
        }
        n++;
    }
    freeifaddrs(ifap);

    *out = arr;
    *out_n = n;
    return FIRC_OK;
}

firc_err_t firc_app_save_groups(firc_app_t *app, const char *conf_path, const char *version) {
    /* derived here rather than carried alongside conf_path, so the two paths cannot drift apart */
    char groups_path[4096];
    firc_err_t err = firc_config_groups_path(conf_path, groups_path, sizeof(groups_path));
    if (err != FIRC_OK) { return err; }
    return firc_config_save_part_file(app->cfg, version, FIRC_CFG_GROUPS, groups_path);
}

firc_err_t firc_app_save_config(firc_app_t *app, const char *conf_path, const char *version) {
    /* settings first, groups second, each its own atomic write; writes the SAVED copy, not cfg->app */
    firc_config_t shell;
    memset(&shell, 0, sizeof(shell));
    shell.app = app->saved; /* shallow: never cleared */
    firc_err_t err = firc_config_save_part_file(&shell, version, FIRC_CFG_SETTINGS, conf_path);
    if (err != FIRC_OK) { return err; }
    return firc_app_save_groups(app, conf_path, version);
}

const firc_app_config_t *firc_app_saved_settings(const firc_app_t *app) { return &app->saved; }

const firc_app_config_t *firc_app_running_settings(const firc_app_t *app) { return &app->cfg->app; }

size_t firc_app_pending_restart(const firc_app_t *app, bool pending[FIRC_SETTINGS_COUNT]) {
    size_t n = 0;
    for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
        const firc_setting_t *s = &firc_settings[i];
        pending[i] = s->cls == FIRC_SETTING_RESTART && !firc_setting_equal(s, &app->saved, &app->cfg->app);
        if (pending[i]) { n++; }
    }
    return n;
}

/* the proxy is told first: the running copy's old string is about to be freed by the copy */
static firc_err_t apply_live(firc_app_t *app, bool applied[FIRC_SETTINGS_COUNT]) {
    firc_err_t first = FIRC_OK;
    firc_app_config_t *run = &app->cfg->app;
    const firc_app_config_t *sav = &app->saved;
    bool differ[FIRC_SETTINGS_COUNT];
    bool upstream = false, flags = false, check = false, stats = false, autolist = false;
    for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
        const firc_setting_t *s = &firc_settings[i];
        differ[i] = s->cls == FIRC_SETTING_LIVE && !firc_setting_equal(s, sav, run);
        if (!differ[i]) { continue; }
        if (s->apply == FIRC_APPLY_UPSTREAM) { upstream = true; }
        if (s->apply == FIRC_APPLY_PROXY_FLAGS) { flags = true; }
        if (s->apply == FIRC_APPLY_DNS_STATS) { stats = true; }
        if (s->apply == FIRC_APPLY_AUTOLIST) { autolist = true; }
        if (s->apply == FIRC_APPLY_CHECK || s->apply == FIRC_APPLY_UPSTREAM) { check = true; }
    }
    if (upstream && app->proxy != NULL) {
        firc_err_t err = firc_dnsproxy_set_upstream(app->proxy, sav->dns_proxy.upstream.address,
                                                    sav->dns_proxy.upstream.port);
        if (err != FIRC_OK) {
            FIRC_ERROR("settings: the DNS proxy stays on %s:%u: %s", run->dns_proxy.upstream.address,
                       run->dns_proxy.upstream.port, firc_err_str(err));
            first = err;
            for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
                if (firc_settings[i].apply == FIRC_APPLY_UPSTREAM) { differ[i] = false; }
            }
        }
    }
    if (flags && app->proxy != NULL) {
        firc_dnsproxy_set_disable_drop_aaaa(app->proxy, sav->dns_proxy.disable_drop_aaaa);
    }
    if (stats && app->dnsstats != NULL) {
        firc_err_t err = app->dnsstats(app->dnsstats_ud, !sav->dns_proxy.disable_stats);
        if (err != FIRC_OK) {
            if (first == FIRC_OK) { first = err; }
            for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
                if (firc_settings[i].apply == FIRC_APPLY_DNS_STATS) { differ[i] = false; }
            }
        }
    }
    for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
        if (applied != NULL) { applied[i] = false; }
        if (!differ[i]) { continue; }
        const firc_setting_t *s = &firc_settings[i];
        if (firc_setting_copy(s, run, sav) != FIRC_OK) {
            FIRC_WARN("settings: %s not applied: out of memory", s->path);
            if (first == FIRC_OK) { first = FIRC_ERR_NOMEM; }
            continue;
        }
        if (s->apply == FIRC_APPLY_UNMATCHED_TTL && app->pipeline != NULL) {
            firc_dns_pipeline_set_unmatched_ttl(
                app->pipeline, (uint32_t)(run->dns_proxy.unmatched_ttl / FIRC_DURATION_SEC));
        }
        if (applied != NULL) { applied[i] = true; }
        FIRC_INFO("settings: %s applied", s->path);
    }
    if (check && app->ifaces.settings != NULL) { app->ifaces.settings(app->ifaces.ud, run); }
    if (autolist && app->running) { (void)firc_app_autolist_apply(app); }
    return first;
}

/* next becomes the saved copy (taken: zeroed on return); returns apply_live's first failure */
static firc_err_t adopt_saved(firc_app_t *app, firc_app_config_t *next,
                              bool applied[FIRC_SETTINGS_COUNT]) {
    firc_app_config_clear(&app->saved);
    app->saved = *next;
    memset(next, 0, sizeof(*next));
    return apply_live(app, applied);
}

firc_err_t firc_app_put_settings(firc_app_t *app, firc_app_config_t *next, const char *conf_path,
                                 const char *version, bool applied[FIRC_SETTINGS_COUNT],
                                 firc_err_t *live_err) {
    if (applied != NULL) { memset(applied, 0, FIRC_SETTINGS_COUNT * sizeof(bool)); }
    if (live_err != NULL) { *live_err = FIRC_OK; }
    bool changed = false;
    for (size_t i = 0; i < FIRC_SETTINGS_COUNT && !changed; i++) {
        changed = !firc_setting_equal(&firc_settings[i], next, &app->saved);
    }
    if (!changed) {
        firc_app_config_clear(next);
        firc_err_t lerr = apply_live(app, applied);
        if (live_err != NULL) { *live_err = lerr; }
        return FIRC_OK;
    }
    if (conf_path != NULL) {
        firc_config_t shell;
        memset(&shell, 0, sizeof(shell));
        shell.app = *next; /* shallow: never cleared */
        firc_err_t err = firc_config_save_part_file(&shell, version, FIRC_CFG_SETTINGS, conf_path);
        if (err != FIRC_OK) { return err; }
    }
    firc_err_t lerr = adopt_saved(app, next, applied);
    if (live_err != NULL) { *live_err = lerr; }
    return FIRC_OK;
}

firc_err_t firc_app_move_web_port(firc_app_t *app, uint16_t port, const char *conf_path,
                                  const char *version) {
    app->cfg->app.http_web.host.port = port;
    firc_app_config_t next;
    memset(&next, 0, sizeof(next));
    firc_err_t err = firc_app_config_copy(&next, &app->saved);
    if (err == FIRC_OK) {
        next.http_web.host.port = port;
        err = firc_app_put_settings(app, &next, conf_path, version, NULL, NULL);
    }
    if (err != FIRC_OK) {
        firc_app_config_clear(&next);
        app->saved.http_web.host.port = port;
    }
    return err;
}

firc_err_t firc_app_reload_settings(firc_app_t *app, const firc_app_config_t *from_file,
                                    bool applied[FIRC_SETTINGS_COUNT]) {
    firc_app_config_t next;
    memset(&next, 0, sizeof(next));
    firc_err_t err = firc_app_config_copy(&next, from_file);
    if (err != FIRC_OK) {
        firc_app_config_clear(&next);
        return err;
    }
    return adopt_saved(app, &next, applied);
}

void firc_app_set_port_remap(firc_app_t *app, firc_port_remap_t *remap) {
    app->port_remap = remap;
}

static void set_remap_addrs(firc_app_t *app, const firc_remap_addr_t *addrs, size_t n) {
    app_nf_enter(app);
    firc_err_t err = firc_port_remap_set_addrs(app->port_remap, addrs, n);
    if (err == FIRC_OK) { app_request_more_locked(app); }
    app_nf_leave(app);
    if (err != FIRC_OK) { FIRC_WARN("port 53 redirect keeps its old addresses: %s", firc_err_str(err)); }
}

void firc_app_refresh_remap(firc_app_t *app, const char *iface) {
    if (app->port_remap == NULL) { return; }
    const firc_app_config_t *running = firc_app_running_settings(app);
    bool listed = iface == NULL;
    for (size_t i = 0; !listed && i < running->n_link; i++) { listed = strcmp(running->link[i], iface) == 0; }
    if (!listed) { return; }

    firc_remap_addr_t *addrs = NULL;
    size_t n = 0;
    firc_err_t err = firc_port_remap_collect(running->link, running->n_link, false, &addrs, &n);
    bool same = err == FIRC_OK && firc_port_remap_same_addrs(app->port_remap, addrs, n);
    free(addrs);
    addrs = NULL;
    if (err == FIRC_OK && !same) { err = firc_port_remap_collect(running->link, running->n_link, true, &addrs, &n); }
    if (err != FIRC_OK) {
        FIRC_WARN("port 53 redirect keeps its old addresses: %s", firc_err_str(err));
    } else if (!same) {
        set_remap_addrs(app, addrs, n);
    }
    free(addrs);
}

/* collected by the pass under nf_mu while it walks the registry: DNAT follows "routed" */
typedef struct routed_ids {
    char (*ids)[FIRC_ID_STR_LEN];
    size_t n;
} routed_ids_t;

static int routed_id_cmp(const void *a, const void *b) { return strcmp(a, b); }

static bool routed_has(const char *group_id, void *ud) {
    const routed_ids_t *r = ud;
    return r->n != 0 && bsearch(group_id, r->ids, r->n, sizeof(*r->ids), routed_id_cmp) != NULL;
}

static bool staged_in_pass(const firc_ruleset_t *rs, const firc_ruleset_t *skip) {
    return rs != skip && firc_ruleset_pass(rs) != FIRC_RULESET_PASS_LEFT_OUT && firc_ruleset_routed(rs);
}

typedef struct redir_id {
    char id[FIRC_ID_STR_LEN];
    uint8_t dev;
} redir_id_t;

typedef struct {
    firc_app_t *app;
    const redir_id_t *ids;
    size_t n;
    firc_err_t err;
} image_walk_t;

static int redir_id_cmp(const void *a, const void *b) {
    return strcmp(((const redir_id_t *)a)->id, ((const redir_id_t *)b)->id);
}

static void image_add(void *ud, const char *group_id, const firc_ip_t *fake, const firc_ip_t *real) {
    image_walk_t *w = ud;
    if (w->err != FIRC_OK || group_id == NULL || fake == NULL || fake->len != 4 || real == NULL) {
        return;
    }
    redir_id_t key = {.dev = 0};
    snprintf(key.id, sizeof(key.id), "%s", group_id);
    const redir_id_t *hit = bsearch(&key, w->ids, w->n, sizeof(*w->ids), redir_id_cmp);
    if (hit == NULL) { return; }
    if (w->app->pass_image.n >= FIRC_KTCP_MAP_MAX) {
        if (!w->app->ktcp_cap_said) {
            w->app->ktcp_cap_said = true;
            FIRC_WARN("kernel TCP map is full at %u addresses; the rest are left to the miss query",
                      (unsigned)FIRC_KTCP_MAP_MAX);
        }
        return;
    }
    w->err = firc_ktcp_image_add(&w->app->pass_image, hit->dev, fake->b, real);
}

static void image_pair(void *ud, const char *group_id, unsigned family, const firc_ip_t *fake, const firc_ip_t *real) {
    (void)family;
    image_add(ud, group_id, fake, real);
}

static firc_err_t stage_redir(firc_app_t *app, const firc_ruleset_t *skip, const char *prefix, bool full) {
    firc_ktcp_image_free(&app->pass_image);
    app->pass_off = FIRC_KTCP_OFF_NONE;
    if (app->ipt4 == NULL) { return FIRC_OK; }
    firc_redir_group_t *groups = NULL;
    redir_id_t *ids = NULL;
    if (app->n_rulesets != 0) {
        groups = calloc(app->n_rulesets, sizeof(*groups));
        ids = calloc(app->n_rulesets, sizeof(*ids));
        if (groups == NULL || ids == NULL) {
            free(groups);
            free(ids);
            return FIRC_ERR_NOMEM;
        }
    }
    size_t n = 0;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        const firc_ruleset_t *rs = app->rulesets[i];
        if (!staged_in_pass(rs, skip)) { continue; }
        const firc_group_t *g = firc_ruleset_group(rs);
        int dev = group_tunnel(app, rs);
        if (dev < 0 || app->ktcp_ports[dev] == 0) { continue; }
        groups[n].mark = FIRC_MARK_HANDLED | firc_ruleset_mark_field(rs);
        groups[n].port = app->ktcp_ports[dev];
        firc_id_format(g->id, ids[n].id);
        ids[n].dev = (uint8_t)dev;
        n++;
    }
    if (n > 0 && !(firc_ipt_has(app->ipt4, "REDIRECT") && firc_ipt_has(app->ipt4, "addrtype"))) {
        if (!app->ktcp_lack_said) {
            app->ktcp_lack_said = true;
            FIRC_ERROR("kernel TCP needs the REDIRECT target and the addrtype match; TCP stays on the TUN");
        }
        app->pass_off = FIRC_KTCP_OFF_KERNEL;
        n = 0;
    }
    if (n > 0 && app->redir_off) {
        app->pass_off = FIRC_KTCP_OFF_REFUSED;
        n = 0;
    }
    memset(&app->pass_skip, 0, sizeof(app->pass_skip));
    if (n > 0 && (full || !app->acl_known)) {
        firc_xt_acl_t acl;
        firc_err_t rd = firc_ipt_acl_denying(app->ipt4, "filter", FIRC_REDIR_ACL_CHAIN, FIRC_REDIR_ACL_OUT_CHAIN, &acl);
        if (rd == FIRC_ERR_AGAIN) {
            free(groups);
            free(ids);
            return rd;
        }
        app->acl_known = rd == FIRC_OK;
        if (rd == FIRC_OK) {
            app->acl_cached = acl;
            if (app->acl_fail_said) {
                app->acl_fail_said = false;
                FIRC_INFO("kernel TCP reads the segments' access lists again");
            }
        } else if (!app->acl_fail_said) {
            app->acl_fail_said = true;
            FIRC_ERROR("kernel TCP cannot read the segments' access lists (%s); TCP stays on the TUN",
                       firc_err_str(rd));
        }
    }
    if (n > 0 && app->acl_known && app->acl_cached.all) {
        if (!app->acl_all_said) {
            app->acl_all_said = true;
            FIRC_ERROR("kernel TCP cannot tell which segments an access list covers; TCP stays on the TUN");
        }
        app->pass_off = FIRC_KTCP_OFF_ACL;
        n = 0;
    } else if (n > 0 && app->acl_known) {
        app->acl_all_said = false;
    }
    if (n > 0 && !app->acl_known) {
        app->pass_off = FIRC_KTCP_OFF_ACL;
        n = 0;
    }
    if (n > 0) { app->pass_skip = app->acl_cached; }
    app->redir_staged = n > 0;
    firc_err_t err = firc_redir_build_rules(app->ipt4, prefix, groups, n, (const char (*)[16])app->pass_skip.iface,
                                            app->pass_skip.n);
    if (err == FIRC_OK && n > 0 && app->pass_snap != NULL) {
        qsort(ids, n, sizeof(*ids), redir_id_cmp);
        image_walk_t w = {.app = app, .ids = ids, .n = n, .err = FIRC_OK};
        firc_fakeip_snapshot_walk(app->pass_snap, image_pair, &w);
        firc_fakeip_snapshot_cross_walk(app->pass_snap, image_add, &w);
        err = w.err;
        firc_ktcp_image_sort(&app->pass_image);
    }
    free(groups);
    free(ids);
    return err;
}

static const uint8_t k_set_families[2] = {4, 6};

static firc_ipt_t *set_engine(const firc_app_t *app, uint8_t family) { return family == 4 ? app->ipt4 : app->ipt6; }

static set_rec_t *set_rec_find(const firc_app_t *app, firc_id_t id, uint8_t family) {
    for (size_t i = 0; i < app->n_set_recs; i++) {
        if (app->set_recs[i].family == family && firc_id_equal(app->set_recs[i].id, id)) { return &app->set_recs[i]; }
    }
    return NULL;
}

static set_rec_t *set_rec_get(firc_app_t *app, firc_id_t id, uint8_t family) {
    set_rec_t *rec = set_rec_find(app, id, family);
    if (rec != NULL) { return rec; }
    if (app->n_set_recs == app->cap_set_recs) {
        size_t cap = app->cap_set_recs ? app->cap_set_recs * 2 : 8;
        set_rec_t *v = realloc(app->set_recs, cap * sizeof(*v));
        if (v == NULL) { return NULL; }
        app->set_recs = v;
        app->cap_set_recs = cap;
    }
    rec = &app->set_recs[app->n_set_recs++];
    memset(rec, 0, sizeof(*rec));
    rec->id = id;
    rec->family = family;
    return rec;
}

static bool set_live(const firc_app_t *app, firc_id_t id, uint8_t family) {
    const set_rec_t *rec = set_rec_find(app, id, family);
    return rec != NULL && rec->live;
}

static bool set_missing(const firc_app_t *app, const firc_ruleset_t *rs) {
    firc_id_t id = firc_ruleset_group(rs)->id;
    for (size_t f = 0; f < 2; f++) {
        uint8_t fam = k_set_families[f];
        if (set_engine(app, fam) != NULL && firc_ruleset_has_bare(rs, fam) && !set_live(app, id, fam)) { return true; }
    }
    return false;
}

static void set_jobs_free(set_job_t *jobs, size_t n) {
    for (size_t i = 0; i < n; i++) { firc_nf_prefixes_clear(&jobs[i].list); }
    free(jobs);
}

static bool same_print(firc_nf_print_t a, firc_nf_print_t b) { return a.n == b.n && a.hash == b.hash; }

static firc_err_t sets_collect(firc_app_t *app, set_job_t **out, size_t *n_out, bool *was_due) {
    *out = NULL;
    *n_out = 0;
    *was_due = false;
    if (app->sets == NULL || app->n_rulesets == 0) { return FIRC_OK; }
    set_job_t *jobs = calloc(app->n_rulesets * 2, sizeof(*jobs));
    if (jobs == NULL) { return FIRC_ERR_NOMEM; }
    size_t n = 0;
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; i < app->n_rulesets && err == FIRC_OK; i++) {
        const firc_ruleset_t *rs = app->rulesets[i];
        if (!firc_ruleset_routed(rs)) { continue; }
        firc_id_t id = firc_ruleset_group(rs)->id;
        for (size_t f = 0; f < 2 && err == FIRC_OK; f++) {
            uint8_t fam = k_set_families[f];
            if (set_engine(app, fam) == NULL) { continue; }
            firc_nf_print_t print = firc_ruleset_bare_print(rs, fam);
            if (print.n == 0) { continue; }
            set_rec_t *rec = set_rec_find(app, id, fam);
            if (rec != NULL && rec->live && same_print(rec->print, print)) {
                rec->failed = false;
                continue;
            }
            if (rec != NULL && rec->failed && !app->set_retry_due && same_print(rec->failed_print, print)) { continue; }
            set_job_t *j = &jobs[n];
            err = firc_ruleset_bare(rs, fam, &j->list);
            if (err != FIRC_OK) { continue; }
            j->print = print;
            j->id = id;
            j->family = fam;
            j->err = firc_ruleset_set_name(rs, fam, false, j->live, sizeof(j->live));
            if (j->err == FIRC_OK) { j->err = firc_ruleset_set_name(rs, fam, true, j->scratch, sizeof(j->scratch)); }
            j->done = j->err != FIRC_OK;
            n++;
        }
    }
    if (err != FIRC_OK) {
        set_jobs_free(jobs, app->n_rulesets * 2);
        return err;
    }
    *was_due = app->set_retry_due;
    app->set_retry_due = false;
    *out = jobs;
    *n_out = n;
    return FIRC_OK;
}

static bool sets_write(firc_app_t *app, set_job_t *jobs, size_t n, firc_cancel_t *cancel) {
    for (size_t i = 0; i < n; i++) {
        if (firc_cancel_raised(cancel)) { return false; }
        set_job_t *j = &jobs[i];
        if (j->done) { continue; }
        j->err = firc_setnl_replace(app->sets, j->live, j->scratch, j->family, j->list.v, j->list.n);
        j->done = true;
    }
    return true;
}

static void sets_record(firc_app_t *app, const set_job_t *jobs, size_t n, bool was_due) {
    for (size_t i = 0; i < n; i++) {
        const set_job_t *j = &jobs[i];
        if (!j->done && was_due) { app->set_retry_due = true; }
        if (j->done && j->err != FIRC_ERR_NOSYS && j->err != FIRC_ERR_INVAL) { app->sweep_again = true; }
        firc_ruleset_t *rs = j->done ? firc_app_find_group_by_id(app, j->id) : NULL;
        set_rec_t *rec = rs != NULL ? set_rec_get(app, j->id, j->family) : NULL;
        if (rec == NULL) { continue; }
        if (j->err == FIRC_OK) {
            if (rec->failed && rec->live) { firc_ruleset_owe_flush(rs); }
            rec->failed = false;
            rec->live = true;
            rec->print = j->print;
            if (firc_ruleset_pass(rs) == FIRC_RULESET_PASS_LEFT_OUT) {
                firc_ruleset_set_pass(rs, FIRC_RULESET_PASS_ON_TRIAL);
            }
            continue;
        }
        if (!rec->failed) {
            const firc_group_t *g = firc_ruleset_group(rs);
            char id[FIRC_ID_STR_LEN];
            firc_id_format(g->id, id);
            FIRC_WARN("group \"%s\" (%s): its subnet set was not written: %s", g->name != NULL ? g->name : "", id,
                      firc_err_str(j->err));
        }
        rec->failed = true;
        rec->failed_err = j->err;
        rec->failed_print = j->print;
    }
}

static int set_name_cmp(const void *a, const void *b) { return strcmp(a, b); }

static bool set_wanted(const firc_app_t *app, const set_rec_t *rec) {
    const firc_ruleset_t *rs = firc_app_find_group_by_id(app, rec->id);
    return rs != NULL && firc_ruleset_has_bare(rs, rec->family);
}

static void sets_mark_stale(firc_app_t *app) {
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        firc_id_t id = firc_ruleset_group(rs)->id;
        bool stale = false;
        for (size_t f = 0; f < 2; f++) {
            const set_rec_t *rec = set_rec_find(app, id, k_set_families[f]);
            stale = stale || (rec != NULL && rec->failed && set_wanted(app, rec));
        }
        firc_ruleset_set_sets_stale(rs, stale);
    }
}

typedef struct {
    char (*names)[FIRC_SET_NAME_MAX];
    bool *seen;
    size_t n;
} live_names_t;

static void on_kernel_set(const char *name, void *ud) {
    live_names_t *l = ud;
    for (size_t i = 0; i < l->n; i++) {
        if (strcmp(l->names[i], name) == 0) { l->seen[i] = true; }
    }
}

static void sets_forget_gone(firc_app_t *app) {
    if (app->sets == NULL || app->n_set_recs == 0) { return; }
    live_names_t l = {calloc(app->n_set_recs, sizeof(*l.names)), calloc(app->n_set_recs, sizeof(*l.seen)),
                      app->n_set_recs};
    if (l.names != NULL && l.seen != NULL) {
        for (size_t i = 0; i < l.n; i++) {
            const set_rec_t *rec = &app->set_recs[i];
            firc_ruleset_t *rs = firc_app_find_group_by_id(app, rec->id);
            l.seen[i] = rs == NULL ||
                        firc_ruleset_set_name(rs, rec->family, false, l.names[i], sizeof(l.names[i])) != FIRC_OK;
        }
        if (firc_setnl_names(app->sets, on_kernel_set, &l) == FIRC_OK) {
            for (size_t i = 0; i < l.n; i++) {
                if (!l.seen[i]) { app->set_recs[i].live = false; }
            }
        }
    }
    free(l.names);
    free(l.seen);
}

static void sets_sweep(firc_app_t *app) {
    if (app->sets == NULL) { return; }
    size_t kept = 0;
    for (size_t i = 0; i < app->n_set_recs; i++) {
        if (set_wanted(app, &app->set_recs[i])) { app->set_recs[kept++] = app->set_recs[i]; }
    }
    app->n_set_recs = kept;
    char(*names)[FIRC_SET_NAME_MAX] = kept != 0 ? calloc(kept, sizeof(*names)) : NULL;
    const char **keep = kept != 0 ? calloc(kept, sizeof(*keep)) : NULL;
    char *joined = calloc(kept * FIRC_SET_NAME_MAX + 1, 1);
    if ((kept != 0 && (names == NULL || keep == NULL)) || joined == NULL) {
        free(names);
        free(keep);
        free(joined);
        return;
    }
    size_t n = 0;
    for (size_t i = 0; i < kept; i++) {
        const set_rec_t *rec = &app->set_recs[i];
        if (rec->live &&
            firc_ruleset_set_name(firc_app_find_group_by_id(app, rec->id), rec->family, false, names[n],
                                  sizeof(names[n])) == FIRC_OK) {
            n++;
        }
    }
    if (n > 1) { qsort(names, n, sizeof(*names), set_name_cmp); }
    size_t at = 0;
    for (size_t i = 0; i < n; i++) {
        keep[i] = names[i];
        size_t len = strlen(names[i]);
        memcpy(joined + at, names[i], len);
        joined[at + len] = '\n';
        at += len + 1;
    }
    if (app->sweep_again || strcmp(joined, app->sets_swept != NULL ? app->sets_swept : "") != 0) {
        firc_err_t err = firc_setnl_sweep(app->sets, app->cfg->app.netfilter.iptables.chain_prefix, keep, n, NULL);
        if (err != FIRC_OK && err != FIRC_ERR_AGAIN && firc_setnl_probe(app->sets, NULL) == FIRC_ERR_NOSYS) {
            err = FIRC_OK;
        }
        if (err == FIRC_OK || err == FIRC_ERR_AGAIN) {
            free(app->sets_swept);
            app->sets_swept = joined;
            joined = NULL;
            app->sweep_again = err == FIRC_ERR_AGAIN;
            app->sweep_said = false;
        } else if (!app->sweep_said) {
            app->sweep_said = true;
            FIRC_WARN("subnet sets no group names are left in the kernel: %s", firc_err_str(err));
        }
    }
    free(names);
    free(keep);
    free(joined);
}

static bool sets_failing(const firc_app_t *app) {
    for (size_t i = 0; i < app->n_set_recs; i++) {
        if (app->set_recs[i].failed && app->set_recs[i].failed_err != FIRC_ERR_NOSYS) { return true; }
    }
    return false;
}

static void on_set_retry(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_app_t *app = ud;
    app->set_retry_timer = 0;
    app_nf_enter(app);
    app->set_retry_due = true;
    app_request_more_locked(app);
    app_nf_leave(app);
}

static void set_retry_arm(firc_app_t *app, bool failed) {
    if (!failed) {
        if (app->set_retry_timer != 0) { (void)firc_loop_del_timer(app->loop, app->set_retry_timer); }
        app->set_retry_timer = 0;
        app->set_retry_ms = 0;
        return;
    }
    if (app->set_retry_timer != 0) { return; }
    uint32_t next = app->set_retry_ms == 0 ? FIRC_APP_RETRY_MIN_MS : app->set_retry_ms * 2u;
    app->set_retry_ms = next < FIRC_APP_RETRY_MAX_MS ? next : FIRC_APP_RETRY_MAX_MS;
    int id = 0;
    if (firc_loop_add_timer(app->loop, app->set_retry_ms, 0, on_set_retry, app, &id) != FIRC_OK) {
        FIRC_WARN("could not arm the retry of a subnet set; the next pass writes it");
        app_nf_enter(app);
        app->set_retry_due = true;
        app_nf_leave(app);
        return;
    }
    app->set_retry_timer = id;
}

static firc_err_t leave_out_for_set(firc_app_t *app, firc_ruleset_t *rs) {
    const firc_group_t *g = firc_ruleset_group(rs);
    if (app->n_set_left == app->cap_set_left) {
        size_t cap = app->cap_set_left ? app->cap_set_left * 2 : 8;
        firc_id_t *v = realloc(app->set_left, cap * sizeof(*v));
        if (v == NULL) { return FIRC_ERR_NOMEM; }
        app->set_left = v;
        app->cap_set_left = cap;
    }
    app->set_left[app->n_set_left++] = g->id;
    firc_ruleset_set_pass(rs, FIRC_RULESET_PASS_LEFT_OUT);
    char id[FIRC_ID_STR_LEN];
    firc_id_format(g->id, id);
    FIRC_WARN("group \"%s\" (%s) is left out of netfilter: its subnet set is not in the kernel",
              g->name != NULL ? g->name : "", id);
    return FIRC_OK;
}

static void owe_set_left(firc_app_t *app) {
    for (size_t i = 0; i < app->n_set_left; i++) {
        firc_ruleset_t *rs = firc_app_find_group_by_id(app, app->set_left[i]);
        if (rs != NULL && firc_ruleset_pass(rs) == FIRC_RULESET_PASS_LEFT_OUT) { firc_ruleset_owe_flush(rs); }
    }
    app->n_set_left = 0;
}

static firc_err_t stage_mss(firc_app_t *app, const firc_ruleset_t *skip, const char *prefix) {
    app->mss_staged[0] = app->mss_staged[1] = false;
    bool used[FIRC_KTCP_DEVICES] = {false};
    for (size_t i = 0; i < app->n_rulesets; i++) {
        const firc_ruleset_t *rs = app->rulesets[i];
        if (!staged_in_pass(rs, skip)) { continue; }
        const firc_group_t *g = firc_ruleset_group(rs);
        for (size_t k = 0; k < firc_group_n_ifaces(g); k++) {
            int dev = tun_dev(app, firc_group_iface_at(g, k));
            if (dev >= 0) { used[dev] = true; }
        }
    }
    const char *devs[FIRC_KTCP_DEVICES];
    size_t n = 0;
    for (size_t d = 0; d < FIRC_KTCP_DEVICES; d++) {
        if (used[d]) { devs[n++] = app->tun_names[d]; }
    }
    if (n == 0) { return FIRC_OK; }
    firc_ipt_t *const ipts[] = {app->ipt4, app->ipt6};
    for (size_t f = 0; f < 2; f++) {
        if (ipts[f] == NULL || app->mss_off[f] || !firc_ipt_has(ipts[f], "TCPMSS")) { continue; }
        firc_err_t err = firc_mss_build_rules(ipts[f], prefix, devs, n);
        if (err != FIRC_OK) { return err; }
        app->mss_staged[f] = true;
    }
    return FIRC_OK;
}

static firc_err_t rebuild_netfilter_locked(firc_app_t *app, firc_cancel_t *cancel, bool full,
                                           const firc_ruleset_t *skip, int *refusal) {
    *refusal = 0;
    app->refused_table = NULL;
    app->refused_v6 = false;
    firc_err_t err = firc_netfilter_register_base_chains(app->ipt4, app->ipt6);
    if (err != FIRC_OK) { return err; }

    err = firc_port_remap_prepare_iptables(app->port_remap);
    if (err != FIRC_OK) { return err; }

    err = firc_app_stage_tap(app); /* every pass while a capture runs: the pass after a rewrite puts its rules back */
    if (err != FIRC_OK) { return err; }
    err = firc_app_stage_autolist(app);
    if (err != FIRC_OK) { return err; }

    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (!staged_in_pass(rs, skip)) { continue; }
        if (set_missing(app, rs)) {
            err = leave_out_for_set(app, rs);
            if (err != FIRC_OK) { return err; }
            continue;
        }
        err = firc_ruleset_prepare_iptables(rs);
        if (err != FIRC_OK) { return err; }
    }
    err = firc_app_stage_pool_reject(app);
    if (err != FIRC_OK) { return err; }

    /* every mapping from this pass's snapshot, routed groups only; a non-routed group dies on the unreachable route */
    const char *prefix = app->cfg->app.netfilter.iptables.chain_prefix;
    routed_ids_t routed = {NULL, 0};
    if (app->n_rulesets != 0) {
        routed.ids = calloc(app->n_rulesets, sizeof(*routed.ids));
        if (routed.ids == NULL) { return FIRC_ERR_NOMEM; }
    }
    for (size_t i = 0; i < app->n_rulesets; i++) {
        if (staged_in_pass(app->rulesets[i], skip)) {
            firc_id_format(firc_ruleset_group(app->rulesets[i])->id, routed.ids[routed.n++]);
        }
    }
    if (routed.n > 1) { qsort(routed.ids, routed.n, sizeof(*routed.ids), routed_id_cmp); }
    if (app->ipt4 && app->pass_snap) {
        err = firc_dnat_build_rules(app->ipt4, prefix, app->pass_snap, routed_has, &routed);
    }
    if (err == FIRC_OK && app->ipt6 && app->pass_snap) {
        err = firc_dnat_build_rules(app->ipt6, prefix, app->pass_snap, routed_has, &routed);
    }
    free(routed.ids);
    if (err != FIRC_OK) { return err; }
    err = stage_redir(app, skip, prefix, full);
    if (err != FIRC_OK) { return err; }
    err = stage_mss(app, skip, prefix);
    if (err != FIRC_OK) { return err; }

    if (firc_cancel_raised(cancel)) { return FIRC_ERR_CANCELED; }

    if (app->ipt4) {
        err = full ? firc_ipt_commit_full(app->ipt4) : firc_ipt_commit(app->ipt4);
        *refusal = firc_ipt_refusal(app->ipt4);
        app->refused_table = firc_ipt_refused_table(app->ipt4);
        if (err != FIRC_OK) { return err; }
    }
    if (app->ipt6) {
        err = full ? firc_ipt_commit_full(app->ipt6) : firc_ipt_commit(app->ipt6);
        *refusal = firc_ipt_refusal(app->ipt6);
        app->refused_table = firc_ipt_refused_table(app->ipt6);
        app->refused_v6 = true;
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static void say_pass_group(bool out, const firc_ruleset_t *rs) {
    char id[FIRC_ID_STR_LEN];
    const firc_group_t *g = firc_ruleset_group(rs);
    firc_id_format(g->id, id);
    if (out) {
        FIRC_WARN("group \"%s\" (%s) is left out of netfilter: the kernel refused the pass with it",
                  g->name != NULL ? g->name : "", id);
    } else {
        FIRC_INFO("group \"%s\" (%s) is back in netfilter", g->name != NULL ? g->name : "", id);
    }
}

static bool refused_rule(firc_err_t err, int refusal) {
    return err == FIRC_ERR_IO && (refusal == EINVAL || refusal == ENOENT || refusal == ELOOP);
}

static firc_err_t rebuild_leaving_out(firc_app_t *app, firc_cancel_t *cancel, bool full, bool *set_gone) {
    *set_gone = false;
    int refusal = 0;
    app->redir_staged = false;
    firc_err_t err = rebuild_netfilter_locked(app, cancel, full, NULL, &refusal);
    bool tried = false;
    while (refused_rule(err, refusal) && app->refused_table != NULL) {
        int f = app->refused_v6 ? 1 : 0;
        if (strcmp(app->refused_table, "mangle") == 0 && app->mss_staged[f] && !app->mss_off[f]) {
            app->mss_off[f] = true;
        } else if (strcmp(app->refused_table, "nat") == 0 && !app->refused_v6 && app->redir_staged &&
                   !app->redir_off) {
            app->redir_off = true;
        } else {
            break;
        }
        tried = true;
        firc_ipt_discard(app->ipt4);
        firc_ipt_discard(app->ipt6);
        err = rebuild_netfilter_locked(app, cancel, full, NULL, &refusal);
    }
    bool mss_dropped = app->mss_off[0] || app->mss_off[1], redir_dropped = app->redir_off;
    app->mss_off[0] = app->mss_off[1] = false;
    app->redir_off = false;
    if (tried && err == FIRC_OK) {
        if (mss_dropped && !app->mss_refused_said) {
            app->mss_refused_said = true;
            FIRC_ERROR("the kernel refused the TCP MSS clamp; TCP into tunvless keeps the client's MSS");
        }
        if (redir_dropped && !app->redir_refused_said) {
            app->redir_refused_said = true;
            FIRC_ERROR("the kernel refused the kernel TCP redirect; TCP stays on the TUN");
        }
    } else if (tried) {
        firc_ipt_discard(app->ipt4);
        firc_ipt_discard(app->ipt6);
        err = rebuild_netfilter_locked(app, cancel, full, NULL, &refusal);
    }
    bool refused = refused_rule(err, refusal);
    bool search = refused && app->pass_refused;
    if (err != FIRC_ERR_CANCELED && err != FIRC_ERR_AGAIN) { app->pass_refused = refused; }
    firc_ruleset_t *out = NULL;
    for (size_t i = 0; search && refused && i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (!staged_in_pass(rs, NULL)) { continue; }
        firc_ipt_discard(app->ipt4);
        firc_ipt_discard(app->ipt6);
        firc_err_t again = rebuild_netfilter_locked(app, cancel, full, rs, &refusal);
        if (again == FIRC_OK) { out = rs; }
        refused = refused_rule(again, refusal);
        if (!refused) { err = again; }
    }
    if (search && out == NULL) {
        firc_ipt_discard(app->ipt4);
        firc_ipt_discard(app->ipt6);
        (void)rebuild_netfilter_locked(app, cancel, full, NULL, &refusal);
        if (refused) { app->pass_refused = false; }
    }
    if (err != FIRC_OK) {
        *set_gone = refused_rule(err, refusal) && refusal == ENOENT;
        return err;
    }
    owe_set_left(app);
    app->pass_refused = false;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        if (rs == out || firc_ruleset_pass(rs) != FIRC_RULESET_PASS_ON_TRIAL) { continue; }
        if (firc_ruleset_routed(rs)) { say_pass_group(false, rs); }
        firc_ruleset_set_pass(rs, FIRC_RULESET_PASS_IN);
    }
    if (out != NULL) {
        firc_ruleset_set_pass(out, FIRC_RULESET_PASS_LEFT_OUT);
        firc_ruleset_owe_flush(out);
        say_pass_group(true, out);
    }
    return FIRC_OK;
}

firc_err_t firc_app_stage_tap(firc_app_t *app) {
    if (app == NULL || !app->tap_open) { return FIRC_OK; }
    const char *prefix = app->cfg->app.netfilter.iptables.chain_prefix;
    firc_ipt_t *engines[] = {app->ipt4, app->ipt6};
    for (unsigned fam = 0; fam < 2; fam++) {
        if (engines[fam] == NULL) { continue; }
        firc_err_t err = firc_tap_rules_install(engines[fam], prefix, app->pool,
                                                (const char *const *)app->tap_lan,
                                                app->n_tap_lan);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

/* dedup table has no eviction; once full, every further bypass becomes its own event. 1024 is an estimate. */
#define TAP_MAX_FLOWS 1024

/* unprefixed: a prefix would cut the sentence's end off, already at most one log event's text */
_Static_assert(FIRC_TAP_SENTENCE_MAX < FIRC_EVENT_TEXT, "a summary sentence fits a log event");
static void say_sentence(const char *sentence, void *ud) {
    (void)ud;
    FIRC_INFO("%s", sentence);
}

_Static_assert(FIRC_CAPTURE_LAST_SENTENCES == FIRC_TAP_SENTENCES_MAX, "a kept summary holds every sentence");
_Static_assert(FIRC_CAPTURE_LAST_SENTENCE_BYTES > FIRC_TAP_SENTENCE_MAX, "a kept sentence fits");
static void keep_sentence(const char *sentence, void *ud) {
    firc_capture_last_t *last = ud;
    say_sentence(sentence, NULL);
    if (last->n_summary < FIRC_CAPTURE_LAST_SENTENCES) {
        snprintf(last->summary[last->n_summary++], sizeof(last->summary[0]), "%s", sentence);
    }
}

static void tap_lan_free(char **lan, size_t n) {
    for (size_t i = 0; i < n; i++) { free(lan[i]); }
    free(lan);
}

/* built before the rules are staged: a reader not yet bound is packets the kernel drops */
static firc_err_t tap_reader_up(firc_app_t *app) {
    static const uint16_t groups[2] = {FIRC_TAP_GROUP_NEW, FIRC_TAP_GROUP_HELLO};
    static const uint16_t ranges[2] = {FIRC_TAP_HEAD_RANGE, FIRC_TAP_COPY_RANGE};
    firc_tap_pool_t pool;
    if (!firc_tap_pool_from_fakeip(app->pool, &pool)) { return FIRC_ERR_STATE; }
    firc_tap_report_t *report = firc_tap_report_new(TAP_MAX_FLOWS);
    if (report == NULL) { return FIRC_ERR_NOMEM; }

    firc_nflog_t *socks[2] = {NULL, NULL};
    bool on_loop[2] = {false, false};
    firc_err_t err = FIRC_OK;
    for (int w = 0; w < 2 && err == FIRC_OK; w++) {
        socks[w] = app->tap_nflog_open(groups[w], ranges[w]);
        if (socks[w] == NULL) {
            FIRC_WARN("capture refused: could not bind NFLOG group %u", (unsigned)groups[w]);
            err = FIRC_ERR_SYS;
            break;
        }
        app->tap_reader[w].app = app;
        app->tap_reader[w].which = w;
        /* without a loop the group is bound but nothing drains it; the kernel still copies in rather than drop it */
        if (app->loop != NULL) {
            firc_err_t watched = firc_loop_add_fd(app->loop, firc_nflog_fd(socks[w]), EPOLLIN,
                                                  on_tap_readable, &app->tap_reader[w]);
            if (watched != FIRC_OK) { /* same FIRC_ERR_SYS as a failed bind; this log line is what tells them apart */
                FIRC_WARN("capture refused: NFLOG group %u bound, but the event loop would not "
                           "watch its socket (%s)",
                           (unsigned)groups[w], firc_err_str(watched));
                err = FIRC_ERR_SYS;
                break;
            }
            on_loop[w] = true;
        }
    }
    if (err != FIRC_OK) {
        for (int w = 0; w < 2; w++) {
            if (on_loop[w]) { (void)firc_loop_del_fd(app->loop, firc_nflog_fd(socks[w])); }
            if (socks[w] != NULL) { firc_nflog_close(socks[w]); }
        }
        firc_tap_report_free(report);
        return err;
    }
    for (int w = 0; w < 2; w++) { app->tap_reader[w].sock = socks[w]; }
    app->tap_report = report;
    app->tap_pool = pool;
    return FIRC_OK;
}

/* nothing is kept after a capture: its events are its result; safe on one that never had readers */
static void tap_reader_down(firc_app_t *app) {
    for (int w = 0; w < 2; w++) {
        firc_nflog_t *sock = app->tap_reader[w].sock;
        if (sock == NULL) { continue; }
        if (app->loop != NULL) { (void)firc_loop_del_fd(app->loop, firc_nflog_fd(sock)); }
        firc_nflog_close(sock);
        app->tap_reader[w].sock = NULL;
    }
    firc_tap_report_free(app->tap_report);
    app->tap_report = NULL;
    memset(&app->tap_pool, 0, sizeof(app->tap_pool));
}

/* The kernel has something on one of the two sockets. */
static void on_tap_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    (void)fd;
    (void)events;
    struct tap_reader *r = ud;
    firc_app_t *app = r->app;
    if (r->sock == NULL) { return; }

    /* read fresh on every wakeup, not kept: the judge's covers() reads the pipeline's own snapshot too */
    firc_tap_drain_t d;
    memset(&d, 0, sizeof(d));
    d.ctx.snap = app->pipeline != NULL ? firc_dns_pipeline_snapshot(app->pipeline) : NULL;
    d.ctx.pool = &app->tap_pool;
    d.ctx.pipeline = app->pipeline;
    d.ctx.recall = app->pipeline != NULL ? firc_dns_pipeline_recall(app->pipeline) : NULL;
    d.ctx.policy = app->tap_policy;
    d.ctx.device = app->tap_device;
    d.ctx.ud = app->tap_resolver_ud;
    d.report = app->tap_report;
    d.which = r->which;

    /* bounded, not "until empty": draining a flood here would stop the daemon answering DNS while it did */
    for (int i = 0; i < 64; i++) {
        firc_err_t e = firc_tap_drain(&d, r->sock, NULL);
        if (e == FIRC_ERR_AGAIN) { return; }
        if (e != FIRC_OK) { /* the reader is broken; carrying on would report a false clean five minutes */
            FIRC_WARN("capture: its %s reader failed (%s), so the capture ends here",
                       r->which == 0 ? "new-connection" : "ClientHello", firc_err_str(e));
            (void)capture_end(app, NULL, "failed");
            return;
        }
    }
}

void firc_app_set_policy_resolver(firc_app_t *app, firc_devsel_policy_fn policy,
                                  firc_devsel_device_fn device, void *ud) {
    if (app == NULL) { return; }
    app->tap_policy = policy;
    app->tap_device = device;
    app->tap_resolver_ud = ud;
}

firc_err_t firc_app_stage_autolist(firc_app_t *app) {
    if (app == NULL || !app->al_on) { return FIRC_OK; }
    const char *prefix = app->cfg->app.netfilter.iptables.chain_prefix;
    firc_ipt_t *engines[] = {app->ipt4, app->ipt6};
    for (unsigned fam = 0; fam < 2; fam++) {
        if (engines[fam] == NULL) { continue; }
        firc_err_t err = firc_autolist_rules_install(engines[fam], prefix, app->pool,
                                                     (const char *const *)app->al_lan, app->n_al_lan);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static const firc_group_snapshot_t *autolist_target(firc_app_t *app) {
    const char *gid = app->cfg->app.autolist.group;
    firc_id_t id;
    if (gid == NULL || firc_id_parse(gid, &id) != FIRC_OK || app->pipeline == NULL) { return NULL; }
    const firc_ruleset_snapshot_t *snap = firc_dns_pipeline_snapshot(app->pipeline);
    for (size_t i = 0; snap != NULL && i < snap->n_groups; i++) {
        if (firc_id_equal(snap->groups[i]->id, id)) { return snap->groups[i]; }
    }
    return NULL;
}

static firc_ruleset_t *autolist_target_rs(const firc_app_t *app) {
    const char *gid = app->cfg->app.autolist.group;
    firc_id_t id;
    if (gid == NULL || firc_id_parse(gid, &id) != FIRC_OK) { return NULL; }
    return firc_app_find_group_by_id(app, id);
}

static const char *autolist_target_off(const firc_app_t *app) {
    firc_ruleset_t *rs = autolist_target_rs(app);
    if (rs == NULL) { return "the group it adds to does not exist; choose one in the settings"; }
    if (firc_group_is_block(firc_ruleset_group(rs))) {
        return "the group blocks by DNS: nothing to route its names into";
    }
    return NULL;
}

static bool autolist_running(const firc_app_t *app) {
    return app->al_svc != NULL && autolist_target_off(app) == NULL;
}

static bool autolist_add_mode(const firc_app_t *app) {
    const char *mode = app->cfg->app.autolist.mode;
    return mode != NULL && strcmp(mode, "add") == 0;
}

static int64_t autolist_now(const firc_app_t *app) { return app->wall_s(); }

static bool autolist_eligible(void *ud, const firc_ip_t *client) {
    firc_app_t *app = ud;
    const firc_group_snapshot_t *g = autolist_target(app);
    if (g == NULL || g->block >= 0) { return false; }
    if (g->devices == NULL) { return true; }
    return firc_devsel_allows(g->devices, client, app->tap_policy, app->tap_device, app->tap_resolver_ud);
}

static void autolist_hint(void *ud, const firc_ip_t *client, const char *name, const firc_ip_t *reals,
                          size_t n_reals) {
    firc_app_t *app = ud;
    if (!autolist_eligible(app, client)) { return; }
    firc_autolist_svc_hint(app->al_svc, client, name, reals, n_reals);
}

static const char *autolist_why_text(uint8_t why) {
    switch (why) {
    case FIRC_AL_WHY_NO_ANSWER: return "no answer to the ClientHello";
    case FIRC_AL_WHY_RESET: return "a reset before the server answered";
    case FIRC_AL_WHY_FREEZE: return "a freeze the router's probe confirmed";
    case FIRC_AL_WHY_NO_CONNECT: return "no connection the router's probe confirmed";
    default: return "blocked";
    }
}

static bool autolist_covers_excluded(const firc_app_config_t *ac, const char *domain) {
    char *const one[] = {(char *)domain};
    for (size_t i = 0; i < ac->autolist.n_exclude; i++) {
        if (firc_autolist_excluded(ac->autolist.exclude[i], one, 1)) { return true; }
    }
    return false;
}

static bool autolist_domain(const firc_app_t *app, const char *host, const char *what, char *domain, size_t cap) {
    const firc_app_config_t *ac = &app->cfg->app;
    if (firc_autolist_excluded(host, ac->autolist.exclude, ac->autolist.n_exclude)) {
        FIRC_INFO("autolist: %s, but it is excluded", what);
        return false;
    }
    if (!firc_regdomain(host, domain, cap)) {
        FIRC_INFO("autolist: %s, but it has no domain to add", what);
        return false;
    }
    if (autolist_covers_excluded(ac, domain)) {
        FIRC_INFO("autolist: %s, but %s holds an excluded name", what, domain);
        return false;
    }
    return true;
}

static firc_err_t autolist_rule_id(const firc_group_t *g, firc_id_t *out) {
    for (int tries = 0; tries < 8; tries++) {
        firc_err_t err = firc_id_random(out);
        if (err != FIRC_OK) { return err; }
        bool taken = firc_id_is_zero(*out);
        for (size_t i = 0; i < g->n_rules && !taken; i++) { taken = firc_id_equal(g->rules[i]->id, *out); }
        if (!taken) { return FIRC_OK; }
    }
    return FIRC_ERR_EXIST;
}

static uint32_t autolist_added_hour(const firc_app_t *app, int64_t now) {
    uint32_t n = 0;
    for (size_t i = 0; i < AL_ADDS_PER_HOUR; i++) {
        if (app->al_added[i] != 0 && now - app->al_added[i] < 3600) { n++; }
    }
    return n;
}

static void autolist_count_add(firc_app_t *app, int64_t now) {
    app->al_added[app->al_added_at] = now != 0 ? now : 1;
    app->al_added_at = (app->al_added_at + 1) % AL_ADDS_PER_HOUR;
}

static firc_err_t autolist_set_rules(firc_app_t *app, firc_ruleset_t *rs, firc_group_t *next) {
    bool unsaved = false;
    firc_err_t err = firc_app_set_hand_rules(app, rs, next, app->config_path, app->config_version, &unsaved);
    if (err == FIRC_OK) { return FIRC_OK; }
    const firc_group_t *g = firc_ruleset_group(rs);
    const char *name = g->name != NULL ? g->name : "";
    if (unsaved) {
        FIRC_ERROR("autolist: group \"%s\" changed, but groups.yaml not saved: %s", name, firc_err_str(err));
        return FIRC_OK;
    }
    FIRC_ERROR("autolist: group \"%s\" not synced, its rules put back: %s", name, firc_err_str(err));
    return err;
}

static size_t autolist_rule_at(const firc_group_t *g, const char *domain) {
    for (size_t i = 0; i < g->n_rules; i++) {
        if (g->rules[i]->rule != NULL && strcmp(g->rules[i]->rule, domain) == 0) { return i; }
    }
    return SIZE_MAX;
}

static firc_rule_t *autolist_rule_named(const firc_group_t *g, const char *domain) {
    size_t at = autolist_rule_at(g, domain);
    return at != SIZE_MAX ? g->rules[at] : NULL;
}

static firc_err_t autolist_add_rule(firc_app_t *app, const char *domain, const char *host, firc_al_why_t why) {
    firc_ruleset_t *rs = autolist_target_rs(app);
    if (rs == NULL || firc_group_is_block(firc_ruleset_group(rs))) { return FIRC_ERR_STATE; }
    firc_group_t *g = firc_ruleset_group_mut(rs);
    const char *gname = g->name != NULL ? g->name : "";
    const firc_al_cand_t *c = firc_al_cands_find(app->al_cands, domain);
    unsigned fails = c != NULL ? (unsigned)c->fails : 0;
    bool by_domain = c != NULL && c->by_domain;
    size_t at = autolist_rule_at(g, domain);
    if (at != SIZE_MAX) {
        const firc_rule_t *have = g->rules[at];
        if (have->autolist == NULL) { return FIRC_ERR_EXIST; }
        if (have->enable) { return FIRC_OK; }
        firc_rule_t *on = NULL;
        firc_group_t next = {0};
        firc_err_t set = firc_rule_dup(have, &on);
        if (set == FIRC_OK) { set = firc_strset(&on->autolist, host); }
        if (set == FIRC_OK) {
            on->enable = true;
            on->autolist_by_domain = by_domain;
            set = firc_group_rules_with(g, at, on, NULL, &next);
        }
        if (set != FIRC_OK) {
            firc_rule_free(on);
            FIRC_ERROR("autolist: %s not switched on again: %s", domain, firc_err_str(set));
            return set;
        }
        set = autolist_set_rules(app, rs, &next);
        if (set != FIRC_OK) { return set; }
        app->al_switched_on++;
        FIRC_INFO("autolist: %s switched on again in group \"%s\" for %s: %u failures, %s", domain, gname, host, fails,
                  autolist_why_text(why));
        return FIRC_OK;
    }
    firc_rule_t *r = firc_rule_new();
    firc_err_t err = r != NULL ? autolist_rule_id(g, &r->id) : FIRC_ERR_NOMEM;
    if (err == FIRC_OK) { err = firc_strset(&r->type, "namespace"); }
    if (err == FIRC_OK) { err = firc_strset(&r->rule, domain); }
    if (err == FIRC_OK) { err = firc_strset(&r->autolist, host); }
    firc_group_t next = {0};
    if (err == FIRC_OK) {
        r->enable = true;
        r->autolist_by_domain = by_domain;
        err = firc_group_rules_with(g, SIZE_MAX, NULL, r, &next);
    }
    if (err != FIRC_OK) {
        firc_rule_free(r);
        FIRC_ERROR("autolist: %s not added: %s", domain, firc_err_str(err));
        return err;
    }
    err = autolist_set_rules(app, rs, &next);
    if (err != FIRC_OK) { return err; }
    autolist_count_add(app, autolist_now(app));
    FIRC_INFO("autolist: %s added to group \"%s\" for %s: %u failures, %s", domain, gname, host, fails,
              autolist_why_text(why));
    return FIRC_OK;
}

static void autolist_auto_add(firc_app_t *app, const char *domain, const char *host, firc_al_why_t why) {
    firc_ruleset_t *rs = autolist_target_rs(app);
    bool known = rs != NULL && autolist_rule_named(firc_ruleset_group(rs), domain) != NULL;
    if (!known && autolist_added_hour(app, autolist_now(app)) >= AL_ADDS_PER_HOUR) {
        if (!app->al_cap_said) {
            app->al_cap_said = true;
            FIRC_WARN("autolist: %d hosts added in the last hour, so %s and others wait: so many failing at once "
                      "is more likely the network than blocks",
                      AL_ADDS_PER_HOUR, domain);
        }
        return;
    }
    firc_err_t err = autolist_add_rule(app, domain, host, why);
    firc_al_cand_t *c = firc_al_cands_find(app->al_cands, domain);
    if (err == FIRC_ERR_EXIST) {
        const firc_group_t *g = firc_ruleset_group(rs);
        FIRC_INFO("autolist: %s is already a rule of group \"%s\"", domain, g->name != NULL ? g->name : "");
        if (c != NULL) { c->state = FIRC_AL_CAND; }
        return;
    }
    if (err != FIRC_OK) { return; }
    app->al_cap_said = false;
    if (c != NULL) { c->state = FIRC_AL_ADDED; }
}

static void autolist_listed(void *ud, const firc_autolist_failure_t *f) {
    firc_app_t *app = ud;
    if (!autolist_running(app)) { return; }
    firc_al_why_t why = f->why == FIRC_AUTOLIST_RESET ? FIRC_AL_WHY_RESET : FIRC_AL_WHY_NO_ANSWER;
    bool by_domain = f->domain[0] != '\0';
    char what[2 * FIRC_AUTOLIST_NAME_MAX + 96];
    if (by_domain) {
        snprintf(what, sizeof(what), "%s failed %u times on its hosts, most on %s (%s)", f->domain,
                 (unsigned)f->fails, f->host, autolist_why_text(why));
    } else {
        snprintf(what, sizeof(what), "%s failed %u times in a row (%s)", f->host, (unsigned)f->fails,
                 autolist_why_text(why));
    }
    char domain[FIRC_AUTOLIST_NAME_MAX];
    if (!autolist_domain(app, by_domain ? f->domain : f->host, what, domain, sizeof(domain))) { return; }
    firc_al_cand_t *c =
        firc_al_cands_note(app->al_cands, domain, f->host, why, f->fails, &f->client, autolist_now(app));
    if (c == NULL) {
        FIRC_DEBUG("autolist: %s, but %s was dismissed within the day", what, domain);
        return;
    }
    c->by_domain = by_domain;
    if (autolist_add_mode(app)) {
        autolist_auto_add(app, domain, f->host, why);
        return;
    }
    if (c->state != FIRC_AL_CHECKING && c->state != FIRC_AL_ADDED) { c->state = FIRC_AL_CAND; }
}

static void autolist_ptr_ask(firc_app_t *app, const firc_ip_t *server) {
    if (app->al_ptr == NULL) { return; }
    int64_t now = app->wall_s();
    int64_t oldest = app->al_ptr_sent[app->al_ptr_sent_at];
    if (oldest != 0 && oldest <= now && now - oldest < 60) { return; }
    firc_ifcheck_settings_t s;
    firc_ifcheck_settings_from(&app->cfg->app, &s);
    char name[FIRC_AUTOLIST_NAME_MAX];
    bool sent = false;
    (void)firc_ptr_get(app->al_ptr, server, s.upstream, s.upstream_port, (int64_t)app->mono_ms(), name, sizeof(name),
                       &sent);
    if (!sent) { return; }
    app->al_ptr_sent[app->al_ptr_sent_at] = now != 0 ? now : 1;
    app->al_ptr_sent_at = (app->al_ptr_sent_at + 1) % AL_PTR_PER_MINUTE;
}

static void autolist_bypassed(void *ud, const firc_autolist_failure_t *f) {
    autolist_ptr_ask(ud, &f->server);
}

static bool autolist_counts(const firc_app_t *app, const firc_ip_t *client, const char *name,
                            char group[FIRC_ID_STR_LEN]) {
    const firc_ruleset_snapshot_t *snap = firc_dns_pipeline_snapshot(app->pipeline);
    const firc_group_snapshot_t *owner = firc_ruleset_snapshot_owner(snap, name, client, app->tap_policy,
                                                                     app->tap_device, app->tap_resolver_ud, NULL);
    if (owner == NULL || owner->block >= 0) { return false; }
    firc_id_format(owner->id, group);
    return true;
}

static void autolist_flow(void *ud, const firc_ip_t *client, const firc_ip_t *server, uint8_t proto, uint16_t port,
                          const char *sni) {
    firc_app_t *app = ud;
    if (app->pipeline == NULL || !autolist_running(app)) { return; }
    if (sni == NULL && proto == 6 && port == 443) { return; }
    char group[FIRC_ID_STR_LEN];
    if (sni != NULL && sni[0] != '\0') {
        if (firc_autolist_svc_hinted_name(app->al_svc, client, server, sni)) { return; }
        if (autolist_counts(app, client, sni, group)) {
            firc_autolist_svc_bypass_seen(app->al_svc, client, server, proto, port, sni, group, FIRC_AL_HOW_SNI);
        }
        return;
    }
    if (firc_autolist_svc_hinted(app->al_svc, client, server)) { return; }
    char name[FIRC_AUTOLIST_NAME_MAX];
    if (firc_recall_by_real(firc_dns_pipeline_recall(app->pipeline), server, name, sizeof(name), NULL)) {
        if (autolist_counts(app, client, name, group)) {
            firc_autolist_svc_bypass_seen(app->al_svc, client, server, proto, port, name, group, FIRC_AL_HOW_ANSWER);
        }
        return;
    }
    if (app->al_ptr == NULL) { return; }
    firc_ptr_state_t st = firc_ptr_peek(app->al_ptr, server, (int64_t)app->mono_ms(), name, sizeof(name));
    if (st == FIRC_PTR_PENDING) {
        autolist_ptr_ask(app, server);
    } else if (st == FIRC_PTR_NAME && autolist_counts(app, client, name, group)) {
        firc_autolist_svc_bypass_seen(app->al_svc, client, server, proto, port, name, group, FIRC_AL_HOW_PTR);
    }
}

static void autolist_nominated(void *ud, const firc_autolist_nomination_t *n) {
    firc_app_t *app = ud;
    if (!autolist_running(app)) { return; }
    bool stall = n->kind != FIRC_AL_NOM_NO_CONNECT;
    bool by_domain = n->domain[0] != '\0';
    const char *name = by_domain ? n->domain : n->host;
    char what[FIRC_AUTOLIST_NAME_MAX + 96];
    if (stall) {
        snprintf(what, sizeof(what), "%s stalled after %u bytes on two connections", name, (unsigned)n->received);
    } else {
        snprintf(what, sizeof(what), "%s got no answer to its SYNs", name);
    }
    char domain[FIRC_AUTOLIST_NAME_MAX];
    if (!autolist_domain(app, name, what, domain, sizeof(domain))) { return; }
    int64_t now = autolist_now(app);
    firc_al_cand_t *c = firc_al_cands_find(app->al_cands, domain);
    if (c != NULL && (c->state == FIRC_AL_ADDED || c->state == FIRC_AL_OFF)) {
        FIRC_DEBUG("autolist: %s, but %s is already added", what, domain);
        return;
    }
    const firc_autolist_result_t *shown =
        c != NULL && c->state == FIRC_AL_CAND ? firc_al_probes_result(app->al_probes, n->host, now, NULL) : NULL;
    bool confirmed = shown != NULL && shown->verdict == FIRC_AUTOLIST_BLOCKED;
    if (confirmed) {
        FIRC_DEBUG("autolist: %s, but it was confirmed blocked lately and waits for the user", what);
    } else {
        firc_al_ask_t ask = firc_al_probes_nominate(app->al_probes, n->host, by_domain, now);
        if (ask != FIRC_AL_ASK_QUEUED) {
            FIRC_DEBUG("autolist: %s, but it is not probed: %s", what,
                       ask == FIRC_AL_ASK_CACHED ? "it was probed lately" : "the queue holds only requests");
            return;
        }
    }
    uint32_t fails = c != NULL ? c->fails : 0;
    firc_al_why_t why = c != NULL ? (firc_al_why_t)c->why : stall ? FIRC_AL_WHY_FREEZE : FIRC_AL_WHY_NO_CONNECT;
    c = firc_al_cands_note(app->al_cands, domain, n->host, why, fails, &n->client, now);
    if (c == NULL) { return; }
    c->state = confirmed ? FIRC_AL_CAND : FIRC_AL_CHECKING;
    c->by_domain = by_domain;
    if (!stall) { return; }
    c->notes = c->notes > UINT8_MAX - FIRC_AUTOLIST_NOTES ? UINT8_MAX : (uint8_t)(c->notes + FIRC_AUTOLIST_NOTES);
}

static void autolist_lan_free(char **lan, size_t n) {
    for (size_t i = 0; i < n; i++) { free(lan[i]); }
    free(lan);
}

static const char *autolist_via(const firc_app_t *app) {
    const char *iface = app->cfg->app.autolist.check_iface;
    return iface != NULL && iface[0] != '\0' ? iface : "the default route";
}

static void autolist_rechecked(firc_app_t *app, const char *host, const firc_autolist_result_t *r) {
    const char *via = autolist_via(app);
    if (r->verdict == FIRC_AUTOLIST_BLOCKED) {
        FIRC_DEBUG("autolist: %s is still blocked through %s: %s", host, via, r->error);
        return;
    }
    if (r->verdict != FIRC_AUTOLIST_REACHABLE) {
        FIRC_DEBUG("autolist: %s could not be checked through %s: %s", host, via, r->error);
        return;
    }
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        const firc_group_t *g = firc_ruleset_group(rs);
        bool any = false;
        for (size_t k = 0; !any && k < g->n_rules; k++) {
            const firc_rule_t *rule = g->rules[k];
            any = rule->enable && rule->autolist != NULL && strcmp(rule->autolist, host) == 0;
        }
        if (!any) { continue; }
        firc_group_t next = {0};
        bool *off = calloc(g->n_rules, sizeof(*off));
        size_t n_off = 0;
        firc_err_t err = off == NULL ? FIRC_ERR_NOMEM : FIRC_OK;
        for (size_t k = 0; err == FIRC_OK && k < g->n_rules; k++) {
            firc_rule_t *rule = g->rules[k];
            firc_rule_t *put = rule;
            bool mine = rule->enable && rule->autolist != NULL && strcmp(rule->autolist, host) == 0;
            const firc_al_cand_t *c = mine ? firc_al_cands_find(app->al_cands, rule->rule) : NULL;
            if (mine && r->short_body && rule->autolist_by_domain) {
                FIRC_DEBUG("autolist: %s answered a short page through %s, which says nothing about the rest of %s",
                           host, via, rule->rule);
                mine = false;
            }
            if (mine && r->short_body && c != NULL && c->why == FIRC_AL_WHY_FREEZE) {
                FIRC_DEBUG("autolist: %s answered a short page through %s, which says nothing about a freeze", host,
                           via);
                mine = false;
            }
            if (mine) {
                err = firc_rule_dup(rule, &put);
                if (err != FIRC_OK) { break; }
                put->enable = false;
                off[k] = true;
                n_off++;
            }
            err = firc_group_add_rule(&next, put);
            if (err != FIRC_OK && put != rule) { firc_rule_free(put); }
        }
        if (err != FIRC_OK) {
            for (size_t k = 0; k < next.n_rules; k++) {
                if (next.rules[k] != g->rules[k]) { firc_rule_free(next.rules[k]); }
            }
            FIRC_ERROR("autolist: %s opens through %s again, but group \"%s\" was left as it is: %s", host, via,
                       g->name != NULL ? g->name : "", firc_err_str(err));
        }
        if (err != FIRC_OK || n_off == 0 || autolist_set_rules(app, rs, &next) != FIRC_OK) {
            if (err != FIRC_OK || n_off == 0) { free(next.rules); }
            free(off);
            continue;
        }
        for (size_t k = 0; k < g->n_rules; k++) {
            if (!off[k]) { continue; }
            const firc_rule_t *rule = g->rules[k];
            firc_al_cand_t *c = firc_al_cands_find(app->al_cands, rule->rule);
            app->al_switched_off++;
            if (c != NULL) { c->state = FIRC_AL_OFF; }
            FIRC_INFO("autolist: %s opens through %s again, so %s is switched off in group \"%s\" until it is "
                      "blocked again",
                      host, via, rule->rule, g->name != NULL ? g->name : "");
        }
        free(off);
    }
}

static void autolist_probed(firc_app_t *app, const char *host, bool by_domain, const firc_autolist_result_t *r) {
    char what[FIRC_AUTOLIST_NAME_MAX + 96];
    snprintf(what, sizeof(what), "%s is blocked through %s (%s)", host, autolist_via(app),
             autolist_why_text(r->why));
    char domain[FIRC_AUTOLIST_NAME_MAX];
    if (r->verdict != FIRC_AUTOLIST_BLOCKED) {
        if (!firc_regdomain(host, domain, sizeof(domain))) { return; }
        firc_al_cand_t *c = firc_al_cands_find(app->al_cands, domain);
        if (c == NULL || c->state != FIRC_AL_CHECKING) { return; }
        if (c->fails > 0 || (r->verdict == FIRC_AUTOLIST_REACHABLE && r->short_body && c->why == FIRC_AL_WHY_FREEZE)) {
            c->state = FIRC_AL_CAND;
        } else {
            (void)firc_al_cands_forget(app->al_cands, domain);
        }
        return;
    }
    if (!autolist_domain(app, host, what, domain, sizeof(domain))) { return; }
    firc_al_why_t why = r->why != 0 ? (firc_al_why_t)r->why : FIRC_AL_WHY_NO_ANSWER;
    const firc_al_cand_t *was = firc_al_cands_find(app->al_cands, domain);
    firc_al_cand_t *c =
        firc_al_cands_note(app->al_cands, domain, host, why, was != NULL ? was->fails : 0, NULL, autolist_now(app));
    if (c != NULL) { c->by_domain = by_domain; }
    if (autolist_add_mode(app)) {
        autolist_auto_add(app, domain, host, why);
        return;
    }
    if (c != NULL && c->state != FIRC_AL_ADDED) { c->state = FIRC_AL_CAND; }
}

static void autolist_wan_seen(firc_app_t *app, const firc_autolist_result_t *r) {
    if (r->wan_unreachable) {
        if (!app->al_wan_warning) {
            FIRC_WARN("autolist: %s has no route, so no unanswered SYN can be confirmed", autolist_via(app));
        }
        app->al_wan_warning = true;
        return;
    }
    bool passed = r->verdict == FIRC_AUTOLIST_REACHABLE ||
                  (r->verdict == FIRC_AUTOLIST_BLOCKED && r->why == FIRC_AL_WHY_NO_CONNECT);
    if (passed) { app->al_wan_warning = false; }
}

static void autolist_probe_done(firc_app_t *app, const char *host, bool recheck, bool by_domain,
                                const firc_autolist_result_t *r) {
    int64_t now = autolist_now(app);
    firc_al_probes_done(app->al_probes, host, r, now);
    autolist_wan_seen(app, r);
    if (!recheck) {
        autolist_probed(app, host, by_domain, r);
        return;
    }
    if (!firc_al_probes_rechecking(app->al_probes)) { app->al_checked_at = now; }
    autolist_rechecked(app, host, r);
}

static void autolist_pump(firc_app_t *app, bool allow_notes);

static void autolist_checked(void *ud, uint64_t gen, const firc_autolist_probe_t *probes, size_t n) {
    firc_app_t *app = ud;
    if (gen != app->al_check_gen) { return; }
    firc_autolist_check_free(app->al_check);
    app->al_check = NULL;
    if (n == 1) {
        autolist_probe_done(app, probes[0].host, app->al_check_recheck, app->al_check_by_domain, &probes[0].result);
    }
    autolist_pump(app, false);
}

static void autolist_check_stop(firc_app_t *app) {
    app->al_check_gen++;
    firc_autolist_check_free(app->al_check);
    app->al_check = NULL;
}

static size_t autolist_pools(const firc_app_t *app, firc_autolist_net_t *pools) {
    size_t n = 0;
    static const unsigned families[FIRC_AUTOLIST_POOLS] = {FIRC_FAM_V4, FIRC_FAM_V6};
    for (size_t i = 0; i < FIRC_AUTOLIST_POOLS; i++) {
        if (firc_fakeip_pool_prefix(app->pool, families[i], &pools[n].base, &pools[n].prefix)) { n++; }
    }
    return n;
}

static bool autolist_iface_refused(const firc_app_t *app) {
    const char *iface = app->cfg->app.autolist.check_iface;
    firc_ruleset_t *rs = autolist_target_rs(app);
    return iface != NULL && iface[0] != '\0' && rs != NULL && firc_group_has_iface(firc_ruleset_group(rs), iface);
}

static void autolist_pump(firc_app_t *app, bool allow_notes) {
    if (app->al_check != NULL || app->loop == NULL || !autolist_running(app) || autolist_iface_refused(app)) {
        return;
    }
    firc_autolist_probe_t p;
    memset(&p, 0, sizeof(p));
    bool recheck = false, by_domain = false;
    if (!firc_al_probes_next(app->al_probes, autolist_now(app), p.host, sizeof(p.host), allow_notes, &recheck,
                             &by_domain)) {
        return;
    }
    const firc_app_config_t *ac = &app->cfg->app;
    firc_ifcheck_settings_t s;
    firc_ifcheck_settings_from(ac, &s);
    firc_autolist_check_opts_t o;
    memset(&o, 0, sizeof(o));
    snprintf(o.iface, sizeof(o.iface), "%s", ac->autolist.check_iface != NULL ? ac->autolist.check_iface : "");
    snprintf(o.dns, sizeof(o.dns), "%s", s.upstream);
    o.dns_port = s.upstream_port;
    o.timeout_s = 30;
    o.wan_expects_204 = s.wan_url[0] == '\0';
    snprintf(o.wan_url, sizeof(o.wan_url), "%s", o.wan_expects_204 ? s.url : s.wan_url);
    o.n_pools = autolist_pools(app, o.pools);
    if (o.wan_url[0] == '\0' && !app->al_wan_url_said) {
        app->al_wan_url_said = true;
        FIRC_WARN("autolist: app.check.url and app.check.wanUrl are both empty, so no unanswered SYN can be "
                  "confirmed as a block");
    }
    snprintf(app->al_check_host, sizeof(app->al_check_host), "%s", p.host);
    app->al_check_recheck = recheck;
    app->al_check_by_domain = by_domain;
    app->al_check = firc_autolist_check_start(app->loop, &o, &p, 1, ++app->al_check_gen, app->al_probe,
                                              autolist_checked, app);
    if (app->al_check == NULL) {
        firc_autolist_result_t r;
        memset(&r, 0, sizeof(r));
        snprintf(r.error, sizeof(r.error), "the probe could not start a thread");
        FIRC_WARN("autolist: %s not probed: %s", p.host, r.error);
        autolist_probe_done(app, p.host, recheck, by_domain, &r);
    }
}

static void autolist_recheck_run(firc_app_t *app) {
    int64_t now = autolist_now(app);
    const char **hosts = NULL;
    size_t n = 0, cap = 0;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        const firc_group_t *g = firc_ruleset_group(app->rulesets[i]);
        for (size_t k = 0; k < g->n_rules; k++) {
            const firc_rule_t *r = g->rules[k];
            if (!r->enable || r->autolist == NULL || r->autolist[0] == '\0') { continue; }
            bool seen = false;
            for (size_t j = 0; j < n && !seen; j++) { seen = strcmp(hosts[j], r->autolist) == 0; }
            if (seen) { continue; }
            if (n == cap) {
                size_t grown_cap = cap != 0 ? cap * 2 : 32;
                const char **grown = realloc(hosts, grown_cap * sizeof(*grown));
                if (grown == NULL) {
                    free(hosts);
                    FIRC_WARN("autolist: no recheck this time: out of memory");
                    return;
                }
                hosts = grown;
                cap = grown_cap;
            }
            hosts[n++] = r->autolist;
        }
    }
    size_t start = n != 0 ? app->al_recheck_from % n : 0;
    size_t take = n < AL_RECHECK_HOSTS ? n : AL_RECHECK_HOSTS;
    size_t queued = 0, done = 0;
    for (; done < take; done++) {
        firc_al_ask_t ask = firc_al_probes_recheck(app->al_probes, hosts[(start + done) % n], now);
        if (ask == FIRC_AL_ASK_FULL) { break; }
        if (ask == FIRC_AL_ASK_QUEUED) { queued++; }
    }
    app->al_recheck_from = n != 0 ? (start + done) % n : 0;
    free(hosts);
    if (queued == 0) { app->al_checked_at = now; }
    autolist_pump(app, false);
}

static void on_autolist_recheck(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_app_t *app = ud;
    if (autolist_running(app) && !autolist_iface_refused(app)) { autolist_recheck_run(app); }
}

static void on_autolist_tick(firc_loop_t *loop, void *ud) {
    (void)loop;
    autolist_pump(ud, true);
}

static void autolist_recheck_arm(firc_app_t *app) {
    const firc_app_config_t *ac = &app->cfg->app;
    bool want = app->al_svc != NULL && ac->autolist.recheck && app->loop != NULL;
    uint64_t ms = want ? (uint64_t)(ac->autolist.recheck_interval / FIRC_DURATION_MS) : 0;
    if (want && app->al_recheck_timer != 0 && ms == app->al_recheck_ms) { return; }
    if (app->al_recheck_timer != 0) { (void)firc_loop_del_timer(app->loop, app->al_recheck_timer); }
    app->al_recheck_timer = 0;
    app->al_recheck_ms = 0;
    if (!want) { return; }
    if (firc_loop_add_timer(app->loop, ms, ms, on_autolist_recheck, app, &app->al_recheck_timer) != FIRC_OK) {
        app->al_recheck_timer = 0;
        FIRC_WARN("autolist: the check's timer could not be armed");
        return;
    }
    app->al_recheck_ms = ms;
}

static void autolist_tick_arm(firc_app_t *app) {
    if (app->al_tick_timer != 0 || app->loop == NULL) { return; }
    uint64_t ms = (uint64_t)FIRC_AUTOLIST_PROBES_TICK_SECS * 1000u;
    if (firc_loop_add_timer(app->loop, ms, ms, on_autolist_tick, app, &app->al_tick_timer) != FIRC_OK) {
        app->al_tick_timer = 0;
        FIRC_WARN("autolist: the probe queue's timer could not be armed");
    }
}

firc_err_t firc_app_autolist_recheck_now(firc_app_t *app) {
    if (app == NULL) { return FIRC_ERR_INVAL; }
    if (!autolist_running(app)) { return FIRC_ERR_STATE; }
    if (autolist_iface_refused(app)) { return FIRC_ERR_CLOSED; }
    autolist_recheck_run(app);
    return FIRC_OK;
}

firc_err_t firc_app_autolist_probe(firc_app_t *app, const char *host, firc_al_ask_t *ask) {
    if (app == NULL) { return FIRC_ERR_INVAL; }
    if (!autolist_running(app)) { return FIRC_ERR_STATE; }
    if (autolist_iface_refused(app)) { return FIRC_ERR_CLOSED; }
    if (host == NULL || !firc_autolist_exclude_ok(host)) { return FIRC_ERR_INVAL; }
    int64_t now = autolist_now(app);
    firc_al_ask_t got = firc_al_probes_request(app->al_probes, host, now);
    if (ask != NULL) { *ask = got; }
    if (got == FIRC_AL_ASK_FULL) {
        uint32_t used = 0;
        firc_al_probes_counts(app->al_probes, now, &used, NULL);
        return used < FIRC_AUTOLIST_PROBES_PER_HOUR ? FIRC_ERR_LIMIT : FIRC_OK;
    }
    if (got != FIRC_AL_ASK_QUEUED) { return FIRC_OK; }
    char domain[FIRC_AUTOLIST_NAME_MAX];
    firc_al_cand_t *c = firc_regdomain(host, domain, sizeof(domain)) ? firc_al_cands_find(app->al_cands, domain) : NULL;
    if (c != NULL && c->state == FIRC_AL_CAND) { c->state = FIRC_AL_CHECKING; }
    autolist_pump(app, false);
    return FIRC_OK;
}

firc_err_t firc_app_autolist_candidate_add(firc_app_t *app, const char *domain) {
    if (app == NULL || domain == NULL) { return FIRC_ERR_INVAL; }
    if (!autolist_running(app)) { return FIRC_ERR_STATE; }
    firc_al_cand_t *c = firc_al_cands_find(app->al_cands, domain);
    if (c == NULL) { return FIRC_ERR_NOENT; }
    char host[FIRC_AUTOLIST_CAND_NAME];
    snprintf(host, sizeof(host), "%s", c->host);
    firc_err_t err = autolist_add_rule(app, domain, host, (firc_al_why_t)c->why);
    if (err != FIRC_OK) { return err; }
    c = firc_al_cands_find(app->al_cands, domain);
    if (c != NULL) { c->state = FIRC_AL_ADDED; }
    return FIRC_OK;
}

firc_err_t firc_app_autolist_address_dismiss(firc_app_t *app, const firc_ip_t *server, uint8_t proto, uint16_t port) {
    if (app == NULL || server == NULL) { return FIRC_ERR_INVAL; }
    if (!autolist_running(app)) { return FIRC_ERR_STATE; }
    return firc_autolist_svc_addr_dismiss(app->al_svc, server, proto, port) ? FIRC_OK : FIRC_ERR_NOENT;
}

firc_err_t firc_app_autolist_candidate_dismiss(firc_app_t *app, const char *domain) {
    if (app == NULL || domain == NULL) { return FIRC_ERR_INVAL; }
    return firc_al_cands_dismiss(app->al_cands, domain, autolist_now(app)) ? FIRC_OK : FIRC_ERR_NOENT;
}

void firc_app_autolist_tick_for_test(firc_app_t *app) {
    if (app != NULL) { autolist_pump(app, true); }
}

void firc_app_set_autolist_probe_for_test(firc_app_t *app, firc_autolist_probe_fn probe) {
    if (app != NULL) { app->al_probe = probe; }
}

void firc_app_autolist_hint_for_test(firc_app_t *app, const firc_ip_t *client, const char *name,
                                     const firc_ip_t *reals, size_t n_reals) {
    if (app != NULL && app->al_svc != NULL) { autolist_hint(app, client, name, reals, n_reals); }
}

static void autolist_stop(firc_app_t *app) {
    if (app->loop != NULL) {
        if (app->al_recheck_timer != 0) { (void)firc_loop_del_timer(app->loop, app->al_recheck_timer); }
        if (app->al_tick_timer != 0) { (void)firc_loop_del_timer(app->loop, app->al_tick_timer); }
    }
    app->al_recheck_timer = 0;
    app->al_recheck_ms = 0;
    app->al_tick_timer = 0;
    autolist_check_stop(app);
    app->al_recheck_from = 0;
    app->al_wan_warning = false;
    firc_al_cands_free(app->al_cands);
    app->al_cands = NULL;
    firc_al_probes_free(app->al_probes);
    app->al_probes = NULL;
    if (app->al_svc == NULL) { return; }
    app_nf_enter(app);
    app->al_on = false;
    autolist_lan_free(app->al_lan, app->n_al_lan);
    app->al_lan = NULL;
    app->n_al_lan = 0;
    app_nf_leave(app);
    if (app->pipeline != NULL) { firc_dns_pipeline_set_unmatched_fn(app->pipeline, NULL, NULL); }
    firc_autolist_svc_stop(app->al_svc);
    app->al_svc = NULL;
    if (app->committer != NULL) { firc_nfcommit_request(app->committer); }
    FIRC_INFO("autolist stopped");
}

static void autolist_iface_warn(const firc_app_t *app) {
    if (autolist_iface_refused(app)) {
        FIRC_WARN("autolist: no probe runs: the check interface %s belongs to the group it adds to",
                  app->cfg->app.autolist.check_iface);
    }
}

static void autolist_params(const firc_app_config_t *ac, firc_autolist_params_t *p) {
    p->fail_threshold = ac->autolist.fail_threshold;
    p->fail_secs = (uint32_t)(ac->autolist.fail_time / FIRC_DURATION_SEC);
    p->retrans_threshold = ac->autolist.retrans_threshold;
}

firc_err_t firc_app_autolist_apply(firc_app_t *app) {
    if (app == NULL) { return FIRC_ERR_INVAL; }
    const firc_app_config_t *ac = &app->cfg->app;
    if (!ac->autolist.enabled) {
        app->al_why_off = NULL;
        autolist_stop(app);
        return FIRC_OK;
    }
    firc_autolist_params_t params;
    autolist_params(ac, &params);
    if (app->al_svc != NULL) {
        firc_autolist_svc_set_params(app->al_svc, &params);
        autolist_recheck_arm(app);
        autolist_iface_warn(app);
        return FIRC_OK;
    }

    app->al_why_off = "no LAN interface: `link` in the settings is empty or names something that is not one";
    if (ac->n_link == 0) { return FIRC_ERR_INVAL; }
    for (size_t i = 0; i < ac->n_link; i++) {
        if (!firc_is_interface_name(ac->link[i])) { return FIRC_ERR_INVAL; }
    }
    app->al_why_off = "no address pool: the daemon is not routing yet";
    if (app->pool == NULL) { return FIRC_ERR_STATE; }

    app->al_why_off = "out of memory";
    firc_al_cands_t *cands = firc_al_cands_new();
    firc_al_probes_t *probes = firc_al_probes_new();
    char **lan = calloc(ac->n_link, sizeof(*lan));
    if (cands == NULL || probes == NULL || lan == NULL) {
        firc_al_cands_free(cands);
        firc_al_probes_free(probes);
        free(lan);
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < ac->n_link; i++) {
        lan[i] = strdup(ac->link[i]);
        if (lan[i] == NULL) {
            autolist_lan_free(lan, i);
            firc_al_cands_free(cands);
            firc_al_probes_free(probes);
            return FIRC_ERR_NOMEM;
        }
    }
    size_t n_lan = ac->n_link;

    firc_autolist_deps_t deps = {
        .loop = app->loop,
        .nflog_open = app->tap_nflog_open,
        .now = app->wall_s,
        .eligible = autolist_eligible,
        .listed = autolist_listed,
        .bypassed = autolist_bypassed,
        .nominated = autolist_nominated,
        .flow = autolist_flow,
        .ud = app,
    };
    deps.n_pools = autolist_pools(app, deps.pools);
    firc_err_t err;
    firc_autolist_svc_t *svc = firc_autolist_svc_start(&deps, &err);
    if (svc == NULL) {
        autolist_lan_free(lan, n_lan);
        firc_al_cands_free(cands);
        firc_al_probes_free(probes);
        app->al_why_off = err == FIRC_ERR_SYS ? "its NFLOG readers would not start: another tool may hold groups "
                                                "6-12, or the kernel has no nfnetlink_log; the daemon's log says which"
                                              : "out of memory";
        FIRC_WARN("autolist not started: %s", app->al_why_off);
        return err;
    }
    firc_autolist_svc_set_params(svc, &params);

    app_nf_enter(app);
    const char *prefix = ac->netfilter.iptables.chain_prefix;
    if ((app->ipt4 != NULL || app->ipt6 != NULL) && geteuid() == 0) { (void)firc_tap_load_modules(NULL); }
    for (unsigned fam = 0; fam < 2; fam++) {
        firc_ipt_t *engine = fam == 0 ? app->ipt4 : app->ipt6;
        if (engine == NULL) { continue; }
        firc_err_t probed = firc_ipt_probe_capture(engine);
        firc_err_t asked = probed;
        if (asked == FIRC_OK) {
            asked = firc_autolist_rules_supported(engine, prefix, app->pool, (const char *const *)lan, n_lan);
        }
        if (asked == FIRC_ERR_NOSYS || probed != FIRC_OK) {
            app_nf_leave(app);
            firc_autolist_svc_stop(svc);
            autolist_lan_free(lan, n_lan);
            firc_al_cands_free(cands);
            firc_al_probes_free(probes);
            app->al_why_off = asked == FIRC_ERR_NOSYS
                                  ? "this kernel will not take its rules: they need " FIRC_TAP_LAN_MODULES
                                  : "the kernel could not be asked which extensions it has; the daemon's log names "
                                    "the error";
            FIRC_WARN("autolist not started: %s", app->al_why_off);
            return asked == FIRC_ERR_NOSYS ? FIRC_ERR_NOSYS : FIRC_ERR_AGAIN;
        }
        if (asked != FIRC_OK) {
            FIRC_WARN("could not ask whether this kernel takes the autolist rules (%s): starting it anyway",
                      firc_err_str(asked));
        }
    }
    app->al_lan = lan;
    app->n_al_lan = n_lan;
    app->al_on = true;
    app_nf_leave(app);

    app->al_svc = svc;
    app->al_cands = cands;
    app->al_probes = probes;
    app->al_why_off = NULL;
    if (app->pipeline != NULL) { firc_dns_pipeline_set_unmatched_fn(app->pipeline, autolist_hint, app); }
    if (app->committer != NULL) { firc_nfcommit_request(app->committer); }
    autolist_recheck_arm(app);
    autolist_tick_arm(app);
    autolist_iface_warn(app);
    const char *off = autolist_target_off(app);
    if (off != NULL) {
        FIRC_WARN("autolist started, but adds nothing: %s", off);
    } else {
        const firc_group_t *g = firc_ruleset_group(autolist_target_rs(app));
        FIRC_INFO("autolist started: blocked hosts go to group \"%s\"", g->name != NULL ? g->name : "");
    }
    return FIRC_OK;
}

static bool autolist_admits_all(const firc_app_t *app, const firc_group_snapshot_t *g, const firc_autolist_addr_t *a) {
    if (g->block >= 0) { return false; }
    for (uint8_t i = 0; g->devices != NULL && i < a->n_clients; i++) {
        if (!firc_devsel_allows(g->devices, &a->clients[i], app->tap_policy, app->tap_device, app->tap_resolver_ud)) {
            return false;
        }
    }
    return true;
}

static size_t autolist_covering(const firc_app_t *app, const firc_autolist_addr_t *a, firc_id_t *out) {
    const firc_ruleset_snapshot_t *snap = app->pipeline != NULL ? firc_dns_pipeline_snapshot(app->pipeline) : NULL;
    const char *gid = app->cfg->app.autolist.group;
    firc_id_t target;
    bool has_target = gid != NULL && firc_id_parse(gid, &target) == FIRC_OK;
    size_t n = 0;
    for (int pass = 0; pass < 2; pass++) {
        for (size_t i = 0; snap != NULL && i < snap->n_groups && n < FIRC_AUTOLIST_GROUPS_MAX; i++) {
            const firc_group_snapshot_t *g = snap->groups[i];
            bool is_target = has_target && firc_id_equal(g->id, target);
            if (is_target != (pass == 0) || !autolist_admits_all(app, g, a)) { continue; }
            out[n++] = g->id;
        }
    }
    return n;
}

void firc_app_autolist_status(const firc_app_t *app, firc_autolist_status_t *out) {
    if (out == NULL) { return; }
    memset(out, 0, sizeof(*out));
    if (app == NULL) { return; }
    const firc_app_config_t *ac = &app->cfg->app;
    out->enabled = ac->autolist.enabled;
    out->running = autolist_running(app);
    out->why_off = app->al_svc != NULL ? autolist_target_off(app) : app->al_why_off;
    out->mode = ac->autolist.mode != NULL ? ac->autolist.mode : "candidates";
    out->group = ac->autolist.group != NULL && ac->autolist.group[0] != '\0' ? ac->autolist.group : NULL;
    firc_autolist_svc_counts(app->al_svc, &out->failures, &out->listed);
    int64_t now = autolist_now(app);
    out->failures_day = firc_autolist_svc_failures_day(app->al_svc);
    out->ambiguous_day = firc_autolist_svc_ambiguous_day(app->al_svc);
    out->wan_warning = app->al_wan_warning ? "the check interface has no route: the WAN check cannot pass" : NULL;
    out->added_hour = autolist_added_hour(app, now);
    out->checking = firc_al_probes_rechecking(app->al_probes) || (app->al_check != NULL && app->al_check_recheck);
    out->checked_at = app->al_checked_at;
    out->switched_off = app->al_switched_off;
    out->switched_on = app->al_switched_on;
    out->n_addresses = firc_autolist_svc_addresses(app->al_svc, out->addresses, 2 * FIRC_AUTOLIST_ADDRS);
    int64_t now_ms = (int64_t)app->mono_ms();
    for (size_t i = 0; i < out->n_addresses; i++) {
        out->ptr_state[i] = app->al_ptr != NULL ? firc_ptr_peek(app->al_ptr, &out->addresses[i].server, now_ms,
                                                                out->ptr[i], sizeof(out->ptr[i]))
                                                : FIRC_PTR_NONE;
        out->n_groups[i] = autolist_covering(app, &out->addresses[i], out->groups[i]);
    }
    const firc_al_cand_t *cands[FIRC_AUTOLIST_CANDIDATES];
    out->n_candidates = firc_al_cands_list(app->al_cands, cands, FIRC_AUTOLIST_CANDIDATES);
    for (size_t i = 0; i < out->n_candidates; i++) { out->candidates[i] = *cands[i]; }
    firc_al_probes_counts(app->al_probes, now, &out->probes_used_hour, &out->probes_queued);
    out->probe_running = app->al_check != NULL;
    if (out->probe_running) { snprintf(out->probe_host, sizeof(out->probe_host), "%s", app->al_check_host); }
    out->n_results = firc_al_probes_results(app->al_probes, now, out->results, FIRC_AUTOLIST_PROBE_CACHE);
}

void firc_app_set_device_lookup(firc_app_t *app, firc_devsel_mark_fn mark, firc_devsel_hosts_fn hosts,
                                firc_devsel_policy_hosts_fn policy_hosts, firc_devsel_policy_nets_fn policy_nets,
                                void *ud) {
    if (app == NULL) { return; }
    app->lookup.policy_mark = mark;
    app->lookup.host_addrs = hosts;
    app->lookup.policy_hosts = policy_hosts;
    app->lookup.policy_nets = policy_nets;
    app->lookup.ud = ud;
}

void firc_app_set_dns_down(firc_app_t *app, bool down) {
    app_nf_enter(app);
    bool up = app->dns_down && !down;
    app->dns_down = down;
    if (up) {
        firc_port_remap_arm(app->port_remap);
        if (app->committer != NULL) { firc_nfcommit_request(app->committer); }
    }
    app_nf_leave(app);
}

bool firc_app_dns_down_for_test(const firc_app_t *app) { return app->dns_down; }

void firc_app_set_tunnel_down(firc_app_t *app, firc_app_tunnel_down_fn down, void *ud) {
    if (app == NULL) { return; }
    app->tunnel_down = down;
    app->tunnel_down_ud = ud;
}

void firc_app_set_ifaces(firc_app_t *app, const firc_app_ifaces_t *h) {
    if (app == NULL) { return; }
    if (h == NULL) {
        memset(&app->ifaces, 0, sizeof(app->ifaces));
        return;
    }
    app->ifaces = *h;
}

firc_err_t firc_app_set_dnsstats(firc_app_t *app, firc_app_dnsstats_fn fn, void *ud) {
    if (app == NULL) { return FIRC_OK; }
    app->dnsstats = fn;
    app->dnsstats_ud = ud;
    if (fn == NULL) { return FIRC_OK; }
    firc_err_t err = fn(ud, !app->cfg->app.dns_proxy.disable_stats);
    if (err != FIRC_OK) { app->cfg->app.dns_proxy.disable_stats = true; }
    return err;
}

bool firc_app_iface_view(const firc_app_t *app, const char *name, firc_if_view_t *out) {
    return app != NULL && app->ifaces.view != NULL && app->ifaces.view(app->ifaces.ud, name, out);
}

firc_err_t firc_app_move_up(firc_app_t *app, firc_id_t id) {
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    if (rs == NULL) { return FIRC_ERR_NOENT; }
    if (firc_group_n_ifaces(firc_ruleset_group(rs)) < 2) { return FIRC_ERR_INVAL; }
    if (app->ifaces.move_up == NULL) { return FIRC_ERR_NOSYS; }
    if (!firc_ruleset_routed(rs)) { return FIRC_ERR_AGAIN; }
    firc_err_t err = app->ifaces.move_up(app->ifaces.ud, id);
    return err == FIRC_ERR_NOENT ? FIRC_ERR_NOSYS : err;
}

static void drop_teardown_flushes(firc_app_t *app, uint32_t value, firc_id_t owner) {
    size_t kept = 0;
    for (size_t i = 0; i < app->n_flushes; i++) {
        nf_flush_t e = app->flushes[i];
        if (e.value == value && firc_id_equal(e.owner, owner) && !e.narrowed && !e.sweep) { continue; }
        app->flushes[kept++] = e;
    }
    app->n_flushes = kept;
}

static bool ktcp_redirected(const firc_app_t *app, int dev) {
    return dev >= 0 && dev < FIRC_KTCP_DEVICES && app->ktcp_ports[dev] != 0;
}

static bool queued_redirected(const firc_app_t *app, uint32_t value, firc_id_t owner) {
    for (size_t i = 0; i < app->n_flushes; i++) {
        const nf_flush_t *e = &app->flushes[i];
        if (e->value == value && firc_id_equal(e->owner, owner) && ktcp_redirected(app, e->dev)) { return true; }
    }
    return false;
}

static firc_err_t move_group_locked(firc_app_t *app, firc_id_t id, size_t to, bool blocked) {
    firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    if (rs == NULL || !firc_ruleset_routed(rs)) { return FIRC_ERR_NOENT; }
    const firc_group_t *g = firc_ruleset_group(rs);
    if (to >= firc_group_n_ifaces(g)) { return FIRC_ERR_INVAL; }
    bool was_blocked = false;
    size_t from = firc_ruleset_active(rs, &was_blocked);
    bool written = blocked || firc_iface_is_sink(firc_group_iface_at(g, to)) ||
                   firc_ruleset_has_iface_route(rs, AF_INET);
    if (from == to && was_blocked == blocked && written) { return FIRC_OK; }
    bool redirected = from != to && (ktcp_redirected(app, group_tunnel(app, rs)) ||
                                     ktcp_redirected(app, tun_dev(app, firc_group_iface_at(g, to))) ||
                                     queued_redirected(app, firc_ruleset_mark_field(rs), g->id));
    firc_nf_prefixes_t close = {NULL, 0};
    if (redirected) { close_capture(app, rs, &close); }
    char was[FIRC_IFACE_NAME_CAP];
    snprintf(was, sizeof(was), "%s", firc_ruleset_active_iface(rs));
    firc_err_t err = firc_ruleset_set_active(rs, to, blocked);
    if (from != to) {
        char who[FIRC_ID_STR_LEN];
        firc_id_format(g->id, who);
        uint32_t value = firc_ruleset_mark_field(rs);
        if (redirected && app->committer != NULL) {
            flush_queue_add(app, value, g, was, FLUSH_TEARDOWN, &close);
            app_request_more_locked(app);
        } else if (redirected) {
            withdraw(app, was, tun_dev(app, was), value, g->id, who, WD_FLUSH, false, false, &close, NULL);
        } else {
            firc_ipset_to_link_flush_mark(app->ct, value, who);
            drop_teardown_flushes(app, value, g->id);
        }
    }
    firc_nf_prefixes_clear(&close);
    if (from != to || was_blocked != blocked) {
        if (app->proxy != NULL) { firc_dnsproxy_close_group_pools(app->proxy, g->id); }
        firc_app_republish_resolve_routes(app);
    }
    return err;
}

firc_err_t firc_app_move_group(firc_app_t *app, firc_id_t id, size_t to, bool blocked) {
    app_nf_enter(app);
    firc_err_t err = move_group_locked(app, id, to, blocked);
    app_nf_leave(app);
    return err;
}

void firc_app_set_policies_read(firc_app_t *app, bool (*read)(void *ud)) {
    if (app != NULL) { app->lookup.policies_read = read; }
}

void firc_app_devices_changed(firc_app_t *app) {
    if (app == NULL || !app->running) { return; }
    /* compared before taking nf_mu, so a table that moved no chain costs the committer nothing */
    bool any = false;
    for (size_t i = 0; i < app->n_rulesets && !any; i++) {
        bool moved = false;
        any = firc_ruleset_devices_would_move(app->rulesets[i], &moved) != FIRC_OK || moved;
    }
    if (!any) { return; }
    app_nf_enter(app);
    bool moved = false;
    for (size_t i = 0; i < app->n_rulesets; i++) {
        firc_ruleset_t *rs = app->rulesets[i];
        bool changed = false;
        firc_err_t err = firc_ruleset_refresh_devices(rs, &changed);
        if (err != FIRC_OK) {
            const char *name = firc_ruleset_group(rs)->name;
            FIRC_WARN("group \"%s\": its devices chain could not follow the firmware's host table (%s); it keeps "
                      "the one it had", name != NULL ? name : "", firc_err_str(err));
            continue;
        }
        if (!changed) { continue; }
        moved = true;
        /* a device no longer matched by a narrowed chain keeps its flows unless flushed */
        if (firc_ruleset_devices_narrowed(rs)) { note_narrowed(app, rs); }
    }
    if (moved && app->committer != NULL) { app_request_more_locked(app); }
    app_nf_leave(app);
}

/* any missing LAN interface is "one went away": iptables accepts a rule naming one that does not exist */
static void tap_note_iface(firc_app_t *app) {
    if (app->tap_report == NULL || app->n_tap_lan == 0) { return; }
    bool gone = false;
    for (size_t i = 0; i < app->n_tap_lan && !gone; i++) {
        gone = app->tap_iface_index(app->tap_lan[i]) == 0;
    }
    if (gone) {
        firc_tap_report_saw_iface_gone(app->tap_report);
    } else {
        firc_tap_report_saw_iface_there(app->tap_report);
    }
}

/* folds what the committer counted since last asked; called by the stop and the start, so nothing is owed twice */
static void tap_fold_failed_passes(firc_app_t *app) {
    uint32_t failed = atomic_load_explicit(&app->tap_failed_passes, memory_order_relaxed);
    for (; app->tap_failed_passes_seen < failed; app->tap_failed_passes_seen++) {
        firc_tap_report_saw_rules_gone(app->tap_report);
    }
}

static int64_t mono_secs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec;
}

firc_err_t firc_app_capture_start(firc_app_t *app, int64_t now_unix,
                                  char token_out[FIRC_TAP_TOKEN_CHARS + 1]) {
    if (app == NULL) { return FIRC_ERR_INVAL; }
    /* checked first: a running capture is the answer regardless of a cleared link/pool since it started */
    if (app->tap_open) { return FIRC_ERR_EXIST; }
    /* syntax only: the name goes into an -i match */
    const firc_app_config_t *ac = &app->cfg->app;
    if (ac->n_link == 0) { return FIRC_ERR_INVAL; }
    for (size_t i = 0; i < ac->n_link; i++) {
        if (!firc_is_interface_name(ac->link[i])) { return FIRC_ERR_INVAL; }
    }
    if (app->pool == NULL) { return FIRC_ERR_STATE; }

    /* generated before the lock: it reads /dev/urandom, and nf_mu is not a thing to hold across a file */
    char token[FIRC_TAP_TOKEN_CHARS + 1];
    {
        uint8_t raw[FIRC_TAP_TOKEN_CHARS / 2];
        if (firc_random_bytes(raw, sizeof(raw)) != FIRC_OK) { /* FIRC_ERR_IO, deliberately distinct from the NFLOG bind's FIRC_ERR_SYS */
            FIRC_WARN("capture refused: no random bytes for its token");
            return FIRC_ERR_IO;
        }
        for (size_t i = 0; i < sizeof(raw); i++) { snprintf(token + i * 2, 3, "%02x", raw[i]); }
    }

    /* readers come up outside the mutex below: binding an NFLOG group is a kernel round trip with a two-second ceiling */
    char **lan = calloc(ac->n_link, sizeof(*lan));
    if (lan == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < ac->n_link; i++) {
        lan[i] = strdup(ac->link[i]);
        if (lan[i] == NULL) {
            tap_lan_free(lan, i);
            return FIRC_ERR_NOMEM;
        }
    }
    size_t n_lan = ac->n_link;

    /* readers first, rules only if they came up: rules with nothing bound to their groups are packets the kernel drops */
    firc_err_t err = tap_reader_up(app);
    if (err != FIRC_OK) {
        tap_lan_free(lan, n_lan);
        FIRC_WARN("capture refused: its readers could not be started (%s)", firc_err_str(err));
        return err;
    }
    app->tap_failed_passes_seen = /* the last capture's failed passes are its own; it has stopped */
        atomic_load_explicit(&app->tap_failed_passes, memory_order_relaxed);

    app_nf_enter(app); /* driving an iptables engine the committer may be mid-pass on is the race this mutex exists for */
    if (app->tap_open) {
        app_nf_leave(app);
        tap_reader_down(app);
        tap_lan_free(lan, n_lan);
        return FIRC_ERR_EXIST;
    }

    /* probed under the lock: the probe writes mangle, which the committer writes under this same mutex */
    const char *prefix = ac->netfilter.iptables.chain_prefix;
    /* firmware ships xt_NFLOG/xt_string but may not load them after a reboot; root-only so tests avoid the host's modules */
    if ((app->ipt4 != NULL || app->ipt6 != NULL) && geteuid() == 0) {
        (void)firc_tap_load_modules(NULL);
    }
    for (unsigned fam = 0; fam < 2; fam++) {
        firc_ipt_t *engine = fam == 0 ? app->ipt4 : app->ipt6;
        if (engine == NULL) { continue; }
        firc_err_t probed = firc_ipt_probe_capture(engine);
        firc_err_t asked = probed;
        if (asked == FIRC_OK) {
            asked = firc_tap_rules_supported(engine, prefix, app->pool, (const char *const *)lan, n_lan);
        }
        if (asked == FIRC_ERR_NOSYS || probed != FIRC_OK) {
            app_nf_leave(app);
            tap_reader_down(app);
            tap_lan_free(lan, n_lan);
            if (asked == FIRC_ERR_NOSYS) {
                FIRC_WARN("capture refused: this kernel will not take the capture rules. They need "
                           FIRC_TAP_LAN_MODULES "; the log above names what it lacks");
                return FIRC_ERR_NOSYS;
            }
            FIRC_WARN("capture refused: the kernel could not be asked which capture extensions it has (%s); "
                       "the log above names the errno",
                       firc_err_str(probed));
            return FIRC_ERR_AGAIN;
        }
        if (asked != FIRC_OK) {
            FIRC_WARN("could not ask whether this kernel takes the capture rules (%s): starting "
                      "the capture anyway",
                      firc_err_str(asked));
        }
    }

    memcpy(app->tap_token, token, sizeof(token));
    app->tap_lan = lan;
    app->n_tap_lan = n_lan;
    app->tap_ends_at = now_unix + app->tap_seconds;
    app->tap_ends_mono = mono_secs() + app->tap_seconds;
    app->tap_started_ms = (int64_t)app->mono_ms();
    app->tap_open = true;
    app_nf_leave(app);

    /* one-shot deadline; without a loop (tests, the pre-loop window) a capture has no deadline and runs until stopped */
    if (app->loop != NULL) {
        uint64_t ms = (uint64_t)app->tap_seconds * 1000u;
        if (firc_loop_add_timer(app->loop, ms, 0, on_tap_deadline, app, &app->tap_timer) !=
            FIRC_OK) {
            /* reachable only when out of file descriptors, untested; a quiet teardown since the start was never said */
            app->tap_timer = 0;
            app_nf_enter(app);
            app->tap_open = false;
            memset(app->tap_token, 0, sizeof(app->tap_token));
            app->tap_lan = NULL;
            app->n_tap_lan = 0;
            app_nf_leave(app);
            tap_reader_down(app);
            tap_lan_free(lan, n_lan);
            FIRC_WARN("capture refused: its deadline could not be armed");
            return FIRC_ERR_NOMEM;
        }
    }

    app->tap_has_last = false;

    /* a recall with no real address yet recognises nothing by address; noted now, said at the capture's end */
    firc_recall_t *recall = app->pipeline != NULL ? firc_dns_pipeline_recall(app->pipeline) : NULL;
    if (firc_recall_real_count(recall) == 0) { firc_tap_report_saw_cold_recall(app->tap_report); }
    tap_note_iface(app);

    /* said after it is actually running, so the log cannot say the capability is on when it is not */
    char names[FIRC_EVENT_TEXT];
    size_t at = 0;
    names[0] = '\0';
    for (size_t i = 0; i < n_lan && at < sizeof(names); i++) {
        int n = snprintf(names + at, sizeof(names) - at, "%s%s", i ? ", " : "", lan[i]);
        if (n < 0) { break; }
        at += (size_t)n;
    }
    FIRC_INFO("capture started: %llds on the LAN side, on %s", (long long)app->tap_seconds,
                names);
    if (app->committer != NULL) { firc_nfcommit_request(app->committer); }
    if (token_out != NULL) { memcpy(token_out, token, sizeof(token)); }
    return FIRC_OK;
}

static void on_tap_deadline(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_app_t *app = ud;
    app->tap_timer = 0; /* the timer has fired and is dead; saying so first keeps the stop from deleting an id that names nothing */
    FIRC_INFO("capture reached its end");
    (void)capture_end(app, NULL, "reached-end");
}

firc_err_t firc_app_capture_stop(firc_app_t *app, const char *token) {
    return capture_end(app, token, "stopped");
}

bool firc_app_capture_last(const firc_app_t *app, firc_capture_last_t *out) {
    if (app == NULL || out == NULL || !app->tap_has_last) { return false; }
    *out = app->tap_last;
    return true;
}

static firc_err_t capture_end(firc_app_t *app, const char *token, const char *how) {
    if (app == NULL) { return FIRC_ERR_INVAL; }

    app_nf_enter(app);
    if (!app->tap_open) {
        app_nf_leave(app);
        return FIRC_OK;
    }
    if (token != NULL && strcmp(token, app->tap_token) != 0) { /* decided under the same lock that decides whether a capture exists at all */
        app_nf_leave(app);
        return FIRC_ERR_NOENT;
    }
    tap_note_iface(app); /* the last reading taken while still running; after this the summary is what it is */
    tap_fold_failed_passes(app);
    app->tap_open = false;
    memset(app->tap_token, 0, sizeof(app->tap_token));
    tap_lan_free(app->tap_lan, app->n_tap_lan);
    app->tap_lan = NULL;
    app->n_tap_lan = 0;
    int timer = app->tap_timer; /* noted here, cancelled below the unlock; safe since every caller is the loop thread */
    app->tap_timer = 0;
    app_nf_leave(app);

    if (timer != 0 && app->loop != NULL) { (void)firc_loop_del_timer(app->loop, timer); }

    FIRC_INFO("capture stopped");
    memset(&app->tap_last, 0, sizeof(app->tap_last));
    app->tap_last.started_at = app->tap_ends_at - app->tap_seconds;
    int64_t ran = strcmp(how, "reached-end") == 0
                      ? app->tap_seconds
                      : ((int64_t)app->mono_ms() - app->tap_started_ms) / 1000;
    app->tap_last.ended_at = app->tap_last.started_at + (ran < 0 ? 0 : ran > app->tap_seconds ? app->tap_seconds : ran);
    app->tap_last.ended = how;
    app->tap_last.leaks = firc_tap_report_printed(app->tap_report);
    firc_tap_report_sentences(app->tap_report, true, keep_sentence, &app->tap_last);
    app->tap_has_last = true;
    tap_reader_down(app);
    if (app->committer != NULL) { firc_nfcommit_request(app->committer); }
    return FIRC_OK;
}

void firc_app_capture_status(const firc_app_t *app, firc_capture_status_t *out) {
    if (out == NULL) { return; }
    memset(out, 0, sizeof(*out));
    if (app == NULL) { return; }
    /* the mutex directly, not app_nf_enter, which would wrongly interrupt a running commit for a status read */
    firc_app_t *mut = (firc_app_t *)app;
    if (mut->nf_mu_ready) { pthread_mutex_lock(&mut->nf_mu); }
    out->running = app->tap_open;
    if (out->running) {
        out->ends_at = app->tap_ends_at;
        int64_t left = app->tap_ends_mono - mono_secs();
        out->seconds_left = left > 0 ? left : 0;
    }
    if (mut->nf_mu_ready) { pthread_mutex_unlock(&mut->nf_mu); }
}

firc_err_t firc_app_stage_pool_reject(firc_app_t *app) {
    for (unsigned fam = 0; fam < 2; fam++) {
        firc_err_t err = firc_app_stage_pool_reject_one(app, fam);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

firc_err_t firc_app_rebuild_netfilter(firc_app_t *app, firc_cancel_t *cancel) {
    return firc_app_rebuild_netfilter_kind(app, cancel, true);
}

static firc_err_t firc_app_rebuild_netfilter_locked(firc_app_t *app, firc_cancel_t *cancel,
                                                    bool full);

firc_err_t firc_app_rebuild_netfilter_kind(firc_app_t *app, firc_cancel_t *cancel, bool full) {
    firc_err_t err = firc_app_rebuild_netfilter_locked(app, cancel, full);
    /* a cancelled/raced pass leaves no unwritten window; anything else does, so a capture's rules may be missing */
    if (err != FIRC_OK && err != FIRC_ERR_CANCELED && err != FIRC_ERR_AGAIN && app->tap_open) {
        atomic_fetch_add_explicit(&app->tap_failed_passes, 1u, memory_order_relaxed);
    }
    return err;
}

static firc_err_t firc_app_rebuild_netfilter_locked(firc_app_t *app, firc_cancel_t *cancel,
                                                    bool full) {
    set_job_t *jobs = NULL;
    size_t n_jobs = 0;
    if (app->nf_mu_ready) { pthread_mutex_lock(&app->nf_mu); }
    bool was_due = false;
    firc_err_t err = sets_collect(app, &jobs, &n_jobs, &was_due);
    if (app->nf_mu_ready) { pthread_mutex_unlock(&app->nf_mu); }
    bool sets_whole = sets_write(app, jobs, n_jobs, cancel);
    if (app->nf_mu_ready) { pthread_mutex_lock(&app->nf_mu); }
    sets_record(app, jobs, n_jobs, was_due);
    sets_mark_stale(app);
    set_jobs_free(jobs, n_jobs);
    if (err == FIRC_OK && (!sets_whole || firc_cancel_raised(cancel))) { err = FIRC_ERR_CANCELED; }
    uint64_t ticket = app->nf_change; /* every mutation made before this pass took the lock */

    /* attached for exactly this pass: leaving it on would also abort a synchronous API commit on the loop thread */
    if (app->ipt4) { firc_ipt_set_cancel(app->ipt4, cancel); }
    if (app->ipt6) { firc_ipt_set_cancel(app->ipt6, cancel); }

    char report4[512], report6[512];
    firc_ipt_take_report(app->ipt4, report4, sizeof(report4));
    firc_ipt_take_report(app->ipt6, report6, sizeof(report6));
    struct timespec pass_t0, pass_t1;
    clock_gettime(CLOCK_MONOTONIC, &pass_t0);
    uint64_t gen = pass_snapshot_begin(app);
    bool set_gone = false;
    if (err == FIRC_OK) { err = rebuild_leaving_out(app, cancel, full, &set_gone); }
    if (err == FIRC_OK) { sets_sweep(app); }
    if (set_gone) { sets_forget_gone(app); }
    bool sets_failed = sets_failing(app);
    pass_snapshot_end(app);
    clock_gettime(CLOCK_MONOTONIC, &pass_t1);
    firc_ipt_take_report(app->ipt4, report4, sizeof(report4));
    firc_ipt_take_report(app->ipt6, report6, sizeof(report6));
    FIRC_DEBUG("netfilter pass (%s): %lld ms (%s); ipv4: %s; ipv6: %s", full ? "full" : "incremental",
               (long long)((pass_t1.tv_sec - pass_t0.tv_sec) * 1000 + (pass_t1.tv_nsec - pass_t0.tv_nsec) / 1000000),
               firc_err_str(err), app->ipt4 ? report4 : "off", app->ipt6 ? report6 : "off");

    if (err != FIRC_OK) { /* discard, or the next commit on this engine would carry out what was only staged */
        firc_ipt_discard(app->ipt4);
        firc_ipt_discard(app->ipt6);
    }
    firc_ktcp_image_t image = app->pass_image;
    memset(&app->pass_image, 0, sizeof(app->pass_image));
    firc_xt_acl_t skip_list = app->pass_skip;
    firc_ktcp_off_t off = app->pass_off;
    if (err != FIRC_OK) { firc_ktcp_image_free(&image); }

    if (app->ipt4) { firc_ipt_set_cancel(app->ipt4, NULL); }
    if (app->ipt6) { firc_ipt_set_cancel(app->ipt6, NULL); }

    if (app->nf_mu_ready) { pthread_mutex_unlock(&app->nf_mu); }
    if (err == FIRC_OK) { report_committed(app, gen, full, ticket, &image, &skip_list, off, sets_failed); }
    return err;
}

static firc_err_t rebuild_netfilter_cb(void *ud, firc_cancel_t *cancel, bool full) {
    return firc_app_rebuild_netfilter_kind(ud, cancel, full);
}

static bool tables_moved_cb(void *ud) {
    firc_app_t *app = ud;
    pthread_mutex_lock(&app->nf_mu);
    bool moved = firc_ipt_tables_moved(app->ipt4) || firc_ipt_tables_moved(app->ipt6);
    pthread_mutex_unlock(&app->nf_mu);
    return moved;
}

/* only after a full pass: an incremental one leaves the kernel with our rules, so sweeping it would drop live flows */
/* ponytail: a whole-table dump on the DNS loop thread, cost unmeasured */
static void sweep_fail_open_flows(firc_app_t *app) {
    if (app == NULL || app->ct == NULL || app->pool == NULL) { return; }
    firc_ip_t b4, b6;
    uint8_t l4 = 0, l6 = 0;
    bool has4 = firc_fakeip_pool_prefix(app->pool, FIRC_FAM_V4, &b4, &l4);
    bool has6 = firc_fakeip_pool_prefix(app->pool, FIRC_FAM_V6, &b6, &l6);
    size_t dropped = 0;
    firc_err_t err = firc_ct_flush_pool_replies(app->ct, has4 ? b4.b : NULL, has4 ? l4 : 0,
                                                has6 ? b6.b : NULL, has6 ? l6 : 0, &dropped);
    if (dropped > 0) { /* the count regardless: a sweep that failed halfway has still deleted what it deleted */
        FIRC_INFO("cleared %zu flow(s) answered with a fake address while the pool was "
                  "unprotected",
                  dropped);
    }
    if (err != FIRC_OK) { /* a flow the sweep missed recovers on the client's next connection anyway, and the next full pass sweeps again */
        FIRC_WARN("could not clear the flows of the fail-open window: %s -- any left are swept "
                  "at the next full rebuild",
                  firc_err_str(err));
    }
}

/* reported on the loop thread: the pool, the proxy and the conntrack handle are all loop-thread objects */
typedef struct committed_report {
    firc_app_t *app;
    uint64_t gen;
    bool full;
    uint64_t ticket;
    firc_ktcp_image_t image;
    firc_xt_acl_t skip;
    firc_ktcp_off_t off;
    bool sets_failed;
} committed_report_t;

static void push_image(firc_app_t *app, firc_ktcp_image_t *image) {
    if (app->ktcp_sink.pushed != NULL) { app->ktcp_sink.pushed(&app->last_image, image, app->ktcp_sink.ud); }
    firc_ktcp_image_free(&app->last_image);
    app->last_image = *image;
    memset(image, 0, sizeof(*image));
}

static void take_skip(firc_app_t *app, const firc_xt_acl_t *skip, firc_ktcp_off_t off) {
    app->last_skip = *skip;
    bool moved = app->last_off != off;
    app->last_off = off;
    if (moved) { firc_app_republish_resolve_routes(app); }
}

static void drop_report(void *ud) {
    committed_report_t *r = ud;
    if (r == NULL) { return; }
    firc_ktcp_image_free(&r->image);
    free(r);
}

static void on_committed_posted(firc_loop_t *loop, void *ud) {
    (void)loop;
    committed_report_t *r = ud;
    r->app->owed_full_sweep = false; /* a report landed, so whatever was owed is paid; the next failure is a new one */
    /* map first, so a released answer finds its pair sent; answers before the sweep, which blocks on netlink */
    push_image(r->app, &r->image);
    take_skip(r->app, &r->skip, r->off);
    firc_app_pass_committed(r->app, r->gen);
    if (r->full) { sweep_fail_open_flows(r->app); }
    pay_flushes(r->app, r->ticket); /* the flushes this pass carried */
    waits_due(r->app);
    firc_app_retry_groups(r->app, NULL); /* a group whose enable failed is tried again when due */
    set_retry_arm(r->app, r->sets_failed);
    drop_report(r);
}

static void report_committed(firc_app_t *app, uint64_t gen, bool full, uint64_t ticket, firc_ktcp_image_t *image,
                             const firc_xt_acl_t *skip, firc_ktcp_off_t off, bool sets_failed) {
    if (app->loop == NULL) {
        /* no loop: the committer thread itself writes loop objects (pool, conntrack handle), all under nf_mu */
        if (app->nf_mu_ready) { pthread_mutex_lock(&app->nf_mu); }
        push_image(app, image);
        take_skip(app, skip, off);
        firc_app_pass_committed(app, gen);
        if (full) { sweep_fail_open_flows(app); }
        pay_flushes(app, ticket);
        waits_due(app);
        if (app->nf_mu_ready) { pthread_mutex_unlock(&app->nf_mu); }
        return;
    }
    committed_report_t *r = malloc(sizeof(*r));
    if (r != NULL) {
        r->app = app;
        r->gen = gen;
        r->full = full;
        r->ticket = ticket;
        r->image = *image;
        memset(image, 0, sizeof(*image));
        r->skip = *skip;
        r->off = off;
        r->sets_failed = sets_failed;
    }
    if (r == NULL ||
        firc_loop_post_with_drop(app->loop, on_committed_posted, r, drop_report) != FIRC_OK) {
        /* nothing would report or schedule another pass, leaving every answer waiting out its deadline */
        firc_ktcp_image_free(image);
        drop_report(r);
        if (full) {
            /* the sweep's report could not post; an incremental follow-up would not sweep, so a full one is asked, once */
            if (app->owed_full_sweep) {
                FIRC_WARN("could not report a completed rebuild again; the flows of its fail-open "
                          "window are left unswept rather than asking for a third pass that would "
                          "open another");
            } else {
                app->owed_full_sweep = true;
                FIRC_WARN("could not report a completed rebuild; asking for another so the flows "
                          "of its fail-open window are swept");
                firc_nfcommit_request(app->committer);
            }
        } else {
            firc_nfcommit_request_more(app->committer);
        }
    }
}

static bool answer_ready(const firc_dns_msg_t *msg, void *ud) {
    return firc_app_answer_ready(ud, msg);
}

bool firc_app_answer_ready(firc_app_t *app, const firc_dns_msg_t *msg) {
    for (size_t i = 0; i < msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        size_t len = rr->rtype == FIRC_DNS_TYPE_A ? 4 : rr->rtype == FIRC_DNS_TYPE_AAAA ? 16 : 0;
        if (len == 0 || rr->rdata_len != len) { continue; }
        firc_ip_t fake = {{0}, (uint8_t)len};
        memcpy(fake.b, rr->rdata, len);
        if (firc_fakeip_needs_commit(app->pool, &fake)) { return false; }
    }
    return true;
}

void firc_app_pass_committed(firc_app_t *app, uint64_t gen) {
    if (app == NULL) { return; }
    firc_fakeip_mark_committed(app->pool, gen);
    if (app->proxy) { firc_dnsproxy_release(app->proxy, answer_ready, app); }
}

void firc_app_pool_changed(firc_app_t *app) {
    if (app == NULL) { return; }
    firc_err_t err = firc_app_refresh_pool_snapshot(app);
    if (err != FIRC_OK) { /* the old snapshot stays current; the next change retries the refresh */
        firc_lograte_say(&app->said_snapshot, 60000, FIRC_LOG_WARN, NULL, "failed to snapshot the address pool: %s",
                         firc_err_str(err));
    }
    if (app->committer) {
        firc_nfcommit_request_addresses(app->committer);
    } else {
        err = firc_app_rebuild_netfilter_kind(app, NULL, false);
        if (err != FIRC_OK) { FIRC_WARN("netfilter rebuild failed: %s", firc_err_str(err)); }
    }
}

firc_nfcommit_t *firc_app_committer_for_test(firc_app_t *app) {
    return app != NULL ? app->committer : NULL;
}

firc_err_t firc_app_start_netfilter_committer(firc_app_t *app) {
    if (app->committer) { return FIRC_ERR_STATE; }

    pthread_mutexattr_t attr;
    if (pthread_mutexattr_init(&attr) != 0) { return FIRC_ERR_SYS; }
    bool mu_ok = pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_RECURSIVE) == 0 &&
                 pthread_mutex_init(&app->nf_mu, &attr) == 0;
    pthread_mutexattr_destroy(&attr);
    if (!mu_ok) { return FIRC_ERR_SYS; }

    app->committer = firc_nfcommit_new(rebuild_netfilter_cb, app);
    if (!app->committer) {
        pthread_mutex_destroy(&app->nf_mu);
        return FIRC_ERR_NOMEM;
    }

    firc_nfcommit_set_watch(app->committer, tables_moved_cb, FIRC_NFCOMMIT_WATCH_MS);

    app->nf_mu_ready = true; /* published before the thread starts, so the first webhook request already takes the lock */

    firc_err_t err = firc_nfcommit_start(app->committer);
    if (err != FIRC_OK) {
        app->nf_mu_ready = false;
        firc_nfcommit_free(app->committer);
        app->committer = NULL;
        pthread_mutex_destroy(&app->nf_mu);
        return err;
    }
    FIRC_DEBUG("netfilter table committer started");
    return FIRC_OK;
}

void firc_app_stop_netfilter_committer(firc_app_t *app) {
    if (!app || !app->committer) { return; }

    firc_nfcommit_free(app->committer); /* joins the thread, so no pass can still have the token attached to an engine by the time this returns */
    app->committer = NULL;

    app->nf_mu_ready = false;
    pthread_mutex_destroy(&app->nf_mu);
}

firc_err_t firc_app_force_commit_iptables(firc_app_t *app) {
    if (app->committer) { /* nothing to wait for or report: the committer aborts whatever it is writing and rebuilds from scratch */
        firc_nfcommit_request(app->committer);
        return FIRC_OK;
    }
    return FIRC_ERR_STATE;
}

void firc_app_set_tunnels(firc_app_t *app, const uint16_t ports[FIRC_KTCP_DEVICES],
                          const char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP]) {
    if (app == NULL || ports == NULL || names == NULL ||
        (memcmp(app->ktcp_ports, ports, sizeof(app->ktcp_ports)) == 0 &&
         memcmp(app->tun_names, names, sizeof(app->tun_names)) == 0)) {
        return;
    }
    app_nf_enter(app);
    memcpy(app->ktcp_ports, ports, sizeof(app->ktcp_ports));
    memcpy(app->tun_names, names, sizeof(app->tun_names));
    app_request_more_locked(app);
    firc_app_republish_resolve_routes(app);
    app_nf_leave(app);
}

void firc_app_set_ktcp_sink(firc_app_t *app, const firc_app_ktcp_sink_t *sink) {
    if (app == NULL) { return; }
    if (sink != NULL) {
        app->ktcp_sink = *sink;
    } else {
        memset(&app->ktcp_sink, 0, sizeof(app->ktcp_sink));
    }
}

static void waits_enter(firc_app_t *app) {
    if (app->loop == NULL && app->nf_mu_ready) { pthread_mutex_lock(&app->nf_mu); }
}

static void waits_leave(firc_app_t *app) {
    if (app->loop == NULL && app->nf_mu_ready) { pthread_mutex_unlock(&app->nf_mu); }
}

void firc_app_ktcp_closed(firc_app_t *app, uint8_t dev) {
    if (app == NULL || dev >= FIRC_KTCP_DEVICES) { return; }
    waits_enter(app);
    if (app->owed[dev] > 0) {
        app->owed[dev]--;
        waits_leave(app);
        return;
    }
    for (size_t i = 0; i < app->n_waits; i++) {
        ct_wait_t *w = &app->waits[i];
        if (w->dev != dev || w->gen != app->child_gen[dev] || w->acks <= 0) { continue; }
        if (--app->waits[i].acks == 0) {
            wait_pay_at(app, i);
            waits_rearm(app);
        }
        break;
    }
    waits_leave(app);
}

void firc_app_ktcp_ready(firc_app_t *app, uint8_t dev) {
    if (app == NULL || dev >= FIRC_KTCP_DEVICES) { return; }
    waits_enter(app);
    app->owed[dev] = 0;
    app->child_gen[dev]++;
    waits_leave(app);
}

void firc_app_ktcp_due_for_test(firc_app_t *app) {
    waits_enter(app);
    waits_due(app);
    waits_leave(app);
}

const firc_ktcp_image_t *firc_app_ktcp_image(const firc_app_t *app) { return app != NULL ? &app->last_image : NULL; }

const firc_xt_acl_t *firc_app_ktcp_skipped(const firc_app_t *app) {
    return app != NULL ? &app->last_skip : NULL;
}

firc_ktcp_off_t firc_app_ktcp_off(const firc_app_t *app) { return app != NULL ? app->last_off : FIRC_KTCP_OFF_NONE; }

void firc_app_set_ktcp_skipped_for_test(firc_app_t *app, const firc_xt_acl_t *acl, firc_ktcp_off_t off) {
    take_skip(app, acl, off);
}

bool firc_app_ktcp_real_of(firc_app_t *app, uint8_t dev, const uint8_t fake[4], firc_ip_t *real) {
    if (app == NULL || dev >= FIRC_KTCP_DEVICES || app->ktcp_ports[dev] == 0) { return false; }
    firc_ip_t f = {{0}, 4}, r = {{0}, 0};
    memcpy(f.b, fake, 4);
    const char *gid = NULL;
    firc_id_t id;
    if (!firc_fakeip_pair_of(app->pool, &f, &gid, &r) || firc_id_parse(gid, &id) != FIRC_OK) { return false; }
    const firc_ruleset_t *rs = firc_app_find_group_by_id(app, id);
    if (rs == NULL || !firc_ruleset_routed(rs) || group_tunnel(app, rs) != dev) {
        return false;
    }
    *real = r;
    return true;
}
