#include "firc/resolver_addr.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>

static const uint8_t V4_MAPPED[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff};

static bool port_parse(const char *s, uint16_t *out)
{
    size_t n = strlen(s);
    if (n == 0 || n > 5) { return false; }
    unsigned v = 0;
    for (size_t i = 0; i < n; i++) {
        if (s[i] < '0' || s[i] > '9') { return false; }
        v = v * 10u + (unsigned)(s[i] - '0');
    }
    if (v == 0 || v > 65535u) { return false; }
    *out = (uint16_t)v;
    return true;
}

bool firc_resolver_ip_is_sink(const firc_ip_t *ip)
{
    static const uint8_t zero[16] = {0};
    if (ip->len == 4) {
        return memcmp(ip->b, zero, 4) == 0 || ip->b[0] == 127;
    }
    if (ip->len == 16) {
        if (memcmp(ip->b, zero, 15) == 0) { return ip->b[15] == 0 || ip->b[15] == 1; }
        if (memcmp(ip->b, V4_MAPPED, 12) == 0) {
            return (ip->b[12] == 0 && ip->b[13] == 0 && ip->b[14] == 0 && ip->b[15] == 0) ||
                   ip->b[12] == 127;
        }
        return false;
    }
    return false;
}

firc_resolver_addr_res_t firc_resolver_addr_parse(const char *s, firc_resolver_addr_t *out)
{
    if (s == NULL || out == NULL) { return FIRC_RESOLVER_ADDR_SYNTAX; }
    memset(out, 0, sizeof(*out));
    out->port = FIRC_RESOLVER_DEFAULT_PORT;
    char host[INET6_ADDRSTRLEN];
    const char *port = NULL;
    bool v6_only = false;
    size_t n = strlen(s);
    if (n == 0) { return FIRC_RESOLVER_ADDR_SYNTAX; }
    if (s[0] == '[') {
        const char *close = strchr(s, ']');
        if (close == NULL) { return FIRC_RESOLVER_ADDR_SYNTAX; }
        size_t hl = (size_t)(close - s - 1);
        if (hl == 0 || hl >= sizeof(host)) { return FIRC_RESOLVER_ADDR_SYNTAX; }
        memcpy(host, s + 1, hl);
        host[hl] = '\0';
        if (close[1] == ':') {
            port = close + 2;
        } else if (close[1] != '\0') {
            return FIRC_RESOLVER_ADDR_SYNTAX;
        }
        v6_only = true;
    } else {
        const char *first = strchr(s, ':');
        const char *last = strrchr(s, ':');
        size_t hl = n;
        if (first != NULL && first == last) {
            hl = (size_t)(first - s);
            port = first + 1;
        }
        if (hl == 0 || hl >= sizeof(host)) { return FIRC_RESOLVER_ADDR_SYNTAX; }
        memcpy(host, s, hl);
        host[hl] = '\0';
    }
    if (!v6_only && inet_pton(AF_INET, host, out->ip.b) == 1) {
        out->ip.len = 4;
    } else if (inet_pton(AF_INET6, host, out->ip.b) == 1) {
        out->ip.len = 16;
        if (!v6_only && port != NULL) { return FIRC_RESOLVER_ADDR_SYNTAX; }
    } else {
        return FIRC_RESOLVER_ADDR_SYNTAX;
    }
    if (port != NULL && !port_parse(port, &out->port)) {
        bool digits = port[0] != '\0';
        for (const char *c = port; *c != '\0'; c++) {
            if ((*c < '0' || *c > '9') && !(c == port && (*c == '+' || *c == '-'))) { digits = false; }
        }
        return digits ? FIRC_RESOLVER_ADDR_PORT : FIRC_RESOLVER_ADDR_SYNTAX;
    }
    if (out->ip.len == 16 && memcmp(out->ip.b, V4_MAPPED, 12) == 0) { return FIRC_RESOLVER_ADDR_MAPPED; }
    if (firc_resolver_ip_is_sink(&out->ip)) { return FIRC_RESOLVER_ADDR_SINK; }
    return FIRC_RESOLVER_ADDR_OK;
}

const char *firc_resolver_addr_why(firc_resolver_addr_res_t r)
{
    switch (r) {
    case FIRC_RESOLVER_ADDR_OK: return "is an address";
    case FIRC_RESOLVER_ADDR_SYNTAX:
        return "is not an address literal (addr, addr:port, [v6addr] or [v6addr]:port)";
    case FIRC_RESOLVER_ADDR_PORT: return "has a port outside 1..65535";
    case FIRC_RESOLVER_ADDR_SINK: return "is a sink address (0.0.0.0, 127.0.0.0/8, ::, ::1), which no resolver answers from";
    case FIRC_RESOLVER_ADDR_MAPPED: return "is an IPv4-mapped IPv6 address; write the IPv4 address";
    }
    return "is not usable";
}

void firc_resolver_addr_sockaddr(const firc_resolver_addr_t *a, struct sockaddr_storage *ss, socklen_t *len)
{
    memset(ss, 0, sizeof(*ss));
    if (a->ip.len == 4) {
        struct sockaddr_in *s4 = (struct sockaddr_in *)ss;
        s4->sin_family = AF_INET;
        s4->sin_port = htons(a->port);
        memcpy(&s4->sin_addr, a->ip.b, 4);
        *len = (socklen_t)sizeof(*s4);
    } else {
        struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)ss;
        s6->sin6_family = AF_INET6;
        s6->sin6_port = htons(a->port);
        memcpy(&s6->sin6_addr, a->ip.b, 16);
        *len = (socklen_t)sizeof(*s6);
    }
}

size_t firc_resolver_addr_format(const firc_resolver_addr_t *a, char *buf, size_t cap)
{
    if (cap == 0) { return 0; }
    buf[0] = '\0';
    if (a->doh) { return 0; }
    char host[INET6_ADDRSTRLEN];
    if (inet_ntop(a->ip.len == 4 ? AF_INET : AF_INET6, a->ip.b, host, sizeof(host)) == NULL) { return 0; }
    int w;
    if (a->port == FIRC_RESOLVER_DEFAULT_PORT) {
        w = snprintf(buf, cap, "%s", host);
    } else if (a->ip.len == 4) {
        w = snprintf(buf, cap, "%s:%u", host, (unsigned)a->port);
    } else {
        w = snprintf(buf, cap, "[%s]:%u", host, (unsigned)a->port);
    }
    if (w < 0 || (size_t)w >= cap) {
        buf[0] = '\0';
        return 0;
    }
    return (size_t)w;
}

static bool is_blank(char c)
{
    return c == ' ' || c == '\t';
}

#define QUOTED_MAX 64

/* The value as an error sentence quotes it. Masked, every entry ends at its host: the user, path and query stay out. */
static const char *quote(const char *b, size_t n, bool masked, char buf[QUOTED_MAX + 1])
{
    if (!masked) {
        snprintf(buf, QUOTED_MAX + 1, "%.*s", n > QUOTED_MAX ? QUOTED_MAX : (int)n, b);
        return buf;
    }
    const char *end = b + n;
    buf[0] = '\0';
    for (size_t o = 0; b < end && o < QUOTED_MAX; o = strlen(buf)) {
        const char *e = memchr(b, ',', (size_t)(end - b));
        if (e == NULL) { e = end; }
        const char *auth = b;
        for (const char *c = b; c + 3 <= e && auth == b; c++) {
            if (memcmp(c, "://", 3) == 0) { auth = c + 3; }
        }
        const char *host = auth;
        const char *cut = auth;
        for (; cut < e && *cut != '/' && *cut != '?' && *cut != '#'; cut++) {
            if (*cut == '@') { host = cut + 1; }
        }
        snprintf(buf + o, QUOTED_MAX + 1 - o, "%.*s%.*s%s%s", (int)(auth - b), b, (int)(cut - host), host,
                 cut < e ? "/…" : "", e < end ? "," : "");
        b = e < end ? e + 1 : end;
    }
    return buf;
}

static void say(char *why, size_t cap, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

static void say(char *why, size_t cap, const char *fmt, ...)
{
    if (why == NULL || cap == 0) { return; }
    va_list ap;
    va_start(ap, fmt);
    (void)vsnprintf(why, cap, fmt, ap);
    va_end(ap);
}

static bool is_url_entry(const char *b, size_t n)
{
    for (size_t i = 0; i + 3 <= n; i++) {
        if (memcmp(b + i, "://", 3) == 0) { return true; }
    }
    return false;
}

static bool url_authority(const char *url, size_t n, const char **host, size_t *host_len, bool *bracket)
{
    if (n < 8 || strncasecmp(url, "https://", 8) != 0) { return false; }
    const char *a = url + 8;
    const char *end = url + n;
    const char *slash = memchr(a, '/', (size_t)(end - a));
    if (slash == NULL) { return false; }
    for (const char *c = url; c < end; c++) {
        if ((unsigned char)*c <= ' ' || *c == 0x7f || *c == '?' || *c == '#') { return false; }
    }
    const char *q = a;
    if (q < slash && *q == '[') {
        const char *close = memchr(q, ']', (size_t)(slash - q));
        if (close == NULL || close == q + 1) { return false; }
        *host = q + 1;
        *host_len = (size_t)(close - q - 1);
        char lit[INET6_ADDRSTRLEN];
        unsigned char tmp[16];
        if (*host_len >= sizeof(lit)) { return false; }
        memcpy(lit, *host, *host_len);
        lit[*host_len] = '\0';
        if (inet_pton(AF_INET6, lit, tmp) != 1) { return false; }
        *bracket = true;
        q = close + 1;
    } else {
        const char *he = q;
        while (he < slash && *he != ':') { he++; }
        if (he == q) { return false; }
        *host = q;
        *host_len = (size_t)(he - q);
        *bracket = false;
        q = he;
    }
    for (size_t i = 0; i < *host_len; i++) {
        if ((*host)[i] == '@' || (*host)[i] == '[' || (*host)[i] == ']' || (!*bracket && (*host)[i] == ':')) {
            return false;
        }
    }
    if (q == slash) { return true; }
    if (*q != ':' || q + 1 == slash) { return false; }
    unsigned long port = 0;
    for (const char *d = q + 1; d < slash; d++) {
        if (*d < '0' || *d > '9') { return false; }
        port = port * 10 + (unsigned long)(*d - '0');
        if (port > 65535) { return false; }
    }
    return port >= 1;
}

size_t firc_resolver_url_show(const char *url, char *buf, size_t cap)
{
    if (cap == 0) { return 0; }
    buf[0] = '\0';
    const char *slash = strncasecmp(url, "https://", 8) == 0 ? strchr(url + 8, '/') : NULL;
    if (slash == NULL) { return 0; }
    int w = snprintf(buf, cap, "%.*s/%s", (int)(slash - url), url, slash[1] != '\0' ? "…" : "");
    if (w < 0 || (size_t)w >= cap) {
        buf[0] = '\0';
        return 0;
    }
    return (size_t)w;
}

bool firc_resolver_url_literal(const char *url, firc_ip_t *ip)
{
    const char *host;
    size_t hl;
    bool bracket;
    if (!url_authority(url, strlen(url), &host, &hl, &bracket) || hl >= INET6_ADDRSTRLEN) { return false; }
    char h[INET6_ADDRSTRLEN];
    memcpy(h, host, hl);
    h[hl] = '\0';
    memset(ip, 0, sizeof(*ip));
    if (!bracket && inet_pton(AF_INET, h, ip->b) == 1) {
        ip->len = 4;
        return true;
    }
    if (bracket && inet_pton(AF_INET6, h, ip->b) == 1) {
        ip->len = 16;
        return true;
    }
    return false;
}

static bool same_server(const firc_resolver_list_t *l, size_t i, const firc_resolver_addr_t *b, const char *url)
{
    const firc_resolver_addr_t *a = &l->servers[i];
    if (a->doh != b->doh) { return false; }
    if (a->doh) { return strcmp(l->urls[i], url) == 0; }
    return a->port == b->port && a->ip.len == b->ip.len && memcmp(a->ip.b, b->ip.b, a->ip.len) == 0;
}

static firc_resolver_list_res_t list_parse(const char *s, firc_resolver_list_t *out, char *why, size_t why_cap,
                                           bool masked)
{
    memset(out, 0, sizeof(*out));
    if (why != NULL && why_cap > 0) { why[0] = '\0'; }
    if (s == NULL) {
        say(why, why_cap, "is missing");
        return FIRC_RESOLVER_LIST_ADDR;
    }
    char whole[QUOTED_MAX + 1], one[QUOTED_MAX + 1];
    (void)quote(s, strlen(s), masked, whole);
    const char *p = s;
    for (;;) {
        const char *end = strchr(p, ',');
        if (end == NULL) { end = p + strlen(p); }
        const char *b = p;
        const char *e = end;
        while (b < e && is_blank(*b)) { b++; }
        while (e > b && is_blank(e[-1])) { e--; }
        size_t n = (size_t)(e - b);
        if (n == 0) {
            memset(out, 0, sizeof(*out));
            say(why, why_cap, "\"%s\" has an empty entry", whole);
            return FIRC_RESOLVER_LIST_EMPTY;
        }
        if (out->n == FIRC_RESOLVE_MAX_SERVERS) {
            memset(out, 0, sizeof(*out));
            say(why, why_cap, "\"%s\" names more than %d servers", whole, FIRC_RESOLVE_MAX_SERVERS);
            return FIRC_RESOLVER_LIST_TOO_MANY;
        }
        char entry[INET6_ADDRSTRLEN + 16];
        firc_resolver_addr_t a;
        memset(&a, 0, sizeof(a));
        char url[FIRC_RESOLVER_URL_MAX + 1];
        url[0] = '\0';
        if (is_url_entry(b, n)) {
            const char *host;
            size_t hl;
            bool bracket;
            if (n > FIRC_RESOLVER_URL_MAX || !url_authority(b, n, &host, &hl, &bracket)) {
                memset(out, 0, sizeof(*out));
                say(why, why_cap, "\"%s\" is not a DoH URL (https://host[:port]/path, at most %d bytes, no user, "
                    "query or fragment)", quote(b, n, masked, one), FIRC_RESOLVER_URL_MAX);
                return FIRC_RESOLVER_LIST_URL;
            }
            memcpy(url, b, n);
            url[n] = '\0';
            a.doh = true;
            firc_ip_t lit;
            if (firc_resolver_url_literal(url, &lit)) {
                /* The same addresses a plain entry refuses. */
                bool is_mapped = lit.len == 16 && memcmp(lit.b, V4_MAPPED, sizeof(V4_MAPPED)) == 0;
                if (is_mapped || firc_resolver_ip_is_sink(&lit)) {
                    memset(out, 0, sizeof(*out));
                    say(why, why_cap, "\"%s\" %s", quote(b, n, masked, one),
                        firc_resolver_addr_why(is_mapped ? FIRC_RESOLVER_ADDR_MAPPED : FIRC_RESOLVER_ADDR_SINK));
                    return FIRC_RESOLVER_LIST_URL;
                }
            }
        } else {
            firc_resolver_addr_res_t r = FIRC_RESOLVER_ADDR_SYNTAX;
            if (n < sizeof(entry)) {
                memcpy(entry, b, n);
                entry[n] = '\0';
                r = firc_resolver_addr_parse(entry, &a);
            }
            if (r != FIRC_RESOLVER_ADDR_OK) {
                memset(out, 0, sizeof(*out));
                say(why, why_cap, "\"%s\" %s", quote(b, n, masked, one), firc_resolver_addr_why(r));
                return FIRC_RESOLVER_LIST_ADDR;
            }
        }
        for (size_t i = 0; i < out->n; i++) {
            if (same_server(out, i, &a, url)) {
                memset(out, 0, sizeof(*out));
                say(why, why_cap, "\"%s\" names \"%s\" twice", whole, quote(b, n, masked, one));
                return FIRC_RESOLVER_LIST_DUPLICATE;
            }
        }
        memcpy(out->urls[out->n], url, strlen(url) + 1);
        out->servers[out->n++] = a;
        if (*end == '\0') { return FIRC_RESOLVER_LIST_OK; }
        p = end + 1;
    }
}

firc_resolver_list_res_t firc_resolver_list_parse(const char *s, firc_resolver_list_t *out, char *why,
                                                  size_t why_cap)
{
    return list_parse(s, out, why, why_cap, false);
}

firc_resolver_list_res_t firc_resolver_list_parse_for_log(const char *s, firc_resolver_list_t *out, char *why,
                                                          size_t why_cap)
{
    return list_parse(s, out, why, why_cap, true);
}
