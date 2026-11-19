#include "firc/settings.h"

#include <arpa/inet.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include "firc/dnat.h"
#include "firc/fakeip.h"
#include "firc/fakeip_addr.h"
#include "firc/id.h"
#include "firc/ifacename.h"
#include "firc/log.h"
#include "firc/mark.h"
#include "firc/pool_reject.h"
#include "firc/rtnl.h"
#include "firc/taprules.h"
#include "firc/autolistrules.h"
#include "firc/regdomain.h"

#define AT(f) offsetof(firc_app_config_t, f)
#define LIVE FIRC_SETTING_LIVE
#define RESTART FIRC_SETTING_RESTART

const firc_setting_t firc_settings[] = {
    {"app.dnsProxy.upstream.address", FIRC_SK_ADDR, LIVE, AT(dns_proxy.upstream.address), 0,
     FIRC_APPLY_UPSTREAM},
    {"app.dnsProxy.upstream.port", FIRC_SK_PORT, LIVE, AT(dns_proxy.upstream.port), 0,
     FIRC_APPLY_UPSTREAM},
    {"app.dnsProxy.disableDropAAAA", FIRC_SK_BOOL, LIVE, AT(dns_proxy.disable_drop_aaaa), 0,
     FIRC_APPLY_PROXY_FLAGS},
    {"app.dnsProxy.disableStats", FIRC_SK_BOOL, LIVE, AT(dns_proxy.disable_stats), 0, FIRC_APPLY_DNS_STATS},
    {"app.dnsProxy.unmatchedTtl", FIRC_SK_SEC, LIVE, AT(dns_proxy.unmatched_ttl), 0,
     FIRC_APPLY_UNMATCHED_TTL},
    {"app.dnsProxy.timeout", FIRC_SK_MS, RESTART, AT(dns_proxy.timeout), 0, FIRC_APPLY_NONE},
    {"app.dnsProxy.maxConcurrent", FIRC_SK_U64, RESTART, AT(dns_proxy.max_concurrent), 0, FIRC_APPLY_NONE},
    {"app.dnsProxy.maxIdleConns", FIRC_SK_U64, RESTART, AT(dns_proxy.max_idle_conns), 0, FIRC_APPLY_NONE},
    {"app.dnsProxy.host.address", FIRC_SK_ADDR, RESTART, AT(dns_proxy.host.address), 0, FIRC_APPLY_NONE},
    {"app.dnsProxy.host.port", FIRC_SK_PORT, RESTART, AT(dns_proxy.host.port), 0, FIRC_APPLY_NONE},
    {"app.dnsProxy.disableRemap53", FIRC_SK_BOOL, RESTART, AT(dns_proxy.disable_remap53), 0, FIRC_APPLY_NONE},
    {"app.addressPool.ttlClamp", FIRC_SK_SEC, RESTART, AT(fakeip.ttl_clamp), 0, FIRC_APPLY_NONE},
    {"app.addressPool.maxNames", FIRC_SK_U32, RESTART, AT(fakeip.max_names), 0, FIRC_APPLY_NONE},
    {"app.addressPool.v4.pool", FIRC_SK_STRING, RESTART, AT(fakeip.v4.pool), 0, FIRC_APPLY_NONE},
    {"app.addressPool.v4.chunk", FIRC_SK_CHUNK, RESTART, AT(fakeip.v4.chunk), 0, FIRC_APPLY_NONE},
    {"app.addressPool.v6.pool", FIRC_SK_STRING, RESTART, AT(fakeip.v6.pool), 0, FIRC_APPLY_NONE},
    {"app.addressPool.v6.chunk", FIRC_SK_CHUNK, RESTART, AT(fakeip.v6.chunk), 0, FIRC_APPLY_NONE},
    {"app.netfilter.disableIPv4", FIRC_SK_BOOL, RESTART, AT(netfilter.disable_ipv4), 0, FIRC_APPLY_NONE},
    {"app.netfilter.disableIPv6", FIRC_SK_BOOL, RESTART, AT(netfilter.disable_ipv6), 0, FIRC_APPLY_NONE},
    {"app.link", FIRC_SK_LIST, RESTART, AT(link), AT(n_link), FIRC_APPLY_NONE},
    {"app.netfilter.iptables.chainPrefix", FIRC_SK_STRING, RESTART, AT(netfilter.iptables.chain_prefix), 0, FIRC_APPLY_NONE},
    {"app.netfilter.startMarkTableIndex", FIRC_SK_HEX32, RESTART, AT(netfilter.start_mark_table_index), 0, FIRC_APPLY_NONE},
    {"app.httpWeb.host.address", FIRC_SK_ADDR, RESTART, AT(http_web.host.address), 0, FIRC_APPLY_NONE},
    {"app.httpWeb.host.port", FIRC_SK_PORT, RESTART, AT(http_web.host.port), 0, FIRC_APPLY_NONE},
    {"app.update.check", FIRC_SK_BOOL, LIVE, AT(update.check), 0, FIRC_APPLY_NONE},
    {"app.check.url", FIRC_SK_STRING, LIVE, AT(check.url), 0, FIRC_APPLY_CHECK},
    {"app.check.wanUrl", FIRC_SK_STRING, LIVE, AT(check.wan_url), 0, FIRC_APPLY_CHECK},
    {"app.check.interval", FIRC_SK_SEC, LIVE, AT(check.interval), 0, FIRC_APPLY_CHECK},
    {"app.check.timeout", FIRC_SK_SEC, LIVE, AT(check.timeout), 0, FIRC_APPLY_CHECK},
    {"app.autolist.enabled", FIRC_SK_BOOL, LIVE, AT(autolist.enabled), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.group", FIRC_SK_STRING, LIVE, AT(autolist.group), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.failThreshold", FIRC_SK_U32, LIVE, AT(autolist.fail_threshold), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.failTime", FIRC_SK_SEC, LIVE, AT(autolist.fail_time), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.retransThreshold", FIRC_SK_U32, LIVE, AT(autolist.retrans_threshold), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.exclude", FIRC_SK_LIST, LIVE, AT(autolist.exclude), AT(autolist.n_exclude),
     FIRC_APPLY_AUTOLIST},
    {"app.autolist.recheck", FIRC_SK_BOOL, LIVE, AT(autolist.recheck), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.recheckInterval", FIRC_SK_SEC, LIVE, AT(autolist.recheck_interval), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.checkInterface", FIRC_SK_STRING, LIVE, AT(autolist.check_iface), 0, FIRC_APPLY_AUTOLIST},
    {"app.autolist.mode", FIRC_SK_STRING, LIVE, AT(autolist.mode), 0, FIRC_APPLY_AUTOLIST},
};
_Static_assert(sizeof(firc_settings) / sizeof(firc_settings[0]) == FIRC_SETTINGS_COUNT,
               "FIRC_SETTINGS_COUNT is the number of rows");

static void *field_at(firc_app_config_t *c, size_t off) { return (char *)c + off; }
static const void *cfield_at(const firc_app_config_t *c, size_t off) { return (const char *)c + off; }
static const char *or_empty(const char *s) { return s != NULL ? s : ""; }

const firc_setting_t *firc_setting_find(const char *path)
{
    if (path == NULL) { return NULL; }
    for (size_t i = 0; i < FIRC_SETTINGS_COUNT; i++) {
        if (strcmp(firc_settings[i].path, path) == 0) { return &firc_settings[i]; }
    }
    return NULL;
}

bool firc_setting_equal(const firc_setting_t *s, const firc_app_config_t *a,
                        const firc_app_config_t *b)
{
    const void *x = cfield_at(a, s->off);
    const void *y = cfield_at(b, s->off);
    switch (s->kind) {
    case FIRC_SK_BOOL: return *(const bool *)x == *(const bool *)y;
    case FIRC_SK_ADDR:
    case FIRC_SK_STRING:
        return strcmp(or_empty(*(char *const *)x), or_empty(*(char *const *)y)) == 0;
    case FIRC_SK_PORT: return *(const uint16_t *)x == *(const uint16_t *)y;
    case FIRC_SK_U64: return *(const uint64_t *)x == *(const uint64_t *)y;
    case FIRC_SK_U32:
    case FIRC_SK_HEX32: return *(const uint32_t *)x == *(const uint32_t *)y;
    case FIRC_SK_CHUNK: return *(const uint8_t *)x == *(const uint8_t *)y;
    case FIRC_SK_MS:
    case FIRC_SK_SEC: return *(const firc_duration_t *)x == *(const firc_duration_t *)y;
    case FIRC_SK_LIST: {
        size_t na = *(const size_t *)cfield_at(a, s->off_n);
        size_t nb = *(const size_t *)cfield_at(b, s->off_n);
        if (na != nb) { return false; }
        char *const *la = *(char **const *)x;
        char *const *lb = *(char **const *)y;
        for (size_t i = 0; i < na; i++) {
            if (strcmp(or_empty(la[i]), or_empty(lb[i])) != 0) { return false; }
        }
        return true;
    }
    }
    return false;
}

static firc_err_t copy_list(const firc_setting_t *s, firc_app_config_t *dst,
                            const firc_app_config_t *src)
{
    size_t n = *(const size_t *)cfield_at(src, s->off_n);
    char *const *from = *(char **const *)cfield_at(src, s->off);
    char **list = calloc(n > 0 ? n : 1, sizeof(char *));
    if (list == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n; i++) {
        list[i] = strdup(or_empty(from[i]));
        if (list[i] == NULL) {
            for (size_t k = 0; k < i; k++) { free(list[k]); }
            free(list);
            return FIRC_ERR_NOMEM;
        }
    }
    char ***to = (char ***)field_at(dst, s->off);
    size_t *to_n = (size_t *)field_at(dst, s->off_n);
    for (size_t i = 0; i < *to_n; i++) { free((*to)[i]); }
    free(*to);
    *to = list;
    *to_n = n;
    return FIRC_OK;
}

firc_err_t firc_setting_copy(const firc_setting_t *s, firc_app_config_t *dst,
                             const firc_app_config_t *src)
{
    void *d = field_at(dst, s->off);
    const void *v = cfield_at(src, s->off);
    switch (s->kind) {
    case FIRC_SK_BOOL: *(bool *)d = *(const bool *)v; return FIRC_OK;
    case FIRC_SK_ADDR:
    case FIRC_SK_STRING: return firc_strset((char **)d, *(char *const *)v);
    case FIRC_SK_PORT: *(uint16_t *)d = *(const uint16_t *)v; return FIRC_OK;
    case FIRC_SK_U64: *(uint64_t *)d = *(const uint64_t *)v; return FIRC_OK;
    case FIRC_SK_U32:
    case FIRC_SK_HEX32: *(uint32_t *)d = *(const uint32_t *)v; return FIRC_OK;
    case FIRC_SK_CHUNK: *(uint8_t *)d = *(const uint8_t *)v; return FIRC_OK;
    case FIRC_SK_MS:
    case FIRC_SK_SEC: *(firc_duration_t *)d = *(const firc_duration_t *)v; return FIRC_OK;
    case FIRC_SK_LIST: return copy_list(s, dst, src);
    }
    return FIRC_ERR_INVAL;
}

firc_err_t firc_app_config_copy(firc_app_config_t *dst, const firc_app_config_t *src)
{
    dst->http_web.enabled = src->http_web.enabled;
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; err == FIRC_OK && i < FIRC_SETTINGS_COUNT; i++) {
        err = firc_setting_copy(&firc_settings[i], dst, src);
    }
    return err;
}

/* an IPv4/IPv6 literal, optionally bracketed; empty_ok is for the WebUI listener only */
static bool addr_parse(const char *s, bool empty_ok, unsigned char out[16])
{
    memset(out, 0, 16);
    if (s == NULL) { return false; }
    size_t n = strlen(s);
    if (n >= 2 && s[0] == '[' && s[n - 1] == ']') {
        s++;
        n -= 2;
    }
    if (n == 0) { return empty_ok; }
    char buf[INET6_ADDRSTRLEN];
    if (n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n);
    buf[n] = '\0';
    if (inet_pton(AF_INET, buf, out + 12) == 1) {
        out[10] = out[11] = 0xff; /* ::ffff:a.b.c.d, 0.0.0.0 stays all zero */
        if (memcmp(out + 12, "\0\0\0\0", 4) == 0) { memset(out, 0, 16); }
        return true;
    }
    return inet_pton(AF_INET6, buf, out) == 1;
}

static bool addr_literal(const char *s, bool empty_ok)
{
    unsigned char out[16];
    return addr_parse(s, empty_ok, out);
}

/* both bind dual-stack with IPV6_V6ONLY off, so a wildcard address takes its port on every address */
static bool same_listener(const char *a, uint16_t pa, const char *b, uint16_t pb)
{
    if (pa != pb) { return false; }
    static const unsigned char any[16] = {0};
    unsigned char x[16], y[16];
    if (!addr_parse(a, true, x) || !addr_parse(b, true, y)) { return false; }
    return memcmp(x, any, 16) == 0 || memcmp(y, any, 16) == 0 || memcmp(x, y, 16) == 0;
}

/* XT_EXTENSION_MAXNAMELEN is 29 with the NUL; iptables refuses a chain name of 29 or more */
#define CHAIN_NAME_MAX 28

/* longest text firc puts after the prefix; a new suffix longer than these must be added here */
static size_t longest_chain_suffix(void)
{
    static const size_t lens[] = {
        FIRC_ID_STR_LEN - 1,
        FIRC_ID_STR_LEN - 1 + sizeof(FIRC_DEVICES_CHAIN_SUFFIX) - 1,
        FIRC_ID_STR_LEN - 1 + sizeof(FIRC_EGRESS_LIST_SUFFIX) - 1,
        sizeof(FIRC_EGRESS_CHAIN_SUFFIX) - 1,
        sizeof(FIRC_DNAT_CHAIN_SUFFIX) - 1,
        sizeof(FIRC_POOL_REJECT_CHAIN_SUFFIX) - 1,
        sizeof(FIRC_TAP_CHAIN_SUFFIX FIRC_TAP_PROBE_SUFFIX) - 1,
        sizeof(FIRC_AUTOLIST_CHAIN_SUFFIX FIRC_AUTOLIST_PROBE_SUFFIX) - 1,
        sizeof("DNSOR") - 1,
    };
    size_t max = 0;
    for (size_t i = 0; i < sizeof(lens) / sizeof(lens[0]); i++) {
        if (lens[i] > max) { max = lens[i]; }
    }
    return max;
}

/* a chain name without whitespace or quotes, short enough with the longest suffix */
static bool chain_prefix_ok(const char *s)
{
    if (s == NULL || s[0] == '\0' || s[0] == '-' || s[0] == '!') { return false; }
    size_t n = strlen(s);
    if (n + longest_chain_suffix() > CHAIN_NAME_MAX) { return false; }
    for (const unsigned char *p = (const unsigned char *)s; *p != '\0'; p++) {
        if (*p <= 0x20 || *p == 0x7f || *p == '"' || *p == '\'') { return false; }
    }
    return true;
}

#define REFUSE(f, w)          \
    do {                      \
        *field = (f);         \
        *why = (w);           \
        return FIRC_ERR_INVAL; \
    } while (0)

bool firc_check_url_ok(const char *u)
{
    if (u == NULL || strlen(u) > 512) { return false; }
    size_t skip = strncasecmp(u, "http://", 7) == 0 ? 7 : strncasecmp(u, "https://", 8) == 0 ? 8 : 0;
    if (skip == 0) { return false; }
    char h = u[skip];
    if (h == '\0' || h == '/' || h == '?' || h == '#' || h == ':') { return false; }
    if (memchr(u + skip, '[', strcspn(u + skip, "/?#")) != NULL) { return false; }
    for (const unsigned char *p = (const unsigned char *)u; *p != '\0'; p++) {
        if (*p <= 0x20 || *p == 0x7f) { return false; }
    }
    return true;
}

firc_err_t firc_app_config_check(const firc_app_config_t *c, const char **field,
                                 const char **why)
{
    static const char NOT_ADDR[] = "not an IPv4 or IPv6 address";
    static const char BAD_PORT[] = "a port is 1-65535";
    static const char BAD_CHUNK[] =
        "the chunk must be longer than the pool's prefix, hold at least 4 addresses, "
        "and cut the pool into fewer than 2^32 chunks";

    /* a WebUI that is off binds nothing, and its host is never read */
    if (c->http_web.enabled) {
        if (!addr_literal(c->http_web.host.address, true)) {
            REFUSE("app.httpWeb.host.address", NOT_ADDR);
        }
        if (c->http_web.host.port == 0) { REFUSE("app.httpWeb.host.port", BAD_PORT); }
        /* the WebUI starts after the proxy: a port collision fails its bind and takes the daemon down */
        if (same_listener(c->http_web.host.address, c->http_web.host.port,
                          c->dns_proxy.host.address, c->dns_proxy.host.port)) {
            REFUSE("app.httpWeb.host.port", "the DNS proxy listens on this port");
        }
    }
    if (!addr_literal(c->dns_proxy.host.address, false)) {
        REFUSE("app.dnsProxy.host.address", NOT_ADDR);
    }
    if (c->dns_proxy.host.port == 0) { REFUSE("app.dnsProxy.host.port", BAD_PORT); }
    if (!addr_literal(c->dns_proxy.upstream.address, false)) {
        REFUSE("app.dnsProxy.upstream.address", NOT_ADDR);
    }
    if (c->dns_proxy.upstream.port == 0) { REFUSE("app.dnsProxy.upstream.port", BAD_PORT); }
    /* cast to 32-bit ms by the caller: a negative or oversized value wraps; 0 means the 5s default */
    if (c->dns_proxy.timeout < 0 ||
        c->dns_proxy.timeout / FIRC_DURATION_MS > (firc_duration_t)UINT32_MAX) {
        REFUSE("app.dnsProxy.timeout", "0 (the default) to 4294967295 ms");
    }
    /* cast to 32 bits by the caller; 0 means the default of 100 */
    if (c->dns_proxy.max_concurrent > UINT32_MAX) {
        REFUSE("app.dnsProxy.maxConcurrent", "0 (the default) to 4294967295");
    }
    if (c->dns_proxy.max_idle_conns > UINT32_MAX) {
        REFUSE("app.dnsProxy.maxIdleConns", "0 to 4294967295");
    }
    /* 0 = off; otherwise 1s-1d -- past that it shortens nothing an upstream's TTL doesn't already */
    if (c->dns_proxy.unmatched_ttl != 0 &&
        (c->dns_proxy.unmatched_ttl < FIRC_DURATION_SEC ||
         c->dns_proxy.unmatched_ttl > 86400 * FIRC_DURATION_SEC)) {
        REFUSE("app.dnsProxy.unmatchedTtl", "0 (off), or 1 second to 86400 seconds");
    }
    /* must leave room below FIRC_RTNL_TABLE_END for FIRC_MARK_MAX_GROUPS group tables */
    if (c->netfilter.start_mark_table_index > FIRC_RTNL_TABLE_END - FIRC_MARK_MAX_GROUPS) {
        REFUSE("app.netfilter.startMarkTableIndex", "0x0 to 0x7ffffeff: 255 groups each take a table below 0x7ffffffe");
    }
    if (!chain_prefix_ok(c->netfilter.iptables.chain_prefix)) {
        REFUSE("app.netfilter.iptables.chainPrefix",
               "1-18 characters, no spaces, quotes or control characters, not starting with - or !: "
               "iptables allows 28 in a chain name and firc adds up to 10");
    }

    /* Zeroed: firc_ip_parse_cidr leaves its outputs alone when it fails. */
    firc_ip_t base = {{0}, 0};
    uint8_t plen = 0;
    if (!firc_ip_parse_cidr(c->fakeip.v4.pool, &base, &plen) || base.len != 4) {
        REFUSE("app.addressPool.v4.pool", "not an IPv4 prefix such as 198.18.0.0/15");
    }
    if (firc_fakeip_check_geometry(&base, plen, c->fakeip.v4.chunk, 4) != FIRC_OK) {
        REFUSE("app.addressPool.v4.chunk", BAD_CHUNK);
    }
    /* empty means the generated ULA /48; its length is known even without its bits, so chunk is checkable */
    if (c->fakeip.v6.pool == NULL || c->fakeip.v6.pool[0] == '\0') {
        if (firc_fakeip_check_geometry(NULL, FIRC_FAKEIP_V6_GEN_CIDR, c->fakeip.v6.chunk, 16) !=
            FIRC_OK) {
            REFUSE("app.addressPool.v6.chunk", BAD_CHUNK);
        }
    } else {
        if (!firc_ip_parse_cidr(c->fakeip.v6.pool, &base, &plen) || base.len != 16) {
            REFUSE("app.addressPool.v6.pool",
                   "not an IPv6 prefix such as fd7a:115c:a1e0::/48, or empty for the generated one");
        }
        if (firc_fakeip_check_geometry(&base, plen, c->fakeip.v6.chunk, 16) != FIRC_OK) {
            REFUSE("app.addressPool.v6.chunk", BAD_CHUNK);
        }
    }
    /* asked one window at a time so refusal names the right key; a bare YAML integer here is nanoseconds */
    if (c->fakeip.ttl_clamp < FIRC_DURATION_SEC ||
        c->fakeip.ttl_clamp > INT64_C(182) * 24 * 3600 * FIRC_DURATION_SEC ||
        firc_fakeip_check_windows(1, c->fakeip.ttl_clamp / FIRC_DURATION_SEC) != FIRC_OK) {
        REFUSE("app.addressPool.ttlClamp", "1 second to 182 days: names are released after twice it");
    }
    /* 0 reaches the pool as "use the default", the opposite of a request for no bound */
    if (c->fakeip.max_names == 0) {
        REFUSE("app.addressPool.maxNames", "at least 1: there is no unbounded setting");
    }
    /* each name goes into a capture rule's -i match: only a valid kernel interface name */
    for (size_t i = 0; i < c->n_link; i++) {
        if (!firc_is_interface_name(c->link[i])) {
            REFUSE("app.link",
                   "an interface name is 1-15 letters, digits, '.', '_', '-' or '@'");
        }
        /* the same interface twice writes each of its rules twice, and the page keys its chips by name */
        for (size_t k = 0; k < i; k++) {
            if (strcmp(c->link[k], c->link[i]) == 0) {
                REFUSE("app.link", "an interface is named twice");
            }
        }
    }
    if (!firc_check_url_ok(c->check.url)) { REFUSE("app.check.url", "an http:// or https:// URL with a name or IPv4 host"); }
    if (c->check.wan_url != NULL && c->check.wan_url[0] != '\0' && !firc_check_url_ok(c->check.wan_url)) {
        REFUSE("app.check.wanUrl", "empty, or an http:// or https:// URL with a name or IPv4 host");
    }
    if (c->check.interval < 10 * FIRC_DURATION_SEC || c->check.interval > 3600 * FIRC_DURATION_SEC) {
        REFUSE("app.check.interval", "10 to 3600 seconds");
    }
    if (c->check.timeout < 1 * FIRC_DURATION_SEC || c->check.timeout > 30 * FIRC_DURATION_SEC) {
        REFUSE("app.check.timeout", "1 to 30 seconds");
    }
    if (c->autolist.group != NULL && c->autolist.group[0] != '\0') {
        firc_id_t gid;
        if (firc_id_parse(c->autolist.group, &gid) != FIRC_OK) {
            REFUSE("app.autolist.group", "empty, or a group's id: 8 hex digits");
        }
    }
    if (c->autolist.mode == NULL ||
        (strcmp(c->autolist.mode, "candidates") != 0 && strcmp(c->autolist.mode, "add") != 0)) {
        REFUSE("app.autolist.mode", "candidates or add");
    }
    if (c->autolist.enabled && (c->autolist.group == NULL || c->autolist.group[0] == '\0')) {
        REFUSE("app.autolist.group", "a group is needed while the automatic list is on");
    }
    if (c->autolist.enabled && c->dns_proxy.unmatched_ttl == 0) {
        REFUSE("app.autolist.enabled",
               "needs app.dnsProxy.unmatchedTtl above 0: without it devices keep the old address");
    }
    if (c->autolist.fail_threshold < 1 || c->autolist.fail_threshold > 20) {
        REFUSE("app.autolist.failThreshold", "1 to 20 failures");
    }
    if (c->autolist.fail_time < 10 * FIRC_DURATION_SEC || c->autolist.fail_time > 3600 * FIRC_DURATION_SEC) {
        REFUSE("app.autolist.failTime", "10 to 3600 seconds");
    }
    if (c->autolist.retrans_threshold < 1 || c->autolist.retrans_threshold > 8) {
        REFUSE("app.autolist.retransThreshold", "1 to 8 sendings again");
    }
    if (c->autolist.recheck_interval < 600 * FIRC_DURATION_SEC ||
        c->autolist.recheck_interval > 7 * 86400 * FIRC_DURATION_SEC) {
        REFUSE("app.autolist.recheckInterval", "600 seconds to 7 days");
    }
    if (c->autolist.check_iface != NULL && c->autolist.check_iface[0] != '\0' &&
        !firc_is_interface_name(c->autolist.check_iface)) {
        REFUSE("app.autolist.checkInterface", "empty for the default route, or an interface name");
    }
    if (c->autolist.n_exclude > FIRC_AUTOLIST_EXCLUDE_MAX) {
        REFUSE("app.autolist.exclude", "at most 1000 names");
    }
    for (size_t i = 0; i < c->autolist.n_exclude; i++) {
        if (!firc_autolist_exclude_ok(c->autolist.exclude[i])) {
            REFUSE("app.autolist.exclude",
                   "a domain name in lower case: letters, digits, '-', '_' and dots between labels");
        }
        for (size_t k = 0; k < i; k++) {
            if (strcmp(c->autolist.exclude[k], c->autolist.exclude[i]) == 0) {
                REFUSE("app.autolist.exclude", "a name is listed twice");
            }
        }
    }
    return FIRC_OK;
}
