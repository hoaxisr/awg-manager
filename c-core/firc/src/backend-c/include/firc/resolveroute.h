#ifndef FIRC_RESOLVEROUTE_H
#define FIRC_RESOLVEROUTE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/dnspipeline.h"
#include "firc/dnsproxy.h"
#include "firc/err.h"
#include "firc/id.h"
#include "firc/models.h"
#include "firc/resolver_addr.h"

typedef enum firc_resolve_source {
    FIRC_RESOLVE_SOURCE_OFF = 0,
    FIRC_RESOLVE_SOURCE_NONE,
    FIRC_RESOLVE_SOURCE_GROUP,
    FIRC_RESOLVE_SOURCE_FIRMWARE,
} firc_resolve_source_t;

const char *firc_resolve_source_name(firc_resolve_source_t s);

typedef struct firc_resolve_route {
    firc_id_t group_id;
    char group_name[64];
    char iface[16];
    bool blackhole;
    uint32_t mark; /* 0: not in the kernel */
    bool v6;
    bool ktcp;
    firc_resolve_source_t source;
    size_t n_servers;
    firc_resolver_addr_t servers[FIRC_RESOLVE_MAX_SERVERS];
    uint64_t gen;
} firc_resolve_route_t;

bool firc_resolve_route_usable(const firc_resolve_route_t *r);

typedef struct firc_resolve_input {
    const firc_group_t *group;
    const char *iface;
    uint32_t mark;
    bool blackhole;
    bool v6_route;
    bool ktcp;
    const firc_resolver_addr_t *firmware;
    size_t n_firmware;
} firc_resolve_input_t;

typedef struct firc_resolve_table {
    firc_resolve_route_t *items;
    size_t n;
    uint64_t last_gen;
} firc_resolve_table_t;

/* `prev` (nullable): an unchanged route keeps its gen, others get last_gen + 1. */
firc_err_t firc_resolve_table_build(const firc_resolve_input_t *in, size_t n, const firc_resolve_table_t *prev,
                                    firc_resolve_table_t *out);
void firc_resolve_table_free(firc_resolve_table_t *t);
const firc_resolve_route_t *firc_resolve_table_find(const firc_resolve_table_t *t, firc_id_t id);

/* Groups of `prev` (nullable) missing from `now` or under another gen. At most `cap`. */
size_t firc_resolve_table_stale(const firc_resolve_table_t *prev, const firc_resolve_table_t *now, firc_id_t *out,
                                size_t cap);

/* Groups of `prev` missing from `now`. */
size_t firc_resolve_table_gone(const firc_resolve_table_t *prev, const firc_resolve_table_t *now, firc_id_t *out,
                               size_t cap);

/* The log sentence for the route. strlen, 0 if it did not fit. */
size_t firc_resolve_route_describe(const firc_resolve_route_t *r, char *buf, size_t cap);

typedef struct firc_resolve_router firc_resolve_router_t;

firc_resolve_router_t *firc_resolve_router_new(firc_dns_pipeline_t *pipeline);
void firc_resolve_router_free(firc_resolve_router_t *r);
/* Takes t->items and zeroes *t. Loop thread. */
void firc_resolve_router_publish(firc_resolve_router_t *r, firc_resolve_table_t *t);
const firc_resolve_table_t *firc_resolve_router_table(const firc_resolve_router_t *r);
bool firc_resolve_router_route(const firc_resolve_router_t *r, firc_id_t id, firc_resolve_route_t *out);

/* A firc_dns_v6_fn; `ud` is the router. False for a group the table lacks. Loop thread. */
bool firc_resolve_router_carries_v6(void *ud, firc_id_t group_id);

/* A firc_dns_cross_fn; `ud` is the router. True when the route carries v6 and is kernel-TCP redirected. Loop thread. */
bool firc_resolve_router_cross_v6(void *ud, firc_id_t group_id);

/* A firc_dnsproxy_route_fn; `ud` is the router. Loop thread. */
bool firc_resolve_router_decide(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                                firc_dnsproxy_route_t *out);

#endif
