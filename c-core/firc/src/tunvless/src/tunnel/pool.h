/* Node pool: several nodes active at once, new connections spread over them, each one health
 * checked and replaced from the candidates when it dies. Details in pool.c. */
#ifndef TUNVLESS_POOL_H
#define TUNVLESS_POOL_H
#include <stddef.h>
#include "dialer.h"
#include "stack.h"

/* How new connections are spread over the active nodes. A connection stays on its node until it
 * ends. */
enum pool_by {
    POOL_BY_CONNECTION,     /* each new connection: a random live node */
    POOL_BY_SITE,           /* by destination address: a site stays on one node */
    POOL_BY_SITE_CLIENT,    /* by client and destination address */
};

/* "connection", "site", "site-client" (or "site_client"): the enum value, or -1. */
int pool_by_parse(const char *s);
const char *pool_by_name(int by);

/* What the pool needs from the protocol. The dialer's ctx is a node. */
struct pool_proto {
    const struct dialer_ops *ops;
    const struct dialer_ops *alt;
    unsigned (*kind)(const void *node);
    /* The same check that chose the node at startup: 0 — alive, otherwise why says what failed. */
    int (*probe)(const void *node, int timeout_s, char *why, size_t why_n);
    int (*probe6)(const void *node, int timeout_s, char *why, size_t why_n);
    int (*reach)(const void *const *nodes, size_t n, int timeout_ms, char *ok, int *err);
    const char *(*name)(const void *node);
};

#define POOL_INTERVAL_S 60
#define POOL_SILENCE_S  20
#define POOL_WALK       8
#define POOL_WALK_DOWN  16
#define POOL_REACH_MS   3000

struct pool_cfg {
    const struct pool_proto *proto;
    const void *(*node)(int i);
    const int *sel;         /* candidates (node indexes) in order of preference */
    size_t sel_n;
    int first;              /* the node chosen at startup */
    int checked;            /* first was probed at startup; otherwise it is probed at once */
    int active;             /* how many nodes to keep active, at least 1 */
    int by;                 /* enum pool_by */
    int interval_s;         /* health check period of each active node */
    int silence_s;          /* stall threshold on live connections (struct dialer), 0 — none */
    int (*v6_changed)(int on);   /* nonzero — not applied, called again at the next publish while on */
};

/* Run the tunnel tc over the pool: the stack with the pool's dialer, health checks once the device
 * is up, then ready (may be NULL). Returns the process exit code, as stack_run does. */
int pool_run(const struct tun_cfg *tc, const struct pool_cfg *pc, stack_ready_fn ready, void *arg);

int pool_select(const int *sel, size_t n);

int pool_wake(const int *idx, size_t n);

size_t pool_order(const int *sel, size_t n, int *out, size_t k);

void pool_note(int c, int ok);

int pool_flow_open_on(void *sess, int node, const struct flow_key *k, int udp, const char *host);

int pool_ref_stale(const struct xc_ref *r);

int pool_reach_some(const struct pool_proto *p, const void *const *nodes, size_t n, int timeout_ms, char *ok, int *err);

#endif
