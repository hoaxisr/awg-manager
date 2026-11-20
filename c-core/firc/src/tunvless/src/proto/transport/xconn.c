#define _GNU_SOURCE
#include <errno.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

#include "xconn.h"
#include "fmlink.h"
#include "osrand.h"
#include "tscratch.h"

#define XC_NONE    UINT16_MAX
#define XC_CANCEL  8
#define XC_ST_FIRST 16
#define XC_REC_OVER 22
#define XC_REFUSED_STREAM 7

_Static_assert(TLS13_MAX_PLAIN <= XC_CHUNK, "a TLS record's plaintext fits one chunk");

struct xc_chunk {
    struct xc_chunk *next;
    uint32_t off, len, pt, ctl, cap;
    size_t pay;
    int64_t gap;
    unsigned char d[];
};

#define XC_PIECE_COST (sizeof(struct xc_chunk) + 16)

static __thread struct xc_host g_host;

static __thread uint32_t g_pend_max = XC_PEND_MAX;
static __thread int64_t g_held;
static __thread uint32_t g_soft = XC_RX_BUDGET, g_hard = XC_RX_HARD, g_resume = XC_RX_RESUME;
static __thread unsigned g_wh_n, g_paused_n;
static __thread uint64_t g_since_ms, g_try_ms, g_now_ms;
static __thread size_t g_queued, g_pend;
static __thread struct xc_chunk *g_free;
static __thread unsigned g_free_n, g_links;
static __thread struct xconn *g_list;

static struct xconn_scratch *scratch(void) {
    return ts_get(TS_XCONN, sizeof(struct xconn_scratch));
}

static struct xconn_scratch *scratch_now(void) { return ts_g[TS_XCONN]; }

void xc_set_host(const struct xc_host *h) {
    if (h) g_host = *h;
    else memset(&g_host, 0, sizeof g_host);
}

void xc_pend_set(uint32_t limit) { g_pend_max = limit; }

void xc_budget_set(uint32_t soft, uint32_t hard, uint32_t resume) {
    g_soft = soft;
    g_hard = hard;
    g_resume = resume;
}

int64_t xc_held(void) { return g_held; }

uint32_t xc_pend_limit(void) { return g_pend_max; }

size_t xc_queued(void) { return g_queued; }

unsigned xc_free_n(void) { return g_free_n; }

static struct xc_chunk *chunk_init(struct xc_chunk *c, size_t cap) {
    c->next = NULL;
    c->off = c->len = c->pt = c->ctl = 0;
    c->cap = (uint32_t)cap;
    c->pay = 0;
    c->gap = 0;
    return c;
}

static struct xc_chunk *chunk_get(void) {
    struct xc_chunk *c = g_free;
    if (c) {
        g_free = c->next;
        g_free_n--;
    } else {
        c = malloc(sizeof *c + XC_CHUNK);
        if (!c) return NULL;
    }
    return chunk_init(c, XC_CHUNK);
}

static struct xc_chunk *chunk_sized(size_t n) {
    if (n == XC_CHUNK) return chunk_get();
    struct xc_chunk *c = malloc(sizeof *c + n);
    return c ? chunk_init(c, n) : NULL;
}

static void chunk_put(struct xc_chunk *c) {
    if (c->cap != XC_CHUNK) {
        free(c);
        return;
    }
    if (g_links && g_free_n < XC_FREE_KEEP) {
        c->next = g_free;
        g_free = c;
        g_free_n++;
    } else {
        free(c);
    }
}

static void free_list_drop(void) {
    while (g_free) {
        struct xc_chunk *c = g_free;
        g_free = c->next;
        free(c);
    }
    g_free_n = 0;
}

static int q_push(struct xc_q *q, const unsigned char *d, size_t n) {
    while (n) {
        struct xc_chunk *t = q->tail;
        if (!t || t->len >= t->cap) {
            t = chunk_get();
            if (!t) return -1;
            if (q->tail) q->tail->next = t;
            else q->head = t;
            q->tail = t;
        }
        size_t k = t->cap - t->len < n ? t->cap - t->len : n;
        memcpy(t->d + t->len, d, k);
        t->len += (uint32_t)k;
        q->n += k;
        d += k;
        n -= k;
    }
    return 0;
}

static void q_link(struct xc_q *q, struct xc_chunk *c) {
    c->next = NULL;
    if (q->tail) q->tail->next = c;
    else q->head = c;
    q->tail = c;
    q->n += c->len - c->off;
}

static size_t q_pop(struct xc_q *q, unsigned char *out, size_t cap) {
    size_t got = 0;
    while (got < cap && q->head) {
        struct xc_chunk *h = q->head;
        size_t k = h->len - h->off < cap - got ? h->len - h->off : cap - got;
        memcpy(out + got, h->d + h->off, k);
        h->off += (uint32_t)k;
        got += k;
        q->n -= k;
        if (h->off == h->len) {
            q->head = h->next;
            if (!q->head) q->tail = NULL;
            chunk_put(h);
        }
    }
    return got;
}

static size_t q_clear(struct xc_q *q) {
    size_t pay = 0;
    while (q->head) {
        struct xc_chunk *h = q->head;
        q->head = h->next;
        pay += h->pay;
        chunk_put(h);
    }
    q->tail = NULL;
    q->n = 0;
    return pay;
}

static void wq_add(uint16_t owner, unsigned what, int rc) {
    if (owner == XS_NOWNER) return;
    struct xconn_scratch *s = scratch_now();
    for (unsigned i = 0; i < s->wq_n; i++)
        if (s->wq[i].owner == owner && s->wq[i].what == what) {
            if (!s->wq[i].rc) s->wq[i].rc = rc;
            return;
        }
    if (s->wq_n < sizeof s->wq / sizeof s->wq[0]) s->wq[s->wq_n++] = (struct xc_wake){ owner, (uint8_t)what, rc };
}

static void wq_run(void) {
    struct xconn_scratch *s = scratch_now();
    if (!s) return;
    unsigned n = s->wq_n;
    struct xc_wake w[sizeof s->wq / sizeof s->wq[0]];
    memcpy(w, s->wq, n * sizeof w[0]);
    s->wq_n = 0;
    if (!g_host.wake) return;
    for (unsigned i = 0; i < n; i++) g_host.wake(g_host.arg, w[i].owner, w[i].what, w[i].rc);
}

static void wake(struct xconn *x, struct xc_st *e, unsigned what, int rc) {
    if (x->shared) wq_add(e->owner, what, rc);
}

static void held_add(struct xconn *x, int64_t n) {
    x->held_n += n;
    if (x->shared) g_held += n;
}

static int bounded(const struct xconn *x) { return x->shared && !x->discard; }

static void wh_drop(struct xc_st *e) {
    if (!e->withheld) return;
    e->withheld = 0;
    g_wh_n--;
}

static struct xc_st *ent(const struct xs *s) {
    if (!s || !s->x || s->slot >= s->x->st_cap) return NULL;
    struct xc_st *e = &s->x->st[s->slot];
    return e->used && e->sid == s->sid ? e : NULL;
}

static struct xc_st *by_slot(struct xconn *x, uint16_t slot) {
    if (slot >= H2M_STREAMS || x->map[slot] == XC_NONE) return NULL;
    return &x->st[x->map[slot]];
}

static void unmap(struct xconn *x, struct xc_st *e) {
    if (e->h2slot != XC_NONE) x->map[e->h2slot] = XC_NONE;
    e->h2slot = XC_NONE;
}

static int check_dead(struct xconn *x, int rc);

static void pend_drop(struct xconn *x, struct xc_st *e) {
    size_t n = e->pend.n;
    q_clear(&e->pend);
    e->pend_end = 0;
    g_pend -= n;
    x->pend_n -= n;
}

static void q_skip(struct xc_q *q, size_t k) {
    struct xc_chunk *h = q->head;
    h->off += (uint32_t)k;
    q->n -= k;
    if (h->off < h->len) return;
    q->head = h->next;
    if (!q->head) q->tail = NULL;
    chunk_put(h);
}

static int q_put(struct xconn *x, struct xc_q *q, size_t n) {
    while (n) {
        struct xc_chunk *h = q->head;
        size_t k = h->len - h->off < n ? h->len - h->off : n;
        int rc = h2m_send_put(&x->h2, h->d + h->off, k);
        if (rc) return rc;
        q_skip(q, k);
        n -= k;
    }
    return 0;
}

static int q_move(struct xc_q *dst, struct xc_q *src, size_t n) {
    while (n) {
        struct xc_chunk *h = src->head;
        size_t k = h->len - h->off < n ? h->len - h->off : n;
        if (q_push(dst, h->d + h->off, k)) return -1;
        q_skip(src, k);
        n -= k;
    }
    return 0;
}

static int seal(struct xconn *x);
static void fail_in(struct xconn *x, int err);

static long q_wire_free(const struct xconn *x, unsigned g) {
    size_t q = x->shared ? g_queued : x->q_pay;
    return q < XC_OUT_HIGH ? (long)((XC_OUT_HIGH - q) / g) : 0;
}

static int pend_flush(struct xconn *x) {
    x->pend_due = 0;
    unsigned g = fm_grow(x->link.tls.fm);
    int staged = 0;
    uint16_t n = x->st_cap, at = x->pend_at;
    for (uint16_t j = 0; j < n && x->state == XC_LIVE; j++) {
        uint16_t i = (uint16_t)((at + j) % n);
        struct xc_st *e = &x->st[i];
        if (!e->used || e->h2slot == XC_NONE || (!e->pend.n && !e->pend_end)) continue;
        long w = h2m_room(&x->h2, e->h2slot);
        size_t k = e->pend.n;
        if (k && w <= 0) continue;
        int rs = g > 1 ? seal(x) : 0;
        if (rs) {
            fail_in(x, rs);
            return staged;
        }
        long f = q_wire_free(x, g);
        if (f <= 0) break;
        if ((long)k > w) k = (size_t)w;
        if ((long)k > f) k = (size_t)f;
        int last = e->pend_end && k == e->pend.n;
        size_t had = e->pend.n;
        int rc = h2m_send_begin(&x->h2, e->h2slot, k, last);
        if (!rc) rc = q_put(x, &e->pend, k);
        g_pend -= had - e->pend.n;
        x->pend_n -= had - e->pend.n;
        if (rc) {
            check_dead(x, rc);
            return staged;
        }
        staged = 1;
        if (k) x->pend_at = (uint16_t)((i + 1) % n);
        if (last) e->pend_end = 0;
    }
    return staged;
}

static void rx_drop(struct xconn *x, struct xc_st *e) {
    size_t n = e->rx.n;
    q_clear(&e->rx);
    held_add(x, -(int64_t)n);
    if (x->state == XC_LIVE) h2m_conn_refund(&x->h2, (uint32_t)n);
}

static void ent_free(struct xconn *x, struct xc_st *e) {
    pend_drop(x, e);
    rx_drop(x, e);
    wh_drop(e);
    unmap(x, e);
    memset(e, 0, sizeof *e);
    e->h2slot = XC_NONE;
    x->nstreams--;
    if (!x->nstreams) x->idle_since_ms = x->now_ms;
}

static void ent_end(struct xconn *x, struct xc_st *e, int code) {
    pend_drop(x, e);
    unmap(x, e);
    e->ended = 1;
    e->code = code;
    if (e->owner == XS_NOWNER && !e->disc) ent_free(x, e);
    else if (e->owner != XS_NOWNER) wake(x, e, XW_READ, 0);
}

static void credit(struct xconn *x, struct xc_st *e, size_t n, int give) {
    uint16_t slot = e->h2slot != XC_NONE ? e->h2slot : UINT16_MAX;
    struct h2m_in *in = e->in.sid ? &e->in : NULL;
    if (give) {
        wh_drop(e);
    } else if (!e->withheld) {
        e->withheld = 1;
        g_wh_n++;
    }
    while (n && x->state == XC_LIVE && !x->h2.dead) {
        uint32_t k = n < XC_READ_UNIT ? (uint32_t)n : XC_READ_UNIT;
        h2m_consumed(&x->h2, slot, in, k, give);
        n -= k;
    }
}

static int x_emit(void *arg, const unsigned char *d, size_t n, int64_t gap) {
    struct xconn *x = arg;
    if (gap < 0) gap = 0;
    if (!n && !gap) return 0;
    if (!n && x->out.tail) {
        x->out.tail->gap += gap;
        return 0;
    }
    struct xc_chunk *c = chunk_sized(n);
    if (!c) return H2_EIO;
    if (n) memcpy(c->d, d, n);
    c->len = (uint32_t)n;
    c->gap = gap;
    c->pay = XC_PIECE_COST;
    x->q_pay += XC_PIECE_COST;
    if (x->shared) g_queued += XC_PIECE_COST;
    q_link(&x->out, c);
    return 0;
}

static int masked(struct xconn *x, const unsigned char *d, size_t n, uint32_t pt) {
    if (x->q_pay >= 4 * XC_OUT_HIGH) return TR_EIO;
    struct xc_chunk *t = x->out.tail;
    size_t had = x->out.n;
    int rc = fm_shape(x->link.tls.fm, d, n, x_emit, x);
    if (rc) return rc == H2_EIO ? rc : TR_EMASK;
    if (x->out.tail == t) {
        struct xc_chunk *c = chunk_sized(0);
        if (!c) return H2_EIO;
        q_link(&x->out, c);
    }
    size_t over = x->out.n - had > n ? x->out.n - had - n : 0;
    x->q_pay += over;
    if (x->shared) g_queued += over;
    x->out.tail->pt += pt;
    x->out.tail->pay += x->stage_pay + over;
    x->out.tail->ctl += x->stage_ctl;
    x->stage_pay = x->stage_ctl = 0;
    return 0;
}

static int seal(struct xconn *x) {
    struct xc_chunk *c = x->stage;
    if (!c) return 0;
    x->stage = NULL;
    if (!c->len) {
        chunk_put(c);
        return 0;
    }
    if (x->link.plain && x->link.tls.fm) {
        int rc = masked(x, c->d, c->len, c->len);
        chunk_put(c);
        return rc;
    }
    if (x->link.plain) {
        c->pay = x->stage_pay;
        x->stage_pay = 0;
        c->pt = c->len;
        c->ctl = x->stage_ctl;
        x->stage_ctl = 0;
        q_link(&x->out, c);
        return 0;
    }
    struct xconn_scratch *s = scratch();
    if (!s) {
        chunk_put(c);
        return H2_EIO;
    }
    size_t rn = 0;
    uint32_t pt = c->len;
    int rc = tls13_seal(&x->link.tls, c->d, c->len, s->seal, sizeof s->seal, &rn);
    chunk_put(c);
    if (rc) return rc;
    if (x->link.tls.fm) return masked(x, s->seal, rn, pt);
    if (q_push(&x->out, s->seal, rn)) return H2_EIO;
    x->out.tail->pt += pt;
    x->out.tail->pay += x->stage_pay;
    x->out.tail->ctl += x->stage_ctl;
    x->stage_pay = x->stage_ctl = 0;
    return 0;
}

static int x_out(void *ctx, const unsigned char *d, size_t n, unsigned flags) {
    struct xconn *x = ctx;
    if (x->in_dial) {
        if (x->pre_n + n > sizeof x->pre) return H2_ETOOBIG;
        memcpy(x->pre + x->pre_n, d, n);
        x->pre_n += n;
        return 0;
    }
    uint32_t cn = (flags & H2M_CTL) ? (uint32_t)n + (x->link.plain || !(flags & H2M_END) ? 0 : XC_REC_OVER) : 0;
    if (cn && x->q_ctl + cn > XC_CTL_MAX) return H2_EIO;
    x->pt_q += n;
    size_t p = (flags & H2M_PAY) ? n : 0;
    while (n) {
        if (!x->stage && !(x->stage = chunk_get())) return H2_EIO;
        struct xc_chunk *c = x->stage;
        size_t lim = x->link.plain ? XC_CHUNK : tls13_plain_cap(&x->link.tls);
        if (c->len >= lim) {
            int rc = seal(x);
            if (rc) return rc;
            continue;
        }
        size_t k = lim - c->len < n ? lim - c->len : n;
        memcpy(c->d + c->len, d, k);
        c->len += (uint32_t)k;
        d += k;
        n -= k;
        if (c->len == lim) {
            int rc = seal(x);
            if (rc) return rc;
        }
    }
    if (x->stage) {
        x->stage_pay += p;
        x->stage_ctl += cn;
    } else {
        x->out.tail->pay += p;
        x->out.tail->ctl += cn;
    }
    x->q_pay += p;
    x->q_ctl += cn;
    if (x->q_ctl >= XC_CTL_HOLD) h2m_hold(&x->h2, 1);
    if (x->shared) g_queued += p;
    return (flags & H2M_END) && !x->link.plain ? seal(x) : 0;
}

static void disc_pay(struct xconn *x, struct xc_st *e) {
    if (!e->disc || x->pt_s < e->wu_at) return;
    uint64_t q = x->pt_q;
    uint32_t n = e->disc;
    e->disc = 0;
    credit(x, e, n, 1);
    if (x->pt_q != q) e->wu_at = x->pt_q;
}

static void nown_pay(struct xconn *x) {
    if (!x->nown_due) return;
    x->nown_due = 0;
    for (uint16_t i = 0; i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (!e->used || e->owner != XS_NOWNER || !e->disc) continue;
        if (x->discard) {
            disc_pay(x, e);
        } else {
            uint32_t n = e->disc;
            e->disc = 0;
            credit(x, e, n, 1);
        }
        if (e->ended && !e->disc) ent_free(x, e);
    }
}

static void x_data(void *ctx, uint16_t slot, const unsigned char *d, size_t n) {
    struct xconn *x = ctx;
    struct xc_st *e = by_slot(x, slot);
    if (!e) return;
    if (e->owner == XS_NOWNER) {
        e->disc += (uint32_t)n;
        x->nown_due = 1;
        return;
    }
    if (x->discard) {
        e->disc += (uint32_t)n;
        disc_pay(x, e);
        return;
    }
    size_t had = e->rx.n;
    if (q_push(&e->rx, d, n)) {
        x->oom = 1;
        held_add(x, (int64_t)(e->rx.n - had));
        h2m_conn_refund(&x->h2, (uint32_t)(n - (e->rx.n - had)));
        return;
    }
    held_add(x, (int64_t)n);
    wake(x, e, XW_READ, 0);
}

static void x_status(void *ctx, uint16_t slot, int status) {
    struct xconn *x = ctx;
    struct xc_st *e = by_slot(x, slot);
    if (!e) return;
    e->seen = 1;
    e->status = status;
    if (status == 200) return;
    h2m_reset(&x->h2, slot, XC_CANCEL);
    ent_end(x, e, H2_ESTATUS);
}

static void x_end(void *ctx, uint16_t slot, int code, const struct h2m_in *in) {
    struct xconn *x = ctx;
    struct xc_st *e = by_slot(x, slot);
    if (!e) return;
    e->in = *in;
    int c = H2_ERESET;
    e->reset = code != 0;
    if (code == H2_EFLOW) c = H2_EFLOW;
    else if (x->h2.goaway && e->sid > x->h2.goaway_last) c = XC_EGOAWAY;
    else if (code == XC_REFUSED_STREAM && !e->seen) c = XC_EREFUSED;
    if (c == XC_EGOAWAY) x->refused++;
    ent_end(x, e, c);
}

static void x_goaway(void *ctx, uint32_t last, uint32_t code) {
    (void)last;
    (void)code;
    ((struct xconn *)ctx)->retired = 1;
}

static void x_ping_ack(void *ctx) { (void)ctx; }

static void x_room(void *ctx, uint16_t slot) {
    struct xconn *x = ctx;
    if (g_pend) x->pend_due = 1;
    if (slot != XC_NONE) {
        struct xc_st *e = by_slot(x, slot);
        if (e) {
            e->win_wait = 0;
            wake(x, e, XW_ROOM, 0);
        }
        return;
    }
    for (uint16_t i = 0; i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (e->used && e->win_wait && !e->ended) {
            e->win_wait = 0;
            wake(x, e, XW_ROOM, 0);
        }
    }
}

static const struct h2m_cb XC_CB = { NULL, x_out, x_data, x_status, x_end, x_goaway, x_ping_ack, x_room };

static void list_add(struct xconn *x) {
    if (!x->shared || x->listed) return;
    x->prev = NULL;
    x->next = g_list;
    if (g_list) g_list->prev = x;
    g_list = x;
    x->listed = 1;
}

static void list_del(struct xconn *x) {
    if (!x->listed) return;
    if (x->prev) x->prev->next = x->next;
    else g_list = x->next;
    if (x->next) x->next->prev = x->prev;
    x->prev = x->next = NULL;
    x->listed = 0;
}

static void pause_link(struct xconn *x) {
    if (x->paused) return;
    if (!g_paused_n++) {
        g_since_ms = g_now_ms;
        g_try_ms = 0;
    }
    x->paused = 1;
}

static void unpause(struct xconn *x) {
    if (!x->paused) return;
    x->paused = 0;
    g_paused_n--;
    x->last_rx_ms = x->now_ms;
    if (x->h2.ping_out) x->ping_at_ms = x->now_ms;
}

static void pause_largest(void) {
    struct xconn *b = NULL;
    for (struct xconn *y = g_list; y; y = y->next)
        if (y->state == XC_LIVE && bounded(y) && !y->paused && (!b || y->held_n > b->held_n)) b = y;
    if (b) pause_link(b);
}

static void budget(void) {
    if (g_paused_n && g_held < (int64_t)g_resume)
        for (struct xconn *y = g_list; y; y = y->next) unpause(y);
    if (!g_wh_n || g_held >= (int64_t)g_soft) return;
    for (struct xconn *y = g_list; y; y = y->next) {
        for (uint16_t i = 0; i < y->st_cap; i++) {
            struct xc_st *e = &y->st[i];
            if (!e->used || !e->withheld) continue;
            wh_drop(e);
            if (y->state != XC_LIVE || y->h2.dead) continue;
            h2m_stream_flush(&y->h2, e->h2slot != XC_NONE ? e->h2slot : UINT16_MAX, e->in.sid ? &e->in : NULL);
        }
        check_dead(y, 0);
    }
}

struct xconn *xconn_new(const struct tr_node *n, int64_t keepalive_s, int shared) {
    if (!scratch()) return NULL;
    struct xconn *x = calloc(1, sizeof *x);
    if (!x) return NULL;
    x->node = *n;
    x->link.fd = -1;
    x->state = XC_DIALING;
    x->shared = shared ? 1 : 0;
    x->keepalive_ms = keepalive_s == 0 ? XC_KEEPALIVE_MS : keepalive_s < 0 ? -1 : keepalive_s * 1000;
    for (unsigned i = 0; i < H2M_STREAMS; i++) x->map[i] = XC_NONE;
    if (x->shared) g_links++;
    return x;
}

void xconn_discard(struct xconn *x) { x->discard = 1; }

int xconn_start(struct xconn *x) {
    if (x->state != XC_DIALING || x->link.fd < 0) return H2_EPROTO;
    int rc = 0;
    {
        struct h2m_cb cb = XC_CB;
        cb.ctx = x;
        x->in_dial = 1;
        x->pre_n = 0;
        unsigned char sb[8];
        if (os_rand_seed(sb, sizeof sb)) memset(sb, 0x5a, sizeof sb);
        uint64_t seed = 0;
        for (size_t i = 0; i < sizeof sb; i++) seed = seed << 8 | sb[i];
        rc = h2m_start(&x->h2, &cb, seed);
        x->in_dial = 0;
        if (!rc) rc = tr_link_write(&x->link, x->pre, x->pre_n);
        if (rc) {
            h2m_free(&x->h2);
            tr_link_close(&x->link);
        }
    }
    x->rc = rc;
    if (rc) x->state = XC_DEAD;
    else if (!x->shared) x->state = XC_LIVE;
    return rc;
}

static int dial_failed(struct xconn *x, int rc) {
    x->rc = rc;
    x->state = XC_DEAD;
    return rc;
}

int xconn_dial(struct xconn *x, int timeout_s) {
    if (x->state != XC_DIALING) return x->state == XC_DEAD ? x->rc : H2_EPROTO;
    if (!scratch()) return dial_failed(x, H2_EIO);
    int rc = tr_link_open(&x->link, &x->node, "h2", timeout_s);
    return rc ? dial_failed(x, rc) : xconn_start(x);
}

int xconn_dial2(struct xconn *a, struct xconn *b, int timeout_s) {
    if (a->state != XC_DIALING || b->state != XC_DIALING) return H2_EPROTO;
    int rc = tr_link_open2(&a->link, &a->node, &b->link, &b->node, "h2", timeout_s);
    if (rc) {
        dial_failed(b, rc);
        return dial_failed(a, rc);
    }
    rc = xconn_start(a);
    if (rc) {
        tr_link_close(&b->link);
        return dial_failed(b, rc);
    }
    rc = xconn_start(b);
    if (rc) xconn_fail(a, rc);
    return rc;
}

int xconn_adopt(struct xconn *x, uint64_t now_ms) {
    if (x->state == XC_DEAD) return x->rc;
    if (x->state != XC_DIALING || x->link.fd < 0) return H2_EPROTO;
    int rc = scratch() ? tr_link_nonblock(&x->link) : H2_EIO;
    if (rc) {
        x->state = XC_LIVE;
        xconn_fail(x, rc);
        return rc;
    }
    x->state = XC_LIVE;
    x->now_ms = x->last_rx_ms = x->idle_since_ms = g_now_ms = now_ms;
    list_add(x);
    return 0;
}

static void low_water(void);
static void pend_kick(void);

static void fail_in(struct xconn *x, int err) {
    if (x->state == XC_DEAD) return;
    if (!err) err = TR_EIO;
    for (uint16_t i = 0; i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (!e->used) continue;
        pend_drop(x, e);
        if (e->ended) {
            if (e->owner == XS_NOWNER) ent_free(x, e);
            continue;
        }
        x->cut_n++;
        unmap(x, e);
        e->ended = 1;
        e->code = err;
        if (e->owner == XS_NOWNER) ent_free(x, e);
        else wake(x, e, XW_READ, err);
    }
    if (x->stage) {
        chunk_put(x->stage);
        x->stage = NULL;
    }
    q_clear(&x->out);
    if (x->shared) g_queued -= x->q_pay;
    x->q_pay = x->stage_pay = 0;
    x->q_ctl = x->stage_ctl = 0;
    if (x->paused) {
        x->paused = 0;
        g_paused_n--;
    }
    x->state = XC_DEAD;
    x->rc = err;
    if (x->shared) {
        low_water();
        pend_kick();
    }
}

void xconn_fail(struct xconn *x, int err) {
    fail_in(x, err);
    wq_run();
}

static int check_dead(struct xconn *x, int rc) {
    if (x->state == XC_LIVE && (x->h2.dead || x->oom)) fail_in(x, x->h2.dead ? x->h2.dead : H2_EIO);
    if (x->state == XC_DEAD) return x->rc;
    return rc;
}

static int feed(struct xconn *x, const unsigned char *d, size_t n) {
    int rc = h2m_feed(&x->h2, d, n);
    if (rc || x->oom) {
        fail_in(x, rc ? rc : H2_EIO);
        return x->rc;
    }
    nown_pay(x);
    if (x->pend_due) pend_flush(x);
    rc = check_dead(x, 0);
    budget();
    return rc;
}

static int buffered(const struct xconn *x) {
    return (!x->link.plain && tls13_has_record(&x->link.tls)) || fm_pending(x->link.tls.fm);
}

static int at_ceiling(const struct xconn *x) {
    return bounded(x) && g_held >= (int64_t)g_hard && !buffered(x);
}

static int read_pass(struct xconn *x, uint64_t now) {
    size_t total = 0;
    while (total < XC_READ_MAX || buffered(x)) {
        if (x->link.plain) {
            struct xconn_scratch *s = scratch();
            if (!s) return H2_EIO;
            ssize_t r = tr_lk_recv(&x->link, s->rbuf, sizeof s->rbuf, MSG_DONTWAIT);
            if (r == 0) return TR_ECLOSED;
            if (r < 0) {
                if (errno == EINTR) continue;
                if (errno == EAGAIN || errno == EWOULDBLOCK) return 0;
                return tr_lk_err(&x->link, r);
            }
            x->last_rx_ms = now;
            total += (size_t)r;
            int rc = feed(x, s->rbuf, (size_t)r);
            if (rc) return rc;
            if (at_ceiling(x)) return 0;
            continue;
        }
        uint64_t seq = x->link.tls.rd_seq;
        const unsigned char *body = NULL;
        size_t n = 0;
        int rc = tls13_read_ref(&x->link.tls, &body, &n);
        if (rc) return rc;
        if (!n) {
            if (x->link.tls.rd_seq == seq && !buffered(x)) return 0;
            x->last_rx_ms = now;
            continue;
        }
        x->last_rx_ms = now;
        total += n;
        rc = feed(x, body, n);
        if (rc) return rc;
        if (at_ceiling(x)) return 0;
    }
    return 0;
}

static int read_shared(struct xconn *x, uint64_t now) {
    if (bounded(x) && g_held >= (int64_t)g_hard) {
        pause_link(x);
        return 0;
    }
    int rc = read_pass(x, now);
    if (!rc && g_held >= (int64_t)g_hard) pause_largest();
    return rc;
}

static int read_one(struct xconn *x) {
    if (x->link.plain) {
        struct xconn_scratch *s = scratch();
        if (!s) return H2_EIO;
        ssize_t r = tr_lk_recv(&x->link, s->rbuf, sizeof s->rbuf, MSG_DONTWAIT);
        if (r == 0) return TR_ECLOSED;
        if (r < 0) return errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR ? 0 : tr_lk_err(&x->link, r);
        return feed(x, s->rbuf, (size_t)r);
    }
    const unsigned char *body = NULL;
    size_t n = 0;
    int rc = tls13_read_ref(&x->link.tls, &body, &n);
    if (rc) return rc;
    return n ? feed(x, body, n) : 0;
}

static void low_water(void) {
    if (g_queued >= XC_OUT_LOW) return;
    for (struct xconn *y = g_list; y; y = y->next)
        for (uint16_t i = 0; i < y->st_cap; i++) {
            struct xc_st *e = &y->st[i];
            if (e->used && e->starved && !e->ended) {
                e->starved = 0;
                wake(y, e, XW_ROOM, 0);
            }
        }
}

static void release(struct xconn *x) {
    h2m_hold(&x->h2, 0);
    for (uint16_t i = 0; i < x->st_cap && !x->h2.dead; i++) {
        struct xc_st *e = &x->st[i];
        if (e->used && e->h2slot == XC_NONE && e->in.sid && e->in.unsent) h2m_stream_flush(&x->h2, UINT16_MAX, &e->in);
    }
}

static int flush_in(struct xconn *x);

static __thread uint8_t g_kicking;

static void pend_kick(void) {
    if (!g_pend || g_kicking) return;
    g_kicking = 1;
    for (struct xconn *y = g_list; y; y = y->next) {
        if (y->state != XC_LIVE || !pend_flush(y) || y->state != XC_LIVE) continue;
        int rc = seal(y);
        if (rc) fail_in(y, rc);
        else flush_in(y);
    }
    g_kicking = 0;
}

static int flush_in(struct xconn *x) {
    if (x->state != XC_LIVE) return x->state == XC_DEAD ? x->rc : 0;
    int sent = 0, done = 0;
    for (uint16_t i = 0; i < x->st_cap; i++) sent |= x->st[i].up_cur;
    for (uint16_t i = 0; sent && i < x->st_cap; i++) {
        x->st[i].up_last = x->st[i].up_cur;
        x->st[i].up_cur = 0;
    }
    int rc = x->link.plain ? seal(x) : 0;
    if (rc) {
        fail_in(x, rc);
        return rc;
    }
    uint64_t now = 0;
    while (x->out.head) {
        if (x->out_due) {
            if (!now) now = tr_mono_ms();
            if (now < x->out_due) break;
            x->out_due = 0;
        }
        struct xc_chunk *h = x->out.head;
        if (h->off < h->len) {
            ssize_t w = send(x->link.fd, h->d + h->off, h->len - h->off, MSG_DONTWAIT | MSG_NOSIGNAL);
            if (w < 0) {
                if (errno == EINTR) continue;
                if (errno == EAGAIN || errno == EWOULDBLOCK) break;
                fail_in(x, TR_EIO);
                return TR_EIO;
            }
            h->off += (uint32_t)w;
            x->out.n -= (size_t)w;
            if (h->off < h->len) continue;
        }
        x->out.head = h->next;
        if (!x->out.head) x->out.tail = NULL;
        x->pt_s += h->pt;
        x->q_pay -= h->pay;
        x->q_ctl -= h->ctl;
        if (x->shared) g_queued -= h->pay;
        if (h->gap > 0) {
            if (!now) now = tr_mono_ms();
            x->out_due = now + (uint64_t)h->gap;
        }
        chunk_put(h);
        done = 1;
    }
    if (x->h2.hold && x->q_ctl < XC_CTL_RESUME) release(x);
    for (uint16_t i = 0; x->discard && i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (!e->used) continue;
        disc_pay(x, e);
        if (e->owner == XS_NOWNER && e->ended && !e->disc) ent_free(x, e);
    }
    if ((rc = check_dead(x, 0))) return rc;
    if (done) pend_kick();
    if (x->state != XC_LIVE) return x->state == XC_DEAD ? x->rc : 0;
    if (x->shared) low_water();
    return 0;
}

int xconn_flush(struct xconn *x) {
    int rc = flush_in(x);
    budget();
    wq_run();
    return rc;
}

static int flush_wake(struct xconn *x) {
    int rc = flush_in(x);
    wq_run();
    return rc;
}

static int has_out(const struct xconn *x) {
    return x->out.head || (x->stage && x->stage->len);
}

int xconn_due(struct xconn *x, uint64_t now_ms) {
    if (x->state != XC_LIVE || !x->out_due || !has_out(x)) return -1;
    if (now_ms >= x->out_due) return 0;
    uint64_t w = x->out_due - now_ms;
    return w > INT32_MAX ? INT32_MAX : (int)w;
}

int xconn_wants_in(const struct xconn *x) { return x->state == XC_LIVE && !x->paused; }

int xconn_wants_out(const struct xconn *x) {
    return x->state == XC_LIVE && has_out(x) && (!x->out_due || tr_mono_ms() >= x->out_due);
}

int xconn_io(struct xconn *x, uint32_t ev, uint64_t now_ms) {
    if (x->state == XC_DEAD) return x->rc;
    if (x->state != XC_LIVE) return H2_EPROTO;
    x->now_ms = g_now_ms = now_ms;
    int rc = 0;
    if (x->paused && (ev & (EPOLLHUP | EPOLLERR))) {
        rc = TR_EIO;
        fail_in(x, rc);
    } else if (!x->paused && (ev & (EPOLLIN | EPOLLHUP | EPOLLERR))) {
        rc = read_shared(x, now_ms);
        if (rc) fail_in(x, rc);
    }
    if (!rc && (ev & EPOLLOUT)) rc = flush_in(x);
    wq_run();
    return x->state == XC_DEAD ? x->rc : rc;
}

static void orphans(struct xconn *x) {
    for (uint16_t i = 0; i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (!e->used || e->ended || !e->post || !e->local_end || e->h2slot == XC_NONE) continue;
        if (e->seen && e->owner != XS_NOWNER) continue;
        if (x->now_ms < e->half_at + XC_ORPHAN_MS) continue;
        h2m_reset(&x->h2, e->h2slot, XC_CANCEL);
        ent_end(x, e, H2_ERESET);
    }
}

static int stall_reset(uint64_t now) {
    struct xconn *bx = NULL;
    struct xc_st *be = NULL;
    g_try_ms = now + XC_STALL_NEXT_MS;
    for (struct xconn *y = g_list; y; y = y->next)
        for (uint16_t i = 0; i < y->st_cap; i++) {
            struct xc_st *e = &y->st[i];
            if (!e->used || e->owner == XS_NOWNER || !e->rx.n) continue;
            uint64_t at = (e->read_ms > g_since_ms ? e->read_ms : g_since_ms) + XC_STALL_MS;
            if (now < at) {
                if (at < g_try_ms) g_try_ms = at;
                continue;
            }
            if (be && e->rx.n <= be->rx.n) continue;
            bx = y;
            be = e;
        }
    if (!be) return 0;
    if (bx->state == XC_LIVE && be->h2slot != XC_NONE) {
        h2m_reset(&bx->h2, be->h2slot, XC_CANCEL);
        unmap(bx, be);
    }
    pend_drop(bx, be);
    rx_drop(bx, be);
    wh_drop(be);
    be->ended = 1;
    be->reset = 1;
    be->code = H2_ERESET;
    wake(bx, be, XW_READ, H2_ERESET);
    check_dead(bx, 0);
    return 1;
}

int xconn_tick(struct xconn *x, uint64_t now_ms) {
    if (x->state == XC_DEAD) return x->rc;
    if (x->state != XC_LIVE) return 0;
    x->now_ms = g_now_ms = now_ms;
    if (x->h2.ping_out && !x->paused) {
        if (now_ms >= x->ping_at_ms + XC_PING_TIMEOUT_MS) {
            xconn_fail(x, XC_EPING);
            return XC_EPING;
        }
    } else if (!x->h2.ping_out && !x->paused && x->keepalive_ms >= 0 &&
               now_ms >= x->last_rx_ms + (uint64_t)x->keepalive_ms) {
        unsigned char op[8];
        if (os_rand_seed(op, sizeof op)) memset(op, 0x5a, sizeof op);
        h2m_ping(&x->h2, op);
        x->ping_at_ms = now_ms;
    }
    orphans(x);
    if (x->paused && now_ms >= g_try_ms && stall_reset(now_ms)) g_try_ms = now_ms + XC_STALL_NEXT_MS;
    int rc = check_dead(x, 0);
    budget();
    wq_run();
    if (rc) return rc;
    if (!x->nstreams && (x->retired || now_ms >= x->idle_since_ms + XC_IDLE_MS)) return XC_IDLE;
    return 0;
}

unsigned xconn_streams(const struct xconn *x) { return x->nstreams; }

void xconn_free(struct xconn *x) {
    if (!x) return;
    list_del(x);
    for (uint16_t i = 0; i < x->st_cap; i++) {
        struct xc_st *e = &x->st[i];
        if (!e->used) continue;
        pend_drop(x, e);
        size_t n = e->rx.n;
        q_clear(&e->rx);
        held_add(x, -(int64_t)n);
        wh_drop(e);
    }
    if (x->paused) {
        x->paused = 0;
        g_paused_n--;
    }
    free(x->st);
    if (x->stage) chunk_put(x->stage);
    q_clear(&x->out);
    if (x->shared) g_queued -= x->q_pay;
    h2m_free(&x->h2);
    tr_link_close(&x->link);
    int shared = x->shared;
    free(x);
    if (shared && g_links && !--g_links) free_list_drop();
    if (shared) {
        low_water();
        pend_kick();
        budget();
        wq_run();
    }
}

static int ent_new(struct xconn *x, uint16_t *idx) {
    for (uint16_t i = 0; i < x->st_cap; i++)
        if (!x->st[i].used) {
            *idx = i;
            return 0;
        }
    if (x->st_cap >= XC_ENTRIES) return H2_EFULL;
    uint16_t cap = x->st_cap ? (uint16_t)(x->st_cap * 2) : XC_ST_FIRST;
    struct xc_st *st = realloc(x->st, cap * sizeof *st);
    if (!st) return H2_EFULL;
    memset(st + x->st_cap, 0, (size_t)(cap - x->st_cap) * sizeof *st);
    for (uint16_t i = x->st_cap; i < cap; i++) st[i].h2slot = XC_NONE;
    *idx = x->st_cap;
    x->st = st;
    x->st_cap = cap;
    return 0;
}

int xs_open(struct xconn *x, const struct h2_req *r, uint16_t owner, struct xs *s) {
    s->x = NULL;
    if (x->state == XC_DEAD) return x->rc;
    if (x->state != XC_LIVE) return H2_EPROTO;
    if (x->retired) return H2_ERESET;
    uint16_t idx;
    int rc = ent_new(x, &idx);
    if (rc) return rc;
    uint16_t hs = XC_NONE;
    rc = h2m_open(&x->h2, r, &hs);
    if (rc) return check_dead(x, rc);
    struct xc_st *e = &x->st[idx];
    memset(e, 0, sizeof *e);
    e->used = 1;
    e->owner = owner;
    e->h2slot = hs;
    e->sid = x->h2.st[hs].sid;
    e->local_end = r->end_stream ? 1 : 0;
    e->post = !e->local_end;
    e->half_at = x->now_ms;
    x->map[hs] = idx;
    x->nstreams++;
    s->x = x;
    s->slot = idx;
    s->sid = e->sid;
    if (!x->shared) return flush_wake(x);
    return 0;
}

static unsigned uploaders(const struct xconn *x, uint16_t owner) {
    struct xconn_scratch *s = scratch_now();
    if (s && !++s->mark_ep) {
        memset(s->mark, 0, sizeof s->mark);
        s->mark_ep = 1;
    }
    unsigned u = 1;
    for (uint16_t i = 0; i < x->st_cap; i++) {
        const struct xc_st *e = &x->st[i];
        if (!e->used || !e->up_last || e->owner == owner) continue;
        if (s && e->owner < XC_MARKS) {
            if (s->mark[e->owner] == s->mark_ep) continue;
            s->mark[e->owner] = s->mark_ep;
        }
        u++;
    }
    return u;
}

static long q_free(const struct xconn *x) {
    long q = (long)(x->shared ? g_queued + g_pend : x->q_pay);
    return q < XC_OUT_HIGH ? (XC_OUT_HIGH - q) / (long)fm_grow(x->link.tls.fm) : 0;
}

static long admit(const struct xconn *x, uint16_t owner, struct xc_st *e) {
    long free_q = q_free(x);
    long c = h2m_conn_room(&x->h2) - (long)x->pend_n;
    if (c < 0) c = 0;
    long a = (c < free_q ? c : free_q) / (long)uploaders(x, owner);
    if (a >= XC_MSS) return a;
    if (free_q < c) {
        e->starved = 1;
        return fm_grow(x->link.tls.fm) > 1 ? 0 : XC_MSS;
    }
    e->win_wait = 1;
    return XC_MSS;
}

long xs_room(const struct xs *s) {
    struct xc_st *e = ent(s);
    if (!e) return 0;
    struct xconn *x = s->x;
    if (x->state != XC_LIVE || e->ended || e->local_end || e->h2slot == XC_NONE) return 0;
    long w = h2m_room(&x->h2, e->h2slot);
    if (!x->shared) {
        if (w <= 0) {
            e->win_wait = 1;
            return 0;
        }
        long a = admit(x, e->owner, e);
        if (w < a) e->win_wait = 1;
        return a < w ? a : w;
    }
    if (g_queued >= XC_OUT_HIGH) {
        e->starved = 1;
        return 0;
    }
    long a = admit(x, e->owner, e);
    if (w < a) e->win_wait = 1;
    long base = w <= 0 ? 0 : a < w ? a : w;
    long u = (long)uploaders(x, e->owner);
    long head = XC_PEND_STREAM - (long)e->pend.n;
    if (head < 0) head = 0;
    long qa = q_free(x) / u, r = base + head;
    if (r > qa) r = qa > base ? qa : base;
    long left = (long)g_pend_max - (long)g_pend;
    left = left > 0 ? left / u : 0;
    if (r - base > left) r = base + left;
    return r;
}

int xc_slot_free(const struct xconn *x) {
    return x->state == XC_LIVE && !x->retired && !x->h2.goaway && h2m_open_n(&x->h2) < h2m_cap(&x->h2);
}

size_t xc_pend_free(void) { return g_pend < g_pend_max ? g_pend_max - g_pend : 0; }

long xc_post_room(const struct xconn *x, size_t n) {
    if (x->state != XC_LIVE || x->retired || x->h2.goaway || h2m_open_n(&x->h2) >= h2m_cap(&x->h2)) return 0;
    long iw = x->h2.peer_init_win;
    long c = h2m_conn_room(&x->h2) - (long)x->pend_n;
    long g = iw < c ? iw : c;
    long need = n < XC_CHUNK ? (long)n : XC_CHUNK;
    if (!n || g < need) return 0;
    return (long)n < iw ? (long)n : iw;
}

static int send_src(const struct xs *s, const unsigned char *d, struct xc_q *q, size_t n, int end) {
    struct xc_st *e = ent(s);
    if (!e) return H2_ERESET;
    struct xconn *x = s->x;
    if (x->state == XC_DEAD) return x->rc;
    if (x->state != XC_LIVE) return H2_EPROTO;
    if (e->ended || e->h2slot == XC_NONE) return e->code ? e->code : H2_ERESET;
    if (e->pend_end || (q && n > q->n)) return H2_EPROTO;
    long w = h2m_room(&x->h2, e->h2slot);
    size_t k = n;
    int behind = x->shared && e->pend.n;
    if (x->shared && (e->pend.n || (long)n > w)) {
        k = e->pend.n || w <= 0 ? 0 : (size_t)w;
        if (g_pend + (n - k) > g_pend_max) return H2_EWINDOW;
    }
    int fin = end && k == n && !behind;
    if (k || fin) {
        int rc = h2m_send_begin(&x->h2, e->h2slot, k, fin);
        if (!rc && k) rc = q ? q_put(x, q, k) : h2m_send_put(&x->h2, d, k);
        if (rc) return check_dead(x, rc);
    }
    if (k < n || (end && !fin)) {
        size_t had = e->pend.n;
        int rc = q ? q_move(&e->pend, q, n - k) : q_push(&e->pend, d + k, n - k);
        g_pend += e->pend.n - had;
        x->pend_n += e->pend.n - had;
        if (rc) {
            x->oom = 1;
            return check_dead(x, H2_EIO);
        }
        e->pend_end = end ? 1 : 0;
    }
    if (n) e->up_cur = 1;
    if (end) {
        e->local_end = 1;
        e->half_at = x->now_ms;
    }
    if (!x->shared) return flush_wake(x);
    return 0;
}

int xs_send(const struct xs *s, const unsigned char *d, size_t n, int end) {
    return send_src(s, d, NULL, n, end);
}

int xs_send_q(const struct xs *s, struct xc_q *q, size_t n, int end) {
    return send_src(s, NULL, q, n, end);
}

int xs_read(const struct xs *s, unsigned char *out, size_t cap, size_t *got) {
    *got = 0;
    struct xc_st *e = ent(s);
    if (!e) return H2_ERESET;
    struct xconn *x = s->x;
    if (!x->shared && !e->rx.n && !e->ended && x->state == XC_LIVE) {
        int rc = read_one(x);
        if (rc) fail_in(x, rc);
        if (x->state == XC_LIVE) flush_in(x);
    }
    if (e->rx.n) {
        size_t k = q_pop(&e->rx, out, cap);
        *got = k;
        held_add(x, -(int64_t)k);
        if (k && bounded(x)) e->read_ms = x->now_ms > g_now_ms ? x->now_ms : g_now_ms;
        if (x->state == XC_LIVE) {
            credit(x, e, k, !(bounded(x) && g_held >= (int64_t)g_soft && e->rx.n));
            check_dead(x, 0);
            if (!x->shared && x->state == XC_LIVE) flush_in(x);
        }
        budget();
        wq_run();
        return 0;
    }
    wq_run();
    if (e->ended) return e->code;
    return 0;
}

int xc_q_push(struct xc_q *q, const unsigned char *d, size_t n) { return q_push(q, d, n); }

size_t xc_q_peek(const struct xc_q *q, unsigned char *out, size_t cap) {
    size_t got = 0;
    for (const struct xc_chunk *h = q->head; h && got < cap; h = h->next) {
        size_t k = h->len - h->off < cap - got ? h->len - h->off : cap - got;
        memcpy(out + got, h->d + h->off, k);
        got += k;
    }
    return got;
}

int xc_q_copy(struct xc_q *dst, const struct xc_q *src, size_t n) {
    for (const struct xc_chunk *h = src->head; h && n; h = h->next) {
        size_t k = h->len - h->off < n ? h->len - h->off : n;
        if (q_push(dst, h->d + h->off, k)) return -1;
        n -= k;
    }
    return 0;
}

size_t xc_q_pop(struct xc_q *q, unsigned char *out, size_t cap) { return q_pop(q, out, cap); }

void xc_q_drop(struct xc_q *q) { q_clear(q); }

size_t xc_q_skip(struct xc_q *q, size_t n) {
    size_t got = 0;
    while (got < n && q->head) {
        struct xc_chunk *h = q->head;
        size_t k = h->len - h->off < n - got ? h->len - h->off : n - got;
        q_skip(q, k);
        got += k;
    }
    return got;
}

int xs_status(const struct xs *s) {
    struct xc_st *e = ent(s);
    return e ? e->status : 0;
}

int xs_ready(const struct xs *s) {
    struct xc_st *e = ent(s);
    return !e || e->rx.n || e->ended;
}

int xs_failed(const struct xs *s) {
    struct xc_st *e = ent(s);
    return !e || (e->ended && (e->reset || e->status != 200 || e->code != H2_ERESET));
}

void xs_close(struct xs *s) {
    struct xc_st *e = ent(s);
    struct xconn *x = s->x;
    s->x = NULL;
    if (!e) return;
    int keep = 0;
    if (x->state == XC_LIVE && e->h2slot != XC_NONE) {
        if (e->post && e->local_end && !e->seen) {
            rx_drop(x, e);
            e->owner = XS_NOWNER;
            keep = 1;
        } else {
            h2m_reset(&x->h2, e->h2slot, XC_CANCEL);
            unmap(x, e);
        }
    }
    if (!keep) {
        if (x->state == XC_LIVE && e->disc) credit(x, e, e->disc, 1);
        ent_free(x, e);
    }
    if (x->state == XC_LIVE) {
        check_dead(x, 0);
        if (!x->shared && x->state == XC_LIVE) flush_in(x);
    }
    budget();
    wq_run();
}

static int room_open(const struct xs *s) {
    struct xc_st *e = ent(s);
    return e && !e->ended && !e->local_end && e->h2slot != XC_NONE && h2m_room(&s->x->h2, e->h2slot) > 0;
}

int xs_wait(const struct xs *s, int timeout_ms) {
    if (!ent(s)) return 1;
    struct xconn *x = s->x;
    uint64_t dl = tr_mono_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 0);
    int had_room = room_open(s);
    for (;;) {
        if (xs_ready(s)) return 1;
        if (x->state != XC_LIVE) return 1;
        if (!had_room && room_open(s)) return 0;
        int queued = xconn_wants_out(x);
        if (buffered(x)) {
            int rc = read_one(x);
            if (rc) fail_in(x, rc);
            else flush_in(x);
            continue;
        }
        uint64_t now = tr_mono_ms();
        if (now >= dl) return 0;
        int gap = xconn_due(x, now);
        if (!gap) {
            flush_in(x);
            continue;
        }
        int tmo = (int)(dl - now);
        if (gap > 0 && gap < tmo) tmo = gap;
        struct pollfd p = { .fd = x->link.fd, .events = (short)(POLLIN | (queued ? POLLOUT : 0)) };
        int pr = poll(&p, 1, tmo);
        if (pr < 0) {
            if (errno == EINTR) continue;
            fail_in(x, TR_EIO);
            continue;
        }
        if (!pr) continue;
        if (p.revents & POLLOUT) {
            if (flush_in(x)) continue;
            if (queued && !xconn_wants_out(x)) return 0;
        }
        if (p.revents & (POLLIN | POLLHUP | POLLERR)) {
            int rc = read_one(x);
            if (rc) fail_in(x, rc);
            else flush_in(x);
        }
    }
}
