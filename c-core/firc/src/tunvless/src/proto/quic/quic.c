/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#define _GNU_SOURCE
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/udp.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/uio.h>

#include <ngtcp2/ngtcp2.h>
#include <ngtcp2/ngtcp2_crypto.h>

#include "quic.h"
#include "qcssl.h"
#include "ngcc.h"
#include "osrand.h"
#include "tscratch.h"

#ifndef UDP_SEGMENT
#define UDP_SEGMENT 103
#endif
#ifndef UDP_GRO
#define UDP_GRO 104
#endif
#ifndef SOL_UDP
#define SOL_UDP 17
#endif
#ifndef IP_PMTUDISC_PROBE
#define IP_PMTUDISC_PROBE 3
#endif
#ifndef IPV6_PMTUDISC_PROBE
#define IPV6_PMTUDISC_PROBE 3
#endif

#define QC_TXBUF        (64 * 1024)
#define QC_DG_QUEUE     64
#define QC_FILTER_PAD   64
#define QC_GSO_SEGS     64
#define QC_GSO_BYTES    65507
#define QC_OBUF         (QC_TXBUF + QC_FILTER_PAD * QC_GSO_SEGS)
#define QC_RX_BATCH     32
#define QC_RX_SLOT      2048
#define QC_RX_GRO_BATCH 16
#define QC_RX_GRO_SLOT  65536
#define QC_CID_LEN      4
#define QC_PATH_MAX     1452
#define QC_PROBE_LOW    1280
#define QC_NOPMTUD_SIZE 1250
#define QC_RCVBUF       (1 << 20)
#define QC_SNDBUF       (1 << 20)
#define QC_RX_CALL      128
#define QC_RX_CALL_BATCHES 4
#define QC_GRO_SEGS     64
#define QC_EXEMPT       4096
#define QC_SEND_TOTAL   (16u << 20)
#define QC_INITCWND     32
#define QC_BRUTAL_SPAN_MAX (16u << 20)
#define QC_HOP_FLOOR_S  5
#define QC_HOP_DEFAULT_S 30

#define QC_CHUNK 32768
#define QC_CHUNK_MIN 4096
struct qc_chunk {
    struct qc_chunk *next;
    uint64_t off;
    size_t len, cap;
    uint8_t data[];
};

struct qc_stream {
    int64_t id;
    struct qc_chunk *head, *tail, *cur;
    uint64_t total, sent, acked;
    int fin_req, fin_sent, blocked, want_room, full;
    int in_pend, in_wr, in_bw;
    struct qc_stream *pprev, *pnext, *wprev, *wnext, *bprev, *bnext;
};

struct qc_stab {
    struct qc_stream **slot;
    size_t cap, n;
};

struct qc_dg {
    uint8_t *d;
    size_t n;
};

struct qc_rx {
    struct mmsghdr mm[QC_RX_BATCH];
    struct iovec iov[QC_RX_BATCH];
    struct sockaddr_storage from[QC_RX_BATCH];
    union { char b[CMSG_SPACE(sizeof(int))]; struct cmsghdr a; } ctl[QC_RX_BATCH];
};

struct qc_rxs {
    struct qc_rx rx;
    uint8_t *fbuf;
    uint8_t *buf;
};

struct qc_txs {
    uint8_t txbuf[QC_TXBUF];
    uint8_t obuf[QC_OBUF];
};

struct qc_scratch {
    int rx_busy, tx_busy;
    struct qc_txs tx;
    struct qc_rxs rx;
    uint8_t rx_fbuf[QC_FILTER_RX_MAX];
    uint8_t rx_buf[QC_RX_GRO_BATCH * QC_RX_GRO_SLOT];
};

#ifdef QC_WITH_SERVER
static unsigned long g_nested_n;
static size_t g_nested_max;
#endif

struct qc {
    struct ngcc_tally tally;
    struct qc_ops ops;
    void *user;
    int fd;
    int ep, pre;
    int server;
    struct sockaddr_storage local, remote;
    socklen_t local_len, remote_len;

    ngtcp2_conn *conn;
    int cc;
    ngtcp2_crypto_conn_ref ref;
    void *ssl;
    void *ctx;
    int own_ctx;
    uint8_t secret[32];
    size_t datagram_max;
    size_t send_buf;
    size_t overhead;
    char alpn[256];
    uint64_t max_data, max_stream_data, max_streams;
    unsigned idle_ms;
    unsigned keepalive_ms;
    unsigned idle_local_ms;

    struct qc_stab tab;
    struct qc_stream *pend_head, *pend_tail, *wr_head, *wr_tail, *bw_head, *bw_tail;
    size_t send_total, queued, brutal_span;
    int defer, want_flush, rx_more, mtu_lost;
    uint64_t now;
    uint64_t rx_last, rx_pkts, rx_long, tx_wait, ka_ns;
    int ping_req;
    uint64_t probe_steps, probe_ops;
    int stream_wait, sopen_pend;
    struct qc_dg dgq[QC_DG_QUEUE];
    unsigned dg_head, dg_n;
    uint64_t dg_dropped, dg_tx, dg_rx;
    uint64_t wait[3], hold_at, pay_tx, pay_rx;
    int hold, hit, blocked_n, full_n;

    int hs_done;
    int closed;
    int delivered;
    int close_reason, close_app;
    uint64_t close_code;
    const char *close_why;
    char why_buf[96];
    int in_cb;
    int pend_err;
    int close_req;
    uint64_t close_req_code;
    int tx_full;

    struct qc_rxs *rs;
    struct qc_txs *ts;
#ifdef QC_WITH_SERVER
    size_t held;
    unsigned w_rx, w_rx_max;
    size_t w_fl_max;
#endif
    uint64_t opened;
    int rs_own, ts_own, ts_depth;
    struct qc_filter filter;
    uint64_t tx_calls, rx_calls;
    int gso_ok;
    int gro_on;
    unsigned rcvbuf, sndbuf;
    unsigned hop_min_s, hop_max_s;
    size_t hop_n;
    struct port_range hop_ports[PORT_RANGES_MAX];
    uint32_t (*rand32)(void);
    uint64_t hop_due, hops, hop_fails, pre_end;
    uint16_t port;
    unsigned hop_mode;
    int (*prepare)(int fd);
#ifdef QC_WITH_SERVER
    uint64_t clock;
    int follow;
    struct sockaddr_storage peer;
    socklen_t peer_len;
#endif
};

_Static_assert(offsetof(struct qc, tally) == 0, "the congestion shim reads the tally at the connection's user data");

struct qc_tls {
    void *ctx;
};

static struct qc_scratch *scratch(void) {
    return ts_get(TS_QUIC, sizeof(struct qc_scratch));
}

static int rx_take(struct qc *q) {
    struct qc_scratch *g = scratch();
    if (g && !g->rx_busy) {
        g->rx_busy = 1;
        g->rx.fbuf = g->rx_fbuf;
        g->rx.buf = g->rx_buf;
        q->rs = &g->rx;
        q->rs_own = 0;
        return 0;
    }
    size_t fb = q->filter.rx ? QC_FILTER_RX_MAX : 0;
    size_t nb = q->gro_on ? (size_t)QC_RX_GRO_BATCH * QC_RX_GRO_SLOT : (size_t)QC_RX_BATCH * QC_RX_SLOT;
    size_t sz = sizeof(struct qc_rxs) + fb + nb;
    uint8_t *m = malloc(sz);
    q->rs_own = 1;
    q->rs = (struct qc_rxs *)m;
    if (!m) return -1;
    q->rs->fbuf = fb ? m + sizeof(struct qc_rxs) : NULL;
    q->rs->buf = m + sizeof(struct qc_rxs) + fb;
#ifdef QC_WITH_SERVER
    g_nested_n++;
    if (sz > g_nested_max) g_nested_max = sz;
#endif
    return 0;
}

static void rx_give(struct qc *q) {
    if (!q->rs) return;
    if (q->rs_own) free(q->rs);
    else scratch()->rx_busy = 0;
    q->rs = NULL;
}

static int tx_take(struct qc *q) {
    if (q->ts) {
        q->ts_depth++;
        return 0;
    }
    struct qc_scratch *g = scratch();
    if (g && !g->tx_busy) {
        g->tx_busy = 1;
        q->ts = &g->tx;
        q->ts_own = 0;
    } else {
        q->ts = malloc(sizeof *q->ts);
        q->ts_own = 1;
        if (!q->ts) return -1;
    }
    q->ts_depth = 1;
    return 0;
}

static void tx_give(struct qc *q) {
    if (!q->ts || --q->ts_depth > 0) return;
    if (q->ts_own) free(q->ts);
    else scratch()->tx_busy = 0;
    q->ts = NULL;
}

#ifdef QC_WITH_SERVER
static unsigned long g_clock_reads;
#define QC_CLK() (g_clock_reads++)
#else
#define QC_CLK() ((void)0)
#endif

static uint64_t now_ns(void) {
    QC_CLK();
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * NGTCP2_SECONDS + (uint64_t)ts.tv_nsec;
}

static void fill_random(uint8_t *p, size_t n) {
    if (os_rand_seed(p, (unsigned)n) != 0) abort();
}

#ifdef QC_WITH_SERVER
#define QC_FOLLOW(q) ((q)->follow)
#define QC_DST(q) ((q)->follow ? (void *)&(q)->peer : NULL)
#define QC_DSTLEN(q) ((q)->follow ? (q)->peer_len : 0)
#define QC_STEP(q) (((struct qc *)(uintptr_t)(q))->probe_steps++)
#define QC_OP(q) (((struct qc *)(uintptr_t)(q))->probe_ops++)
#define QC_HELD(q, e) ((q)->held e)
#define QC_WRX(q, n) ((q)->w_rx = (n), (q)->w_rx_max = (q)->w_rx > (q)->w_rx_max ? (q)->w_rx : (q)->w_rx_max)
#define QC_WFL(q, n) ((q)->w_fl_max = (n) > (q)->w_fl_max ? (n) : (q)->w_fl_max)
#else
#define QC_FOLLOW(q) 0
#define QC_DST(q) NULL
#define QC_DSTLEN(q) 0
#define QC_STEP(q) ((void)0)
#define QC_OP(q) ((void)0)
#define QC_HELD(q, e) ((void)(q))
#define QC_WRX(q, n) ((void)(q))
#define QC_WFL(q, n) ((void)(q), (void)(n))
#endif

static size_t tab_hash(int64_t id, size_t cap) {
    uint64_t h = ((uint64_t)id >> 2) * 0x9E3779B97F4A7C15ull;
    return (size_t)(h >> 32) & (cap - 1);
}

static struct qc_stream *stream_find(const struct qc *q, int64_t id) {
    QC_OP(q);
    if (!q->tab.cap) return NULL;
    size_t mask = q->tab.cap - 1;
    for (size_t i = tab_hash(id, q->tab.cap);; i = (i + 1) & mask) {
        QC_STEP(q);
        struct qc_stream *s = q->tab.slot[i];
        if (!s || s->id == id) return s;
    }
}

static void tab_put(struct qc_stream **slot, size_t cap, struct qc_stream *s) {
    size_t i = tab_hash(s->id, cap);
    while (slot[i]) i = (i + 1) & (cap - 1);
    slot[i] = s;
}

static int tab_add(struct qc *q, struct qc_stream *s) {
    QC_OP(q);
    if ((q->tab.n + 1) * 2 > q->tab.cap) {
        size_t cap = q->tab.cap ? q->tab.cap * 2 : 16;
        struct qc_stream **slot = calloc(cap, sizeof *slot);
        if (!slot) return -1;
        for (size_t i = 0; i < q->tab.cap; i++)
            if (q->tab.slot[i]) tab_put(slot, cap, q->tab.slot[i]);
        free(q->tab.slot);
        q->tab.slot = slot;
        q->tab.cap = cap;
    }
    size_t i = tab_hash(s->id, q->tab.cap);
    while (q->tab.slot[i]) {
        QC_STEP(q);
        i = (i + 1) & (q->tab.cap - 1);
    }
    q->tab.slot[i] = s;
    q->tab.n++;
    return 0;
}

static void tab_del(struct qc *q, const struct qc_stream *s) {
    QC_OP(q);
    size_t mask = q->tab.cap - 1, i = tab_hash(s->id, q->tab.cap);
    while (q->tab.slot[i] != s) {
        QC_STEP(q);
        i = (i + 1) & mask;
    }
    for (size_t j = (i + 1) & mask; q->tab.slot[j]; j = (j + 1) & mask) {
        QC_STEP(q);
        size_t k = tab_hash(q->tab.slot[j]->id, q->tab.cap);
        if (((j - k) & mask) >= ((j - i) & mask)) {
            q->tab.slot[i] = q->tab.slot[j];
            i = j;
        }
    }
    q->tab.slot[i] = NULL;
    q->tab.n--;
}

static int has_tx(const struct qc_stream *s) {
    return s->total > s->sent || (s->fin_req && !s->fin_sent);
}

static void pend_add(struct qc *q, struct qc_stream *s) {
    if (s->in_pend || s->blocked || !has_tx(s)) return;
    s->in_pend = 1;
    s->pnext = NULL;
    s->pprev = q->pend_tail;
    if (q->pend_tail) q->pend_tail->pnext = s; else q->pend_head = s;
    q->pend_tail = s;
}

static void pend_del(struct qc *q, struct qc_stream *s) {
    if (!s->in_pend) return;
    s->in_pend = 0;
    if (s->pprev) s->pprev->pnext = s->pnext; else q->pend_head = s->pnext;
    if (s->pnext) s->pnext->pprev = s->pprev; else q->pend_tail = s->pprev;
    s->pprev = s->pnext = NULL;
}

static void wr_add(struct qc *q, struct qc_stream *s) {
    if (s->in_wr) return;
    s->in_wr = 1;
    s->wnext = NULL;
    s->wprev = q->wr_tail;
    if (q->wr_tail) q->wr_tail->wnext = s; else q->wr_head = s;
    q->wr_tail = s;
}

static void wr_del(struct qc *q, struct qc_stream *s) {
    if (!s->in_wr) return;
    s->in_wr = 0;
    if (s->wprev) s->wprev->wnext = s->wnext; else q->wr_head = s->wnext;
    if (s->wnext) s->wnext->wprev = s->wprev; else q->wr_tail = s->wprev;
    s->wprev = s->wnext = NULL;
}

static void bw_add(struct qc *q, struct qc_stream *s) {
    if (s->in_bw) return;
    s->in_bw = 1;
    s->bnext = NULL;
    s->bprev = q->bw_tail;
    if (q->bw_tail) q->bw_tail->bnext = s; else q->bw_head = s;
    q->bw_tail = s;
}

static void bw_del(struct qc *q, struct qc_stream *s) {
    if (!s->in_bw) return;
    s->in_bw = 0;
    if (s->bprev) s->bprev->bnext = s->bnext; else q->bw_head = s->bnext;
    if (s->bnext) s->bnext->bprev = s->bprev; else q->bw_tail = s->bprev;
    s->bprev = s->bnext = NULL;
}

static size_t budget_left(const struct qc *q) {
    return q->queued < q->send_total ? q->send_total - q->queued : 0;
}

static int stream_known(const struct qc *q, int64_t id) {
    return q->conn && stream_find(q, id) != NULL;
}

static struct qc_stream *stream_new(struct qc *q, int64_t id) {
    struct qc_stream *s = calloc(1, sizeof *s);
    if (!s) return NULL;
    s->id = id;
    if (tab_add(q, s) != 0) {
        free(s);
        return NULL;
    }
    return s;
}

static void chunks_free(struct qc *q, struct qc_stream *s) {
    while (s->head) {
        struct qc_chunk *c = s->head;
        s->head = c->next;
        QC_HELD(q, -= c->cap);
        free(c);
    }
    s->tail = s->cur = NULL;
}

static void stream_free(struct qc *q, struct qc_stream *s) {
    q->queued -= (size_t)(s->total - s->acked);
    q->blocked_n -= s->blocked;
    q->full_n -= s->full;
    chunks_free(q, s);
    free(s);
}

static void stream_drop(struct qc *q, struct qc_stream *s) {
    tab_del(q, s);
    pend_del(q, s);
    wr_del(q, s);
    bw_del(q, s);
    stream_free(q, s);
}

static void stream_tx_done(struct qc *q, struct qc_stream *s) {
    q->queued -= (size_t)(s->total - s->acked);
    s->acked = s->total;
    s->sent = s->total;
    s->fin_sent = 1;
    s->want_room = 0;
    q->blocked_n -= s->blocked;
    s->blocked = 0;
    q->full_n -= s->full;
    s->full = 0;
    chunks_free(q, s);
    pend_del(q, s);
    wr_del(q, s);
    bw_del(q, s);
}

static void stream_trim(struct qc *q, struct qc_stream *s) {
    while (s->head && s->head->off + s->head->len <= s->acked && (s->head->next || s->acked == s->total)) {
        struct qc_chunk *c = s->head;
        s->head = c->next;
        if (s->cur == c) s->cur = c->next;
        if (s->tail == c) s->tail = NULL;
        QC_HELD(q, -= c->cap);
        free(c);
    }
}

static int stream_append(struct qc *q, struct qc_stream *s, const uint8_t *d, size_t n) {
    while (n > 0) {
        struct qc_chunk *c = s->tail;
        if (!c || c->len == c->cap) {
            size_t cap = QC_CHUNK;
            if (!s->head && n < QC_CHUNK) cap = (n + QC_CHUNK_MIN - 1) / QC_CHUNK_MIN * QC_CHUNK_MIN;
            c = malloc(sizeof *c + cap);
            if (!c) return -1;
            c->next = NULL;
            c->off = s->total;
            c->len = 0;
            c->cap = cap;
            QC_HELD(q, += cap);
            if (s->tail) s->tail->next = c; else s->head = c;
            s->tail = c;
            if (!s->cur) s->cur = c;
        }
        size_t k = c->cap - c->len < n ? c->cap - c->len : n;
        memcpy(c->data + c->len, d, k);
        c->len += k;
        s->total += k;
        q->queued += k;
        d += k;
        n -= k;
    }
    return 0;
}

static size_t stream_own_room(const struct qc *q, const struct qc_stream *s) {
    size_t used = (size_t)(s->total - s->acked);
    return used < q->send_buf ? q->send_buf - used : 0;
}

static void full_sync(struct qc *q, struct qc_stream *s) {
    int full = stream_own_room(q, s) == 0;
    q->full_n += full - s->full;
    s->full = full;
}

static size_t stream_budget(const struct qc *q, const struct qc_stream *s) {
    size_t ex = s->total < QC_EXEMPT ? QC_EXEMPT - (size_t)s->total : 0;
    return budget_left(q) + ex;
}

static size_t stream_room_of(const struct qc *q, const struct qc_stream *s) {
    size_t own = stream_own_room(q, s), bud = stream_budget(q, s);
    return own < bud ? own : bud;
}

static ngtcp2_conn *get_conn_cb(ngtcp2_crypto_conn_ref *ref) {
    return ((struct qc *)ref->user_data)->conn;
}

static void rand_cb(uint8_t *dest, size_t destlen, const ngtcp2_rand_ctx *ctx) {
    (void)ctx;
    fill_random(dest, destlen);
}

static int new_cid_cb(ngtcp2_conn *conn, ngtcp2_cid *cid, ngtcp2_stateless_reset_token *token,
                      size_t cidlen, void *ud) {
    struct qc *q = ud;
    (void)conn;
    fill_random(cid->data, cidlen);
    cid->datalen = cidlen;
    if (ngtcp2_crypto_generate_stateless_reset_token(token->data, q->secret, sizeof q->secret, cid) != 0)
        return NGTCP2_ERR_CALLBACK_FAILURE;
    return 0;
}

static void keepalive_tune(struct qc *q) {
    if (!q->keepalive_ms || !q->conn) return;
    const ngtcp2_transport_params *rp = ngtcp2_conn_get_remote_transport_params2(q->conn);
    uint64_t idle = (uint64_t)q->idle_local_ms * NGTCP2_MILLISECONDS;
    if (rp && rp->max_idle_timeout && (!idle || rp->max_idle_timeout < idle)) idle = rp->max_idle_timeout;
    uint64_t ka = (uint64_t)q->keepalive_ms * NGTCP2_MILLISECONDS;
    if (idle && idle / 3 < ka) ka = idle / 3;
    if (ka < 500 * NGTCP2_MILLISECONDS) ka = 500 * NGTCP2_MILLISECONDS;
    q->ka_ns = ka;
    ngtcp2_conn_set_keep_alive_timeout(q->conn, ka);
}

static int handshake_completed_cb(ngtcp2_conn *conn, void *ud) {
    struct qc *q = ud;
    (void)conn;
    if (!q->server && qcssl_alpn_check(q->ssl, q->alpn) != 0) return NGTCP2_ERR_CALLBACK_FAILURE;
    q->hs_done = 1;
    keepalive_tune(q);
    if (q->ops.on_handshake) {
        q->in_cb++;
        q->ops.on_handshake(q->user);
        q->in_cb--;
    }
    return 0;
}

static int stream_open_cb(ngtcp2_conn *conn, int64_t sid, void *ud) {
    struct qc_stream *s = stream_new(ud, sid);
    if (!s) return NGTCP2_ERR_CALLBACK_FAILURE;
    if (ngtcp2_conn_set_stream_user_data(conn, sid, s) != 0) {
        stream_drop(ud, s);
        return NGTCP2_ERR_CALLBACK_FAILURE;
    }
    return 0;
}

static int extend_local_bidi_cb(ngtcp2_conn *conn, uint64_t max, void *ud) {
    struct qc *q = ud;
    (void)conn; (void)max;
    if (q->stream_wait) {
        q->stream_wait = 0;
        q->sopen_pend = 1;
    }
    return 0;
}

static int recv_stream_data_cb(ngtcp2_conn *conn, uint32_t flags, int64_t sid, uint64_t offset,
                               const uint8_t *data, size_t datalen, void *ud, void *sud) {
    struct qc *q = ud;
    (void)conn; (void)offset; (void)sud;
    q->pay_rx += datalen;
    if (q->ops.on_stream_data) {
        q->in_cb++;
        q->ops.on_stream_data(q->user, sid, data, datalen, (flags & NGTCP2_STREAM_DATA_FLAG_FIN) != 0);
        q->in_cb--;
    }
    return 0;
}

static int acked_stream_data_offset_cb(ngtcp2_conn *conn, int64_t sid, uint64_t offset,
                                       uint64_t datalen, void *ud, void *sud) {
    struct qc *q = ud;
    struct qc_stream *s = sud;
    (void)conn; (void)sid;
    if (s && offset + datalen > s->acked) {
        q->queued -= (size_t)(offset + datalen - s->acked);
        s->acked = offset + datalen;
        stream_trim(q, s);
        full_sync(q, s);
        if (s->want_room && stream_room_of(q, s) > 0) {
            s->want_room = 0;
            wr_add(q, s);
        } else if (s->want_room && stream_own_room(q, s) > 0) {
            s->want_room = 0;
            bw_add(q, s);
        }
    }
    return 0;
}

static int stream_close_cb(ngtcp2_conn *conn, uint32_t flags, int64_t sid, uint64_t rx_err,
                           uint64_t tx_err, void *ud, void *sud) {
    struct qc *q = ud;
    struct qc_stream *s = sud;
    int rx = (flags & NGTCP2_STREAM_CLOSE2_FLAG_RX_APP_ERROR_CODE_SET) != 0;
    int tx = (flags & NGTCP2_STREAM_CLOSE2_FLAG_TX_APP_ERROR_CODE_SET) != 0;
    if (s) stream_drop(q, s);
    if (((sid & 1) != 0) != (q->server != 0)) {
        if (sid & 2) ngtcp2_conn_extend_max_streams_uni(conn, 1);
        else ngtcp2_conn_extend_max_streams_bidi(conn, 1);
    }
    if (q->ops.on_stream_close) {
        q->in_cb++;
        q->ops.on_stream_close(q->user, sid, rx, rx ? rx_err : 0, tx, tx ? tx_err : 0);
        q->in_cb--;
    }
    return 0;
}

static int stream_reset_cb(ngtcp2_conn *conn, int64_t sid, uint64_t final_size, uint64_t err, void *ud,
                           void *sud) {
    struct qc *q = ud;
    (void)conn; (void)final_size;
    if (sud && q->ops.on_stream_peer_abort) {
        q->in_cb++;
        q->ops.on_stream_peer_abort(q->user, sid, 1, err, 0, 0);
        q->in_cb--;
    }
    return 0;
}

static int recv_stop_sending_cb(ngtcp2_conn *conn, int64_t sid, uint64_t err, void *ud, void *sud) {
    struct qc *q = ud;
    (void)conn;
    if (!sud) return 0;
    stream_tx_done(q, sud);
    if (q->ops.on_stream_peer_abort) {
        q->in_cb++;
        q->ops.on_stream_peer_abort(q->user, sid, 0, 0, 1, err);
        q->in_cb--;
    }
    return 0;
}

static int extend_max_stream_data_cb(ngtcp2_conn *conn, int64_t sid, uint64_t max, void *ud, void *sud) {
    struct qc *q = ud;
    struct qc_stream *s = sud;
    (void)conn; (void)max; (void)sid;
    if (s) {
        q->blocked_n -= s->blocked;
        s->blocked = 0;
        pend_add(q, s);
    }
    return 0;
}

static int recv_datagram_cb(ngtcp2_conn *conn, uint32_t flags, const uint8_t *data, size_t datalen, void *ud) {
    struct qc *q = ud;
    (void)conn; (void)flags;
    q->dg_rx++;
    q->pay_rx += datalen;
    if (q->ops.on_datagram) {
        q->in_cb++;
        q->ops.on_datagram(q->user, data, datalen);
        q->in_cb--;
    }
    return 0;
}

static const ngtcp2_callbacks g_client_cbs = {
    .client_initial = ngtcp2_crypto_client_initial_cb,
    .recv_crypto_data = ngtcp2_crypto_recv_crypto_data_cb,
    .handshake_completed = handshake_completed_cb,
    .encrypt = ngtcp2_crypto_encrypt_cb,
    .decrypt = ngtcp2_crypto_decrypt_cb,
    .hp_mask = ngtcp2_crypto_hp_mask_cb,
    .recv_stream_data = recv_stream_data_cb,
    .acked_stream_data_offset = acked_stream_data_offset_cb,
    .stream_open = stream_open_cb,
    .recv_retry = ngtcp2_crypto_recv_retry_cb,
    .rand = rand_cb,
    .update_key = ngtcp2_crypto_update_key_cb,
    .extend_max_stream_data = extend_max_stream_data_cb,
    .delete_crypto_aead_ctx = ngtcp2_crypto_delete_crypto_aead_ctx_cb,
    .delete_crypto_cipher_ctx = ngtcp2_crypto_delete_crypto_cipher_ctx_cb,
    .recv_datagram = recv_datagram_cb,
    .version_negotiation = ngtcp2_crypto_version_negotiation_cb,
    .get_new_connection_id2 = new_cid_cb,
    .get_path_challenge_data2 = ngtcp2_crypto_get_path_challenge_data2_cb,
    .stream_close2 = stream_close_cb,
    .extend_max_local_streams_bidi = extend_local_bidi_cb,
    .stream_reset = stream_reset_cb,
    .recv_stop_sending = recv_stop_sending_cb,
};

size_t qc_datagram_max(const struct qc *q) {
    if (!q || !q->conn || !q->hs_done) return 0;
    const ngtcp2_transport_params *rp = ngtcp2_conn_get_remote_transport_params2(q->conn);
    if (!rp || rp->max_datagram_frame_size == 0 || q->datagram_max == 0) return 0;
    size_t udp = ngtcp2_conn_get_path_max_tx_udp_payload_size2(q->conn);
    size_t over = 1 + ngtcp2_conn_get_dcid2(q->conn)->datalen + 4 + 16 + 3;
    size_t cap = udp > over ? udp - over : 0;
    size_t peer = rp->max_datagram_frame_size > 3 ? (size_t)rp->max_datagram_frame_size - 3 : 0;
    return cap < peer ? cap : peer;
}

static int gso_fatal(int err) {
    return err == EIO || err == EINVAL || err == ENOPROTOOPT || err == EOPNOTSUPP || err == EPROTONOSUPPORT;
}

static size_t gso_run(const size_t *len, size_t n) {
    if (!n) return 0;
    size_t seg = len[0], bytes = seg, k = 1;
    if (!seg) return 1;
    while (k < n && k < QC_GSO_SEGS) {
        size_t l = len[k];
        if (!l || l > seg || bytes + l > QC_GSO_BYTES) break;
        bytes += l;
        k++;
        if (l < seg) break;
    }
    return k;
}

static size_t gro_next(size_t total, size_t seg, size_t off) {
    if (off >= total) return 0;
    size_t rem = total - off;
    if (!seg) return rem;
    return rem < seg ? rem : seg;
}

static size_t gro_seg(struct msghdr *mh) {
    size_t off = 0, end = mh->msg_controllen;
    while (off + sizeof(struct cmsghdr) <= end) {
        struct cmsghdr *c = (struct cmsghdr *)((char *)mh->msg_control + off);
        size_t clen = c->cmsg_len;
        if (clen < CMSG_LEN(0) || clen > end - off) break;
        if (c->cmsg_level == SOL_UDP && c->cmsg_type == UDP_GRO) {
            size_t l = clen - CMSG_LEN(0);
            if (l >= sizeof(int)) { int v; memcpy(&v, CMSG_DATA(c), sizeof v); return v > 0 ? (size_t)v : 0; }
            if (l == sizeof(uint16_t)) { uint16_t v; memcpy(&v, CMSG_DATA(c), sizeof v); return v; }
        }
        off += CMSG_ALIGN(clen);
    }
    return 0;
}

static void send_batch(struct qc *q, struct iovec *iov, size_t n) {
    size_t i = 0, lim = q->overhead + (q->conn ? ngtcp2_conn_get_path_max_tx_udp_payload_size2(q->conn) : 0);
    while (i < n) {
        size_t m = n - i < QC_GSO_SEGS ? n - i : QC_GSO_SEGS;
        size_t cnt = 1, plain = 0;
        if (q->gso_ok && m >= 2) {
            size_t lens[QC_GSO_SEGS];
            for (size_t k = 0; k < m; k++) lens[k] = iov[i + k].iov_len;
            size_t run = gso_run(lens, m);
            if (run >= 2) {
                union { char b[CMSG_SPACE(sizeof(uint16_t))]; struct cmsghdr a; } cu;
                memset(&cu, 0, sizeof cu);
                struct msghdr mh = { .msg_name = QC_DST(q), .msg_namelen = QC_DSTLEN(q), .msg_iov = &iov[i],
                                     .msg_iovlen = run, .msg_control = cu.b, .msg_controllen = sizeof cu.b };
                struct cmsghdr *c = CMSG_FIRSTHDR(&mh);
                c->cmsg_level = SOL_UDP;
                c->cmsg_type = UDP_SEGMENT;
                c->cmsg_len = CMSG_LEN(sizeof(uint16_t));
                uint16_t sz = (uint16_t)lens[0];
                memcpy(CMSG_DATA(c), &sz, sizeof sz);
                q->tx_calls++;
                if (sendmsg(q->fd, &mh, MSG_DONTWAIT) >= 0) {
                    q->dg_tx += run;
                    i += run;
                    continue;
                }
                if (errno == EAGAIN || errno == EWOULDBLOCK || errno == ENOBUFS) {
                    q->tx_full = 1;
                    return;
                }
                if (gso_fatal(errno)) q->gso_ok = 0;
                else if (errno != EMSGSIZE) {
                    i += run;
                    continue;
                }
                plain = run;
            }
        }
        if (plain) cnt = plain;
        else if (!q->gso_ok) cnt = m;
        struct mmsghdr mm[QC_GSO_SEGS];
        for (size_t k = 0; k < cnt; k++) {
            memset(&mm[k], 0, sizeof mm[k]);
            mm[k].msg_hdr.msg_name = QC_DST(q);
            mm[k].msg_hdr.msg_namelen = QC_DSTLEN(q);
            mm[k].msg_hdr.msg_iov = &iov[i + k];
            mm[k].msg_hdr.msg_iovlen = 1;
        }
        q->tx_calls++;
        int r = sendmmsg(q->fd, mm, (unsigned)cnt, MSG_DONTWAIT);
        if (r > 0) { q->dg_tx += (unsigned)r; i += (size_t)r; continue; }
        if (r < 0 && errno == EINTR) continue;
        if (r < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == ENOBUFS)) {
            q->tx_full = 1;
            return;
        }
        if (r < 0 && errno == EMSGSIZE && iov[i].iov_len <= lim) q->mtu_lost = 1;
        i++;
    }
}

static void send_one(struct qc *q, const uint8_t *p, size_t n) {
    if (q->filter.tx) {
        n = q->filter.tx(q->filter.user, q->ts->obuf, p, n);
        if (!n) return;
        p = q->ts->obuf;
    }
    struct iovec iov = { .iov_base = (void *)p, .iov_len = n };
    send_batch(q, &iov, 1);
}

static size_t stream_vecs(struct qc_stream *s, ngtcp2_vec v[2], uint64_t *covered) {
    struct qc_chunk *c = s->cur;
    while (c && c->off + c->len <= s->sent && c->next) c = c->next;
    s->cur = c;
    size_t nv = 0;
    *covered = 0;
    for (; c && nv < 2; c = c->next) {
        uint64_t start = c->off > s->sent ? c->off : s->sent;
        size_t skip = (size_t)(start - c->off);
        if (skip >= c->len) continue;
        v[nv].base = c->data + skip;
        v[nv].len = c->len - skip;
        *covered += v[nv].len;
        nv++;
    }
    return nv;
}

static void dg_pop(struct qc *q, int dropped) {
    free(q->dgq[q->dg_head].d);
    q->dg_head = (q->dg_head + 1) % QC_DG_QUEUE;
    q->dg_n--;
    if (dropped) q->dg_dropped++;
}

enum { HOLD_NONE, HOLD_CWND = QC_WAIT_CWND + 1, HOLD_CREDIT = QC_WAIT_CREDIT + 1, HOLD_BUDGET = QC_WAIT_BUDGET + 1 };

static void hit_on(struct qc *q, int h) {
    if (!q->hit) q->hit = h;
}

static ngtcp2_ssize write_pkt_cb(ngtcp2_conn *conn, ngtcp2_path *path, ngtcp2_pkt_info *pi,
                                 uint8_t *dest, size_t destlen, ngtcp2_tstamp ts, void *ud) {
    struct qc *q = ud;
    ngtcp2_ssize nw;
    ngtcp2_ssize dl = -1;

    if (q->hs_done && q->dg_n > 0) {
        size_t dmax = qc_datagram_max(q);
        while (q->dg_n > 0) {
            struct qc_dg *g = &q->dgq[q->dg_head];
            if (g->n > dmax) {
                dg_pop(q, 1);
                continue;
            }
            ngtcp2_vec v = { .base = g->d, .len = g->n };
            int accepted = 0;
            nw = ngtcp2_conn_writev_datagram(conn, path, pi, dest, destlen, &accepted,
                                             NGTCP2_WRITE_DATAGRAM_FLAG_MORE | NGTCP2_WRITE_DATAGRAM_FLAG_PADDING, 0, &v, 1,
                                             ts);
            if (accepted) {
                q->pay_tx += g->n;
                dg_pop(q, 0);
            }
            if (nw == NGTCP2_ERR_WRITE_MORE) continue;
            if (nw < 0 && !accepted) dg_pop(q, 1);
            return nw;
        }
    }

    struct qc_stream *last = q->hs_done ? q->pend_tail : NULL, *next;
    if (last) QC_OP(q);
    for (struct qc_stream *s = last ? q->pend_head : NULL; s; s = next) {
        QC_STEP(q);
        next = s == last ? NULL : s->pnext;
        uint64_t avail = s->total - s->sent;
        int want_fin = s->fin_req && !s->fin_sent;
        if ((avail == 0 && !want_fin) || s->blocked) {
            pend_del(q, s);
            continue;
        }
        ngtcp2_vec v[2];
        uint64_t cov = 0;
        size_t nv = avail ? stream_vecs(s, v, &cov) : 0;
        uint32_t fin = want_fin && cov == avail ? NGTCP2_WRITE_STREAM_FLAG_FIN : 0;
        dl = -1;
        nw = ngtcp2_conn_writev_stream(conn, path, pi, dest, destlen, &dl,
                                       fin | NGTCP2_WRITE_STREAM_FLAG_MORE | NGTCP2_WRITE_STREAM_FLAG_PADDING, s->id, v,
                                       nv, ts);
        if (dl >= 0) {
            s->sent += (uint64_t)dl;
            q->pay_tx += (uint64_t)dl;
            if (fin && s->sent == s->total) s->fin_sent = 1;
        }
        if (nw == NGTCP2_ERR_STREAM_DATA_BLOCKED) {
            s->blocked = 1;
            q->blocked_n++;
            hit_on(q, HOLD_CREDIT);
            pend_del(q, s);
            continue;
        }
        if (nw == NGTCP2_ERR_STREAM_SHUT_WR || nw == NGTCP2_ERR_STREAM_NOT_FOUND) {
            stream_tx_done(q, s);
            continue;
        }
        if (nw < 0 && nw != NGTCP2_ERR_WRITE_MORE) return nw;
        pend_del(q, s);
        pend_add(q, s);
        if (nw != NGTCP2_ERR_WRITE_MORE) return nw;
    }
    return ngtcp2_conn_writev_stream(conn, path, pi, dest, destlen, &dl, 0, -1, NULL, 0, ts);
}

static void closed(struct qc *q, int reason, int app, uint64_t code, const char *why) {
    if (q->closed) return;
    q->closed = 1;
    q->close_reason = reason;
    q->close_app = app;
    q->close_code = code;
    q->close_why = why;
}

static void deliver(struct qc *q) {
    if (!q->closed || q->delivered) return;
    q->delivered = 1;
    if (q->ops.on_closed) {
        q->in_cb++;
        q->ops.on_closed(q->user, q->close_reason, q->close_app, q->close_code, q->close_why);
        q->in_cb--;
    }
}

static int flush_with(struct qc *q, struct qc_txs *t);
static void rx_pass(struct qc *q);
static void hop(struct qc *q, uint64_t now);

static uint64_t in_flight(const struct qc *q) {
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    return ci.bytes_in_flight;
}

static uint64_t acct_now(const struct qc *q) {
#ifdef QC_WITH_SERVER
    if (q->clock) return q->clock;
#endif
    return q->now;
}

static void hold_set(struct qc *q, int h) {
    if (h == q->hold) return;
    uint64_t t = acct_now(q);
    if (q->hold && t > q->hold_at) q->wait[q->hold - 1] += t - q->hold_at;
    q->hold = h;
    q->hold_at = t;
}

static void hold_after(struct qc *q) {
    if (!q->tx_full && q->pend_head) hit_on(q, ngtcp2_conn_get_max_data_left2(q->conn) ? HOLD_CWND : HOLD_CREDIT);
    int full = !q->pend_head && (q->full_n || !budget_left(q));
    hold_set(q, q->hit ? q->hit : q->blocked_n ? HOLD_CREDIT : full ? HOLD_BUDGET : HOLD_NONE);
}

static int flush(struct qc *q) {
    q->want_flush = 0;
    if (q->closed || !q->conn) return QC_ECLOSED;
    if (ngtcp2_conn_in_closing_period(q->conn) || ngtcp2_conn_in_draining_period(q->conn)) return 0;
    if (tx_take(q) != 0) return 0;
    int track = q->hs_done && !q->tx_wait;
    uint64_t f0 = track ? in_flight(q) : 0;
    int ping = q->ping_req && q->hs_done;
    if (ping) ngtcp2_conn_set_keep_alive_timeout(q->conn, 1);
    q->hit = 0;
    int rv = flush_with(q, q->ts);
    if (q->hs_done) hold_after(q);
    if (ping) {
        q->ping_req = 0;
        if (q->keepalive_ms) keepalive_tune(q);
        else ngtcp2_conn_set_keep_alive_timeout(q->conn, UINT64_MAX);
    }
    if (track && in_flight(q) > f0) q->tx_wait = q->now;
    tx_give(q);
    return rv;
}

static int flush_with(struct qc *q, struct qc_txs *t) {
    q->tx_full = 0;
    size_t done = 0;
    while (!q->tx_full) {
        ngtcp2_path_storage ps;
        ngtcp2_pkt_info pi;
        size_t gso = 0;
        ngtcp2_path_storage_zero(&ps);
        ngtcp2_ssize nw = ngtcp2_conn_write_aggregate_pkt(q->conn, &ps.path, &pi, t->txbuf, QC_TXBUF,
                                                          &gso, write_pkt_cb, q->now);
        if (nw < 0) return (int)nw;
        if (nw == 0) return 0;
        done += (size_t)nw;
        QC_WFL(q, done);
        if (gso == 0) gso = (size_t)nw;
        struct iovec iov[QC_GSO_SEGS];
        size_t cnt = 0, oo = 0;
        for (size_t off = 0; off < (size_t)nw; off += gso) {
            size_t n = (size_t)nw - off < gso ? (size_t)nw - off : gso;
            const uint8_t *p = t->txbuf + off;
            if (q->filter.tx) {
                n = q->filter.tx(q->filter.user, t->obuf + oo, p, n);
                if (!n) continue;
                p = t->obuf + oo;
                oo += n;
            }
            iov[cnt].iov_base = (void *)p;
            iov[cnt].iov_len = n;
            if (++cnt == QC_GSO_SEGS) { send_batch(q, iov, cnt); cnt = 0; oo = 0; }
        }
        if (cnt && !q->tx_full) send_batch(q, iov, cnt);
    }
    return 0;
}

static void fail_close(struct qc *q, int err) {
    int reason = QC_CLOSE_ERROR;
    const char *tls_why = q->ssl ? qcssl_why(q->ssl) : NULL;
    const char *why = tls_why ? tls_why : ngtcp2_strerror(err);
    ngtcp2_ccerr ce;
    ngtcp2_ccerr_default(&ce);
    if (err == NGTCP2_ERR_CRYPTO && q->conn)
        ngtcp2_ccerr_set_tls_alert(&ce, ngtcp2_conn_get_tls_alert(q->conn), NULL, 0);
    else
        ngtcp2_ccerr_set_liberr(&ce, err, NULL, 0);
    if (!tls_why && err == NGTCP2_ERR_CRYPTO && q->conn) {
        snprintf(q->why_buf, sizeof q->why_buf, "%s, TLS alert %u", ngtcp2_strerror(err),
                 (unsigned)ngtcp2_conn_get_tls_alert(q->conn));
        why = q->why_buf;
    }
    if (tls_why) reason = QC_CLOSE_HANDSHAKE;
    else if (err == NGTCP2_ERR_IDLE_CLOSE) reason = QC_CLOSE_IDLE;
    else if (err == NGTCP2_ERR_HANDSHAKE_TIMEOUT || err == NGTCP2_ERR_CRYPTO) reason = QC_CLOSE_HANDSHAKE;
    if (q->conn && !ngtcp2_conn_in_closing_period(q->conn) && !ngtcp2_conn_in_draining_period(q->conn) &&
        err != NGTCP2_ERR_IDLE_CLOSE && err != NGTCP2_ERR_HANDSHAKE_TIMEOUT) {
        ngtcp2_path_storage ps;
        ngtcp2_pkt_info pi;
        ngtcp2_path_storage_zero(&ps);
        if (tx_take(q) == 0) {
            ngtcp2_ssize n = ngtcp2_conn_write_connection_close(q->conn, &ps.path, &pi, q->ts->txbuf, QC_TXBUF,
                                                                &ce, now_ns());
            if (n > 0) send_one(q, q->ts->txbuf, (size_t)n);
            tx_give(q);
        }
    }
    closed(q, reason, 0, ce.error_code, why);
}

static void peer_closed(struct qc *q) {
    const ngtcp2_ccerr *ce = ngtcp2_conn_get_ccerr2(q->conn);
    int app = ce->type == NGTCP2_CCERR_TYPE_APPLICATION;
    snprintf(q->why_buf, sizeof q->why_buf, "peer closed with %s code 0x%llx", app ? "application" : "transport",
             (unsigned long long)ce->error_code);
    closed(q, q->hs_done ? QC_CLOSE_PEER : QC_CLOSE_HANDSHAKE, app, ce->error_code, q->why_buf);
}

static void do_close(struct qc *q, uint64_t app_err) {
    if (q->closed) return;
    if (q->conn && !ngtcp2_conn_in_closing_period(q->conn) && !ngtcp2_conn_in_draining_period(q->conn)) {
        ngtcp2_ccerr ce;
        ngtcp2_path_storage ps;
        ngtcp2_pkt_info pi;
        ngtcp2_ccerr_default(&ce);
        ngtcp2_ccerr_set_application_error(&ce, app_err, NULL, 0);
        ngtcp2_path_storage_zero(&ps);
        if (tx_take(q) == 0) {
            ngtcp2_ssize n = ngtcp2_conn_write_connection_close(q->conn, &ps.path, &pi, q->ts->txbuf, QC_TXBUF,
                                                                &ce, now_ns());
            if (n > 0) send_one(q, q->ts->txbuf, (size_t)n);
            tx_give(q);
        }
    }
    closed(q, QC_CLOSE_LOCAL, 1, app_err, "closed by us");
}

static void flush_out(struct qc *q) {
    if (q->in_cb || q->closed || !q->conn || q->pend_err) return;
    q->now = now_ns();
    int rv = flush(q);
    if (rv != 0 && rv != QC_ECLOSED) q->pend_err = rv;
}

static void kick(struct qc *q) {
    q->want_flush = 1;
    if (!q->defer) flush_out(q);
}

static void writable_dispatch(struct qc *q) {
    for (;;) {
        struct qc_stream *s = q->wr_head;
        if (s) {
            wr_del(q, s);
            bw_del(q, s);
        } else if ((s = budget_left(q) ? q->bw_head : NULL) != NULL) bw_del(q, s);
        if (!s || q->closed) break;
        QC_OP(q);
        QC_STEP(q);
        if (q->ops.on_stream_writable) {
            int64_t id = s->id;
            q->in_cb++;
            q->ops.on_stream_writable(q->user, id);
            q->in_cb--;
        }
    }
    if (q->sopen_pend && !q->closed) {
        q->sopen_pend = 0;
        if (q->ops.on_streams_open) {
            q->in_cb++;
            q->ops.on_streams_open(q->user);
            q->in_cb--;
        }
    }
}

static void mtu_close(struct qc *q) {
    ngtcp2_ccerr ce;
    ngtcp2_ccerr_default(&ce);
    ngtcp2_ccerr_set_liberr(&ce, NGTCP2_ERR_INTERNAL, NULL, 0);
    if (q->conn && !ngtcp2_conn_in_closing_period(q->conn) && !ngtcp2_conn_in_draining_period(q->conn) &&
        tx_take(q) == 0) {
        ngtcp2_path_storage ps;
        ngtcp2_pkt_info pi;
        ngtcp2_path_storage_zero(&ps);
        ngtcp2_ssize n = ngtcp2_conn_write_connection_close(q->conn, &ps.path, &pi, q->ts->txbuf, QC_TXBUF, &ce,
                                                            q->now);
        if (n > 0) send_one(q, q->ts->txbuf, (size_t)n);
        tx_give(q);
    }
    closed(q, QC_CLOSE_ERROR, 0, ce.error_code, "path mtu dropped below the packet size");
}

static int finish(struct qc *q) {
    if (!q->closed && q->mtu_lost) mtu_close(q);
    if (!q->closed) writable_dispatch(q);
    if (!q->closed && q->close_req) {
        q->close_req = 0;
        do_close(q, q->close_req_code);
    }
    if (!q->closed && q->pend_err) fail_close(q, q->pend_err);
    if (!q->closed && q->conn) {
        int rv = flush(q);
        if (rv != 0 && rv != QC_ECLOSED) fail_close(q, rv);
    }
    deliver(q);
    return q->closed ? QC_ECLOSED : 0;
}

static void settings_fill(ngtcp2_settings *st, ngtcp2_transport_params *tp, uint16_t probes[2], size_t overhead,
                          size_t datagram_max, unsigned idle_ms, unsigned hs_ms, uint64_t max_data,
                          uint64_t max_stream_data, uint64_t max_streams, uint64_t max_win,
                          uint64_t max_stream_win, int no_pmtud, int cc) {
    ngtcp2_settings_default(st);
    ngtcp2_transport_params_default(tp);
    st->initial_ts = now_ns();
    st->handshake_timeout = (uint64_t)(hs_ms ? hs_ms : 10000) * NGTCP2_MILLISECONDS;
    st->cc_algo = cc == QC_CC_RENO ? NGTCP2_CC_ALGO_RENO : NGTCP2_CC_ALGO_BBR;
    st->max_tx_udp_payload_size = QC_PATH_MAX - overhead;
    probes[0] = (uint16_t)(QC_PROBE_LOW - overhead);
    probes[1] = (uint16_t)(QC_PATH_MAX - overhead);
    st->pmtud_probes = probes;
    st->pmtud_probeslen = 2;
    st->max_window = max_win;
    st->max_stream_window = max_stream_win;
    if (no_pmtud) {
        st->no_pmtud = 1;
        st->no_tx_udp_payload_size_shaping = 1;
        st->max_tx_udp_payload_size = QC_NOPMTUD_SIZE;
        st->pmtud_probeslen = 0;
    }
    tp->initial_max_data = max_data ? max_data : 5u << 20;
    tp->initial_max_stream_data_bidi_local = max_stream_data ? max_stream_data : 2u << 20;
    tp->initial_max_stream_data_bidi_remote = tp->initial_max_stream_data_bidi_local;
    tp->initial_max_stream_data_uni = tp->initial_max_stream_data_bidi_local;
    tp->initial_max_streams_bidi = max_streams ? max_streams : 16;
    tp->initial_max_streams_uni = 3;
    tp->max_idle_timeout = (uint64_t)(idle_ms ? idle_ms : 30000) * NGTCP2_MILLISECONDS;
    tp->max_datagram_frame_size = datagram_max;
}

static int addr_parse(const char *host, uint16_t port, struct sockaddr_storage *ss, socklen_t *len) {
    memset(ss, 0, sizeof *ss);
    struct sockaddr_in *a4 = (struct sockaddr_in *)ss;
    struct sockaddr_in6 *a6 = (struct sockaddr_in6 *)ss;
    if (inet_pton(AF_INET, host, &a4->sin_addr) == 1) {
        a4->sin_family = AF_INET;
        a4->sin_port = htons(port);
        *len = sizeof *a4;
        return 0;
    }
    if (inet_pton(AF_INET6, host, &a6->sin6_addr) == 1) {
        a6->sin6_family = AF_INET6;
        a6->sin6_port = htons(port);
        *len = sizeof *a6;
        return 0;
    }
    return -1;
}

static void sock_tune(struct qc *q, int fd, int family) {
    int df = family == AF_INET6 ? IPV6_PMTUDISC_PROBE : IP_PMTUDISC_PROBE;
    if (family == AF_INET6) (void)setsockopt(fd, IPPROTO_IPV6, IPV6_MTU_DISCOVER, &df, sizeof df);
    else (void)setsockopt(fd, IPPROTO_IP, IP_MTU_DISCOVER, &df, sizeof df);
    int sz = QC_RCVBUF;
    if (setsockopt(fd, SOL_SOCKET, SO_RCVBUFFORCE, &sz, sizeof sz) != 0)
        (void)setsockopt(fd, SOL_SOCKET, SO_RCVBUF, &sz, sizeof sz);
    sz = QC_SNDBUF;
    if (setsockopt(fd, SOL_SOCKET, SO_SNDBUFFORCE, &sz, sizeof sz) != 0)
        (void)setsockopt(fd, SOL_SOCKET, SO_SNDBUF, &sz, sizeof sz);
    int got = 0;
    socklen_t gl = sizeof got;
    if (getsockopt(fd, SOL_SOCKET, SO_RCVBUF, &got, &gl) == 0 && got > 0) q->rcvbuf = (unsigned)got;
    got = 0;
    gl = sizeof got;
    if (getsockopt(fd, SOL_SOCKET, SO_SNDBUF, &got, &gl) == 0 && got > 0) q->sndbuf = (unsigned)got;
    int one = 1;
    if (q->gro_on) (void)setsockopt(fd, SOL_UDP, UDP_GRO, &one, sizeof one);
}

static void sock_batch(struct qc *q, int no_batch) {
    int seg = 0;
    socklen_t sl = sizeof seg;
    q->gso_ok = !no_batch && getsockopt(q->fd, SOL_UDP, UDP_SEGMENT, &seg, &sl) == 0;
    int one = 1;
    q->gro_on = !no_batch && setsockopt(q->fd, SOL_UDP, UDP_GRO, &one, sizeof one) == 0;
}

static struct qc *qc_alloc(const struct qc_ops *ops, void *user, const struct qc_filter *filter) {
    if (filter && filter->overhead > QC_FILTER_PAD) return NULL;
    struct qc *q = calloc(1, sizeof *q);
    if (!q) return NULL;
    q->fd = q->ep = q->pre = -1;
    q->hop_due = q->pre_end = UINT64_MAX;
    q->ops = *ops;
    q->user = user;
    if (filter) q->filter = *filter;
    q->overhead = filter ? filter->overhead : 0;
    fill_random(q->secret, sizeof q->secret);
    q->ref.get_conn = get_conn_cb;
    q->ref.user_data = q;
    return q;
}

static uint16_t ss_port(const struct sockaddr_storage *a) {
    if (a->ss_family == AF_INET6) return ntohs(((const struct sockaddr_in6 *)a)->sin6_port);
    return ntohs(((const struct sockaddr_in *)a)->sin_port);
}

static uint16_t fd_port(int fd) {
    struct sockaddr_storage a;
    socklen_t l = sizeof a;
    return fd >= 0 && getsockname(fd, (struct sockaddr *)&a, &l) == 0 ? ss_port(&a) : 0;
}

#ifdef QC_WITH_SERVER
static const ngtcp2_callbacks g_server_cbs = {
    .recv_client_initial = ngtcp2_crypto_recv_client_initial_cb,
    .recv_crypto_data = ngtcp2_crypto_recv_crypto_data_cb,
    .handshake_completed = handshake_completed_cb,
    .encrypt = ngtcp2_crypto_encrypt_cb,
    .decrypt = ngtcp2_crypto_decrypt_cb,
    .hp_mask = ngtcp2_crypto_hp_mask_cb,
    .recv_stream_data = recv_stream_data_cb,
    .acked_stream_data_offset = acked_stream_data_offset_cb,
    .stream_open = stream_open_cb,
    .rand = rand_cb,
    .update_key = ngtcp2_crypto_update_key_cb,
    .extend_max_stream_data = extend_max_stream_data_cb,
    .delete_crypto_aead_ctx = ngtcp2_crypto_delete_crypto_aead_ctx_cb,
    .delete_crypto_cipher_ctx = ngtcp2_crypto_delete_crypto_cipher_ctx_cb,
    .recv_datagram = recv_datagram_cb,
    .get_new_connection_id2 = new_cid_cb,
    .get_path_challenge_data2 = ngtcp2_crypto_get_path_challenge_data2_cb,
    .stream_close2 = stream_close_cb,
    .extend_max_local_streams_bidi = extend_local_bidi_cb,
    .stream_reset = stream_reset_cb,
    .recv_stop_sending = recv_stop_sending_cb,
};

static int srv_conn(struct qc *q, const ngtcp2_pkt_hd *hd);

static int srv_accept(struct qc *q, const struct sockaddr_storage *from, socklen_t fl,
                      const uint8_t *pkt, size_t n) {
    ngtcp2_pkt_hd hd;
    if (ngtcp2_accept(&hd, pkt, n) != 0) return -1;
    q->remote = q->peer = *from;
    q->remote_len = q->peer_len = fl;
    if (!q->follow && connect(q->fd, (const struct sockaddr *)&q->remote, q->remote_len) != 0) return -1;
    q->ssl = qcssl_new(q->ctx, &q->ref, NULL, q->alpn, 1, 0, NULL);
    if (!q->ssl) return -1;
    if (srv_conn(q, &hd) == 0) return 0;
    qcssl_free(q->ssl);
    q->ssl = NULL;
    return -1;
}

static int srv_conn(struct qc *q, const ngtcp2_pkt_hd *hd) {
    ngtcp2_settings st;
    ngtcp2_transport_params tp;
    uint16_t probes[2];
    settings_fill(&st, &tp, probes, q->overhead, q->datagram_max, q->idle_ms, 0, q->max_data,
                  q->max_stream_data, q->max_streams, 0, 0, 0, QC_CC_BBR);
    ngtcp2_cid scid;
    scid.datalen = QC_CID_LEN;
    fill_random(scid.data, scid.datalen);
    tp.original_dcid = hd->dcid;
    tp.original_dcid_present = 1;
    tp.stateless_reset_token_present = 1;
    if (ngtcp2_crypto_generate_stateless_reset_token(tp.stateless_reset_token, q->secret,
                                                     sizeof q->secret, &scid) != 0) return -1;
    ngtcp2_path path = {
        .local = { .addr = (ngtcp2_sockaddr *)&q->local, .addrlen = q->local_len },
        .remote = { .addr = (ngtcp2_sockaddr *)&q->remote, .addrlen = q->remote_len },
    };
    if (ngtcp2_conn_server_new(&q->conn, &hd->scid, &scid, &path, hd->version, &g_server_cbs, &st, &tp,
                               NULL, q) != 0) { q->conn = NULL; return -1; }
    ngtcp2_conn_set_tls_native_handle(q->conn, q->ssl);
    return 0;
}

int qc_listen(const struct qc_srv_cfg *cfg, const struct qc_ops *ops, void *user, struct qc **out) {
    if (!cfg || !ops || !out || !cfg->bind_host || !cfg->alpn) return QC_EINVAL;
    *out = NULL;
    if (cfg->filter && cfg->filter->overhead > QC_FILTER_PAD) return QC_EINVAL;
    struct qc *q = qc_alloc(ops, user, cfg->filter);
    if (!q) return QC_ENOMEM;
    int rc = QC_ESOCK;
    q->server = 1;
    if (addr_parse(cfg->bind_host, cfg->port, &q->local, &q->local_len) != 0) { rc = QC_EINVAL; goto fail; }
    q->fd = socket(q->local.ss_family, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (q->fd < 0) goto fail;
    sock_tune(q, q->fd, q->local.ss_family);
    sock_batch(q, cfg->no_batch);
    q->follow = cfg->follow_peer;
    if (bind(q->fd, (struct sockaddr *)&q->local, q->local_len) != 0) goto fail;
    q->local_len = sizeof q->local;
    if (getsockname(q->fd, (struct sockaddr *)&q->local, &q->local_len) != 0) goto fail;
    q->ctx = qcssl_ctx_server(cfg->cert_der, cfg->cert_n, cfg->chain_pem, cfg->chain_pem_n, cfg->key_der, cfg->key_n);
    if (!q->ctx) { rc = QC_ETLS; goto fail; }
    q->own_ctx = 1;
    snprintf(q->alpn, sizeof q->alpn, "%s", cfg->alpn);
    q->datagram_max = cfg->datagram_max;
    q->idle_ms = cfg->idle_ms;
    q->max_data = cfg->max_data;
    q->max_stream_data = cfg->max_stream_data;
    q->max_streams = cfg->max_streams;
    q->send_total = QC_SEND_TOTAL;
    q->send_buf = cfg->send_buf && cfg->send_buf < q->send_total ? cfg->send_buf : q->send_total / 8;
    if (cfg->send_buf >= q->send_total) q->send_buf = q->send_total;
    *out = q;
    return 0;
fail:
    qc_free(q);
    return rc;
}

void qc_probe_stats(const struct qc *q, uint64_t *steps, uint64_t *ops) {
    *steps = q->probe_steps;
    *ops = q->probe_ops;
}

size_t qc_send_held(const struct qc *q) { return q->held; }

unsigned long qc_clock_reads(void) { return g_clock_reads; }

void qc_work_stats(struct qc *q, unsigned *rx_max, size_t *flush_max) {
    *rx_max = q->w_rx_max;
    *flush_max = q->w_fl_max;
    q->w_rx_max = 0;
    q->w_fl_max = 0;
}

void qc_nested_stats(unsigned long *count, size_t *max_bytes) {
    *count = g_nested_n;
    *max_bytes = g_nested_max;
    g_nested_n = 0;
    g_nested_max = 0;
}

int qc_stream_reset_write(struct qc *q, int64_t sid, uint64_t app_err) {
    struct qc_stream *s = q->conn && !q->closed ? stream_find(q, sid) : NULL;
    if (!s || ngtcp2_conn_shutdown_stream_write(q->conn, 0, sid, app_err) != 0) return QC_ENOSTREAM;
    stream_tx_done(q, s);
    kick(q);
    return 0;
}

size_t qc_close_reason_len(const struct qc *q) {
    return q->conn ? ngtcp2_conn_get_ccerr2(q->conn)->reasonlen : 0;
}

uint16_t qc_local_port(const struct qc *q) {
    if (q->local.ss_family == AF_INET6) return ntohs(((const struct sockaddr_in6 *)&q->local)->sin6_port);
    return ntohs(((const struct sockaddr_in *)&q->local)->sin_port);
}

int qc_bbr_view(const struct qc *q, struct qc_bbr_view *v) {
    struct ngcc_bbr_view n;
    int rc = q && q->conn ? ngcc_bbr_view(q->conn, &n) : -1;
    if (rc < 0) return rc;
    *v = (struct qc_bbr_view){ n.longterm, n.bw_lo, n.inflight_lo, n.loss_round_delivered, n.probe_up_rounds,
                               n.too_high, n.loss_in_round, n.full_bw, n.startup, n.drain, n.down };
    return rc;
}

int qc_bbr_poke(struct qc *q, uint64_t longterm, int loss_in_round, uint64_t loss_round_delivered, int drain) {
    return q && q->conn ? ngcc_bbr_poke(q->conn, longterm, loss_in_round, loss_round_delivered, drain) : -1;
}

int qc_bbr_poke_probe(struct qc *q) { return q && q->conn ? ngcc_bbr_poke_probe(q->conn) : -1; }

int qc_bbr_blind(struct qc *q) { return q && q->conn ? ngcc_bbr_blind(q->conn) : -1; }

uint64_t qc_srtt_ns(const struct qc *q) {
    if (!q || !q->conn) return 0;
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    return ci.smoothed_rtt;
}

void qc_clock_set(struct qc *q, uint64_t ns) { q->clock = ns; }

void qc_tally_add(struct qc *q, uint64_t sent, uint64_t lost) {
    q->tally.sent += sent;
    q->tally.lost += lost;
}

void qc_hop_now(struct qc *q) {
    if (q->closed || q->hop_due == UINT64_MAX) return;
    q->now = now_ns();
    hop(q, q->now);
}

uint64_t qc_hop_due_ns(const struct qc *q) { return q ? q->hop_due : UINT64_MAX; }

uint64_t qc_linger_end_ns(const struct qc *q) { return q ? q->pre_end : UINT64_MAX; }

static uint16_t sa_port(const ngtcp2_addr *a) {
    if (a->addr->sa_family == AF_INET6) return ntohs(((const struct sockaddr_in6 *)a->addr)->sin6_port);
    return ntohs(((const struct sockaddr_in *)a->addr)->sin_port);
}

void qc_path_ports(const struct qc *q, uint16_t *ng_local, uint16_t *ng_remote) {
    *ng_local = *ng_remote = 0;
    if (!q || !q->conn) return;
    const ngtcp2_path *p = ngtcp2_conn_get_path(q->conn);
    *ng_local = sa_port(&p->local);
    *ng_remote = sa_port(&p->remote);
}

uint16_t qc_peer_port(const struct qc *srv) { return ss_port(srv->follow ? &srv->peer : &srv->remote); }

static uint64_t pkts_in(const struct qc *q) {
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    return ci.pkt_recv;
}

void qc_sock_ports(const struct qc *q, uint16_t *cur_local, uint16_t *pre_local, int *pre_fd) {
    *cur_local = fd_port(q->fd);
    *pre_local = fd_port(q->pre);
    *pre_fd = q->pre;
}
#endif

uint64_t qc_deadline_ns(struct qc *q) {
    if (!q) return UINT64_MAX;
    if (q->closed) return q->delivered ? UINT64_MAX : 0;
    if (q->pend_err || q->close_req || q->wr_head || q->sopen_pend || q->want_flush || q->rx_more || q->mtu_lost)
        return 0;
    if (q->bw_head && budget_left(q)) return 0;
    if (!q->conn) return UINT64_MAX;
    uint64_t exp = ngtcp2_conn_get_expiry(q->conn), t = q->hop_due < q->pre_end ? q->hop_due : q->pre_end;
    return exp < t ? exp : t;
}

int qc_timeout_ms(struct qc *q) {
    uint64_t exp = qc_deadline_ns(q);
    if (exp == UINT64_MAX) return -1;
    uint64_t now = now_ns();
    if (exp <= now) return 0;
    uint64_t ms = (exp - now + NGTCP2_MILLISECONDS - 1) / NGTCP2_MILLISECONDS;
    return ms > 0x7fffffff ? 0x7fffffff : (int)ms;
}

static uint32_t os_rand32(void) {
    uint32_t x;
    fill_random((uint8_t *)&x, sizeof x);
    return x;
}

static uint64_t hop_interval(struct qc *q) {
    unsigned lo = q->hop_min_s, hi = q->hop_max_s;
    if (!lo) lo = hi = QC_HOP_DEFAULT_S;
    return (uint64_t)(lo + q->rand32() % (hi - lo + 1)) * NGTCP2_SECONDS;
}

static int sock_connect(struct qc *q, int fd, uint16_t port) {
    struct sockaddr_storage a = q->remote;
    if (a.ss_family == AF_INET6) ((struct sockaddr_in6 *)&a)->sin6_port = htons(port);
    else ((struct sockaddr_in *)&a)->sin_port = htons(port);
    return connect(fd, (struct sockaddr *)&a, q->remote_len);
}

static int sock_reuse(int fd) {
    int one = 1;
    return setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
}

static int sock_new(struct qc *q, uint16_t port, int same_local) {
    int fd = socket(q->remote.ss_family, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;
    sock_tune(q, fd, q->remote.ss_family);
    if ((q->prepare && q->prepare(fd) != 0) ||
        (same_local && (sock_reuse(fd) != 0 || bind(fd, (struct sockaddr *)&q->local, q->local_len) != 0)) ||
        sock_connect(q, fd, port) != 0) {
        close(fd);
        return -1;
    }
    return fd;
}

static void pre_close(struct qc *q) {
    if (q->pre < 0) return;
    (void)epoll_ctl(q->ep, EPOLL_CTL_DEL, q->pre, NULL);
    close(q->pre);
    q->pre = -1;
    q->pre_end = UINT64_MAX;
}

static uint64_t linger_of(uint64_t srtt, uint64_t interval) {
    uint64_t lg = 4 * srtt, low = (uint64_t)QC_HOP_LINGER_MIN_MS * NGTCP2_MILLISECONDS;
    if (lg < low) lg = low;
    return lg < interval ? lg : interval;
}

#ifdef QC_WITH_SERVER
uint64_t qc_hop_linger_ns(uint64_t srtt_ns, uint64_t interval_ns) { return linger_of(srtt_ns, interval_ns); }
#endif

static uint64_t hop_linger(const struct qc *q, uint64_t interval) {
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    return linger_of(ci.smoothed_rtt, interval);
}

static void hop_done(struct qc *q) {
    q->hops++;
    if (q->hs_done && q->keepalive_ms) {
        q->ping_req = 1;
        q->want_flush = 1;
    }
}

static void hop(struct qc *q, uint64_t now) {
    q->hop_due = now + hop_interval(q);
    uint16_t to = (q->hop_mode & QC_HOP_REMOTE) && q->hop_n ? port_pick(q->hop_ports, q->hop_n, q->rand32()) : q->port;
    int keep = q->hop_mode == QC_HOP_REMOTE;
    if (keep && to == q->port) {
        hop_done(q);
        return;
    }
    int fd = sock_new(q, to, keep);
    struct epoll_event ev = { .events = EPOLLIN };
    if (fd >= 0 && epoll_ctl(q->ep, EPOLL_CTL_ADD, fd, &ev) != 0) {
        close(fd);
        fd = -1;
    }
    if (fd < 0) {
        q->hop_fails++;
        return;
    }
    pre_close(q);
    q->pre = q->fd;
    q->fd = fd;
    q->pre_end = now + hop_linger(q, q->hop_due - now);
    q->port = to;
    hop_done(q);
}

static int hop_setup(struct qc *q, const struct qc_cfg *cfg) {
    size_t n = cfg->hop_n < PORT_RANGES_MAX ? cfg->hop_n : PORT_RANGES_MAX;
    uint64_t total = 0;
    for (size_t i = 0; i < n; i++) {
        if (!cfg->hop_ports[i].lo || cfg->hop_ports[i].lo > cfg->hop_ports[i].hi) return -1;
        total += (uint64_t)cfg->hop_ports[i].hi - cfg->hop_ports[i].lo + 1;
    }
    if (cfg->hop_n > PORT_RANGES_MAX) return -1;
    if ((cfg->hop_min_s || cfg->hop_max_s) && (cfg->hop_min_s < QC_HOP_FLOOR_S || cfg->hop_max_s < cfg->hop_min_s))
        return -1;
    unsigned mode = cfg->hop_mode & (QC_HOP_LOCAL | QC_HOP_REMOTE);
    if (!(mode & QC_HOP_LOCAL) && (!(mode & QC_HOP_REMOTE) || total < 2)) return 0;
    q->hop_mode = mode;
    q->hop_min_s = cfg->hop_min_s;
    q->hop_max_s = cfg->hop_max_s;
    q->hop_n = cfg->hop_n;
    memcpy(q->hop_ports, cfg->hop_ports, cfg->hop_n * sizeof *cfg->hop_ports);
    q->rand32 = cfg->rand32 ? cfg->rand32 : os_rand32;
    q->hop_due = q->opened + hop_interval(q);
    return 0;
}

int qc_on_timer(struct qc *q) {
    if (q->in_cb) return 0;
    if (q->closed) { deliver(q); return QC_ECLOSED; }
    q->now = now_ns();
    if (q->rx_more) rx_pass(q);
    if (q->conn && !q->pend_err && !q->closed && ngtcp2_conn_get_expiry(q->conn) <= q->now) {
        int rv = ngtcp2_conn_handle_expiry(q->conn, q->now);
        if (rv != 0) fail_close(q, rv);
    }
    if (!q->closed && q->pre_end <= q->now) {
        rx_pass(q);
        if (!q->rx_more) pre_close(q);
    }
    if (!q->closed && q->hop_due <= q->now) {
        if (q->hs_done) hop(q, q->now);
        else q->hop_due = q->now + hop_interval(q);
    }
    return finish(q);
}

static int rx_dgram(struct qc *q, uint8_t *buf, size_t n, const struct sockaddr_storage *from, socklen_t fl) {
#ifdef QC_WITH_SERVER
    int fresh = 0;
#endif
    if (q->filter.rx) {
        size_t m = q->filter.rx(q->filter.user, q->rs->fbuf, buf, n);
        if (!m || m > QC_FILTER_RX_MAX) return 0;
        buf = q->rs->fbuf;
        n = m;
    }
#ifdef QC_WITH_SERVER
    if (!q->conn && q->server) {
        if (srv_accept(q, from, fl, buf, n) != 0) return 0;
        fresh = 1;
    }
#endif
    if (!q->conn) return 0;
    ngtcp2_path path = {
        .local = { .addr = (ngtcp2_sockaddr *)&q->local, .addrlen = q->local_len },
        .remote = { .addr = (ngtcp2_sockaddr *)(q->server && !QC_FOLLOW(q) ? from : &q->remote),
                    .addrlen = q->server && !QC_FOLLOW(q) ? fl : q->remote_len },
    };
    ngtcp2_pkt_info pi = { 0 };
    int lng = q->hs_done && (buf[0] & 0x80);
    ngtcp2_conn_info c0, c1;
    if (lng) ngtcp2_conn_get_conn_info2(q->conn, &c0);
#ifdef QC_WITH_SERVER
    int shrt = !(buf[0] & 0x80);
    uint64_t seen = q->follow && shrt ? pkts_in(q) : 0;
#endif
    int rv = ngtcp2_conn_read_pkt(q->conn, &path, &pi, buf, n, q->now);
    if (lng && q->conn) {
        ngtcp2_conn_get_conn_info2(q->conn, &c1);
        q->rx_long += c1.pkt_recv - c0.pkt_recv;
    }
    if (rv != 0) {
#ifdef QC_WITH_SERVER
        if (fresh && rv == NGTCP2_ERR_DROP_CONN) {
            ngtcp2_conn_del(q->conn);
            q->conn = NULL;
            qcssl_free(q->ssl);
            q->ssl = NULL;
            return 0;
        }
#endif
        if (rv == NGTCP2_ERR_DRAINING) { peer_closed(q); return QC_ECLOSED; }
        if (rv == NGTCP2_ERR_DROP_CONN) { closed(q, QC_CLOSE_ERROR, 0, 0, "dropped"); return QC_ECLOSED; }
        fail_close(q, rv);
        return QC_ECLOSED;
    }
#ifdef QC_WITH_SERVER
    if (q->follow && shrt && q->conn && pkts_in(q) > seen && (fl != q->peer_len || memcmp(from, &q->peer, fl) != 0)) {
        q->peer = *from;
        q->peer_len = fl;
    }
#endif
    return q->closed ? QC_ECLOSED : 0;
}

static int rx_batch(const struct qc *q) { return q->gro_on ? QC_RX_GRO_BATCH : QC_RX_BATCH; }
static size_t rx_slot_len(const struct qc *q) { return q->gro_on ? QC_RX_GRO_SLOT : QC_RX_SLOT; }
static uint8_t *rx_slot(const struct qc *q, int i) { return q->rs->buf + (size_t)i * rx_slot_len(q); }

static int rx_recv(struct qc *q, int fd, int cnt) {
    struct qc_rx *r = &q->rs->rx;
    for (int i = 0; i < cnt; i++) {
        r->iov[i].iov_base = rx_slot(q, i);
        r->iov[i].iov_len = rx_slot_len(q);
        memset(&r->mm[i], 0, sizeof r->mm[i]);
        r->mm[i].msg_hdr.msg_name = &r->from[i];
        r->mm[i].msg_hdr.msg_namelen = sizeof r->from[i];
        r->mm[i].msg_hdr.msg_iov = &r->iov[i];
        r->mm[i].msg_hdr.msg_iovlen = 1;
        if (q->gro_on) {
            r->mm[i].msg_hdr.msg_control = r->ctl[i].b;
            r->mm[i].msg_hdr.msg_controllen = sizeof r->ctl[i].b;
        }
    }
    for (;;) {
        q->rx_calls++;
        int got = recvmmsg(fd, r->mm, (unsigned)cnt, MSG_DONTWAIT, NULL);
        if (got >= 0) return got;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) return 0;
        return errno == ECONNREFUSED ? -1 : -2;
    }
}

static void rx_flush(struct qc *q) {
    if (q->closed || !q->conn || q->pend_err) return;
    int rv = flush(q);
    if (rv != 0 && rv != QC_ECLOSED) q->pend_err = rv;
}

static void rx_auth(struct qc *q) {
    if (!q->conn) return;
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    uint64_t auth = ci.pkt_recv - q->rx_long;
    if (auth == q->rx_pkts) return;
    q->rx_pkts = auth;
    q->rx_last = q->now;
    q->tx_wait = 0;
}

static int rx_want(const struct qc *q, unsigned got) {
    unsigned per = q->gro_on ? QC_GRO_SEGS : 1;
    unsigned k = got < QC_RX_CALL ? (QC_RX_CALL - got) / per : 0;
    return k < (unsigned)rx_batch(q) ? (int)k : rx_batch(q);
}

static int rx_sock(struct qc *q, int fd, unsigned *got, int *calls) {
    while (!q->closed) {
        int want = rx_want(q, *got);
        if (*calls == QC_RX_CALL_BATCHES || want == 0) {
            q->rx_more = 1;
            return -1;
        }
        (*calls)++;
        int cnt = rx_recv(q, fd, want);
        if (cnt == 0) return 0;
        if (cnt == -1) {
            if (!q->hs_done) {
                closed(q, QC_CLOSE_ERROR, 0, 0, "connection refused");
                return 0;
            }
            (*got)++;
            continue;
        }
        if (cnt < 0) {
            q->rx_more = 1;
            return -1;
        }
        q->now = now_ns();
        for (int m = 0; m < cnt && !q->closed; m++) {
            struct msghdr *mh = &q->rs->rx.mm[m].msg_hdr;
            if (mh->msg_flags & MSG_TRUNC) continue;
            uint8_t *base = rx_slot(q, m);
            size_t total = q->rs->rx.mm[m].msg_len;
            size_t seg = q->gro_on ? gro_seg(mh) : 0;
            size_t n;
            for (size_t off = 0; !q->closed && (n = gro_next(total, seg, off)) != 0; off += n) {
                (*got)++;
                QC_WRX(q, *got);
                (void)rx_dgram(q, base + off, n, &q->rs->rx.from[m], mh->msg_namelen);
            }
        }
        rx_auth(q);
        rx_flush(q);
        if (cnt < want) return 0;
    }
    return 0;
}

static void rx_pass(struct qc *q) {
    q->rx_more = 0;
    if (rx_take(q) != 0) {
        q->rx_more = 1;
        return;
    }
    unsigned got = 0;
    int calls = 0;
    if (q->pre < 0 || rx_sock(q, q->pre, &got, &calls) == 0) (void)rx_sock(q, q->fd, &got, &calls);
    rx_give(q);
}

int qc_on_readable(struct qc *q) {
    if (q->in_cb) return 0;
    if (q->closed) {
        pre_close(q);
        if (rx_take(q) == 0)
            for (int i = 0; i < 8 && rx_recv(q, q->fd, rx_batch(q)) == rx_batch(q); i++) {}
        rx_give(q);
        deliver(q);
        return QC_ECLOSED;
    }
    q->now = now_ns();
    rx_pass(q);
    return finish(q);
}

struct qc_tls *qc_tls_new(int insecure, const uint8_t *ca_pem, size_t ca_pem_n, const char *ca_file) {
    struct qc_tls *t = calloc(1, sizeof *t);
    if (!t) return NULL;
    t->ctx = qcssl_ctx_client(!insecure, ca_pem, ca_pem_n, ca_file);
    if (!t->ctx) { free(t); return NULL; }
    return t;
}

void qc_tls_free(struct qc_tls *t) {
    if (!t) return;
    qcssl_ctx_free(t->ctx);
    free(t);
}

int qc_open(const struct qc_cfg *cfg, const struct qc_ops *ops, void *user, struct qc **out) {
    if (!cfg || !ops || !out || !cfg->host || !cfg->port || !cfg->alpn) return QC_EINVAL;
    if (cfg->pin_n && !cfg->pin_sha256) return QC_EINVAL;
    if (cfg->cc != QC_CC_BBR && cfg->cc != QC_CC_RENO) return QC_EINVAL;
    *out = NULL;
    struct qc *q = qc_alloc(ops, user, cfg->filter);
    if (!q) return cfg->filter && cfg->filter->overhead > QC_FILTER_PAD ? QC_EINVAL : QC_ENOMEM;
    int rc = QC_ESOCK;
    q->opened = now_ns();
    if (hop_setup(q, cfg) != 0) { rc = QC_EINVAL; goto fail; }

    if (addr_parse(cfg->host, cfg->port, &q->remote, &q->remote_len) != 0) { rc = QC_EINVAL; goto fail; }
    q->prepare = cfg->prepare;
    q->fd = sock_new(q, cfg->port, 0);
    if (q->fd < 0) goto fail;
    sock_batch(q, cfg->no_batch);
    if (q->hop_mode == QC_HOP_REMOTE && sock_reuse(q->fd) != 0) goto fail;
    if (q->hop_mode) {
        struct epoll_event ev = { .events = EPOLLIN };
        q->ep = epoll_create1(EPOLL_CLOEXEC);
        if (q->ep < 0 || epoll_ctl(q->ep, EPOLL_CTL_ADD, q->fd, &ev) != 0) goto fail;
    }
    q->local_len = sizeof q->local;
    if (getsockname(q->fd, (struct sockaddr *)&q->local, &q->local_len) != 0) goto fail;
    q->port = cfg->port;

    if (cfg->tls) {
        q->ctx = cfg->tls->ctx;
    } else {
        q->ctx = qcssl_ctx_client(!(cfg->insecure || cfg->pin_n), cfg->ca_pem, cfg->ca_pem_n, cfg->ca_file);
        if (!q->ctx) { rc = QC_ETLS; goto fail; }
        q->own_ctx = 1;
    }
    const char *name = cfg->verify_name && cfg->verify_name[0] ? cfg->verify_name : cfg->sni;
    struct qcssl_check chk = {
        .pins = cfg->pin_sha256, .pin_n = cfg->pin_n, .insecure = cfg->insecure,
        .name = name && name[0] ? name : cfg->host,
    };
    snprintf(q->alpn, sizeof q->alpn, "%s", cfg->alpn);
    q->ssl = qcssl_new(q->ctx, &q->ref, cfg->sni, cfg->alpn, 0, cfg->prefer_chacha, &chk);
    if (!q->ssl) { rc = QC_ETLS; goto fail; }

    ngtcp2_settings st;
    ngtcp2_transport_params tp;
    uint16_t probes[2];
    settings_fill(&st, &tp, probes, q->overhead, cfg->datagram_max, cfg->idle_ms, cfg->handshake_ms,
                  cfg->max_data, cfg->max_stream_data, cfg->max_streams, cfg->max_data_win, cfg->max_stream_win,
                  cfg->no_pmtud, cfg->cc);
    q->cc = cfg->cc;
    q->datagram_max = cfg->datagram_max;
    q->send_total = cfg->send_total ? cfg->send_total : QC_SEND_TOTAL;
    q->send_buf = cfg->send_buf && cfg->send_buf < q->send_total ? cfg->send_buf : q->send_total / 8;
    if (cfg->send_buf >= q->send_total) q->send_buf = q->send_total;
    q->brutal_span = cfg->brutal_span;
    q->defer = cfg->defer_flush;

    ngtcp2_cid dcid, scid;
    dcid.datalen = 18;
    fill_random(dcid.data, dcid.datalen);
    scid.datalen = QC_CID_LEN;
    fill_random(scid.data, scid.datalen);
    ngtcp2_path path = {
        .local = { .addr = (ngtcp2_sockaddr *)&q->local, .addrlen = q->local_len },
        .remote = { .addr = (ngtcp2_sockaddr *)&q->remote, .addrlen = q->remote_len },
    };
    if (ngtcp2_conn_client_new(&q->conn, &dcid, &scid, &path, NGTCP2_PROTO_VER_V1, &g_client_cbs,
                               &st, &tp, NULL, q) != 0) { q->conn = NULL; rc = QC_EQUIC; goto fail; }
    ngtcp2_conn_set_tls_native_handle(q->conn, q->ssl);
    (void)ngcc_initcwnd(q->conn, QC_INITCWND, st.initial_ts);
    (void)ngcc_bbr_blind(q->conn);
    q->keepalive_ms = cfg->keepalive_ms;
    q->idle_local_ms = cfg->idle_ms ? cfg->idle_ms : 30000;

    q->now = now_ns();
    if (flush(q) != 0) { rc = QC_EQUIC; goto fail; }
    *out = q;
    return 0;
fail:
    qc_free(q);
    return rc;
}

int qc_handshake_done(const struct qc *q) { return q && q->hs_done; }
uint64_t qc_opened_ns(const struct qc *q) { return q ? q->opened : 0; }
int qc_fd(const struct qc *q) { return q->ep >= 0 ? q->ep : q->fd; }
int qc_fd_extra(const struct qc *q) { return q->ep >= 0 ? 3 : 0; }

static int cc_install(struct qc *q, uint64_t bytes_per_s, int no_loss_comp) {
    if (!q || !q->conn || !bytes_per_s) return QC_EINVAL;
    if (ngcc_brutal(q->conn, bytes_per_s, no_loss_comp, q->now) != 0) return QC_EINVAL;
    q->cc = QC_CC_BRUTAL;
    size_t cap = q->brutal_span ? q->brutal_span : QC_BRUTAL_SPAN_MAX;
    size_t span = bytes_per_s < cap ? (size_t)bytes_per_s : cap;
    if (q->send_buf < span) q->send_buf = span;
    if (q->send_total < 2 * span) q->send_total = 2 * span;
    return 0;
}

int qc_cc_brutal(struct qc *q, uint64_t bytes_per_s, int no_loss_comp) {
    if (q && q->closed) return QC_ECLOSED;
    if (q && !q->hs_done) return QC_EAGAIN;
    return cc_install(q, bytes_per_s, no_loss_comp);
}

#ifdef QC_WITH_SERVER
int qc_cc_brutal_now(struct qc *q, uint64_t bytes_per_s, int no_loss_comp) {
    return cc_install(q, bytes_per_s, no_loss_comp);
}
#endif

static int open_common(struct qc *q, int64_t *sid, int bidi) {
    if (!q || q->closed) return QC_ECLOSED;
    if (!q->hs_done) return QC_EAGAIN;
    int rv = bidi ? ngtcp2_conn_open_bidi_stream(q->conn, sid, NULL) : ngtcp2_conn_open_uni_stream(q->conn, sid, NULL);
    if (rv == NGTCP2_ERR_STREAM_ID_BLOCKED) {
        if (bidi) q->stream_wait = 1;
        return QC_EAGAIN;
    }
    if (rv != 0) return QC_EQUIC;
    struct qc_stream *s = stream_new(q, *sid);
    if (!s || ngtcp2_conn_set_stream_user_data(q->conn, *sid, s) != 0) {
        if (s) stream_drop(q, s);
        (void)ngtcp2_conn_shutdown_stream(q->conn, 0, *sid, 0);
        return QC_ENOMEM;
    }
    return 0;
}

int qc_stream_open(struct qc *q, int64_t *sid) { return open_common(q, sid, 1); }
int qc_stream_open_uni(struct qc *q, int64_t *sid) { return open_common(q, sid, 0); }

ssize_t qc_stream_send(struct qc *q, int64_t sid, const uint8_t *d, size_t n, int fin) {
    if (!q || q->closed) return QC_ECLOSED;
    struct qc_stream *s = stream_find(q, sid);
    if (!s || s->fin_req || s->fin_sent) return QC_ENOSTREAM;
    size_t own = stream_own_room(q, s), bud = stream_budget(q, s);
    size_t room = own < bud ? own : bud;
    size_t take = n < room ? n : room;
    if (take > 0 && stream_append(q, s, d, take) != 0) return QC_ENOMEM;
    full_sync(q, s);
    if (fin && take == n) s->fin_req = 1;
    if (take < n) {
        if (bud < own) {
            s->want_room = 0;
            bw_add(q, s);
        } else {
            bw_del(q, s);
            s->want_room = 1;
        }
    }
    pend_add(q, s);
    kick(q);
    if (n > 0 && take == 0) return QC_EAGAIN;
    return (ssize_t)take;
}

size_t qc_stream_room(const struct qc *q, int64_t sid) {
    const struct qc_stream *s = stream_find(q, sid);
    return s ? stream_room_of(q, s) : 0;
}

size_t qc_stream_queued(const struct qc *q, int64_t sid) {
    const struct qc_stream *s = q ? stream_find(q, sid) : NULL;
    return s ? (size_t)(s->total - s->acked) : 0;
}

size_t qc_send_total(const struct qc *q) { return q ? q->send_total : 0; }
size_t qc_send_buf(const struct qc *q) { return q ? q->send_buf : 0; }
uint64_t qc_hop_fails(const struct qc *q) { return q ? q->hop_fails : 0; }

uint16_t qc_sock_port(const struct qc *q) { return q->hop_mode & QC_HOP_LOCAL ? fd_port(q->fd) : ss_port(&q->local); }

size_t qc_stream_unsent(const struct qc *q, int64_t sid) {
    const struct qc_stream *s = q ? stream_find(q, sid) : NULL;
    return s ? (size_t)(s->total - s->sent) : 0;
}

int qc_stream_stop(struct qc *q, int64_t sid, uint64_t app_err) {
    if (!q || q->closed) return QC_ECLOSED;
    if (!stream_known(q, sid) || ngtcp2_conn_shutdown_stream_read(q->conn, 0, sid, app_err) != 0) return QC_ENOSTREAM;
    kick(q);
    return 0;
}

int qc_stream_reset(struct qc *q, int64_t sid, uint64_t app_err) {
    if (!q || q->closed) return QC_ECLOSED;
    struct qc_stream *s = q->conn ? stream_find(q, sid) : NULL;
    if (!s || ngtcp2_conn_shutdown_stream(q->conn, 0, sid, app_err) != 0) return QC_ENOSTREAM;
    stream_tx_done(q, s);
    kick(q);
    return 0;
}

void qc_stream_consumed(struct qc *q, int64_t sid, size_t n) {
    if (!q || !q->conn || q->closed || !n || !stream_known(q, sid)) return;
    if (ngtcp2_conn_extend_max_stream_offset(q->conn, sid, n) != 0) return;
    ngtcp2_conn_extend_max_offset(q->conn, n);
    q->want_flush = 1;
}

void qc_conn_consumed(struct qc *q, size_t n) {
    if (!q || !q->conn || q->closed || !n) return;
    ngtcp2_conn_extend_max_offset(q->conn, n);
    q->want_flush = 1;
}

void qc_flush(struct qc *q) {
    if (!q || q->in_cb || (!q->want_flush && !q->close_req)) return;
    if (q->close_req && !q->closed) {
        q->close_req = 0;
        do_close(q, q->close_req_code);
        return;
    }
    flush_out(q);
}

void qc_keepalive(struct qc *q, unsigned ms) {
    if (!q) return;
    q->keepalive_ms = ms;
    if (!q->conn) return;
    if (!ms) {
        q->ka_ns = 0;
        ngtcp2_conn_set_keep_alive_timeout(q->conn, UINT64_MAX);
    } else if (q->hs_done) keepalive_tune(q);
}

uint64_t qc_keepalive_ns(const struct qc *q) { return q ? q->ka_ns : 0; }
uint64_t qc_rx_last_ns(const struct qc *q) { return q ? q->rx_last : 0; }
uint64_t qc_tx_wait_ns(const struct qc *q) { return q ? q->tx_wait : 0; }
uint64_t qc_now_ns(const struct qc *q) { return q ? q->now : 0; }

void qc_ping(struct qc *q) {
    if (!q || q->closed || !q->hs_done) return;
    q->ping_req = 1;
    kick(q);
}

unsigned qc_datagram_slots(const struct qc *q) {
    if (!q || q->closed) return 0;
    return QC_DG_QUEUE - q->dg_n;
}

int qc_datagram_send(struct qc *q, const uint8_t *d, size_t n) {
    if (!q || q->closed) return QC_ECLOSED;
    if (!q->hs_done) return QC_EAGAIN;
    if (n > qc_datagram_max(q)) return QC_ETOOBIG;
    if (q->dg_n >= QC_DG_QUEUE) return QC_EAGAIN;
    uint8_t *c = malloc(n ? n : 1);
    if (!c) return QC_ENOMEM;
    memcpy(c, d, n);
    struct qc_dg *g = &q->dgq[(q->dg_head + q->dg_n) % QC_DG_QUEUE];
    g->d = c;
    g->n = n;
    q->dg_n++;
    kick(q);
    return 0;
}

void qc_close(struct qc *q, uint64_t app_err) {
    if (!q || q->closed || q->close_req) return;
    if (q->in_cb || q->defer) {
        q->close_req = 1;
        q->close_req_code = app_err;
        return;
    }
    do_close(q, app_err);
}

void qc_free(struct qc *q) {
    if (!q) return;
    if (q->conn) ngtcp2_conn_del(q->conn);
    qcssl_free(q->ssl);
    if (q->own_ctx) qcssl_ctx_free(q->ctx);
    for (size_t i = 0; i < q->tab.cap; i++)
        if (q->tab.slot[i]) stream_free(q, q->tab.slot[i]);
    free(q->tab.slot);
    while (q->dg_n) dg_pop(q, 0);
    if (q->pre >= 0) close(q->pre);
    if (q->fd >= 0) close(q->fd);
    if (q->ep >= 0) close(q->ep);
    free(q);
}

void qc_stats_get(struct qc *q, struct qc_stats *st) {
    memset(st, 0, sizeof *st);
    if (!q) return;
    st->dg_tx = q->dg_tx;
    st->dg_rx = q->dg_rx;
    st->dg_dropped = q->dg_dropped;
    st->tx_calls = q->tx_calls;
    st->rx_calls = q->rx_calls;
    st->handshake_done = q->hs_done;
    st->gso = q->gso_ok;
    st->gro = q->gro_on;
    st->rcvbuf = q->rcvbuf;
    st->sndbuf = q->sndbuf;
    st->cipher = q->hs_done ? qcssl_cipher(q->ssl) : 0;
    st->cc = q->cc;
    st->hops = q->hops;
    st->hop_fails = q->hop_fails;
    st->port = q->port;
    st->pay_tx = q->pay_tx;
    st->pay_rx = q->pay_rx;
    st->cc_sent = q->tally.sent;
    st->cc_lost = q->tally.lost;
    if (!q->conn) return;
    st->pacing_bps = ngcc_pacing_bps(q->conn);
    st->bytes_acked = ngcc_delivered(q->conn);
    st->bbr_state = ngcc_bbr_state(q->conn, &st->bbr_bw);
    st->ack_q16 = ngcc_brutal_ack_q16(q->conn);
    st->no_loss_comp = ngcc_brutal_no_comp(q->conn);
    ngtcp2_conn_info ci;
    ngtcp2_conn_get_conn_info2(q->conn, &ci);
    st->rtt_us = ci.smoothed_rtt / NGTCP2_MICROSECONDS;
    st->min_rtt_us = ci.min_rtt / NGTCP2_MICROSECONDS;
    st->cwnd = ci.cwnd;
    st->in_flight = ci.bytes_in_flight;
    st->pkt_sent = ci.pkt_sent;
    st->pkt_recv = ci.pkt_recv;
    st->bytes_sent = ci.bytes_sent;
    st->bytes_recv = ci.bytes_recv;
    st->pkt_lost = ci.pkt_lost;
}

void qc_wait_get(const struct qc *q, uint64_t now_ns, uint64_t ns[3]) {
    memcpy(ns, q->wait, sizeof q->wait);
    if (q->hold && now_ns > q->hold_at) ns[q->hold - 1] += now_ns - q->hold_at;
}
