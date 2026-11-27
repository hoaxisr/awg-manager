#include "firc/system.h"

#include <arpa/inet.h>
#include <ctype.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <cjson/cJSON.h>

#include "firc/keenetic_policy.h"
#include "firc/events.h"
#include "firc/ifacename.h"
#include "firc/json.h"
#include "firc/keenetic_rci.h"
#include "firc/keenetic_resolvers.h"
#include "firc/log.h"
#include "firc/models.h"
#include "firc/utf8.h"

static void must_route(firc_httpd_t *h, const char *method, const char *pattern, firc_http_handler_fn fn,
                       void *ud) {
    if (firc_httpd_route(h, method, pattern, fn, ud) != FIRC_OK) {
        FIRC_ERROR("failed to register route %s %s", method, pattern);
    }
}

#define EVENTS_PAGE_MAX 1024

static const char *level_name(firc_log_level_t l) {
    switch (l) {
    case FIRC_LOG_DEBUG: return "debug";
    case FIRC_LOG_INFO: return "info";
    case FIRC_LOG_WARN: return "warn";
    case FIRC_LOG_ERROR: return "error";
    }
    return "error";
}

static void put_ip(cJSON *obj, const char *key, const firc_ip_t *ip) {
    char s[INET6_ADDRSTRLEN];
    if (ip->len == 0 || inet_ntop(ip->len == 4 ? AF_INET : AF_INET6, ip->b, s, sizeof(s)) == NULL) {
        return;
    }
    cJSON_AddStringToObject(obj, key, s);
}

static const char *qtype_name(uint16_t t, char *buf, size_t cap) {
    switch (t) {
    case 1: return "A";
    case 2: return "NS";
    case 5: return "CNAME";
    case 6: return "SOA";
    case 12: return "PTR";
    case 15: return "MX";
    case 16: return "TXT";
    case 28: return "AAAA";
    case 33: return "SRV";
    case 64: return "SVCB";
    case 65: return "HTTPS";
    }
    snprintf(buf, cap, "TYPE%u", (unsigned)t);
    return buf;
}

static const char *rcode_name(uint8_t r, char *buf, size_t cap) {
    static const char *names[] = {"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"};
    if (r < sizeof(names) / sizeof(names[0])) { return names[r]; }
    snprintf(buf, cap, "RCODE%u", (unsigned)r);
    return buf;
}

static const char *decision_name(uint8_t d) {
    switch ((firc_dns_decision_t)d) {
    case FIRC_DNS_ISSUED: return "issued";
    case FIRC_DNS_NOT_COVERED: return "not-covered";
    case FIRC_DNS_NO_MATCH: return "no-match";
    case FIRC_DNS_POOL_REFUSED: return "pool-refused";
    case FIRC_DNS_PASSED: return "passed";
    case FIRC_DNS_BLOCKED: return "blocked";
    case FIRC_DNS_DNS_ONLY: return "dns-only";
    }
    return "passed";
}

static const char *proto_name(uint8_t proto, char *buf, size_t cap) {
    switch (proto) {
    case 6: return "tcp";
    case 17: return "udp";
    case 1: return "icmp";
    case 58: return "icmpv6";
    }
    snprintf(buf, cap, "%u", (unsigned)proto);
    return buf;
}

static const char *bypass_how_name(uint8_t how) {
    switch ((firc_bypass_how_t)how) {
    case FIRC_BYPASS_BY_ADDR: return "addr";
    case FIRC_BYPASS_BY_SNI: return "sni";
    }
    return "addr";
}

static void dns_to_json(cJSON *item, const firc_event_dns_t *d) {
    char tmp[16];
    put_ip(item, "client", &d->client);
    cJSON_AddStringToObject(item, "name", d->name);
    cJSON_AddStringToObject(item, "qtype", qtype_name(d->qtype, tmp, sizeof(tmp)));
    cJSON_AddStringToObject(item, "rcode", rcode_name(d->rcode, tmp, sizeof(tmp)));
    cJSON_AddStringToObject(item, "decision", decision_name(d->decision));
    cJSON_AddStringToObject(item, "resolver", firc_dns_resolver_name(d->resolver));
    if (d->group_id[0] != '\0') {
        cJSON *g = cJSON_AddObjectToObject(item, "group");
        if (g != NULL) {
            cJSON_AddStringToObject(g, "id", d->group_id);
            cJSON_AddStringToObject(g, "name", d->group_name);
        }
    }
    put_ip(item, "fake", &d->fake);
    cJSON *reals = cJSON_AddArrayToObject(item, "reals");
    for (uint8_t i = 0; reals != NULL && i < d->n_reals && i < FIRC_EVENT_REALS; i++) {
        char s[INET6_ADDRSTRLEN];
        const firc_ip_t *ip = &d->reals[i];
        if (inet_ntop(ip->len == 4 ? AF_INET : AF_INET6, ip->b, s, sizeof(s)) != NULL) {
            cJSON_AddItemToArray(reals, cJSON_CreateString(s));
        }
    }
}

/* last is absent when the recall has none inside its horizon (FIRC_BYPASS_NOT_ASKED) */
static void bypass_to_json(cJSON *item, const firc_event_bypass_t *b) {
    char tmp[16];
    put_ip(item, "client", &b->client);
    put_ip(item, "dst", &b->dst);
    if (b->dst_port != 0) { cJSON_AddNumberToObject(item, "port", b->dst_port); }
    cJSON_AddStringToObject(item, "proto", proto_name(b->proto, tmp, sizeof(tmp)));
    cJSON_AddStringToObject(item, "how", bypass_how_name(b->how));
    cJSON_AddStringToObject(item, "name", b->name);
    if (b->group_id[0] != '\0') {
        cJSON *g = cJSON_AddObjectToObject(item, "group");
        if (g != NULL) {
            cJSON_AddStringToObject(g, "id", b->group_id);
            cJSON_AddStringToObject(g, "name", b->group_name);
        }
    }
    if (b->last_decision != FIRC_BYPASS_NOT_ASKED) {
        cJSON *last = cJSON_AddObjectToObject(item, "last");
        if (last != NULL) {
            cJSON_AddStringToObject(last, "decision", decision_name(b->last_decision));
            cJSON_AddNumberToObject(last, "at", (double)b->last_at);
            put_ip(last, "fake", &b->last_fake);
        }
    }
    cJSON_AddNumberToObject(item, "repeats", (double)b->repeats);
}

static cJSON *event_to_json(const firc_event_t *e) {
    cJSON *item = cJSON_CreateObject();
    if (item == NULL) { return NULL; }
    cJSON_AddNumberToObject(item, "seq", (double)e->seq);
    cJSON_AddNumberToObject(item, "at", (double)e->at);
    switch (e->kind) {
    case FIRC_EVENT_LOG:
        cJSON_AddStringToObject(item, "kind", "log");
        cJSON_AddStringToObject(item, "level", level_name(e->u.log.level));
        cJSON_AddStringToObject(item, "source", e->u.log.source[0] != '\0' ? e->u.log.source : "fircd");
        cJSON_AddStringToObject(item, "message", e->u.log.text);
        break;
    case FIRC_EVENT_DNS:
        cJSON_AddStringToObject(item, "kind", "dns");
        dns_to_json(item, &e->u.dns);
        break;
    case FIRC_EVENT_BYPASS:
        cJSON_AddStringToObject(item, "kind", "bypass");
        bypass_to_json(item, &e->u.bypass);
        break;
    }
    return item;
}

/* since is the cursor from the previous answer; 0 asks for everything the ring still holds */
static void handle_events(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)ud;
    uint64_t since = 0;
    const char *since_s = firc_http_req_query(req, "since");
    if (since_s != NULL) {
        char *end = NULL;
        unsigned long long v = strtoull(since_s, &end, 10);
        if (end == since_s || *end != '\0') {
            firc_http_res_write_error(res, 400, "since must be a whole number");
            return;
        }
        since = (uint64_t)v;
    }

    firc_event_t *events = calloc(EVENTS_PAGE_MAX, sizeof(*events));
    if (events == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    uint64_t next = since, dropped = 0;
    size_t n = firc_event_read(since, events, EVENTS_PAGE_MAX, &next, &dropped);

    cJSON *out = cJSON_CreateObject();
    cJSON *arr = out != NULL ? cJSON_AddArrayToObject(out, "events") : NULL;
    if (arr == NULL) {
        cJSON_Delete(out);
        free(events);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    for (size_t i = 0; i < n; i++) {
        cJSON *item = event_to_json(&events[i]);
        if (item != NULL) { cJSON_AddItemToArray(arr, item); }
    }
    cJSON_AddNumberToObject(out, "next", (double)next);
    cJSON_AddNumberToObject(out, "dropped", (double)dropped);
    cJSON_AddStringToObject(out, "boot", firc_event_boot());
    free(events);
    firc_http_res_write_json(res, 200, out);
}

/* an empty list is an answer, not a failure: this router may have none configured */
static cJSON *policies_body(firc_system_ctx_t *ctx) {
    cJSON *out = cJSON_CreateObject();
    if (out == NULL) { return NULL; }
    cJSON *arr = cJSON_AddArrayToObject(out, "policies");
    firc_kn_policy_info_t list[64];
    size_t n = firc_kn_policies_list(ctx->policies, list, sizeof(list) / sizeof(list[0]));
    for (size_t i = 0; i < n; i++) {
        cJSON *item = cJSON_CreateObject();
        cJSON_AddStringToObject(item, "name", list[i].name);
        if (list[i].description[0] != '\0') {
            cJSON_AddStringToObject(item, "description", list[i].description);
        }
        cJSON_AddNumberToObject(item, "devices", (double)list[i].devices);
        cJSON_AddItemToArray(arr, item);
    }
    return out;
}

void firc_system_hosts_mark(cJSON *hosts, const firc_ifnet4_t *nets, size_t n, char *const *link, size_t n_link,
                            bool remap_on) {
    cJSON *h = NULL;
    cJSON_ArrayForEach(h, hosts) {
        const cJSON *ip = cJSON_GetObjectItemCaseSensitive(h, "ip");
        const char *seg = firc_ifnet4_find(nets, n, cJSON_IsString(ip) ? ip->valuestring : NULL);
        bool listed = seg == NULL;
        for (size_t i = 0; !listed && i < n_link; i++) { listed = strcmp(seg, link[i]) == 0; }
        if (seg != NULL) { cJSON_AddStringToObject(h, "segment", seg); }
        cJSON_AddBoolToObject(h, "intercepted", !remap_on || listed);
    }
}

void firc_system_hosts_mark_running(cJSON *hosts, firc_ifnet4_t *nets, size_t n, const firc_app_config_t *running) {
    if (running == NULL) {
        firc_system_hosts_mark(hosts, nets, n, NULL, 0, false);
        return;
    }
    firc_ifnet4_link_first(nets, n, running->link, running->n_link);
    firc_system_hosts_mark(hosts, nets, n, running->link, running->n_link, !running->dns_proxy.disable_remap53);
}

/* answered from the policy refresher's latest cached table, never a live RCI call */
static cJSON *hosts_body(firc_system_ctx_t *ctx) {
    cJSON *out = cJSON_CreateObject();
    cJSON *hosts = firc_kn_policies_hosts_json(ctx->policies);
    if (out == NULL || hosts == NULL) {
        cJSON_Delete(out);
        cJSON_Delete(hosts);
        return NULL;
    }
    firc_ifnet4_t nets[64];
    size_t n = firc_ifnet4_list(nets, sizeof(nets) / sizeof(nets[0]));
    firc_system_hosts_mark_running(hosts, nets, n, ctx->app != NULL ? firc_app_running_settings(ctx->app) : NULL);
    if (!cJSON_AddItemToObject(out, "hosts", hosts)) {
        cJSON_Delete(hosts);
        cJSON_Delete(out);
        return NULL;
    }
    return out;
}

static const unsigned FRESH_MS = 5000;
static const uint64_t FRESH_WAIT_MS = 2000;
static const uint64_t FRESH_TICK_MS = 100;

typedef cJSON *(*fresh_body_fn)(firc_system_ctx_t *ctx);

typedef struct fresh_waiter {
    struct fresh_waiter *next;
    firc_system_ctx_t *ctx;
    firc_http_deferred_t *d;
    fresh_body_fn build;
    uint64_t round;
    uint64_t until_ms;
    int timer;
    bool left;
} fresh_waiter_t;

static uint64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static uint8_t *fresh_answer(firc_system_ctx_t *ctx, fresh_body_fn build, int *status, size_t *len) {
    cJSON *out = build(ctx);
    *status = out != NULL ? 200 : 500;
    if (out == NULL) { out = firc_json_error(firc_err_str(FIRC_ERR_NOMEM)); }
    char *text = out != NULL ? firc_json_dump(out) : NULL;
    cJSON_Delete(out);
    if (text == NULL) {
        *status = 500;
        text = strdup("{\"error\":\"internal error\"}");
    }
    *len = text != NULL ? strlen(text) : 0;
    return (uint8_t *)text;
}

static void fresh_unlink(fresh_waiter_t *w) {
    for (fresh_waiter_t **link = &w->ctx->fresh_waiters; *link != NULL; link = &(*link)->next) {
        if (*link == w) {
            *link = w->next;
            break;
        }
    }
}

static void fresh_tick(firc_loop_t *loop, void *ud) {
    fresh_waiter_t *w = ud;
    if (firc_kn_policies_rounds(w->ctx->policies) < w->round && mono_ms() < w->until_ms) { return; }
    (void)firc_loop_del_timer(loop, w->timer);
    fresh_unlink(w);
    int status = 200;
    size_t len = 0;
    uint8_t *body = w->left ? NULL : fresh_answer(w->ctx, w->build, &status, &len);
    firc_http_deferred_finish(w->d, status, "application/json; charset=utf-8", body, len);
    free(w);
}

static void fresh_left(void *ud) {
    ((fresh_waiter_t *)ud)->left = true;
}

static void answer_fresh(firc_http_res_t *res, firc_system_ctx_t *ctx, fresh_body_fn build) {
    uint64_t round = firc_kn_policies_wake(ctx->policies, FRESH_MS);
    fresh_waiter_t *w = round != 0 ? calloc(1, sizeof(*w)) : NULL;
    firc_loop_t *loop = firc_http_res_loop(res);
    if (w != NULL) {
        w->ctx = ctx;
        w->build = build;
        w->round = round;
        w->until_ms = mono_ms() + FRESH_WAIT_MS;
        if (firc_loop_add_timer(loop, FRESH_TICK_MS, FRESH_TICK_MS, fresh_tick, w, &w->timer) != FIRC_OK) {
            free(w);
            w = NULL;
        }
    }
    if (w != NULL) {
        w->d = firc_http_res_defer(res, fresh_left, w);
        if (w->d != NULL) {
            w->next = ctx->fresh_waiters;
            ctx->fresh_waiters = w;
            return;
        }
        (void)firc_loop_del_timer(loop, w->timer);
        free(w);
    }
    int status;
    size_t len;
    uint8_t *body = fresh_answer(ctx, build, &status, &len);
    firc_http_res_take(res, status, "application/json; charset=utf-8", body, len);
}

static void handle_policies(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    answer_fresh(res, ud, policies_body);
}

static void handle_hosts(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    answer_fresh(res, ud, hosts_body);
}

/* empty is an answer */
static void handle_resolvers(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_system_ctx_t *ctx = ud;
    cJSON *out = cJSON_CreateObject();
    cJSON *arr = out != NULL ? cJSON_AddArrayToObject(out, "resolvers") : NULL;
    if (arr == NULL) {
        cJSON_Delete(out);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_kn_iface_resolvers_t list[32];
    size_t n = firc_kn_resolvers_list(ctx->resolvers, list, sizeof(list) / sizeof(list[0]));
    for (size_t i = 0; i < n; i++) {
        cJSON *item = cJSON_CreateObject();
        cJSON_AddStringToObject(item, "interface", list[i].iface);
        cJSON *servers = cJSON_AddArrayToObject(item, "servers");
        for (size_t j = 0; j < list[i].n; j++) {
            char s[FIRC_RESOLVER_ADDR_STRLEN];
            if (firc_resolver_addr_format(&list[i].servers[j], s, sizeof(s)) > 0) {
                cJSON_AddItemToArray(servers, cJSON_CreateString(s));
            }
        }
        cJSON_AddItemToArray(arr, item);
    }
    firc_http_res_write_json(res, 200, out);
}

/* ok false after 5s of failing passes, or firstWritePending true before the first pass completes */
static void handle_netfilter(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_system_ctx_t *ctx = ud;
    firc_nfcommit_health_t h;
    firc_app_netfilter_health(ctx->app, &h);
    cJSON *out = cJSON_CreateObject();
    if (out == NULL || cJSON_AddBoolToObject(out, "ok", !h.failing && !h.first_pending) == NULL ||
        (h.first_pending && cJSON_AddBoolToObject(out, "firstWritePending", true) == NULL) ||
        (h.failing && (cJSON_AddStringToObject(out, "error", firc_err_str(h.err)) == NULL ||
                       cJSON_AddNumberToObject(out, "since", (double)h.since) == NULL))) {
        cJSON_Delete(out);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, 200, out);
}

static const char *autolist_why_name(uint8_t why) {
    switch (why) {
    case FIRC_AL_WHY_NO_ANSWER: return "no-answer";
    case FIRC_AL_WHY_RESET: return "reset";
    case FIRC_AL_WHY_FREEZE: return "freeze";
    case FIRC_AL_WHY_NO_CONNECT: return "no-connect";
    default: return "";
    }
}

static const char *autolist_state_name(uint8_t state) {
    switch (state) {
    case FIRC_AL_CHECKING: return "checking";
    case FIRC_AL_ADDED: return "added";
    case FIRC_AL_OFF: return "off";
    default: return "candidate";
    }
}

static const char *autolist_verdict_name(const firc_autolist_result_t *r) {
    if (r->verdict == FIRC_AUTOLIST_BLOCKED) { return "blocked"; }
    if (r->verdict == FIRC_AUTOLIST_REACHABLE) { return r->short_body ? "short" : "open"; }
    return "unknown";
}

static bool ip_text(const firc_ip_t *ip, char *out, size_t cap) {
    return ip->len != 0 && inet_ntop(ip->len == 4 ? AF_INET : AF_INET6, ip->b, out, (socklen_t)cap) != NULL;
}

static bool autolist_candidates_json(cJSON *out, const firc_autolist_status_t *st) {
    cJSON *arr = cJSON_AddArrayToObject(out, "candidates");
    if (arr == NULL) { return false; }
    for (size_t i = 0; i < st->n_candidates; i++) {
        const firc_al_cand_t *c = &st->candidates[i];
        cJSON *o = cJSON_CreateObject();
        if (o == NULL || !cJSON_AddItemToArray(arr, o)) {
            cJSON_Delete(o);
            return false;
        }
        cJSON *clients = NULL;
        if (cJSON_AddStringToObject(o, "domain", c->domain) == NULL ||
            cJSON_AddStringToObject(o, "host", c->host) == NULL ||
            cJSON_AddStringToObject(o, "why", autolist_why_name(c->why)) == NULL ||
            cJSON_AddNumberToObject(o, "fails", (double)c->fails) == NULL ||
            (clients = cJSON_AddArrayToObject(o, "clients")) == NULL ||
            cJSON_AddNumberToObject(o, "lastAt", (double)c->last_at) == NULL ||
            cJSON_AddStringToObject(o, "state", autolist_state_name(c->state)) == NULL ||
            cJSON_AddNumberToObject(o, "notes", (double)c->notes) == NULL) {
            return false;
        }
        for (uint8_t k = 0; k < c->n_clients; k++) {
            char addr[INET6_ADDRSTRLEN];
            if (!ip_text(&c->clients[k], addr, sizeof(addr))) { continue; }
            cJSON *one = cJSON_CreateString(addr);
            if (one == NULL || !cJSON_AddItemToArray(clients, one)) {
                cJSON_Delete(one);
                return false;
            }
        }
    }
    return true;
}

static const char *bypass_why_name(uint8_t why) {
    if (why == FIRC_AUTOLIST_RESET) { return "reset"; }
    return why == FIRC_AUTOLIST_NO_CONNECT ? "no-connect" : "no-answer";
}

static const char *autolist_how_name(uint8_t how) {
    if (how == FIRC_AL_HOW_ANSWER) { return "answer"; }
    if (how == FIRC_AL_HOW_SNI) { return "sni"; }
    return how == FIRC_AL_HOW_PTR ? "ptr" : "";
}

static const char *ptr_state_name(firc_ptr_state_t st) {
    if (st == FIRC_PTR_NAME) { return "name"; }
    return st == FIRC_PTR_NONE ? "none" : "pending";
}

static bool string_to_array(cJSON *arr, const char *text) {
    cJSON *one = cJSON_CreateString(text);
    if (one == NULL || !cJSON_AddItemToArray(arr, one)) {
        cJSON_Delete(one);
        return false;
    }
    return true;
}

static bool autolist_address_json(cJSON *arr, const firc_autolist_status_t *st, size_t i) {
    const firc_autolist_addr_t *a = &st->addresses[i];
    char addr[INET6_ADDRSTRLEN];
    if (!ip_text(&a->server, addr, sizeof(addr))) { return true; }
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || !cJSON_AddItemToArray(arr, o)) {
        cJSON_Delete(o);
        return false;
    }
    cJSON *clients = NULL, *groups = NULL;
    bool bypass = a->outcome == FIRC_AL_OUTCOME_BYPASS;
    if (cJSON_AddStringToObject(o, "address", addr) == NULL ||
        cJSON_AddStringToObject(o, "outcome", bypass ? "bypass" : "failed") == NULL ||
        cJSON_AddStringToObject(o, "how", bypass ? autolist_how_name(a->how) : "") == NULL ||
        cJSON_AddStringToObject(o, "group", bypass ? a->group : "") == NULL ||
        cJSON_AddStringToObject(o, "proto", a->proto == IPPROTO_UDP ? "udp" : "tcp") == NULL ||
        cJSON_AddNumberToObject(o, "port", (double)a->port) == NULL ||
        cJSON_AddStringToObject(o, "name", a->name) == NULL ||
        cJSON_AddStringToObject(o, "ptr", st->ptr_state[i] == FIRC_PTR_NAME ? st->ptr[i] : "") == NULL ||
        cJSON_AddStringToObject(o, "ptrState", ptr_state_name(st->ptr_state[i])) == NULL ||
        cJSON_AddStringToObject(o, "why", bypass ? "" : bypass_why_name(a->why)) == NULL ||
        cJSON_AddNumberToObject(o, "fails", (double)a->fails) == NULL ||
        (clients = cJSON_AddArrayToObject(o, "clients")) == NULL ||
        cJSON_AddNumberToObject(o, "firstAt", (double)a->first_at) == NULL ||
        cJSON_AddNumberToObject(o, "lastAt", (double)a->last_at) == NULL ||
        (groups = cJSON_AddArrayToObject(o, "groups")) == NULL) {
        return false;
    }
    for (uint8_t k = 0; k < a->n_clients; k++) {
        char client[INET6_ADDRSTRLEN];
        if (ip_text(&a->clients[k], client, sizeof(client)) && !string_to_array(clients, client)) { return false; }
    }
    for (size_t k = 0; k < st->n_groups[i]; k++) {
        char id[FIRC_ID_STR_LEN];
        firc_id_format(st->groups[i][k], id);
        if (!string_to_array(groups, id)) { return false; }
    }
    return true;
}

static bool autolist_addresses_json(cJSON *out, const firc_autolist_status_t *st) {
    cJSON *arr = cJSON_AddArrayToObject(out, "addresses");
    if (arr == NULL) { return false; }
    for (size_t i = 0; i < st->n_addresses; i++) {
        if (!autolist_address_json(arr, st, i)) { return false; }
    }
    return true;
}

static bool autolist_probes_json(cJSON *out, const firc_autolist_status_t *st) {
    cJSON *p = cJSON_AddObjectToObject(out, "probes");
    cJSON *arr = NULL;
    if (p == NULL || cJSON_AddNumberToObject(p, "usedHour", (double)st->probes_used_hour) == NULL ||
        cJSON_AddNumberToObject(p, "queued", (double)st->probes_queued) == NULL ||
        (st->probe_running ? cJSON_AddStringToObject(p, "running", st->probe_host)
                           : cJSON_AddNullToObject(p, "running")) == NULL ||
        (arr = cJSON_AddArrayToObject(p, "results")) == NULL) {
        return false;
    }
    for (size_t i = 0; i < st->n_results; i++) {
        const firc_al_probe_seen_t *r = &st->results[i];
        cJSON *o = cJSON_CreateObject();
        if (o == NULL || !cJSON_AddItemToArray(arr, o)) {
            cJSON_Delete(o);
            return false;
        }
        if (cJSON_AddStringToObject(o, "host", r->host) == NULL ||
            cJSON_AddStringToObject(o, "verdict", autolist_verdict_name(&r->result)) == NULL ||
            cJSON_AddStringToObject(o, "why",
                                    autolist_why_name(r->result.verdict == FIRC_AUTOLIST_BLOCKED ? r->result.why : 0)) ==
                NULL ||
            cJSON_AddNumberToObject(o, "bytes", (double)r->result.bytes) == NULL ||
            cJSON_AddNumberToObject(o, "at", (double)r->at) == NULL ||
            cJSON_AddStringToObject(o, "error", r->result.error) == NULL) {
            return false;
        }
    }
    return true;
}

static void write_autolist(firc_system_ctx_t *ctx, firc_http_res_t *res, int code) {
    firc_autolist_status_t *st = malloc(sizeof(*st));
    cJSON *out = st != NULL ? cJSON_CreateObject() : NULL;
    if (out == NULL) {
        free(st);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_app_autolist_status(ctx->app, st);
    bool ok = cJSON_AddBoolToObject(out, "enabled", st->enabled) != NULL &&
              cJSON_AddBoolToObject(out, "running", st->running) != NULL &&
              cJSON_AddNumberToObject(out, "failures", (double)st->failures) != NULL &&
              cJSON_AddNumberToObject(out, "listed", (double)st->listed) != NULL &&
              (st->why_off == NULL || cJSON_AddStringToObject(out, "whyOff", st->why_off) != NULL) &&
              cJSON_AddBoolToObject(out, "checking", st->checking) != NULL &&
              cJSON_AddNumberToObject(out, "checkedAt", (double)st->checked_at) != NULL &&
              cJSON_AddNumberToObject(out, "switchedOff", (double)st->switched_off) != NULL &&
              cJSON_AddNumberToObject(out, "switchedOn", (double)st->switched_on) != NULL &&
              cJSON_AddStringToObject(out, "mode", st->mode) != NULL &&
              (st->group != NULL ? cJSON_AddStringToObject(out, "group", st->group)
                                 : cJSON_AddNullToObject(out, "group")) != NULL &&
              cJSON_AddNumberToObject(out, "failuresDay", (double)st->failures_day) != NULL &&
              cJSON_AddNumberToObject(out, "ambiguousDay", (double)st->ambiguous_day) != NULL &&
              (st->wan_warning == NULL || cJSON_AddStringToObject(out, "wanWarning", st->wan_warning) != NULL) &&
              cJSON_AddNumberToObject(out, "addedHour", (double)st->added_hour) != NULL &&
              autolist_addresses_json(out, st) && autolist_candidates_json(out, st) && autolist_probes_json(out, st);
    free(st);
    if (!ok) {
        cJSON_Delete(out);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, code, out);
}

static void handle_autolist(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    write_autolist(ud, res, 200);
}

static const char autolist_not_running[] = "the automatic list is not running";
static const char autolist_iface_in_group[] = "the check interface belongs to the group";

static void handle_autolist_check(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_system_ctx_t *ctx = ud;
    firc_err_t err = firc_app_autolist_recheck_now(ctx->app);
    if (err == FIRC_ERR_STATE) {
        firc_http_res_write_error(res, 409, autolist_not_running);
        return;
    }
    if (err == FIRC_ERR_CLOSED) {
        firc_http_res_write_error(res, 409, autolist_iface_in_group);
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    write_autolist(ctx, res, 200);
}

static void handle_autolist_probe(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_system_ctx_t *ctx = ud;
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    const cJSON *host = cJSON_GetObjectItemCaseSensitive(json, "host");
    firc_al_ask_t ask = FIRC_AL_ASK_QUEUED;
    firc_err_t err = cJSON_IsString(host) ? firc_app_autolist_probe(ctx->app, host->valuestring, &ask)
                                          : FIRC_ERR_INVAL;
    cJSON_Delete(json);
    if (err == FIRC_ERR_STATE) {
        firc_http_res_write_error(res, 409, autolist_not_running);
        return;
    }
    if (err == FIRC_ERR_CLOSED) {
        firc_http_res_write_error(res, 409, autolist_iface_in_group);
        return;
    }
    if (err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 400, "not a host name");
        return;
    }
    if (err == FIRC_ERR_LIMIT) {
        firc_http_res_write_error(res, 429, "the check queue is full");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    if (ask == FIRC_AL_ASK_FULL) {
        firc_http_res_write_error(res, 429, "the hour's 30 checks are spent");
        return;
    }
    if (ask == FIRC_AL_ASK_BUSY) {
        firc_http_res_write_error(res, 429, "this host was checked within 10 minutes");
        return;
    }
    write_autolist(ctx, res, 202);
}

static void handle_autolist_candidate(firc_http_req_t *req, firc_http_res_t *res, firc_system_ctx_t *ctx,
                                      bool add) {
    const char *domain = firc_http_req_param(req, "domain");
    firc_err_t err = add ? firc_app_autolist_candidate_add(ctx->app, domain)
                         : firc_app_autolist_candidate_dismiss(ctx->app, domain);
    if (err == FIRC_ERR_STATE) {
        firc_http_res_write_error(res, 409, autolist_not_running);
        return;
    }
    if (err == FIRC_ERR_NOENT || err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 404, "no such candidate");
        return;
    }
    if (err == FIRC_ERR_EXIST) {
        firc_http_res_write_error(res, 409, "the domain is already a rule of the group");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    write_autolist(ctx, res, 200);
}

static void handle_autolist_add(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    handle_autolist_candidate(req, res, ud, true);
}

static void handle_autolist_dismiss(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    handle_autolist_candidate(req, res, ud, false);
}

static bool parse_address_key(firc_http_req_t *req, firc_ip_t *server, uint8_t *proto, uint16_t *port) {
    const char *text = firc_http_req_param(req, "address");
    memset(server, 0, sizeof(*server));
    if (text == NULL) { return false; }
    if (inet_pton(AF_INET, text, server->b) == 1) {
        server->len = 4;
    } else if (inet_pton(AF_INET6, text, server->b) == 1) {
        server->len = 16;
    } else {
        return false;
    }
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    const cJSON *p = cJSON_GetObjectItemCaseSensitive(json, "proto");
    const cJSON *n = cJSON_GetObjectItemCaseSensitive(json, "port");
    bool ok = cJSON_IsString(p) && cJSON_IsNumber(n) && n->valuedouble >= 1 && n->valuedouble <= 65535 &&
              n->valuedouble == (double)(uint16_t)n->valuedouble;
    if (ok && strcmp(p->valuestring, "tcp") == 0) {
        *proto = IPPROTO_TCP;
    } else if (ok && strcmp(p->valuestring, "udp") == 0) {
        *proto = IPPROTO_UDP;
    } else {
        ok = false;
    }
    if (ok) { *port = (uint16_t)n->valuedouble; }
    cJSON_Delete(json);
    return ok;
}

static void handle_autolist_address_dismiss(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_system_ctx_t *ctx = ud;
    firc_ip_t server;
    uint8_t proto = 0;
    uint16_t port = 0;
    if (!parse_address_key(req, &server, &proto, &port)) {
        firc_http_res_write_error(res, 400, "not an address, proto and port");
        return;
    }
    firc_err_t err = firc_app_autolist_address_dismiss(ctx->app, &server, proto, port);
    if (err == FIRC_ERR_STATE) {
        firc_http_res_write_error(res, 409, autolist_not_running);
        return;
    }
    if (err == FIRC_ERR_NOENT) {
        firc_http_res_write_error(res, 404, "no such address");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    cJSON *out = cJSON_CreateObject();
    if (out == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, 200, out);
}

void firc_system_name_tunnels(firc_iface_info_t *ifaces, size_t n, const firc_tunnels_t *ts) {
    for (size_t i = 0; ts != NULL && i < ts->n; i++) {
        if (ts->t[i].description[0] == '\0') { continue; }
        for (size_t j = 0; j < n; j++) {
            if (strcmp(ifaces[j].id, ts->t[i].device) == 0) {
                snprintf(ifaces[j].name, sizeof(ifaces[j].name), "%.*s",
                         firc_utf8_clamp(ts->t[i].description, 63), ts->t[i].description);
            }
        }
    }
}

typedef struct ifaces_waiter {
    struct ifaces_waiter *next;
    struct ifaces_read *read;
    firc_http_deferred_t *d;
} ifaces_waiter_t;

typedef struct ifaces_read {
    firc_system_ctx_t *ctx;
    firc_loop_t *loop;
    pthread_t thread;
    firc_kn_aliases_t aliases;
    atomic_bool stopping;
    bool posted;
    bool joined;
    ifaces_waiter_t *waiters;
} ifaces_read_t;

static char *interfaces_body(const firc_system_ctx_t *ctx, const firc_kn_aliases_t *aliases, int *status) {
    firc_iface_info_t *ifaces = NULL;
    size_t n = 0;
    firc_err_t err = firc_app_list_interfaces(aliases, ctx->tunnels, &ifaces, &n);
    cJSON *out;
    if (err != FIRC_OK) {
        *status = 500;
        out = firc_json_error(firc_err_str(err));
    } else {
        *status = 200;
        firc_system_name_tunnels(ifaces, n, ctx->tunnels);
        out = cJSON_CreateObject();
        cJSON *arr = cJSON_AddArrayToObject(out, "interfaces");
        cJSON *blackhole = cJSON_CreateObject();
        cJSON_AddStringToObject(blackhole, "id", "blackhole");
        cJSON_AddItemToArray(arr, blackhole);
        cJSON *block = cJSON_CreateObject();
        cJSON_AddStringToObject(block, "id", FIRC_IFACE_BLOCK);
        cJSON_AddItemToArray(arr, block);
        cJSON *dns = cJSON_CreateObject();
        cJSON_AddStringToObject(dns, "id", FIRC_IFACE_DNS);
        cJSON_AddItemToArray(arr, dns);
        for (size_t i = 0; i < n; i++) {
            cJSON *item = cJSON_CreateObject();
            cJSON_AddStringToObject(item, "id", ifaces[i].id);
            if (ifaces[i].name[0] != '\0') { cJSON_AddStringToObject(item, "name", ifaces[i].name); }
            if (ifaces[i].ndms_name[0] != '\0') { cJSON_AddStringToObject(item, "ndmsName", ifaces[i].ndms_name); }
            cJSON_AddStringToObject(item, "role", firc_iface_role_name(ifaces[i].role));
            cJSON_AddItemToArray(arr, item);
        }
        free(ifaces);
    }
    char *text = out != NULL ? firc_json_dump(out) : NULL;
    cJSON_Delete(out);
    if (text == NULL) {
        *status = 500;
        text = strdup("{\"error\":\"out of memory\"}");
    }
    return text;
}

static void read_free(ifaces_read_t *r) {
    firc_kn_aliases_free(&r->aliases);
    free(r);
}

static void read_done(firc_loop_t *loop, void *ud);

static void read_drop(void *ud) {
    ifaces_read_t *r = ud;
    if (!r->joined) { pthread_join(r->thread, NULL); }
    read_free(r);
}

static void *read_thread(void *arg) {
    ifaces_read_t *r = arg;
    firc_err_t err = firc_kn_get_iface_aliases(&r->aliases);
    if (err != FIRC_OK) {
        FIRC_DEBUG("failed to load interface aliases: %s", firc_err_str(err));
        firc_kn_aliases_free(&r->aliases);
    }
    r->posted = true;
    while (firc_loop_post_with_drop(r->loop, read_done, r, read_drop) != FIRC_OK) {
        if (atomic_load(&r->stopping)) {
            r->posted = false;
            return NULL;
        }
        struct timespec ts = {.tv_sec = 0, .tv_nsec = 20 * 1000 * 1000};
        nanosleep(&ts, NULL);
    }
    return NULL;
}

static void read_done(firc_loop_t *loop, void *ud) {
    (void)loop;
    ifaces_read_t *r = ud;
    if (!r->joined) {
        pthread_join(r->thread, NULL);
        r->joined = true;
    }
    if (r->ctx != NULL) {
        r->ctx->ifaces_read = NULL;
        while (r->waiters != NULL) {
            ifaces_waiter_t *w = r->waiters;
            r->waiters = w->next;
            int status;
            char *text = interfaces_body(r->ctx, &r->aliases, &status);
            firc_http_deferred_finish(w->d, status, "application/json; charset=utf-8", (uint8_t *)text,
                                      text != NULL ? strlen(text) : 0);
            free(w);
        }
    }
    read_free(r);
}

static void waiter_left(void *ud) {
    ifaces_waiter_t *w = ud;
    for (ifaces_waiter_t **link = &w->read->waiters; *link != NULL; link = &(*link)->next) {
        if (*link == w) {
            *link = w->next;
            break;
        }
    }
    firc_http_deferred_drop(w->d);
    free(w);
}

static void handle_list_interfaces(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_system_ctx_t *ctx = ud;
    ifaces_waiter_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    ifaces_read_t *r = ctx->ifaces_read;
    if (r == NULL) {
        r = calloc(1, sizeof(*r));
        if (r == NULL) {
            free(w);
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        r->ctx = ctx;
        r->loop = firc_http_res_loop(res);
        atomic_init(&r->stopping, false);
        if (pthread_create(&r->thread, NULL, read_thread, r) != 0) {
            free(r);
            free(w);
            firc_http_res_write_error(res, 500, "failed to start the interface read");
            return;
        }
        ctx->ifaces_read = r;
    }
    w->read = r;
    w->d = firc_http_res_defer(res, waiter_left, w);
    if (w->d == NULL) {
        free(w);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    w->next = r->waiters;
    r->waiters = w;
}

static void stat_scratch_free(struct dnsstat_scratch *sc);

static void stats_drop(firc_system_ctx_t *ctx) {
    firc_dnsstat_free(ctx->dnsstat);
    ctx->dnsstat = NULL;
    stat_scratch_free(ctx->dnsstat_scratch);
    ctx->dnsstat_scratch = NULL;
}

void firc_system_close(firc_system_ctx_t *ctx) {
    (void)firc_app_set_dnsstats(ctx->app, NULL, NULL);
    stats_drop(ctx);
    while (ctx->fresh_waiters != NULL) {
        fresh_waiter_t *w = ctx->fresh_waiters;
        ctx->fresh_waiters = w->next;
        firc_http_deferred_drop(w->d);
        free(w);
    }
    ifaces_read_t *r = ctx->ifaces_read;
    if (r == NULL) { return; }
    ctx->ifaces_read = NULL;
    atomic_store(&r->stopping, true);
    pthread_join(r->thread, NULL);
    r->joined = true;
    while (r->waiters != NULL) {
        ifaces_waiter_t *w = r->waiters;
        r->waiters = w->next;
        firc_http_deferred_drop(w->d);
        free(w);
    }
    if (r->posted) {
        r->ctx = NULL;
    } else {
        read_free(r);
    }
}

static void handle_save_config(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_system_ctx_t *ctx = ud;
    if (!ctx->config_path) {
        firc_http_res_write(res, 200, NULL, NULL, 0);
        return;
    }
    firc_err_t err =
        firc_app_save_config(ctx->app, ctx->config_path, ctx->config_version ? ctx->config_version : "");
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    firc_http_res_write(res, 200, NULL, NULL, 0);
}

static void handle_netfilterd_hook(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_system_ctx_t *ctx = ud;
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    cJSON *type_j = cJSON_GetObjectItemCaseSensitive(json, "type");
    cJSON *table_j = cJSON_GetObjectItemCaseSensitive(json, "table");
    FIRC_DEBUG("received netfilter.d event: type=%s table=%s",
            cJSON_IsString(type_j) ? type_j->valuestring : "",
            cJSON_IsString(table_j) ? table_j->valuestring : "");
    cJSON_Delete(json);

    /* never turned into an HTTP error: the firmware fires this hook mid-rewrite and ignores the answer */
    (void)firc_app_force_commit_iptables(ctx->app);
    firc_http_res_write(res, 200, NULL, NULL, 0);
}

static void handle_iflayer_hook(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_system_ctx_t *ctx = ud;
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    const cJSON *iface = cJSON_GetObjectItemCaseSensitive(json, "iface");
    if (!cJSON_IsString(iface) || !firc_is_interface_name(iface->valuestring)) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "an interface name is required");
        return;
    }
    const cJSON *layer = cJSON_GetObjectItemCaseSensitive(json, "layer");
    const cJSON *level = cJSON_GetObjectItemCaseSensitive(json, "level");
    char l1[33], l2[33];
    firc_interface_name_for_log(cJSON_IsString(layer) ? layer->valuestring : "", l1, sizeof(l1));
    firc_interface_name_for_log(cJSON_IsString(level) ? level->valuestring : "", l2, sizeof(l2));
    FIRC_DEBUG("received iflayerchanged.d event: iface=%s layer=%s level=%s", iface->valuestring, l1, l2);
    if (ctx->iface_event != NULL) { ctx->iface_event(ctx->iface_event_ud, iface->valuestring); }
    cJSON_Delete(json);
    firc_http_res_write(res, 200, NULL, NULL, 0);
}

#define DNSSTAT_PAGE_DEFAULT 50
#define DNSSTAT_PAGE_MAX 500
#define DNSSTAT_PAGE_SPAN 550
#define DNSSTAT_DEVICES_MAX 12
#define DNSSTAT_ROW_CLIENTS 16
#define DNSSTAT_QUERY_VALUE_MAX 511

typedef struct {
    const char *name;
    uint32_t gen, queries, blocked, clients, head, tail;
    int64_t last;
    firc_id_t owner;
    bool owner_blocked, hidden, listed;
} stat_name_t;

typedef struct {
    firc_ip_t ip;
    uint32_t queries, next;
} stat_client_t;

typedef struct {
    uint32_t n;
    stat_client_t c[DNSSTAT_ROW_CLIENTS];
} stat_row_t;

typedef struct {
    uint32_t queries, slot;
} stat_pick_t;

struct dnsstat_scratch {
    size_t slots;
    uint32_t gen;
    stat_name_t *names;
    uint32_t *matched;
    stat_pick_t *picks;
    stat_client_t *clients;
    stat_name_t *heap[DNSSTAT_PAGE_SPAN];
};

typedef struct {
    const firc_dnsstat_t *store;
    stat_name_t *names;
    uint32_t *matched_slots;
    stat_pick_t *picks;
    stat_client_t *clients;
    size_t slots, n_clients;
    uint32_t gen;
    firc_ip_t devices[DNSSTAT_DEVICES_MAX];
    size_t n_devices;
    bool arpa;
    const char *needle;
    size_t needle_len;
    uint64_t queries, blocked;
    size_t domains, matched;
} stat_walk_t;

static uint32_t sat_add32(uint32_t a, uint32_t b) {
    return a > UINT32_MAX - b ? UINT32_MAX : a + b;
}

static bool stat_keeps(const stat_walk_t *w, const firc_ip_t *client) {
    if (w->n_devices == 0) { return true; }
    size_t i = 0;
    while (i < w->n_devices &&
           !(w->devices[i].len == client->len && memcmp(w->devices[i].b, client->b, client->len) == 0)) {
        i++;
    }
    return i < w->n_devices;
}

static bool stat_contains(const char *s, const char *needle, size_t len) {
    for (const char *p = strchr(s, needle[0]); p != NULL; p = strchr(p + 1, needle[0])) {
        if (strncmp(p + 1, needle + 1, len - 1) == 0) { return true; }
    }
    return false;
}

static bool stat_aggregate(void *ud, uint32_t slot, const char *name, const firc_ip_t *client,
                           firc_dnsstat_count_t c, int64_t last, firc_id_t owner, bool last_blocked) {
    stat_walk_t *w = ud;
    if (!stat_keeps(w, client)) { return true; }
    stat_name_t *n = &w->names[slot];
    if (n->gen != w->gen) {
        *n = (stat_name_t){.name = name, .gen = w->gen};
        n->hidden = !w->arpa && firc_dnsstat_name_is_arpa(w->store, slot);
        if (n->hidden) { return true; }
        w->domains++;
        n->listed = w->needle == NULL || stat_contains(name, w->needle, w->needle_len);
        if (n->listed) { w->matched_slots[w->matched++] = slot; }
    } else if (n->hidden) {
        return true;
    }
    n->queries = sat_add32(n->queries, c.queries);
    n->blocked = sat_add32(n->blocked, c.blocked);
    n->clients++;
    if (n->clients == 1 || last > n->last) {
        n->last = last;
        n->owner = owner;
        n->owner_blocked = last_blocked;
    }
    w->queries += c.queries;
    w->blocked += c.blocked;
    if (n->listed && w->n_clients < w->slots) {
        uint32_t e = (uint32_t)++w->n_clients;
        w->clients[e - 1] = (stat_client_t){*client, c.queries, 0};
        if (n->tail == 0) {
            n->head = e;
        } else {
            w->clients[n->tail - 1].next = e;
        }
        n->tail = e;
    }
    return true;
}

static void stat_row_clients(const stat_walk_t *w, const stat_name_t *n, stat_row_t *r) {
    r->n = 0;
    for (uint32_t e = n->head; e != 0; e = w->clients[e - 1].next) {
        const stat_client_t *c = &w->clients[e - 1];
        if (r->n < DNSSTAT_ROW_CLIENTS) {
            r->c[r->n++] = *c;
            continue;
        }
        size_t low = 0;
        for (size_t i = 1; i < DNSSTAT_ROW_CLIENTS; i++) {
            if (r->c[i].queries < r->c[low].queries) { low = i; }
        }
        if (c->queries > r->c[low].queries) { r->c[low] = *c; }
    }
}

static bool stat_ranks_below(const stat_name_t *a, const stat_name_t *b) {
    return a->queries < b->queries || (a->queries == b->queries && strcmp(a->name, b->name) > 0);
}

static int stat_cmp_rows(const void *x, const void *y) {
    const stat_name_t *a = *(const stat_name_t *const *)x, *b = *(const stat_name_t *const *)y;
    if (stat_ranks_below(a, b)) { return 1; }
    return stat_ranks_below(b, a) ? -1 : 0;
}

static int stat_cmp_clients(const void *x, const void *y) {
    const stat_client_t *a = x, *b = y;
    if (a->queries != b->queries) { return a->queries < b->queries ? 1 : -1; }
    if (a->ip.len != b->ip.len) { return a->ip.len < b->ip.len ? -1 : 1; }
    return memcmp(a->ip.b, b->ip.b, a->ip.len);
}

static void stat_sift_down(stat_name_t **heap, size_t n, size_t pos) {
    for (;;) {
        size_t low = pos, l = 2 * pos + 1, r = l + 1;
        if (l < n && stat_ranks_below(heap[l], heap[low])) { low = l; }
        if (r < n && stat_ranks_below(heap[r], heap[low])) { low = r; }
        if (low == pos) { return; }
        stat_name_t *t = heap[pos];
        heap[pos] = heap[low];
        heap[low] = t;
        pos = low;
    }
}

static void stat_sift_up(stat_name_t **heap, size_t pos) {
    while (pos > 0) {
        size_t parent = (pos - 1) / 2;
        if (!stat_ranks_below(heap[pos], heap[parent])) { return; }
        stat_name_t *t = heap[pos];
        heap[pos] = heap[parent];
        heap[parent] = t;
        pos = parent;
    }
}

static void stat_partition(stat_pick_t *v, size_t n, size_t k, size_t *lt_out, size_t *gt_out) {
    size_t lo = 0, hi = n;
    for (;;) {
        uint32_t a = v[lo].queries, b = v[lo + (hi - lo) / 2].queries, c = v[hi - 1].queries;
        uint32_t p = a < b ? (b < c ? b : (a < c ? c : a)) : (a < c ? a : (b < c ? c : b));
        size_t lt = lo, i = lo, gt = hi;
        while (i < gt) {
            stat_pick_t x = v[i];
            if (x.queries > p) {
                v[i++] = v[lt];
                v[lt++] = x;
            } else if (x.queries < p) {
                v[i] = v[--gt];
                v[gt] = x;
            } else {
                i++;
            }
        }
        if (k < lt) {
            hi = lt;
        } else if (k >= gt) {
            lo = gt;
        } else {
            *lt_out = lt;
            *gt_out = gt;
            return;
        }
    }
}

static size_t stat_select(const stat_walk_t *w, size_t k, stat_name_t **heap) {
    size_t m = w->matched;
    if (k == 0) { return 0; }
    if (m <= k) {
        for (size_t i = 0; i < m; i++) { heap[i] = &w->names[w->matched_slots[i]]; }
        qsort(heap, m, sizeof(*heap), stat_cmp_rows);
        return m;
    }
    stat_pick_t *v = w->picks;
    for (size_t i = 0; i < m; i++) { v[i] = (stat_pick_t){w->names[w->matched_slots[i]].queries, w->matched_slots[i]}; }
    size_t lt = 0, gt = 0;
    stat_partition(v, m, k - 1, &lt, &gt);
    for (size_t i = 0; i < lt; i++) { heap[i] = &w->names[v[i].slot]; }
    stat_name_t **ties = heap + lt;
    size_t room = k - lt, n = 0;
    for (size_t i = lt; i < gt; i++) {
        stat_name_t *x = &w->names[v[i].slot];
        if (n < room) {
            ties[n] = x;
            stat_sift_up(ties, n++);
        } else if (n > 0 && stat_ranks_below(ties[0], x)) {
            ties[0] = x;
            stat_sift_down(ties, n, 0);
        }
    }
    qsort(heap, lt + n, sizeof(*heap), stat_cmp_rows);
    return lt + n;
}

static bool stat_read_devices(firc_http_req_t *req, firc_http_res_t *res, stat_walk_t *w) {
    const char *v = firc_http_req_query(req, "device");
    if (v == NULL || v[0] == '\0') { return true; }
    if (strlen(v) >= DNSSTAT_QUERY_VALUE_MAX) {
        firc_http_res_write_error(res, 400, "device: too long");
        return false;
    }
    char buf[DNSSTAT_QUERY_VALUE_MAX + 1];
    snprintf(buf, sizeof(buf), "%s", v);
    char *p = buf;
    for (;;) {
        char *comma = strchr(p, ',');
        if (comma != NULL) { *comma = '\0'; }
        if (w->n_devices == DNSSTAT_DEVICES_MAX) {
            firc_http_res_write_error(res, 400, "device: at most 12 addresses");
            return false;
        }
        firc_ip_t *ip = &w->devices[w->n_devices];
        struct in6_addr a6;
        if (inet_pton(AF_INET, p, ip->b) == 1) {
            ip->len = 4;
        } else if (inet_pton(AF_INET6, p, &a6) == 1) {
            if (IN6_IS_ADDR_V4MAPPED(&a6)) {
                memcpy(ip->b, &a6.s6_addr[12], 4);
                ip->len = 4;
            } else {
                memcpy(ip->b, &a6, 16);
                ip->len = 16;
            }
        } else {
            char msg[DNSSTAT_QUERY_VALUE_MAX + 48];
            snprintf(msg, sizeof(msg), "device: not an address: \"%s\"", p);
            firc_http_res_write_error(res, 400, msg);
            return false;
        }
        w->n_devices++;
        if (comma == NULL) { return true; }
        p = comma + 1;
    }
}

static int64_t mono_s(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec;
}

static firc_err_t stat_put_str(firc_bytebuf_t *b, const char *s) {
    firc_err_t err = firc_bytebuf_append_byte(b, '"');
    const char *run = s;
    for (const char *p = s; err == FIRC_OK; p++) {
        unsigned char ch = (unsigned char)*p;
        if (ch >= 32 && ch != '"' && ch != '\\') { continue; }
        err = firc_bytebuf_append(b, run, (size_t)(p - run));
        if (ch == '\0' || err != FIRC_OK) { break; }
        char esc[8];
        switch (ch) {
        case '"': snprintf(esc, sizeof(esc), "\\\""); break;
        case '\\': snprintf(esc, sizeof(esc), "\\\\"); break;
        case '\b': snprintf(esc, sizeof(esc), "\\b"); break;
        case '\f': snprintf(esc, sizeof(esc), "\\f"); break;
        case '\n': snprintf(esc, sizeof(esc), "\\n"); break;
        case '\r': snprintf(esc, sizeof(esc), "\\r"); break;
        case '\t': snprintf(esc, sizeof(esc), "\\t"); break;
        default: snprintf(esc, sizeof(esc), "\\u%04x", ch); break;
        }
        err = firc_bytebuf_append_str(b, esc);
        run = p + 1;
    }
    return err == FIRC_OK ? firc_bytebuf_append_byte(b, '"') : err;
}

static firc_err_t stat_put_owner(firc_bytebuf_t *b, const firc_system_ctx_t *ctx, const stat_name_t *n) {
    firc_ruleset_t *rs = firc_id_is_zero(n->owner) || ctx->app == NULL
                             ? NULL
                             : firc_app_find_group_by_id(ctx->app, n->owner);
    if (rs == NULL) { return firc_bytebuf_append_str(b, "null"); }
    const firc_group_t *g = firc_ruleset_group(rs);
    const char *iface = firc_ruleset_active_iface(rs);
    char id[FIRC_ID_STR_LEN];
    firc_id_format(n->owner, id);
    char buf[48];
    snprintf(buf, sizeof(buf), "{\"id\":\"%s\",\"name\":", id);
    firc_err_t err = firc_bytebuf_append_str(b, buf);
    if (err == FIRC_OK) { err = stat_put_str(b, g->name != NULL ? g->name : ""); }
    if (err == FIRC_OK && iface != NULL) {
        err = firc_bytebuf_append_str(b, ",\"interface\":");
        if (err == FIRC_OK) { err = stat_put_str(b, iface); }
    }
    if (err != FIRC_OK) { return err; }
    return firc_bytebuf_append_str(b, n->owner_blocked ? ",\"blocked\":true}" : ",\"blocked\":false}");
}

static firc_err_t stat_put_row(firc_bytebuf_t *b, const firc_system_ctx_t *ctx, const stat_walk_t *w,
                               const stat_name_t *n, int64_t to_unix) {
    stat_row_t r;
    stat_row_clients(w, n, &r);
    qsort(r.c, r.n, sizeof(r.c[0]), stat_cmp_clients);
    char buf[128];
    firc_err_t err = firc_bytebuf_append_str(b, "{\"name\":");
    if (err == FIRC_OK) { err = stat_put_str(b, n->name); }
    if (err == FIRC_OK) {
        snprintf(buf, sizeof(buf), ",\"queries\":%u,\"blocked\":%u,\"last\":%lld,\"owner\":", (unsigned)n->queries,
                 (unsigned)n->blocked, (long long)(n->last + to_unix));
        err = firc_bytebuf_append_str(b, buf);
    }
    if (err == FIRC_OK) { err = stat_put_owner(b, ctx, n); }
    if (err == FIRC_OK) { err = firc_bytebuf_append_str(b, ",\"clients\":["); }
    for (uint32_t i = 0; err == FIRC_OK && i < r.n; i++) {
        char addr[INET6_ADDRSTRLEN];
        const firc_ip_t *ip = &r.c[i].ip;
        bool named = ip->len != 0 && inet_ntop(ip->len == 4 ? AF_INET : AF_INET6, ip->b, addr, sizeof(addr)) != NULL;
        if (named) {
            snprintf(buf, sizeof(buf), "%s{\"address\":\"%s\",\"queries\":%u}", i > 0 ? "," : "", addr,
                     (unsigned)r.c[i].queries);
        } else {
            snprintf(buf, sizeof(buf), "%s{\"queries\":%u}", i > 0 ? "," : "", (unsigned)r.c[i].queries);
        }
        err = firc_bytebuf_append_str(b, buf);
    }
    if (err == FIRC_OK) {
        snprintf(buf, sizeof(buf), "],\"clientsTotal\":%u}", (unsigned)n->clients);
        err = firc_bytebuf_append_str(b, buf);
    }
    return err;
}

static void stat_scratch_free(struct dnsstat_scratch *sc) {
    if (sc == NULL) { return; }
    free(sc->names);
    free(sc->matched);
    free(sc->picks);
    free(sc->clients);
    free(sc);
}

static struct dnsstat_scratch *stat_scratch(firc_system_ctx_t *ctx, size_t slots) {
    struct dnsstat_scratch *sc = ctx->dnsstat_scratch;
    if (sc != NULL && sc->slots != slots) {
        stat_scratch_free(sc);
        sc = ctx->dnsstat_scratch = NULL;
    }
    size_t cap = slots > 0 ? slots : 1;
    if (sc == NULL) {
        sc = calloc(1, sizeof(*sc));
        if (sc == NULL) { return NULL; }
        sc->slots = slots;
        sc->names = calloc(cap, sizeof(*sc->names));
        sc->matched = malloc(cap * sizeof(*sc->matched));
        sc->picks = malloc(cap * sizeof(*sc->picks));
        sc->clients = malloc(cap * sizeof(*sc->clients));
        if (sc->names == NULL || sc->matched == NULL || sc->picks == NULL || sc->clients == NULL) {
            stat_scratch_free(sc);
            return NULL;
        }
        ctx->dnsstat_scratch = sc;
    }
    if (++sc->gen == 0) {
        memset(sc->names, 0, cap * sizeof(*sc->names));
        sc->gen = 1;
    }
    return sc;
}

static void handle_dnsstats(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_system_ctx_t *ctx = ud;
    stat_walk_t w;
    memset(&w, 0, sizeof(w));
    size_t offset = 0, limit = DNSSTAT_PAGE_DEFAULT;
    if (!firc_http_req_size(req, res, "offset", 0, &offset)) { return; }
    if (!firc_http_req_size(req, res, "limit", DNSSTAT_PAGE_DEFAULT, &limit)) { return; }
    if (limit == 0) { limit = DNSSTAT_PAGE_DEFAULT; }
    if (limit > DNSSTAT_PAGE_MAX) { limit = DNSSTAT_PAGE_MAX; }
    if (offset > DNSSTAT_PAGE_SPAN - limit) {
        firc_http_res_write_error(res, 400, "invalid offset: offset + limit is at most 550");
        return;
    }
    if (!stat_read_devices(req, res, &w)) { return; }
    const char *arpa = firc_http_req_query(req, "arpa");
    w.arpa = arpa != NULL && strcmp(arpa, "1") == 0;
    char needle[DNSSTAT_QUERY_VALUE_MAX + 1];
    const char *q = firc_http_req_query(req, "q");
    if (q != NULL && q[0] != '\0') {
        size_t i = 0;
        for (; i < DNSSTAT_QUERY_VALUE_MAX && q[i] != '\0'; i++) { needle[i] = (char)tolower((unsigned char)q[i]); }
        needle[i] = '\0';
        w.needle = needle;
        w.needle_len = i;
    }

    int64_t now_mono = mono_s();
    int64_t to_unix = (int64_t)time(NULL) - now_mono;
    if (ctx->dnsstat == NULL) {
        char off[224];
        snprintf(off, sizeof(off),
                 "{\"enabled\":false,\"window\":%d,\"now\":%lld,\"since\":%lld,\"queries\":0,\"blocked\":0,"
                 "\"domains\":0,\"rows\":[],\"matched\":0,\"offset\":%zu}",
                 FIRC_DNSSTAT_HOUR * FIRC_DNSSTAT_HOURS, (long long)(now_mono + to_unix),
                 (long long)(now_mono + to_unix), offset);
        firc_bytebuf_t b;
        firc_bytebuf_init(&b);
        firc_http_res_take_json(res, &b, firc_bytebuf_append_str(&b, off));
        return;
    }
    size_t slots = firc_dnsstat_name_slots(ctx->dnsstat);
    struct dnsstat_scratch *sc = stat_scratch(ctx, slots);
    if (sc == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    w.store = ctx->dnsstat;
    w.names = sc->names;
    w.matched_slots = sc->matched;
    w.picks = sc->picks;
    w.clients = sc->clients;
    w.slots = slots;
    w.gen = sc->gen;
    firc_dnsstat_each(ctx->dnsstat, now_mono, stat_aggregate, &w);
    size_t k = offset + limit < slots ? offset + limit : slots;
    size_t n_top = stat_select(&w, k, sc->heap);
    size_t n_rows = n_top > offset ? n_top - offset : 0;

    uint64_t queries = w.queries, blocked = w.blocked;
    if (w.n_devices == 0) {
        firc_dnsstat_count_t t = firc_dnsstat_totals(ctx->dnsstat, now_mono);
        firc_dnsstat_count_t a = w.arpa ? (firc_dnsstat_count_t){0, 0} : firc_dnsstat_totals_arpa(ctx->dnsstat, now_mono);
        queries = t.queries > a.queries ? t.queries - a.queries : 0;
        blocked = t.blocked > a.blocked ? t.blocked - a.blocked : 0;
    }
    int64_t since = ctx->dnsstat_since_mono + to_unix;
    firc_bytebuf_t b;
    firc_bytebuf_init(&b);
    char head[224];
    snprintf(head, sizeof(head),
             "{\"enabled\":true,\"window\":%d,\"now\":%lld,\"since\":%lld,\"queries\":%llu,\"blocked\":%llu,\"domains\":%zu,"
             "\"rows\":[",
             FIRC_DNSSTAT_HOUR * FIRC_DNSSTAT_HOURS, (long long)(now_mono + to_unix), (long long)since,
             (unsigned long long)queries, (unsigned long long)blocked, w.domains);
    firc_err_t err = firc_bytebuf_append_str(&b, head);
    for (size_t i = 0; err == FIRC_OK && i < n_rows; i++) {
        if (i > 0) { err = firc_bytebuf_append_byte(&b, ','); }
        if (err == FIRC_OK) { err = stat_put_row(&b, ctx, &w, sc->heap[offset + i], to_unix); }
    }
    if (err == FIRC_OK) {
        char tail[96];
        snprintf(tail, sizeof(tail), "],\"matched\":%zu,\"offset\":%zu}", w.matched, offset);
        err = firc_bytebuf_append_str(&b, tail);
    }
    firc_http_res_take_json(res, &b, err);
}

static firc_err_t stats_switch(void *ud, bool on) {
    firc_system_ctx_t *ctx = ud;
    if (!on) {
        if (ctx->dnsstat != NULL) { FIRC_INFO("dns stats: off, the counts are dropped"); }
        stats_drop(ctx);
        return FIRC_OK;
    }
    if (ctx->dnsstat != NULL) { return FIRC_OK; }
    ctx->dnsstat = firc_dnsstat_new(FIRC_DNSSTAT_KEYS);
    if (ctx->dnsstat == NULL) {
        FIRC_ERROR("dns stats: no memory for the store, nothing is counted");
        return FIRC_ERR_NOMEM;
    }
    ctx->dnsstat_since_mono = mono_s();
    return FIRC_OK;
}

firc_err_t firc_system_dnsstats_start(firc_system_ctx_t *ctx) {
    return firc_app_set_dnsstats(ctx->app, stats_switch, ctx);
}

void firc_system_register_routes(firc_httpd_t *h, firc_system_ctx_t *ctx) {
    must_route(h, "GET", "/api/v1/system/interfaces", handle_list_interfaces, ctx);
    must_route(h, "GET", "/api/v1/system/events", handle_events, ctx);
    must_route(h, "GET", "/api/v1/system/dnsstats", handle_dnsstats, ctx);
    must_route(h, "GET", "/api/v1/system/policies", handle_policies, ctx);
    must_route(h, "GET", "/api/v1/system/hosts", handle_hosts, ctx);
    must_route(h, "GET", "/api/v1/system/resolvers", handle_resolvers, ctx);
    must_route(h, "GET", "/api/v1/system/netfilter", handle_netfilter, ctx);
    must_route(h, "GET", "/api/v1/system/autolist", handle_autolist, ctx);
    must_route(h, "POST", "/api/v1/system/autolist/check", handle_autolist_check, ctx);
    must_route(h, "POST", "/api/v1/system/autolist/probe", handle_autolist_probe, ctx);
    must_route(h, "POST", "/api/v1/system/autolist/candidates/{domain}/add", handle_autolist_add, ctx);
    must_route(h, "POST", "/api/v1/system/autolist/candidates/{domain}/dismiss", handle_autolist_dismiss, ctx);
    must_route(h, "POST", "/api/v1/system/autolist/addresses/{address}/dismiss", handle_autolist_address_dismiss, ctx);
    must_route(h, "POST", "/api/v1/system/config/save", handle_save_config, ctx);
    must_route(h, "POST", "/api/v1/system/hooks/netfilterd", handle_netfilterd_hook, ctx);
    must_route(h, "POST", "/api/v1/system/hooks/iflayer", handle_iflayer_hook, ctx);
    firc_settings_register_routes(h, ctx);
}
