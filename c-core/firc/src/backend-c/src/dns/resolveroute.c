#include "firc/resolveroute.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/log.h"
#include "firc/models.h"
#include "firc/rulesnap.h"
#include "firc/utf8.h"

struct firc_resolve_router {
    firc_dns_pipeline_t *pipeline;
    firc_resolve_table_t table;
};

const char *firc_resolve_source_name(firc_resolve_source_t s)
{
    switch (s) {
    case FIRC_RESOLVE_SOURCE_OFF: return "off";
    case FIRC_RESOLVE_SOURCE_NONE: return "none";
    case FIRC_RESOLVE_SOURCE_GROUP: return "group";
    case FIRC_RESOLVE_SOURCE_FIRMWARE: return "firmware";
    }
    return "none";
}

bool firc_resolve_route_usable(const firc_resolve_route_t *r)
{
    return r != NULL && r->mark != 0 && r->n_servers > 0;
}

static bool addr_equal(const firc_resolver_addr_t *a, const firc_resolver_addr_t *b)
{
    return a->port == b->port && a->ip.len == b->ip.len && memcmp(a->ip.b, b->ip.b, a->ip.len) == 0;
}

static bool same_wiring(const firc_resolve_route_t *a, const firc_resolve_route_t *b)
{
    if (a->mark != b->mark || strcmp(a->iface, b->iface) != 0 || a->n_servers != b->n_servers) { return false; }
    for (size_t i = 0; i < a->n_servers; i++) {
        if (!addr_equal(&a->servers[i], &b->servers[i])) { return false; }
    }
    return true;
}

static void add_family(firc_resolve_route_t *r, const firc_resolver_addr_t *src, size_t n, uint8_t len)
{
    for (size_t i = 0; i < n && r->n_servers < FIRC_RESOLVE_MAX_SERVERS; i++) {
        if (src[i].ip.len == len) { r->servers[r->n_servers++] = src[i]; }
    }
}

static void route_fill(const firc_resolve_input_t *in, firc_resolve_route_t *r)
{
    memset(r, 0, sizeof(*r));
    const firc_group_t *g = in->group;
    r->group_id = g->id;
    const char *gn = g->name != NULL ? g->name : "";
    snprintf(r->group_name, sizeof(r->group_name), "%.*s",
             firc_utf8_clamp(gn, (int)sizeof(r->group_name) - 1), gn);
    snprintf(r->iface, sizeof(r->iface), "%s", in->iface != NULL ? in->iface : (g->iface != NULL ? g->iface : ""));
    r->blackhole = in->blackhole;
    r->mark = in->mark;
    r->v6 = in->v6_route;
    r->ktcp = in->ktcp;
    if (!g->resolve.tunnel) {
        r->source = FIRC_RESOLVE_SOURCE_OFF;
        return;
    }
    if (in->blackhole) {
        r->source = FIRC_RESOLVE_SOURCE_NONE;
        return;
    }
    firc_resolver_list_t own;
    const firc_resolver_addr_t *src = NULL;
    size_t n = 0;
    if (g->resolve.server != NULL && g->resolve.server[0] != '\0' &&
        firc_resolver_list_parse(g->resolve.server, &own, NULL, 0) == FIRC_RESOLVER_LIST_OK) {
        r->source = FIRC_RESOLVE_SOURCE_GROUP;
        src = own.servers;
        n = own.n;
    } else if (in->n_firmware > 0) {
        r->source = FIRC_RESOLVE_SOURCE_FIRMWARE;
        src = in->firmware;
        n = in->n_firmware;
    } else {
        r->source = FIRC_RESOLVE_SOURCE_NONE;
        return;
    }
    add_family(r, src, n, 4);
    if (in->v6_route) { add_family(r, src, n, 16); }
}

firc_err_t firc_resolve_table_build(const firc_resolve_input_t *in, size_t n, const firc_resolve_table_t *prev,
                                    firc_resolve_table_t *out)
{
    memset(out, 0, sizeof(*out));
    out->last_gen = prev != NULL ? prev->last_gen : 0;
    if (n == 0) { return FIRC_OK; }
    out->items = calloc(n, sizeof(*out->items));
    if (out->items == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n; i++) {
        firc_resolve_route_t *r = &out->items[i];
        route_fill(&in[i], r);
        const firc_resolve_route_t *was = firc_resolve_table_find(prev, r->group_id);
        r->gen = (was != NULL && same_wiring(was, r)) ? was->gen : ++out->last_gen;
    }
    out->n = n;
    return FIRC_OK;
}

void firc_resolve_table_free(firc_resolve_table_t *t)
{
    if (t == NULL) { return; }
    free(t->items);
    t->items = NULL;
    t->n = 0;
}

size_t firc_resolve_table_stale(const firc_resolve_table_t *prev, const firc_resolve_table_t *now, firc_id_t *out,
                                size_t cap)
{
    size_t n = 0;
    for (size_t i = 0; prev != NULL && i < prev->n && n < cap; i++) {
        const firc_resolve_route_t *was = &prev->items[i];
        const firc_resolve_route_t *is = firc_resolve_table_find(now, was->group_id);
        if (is == NULL || is->gen != was->gen) { out[n++] = was->group_id; }
    }
    return n;
}

size_t firc_resolve_table_gone(const firc_resolve_table_t *prev, const firc_resolve_table_t *now, firc_id_t *out,
                               size_t cap)
{
    size_t n = 0;
    for (size_t i = 0; prev != NULL && i < prev->n && n < cap; i++) {
        if (firc_resolve_table_find(now, prev->items[i].group_id) == NULL) { out[n++] = prev->items[i].group_id; }
    }
    return n;
}

const firc_resolve_route_t *firc_resolve_table_find(const firc_resolve_table_t *t, firc_id_t id)
{
    if (t == NULL) { return NULL; }
    for (size_t i = 0; i < t->n; i++) {
        if (firc_id_equal(t->items[i].group_id, id)) { return &t->items[i]; }
    }
    return NULL;
}

static size_t servers_text(const firc_resolve_route_t *r, char *buf, size_t cap)
{
    size_t off = 0;
    buf[0] = '\0';
    for (size_t i = 0; i < r->n_servers; i++) {
        char one[FIRC_RESOLVER_ADDR_STRLEN];
        if (firc_resolver_addr_format(&r->servers[i], one, sizeof(one)) == 0) { continue; }
        int w = snprintf(buf + off, cap - off, "%s%s", off ? ", " : "", one);
        if (w < 0 || (size_t)w >= cap - off) { break; }
        off += (size_t)w;
    }
    return off;
}

size_t firc_resolve_route_describe(const firc_resolve_route_t *r, char *buf, size_t cap)
{
    char servers[FIRC_RESOLVE_MAX_SERVERS * (FIRC_RESOLVER_ADDR_STRLEN + 2)];
    servers_text(r, servers, sizeof(servers));
    char from[48];
    if (r->source == FIRC_RESOLVE_SOURCE_GROUP) {
        snprintf(from, sizeof(from), "resolve.server");
    } else {
        snprintf(from, sizeof(from), "the firmware's for %s", r->iface);
    }
    int w;
    if (r->source == FIRC_RESOLVE_SOURCE_OFF) {
        w = snprintf(buf, cap, "uses the common upstream: resolving through the tunnel is off");
    } else if (r->blackhole && strcmp(r->iface, FIRC_IFACE_BLOCK) == 0) {
        w = snprintf(buf, cap, "uses the common upstream: its names are answered by firc");
    } else if (r->blackhole) {
        w = snprintf(buf, cap, "uses the common upstream: a blackhole group has no tunnel");
    } else if (r->source == FIRC_RESOLVE_SOURCE_NONE) {
        w = snprintf(buf, cap, "uses the common upstream: the firmware binds no resolver to %s", r->iface);
    } else if (r->n_servers == 0) {
        if (r->source == FIRC_RESOLVE_SOURCE_GROUP) {
            w = snprintf(buf, cap,
                         "uses the common upstream: resolve.server names only IPv6 resolvers and %s has no IPv6 route",
                         r->iface);
        } else {
            w = snprintf(buf, cap, "uses the common upstream: its resolvers for %s are IPv6 and %s has no IPv6 route",
                         r->iface, r->iface);
        }
    } else if (r->mark == 0) {
        w = snprintf(buf, cap, "uses the common upstream until %s is routed (%s, %s)", r->iface, servers, from);
    } else {
        w = snprintf(buf, cap, "resolves through %s: %s (%s)", r->iface, servers, from);
    }
    if (w < 0 || (size_t)w >= cap) {
        if (cap) { buf[0] = '\0'; }
        return 0;
    }
    return (size_t)w;
}

firc_resolve_router_t *firc_resolve_router_new(firc_dns_pipeline_t *pipeline)
{
    firc_resolve_router_t *r = calloc(1, sizeof(*r));
    if (r != NULL) { r->pipeline = pipeline; }
    return r;
}

void firc_resolve_router_free(firc_resolve_router_t *r)
{
    if (r == NULL) { return; }
    firc_resolve_table_free(&r->table);
    free(r);
}

void firc_resolve_router_publish(firc_resolve_router_t *r, firc_resolve_table_t *t)
{
    firc_resolve_table_t old = r->table;
    r->table = *t;
    memset(t, 0, sizeof(*t));
    for (size_t i = 0; i < r->table.n; i++) {
        const firc_resolve_route_t *now = &r->table.items[i];
        char a[512], b[512];
        firc_resolve_route_describe(now, a, sizeof(a));
        const firc_resolve_route_t *was = firc_resolve_table_find(&old, now->group_id);
        if (was != NULL) {
            firc_resolve_route_describe(was, b, sizeof(b));
            if (strcmp(a, b) == 0 && strcmp(was->group_name, now->group_name) == 0) { continue; }
        }
        FIRC_INFO("group \"%s\" %s", now->group_name, a);
    }
    firc_resolve_table_free(&old);
}

const firc_resolve_table_t *firc_resolve_router_table(const firc_resolve_router_t *r)
{
    return r != NULL ? &r->table : NULL;
}

bool firc_resolve_router_route(const firc_resolve_router_t *r, firc_id_t id, firc_resolve_route_t *out)
{
    const firc_resolve_route_t *f = r != NULL ? firc_resolve_table_find(&r->table, id) : NULL;
    if (f == NULL) { return false; }
    *out = *f;
    return true;
}

bool firc_resolve_router_carries_v6(void *ud, firc_id_t group_id)
{
    const firc_resolve_router_t *r = ud;
    const firc_resolve_route_t *rt = r != NULL ? firc_resolve_table_find(&r->table, group_id) : NULL;
    return rt != NULL && rt->v6;
}

bool firc_resolve_router_cross_v6(void *ud, firc_id_t group_id)
{
    const firc_resolve_router_t *r = ud;
    const firc_resolve_route_t *rt = r != NULL ? firc_resolve_table_find(&r->table, group_id) : NULL;
    return rt != NULL && rt->v6 && rt->ktcp;
}

bool firc_resolve_router_decide(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client,
                                firc_dnsproxy_route_t *out)
{
    firc_resolve_router_t *r = ud;
    out->blocked = false;
    if (r == NULL || r->pipeline == NULL) { return false; }
    const firc_group_snapshot_t *owner = firc_dns_pipeline_owner(r->pipeline, query, client);
    if (owner == NULL) { return false; }
    out->owner = owner->id;
    if (owner->block >= 0 && firc_dns_blockable(query)) {
        memset(out, 0, sizeof(*out));
        out->owner = owner->id;
        out->group_id = owner->id;
        out->blocked = true;
        out->answer = (firc_block_answer_t)owner->block;
        return true;
    }
    const firc_resolve_route_t *rt = firc_resolve_table_find(&r->table, owner->id);
    if (!firc_resolve_route_usable(rt)) { return false; }
    memset(out, 0, sizeof(*out));
    out->owner = owner->id;
    out->group_id = rt->group_id;
    snprintf(out->group_name, sizeof(out->group_name), "%s", rt->group_name);
    out->mark = rt->mark;
    out->gen = rt->gen;
    out->n_servers = rt->n_servers;
    for (size_t i = 0; i < rt->n_servers; i++) {
        firc_resolver_addr_sockaddr(&rt->servers[i], &out->servers[i], &out->server_lens[i]);
    }
    return true;
}
