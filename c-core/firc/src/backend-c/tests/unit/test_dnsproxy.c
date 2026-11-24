#include "greatest.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/epoll.h>
#include <sys/time.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <sys/socket.h>
#include <unistd.h>

#include "firc/dnsproxy.h"
#include "firc/doh.h"
#include "firc/anscache.h"
#include "firc/events.h"
#include "firc/dnswire.h"
#include "firc/fakeip.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/loop.h"
#include "firc/resolveroute.h"
#include "firc/dnspipeline.h"
#include "firc/dnsrewrite.h"
#include "firc/models.h"
#include "firc/rulesnap.h"

static bool bind_loopback(int fd, uint16_t port, uint16_t *got) {
    struct sockaddr_in a;
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    a.sin_port = htons(port);
    if (bind(fd, (struct sockaddr *)&a, sizeof(a)) != 0) { return false; }
    if (got != NULL) {
        socklen_t l = sizeof(a);
        if (getsockname(fd, (struct sockaddr *)&a, &l) != 0) { return false; }
        *got = ntohs(a.sin_port);
    }
    return true;
}

static void ephemeral_range(unsigned *lo, unsigned *hi) {
    *lo = 32768, *hi = 60999;
    FILE *f = fopen("/proc/sys/net/ipv4/ip_local_port_range", "r");
    if (f != NULL) {
        if (fscanf(f, "%u %u", lo, hi) != 2) { *lo = 32768, *hi = 60999; }
        fclose(f);
    }
}

static bool port_free_both(uint16_t port) {
    int u = socket(AF_INET, SOCK_DGRAM, 0);
    int t = socket(AF_INET, SOCK_STREAM, 0);
    bool ok = u >= 0 && t >= 0 && bind_loopback(u, port, NULL) && bind_loopback(t, port, NULL);
    if (u >= 0) { close(u); }
    if (t >= 0) { close(t); }
    return ok;
}

/* Ports outside the ephemeral range, from the top down, so no socket the test opens can be handed one before it is bound. */
static bool free_ports(uint16_t *a_out, uint16_t *b_out) {
    unsigned lo, hi;
    ephemeral_range(&lo, &hi);
    uint16_t *outs[2] = {a_out, b_out};
    int found = 0;
    for (unsigned cand = 65535; cand >= 1024 && found < 2; cand--) {
        if (cand >= lo && cand <= hi) { continue; }
        if (port_free_both((uint16_t)cand)) { *outs[found++] = (uint16_t)cand; }
    }
    return found == 2;
}

static size_t build_query_for(uint8_t *buf, uint16_t id, uint16_t edns_size, char first) {
    size_t p = 0;
    buf[p++] = (uint8_t)(id >> 8);
    buf[p++] = (uint8_t)id;
    buf[p++] = 0x01;
    buf[p++] = 0x00;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = 0x00;
    buf[p++] = 0x00; buf[p++] = 0x00;
    buf[p++] = 0x00; buf[p++] = edns_size ? 0x01 : 0x00;
    const char first_label[2] = {first, '\0'};
    const char *labels[] = {first_label, "example", "com"};
    for (size_t i = 0; i < 3; i++) {
        size_t l = strlen(labels[i]);
        buf[p++] = (uint8_t)l;
        memcpy(buf + p, labels[i], l);
        p += l;
    }
    buf[p++] = 0;
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_A;
    buf[p++] = 0x00; buf[p++] = 0x01;
    if (edns_size) {
        buf[p++] = 0;
        buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_OPT;
        buf[p++] = (uint8_t)(edns_size >> 8); buf[p++] = (uint8_t)edns_size;
        buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 0;
        buf[p++] = 0; buf[p++] = 0;
    }
    return p;
}

/* A query for a.example.com IN A, with an OPT record advertising `edns_size` when it is not 0. */
static size_t build_query(uint8_t *buf, uint16_t id, uint16_t edns_size) {
    return build_query_for(buf, id, edns_size, 'a');
}

static size_t build_ptr_query(uint8_t *buf, uint16_t id, const firc_ip_t *ip, bool edns) {
    size_t p = 0;
    const uint8_t head[12] = {(uint8_t)(id >> 8), (uint8_t)id, 0x01, 0, 0, 1, 0, 0, 0, 0, 0, edns ? 1 : 0};
    memcpy(buf, head, sizeof(head));
    p = sizeof(head);
    static const char hex[] = "0123456789abcdef";
    for (int i = ip->len - 1; i >= 0; i--) {
        if (ip->len == 4) {
            int n = snprintf((char *)buf + p + 1, 4, "%u", ip->b[i]);
            buf[p] = (uint8_t)n;
            p += 1 + (size_t)n;
            continue;
        }
        buf[p++] = 1;
        buf[p++] = (uint8_t)hex[ip->b[i] & 0x0f];
        buf[p++] = 1;
        buf[p++] = (uint8_t)hex[ip->b[i] >> 4];
    }
    const char *zone = ip->len == 4 ? "\007in-addr\004arpa" : "\003ip6\004arpa";
    size_t zl = strlen(zone) + 1;
    memcpy(buf + p, zone, zl);
    p += zl;
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_PTR;
    buf[p++] = 0x00; buf[p++] = 0x01;
    if (edns) {
        const uint8_t opt[11] = {0, 0, FIRC_DNS_TYPE_OPT, 0x04, 0xd0, 0, 0, 0, 0, 0, 0};
        memcpy(buf + p, opt, sizeof(opt));
        p += sizeof(opt);
    }
    return p;
}

#define BIG_A_OVER_512 40
static unsigned g_big_a;
static unsigned g_big_aaaa;
static bool g_padded_opt;
#define STUB_BUF 2048

static _Atomic int g_tcp_bad_len_once;
static _Atomic int g_tcp_sink_a_once;
static _Atomic int g_tcp_wrong_id_once;

/* The query echoed back as an answer with one A record, 10.1.2.3, and no OPT record. */
static size_t build_answer(const uint8_t *q, size_t qlen, uint8_t *buf) {
    size_t qd_end = 12;
    while (q[qd_end] != 0) { qd_end += 1 + q[qd_end]; }
    qd_end += 1 + 4;
    memcpy(buf, q, qd_end);
    buf[2] = 0x81;
    buf[3] = 0x80;
    buf[10] = 0; buf[11] = 0;
    qlen = qd_end;
    if (g_big_a) {
        buf[6] = 0x00; buf[7] = (uint8_t)(g_big_a + g_big_aaaa);
        size_t p = qlen;
        for (unsigned i = 0; i < g_big_a; i++) {
            buf[p++] = 0xc0; buf[p++] = 0x0c;
            buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_A;
            buf[p++] = 0x00; buf[p++] = 0x01;
            buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x01; buf[p++] = 0x2c;
            buf[p++] = 0x00; buf[p++] = 0x04;
            buf[p++] = 10; buf[p++] = 1; buf[p++] = 2; buf[p++] = (uint8_t)(3 + i);
        }
        for (unsigned i = 0; i < g_big_aaaa; i++) {
            buf[p++] = 0xc0; buf[p++] = 0x0c;
            buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_AAAA;
            buf[p++] = 0x00; buf[p++] = 0x01;
            buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x01; buf[p++] = 0x2c;
            buf[p++] = 0x00; buf[p++] = 0x10;
            buf[p++] = 0x20; buf[p++] = 0x01; buf[p++] = 0x0d; buf[p++] = 0xb8;
            for (int k = 0; k < 11; k++) { buf[p++] = 0; }
            buf[p++] = (uint8_t)(1 + i);
        }
        if (g_padded_opt) {
            buf[10] = 0; buf[11] = 1;
            buf[p++] = 0;
            buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_OPT;
            buf[p++] = 0x10; buf[p++] = 0x00;
            buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 0;
            buf[p++] = 0x01; buf[p++] = 0xe4;
            buf[p++] = 0x00; buf[p++] = 12;
            buf[p++] = 0x01; buf[p++] = 0xe0;
            memset(buf + p, 0, 480);
            p += 480;
        }
        return p;
    }
    buf[6] = 0x00; buf[7] = 0x02;
    size_t p = qlen;
    buf[p++] = 0xc0; buf[p++] = 0x0c;
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_A;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x01; buf[p++] = 0x2c;
    buf[p++] = 0x00; buf[p++] = 0x04;
    buf[p++] = 10; buf[p++] = 1; buf[p++] = 2; buf[p++] = 3;

    memcpy(buf + p, q + 12, qd_end - 4 - 12);
    p += qd_end - 4 - 12;
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_AAAA;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x01; buf[p++] = 0x2c;
    buf[p++] = 0x00; buf[p++] = 0x10;
    buf[p++] = 0x20; buf[p++] = 0x01; buf[p++] = 0x0d; buf[p++] = 0xb8;
    for (int i = 0; i < 11; i++) { buf[p++] = 0; }
    buf[p++] = 0x01;
    return p;
}

typedef struct {
    firc_loop_t *loop;
    int upstream_fd;
    int upstream_tcp_fd;
    bool rewrite;
    bool report;
    bool hold;
    bool retime;
    int hook_calls;
    bool saw_client;
    uint8_t last_client[4];
} fixture_t;

/* Stands in for the pipeline: rewrites the answer's address. */
static firc_dns_verdict_t hook(firc_dns_msg_t *msg, const firc_ip_t *client, const char *network,
                               firc_dns_resolver_t resolver, void *ud) {
    fixture_t *fx = (fixture_t *)ud;
    (void)network;
    (void)resolver;
    fx->hook_calls++;
    if (client != NULL && client->len == 4) {
        memcpy(fx->last_client, client->b, 4);
        fx->saw_client = true;
    }
    if (fx->retime) {
        for (size_t i = 0; i < msg->n_answers; i++) {
            if (msg->answers[i].rtype == FIRC_DNS_TYPE_A ||
                msg->answers[i].rtype == FIRC_DNS_TYPE_AAAA) {
                msg->answers[i].ttl = 60;
            }
        }
        return FIRC_DNS_RETIMED;
    }
    if (!fx->rewrite) { return FIRC_DNS_PASS; }
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == FIRC_DNS_TYPE_A && msg->answers[i].rdata_len == 4) {
            msg->answers[i].rdata[0] = 198;
            msg->answers[i].rdata[1] = 18;
            msg->answers[i].rdata[2] = 0;
            msg->answers[i].rdata[3] = 7;
        }
    }
    if (!fx->report) { return FIRC_DNS_PASS; }
    return fx->hold ? FIRC_DNS_HOLD : FIRC_DNS_REWRITTEN;
}

typedef struct {
    _Atomic int opens, marks, connects, sends;
    _Atomic uint32_t last_mark;
    _Atomic int fail_open, fail_open_next, fail_mark, fail_connect, fail_send;
    _Atomic int marked_fd;
    _Atomic int connects_marked;
    _Atomic int stream_opens;
} rec_t;

static rec_t g_rec;

static void rec_reset(void) {
    memset(&g_rec, 0, sizeof(g_rec));
    atomic_store(&g_rec.marked_fd, -1);
}

static int rec_open(void *ud, int family, int type) {
    rec_t *r = ud;
    atomic_fetch_add(&r->opens, 1);
    if ((type & 0xf) == SOCK_STREAM) { atomic_fetch_add(&r->stream_opens, 1); }
    int e = atomic_exchange(&r->fail_open_next, 0);
    if (e == 0) { e = atomic_load(&r->fail_open); }
    if (e != 0) {
        errno = e;
        return -1;
    }
    int fd = firc_dnsproxy_sock_ops_real.open(NULL, family, type);
    int was = fd;
    if (fd >= 0) { atomic_compare_exchange_strong(&r->marked_fd, &was, -1); }
    return fd;
}

static int rec_mark(void *ud, int fd, uint32_t mark) {
    rec_t *r = ud;
    atomic_fetch_add(&r->marks, 1);
    atomic_store(&r->last_mark, mark);
    atomic_store(&r->marked_fd, fd);
    int e = atomic_load(&r->fail_mark);
    if (e != 0) {
        errno = e;
        return -1;
    }
    return 0;
}

static int rec_connect(void *ud, int fd, const struct sockaddr *sa, socklen_t len) {
    rec_t *r = ud;
    atomic_fetch_add(&r->connects, 1);
    bool marked = fd == atomic_load(&r->marked_fd);
    if (marked) { atomic_fetch_add(&r->connects_marked, 1); }
    int e = marked ? atomic_load(&r->fail_connect) : 0;
    if (e != 0) {
        errno = e;
        return -1;
    }
    return firc_dnsproxy_sock_ops_real.connect(NULL, fd, sa, len);
}

static ssize_t rec_send(void *ud, int fd, const void *buf, size_t len, int flags) {
    rec_t *r = ud;
    atomic_fetch_add(&r->sends, 1);
    int e = fd == atomic_load(&r->marked_fd) ? atomic_load(&r->fail_send) : 0;
    if (e != 0) {
        errno = e;
        return -1;
    }
    return firc_dnsproxy_sock_ops_real.send(NULL, fd, buf, len, flags);
}

static const firc_dnsproxy_sock_ops_t REC_OPS = {
    .open = rec_open, .set_mark = rec_mark, .connect = rec_connect, .send = rec_send, .ud = &g_rec,
};

static const firc_dnsproxy_sock_ops_t *g_live_ops;

typedef struct {
    bool take;
    uint32_t mark;
    uint16_t ports[4];
    size_t n;
    uint64_t gen;
    uint8_t group;
    _Atomic int asked;
    _Atomic int saw_client;
    const firc_resolve_route_t *own;
    const char *name;
    uint16_t url_ids[4];
    bool dns_only;
    bool no_fallback;
} troute_t;

static troute_t g_troute;

static bool test_route(void *ud, const firc_dns_msg_t *q, const firc_ip_t *client, firc_dnsproxy_route_t *out) {
    troute_t *t = ud;
    (void)q;
    atomic_fetch_add(&t->asked, 1);
    if (client != NULL && client->len == 4 && client->b[0] == 127 && client->b[3] == 1) {
        atomic_store(&t->saw_client, 1);
    }
    if (!t->take) { return false; }
    memset(out, 0, sizeof(*out));
    out->group_id.b[3] = t->group ? t->group : 7;
    snprintf(out->group_name, sizeof(out->group_name), "%s", t->name != NULL ? t->name : "");
    out->mark = t->mark;
    out->gen = t->gen != 0 ? t->gen : 1;
    if (t->own != NULL) {
        snprintf(out->group_name, sizeof(out->group_name), "%s", t->own->group_name);
        out->n_servers = t->own->n_servers;
        for (size_t i = 0; i < t->own->n_servers; i++) {
            firc_resolver_addr_sockaddr(&t->own->servers[i], &out->servers[i], &out->server_lens[i]);
        }
        return true;
    }
    out->n_servers = t->n;
    for (size_t i = 0; i < t->n; i++) {
        struct sockaddr_in *s4 = (struct sockaddr_in *)&out->servers[i];
        s4->sin_family = AF_INET;
        s4->sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        s4->sin_port = htons(t->ports[i]);
        out->server_lens[i] = sizeof(*s4);
        if (t->url_ids[i] != 0) {
            memset(&out->servers[i], 0, sizeof(out->servers[i]));
            out->server_lens[i] = 0;
            out->url_ids[i] = t->url_ids[i];
        }
    }
    out->dns_only = t->dns_only;
    out->no_fallback = t->no_fallback;
    return true;
}

static troute_t *g_live_route;
static firc_resolve_router_t *g_live_router;
static uint32_t g_live_clamp;
static _Atomic int g_blocked_calls;
static _Atomic int g_blocked_answer = -1;
static _Atomic int g_blocked_client = -1;

static void live_blocked(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                         firc_block_answer_t answer) {
    (void)ud;
    (void)query;
    atomic_fetch_add(&g_blocked_calls, 1);
    atomic_store(&g_blocked_answer, (int)answer);
    atomic_store(&g_blocked_client, client != NULL && client->len == 4 ? client->b[3] : -1);
}
typedef struct {
    char first;
    uint8_t owner;
    bool blocked;
    bool client;
} tally_t;

static tally_t g_tallies[32];
static _Atomic int g_tally_n;
static const firc_fakeip_t *g_live_pool;
static firc_dnsproxy_route_fn g_live_route_fn;

static void live_tally(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client, firc_id_t owner,
                       bool blocked) {
    (void)ud;
    int n = atomic_load(&g_tally_n);
    if (n >= (int)(sizeof(g_tallies) / sizeof(g_tallies[0]))) { return; }
    char name[256];
    g_tallies[n].first = firc_dns_msg_queried_name(query, name, sizeof(name)) ? name[0] : '?';
    g_tallies[n].owner = owner.b[3];
    g_tallies[n].blocked = blocked;
    g_tallies[n].client = client != NULL;
    atomic_store(&g_tally_n, n + 1);
}
static uint32_t g_live_timeout_ms;
static uint16_t g_live_upstream_port;
static uint32_t g_live_max_concurrent;
static uint32_t g_live_rest_ms;

static long ms_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1000L + ts.tv_nsec / 1000000L;
}

/* A free loopback UDP port outside the ephemeral range, so no socket of the test can be handed it. */
static uint16_t closed_udp_port(void) {
    unsigned lo, hi;
    ephemeral_range(&lo, &hi);
    for (unsigned cand = hi >= 65535 ? 1024 : hi + 1; cand != lo; cand = cand >= 65535 ? 1024 : cand + 1) {
        if (cand >= lo && cand <= hi) { continue; }
        int fd = socket(AF_INET, SOCK_DGRAM, 0);
        if (fd < 0) { return 0; }
        bool ok = bind_loopback(fd, (uint16_t)cand, NULL);
        close(fd);
        if (ok) { return (uint16_t)cand; }
    }
    return 0;
}

/* The UDP stub upstream: one query in, one canned answer out, on the loop. */
static void on_upstream_query(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    (void)events;
    (void)ud;
    uint8_t q[512], a[STUB_BUF];
    struct sockaddr_storage from;
    socklen_t fl = sizeof(from);
    ssize_t n = recvfrom(fd, q, sizeof(q), 0, (struct sockaddr *)&from, &fl);
    if (n <= 0) { return; }
    size_t alen = build_answer(q, (size_t)n, a);
    sendto(fd, a, alen, 0, (struct sockaddr *)&from, fl);
}

/* The TCP stub runs on its own thread: on the loop it would block the proxy's send and deadlock. */
static void *serve_upstream_tcp(void *arg) {
    int c = (int)(intptr_t)arg;
    uint8_t hdr[2];
    if (recv(c, hdr, 2, MSG_WAITALL) != 2) {
        close(c);
        return NULL;
    }
    size_t qlen = ((size_t)hdr[0] << 8) | hdr[1];
    uint8_t q[512], a[STUB_BUF];
    if (qlen > sizeof(q) || recv(c, q, qlen, MSG_WAITALL) != (ssize_t)qlen) {
        close(c);
        return NULL;
    }
    if (atomic_exchange(&g_tcp_bad_len_once, 0)) {
        uint8_t bad[2] = {0, 0};
        send(c, bad, sizeof(bad), 0);
        close(c);
        return NULL;
    }
    size_t alen = build_answer(q, qlen, a);
    if (atomic_exchange(&g_tcp_sink_a_once, 0)) {
        a[43] = 0; a[44] = 0; a[45] = 0; a[46] = 0;
    }
    if (atomic_exchange(&g_tcp_wrong_id_once, 0)) {
        a[0] ^= 0xff;
        a[1] ^= 0xff;
    }
    uint8_t framed[STUB_BUF + 2];
    framed[0] = (uint8_t)(alen >> 8);
    framed[1] = (uint8_t)alen;
    memcpy(framed + 2, a, alen);
    send(c, framed, alen + 2, 0);
    close(c);
    return NULL;
}

static void on_upstream_accept(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    (void)events;
    (void)ud;
    int c = accept(fd, NULL, NULL);
    if (c < 0) { return; }
    struct timeval tv = {.tv_sec = 3, .tv_usec = 0};
    setsockopt(c, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    pthread_t th;
    if (pthread_create(&th, NULL, serve_upstream_tcp, (void *)(intptr_t)c) != 0) {
        close(c);
        return;
    }
    pthread_detach(th);
}

static void *loop_thread(void *ud) {
    firc_loop_run((firc_loop_t *)ud);
    return NULL;
}

typedef struct {
    uint8_t a[4];
    bool saw_a, saw_aaaa;
    uint32_t aaaa_ttl;
    bool tc;
    bool has_opt;
    long waited_ms;
    bool arrived_before_release;
    bool second_replied;
    bool saw_client;
    uint8_t client[4];
    bool second_before_release;
    size_t held_at_end;
    size_t n_answers;
    size_t wire_len;
    uint64_t inflight_at_end;
    unsigned rcode;
    bool aa, ra, rd;
    bool saw_ptr;
    uint32_t ptr_ttl;
    uint8_t ptr[256];
    size_t ptr_len;
} reply_t;

typedef struct {
    bool over_tcp;
    bool rewrite;
    bool report;
    bool retime;
    bool disable_drop_aaaa;
    unsigned big_a;
    unsigned big_aaaa;
    bool padded_opt;
    uint16_t edns_size;
    bool hold;
    uint32_t hold_ms;
    uint32_t release_after_ms;
    const char *listen_addr;
    bool release_declines;
    uint32_t client_wait_ms;
    bool tcp_client_leaves_mid_hold;
    uint32_t max_concurrent;
    bool second_client;
    uint32_t max_held;
    bool tunnel;
    int tunnel_fail_connect;
    bool tunnel_bad_len;
    bool tunnel_sink_a;
    bool tunnel_wrong_id;
    uint32_t timeout_ms;
    uint32_t tcp_send_after_ms;
    uint32_t linger_ms;
    const firc_ip_t *ptr;
    bool ptr_no_rd;
    const firc_fakeip_t *pool;
    uint32_t pool_ttl;
    bool dns_route;
    bool no_fallback;
} xopts_t;

/* Waits until the proxy holds at least `n` answers, or two seconds. */
static void wait_held(firc_dnsproxy_t *proxy, size_t n) {
    for (int i = 0; i < 200 && firc_dnsproxy_held(proxy) < n; i++) {
        struct timespec ts = {.tv_sec = 0, .tv_nsec = 10 * 1000000L};
        nanosleep(&ts, NULL);
    }
}

static bool never_ready(const firc_dns_msg_t *msg, void *ud) {
    (void)msg;
    (void)ud;
    return false;
}

typedef struct {
    firc_dnsproxy_t *proxy;
    bool decline;
} release_t;

/* Posted onto the loop: the proxy's held list is loop state. */
static void release_all(firc_loop_t *loop, void *ud) {
    (void)loop;
    release_t *r = ud;
    firc_dnsproxy_release(r->proxy, r->decline ? never_ready : NULL, NULL);
}

static int run_exchange(const xopts_t *o, reply_t *out, int *hook_calls) {
    bool rewrite = o->rewrite, report = o->report, disable_drop_aaaa = o->disable_drop_aaaa,
         over_tcp = o->over_tcp;
    g_big_a = o->big_a;
    g_big_aaaa = o->big_aaaa;
    g_padded_opt = o->padded_opt;
    atomic_store(&g_tcp_bad_len_once, o->tunnel_bad_len ? 1 : 0);
    atomic_store(&g_tcp_sink_a_once, o->tunnel_sink_a ? 1 : 0);
    atomic_store(&g_tcp_wrong_id_once, o->tunnel_wrong_id ? 1 : 0);
    fixture_t fx;
    memset(&fx, 0, sizeof(fx));
    fx.rewrite = rewrite;
    fx.report = report;
    fx.hold = o->hold;
    fx.retime = o->retime;

    if (firc_loop_create(&fx.loop) != FIRC_OK) { return -2; }

    uint16_t up_port = 0, proxy_port = 0;
    if (!free_ports(&up_port, &proxy_port)) {
        firc_loop_destroy(fx.loop);
        return -2;
    }

    fx.upstream_fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (fx.upstream_fd < 0) {
        firc_loop_destroy(fx.loop);
        return -2;
    }
    struct sockaddr_in ua;
    memset(&ua, 0, sizeof(ua));
    ua.sin_family = AF_INET;
    ua.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    ua.sin_port = htons(up_port);
    if (bind(fx.upstream_fd, (struct sockaddr *)&ua, sizeof(ua)) != 0) {
        close(fx.upstream_fd);
        firc_loop_destroy(fx.loop);
        return -2;
    }
    firc_loop_add_fd(fx.loop, fx.upstream_fd, EPOLLIN, on_upstream_query, &fx);

    fx.upstream_tcp_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fx.upstream_tcp_fd < 0 || !bind_loopback(fx.upstream_tcp_fd, up_port, NULL) ||
        listen(fx.upstream_tcp_fd, 4) != 0) {
        if (fx.upstream_tcp_fd >= 0) { close(fx.upstream_tcp_fd); }
        firc_loop_del_fd(fx.loop, fx.upstream_fd);
        close(fx.upstream_fd);
        firc_loop_destroy(fx.loop);
        return -2;
    }
    firc_loop_add_fd(fx.loop, fx.upstream_tcp_fd, EPOLLIN, on_upstream_accept, &fx);

    firc_dnsproxy_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.listen_addr = o->listen_addr ? o->listen_addr : "127.0.0.1";
    cfg.listen_port = proxy_port;
    cfg.upstream_addr = "127.0.0.1";
    cfg.upstream_port = up_port;
    cfg.timeout_ms = o->timeout_ms ? o->timeout_ms : 3000;
    cfg.max_concurrent = o->max_concurrent ? o->max_concurrent : 16;
    cfg.max_idle_conns = 2;
    cfg.disable_drop_aaaa = disable_drop_aaaa;
    cfg.hold_ms = o->hold_ms;
    cfg.max_held = o->max_held;

    firc_dnsproxy_t *proxy = NULL;
    if (firc_dnsproxy_create(&cfg, fx.loop, hook, &fx, &proxy) != FIRC_OK) { return -2; }
    if (o->pool != NULL) { firc_dnsproxy_set_pool(proxy, o->pool, o->pool_ttl); }
    if (o->tunnel) {
        rec_reset();
        memset(&g_troute, 0, sizeof(g_troute));
        g_troute.take = true;
        g_troute.mark = 0x40010000u;
        g_troute.n = 1;
        g_troute.ports[0] = up_port;
        if (o->dns_route) {
            g_troute.mark = 0;
            g_troute.dns_only = true;
            g_troute.no_fallback = o->no_fallback;
        }
        firc_dnsproxy_set_sock_ops(proxy, &REC_OPS);
        firc_dnsproxy_set_router(proxy, test_route, &g_troute);
        atomic_store(&g_rec.fail_connect, o->tunnel_fail_connect);
    }
    if (firc_dnsproxy_start(proxy) != FIRC_OK) {
        firc_dnsproxy_destroy(proxy);
        firc_loop_del_fd(fx.loop, fx.upstream_fd);
        close(fx.upstream_fd);
        firc_loop_destroy(fx.loop);
        return -2;
    }

    pthread_t th;
    pthread_create(&th, NULL, loop_thread, fx.loop);

    int cfd = socket(AF_INET, over_tcp ? SOCK_STREAM : SOCK_DGRAM, 0);
    struct timeval tv = {.tv_sec = 3, .tv_usec = 0};
    setsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(cfd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
    struct sockaddr_in pa;
    memset(&pa, 0, sizeof(pa));
    pa.sin_family = AF_INET;
    pa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    pa.sin_port = htons(proxy_port);

    uint8_t q[512];
    size_t qlen = o->ptr != NULL ? build_ptr_query(q, 0x2468, o->ptr, o->edns_size != 0)
                                 : build_query(q, 0x2468, o->edns_size);
    if (o->ptr != NULL && o->ptr_no_rd) { q[2] = 0x00; }
    uint8_t r[2048];
    ssize_t rn = -1;
    release_t rel = {.proxy = proxy, .decline = o->release_declines};
    int cfd2 = -1;
    if (o->second_client) {
        cfd2 = socket(AF_INET, SOCK_DGRAM, 0);
        setsockopt(cfd2, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    }
    if (over_tcp) {
        if (connect(cfd, (struct sockaddr *)&pa, sizeof(pa)) == 0) {
            uint8_t framed[520];
            framed[0] = (uint8_t)(qlen >> 8);
            framed[1] = (uint8_t)qlen;
            memcpy(framed + 2, q, qlen);
            if (o->tcp_send_after_ms > 0) {
                struct timespec ts = {.tv_sec = o->tcp_send_after_ms / 1000,
                                      .tv_nsec = (o->tcp_send_after_ms % 1000) * 1000000L};
                nanosleep(&ts, NULL);
            }
            if (o->tcp_client_leaves_mid_hold &&
                send(cfd, framed, qlen + 2, 0) == (ssize_t)(qlen + 2)) {
                struct timespec ts = {.tv_sec = 0, .tv_nsec = 300 * 1000000L};
                wait_held(proxy, 1);
                struct linger lg = {.l_onoff = 1, .l_linger = 0};
                setsockopt(cfd, SOL_SOCKET, SO_LINGER, &lg, sizeof(lg));
                close(cfd);
                cfd = socket(AF_INET, SOCK_STREAM, 0);
                nanosleep(&ts, NULL);
                firc_loop_post(fx.loop, release_all, &rel);
                nanosleep(&ts, NULL);
            } else if (send(cfd, framed, qlen + 2, 0) == (ssize_t)(qlen + 2)) {
                uint8_t hdr[2];
                if (recv(cfd, hdr, 2, MSG_WAITALL) == 2) {
                    size_t want = ((size_t)hdr[0] << 8) | hdr[1];
                    if (want <= sizeof(r) && recv(cfd, r, want, MSG_WAITALL) == (ssize_t)want) {
                        rn = (ssize_t)want;
                    }
                }
            }
        }
    } else {
        sendto(cfd, q, qlen, 0, (struct sockaddr *)&pa, sizeof(pa));
        if (cfd2 >= 0) {
            wait_held(proxy, 1);
            uint8_t q2[512];
            size_t q2len = build_query(q2, 0x2469, 0);
            sendto(cfd2, q2, q2len, 0, (struct sockaddr *)&pa, sizeof(pa));
        }
        if (o->release_after_ms > 0) {
            struct timespec ts = {.tv_sec = o->release_after_ms / 1000,
                                  .tv_nsec = (o->release_after_ms % 1000) * 1000000L};
            nanosleep(&ts, NULL);
            uint8_t probe[16];
            out->arrived_before_release = recv(cfd, probe, sizeof(probe), MSG_DONTWAIT | MSG_PEEK) > 0;
            if (cfd2 >= 0) {
                out->second_before_release = recv(cfd2, probe, sizeof(probe), MSG_DONTWAIT | MSG_PEEK) > 0;
            }
            firc_loop_post(fx.loop, release_all, &rel);
        }
        if (o->client_wait_ms > 0) {
            struct timeval w = {.tv_sec = o->client_wait_ms / 1000,
                                .tv_usec = (o->client_wait_ms % 1000) * 1000};
            setsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &w, sizeof(w));
        }
        struct timespec t0, t1;
        clock_gettime(CLOCK_MONOTONIC, &t0);
        rn = recv(cfd, r, sizeof(r), 0);
        clock_gettime(CLOCK_MONOTONIC, &t1);
        out->waited_ms = (long)((t1.tv_sec - t0.tv_sec) * 1000 + (t1.tv_nsec - t0.tv_nsec) / 1000000);
    }
    if (cfd2 >= 0) {
        uint8_t r2[512];
        out->second_replied = recv(cfd2, r2, sizeof(r2), 0) > 0;
        close(cfd2);
    }
    out->held_at_end = firc_dnsproxy_held(proxy);
    if (o->linger_ms > 0) {
        struct timespec ts = {.tv_sec = o->linger_ms / 1000, .tv_nsec = (o->linger_ms % 1000) * 1000000L};
        nanosleep(&ts, NULL);
        out->inflight_at_end = firc_dnsproxy_inflight(proxy);
    }
    int rc = -1;
    if (rn > 0) {
        firc_dns_msg_t *msg = NULL;
        if (firc_dns_msg_parse(r, (size_t)rn, &msg) == FIRC_OK) {
            rc = 0;
            out->wire_len = (size_t)rn;
            out->tc = (msg->flags & FIRC_DNS_FLAG_TC) != 0;
            out->rcode = msg->flags & 0x0fu;
            out->aa = (msg->flags & 0x0400u) != 0;
            out->ra = (msg->flags & FIRC_DNS_FLAG_RA) != 0;
            out->rd = (msg->flags & FIRC_DNS_FLAG_RD) != 0;
            out->n_answers = msg->n_answers;
            for (size_t i = 0; i < msg->n_additional; i++) {
                if (msg->additional[i].rtype == FIRC_DNS_TYPE_OPT) { out->has_opt = true; }
            }
            for (size_t i = 0; i < msg->n_answers; i++) {
                if (msg->answers[i].rtype == FIRC_DNS_TYPE_A && msg->answers[i].rdata_len == 4) {
                    memcpy(out->a, msg->answers[i].rdata, 4);
                    out->saw_a = true;
                }
                if (msg->answers[i].rtype == FIRC_DNS_TYPE_AAAA) {
                    out->saw_aaaa = true;
                    out->aaaa_ttl = msg->answers[i].ttl;
                }
                if (msg->answers[i].rtype == FIRC_DNS_TYPE_PTR && msg->answers[i].rdata_len <= sizeof(out->ptr)) {
                    out->saw_ptr = true;
                    out->ptr_ttl = msg->answers[i].ttl;
                    out->ptr_len = msg->answers[i].rdata_len;
                    memcpy(out->ptr, msg->answers[i].rdata, out->ptr_len);
                }
            }
            firc_dns_msg_free(msg);
        }
    }
    *hook_calls = fx.hook_calls;
    out->saw_client = fx.saw_client;
    memcpy(out->client, fx.last_client, 4);

    close(cfd);
    firc_loop_stop(fx.loop);
    pthread_join(th, NULL);
    firc_dnsproxy_destroy(proxy);
    firc_loop_del_fd(fx.loop, fx.upstream_tcp_fd);
    close(fx.upstream_tcp_fd);
    firc_loop_del_fd(fx.loop, fx.upstream_fd);
    close(fx.upstream_fd);
    firc_loop_destroy(fx.loop);
    return rc;
}

typedef struct {
    int fd;
    uint16_t port;
    _Atomic uint8_t mark;
    _Atomic int received;
    _Atomic int hold;
    _Atomic int go;
    _Atomic int stop;
    pthread_t th;
    _Atomic int rcode;
    _Atomic int a_set;
    _Atomic uint32_t a_ip;
    _Atomic int aaaa_loopback;
    _Atomic int garbage;
    _Atomic int aaaa_custom_set;
    _Atomic uint64_t aaaa_hi;
    _Atomic uint64_t aaaa_lo;
    _Atomic int second_a_sink;
    _Atomic int hold_ms;
    _Atomic int decoy_first;
    _Atomic int drop;
    _Atomic int tc;
    _Atomic int replied;
    _Atomic int v6only;
    _Atomic int aaaa_mode;
    _Atomic int a_delay_ms;
    _Atomic int aaaa_delay_ms;
    _Atomic int seen_a;
    _Atomic int seen_aaaa;
} stub_t;

enum { V6_ANSWER = 0, V6_NODATA, V6_SERVFAIL, V6_SILENT };
#define V6_LAST 5

static void put_soa(uint8_t *buf, size_t *pp) {
    size_t p = *pp;
    buf[p++] = 0xc0; buf[p++] = 0x0c;
    buf[p++] = 0x00; buf[p++] = 6;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 60;
    buf[p++] = 0x00; buf[p++] = 22;
    buf[p++] = 0; buf[p++] = 0;
    for (int k = 0; k < 4; k++) {
        buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 1;
    }
    buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 60;
    *pp = p;
}

static int query_type(const uint8_t *q, size_t qlen, size_t *qd_end) {
    size_t e = 12;
    while (e < qlen && q[e] != 0) { e += 1 + q[e]; }
    e += 1 + 4;
    if (e > qlen) { return -1; }
    *qd_end = e;
    return (int)((unsigned)q[e - 4] << 8 | q[e - 3]);
}

static size_t build_v6only(const uint8_t *q, size_t qlen, uint8_t *buf, int mode) {
    size_t qd_end = 0;
    int qt = query_type(q, qlen, &qd_end);
    if (qt < 0 || (qt == FIRC_DNS_TYPE_AAAA && mode == V6_SILENT)) { return 0; }
    memcpy(buf, q, qd_end);
    buf[2] = 0x81;
    buf[3] = 0x80;
    memset(buf + 6, 0, 6);
    size_t p = qd_end;
    if (qt == FIRC_DNS_TYPE_AAAA && mode == V6_SERVFAIL) {
        buf[3] = 0x82;
        return p;
    }
    if (qt == FIRC_DNS_TYPE_AAAA && mode == V6_ANSWER) {
        buf[7] = 1;
        buf[p++] = 0xc0; buf[p++] = 0x0c;
        buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_AAAA;
        buf[p++] = 0x00; buf[p++] = 0x01;
        buf[p++] = 0x00; buf[p++] = 0x00; buf[p++] = 0x01; buf[p++] = 0x2c;
        buf[p++] = 0x00; buf[p++] = 0x10;
        buf[p++] = 0x20; buf[p++] = 0x01; buf[p++] = 0x0d; buf[p++] = 0xb8;
        for (int k = 0; k < 11; k++) { buf[p++] = 0; }
        buf[p++] = V6_LAST;
        return p;
    }
    buf[9] = 1;
    put_soa(buf, &p);
    return p;
}

static void v6only_reply(stub_t *s, const uint8_t *q, size_t n, const struct sockaddr_storage *from, socklen_t fl) {
    size_t qd_end = 0;
    int qt = query_type(q, n, &qd_end);
    int delay = 0;
    int drops = atomic_load(&s->drop);
    if (drops > 0) {
        atomic_store(&s->drop, drops - 1);
        return;
    }
    if (qt == FIRC_DNS_TYPE_A) {
        delay = atomic_load(&s->a_delay_ms);
    } else if (qt == FIRC_DNS_TYPE_AAAA) {
        delay = atomic_load(&s->aaaa_delay_ms);
    }
    if (delay > 0) {
        struct timespec ts = {.tv_sec = delay / 1000, .tv_nsec = (delay % 1000) * 1000000L};
        nanosleep(&ts, NULL);
    }
    uint8_t a[STUB_BUF];
    size_t alen = build_v6only(q, n, a, atomic_load(&s->aaaa_mode));
    if (alen == 0) { return; }
    sendto(s->fd, a, alen, 0, (const struct sockaddr *)from, fl);
    atomic_fetch_add(&s->replied, 1);
}

#define STUB_A_LAST_OCTET 46

static void *stub_serve(void *arg) {
    stub_t *s = arg;
    uint8_t q[512], a[STUB_BUF];
    while (!atomic_load(&s->stop)) {
        struct sockaddr_storage from;
        socklen_t fl = sizeof(from);
        ssize_t n = recvfrom(s->fd, q, sizeof(q), 0, (struct sockaddr *)&from, &fl);
        if (n <= 0) { continue; }
        atomic_fetch_add(&s->received, 1);
        if (atomic_load(&s->v6only)) {
            size_t qd_end = 0;
            int qt = query_type(q, (size_t)n, &qd_end);
            atomic_fetch_add(qt == FIRC_DNS_TYPE_AAAA ? &s->seen_aaaa : &s->seen_a, 1);
            v6only_reply(s, q, (size_t)n, &from, fl);
            continue;
        }
        int drops = atomic_load(&s->drop);
        if (drops > 0) {
            atomic_store(&s->drop, drops - 1);
            continue;
        }
        while (atomic_load(&s->hold) && !atomic_load(&s->go) && !atomic_load(&s->stop)) {
            struct timespec ts = {.tv_sec = 0, .tv_nsec = 5 * 1000000L};
            nanosleep(&ts, NULL);
        }
        int hm = atomic_load(&s->hold_ms);
        if (hm > 0) {
            struct timespec ts = {.tv_sec = hm / 1000, .tv_nsec = (hm % 1000) * 1000000L};
            nanosleep(&ts, NULL);
        }
        size_t alen = build_answer(q, (size_t)n, a);
        a[STUB_A_LAST_OCTET] = atomic_load(&s->mark);
        if (atomic_load(&s->tc)) { a[2] |= 0x02; }
        int rc = atomic_load(&s->rcode);
        if (rc != 0) { a[3] = (uint8_t)((a[3] & 0xf0) | (rc & 0x0f)); }
        if (atomic_load(&s->a_set)) {
            uint32_t ip = atomic_load(&s->a_ip);
            a[43] = (uint8_t)(ip >> 24);
            a[44] = (uint8_t)(ip >> 16);
            a[45] = (uint8_t)(ip >> 8);
            a[46] = (uint8_t)ip;
        }
        if (atomic_load(&s->aaaa_loopback)) {
            memset(a + 72, 0, 15);
            a[87] = 1;
        }
        if (atomic_load(&s->aaaa_custom_set)) {
            uint64_t hi = atomic_load(&s->aaaa_hi);
            uint64_t lo = atomic_load(&s->aaaa_lo);
            for (int k = 0; k < 8; k++) { a[72 + k] = (uint8_t)(hi >> (56 - 8 * k)); }
            for (int k = 0; k < 8; k++) { a[80 + k] = (uint8_t)(lo >> (56 - 8 * k)); }
        }
        if (atomic_load(&s->second_a_sink)) {
            a[7] = (uint8_t)(a[7] + 1);
            size_t p = alen;
            a[p++] = 0xc0; a[p++] = 0x0c;
            a[p++] = 0x00; a[p++] = FIRC_DNS_TYPE_A;
            a[p++] = 0x00; a[p++] = 0x01;
            a[p++] = 0x00; a[p++] = 0x00; a[p++] = 0x00; a[p++] = 0x00;
            a[p++] = 0x00; a[p++] = 0x04;
            a[p++] = 0; a[p++] = 0; a[p++] = 0; a[p++] = 0;
            alen = p;
        }
        if (atomic_load(&s->garbage)) { alen = 5; }
        int decoy = atomic_load(&s->decoy_first);
        if (decoy != 0 && alen > STUB_A_LAST_OCTET) {
            uint8_t d[STUB_BUF];
            memcpy(d, a, alen);
            d[STUB_A_LAST_OCTET] = 99;
            if (decoy == 1) {
                d[0] ^= 0xff;
                d[1] ^= 0xff;
            } else {
                d[13] = 'b';
            }
            sendto(s->fd, d, alen, 0, (struct sockaddr *)&from, fl);
        }
        sendto(s->fd, a, alen, 0, (struct sockaddr *)&from, fl);
        atomic_fetch_add(&s->replied, 1);
    }
    return NULL;
}

static bool stub_start(stub_t *s, uint8_t mark) {
    memset(s, 0, sizeof(*s));
    atomic_store(&s->mark, mark);
    s->fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (s->fd < 0) { return false; }
    struct timeval tv = {.tv_sec = 0, .tv_usec = 100000};
    setsockopt(s->fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    if (!bind_loopback(s->fd, 0, &s->port) || pthread_create(&s->th, NULL, stub_serve, s) != 0) {
        close(s->fd);
        return false;
    }
    return true;
}

static void stub_stop(stub_t *s) {
    atomic_store(&s->stop, 1);
    pthread_join(s->th, NULL);
    close(s->fd);
}

typedef struct {
    firc_loop_t *loop;
    firc_dnsproxy_t *proxy;
    uint16_t proxy_port;
    stub_t a, b, c;
    pthread_t th;
} live_t;

enum { DOH_GOOD = 0, DOH_SERVFAIL, DOH_SINK, DOH_FAIL, DOH_SILENT, DOH_BIG, DOH_REFUSE, DOH_STRAY_ID, DOH_STRAY_NAME };

#define DOH_ANSWER_OCTET 9
#define DOH_CALLS 32

struct fdoh;

typedef struct {
    struct fdoh *d;
    uint64_t ticket;
    bool live;
    firc_doh_done_fn fn;
    void *ud;
    uint8_t q[512];
    size_t qlen;
    int mode;
} fdoh_call_t;

typedef struct fdoh {
    firc_loop_t *loop;
    _Atomic int mode;
    _Atomic int queries, cancels, delivered;
    _Atomic int last_url, last_id, last_len;
    _Atomic uint32_t last_mark, last_timeout;
    _Atomic uint64_t last_ticket, last_cancelled;
    uint64_t next;
    fdoh_call_t calls[DOH_CALLS];
} fdoh_t;

static fdoh_t g_fdoh;
static bool g_live_doh;

static void fdoh_fire(fdoh_call_t *c) {
    if (!c->live) { return; }
    c->live = false;
    atomic_fetch_add(&c->d->delivered, 1);
    if (c->mode == DOH_FAIL) {
        c->fn(c->ud, NULL, 0, false);
        return;
    }
    uint8_t a[STUB_BUF];
    unsigned big = g_big_a;
    if (c->mode == DOH_BIG) { g_big_a = BIG_A_OVER_512; }
    size_t n = build_answer(c->q, c->qlen, a);
    g_big_a = big;
    if (c->mode != DOH_BIG) { a[STUB_A_LAST_OCTET] = DOH_ANSWER_OCTET; }
    if (c->mode == DOH_SERVFAIL) { a[3] = (uint8_t)((a[3] & 0xf0) | 2); }
    if (c->mode == DOH_SINK) { memset(a + STUB_A_LAST_OCTET - 3, 0, 4); }
    if (c->mode == DOH_STRAY_ID) { a[0] ^= 0xff; }
    if (c->mode == DOH_STRAY_NAME) { a[13] = a[13] == 'z' ? 'y' : 'z'; }
    c->fn(c->ud, a, n, true);
}

static void fdoh_on_timer(firc_loop_t *loop, void *ud) {
    (void)loop;
    fdoh_fire(ud);
}

static void fdoh_flush(firc_loop_t *loop, void *ud) {
    (void)loop;
    fdoh_t *d = ud;
    for (size_t i = 0; i < DOH_CALLS; i++) {
        if (d->calls[i].live) {
            d->calls[i].mode = DOH_GOOD;
            fdoh_fire(&d->calls[i]);
        }
    }
}

static firc_err_t fdoh_query(void *ud, uint16_t url_id, uint32_t mark, const uint8_t *q, size_t len,
                             uint32_t timeout_ms, firc_doh_done_fn fn, void *fn_ud, uint64_t *ticket) {
    fdoh_t *d = ud;
    atomic_fetch_add(&d->queries, 1);
    atomic_store(&d->last_url, url_id);
    atomic_store(&d->last_mark, mark);
    atomic_store(&d->last_timeout, timeout_ms);
    atomic_store(&d->last_len, (int)len);
    atomic_store(&d->last_id, len >= 2 ? (int)((unsigned)q[0] << 8 | q[1]) : -1);
    int mode = atomic_load(&d->mode);
    if (mode == DOH_REFUSE || len > sizeof(d->calls[0].q)) { return FIRC_ERR_NOMEM; }
    fdoh_call_t *c = &d->calls[d->next % DOH_CALLS];
    d->next++;
    c->d = d;
    c->ticket = d->next;
    c->live = true;
    c->fn = fn;
    c->ud = fn_ud;
    memcpy(c->q, q, len);
    c->qlen = len;
    c->mode = mode;
    *ticket = c->ticket;
    atomic_store(&d->last_ticket, c->ticket);
    int tid = 0;
    if (mode != DOH_SILENT) { (void)firc_loop_add_timer(d->loop, 0, 0, fdoh_on_timer, c, &tid); }
    return FIRC_OK;
}

static void fdoh_cancel(void *ud, uint64_t ticket) {
    fdoh_t *d = ud;
    atomic_fetch_add(&d->cancels, 1);
    atomic_store(&d->last_cancelled, ticket);
    for (size_t i = 0; i < DOH_CALLS; i++) {
        if (d->calls[i].live && d->calls[i].ticket == ticket) { d->calls[i].live = false; }
    }
}

static void fdoh_setup(int mode) {
    memset(&g_fdoh, 0, sizeof(g_fdoh));
    atomic_store(&g_fdoh.mode, mode);
    g_live_doh = true;
}

static bool g_live_hold;
static bool g_live_cache;
static _Atomic uint64_t g_cache_skew_ms;
static _Atomic uint64_t g_health_ms;
static bool g_live_health_clock;

static uint64_t health_clock(void) { return atomic_load(&g_health_ms); }
static _Atomic int g_live_hook_calls;

static uint64_t skewed_ms(void) { return 1000000u + atomic_load(&g_cache_skew_ms); }

static _Atomic int g_live_resolver = -1;

static bool g_live_cross;
static _Atomic int g_cross_calls;
static _Atomic int g_cross_null;
static _Atomic int g_cross_resolver = -1;
static _Atomic int g_cross_rcode = -1;

static bool has_rr(const firc_dns_msg_t *msg, uint16_t type) {
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == type) { return true; }
    }
    return false;
}

static firc_dns_verdict_t live_hook(firc_dns_msg_t *msg, const firc_ip_t *client,
                                    const char *network, firc_dns_resolver_t resolver, void *ud) {
    (void)client;
    (void)network;
    (void)ud;
    atomic_fetch_add(&g_live_hook_calls, 1);
    atomic_store(&g_live_resolver, (int)resolver);
    if (g_live_cross && msg->n_questions == 1 && msg->questions[0].qtype == FIRC_DNS_TYPE_A &&
        !has_rr(msg, FIRC_DNS_TYPE_A)) {
        return FIRC_DNS_ASK_AAAA;
    }
    return g_live_hold ? FIRC_DNS_HOLD : FIRC_DNS_PASS;
}

static firc_dns_verdict_t live_cross(firc_dns_msg_t *a_msg, const firc_dns_msg_t *aaaa_msg, const firc_ip_t *client,
                                     firc_dns_resolver_t resolver, void *ud) {
    (void)client;
    (void)ud;
    atomic_fetch_add(&g_cross_calls, 1);
    atomic_store(&g_cross_resolver, (int)resolver);
    if (aaaa_msg == NULL) {
        atomic_fetch_add(&g_cross_null, 1);
        return FIRC_DNS_PASS;
    }
    atomic_store(&g_cross_rcode, (int)(aaaa_msg->flags & 0x0fu));
    if ((aaaa_msg->flags & 0x0fu) != 0) { return FIRC_DNS_PASS; }
    for (size_t i = 0; i < aaaa_msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &aaaa_msg->answers[i];
        if (rr->rtype == FIRC_DNS_TYPE_AAAA && rr->rdata_len == 16) {
            firc_ip_t fake4 = {.b = {198, 18, 0, (uint8_t)(rr->rdata[15] + 100)}, .len = 4};
            return firc_dns_msg_collapse(a_msg, &fake4, 1, NULL, 0, 60) == FIRC_OK ? FIRC_DNS_REWRITTEN : FIRC_DNS_DROP;
        }
    }
    return FIRC_DNS_PASS;
}

/* False when the harness could not be set up. */
static bool live_start(live_t *lv) {
    memset(lv, 0, sizeof(*lv));
    g_big_a = 0;
    g_big_aaaa = 0;
    g_padded_opt = false;
    uint16_t unused = 0;
    if (!free_ports(&lv->proxy_port, &unused)) { return false; }
    if (firc_loop_create(&lv->loop) != FIRC_OK) { return false; }
    if (!stub_start(&lv->a, 1)) {
        firc_loop_destroy(lv->loop);
        return false;
    }
    if (!stub_start(&lv->b, 2)) {
        stub_stop(&lv->a);
        firc_loop_destroy(lv->loop);
        return false;
    }
    if (!stub_start(&lv->c, 3)) {
        stub_stop(&lv->b);
        stub_stop(&lv->a);
        firc_loop_destroy(lv->loop);
        return false;
    }
    firc_dnsproxy_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.listen_addr = "127.0.0.1";
    cfg.listen_port = lv->proxy_port;
    cfg.upstream_addr = "127.0.0.1";
    cfg.upstream_port = g_live_upstream_port ? g_live_upstream_port : lv->a.port;
    cfg.timeout_ms = g_live_timeout_ms ? g_live_timeout_ms : 3000;
    cfg.max_concurrent = g_live_max_concurrent ? g_live_max_concurrent : 16;
    cfg.max_idle_conns = 2;
    cfg.hold_ms = 200;
    if (firc_dnsproxy_create(&cfg, lv->loop, live_hook, NULL, &lv->proxy) != FIRC_OK) {
        stub_stop(&lv->c);
        stub_stop(&lv->b);
        stub_stop(&lv->a);
        firc_loop_destroy(lv->loop);
        return false;
    }
    if (g_live_ops != NULL) { firc_dnsproxy_set_sock_ops(lv->proxy, g_live_ops); }
    if (g_live_cache) {
        firc_dnsproxy_set_cache_clock_for_test(lv->proxy, skewed_ms);
    } else {
        firc_dnsproxy_set_cache(lv->proxy, 0, 0);
    }
    atomic_store(&g_live_hook_calls, 0);
    atomic_store(&g_cross_calls, 0);
    atomic_store(&g_cross_null, 0);
    atomic_store(&g_cross_resolver, -1);
    atomic_store(&g_cross_rcode, -1);
    if (g_live_cross) { firc_dnsproxy_set_cross(lv->proxy, live_cross, NULL); }
    if (g_live_route != NULL) { firc_dnsproxy_set_router(lv->proxy, test_route, g_live_route); }
    if (g_live_router != NULL) { firc_dnsproxy_set_router(lv->proxy, firc_resolve_router_decide, g_live_router); }
    if (g_live_route_fn != NULL) { firc_dnsproxy_set_router(lv->proxy, g_live_route_fn, NULL); }
    if (g_live_clamp != 0 || g_live_pool != NULL) {
        firc_dnsproxy_set_pool(lv->proxy, g_live_pool, g_live_clamp != 0 ? g_live_clamp : 300);
    }
    atomic_store(&g_blocked_calls, 0);
    atomic_store(&g_blocked_answer, -1);
    atomic_store(&g_blocked_client, -1);
    firc_dnsproxy_set_blocked(lv->proxy, live_blocked, NULL);
    atomic_store(&g_tally_n, 0);
    firc_dnsproxy_set_tally(lv->proxy, live_tally, NULL);
    firc_ip_t v4 = {.b = {198, 18, 0, 0}, .len = 4};
    firc_ip_t v6 = {.b = {0xfd, 0x00, 0x12, 0x34}, .len = 16};
    firc_dnsproxy_set_pool_prefixes(lv->proxy, &v4, 15, &v6, 48);
    if (g_live_rest_ms != 0) { firc_dnsproxy_set_health(lv->proxy, 0, g_live_rest_ms); }
    if (g_live_doh) {
        g_fdoh.loop = lv->loop;
        firc_dnsproxy_set_doh(lv->proxy, (firc_doh_ops_t){.query = fdoh_query, .cancel = fdoh_cancel, .ud = &g_fdoh});
    }
    if (g_live_health_clock) { firc_dnsproxy_set_health_clock_for_test(lv->proxy, health_clock); }
    if (firc_dnsproxy_start(lv->proxy) != FIRC_OK) {
        firc_dnsproxy_destroy(lv->proxy);
        stub_stop(&lv->c);
        stub_stop(&lv->b);
        stub_stop(&lv->a);
        firc_loop_destroy(lv->loop);
        return false;
    }
    pthread_create(&lv->th, NULL, loop_thread, lv->loop);
    return true;
}

static void live_stop(live_t *lv) {
    firc_loop_stop(lv->loop);
    pthread_join(lv->th, NULL);
    firc_dnsproxy_destroy(lv->proxy);
    stub_stop(&lv->c);
    stub_stop(&lv->b);
    stub_stop(&lv->a);
    firc_loop_destroy(lv->loop);
    g_live_ops = NULL;
    g_live_route = NULL;
    g_live_router = NULL;
    g_live_clamp = 0;
    g_live_pool = NULL;
    g_live_route_fn = NULL;
    g_live_timeout_ms = 0;
    g_live_upstream_port = 0;
    g_live_max_concurrent = 0;
    g_live_rest_ms = 0;
    g_live_cache = false;
    g_live_health_clock = false;
    g_live_cross = false;
    g_live_doh = false;
    atomic_store(&g_cache_skew_ms, 0);
}

typedef struct {
    firc_dnsproxy_t *proxy;
    const char *addr;
    uint16_t port;
    firc_err_t err;
    _Atomic int done;
} set_t;

static int ask_send_for(uint16_t port, uint16_t id, uint16_t edns, char first) {
    int c = socket(AF_INET, SOCK_DGRAM, 0);
    if (c < 0) { return -1; }
    struct timeval tv = {.tv_sec = 3, .tv_usec = 0};
    setsockopt(c, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    struct sockaddr_in pa;
    memset(&pa, 0, sizeof(pa));
    pa.sin_family = AF_INET;
    pa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    pa.sin_port = htons(port);
    uint8_t q[512];
    size_t qlen = build_query_for(q, id, edns, first);
    if (sendto(c, q, qlen, 0, (struct sockaddr *)&pa, sizeof(pa)) != (ssize_t)qlen) {
        close(c);
        return -1;
    }
    return c;
}

static int ask_send(uint16_t port, uint16_t id) {
    return ask_send_for(port, id, 0, 'a');
}

/* The last octet of the A record in the answer on `c`, or -1. Closes `c`. */
static int ask_read(int c) {
    uint8_t r[2048];
    ssize_t n = recv(c, r, sizeof(r), 0);
    close(c);
    if (n <= 0) { return -1; }
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(r, (size_t)n, &msg) != FIRC_OK) { return -1; }
    int mark = -1;
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == FIRC_DNS_TYPE_A && msg->answers[i].rdata_len == 4) {
            mark = msg->answers[i].rdata[3];
        }
    }
    firc_dns_msg_free(msg);
    return mark;
}

static int ask(uint16_t port, uint16_t id) {
    int c = ask_send(port, id);
    return c < 0 ? -1 : ask_read(c);
}

/* A TCP connection to the proxy with three-second send and receive timeouts, or -1. */
static int tcp_open(uint16_t port) {
    int c = socket(AF_INET, SOCK_STREAM, 0);
    if (c < 0) { return -1; }
    struct timeval tv = {.tv_sec = 3, .tv_usec = 0};
    setsockopt(c, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(c, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
    struct sockaddr_in pa;
    memset(&pa, 0, sizeof(pa));
    pa.sin_family = AF_INET;
    pa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    pa.sin_port = htons(port);
    if (connect(c, (struct sockaddr *)&pa, sizeof(pa)) != 0) {
        close(c);
        return -1;
    }
    return c;
}

/* One length-prefixed query on a new TCP connection; the answer's last A octet, or -1. */
static int ask_tcp(uint16_t port, uint16_t id) {
    int c = tcp_open(port);
    if (c < 0) { return -1; }
    uint8_t q[512];
    size_t qlen = build_query(q, id, 0);
    uint8_t framed[514];
    framed[0] = (uint8_t)(qlen >> 8);
    framed[1] = (uint8_t)qlen;
    memcpy(framed + 2, q, qlen);
    if (send(c, framed, qlen + 2, MSG_NOSIGNAL) != (ssize_t)(qlen + 2)) {
        close(c);
        return -1;
    }
    uint8_t hdr[2];
    if (recv(c, hdr, 2, MSG_WAITALL) != 2) {
        close(c);
        return -1;
    }
    size_t want = ((size_t)hdr[0] << 8) | hdr[1];
    uint8_t r[2048];
    ssize_t got = (want <= sizeof(r)) ? recv(c, r, want, MSG_WAITALL) : -1;
    close(c);
    if (got != (ssize_t)want) { return -1; }
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(r, want, &msg) != FIRC_OK) { return -1; }
    int last = -1;
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == FIRC_DNS_TYPE_A && msg->answers[i].rdata_len == 4) {
            last = msg->answers[i].rdata[3];
        }
    }
    firc_dns_msg_free(msg);
    return last;
}


static void tunnel_setup(uint16_t port) {
    rec_reset();
    memset(&g_troute, 0, sizeof(g_troute));
    g_troute.take = true;
    g_troute.mark = 0x40010000u;
    g_troute.n = 1;
    g_troute.ports[0] = port;
    g_live_ops = &REC_OPS;
    g_live_route = &g_troute;
}

static uint64_t live_fallbacks(live_t *lv);
static bool live_health(live_t *lv, uint64_t gen, firc_dnsproxy_health_t *out);

typedef struct {
    firc_config_t cfg;
    firc_dns_pipeline_t *pl;
    firc_resolve_router_t *router;
} blockfx_t;

static _Atomic uint64_t g_rep_ms = 1000;
static uint64_t rep_clock(void) { return atomic_load(&g_rep_ms); }

static int journal_at(firc_log_level_t level, const char *needle) {
    firc_event_t ev[256];
    uint64_t next = 0, dropped = 0;
    size_t n = firc_event_read(0, ev, 256, &next, &dropped);
    int c = 0;
    for (size_t i = 0; i < n; i++) {
        if (ev[i].kind == FIRC_EVENT_LOG && ev[i].u.log.level == level && strstr(ev[i].u.log.text, needle) != NULL) {
            c++;
        }
    }
    return c;
}

typedef struct {
    int fd;
    _Atomic int stop;
    pthread_t th;
} tcp_up_t;

#define BIG_TXT_RECORDS 230
typedef struct {
    int lfd;
    uint16_t port;
    pthread_t th;
} big_stub_t;

typedef struct {
    firc_dnsproxy_t *proxy;
    firc_id_t id;
    uint64_t gen;
    firc_dnsproxy_health_t out;
    _Atomic int done;
} fb_t;

static void health_on_loop(firc_loop_t *loop, void *ud) {
    (void)loop;
    fb_t *f = ud;
    firc_dnsproxy_group_health(f->proxy, f->id, f->gen, &f->out);
    atomic_store(&f->done, 1);
}

/* The proxy's counters are loop state, so they are read on the loop. */
static bool live_health(live_t *lv, uint64_t gen, firc_dnsproxy_health_t *out) {
    fb_t f = {.proxy = lv->proxy, .gen = gen};
    f.id.b[3] = 7;
    if (firc_loop_post(lv->loop, health_on_loop, &f) != FIRC_OK) { return false; }
    for (int i = 0; i < 200 && !atomic_load(&f.done); i++) {
        struct timespec ts = {.tv_sec = 0, .tv_nsec = 10 * 1000000L};
        nanosleep(&ts, NULL);
    }
    if (!atomic_load(&f.done)) { return false; }
    *out = f.out;
    return true;
}

static uint64_t live_fallbacks(live_t *lv) {
    firc_dnsproxy_health_t h;
    return live_health(lv, g_troute.gen != 0 ? g_troute.gen : 1, &h) ? h.fallbacks : UINT64_MAX;
}

static int g_log_pipe[2] = {-1, -1};

/* Sends everything logged at INFO and above to a pipe. */
static void log_capture_start(void) {
    if (pipe(g_log_pipe) != 0) { return; }
    fcntl(g_log_pipe[0], F_SETFL, O_NONBLOCK);
    firc_log_set_output_floor(FIRC_LOG_INFO);
    firc_log_set_fd(g_log_pipe[1]);
}

static int occurrences(const char *hay, const char *needle) {
    int count = 0;
    for (const char *at = hay; (at = strstr(at, needle)) != NULL; at += strlen(needle)) { count++; }
    return count;
}

/* Stops the capture, restores the output floor, and counts two phrases in what was logged. */
static void log_capture_counts(const char *a, int *na, const char *b, int *nb) {
    firc_log_set_fd(STDOUT_FILENO);
    firc_log_set_output_floor(FIRC_LOG_DEBUG);
    close(g_log_pipe[1]);
    static char buf[65536];
    ssize_t n = read(g_log_pipe[0], buf, sizeof(buf) - 1);
    close(g_log_pipe[0]);
    buf[n > 0 ? n : 0] = '\0';
    *na = occurrences(buf, a);
    *nb = occurrences(buf, b);
}

typedef struct {
    _Atomic int received;
    _Atomic int go;
} tcp_probe_t;

typedef struct {
    int lfd;
    _Atomic int done;
} tcp_probe_listen_t;

typedef struct {
    firc_dnsproxy_t *proxy;
    uint8_t group;
    bool forget;
    _Atomic int done;
} close_t;

static void pause_ms(long ms) {
    struct timespec ts = {.tv_sec = ms / 1000, .tv_nsec = (ms % 1000) * 1000000L};
    nanosleep(&ts, NULL);
}


typedef struct {
    firc_dnsproxy_t *proxy;
    _Atomic size_t out;
    _Atomic int done;
} pfq_t;

typedef struct {
    uint16_t port;
    uint16_t id;
    int got;
} tcp_ask_t;

GREATEST_MAIN_DEFS();

typedef struct {
    uint16_t id;
    uint16_t qtype;
    int rcode;
    int n_answers;
    int n_authority;
    int a_octets[4];
} xreply_t;

typedef struct {
    int fd;
    uint16_t port;
    _Atomic int stop;
    _Atomic int seen_a;
    _Atomic int seen_aaaa;
    pthread_t th;
} v6tcp_t;

typedef struct {
    int rcode;
    int last;
    bool tc;
} got_t;

static bool got_parse(const uint8_t *r, size_t n, got_t *g) {
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(r, n, &msg) != FIRC_OK) { return false; }
    g->rcode = (int)(msg->flags & 0x0fu);
    g->tc = (msg->flags & FIRC_DNS_FLAG_TC) != 0;
    g->last = -1;
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == FIRC_DNS_TYPE_A && msg->answers[i].rdata_len == 4) {
            g->last = msg->answers[i].rdata[3];
        }
    }
    firc_dns_msg_free(msg);
    return true;
}

static bool ask_got(uint16_t port, uint16_t id, got_t *g) {
    int c = ask_send(port, id);
    if (c < 0) { return false; }
    uint8_t r[4096];
    ssize_t n = recv(c, r, sizeof(r), 0);
    close(c);
    return n > 0 && got_parse(r, (size_t)n, g);
}

static bool ask_tcp_got(uint16_t port, uint16_t id, got_t *g) {
    int c = tcp_open(port);
    if (c < 0) { return false; }
    uint8_t q[512];
    size_t qlen = build_query(q, id, 0);
    uint8_t framed[514];
    framed[0] = (uint8_t)(qlen >> 8);
    framed[1] = (uint8_t)qlen;
    memcpy(framed + 2, q, qlen);
    uint8_t hdr[2];
    if (send(c, framed, qlen + 2, MSG_NOSIGNAL) != (ssize_t)(qlen + 2) || recv(c, hdr, 2, MSG_WAITALL) != 2) {
        close(c);
        return false;
    }
    size_t want = ((size_t)hdr[0] << 8) | hdr[1];
    uint8_t r[4096];
    ssize_t n = want <= sizeof(r) ? recv(c, r, want, MSG_WAITALL) : -1;
    close(c);
    return n == (ssize_t)want && got_parse(r, want, g);
}

static void doh_setup(int mode, uint32_t mark, bool dns_only, bool no_fallback) {
    tunnel_setup(0);
    fdoh_setup(mode);
    g_troute.mark = mark;
    g_troute.dns_only = dns_only;
    g_troute.no_fallback = no_fallback;
}

#define DOH_URL 5
#define DOH_MARK 0x40010000u

TEST a_doh_server_answers_as_a_leg(void) {
    live_t lv;
    doh_setup(DOH_GOOD, DOH_MARK, false, false);
    g_troute.url_ids[0] = DOH_URL;
    ASSERT(live_start(&lv));
    ASSERT_EQ(DOH_ANSWER_OCTET, ask(lv.proxy_port, 0xd101));
    ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
    ASSERT_EQ(DOH_URL, atomic_load(&g_fdoh.last_url));
    ASSERT_EQ(DOH_MARK, atomic_load(&g_fdoh.last_mark));
    ASSERT_EQ(0xd101, atomic_load(&g_fdoh.last_id));
    ASSERT(atomic_load(&g_fdoh.last_timeout) > 0);
    ASSERT(atomic_load(&g_fdoh.last_timeout) <= FIRC_DNSPROXY_TUNNEL_TIMEOUT_MS);
    ASSERT_EQ(FIRC_DNS_RESOLVER_GROUP, atomic_load(&g_live_resolver));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    ASSERT_EQ(0, atomic_load(&g_rec.opens));
    ASSERT_EQ(0u, live_fallbacks(&lv));
    live_stop(&lv);
    PASS();
}

TEST a_doh_leg_losing_the_race_is_cancelled(void) {
    live_t lv;
    doh_setup(DOH_SILENT, DOH_MARK, false, false);
    g_troute.n = 2;
    g_troute.url_ids[1] = DOH_URL;
    ASSERT(live_start(&lv));
    g_troute.ports[0] = lv.b.port;
    ASSERT_EQ(2, ask(lv.proxy_port, 0xd201));
    ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
    ASSERT_EQ(1, atomic_load(&g_fdoh.cancels));
    ASSERT_EQ(atomic_load(&g_fdoh.last_ticket), atomic_load(&g_fdoh.last_cancelled));
    ASSERT_EQ(FIRC_OK, firc_loop_post(lv.loop, fdoh_flush, &g_fdoh));
    pause_ms(50);
    ASSERT_EQ(0, atomic_load(&g_fdoh.delivered));
    live_stop(&lv);
    PASS();
}

/* Catches: a DoH answer to another question treated as a stray datagram, waiting for one that never comes. */
TEST a_stray_doh_answer_fails_its_leg_at_once(void) {
    int modes[2] = {DOH_STRAY_ID, DOH_STRAY_NAME};
    for (int m = 0; m < 2; m++) {
        live_t lv;
        doh_setup(modes[m], DOH_MARK, false, false);
        g_troute.url_ids[0] = DOH_URL;
        ASSERT(live_start(&lv));
        long t0 = ms_now();
        ASSERT_EQ(1, ask(lv.proxy_port, 0xd251));
        ASSERT_LT(ms_now() - t0, 500);
        ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
        ASSERT_EQ(FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL, atomic_load(&g_live_resolver));
        live_stop(&lv);
    }
    PASS();
}

TEST a_dns_route_sets_no_mark(void) {
    live_t lv;
    tunnel_setup(0);
    g_troute.mark = 0;
    g_troute.dns_only = true;
    ASSERT(live_start(&lv));
    g_troute.ports[0] = lv.b.port;
    ASSERT_EQ(2, ask(lv.proxy_port, 0xd301));
    ASSERT_EQ(1, atomic_load(&g_rec.opens));
    ASSERT_EQ(0, atomic_load(&g_rec.marks));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    live_stop(&lv);
    xopts_t o;
    memset(&o, 0, sizeof(o));
    o.over_tcp = true;
    o.tunnel = true;
    o.dns_route = true;
    reply_t out;
    memset(&out, 0, sizeof(out));
    int hook_calls = 0;
    int rc = run_exchange(&o, &out, &hook_calls);
    if (rc == -2) { SKIPm("could not bind the harness's ports"); }
    ASSERT_EQ(0, rc);
    ASSERT(out.saw_a);
    ASSERT_EQ(1, atomic_load(&g_rec.stream_opens));
    ASSERT_EQ(0, atomic_load(&g_rec.marks));
    PASS();
}

TEST a_dns_routes_aaaa_is_stripped_when_drop_aaaa_is_on(void) {
    xopts_t o;
    memset(&o, 0, sizeof(o));
    o.tunnel = true;
    o.dns_route = true;
    o.disable_drop_aaaa = false;
    reply_t out;
    memset(&out, 0, sizeof(out));
    int hook_calls = 0;
    int rc = run_exchange(&o, &out, &hook_calls);
    if (rc == -2) { SKIPm("could not bind the harness's ports"); }
    ASSERT_EQ(0, rc);
    ASSERT(out.saw_a);
    ASSERT_FALSE(out.saw_aaaa);
    PASS();
}

TEST a_dns_routes_aaaa_passes_when_drop_aaaa_is_off(void) {
    xopts_t o;
    memset(&o, 0, sizeof(o));
    o.tunnel = true;
    o.dns_route = true;
    o.disable_drop_aaaa = true;
    reply_t out;
    memset(&out, 0, sizeof(out));
    int hook_calls = 0;
    int rc = run_exchange(&o, &out, &hook_calls);
    if (rc == -2) { SKIPm("could not bind the harness's ports"); }
    ASSERT_EQ(0, rc);
    ASSERT(out.saw_a);
    ASSERT(out.saw_aaaa);
    PASS();
}

TEST without_fallback_a_servfail_reaches_the_client(void) {
    for (int beside = 0; beside < 2; beside++) {
        live_t lv;
        doh_setup(DOH_SILENT, 0, true, true);
        g_troute.n = beside ? 2u : 1u;
        g_troute.url_ids[1] = DOH_URL;
        ASSERT(live_start(&lv));
        g_troute.ports[0] = lv.b.port;
        atomic_store(&lv.b.rcode, 2);
        got_t g;
        ASSERTm(beside ? "beside a silent leg" : "alone", ask_got(lv.proxy_port, 0xd401, &g));
        ASSERT_EQ(2, g.rcode);
        ASSERT_EQ(2, g.last);
        ASSERT_EQ(FIRC_DNS_RESOLVER_GROUP, atomic_load(&g_live_resolver));
        ASSERT_EQ(0, atomic_load(&lv.a.received));
        ASSERT_EQ(beside, atomic_load(&g_fdoh.cancels));
        live_stop(&lv);
    }
    PASS();
}

TEST without_fallback_a_sink_answer_reaches_the_client(void) {
    live_t lv;
    doh_setup(DOH_SINK, 0, true, true);
    g_troute.url_ids[0] = DOH_URL;
    g_live_cache = true;
    ASSERT(live_start(&lv));
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xd501, &g));
    ASSERT_EQ(0, g.rcode);
    ASSERT_EQ(0, g.last);
    ASSERT_EQ(FIRC_DNS_RESOLVER_GROUP, atomic_load(&g_live_resolver));
    ASSERT(ask_got(lv.proxy_port, 0xd502, &g));
    ASSERT_EQ(0, g.last);
    ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    ASSERT_EQ(0u, live_fallbacks(&lv));
    live_stop(&lv);
    PASS();
}

TEST without_fallback_every_leg_timing_out_is_servfail(void) {
    live_t lv;
    doh_setup(DOH_SILENT, 0, true, true);
    g_troute.n = 2;
    g_troute.url_ids[1] = DOH_URL;
    ASSERT(live_start(&lv));
    g_troute.ports[0] = lv.b.port;
    atomic_store(&lv.b.hold, 1);
    long t0 = ms_now();
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xd601, &g));
    ASSERT_GTE(ms_now() - t0, (long)FIRC_DNSPROXY_TUNNEL_TIMEOUT_MS - 50);
    ASSERT_EQ(2, g.rcode);
    ASSERT_EQ(-1, g.last);
    ASSERT_EQ(FIRC_DNS_RESOLVER_LOCAL, atomic_load(&g_live_resolver));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    ASSERT_EQ(1, atomic_load(&lv.b.received));
    ASSERT_EQ(1, atomic_load(&g_fdoh.cancels));
    atomic_store(&lv.b.go, 1);
    live_stop(&lv);
    PASS();
}

static void rest_dns_group(live_t *lv) {
    for (int i = 0; i < (int)FIRC_RESOLVE_HEALTH_FAILS; i++) {
        got_t g;
        g.rcode = -1;
        (void)ask_got(lv->proxy_port, (uint16_t)(0xd700 + i), &g);
    }
}

TEST without_fallback_a_resting_group_answers_servfail_at_once(void) {
    live_t lv;
    doh_setup(DOH_GOOD, 0, true, true);
    ASSERT(live_start(&lv));
    g_troute.ports[0] = closed_udp_port();
    ASSERT(g_troute.ports[0] != 0);
    log_capture_start();
    rest_dns_group(&lv);
    ASSERT_EQ((int)FIRC_RESOLVE_HEALTH_FAILS, atomic_load(&g_rec.opens));
    long t0 = ms_now();
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xd711, &g));
    ASSERT_LT(ms_now() - t0, 100);
    ASSERT_EQ(2, g.rcode);
    ASSERT_EQ(FIRC_DNS_RESOLVER_LOCAL, atomic_load(&g_live_resolver));
    ASSERT_EQm("resting: no leg opened", (int)FIRC_RESOLVE_HEALTH_FAILS, atomic_load(&g_rec.opens));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    firc_dnsproxy_health_t h;
    ASSERT(live_health(&lv, 1, &h));
    ASSERT(h.resting);
    live_stop(&lv);
    int servfail = 0, upstream = 0;
    log_capture_counts("the group's names are answered SERVFAIL", &servfail, "common upstream", &upstream);
    ASSERT_EQ(1, servfail);
    ASSERT_EQ(0, upstream);
    PASS();
}

TEST without_fallback_a_due_probe_still_asks(void) {
    live_t lv;
    doh_setup(DOH_GOOD, 0, true, true);
    g_live_rest_ms = 300;
    ASSERT(live_start(&lv));
    g_troute.ports[0] = closed_udp_port();
    ASSERT(g_troute.ports[0] != 0);
    rest_dns_group(&lv);
    g_troute.ports[0] = lv.b.port;
    pause_ms(350);
    log_capture_start();
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xd801, &g));
    ASSERT_EQ(0, g.rcode);
    ASSERT_EQ(2, g.last);
    ASSERT_EQ(1, atomic_load(&lv.b.received));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    live_stop(&lv);
    int resolver = 0, tunnel = 0;
    log_capture_counts("answering again; the group's names go to its resolver", &resolver, "tunnel", &tunnel);
    ASSERT_EQ(1, resolver);
    ASSERT_EQ(0, tunnel);
    PASS();
}

TEST with_fallback_a_dns_route_falls_back_as_any_group(void) {
    live_t lv;
    doh_setup(DOH_GOOD, 0, true, false);
    ASSERT(live_start(&lv));
    g_troute.ports[0] = closed_udp_port();
    ASSERT(g_troute.ports[0] != 0);
    ASSERT_EQ(1, ask(lv.proxy_port, 0xd901));
    ASSERT_EQ(1, atomic_load(&lv.a.received));
    ASSERT_EQ(0, atomic_load(&g_rec.marks));
    ASSERT_EQ(FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE, atomic_load(&g_live_resolver));
    live_stop(&lv);
    PASS();
}

TEST a_tcp_client_is_answered_over_doh(void) {
    live_t lv;
    doh_setup(DOH_GOOD, DOH_MARK, false, false);
    g_troute.n = 2;
    g_troute.url_ids[0] = DOH_URL;
    g_troute.url_ids[1] = DOH_URL + 1;
    ASSERT(live_start(&lv));
    uint8_t q[512];
    size_t qlen = build_query(q, 0xda01, 0);
    ASSERT_EQ(DOH_ANSWER_OCTET, ask_tcp(lv.proxy_port, 0xda01));
    ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
    ASSERT_EQ(DOH_URL, atomic_load(&g_fdoh.last_url));
    ASSERT_EQ((int)qlen, atomic_load(&g_fdoh.last_len));
    ASSERT_EQ(0xda01, atomic_load(&g_fdoh.last_id));
    ASSERT_EQ(0, atomic_load(&g_rec.stream_opens));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    live_stop(&lv);
    PASS();
}

TEST a_doh_answer_is_cached(void) {
    live_t lv;
    doh_setup(DOH_GOOD, DOH_MARK, false, false);
    g_troute.url_ids[0] = DOH_URL;
    g_live_cache = true;
    ASSERT(live_start(&lv));
    ASSERT_EQ(DOH_ANSWER_OCTET, ask(lv.proxy_port, 0xdb01));
    ASSERT_EQ(DOH_ANSWER_OCTET, ask(lv.proxy_port, 0xdb02));
    ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
    ASSERT_EQ(FIRC_DNS_RESOLVER_CACHE, atomic_load(&g_live_resolver));
    live_stop(&lv);
    PASS();
}

TEST an_oversized_doh_answer_is_truncated_for_udp(void) {
    live_t lv;
    doh_setup(DOH_BIG, DOH_MARK, false, false);
    g_troute.url_ids[0] = DOH_URL;
    ASSERT(live_start(&lv));
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xdc01, &g));
    ASSERT(g.tc);
    ASSERT_EQ(-1, g.last);
    live_stop(&lv);
    PASS();
}

TEST without_fallback_servfails_from_the_server_do_not_rest_the_group(void) {
    live_t lv;
    doh_setup(DOH_GOOD, 0, true, true);
    ASSERT(live_start(&lv));
    g_troute.ports[0] = lv.b.port;
    atomic_store(&lv.b.rcode, 2);
    for (int i = 0; i < 4; i++) {
        got_t g;
        ASSERT(ask_got(lv.proxy_port, (uint16_t)(0xdd01 + i), &g));
        ASSERT_EQ(2, g.rcode);
    }
    ASSERT_EQ(4, atomic_load(&lv.b.received));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    firc_dnsproxy_health_t h;
    ASSERT(live_health(&lv, 1, &h));
    ASSERT_FALSE(h.resting);
    ASSERT_EQ(0u, h.fallbacks);
    live_stop(&lv);
    PASS();
}

TEST without_fallback_no_server_is_servfail_not_upstream(void) {
    live_t lv;
    doh_setup(DOH_GOOD, 0, true, true);
    g_troute.n = 0;
    ASSERT(live_start(&lv));
    got_t g;
    ASSERT(ask_got(lv.proxy_port, 0xde01, &g));
    ASSERT_EQ(2, g.rcode);
    ASSERT_EQ(FIRC_DNS_RESOLVER_LOCAL, atomic_load(&g_live_resolver));
    ASSERT_EQ(0, atomic_load(&lv.a.received));
    ASSERT_EQ(0, atomic_load(&g_rec.opens));
    live_stop(&lv);
    PASS();
}

TEST without_fallback_tcp_never_reaches_upstream(void) {
    int modes[2] = {DOH_FAIL, DOH_REFUSE};
    for (int m = 0; m < 2; m++) {
        live_t lv;
        doh_setup(modes[m], 0, true, true);
        g_troute.url_ids[0] = DOH_URL;
        ASSERT(live_start(&lv));
        got_t g;
        long t0 = ms_now();
        ASSERTm(m == 0 ? "failed transfer" : "refused submit", ask_tcp_got(lv.proxy_port, 0xdf01, &g));
        ASSERT_LT(ms_now() - t0, 500);
        ASSERT_EQ(2, g.rcode);
        ASSERT_EQ(FIRC_DNS_RESOLVER_LOCAL, atomic_load(&g_live_resolver));
        ASSERT_EQ(1, atomic_load(&g_fdoh.queries));
        ASSERT_EQ(0, atomic_load(&g_rec.stream_opens));
        live_stop(&lv);
    }
    xopts_t o;
    memset(&o, 0, sizeof(o));
    o.over_tcp = true;
    o.tunnel = true;
    o.dns_route = true;
    o.no_fallback = true;
    o.tunnel_sink_a = true;
    reply_t out;
    memset(&out, 0, sizeof(out));
    int hook_calls = 0;
    int rc = run_exchange(&o, &out, &hook_calls);
    if (rc == -2) { SKIPm("could not bind the harness's ports"); }
    ASSERT_EQ(0, rc);
    ASSERT(out.saw_a);
    ASSERT_EQ(0, out.a[3]);
    ASSERT_EQ(1, atomic_load(&g_rec.connects));
    PASS();
}

static void dns_owner_batch(live_t *lv, int drop, uint16_t id) {
    int fds[8];
    atomic_store(&lv->a.drop, drop);
    for (int i = 0; i < 8; i++) { fds[i] = ask_send_for(lv->proxy_port, (uint16_t)(id + i), 0, (char)('c' + i)); }
    pause_ms(700);
    for (int i = 0; i < 8; i++) {
        if (fds[i] >= 0) { close(fds[i]); }
    }
}

TEST a_dns_owner_is_never_counted_by_the_upstream_outage_detector(void) {
    live_t lv;
    tunnel_setup(0);
    g_troute.mark = 0;
    g_troute.dns_only = true;
    g_live_timeout_ms = 100;
    atomic_store(&g_rep_ms, 1000);
    firc_logrep_set_clock_for_test(rep_clock);
    firc_event_reset_for_test();
    ASSERT(live_start(&lv));
    g_troute.ports[0] = closed_udp_port();
    ASSERT(g_troute.ports[0] != 0);
    dns_owner_batch(&lv, 8, 0x6a00);
    int dns_missed = journal_at(FIRC_LOG_WARN, "the common upstream does not answer");
    g_troute.take = false;
    dns_owner_batch(&lv, 8, 0x6a10);
    int warned = journal_at(FIRC_LOG_WARN, "the common upstream does not answer");
    g_troute.take = true;
    ASSERT_EQ(1, ask(lv.proxy_port, 0x6a20));
    int dns_recovered = journal_at(FIRC_LOG_INFO, "the common upstream: recovered");
    g_troute.take = false;
    ASSERT_EQ(1, ask(lv.proxy_port, 0x6a21));
    int recovered = journal_at(FIRC_LOG_INFO, "the common upstream: recovered");
    live_stop(&lv);
    firc_logrep_set_clock_for_test(NULL);
    ASSERT_EQ_FMT(0, dns_missed, "%d");
    ASSERT_EQ_FMT(1, warned, "%d");
    ASSERT_EQ_FMT(0, dns_recovered, "%d");
    ASSERT_EQ_FMT(1, recovered, "%d");
    PASS();
}

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(a_dns_routes_aaaa_is_stripped_when_drop_aaaa_is_on);
    RUN_TEST(a_dns_routes_aaaa_passes_when_drop_aaaa_is_off);
    RUN_TEST(a_doh_server_answers_as_a_leg);
    RUN_TEST(a_doh_leg_losing_the_race_is_cancelled);
    RUN_TEST(a_stray_doh_answer_fails_its_leg_at_once);
    RUN_TEST(a_dns_route_sets_no_mark);
    RUN_TEST(without_fallback_a_servfail_reaches_the_client);
    RUN_TEST(without_fallback_a_sink_answer_reaches_the_client);
    RUN_TEST(without_fallback_every_leg_timing_out_is_servfail);
    RUN_TEST(without_fallback_a_resting_group_answers_servfail_at_once);
    RUN_TEST(without_fallback_a_due_probe_still_asks);
    RUN_TEST(with_fallback_a_dns_route_falls_back_as_any_group);
    RUN_TEST(a_tcp_client_is_answered_over_doh);
    RUN_TEST(a_doh_answer_is_cached);
    RUN_TEST(an_oversized_doh_answer_is_truncated_for_udp);
    RUN_TEST(without_fallback_servfails_from_the_server_do_not_rest_the_group);
    RUN_TEST(without_fallback_no_server_is_servfail_not_upstream);
    RUN_TEST(without_fallback_tcp_never_reaches_upstream);
    RUN_TEST(a_dns_owner_is_never_counted_by_the_upstream_outage_detector);
    GREATEST_MAIN_END();
}
