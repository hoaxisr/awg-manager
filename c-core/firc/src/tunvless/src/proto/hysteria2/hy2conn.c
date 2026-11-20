/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "hy2conn.h"
#include "hy2link.h"
#include "hy2wire.h"
#include "qcbrutal.h"
#include "osrand.h"
#include "reality.h"

#define RESP_MAX 6160
#define SALT_POOL 256
#define UDP_DEFAULT 256
#define DGRAM_BUF 2048
#define AUTH_MAX (HY2_AUTH_RESP_MAX + 1)
#define RASM_PIECE 2048
#define RASM_BUDGET (1u << 20)
#define TAB_MIN 16
#define UDP_MSG_MAX 8192
#define DRAIN_NS 30000000000ull
#define LIVE_GRACE_NS 5000000000ull
#define LIVE_SLACK_NS 1000000000ull
#define LEFT_NS ((uint64_t)HY2C_LEFT_MS * 1000000u)

enum { ST_HANDSHAKE, ST_AUTH, ST_READY, ST_DEAD };

struct hy2s {
    int64_t sid;
    size_t held;
    uint8_t *resp;
    struct hy2s *prev, *next;
    uint64_t t0;
    uint16_t resp_n, resp_cap;
    uint8_t open, fin, stopped, hidden, pfin;
#ifdef QC_WITH_SERVER
    uint64_t t_in, t_ret;
#endif
};

struct slist {
    struct hy2s *head, *tail;
    size_t n;
};

struct hy2u {
    struct hy2u *older, *newer;
    uint32_t id;
    uint8_t queued;
    size_t rbytes;
    struct hy2_rasm ras;
};

struct slot {
    uint64_t key;
    void *v;
};

struct tab {
    struct slot *s;
    size_t cap, n;
};

struct obfs {
    struct hy2_salamander s;
    uint8_t pool[SALT_POOL];
    size_t off;
};

struct hy2c {
    struct qc *q;
    struct hy2c_ops ops;
    void *user;
    int state, fail, want_streams, udp, ka_on, ka_ever, lost, left_cut;
    unsigned ka_ms;
    uint64_t ka_ns, ka_off, live_dl, rx_seen, tx_seen, s1, pass_at;
    int64_t auth_sid;
    uint8_t *abuf;
    size_t an;
    uint64_t auth_deadline;
    unsigned hs_ms;
    char *auth;
    uint64_t rx_bps, up_bps, tx_bps;
    int cc, no_loss_comp;
    unsigned udp_max;
    uint32_t udp_next;
    size_t rasm_bytes;
    struct hy2u *oldest, *newest;
    struct slist drain, pend, left;
    struct tab streams, udps;
    struct obfs *ob;
    struct qc_filter flt;
    char why[48];
#ifdef QC_WITH_SERVER
    uint64_t probe_steps, probe_ops, rasm_peak, clock;
#endif
};

#ifdef QC_WITH_SERVER
void (*hy2c_credit_hook)(void *u, int64_t sid, uint64_t delivered, uint64_t returned);
#define STEP(c) (((struct hy2c *)(uintptr_t)(c))->probe_steps++)
#define OP(c) (((struct hy2c *)(uintptr_t)(c))->probe_ops++)
#define T_IN(s, n) ((s)->t_in += (n))
#define T_RET(s, n) ((s)->t_ret += (n))
#else
#define STEP(c) ((void)(c))
#define OP(c) ((void)(c))
#define T_IN(s, n) ((void)0)
#define T_RET(s, n) ((void)0)
#endif

static uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec;
}

static uint64_t clk(const struct hy2c *c) {
#ifdef QC_WITH_SERVER
    if (c->clock) return c->clock;
#endif
    (void)c;
    return now_ns();
}

static uint64_t at_real(const struct hy2c *c, uint64_t t) {
#ifdef QC_WITH_SERVER
    if (c->clock) {
        uint64_t r = now_ns();
        return t <= c->clock ? r : r + (t - c->clock);
    }
#endif
    (void)c;
    return t;
}

static uint64_t on_clock(const struct hy2c *c, uint64_t t) {
#ifdef QC_WITH_SERVER
    if (c->clock) return c->clock;
#endif
    (void)c;
    return t;
}

static void l_push(struct slist *l, struct hy2s *s) {
    s->next = NULL;
    s->prev = l->tail;
    if (l->tail) l->tail->next = s; else l->head = s;
    l->tail = s;
    l->n++;
}

static void l_del(struct slist *l, struct hy2s *s) {
    if (s->prev) s->prev->next = s->next; else l->head = s->next;
    if (s->next) s->next->prev = s->prev; else l->tail = s->prev;
    s->prev = s->next = NULL;
    l->n--;
}

static void *t_get(const struct hy2c *c, const struct tab *t, uint64_t key) {
    OP(c);
    if (!t->cap) return NULL;
    size_t m = t->cap - 1;
    for (size_t i = (size_t)key & m;; i = (i + 1) & m) {
        STEP(c);
        if (!t->s[i].v) return NULL;
        if (t->s[i].key == key) return t->s[i].v;
    }
}

static void t_place(const struct hy2c *c, struct tab *t, uint64_t key, void *v) {
    size_t m = t->cap - 1, i = (size_t)key & m;
    for (; t->s[i].v; i = (i + 1) & m) STEP(c);
    STEP(c);
    t->s[i].key = key;
    t->s[i].v = v;
}

static int t_put(struct hy2c *c, struct tab *t, uint64_t key, void *v) {
    OP(c);
    if ((t->n + 1) * 2 > t->cap) {
        size_t cap = t->cap ? t->cap * 2 : TAB_MIN;
        struct slot *old = t->s;
        size_t oc = t->cap;
        t->s = calloc(cap, sizeof *t->s);
        if (!t->s) { t->s = old; return -1; }
        t->cap = cap;
        for (size_t i = 0; i < oc; i++)
            if (old[i].v) t_place(c, t, old[i].key, old[i].v);
        free(old);
    }
    t_place(c, t, key, v);
    t->n++;
    return 0;
}

static void t_del(struct hy2c *c, struct tab *t, uint64_t key) {
    OP(c);
    if (!t->cap) return;
    size_t m = t->cap - 1, i = (size_t)key & m;
    for (;; i = (i + 1) & m) {
        STEP(c);
        if (!t->s[i].v) return;
        if (t->s[i].key == key) break;
    }
    for (size_t j = i;;) {
        j = (j + 1) & m;
        STEP(c);
        if (!t->s[j].v) break;
        size_t h = (size_t)t->s[j].key & m;
        int stays = i <= j ? (h > i && h <= j) : (h > i || h <= j);
        if (stays) continue;
        t->s[i] = t->s[j];
        i = j;
    }
    t->s[i].v = NULL;
    t->s[i].key = 0;
    t->n--;
}

static uint64_t s_key(int64_t sid) { return (uint64_t)sid >> 2; }

static struct hy2s *s_find(const struct hy2c *c, int64_t sid) {
    if (sid < 0 || (sid & 3) != 0) return NULL;
    struct hy2s *s = t_get(c, &c->streams, s_key(sid));
    return s && s->sid == sid ? s : NULL;
}

static struct hy2s *s_live(const struct hy2c *c, int64_t sid) {
    struct hy2s *s = s_find(c, sid);
    return s && !s->hidden ? s : NULL;
}

static void s_free(struct hy2s *s) {
    free(s->resp);
    free(s);
}

static void s_drop(struct hy2c *c, struct hy2s *s) {
    t_del(c, &c->streams, s_key(s->sid));
#ifdef QC_WITH_SERVER
    if (hy2c_credit_hook) hy2c_credit_hook(c->user, s->sid, s->t_in, s->t_ret);
#endif
    s_free(s);
}

static void give_stream(struct hy2c *c, struct hy2s *s, size_t n) {
    if (!n) return;
    qc_stream_consumed(c->q, s->sid, n);
    T_RET(s, n);
    (void)s;
}

static void give_conn(struct hy2c *c, struct hy2s *s, size_t n) {
    if (!n) return;
    qc_conn_consumed(c->q, n);
    T_RET(s, n);
    (void)s;
}

static struct hy2u *u_find(const struct hy2c *c, uint32_t id) {
    return id ? t_get(c, &c->udps, id) : NULL;
}

static void q_unlink(struct hy2c *c, struct hy2u *x) {
    if (!x->queued) return;
    if (x->older) x->older->newer = x->newer; else c->oldest = x->newer;
    if (x->newer) x->newer->older = x->older; else c->newest = x->older;
    x->older = x->newer = NULL;
    x->queued = 0;
}

static void q_push(struct hy2c *c, struct hy2u *x) {
    q_unlink(c, x);
    x->older = c->newest;
    if (c->newest) c->newest->newer = x; else c->oldest = x;
    c->newest = x;
    x->queued = 1;
}

static void rasm_account(struct hy2c *c, struct hy2u *x) {
    size_t now = x->ras.buf && x->ras.count ? (size_t)x->ras.count * x->ras.piece : 0;
    c->rasm_bytes = c->rasm_bytes - x->rbytes + now;
    x->rbytes = now;
    if (!now) q_unlink(c, x);
    else if (!x->queued) q_push(c, x);
#ifdef QC_WITH_SERVER
    if (c->rasm_bytes > c->rasm_peak) c->rasm_peak = c->rasm_bytes;
#endif
}

static void u_free(struct hy2c *c, struct hy2u *x) {
    q_unlink(c, x);
    c->rasm_bytes -= x->rbytes;
    hy2_rasm_free(&x->ras);
    free(x);
}

static size_t ob_tx(void *u, uint8_t *dst, const uint8_t *src, size_t n) {
    struct obfs *o = u;
    if (o->off + 8 > SALT_POOL) {
        if (os_rand_seed(o->pool, SALT_POOL) != 0) return 0;
        o->off = 0;
    }
    size_t w = hy2_salamander_wrap(&o->s, o->pool + o->off, dst, src, n);
    o->off += 8;
    return w;
}

static size_t ob_rx(void *u, uint8_t *dst, const uint8_t *src, size_t n) {
    return hy2_salamander_unwrap(&((struct obfs *)u)->s, dst, src, n);
}

static void auth_fail(struct hy2c *c, const char *why) {
    if (c->fail) return;
    c->fail = 1;
    snprintf(c->why, sizeof c->why, "%s", why);
    free(c->abuf);
    c->abuf = NULL;
    qc_close(c->q, 0x101);
}

static void left_cut(struct hy2c *c) {
    if (c->left_cut) return;
    c->left_cut = 1;
    qc_close(c->q, 0x100);
}

static void s_leave(struct hy2c *c, struct hy2s *s) {
    if (s->pfin) { s_drop(c, s); return; }
    s->hidden = 3;
    s->t0 = clk(c);
    l_push(&c->left, s);
}

static void drain_reset(struct hy2c *c, struct hy2s *s) {
    l_del(&c->drain, s);
    qc_stream_reset(c->q, s->sid, 0);
    s_leave(c, s);
}

static void drain_enforce(struct hy2c *c) {
    size_t sum = 0, total = qc_send_total(c->q), buf = qc_send_buf(c->q), cap = total / 4;
    size_t two = 2 * buf < total - buf ? 2 * buf : total - buf;
    if (cap < two) cap = two;
    for (struct hy2s *s = c->drain.head; s; s = s->next) sum += qc_stream_queued(c->q, s->sid);
    while (sum > cap && c->drain.head) {
        struct hy2s *o = c->drain.head;
        sum -= qc_stream_queued(c->q, o->sid);
        drain_reset(c, o);
    }
}

static uint64_t drain_at(const struct hy2c *c) { return c->drain.head ? c->drain.head->t0 + DRAIN_NS : UINT64_MAX; }

static void drain_due(struct hy2c *c) {
    if (!c->drain.head) return;
    uint64_t now = clk(c);
    while (c->drain.head && now >= drain_at(c)) drain_reset(c, c->drain.head);
}

static void stream_kill(struct hy2c *c, struct hy2s *s) {
    int64_t sid = s->sid;
    qc_stream_stop(c->q, sid, 0);
    size_t unsent = s->stopped ? 0 : qc_stream_unsent(c->q, sid);
    if (!s->fin && !s->stopped) {
        if (unsent > 0) qc_stream_reset(c->q, sid, 0);
        else (void)qc_stream_send(c->q, sid, NULL, 0, 1);
    }
    give_conn(c, s, s->held);
    s->held = 0;
    if (s->fin && unsent > 0) {
        s->hidden = 2;
        s->t0 = clk(c);
        l_push(&c->drain, s);
        drain_enforce(c);
        return;
    }
    s_leave(c, s);
}

static void pend_run(struct hy2c *c) {
    while (c->pend.head) {
        struct hy2s *s = c->pend.head;
        l_del(&c->pend, s);
        qc_stream_reset(c->q, s->sid, 0);
        s_drop(c, s);
    }
}

static void stream_abort(struct hy2c *c, struct hy2s *s) {
    int64_t sid = s->sid;
    stream_kill(c, s);
    if (c->ops.on_stream_close) c->ops.on_stream_close(c->user, sid, HY2C_END_RESET);
}

static void cb_handshake(void *u) {
    struct hy2c *c = u;
    uint8_t b[4096], rnd[2048];
    char pad[2048];
    int64_t ctl;
    c->state = ST_AUTH;
    c->auth_deadline = qc_opened_ns(c->q) + (uint64_t)c->hs_ms * 1000000u;
    size_t l = hy2_h3_control(b, sizeof b);
    if (qc_stream_open_uni(c->q, &ctl) != 0 || qc_stream_send(c->q, ctl, b, l, 0) != (ssize_t)l) {
        auth_fail(c, "auth could not start");
        return;
    }
    if (os_rand_seed(rnd, 2) != 0) { auth_fail(c, "auth could not start"); return; }
    size_t pn = hy2_pad_len((uint16_t)(((unsigned)rnd[0] << 8) | rnd[1]), 256, 2048);
    if (os_rand_seed(rnd, (unsigned)pn) != 0) { auth_fail(c, "auth could not start"); return; }
    hy2_pad_fill(pad, pn, rnd);
    l = hy2_auth_request(b, sizeof b, c->auth, c->rx_bps, pad, pn);
    if (!l || qc_stream_open(c->q, &c->auth_sid) != 0 || qc_stream_send(c->q, c->auth_sid, b, l, 1) != (ssize_t)l)
        auth_fail(c, "auth could not start");
}

static void cc_choose(struct hy2c *c, uint64_t rx) {
    uint64_t bps = 0;
    if (c->cc == HY2_CC_FORCE) bps = c->up_bps;
    else if (c->cc == HY2_CC_AUTO && c->up_bps && rx) bps = c->up_bps < rx ? c->up_bps : rx;
    if (bps && qc_cc_brutal(c->q, bps, c->no_loss_comp) == 0) c->tx_bps = bps < QCB_BPS_MAX ? bps : QCB_BPS_MAX;
}

static void auth_feed(struct hy2c *c, const uint8_t *d, size_t n, int fin) {
    size_t take = n < AUTH_MAX - c->an ? n : AUTH_MAX - c->an;
    if (take) {
        uint8_t *b = realloc(c->abuf, c->an + take);
        if (!b) { auth_fail(c, "auth response malformed"); return; }
        c->abuf = b;
        memcpy(c->abuf + c->an, d, take);
        c->an += take;
    }
    struct hy2_auth_resp r;
    int rc = hy2_auth_response(c->abuf, c->an, &r);
    if (rc < 0 || (rc == 0 && c->an == AUTH_MAX)) { auth_fail(c, "auth response malformed"); return; }
    if (rc == 0) {
        if (fin) auth_fail(c, "auth stream ended");
        return;
    }
    if (r.status != HY2_STATUS_OK) {
        char w[32];
        snprintf(w, sizeof w, "auth failed code %d", r.status);
        auth_fail(c, w);
        return;
    }
    free(c->abuf);
    c->abuf = NULL;
    c->an = 0;
    c->state = ST_READY;
    c->udp = r.udp && qc_datagram_max(c->q) > 0;
    cc_choose(c, r.rx_bps);
    if (c->ops.on_ready) c->ops.on_ready(c->user, r.rx_bps, c->udp);
}

static int resp_keep(struct hy2s *s, const uint8_t *d, size_t n) {
    size_t need = (size_t)s->resp_n + n;
    if (need > s->resp_cap) {
        size_t cap = s->resp_cap ? (size_t)s->resp_cap * 2 : 256;
        while (cap < need) cap *= 2;
        if (cap > RESP_MAX) cap = RESP_MAX;
        uint8_t *b = realloc(s->resp, cap);
        if (!b) return -1;
        s->resp = b;
        s->resp_cap = (uint16_t)cap;
    }
    memcpy(s->resp + s->resp_n, d, n);
    s->resp_n = (uint16_t)need;
    return 0;
}

static void resp_feed(struct hy2c *c, struct hy2s *s, const uint8_t *d, size_t n, int fin) {
    int64_t sid = s->sid;
    size_t before = s->resp_n, bn = n;
    const uint8_t *buf = d;
    if (before) {
        size_t take = n < RESP_MAX - before ? n : RESP_MAX - before;
        if (resp_keep(s, d, take) != 0) { give_stream(c, s, n); stream_abort(c, s); return; }
        s->resp_n = (uint16_t)before;
        buf = s->resp;
        bn = before + take;
    }
    struct hy2_tcp_resp r;
    int rc = hy2_tcp_response(buf, bn, &r);
    if (rc == 0) {
        give_stream(c, s, n);
        if (fin || bn >= RESP_MAX) { stream_abort(c, s); return; }
        if (before) s->resp_n = (uint16_t)bn;
        else if (resp_keep(s, d, n) != 0) { stream_abort(c, s); return; }
        return;
    }
    size_t here = rc > 0 ? r.used - before : n;
    give_stream(c, s, here);
    free(s->resp);
    s->resp = NULL;
    s->resp_n = s->resp_cap = 0;
    if (rc < 0) { stream_abort(c, s); return; }
    if (!r.ok) {
        give_stream(c, s, n - here);
        stream_kill(c, s);
        if (c->ops.on_stream_refused) c->ops.on_stream_refused(c->user, sid, r.msg);
        return;
    }
    s->open = 1;
    size_t rest = n - here;
    if (!rest && !fin) return;
    s->held += rest;
    if (c->ops.on_stream_data) c->ops.on_stream_data(c->user, sid, d + here, rest, fin);
}

static void cb_data(void *u, int64_t sid, const uint8_t *d, size_t n, int fin) {
    struct hy2c *c = u;
    if (sid == c->auth_sid) {
        qc_stream_consumed(c->q, sid, n);
        if (c->state == ST_AUTH && !c->fail) auth_feed(c, d, n, fin);
        return;
    }
    struct hy2s *s = s_find(c, sid);
    if (!s) { qc_stream_consumed(c->q, sid, n); return; }
    T_IN(s, n);
    if (fin) s->pfin = 1;
    if (s->hidden) { give_stream(c, s, n); return; }
    if (!s->open) { resp_feed(c, s, d, n, fin); return; }
    s->held += n;
    if (c->ops.on_stream_data) c->ops.on_stream_data(c->user, sid, d, n, fin);
}

static void cb_writable(void *u, int64_t sid) {
    struct hy2c *c = u;
    if (s_live(c, sid) && c->ops.on_stream_writable) c->ops.on_stream_writable(c->user, sid);
}

static void cb_peer_abort(void *u, int64_t sid, int reset, uint64_t rx_err, int stopped, uint64_t tx_err) {
    struct hy2c *c = u;
    (void)rx_err; (void)tx_err;
    if (!reset && !stopped) return;
    if (sid == c->auth_sid) {
        if (reset && c->state == ST_AUTH && !c->fail) auth_fail(c, "auth stream ended");
        return;
    }
    struct hy2s *s = s_find(c, sid);
    if (s && reset) s->pfin = 1;
    if (!s || s->hidden) return;
    if (!reset) {
        s->stopped = 1;
        return;
    }
    give_conn(c, s, s->held);
    s->held = 0;
    s->hidden = 1;
    l_push(&c->pend, s);
    if (c->ops.on_stream_close) c->ops.on_stream_close(c->user, sid, HY2C_END_RESET);
}

static void cb_streams_open(void *u) {
    struct hy2c *c = u;
    if (!c->want_streams) return;
    c->want_streams = 0;
    if (c->ops.on_streams_open) c->ops.on_streams_open(c->user);
}

static void cb_sclose(void *u, int64_t sid, int rx_reset, uint64_t rx_err, int tx_stopped, uint64_t tx_err) {
    struct hy2c *c = u;
    (void)rx_err; (void)tx_stopped; (void)tx_err;
    if (sid == c->auth_sid) {
        if (c->state == ST_AUTH && !c->fail) auth_fail(c, "auth stream ended");
        return;
    }
    struct hy2s *s = s_find(c, sid);
    if (!s) return;
    if (s->hidden) {
        l_del(s->hidden == 3 ? &c->left : s->hidden == 2 ? &c->drain : &c->pend, s);
        s_drop(c, s);
        return;
    }
    int how = rx_reset || !s->open ? HY2C_END_RESET : HY2C_END_CLEAN;
    if (how != HY2C_END_CLEAN) give_conn(c, s, s->held);
    s->held = 0;
    s_drop(c, s);
    if (c->ops.on_stream_close) c->ops.on_stream_close(c->user, sid, how);
}

static int rasm_fresh(const struct hy2u *x, const struct hy2_udp_msg *m) {
    return m->count > 1 && !(x->ras.count && m->pkt == x->ras.pkt && m->count == x->ras.count);
}

static int rasm_admit(struct hy2c *c, struct hy2u *x, const struct hy2_udp_msg *m) {
    if (!rasm_fresh(x, m)) return 1;
    size_t cost = (size_t)m->count * RASM_PIECE;
    if (cost > RASM_BUDGET) return 0;
    while (c->rasm_bytes - x->rbytes + cost > RASM_BUDGET) {
        struct hy2u *o = c->oldest == x ? x->newer : c->oldest;
        if (!o) return 0;
        hy2_rasm_free(&o->ras);
        rasm_account(c, o);
    }
    return 1;
}

static void cb_dgram(void *u, const uint8_t *d, size_t n) {
    struct hy2c *c = u;
    struct hy2_udp_msg m;
    if (c->state != ST_READY || hy2_udp_parse(d, n, &m) != 0) return;
    struct hy2u *x = u_find(c, m.sid);
    if (!x || !rasm_admit(c, x, &m)) return;
    int fresh = rasm_fresh(x, &m);
    size_t len = 0;
    const uint8_t *p = hy2_rasm_feed(&x->ras, &m, &len);
    rasm_account(c, x);
    if (fresh && x->queued) q_push(c, x);
    if (!p) return;
    uint32_t id = m.sid;
    int pooled = p != m.data;
    if (c->ops.on_udp) c->ops.on_udp(c->user, id, m.addr, m.addr_n, p, len);
    if (pooled && (x = u_find(c, id)) != NULL) {
        hy2_rasm_free(&x->ras);
        rasm_account(c, x);
    }
}

static void cb_closed(void *u, int reason, int app, uint64_t code, const char *why) {
    struct hy2c *c = u;
    (void)app; (void)code;
    c->state = ST_DEAD;
    for (size_t i = 0; i < c->udps.cap; i++) {
        struct hy2u *x = c->udps.s[i].v;
        if (!x) continue;
        hy2_rasm_free(&x->ras);
        rasm_account(c, x);
    }
    for (size_t i = 0; i < c->streams.cap;) {
        struct hy2s *s = c->streams.s[i].v;
        if (!s) { i++; continue; }
        int64_t sid = s->sid;
        int hidden = s->hidden;
        t_del(c, &c->streams, s_key(sid));
        s_free(s);
        if (!hidden && c->ops.on_stream_close) c->ops.on_stream_close(c->user, sid, HY2C_END_LINK);
    }
    memset(&c->drain, 0, sizeof c->drain);
    memset(&c->pend, 0, sizeof c->pend);
    memset(&c->left, 0, sizeof c->left);
    if (c->fail) {
        reason = HY2C_CLOSE_AUTH;
        why = c->why;
    } else if ((c->lost && reason == QC_CLOSE_LOCAL) || (c->ka_on && reason == QC_CLOSE_IDLE)) {
        reason = QC_CLOSE_ERROR;
        why = "the node stopped answering";
    } else if (c->left_cut && reason == QC_CLOSE_LOCAL) {
        reason = QC_CLOSE_ERROR;
        why = "peer left closed streams open";
    }
    if (c->ops.on_closed) c->ops.on_closed(c->user, reason, why);
}

static const struct qc_ops g_ops = {
    .on_handshake = cb_handshake,
    .on_stream_data = cb_data,
    .on_stream_writable = cb_writable,
    .on_streams_open = cb_streams_open,
    .on_stream_peer_abort = cb_peer_abort,
    .on_stream_close = cb_sclose,
    .on_datagram = cb_dgram,
    .on_closed = cb_closed,
};

int hy2c_open(const struct hy2c_cfg *cfg, const struct hy2c_ops *ops, void *user, struct hy2c **out) {
    if (!cfg || !ops || !out || !cfg->auth) return QC_EINVAL;
    *out = NULL;
    struct hy2c *c = calloc(1, sizeof *c);
    if (!c) return QC_ENOMEM;
    c->ops = *ops;
    c->user = user;
    c->auth_sid = -1;
    c->rx_bps = cfg->rx_bps;
    c->up_bps = cfg->up_bps;
    c->cc = cfg->cc;
    c->no_loss_comp = cfg->no_loss_comp;
    c->hs_ms = cfg->qc.handshake_ms ? cfg->qc.handshake_ms : 10000;
    c->udp_max = cfg->udp_sessions ? cfg->udp_sessions : UDP_DEFAULT;
    c->udp_next = 1;
    c->auth = strdup(cfg->auth);
    int rc = QC_ENOMEM;
    if (!c->auth) goto fail;
    struct qc_cfg q = cfg->qc;
    c->ka_ms = q.keepalive_ms;
    q.keepalive_ms = 0;
    q.alpn = "h3";
    q.datagram_max = 65535;
    q.defer_flush = 1;
    if (!q.prefer_chacha) q.prefer_chacha = !xc_cpu_has_aes();
    if (cfg->obfs_password && cfg->obfs_password[0]) {
        c->ob = calloc(1, sizeof *c->ob);
        if (!c->ob) goto fail;
        rc = QC_EINVAL;
        if (hy2_salamander_init(&c->ob->s, cfg->obfs_password) != 0) goto fail;
        c->ob->off = SALT_POOL;
        c->flt.tx = ob_tx;
        c->flt.rx = ob_rx;
        c->flt.user = c->ob;
        c->flt.overhead = 8;
        q.filter = &c->flt;
    }
    rc = qc_open(&q, &g_ops, c, &c->q);
    if (rc != 0) goto fail;
    *out = c;
    return 0;
fail:
    hy2c_free(c);
    return rc;
}

void hy2c_close(struct hy2c *c) {
    if (!c || c->state == ST_DEAD) return;
    qc_close(c->q, 0x100);
    qc_flush(c->q);
}

void hy2c_free(struct hy2c *c) {
    if (!c) return;
    qc_free(c->q);
    for (size_t i = 0; i < c->streams.cap; i++)
        if (c->streams.s[i].v) s_free(c->streams.s[i].v);
    for (size_t i = 0; i < c->udps.cap; i++)
        if (c->udps.s[i].v) u_free(c, c->udps.s[i].v);
    free(c->streams.s);
    free(c->udps.s);
    free(c->abuf);
    free(c->auth);
    free(c->ob);
    free(c);
}

int hy2c_fd(const struct hy2c *c) { return qc_fd(c->q); }
int hy2c_fd_extra(const struct hy2c *c) { return qc_fd_extra(c->q); }

static void live_seen(struct hy2c *c) {
    uint64_t r = qc_rx_last_ns(c->q);
    if (r != c->rx_seen) {
        c->rx_seen = r;
        uint64_t dl = on_clock(c, r) + c->ka_ns + LIVE_GRACE_NS;
        if (dl > c->live_dl) c->live_dl = dl;
    }
    uint64_t w = qc_tx_wait_ns(c->q);
    if (w != c->tx_seen) {
        c->tx_seen = w;
        c->s1 = w ? on_clock(c, w) : 0;
    }
}

static void ka_sync(struct hy2c *c) {
    int on = c->ka_ms && c->state == ST_READY && c->streams.n - c->drain.n - c->pend.n - c->left.n + c->udps.n > 0;
    if (on == c->ka_on) return;
    c->ka_on = on;
    uint64_t now = clk(c);
    if (!on) {
        qc_keepalive(c->q, 0);
        c->ka_off = now;
        return;
    }
    qc_keepalive(c->q, c->ka_ms);
    c->ka_ns = qc_keepalive_ns(c->q);
    if (!c->ka_ever || now >= c->ka_off + c->ka_ns) c->live_dl = now + c->ka_ns + LIVE_GRACE_NS;
    c->ka_ever = 1;
}

static int live_on(const struct hy2c *c) { return c->ka_on && c->state == ST_READY && !c->lost; }

static void live_due(struct hy2c *c) {
    if (!live_on(c)) return;
    uint64_t now = on_clock(c, qc_now_ns(c->q));
    if (now < c->live_dl) return;
    (void)qc_on_readable(c->q);
    if (c->state != ST_DEAD) pend_run(c);
    live_seen(c);
    ka_sync(c);
    if (!live_on(c) || now < c->live_dl) return;
    if (c->s1 && now >= c->s1 + LIVE_GRACE_NS && now <= c->live_dl + LIVE_SLACK_NS &&
        c->pass_at + LIVE_GRACE_NS / 2 >= c->live_dl) {
        c->lost = 1;
        qc_close(c->q, 0x100);
        return;
    }
    c->live_dl = c->s1 && c->s1 + LIVE_GRACE_NS > now ? c->s1 + LIVE_GRACE_NS : now + LIVE_GRACE_NS;
    qc_ping(c->q);
}

static uint64_t live_wake(const struct hy2c *c) {
    uint64_t mid = c->live_dl - LIVE_GRACE_NS / 2;
    return c->pass_at < mid ? mid : c->live_dl;
}

#ifdef QC_WITH_SERVER
uint64_t hy2c_live_wake_ns(const struct hy2c *c) { return live_on(c) ? live_wake(c) : UINT64_MAX; }
uint64_t hy2c_drain_due_ns(const struct hy2c *c) { return drain_at(c); }
#endif

uint64_t hy2c_deadline_ns(struct hy2c *c) {
    uint64_t d = qc_deadline_ns(c->q);
    if (c->state == ST_AUTH && !c->fail) {
        uint64_t ad = at_real(c, c->auth_deadline);
        if (ad < d) d = ad;
    }
    if (c->drain.head) {
        uint64_t dd = at_real(c, drain_at(c));
        if (dd < d) d = dd;
    }
    if (c->left.head && !c->left_cut) {
        uint64_t lt = at_real(c, c->left.head->t0 + LEFT_NS);
        if (lt < d) d = lt;
    }
    if (live_on(c)) {
        uint64_t ld = at_real(c, live_wake(c));
        if (ld < d) d = ld;
    }
    return d;
}

int hy2c_timeout_ms(struct hy2c *c) {
    uint64_t d = hy2c_deadline_ns(c);
    if (d == UINT64_MAX) return -1;
    uint64_t now = now_ns();
    if (d <= now) return 0;
    uint64_t ms = (d - now + 999999u) / 1000000u;
    return ms > 0x7fffffff ? 0x7fffffff : (int)ms;
}

static int left_over(const struct hy2c *c) {
    return c->left.head && !c->left_cut && clk(c) >= c->left.head->t0 + LEFT_NS;
}

static void left_due(struct hy2c *c) {
    if (c->state == ST_DEAD || !left_over(c)) return;
    (void)qc_on_readable(c->q);
    if (c->state == ST_DEAD) return;
    pend_run(c);
    if (left_over(c)) left_cut(c);
}

static void auth_due(struct hy2c *c) {
    if (c->state == ST_AUTH && !c->fail && clk(c) >= c->auth_deadline) auth_fail(c, "auth timed out");
}

int hy2c_on_readable(struct hy2c *c) {
    auth_due(c);
    if (c->state != ST_DEAD) drain_due(c);
    int rc = qc_on_readable(c->q);
    if (c->state != ST_DEAD) pend_run(c);
    live_seen(c);
    ka_sync(c);
    qc_flush(c->q);
    live_seen(c);
    c->pass_at = on_clock(c, qc_now_ns(c->q));
    return rc;
}

int hy2c_on_timer(struct hy2c *c) {
    auth_due(c);
    if (c->state != ST_DEAD) drain_due(c);
    int rc = qc_on_timer(c->q);
    if (c->state != ST_DEAD) pend_run(c);
    left_due(c);
    live_seen(c);
    ka_sync(c);
    live_due(c);
    qc_flush(c->q);
    live_seen(c);
    c->pass_at = on_clock(c, qc_now_ns(c->q));
    return rc;
}

void hy2c_flush(struct hy2c *c) {
    qc_flush(c->q);
    live_seen(c);
}

int hy2c_ready(const struct hy2c *c) { return c && c->state == ST_READY; }

int hy2c_closed(const struct hy2c *c) { return !c || c->state == ST_DEAD; }

void hy2c_stats(struct hy2c *c, struct qc_stats *st) { qc_stats_get(c->q, st); }
uint64_t hy2c_clock(const struct hy2c *c, uint64_t now_ns) { return on_clock(c, now_ns); }
void hy2c_wait(const struct hy2c *c, uint64_t now_ns, uint64_t ns[3]) { qc_wait_get(c->q, now_ns, ns); }
uint64_t hy2c_hop_fails(const struct hy2c *c) { return qc_hop_fails(c->q); }
uint16_t hy2c_sock_port(const struct hy2c *c) { return qc_sock_port(c->q); }

int hy2c_cc(const struct hy2c *c, uint64_t *tx_bps) {
    struct qc_stats st;
    qc_stats_get(c->q, &st);
    *tx_bps = c->tx_bps;
    return st.cc;
}

int hy2c_tcp_open(struct hy2c *c, const char *host, uint16_t port, int64_t *sid) {
    if (c->state == ST_DEAD || c->fail) return QC_ECLOSED;
    if (c->state != ST_READY) return QC_EAGAIN;
    uint8_t b[4 + 2048 + 4 + 512], rnd[512];
    char pad[512];
    if (os_rand_seed(rnd, 2) != 0) return QC_ENOMEM;
    size_t pn = hy2_pad_len((uint16_t)(((unsigned)rnd[0] << 8) | rnd[1]), 64, 512);
    if (os_rand_seed(rnd, (unsigned)pn) != 0) return QC_ENOMEM;
    hy2_pad_fill(pad, pn, rnd);
    size_t l = hy2_tcp_request(b, sizeof b, host, port, pad, pn);
    if (!l) return QC_EINVAL;
    struct hy2s *s = calloc(1, sizeof *s);
    if (!s) return QC_ENOMEM;
    int64_t id;
    int rc = qc_stream_open(c->q, &id);
    if (rc != 0) {
        free(s);
        if (rc == QC_EAGAIN) c->want_streams = 1;
        return rc;
    }
    s->sid = id;
    if (t_put(c, &c->streams, s_key(id), s) != 0) {
        qc_stream_reset(c->q, id, 0);
        free(s);
        return QC_ENOMEM;
    }
    ssize_t w = qc_stream_send(c->q, id, b, l, 0);
    if (w != (ssize_t)l) {
        qc_stream_reset(c->q, id, 0);
        t_del(c, &c->streams, s_key(id));
        free(s);
        return w < 0 ? (int)w : QC_ETOOBIG;
    }
    *sid = id;
    ka_sync(c);
    return 0;
}

ssize_t hy2c_tcp_send(struct hy2c *c, int64_t sid, const uint8_t *d, size_t n) {
    if (c->state == ST_DEAD) return QC_ECLOSED;
    struct hy2s *s = s_live(c, sid);
    if (!s) return QC_ENOSTREAM;
    if (s->stopped) return HY2C_ESTOPPED;
    return qc_stream_send(c->q, sid, d, n, 0);
}

size_t hy2c_tcp_room(const struct hy2c *c, int64_t sid) {
    const struct hy2s *s = s_live(c, sid);
    return s && !s->stopped ? qc_stream_room(c->q, sid) : 0;
}

int hy2c_tcp_finish(struct hy2c *c, int64_t sid) {
    if (c->state == ST_DEAD) return QC_ECLOSED;
    struct hy2s *s = s_live(c, sid);
    if (!s) return QC_ENOSTREAM;
    if (s->stopped) return HY2C_ESTOPPED;
    ssize_t r = qc_stream_send(c->q, sid, NULL, 0, 1);
    if (r < 0) return (int)r;
    s->fin = 1;
    return 0;
}

void hy2c_tcp_consumed(struct hy2c *c, int64_t sid, size_t n) {
    struct hy2s *s = s_live(c, sid);
    if (!s) return;
    if (n > s->held) n = s->held;
    s->held -= n;
    give_stream(c, s, n);
}

void hy2c_conn_consumed(struct hy2c *c, size_t n) {
    if (c->state != ST_DEAD && n) qc_conn_consumed(c->q, n);
}

void hy2c_tcp_close(struct hy2c *c, int64_t sid) {
    struct hy2s *s = s_live(c, sid);
    if (!s) return;
    stream_kill(c, s);
    ka_sync(c);
}

uint32_t hy2c_udp_session(struct hy2c *c) {
    if (c->state == ST_DEAD || !c->udp || c->udps.n >= c->udp_max) return 0;
    while (c->udp_next == 0 || u_find(c, c->udp_next)) c->udp_next++;
    struct hy2u *x = calloc(1, sizeof *x);
    if (!x) return 0;
    x->id = c->udp_next;
    if (t_put(c, &c->udps, x->id, x) != 0) { free(x); return 0; }
    c->udp_next++;
    ka_sync(c);
    return x->id;
}

static int pkt_id(uint16_t *pkt) {
    for (;;) {
        uint8_t r[2];
        if (os_rand_seed(r, 2) != 0) return -1;
        *pkt = (uint16_t)(((unsigned)r[0] << 8) | r[1]);
        if (*pkt) return 0;
    }
}

int hy2c_udp_send(struct hy2c *c, uint32_t session, const char *host, uint16_t port, const uint8_t *d, size_t n) {
    if (c->state == ST_DEAD) return QC_ECLOSED;
    if (c->state != ST_READY) return QC_EAGAIN;
    if (!u_find(c, session)) return QC_EINVAL;
    uint8_t dg[DGRAM_BUF];
    size_t hl = hy2_udp_header(dg, sizeof dg, session, 0, 0, 1, host, port);
    if (!hl) return QC_EINVAL;
    if (hl + n > UDP_MSG_MAX) return QC_ETOOBIG;
    size_t max = qc_datagram_max(c->q);
    if (max > sizeof dg) max = sizeof dg;
    if (hl + n <= max) {
        if (n) memcpy(dg + hl, d, n);
        return qc_datagram_send(c->q, dg, hl + n);
    }
    unsigned f = hy2_udp_frags(n, hl, max);
    if (!f) return QC_ETOOBIG;
    if (qc_datagram_slots(c->q) < f) return QC_EAGAIN;
    uint16_t pkt;
    if (pkt_id(&pkt) != 0) return QC_ENOMEM;
    size_t per = max - hl, off = 0;
    for (unsigned i = 0; i < f; i++) {
        hy2_udp_header(dg, sizeof dg, session, pkt, (uint8_t)i, (uint8_t)f, host, port);
        size_t k = n - off < per ? n - off : per;
        memcpy(dg + hl, d + off, k);
        off += k;
        int rc = qc_datagram_send(c->q, dg, hl + k);
        if (rc != 0) return rc;
    }
    return 0;
}

void hy2c_udp_end(struct hy2c *c, uint32_t session) {
    struct hy2u *x = u_find(c, session);
    if (!x) return;
    t_del(c, &c->udps, session);
    u_free(c, x);
    ka_sync(c);
}

#ifdef QC_WITH_SERVER
void hy2c_probe_get(const struct hy2c *c, struct hy2c_probe *p) {
    p->steps = c->probe_steps;
    p->ops = c->probe_ops;
    p->rasm_bytes = c->rasm_bytes;
    p->rasm_peak = c->rasm_peak;
    p->drain_n = c->drain.n;
    p->left_n = c->left.n;
}
#endif

#ifdef QC_WITH_SERVER
void hy2c_udp_next_set(struct hy2c *c, uint32_t id) { c->udp_next = id; }
size_t hy2c_tcp_unsent(const struct hy2c *c, int64_t sid) { return s_live(c, sid) ? qc_stream_unsent(c->q, sid) : 0; }
void hy2c_clock_set(struct hy2c *c, uint64_t ns) {
    c->clock = ns;
    qc_clock_set(c->q, ns);
}
void hy2c_hop_now(struct hy2c *c) { qc_hop_now(c->q); }
void hy2c_tally_add(struct hy2c *c, uint64_t sent, uint64_t lost) { qc_tally_add(c->q, sent, lost); }
uint64_t hy2c_opened_ns(const struct hy2c *c) { return qc_opened_ns(c->q); }
#endif
