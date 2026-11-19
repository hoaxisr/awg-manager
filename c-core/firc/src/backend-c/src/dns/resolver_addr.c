#include "firc/resolver_addr.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

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

static int len_quoted(size_t n)
{
    return n > 64 ? 64 : (int)n;
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

static bool same_server(const firc_resolver_addr_t *a, const firc_resolver_addr_t *b)
{
    return a->port == b->port && a->ip.len == b->ip.len && memcmp(a->ip.b, b->ip.b, a->ip.len) == 0;
}

firc_resolver_list_res_t firc_resolver_list_parse(const char *s, firc_resolver_list_t *out, char *why,
                                                  size_t why_cap)
{
    memset(out, 0, sizeof(*out));
    if (why != NULL && why_cap > 0) { why[0] = '\0'; }
    if (s == NULL) {
        say(why, why_cap, "is missing");
        return FIRC_RESOLVER_LIST_ADDR;
    }
    int whole = len_quoted(strlen(s));
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
            say(why, why_cap, "\"%.*s\" has an empty entry", whole, s);
            return FIRC_RESOLVER_LIST_EMPTY;
        }
        if (out->n == FIRC_RESOLVE_MAX_SERVERS) {
            memset(out, 0, sizeof(*out));
            say(why, why_cap, "\"%.*s\" names more than %d servers", whole, s, FIRC_RESOLVE_MAX_SERVERS);
            return FIRC_RESOLVER_LIST_TOO_MANY;
        }
        char entry[INET6_ADDRSTRLEN + 16];
        firc_resolver_addr_t a;
        firc_resolver_addr_res_t r = FIRC_RESOLVER_ADDR_SYNTAX;
        if (n < sizeof(entry)) {
            memcpy(entry, b, n);
            entry[n] = '\0';
            r = firc_resolver_addr_parse(entry, &a);
        }
        if (r != FIRC_RESOLVER_ADDR_OK) {
            memset(out, 0, sizeof(*out));
            say(why, why_cap, "\"%.*s\" %s", len_quoted(n), b, firc_resolver_addr_why(r));
            return FIRC_RESOLVER_LIST_ADDR;
        }
        for (size_t i = 0; i < out->n; i++) {
            if (same_server(&out->servers[i], &a)) {
                memset(out, 0, sizeof(*out));
                say(why, why_cap, "\"%.*s\" names \"%.*s\" twice", whole, s, len_quoted(n), b);
                return FIRC_RESOLVER_LIST_DUPLICATE;
            }
        }
        out->servers[out->n++] = a;
        if (*end == '\0') { return FIRC_RESOLVER_LIST_OK; }
        p = end + 1;
    }
}
