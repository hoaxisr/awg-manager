#define _GNU_SOURCE
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <arpa/inet.h>

#include "hy2node.h"
#include "hy2link.h"
#include "osrand.h"
#include "portpick.h"
#include "roots.h"
#include "sockmark.h"
#include "transport.h"

_Static_assert(HY2_RANGES_MAX == PORT_RANGES_MAX, "a node's port list and the QUIC layer's table hold as many ranges");

static struct qc_tls *g_tls;
static pthread_once_t g_tls_once = PTHREAD_ONCE_INIT;

static void tls_make(void) {
    g_tls = qc_tls_new(0, NULL, 0, tls_cert_roots());
    if (!g_tls) g_tls = qc_tls_new(1, NULL, 0, NULL);
}

struct qc_tls *hy2_tls(void) {
    pthread_once(&g_tls_once, tls_make);
    return g_tls;
}

static int is_ip(const char *s) {
    unsigned char b[16];
    return inet_pton(AF_INET, s, b) == 1 || inet_pton(AF_INET6, s, b) == 1;
}

static int hexv(int c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    return -1;
}

static int pins_parse(const char *s, uint8_t *out, size_t *n) {
    for (size_t k = 0; k < 8; k++) {
        for (int i = 0; i < 32; i++) {
            int h = hexv((unsigned char)s[2 * i]);
            int l = h < 0 ? -1 : hexv((unsigned char)s[2 * i + 1]);
            if (l < 0) return -1;
            out[32 * k + (size_t)i] = (uint8_t)(h << 4 | l);
        }
        s += 64;
        if (!*s) {
            *n = k + 1;
            return 0;
        }
        if (*s++ != ',') return -1;
    }
    return -1;
}

static void win_pick(uint64_t dflt, uint64_t cap, uint64_t init, uint64_t max, uint64_t *start, uint64_t *ceil) {
    if (init > cap) init = cap;
    if (max > cap) max = cap;
    *start = init ? init : (max && max < dflt ? max : dflt);
    *ceil = max > *start ? max : (max ? *start : 0);
}

int hy2_no_batch;

struct hy2_budget g_hy2_budget = { 16u << 20, 2u << 20, 16u << 20 };

void hy2_budget_set(uint64_t mem_bytes) {
    int small = mem_bytes < (384ull << 20);
    g_hy2_budget.send_total = small ? 8u << 20 : 16u << 20;
    g_hy2_budget.send_buf = 2u << 20;
    g_hy2_budget.brutal_span = small ? 4u << 20 : 16u << 20;
}

void hy2_budget_init(const char *meminfo, char *line, size_t n) {
    FILE *f = fopen(meminfo, "r");
    unsigned long long kb = 0;
    int found = 0;
    if (f) {
        char l[128];
        while (!found && fgets(l, sizeof l, f)) found = sscanf(l, "MemTotal: %llu kB", &kb) == 1;
        fclose(f);
    }
    hy2_budget_set(found ? (uint64_t)kb << 10 : 0);
    const struct hy2_budget *b = &g_hy2_budget;
    int k = snprintf(line, n, "Hysteria2 send budgets: %zu MiB per link, %zu MiB per stream, Brutal up to %zu MiB per stream and %zu MiB per link",
                     b->send_total >> 20, b->send_buf >> 20, b->brutal_span >> 20, b->brutal_span >> 19);
    if (k < 0 || (size_t)k >= n) return;
    if (found) snprintf(line + k, n - (size_t)k, " (memory %llu MiB)", kb >> 10);
    else snprintf(line + k, n - (size_t)k, " (memory size unknown, the small tier)");
}

static uint32_t os_rand32(void) {
    uint32_t x = 0;
    if (os_rand_seed((unsigned char *)&x, sizeof x) != 0) x = 0;
    return x;
}

uint32_t (*g_hy2_rand)(void) = os_rand32;

static uint16_t dial_port(const struct vless_node *n) {
    struct port_range r[PORT_RANGES_MAX];
    size_t cnt = n->ports[0] && (n->hop_mode & (HY2_HOP_REMOTE | HY2_HOP_ONCE)) ? port_ranges(n->ports, r, PORT_RANGES_MAX) : 0;
    return cnt ? port_pick(r, cnt, g_hy2_rand()) : n->port;
}

static void hop_fill(const struct vless_node *n, struct qc_cfg *q) {
    q->hop_mode = (n->hop_mode & HY2_HOP_LOCAL ? QC_HOP_LOCAL : 0u) | (n->hop_mode & HY2_HOP_REMOTE ? QC_HOP_REMOTE : 0u);
    if (!q->hop_mode) return;
    q->hop_min_s = n->hop_from;
    q->hop_max_s = n->hop_to;
    q->rand32 = g_hy2_rand;
    if (n->ports[0] && (q->hop_mode & QC_HOP_REMOTE)) q->hop_n = port_ranges(n->ports, q->hop_ports, PORT_RANGES_MAX);
}

int hy2_nodecfg(const struct vless_node *n, unsigned addr_i, struct hy2_nodecfg *out) {
    memset(out, 0, sizeof *out);
    int named = !is_ip(n->host), count = 1;
    if (!named) memcpy(out->ip, n->host, strlen(n->host) + 1);
    else if ((count = transport_node_ip(n->host, addr_i, out->ip, sizeof out->ip)) <= 0) return HY2_ENOADDR;
    size_t pn = 0;
    if (n->pcs && pins_parse(n->pcs, out->pins, &pn) != 0) return HY2_EPIN;
    if (n->sni[0]) snprintf(out->sni, sizeof out->sni, "%s", n->sni);
    else if (named) snprintf(out->sni, sizeof out->sni, "%s", n->host);
    struct qc_cfg *q = &out->c.qc;
    q->host = out->ip;
    q->port = dial_port(n);
    q->sni = out->sni[0] ? out->sni : NULL;
    q->verify_name = n->vcn;
    q->no_batch = hy2_no_batch;
    q->send_total = g_hy2_budget.send_total;
    q->send_buf = g_hy2_budget.send_buf;
    q->brutal_span = g_hy2_budget.brutal_span;
    q->insecure = n->insecure;
    q->pin_sha256 = pn ? out->pins : NULL;
    q->pin_n = pn;
    q->prepare = tr_sock_prepare;
    hop_fill(n, q);
    q->tls = hy2_tls();
    q->handshake_ms = 8000;
    q->keepalive_ms = 10000;
    struct hy2_qp qp;
    hy2_node_qp(n, &qp);
    if (!qp.no_parrot) {
        out->qp_unparroted = (qp.init_stream_win ? 1u : 0) | (qp.max_stream_win ? 2u : 0) | (qp.init_conn_win ? 4u : 0) |
                             (qp.max_conn_win ? 8u : 0) | (qp.idle_s ? 16u : 0);
        if (out->qp_unparroted) out->qp_ignored |= HY2_QP_IGN_CHROME;
        qp.idle_s = 0;
        qp.init_stream_win = qp.max_stream_win = qp.init_conn_win = qp.max_conn_win = 0;
    }
    out->qp_ignored |= qp.ignored;
    win_pick(2u << 20, 8u << 20, qp.init_stream_win, qp.max_stream_win, &q->max_stream_data, &q->max_stream_win);
    win_pick(12u << 20, 20u << 20, qp.init_conn_win, qp.max_conn_win, &q->max_data, &q->max_data_win);
    if (qp.idle_s) q->idle_ms = qp.idle_s * 1000u;
    if (qp.keepalive_s) q->keepalive_ms = qp.keepalive_s * 1000u;
    q->no_pmtud = qp.no_pmtud;
    out->c.rx_bps = qp.down_bps;
    out->c.up_bps = qp.up_bps;
    out->c.cc = qp.cc;
    out->c.no_loss_comp = qp.no_loss_comp;
    if (qp.cc == HY2_CC_RENO) q->cc = QC_CC_RENO;
    out->c.auth = n->auth;
    out->c.obfs_password = !strcmp(n->obfs, "salamander") ? n->obfs_pw : NULL;
    return q->tls ? count : QC_ENOMEM;
}
