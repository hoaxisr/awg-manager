#define PCRE2_CODE_UNIT_WIDTH 8

#include "firc/match.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <pcre2.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/log.h"

/* What a name can be on the wire, which is what a pattern has to fit. */
#define FIRC_DNS_LABEL_MAX 63
#define FIRC_DNS_NAME_MAX_TEXT 253

/* PCRE2 hardening limits. */
#define FIRC_REGEX_MATCH_LIMIT 1000000
#define FIRC_REGEX_DEPTH_LIMIT 10000

typedef enum rule_kind {
    RK_DOMAIN,
    RK_NAMESPACE,
    RK_WILDCARD,
    RK_REGEX,
    RK_NEVER, /* subnet/subnet6/unknown: never matches */
} rule_kind_t;

struct firc_rule_matcher {
    rule_kind_t kind;
    char *rule;
    size_t rule_len;
    pcre2_code *re;               /* RK_REGEX only, NULL if compile failed */
    pcre2_match_data *md;
    pcre2_match_context *mctx;
    bool compile_ok;
};

static rule_kind_t kind_of(const char *type)
{
    if (strcmp(type, FIRC_RULE_DOMAIN) == 0) {
        return RK_DOMAIN;
    }
    if (strcmp(type, FIRC_RULE_NAMESPACE) == 0) {
        return RK_NAMESPACE;
    }
    if (strcmp(type, FIRC_RULE_WILDCARD) == 0) {
        return RK_WILDCARD;
    }
    if (strcmp(type, FIRC_RULE_REGEX) == 0) {
        return RK_REGEX;
    }
    return RK_NEVER;
}

/* folds a rule's name like a query is folded: ASCII-only case fold, so byte comparison stays valid */
static void fold_like_a_query(char *p, size_t *len) {
    size_t n = *len;
    size_t start = 0;
    while (start < n && (p[start] == ' ' || p[start] == '\t')) { start++; }
    while (n > start && (p[n - 1] == ' ' || p[n - 1] == '\t')) { n--; }
    if (start > 0) {
        memmove(p, p + start, n - start);
        n -= start;
    }
    /* the query loses its trailing dot before matching; keeping it here would stop every match */
    if (n > 0 && p[n - 1] == '.') { n--; }
    p[n] = '\0';
    for (size_t i = 0; i < n; i++) {
        if (p[i] >= 'A' && p[i] <= 'Z') { p[i] = (char)(p[i] - 'A' + 'a'); }
    }
    *len = n;
}

size_t firc_rule_fold_name(char *p)
{
    size_t n = strlen(p);
    fold_like_a_query(p, &n);
    return n;
}

firc_rule_matcher_t *firc_rule_matcher_new(const char *type, const char *rule)
{
    firc_rule_matcher_t *m = calloc(1, sizeof(*m));
    if (m == NULL) {
        return NULL;
    }
    m->kind = kind_of(type);
    m->rule = strdup(rule);
    if (m->rule == NULL) {
        free(m);
        return NULL;
    }
    m->rule_len = strlen(m->rule);
    /* regex matches caselessly already and treats dot/space as pattern; subnet types never reach a matcher */
    if (m->kind == RK_DOMAIN || m->kind == RK_NAMESPACE || m->kind == RK_WILDCARD) {
        fold_like_a_query(m->rule, &m->rule_len);
    }
    m->compile_ok = true;

    if (m->kind == RK_REGEX) {
        int errcode = 0;
        PCRE2_SIZE erroff = 0;
        /* compiled from m->rule (the folded copy), never from the raw rule argument */
        m->re = pcre2_compile((PCRE2_SPTR)m->rule, PCRE2_ZERO_TERMINATED,
                              PCRE2_CASELESS | PCRE2_UTF | PCRE2_UCP,
                              &errcode, &erroff, NULL);
        if (m->re == NULL) {
            PCRE2_UCHAR msg[128];
            pcre2_get_error_message(errcode, msg, sizeof(msg));
            /* DEBUG not ERROR: this ctor doubles as the usability validator; a failed compile is expected */
            FIRC_DEBUG("regex compile failed: %s (pattern %s at %zu)",
                     (char *)msg, m->rule, (size_t)erroff);
            m->compile_ok = false;
        } else {
            m->md = pcre2_match_data_create_from_pattern(m->re, NULL);
            m->mctx = pcre2_match_context_create(NULL);
            if (m->md == NULL || m->mctx == NULL) {
                firc_rule_matcher_free(m);
                return NULL;
            }
            pcre2_set_match_limit(m->mctx, FIRC_REGEX_MATCH_LIMIT);
            pcre2_set_depth_limit(m->mctx, FIRC_REGEX_DEPTH_LIMIT);
        }
    }
    return m;
}

void firc_rule_matcher_free(firc_rule_matcher_t *m)
{
    if (m == NULL) {
        return;
    }
    if (m->md != NULL) {
        pcre2_match_data_free(m->md);
    }
    if (m->mctx != NULL) {
        pcre2_match_context_free(m->mctx);
    }
    if (m->re != NULL) {
        pcre2_code_free(m->re);
    }
    free(m->rule);
    free(m);
}

bool firc_rule_matcher_ok(const firc_rule_matcher_t *m)
{
    return m->compile_ok;
}

static bool namespace_match(const char *rule, size_t rule_len,
                            const char *domain, size_t domain_len)
{
    if (domain_len == rule_len && memcmp(domain, rule, rule_len) == 0) {
        return true;
    }
    if (domain_len < rule_len + 1) {
        return false;
    }
    return domain[domain_len - rule_len - 1] == '.' &&
           memcmp(domain + domain_len - rule_len, rule, rule_len) == 0;
}

bool firc_rule_matcher_match(firc_rule_matcher_t *m, const char *domain)
{
    size_t domain_len = strlen(domain);
    switch (m->kind) {
    case RK_DOMAIN:
        return domain_len == m->rule_len &&
               memcmp(domain, m->rule, domain_len) == 0;
    case RK_NAMESPACE:
        return namespace_match(m->rule, m->rule_len, domain, domain_len);
    case RK_WILDCARD:
        return firc_wildcard_match(m->rule, domain);
    case RK_REGEX: {
        if (m->re == NULL) {
            return false;
        }
        int rc = pcre2_match(m->re, (PCRE2_SPTR)domain, domain_len, 0, 0,
                             m->md, m->mctx);
        return rc >= 0;
    }
    case RK_NEVER:
        return false;
    }
    return false;
}

/* Open-addressing set of names, used for both the exact-name set and the namespace-root set. */
typedef struct name_slot {
    const char *s; /* NULL: empty */
    uint32_t hash;
} name_slot_t;

typedef struct name_set {
    name_slot_t *slots;
    size_t cap; /* power of two, or 0 */
    size_t len;
} name_set_t;

static uint32_t hash_n(const char *s, size_t n)
{
    uint32_t h = UINT32_C(2166136261);
    for (size_t i = 0; i < n; i++) {
        h ^= (unsigned char)s[i];
        h *= UINT32_C(16777619);
    }
    return h;
}

static firc_err_t name_set_grow(name_set_t *set)
{
    size_t new_cap = set->cap == 0 ? 16 : set->cap * 2;
    name_slot_t *ns = calloc(new_cap, sizeof(*ns));
    if (ns == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < set->cap; i++) {
        if (set->slots[i].s == NULL) {
            continue;
        }
        size_t idx = set->slots[i].hash & (new_cap - 1);
        while (ns[idx].s != NULL) {
            idx = (idx + 1) & (new_cap - 1);
        }
        ns[idx] = set->slots[i];
    }
    free(set->slots);
    set->slots = ns;
    set->cap = new_cap;
    return FIRC_OK;
}

/* *fresh is true when s was new; a duplicate is not stored twice and not an error. */
static firc_err_t name_set_insert(name_set_t *set, const char *s, bool *fresh)
{
    *fresh = false;
    if (set->cap == 0 || set->len * 4 >= set->cap * 3) {
        firc_err_t err = name_set_grow(set);
        if (err != FIRC_OK) {
            return err;
        }
    }
    size_t n = strlen(s);
    uint32_t h = hash_n(s, n);
    size_t idx = h & (set->cap - 1);
    while (set->slots[idx].s != NULL) {
        if (set->slots[idx].hash == h && strcmp(set->slots[idx].s, s) == 0) {
            return FIRC_OK;
        }
        idx = (idx + 1) & (set->cap - 1);
    }
    set->slots[idx].s = s;
    set->slots[idx].hash = h;
    set->len++;
    *fresh = true;
    return FIRC_OK;
}

static bool name_set_has(const name_set_t *set, const char *s, size_t n)
{
    if (set->len == 0) {
        return false;
    }
    uint32_t h = hash_n(s, n);
    size_t idx = h & (set->cap - 1);
    while (set->slots[idx].s != NULL) {
        if (set->slots[idx].hash == h && strncmp(set->slots[idx].s, s, n) == 0 &&
            set->slots[idx].s[n] == '\0') {
            return true;
        }
        idx = (idx + 1) & (set->cap - 1);
    }
    return false;
}

struct firc_matcher {
    name_set_t exact;
    name_set_t ns;
    bool ns_empty_rule; /* namespace rule "": matches "" and names ending in '.' */
    char **owned; /* the copies this matcher made; every one, borrowing or not */
    size_t n_owned, cap_owned;
    firc_rule_matcher_t **scan; /* wildcard + regex */
    size_t n_scan;
};

firc_matcher_t *firc_matcher_new(void)
{
    return calloc(1, sizeof(firc_matcher_t));
}

void firc_matcher_free(firc_matcher_t *m)
{
    if (m == NULL) {
        return;
    }
    free(m->exact.slots);
    free(m->ns.slots);
    for (size_t i = 0; i < m->n_owned; i++) {
        free(m->owned[i]);
    }
    free(m->owned);
    for (size_t i = 0; i < m->n_scan; i++) {
        firc_rule_matcher_free(m->scan[i]);
    }
    free(m->scan);
    free(m);
}

/* Is `s` already what firc_rule_fold_name would make of it? */
static bool is_folded(const char *s)
{
    size_t n = strlen(s);
    if (n == 0) {
        return true;
    }
    if (s[0] == ' ' || s[0] == '\t' || s[n - 1] == ' ' || s[n - 1] == '\t' || s[n - 1] == '.') {
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        if (s[i] >= 'A' && s[i] <= 'Z') {
            return false;
        }
    }
    return true;
}

static firc_err_t matcher_own(firc_matcher_t *m, char *copy)
{
    if (m->n_owned == m->cap_owned) {
        size_t cap = m->cap_owned == 0 ? 16 : m->cap_owned * 2;
        char **grown = realloc(m->owned, cap * sizeof(*grown));
        if (grown == NULL) {
            return FIRC_ERR_NOMEM;
        }
        m->owned = grown;
        m->cap_owned = cap;
    }
    m->owned[m->n_owned++] = copy;
    return FIRC_OK;
}

/* folds a group's rule here too -- folding only in firc_rule_matcher_new left "Google.COM" matching nothing */
static firc_err_t add_name(firc_matcher_t *m, name_set_t *set, const char *rule, bool borrow)
{
    char *copy = NULL;
    const char *text = rule;
    if (!borrow || !is_folded(rule)) {
        copy = strdup(rule);
        if (copy == NULL) {
            return FIRC_ERR_NOMEM;
        }
        firc_rule_fold_name(copy);
        text = copy;
    }
    bool fresh = false;
    firc_err_t err = name_set_insert(set, text, &fresh);
    if (err == FIRC_OK && fresh && copy != NULL) {
        err = matcher_own(m, copy);
        if (err != FIRC_OK) {
            /* set has no general delete; safe here only because this slot was the last one written */
            for (size_t i = 0; i < set->cap; i++) {
                if (set->slots[i].s == text) {
                    set->slots[i].s = NULL;
                    set->len--;
                    break;
                }
            }
        }
    }
    if (err != FIRC_OK || !fresh) {
        free(copy);
    }
    return err;
}

static firc_err_t matcher_add(firc_matcher_t *m, const char *type, const char *rule, bool borrow)
{
    rule_kind_t kind = kind_of(type);
    firc_err_t add_err;
    switch (kind) {
    case RK_DOMAIN:
        return add_name(m, &m->exact, rule, borrow);
    case RK_NAMESPACE:
        if (rule[0] == '\0') {
            m->ns_empty_rule = true;
            return FIRC_OK;
        }
        return add_name(m, &m->ns, rule, borrow);
    case RK_WILDCARD:
    case RK_REGEX: {
        firc_rule_matcher_t *rm = firc_rule_matcher_new(type, rule);
        if (rm == NULL) {
            return FIRC_ERR_NOMEM;
        }
        /* FIRC_ERR_INVAL, not NOMEM: lets the caller tell a broken rule from out-of-memory */
        if (!firc_rule_matcher_ok(rm)) {
            firc_rule_matcher_free(rm);
            return FIRC_ERR_INVAL;
        }
        firc_rule_matcher_t **grown =
            realloc(m->scan, (m->n_scan + 1) * sizeof(*grown));
        if (grown == NULL) {
            firc_rule_matcher_free(rm);
            return FIRC_ERR_NOMEM;
        }
        m->scan = grown;
        m->scan[m->n_scan++] = rm;
        add_err = FIRC_OK;
        break;
    }
    case RK_NEVER:
        add_err = FIRC_OK; /* subnet/subnet6/unknown never match names */
        break;
    }
    return add_err;
}

firc_err_t firc_matcher_add(firc_matcher_t *m, const char *type, const char *rule)
{
    return matcher_add(m, type, rule, false);
}

firc_err_t firc_matcher_add_borrowed(firc_matcher_t *m, const char *type, const char *rule)
{
    return matcher_add(m, type, rule, true);
}

bool firc_matcher_match(firc_matcher_t *m, const char *domain)
{
    size_t len = strlen(domain);
    if (name_set_has(&m->exact, domain, len)) {
        return true;
    }
    if (m->ns_empty_rule && (len == 0 || domain[len - 1] == '.')) {
        return true;
    }
    if (m->ns.len > 0) {
        if (name_set_has(&m->ns, domain, len)) {
            return true;
        }
        for (size_t i = 0; i < len; i++) {
            if (domain[i] == '.' && name_set_has(&m->ns, domain + i + 1, len - i - 1)) {
                return true;
            }
        }
    }
    for (size_t i = 0; i < m->n_scan; i++) {
        if (firc_rule_matcher_match(m->scan[i], domain)) {
            return true;
        }
    }
    return false;
}

bool firc_rule_parse_subnet4(const char *rule, firc_ipv4_subnet_t *out) {
    char buf[256];
    memset(out, 0, sizeof(*out));
    if (snprintf(buf, sizeof(buf), "%s", rule) >= (int)sizeof(buf)) { return false; }

    char *slash = strchr(buf, '/');
    long ones = 32;
    if (slash != NULL) {
        *slash = '\0';
        /* strtol accepts a leading sign/space; require a digit first or "/-0" parses as prefix 0 */
        if (slash[1] < '0' || slash[1] > '9') { return false; }
        char *end = NULL;
        ones = strtol(slash + 1, &end, 10);
        if (end == slash + 1 || *end != '\0' || ones < 0 || ones > 32) { return false; }
    }

    struct in_addr a;
    if (inet_pton(AF_INET, buf, &a) != 1) { return false; }

    uint32_t ipval = ntohl(a.s_addr);
    uint32_t mask = (ones == 0) ? 0u : (uint32_t)(0xFFFFFFFFu << (32 - ones));
    uint32_t masked = ipval & mask;

    out->addr[0] = (uint8_t)(masked >> 24);
    out->addr[1] = (uint8_t)(masked >> 16);
    out->addr[2] = (uint8_t)(masked >> 8);
    out->addr[3] = (uint8_t)masked;
    out->cidr = (uint8_t)ones;
    return true;
}

bool firc_rule_parse_subnet6(const char *rule, firc_ipv6_subnet_t *out) {
    char buf[256];
    memset(out, 0, sizeof(*out));
    if (snprintf(buf, sizeof(buf), "%s", rule) >= (int)sizeof(buf)) { return false; }

    char *slash = strchr(buf, '/');
    long ones = 128;
    if (slash != NULL) {
        *slash = '\0';
        /* strtol accepts a leading sign/space; require a digit first or "/-0" parses as prefix 0 */
        if (slash[1] < '0' || slash[1] > '9') { return false; }
        char *end = NULL;
        ones = strtol(slash + 1, &end, 10);
        if (end == slash + 1 || *end != '\0' || ones < 0 || ones > 128) { return false; }
    }

    struct in6_addr a;
    if (inet_pton(AF_INET6, buf, &a) != 1) { return false; }

    uint8_t full_bytes = (uint8_t)(ones / 8);
    uint8_t rem_bits = (uint8_t)(ones % 8);
    for (int i = 0; i < 16; i++) {
        if (i < full_bytes) {
            out->addr[i] = a.s6_addr[i];
        } else if (i == full_bytes && rem_bits > 0) {
            uint8_t mask = (uint8_t)(0xFFu << (8 - rem_bits));
            out->addr[i] = (uint8_t)(a.s6_addr[i] & mask);
        } else {
            out->addr[i] = 0;
        }
    }
    out->cidr = (uint8_t)ones;
    return true;
}

/* ASCII only: a queried name can never be raw UTF-8 (dns/wire.c escapes/refuses it); write IDNs as xn--. */
bool firc_rule_name_pattern_is_usable(const char *rule, bool allow_glob, const char **why) {
    const char *ignored = NULL;
    if (why == NULL) { why = &ignored; }
    size_t n = strlen(rule);
    size_t start = 0;
    while (start < n && (rule[start] == ' ' || rule[start] == '\t')) { start++; }
    while (n > start && (rule[n - 1] == ' ' || rule[n - 1] == '\t')) { n--; }
    /* one trailing dot is the lost root label; stripped before the scan so a second dot still shows as empty */
    if (n > start && rule[n - 1] == '.') { n--; }
    if (n == start) {
        *why = "the pattern is empty and would match nothing";
        return false;
    }
    if (n - start > FIRC_DNS_NAME_MAX_TEXT) {
        *why = "a name is at most 253 characters";
        return false;
    }
    if (rule[start] == '.') {
        *why = "a name does not start with a dot";
        return false;
    }
    size_t label = 0;
    for (size_t i = start; i < n; i++) {
        unsigned char c = (unsigned char)rule[i];
        if (c == '.') {
            if (label == 0) {
                *why = "a name has no empty label";
                return false;
            }
            label = 0;
            continue;
        }
        if (++label > FIRC_DNS_LABEL_MAX) {
            *why = "a name's labels are at most 63 characters";
            return false;
        }
        if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
            c == '-' || c == '_') {
            continue;
        }
        if (allow_glob && (c == '*' || c == '?')) { continue; }
        if (c >= 0x80) {
            *why = "a name reaches the daemon in ASCII; write an international name in its "
                   "xn-- form";
            return false;
        }
        *why = "a name holds letters, digits, hyphens, underscores and dots, and nothing else";
        return false;
    }
    if (label == 0) {
        /* the pattern ended on a dot that was not the root one */
        *why = "a name has no empty label";
        return false;
    }
    return true;
}


const char firc_rule_why_nomem[] = "could not be checked (out of memory)";

bool firc_rule_parse_proto(const char *proto, uint8_t *out) {
    if (proto == NULL || *proto == '\0') { *out = 0; return true; }
    if (strcmp(proto, "tcp") == 0) { *out = IPPROTO_TCP; return true; }
    if (strcmp(proto, "udp") == 0) { *out = IPPROTO_UDP; return true; }
    *out = 0;
    return false;
}

/* digits only, 1..65535; strtol would also accept a sign/leading space, the same typo refused for a prefix */
static bool parse_port(const char **p, unsigned *out) {
    const char *s = *p;
    if (*s < '0' || *s > '9') { return false; }
    unsigned v = 0;
    for (; *s >= '0' && *s <= '9'; s++) {
        v = v * 10 + (unsigned)(*s - '0');
        /* at most 5 digits, so "000053" is refused the same way the WebUI refuses it */
        if (v > 65535 || s - *p >= 5) { return false; }
    }
    if (v == 0) { return false; }
    *out = v;
    *p = s;
    return true;
}

bool firc_rule_parse_ports(const char *ports, char *out, size_t cap) {
    if (cap == 0) { return false; }
    out[0] = '\0';
    if (ports == NULL || *ports == '\0') { return true; }
    const char *p = ports;
    size_t len = 0, n = 0;
    for (;;) {
        unsigned lo, hi;
        if (!parse_port(&p, &lo)) { return false; }
        hi = lo;
        if (*p == '-') {
            p++;
            if (!parse_port(&p, &hi) || hi < lo) { return false; }
        }
        /* xt_multiport counts slots, and a range takes two. */
        n += lo == hi ? 1 : 2;
        if (n > FIRC_PORTS_MAX_SLOTS) { return false; }
        int w = lo == hi ? snprintf(out + len, cap - len, "%s%u", len ? "," : "", lo)
                         : snprintf(out + len, cap - len, "%s%u:%u", len ? "," : "", lo, hi);
        if (w < 0 || (size_t)w >= cap - len) { return false; }
        len += (size_t)w;
        if (*p == '\0') { return true; }
        if (*p != ',') { return false; }
        p++;
    }
}

bool firc_rule_spec_is_usable(const char *type, const char *rule, const char *proto,
                              const char *ports, const char **why) {
    const char *ignored = NULL;
    if (why == NULL) { why = &ignored; }
    if (!firc_rule_is_usable(type, rule, why)) { return false; }
    bool has_proto = proto != NULL && *proto != '\0';
    bool has_ports = ports != NULL && *ports != '\0';
    if (!has_proto && !has_ports) { return true; }
    if (strcmp(type, FIRC_RULE_SUBNET) != 0 && strcmp(type, FIRC_RULE_SUBNET6) != 0) {
        *why = "proto and ports apply to subnet and subnet6 rules only";
        return false;
    }
    uint8_t p;
    if (!firc_rule_parse_proto(proto, &p)) {
        *why = "proto is tcp or udp";
        return false;
    }
    if (!has_proto) {
        *why = "ports need a protocol (tcp or udp)";
        return false;
    }
    char buf[FIRC_PORTS_STR_MAX];
    if (!firc_rule_parse_ports(ports, buf, sizeof(buf))) {
        *why = "ports are a comma-separated list of ports or ranges, like 53,1000-2000 (at most 15 ports, a range counts as two)";
        return false;
    }
    return true;
}

bool firc_rule_is_usable(const char *type, const char *rule, const char **why) {
    const char *ignored = NULL;
    if (why == NULL) { why = &ignored; }
    *why = NULL;
    if (type == NULL || *type == '\0') {
        *why = "type is required";
        return false;
    }
    if (rule == NULL || *rule == '\0') {
        *why = "the pattern is empty and would match nothing";
        return false;
    }
    if (strcmp(type, FIRC_RULE_SUBNET) == 0) {
        firc_ipv4_subnet_t v4;
        if (!firc_rule_parse_subnet4(rule, &v4)) {
            *why = "a subnet is an IPv4 address or prefix, like 10.0.0.0/8";
            return false;
        }
        return true;
    }
    if (strcmp(type, FIRC_RULE_SUBNET6) == 0) {
        firc_ipv6_subnet_t v6;
        if (!firc_rule_parse_subnet6(rule, &v6)) {
            *why = "a subnet6 is an IPv6 address or prefix, like fd00::/8";
            return false;
        }
        return true;
    }
    if (strcmp(type, FIRC_RULE_REGEX) == 0) {
        firc_rule_matcher_t *m = firc_rule_matcher_new(type, rule);
        if (m == NULL) {
            /* OOM, not a bad regex: "does not compile" would send the operator to fix a rule that is fine */
            *why = firc_rule_why_nomem;
            return false;
        }
        bool ok = firc_rule_matcher_ok(m);
        firc_rule_matcher_free(m);
        if (!ok) {
            *why = "this regex does not compile and would match nothing";
            return false;
        }
        return true;
    }
    if (strcmp(type, FIRC_RULE_DOMAIN) == 0 || strcmp(type, FIRC_RULE_NAMESPACE) == 0 ||
        strcmp(type, FIRC_RULE_WILDCARD) == 0) {
        return firc_rule_name_pattern_is_usable(rule, strcmp(type, FIRC_RULE_WILDCARD) == 0, why);
    }
    *why = "unknown type (domain, namespace, wildcard, regex, subnet, subnet6)";
    return false;
}
