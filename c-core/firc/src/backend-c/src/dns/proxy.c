#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/dnsproxy.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

#include "firc/listen.h"
#include "firc/anscache.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/resolver_addr.h"
#include "pktinfo.h"

#define TCP_MAX_MSG 65535

typedef struct sock_pool {
    int *fds;
    size_t len;
    size_t cap;
} sock_pool_t;

typedef struct tunnel_pool {
    firc_id_t id;
    struct sockaddr_storage server;
    socklen_t server_len;
    uint64_t gen;
    uint64_t epoch;
    sock_pool_t pool;
} tunnel_pool_t;

#define HEALTH_BUCKETS 10u
#define HEALTH_BUCKET_MS (FIRC_DNSPROXY_HEALTH_WINDOW_S * 1000u / HEALTH_BUCKETS)

typedef struct health_bucket {
    uint64_t minute;
    uint32_t total, fallbacks;
} health_bucket_t;

typedef struct group_health {
    firc_id_t id;
    uint64_t gen;
    size_t preferred;
    unsigned fails;
    uint64_t rest_until_ms; /* 0: not resting */
    uint64_t rest_start_ms;
    bool probing;
    health_bucket_t buckets[HEALTH_BUCKETS];
    uint64_t last_ms;
    uint8_t last_reason;
} group_health_t;

struct firc_dnsproxy {
    char listen_error[192];
    firc_dnsproxy_listen_t listen;
    firc_listen_seen_t seen[2];
    firc_dnsproxy_config_t cfg;
    firc_loop_t *loop;
    firc_dnsproxy_msg_cb cb;
    void *cb_ud;
    firc_dnsproxy_cross_cb cross_fn;
    void *cross_ud;

    int udp_fd;
    int tcp_fd;
    int family; /* AF_INET / AF_INET6 */

    struct sockaddr_storage upstream_sa;
    socklen_t upstream_sa_len;

    /* cfg.upstream_addr points here */
    char *upstream_addr;
    uint64_t upstream_gen;

    sock_pool_t udp_pool;

    firc_dnsproxy_sock_ops_t ops;
    firc_dnsproxy_route_fn route_fn;
    void *route_ud;
    firc_dnsproxy_blocked_cb blocked_fn;
    void *blocked_ud;
    firc_dnsproxy_tally_fn tally_fn;
    void *tally_ud;
    firc_ip_t pool4, pool6;
    uint8_t pool4_len, pool6_len;
    bool have_pool4, have_pool6;
    const firc_fakeip_t *fakeip;
    uint32_t ptr_ttl;
    firc_anscache_t *cache;
    uint64_t (*cache_now)(void);
    uint64_t (*health_clock)(void);

    group_health_t *health;
    size_t n_health, cap_health;
    bool health_planted;
    firc_id_t planted_id;
    uint64_t planted_gen;
    firc_dnsproxy_health_t planted;
    unsigned health_fails;
    uint32_t health_rest_ms;

    tunnel_pool_t *tpools;
    size_t n_tpools, cap_tpools;

    _Atomic uint64_t inflight;
    _Atomic uint64_t dropped;
    uint64_t drop_warn_ms;
    uint64_t drop_warn_base;
    firc_logrep_t mark_refused;
    firc_logrep_t silent;
    uint64_t misses;
    bool drop_warned;

    struct exchange *held_head;
    size_t n_held;
    struct exchange *prefetch_head;
    size_t n_prefetch;
};

static void pool_init(sock_pool_t *p, size_t cap)
{
    p->fds = calloc(cap > 0 ? cap : 1, sizeof(int));
    p->len = 0;
    p->cap = cap;
}

static void pool_clear(sock_pool_t *p)
{
    for (size_t i = 0; i < p->len; i++) {
        close(p->fds[i]);
    }
    free(p->fds);
    p->fds = NULL;
    p->len = 0;
}

static void pool_drain(sock_pool_t *p)
{
    for (size_t i = 0; i < p->len; i++) { close(p->fds[i]); }
    p->len = 0;
}

static int pool_get(sock_pool_t *p)
{
    if (p->len > 0) {
        return p->fds[--p->len];
    }
    return -1;
}

static void pool_put(sock_pool_t *p, int fd)
{
    if (p->len < p->cap) {
        p->fds[p->len++] = fd;
    } else {
        close(fd);
    }
}

/* NULL is 0.0.0.0, and "" is not [::]. */
static int parse_listen(const char *addr, uint16_t port, int *family,
                        struct sockaddr_storage *sa, socklen_t *sa_len)
{
    if (!firc_listen_addr(addr != NULL ? addr : "0.0.0.0", port, false, sa, sa_len)) {
        return -1;
    }
    *family = sa->ss_family;
    return 0;
}

static int real_open(void *ud, int family, int type)
{
    (void)ud;
    return socket(family, type, 0);
}

static int real_set_mark(void *ud, int fd, uint32_t mark)
{
    (void)ud;
    return setsockopt(fd, SOL_SOCKET, SO_MARK, &mark, sizeof(mark));
}

static int real_connect(void *ud, int fd, const struct sockaddr *sa, socklen_t len)
{
    (void)ud;
    return connect(fd, sa, len);
}

static ssize_t real_send(void *ud, int fd, const void *buf, size_t len, int flags)
{
    (void)ud;
    return send(fd, buf, len, flags);
}

const firc_dnsproxy_sock_ops_t firc_dnsproxy_sock_ops_real = {
    .open = real_open, .set_mark = real_set_mark, .connect = real_connect, .send = real_send, .ud = NULL,
};

void firc_dnsproxy_set_sock_ops(firc_dnsproxy_t *p, const firc_dnsproxy_sock_ops_t *ops)
{
    p->ops = ops != NULL ? *ops : firc_dnsproxy_sock_ops_real;
}

void firc_dnsproxy_set_router(firc_dnsproxy_t *p, firc_dnsproxy_route_fn fn, void *ud)
{
    p->route_fn = fn;
    p->route_ud = ud;
}

void firc_dnsproxy_set_blocked(firc_dnsproxy_t *p, firc_dnsproxy_blocked_cb fn, void *ud)
{
    p->blocked_fn = fn;
    p->blocked_ud = ud;
}

void firc_dnsproxy_set_cross(firc_dnsproxy_t *p, firc_dnsproxy_cross_cb fn, void *ud)
{
    p->cross_fn = fn;
    p->cross_ud = ud;
}

void firc_dnsproxy_set_tally(firc_dnsproxy_t *p, firc_dnsproxy_tally_fn fn, void *ud)
{
    p->tally_fn = fn;
    p->tally_ud = ud;
}

typedef struct leg {
    struct exchange *ex;
    int fd;
    size_t server;
    uint64_t tpool_epoch;
    bool done;
} leg_t;

typedef struct exchange {
    firc_dnsproxy_t *p;
    bool is_tcp;
    int upstream_fd;
    int timer_id;
    uint64_t upstream_gen;
    /* upstream_tunnel, not tunnel, decides which pool the socket returns to */
    bool upstream_tunnel;
    bool connecting;

    uint8_t *req;
    size_t req_len;
    size_t req_sent;
    uint16_t udp_max;

    struct sockaddr_storage client;
    socklen_t client_len;
    firc_pktinfo_t dst;
    bool have_dst;

    int client_fd;
    size_t req_got;

    uint8_t len_buf[2];
    size_t len_got;
    size_t resp_expected;
    uint8_t *resp;
    size_t resp_got;

    uint8_t *out;
    size_t out_len;
    size_t out_sent;

    bool counted; /* holds one unit of p->inflight */

    firc_dnsproxy_route_t route;
    bool tunnel;
    bool framed;          /* req already carries its TCP length prefix */
    size_t server;
    leg_t legs[FIRC_RESOLVE_MAX_SERVERS];
    size_t n_legs;
    size_t legs_open;
    int retry_timer_id;
    uint8_t resolver;     /* firc_dns_resolver_t: where the answer came from */
    uint64_t deadline_ms; /* monotonic; one deadline shared by both legs */
    bool probe;
    firc_anscache_key_t ckey;
    bool cacheable;
    uint64_t tpool_epoch;
    bool want_prefetch;
    bool prefetch;
    uint8_t *cached;
    size_t cached_len;

    uint16_t qid;
    size_t n_q;
    firc_dns_question_t q;

    uint8_t *held;
    size_t held_len;
    firc_dns_msg_t *held_msg;
    firc_dns_msg_t *cross_a;
    uint8_t cross_resolver;
    struct exchange *held_next;
    struct exchange *prefetch_next;
} exchange_t;

static uint64_t mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

#define DROP_WARN_INTERVAL_MS 10000u

#define SILENT_RUN 8u
#define SILENT_ERROR_AFTER_MS 60000u

static bool common_leg_open(const exchange_t *ex)
{
    return ex->upstream_fd >= 0 && !ex->upstream_tunnel;
}

static void upstream_missed(firc_dnsproxy_t *p)
{
    if (++p->misses < SILENT_RUN) { return; }
    firc_logrep_fail_for_quiet(&p->silent, SILENT_ERROR_AFTER_MS, NULL,
                         "the common upstream does not answer: %llu queries in a row went unanswered",
                         (unsigned long long)p->misses);
}

static void upstream_answered(firc_dnsproxy_t *p)
{
    p->misses = 0;
    (void)firc_logrep_ok(&p->silent, NULL, "the common upstream");
}

static void mark_refused(firc_dnsproxy_t *p)
{
    firc_logrep_fail_for_quiet(&p->mark_refused, UINT64_MAX, NULL,
                               "group resolver: SO_MARK refused (%s); the group's names go to the common upstream",
                               strerror(errno));
}

static void note_drop(firc_dnsproxy_t *p)
{
    uint64_t total = atomic_fetch_add(&p->dropped, 1) + 1;
    uint64_t now = p->cache_now();
    if (p->drop_warned && now - p->drop_warn_ms < DROP_WARN_INTERVAL_MS) { return; }
    FIRC_WARN("dns proxy: in-flight budget full, dropped %llu since the last warning (app.dnsProxy.maxConcurrent %u)",
              (unsigned long long)(total - p->drop_warn_base), p->cfg.max_concurrent);
    p->drop_warned = true;
    p->drop_warn_ms = now;
    p->drop_warn_base = total;
}

static uint32_t leg_timeout_ms(const exchange_t *ex)
{
    uint64_t now = mono_ms();
    uint64_t left = ex->deadline_ms > now ? ex->deadline_ms - now : 0;
    if (!ex->tunnel) {
        return (uint32_t)(left > FIRC_DNSPROXY_FALLBACK_FLOOR_MS ? left : FIRC_DNSPROXY_FALLBACK_FLOOR_MS);
    }
    uint64_t t = left > (uint64_t)FIRC_DNSPROXY_FALLBACK_FLOOR_MS * 2u ? left - FIRC_DNSPROXY_FALLBACK_FLOOR_MS : left / 2u;
    if (t > FIRC_DNSPROXY_TUNNEL_TIMEOUT_MS) { t = FIRC_DNSPROXY_TUNNEL_TIMEOUT_MS; }
    return (uint32_t)(t > 0 ? t : 1);
}

static bool id_same(firc_id_t a, firc_id_t b)
{
    return memcmp(&a, &b, sizeof(a)) == 0;
}

static uint64_t health_now(const firc_dnsproxy_t *p)
{
    return p->health_clock();
}

static bool health_in_window(uint64_t minute, uint64_t now_minute)
{
    return minute + HEALTH_BUCKETS - 1u >= now_minute;
}

static health_bucket_t *health_bucket(group_health_t *h, uint64_t now)
{
    uint64_t minute = now / HEALTH_BUCKET_MS;
    health_bucket_t *b = &h->buckets[minute % HEALTH_BUCKETS];
    if (b->minute != minute) {
        b->minute = minute;
        b->total = 0;
        b->fallbacks = 0;
    }
    return b;
}

static void health_answered(group_health_t *h, uint64_t now)
{
    health_bucket(h, now)->total++;
}

static void health_fell_back(group_health_t *h, uint64_t now, firc_dns_resolver_t why)
{
    health_bucket_t *b = health_bucket(h, now);
    b->total++;
    b->fallbacks++;
    h->last_ms = now;
    h->last_reason = (uint8_t)why;
}

static group_health_t *health_for(firc_dnsproxy_t *p, firc_id_t id, uint64_t gen)
{
    group_health_t *h = NULL;
    for (size_t i = 0; i < p->n_health && h == NULL; i++) {
        if (id_same(p->health[i].id, id)) { h = &p->health[i]; }
    }
    if (h == NULL) {
        if (p->n_health == p->cap_health) {
            size_t nc = p->cap_health ? p->cap_health * 2 : 8;
            group_health_t *nh = realloc(p->health, nc * sizeof(*nh));
            if (nh == NULL) { return NULL; }
            p->health = nh;
            p->cap_health = nc;
        }
        h = &p->health[p->n_health++];
        memset(h, 0, sizeof(*h));
        h->id = id;
        h->gen = gen;
    }
    if (h->gen != gen) {
        group_health_t keep = *h;
        memset(h, 0, sizeof(*h));
        h->id = id;
        h->gen = gen;
        memcpy(h->buckets, keep.buckets, sizeof(h->buckets));
        h->last_ms = keep.last_ms;
        h->last_reason = keep.last_reason;
    }
    return h;
}

/* Never creates or resets: an outcome belongs to the exchange's own generation. */
static group_health_t *health_lookup(firc_dnsproxy_t *p, firc_id_t id)
{
    for (size_t i = 0; i < p->n_health; i++) {
        if (id_same(p->health[i].id, id)) { return &p->health[i]; }
    }
    return NULL;
}

static void health_fallback_counted(firc_dnsproxy_t *p, firc_id_t id, firc_dns_resolver_t why)
{
    group_health_t *h = health_lookup(p, id);
    if (h != NULL) { health_fell_back(h, health_now(p), why); }
}

static void health_failure(firc_dnsproxy_t *p, exchange_t *ex)
{
    bool probe = ex->probe;
    ex->probe = false;
    group_health_t *h = health_lookup(p, ex->route.group_id);
    if (h == NULL || h->gen != ex->route.gen) { return; }
    h->preferred = (ex->server + 1) % ex->route.n_servers;
    h->fails++;
    if (probe || h->fails >= p->health_fails) {
        /* By hand: firc_id_format is not linked into firc-dnstool. */
        const uint8_t *g = ex->route.group_id.b;
        if (probe) {
            FIRC_DEBUG("group resolver: group \"%s\" (%02x%02x%02x%02x): the probe failed; resting another %u s",
                       ex->route.group_name, g[0], g[1], g[2], g[3], (unsigned)(p->health_rest_ms / 1000u));
        } else if (h->rest_until_ms == 0) {
            FIRC_WARN("group resolver: group \"%s\" (%02x%02x%02x%02x): %u consecutive failures, the group's names "
                      "go to the common upstream until one query every %u s finds it answering",
                      ex->route.group_name, g[0], g[1], g[2], g[3], h->fails, (unsigned)(p->health_rest_ms / 1000u));
        }
        uint64_t now = health_now(p);
        if (h->rest_until_ms == 0) { h->rest_start_ms = now; }
        h->rest_until_ms = now + p->health_rest_ms;
        h->probing = false;
    }
}

static bool health_success(firc_dnsproxy_t *p, exchange_t *ex)
{
    ex->probe = false;
    group_health_t *h = health_lookup(p, ex->route.group_id);
    if (h == NULL || h->gen != ex->route.gen) { return false; }
    bool was_resting = h->rest_until_ms != 0;
    h->fails = 0;
    h->rest_until_ms = 0;
    h->rest_start_ms = 0;
    h->probing = false;
    if (was_resting) {
        const uint8_t *g = ex->route.group_id.b;
        FIRC_INFO("group resolver: group \"%s\" (%02x%02x%02x%02x): answering again; the group's names go "
                  "through its tunnel",
                  ex->route.group_name, g[0], g[1], g[2], g[3]);
    }
    return true;
}

void firc_dnsproxy_set_health(firc_dnsproxy_t *p, unsigned fails, uint32_t rest_ms)
{
    p->health_fails = fails ? fails : FIRC_RESOLVE_HEALTH_FAILS;
    p->health_rest_ms = rest_ms ? rest_ms : FIRC_RESOLVE_HEALTH_SKIP_MS;
}

void firc_dnsproxy_group_health(const firc_dnsproxy_t *p, firc_id_t group_id, uint64_t gen,
                                firc_dnsproxy_health_t *out)
{
    memset(out, 0, sizeof(*out));
    if (p->health_planted && id_same(p->planted_id, group_id) && p->planted_gen == gen) {
        *out = p->planted;
        return;
    }
    const group_health_t *h = NULL;
    for (size_t i = 0; i < p->n_health && h == NULL; i++) {
        if (id_same(p->health[i].id, group_id)) { h = &p->health[i]; }
    }
    if (h == NULL) { return; }
    uint64_t now = health_now(p);
    uint64_t minute = now / HEALTH_BUCKET_MS;
    for (size_t i = 0; i < HEALTH_BUCKETS; i++) {
        if (!health_in_window(h->buckets[i].minute, minute)) { continue; }
        out->total += h->buckets[i].total;
        out->fallbacks += h->buckets[i].fallbacks;
    }
    if (h->last_reason != 0 && health_in_window(h->last_ms / HEALTH_BUCKET_MS, minute)) {
        out->has_last = true;
        out->last_ago_ms = now > h->last_ms ? now - h->last_ms : 0;
        out->reason = h->last_reason;
    }
    if (h->gen == gen && h->rest_until_ms != 0) {
        out->resting = true;
        out->since_ago_ms = now > h->rest_start_ms ? now - h->rest_start_ms : 0;
    }
}

static bool server_same(const tunnel_pool_t *t, const firc_dnsproxy_route_t *rt, size_t server)
{
    socklen_t len = rt->server_lens[server];
    return t->server_len == len && memcmp(&t->server, &rt->servers[server], len) == 0;
}

static tunnel_pool_t *tpool_find(firc_dnsproxy_t *p, const firc_dnsproxy_route_t *rt, size_t server)
{
    for (size_t i = 0; i < p->n_tpools; i++) {
        if (id_same(p->tpools[i].id, rt->group_id) && server_same(&p->tpools[i], rt, server)) { return &p->tpools[i]; }
    }
    return NULL;
}

static tunnel_pool_t *tpool_for(firc_dnsproxy_t *p, const firc_dnsproxy_route_t *rt, size_t server)
{
    tunnel_pool_t *t = tpool_find(p, rt, server);
    if (t == NULL) {
        if (p->n_tpools == p->cap_tpools) {
            size_t nc = p->cap_tpools ? p->cap_tpools * 2 : 8;
            tunnel_pool_t *nt = realloc(p->tpools, nc * sizeof(*nt));
            if (nt == NULL) { return NULL; }
            p->tpools = nt;
            p->cap_tpools = nc;
        }
        t = &p->tpools[p->n_tpools];
        memset(t, 0, sizeof(*t));
        t->id = rt->group_id;
        t->server_len = rt->server_lens[server];
        memcpy(&t->server, &rt->servers[server], t->server_len);
        t->gen = rt->gen;
        pool_init(&t->pool, p->cfg.max_idle_conns);
        if (t->pool.fds == NULL) { return NULL; }
        p->n_tpools++;
    }
    if (t->gen < rt->gen) {
        pool_drain(&t->pool);
        t->gen = rt->gen;
        t->epoch++;
    }
    return t;
}

void firc_dnsproxy_close_group_pools(firc_dnsproxy_t *p, firc_id_t group_id)
{
    for (size_t i = 0; i < p->n_tpools; i++) {
        if (id_same(p->tpools[i].id, group_id)) {
            pool_drain(&p->tpools[i].pool);
            p->tpools[i].epoch++;
        }
    }
    firc_anscache_drop_group(p->cache, group_id);
}

void firc_dnsproxy_forget_group(firc_dnsproxy_t *p, firc_id_t group_id)
{
    for (size_t i = 0; i < p->n_tpools;) {
        if (id_same(p->tpools[i].id, group_id)) {
            pool_clear(&p->tpools[i].pool);
            p->tpools[i] = p->tpools[--p->n_tpools];
        } else {
            i++;
        }
    }
    for (size_t i = 0; i < p->n_health; i++) {
        if (id_same(p->health[i].id, group_id)) {
            p->health[i] = p->health[--p->n_health];
            break;
        }
    }
    firc_anscache_drop_group(p->cache, group_id);
}

static void exchange_free(exchange_t *ex)
{
    free(ex->req);
    free(ex->resp);
    free(ex->out);
    free(ex->held);
    free(ex->cached);
    firc_dns_msg_free(ex->held_msg);
    firc_dns_msg_free(ex->cross_a);
    free(ex);
}

/* firc_loop_mod_fd changes only the mask, so a callback switch is del + add. */
static bool rearm_fd(firc_loop_t *loop, int fd, uint32_t events, firc_fd_cb cb,
                     void *ud)
{
    (void)firc_loop_del_fd(loop, fd);
    return firc_loop_add_fd(loop, fd, events, cb, ud) == FIRC_OK;
}

static void held_unlink(firc_dnsproxy_t *p, exchange_t *ex);
static void prefetch_unlink(firc_dnsproxy_t *p, exchange_t *ex);
static void prefetch_start(firc_dnsproxy_t *p, const exchange_t *src);
static void leg_failed(exchange_t *ex, firc_dns_resolver_t why);
static void tunnel_failed(exchange_t *ex, firc_dns_resolver_t why);
static void start_upstream(firc_dnsproxy_t *p, exchange_t *ex);
static void on_timeout(firc_loop_t *loop, void *ud);
static bool follow_up(exchange_t *ex, firc_dns_msg_t *a);

/* Pool by upstream_tunnel, never tunnel: a marked socket must not reach the common pool. */
static void upstream_release(firc_dnsproxy_t *p, exchange_t *ex, bool reuse)
{
    if (ex->upstream_fd < 0) { return; }
    firc_loop_del_fd(p->loop, ex->upstream_fd);
    if (reuse && !ex->is_tcp && !ex->upstream_tunnel && ex->upstream_gen == p->upstream_gen) {
        pool_put(&p->udp_pool, ex->upstream_fd);
    } else {
        close(ex->upstream_fd);
    }
    ex->upstream_fd = -1;
    ex->upstream_tunnel = false;
}

static void leg_release(firc_dnsproxy_t *p, exchange_t *ex, leg_t *leg, bool reuse)
{
    if (leg->fd < 0) { return; }
    firc_loop_del_fd(p->loop, leg->fd);
    tunnel_pool_t *t = reuse ? tpool_find(p, &ex->route, leg->server) : NULL;
    if (t != NULL && t->gen == ex->route.gen && t->epoch == leg->tpool_epoch) {
        pool_put(&t->pool, leg->fd);
    } else {
        close(leg->fd);
    }
    leg->fd = -1;
}

static void legs_release(firc_dnsproxy_t *p, exchange_t *ex, bool reuse)
{
    for (size_t i = 0; i < ex->n_legs; i++) { leg_release(p, ex, &ex->legs[i], reuse); }
    ex->n_legs = 0;
    ex->legs_open = 0;
    if (ex->retry_timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->retry_timer_id);
        ex->retry_timer_id = 0;
    }
}

static void exchange_finish(exchange_t *ex, bool pool_upstream)
{
    firc_dnsproxy_t *p = ex->p;
    if (ex->probe) {
        group_health_t *h = health_lookup(p, ex->route.group_id);
        if (h != NULL && h->gen == ex->route.gen) { h->probing = false; }
        ex->probe = false;
    }
    if (ex->timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    upstream_release(p, ex, pool_upstream);
    legs_release(p, ex, pool_upstream);
    if (ex->client_fd >= 0) {
        firc_loop_del_fd(p->loop, ex->client_fd);
        close(ex->client_fd);
        ex->client_fd = -1;
    }
    if (ex->held_msg != NULL) { held_unlink(p, ex); } /* a TCP client gone mid-hold */
    if (ex->prefetch) {
        prefetch_unlink(p, ex);
        firc_anscache_prefetch_done(p->cache, &ex->ckey);
    }
    if (ex->counted) { atomic_fetch_sub(&p->inflight, 1); }
    exchange_free(ex);
}

static void udp_reply(firc_dnsproxy_t *p, exchange_t *ex, const uint8_t *data,
                      size_t len)
{
    struct msghdr msg;
    struct iovec iov;
    firc_pktinfo_cmsg_t cbuf;
    memset(&msg, 0, sizeof(msg));

    /* sendmsg does not modify the payload. NOLINTNEXTLINE(performance-no-int-to-ptr) */
    memcpy(&iov.iov_base, &data, sizeof(iov.iov_base));
    iov.iov_len = len;
    msg.msg_name = &ex->client;
    msg.msg_namelen = ex->client_len;
    msg.msg_iov = &iov;
    msg.msg_iovlen = 1;

    if (ex->have_dst) {
        msg.msg_control = &cbuf;
        msg.msg_controllen = firc_pktinfo_write(&ex->dst, &cbuf);
    }
    (void)sendmsg(p->udp_fd, &msg, 0);
}

static void exchange_send(exchange_t *ex, const uint8_t *out, size_t out_len)
{
    firc_dnsproxy_t *p = ex->p;
    if (ex->is_tcp) {
        ex->out = malloc(out_len + 2);
        if (ex->out == NULL) {
            exchange_finish(ex, false);
            return;
        }
        ex->out[0] = (uint8_t)(out_len >> 8);
        ex->out[1] = (uint8_t)out_len;
        memcpy(ex->out + 2, out, out_len);
        ex->out_len = out_len + 2;
        ex->out_sent = 0;
        if (ex->upstream_fd >= 0) {
            firc_loop_del_fd(p->loop, ex->upstream_fd);
            close(ex->upstream_fd);
            ex->upstream_fd = -1;
            ex->upstream_tunnel = false;
        }
        /* From here nothing may fall back: the leg timer becomes the client write's. */
        if (ex->tunnel) {
            ex->tunnel = false;
            if (ex->timer_id > 0) {
                firc_loop_del_timer(p->loop, ex->timer_id);
                ex->timer_id = 0;
                if (firc_loop_add_timer(p->loop, leg_timeout_ms(ex), 0, on_timeout, ex, &ex->timer_id) !=
                    FIRC_OK) {
                    exchange_finish(ex, false);
                    return;
                }
            }
        }
        firc_loop_mod_fd(p->loop, ex->client_fd, EPOLLOUT);
        return;
    }
    udp_reply(p, ex, out, out_len);
    exchange_finish(ex, true);
}

static void held_unlink(firc_dnsproxy_t *p, exchange_t *ex)
{
    for (exchange_t **pp = &p->held_head; *pp != NULL; pp = &(*pp)->held_next) {
        if (*pp == ex) {
            *pp = ex->held_next;
            p->n_held--;
            break;
        }
    }
    ex->held_next = NULL;
}

static void prefetch_unlink(firc_dnsproxy_t *p, exchange_t *ex)
{
    for (exchange_t **pp = &p->prefetch_head; *pp != NULL; pp = &(*pp)->prefetch_next) {
        if (*pp == ex) {
            *pp = ex->prefetch_next;
            p->n_prefetch--;
            break;
        }
    }
    ex->prefetch_next = NULL;
}

static void held_send(exchange_t *ex)
{
    firc_dnsproxy_t *p = ex->p;
    held_unlink(p, ex);
    if (ex->timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    uint8_t *out = ex->held;
    size_t out_len = ex->held_len;
    ex->held = NULL;
    ex->held_len = 0;
    firc_dns_msg_free(ex->held_msg);
    ex->held_msg = NULL;
    exchange_send(ex, out, out_len);
    free(out);
}

/* At the deadline the answer goes out anyway: silence sends the client elsewhere. */
static void on_hold_deadline(firc_loop_t *loop, void *ud)
{
    (void)loop;
    exchange_t *ex = ud;
    ex->timer_id = 0; /* already fired */
    held_send(ex);
}

void firc_dnsproxy_release(firc_dnsproxy_t *p, firc_dnsproxy_ready_fn ready, void *ud)
{
    if (p == NULL) { return; }
    exchange_t *ex = p->held_head;
    while (ex != NULL) {
        exchange_t *next = ex->held_next;
        if (ready == NULL || ready(ex->held_msg, ud)) { held_send(ex); }
        ex = next;
    }
}

size_t firc_dnsproxy_held(const firc_dnsproxy_t *p)
{
    return p == NULL ? 0 : p->n_held;
}

static bool client_ip(const exchange_t *ex, firc_ip_t *out)
{
    if (ex->client_len == 0) { return false; }
    if (ex->client.ss_family == AF_INET) {
        const struct sockaddr_in *s4 = (const struct sockaddr_in *)&ex->client;
        memcpy(out->b, &s4->sin_addr, 4);
        out->len = 4;
        return true;
    }
    if (ex->client.ss_family == AF_INET6) {
        const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)&ex->client;
        if (IN6_IS_ADDR_V4MAPPED(&s6->sin6_addr)) {
            memcpy(out->b, &s6->sin6_addr.s6_addr[12], 4);
            out->len = 4;
        } else {
            memcpy(out->b, &s6->sin6_addr, 16);
            out->len = 16;
        }
        return true;
    }
    return false;
}

void firc_dnsproxy_set_pool_prefixes(firc_dnsproxy_t *p, const firc_ip_t *v4, uint8_t v4_len,
                                     const firc_ip_t *v6, uint8_t v6_len)
{
    p->have_pool4 = v4 != NULL && v4->len == 4 && v4_len <= 32;
    if (p->have_pool4) {
        p->pool4 = *v4;
        p->pool4_len = v4_len;
    }
    p->have_pool6 = v6 != NULL && v6->len == 16 && v6_len <= 128;
    if (p->have_pool6) {
        p->pool6 = *v6;
        p->pool6_len = v6_len;
    }
}

static bool prefix_has(const firc_ip_t *base, uint8_t len, const uint8_t *addr)
{
    size_t whole = len / 8u;
    if (memcmp(base->b, addr, whole) != 0) { return false; }
    unsigned rest = len % 8u;
    if (rest == 0) { return true; }
    uint8_t mask = (uint8_t)(0xffu << (8u - rest));
    return (base->b[whole] & mask) == (addr[whole] & mask);
}

static firc_dns_resolver_t tunnel_answer_verdict(const firc_dnsproxy_t *p, const firc_dns_msg_t *msg)
{
    unsigned rcode = msg->flags & 0x0fu;
    if (rcode == 2u) { return FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL; }
    if (rcode == 5u) { return FIRC_DNS_RESOLVER_FALLBACK_REFUSED; }
    for (size_t i = 0; i < msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        firc_ip_t ip;
        if (rr->rtype == FIRC_DNS_TYPE_A && rr->rdata_len == 4) {
            ip.len = 4;
        } else if (rr->rtype == FIRC_DNS_TYPE_AAAA && rr->rdata_len == 16) {
            ip.len = 16;
        } else {
            continue;
        }
        memcpy(ip.b, rr->rdata, ip.len);
        if (firc_resolver_ip_is_sink(&ip)) { return FIRC_DNS_RESOLVER_FALLBACK_SINK; }
        if (ip.len == 4 && p->have_pool4 && prefix_has(&p->pool4, p->pool4_len, ip.b)) {
            return FIRC_DNS_RESOLVER_FALLBACK_SINK;
        }
        if (ip.len == 16 && p->have_pool6 && prefix_has(&p->pool6, p->pool6_len, ip.b)) {
            return FIRC_DNS_RESOLVER_FALLBACK_SINK;
        }
    }
    return FIRC_DNS_RESOLVER_GROUP;
}

static bool name_eq_nocase(const uint8_t *a, const uint8_t *b, size_t len)
{
    for (size_t i = 0; i < len; i++) {
        uint8_t x = a[i], y = b[i];
        if (x >= 'A' && x <= 'Z') { x = (uint8_t)(x + 32); }
        if (y >= 'A' && y <= 'Z') { y = (uint8_t)(y + 32); }
        if (x != y) { return false; }
    }
    return true;
}

/* An error answer with no question is accepted: some servers send FORMERR/NOTIMP so. */
static bool question_matches(const exchange_t *ex, const firc_dns_msg_t *msg)
{
    if (msg->n_questions == 0 && (msg->flags & 0x0fu) != 0) { return true; }
    if (msg->n_questions != ex->n_q) { return false; }
    if (ex->n_q == 0) { return true; }
    const firc_dns_question_t *a = &msg->questions[0];
    return a->qtype == ex->q.qtype && a->qclass == ex->q.qclass && a->name_len == ex->q.name_len &&
           name_eq_nocase(a->name, ex->q.name, a->name_len);
}

/* UDP: ignore it and keep waiting (late duplicate, spoof); TCP or a cache hit: an unusable answer. */
static void stray_answer(exchange_t *ex)
{
    if (ex->is_tcp || ex->resolver == FIRC_DNS_RESOLVER_CACHE) { leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL); }
}

static void deliver_parsed(exchange_t *ex, firc_dns_msg_t *msg, const uint8_t *resp, size_t resp_len)
{
    firc_dnsproxy_t *p = ex->p;
    const char *network = ex->is_tcp ? "tcp" : "udp";
    firc_dns_verdict_t verdict = FIRC_DNS_PASS;
    if (ex->prefetch) {
        firc_dns_msg_free(msg);
        exchange_finish(ex, true);
        return;
    }
    firc_ip_t client;
    const firc_ip_t *cp = client_ip(ex, &client) ? &client : NULL;
    bool repack = false;
    if (ex->cross_a != NULL) {
        firc_dns_msg_t *a = ex->cross_a;
        ex->cross_a = NULL;
        verdict = p->cross_fn(a, msg, cp, (firc_dns_resolver_t)ex->cross_resolver, p->cross_ud);
        firc_dns_msg_free(msg);
        msg = a;
        repack = true;
    } else if (p->cb != NULL) {
        verdict = p->cb(msg, cp, network, (firc_dns_resolver_t)ex->resolver, p->cb_ud);
    }
    if (verdict == FIRC_DNS_ASK_AAAA) {
        if (follow_up(ex, msg)) { return; }
        verdict = FIRC_DNS_PASS;
    }
    if (verdict == FIRC_DNS_DROP) {
        /* The real answer must not stand in for a rewrite that failed. */
        firc_dns_msg_free(msg);
        exchange_finish(ex, true);
        return;
    }
    /* RETIMED is not a rewrite: AAAA stripping must still apply to it. */
    bool modified = verdict == FIRC_DNS_REWRITTEN || verdict == FIRC_DNS_HOLD;
    bool retimed = verdict == FIRC_DNS_RETIMED;

    const uint8_t *out = resp;
    size_t out_len = resp_len;
    uint8_t *packed = NULL;
    /* No AAAA strip after a rewrite: it would delete what the pool just issued. */
    bool stripping = !p->cfg.disable_drop_aaaa && !modified && firc_dns_msg_strip_aaaa(msg);
    if (modified || retimed || stripping || repack) {
        if (firc_dns_msg_pack(msg, &packed, &out_len) != FIRC_OK) {
            firc_dns_msg_free(msg);
            exchange_finish(ex, false);
            return;
        }
        /* Packing writes rdata whole, so a reply that fitted may no longer fit UDP. */
        if (!ex->is_tcp && out_len > ex->udp_max) {
            /* A truncated reply carries no address: nothing to hold for. */
            verdict = FIRC_DNS_REWRITTEN;
            firc_dns_msg_truncate(msg);
            free(packed);
            packed = NULL;
            if (firc_dns_msg_pack(msg, &packed, &out_len) != FIRC_OK) {
                firc_dns_msg_free(msg);
                exchange_finish(ex, false);
                return;
            }
            /* The OPT record must stay in a reply to an EDNS query (RFC 6891 7). */
            if (out_len > ex->udp_max && msg->n_additional > 0) {
                for (size_t i = 0; i < msg->n_additional; i++) {
                    free(msg->additional[i].rdata);
                    msg->additional[i].rdata = NULL;
                    msg->additional[i].rdata_len = 0;
                }
                free(packed);
                packed = NULL;
                if (firc_dns_msg_pack(msg, &packed, &out_len) != FIRC_OK) {
                    firc_dns_msg_free(msg);
                    exchange_finish(ex, false);
                    return;
                }
            }
        }
        out = packed;
    }

    size_t max_held = p->cfg.max_held ? p->cfg.max_held : p->cfg.max_concurrent;
    if (verdict == FIRC_DNS_HOLD && p->cfg.hold_ms > 0 && p->n_held < max_held) {
        ex->held = malloc(out_len);
        if (ex->held == NULL) {
            free(packed);
            firc_dns_msg_free(msg);
            exchange_finish(ex, false);
            return;
        }
        memcpy(ex->held, out, out_len);
        ex->held_len = out_len;
        ex->held_msg = msg;
        free(packed);
        upstream_release(p, ex, true);
        if (ex->timer_id > 0) { firc_loop_del_timer(p->loop, ex->timer_id); }
        ex->timer_id = 0;
        if (firc_loop_add_timer(p->loop, p->cfg.hold_ms, 0, on_hold_deadline, ex, &ex->timer_id) !=
            FIRC_OK) {
            exchange_finish(ex, false);
            return;
        }
        ex->held_next = p->held_head;
        p->held_head = ex;
        p->n_held++;
        /* Held answers leave the in-flight budget, or overflow would drop all DNS. */
        atomic_fetch_sub(&p->inflight, 1);
        ex->counted = false;
        return;
    }

    exchange_send(ex, out, out_len);
    free(packed);
    firc_dns_msg_free(msg);
}

static void tunnel_won(exchange_t *ex, const firc_dns_msg_t *msg, const uint8_t *resp, size_t resp_len)
{
    firc_dnsproxy_t *p = ex->p;
    if (!ex->prefetch && ex->cross_a == NULL) {
        group_health_t *h = health_lookup(p, ex->route.group_id);
        if (h != NULL) { health_answered(h, health_now(p)); }
    }
    if (health_success(p, ex) && ex->cacheable) {
        (void)firc_anscache_put(p->cache, &ex->ckey, resp, resp_len, firc_anscache_lifetime(msg), p->cache_now());
    }
}

static void deliver_response(exchange_t *ex, const uint8_t *resp, size_t resp_len)
{
    firc_dnsproxy_t *p = ex->p;
    if (resp_len < 2 || (uint16_t)((uint16_t)resp[0] << 8 | resp[1]) != ex->qid) {
        stray_answer(ex);
        return;
    }
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(resp, resp_len, &msg) != FIRC_OK) {
        leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL);
        return;
    }
    if (!question_matches(ex, msg)) {
        firc_dns_msg_free(msg);
        stray_answer(ex);
        return;
    }
    if (ex->tunnel) {
        firc_dns_resolver_t why = tunnel_answer_verdict(p, msg);
        if (why != FIRC_DNS_RESOLVER_GROUP) {
            firc_dns_msg_free(msg);
            leg_failed(ex, why);
            return;
        }
        tunnel_pool_t *t = tpool_find(p, &ex->route, ex->server);
        if (t == NULL || t->epoch != ex->tpool_epoch) { ex->cacheable = false; }
        tunnel_won(ex, msg, resp, resp_len);
    } else if (common_leg_open(ex)) {
        upstream_answered(p);
    }
    deliver_parsed(ex, msg, resp, resp_len);
}

static void on_timeout(firc_loop_t *loop, void *ud)
{
    (void)loop;
    exchange_t *ex = ud;
    ex->timer_id = 0; /* already fired */
    leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_TIMEOUT);
}

static void on_upstream(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    exchange_t *ex = ud;

    if (events & (EPOLLERR | EPOLLHUP)) {
        leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
        return;
    }

    if (ex->is_tcp && ex->connecting) {
        int err = 0;
        socklen_t elen = sizeof(err);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &elen) != 0 ||
            err != 0) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->connecting = false;
        firc_loop_mod_fd(loop, fd, EPOLLOUT);
        return;
    }

    if (!ex->is_tcp) {
        uint8_t buf[FIRC_DNS_MAX_MSG];
        ssize_t n = recv(fd, buf, sizeof(buf), 0);
        if (n <= 0) {
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                return;
            }
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        deliver_response(ex, buf, (size_t)n);
        return;
    }

    while (ex->len_got < 2) {
        ssize_t n = recv(fd, ex->len_buf + ex->len_got, 2 - ex->len_got, 0);
        if (n <= 0) {
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                return;
            }
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->len_got += (size_t)n;
    }
    if (ex->resp == NULL) {
        ex->resp_expected =
            (size_t)ex->len_buf[0] << 8 | (size_t)ex->len_buf[1];
        if (ex->resp_expected == 0 || ex->resp_expected > TCP_MAX_MSG) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL);
            return;
        }
        /* Zeroed for the analyzer: the loop below fills it before it is read. */
        ex->resp = calloc(1, ex->resp_expected);
        if (ex->resp == NULL) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
    }
    while (ex->resp_got < ex->resp_expected) {
        ssize_t n = recv(fd, ex->resp + ex->resp_got,
                         ex->resp_expected - ex->resp_got, 0);
        if (n <= 0) {
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                return;
            }
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->resp_got += (size_t)n;
    }
    deliver_response(ex, ex->resp, ex->resp_got);
}

static void on_upstream_writable(firc_loop_t *loop, int fd, uint32_t events,
                                 void *ud)
{
    exchange_t *ex = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
        return;
    }
    if (ex->is_tcp && ex->connecting) {
        int err = 0;
        socklen_t elen = sizeof(err);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &elen) != 0 ||
            err != 0) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->connecting = false;
    }
    while (ex->req_sent < ex->req_len) {
        ssize_t n = ex->p->ops.send(ex->p->ops.ud, fd, ex->req + ex->req_sent,
                         ex->req_len - ex->req_sent, MSG_NOSIGNAL);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
                return;
            }
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->req_sent += (size_t)n;
    }
    if (!rearm_fd(loop, fd, EPOLLIN, on_upstream, ex)) {
        leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
    }
}

static void leg_fail(leg_t *leg, firc_dns_resolver_t why, bool reuse)
{
    exchange_t *ex = leg->ex;
    if (leg->done) { return; }
    leg->done = true;
    leg_release(ex->p, ex, leg, reuse);
    if (ex->legs_open > 0) { ex->legs_open--; }
    if (ex->legs_open == 0) { tunnel_failed(ex, why); }
}

static void deliver_leg(leg_t *leg, const uint8_t *resp, size_t resp_len)
{
    exchange_t *ex = leg->ex;
    firc_dnsproxy_t *p = ex->p;
    if (resp_len < 2 || (uint16_t)((uint16_t)resp[0] << 8 | resp[1]) != ex->qid) {
        stray_answer(ex);
        return;
    }
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(resp, resp_len, &msg) != FIRC_OK) {
        leg_fail(leg, FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL, true);
        return;
    }
    if (!question_matches(ex, msg)) {
        firc_dns_msg_free(msg);
        stray_answer(ex);
        return;
    }
    firc_dns_resolver_t why = tunnel_answer_verdict(p, msg);
    if (why != FIRC_DNS_RESOLVER_GROUP) {
        firc_dns_msg_free(msg);
        leg_fail(leg, why, true);
        return;
    }
    tunnel_pool_t *t = tpool_find(p, &ex->route, leg->server);
    if (t == NULL || t->epoch != leg->tpool_epoch) { ex->cacheable = false; }
    legs_release(p, ex, true);
    tunnel_won(ex, msg, resp, resp_len);
    deliver_parsed(ex, msg, resp, resp_len);
}

static void on_leg_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    leg_t *leg = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        leg_fail(leg, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE, false);
        return;
    }
    uint8_t buf[FIRC_DNS_MAX_MSG];
    ssize_t n = recv(fd, buf, sizeof(buf), 0);
    if (n <= 0) {
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) { return; }
        leg_fail(leg, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE, false);
        return;
    }
    deliver_leg(leg, buf, (size_t)n);
}

static bool leg_open(firc_dnsproxy_t *p, exchange_t *ex, leg_t *leg)
{
    const firc_dnsproxy_route_t *rt = &ex->route;
    tunnel_pool_t *t = tpool_for(p, rt, leg->server);
    leg->tpool_epoch = t != NULL ? t->epoch : 0;
    int fd = (t != NULL && t->gen == rt->gen) ? pool_get(&t->pool) : -1;
    if (fd < 0) {
        fd = p->ops.open(p->ops.ud, rt->servers[leg->server].ss_family, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (fd < 0) { return false; }
        if (p->ops.set_mark(p->ops.ud, fd, rt->mark) != 0) {
            mark_refused(p);
            close(fd);
            return false;
        }
        (void)firc_logrep_ok(&p->mark_refused, NULL, "group resolver: SO_MARK");
        if (p->ops.connect(p->ops.ud, fd, (const struct sockaddr *)&rt->servers[leg->server],
                           rt->server_lens[leg->server]) != 0) {
            close(fd);
            return false;
        }
    }
    leg->fd = fd;
    ssize_t n = p->ops.send(p->ops.ud, fd, ex->req, ex->req_len, MSG_NOSIGNAL);
    bool sent = n == (ssize_t)ex->req_len || (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK));
    if (!sent || firc_loop_add_fd(p->loop, fd, EPOLLIN, on_leg_readable, leg) != FIRC_OK) {
        leg_release(p, ex, leg, false);
        return false;
    }
    return true;
}

static void on_retry(firc_loop_t *loop, void *ud)
{
    (void)loop;
    exchange_t *ex = ud;
    ex->retry_timer_id = 0;
    leg_t *leg = &ex->legs[0];
    if (ex->n_legs != 1 || leg->done || leg->fd < 0) { return; }
    ssize_t n = ex->p->ops.send(ex->p->ops.ud, leg->fd, ex->req, ex->req_len, MSG_NOSIGNAL);
    if (n < 0 && errno != EAGAIN && errno != EWOULDBLOCK) {
        leg_fail(leg, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE, false);
    }
}

static void start_legs(firc_dnsproxy_t *p, exchange_t *ex)
{
    ex->n_legs = ex->route.n_servers;
    ex->legs_open = 0;
    for (size_t i = 0; i < ex->n_legs; i++) {
        leg_t *leg = &ex->legs[i];
        memset(leg, 0, sizeof(*leg));
        leg->ex = ex;
        leg->fd = -1;
        leg->server = (ex->server + i) % ex->route.n_servers;
        if (leg_open(p, ex, leg)) {
            ex->legs_open++;
        } else {
            leg->done = true;
        }
    }
    if (ex->legs_open == 0) {
        tunnel_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
        return;
    }
    uint32_t window = leg_timeout_ms(ex);
    if (firc_loop_add_timer(p->loop, window, 0, on_timeout, ex, &ex->timer_id) != FIRC_OK) {
        tunnel_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
        return;
    }
    if (ex->n_legs == 1 && window > FIRC_DNSPROXY_RETRY_MS) {
        (void)firc_loop_add_timer(p->loop, FIRC_DNSPROXY_RETRY_MS, 0, on_retry, ex, &ex->retry_timer_id);
    }
}

static void on_client_writable(firc_loop_t *loop, int fd, uint32_t events,
                               void *ud)
{
    (void)loop;
    (void)fd;
    exchange_t *ex = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        exchange_finish(ex, false);
        return;
    }
    while (ex->out_sent < ex->out_len) {
        ssize_t n = send(ex->client_fd, ex->out + ex->out_sent,
                         ex->out_len - ex->out_sent, MSG_NOSIGNAL);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
                return;
            }
            exchange_finish(ex, false);
            return;
        }
        ex->out_sent += (size_t)n;
    }
    exchange_finish(ex, false);
}

static void follow_up_failed(exchange_t *ex)
{
    firc_dnsproxy_t *p = ex->p;
    if (ex->timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    legs_release(p, ex, true);
    upstream_release(p, ex, false);
    ex->tunnel = false;
    if (ex->is_tcp && firc_loop_add_timer(p->loop, leg_timeout_ms(ex), 0, on_timeout, ex, &ex->timer_id) != FIRC_OK) {
        exchange_finish(ex, false);
        return;
    }
    deliver_parsed(ex, NULL, NULL, 0);
}

static void tunnel_failed(exchange_t *ex, firc_dns_resolver_t why)
{
    firc_dnsproxy_t *p = ex->p;
    if (ex->cross_a != NULL) {
        follow_up_failed(ex);
        return;
    }
    if (ex->timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    legs_release(p, ex, true);
    upstream_release(p, ex, false);
    ex->tunnel = false;
    if (ex->prefetch) {
        exchange_finish(ex, false);
        return;
    }
    health_failure(p, ex);
    health_fallback_counted(p, ex->route.group_id, why);
    ex->resolver = (uint8_t)why;
    start_upstream(p, ex);
}

static void leg_failed(exchange_t *ex, firc_dns_resolver_t why)
{
    if (common_leg_open(ex)) { upstream_missed(ex->p); }
    if (ex->cross_a != NULL) {
        follow_up_failed(ex);
        return;
    }
    if (!ex->tunnel) {
        exchange_finish(ex, false);
        return;
    }
    tunnel_failed(ex, why);
}

static void start_upstream(firc_dnsproxy_t *p, exchange_t *ex)
{
    /* Reset read state: the TCP client read and a failed tunnel leg reused it. */
    ex->len_got = 0;
    ex->resp_expected = 0;
    ex->resp_got = 0;
    free(ex->resp);
    ex->resp = NULL;
    ex->req_sent = 0;
    ex->connecting = false;
    if (ex->tunnel && !ex->is_tcp) {
        start_legs(p, ex);
        return;
    }

    const struct sockaddr *dst = (const struct sockaddr *)&p->upstream_sa;
    socklen_t dst_len = p->upstream_sa_len;
    int family = p->family;
    if (ex->tunnel) {
        tunnel_pool_t *t = tpool_for(p, &ex->route, ex->server);
        ex->tpool_epoch = t != NULL ? t->epoch : 0;
        dst = (const struct sockaddr *)&ex->route.servers[ex->server];
        dst_len = ex->route.server_lens[ex->server];
        family = ex->route.servers[ex->server].ss_family;
    }

    if (ex->is_tcp && !ex->framed) {
        /* Framed once, for both legs. */
        uint8_t *framed = malloc(ex->req_len + 2);
        if (framed == NULL) {
            exchange_finish(ex, false);
            return;
        }
        framed[0] = (uint8_t)(ex->req_len >> 8);
        framed[1] = (uint8_t)ex->req_len;
        memcpy(framed + 2, ex->req, ex->req_len);
        free(ex->req);
        ex->req = framed;
        ex->req_len += 2;
        ex->framed = true;
    }

    int fd = -1;
    if (!ex->is_tcp && !ex->tunnel) { fd = pool_get(&p->udp_pool); }
    bool fresh = fd < 0;
    if (fresh) {
        fd = p->ops.open(p->ops.ud, family, (ex->is_tcp ? SOCK_STREAM : SOCK_DGRAM) | SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (fd < 0) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
    }
    ex->upstream_fd = fd; /* from here a failure closes it through upstream_release */
    ex->upstream_gen = p->upstream_gen;
    ex->upstream_tunnel = ex->tunnel;
    if (fresh) {
        /* SO_MARK before connect: the route is chosen at connect. */
        if (ex->tunnel && p->ops.set_mark(p->ops.ud, fd, ex->route.mark) != 0) {
            mark_refused(p);
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        if (ex->tunnel) { (void)firc_logrep_ok(&p->mark_refused, NULL, "group resolver: SO_MARK"); }
        int rc = p->ops.connect(p->ops.ud, fd, dst, dst_len);
        if (rc < 0 && !(ex->is_tcp && errno == EINPROGRESS)) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->connecting = ex->is_tcp && rc < 0;
    }

    if (ex->is_tcp) {
        if (firc_loop_add_fd(p->loop, fd, EPOLLOUT, on_upstream_writable, ex) != FIRC_OK) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
    } else {
        ssize_t n = p->ops.send(p->ops.ud, fd, ex->req, ex->req_len, MSG_NOSIGNAL);
        if (n < 0 && errno != EAGAIN && errno != EWOULDBLOCK) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
        ex->req_sent = (n > 0) ? (size_t)n : 0;
        uint32_t ev = (ex->req_sent < ex->req_len) ? EPOLLOUT : EPOLLIN;
        firc_fd_cb cb = (ev == EPOLLOUT) ? on_upstream_writable : on_upstream;
        if (firc_loop_add_fd(p->loop, fd, ev, cb, ex) != FIRC_OK) {
            leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
            return;
        }
    }

    int tid = 0;
    if (firc_loop_add_timer(p->loop, leg_timeout_ms(ex), 0, on_timeout, ex, &tid) != FIRC_OK) {
        leg_failed(ex, FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE);
        return;
    }
    ex->timer_id = tid;
}

static bool qtype_offset(const uint8_t *m, size_t len, size_t *at)
{
    size_t i = 12;
    for (;;) {
        if (i >= len) { return false; }
        uint8_t l = m[i];
        if (l == 0) {
            i += 1;
            break;
        }
        if ((l & 0xc0u) == 0xc0u) {
            i += 2;
            break;
        }
        if ((l & 0xc0u) != 0) { return false; }
        i += 1u + l;
    }
    if (i + 4 > len) { return false; }
    *at = i;
    return true;
}

static bool follow_up(exchange_t *ex, firc_dns_msg_t *a)
{
    firc_dnsproxy_t *p = ex->p;
    size_t off = ex->framed ? 2u : 0u;
    size_t at = 0;
    if (p->cross_fn == NULL || ex->n_q != 1 || ex->q.qtype != FIRC_DNS_TYPE_A ||
        !qtype_offset(ex->req + off, ex->req_len - off, &at)) {
        return false;
    }
    if (ex->timer_id > 0) {
        firc_loop_del_timer(p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    upstream_release(p, ex, true);
    legs_release(p, ex, true);
    ex->req[off + at] = (uint8_t)((unsigned)FIRC_DNS_TYPE_AAAA >> 8u);
    ex->req[off + at + 1] = (uint8_t)FIRC_DNS_TYPE_AAAA;
    ex->q.qtype = FIRC_DNS_TYPE_AAAA;
    ex->cross_a = a;
    ex->cross_resolver = ex->resolver;
    ex->deadline_ms = mono_ms() + p->cfg.timeout_ms;
    bool routed = ex->route.n_servers > 0;
    if (routed && ex->resolver == FIRC_DNS_RESOLVER_CACHE) {
        group_health_t *h = health_lookup(p, ex->route.group_id);
        bool own = h != NULL && h->gen == ex->route.gen;
        uint64_t now = health_now(p);
        bool resting = own && h->rest_until_ms != 0 && (now < h->rest_until_ms || h->probing);
        ex->tunnel = !resting;
        ex->server = own ? h->preferred % ex->route.n_servers : 0;
        ex->resolver = (uint8_t)(resting ? FIRC_DNS_RESOLVER_HEALTH_SKIP : FIRC_DNS_RESOLVER_GROUP);
    }
    firc_dns_msg_t *q = NULL;
    ex->cacheable = routed && firc_dns_msg_parse(ex->req + off, ex->req_len - off, &q) == FIRC_OK &&
                    firc_anscache_key_of(q, ex->route.group_id, ex->route.gen, &ex->ckey);
    firc_dns_msg_free(q);
    firc_anscache_hit_t hit;
    if (ex->cacheable &&
        firc_anscache_get(p->cache, &ex->ckey, ex->req + off, ex->req_len - off, p->cache_now(), &hit)) {
        bool usable = ex->is_tcp || hit.len <= ex->udp_max;
        if (hit.prefetch && usable) {
            prefetch_start(p, ex);
        } else if (hit.prefetch) {
            firc_anscache_prefetch_done(p->cache, &ex->ckey);
        }
        if (usable) {
            ex->tunnel = false;
            ex->resolver = FIRC_DNS_RESOLVER_CACHE;
            if (ex->is_tcp &&
                firc_loop_add_timer(p->loop, leg_timeout_ms(ex), 0, on_timeout, ex, &ex->timer_id) != FIRC_OK) {
                free(hit.wire);
                exchange_finish(ex, false);
                return true;
            }
            deliver_response(ex, hit.wire, hit.len);
            free(hit.wire);
            return true;
        }
        free(hit.wire);
    }
    start_upstream(p, ex);
    return true;
}

static void prefetch_start(firc_dnsproxy_t *p, const exchange_t *src)
{
    group_health_t *h = health_lookup(p, src->route.group_id);
    bool own = h != NULL && h->gen == src->route.gen;
    uint64_t now = health_now(p);
    bool gated = own && h->rest_until_ms != 0 && (now < h->rest_until_ms || h->probing);
    exchange_t *ex = (p->n_prefetch < FIRC_DNSPROXY_MAX_PREFETCH && !gated) ? calloc(1, sizeof(*ex)) : NULL;
    size_t off = src->framed ? 2u : 0u;
    uint8_t *req = ex != NULL ? malloc(src->req_len - off) : NULL;
    if (req == NULL) {
        free(ex);
        firc_anscache_prefetch_done(p->cache, &src->ckey);
        return;
    }
    memcpy(req, src->req + off, src->req_len - off);
    ex->p = p;
    ex->upstream_fd = -1;
    ex->client_fd = -1;
    ex->req = req;
    ex->req_len = src->req_len - off;
    ex->udp_max = src->udp_max;
    ex->qid = src->qid;
    ex->n_q = src->n_q;
    ex->q = src->q;
    ex->route = src->route;
    ex->ckey = src->ckey;
    ex->cacheable = true;
    ex->prefetch = true;
    ex->tunnel = true;
    ex->server = own ? h->preferred % src->route.n_servers : 0;
    ex->resolver = FIRC_DNS_RESOLVER_GROUP;
    ex->deadline_ms = mono_ms() + p->cfg.timeout_ms;
    ex->prefetch_next = p->prefetch_head;
    p->prefetch_head = ex;
    p->n_prefetch++;
    start_upstream(p, ex);
}

static void exchange_begin(firc_dnsproxy_t *p, exchange_t *ex)
{
    if (ex->cached == NULL) {
        start_upstream(p, ex);
        return;
    }
    if (ex->want_prefetch) { prefetch_start(p, ex); }
    uint8_t *wire = ex->cached;
    size_t len = ex->cached_len;
    ex->cached = NULL;
    if (ex->is_tcp && firc_loop_add_timer(p->loop, leg_timeout_ms(ex), 0, on_timeout, ex, &ex->timer_id) != FIRC_OK) {
        free(wire);
        exchange_finish(ex, false);
        return;
    }
    deliver_response(ex, wire, len);
    free(wire);
}

typedef enum request_disp {
    REQ_DROP = 0,     /* consumed: caller must finish/free */
    REQ_REPLIED_ASYNC, /* TCP local reply queued: keep ex, wait for write */
    REQ_FORWARD,      /* send upstream */
} request_disp_t;

static request_disp_t reply_local(firc_dnsproxy_t *p, exchange_t *ex, uint8_t *resp, size_t rlen)
{
    if (ex->is_tcp) {
        ex->out = malloc(rlen + 2);
        if (ex->out == NULL) {
            free(resp);
            return REQ_DROP;
        }
        ex->out[0] = (uint8_t)(rlen >> 8);
        ex->out[1] = (uint8_t)rlen;
        memcpy(ex->out + 2, resp, rlen);
        ex->out_len = rlen + 2;
        free(resp);
        firc_loop_mod_fd(p->loop, ex->client_fd, EPOLLOUT);
        return REQ_REPLIED_ASYNC;
    }
    udp_reply(p, ex, resp, rlen);
    free(resp);
    return REQ_DROP; /* UDP reply is synchronous; free ex */
}

static request_disp_t reply_ptr(firc_dnsproxy_t *p, exchange_t *ex, const firc_dns_msg_t *query,
                                const char *name)
{
    uint8_t *resp = NULL;
    size_t rlen = 0;
    firc_err_t err = firc_dns_make_ptr_response(query, name, p->ptr_ttl, &resp, &rlen);
    if (err == FIRC_ERR_INVAL && name != NULL) {
        err = firc_dns_make_ptr_response(query, NULL, p->ptr_ttl, &resp, &rlen);
    }
    if (err != FIRC_OK) { return REQ_DROP; }
    return reply_local(p, ex, resp, rlen);
}

static request_disp_t reply_block(firc_dnsproxy_t *p, exchange_t *ex, const firc_dns_msg_t *query,
                                  const firc_ip_t *client, firc_block_answer_t answer)
{
    uint8_t *resp = NULL;
    size_t rlen = 0;
    if (firc_dns_make_block_response(query, answer, p->ptr_ttl, &resp, &rlen) != FIRC_OK) { return REQ_DROP; }
    if (p->blocked_fn != NULL) { p->blocked_fn(p->blocked_ud, query, client, answer); }
    return reply_local(p, ex, resp, rlen);
}

static void tally(firc_dnsproxy_t *p, const firc_dns_msg_t *query, const firc_ip_t *client, firc_id_t owner,
                  bool blocked)
{
    if (p->tally_fn != NULL && client != NULL) { p->tally_fn(p->tally_ud, query, client, owner, blocked); }
}

static request_disp_t handle_request_common(firc_dnsproxy_t *p, exchange_t *ex)
{
    firc_dns_msg_t *req_msg = NULL;
    if (firc_dns_msg_parse(ex->req, ex->req_len, &req_msg) != FIRC_OK) {
        return REQ_DROP;
    }
    ex->udp_max = firc_dns_msg_udp_payload_size(req_msg);
    ex->qid = req_msg->id;
    ex->n_q = req_msg->n_questions;
    if (ex->n_q > 0) { ex->q = req_msg->questions[0]; }
    firc_ip_t client;
    const firc_ip_t *cp = client_ip(ex, &client) ? &client : NULL;
    firc_id_t no_owner = {{0}};

    firc_ip_t ptr_addr;
    if (p->fakeip != NULL && firc_dns_ptr_query_addr(req_msg, &ptr_addr) &&
        firc_fakeip_overlaps(p->fakeip, &ptr_addr, (uint8_t)(ptr_addr.len * 8u))) {
        tally(p, req_msg, cp, no_owner, false);
        request_disp_t disp = reply_ptr(p, ex, req_msg, firc_fakeip_name_of(p->fakeip, &ptr_addr));
        firc_dns_msg_free(req_msg);
        return disp;
    }
    /* The deadline starts here and is shared by both legs. */
    ex->deadline_ms = mono_ms() + p->cfg.timeout_ms;
    ex->resolver = FIRC_DNS_RESOLVER_UPSTREAM;
    firc_id_t owner = no_owner;
    if (p->route_fn != NULL) {
        firc_dnsproxy_route_t rt;
        rt.blocked = false;
        rt.owner = no_owner;
        bool routed = p->route_fn(p->route_ud, req_msg, cp, &rt);
        owner = rt.owner;
        if (routed && rt.blocked) {
            tally(p, req_msg, cp, owner, true);
            request_disp_t disp = reply_block(p, ex, req_msg, cp, rt.answer);
            firc_dns_msg_free(req_msg);
            return disp;
        }
        if (routed && rt.n_servers > 0 && rt.n_servers <= FIRC_RESOLVE_MAX_SERVERS && rt.mark != 0) {
            ex->route = rt;
            ex->cacheable = firc_anscache_key_of(req_msg, rt.group_id, rt.gen, &ex->ckey);
            firc_anscache_hit_t hit;
            if (ex->cacheable &&
                firc_anscache_get(p->cache, &ex->ckey, ex->req, ex->req_len, p->cache_now(), &hit)) {
                if (ex->is_tcp || hit.len <= ex->udp_max) {
                    ex->cached = hit.wire;
                    ex->cached_len = hit.len;
                    ex->want_prefetch = hit.prefetch;
                    ex->resolver = FIRC_DNS_RESOLVER_CACHE;
                    tally(p, req_msg, cp, owner, false);
                    firc_dns_msg_free(req_msg);
                    return REQ_FORWARD;
                }
                if (hit.prefetch) { firc_anscache_prefetch_done(p->cache, &ex->ckey); }
                free(hit.wire);
            }
            group_health_t *h = health_for(p, rt.group_id, rt.gen);
            uint64_t now = health_now(p);
            bool resting = h != NULL && h->rest_until_ms != 0 && (now < h->rest_until_ms || h->probing);
            if (resting) {
                ex->resolver = FIRC_DNS_RESOLVER_HEALTH_SKIP;
                health_fell_back(h, now, FIRC_DNS_RESOLVER_HEALTH_SKIP);
            } else {
                if (h != NULL && h->rest_until_ms != 0) {
                    h->probing = true;
                    ex->probe = true;
                }
                ex->tunnel = true;
                ex->server = h != NULL ? h->preferred % rt.n_servers : 0;
                ex->resolver = FIRC_DNS_RESOLVER_GROUP;
            }
        }
    }
    tally(p, req_msg, cp, owner, false);
    firc_dns_msg_free(req_msg);
    return REQ_FORWARD;
}

static void on_udp_readable(firc_loop_t *loop, int fd, uint32_t events,
                            void *ud)
{
    (void)loop;
    firc_dnsproxy_t *p = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        return;
    }

    for (;;) {
        uint8_t buf[FIRC_DNS_MAX_MSG];
        struct sockaddr_storage client;
        struct iovec iov = {buf, sizeof(buf)};
        union {
            char v4[CMSG_SPACE(sizeof(struct in_pktinfo))];
            char v6[CMSG_SPACE(sizeof(struct in6_pktinfo))];
        } cbuf;
        struct msghdr msg;
        memset(&msg, 0, sizeof(msg));
        msg.msg_name = &client;
        msg.msg_namelen = sizeof(client);
        msg.msg_iov = &iov;
        msg.msg_iovlen = 1;
        msg.msg_control = &cbuf;
        msg.msg_controllen = sizeof(cbuf);

        ssize_t n = recvmsg(fd, &msg, 0);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
                return;
            }
            if (errno == EINTR) {
                continue;
            }
            return;
        }

        uint64_t cur = atomic_load(&p->inflight);
        if (cur >= p->cfg.max_concurrent) {
            note_drop(p);
            continue;
        }

        exchange_t *ex = calloc(1, sizeof(*ex));
        if (ex == NULL) {
            continue;
        }
        ex->p = p;
        ex->is_tcp = false;
        ex->upstream_fd = -1;
        ex->client_fd = -1;
        ex->timer_id = 0;
        ex->client_len = msg.msg_namelen;
        memcpy(&ex->client, &client, sizeof(client));
        ex->req = malloc((size_t)n);
        if (ex->req == NULL) {
            free(ex);
            continue;
        }
        memcpy(ex->req, buf, (size_t)n);
        ex->req_len = (size_t)n;

        for (struct cmsghdr *cm = CMSG_FIRSTHDR(&msg); cm != NULL;
             cm = CMSG_NXTHDR(&msg, cm)) {
            if (firc_pktinfo_read(cm, &ex->dst)) { ex->have_dst = true; }
        }

        atomic_fetch_add(&p->inflight, 1);
        ex->counted = true;
        if (handle_request_common(p, ex) != REQ_FORWARD) {
            atomic_fetch_sub(&p->inflight, 1);
            exchange_free(ex);
            continue;
        }
        exchange_begin(p, ex);
    }
}

static void on_tcp_read_deadline(firc_loop_t *loop, void *ud)
{
    (void)loop;
    exchange_t *ex = ud;
    ex->timer_id = 0; /* already fired */
    exchange_finish(ex, false);
}

static void on_tcp_client_read(firc_loop_t *loop, int fd, uint32_t events,
                               void *ud)
{
    (void)loop;
    exchange_t *ex = ud;

    if (events & (EPOLLERR | EPOLLHUP)) {
        exchange_finish(ex, false);
        return;
    }

    while (ex->len_got < 2) {
        ssize_t n = recv(fd, ex->len_buf + ex->len_got, 2 - ex->len_got, 0);
        if (n <= 0) {
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                return;
            }
            exchange_finish(ex, false);
            return;
        }
        ex->len_got += (size_t)n;
    }
    if (ex->req == NULL) {
        size_t want = (size_t)ex->len_buf[0] << 8 | (size_t)ex->len_buf[1];
        if (want == 0 || want > TCP_MAX_MSG) {
            exchange_finish(ex, false);
            return;
        }
        ex->req = malloc(want);
        if (ex->req == NULL) {
            exchange_finish(ex, false);
            return;
        }
        ex->req_len = want;
        ex->req_got = 0;
    }
    while (ex->req_got < ex->req_len) {
        ssize_t n = recv(fd, ex->req + ex->req_got, ex->req_len - ex->req_got,
                         0);
        if (n <= 0) {
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                return;
            }
            exchange_finish(ex, false);
            return;
        }
        ex->req_got += (size_t)n;
    }

    /* Drop the read deadline first: later code stores its timer in timer_id unchecked. */
    if (ex->timer_id > 0) {
        firc_loop_del_timer(ex->p->loop, ex->timer_id);
        ex->timer_id = 0;
    }
    firc_loop_del_fd(ex->p->loop, ex->client_fd);
    if (firc_loop_add_fd(ex->p->loop, ex->client_fd, 0, on_client_writable,
                       ex) != FIRC_OK) {
        exchange_finish(ex, false);
        return;
    }

    request_disp_t disp = handle_request_common(ex->p, ex);
    if (disp == REQ_DROP) {
        exchange_finish(ex, false);
        return;
    }
    if (disp == REQ_REPLIED_ASYNC) {
        return;
    }
    exchange_begin(ex->p, ex);
}

static void on_tcp_accept(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    firc_dnsproxy_t *p = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        return;
    }
    for (;;) {
        struct sockaddr_storage peer;
        socklen_t peer_len = sizeof(peer);
        int cfd = accept4(fd, (struct sockaddr *)&peer, &peer_len, SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (cfd < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) {
                return;
            }
            return;
        }
        if (atomic_load(&p->inflight) >= p->cfg.max_concurrent) {
            note_drop(p);
            close(cfd);
            continue;
        }
        exchange_t *ex = calloc(1, sizeof(*ex));
        if (ex == NULL) {
            close(cfd);
            continue;
        }
        ex->p = p;
        ex->is_tcp = true;
        ex->upstream_fd = -1;
        ex->client_fd = cfd;
        memcpy(&ex->client, &peer, peer_len <= sizeof(ex->client) ? peer_len : sizeof(ex->client));
        ex->client_len = peer_len;
        atomic_fetch_add(&p->inflight, 1);
        ex->counted = true;
        if (firc_loop_add_fd(p->loop, cfd, EPOLLIN, on_tcp_client_read, ex) !=
            FIRC_OK) {
            atomic_fetch_sub(&p->inflight, 1);
            close(cfd);
            free(ex);
            continue;
        }
        if (firc_loop_add_timer(p->loop, p->cfg.timeout_ms, 0, on_tcp_read_deadline, ex, &ex->timer_id) !=
            FIRC_OK) {
            exchange_finish(ex, false);
        }
    }
}

firc_err_t firc_dnsproxy_create(const firc_dnsproxy_config_t *cfg, firc_loop_t *loop,
                            firc_dnsproxy_msg_cb cb, void *cb_ud,
                            firc_dnsproxy_t **out)
{
    firc_dnsproxy_t *p = calloc(1, sizeof(*p));
    if (p == NULL) {
        return FIRC_ERR_NOMEM;
    }
    p->cfg = *cfg;
    p->ops = firc_dnsproxy_sock_ops_real;
    p->health_fails = FIRC_RESOLVE_HEALTH_FAILS;
    p->health_rest_ms = FIRC_RESOLVE_HEALTH_SKIP_MS;
    p->loop = loop;
    p->cb = cb;
    p->cb_ud = cb_ud;
    p->udp_fd = -1;
    p->tcp_fd = -1;

    /* Own copy: a settings PUT replaces the config's string. */
    p->upstream_addr = strdup(cfg->upstream_addr != NULL ? cfg->upstream_addr : "0.0.0.0");
    if (p->upstream_addr == NULL) {
        free(p);
        return FIRC_ERR_NOMEM;
    }
    p->cfg.upstream_addr = p->upstream_addr;
    p->cache_now = mono_ms;
    p->health_clock = mono_ms;
    p->cache = firc_anscache_new(FIRC_ANSCACHE_ENTRIES, FIRC_ANSCACHE_BYTES);

    int fam_listen;
    struct sockaddr_storage listen_sa;
    socklen_t listen_len;
    if (parse_listen(cfg->listen_addr, cfg->listen_port, &fam_listen,
                     &listen_sa, &listen_len) != 0) {
        free(p->upstream_addr);
        firc_anscache_free(p->cache);
        free(p);
        return FIRC_ERR_INVAL;
    }
    p->family = fam_listen;

    int fam_up;
    if (parse_listen(cfg->upstream_addr, cfg->upstream_port, &fam_up,
                     &p->upstream_sa, &p->upstream_sa_len) != 0) {
        free(p->upstream_addr);
        firc_anscache_free(p->cache);
        free(p);
        return FIRC_ERR_INVAL;
    }
    p->family = fam_up;

    pool_init(&p->udp_pool, cfg->max_idle_conns);
    *out = p;
    return FIRC_OK;
}

const char *firc_dnsproxy_listen_error(const firc_dnsproxy_t *p)
{
    return p->listen_error;
}

static void listen_failed(firc_dnsproxy_t *p, int type, int err)
{
    const firc_listen_seen_t *seen = &p->seen[type == SOCK_DGRAM ? 0 : 1];
    p->listen.type = type;
    p->listen.port = p->cfg.listen_port;
    p->listen.busy = err == EADDRINUSE;
    p->listen.held = p->listen.busy && seen->held;
    if (p->listen.held) { p->listen.holder = seen->holder; }
}

firc_err_t firc_dnsproxy_start(firc_dnsproxy_t *p)
{
    struct sockaddr_storage listen_sa;
    socklen_t listen_len;
    int fam;
    if (parse_listen(p->cfg.listen_addr, p->cfg.listen_port, &fam, &listen_sa,
                     &listen_len) != 0) {
        return FIRC_ERR_INVAL;
    }

    p->listen_error[0] = '\0';
    memset(&p->listen, 0, sizeof(p->listen));
    int ufd = firc_listen_open_seen("DNS proxy", SOCK_DGRAM, &listen_sa, listen_len, FIRC_DNS_UDP_LISTEN,
                                    p->listen_error, sizeof(p->listen_error), &p->seen[0]);
    if (ufd < 0) {
        listen_failed(p, SOCK_DGRAM, -ufd);
        return firc_err_from_errno(-ufd);
    }
    p->udp_fd = ufd;

    int tfd = firc_listen_open_seen("DNS proxy", SOCK_STREAM, &listen_sa, listen_len, FIRC_DNS_TCP_LISTEN,
                                    p->listen_error, sizeof(p->listen_error), &p->seen[1]);
    if (tfd < 0) {
        close(ufd);
        p->udp_fd = -1;
        listen_failed(p, SOCK_STREAM, -tfd);
        return firc_err_from_errno(-tfd);
    }
    p->tcp_fd = tfd;

    firc_err_t err = firc_loop_add_fd(p->loop, ufd, EPOLLIN, on_udp_readable, p);
    if (err == FIRC_OK) {
        err = firc_loop_add_fd(p->loop, tfd, EPOLLIN, on_tcp_accept, p);
        if (err != FIRC_OK) { (void)firc_loop_del_fd(p->loop, ufd); }
    }
    if (err != FIRC_OK) {
        close(ufd);
        close(tfd);
        p->udp_fd = -1;
        p->tcp_fd = -1;
        return err;
    }
    char uname[96], tname[96];
    firc_listen_name(ufd, SOCK_DGRAM, uname, sizeof(uname));
    firc_listen_name(tfd, SOCK_STREAM, tname, sizeof(tname));
    FIRC_INFO("DNS proxy listening on %s and %s, upstream %s:%u", uname, tname, p->cfg.upstream_addr,
              p->cfg.upstream_port);
    p->listen.listening = true;
    return FIRC_OK;
}

void firc_dnsproxy_listen_state(const firc_dnsproxy_t *p, firc_dnsproxy_listen_t *out)
{
    *out = p->listen;
}

void firc_dnsproxy_destroy(firc_dnsproxy_t *p)
{
    if (p == NULL) {
        return;
    }
    /* Flush held answers rather than drop them: a fake address beats no answer. */
    while (p->held_head != NULL) {
        exchange_t *ex = p->held_head;
        held_unlink(p, ex);
        if (ex->timer_id > 0) {
            firc_loop_del_timer(p->loop, ex->timer_id);
            ex->timer_id = 0;
        }
        if (ex->is_tcp) {
            uint8_t hdr[2] = {(uint8_t)(ex->held_len >> 8), (uint8_t)ex->held_len};
            if (send(ex->client_fd, hdr, 2, MSG_NOSIGNAL | MSG_DONTWAIT) == 2) {
                (void)send(ex->client_fd, ex->held, ex->held_len, MSG_NOSIGNAL | MSG_DONTWAIT);
            }
        } else {
            udp_reply(p, ex, ex->held, ex->held_len);
        }
        if (ex->client_fd >= 0) {
            firc_loop_del_fd(p->loop, ex->client_fd);
            close(ex->client_fd);
            ex->client_fd = -1;
        }
        exchange_free(ex);
    }
    while (p->prefetch_head != NULL) {
        exchange_t *ex = p->prefetch_head;
        p->prefetch_head = ex->prefetch_next;
        if (ex->timer_id > 0) { firc_loop_del_timer(p->loop, ex->timer_id); }
        legs_release(p, ex, false);
        exchange_free(ex);
    }
    p->n_prefetch = 0;
    if (p->udp_fd >= 0) {
        firc_loop_del_fd(p->loop, p->udp_fd);
        close(p->udp_fd);
    }
    if (p->tcp_fd >= 0) {
        firc_loop_del_fd(p->loop, p->tcp_fd);
        close(p->tcp_fd);
    }
    pool_clear(&p->udp_pool);
    for (size_t i = 0; i < p->n_tpools; i++) { pool_clear(&p->tpools[i].pool); }
    free(p->tpools);
    free(p->upstream_addr);
    free(p->health);
    firc_anscache_free(p->cache);
    free(p);
}

uint64_t firc_dnsproxy_dropped(const firc_dnsproxy_t *p)
{
    return atomic_load(&p->dropped);
}

uint64_t firc_dnsproxy_inflight(const firc_dnsproxy_t *p)
{
    return atomic_load(&p->inflight);
}

void firc_dnsproxy_set_disable_drop_aaaa(firc_dnsproxy_t *p, bool disable_drop_aaaa)
{
    p->cfg.disable_drop_aaaa = disable_drop_aaaa;
}

void firc_dnsproxy_set_pool(firc_dnsproxy_t *p, const firc_fakeip_t *pool, uint32_t ttl)
{
    p->fakeip = pool;
    p->ptr_ttl = ttl;
}

firc_err_t firc_dnsproxy_set_upstream(firc_dnsproxy_t *p, const char *addr, uint16_t port)
{
    if (addr == NULL) { return FIRC_ERR_INVAL; }
    int fam;
    struct sockaddr_storage sa;
    socklen_t sa_len;
    if (parse_listen(addr, port, &fam, &sa, &sa_len) != 0) { return FIRC_ERR_INVAL; }
    char *copy = strdup(addr);
    if (copy == NULL) { return FIRC_ERR_NOMEM; }
    free(p->upstream_addr);
    p->upstream_addr = copy;
    p->cfg.upstream_addr = copy;
    p->cfg.upstream_port = port;
    p->upstream_sa = sa;
    p->upstream_sa_len = sa_len;
    p->family = fam;
    p->upstream_gen++;
    /* Idle sockets are connected to the old server. */
    for (size_t i = 0; i < p->udp_pool.len; i++) { close(p->udp_pool.fds[i]); }
    p->udp_pool.len = 0;
    FIRC_INFO("DNS proxy upstream is now %s:%u", copy, port);
    return FIRC_OK;
}

const char *firc_dnsproxy_upstream(const firc_dnsproxy_t *p, uint16_t *port_out)
{
    if (port_out != NULL) { *port_out = p->cfg.upstream_port; }
    return p->cfg.upstream_addr;
}

void firc_dnsproxy_set_cache(firc_dnsproxy_t *p, size_t max_entries, size_t max_bytes)
{
    firc_anscache_free(p->cache);
    p->cache = firc_anscache_new(max_entries, max_bytes);
}

void firc_dnsproxy_set_cache_clock_for_test(firc_dnsproxy_t *p, uint64_t (*now_ms)(void))
{
    p->cache_now = now_ms != NULL ? now_ms : mono_ms;
}

void firc_dnsproxy_set_health_clock_for_test(firc_dnsproxy_t *p, uint64_t (*now_ms)(void))
{
    p->health_clock = now_ms != NULL ? now_ms : mono_ms;
}

void firc_dnsproxy_set_group_health_for_test(firc_dnsproxy_t *p, firc_id_t id, uint64_t gen,
                                             const firc_dnsproxy_health_t *h)
{
    p->health_planted = h != NULL;
    p->planted_id = id;
    p->planted_gen = gen;
    if (h != NULL) { p->planted = *h; }
}

size_t firc_dnsproxy_prefetching(const firc_dnsproxy_t *p)
{
    return p == NULL ? 0 : p->n_prefetch;
}
