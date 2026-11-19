#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "hy2check.h"
#include "hy2node.h"
#include "hy2run.h"
#include "client.h"

struct pr {
    struct hy2c *c;
    const struct probe_target *tg;
    int ready, closed, open, ended, refused, got;
    int64_t sid;
    uint64_t t_ready, t_sent, t_byte;
    char why[160], msg[128];
};

static uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec;
}

static int ms_of(uint64_t ns) { return (int)(ns / 1000000u); }

static void p_ready(void *u, uint64_t rx, int udp) {
    struct pr *p = u;
    (void)rx;
    (void)udp;
    p->ready = 1;
    p->t_ready = now_ns();
}

static void p_data(void *u, int64_t sid, const uint8_t *d, size_t n, int fin) {
    struct pr *p = u;
    (void)d;
    (void)fin;
    if (!p->open || sid != p->sid || !n || p->got) return;
    p->got = 1;
    p->t_byte = now_ns();
}

static void p_sid(void *u, int64_t sid) { (void)u; (void)sid; }

static void p_none(void *u) { (void)u; }

static void p_refused(void *u, int64_t sid, const char *msg) {
    struct pr *p = u;
    if (!p->open || sid != p->sid) return;
    p->refused = 1;
    snprintf(p->msg, sizeof p->msg, "%s", msg && msg[0] ? msg : "no reason given");
}

static void p_sclose(void *u, int64_t sid, int how) {
    struct pr *p = u;
    (void)how;
    if (p->open && sid == p->sid) p->ended = 1;
}

static void p_udp(void *u, uint32_t s, const char *a, size_t an, const uint8_t *d, size_t n) {
    (void)u; (void)s; (void)a; (void)an; (void)d; (void)n;
}

static void p_closed(void *u, int reason, const char *why) {
    struct pr *p = u;
    (void)reason;
    p->closed = 1;
    snprintf(p->why, sizeof p->why, "%s", why && why[0] ? why : "the connection closed");
}

static const struct hy2c_ops g_ops = {
    .on_ready = p_ready,
    .on_stream_data = p_data,
    .on_stream_writable = p_sid,
    .on_streams_open = p_none,
    .on_stream_refused = p_refused,
    .on_stream_close = p_sclose,
    .on_udp = p_udp,
    .on_closed = p_closed,
};

static int ready_step(void *u) { return ((struct pr *)u)->ready; }

static int heard(struct hy2c *c) {
    struct qc_stats st;
    hy2c_stats(c, &st);
    return st.pkt_recv > 0;
}

static int target_step(void *u) {
    struct pr *p = u;
    if (!p->open) {
        int rc = hy2c_tcp_open(p->c, p->tg->host, 80, &p->sid);
        if (rc == QC_EAGAIN) return 0;
        if (rc != 0) return 2;
        p->open = 1;
        char get[128];
        size_t n = probe_get(p->tg, get, sizeof get);
        ssize_t w = hy2c_tcp_send(p->c, p->sid, (const uint8_t *)get, n);
        p->t_sent = now_ns();
        if (!n || w != (ssize_t)n) return 2;
    }
    if (p->got) return 1;
    return p->ended || p->refused ? 2 : 0;
}

static int targets(struct pr *p, const struct probe_target *tg, uint64_t end, char *why, size_t why_n, int *ttfb_ms) {
    for (int k = 0; k < 2; k++) {
        uint64_t now = now_ns();
        uint64_t until = k == 0 && end > now ? now + (end - now) / 2 : end;
        p->tg = &tg[k];
        p->open = p->ended = p->refused = p->got = 0;
        int r = target_step(p);
        if (r == 0) r = hy2_run(p->c, until, target_step, p, NULL);
        if (r == 1) {
            if (ttfb_ms) *ttfb_ms = ms_of(p->t_byte - p->t_sent);
            snprintf(why, why_n, "ok, %s answered", tg[k].host);
            return 0;
        }
        if (p->open && !p->ended && !p->closed) hy2c_tcp_close(p->c, p->sid);
        if (p->closed) {
            snprintf(why, why_n, "%s", p->why);
            return HY2_RUN_CLOSED;
        }
        if (p->refused) snprintf(why, why_n, "%s refused: %s", tg[k].host, p->msg);
        else if (p->ended) snprintf(why, why_n, "%s ended the stream with no data", tg[k].host);
        else snprintf(why, why_n, "the node relayed no data from %s", tg[k].host);
    }
    return HY2_RUN_TIMEOUT;
}

static int probe(const struct vless_node *n, int timeout_s, char *why, size_t why_n, int *handshake_ms, int *ttfb_ms,
                 const struct probe_target *tg) {
    if (handshake_ms) *handshake_ms = -1;
    if (ttfb_ms) *ttfb_ms = -1;
    uint64_t t0 = now_ns(), end = t0 + (uint64_t)(timeout_s > 0 ? timeout_s : 8) * 1000000000u;
    struct hy2_nodecfg cfg;
    for (unsigned i = 0;; i++) {
        int count = hy2_nodecfg(n, i, &cfg);
        if (count < 0) {
            if (count == HY2_ENOADDR) snprintf(why, why_n, "%s does not resolve", n->host);
            else if (count == HY2_EPIN) snprintf(why, why_n, "pinSHA256: not a hex SHA-256");
            else snprintf(why, why_n, "out of memory");
            return count;
        }
        cfg.c.qc.keepalive_ms = 0;
        cfg.c.qc.hop_mode = 0;
        struct pr p;
        memset(&p, 0, sizeof p);
        uint64_t hear = now_ns() + (uint64_t)HY2_ANSWER_MS * 1000000u;
        int rc = hy2c_open(&cfg.c, &g_ops, &p, &p.c);
        if (rc == 0) {
            rc = hy2_run(p.c, hear < end ? hear : end, ready_step, &p, NULL);
            if (rc == HY2_RUN_TIMEOUT && !heard(p.c) && n->ports[0])
                snprintf(p.why, sizeof p.why, "no answer from %s on port %u", cfg.ip, (unsigned)cfg.c.qc.port);
            else if (rc == HY2_RUN_TIMEOUT && !heard(p.c)) snprintf(p.why, sizeof p.why, "no answer from %s", cfg.ip);
            else if (rc == HY2_RUN_TIMEOUT) rc = hy2_run(p.c, end, ready_step, &p, NULL);
            if (rc == QC_ESOCK) snprintf(p.why, sizeof p.why, "cannot watch the socket to %s", cfg.ip);
        } else snprintf(p.why, sizeof p.why, "cannot open a socket to %s (%d)", cfg.ip, rc);
        if (p.ready) {
            if (handshake_ms) *handshake_ms = ms_of(p.t_ready - t0);
            rc = targets(&p, tg, end, why, why_n, ttfb_ms);
            hy2c_close(p.c);
            hy2c_free(p.c);
            return rc;
        }
        if (!p.why[0]) snprintf(p.why, sizeof p.why, "%s answered but was not ready within %d s", cfg.ip, timeout_s > 0 ? timeout_s : 8);
        hy2c_close(p.c);
        hy2c_free(p.c);
        if (i + 1 >= (unsigned)count || now_ns() >= end) {
            snprintf(why, why_n, "%s", p.why);
            return rc ? rc : HY2_RUN_TIMEOUT;
        }
    }
}

int hy2_probe_timed(const struct vless_node *n, int timeout_s, char *why, size_t why_n, int *handshake_ms, int *ttfb_ms) {
    return probe(n, timeout_s, why, why_n, handshake_ms, ttfb_ms, PROBE_TARGETS);
}

int hy2_probe6(const struct vless_node *n, int timeout_s, char *why, size_t why_n) {
    return probe(n, timeout_s, why, why_n, NULL, NULL, PROBE6_TARGETS);
}
