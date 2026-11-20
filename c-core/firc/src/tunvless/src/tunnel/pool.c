/* Node pool: `active` nodes work at once, new connections are spread over them, each node is
 * health checked, and a dead one is replaced by the next free candidate without restarting.
 *
 * The pool wraps the protocol's dialer (dialer.h). The protocol does not know about it: the ctx of
 * its table is still a node, but each connection has its own node, kept in the pool's session
 * header. The stack asks the pool the four optional questions of the table (peer_of, match, stale,
 * lost) and knows nothing about nodes.
 *
 * SLOTS. A slot is the place of one active node: the node (or none), whether it is alive, and a
 * generation. The generation changes when the slot's node is declared dead or replaced, and the
 * stack resets the connections of older generations (stale: RST to the client) while those of
 * live slots are left alone. A candidate is taken if it is the node of any slot: two slots never
 * hold the same node.
 *
 * SPREADING (`by`). connection — every new connection to a random live slot; site — the slot by a
 * hash of the destination address (if that slot is dead, the next live one round the circle, so
 * the sites of live slots do not move); site_client — by a hash of client and destination.
 *
 * SPARE CONNECTIONS of the stack (DC_PRECONNECT) are opened to live slots in turn, never to a node
 * whose flows share links (dialer_mux.shares): those flows take no spare. A connection
 * takes a spare that suits its node (match): with connection, any — the connection moves to the
 * spare's node; with site, only its own slot's. A spare to a node no longer active is dropped.
 *
 * HEALTH. The measure is the protocol's probe (for VLESS: connect, TLS/Reality, a request through
 * the node and the first byte of the answer), the same check that chose the node at startup.
 * Outcomes of live traffic are not a verdict, they only bring the check forward: a burst of SYNs
 * fails together on one packet loss, and a good handshake does not prove the node passes traffic.
 *   - a live node is checked every `interval` (60 s); a failure is retried after 3 s, two in a
 *     row and the node is dead (after a stall the first failure is enough: the node has already
 *     been silent longer than the threshold);
 *   - checks come early after three failed connects in a row and after a live connection is cut
 *     by the kernel for silence (lost: stack.c, node_sock_silence);
 *   - a dead node: its connections are reset (generation) and a replacement is searched at once
 *     among the free candidates. A round probes its own node first, then at most 8 free
 *     candidates while another slot is up (one that failed recently waits out its backoff); the
 *     next round comes 5 min later after a round that probed a free candidate, otherwise after
 *     15 s, then 30 s. With no slot up only the first slot that is not up probes free
 *     candidates, 16 a round, every one in turn, and the next round comes after 15 s, 30 s once
 *     all were tried. An empty slot (fewer candidates answered at startup than `active`) searches
 *     the same way.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <pthread.h>

#include "pool.h"
#include "../control.h"
#include "transport.h"
#include "xconn.h"
#include "osrand.h"
#include "tstack.h"

#ifndef GRND_NONBLOCK
#define GRND_NONBLOCK 0x0001
#endif

#define PL_LOG_W "tunvless[warn]: "
#define PL_LOG_I "tunvless[info]: "
#define PL_LOG_E "tunvless[error]: "
#define PL_LOG_D "tunvless[debug]: "

#define PL_CONFIRM_S   3
#define PL_RETRY_S     15
#define PL_RETRY_MAX_S 30
#define PL_STREAK      3
#define PL_UNPROVEN    2
#define PL_TIMEOUT_S   8
#define PL_TIMEOUT6_S  4
#define PL_GAP6        10
/* A cut connection brings a check forward at most this often: the connections of a dead node are
 * cut together, and one check is enough. */
#define PL_LOST_GAP_MS 5000
#define PL_DRAINED 8
#define PL_BACK_S      300
#define PL_BACK_MAX_S  1800
#define PL_EXTRA_S     300

/* The pool's session header; the protocol's session follows at PL_HDR. */
struct pl_sess {
    const void *node;           /* the connection's node; NULL — a spare not yet anybody's */
    unsigned gen;               /* the slot's generation when the node was chosen */
    int slot;                   /* -1 — none */
    uint8_t udp, opened;        /* flow_open was called with k */
    uint8_t pinned;
    uint8_t kind;
    struct flow_key k;
};
#define PL_HDR 96
_Static_assert(sizeof(struct pl_sess) <= PL_HDR, "pool session header larger than PL_HDR");
#define INNER(s) ((void *)((unsigned char *)(s) + PL_HDR))
#define CINNER(s) ((const void *)((const unsigned char *)(s) + PL_HDR))
#define PL_K(s) ((s)->kind == 1)

struct pl_slot {
    int node;                   /* node index; -1 — empty */
    int up;                     /* alive: new connections come here */
    unsigned gen;
    int fails;                  /* failed checks in a row */
    int streak;                 /* failed connects in a row */
    int unproven;
    int kick;                   /* check now */
    int lost;                   /* a stall brought the check: one failure is enough */
    uint64_t due;               /* next check, ms of CLOCK_MONOTONIC */
    uint64_t retry;             /* pause between rounds of a dead or empty slot, s */
    uint64_t checked_at;
    unsigned drained[PL_DRAINED];
    unsigned drained_n;
    int v6;
    int skip6;
    unsigned woke;
    uint64_t rot;
};

struct pl_sel { struct pl_sel *old; size_t n; int c[]; };

struct pl_cand { uint32_t fails; uint64_t next, last; };

static struct {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    struct pool_cfg cf;
    const struct dialer_ops *in[2];
    unsigned (*kind)(const void *node);
    struct pl_slot *slot;
    struct pl_sel *sel;
    int n;                      /* slots: active, but no more than candidates */
    unsigned rr;                /* round robin of spare connections */
    int said_up;                /* the last state logged: some node alive */
    int pub;
    char why[256];              /* reason of the last failed check */
    char sig[1024];             /* the last active set logged */
    uint64_t sig6;
    int any_v6;
    int v6_retry;
    int *down;                  /* nodes reported down and not reported up since */
    size_t down_n, down_cap;
    struct pl_cand *cand;
    size_t cand_n;
} g_pl = { .mu = PTHREAD_MUTEX_INITIALIZER };

static const void *pl_node(int i) {
    return g_pl.cf.node(i);
}

static uint64_t g_pl_clock_ms;

static uint64_t pl_now_ms(void) {
    if (g_pl_clock_ms) return g_pl_clock_ms;
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000ull + (uint64_t)ts.tv_nsec / 1000000ull;
}

int pool_by_parse(const char *s) {
    if (!strcmp(s, "connection")) return POOL_BY_CONNECTION;
    if (!strcmp(s, "site")) return POOL_BY_SITE;
    if (!strcmp(s, "site-client") || !strcmp(s, "site_client")) return POOL_BY_SITE_CLIENT;
    return -1;
}

const char *pool_by_name(int by) {
    switch (by) {
    case POOL_BY_SITE: return "site";
    case POOL_BY_SITE_CLIENT: return "site-client";
    default: return "connection";
    }
}

/* ---- spreading ------------------------------------------------------------------------- */

static uint32_t pl_mix(uint32_t h) {
    h ^= h >> 16;
    h *= 0x7feb352du;
    h ^= h >> 15;
    h *= 0x846ca68bu;
    h ^= h >> 16;
    return h;
}

/* Per loop thread; spreading evenly needs no cryptographic strength. */
static unsigned pl_rand(void) {
    static __thread uint32_t x;
    if (!x) {
        if (os_getrandom(&x, sizeof x, GRND_NONBLOCK) != (ssize_t)sizeof x || !x)
            x = (uint32_t)pl_now_ms() ^ (uint32_t)getpid() ^ 0x9e3779b9u;
        if (!x) x = 1;
    }
    x ^= x << 13;
    x ^= x >> 17;
    x ^= x << 5;
    return x;
}

static int pl_fits(const struct pl_slot *sl, int v6) {
    return sl->node >= 0 && sl->up && (!v6 || sl->v6);
}

/* The slot for a new connection. Under the lock. -1 — no slot has a node. With no live slot, a
 * dead one that has a node: the connection fails fast (RST). An IPv6 connection takes only a live
 * slot whose node carries IPv6, and no dead one: -1 without such a slot. */
static int pl_pick(const struct flow_key *k) {
    int v6 = k && k->v6;
    int live[64], nl = 0, any = -1;
    for (int i = 0; i < g_pl.n; i++) {
        if (g_pl.slot[i].node < 0) continue;
        if (any < 0 && !v6) any = i;
        if (pl_fits(&g_pl.slot[i], v6) && nl < (int)(sizeof live / sizeof live[0])) live[nl++] = i;
    }
    if (!nl) return any;
    if (g_pl.cf.by == POOL_BY_CONNECTION || !k) return live[pl_rand() % (unsigned)nl];
    int by_client = g_pl.cf.by == POOL_BY_SITE_CLIENT;
    uint32_t h = by_client ? pl_mix(k->src.v4 * 0x9e3779b1u) ^ k->dst.v4 : k->dst.v4;
    for (int i = 1; i < 4; i++) {
        uint32_t s, d;
        memcpy(&s, k->src.b + 4 * i, 4);
        memcpy(&d, k->dst.b + 4 * i, 4);
        h ^= (by_client ? s * 0x9e3779b1u : 0) ^ d;
    }
    int at = (int)(pl_mix(h) % (uint32_t)g_pl.n);
    for (int d = 0; d < g_pl.n; d++)
        if (pl_fits(&g_pl.slot[(at + d) % g_pl.n], v6)) return (at + d) % g_pl.n;
    return any;
}

static void pl_bind(struct pl_sess *s, int slot) {
    s->slot = slot;
    s->node = slot >= 0 ? pl_node(g_pl.slot[slot].node) : NULL;
    s->gen = slot >= 0 ? g_pl.slot[slot].gen : 0;
}

static uint8_t pl_kind_of(const void *node) { return node && g_pl.kind && g_pl.kind(node) ? 1 : 0; }

static const struct dialer_ops *pl_ops_of(const void *node) { return g_pl.in[pl_kind_of(node)]; }

static void pl_leave(struct pl_sess *s) {
    g_pl.in[1]->clear(INNER(s));
    memset(INNER(s), 0, g_pl.in[1]->sess_size);
    g_pl.in[0]->clear(INNER(s));
    s->kind = 0;
}

static void pl_enter(struct pl_sess *s) {
    uint8_t k = pl_kind_of(s->node);
    s->kind = PL_K(s);
    if (k == s->kind) return;
    if (!k) {
        pl_leave(s);
        return;
    }
    memset(INNER(s), 0, g_pl.in[1]->sess_size);
    g_pl.in[1]->clear(INNER(s));
    s->kind = 1;
}

/* ---- health: state in the log ----------------------------------------------------------- */

/* Log the pool's state when it changes: the active nodes and which carry IPv6, and the moment no
 * node answers (with the reason of the last failed check) or one answers again. Tells v6_changed
 * when the first active node carries IPv6 or the last stops. Takes the lock itself. */
static void pl_publish(void) {
    char names[1024] = "";
    const char *plain[64];
    int idx[64], v6[64];
    size_t w = 0, np = 0;
    int any = 0, any6 = 0;
    uint64_t sig6 = 0;
    pthread_mutex_lock(&g_pl.mu);
    for (int i = 0; i < g_pl.n; i++) {
        if (g_pl.slot[i].node < 0 || !g_pl.slot[i].up) continue;
        any = 1;
        if (g_pl.slot[i].v6) any6 = 1;
        const char *nm = g_pl.cf.proto->name(pl_node(g_pl.slot[i].node));
        if (np < 64) {
            idx[np] = g_pl.slot[i].node;
            v6[np] = g_pl.slot[i].v6;
            if (v6[np]) sig6 |= 1ull << np;
            plain[np++] = nm;
        }
        if (w + 2 < sizeof names)
            w += (size_t)snprintf(names + w, sizeof names - w, "%s%s (#%d)", w ? ", " : "", nm,
                                  g_pl.slot[i].node);
    }
    int flip6 = any6 != g_pl.any_v6 || (any6 && g_pl.v6_retry);
    g_pl.any_v6 = any6;
    pthread_mutex_unlock(&g_pl.mu);
    if (flip6 && g_pl.cf.v6_changed) g_pl.v6_retry = g_pl.cf.v6_changed(any6) != 0 && any6;

    if (any && !g_pl.said_up) {
        g_pl.said_up = 1;
    } else if (!any && g_pl.said_up) {
        g_pl.said_up = 0;
        fprintf(stderr, PL_LOG_W "no active node answers (%s) — new connections are refused "
                        "until one does\n", g_pl.why[0] ? g_pl.why : "no answer");
    }
    if (w >= sizeof names - 2) w = sizeof names - 1;
    if (!strcmp(names, g_pl.sig) && sig6 == g_pl.sig6) return;
    snprintf(g_pl.sig, sizeof g_pl.sig, "%s", names);
    g_pl.sig6 = sig6;
    if (any) fprintf(stderr, PL_LOG_I "active: %s\n", names);
    ctl_emit_active(plain, idx, v6, np);
}

/* ---- health: checks --------------------------------------------------------------------- */

static int pl_probe(int node, char *why, size_t n) {
    return g_pl.cf.proto->probe(pl_node(node), PL_TIMEOUT_S, why, n);
}

static void pl_probe6(int i, int node, unsigned gen) {
    if (!g_pl.cf.proto->probe6) return;
    char why[256];
    int ok = g_pl.cf.proto->probe6(pl_node(node), PL_TIMEOUT6_S, why, sizeof why) == 0;
    pthread_mutex_lock(&g_pl.mu);
    struct pl_slot *sl = &g_pl.slot[i];
    int cur = sl->gen == gen, was = sl->v6;
    if (cur) {
        sl->v6 = ok;
        sl->skip6 = PL_GAP6 - 1;
    }
    pthread_mutex_unlock(&g_pl.mu);
    if (!cur) return;
    if (ok && !was)
        fprintf(stderr, PL_LOG_I "node %s carries IPv6\n", g_pl.cf.proto->name(pl_node(node)));
    else if (!ok && was)
        fprintf(stderr, PL_LOG_I "node %s no longer carries IPv6: %s\n", g_pl.cf.proto->name(pl_node(node)), why);
    pl_publish();
}

/* Whether candidate c is the node of another slot (live, or dead and waiting for it). Under the
 * lock. */
static int pl_taken(int c, int except) {
    for (int i = 0; i < g_pl.n; i++)
        if (i != except && g_pl.slot[i].node == c) return 1;
    return 0;
}

static void pl_slot_alive(struct pl_slot *sl) {
    sl->up = 1;
    sl->fails = sl->streak = sl->kick = sl->lost = 0;
    if (!sl->unproven) sl->retry = PL_RETRY_S;
    sl->checked_at = pl_now_ms();
    sl->due = sl->checked_at + (uint64_t)g_pl.cf.interval_s * 1000ull;
}

static void pl_mark_down(int node) {
    for (size_t k = 0; k < g_pl.down_n; k++)
        if (g_pl.down[k] == node) return;
    if (g_pl.down_n == g_pl.down_cap) {
        size_t cap = g_pl.down_cap ? g_pl.down_cap * 2 : 8;
        int *d = realloc(g_pl.down, cap * sizeof(*d));
        if (!d) return;
        g_pl.down = d;
        g_pl.down_cap = cap;
    }
    g_pl.down[g_pl.down_n++] = node;
}

static int pl_unmark_down(int node) {
    for (size_t k = 0; k < g_pl.down_n; k++)
        if (g_pl.down[k] == node) {
            g_pl.down[k] = g_pl.down[--g_pl.down_n];
            return 1;
        }
    return 0;
}

static int pl_in(const struct pl_sel *sel, int c) {
    for (size_t k = 0; k < sel->n; k++)
        if (sel->c[k] == c) return 1;
    return 0;
}

static struct pl_cand *pl_peek(int c) {
    return c >= 0 && (size_t)c < g_pl.cand_n ? &g_pl.cand[c] : NULL;
}

static struct pl_cand *pl_rec(int c) {
    if (c < 0) return NULL;
    if ((size_t)c >= g_pl.cand_n) {
        size_t n = g_pl.cand_n * 2 > (size_t)c + 1 ? g_pl.cand_n * 2 : (size_t)c + 1;
        struct pl_cand *r = realloc(g_pl.cand, n * sizeof(*r));
        if (!r) return NULL;
        memset(r + g_pl.cand_n, 0, (n - g_pl.cand_n) * sizeof(*r));
        g_pl.cand = r;
        g_pl.cand_n = n;
    }
    return &g_pl.cand[c];
}

static uint64_t pl_backoff_ms(uint32_t fails) {
    uint64_t s = PL_BACK_S;
    for (uint32_t k = 1; k < fails && s < PL_BACK_MAX_S; k++) s *= 2;
    return (s < PL_BACK_MAX_S ? s : PL_BACK_MAX_S) * 1000ull;
}

static void pl_note(int c, int ok, uint64_t now) {
    struct pl_cand *r = ok ? pl_peek(c) : pl_rec(c);
    if (!r) return;
    if (ok) {
        r->fails = 0;
        r->next = r->last = 0;
        return;
    }
    if (r->fails < UINT32_MAX) r->fails++;
    r->last = now;
    r->next = now + pl_backoff_ms(r->fails);
}

struct pl_key { uint64_t tier, last; size_t at; };

static int pl_key_lt(const struct pl_key *a, const struct pl_key *b) {
    if (a->tier != b->tier) return a->tier < b->tier;
    if (a->last != b->last) return a->last < b->last;
    return a->at < b->at;
}

static size_t pl_order(const int *sel, size_t n, int self, int prev, int down, uint64_t now, int *out,
                       size_t k) {
    struct pl_key after = { 0, 0, 0 };
    size_t m = 0;
    while (m < k) {
        struct pl_key best = { 0, 0, 0 };
        int found = 0;
        for (size_t j = 0; j < n; j++) {
            int c = sel[j];
            if (c == prev || pl_taken(c, self)) continue;
            const struct pl_cand *r = pl_peek(c);
            int failed = r && r->fails;
            if (!down && failed && now < r->next) continue;
            struct pl_key key = { (uint64_t)(down && failed), down && failed ? r->last : 0, j };
            if (m && !pl_key_lt(&after, &key)) continue;
            if (found && !pl_key_lt(&key, &best)) continue;
            best = key;
            found = 1;
        }
        if (!found) break;
        out[m++] = sel[best.at];
        after = best;
    }
    return m;
}

size_t pool_order(const int *sel, size_t n, int *out, size_t k) {
    pthread_mutex_lock(&g_pl.mu);
    size_t m = pl_order(sel, n, -1, -1, 1, pl_now_ms(), out, k);
    pthread_mutex_unlock(&g_pl.mu);
    return m;
}

void pool_note(int c, int ok) {
    pthread_mutex_lock(&g_pl.mu);
    pl_note(c, ok, pl_now_ms());
    pthread_mutex_unlock(&g_pl.mu);
}

static int pl_none_up(void) {
    for (int k = 0; k < g_pl.n; k++)
        if (g_pl.slot[k].node >= 0 && g_pl.slot[k].up) return 0;
    return 1;
}

static int pl_leader(void) {
    for (int k = 0; k < g_pl.n; k++)
        if (g_pl.slot[k].node < 0 || !g_pl.slot[k].up) return k;
    return -1;
}

static size_t pl_free_n(int i, int prev, uint64_t rot, int *rotated) {
    size_t f = 0;
    *rotated = 1;
    for (size_t k = 0; k < g_pl.sel->n; k++) {
        int c = g_pl.sel->c[k];
        if (c == prev || pl_taken(c, i)) continue;
        f++;
        const struct pl_cand *r = pl_peek(c);
        if (!r || !r->fails || r->last < rot) *rotated = 0;
    }
    return f;
}

/* Give slot i a live node: its own previous one (if own), then a bounded number of free
 * candidates in the round's order. 1 — found. Probes run without the lock; the state is logged
 * after every failure, so "no node answers" is said at once, not after the whole round. */
static int pl_refill(int i, int own) {
    char why[256];
    pthread_mutex_lock(&g_pl.mu);
    int prev = g_pl.slot[i].node;
    unsigned gen = g_pl.slot[i].gen, woke = g_pl.slot[i].woke;
    const struct pl_sel *sel = g_pl.sel;
    uint64_t now = pl_now_ms();
    int down = pl_none_up(), leader = pl_leader() == i;
    pthread_mutex_unlock(&g_pl.mu);
    if (own && prev >= 0) {
        int rc = pl_probe(prev, why, sizeof why);
        pthread_mutex_lock(&g_pl.mu);
        pl_note(prev, rc == 0, now);
        if (rc == 0) {
            if (g_pl.slot[i].gen != gen) { pthread_mutex_unlock(&g_pl.mu); return 0; }
            pl_slot_alive(&g_pl.slot[i]);
            pl_unmark_down(prev);
            pthread_mutex_unlock(&g_pl.mu);
            fprintf(stderr, PL_LOG_I "node %s answers again\n", g_pl.cf.proto->name(pl_node(prev)));
            ctl_emit_node_up(g_pl.cf.proto->name(pl_node(prev)), prev);
            pl_publish();
            pl_probe6(i, prev, gen);
            return 1;
        }
        pthread_mutex_unlock(&g_pl.mu);
        snprintf(g_pl.why, sizeof g_pl.why, "%s", why);
        pl_publish();
    }
    int out[POOL_WALK_DOWN];
    size_t m = 0, probed = 0;
    if (!down || leader) {
        pthread_mutex_lock(&g_pl.mu);
        m = pl_order(sel->c, sel->n, i, prev, down, now, out, down ? POOL_WALK_DOWN : POOL_WALK);
        pthread_mutex_unlock(&g_pl.mu);
    }
    char ok[POOL_WALK_DOWN];
    int err[POOL_WALK_DOWN] = { 0 };
    memset(ok, 1, sizeof ok);
    if (m && g_pl.cf.proto->reach) {
        const void *nd[POOL_WALK_DOWN];
        for (size_t k = 0; k < m; k++) nd[k] = pl_node(out[k]);
        if (pool_reach_some(g_pl.cf.proto, nd, m, POOL_REACH_MS, ok, err) < 0) memset(ok, 1, sizeof ok);
    }
    for (size_t k = 0; k < m; k++) {
        int c = out[k];
        pthread_mutex_lock(&g_pl.mu);
        int moved = g_pl.slot[i].gen != gen;
        int busy = c == prev || pl_taken(c, i);
        pthread_mutex_unlock(&g_pl.mu);
        if (moved) return 0;
        if (busy) continue;
        if (!ok[k]) {
            probed++;
            pthread_mutex_lock(&g_pl.mu);
            pl_note(c, 0, now);
            pthread_mutex_unlock(&g_pl.mu);
            if (!g_pl.why[0]) {
                const char *t = tr_dial_strerror(err[k]);
                snprintf(g_pl.why, sizeof g_pl.why, "%s", t ? t : "no TCP answer");
                pl_publish();
            }
            continue;
        }
        int rc = pl_probe(c, why, sizeof why);
        probed++;
        pthread_mutex_lock(&g_pl.mu);
        pl_note(c, rc == 0, now);
        if (rc != 0) {
            pthread_mutex_unlock(&g_pl.mu);
            if (!g_pl.why[0]) snprintf(g_pl.why, sizeof g_pl.why, "%s", why);
            pl_publish();
            continue;
        }
        if (g_pl.slot[i].gen != gen) { pthread_mutex_unlock(&g_pl.mu); return 0; }
        if (pl_taken(c, i) || !pl_in(g_pl.sel, c)) { pthread_mutex_unlock(&g_pl.mu); continue; }
        struct pl_slot *sl = &g_pl.slot[i];
        sl->node = c;
        __atomic_add_fetch(&sl->gen, 1, __ATOMIC_RELEASE);  /* pl_stale reads it without the lock */
        sl->unproven = 0;
        pl_slot_alive(sl);
        unsigned ngen = sl->gen;
        int n = g_pl.n, again = pl_unmark_down(c);
        pthread_mutex_unlock(&g_pl.mu);
        if (again) {
            fprintf(stderr, PL_LOG_I "node %s answers again\n", g_pl.cf.proto->name(pl_node(c)));
            ctl_emit_node_up(g_pl.cf.proto->name(pl_node(c)), c);
        }
        if (prev >= 0)
            fprintf(stderr, PL_LOG_D "node %s does not answer — %s takes its place\n",
                    g_pl.cf.proto->name(pl_node(prev)), g_pl.cf.proto->name(pl_node(c)));
        else
            fprintf(stderr, PL_LOG_I "active node %d of %d: %s\n", i + 1, n,
                    g_pl.cf.proto->name(pl_node(c)));
        stack_nodes_changed();
        pl_publish();
        pl_probe6(i, c, ngen);
        return 1;
    }
    pthread_mutex_lock(&g_pl.mu);
    struct pl_slot *sl = &g_pl.slot[i];
    if (sl->gen != gen || g_pl.sel != sel || sl->woke != woke) { pthread_mutex_unlock(&g_pl.mu); return 0; }
    int none_up = pl_none_up(), rotated;
    size_t free_n = pl_free_n(i, prev, sl->rot, &rotated);
    int pause_s = !none_up && probed ? PL_EXTRA_S : (int)sl->retry;
    sl->due = pl_now_ms() + (uint64_t)pause_s * 1000ull;
    if (!none_up || rotated) {
        sl->retry = sl->retry * 2 > PL_RETRY_MAX_S ? PL_RETRY_MAX_S : sl->retry * 2;
        if (none_up) sl->rot = now + 1;
    }
    pthread_mutex_unlock(&g_pl.mu);
    fprintf(stderr, PL_LOG_D "slot %d: %zu of %zu free candidates probed, none answers; next round in %d s\n",
            i + 1, probed, free_n, pause_s);
    pl_publish();
    if (none_up) ctl_emit_no_node(pause_s);
    return 0;
}

static int pl_has_free(int i) {
    for (size_t k = 0; k < g_pl.sel->n; k++) {
        int c = g_pl.sel->c[k];
        if (c != g_pl.slot[i].node && !pl_taken(c, i)) return 1;
    }
    return 0;
}

/* One check of slot i: a live slot gets its regular check, a dead or empty one a search round. */
static void pl_check(int i) {
    pthread_mutex_lock(&g_pl.mu);
    struct pl_slot *sl = &g_pl.slot[i];
    int node = sl->node, up = sl->up, lost = sl->lost, kicked = sl->kick;
    unsigned gen = sl->gen;
    sl->kick = sl->lost = 0;
    pthread_mutex_unlock(&g_pl.mu);
    if (node < 0 || !up) { pl_refill(i, 1); return; }

    char why[256];
    int rc = pl_probe(node, why, sizeof why);
    pthread_mutex_lock(&g_pl.mu);
    if (sl->gen != gen) { pthread_mutex_unlock(&g_pl.mu); return; }
    sl->checked_at = pl_now_ms();
    int unproven = rc == 0 && kicked && !lost && ++sl->unproven >= PL_UNPROVEN && pl_has_free(i);
    if (rc == 0 && !unproven) {
        pl_note(node, 1, sl->checked_at);
        sl->fails = sl->streak = 0;
        sl->due = sl->checked_at + (uint64_t)g_pl.cf.interval_s * 1000ull;
        int do6 = sl->v6 || sl->skip6 <= 0;
        if (!do6) sl->skip6--;
        pthread_mutex_unlock(&g_pl.mu);
        if (do6) pl_probe6(i, node, gen);
        return;
    }
    /* Two failures in a row, against a single loss on a radio link; one is enough when a stall
     * brought the check. */
    if (unproven)
        snprintf(why, sizeof why, "the check passes but connections do not open (%d failed connects in a row)",
                 PL_STREAK * sl->unproven);
    if (!unproven && ++sl->fails < 2 && !lost) {
        sl->due = sl->checked_at + PL_CONFIRM_S * 1000ull;
        pthread_mutex_unlock(&g_pl.mu);
        return;
    }
    /* Dead: its connections are reset now (generation), without waiting for a replacement. */
    sl->up = 0;
    sl->v6 = 0;
    sl->fails = 0;
    __atomic_add_fetch(&sl->gen, 1, __ATOMIC_RELEASE);
    if (!unproven) sl->retry = PL_RETRY_S;
    pl_mark_down(node);
    pl_note(node, 0, sl->checked_at);
    sl->rot = sl->checked_at;
    if (pl_none_up())
        for (int k = 0; k < g_pl.n; k++) {
            struct pl_slot *o = &g_pl.slot[k];
            if (k == i || (o->node >= 0 && o->up)) continue;
            o->retry = PL_RETRY_S;
            o->due = o->rot = sl->checked_at;
            o->woke++;
        }
    pthread_mutex_unlock(&g_pl.mu);
    snprintf(g_pl.why, sizeof g_pl.why, "%s", why);
    fprintf(stderr, PL_LOG_D "node %s does not answer: %s — looking for a replacement\n",
            g_pl.cf.proto->name(pl_node(node)), why);
    ctl_emit_node_down(g_pl.cf.proto->name(pl_node(node)), node, why);
    stack_nodes_changed();
    /* Its own node has just failed twice: only the free candidates now; it waits for the next
     * round. */
    pl_refill(i, 0);
}

/* The slot to check next: one asked to be checked now first, otherwise the earliest due. Under the
 * lock; waits until it is due. */
static int pl_next(void) {
    for (;;) {
        if (g_pl.pub) {
            g_pl.pub = 0;
            return -1;
        }
        uint64_t now = pl_now_ms(), due = UINT64_MAX;
        int best = -1;
        for (int i = 0; i < g_pl.n; i++) {
            const struct pl_slot *sl = &g_pl.slot[i];
            if (sl->kick && sl->up && sl->node >= 0) return i;
            if (sl->due < due) { due = sl->due; best = i; }
        }
        if (best >= 0 && due <= now) return best;
        struct timespec ts = { .tv_sec = (time_t)(due / 1000), .tv_nsec = (long)(due % 1000) * 1000000L };
        pthread_cond_timedwait(&g_pl.cv, &g_pl.mu, &ts);
    }
}

static void pl_start6(void) {
    if (!g_pl.cf.checked) return;
    pthread_mutex_lock(&g_pl.mu);
    int node = g_pl.slot[0].node, up = g_pl.slot[0].up;
    unsigned gen = g_pl.slot[0].gen;
    pthread_mutex_unlock(&g_pl.mu);
    if (node >= 0 && up) pl_probe6(0, node, gen);
}

static void *pl_thread(void *arg) {
    (void)arg;
    pl_start6();
    for (;;) {
        pthread_mutex_lock(&g_pl.mu);
        int i = pl_next();
        pthread_mutex_unlock(&g_pl.mu);
        if (i < 0) pl_publish();
        else pl_check(i);
    }
    return NULL;
}

/* Under the lock. */
static void pl_kick(int i) {
    g_pl.slot[i].kick = 1;
    pthread_cond_signal(&g_pl.cv);
}

/* ---- the pool's dialer ------------------------------------------------------------------ */

static const char *pl_peer(const void *ctx) {
    (void)ctx;
    const char *r = "?";
    pthread_mutex_lock(&g_pl.mu);
    for (int i = 0; i < g_pl.n; i++)
        if (g_pl.slot[i].node >= 0) {
            const void *nd = pl_node(g_pl.slot[i].node);
            r = pl_ops_of(nd)->peer(nd);
            break;
        }
    pthread_mutex_unlock(&g_pl.mu);
    return r;
}

static const char *pl_peer_of(const void *ctx, const void *sess) {
    const struct pl_sess *s = sess;
    return s->node ? g_pl.in[PL_K(s)]->peer(s->node) : pl_peer(ctx);
}

static void pl_describe(const void *ctx, char *out, size_t n) {
    (void)ctx;
    size_t w = 0;
    out[0] = '\0';
    pthread_mutex_lock(&g_pl.mu);
    for (int i = 0; i < g_pl.n && w + 2 < n; i++) {
        if (g_pl.slot[i].node < 0) continue;
        if (w) w += (size_t)snprintf(out + w, n - w, ", ");
        if (w < n) {
            const void *nd = pl_node(g_pl.slot[i].node);
            pl_ops_of(nd)->describe(nd, out + w, n - w);
            w += strlen(out + w);
        }
    }
    pthread_mutex_unlock(&g_pl.mu);
    if (g_pl.cf.active > 1 && w < n)
        snprintf(out + w, n - w, " — %d active, spread by %s", g_pl.cf.active, pool_by_name(g_pl.cf.by));
}

static const char *pl_strerror(int rc) { return g_pl.in[0]->strerror(rc); }
static const char *pl_why(const void *sess, int rc) {
    const struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    return o->why ? o->why(CINNER(s), rc) : o->strerror(rc);
}

/* A connect outcome for the slot's health: a streak of failures brings its check forward. */
static void pl_seen(int slot, unsigned gen, int rc, uint64_t started) {
    if (slot < 0) return;
    pthread_mutex_lock(&g_pl.mu);
    struct pl_slot *sl = &g_pl.slot[slot];
    if (sl->gen == gen) {
        if (rc == 0) sl->streak = sl->unproven = 0;
        else if (started >= sl->checked_at && ++sl->streak >= PL_STREAK) {
            sl->streak = 0;
            pl_kick(slot);
        }
    }
    pthread_mutex_unlock(&g_pl.mu);
}

static void pl_seen_at(const struct pl_sess *s, int rc, uint64_t started) { pl_seen(s->slot, s->gen, rc, started); }

static int pl_slot_dead(int slot) {
    if (slot < 0) return 0;
    pthread_mutex_lock(&g_pl.mu);
    int dead = !g_pl.slot[slot].up;
    pthread_mutex_unlock(&g_pl.mu);
    return dead;
}

static int pl_mux_shares(const void *node) {
    if (pl_kind_of(node)) return 1;
    const struct dialer_mux *m = g_pl.in[0]->mux;
    return m && m->shares && m->shares(node);
}

static int pl_connect(const void *ctx, void *sess, int timeout_s) {
    (void)ctx;
    struct pl_sess *s = sess;
    if (!s->node) {
        /* A spare: to the live slots in turn, past the nodes whose flows share links and never
         * take one. */
        pthread_mutex_lock(&g_pl.mu);
        int pick = -1, shared = 0;
        for (int d = 0; d < g_pl.n && pick < 0; d++) {
            int i = (int)((g_pl.rr + (unsigned)d) % (unsigned)g_pl.n);
            if (g_pl.slot[i].node < 0 || !g_pl.slot[i].up) continue;
            if (pl_mux_shares(pl_node(g_pl.slot[i].node))) shared = 1;
            else pick = i;
        }
        if (pick >= 0) {
            g_pl.rr = (unsigned)pick + 1;
            pl_bind(s, pick);
        }
        pthread_mutex_unlock(&g_pl.mu);
        if (pick < 0) return shared ? DIAL_NOSPARE : TR_ENONODE;
    } else if (pl_slot_dead(s->slot)) {
        return TR_ENONODE;
    }
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    if (!o->connect) return TR_ENONODE;
    uint64_t started = pl_now_ms();
    int rc = o->connect(s->node, INNER(s), timeout_s);
    pl_seen_at(s, rc, started);
    return rc;
}

/* A spare connection to a client's connection. If the spare's node is another one (by connection),
 * the connection moves to it: the protocol's flow state is set up again for the new node (VLESS:
 * UUID and Vision), then the link is moved. */
static void pl_take(void *dst, void *src) {
    struct pl_sess *d = dst, *s = src;
    if (d->node != s->node) {
        if (PL_K(d) != PL_K(s)) g_pl.in[PL_K(d)]->close(INNER(d));
        d->node = s->node;
        d->slot = s->slot;
        d->gen = s->gen;
        pl_enter(d);
        if (d->opened) g_pl.in[PL_K(d)]->flow_open(d->node, INNER(d), &d->k, d->udp);
    }
    g_pl.in[PL_K(d)]->take(INNER(d), INNER(s));
    s->node = NULL;
    s->slot = -1;
}

static void pl_close(void *sess) {
    struct pl_sess *s = sess;
    g_pl.in[PL_K(s)]->close(INNER(s));
}

static void pl_clear(void *sess) {
    struct pl_sess *s = sess;
    s->node = NULL;
    s->slot = -1;
    s->opened = 0;
    s->pinned = 0;
    if (PL_K(s)) {
        pl_leave(s);
        return;
    }
    s->kind = 0;
    g_pl.in[0]->clear(INNER(s));
}

static int pl_fd(const void *sess) {
    const struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->fd(CINNER(s));
}
static int pl_has_data(const void *sess) {
    const struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->has_data(CINNER(s));
}
static int pl_due(void *sess, uint64_t now_ms, int *next_ms) {
    struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    if (!o->due) {
        *next_ms = -2;
        return SEND_OK;
    }
    return o->due(INNER(s), now_ms, next_ms);
}

static int pl_flow_open(const void *ctx, void *sess, const struct flow_key *k, int udp) {
    (void)ctx;
    struct pl_sess *s = sess;
    pthread_mutex_lock(&g_pl.mu);
    pl_bind(s, pl_pick(k));
    pthread_mutex_unlock(&g_pl.mu);
    if (!s->node) return -1;
    pl_enter(s);
    s->k = *k;
    s->udp = (uint8_t)udp;
    s->opened = 1;
    return g_pl.in[PL_K(s)]->flow_open(s->node, INNER(s), k, udp);
}

int pool_flow_open_on(void *sess, int node, const struct flow_key *k, int udp, const char *host) {
    struct pl_sess *s = sess;
    if (node < 0) {
        struct flow_key pk = *k;
        if (!pk.v6 && !pk.dst.v4 && host && host[0]) {
            pk.dst.v4 = 0x811c9dc5u;
            for (const unsigned char *p = (const unsigned char *)host; *p; p++)
                pk.dst.v4 = (pk.dst.v4 ^ *p) * 0x01000193u;
        }
        pthread_mutex_lock(&g_pl.mu);
        pl_bind(s, pl_pick(&pk));
        pthread_mutex_unlock(&g_pl.mu);
        if (!s->node) return -1;
        s->pinned = 0;
    } else {
        s->node = pl_node(node);
        s->slot = -1;
        s->gen = 0;
        s->pinned = 1;
    }
    pl_enter(s);
    s->k = *k;
    s->udp = (uint8_t)udp;
    s->opened = 1;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    int rc = o->flow_open(s->node, INNER(s), k, udp);
    if (rc || !host || !host[0]) return rc;
    return o->set_host && o->set_host(INNER(s), host) == 0 ? 0 : -1;
}

static int pl_set_host(void *sess, const char *host) {
    struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    return o->set_host ? o->set_host(INNER(s), host) : -1;
}

static int pl_send(const void *ctx, void *sess, const struct flow_key *k, int udp,
                   const unsigned char *d, size_t n) {
    (void)ctx;
    struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->send(s->node, INNER(s), k, udp, d, n);
}

static long pl_room(const void *ctx, void *sess) {
    (void)ctx;
    struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    if (!o->room || !s->node) return -1;
    return o->room(s->node, INNER(s));
}

static int pl_finish(const void *ctx, void *sess) {
    (void)ctx;
    struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    return o->finish && s->node ? o->finish(s->node, INNER(s)) : SEND_OK;
}

static size_t pl_up_write(const void *ctx, void *sess) {
    (void)ctx;
    struct pl_sess *s = sess;
    const struct dialer_ops *o = g_pl.in[PL_K(s)];
    if (!s->node || !o->up_write) return 0;
    return o->up_write(s->node, INNER(s));
}

static size_t pl_dgram_frame(const void *ctx, const void *sess, const unsigned char *p, size_t n, unsigned char *out,
                             size_t cap) {
    (void)ctx;
    const struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->dgram_frame(s->node, CINNER(s), p, n, out, cap);
}

static int pl_read(void *sess, unsigned char *buf, size_t cap, const unsigned char **data, size_t *got) {
    struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->read(INNER(s), buf, cap, data, got);
}

static int pl_deliver(const void *ctx, void *sess, int udp, const unsigned char *d, size_t n,
                      dialer_emit_fn emit, void *arg) {
    (void)ctx;
    struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->deliver(s->node, INNER(s), udp, d, n, emit, arg);
}

static int pl_match(const void *ctx, const void *dst, const void *src) {
    (void)ctx;
    const struct pl_sess *d = dst, *s = src;
    if (d->pinned) return 0;
    if (!s->node || s->slot < 0) return -1;
    pthread_mutex_lock(&g_pl.mu);
    const struct pl_slot *sl = &g_pl.slot[s->slot];
    int live = sl->gen == s->gen && sl->up, v6 = sl->v6;
    pthread_mutex_unlock(&g_pl.mu);
    if (!live) return -1;
    if (d->opened && d->k.v6 && !v6) return 0;
    if (g_pl.cf.by == POOL_BY_CONNECTION) return 1;
    return d->slot == s->slot;
}

static int pl_gen_stale(const void *node, int slot, unsigned gen) {
    if (slot < 0 || !node) return 0;
    const struct pl_slot *sl = &g_pl.slot[slot];
    if (__atomic_load_n(&sl->gen, __ATOMIC_ACQUIRE) == gen) return 0;
    for (int k = 0; k < PL_DRAINED; k++)
        if (__atomic_load_n(&sl->drained[k], __ATOMIC_RELAXED) == gen + 1) return -1;
    return 1;
}

static int pl_stale(const void *ctx, const void *sess) {
    (void)ctx;
    struct pl_sess *s = (struct pl_sess *)sess;
    int st = pl_gen_stale(s->node, s->slot, s->gen);
    if (st < 0) s->slot = -1;
    return st > 0;
}

int pool_ref_stale(const struct xc_ref *r) { return pl_gen_stale(r->node, r->slot, r->gen) > 0; }

static void pl_ref_seen(const struct xc_ref *r, int rc, uint64_t started) { pl_seen(r->slot, r->gen, rc, started); }

int pool_reach_some(const struct pool_proto *p, const void *const *nodes, size_t n, int timeout_ms, char *ok, int *err) {
    if (!p->reach) return -1;
    if (!p->kind) return p->reach(nodes, n, timeout_ms, ok, err);
    size_t c = n ? n : 1;
    const void **sub = malloc(c * sizeof(*sub));
    size_t *at = malloc(c * sizeof(*at));
    char *sok = malloc(c);
    int *serr = malloc(c * sizeof(*serr));
    int rc = -1;
    if (sub && at && sok && serr) {
        size_t m = 0;
        for (size_t k = 0; k < n; k++) {
            ok[k] = 1;
            err[k] = 0;
            if (p->kind(nodes[k])) continue;
            sub[m] = nodes[k];
            at[m++] = k;
        }
        rc = m ? p->reach(sub, m, timeout_ms, sok, serr) : 0;
        for (size_t j = 0; rc >= 0 && j < m; j++) {
            ok[at[j]] = sok[j];
            err[at[j]] = serr[j];
        }
        if (rc >= 0) rc += (int)(n - m);
    }
    free(sub);
    free(at);
    free(sok);
    free(serr);
    return rc;
}

static int pl_mux_attach(const void *ctx, void *sess, uint16_t owner, const struct xc_ref *ref) {
    (void)ctx;
    (void)ref;
    struct pl_sess *s = sess;
    const struct dialer_mux *m = g_pl.in[PL_K(s)]->mux;
    if (!m) return XA_NONE;
    if (PL_K(s) && pl_slot_dead(s->slot)) return TR_ENONODE;
    struct xc_ref r = { s->node, s->slot, s->gen };
    return m->attach(s->node, INNER(s), owner, &r);
}

static int pl_mux_dial(const void *ctx, void *link, int timeout_s) {
    (void)ctx;
    struct xc_ref r = ((const struct xconn *)link)->ref;
    if (pl_slot_dead(r.slot)) return TR_ENONODE;
    uint64_t started = pl_now_ms();
    int rc = g_pl.in[0]->mux->dial(r.node, link, timeout_s);
    pl_seen(r.slot, r.gen, rc, started);
    return rc;
}

static int pl_mux_cut(const void *sess) {
    const struct pl_sess *s = sess;
    return g_pl.in[PL_K(s)]->mux->cut(CINNER(s));
}

static void pl_mux_host(const struct mux_host *h) {
    const struct dialer_mux *a = g_pl.in[0]->mux;
    if (a) a->host(h);
    struct mux_host c = *h;
    c.submit = NULL;
    c.idx_base = XC_MAX;
    c.seen = pl_ref_seen;
    c.stale = pool_ref_stale;
    g_pl.in[1]->mux->host(&c);
}

static int pl_mux_event(unsigned idx, uint32_t ev) {
    const struct dialer_mux *a = g_pl.in[0]->mux;
    if (idx >= XC_MAX) return g_pl.in[1]->mux->event(idx - XC_MAX, ev);
    return a ? a->event(idx, ev) : 0;
}

static int pl_mux_pass(uint64_t now_ms, int phase) {
    const struct dialer_mux *a = g_pl.in[0]->mux;
    int x = a ? a->pass(now_ms, phase) : 0;
    int y = g_pl.in[1]->mux->pass(now_ms, phase);
    if (x > 0 && (y <= 0 || x < y)) return x;
    return y > 0 ? y : 0;
}

static int pl_mux_busy(void) {
    const struct dialer_mux *a = g_pl.in[0]->mux, *b = g_pl.in[1]->mux;
    return (a && a->busy ? a->busy() : 0) + (b->busy ? b->busy() : 0);
}

static void pl_mux_stop(void) {
    const struct dialer_mux *a = g_pl.in[0]->mux, *b = g_pl.in[1]->mux;
    if (a && a->stop) a->stop();
    if (b->stop) b->stop();
}

static struct dialer_mux g_pl_mux;

static void pl_lost(const void *ctx, const void *sess) {
    (void)ctx;
    const struct pl_sess *s = sess;
    if (s->slot < 0) return;
    pthread_mutex_lock(&g_pl.mu);
    struct pl_slot *sl = &g_pl.slot[s->slot];
    if (sl->gen == s->gen && sl->up && pl_now_ms() - sl->checked_at >= PL_LOST_GAP_MS) {
        sl->lost = 1;
        pl_kick(s->slot);
    }
    pthread_mutex_unlock(&g_pl.mu);
}

static struct dialer_ops g_pl_ops;

/* ---- start ------------------------------------------------------------------------------ */

struct pl_ready { stack_ready_fn ready; void *arg; };

/* The device is up: health checks start. A node named on the command line was not probed at
 * startup, so its first check runs at once. */
static void pl_ready_cb(void *arg, const char *dev) {
    const struct pl_ready *r = arg;
    pl_publish();
    pthread_attr_t a;
    pthread_attr_init(&a);
    tstack_set(&a, 256 * 1024);
    pthread_attr_setdetachstate(&a, PTHREAD_CREATE_DETACHED);
    pthread_t t;
    int err = pthread_create(&t, &a, pl_thread, NULL);
    pthread_attr_destroy(&a);
    if (err)
        fprintf(stderr, PL_LOG_W "no health check thread (%s) — a dead node will not be noticed\n",
                strerror(err));
    if (r->ready) r->ready(r->arg, dev);
}

static struct pl_sel *pl_sel_new(const int *c, size_t n) {
    struct pl_sel *s = malloc(sizeof *s + n * sizeof s->c[0]);
    if (!s) return NULL;
    s->old = NULL;
    s->n = n;
    if (n) memcpy(s->c, c, n * sizeof s->c[0]);
    return s;
}

/* Slots and the pool's dialer for configuration pc. Separate from pool_run
 * for the tests, which set the pool up without a device. */
static const struct dialer *pool_setup(const struct pool_cfg *pc) {
    const struct dialer_ops *in0 = pc->proto->ops, *in1 = pc->proto->alt;
    if (!in0->has_data || (in1 && !in1->has_data)) {
        fprintf(stderr, PL_LOG_E "dialer %s has no has_data\n", !in0->has_data ? in0->name : in1->name);
        return NULL;
    }
    /* CLOCK_MONOTONIC: deadlines must not move with the wall clock (NTP steps it after boot). */
    pthread_condattr_t ca;
    pthread_condattr_init(&ca);
    pthread_condattr_setclock(&ca, CLOCK_MONOTONIC);
    pthread_cond_init(&g_pl.cv, &ca);
    pthread_condattr_destroy(&ca);
    g_pl.cf = *pc;
    g_pl.in[0] = in0;
    g_pl.in[1] = in1;
    g_pl.kind = in1 ? pc->proto->kind : NULL;
    if (g_pl.cf.interval_s <= 0) g_pl.cf.interval_s = POOL_INTERVAL_S;
    if (g_pl.cf.active < 1) g_pl.cf.active = 1;
    g_pl.n = g_pl.cf.active;
    if ((size_t)g_pl.n > pc->sel_n) {
        fprintf(stderr, PL_LOG_W "%d active nodes asked, %zu candidates — all of them are active\n",
                g_pl.cf.active, pc->sel_n);
        g_pl.n = (int)pc->sel_n;
    }
    if (g_pl.n < 1) g_pl.n = 1;
    struct pl_sel *sel = pl_sel_new(pc->sel, pc->sel_n);
    g_pl.slot = calloc((size_t)g_pl.cf.active, sizeof *g_pl.slot);
    if (!g_pl.slot || !sel) {
        free(sel);
        fprintf(stderr, PL_LOG_E "out of memory for node slots\n");
        return NULL;
    }
    sel->old = g_pl.sel;
    g_pl.sel = sel;
    uint64_t now = pl_now_ms();
    for (int i = 0; i < g_pl.cf.active; i++) {
        g_pl.slot[i].node = -1;
        g_pl.slot[i].retry = PL_RETRY_S;
        g_pl.slot[i].due = now;            /* an empty slot searches at once */
    }
    g_pl.slot[0].node = pc->first;
    g_pl.slot[0].up = 1;
    g_pl.slot[0].checked_at = now;
    g_pl.slot[0].due = pc->checked ? now + (uint64_t)g_pl.cf.interval_s * 1000ull : now;

    g_pl_ops = *in0;
    g_pl_ops.sess_size = PL_HDR + (in1 && in1->sess_size > in0->sess_size ? in1->sess_size : in0->sess_size);
    g_pl_ops.peer = pl_peer;
    g_pl_ops.describe = pl_describe;
    g_pl_ops.strerror = pl_strerror;
    g_pl_ops.why = in0->why || in1 ? pl_why : NULL;
    g_pl_ops.connect = pl_connect;
    g_pl_ops.take = pl_take;
    g_pl_ops.close = pl_close;
    g_pl_ops.clear = pl_clear;
    g_pl_ops.fd = pl_fd;
    g_pl_ops.has_data = pl_has_data;
    g_pl_ops.flow_open = pl_flow_open;
    g_pl_ops.send = pl_send;
    g_pl_ops.room = pl_room;
    g_pl_ops.finish = in0->finish || (in1 && in1->finish) ? pl_finish : NULL;
    g_pl_ops.due = in0->due || (in1 && in1->due) ? pl_due : NULL;
    g_pl_ops.up_write = in0->up_write || (in1 && in1->up_write) ? pl_up_write : NULL;
    g_pl_ops.dgram_frame = pl_dgram_frame;
    g_pl_ops.read = pl_read;
    g_pl_ops.deliver = pl_deliver;
    g_pl_ops.peer_of = pl_peer_of;
    g_pl_ops.match = pl_match;
    g_pl_ops.stale = pl_stale;
    g_pl_ops.lost = pl_lost;
    g_pl_ops.set_host = in0->set_host || (in1 && in1->set_host) ? pl_set_host : NULL;
    g_pl_ops.flow_open_on = pool_flow_open_on;
    if (in1 && in1->mux) {
        g_pl_mux = (struct dialer_mux){
            .host = pl_mux_host, .attach = pl_mux_attach, .event = pl_mux_event, .pass = pl_mux_pass,
            .cut = pl_mux_cut, .busy = pl_mux_busy, .shares = pl_mux_shares, .stop = pl_mux_stop,
        };
        if (in0->mux) {
            g_pl_mux.dial = pl_mux_dial;
            g_pl_mux.dropped = in0->mux->dropped;
        }
    } else if (in0->mux) {
        g_pl_mux = *in0->mux;
        g_pl_mux.attach = pl_mux_attach;
        g_pl_mux.dial = pl_mux_dial;
        g_pl_mux.cut = pl_mux_cut;
    }
    g_pl_ops.mux = in0->mux || (in1 && in1->mux) ? &g_pl_mux : NULL;

    /* The stack keeps the pointer until the process ends. */
    static struct dialer d;
    d.ops = &g_pl_ops;
    d.ctx = &g_pl;
    d.silence_s = pc->silence_s;
    return &d;
}

int pool_run(const struct tun_cfg *tc, const struct pool_cfg *pc, stack_ready_fn ready, void *arg) {
    const struct dialer *d = pool_setup(pc);
    if (!d) return 1;
    static struct pl_ready r;
    r.ready = ready;
    r.arg = arg;
    return stack_run(tc, d, pl_ready_cb, &r);
}

int pool_select(const int *c, size_t n) {
    struct pl_sel *sel = pl_sel_new(c, n);
    if (!sel) return -1;
    const char *gone[64];
    int ng = 0, drained = 0;
    uint64_t now = pl_now_ms();
    pthread_mutex_lock(&g_pl.mu);
    sel->old = g_pl.sel;
    g_pl.sel = sel;
    for (size_t k = 0; k < n; k++)
        if (!sel->old || !pl_in(sel->old, c[k])) pl_note(c[k], 1, now);
    int want = (size_t)g_pl.cf.active < n ? g_pl.cf.active : (int)n;
    if (want > g_pl.n) g_pl.n = want;
    for (int i = 0; i < g_pl.n; i++) {
        struct pl_slot *sl = &g_pl.slot[i];
        if (sl->node >= 0 && !pl_in(sel, sl->node)) {
            if (ng < 64) gone[ng++] = g_pl.cf.proto->name(pl_node(sl->node));
            __atomic_store_n(&sl->drained[sl->drained_n++ % PL_DRAINED], sl->gen + 1, __ATOMIC_RELAXED);
            sl->node = -1;
            sl->up = sl->v6 = 0;
            sl->fails = sl->streak = sl->kick = sl->lost = 0;
            __atomic_add_fetch(&sl->gen, 1, __ATOMIC_RELEASE);
            drained++;
        }
        if (sl->node < 0 || !sl->up) {
            sl->retry = PL_RETRY_S;
            sl->due = sl->rot = now;
            sl->woke++;
        }
    }
    if (drained) g_pl.pub = 1;
    pthread_cond_signal(&g_pl.cv);
    pthread_mutex_unlock(&g_pl.mu);
    for (int k = 0; k < ng; k++)
        fprintf(stderr, PL_LOG_I "node %s left the list — its connections stay on it until they end\n",
                gone[k]);
    if (drained) stack_nodes_changed();
    return drained;
}

int pool_wake(const int *idx, size_t n) {
    pthread_mutex_lock(&g_pl.mu);
    uint64_t now = pl_now_ms();
    for (size_t k = 0; k < n; k++) pl_note(idx[k], 1, now);
    for (int i = 0; i < g_pl.n; i++) {
        struct pl_slot *sl = &g_pl.slot[i];
        if (sl->node >= 0 && sl->up) continue;
        sl->retry = PL_RETRY_S;
        sl->due = sl->rot = now;
        sl->woke++;
    }
    pthread_cond_signal(&g_pl.cv);
    pthread_mutex_unlock(&g_pl.mu);
    return 0;
}
