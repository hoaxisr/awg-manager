#ifndef FIRC_DNSPROXY_H
#define FIRC_DNSPROXY_H

#include <stdbool.h>
#include <stdint.h>
#include <sys/socket.h>
#include <sys/types.h>

#include "firc/dnswire.h"
#include "firc/err.h"
#include "firc/listen.h"
#include "firc/events.h"
#include "firc/fakeip.h"
#include "firc/fakeip_addr.h"
#include "firc/id.h"
#include "firc/loop.h"
#include "firc/resolver_addr.h"

typedef struct firc_dnsproxy firc_dnsproxy_t;

/* Under the shortest client resolver timeout (~1 s). */
#define FIRC_DNSPROXY_HOLD_MS 800u

typedef struct firc_dnsproxy_config {
    const char *listen_addr;
    uint16_t listen_port;
    const char *upstream_addr;
    uint16_t upstream_port;
    uint32_t timeout_ms;
    uint32_t max_concurrent;
    uint32_t max_idle_conns;
    bool disable_drop_aaaa;
    uint32_t hold_ms;
    uint32_t max_held; /* 0 = max_concurrent */
} firc_dnsproxy_config_t;

/* May modify msg, not keep or free it; PASS sends the original bytes regardless. */
typedef firc_dns_verdict_t (*firc_dnsproxy_msg_cb)(firc_dns_msg_t *msg, const firc_ip_t *client,
                                                   const char *network, firc_dns_resolver_t resolver,
                                                   void *ud);

/* Answers a_msg, the A answer the callback asked AAAA for, from that AAAA answer (NULL: none came); PASS sends a_msg. */
typedef firc_dns_verdict_t (*firc_dnsproxy_cross_cb)(firc_dns_msg_t *a_msg, const firc_dns_msg_t *aaaa_msg,
                                                     const firc_ip_t *client, firc_dns_resolver_t resolver, void *ud);

/* NULL asks no AAAA: FIRC_DNS_ASK_AAAA is then PASS. Loop thread, or before start. */
void firc_dnsproxy_set_cross(firc_dnsproxy_t *p, firc_dnsproxy_cross_cb fn, void *ud);

/* Every upstream socket goes through these; each returns what its syscall does. */
typedef struct firc_dnsproxy_sock_ops {
    int (*open)(void *ud, int family, int type);
    int (*set_mark)(void *ud, int fd, uint32_t mark);
    int (*connect)(void *ud, int fd, const struct sockaddr *sa, socklen_t len);
    ssize_t (*send)(void *ud, int fd, const void *buf, size_t len, int flags);
    void *ud;
} firc_dnsproxy_sock_ops_t;

extern const firc_dnsproxy_sock_ops_t firc_dnsproxy_sock_ops_real;

/* Copies *ops; NULL restores the real ones. Loop thread, or before start. */
void firc_dnsproxy_set_sock_ops(firc_dnsproxy_t *p, const firc_dnsproxy_sock_ops_t *ops);

#define FIRC_DNSPROXY_TUNNEL_TIMEOUT_MS 1000u
#define FIRC_DNSPROXY_FALLBACK_FLOOR_MS 250u
#define FIRC_DNSPROXY_RETRY_MS 300u
#define FIRC_DNSPROXY_MAX_PREFETCH 16u

#define FIRC_RESOLVE_HEALTH_FAILS 3u
#define FIRC_RESOLVE_HEALTH_SKIP_MS 30000u

/* 0 keeps the default for either. */
void firc_dnsproxy_set_health(firc_dnsproxy_t *p, unsigned fails, uint32_t rest_ms);

#define FIRC_DNSPROXY_HEALTH_WINDOW_S 600u

typedef struct firc_dnsproxy_health {
    uint64_t total, fallbacks;
    uint64_t last_ago_ms;
    bool has_last;
    uint8_t reason;
    bool resting;
    uint64_t since_ago_ms;
} firc_dnsproxy_health_t;

/* The group's last FIRC_DNSPROXY_HEALTH_WINDOW_S; last_ago_ms and reason (a firc_dns_resolver_t) hold
 * when has_last, since_ago_ms when resting on `gen`. Zeros for an unknown group. Loop thread. */
void firc_dnsproxy_group_health(const firc_dnsproxy_t *p, firc_id_t group_id, uint64_t gen,
                                firc_dnsproxy_health_t *out);

/* Tests only: the clock of the health gate and window. NULL restores the monotonic one. */
void firc_dnsproxy_set_health_clock_for_test(firc_dnsproxy_t *p, uint64_t (*now_ms)(void));

/* Tests only: the health getter answers *h for (id, gen) until replaced; NULL clears. */
void firc_dnsproxy_set_group_health_for_test(firc_dnsproxy_t *p, firc_id_t id, uint64_t gen,
                                             const firc_dnsproxy_health_t *h);

/* Closes idle tunnel sockets now and in-flight ones on return; health stays. Loop thread. */
void firc_dnsproxy_close_group_pools(firc_dnsproxy_t *p, firc_id_t group_id);

/* Closes the group's pools and drops its health record. Loop thread. */
void firc_dnsproxy_forget_group(firc_dnsproxy_t *p, firc_id_t group_id);

/* Copied by value: the route table it came from is freed on the next publish. */
typedef struct firc_dnsproxy_route {
    firc_id_t group_id;
    char group_name[64];
    uint32_t mark;
    uint64_t gen;
    size_t n_servers;
    struct sockaddr_storage servers[FIRC_RESOLVE_MAX_SERVERS];
    socklen_t server_lens[FIRC_RESOLVE_MAX_SERVERS];
    bool blocked; /* the proxy answers with `answer` itself */
    firc_block_answer_t answer;
    firc_id_t owner;
} firc_dnsproxy_route_t;

/* True, with *out filled, when the query goes through a group's tunnel. */
typedef bool (*firc_dnsproxy_route_fn)(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                                       firc_dnsproxy_route_t *out);

/* NULL sends everything to the common upstream. Loop thread, or before start. */
void firc_dnsproxy_set_router(firc_dnsproxy_t *p, firc_dnsproxy_route_fn fn, void *ud);

/* Told of every query the proxy answered with a block answer, before the reply. */
typedef void (*firc_dnsproxy_blocked_cb)(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                                         firc_block_answer_t answer);

/* NULL tells no one. Loop thread, or before start. */
void firc_dnsproxy_set_blocked(firc_dnsproxy_t *p, firc_dnsproxy_blocked_cb fn, void *ud);

/* Told once of every query the proxy parsed and answers, after the owner decision; owner is zero when none.
 * Never for a query it dropped or could not parse, nor for a cache refresh. */
typedef void (*firc_dnsproxy_tally_fn)(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                                       firc_id_t owner, bool blocked);

/* NULL tells no one. Loop thread, or before start. */
void firc_dnsproxy_set_tally(firc_dnsproxy_t *p, firc_dnsproxy_tally_fn fn, void *ud);

/* A tunnel answer inside these is a sink. Copied; NULL = none. Before start. */
void firc_dnsproxy_set_pool_prefixes(firc_dnsproxy_t *p, const firc_ip_t *v4, uint8_t v4_len,
                                     const firc_ip_t *v6, uint8_t v6_len);

void firc_dnsproxy_set_pool(firc_dnsproxy_t *p, const firc_fakeip_t *pool, uint32_t ttl);

/* 0 for either bound turns the group resolver answer cache off. Loop thread, or before start. */
void firc_dnsproxy_set_cache(firc_dnsproxy_t *p, size_t max_entries, size_t max_bytes);

/* Tests only: the clock the cache ages entries by. NULL restores the monotonic one. */
void firc_dnsproxy_set_cache_clock_for_test(firc_dnsproxy_t *p, uint64_t (*now_ms)(void));

/* Sends every held answer `ready` approves of. Loop thread only (firc_loop_post). */
typedef bool (*firc_dnsproxy_ready_fn)(const firc_dns_msg_t *msg, void *ud);
void firc_dnsproxy_release(firc_dnsproxy_t *p, firc_dnsproxy_ready_fn ready, void *ud);

size_t firc_dnsproxy_held(const firc_dnsproxy_t *p);

/* Cache refreshes in flight. Loop thread. */
size_t firc_dnsproxy_prefetching(const firc_dnsproxy_t *p);

firc_err_t firc_dnsproxy_create(const firc_dnsproxy_config_t *cfg, firc_loop_t *loop,
                            firc_dnsproxy_msg_cb cb, void *cb_ud,
                            firc_dnsproxy_t **out);
void firc_dnsproxy_destroy(firc_dnsproxy_t *p);

firc_err_t firc_dnsproxy_start(firc_dnsproxy_t *p);

/* Why the last start could not open its sockets, as firc_listen_describe words it; "" otherwise. */
const char *firc_dnsproxy_listen_error(const firc_dnsproxy_t *p);

typedef struct firc_dnsproxy_listen {
    bool listening;
    bool busy;
    int type;
    uint16_t port;
    bool held;
    firc_listen_holder_t holder;
} firc_dnsproxy_listen_t;

/* Whether the listeners are open, and what the last failed start met; loop thread. */
void firc_dnsproxy_listen_state(const firc_dnsproxy_t *p, firc_dnsproxy_listen_t *out);

uint64_t firc_dnsproxy_dropped(const firc_dnsproxy_t *p);
uint64_t firc_dnsproxy_inflight(const firc_dnsproxy_t *p);

/* Loop thread. */
void firc_dnsproxy_set_disable_drop_aaaa(firc_dnsproxy_t *p, bool disable_drop_aaaa);

/* Copies addr; the proxy is unchanged on error. Loop thread only. */
firc_err_t firc_dnsproxy_set_upstream(firc_dnsproxy_t *p, const char *addr, uint16_t port);

/* `port_out` may be NULL. */
const char *firc_dnsproxy_upstream(const firc_dnsproxy_t *p, uint16_t *port_out);

#endif /* FIRC_DNSPROXY_H */
