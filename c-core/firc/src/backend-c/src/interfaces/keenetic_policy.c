#include "firc/keenetic_policy.h"

#include <arpa/inet.h>
#include <cjson/cJSON.h>
#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/keenetic_rci.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/mark.h"
#include "firc/rand.h"

typedef struct {
    char *policy;
    size_t host;
    bool active;
    firc_ip_t addr;
} member_t;

typedef struct {
    char *description;
    char *policy;
} alias_t;

typedef struct {
    char *policy;
    uint32_t net, mask;
} segment_t;

typedef struct {
    char *policy;
    uint32_t mark;
} mark_t;

typedef enum { RC_NONE, RC_DEFAULT, RC_CONFORM, RC_NAMED } rc_entry_t;

typedef struct {
    firc_mac_t mac;
    char *name;
    char *policy;
    char *ip;
    bool active, registered;
    rc_entry_t rc;
    char *rc_policy;
    size_t first, n_addrs;
} host_t;

typedef struct {
    firc_ip_t addr;
    uint32_t host;
} slot_t;

struct firc_kn_policy_map {
    member_t *members;
    size_t n_members, cap_members;
    segment_t *segments;
    size_t n_segments;
    alias_t *aliases;
    size_t n_aliases, cap_aliases;
    host_t *hosts;
    size_t n_hosts, cap_hosts;
    slot_t *index;
    size_t index_mask;
    uint32_t seed;
    struct firc_kn_policy_map *next_retired;
    size_t *owned;
    size_t n_owned;
    mark_t *marks;
    size_t n_marks;
    char clash[2][FIRC_KN_POLICY_NAME_MAX];
};

static bool parse_addr(const char *text, firc_ip_t *out) {
    if (text == NULL || *text == '\0' || strlen(text) > 48) { return false; }
    char with_len[64];
    snprintf(with_len, sizeof(with_len), "%s/%s", text, strchr(text, ':') != NULL ? "128" : "32");
    uint8_t prefix = 0;
    return firc_ip_parse_cidr(with_len, out, &prefix);
}

static firc_err_t add_member(firc_kn_policy_map_t *m, const char *policy, size_t host, bool active,
                             const firc_ip_t *addr) {
    if (m->n_members == m->cap_members) {
        size_t cap = m->cap_members ? m->cap_members * 2 : 16;
        member_t *v = realloc(m->members, cap * sizeof(*v));
        if (v == NULL) { return FIRC_ERR_NOMEM; }
        m->members = v;
        m->cap_members = cap;
    }
    member_t *e = &m->members[m->n_members];
    e->policy = strdup(policy);
    if (e->policy == NULL) { return FIRC_ERR_NOMEM; }
    e->host = host;
    e->active = active;
    e->addr = *addr;
    m->n_members++;
    m->hosts[host - 1].n_addrs++;
    return FIRC_OK;
}

static firc_err_t add_alias(firc_kn_policy_map_t *m, const char *description, const char *policy) {
    if (m->n_aliases == m->cap_aliases) {
        size_t cap = m->cap_aliases ? m->cap_aliases * 2 : 8;
        alias_t *v = realloc(m->aliases, cap * sizeof(*v));
        if (v == NULL) { return FIRC_ERR_NOMEM; }
        m->aliases = v;
        m->cap_aliases = cap;
    }
    alias_t *a = &m->aliases[m->n_aliases];
    a->description = strdup(description);
    a->policy = strdup(policy);
    if (a->description == NULL || a->policy == NULL) {
        free(a->description);
        free(a->policy);
        return FIRC_ERR_NOMEM;
    }
    m->n_aliases++;
    return FIRC_OK;
}

static const char *string_or(const cJSON *h, const char *key, const char *fallback) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(h, key);
    return cJSON_IsString(v) && v->valuestring[0] != '\0' ? v->valuestring : fallback;
}

static firc_err_t add_host(firc_kn_policy_map_t *m, const cJSON *h, const char *policy, bool active) {
    if (m->n_hosts == m->cap_hosts) {
        size_t cap = m->cap_hosts ? m->cap_hosts * 2 : 16;
        host_t *v = realloc(m->hosts, cap * sizeof(*v));
        if (v == NULL) { return FIRC_ERR_NOMEM; }
        m->hosts = v;
        m->cap_hosts = cap;
    }
    host_t *e = &m->hosts[m->n_hosts];
    memset(e, 0, sizeof(*e));
    m->n_hosts++; /* counted before the strdups, so the free below sees it either way */
    const cJSON *mac = cJSON_GetObjectItemCaseSensitive(h, "mac");
    if (cJSON_IsString(mac)) { (void)firc_mac_parse(mac->valuestring, &e->mac); }
    char ip[INET_ADDRSTRLEN] = "";
    const cJSON *ip_j = cJSON_GetObjectItemCaseSensitive(h, "ip");
    firc_ip_t a;
    if (cJSON_IsString(ip_j) && parse_addr(ip_j->valuestring, &a) && a.len == 4) {
        (void)inet_ntop(AF_INET, a.b, ip, sizeof(ip));
    }
    e->name = strdup(string_or(h, "name", string_or(h, "hostname", "")));
    e->policy = strdup(policy);
    e->ip = strdup(ip);
    e->active = active;
    e->registered = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(h, "registered"));
    e->first = m->n_members;
    return e->name != NULL && e->policy != NULL && e->ip != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
}

static firc_err_t parse_hosts(firc_kn_policy_map_t *m, const char *json) {
    cJSON *root = cJSON_Parse(json);
    if (root == NULL) { return FIRC_ERR_PROTO; }
    const cJSON *hosts = cJSON_IsObject(root) ? cJSON_GetObjectItemCaseSensitive(root, "host") : NULL;
    if (!cJSON_IsArray(hosts)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    firc_err_t err = FIRC_OK;
    size_t host = 0;
    const cJSON *h = NULL;
    cJSON_ArrayForEach(h, hosts) {
        if (err != FIRC_OK) { break; }
        if (!cJSON_IsObject(h)) { continue; }
        host++;
        /* every host is recorded, policy or not: an address selector needs the device's other addresses */
        const cJSON *policy = cJSON_GetObjectItemCaseSensitive(h, "policy");
        const char *pname = cJSON_IsString(policy) ? policy->valuestring : "";
        const cJSON *active_j = cJSON_GetObjectItemCaseSensitive(h, "active");
        bool active = cJSON_IsTrue(active_j);
        err = add_host(m, h, pname, active);
        if (err != FIRC_OK) { break; }
        firc_ip_t addr;
        const cJSON *ip = cJSON_GetObjectItemCaseSensitive(h, "ip");
        if (cJSON_IsString(ip) && parse_addr(ip->valuestring, &addr)) {
            err = add_member(m, pname, host, active, &addr);
        }
        const cJSON *ip6 = cJSON_GetObjectItemCaseSensitive(h, "ip6");
        const cJSON *a6 = NULL;
        if (cJSON_IsArray(ip6)) {
            cJSON_ArrayForEach(a6, ip6) {
                if (err != FIRC_OK) { break; }
                if (cJSON_IsString(a6) && parse_addr(a6->valuestring, &addr)) {
                    err = add_member(m, pname, host, active, &addr);
                }
            }
        }
    }
    cJSON_Delete(root);
    return err;
}

static firc_err_t parse_policies(firc_kn_policy_map_t *m, const char *json) {
    cJSON *root = cJSON_Parse(json);
    if (root == NULL) { return FIRC_ERR_PROTO; }
    /* an empty array means no policies, not a shape error */
    if (cJSON_IsArray(root) && cJSON_GetArraySize(root) == 0) {
        cJSON_Delete(root);
        return FIRC_OK;
    }
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    firc_err_t err = FIRC_OK;
    const cJSON *p = NULL;
    cJSON_ArrayForEach(p, root) {
        if (err != FIRC_OK) { break; }
        if (!cJSON_IsObject(p) || p->string == NULL || p->string[0] == '\0') { continue; }
        const cJSON *d = cJSON_GetObjectItemCaseSensitive(p, "description");
        const char *desc = cJSON_IsString(d) && d->valuestring[0] != '\0' ? d->valuestring : p->string;
        err = add_alias(m, desc, p->string);
    }
    cJSON_Delete(root);
    return err;
}

static bool same_addr(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

/* murmur3 finaliser: a plain multiply would start a whole /24 at two slots. */
static uint32_t fmix32(uint32_t h) {
    h ^= h >> 16;
    h *= 0x85ebca6bu;
    h ^= h >> 13;
    h *= 0xc2b2ae35u;
    h ^= h >> 16;
    return h;
}

static size_t addr_hash(const firc_kn_policy_map_t *m, const firc_ip_t *a) {
    uint32_t h = m->seed ^ a->len;
    for (size_t i = 0; i < a->len; i += 4) {
        uint32_t w;
        memcpy(&w, a->b + i, 4);
        h = fmix32(h ^ w);
    }
    return (size_t)h;
}

static slot_t *index_slot(const firc_kn_policy_map_t *m, const firc_ip_t *a) {
    size_t i = addr_hash(m, a) & m->index_mask;
    while (m->index[i].host != 0 && !same_addr(&m->index[i].addr, a)) { i = (i + 1) & m->index_mask; }
    return &m->index[i];
}

/* First host listed at an address wins unless a later one is active (offline hosts keep old leases). */
static firc_err_t build_index(firc_kn_policy_map_t *m) {
    size_t cap = 16;
    while (cap < 2 * m->n_members) { cap *= 2; }
    m->index = calloc(cap, sizeof(*m->index));
    m->owned = calloc(m->n_hosts ? m->n_hosts : 1, sizeof(*m->owned));
    if (m->index == NULL || m->owned == NULL) { return FIRC_ERR_NOMEM; }
    m->index_mask = cap - 1;
    if (firc_random_bytes((uint8_t *)&m->seed, sizeof(m->seed)) != FIRC_OK) {
        /* no /dev/urandom: still not a constant anyone can read off the source */
        struct timespec t;
        clock_gettime(CLOCK_MONOTONIC, &t);
        m->seed = (uint32_t)t.tv_nsec ^ (uint32_t)(uintptr_t)m;
    }
    for (size_t i = 0; i < m->n_members; i++) {
        const member_t *e = &m->members[i];
        slot_t *s = index_slot(m, &e->addr);
        if (s->host == 0 || (e->active && !m->hosts[s->host - 1].active)) {
            s->addr = e->addr;
            s->host = (uint32_t)e->host;
        }
    }
    for (size_t i = 0; i < m->n_hosts; i++) {
        const char *own = m->hosts[i].policy;
        if (m->hosts[i].n_addrs == 0 || own[0] == '\0') { continue; }
        bool seen = false;
        for (size_t j = 0; j < m->n_owned && !seen; j++) { seen = strcmp(m->hosts[m->owned[j]].policy, own) == 0; }
        if (!seen) { m->owned[m->n_owned++] = i; }
    }
    return FIRC_OK;
}

firc_err_t firc_kn_policy_map_parse(const char *hotspot_json, const char *policies_json,
                                    firc_kn_policy_map_t **out) {
    *out = NULL;
    if (hotspot_json == NULL) { return FIRC_ERR_INVAL; }
    firc_kn_policy_map_t *m = calloc(1, sizeof(*m));
    if (m == NULL) { return FIRC_ERR_NOMEM; }
    firc_err_t err = parse_hosts(m, hotspot_json);
    if (err == FIRC_OK) { err = build_index(m); }
    if (err == FIRC_OK && policies_json != NULL) { err = parse_policies(m, policies_json); }
    if (err != FIRC_OK) {
        firc_kn_policy_map_free(m);
        return err;
    }
    *out = m;
    return FIRC_OK;
}

void firc_kn_policy_map_free(firc_kn_policy_map_t *m) {
    if (m == NULL) { return; }
    for (size_t i = 0; i < m->n_members; i++) { free(m->members[i].policy); }
    free(m->members);
    for (size_t i = 0; i < m->n_aliases; i++) {
        free(m->aliases[i].description);
        free(m->aliases[i].policy);
    }
    free(m->aliases);
    for (size_t i = 0; i < m->n_segments; i++) { free(m->segments[i].policy); }
    free(m->segments);
    for (size_t i = 0; i < m->n_hosts; i++) {
        free(m->hosts[i].name);
        free(m->hosts[i].policy);
        free(m->hosts[i].ip);
        free(m->hosts[i].rc_policy);
    }
    free(m->hosts);
    free(m->index);
    free(m->owned);
    for (size_t i = 0; i < m->n_marks; i++) { free(m->marks[i].policy); }
    free(m->marks);
    free(m);
}

static size_t segments_of(const cJSON *root, firc_kn_segment_t *out, size_t cap) {
    const cJSON *list = cJSON_IsObject(root) ? cJSON_GetObjectItemCaseSensitive(root, "policy") : NULL;
    size_t n = 0;
    const cJSON *e = NULL;
    if (cJSON_IsArray(list)) {
        cJSON_ArrayForEach(e, list) {
            if (n == cap) { break; }
            const cJSON *iface = cJSON_GetObjectItemCaseSensitive(e, "interface");
            const cJSON *policy = cJSON_GetObjectItemCaseSensitive(e, "policy");
            if (!cJSON_IsString(iface) || !cJSON_IsString(policy) || iface->valuestring[0] == '\0' ||
                policy->valuestring[0] == '\0' || strlen(iface->valuestring) >= sizeof(out->iface) ||
                strlen(policy->valuestring) >= sizeof(out->policy)) {
                continue;
            }
            snprintf(out[n].iface, sizeof(out[n].iface), "%s", iface->valuestring);
            snprintf(out[n].policy, sizeof(out[n].policy), "%s", policy->valuestring);
            n++;
        }
    }
    return n;
}

size_t firc_kn_hotspot_segments_parse(const char *rc_hotspot_json, firc_kn_segment_t *out, size_t cap) {
    cJSON *root = rc_hotspot_json != NULL ? cJSON_Parse(rc_hotspot_json) : NULL;
    size_t n = segments_of(root, out, cap);
    cJSON_Delete(root);
    return n;
}

static bool parse_v4(const char *text, uint32_t *out) {
    firc_ip_t a;
    if (!parse_addr(text, &a) || a.len != 4) { return false; }
    *out = (uint32_t)a.b[0] << 24 | (uint32_t)a.b[1] << 16 | (uint32_t)a.b[2] << 8 | a.b[3];
    return true;
}

firc_err_t firc_kn_policy_map_add_segment(firc_kn_policy_map_t *m, const char *policy, const char *address,
                                          const char *mask) {
    uint32_t net = 0, msk = 0;
    if (m == NULL || policy == NULL || policy[0] == '\0' || !parse_v4(address, &net) || !parse_v4(mask, &msk)) {
        return FIRC_ERR_INVAL;
    }
    /* A non-contiguous mask is no network a -s can express: refused. */
    uint32_t host_bits = ~msk;
    if ((host_bits & (host_bits + 1u)) != 0) { return FIRC_ERR_INVAL; }
    segment_t *v = realloc(m->segments, (m->n_segments + 1) * sizeof(*v));
    if (v == NULL) { return FIRC_ERR_NOMEM; }
    m->segments = v;
    segment_t *e = &m->segments[m->n_segments];
    e->policy = strdup(policy);
    if (e->policy == NULL) { return FIRC_ERR_NOMEM; }
    e->net = net & msk;
    e->mask = msk;
    m->n_segments++;
    return FIRC_OK;
}

firc_err_t firc_kn_policy_map_add_segments(firc_kn_policy_map_t *m, const firc_kn_segment_t *seg, size_t n,
                                           const char *interfaces_json) {
    cJSON *root = interfaces_json != NULL ? cJSON_Parse(interfaces_json) : NULL;
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; i < n && err == FIRC_OK; i++) {
        const cJSON *ifc = cJSON_GetObjectItemCaseSensitive(root, seg[i].iface);
        const cJSON *a = cJSON_GetObjectItemCaseSensitive(ifc, "address");
        const cJSON *k = cJSON_GetObjectItemCaseSensitive(ifc, "mask");
        if (!cJSON_IsString(a) || !cJSON_IsString(k)) { continue; }
        firc_err_t e = firc_kn_policy_map_add_segment(m, seg[i].policy, a->valuestring, k->valuestring);
        if (e == FIRC_ERR_NOMEM) { err = e; }
    }
    cJSON_Delete(root);
    return err;
}

static int hex_value(char c) {
    if (c >= '0' && c <= '9') { return c - '0'; }
    if (c >= 'a' && c <= 'f') { return c - 'a' + 10; }
    if (c >= 'A' && c <= 'F') { return c - 'A' + 10; }
    return -1;
}

static bool parse_mark(const char *s, uint32_t *out) {
    size_t n = strlen(s);
    if (n == 0 || n > 8) { return false; }
    uint32_t v = 0;
    for (size_t i = 0; i < n; i++) {
        int d = hex_value(s[i]);
        if (d < 0) { return false; }
        v = v << 4 | (uint32_t)d;
    }
    *out = v;
    return true;
}

static firc_err_t add_mark(firc_kn_policy_map_t *m, const char *policy, uint32_t mark) {
    mark_t *v = realloc(m->marks, (m->n_marks + 1) * sizeof(*v));
    if (v == NULL) { return FIRC_ERR_NOMEM; }
    m->marks = v;
    m->marks[m->n_marks].policy = strdup(policy);
    if (m->marks[m->n_marks].policy == NULL) { return FIRC_ERR_NOMEM; }
    m->marks[m->n_marks].mark = mark;
    m->n_marks++;
    return FIRC_OK;
}

/* The first pair whose marks agree under FIRC_MARK_POLICY_MASK is kept for the refresher's warning. */
static void find_clash(firc_kn_policy_map_t *m) {
    for (size_t i = 0; i < m->n_marks && m->clash[0][0] == '\0'; i++) {
        for (size_t j = i + 1; j < m->n_marks; j++) {
            if ((m->marks[i].mark & FIRC_MARK_POLICY_MASK) != (m->marks[j].mark & FIRC_MARK_POLICY_MASK)) { continue; }
            snprintf(m->clash[0], sizeof(m->clash[0]), "%s", m->marks[i].policy);
            snprintf(m->clash[1], sizeof(m->clash[1]), "%s", m->marks[j].policy);
            break;
        }
    }
}

firc_err_t firc_kn_policy_map_add_marks(firc_kn_policy_map_t *m, const char *ip_policy_json) {
    cJSON *root = ip_policy_json != NULL ? cJSON_Parse(ip_policy_json) : NULL;
    if (cJSON_IsArray(root) && cJSON_GetArraySize(root) == 0) {
        cJSON_Delete(root);
        return FIRC_OK;
    }
    if (m == NULL || !cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    firc_err_t err = FIRC_OK;
    const cJSON *p = NULL;
    cJSON_ArrayForEach(p, root) {
        if (err != FIRC_OK) { break; }
        if (!cJSON_IsObject(p) || p->string == NULL || p->string[0] == '\0') { continue; }
        const cJSON *mk = cJSON_GetObjectItemCaseSensitive(p, "mark");
        uint32_t v = 0;
        /* no guessing: an entry without a mark of the firmware's form writes no rule */
        if (!cJSON_IsString(mk) || !parse_mark(mk->valuestring, &v)) { continue; }
        /* A mark that is 0 under the mask would match every unmarked packet. */
        if ((v & FIRC_MARK_POLICY_MASK) == 0) { continue; }
        err = add_mark(m, p->string, v);
    }
    cJSON_Delete(root);
    if (err == FIRC_OK) { find_clash(m); }
    return err;
}

static const char *segment_policy(const firc_kn_policy_map_t *m, const firc_ip_t *a) {
    if (a->len != 4) { return ""; }
    uint32_t v = (uint32_t)a->b[0] << 24 | (uint32_t)a->b[1] << 16 | (uint32_t)a->b[2] << 8 | a->b[3];
    for (size_t i = 0; i < m->n_segments; i++) {
        if ((v & m->segments[i].mask) == m->segments[i].net) { return m->segments[i].policy; }
    }
    return "";
}

static const char *internal_name(const firc_kn_policy_map_t *m, const char *policy) {
    for (size_t i = 0; i < m->n_aliases; i++) {
        if (strcmp(m->aliases[i].description, policy) == 0) { return m->aliases[i].policy; }
    }
    return policy;
}

bool firc_kn_policy_map_mark(const firc_kn_policy_map_t *m, const char *policy, uint32_t *mark) {
    if (m == NULL || policy == NULL || policy[0] == '\0' || mark == NULL) { return false; }
    const char *name = internal_name(m, policy);
    for (size_t i = 0; i < m->n_marks; i++) {
        if (strcmp(m->marks[i].policy, name) == 0) {
            *mark = m->marks[i].mark;
            return true;
        }
    }
    return false;
}

static const host_t *host_at(const firc_kn_policy_map_t *m, const firc_ip_t *client) {
    uint32_t host = index_slot(m, client)->host;
    return host != 0 ? &m->hosts[host - 1] : NULL;
}

static size_t host_others(const firc_kn_policy_map_t *m, const host_t *h, const firc_ip_t *client,
                          firc_ip_t *out, size_t cap) {
    size_t n = 0;
    for (size_t i = h->first; i < h->first + h->n_addrs && n < cap; i++) {
        if (!same_addr(&m->members[i].addr, client)) { out[n++] = m->members[i].addr; }
    }
    return n;
}

size_t firc_kn_policy_map_device(const firc_kn_policy_map_t *m, const firc_ip_t *client, firc_ip_t *out,
                                 size_t cap) {
    if (m == NULL || client == NULL || client->len == 0) { return 0; }
    const host_t *h = host_at(m, client);
    return h != NULL ? host_others(m, h, client, out, cap) : 0;
}

static bool mac_is_zero(const firc_mac_t *mac) {
    static const firc_mac_t zero = {{0}};
    return memcmp(mac->b, zero.b, sizeof(zero.b)) == 0;
}

bool firc_kn_policy_map_mac(const firc_kn_policy_map_t *m, const firc_ip_t *client, firc_mac_t *mac) {
    if (m == NULL || client == NULL || client->len == 0 || mac == NULL) { return false; }
    const host_t *h = host_at(m, client);
    if (h == NULL || mac_is_zero(&h->mac)) { return false; }
    *mac = h->mac;
    return true;
}

static const char *host_effective_policy(const firc_kn_policy_map_t *m, const host_t *h, bool *from_segment) {
    bool unused = false;
    if (from_segment == NULL) { from_segment = &unused; }
    *from_segment = false;
    if (h->rc == RC_NAMED) { return h->rc_policy; }
    if (h->rc == RC_DEFAULT) { return ""; }
    const char *seg = "";
    for (size_t i = h->first; i < h->first + h->n_addrs && seg[0] == '\0'; i++) {
        seg = segment_policy(m, &m->members[i].addr);
    }
    if (h->policy[0] != '\0') {
        *from_segment = seg[0] != '\0' && (h->rc == RC_CONFORM || strcmp(seg, h->policy) == 0);
        return h->policy;
    }
    *from_segment = seg[0] != '\0';
    return seg;
}

/* Skips addresses another host holds now; an allow stays within the device window (in_window). */
static void emit_host(const firc_kn_policy_map_t *m, const host_t *h, bool deny, firc_devsel_addr_fn fn, void *ud) {
    size_t n = deny || h->n_addrs < FIRC_DEVSEL_MAX_DEVICE_ADDRS ? h->n_addrs : FIRC_DEVSEL_MAX_DEVICE_ADDRS;
    for (size_t i = h->first; i < h->first + n; i++) {
        if (host_at(m, &m->members[i].addr) != h) { continue; }
        fn(&m->members[i].addr, ud);
    }
}

/* An allow past the first FIRC_DEVSEL_MAX_DEVICE_ADDRS addresses would route a device the selector never named. */
static bool in_window(const firc_kn_policy_map_t *m, const host_t *h, const firc_ip_t *net, uint8_t prefix) {
    size_t n = h->n_addrs < FIRC_DEVSEL_MAX_DEVICE_ADDRS ? h->n_addrs : FIRC_DEVSEL_MAX_DEVICE_ADDRS;
    for (size_t i = h->first; i < h->first + n; i++) {
        if (firc_devsel_prefix_covers(net, prefix, &m->members[i].addr)) { return true; }
    }
    return false;
}

firc_err_t firc_kn_policy_map_hosts_in(const firc_kn_policy_map_t *m, const firc_ip_t *net, uint8_t prefix,
                                       bool deny, firc_devsel_addr_fn fn, void *ud) {
    if (m == NULL || net == NULL || net->len == 0 || fn == NULL || m->n_hosts == 0) { return FIRC_OK; }
    if (!deny && prefix == net->len * 8u) {
        const host_t *h = host_at(m, net);
        if (h != NULL && in_window(m, h, net, prefix)) { emit_host(m, h, false, fn, ud); }
        return FIRC_OK;
    }
    /* A prefix or a deny walks every member: the index holds one host per address. */
    bool *seen = calloc(m->n_hosts, sizeof(*seen));
    if (seen == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < m->n_members; i++) {
        if (!firc_devsel_prefix_covers(net, prefix, &m->members[i].addr)) { continue; }
        const host_t *h = deny ? &m->hosts[m->members[i].host - 1] : host_at(m, &m->members[i].addr);
        if (h == NULL || seen[h - m->hosts]) { continue; }
        seen[h - m->hosts] = true;
        if (!deny && !in_window(m, h, net, prefix)) { continue; }
        emit_host(m, h, deny, fn, ud);
    }
    free(seen);
    return FIRC_OK;
}

firc_err_t firc_kn_policy_map_policy_hosts(const firc_kn_policy_map_t *m, const char *policy, bool deny,
                                           firc_devsel_addr_fn fn, void *ud) {
    if (m == NULL || policy == NULL || policy[0] == '\0' || fn == NULL) { return FIRC_OK; }
    const char *name = internal_name(m, policy);
    for (size_t i = 0; i < m->n_hosts; i++) {
        const host_t *h = &m->hosts[i];
        if (h->n_addrs == 0 || strcmp(host_effective_policy(m, h, NULL), name) != 0) { continue; }
        emit_host(m, h, deny, fn, ud);
    }
    return FIRC_OK;
}

static bool segments_overlap(const segment_t *a, const segment_t *b) {
    return (a->net & b->mask) == b->net || (b->net & a->mask) == a->net;
}

/* A segment is written only if every client on it is in the policy; otherwise the firmware mark alone matches. */
static bool segment_is_policy_s_alone(const firc_kn_policy_map_t *m, size_t i, const char *name) {
    const segment_t *s = &m->segments[i];
    for (size_t j = 0; j < i; j++) {
        if (strcmp(m->segments[j].policy, name) != 0 && segments_overlap(&m->segments[j], s)) { return false; }
    }
    for (size_t k = 0; k < m->n_members; k++) {
        const firc_ip_t *a = &m->members[k].addr;
        if (a->len != 4) { continue; }
        uint32_t v = (uint32_t)a->b[0] << 24 | (uint32_t)a->b[1] << 16 | (uint32_t)a->b[2] << 8 | a->b[3];
        if ((v & s->mask) != s->net) { continue; }
        const host_t *h = host_at(m, a);
        if (h == NULL || strcmp(host_effective_policy(m, h, NULL), name) != 0) { return false; }
    }
    return true;
}

static void policy_nets(const firc_kn_policy_map_t *m, const char *policy, bool deny, firc_devsel_net_fn fn,
                        void *ud, size_t *skipped) {
    if (m == NULL || policy == NULL || policy[0] == '\0' || fn == NULL) { return; }
    const char *name = internal_name(m, policy);
    for (size_t i = 0; i < m->n_segments; i++) {
        const segment_t *s = &m->segments[i];
        if (strcmp(s->policy, name) != 0) { continue; }
        if (!segment_is_policy_s_alone(m, i, name)) {
            if (!deny) { (*skipped)++; }
            continue;
        }
        firc_ip_t net = {{(uint8_t)(s->net >> 24), (uint8_t)(s->net >> 16), (uint8_t)(s->net >> 8), (uint8_t)s->net},
                         4};
        uint8_t prefix = 0;
        for (uint32_t k = s->mask; k != 0; k <<= 1) { prefix++; }
        fn(&net, prefix, ud);
    }
}

firc_err_t firc_kn_policy_map_policy_nets(const firc_kn_policy_map_t *m, const char *policy, bool deny,
                                          firc_devsel_net_fn fn, void *ud) {
    size_t skipped = 0;
    policy_nets(m, policy, deny, fn, ud, &skipped);
    return FIRC_OK;
}

static bool host_json_fill(cJSON *o, const firc_kn_policy_map_t *m, size_t idx) {
    const host_t *h = &m->hosts[idx];
    char mac[18];
    firc_mac_format(&h->mac, mac);
    if (cJSON_AddStringToObject(o, "mac", mac) == NULL || cJSON_AddStringToObject(o, "name", h->name) == NULL ||
        cJSON_AddStringToObject(o, "ip", h->ip) == NULL) {
        return false;
    }
    cJSON *ip6 = cJSON_AddArrayToObject(o, "ip6");
    if (ip6 == NULL) { return false; }
    for (size_t i = h->first; i < h->first + h->n_addrs; i++) {
        const member_t *e = &m->members[i];
        if (e->addr.len != 16) { continue; }
        char s[INET6_ADDRSTRLEN];
        if (inet_ntop(AF_INET6, e->addr.b, s, sizeof(s)) == NULL) { continue; }
        cJSON *str = cJSON_CreateString(s);
        if (str == NULL || !cJSON_AddItemToArray(ip6, str)) {
            cJSON_Delete(str);
            return false;
        }
    }
    bool from_segment = false;
    const char *policy = host_effective_policy(m, h, &from_segment);
    return cJSON_AddBoolToObject(o, "active", h->active) != NULL &&
           cJSON_AddBoolToObject(o, "registered", h->registered) != NULL &&
           cJSON_AddStringToObject(o, "policy", policy) != NULL &&
           cJSON_AddBoolToObject(o, "policyFromSegment", from_segment) != NULL;
}

cJSON *firc_kn_policy_map_hosts_json(const firc_kn_policy_map_t *m) {
    cJSON *arr = cJSON_CreateArray();
    if (arr == NULL || m == NULL) { return arr; }
    for (size_t i = 0; i < m->n_hosts; i++) {
        if (mac_is_zero(&m->hosts[i].mac)) { continue; }
        cJSON *o = cJSON_CreateObject();
        if (o == NULL || !cJSON_AddItemToArray(arr, o)) {
            cJSON_Delete(o);
            cJSON_Delete(arr);
            return NULL;
        }
        if (!host_json_fill(o, m, i)) {
            cJSON_Delete(arr);
            return NULL;
        }
    }
    return arr;
}

bool firc_kn_policy_map_knows(const firc_kn_policy_map_t *m, const char *policy) {
    if (m == NULL || policy == NULL || policy[0] == '\0') { return false; }
    for (size_t i = 0; i < m->n_aliases; i++) {
        if (strcmp(m->aliases[i].description, policy) == 0 || strcmp(m->aliases[i].policy, policy) == 0) { return true; }
    }
    for (size_t i = 0; i < m->n_owned; i++) {
        if (strcmp(m->hosts[m->owned[i]].policy, policy) == 0) { return true; }
    }
    return false;
}

bool firc_kn_policy_map_has(const firc_kn_policy_map_t *m, const char *policy, const firc_ip_t *client) {
    if (m == NULL || policy == NULL || policy[0] == '\0' || client == NULL || client->len == 0) { return false; }
    const char *name = internal_name(m, policy);
    const host_t *h = host_at(m, client);
    const char *eff = h != NULL ? host_effective_policy(m, h, NULL) : segment_policy(m, client);
    return eff[0] != '\0' && strcmp(eff, name) == 0;
}

size_t firc_kn_policy_map_count(const firc_kn_policy_map_t *m, const char *policy) {
    if (m == NULL || policy == NULL) { return 0; }
    const char *name = internal_name(m, policy);
    if (name[0] == '\0') { return 0; }
    size_t n = 0;
    for (size_t i = 0; i < m->n_hosts; i++) {
        const host_t *h = &m->hosts[i];
        if (h->n_addrs > 0 && strcmp(host_effective_policy(m, h, NULL), name) == 0) { n++; }
    }
    return n;
}

uint32_t firc_kn_policy_map_seed_for_test(const firc_kn_policy_map_t *m) { return m->seed; }

size_t firc_kn_policy_map_home_slots_for_test(const firc_kn_policy_map_t *m, size_t *slots) {
    *slots = m->index_mask + 1;
    bool *home = calloc(*slots, sizeof(*home));
    if (home == NULL) { return 0; }
    size_t n = 0;
    for (size_t i = 0; i < m->n_members; i++) {
        size_t h = addr_hash(m, &m->members[i].addr) & m->index_mask;
        if (!home[h]) { n++; }
        home[h] = true;
    }
    free(home);
    return n;
}

static bool str_same(const char *a, const char *b) { return strcmp(a, b) == 0; }

/* The index and its seed are not compared (how, not what); marks are. */
static bool map_same(const firc_kn_policy_map_t *a, const firc_kn_policy_map_t *b) {
    if (a->n_members != b->n_members || a->n_hosts != b->n_hosts || a->n_aliases != b->n_aliases ||
        a->n_segments != b->n_segments) {
        return false;
    }
    for (size_t i = 0; i < a->n_members; i++) {
        const member_t *x = &a->members[i], *y = &b->members[i];
        if (x->host != y->host || x->active != y->active || !same_addr(&x->addr, &y->addr) ||
            !str_same(x->policy, y->policy)) {
            return false;
        }
    }
    for (size_t i = 0; i < a->n_hosts; i++) {
        const host_t *x = &a->hosts[i], *y = &b->hosts[i];
        if (memcmp(x->mac.b, y->mac.b, sizeof(x->mac.b)) != 0 || x->active != y->active ||
            x->registered != y->registered || x->rc != y->rc ||
            (x->rc == RC_NAMED && !str_same(x->rc_policy, y->rc_policy)) || x->first != y->first ||
            x->n_addrs != y->n_addrs ||
            !str_same(x->name, y->name) || !str_same(x->policy, y->policy) || !str_same(x->ip, y->ip)) {
            return false;
        }
    }
    for (size_t i = 0; i < a->n_aliases; i++) {
        if (!str_same(a->aliases[i].description, b->aliases[i].description) ||
            !str_same(a->aliases[i].policy, b->aliases[i].policy)) {
            return false;
        }
    }
    for (size_t i = 0; i < a->n_segments; i++) {
        if (a->segments[i].net != b->segments[i].net || a->segments[i].mask != b->segments[i].mask ||
            !str_same(a->segments[i].policy, b->segments[i].policy)) {
            return false;
        }
    }
    if (a->n_marks != b->n_marks) { return false; }
    for (size_t i = 0; i < a->n_marks; i++) {
        if (a->marks[i].mark != b->marks[i].mark || !str_same(a->marks[i].policy, b->marks[i].policy)) {
            return false;
        }
    }
    return true;
}

/* The loop thread reads live without a lock; maps it replaces go to retired for the refresher to free. */
struct firc_kn_policies {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    firc_kn_policy_map_t *pending;
    bool published;
    _Atomic bool has_pending;
    firc_kn_policy_map_t *live;
    firc_kn_policy_map_t *retired;
    const firc_kn_policy_map_t *newest;
    char *base_url;
    firc_kn_rci_get_fn get;
    void *get_ud;
    unsigned refresh_secs;
    bool running, stop;
    bool in_round, wake;
    uint64_t rounds;
    uint64_t round_start_ms;
    uint64_t round_end_ms;
    /* Threads inside wait_first: stop must not free the struct under them. */
    unsigned n_waiters;
    pthread_t th;
    char warned[8][64];
    size_t n_warned;
    char mark_warned[8][64];
    size_t n_mark_warned;
    char segment_warned[8][64];
    size_t n_segment_warned;
    firc_kn_policies_changed_fn on_change;
    void *on_change_ud;
    bool clash_said;
    firc_logrep_t marks_failing;
    char *marks_list;
    mark_t *good_marks;
    size_t n_good_marks;
    bool announce_owed;
};

/* Collects a pending map; loop thread only, no lock unless has_pending is set. */
static const firc_kn_policy_map_t *current(firc_kn_policies_t *p) {
    if (atomic_load_explicit(&p->has_pending, memory_order_relaxed)) {
        pthread_mutex_lock(&p->mu);
        firc_kn_policy_map_t *m = p->pending;
        p->pending = NULL;
        atomic_store_explicit(&p->has_pending, false, memory_order_relaxed);
        if (m != NULL) {
            if (p->live != NULL) { /* freed by the refresher, never the loop */
                p->live->next_retired = p->retired;
                p->retired = p->live;
            }
            p->live = m;
        }
        pthread_mutex_unlock(&p->mu);
    }
    return p->live;
}

static void free_retired(firc_kn_policy_map_t *m) {
    while (m != NULL) {
        firc_kn_policy_map_t *next = m->next_retired;
        firc_kn_policy_map_free(m);
        m = next;
    }
}

typedef struct {
    const char *base_url;
} rci_t;

static firc_err_t rci_get(void *ud, const char *path, char **body) {
    return firc_kn_rci_get(((const rci_t *)ud)->base_url, path, body);
}

static firc_err_t add_rc_hosts(firc_kn_policy_map_t *m, const cJSON *root) {
    const cJSON *list = cJSON_IsObject(root) ? cJSON_GetObjectItemCaseSensitive(root, "host") : NULL;
    bool empty = cJSON_IsArray(root) && cJSON_GetArraySize(root) == 0;
    if (m == NULL || (!cJSON_IsObject(root) && !empty) || (list != NULL && !cJSON_IsArray(list))) {
        return FIRC_ERR_PROTO;
    }
    firc_err_t err = FIRC_OK;
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, list) {
        const cJSON *mac_j = cJSON_GetObjectItemCaseSensitive(e, "mac");
        firc_mac_t mac;
        if (!cJSON_IsString(mac_j) || !firc_mac_parse(mac_j->valuestring, &mac)) { continue; }
        const char *policy = string_or(e, "policy", "");
        rc_entry_t kind = policy[0] != '\0'                                              ? RC_NAMED
                          : cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(e, "conform")) ? RC_CONFORM
                                                                                         : RC_DEFAULT;
        for (size_t i = 0; i < m->n_hosts && err == FIRC_OK; i++) {
            host_t *h = &m->hosts[i];
            if (memcmp(h->mac.b, mac.b, sizeof(mac.b)) != 0) { continue; }
            h->rc = kind;
            free(h->rc_policy);
            h->rc_policy = kind == RC_NAMED ? strdup(policy) : NULL;
            if (kind == RC_NAMED && h->rc_policy == NULL) { err = FIRC_ERR_NOMEM; }
        }
        if (err != FIRC_OK) { break; }
    }
    return err;
}

static firc_err_t fetch_segments(firc_kn_rci_get_fn get, void *ud, firc_kn_policy_map_t *m, const char **at) {
    char *rc = NULL;
    *at = "/rci/show/rc/ip/hotspot";
    firc_err_t err = get(ud, *at, &rc);
    if (err != FIRC_OK) { return err; }
    cJSON *root = cJSON_Parse(rc);
    free(rc);
    err = add_rc_hosts(m, root);
    firc_kn_segment_t seg[32];
    size_t n = err == FIRC_OK ? segments_of(root, seg, 32) : 0;
    cJSON_Delete(root);
    if (err != FIRC_OK || n == 0) { return err; }
    /* One read of every interface, looked up by id: an id can hold a '/'. */
    char *body = NULL;
    *at = "/rci/show/interface";
    err = get(ud, *at, &body);
    if (err != FIRC_OK) { return err; }
    err = firc_kn_policy_map_add_segments(m, seg, n, body);
    free(body);
    return err;
}

static firc_err_t fetch_with(firc_kn_rci_get_fn get, void *ud, const char *known, firc_kn_policy_map_t **out,
                             firc_err_t *marks_err, bool *asked, char **list, const char **at) {
    *out = NULL;
    if (marks_err != NULL) { *marks_err = FIRC_OK; }
    char *hosts = NULL, *policies = NULL, *marks = NULL;
    *at = "/rci/show/ip/hotspot";
    firc_err_t err = get(ud, *at, &hosts);
    /* Hosts, policy list and segments are all-or-nothing: a partial map would misroute whole sets of clients. */
    if (err == FIRC_OK) {
        *at = "/rci/show/rc/ip/policy";
        err = get(ud, *at, &policies);
    }
    bool ask = err == FIRC_OK && (known == NULL || strcmp(known, policies) != 0);
    /* Marks are not all-or-nothing: without them the round still refreshes hosts and segments. */
    firc_err_t merr = ask ? get(ud, "/rci/show/ip/policy", &marks) : FIRC_OK;
    if (err == FIRC_OK) {
        *at = "/rci/show/ip/hotspot and /rci/show/rc/ip/policy";
        err = firc_kn_policy_map_parse(hosts, policies, out);
    }
    if (err == FIRC_OK && ask && merr == FIRC_OK) {
        merr = firc_kn_policy_map_add_marks(*out, marks);
        if (merr == FIRC_ERR_NOMEM) {
            *at = "/rci/show/ip/policy";
            err = merr;
        }
    }
    if (marks_err != NULL) { *marks_err = merr; }
    if (asked != NULL) { *asked = ask; }
    free(hosts);
    free(marks);
    if (err == FIRC_OK) { err = fetch_segments(get, ud, *out, at); }
    if (err == FIRC_OK && list != NULL) {
        *list = policies;
        policies = NULL;
    }
    free(policies);
    if (err != FIRC_OK && *out != NULL) {
        firc_kn_policy_map_free(*out);
        *out = NULL;
    }
    return err;
}

firc_err_t firc_kn_policy_map_fetch_for_test(firc_kn_rci_get_fn get, void *ud, firc_kn_policy_map_t **out) {
    const char *at = "";
    return fetch_with(get, ud, NULL, out, NULL, NULL, NULL, &at);
}

static void marks_free(mark_t *v, size_t n) {
    for (size_t i = 0; i < n; i++) { free(v[i].policy); }
    free(v);
}

static bool listed(const firc_kn_policy_map_t *m, const char *policy) {
    for (size_t i = 0; i < m->n_aliases; i++) {
        if (strcmp(m->aliases[i].policy, policy) == 0) { return true; }
    }
    return false;
}

static bool keep_marks(firc_kn_policies_t *p, const firc_kn_policy_map_t *m) {
    marks_free(p->good_marks, p->n_good_marks);
    p->good_marks = NULL;
    p->n_good_marks = 0;
    if (m->n_marks == 0) { return m->n_aliases == 0; }
    mark_t *v = calloc(m->n_marks, sizeof(*v));
    if (v == NULL) { return false; }
    for (size_t i = 0; i < m->n_marks; i++) {
        v[i].policy = strdup(m->marks[i].policy);
        v[i].mark = m->marks[i].mark;
        if (v[i].policy == NULL) {
            marks_free(v, i);
            return false;
        }
    }
    p->good_marks = v;
    p->n_good_marks = m->n_marks;
    return true;
}

/* Carry the last marks only for policies the fresh list still names. */
static firc_err_t carry_marks(const firc_kn_policies_t *p, firc_kn_policy_map_t *m) {
    for (size_t i = 0; i < p->n_good_marks; i++) {
        if (!listed(m, p->good_marks[i].policy)) { continue; }
        firc_err_t err = add_mark(m, p->good_marks[i].policy, p->good_marks[i].mark);
        if (err != FIRC_OK) { return err; }
    }
    find_clash(m);
    return FIRC_OK;
}

static firc_err_t refresh_round(firc_kn_policies_t *p, firc_kn_rci_get_fn get, void *ud, const char **at) {
    firc_kn_policy_map_t *m = NULL;
    firc_err_t merr = FIRC_OK;
    bool asked = false;
    char *list = NULL;
    /* ndm walks every route of both families to answer the marks document, so it is read only for a new list. */
    firc_err_t err = fetch_with(get, ud, p->marks_list, &m, &merr, &asked, &list, at);
    if (err != FIRC_OK) { return err; }
    bool held = asked && merr == FIRC_OK && keep_marks(p, m);
    if (asked) {
        free(p->marks_list);
        p->marks_list = held ? list : NULL;
    }
    if (!held) { free(list); }
    if (!asked || merr != FIRC_OK) {
        /* Without marks a deny would MARK unlisted IPv6 clients of the policy. */
        err = carry_marks(p, m);
        if (err != FIRC_OK) {
            firc_kn_policy_map_free(m);
            return err;
        }
    }
    if (asked && merr != FIRC_OK) {
        firc_logrep_fail(&p->marks_failing, FIRC_LOG_WARN, NULL,
                         "keenetic policies: the policy marks could not be read (%s); the last marks read are kept "
                         "until they can",
                         firc_err_str(merr));
    } else if (asked) {
        (void)firc_logrep_ok(&p->marks_failing, NULL, "keenetic policies: the policy marks");
    }
    (void)firc_kn_policies_offer(p, m);
    return FIRC_OK;
}

firc_err_t firc_kn_policies_round_for_test(firc_kn_policies_t *p, firc_kn_rci_get_fn get, void *ud) {
    const char *at = "";
    return refresh_round(p, get, ud, &at);
}

static uint64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static void *refresher(void *ud) {
    firc_kn_policies_t *p = ud;
    unsigned retry = 1;
    firc_logrep_t failing = {0};
    pthread_mutex_lock(&p->mu);
    while (!p->stop) {
        p->in_round = true;
        p->wake = false;
        p->round_start_ms = mono_ms();
        pthread_mutex_unlock(&p->mu);
        rci_t rci = {p->base_url};
        /* RCI bytes change on every read, so map_same compares parsed maps; an equal map is freed without a swap. */
        const char *at = "";
        firc_err_t err =
            p->get != NULL ? refresh_round(p, p->get, p->get_ud, &at) : refresh_round(p, rci_get, &rci, &at);
        unsigned wait;
        if (err == FIRC_OK) {
            (void)firc_logrep_ok(&failing, NULL, "keenetic policies: RCI");
            retry = 1;
            wait = p->refresh_secs;
        } else {
            /* Keep the last good map across a failure; before the first map retry fast (ndm may still be starting). */
            firc_logrep_fail(&failing, FIRC_LOG_WARN, NULL, "keenetic policies: refresh failed (%s): %s", at,
                             firc_err_str(err));
            wait = retry < p->refresh_secs ? retry : p->refresh_secs;
            if (retry < p->refresh_secs) { retry *= 2; }
        }
        pthread_mutex_lock(&p->mu);
        p->in_round = false;
        p->rounds++;
        p->round_end_ms = mono_ms();
        pthread_cond_broadcast(&p->cv);
        struct timespec until;
        clock_gettime(CLOCK_MONOTONIC, &until);
        until.tv_sec += (time_t)wait;
        while (!p->stop && !p->wake) {
            if (pthread_cond_timedwait(&p->cv, &p->mu, &until) == ETIMEDOUT) { break; }
        }
    }
    pthread_mutex_unlock(&p->mu);
    return NULL;
}

static firc_kn_policies_t *start_with(const char *base_url, unsigned refresh_secs,
                                      firc_kn_policies_changed_fn on_change, void *ud, firc_kn_rci_get_fn get,
                                      void *get_ud) {
    firc_kn_policies_t *p = calloc(1, sizeof(*p));
    if (p == NULL) { return NULL; }
    pthread_mutex_init(&p->mu, NULL);
    /* Monotonic clock: a boot-time NTP step must not stretch or skip a refresh. */
    pthread_condattr_t ca;
    pthread_condattr_init(&ca);
    pthread_condattr_setclock(&ca, CLOCK_MONOTONIC);
    pthread_cond_init(&p->cv, &ca);
    pthread_condattr_destroy(&ca);
    p->refresh_secs = refresh_secs ? refresh_secs : 30;
    p->on_change = on_change;
    p->on_change_ud = ud;
    p->get = get;
    p->get_ud = get_ud;
    if (base_url != NULL || get != NULL) {
        p->base_url = base_url != NULL ? strdup(base_url) : NULL;
        if ((base_url != NULL && p->base_url == NULL) || pthread_create(&p->th, NULL, refresher, p) != 0) {
            firc_kn_policies_stop(p);
            return NULL;
        }
        p->running = true;
    }
    return p;
}

firc_kn_policies_t *firc_kn_policies_start_notify(const char *base_url, unsigned refresh_secs,
                                                  firc_kn_policies_changed_fn on_change, void *ud) {
    return start_with(base_url, refresh_secs, on_change, ud, NULL, NULL);
}

firc_kn_policies_t *firc_kn_policies_start_for_test(firc_kn_rci_get_fn get, void *ud, unsigned refresh_secs) {
    return start_with(NULL, refresh_secs, NULL, NULL, get, ud);
}

firc_kn_policies_t *firc_kn_policies_start(const char *base_url, unsigned refresh_secs) {
    return firc_kn_policies_start_notify(base_url, refresh_secs, NULL, NULL);
}

void firc_kn_policies_stop(firc_kn_policies_t *p) {
    if (p == NULL) { return; }
    pthread_mutex_lock(&p->mu);
    p->stop = true;
    pthread_cond_broadcast(&p->cv);
    bool joinable = p->running;
    pthread_mutex_unlock(&p->mu);
    if (joinable) { pthread_join(p->th, NULL); }
    /* Waiters are not joinable, so wait for them: freeing the struct under one would be a use-after-free. */
    pthread_mutex_lock(&p->mu);
    /* Under the lock: wait_first reads it in its predicate (TSan reported a race). */
    p->running = false;
    while (p->n_waiters > 0) { pthread_cond_wait(&p->cv, &p->mu); }
    pthread_mutex_unlock(&p->mu);

    marks_free(p->good_marks, p->n_good_marks);
    free(p->marks_list);
    firc_kn_policy_map_free(p->pending);
    firc_kn_policy_map_free(p->live);
    free_retired(p->retired);
    free(p->base_url);
    pthread_cond_destroy(&p->cv);
    pthread_mutex_destroy(&p->mu);
    free(p);
}

void firc_kn_policies_swap(firc_kn_policies_t *p, firc_kn_policy_map_t *m) {
    pthread_mutex_lock(&p->mu);
    /* A map never collected was never read, so it can go now; live is not touched. */
    firc_kn_policy_map_t *old = p->pending;
    p->pending = m;
    p->newest = m;
    p->published = true;
    atomic_store_explicit(&p->has_pending, true, memory_order_relaxed);
    pthread_cond_broadcast(&p->cv);
    firc_kn_policy_map_t *retired = p->retired;
    p->retired = NULL;
    pthread_mutex_unlock(&p->mu);
    firc_kn_policy_map_free(old);
    free_retired(retired);
}

bool firc_kn_policies_offer(firc_kn_policies_t *p, firc_kn_policy_map_t *m) {
    pthread_mutex_lock(&p->mu);
    bool same = p->newest != NULL && map_same(p->newest, m);
    firc_kn_policy_map_t *retired = NULL;
    if (same) { /* still reap: a quiet table must not keep the old map forever */
        retired = p->retired;
        p->retired = NULL;
    }
    pthread_mutex_unlock(&p->mu);
    if (!same) {
        if (m->clash[0][0] != '\0' && !p->clash_said) {
            FIRC_WARN("keenetic policies: policies \"%s\" and \"%s\" have marks that agree outside firc's bits; "
                      "a selector cannot tell them apart in the packet path",
                      m->clash[0], m->clash[1]);
            p->clash_said = true;
        } else if (m->clash[0][0] == '\0') {
            p->clash_said = false;
        }
        firc_kn_policies_swap(p, m);
        /* After the hand-over, so the loop task this posts collects this map or a newer one. */
        if (p->on_change != NULL) { p->announce_owed = !p->on_change(p->on_change_ud); }
        return true;
    }
    firc_kn_policy_map_free(m);
    free_retired(retired);
    /* The last announcement was lost: announce again although nothing changed. */
    if (p->announce_owed && p->on_change != NULL) { p->announce_owed = !p->on_change(p->on_change_ud); }
    return false;
}

size_t firc_kn_policies_retired_for_test(firc_kn_policies_t *p) {
    pthread_mutex_lock(&p->mu);
    size_t n = 0;
    for (const firc_kn_policy_map_t *m = p->retired; m != NULL; m = m->next_retired) { n++; }
    pthread_mutex_unlock(&p->mu);
    return n;
}

const firc_kn_policy_map_t *firc_kn_policies_live_for_test(firc_kn_policies_t *p) { return current(p); }

bool firc_kn_policies_wait_first(firc_kn_policies_t *p, unsigned ms) {
    if (p == NULL) { return false; }
    struct timespec until;
    clock_gettime(CLOCK_MONOTONIC, &until);
    until.tv_sec += (time_t)(ms / 1000);
    until.tv_nsec += (long)(ms % 1000) * 1000000L;
    if (until.tv_nsec >= 1000000000L) {
        until.tv_sec++;
        until.tv_nsec -= 1000000000L;
    }
    pthread_mutex_lock(&p->mu);
    /* Counted before the wait and uncounted before the unlock, so stop knows who is still reading. */
    p->n_waiters++;
    while (!p->published && p->running && !p->stop) {
        if (pthread_cond_timedwait(&p->cv, &p->mu, &until) == ETIMEDOUT) { break; }
    }
    bool have = p->published;
    p->n_waiters--;
    pthread_cond_broadcast(&p->cv);
    pthread_mutex_unlock(&p->mu);
    return have;
}

size_t firc_kn_policies_list(firc_kn_policies_t *p, firc_kn_policy_info_t *out, size_t cap) {
    if (p == NULL) { return 0; }
    return firc_kn_policy_map_list(current(p), out, cap);
}

cJSON *firc_kn_policies_hosts_json(firc_kn_policies_t *p) {
    if (p == NULL) { return cJSON_CreateArray(); }
    return firc_kn_policy_map_hosts_json(current(p));
}

unsigned firc_kn_policies_waiters_for_test(firc_kn_policies_t *p) {
    pthread_mutex_lock(&p->mu);
    unsigned n = p->n_waiters;
    pthread_mutex_unlock(&p->mu);
    return n;
}

size_t firc_kn_policy_map_list(const firc_kn_policy_map_t *m, firc_kn_policy_info_t *out,
                               size_t cap) {
    if (m == NULL || out == NULL || cap == 0) { return 0; }
    size_t n = 0;

    for (size_t i = 0; i < m->n_aliases && n < cap; i++) {
        snprintf(out[n].name, sizeof(out[n].name), "%s", m->aliases[i].policy);
        const char *desc = m->aliases[i].description;
        bool has_desc = desc != NULL && strcmp(desc, m->aliases[i].policy) != 0;
        snprintf(out[n].description, sizeof(out[n].description), "%s", has_desc ? desc : "");
        out[n].devices = firc_kn_policy_map_count(m, m->aliases[i].policy);
        n++;
    }

    for (size_t i = 0; i < m->n_members && n < cap; i++) {
        /* No NULL check: add_member bumps n_members only after its strdup succeeded. */
        const char *name = m->members[i].policy;
        if (name[0] == '\0') { continue; }
        bool seen = false;
        for (size_t j = 0; j < n && !seen; j++) { seen = strcmp(out[j].name, name) == 0; }
        if (seen) { continue; }
        snprintf(out[n].name, sizeof(out[n].name), "%s", name);
        out[n].description[0] = '\0';
        out[n].devices = firc_kn_policy_map_count(m, name);
        n++;
    }
    return n;
}

static bool claim_name(char (*table)[64], size_t cap, size_t *n, const char *name, bool *last) {
    *last = false;
    for (size_t i = 0; i < *n; i++) {
        if (strcmp(table[i], name) == 0) { return false; }
    }
    if (*n == cap) { return false; }
    snprintf(table[(*n)++], 64, "%s", name);
    *last = *n == cap;
    return true;
}

static bool claim_unknown(firc_kn_policies_t *p, const char *policy, bool *last) {
    return claim_name(p->warned, sizeof(p->warned) / sizeof(p->warned[0]), &p->n_warned, policy, last);
}

static void say_unknown(firc_kn_policies_t *p, const char *policy) {
    bool last = false;
    if (!claim_unknown(p, policy, &last)) { return; }
    FIRC_WARN("keenetic policies: no policy called \"%s\" on this router; the entry matches no device", policy);
    if (last) { FIRC_WARN("keenetic policies: further unknown policy names are not reported"); }
}

bool firc_kn_policies_resolve(const char *policy, const firc_ip_t *client, void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL) { return false; }
    const firc_kn_policy_map_t *m = current(p);
    bool in = firc_kn_policy_map_has(m, policy, client);
    if (!in && m != NULL && !firc_kn_policy_map_knows(m, policy)) { say_unknown(p, policy); }
    return in;
}

size_t firc_kn_policies_device(const firc_ip_t *client, firc_ip_t *out, size_t cap, firc_mac_t *mac, void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL) { return 0; }
    const firc_kn_policy_map_t *m = current(p);
    if (m == NULL || client == NULL || client->len == 0) { return 0; }
    const host_t *h = host_at(m, client);
    if (h == NULL) { return 0; }
    if (mac != NULL && !mac_is_zero(&h->mac)) { *mac = h->mac; }
    return host_others(m, h, client, out, cap);
}

bool firc_kn_policies_read(void *ud) {
    firc_kn_policies_t *p = ud;
    return p == NULL || current(p) != NULL;
}

bool firc_kn_policies_mark(const char *policy, uint32_t *mark, void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL || policy == NULL || policy[0] == '\0') { return false; }
    const firc_kn_policy_map_t *m = current(p);
    if (m == NULL) { return false; } /* no table yet: nothing to say about any name */
    if (firc_kn_policy_map_mark(m, policy, mark)) { return true; }
    bool last = false;
    if (!firc_kn_policy_map_knows(m, policy)) {
        say_unknown(p, policy);
    } else if (claim_name(p->mark_warned, sizeof(p->mark_warned) / sizeof(p->mark_warned[0]), &p->n_mark_warned,
                          policy, &last)) {
        FIRC_WARN("keenetic policies: policy \"%s\" has no firmware mark; its entry marks no packet", policy);
    }
    return false;
}

firc_err_t firc_kn_policies_hosts_in(const firc_ip_t *net, uint8_t prefix, bool deny, firc_devsel_addr_fn fn,
                                     void *fn_ud, void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL) { return FIRC_OK; }
    return firc_kn_policy_map_hosts_in(current(p), net, prefix, deny, fn, fn_ud);
}

firc_err_t firc_kn_policies_policy_hosts(const char *policy, bool deny, firc_devsel_addr_fn fn, void *fn_ud,
                                         void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL) { return FIRC_OK; }
    return firc_kn_policy_map_policy_hosts(current(p), policy, deny, fn, fn_ud);
}

firc_err_t firc_kn_policies_policy_nets(const char *policy, bool deny, firc_devsel_net_fn fn, void *fn_ud, void *ud) {
    firc_kn_policies_t *p = ud;
    if (p == NULL) { return FIRC_OK; }
    size_t skipped = 0;
    policy_nets(current(p), policy, deny, fn, fn_ud, &skipped);
    bool last = false;
    if (skipped > 0 && claim_name(p->segment_warned, sizeof(p->segment_warned) / sizeof(p->segment_warned[0]),
                                  &p->n_segment_warned, policy, &last)) {
        FIRC_WARN("keenetic policies: %zu segment%s bound to policy \"%s\" %s not written for an allow entry: "
                  "the answer puts a client there in another policy or in none, so a client the table does not "
                  "list is matched by the firmware's mark alone",
                  skipped, skipped == 1 ? "" : "s", policy, skipped == 1 ? "is" : "are");
    }
    return FIRC_OK;
}

uint64_t firc_kn_policies_wake(firc_kn_policies_t *p, unsigned fresh_ms) {
    if (p == NULL) { return 0; }
    pthread_mutex_lock(&p->mu);
    uint64_t want = 0;
    uint64_t now = mono_ms();
    if (p->running && !p->stop && (p->rounds == 0 || now - p->round_end_ms > fresh_ms)) {
        if (p->in_round && now - p->round_start_ms <= fresh_ms) {
            want = p->rounds + 1u;
        } else {
            p->wake = true;
            pthread_cond_broadcast(&p->cv);
            want = p->rounds + (p->in_round ? 2u : 1u);
        }
    }
    pthread_mutex_unlock(&p->mu);
    return want;
}

uint64_t firc_kn_policies_rounds(firc_kn_policies_t *p) {
    if (p == NULL) { return 0; }
    pthread_mutex_lock(&p->mu);
    uint64_t n = p->rounds;
    pthread_mutex_unlock(&p->mu);
    return n;
}
