#define _GNU_SOURCE
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>
#include <sys/epoll.h>

#include "hy2dial.h"
#include "hy2conn.h"
#include "hy2node.h"
#include "hy2link.h"
#include "hy2loss.h"
#include "transport.h"

#define DIAL_MS 8000
#define NEXT_MAX 64
#define NO_LINK 0xff
#define TAB_MIN 16
#define RXC_SIZE 16384
#define FREE_MAX 16
#define OPEN_ROOM 65536
#define UDP_QMAX 128
#define PRE_MAX (4 * RXC_CAP)
#define SAID_MAX 64
#define STATS_NS 1000000000ull
#define LOSS_SAY_MS 600000u

enum { S_NONE, S_OPEN, S_WAIT, S_LIVE, S_FAIL };
enum { E_NONE, E_FIN, E_RESET, E_LINK };
enum { L_FREE, L_DIAL, L_READY, L_DEAD };

struct tslot { uint64_t key; struct hy2_sess *s; };

struct hy2_rxc {
    struct hy2_rxc *next;
    uint32_t n, off;
    uint8_t d[];
};

#define RXC_CAP (RXC_SIZE - sizeof(struct hy2_rxc))

struct link {
    struct hy2c *c;
    const void *node;
    struct xc_ref ref;
    uint64_t started, rx_asked, st_at;
    struct qc_stats st;
    struct hy2loss loss;
    uint64_t wait[3];
    unsigned ign, unparroted;
    uint16_t port;
    struct hy2_sess *head, *tail;
    struct tslot *tab;
    size_t cap, n;
    unsigned gen, flows, udp_n, pre_n;
    int fdx;
    uint8_t state, udp_ok, stale, hop_said;
};

struct thr {
    struct mux_host h;
    uint64_t now;
    struct link l[MUX_HY2_LINKS];
    struct { const void *node; unsigned i; } next[NEXT_MAX];
    unsigned next_rr;
    struct hy2_rxc *free;
    unsigned free_n;
#ifdef QC_WITH_SERVER
    unsigned long udp_in;
#endif
};

static __thread struct thr *g_t;

static struct { const void *node; uint64_t h; int kind; } g_said[SAID_MAX];
static unsigned g_said_rr;
static struct { const void *node; uint64_t next; } g_loss_said[SAID_MAX];
static unsigned g_loss_rr;
static pthread_mutex_t g_said_mu = PTHREAD_MUTEX_INITIALIZER;

static uint64_t tkey(int64_t sid) { return ((uint64_t)sid >> 2) << 1; }
static uint64_t ukey(uint32_t id) { return (uint64_t)id << 1 | 1u; }

static struct hy2_sess *t_get(const struct link *l, uint64_t key) {
    if (!l->cap) return NULL;
    size_t m = l->cap - 1;
    for (size_t i = (size_t)key & m;; i = (i + 1) & m) {
        if (!l->tab[i].s) return NULL;
        if (l->tab[i].key == key) return l->tab[i].s;
    }
}

static void t_place(struct tslot *tab, size_t cap, uint64_t key, struct hy2_sess *s) {
    size_t m = cap - 1, i = (size_t)key & m;
    while (tab[i].s) i = (i + 1) & m;
    tab[i].key = key;
    tab[i].s = s;
}

static int t_put(struct link *l, uint64_t key, struct hy2_sess *s) {
    if ((l->n + 1) * 2 > l->cap) {
        size_t cap = l->cap ? l->cap * 2 : TAB_MIN;
        struct tslot *tab = calloc(cap, sizeof *tab);
        if (!tab) return -1;
        for (size_t i = 0; i < l->cap; i++)
            if (l->tab[i].s) t_place(tab, cap, l->tab[i].key, l->tab[i].s);
        free(l->tab);
        l->tab = tab;
        l->cap = cap;
    }
    t_place(l->tab, l->cap, key, s);
    l->n++;
    return 0;
}

static void t_del(struct link *l, uint64_t key) {
    if (!l->cap) return;
    size_t m = l->cap - 1, i = (size_t)key & m;
    for (;; i = (i + 1) & m) {
        if (!l->tab[i].s) return;
        if (l->tab[i].key == key) break;
    }
    for (size_t j = i;;) {
        j = (j + 1) & m;
        if (!l->tab[j].s) break;
        size_t h = (size_t)l->tab[j].key & m;
        int stays = i <= j ? (h > i && h <= j) : (h > i || h <= j);
        if (stays) continue;
        l->tab[i] = l->tab[j];
        i = j;
    }
    l->tab[i].s = NULL;
    l->tab[i].key = 0;
    l->n--;
}

static struct link *link_of(const struct hy2_sess *s) {
    if (!g_t || s->li >= MUX_HY2_LINKS) return NULL;
    struct link *l = &g_t->l[s->li];
    return l->state != L_FREE && l->gen == s->lgen ? l : NULL;
}

static struct link *live_of(const struct hy2_sess *s) {
    struct link *l = link_of(s);
    return l && l->state == L_READY ? l : NULL;
}

static void wake(const struct hy2_sess *s, unsigned what, int rc) {
    if (g_t && g_t->h.wake) g_t->h.wake(g_t->h.arg, s->owner, what, rc);
}

static void list_add(struct link *l, struct hy2_sess *s) {
    s->next = NULL;
    s->prev = l->tail;
    if (l->tail) l->tail->next = s;
    else l->head = s;
    l->tail = s;
    l->flows++;
}

static void list_del(struct link *l, struct hy2_sess *s) {
    if (s->prev) s->prev->next = s->next;
    else l->head = s->next;
    if (s->next) s->next->prev = s->prev;
    else l->tail = s->prev;
    s->prev = s->next = NULL;
    l->flows--;
}

static unsigned next_get(const void *node) {
    for (unsigned i = 0; i < NEXT_MAX; i++)
        if (g_t->next[i].node == node) return g_t->next[i].i;
    return 0;
}

static void next_bump(const void *node) {
    for (unsigned i = 0; i < NEXT_MAX; i++)
        if (g_t->next[i].node == node) {
            g_t->next[i].i++;
            return;
        }
    unsigned k = g_t->next_rr++ % NEXT_MAX;
    g_t->next[k].node = node;
    g_t->next[k].i = 1;
}

static struct hy2_rxc *rxc_get(void) {
    struct hy2_rxc *c = g_t ? g_t->free : NULL;
    if (c) {
        g_t->free = c->next;
        g_t->free_n--;
    } else if (!(c = malloc(RXC_SIZE))) {
        return NULL;
    }
    c->next = NULL;
    c->n = c->off = 0;
    return c;
}

static void rxc_put(struct hy2_rxc *c) {
    if (g_t && g_t->free_n < FREE_MAX) {
        c->next = g_t->free;
        g_t->free = c;
        g_t->free_n++;
        return;
    }
    free(c);
}

static void q_drop(struct hy2_sess *s) {
    while (s->head) {
        struct hy2_rxc *c = s->head;
        s->head = c->next;
        rxc_put(c);
    }
    s->tail = NULL;
    s->bytes = 0;
    s->dg_n = 0;
}

static void pre_drop(struct link *l, struct hy2_sess *s) {
    if (!s->pre) return;
    while (s->pre) {
        struct hy2_rxc *c = s->pre;
        s->pre = c->next;
        rxc_put(c);
    }
    if (l) l->pre_n--;
}

static int q_put(struct hy2_sess *s, const uint8_t *d, size_t n) {
    while (n) {
        struct hy2_rxc *t = s->tail;
        if (!t || t->n == RXC_CAP) {
            struct hy2_rxc *c = rxc_get();
            if (!c) return -1;
            if (t) t->next = c;
            else s->head = c;
            s->tail = t = c;
        }
        size_t k = RXC_CAP - t->n < n ? RXC_CAP - t->n : n;
        memcpy(t->d + t->n, d, k);
        t->n += (uint32_t)k;
        s->bytes += (uint32_t)k;
        d += k;
        n -= k;
    }
    return 0;
}

static int q_put_dg(struct hy2_sess *s, const uint8_t *d, size_t n) {
    if (2 + n > RXC_CAP) return -1;
    struct hy2_rxc *t = s->tail;
    if (!t || RXC_CAP - t->n < 2 + n) {
        struct hy2_rxc *c = rxc_get();
        if (!c) return -1;
        if (t) t->next = c;
        else s->head = c;
        s->tail = t = c;
    }
    uint16_t len = (uint16_t)n;
    memcpy(t->d + t->n, &len, 2);
    memcpy(t->d + t->n + 2, d, n);
    t->n += (uint32_t)(2 + n);
    s->bytes += (uint32_t)(2 + n);
    s->dg_n++;
    return 0;
}

static void q_take(struct hy2_sess *s, size_t n) {
    struct hy2_rxc *h = s->head;
    h->off += (uint32_t)n;
    s->bytes -= (uint32_t)n;
    if (h->off < h->n) return;
    s->head = h->next;
    if (!s->head) s->tail = NULL;
    rxc_put(h);
}

static void why_set(struct hy2_sess *s, int rc, const char *why) {
    s->why_rc = rc;
    snprintf(s->why, sizeof s->why, "%s", why);
}

static void dial_fail(struct link *l, const char *why) {
    while (l->head) {
        struct hy2_sess *s = l->head;
        list_del(l, s);
        s->st = S_FAIL;
        s->li = NO_LINK;
        why_set(s, HY2_EDIAL, why && why[0] ? why : "the node did not answer");
        wake(s, XW_DONE, HY2_EDIAL);
    }
    if (g_t->h.seen) g_t->h.seen(&l->ref, HY2_EDIAL, l->started);
    next_bump(l->node);
    l->state = L_DEAD;
}

static int udp_open(struct link *l, struct hy2_sess *s) {
    const char *why = !l->udp_ok ? "the node takes no UDP" : l->udp_n >= MAX_CONNS ? "UDP sessions full" : NULL;
    uint32_t id = why ? 0 : hy2c_udp_session(l->c);
    if (!why && (!id || t_put(l, ukey(id), s) != 0)) {
        if (id) hy2c_udp_end(l->c, id);
        why = "out of memory";
    }
    if (why) {
        why_set(s, HY2_ENOUDP, why);
        return HY2_ENOUDP;
    }
    s->usid = id;
    l->udp_n++;
    return 0;
}

static const char *clean(const char *s, char *out, size_t n) {
    size_t o = 0;
    for (; *s && o + 1 < n; s++) {
        unsigned char c = (unsigned char)*s;
        out[o++] = c < 0x20 || c == 0x7f ? ' ' : (char)c;
    }
    out[o] = 0;
    return out;
}

static void put(char *b, size_t cap, size_t *k, const char *fmt, ...) {
    if (*k >= cap) return;
    va_list ap;
    va_start(ap, fmt);
    int w = vsnprintf(b + *k, cap - *k, fmt, ap);
    va_end(ap);
    *k = w < 0 ? cap : *k + (size_t)w;
}

static int said_new(const void *node, int kind, const char *line, size_t cut_from, size_t cut_to) {
    uint64_t h = 14695981039346656037ull;
    size_t len = strlen(line);
    for (size_t i = 0; i < len; i++) {
        if (i == cut_from) i = cut_to;
        if (i < len) h = (h ^ (unsigned char)line[i]) * 1099511628211ull;
    }
    int fresh = 1;
    pthread_mutex_lock(&g_said_mu);
    unsigned i = 0;
    while (i < SAID_MAX && (g_said[i].node != node || g_said[i].kind != kind)) i++;
    if (i == SAID_MAX) i = g_said_rr++ % SAID_MAX;
    else fresh = g_said[i].h != h;
    g_said[i].node = node;
    g_said[i].kind = kind;
    g_said[i].h = h;
    pthread_mutex_unlock(&g_said_mu);
    return fresh;
}

static int loss_due(const void *node, uint64_t now) {
    pthread_mutex_lock(&g_said_mu);
    unsigned i = 0;
    while (i < SAID_MAX && g_loss_said[i].node != node) i++;
    if (i == SAID_MAX) {
        i = g_loss_rr++ % SAID_MAX;
        g_loss_said[i].node = node;
        g_loss_said[i].next = 0;
    }
    int due = now >= g_loss_said[i].next;
    if (due) g_loss_said[i].next = now + LOSS_SAY_MS;
    pthread_mutex_unlock(&g_said_mu);
    return due;
}

static void loss_say(const struct link *l, uint64_t now) {
    const struct vless_node *n = l->node;
    if (!loss_due(n, now)) return;
    char nm[sizeof n->name];
    fprintf(stderr, "tunvless[info]: hysteria2 %s: lossy path on port %u: %llu of %llu packets lost\n",
            clean(n->name, nm, sizeof nm), (unsigned)hy2c_sock_port(l->c), (unsigned long long)l->loss.run_lost,
            (unsigned long long)l->loss.run_sent);
}

static void ready_say(const struct link *l) {
    static const char *const keys[] = { "bbrProfile", "debug", "initStreamReceiveWindow", "maxStreamReceiveWindow",
                                        "initConnectionReceiveWindow", "maxConnectionReceiveWindow", "maxIdleTimeout",
                                        "disableGSO", "maxIncomingStreams", "disableStatelessReset" };
    const struct vless_node *n = l->node;
    char nm[sizeof n->name], b[640];
    size_t k = 0;
    const char *sep = ", not applied: ";
    uint64_t tx;
    int cc = hy2c_cc(l->c, &tx);
    put(b, sizeof b, &k, "hysteria2 %s: link ready", clean(n->name, nm, sizeof nm));
    size_t cut_from = k;
    put(b, sizeof b, &k, " on port %u", (unsigned)l->port);
    size_t cut_to = k;
    put(b, sizeof b, &k, ", up ");
    if (cc == QC_CC_BRUTAL) put(b, sizeof b, &k, "brutal %llu B/s", (unsigned long long)tx);
    else put(b, sizeof b, &k, "%s", cc == QC_CC_RENO ? "reno" : "bbr");
    if (l->rx_asked) put(b, sizeof b, &k, ", down asked %llu B/s", (unsigned long long)l->rx_asked);
    else put(b, sizeof b, &k, ", down asked nothing");
    unsigned m = (l->ign & 3u) | l->unparroted << 2 | (l->ign >> 3) << 7;
    for (unsigned i = 0; i < sizeof keys / sizeof keys[0]; i++)
        if (m & 1u << i) {
            put(b, sizeof b, &k, "%s%s", sep, keys[i]);
            sep = ", ";
        }
    if (n->hop_mode & HY2_HOP_IPS) put(b, sizeof b, &k, "%sremoteIPs", sep);
    if (said_new(n, 0, b, cut_from, cut_to)) fprintf(stderr, "tunvless[info]: %s\n", b);
}

static void stats_take(struct link *l, uint64_t t) {
    l->st_at = t;
    hy2c_stats(l->c, &l->st);
    hy2c_wait(l->c, t, l->wait);
}

static void stats_say(struct link *l, uint64_t now) {
    uint64_t t = hy2c_clock(l->c, now * 1000000u);
    if (t < l->st_at + STATS_NS) return;
    struct qc_stats p = l->st;
    uint64_t span = t - l->st_at, w[3];
    memcpy(w, l->wait, sizeof w);
    stats_take(l, t);
    const struct qc_stats *s = &l->st;
    if (hy2loss_step(&l->loss, s->cc_sent - p.cc_sent, s->cc_lost - p.cc_lost, s->hops != p.hops, span / 1000000u) ==
        HY2LOSS_LOSSY)
        loss_say(l, now);
    if (s->pay_tx == p.pay_tx && s->pay_rx == p.pay_rx) return;
    const struct vless_node *n = l->node;
    char nm[sizeof n->name], b[sizeof nm + 448];
    size_t k = 0;
    put(b, sizeof b, &k, "hysteria2 %s: stats span %llu port %u %s cwnd %llu flight %llu srtt %llu min %llu",
        clean(n->name, nm, sizeof nm), (unsigned long long)(span / 1000000u), (unsigned)s->port, s->cc == QC_CC_BRUTAL ? "brutal" : s->cc == QC_CC_RENO ? "reno" : "bbr",
        (unsigned long long)s->cwnd, (unsigned long long)s->in_flight, (unsigned long long)(s->rtt_us / 1000),
        (unsigned long long)(s->min_rtt_us / 1000));
    put(b, sizeof b, &k, " sent %llu lost %llu/%llu acked %llu rx %llu", (unsigned long long)(s->bytes_sent - p.bytes_sent),
        (unsigned long long)(s->cc_lost - p.cc_lost), (unsigned long long)(s->cc_sent - p.cc_sent),
        (unsigned long long)(s->bytes_acked - p.bytes_acked), (unsigned long long)(s->bytes_recv - p.bytes_recv));
    put(b, sizeof b, &k, " wait cwnd %llu credit %llu budget %llu",
        (unsigned long long)((l->wait[QC_WAIT_CWND] - w[QC_WAIT_CWND]) / 1000000u),
        (unsigned long long)((l->wait[QC_WAIT_CREDIT] - w[QC_WAIT_CREDIT]) / 1000000u),
        (unsigned long long)((l->wait[QC_WAIT_BUDGET] - w[QC_WAIT_BUDGET]) / 1000000u));
    if (s->bbr_state)
        put(b, sizeof b, &k, " bbr %s bw %llu pace %llu", s->bbr_state, (unsigned long long)s->bbr_bw,
            (unsigned long long)s->pacing_bps);
    fprintf(stderr, "tunvless[debug]: %s\n", b);
}

static void c_ready(void *u, uint64_t rx, int udp) {
    (void)rx;
    struct link *l = u;
    ready_say(l);
    stats_take(l, hy2c_clock(l->c, g_t->now * 1000000u));
    l->state = L_READY;
    l->udp_ok = (uint8_t)udp;
    for (struct hy2_sess *s = l->head, *nx; s; s = nx) {
        nx = s->next;
        if (s->st != S_WAIT) continue;
        if (s->udp && udp_open(l, s) != 0) {
            list_del(l, s);
            s->st = S_FAIL;
            s->li = NO_LINK;
            wake(s, XW_DONE, HY2_ENOUDP);
            continue;
        }
        s->st = S_LIVE;
        wake(s, XW_DONE, 0);
    }
    if (g_t->h.seen) g_t->h.seen(&l->ref, 0, l->started);
}

static void end_set(struct hy2_sess *s, int how) {
    if (s->fin_seen || s->how) return;
    s->how = (uint8_t)how;
    if (how != E_FIN) q_drop(s);
    wake(s, XW_READ, 0);
}

static struct hy2_sess *stream_gone(struct link *l, int64_t sid) {
    struct hy2_sess *s = t_get(l, tkey(sid));
    if (!s) return NULL;
    t_del(l, tkey(sid));
    s->gone = 1;
    return s;
}

static void c_data(void *u, int64_t sid, const uint8_t *d, size_t n, int fin) {
    struct link *l = u;
    struct hy2_sess *s = t_get(l, tkey(sid));
    if (!s || s->how) return;
    int was = s->bytes != 0;
    if (n && q_put(s, d, n) != 0) {
        hy2c_tcp_close(l->c, sid);
        stream_gone(l, sid);
        end_set(s, E_RESET);
        return;
    }
    if (fin) {
        s->how = E_FIN;
        s->fin_seen = 1;
    }
    if (!was || fin) wake(s, XW_READ, 0);
}

static void c_writable(void *u, int64_t sid) {
    struct hy2_sess *s = t_get(u, tkey(sid));
    if (s) wake(s, XW_ROOM, 0);
}

static void c_streams(void *u) {
    struct link *l = u;
    for (struct hy2_sess *s = l->head; s; s = s->next) {
        if (!s->want_stream) continue;
        s->want_stream = 0;
        wake(s, XW_ROOM, 0);
    }
}

static void c_refused(void *u, int64_t sid, const char *msg) {
    (void)msg;
    struct hy2_sess *s = stream_gone(u, sid);
    if (s) end_set(s, E_RESET);
}

static void c_sclose(void *u, int64_t sid, int how) {
    struct hy2_sess *s = stream_gone(u, sid);
    if (!s) return;
    if (how == HY2C_END_CLEAN) {
        s->up_stopped = 1;
        s->gone = 2;
    }
    end_set(s, how == HY2C_END_CLEAN ? E_FIN : how == HY2C_END_LINK ? E_LINK : E_RESET);
}

static void c_udp(void *u, uint32_t id, const char *addr, size_t an, const uint8_t *d, size_t n) {
    (void)addr;
    (void)an;
    struct hy2_sess *s = t_get(u, ukey(id));
#ifdef QC_WITH_SERVER
    g_t->udp_in++;
#endif
    if (!s || s->how || !n || n > UDP_DGRAM_MAX || s->dg_n >= UDP_QMAX) return;
    int was = s->bytes != 0;
    if (q_put_dg(s, d, n) != 0) return;
    if (!was) wake(s, XW_READ, 0);
}

static void c_closed(void *u, int reason, const char *why) {
    struct link *l = u;
    if (l->state == L_DIAL) {
        dial_fail(l, why);
        return;
    }
    for (struct hy2_sess *s = l->head; s; s = s->next) {
        if (s->pre && s->how == E_FIN && !s->up_stopped) s->how = s->fin_seen = 0;
        if (s->pre && s->up_stopped) {
            pre_drop(l, s);
            wake(s, XW_READ, 0);
        }
        end_set(s, E_LINK);
    }
    if (reason == QC_CLOSE_ERROR) {
        const struct vless_node *n = l->node;
        char nm[sizeof n->name], w[160];
        fprintf(stderr, "tunvless[warn]: hysteria2 %s: link cut: %s\n", clean(n->name, nm, sizeof nm), clean(why ? why : "", w, sizeof w));
        if (g_t->h.seen) g_t->h.seen(&l->ref, HY2_EDIAL, g_t->now);
    }
    l->state = L_DEAD;
}

static const struct hy2c_ops g_cops = {
    .on_ready = c_ready,
    .on_stream_data = c_data,
    .on_stream_writable = c_writable,
    .on_streams_open = c_streams,
    .on_stream_refused = c_refused,
    .on_stream_close = c_sclose,
    .on_udp = c_udp,
    .on_closed = c_closed,
};

static void hop_say(struct link *l) {
    if (!hy2c_hop_fails(l->c)) return;
    l->hop_said = 1;
    struct qc_stats st;
    hy2c_stats(l->c, &st);
    const struct vless_node *n = l->node;
    char nm[sizeof n->name], b[sizeof nm + 64];
    size_t k = 0;
    put(b, sizeof b, &k, "hysteria2 %s: port hop failed, staying on", clean(n->name, nm, sizeof nm));
    size_t cut_from = k;
    put(b, sizeof b, &k, " %u", (unsigned)st.port);
    if (said_new(n, 1, b, cut_from, k)) fprintf(stderr, "tunvless[warn]: %s\n", b);
}

static void link_free(unsigned i) {
    struct link *l = &g_t->l[i];
    if (l->c) {
        if (g_t->h.watch) g_t->h.watch(g_t->h.idx_base + i, hy2c_fd(l->c), 0);
        if (l->fdx && g_t->h.fd_extra) g_t->h.fd_extra(-l->fdx);
        hy2c_close(l->c);
        hy2c_free(l->c);
    }
    while (l->head) {
        struct hy2_sess *s = l->head;
        list_del(l, s);
        s->li = NO_LINK;
    }
    free(l->tab);
    unsigned gen = l->gen + 1;
    memset(l, 0, sizeof *l);
    l->gen = gen;
}

static int is_ip(const char *h) {
    unsigned char b[16];
    return inet_pton(AF_INET, h, b) == 1 || inet_pton(AF_INET6, h, b) == 1;
}

static int link_open(const struct vless_node *n, const struct xc_ref *ref, struct hy2_sess *s) {
    unsigned i = 0;
    while (i < MUX_HY2_LINKS && g_t->l[i].state != L_FREE) i++;
    if (i == MUX_HY2_LINKS) return HY2_ELINKS;
    if (!is_ip(n->host) && transport_pin_count(n->host) <= 0) {
        s->why_rc = HY2_EDIAL;
        snprintf(s->why, sizeof s->why, "no pinned address for %s", n->host);
        return HY2_EDIAL;
    }
    struct hy2_nodecfg cfg;
    int rc = hy2_nodecfg(n, next_get(n), &cfg);
    if (rc <= 0) {
        why_set(s, HY2_EDIAL, rc == HY2_EPIN ? "the certificate pin does not parse" : "the node has no address");
        return HY2_EDIAL;
    }
    cfg.c.udp_sessions = MAX_CONNS;
    struct link *l = &g_t->l[i];
    rc = hy2c_open(&cfg.c, &g_cops, l, &l->c);
    if (rc != 0) {
        l->c = NULL;
        next_bump(n);
        why_set(s, HY2_EDIAL, rc == QC_ESOCK ? "the socket to the node did not open" : "the session did not start");
        return HY2_EDIAL;
    }
    if (g_t->h.watch && g_t->h.watch(g_t->h.idx_base + i, hy2c_fd(l->c), EPOLLIN) != 0) {
        hy2c_close(l->c);
        hy2c_free(l->c);
        l->c = NULL;
        why_set(s, HY2_EDIAL, "the loop did not take the socket");
        return HY2_EDIAL;
    }
    l->fdx = hy2c_fd_extra(l->c);
    if (l->fdx && g_t->h.fd_extra) g_t->h.fd_extra(l->fdx);
    l->node = n;
    l->port = cfg.c.qc.port;
    l->rx_asked = cfg.c.rx_bps;
    l->ign = cfg.qp_ignored;
    l->unparroted = cfg.qp_unparroted;
    l->ref = ref ? *ref : (struct xc_ref){ n, -1, 0 };
    l->started = g_t->now;
    l->state = L_DIAL;
    return (int)i;
}

static int pre_put(struct link *l, struct hy2_sess *s, const unsigned char *d, size_t n) {
    if (!n) return 0;
    struct hy2_rxc *t = s->pre;
    size_t have = 0;
    for (; t; t = t->next) {
        have += t->n - t->off;
        if (!t->next) break;
    }
    if (have + n > PRE_MAX) return -1;
    size_t need = t && RXC_CAP - t->n < n ? n - (RXC_CAP - t->n) : t ? 0 : n;
    struct hy2_rxc *add = NULL, **ap = &add;
    for (; need; need -= need < RXC_CAP ? need : RXC_CAP) {
        struct hy2_rxc *c = rxc_get();
        if (!c) {
            while (add) {
                c = add->next;
                rxc_put(add);
                add = c;
            }
            return -1;
        }
        *ap = c;
        ap = &c->next;
    }
    if (!t) {
        s->pre = t = add;
        l->pre_n++;
    } else {
        t->next = add;
    }
    while (n) {
        if (t->n == RXC_CAP) t = t->next;
        size_t k = RXC_CAP - t->n < n ? RXC_CAP - t->n : n;
        memcpy(t->d + t->n, d, k);
        t->n += (uint32_t)k;
        d += k;
        n -= k;
    }
    return 0;
}

static int stream_open(struct link *l, struct hy2_sess *s) {
    int64_t sid;
    int rc = hy2c_tcp_open(l->c, s->host, s->port, &sid);
    if (rc == QC_EAGAIN) {
        s->want_stream = 1;
        return SEND_AGAIN;
    }
    if (rc != 0) return SEND_FATAL;
    if (t_put(l, tkey(sid), s) != 0) {
        hy2c_tcp_close(l->c, sid);
        return SEND_FATAL;
    }
    s->sid = sid;
    s->want_stream = 0;
    return SEND_OK;
}

static int pre_flush(struct link *l, struct hy2_sess *s) {
    if (s->sid < 0) {
        int sr = stream_open(l, s);
        if (sr != SEND_OK) return sr;
    }
    while (s->pre) {
        struct hy2_rxc *p = s->pre;
        ssize_t w = hy2c_tcp_send(l->c, s->sid, p->d + p->off, p->n - p->off);
        if (w == HY2C_ESTOPPED) {
            s->up_stopped = 1;
            pre_drop(l, s);
            return SEND_OK;
        }
        if (w == QC_EAGAIN) return SEND_AGAIN;
        if (w < 0) return SEND_FATAL;
        p->off += (uint32_t)w;
        if (p->off < p->n) return SEND_AGAIN;
        if (!p->next) break;
        s->pre = p->next;
        rxc_put(p);
    }
    pre_drop(l, s);
    return SEND_OK;
}

static int pre_pass(struct link *l) {
    int moved = 0;
    for (struct hy2_sess *s = l->head, *nx; s && l->pre_n; s = nx) {
        nx = s->next;
        if (!s->pre || (s->how && s->how != E_FIN)) continue;
        if (s->up_stopped) {
            pre_drop(l, s);
            if (s->how) wake(s, XW_READ, 0);
            continue;
        }
        int sr = pre_flush(l, s);
        if (sr == SEND_FATAL) {
            pre_drop(l, s);
            q_drop(s);
            s->how = E_RESET;
            s->fin_seen = 0;
            s->said = 0;
            wake(s, XW_READ, 0);
            continue;
        }
        if (sr == SEND_OK) moved = 1;
        if (sr == SEND_OK && s->how) wake(s, XW_READ, 0);
        if (sr == SEND_AGAIN && s->sid < 0) break;
    }
    return moved;
}

static int stale(struct link *l) {
    if (!l->stale && g_t->h.stale && g_t->h.stale(&l->ref)) l->stale = 1;
    return l->stale;
}

static int m_attach(const void *ctx, void *sess, uint16_t owner, const struct xc_ref *ref) {
    const struct vless_node *n = ctx;
    struct hy2_sess *s = sess;
    if (!g_t) {
        why_set(s, HY2_EDIAL, "the loop has no Hysteria2 state");
        return HY2_EDIAL;
    }
    s->owner = owner;
    int li = -1;
    for (unsigned i = 0; i < MUX_HY2_LINKS && li < 0; i++) {
        struct link *l = &g_t->l[i];
        if (l->node == n && (l->state == L_DIAL || l->state == L_READY) && !stale(l)) li = (int)i;
    }
    if (li < 0) li = link_open(n, ref, s);
    if (li < 0) return li;
    struct link *l = &g_t->l[li];
    if (l->state == L_READY && s->udp && udp_open(l, s) != 0) return HY2_ENOUDP;
    s->li = (uint8_t)li;
    s->lgen = l->gen;
    list_add(l, s);
    if (l->state == L_READY) {
        s->st = S_LIVE;
        return XA_READY;
    }
    s->st = S_WAIT;
    return XA_WAIT;
}

static void m_host(const struct mux_host *h) {
    if (!g_t) g_t = calloc(1, sizeof *g_t);
    if (!g_t) return;
    if (h) g_t->h = *h;
    else memset(&g_t->h, 0, sizeof g_t->h);
}

static int m_event(unsigned i, uint32_t ev) {
    (void)ev;
    if (!g_t || i >= MUX_HY2_LINKS) return 0;
    struct link *l = &g_t->l[i];
    if (l->state == L_DIAL || l->state == L_READY) (void)hy2c_on_readable(l->c);
    return 0;
}

static int due_of(struct link *l, uint64_t now) {
    uint64_t d = hy2c_deadline_ns(l->c), at = now * 1000000u;
    int ms = -1;
    if (d != UINT64_MAX) {
        uint64_t w = d <= at ? 1 : (d - at + 999999u) / 1000000u;
        ms = w > INT32_MAX ? INT32_MAX : (int)w;
    }
    if (l->state == L_DIAL) {
        uint64_t b = l->started + DIAL_MS;
        int left = b <= now ? 1 : (int)(b - now);
        if (ms < 0 || left < ms) ms = left;
    }
    return ms;
}

static int m_pass(uint64_t now, int phase) {
    if (!g_t) return 0;
    g_t->now = now;
    if (phase == MUXP_EXIT) {
        for (unsigned i = 0; i < MUX_HY2_LINKS; i++)
            if (g_t->l[i].state != L_FREE) link_free(i);
        while (g_t->free) {
            struct hy2_rxc *c = g_t->free;
            g_t->free = c->next;
            free(c);
        }
        free(g_t);
        g_t = NULL;
        return 0;
    }
    for (unsigned i = 0; i < MUX_HY2_LINKS; i++) {
        struct link *l = &g_t->l[i];
        if (l->state == L_DIAL && now >= l->started + DIAL_MS) dial_fail(l, "the node did not answer within 8 s");
        if (phase == MUXP_EARLY && (l->state == L_DIAL || l->state == L_READY) &&
            hy2c_deadline_ns(l->c) / 1000000u <= now)
            (void)hy2c_on_timer(l->c);
    }
    if (phase != MUXP_LATE) return 0;
    int due = 0;
    for (unsigned i = 0; i < MUX_HY2_LINKS; i++) {
        struct link *l = &g_t->l[i];
        if (l->state == L_DEAD || (l->state == L_READY && !l->flows && stale(l))) link_free(i);
        if (l->state != L_DIAL && l->state != L_READY) continue;
        if (l->fdx && !l->hop_said) hop_say(l);
        if (l->state == L_READY) stats_say(l, now);
        int moved = l->state == L_READY && l->pre_n ? pre_pass(l) : 0;
        hy2c_flush(l->c);
        int ms = moved ? 1 : due_of(l, now);
        if (ms > 0 && (!due || ms < due)) due = ms;
    }
    return due;
}

static void m_stop(void) {
    if (!g_t) return;
    for (unsigned i = 0; i < MUX_HY2_LINKS; i++)
        if (g_t->l[i].state == L_READY) hy2c_close(g_t->l[i].c);
}

static int m_cut(const void *sess) {
    const struct hy2_sess *s = sess;
    return s->how == E_LINK ? 1 : s->how == E_RESET ? 2 : 0;
}

static int m_busy(void) { return 0; }

static int m_shares(const void *ctx) {
    (void)ctx;
    return 1;
}

static const char *d_peer(const void *ctx) { return ((const struct vless_node *)ctx)->host; }

static void d_describe(const void *ctx, char *out, size_t n) {
    const struct vless_node *node = ctx;
    snprintf(out, n, "%s (%s:%u hysteria2)", node->name, node->host, node->port);
}

static const char *d_strerror(int rc) {
    switch (rc) {
    case HY2_ELINKS: return "no room for another Hysteria2 link on this loop";
    case HY2_EDIAL: return "the Hysteria2 node did not answer";
    case HY2_ENOUDP: return "no UDP session on the node";
    case TR_ENONODE: return "no live node";
    default: return "Hysteria2 flow failed";
    }
}

static const char *d_why(const void *sess, int rc) {
    const struct hy2_sess *s = sess;
    return rc && s->why_rc == rc && s->why[0] ? s->why : d_strerror(rc);
}

static int d_connect(const void *ctx, void *sess, int timeout_s) {
    (void)ctx;
    (void)timeout_s;
    why_set(sess, HY2_EDIAL, "a Hysteria2 flow has no link of its own");
    return HY2_EDIAL;
}

static void d_take(void *dst, void *src) {
    (void)dst;
    (void)src;
}

static void d_close(void *sess) {
    struct hy2_sess *s = sess;
    struct link *l = link_of(s);
    if (l && s->sid >= 0 && !s->gone) {
        if (l->state == L_READY && s->how == E_FIN && !s->up_stopped && !s->pre) (void)hy2c_tcp_finish(l->c, s->sid);
        if (l->state == L_READY) hy2c_tcp_close(l->c, s->sid);
        t_del(l, tkey(s->sid));
    }
    if (l && l->state == L_READY && s->gone == 2 && s->bytes) hy2c_conn_consumed(l->c, s->bytes);
    if (l && s->usid) {
        if (l->state == L_READY) hy2c_udp_end(l->c, s->usid);
        t_del(l, ukey(s->usid));
        l->udp_n--;
    }
    if (l && (s->st == S_WAIT || s->st == S_LIVE)) {
        list_del(l, s);
        if (!l->flows && l->state == L_READY) (void)stale(l);
    }
    pre_drop(l, s);
    q_drop(s);
    s->li = NO_LINK;
    s->st = S_NONE;
}

static void d_clear(void *sess) {
    struct hy2_sess *s = sess;
    memset(s, 0, offsetof(struct hy2_sess, host));
    s->li = NO_LINK;
    s->sid = -1;
    s->host[0] = 0;
}

static int d_fd(const void *sess) {
    (void)sess;
    return -1;
}

static int d_has_data(const void *sess) {
    const struct hy2_sess *s = sess;
    return s->bytes || (s->how && !s->said && !(s->how == E_FIN && s->pre));
}

static int d_flow_open(const void *ctx, void *sess, const struct flow_key *k, int udp) {
    (void)ctx;
    struct hy2_sess *s = sess;
    d_clear(s);
    s->st = S_OPEN;
    s->udp = (uint8_t)(udp != 0);
    s->port = k->dport;
    if (!inet_ntop(k->v6 ? AF_INET6 : AF_INET, k->dst.b, s->host, sizeof s->host)) return -1;
    return 0;
}

static int d_set_host(void *sess, const char *host) {
    struct hy2_sess *s = sess;
    size_t n = host ? strlen(host) : 0;
    if (!n || n >= sizeof s->host) return -1;
    memcpy(s->host, host, n + 1);
    return 0;
}

static int tcp_send(struct link *l, struct hy2_sess *s, const unsigned char *d, size_t n) {
    if (s->up_stopped) return SEND_OK;
    if (s->pre) return s->sid < 0 && pre_put(l, s, d, n) == 0 ? SEND_OK : SEND_AGAIN;
    if (s->sid < 0) {
        int sr = stream_open(l, s);
        if (sr == SEND_AGAIN) return n && pre_put(l, s, d, n) == 0 ? SEND_OK : SEND_AGAIN;
        if (sr != SEND_OK) return sr;
    }
    if (!n) return SEND_OK;
    size_t room = hy2c_tcp_room(l->c, s->sid);
    if (room && n > room) return SEND_AGAIN;
    ssize_t w = hy2c_tcp_send(l->c, s->sid, d, n);
    if (w == HY2C_ESTOPPED) {
        s->up_stopped = 1;
        return SEND_OK;
    }
    if (w == QC_EAGAIN) return SEND_AGAIN;
    return w == (ssize_t)n ? SEND_OK : SEND_FATAL;
}

static int udp_send(struct link *l, struct hy2_sess *s, const unsigned char *d, size_t n) {
    if (!s->usid || !n) return SEND_FATAL;
    int rc = hy2c_udp_send(l->c, s->usid, s->host, s->port, d + 1, n - 1);
    if (rc == 0 || rc == QC_ETOOBIG) return SEND_OK;
    return rc == QC_EAGAIN ? SEND_AGAIN : SEND_FATAL;
}

static int d_send(const void *ctx, void *sess, const struct flow_key *k, int udp, const unsigned char *d, size_t n) {
    (void)ctx;
    (void)k;
    struct hy2_sess *s = sess;
    struct link *l = live_of(s);
    if (!udp && s->up_stopped && s->st == S_LIVE) return SEND_OK;
    if (!l || s->st != S_LIVE || s->gone || (s->how && s->how != E_FIN)) return SEND_FATAL;
    return udp ? udp_send(l, s, d, n) : tcp_send(l, s, d, n);
}

static int d_finish(const void *ctx, void *sess) {
    (void)ctx;
    struct hy2_sess *s = sess;
    struct link *l = live_of(s);
    if (s->udp || s->up_stopped || s->gone || (s->how && s->how != E_FIN) || !l || s->st != S_LIVE) return SEND_OK;
    if (s->pre || s->want_stream) return SEND_AGAIN;
    if (s->sid < 0) {
        int sr = tcp_send(l, s, NULL, 0);
        if (sr != SEND_OK || s->sid < 0) return sr;
    }
    int rc = hy2c_tcp_finish(l->c, s->sid);
    if (rc == HY2C_ESTOPPED) s->up_stopped = 1;
    return SEND_OK;
}

static long d_room(const void *ctx, void *sess) {
    (void)ctx;
    struct hy2_sess *s = sess;
    if (s->up_stopped) return OPEN_ROOM;
    if (s->want_stream || s->pre) return 0;
    struct link *l = live_of(s);
    if (!l || s->gone) return 0;
    if (s->sid < 0) return OPEN_ROOM;
    return (long)hy2c_tcp_room(l->c, s->sid);
}

static size_t d_frame(const void *ctx, const void *sess, const unsigned char *p, size_t n, unsigned char *out, size_t cap) {
    (void)ctx;
    (void)sess;
    if (n > UDP_DGRAM_MAX || n + 1 > cap) return 0;
    out[0] = 0;
    if (n) memcpy(out + 1, p, n);
    return n + 1;
}

static int d_read(void *sess, unsigned char *buf, size_t cap, const unsigned char **data, size_t *got) {
    (void)buf;
    struct hy2_sess *s = sess;
    *got = 0;
    if (!s->how && s->st == S_LIVE && !link_of(s)) s->how = E_LINK;
    if (s->how == E_FIN && !s->bytes && s->pre) return 0;
    if (s->how == E_RESET || s->how == E_LINK || (s->how && !s->bytes)) {
        q_drop(s);
        s->said = 1;
        return 1;
    }
    struct hy2_rxc *h = s->head;
    if (!h) return 0;
    if (s->udp) {
        uint16_t len;
        memcpy(&len, h->d + h->off, 2);
        if (len > cap) {
            q_take(s, 2u + len);
            s->dg_n--;
            return 0;
        }
        *data = h->d + h->off + 2;
        *got = len;
        return 0;
    }
    size_t avail = h->n - h->off;
    *data = h->d + h->off;
    *got = avail < cap ? avail : cap;
    return 0;
}

static int d_deliver(const void *ctx, void *sess, int udp, const unsigned char *d, size_t n, dialer_emit_fn emit,
                     void *arg) {
    (void)ctx;
    struct hy2_sess *s = sess;
    if (!n || !s->head) return 0;
    if (emit(arg, d, n) != 0) return -1;
    if (udp) {
        q_take(s, n + 2);
        s->dg_n--;
        return 0;
    }
    q_take(s, n);
    struct link *l = live_of(s);
    if (!l || s->sid < 0) return 0;
    if (!s->gone) hy2c_tcp_consumed(l->c, s->sid, n);
    else if (s->gone == 2) hy2c_conn_consumed(l->c, n);
    return 0;
}

#ifdef QC_WITH_SERVER
size_t hy2d_rx_held(const struct hy2_sess *s) { return s->bytes; }
struct hy2c *hy2d_link_conn(unsigned i) {
    return g_t && i < MUX_HY2_LINKS && g_t->l[i].state != L_FREE ? g_t->l[i].c : NULL;
}
unsigned long hy2d_udp_in(void) { return g_t ? g_t->udp_in : 0; }
int hy2d_link_dead(const struct hy2_sess *s) {
    const struct link *l = link_of(s);
    return l && l->state == L_DEAD;
}
void hy2d_loss_said_reset(void) {
    memset(g_loss_said, 0, sizeof g_loss_said);
    g_loss_rr = 0;
}
#endif

const struct dialer_mux hy2_mux = {
    .host = m_host, .attach = m_attach, .event = m_event, .pass = m_pass, .cut = m_cut, .busy = m_busy,
    .shares = m_shares, .stop = m_stop,
};

const struct dialer_ops hy2_dialer = {
    .name = "hysteria2",
    .caps = 0,
    .sess_size = sizeof(struct hy2_sess),
    .peer = d_peer,
    .describe = d_describe,
    .strerror = d_strerror,
    .why = d_why,
    .connect = d_connect,
    .take = d_take,
    .close = d_close,
    .clear = d_clear,
    .fd = d_fd,
    .has_data = d_has_data,
    .flow_open = d_flow_open,
    .send = d_send,
    .room = d_room,
    .finish = d_finish,
    .dgram_frame = d_frame,
    .read = d_read,
    .deliver = d_deliver,
    .set_host = d_set_host,
    .mux = &hy2_mux,
};
