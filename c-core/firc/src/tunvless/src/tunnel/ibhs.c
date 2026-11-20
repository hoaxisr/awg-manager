#define _GNU_SOURCE
#include "ibhs.h"
#include <arpa/inet.h>
#include <string.h>
#include <strings.h>

enum { P_START, P_AUTH, P_REQ, P_HTTP };
enum step { S_NEED, S_NEXT, S_END };

static const char H400[] = "HTTP/1.1 400 Bad Request\r\n\r\n";
static const char H405[] = "HTTP/1.1 405 Method Not Allowed\r\n\r\n";
static const char H407[] =
    "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"firc\"\r\n\r\n";

void ibhs_init(struct ibhs *h, const struct ib_cfg *c) {
    memset(h, 0, sizeof *h);
    h->need_auth = c->user[0] || c->pass[0];
}

static void emit(unsigned char *out, size_t *out_n, const void *b, size_t n) {
    memcpy(out + *out_n, b, n);
    *out_n += n;
}

static void socks_rep(unsigned char *out, size_t *out_n, unsigned char rep) {
    const unsigned char r[10] = {5, rep, 0, 1, 0, 0, 0, 0, 0, 0};
    emit(out, out_n, r, sizeof r);
}

static void shift(struct ibhs *h, size_t used) {
    memmove(h->buf, h->buf + used, h->len - used);
    h->len -= used;
}

static int cred_ok(const struct ib_cfg *c, const unsigned char *u, size_t ul, const unsigned char *p,
                   size_t pl) {
    return ul >= 1 && pl >= 1 && ul == strlen(c->user) && pl == strlen(c->pass) &&
           memcmp(u, c->user, ul) == 0 && memcmp(p, c->pass, pl) == 0;
}

static int host_ok(const unsigned char *s, size_t n) {
    for (size_t i = 0; i < n; i++)
        if (s[i] < 0x20 || s[i] == 0x7f || s[i] == ':') return 0;
    return 1;
}

static enum step socks_start(struct ibhs *h, const struct ib_cfg *c, unsigned char *out, size_t *out_n,
                             enum ibhs_rc *rc) {
    if (h->len < 2) return S_NEED;
    size_t nm = h->buf[1];
    if (h->len < 2 + nm) return S_NEED;
    unsigned char want = h->need_auth ? 2 : 0;
    int found = 0;
    for (size_t i = 0; i < nm; i++)
        if (h->buf[2 + i] == want) found = 1;
    shift(h, 2 + nm);
    (void)c;
    if (!found) {
        const unsigned char r[2] = {5, 0xff};
        emit(out, out_n, r, 2);
        *rc = IBHS_FAIL;
        return S_END;
    }
    const unsigned char r[2] = {5, want};
    emit(out, out_n, r, 2);
    h->phase = h->need_auth ? P_AUTH : P_REQ;
    return S_NEXT;
}

static enum step socks_auth(struct ibhs *h, const struct ib_cfg *c, unsigned char *out, size_t *out_n,
                            enum ibhs_rc *rc) {
    if (h->len < 2) return S_NEED;
    size_t ul = h->buf[1];
    if (h->len < 2 + ul + 1) return S_NEED;
    size_t pl = h->buf[2 + ul];
    if (h->len < 3 + ul + pl) return S_NEED;
    int ok = h->buf[0] == 1 && cred_ok(c, h->buf + 2, ul, h->buf + 3 + ul, pl);
    shift(h, 3 + ul + pl);
    const unsigned char r[2] = {1, (unsigned char)(ok ? 0 : 1)};
    emit(out, out_n, r, 2);
    if (!ok) {
        *rc = IBHS_FAIL;
        return S_END;
    }
    h->phase = P_REQ;
    return S_NEXT;
}

static enum step socks_req(struct ibhs *h, unsigned char *out, size_t *out_n, enum ibhs_rc *rc) {
    if (h->len < 4) return S_NEED;
    unsigned char cmd = h->buf[1], atyp = h->buf[3];
    size_t used;
    if (h->buf[0] != 5 || (cmd != 1 && cmd != 3)) {
        socks_rep(out, out_n, h->buf[0] != 5 ? 1 : 7);
        *rc = IBHS_FAIL;
        return S_END;
    }
    if (atyp == 1) {
        if (h->len < 10) return S_NEED;
        used = 10;
        h->ip = (uint32_t)h->buf[4] << 24 | (uint32_t)h->buf[5] << 16 | (uint32_t)h->buf[6] << 8 | h->buf[7];
        h->host[0] = 0;
    } else if (atyp == 3) {
        if (h->len < 5) return S_NEED;
        size_t dl = h->buf[4];
        if (dl == 0) {
            socks_rep(out, out_n, 8);
            *rc = IBHS_FAIL;
            return S_END;
        }
        if (h->len < 7 + dl) return S_NEED;
        if (!host_ok(h->buf + 5, dl)) {
            socks_rep(out, out_n, 8);
            *rc = IBHS_FAIL;
            return S_END;
        }
        used = 7 + dl;
        memcpy(h->host, h->buf + 5, dl);
        h->host[dl] = 0;
        h->ip = 0;
    } else {
        socks_rep(out, out_n, 8);
        *rc = IBHS_FAIL;
        return S_END;
    }
    h->port = (uint16_t)(h->buf[used - 2] << 8 | h->buf[used - 1]);
    shift(h, used);
    *rc = cmd == 1 ? IBHS_CONNECT : IBHS_UDP;
    return S_END;
}

static int b64v(int ch) {
    if (ch >= 'A' && ch <= 'Z') return ch - 'A';
    if (ch >= 'a' && ch <= 'z') return ch - 'a' + 26;
    if (ch >= '0' && ch <= '9') return ch - '0' + 52;
    if (ch == '+') return 62;
    if (ch == '/') return 63;
    return -1;
}

static int b64dec(const unsigned char *s, size_t n, unsigned char *o, size_t cap, size_t *on) {
    while (n && s[n - 1] == '=') n--;
    size_t k = 0;
    uint32_t acc = 0;
    int bits = 0;
    for (size_t i = 0; i < n; i++) {
        int v = b64v(s[i]);
        if (v < 0) return 0;
        acc = acc << 6 | (uint32_t)v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (k >= cap) return 0;
            o[k++] = (unsigned char)(acc >> bits);
        }
    }
    *on = k;
    return 1;
}

static int basic_ok(const struct ib_cfg *c, const unsigned char *v, size_t n) {
    while (n && (*v == ' ' || *v == '\t')) v++, n--;
    while (n && (v[n - 1] == ' ' || v[n - 1] == '\t')) n--;
    if (n < 6 || strncasecmp((const char *)v, "Basic ", 6) != 0) return 0;
    v += 6;
    n -= 6;
    while (n && *v == ' ') v++, n--;
    unsigned char d[600];
    size_t dn;
    if (!b64dec(v, n, d, sizeof d, &dn)) return 0;
    unsigned char *col = memchr(d, ':', dn);
    if (!col) return 0;
    return cred_ok(c, d, (size_t)(col - d), col + 1, dn - (size_t)(col - d) - 1);
}

static int auth_header(const struct ib_cfg *c, const unsigned char *b, size_t pos, size_t end) {
    static const char name[] = "proxy-authorization";
    size_t nl = sizeof name - 1;
    while (pos < end) {
        const unsigned char *q = memmem(b + pos, end + 2 - pos, "\r\n", 2);
        size_t le = (size_t)(q - b);
        if (le - pos > nl && b[pos + nl] == ':' && strncasecmp((const char *)b + pos, name, nl) == 0 &&
            basic_ok(c, b + pos + nl + 1, le - pos - nl - 1))
            return 1;
        pos = le + 2;
    }
    return 0;
}

static enum step http_fail(unsigned char *out, size_t *out_n, enum ibhs_rc *rc, const char *s, size_t n) {
    emit(out, out_n, s, n);
    *rc = IBHS_FAIL;
    return S_END;
}

static enum step http_step(struct ibhs *h, const struct ib_cfg *c, unsigned char *out, size_t *out_n,
                           enum ibhs_rc *rc) {
    const unsigned char *e = memmem(h->buf, h->len, "\r\n\r\n", 4);
    if (!e) {
        if (h->len >= sizeof h->buf) return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
        return S_NEED;
    }
    size_t end = (size_t)(e - h->buf);
    const unsigned char *q = memmem(h->buf, end + 2, "\r\n", 2);
    size_t l = (size_t)(q - h->buf);
    if (l < 8 || memcmp(h->buf, "CONNECT ", 8) != 0) return http_fail(out, out_n, rc, H405, sizeof H405 - 1);
    if (h->need_auth && !auth_header(c, h->buf, l + 2, end)) return http_fail(out, out_n, rc, H407, sizeof H407 - 1);
    const unsigned char *t = h->buf + 8;
    const unsigned char *sp = memchr(t, ' ', l - 8);
    if (!sp || l - (size_t)(sp - h->buf) < 8 || memcmp(sp + 1, "HTTP/1.", 7) != 0)
        return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
    size_t tl = (size_t)(sp - t);
    const unsigned char *colon = memrchr(t, ':', tl);
    if (!colon || t[0] == '[') return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
    size_t hl = (size_t)(colon - t), pl = tl - hl - 1;
    if (hl < 1 || hl > 255 || pl < 1 || pl > 5) return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
    unsigned long port = 0;
    for (size_t i = 0; i < pl; i++) {
        if (colon[1 + i] < '0' || colon[1 + i] > '9') return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
        port = port * 10 + (unsigned long)(colon[1 + i] - '0');
    }
    if (port < 1 || port > 65535 || !host_ok(t, hl)) return http_fail(out, out_n, rc, H400, sizeof H400 - 1);
    memcpy(h->host, t, hl);
    h->host[hl] = 0;
    h->port = (uint16_t)port;
    struct in_addr a;
    h->ip = 0;
    if (inet_pton(AF_INET, h->host, &a) == 1) {
        h->ip = ntohl(a.s_addr);
        h->host[0] = 0;
    }
    shift(h, end + 4);
    *rc = IBHS_CONNECT;
    return S_END;
}

static enum step run(struct ibhs *h, const struct ib_cfg *c, unsigned char *out, size_t *out_n,
                     enum ibhs_rc *rc) {
    if (h->len == 0) return S_NEED;
    if (h->phase == P_START && h->buf[0] != 5) {
        h->http = 1;
        h->phase = P_HTTP;
    }
    switch (h->phase) {
    case P_START: return socks_start(h, c, out, out_n, rc);
    case P_AUTH: return socks_auth(h, c, out, out_n, rc);
    case P_REQ: return socks_req(h, out, out_n, rc);
    default: return http_step(h, c, out, out_n, rc);
    }
}

enum ibhs_rc ibhs_feed(struct ibhs *h, const struct ib_cfg *c, const unsigned char *p, size_t n,
                       unsigned char *out, size_t *out_n) {
    *out_n = 0;
    enum ibhs_rc rc = IBHS_MORE;
    for (;;) {
        size_t room = sizeof h->buf - h->len, k = n < room ? n : room;
        memcpy(h->buf + h->len, p, k);
        h->len += k;
        p += k;
        n -= k;
        enum step s = run(h, c, out, out_n, &rc);
        if (s == S_NEXT) continue;
        if (s == S_END) break;
        if (n == 0) return *out_n ? IBHS_REPLY : IBHS_MORE;
        *out_n = 0;
        if (h->http) emit(out, out_n, H400, sizeof H400 - 1);
        else socks_rep(out, out_n, 1);
        return IBHS_FAIL;
    }
    if (rc == IBHS_FAIL) return rc;
    size_t room = sizeof h->buf - h->len;
    if (n > room) {
        *out_n = 0;
        return IBHS_FAIL;
    }
    memcpy(h->buf + h->len, p, n);
    h->len += n;
    return rc;
}

size_t ibhs_done(int http, enum ibhs_end end, unsigned char *out, size_t cap) {
    if (!http) {
        static const unsigned char rep[] = {0, 4, 3, 1};
        if (cap < 10) return 0;
        size_t n = 0;
        socks_rep(out, &n, rep[end]);
        return n;
    }
    static const char *const line[] = {"HTTP/1.1 200 Connection established\r\n\r\n", "HTTP/1.1 502 Bad Gateway\r\n\r\n",
                                       "HTTP/1.1 503 Service Unavailable\r\n\r\n",
                                       "HTTP/1.1 503 Service Unavailable\r\n\r\n"};
    size_t n = strlen(line[end]);
    if (cap < n) return 0;
    memcpy(out, line[end], n);
    return n;
}

size_t ibhs_rest(const struct ibhs *h, const unsigned char **p) {
    *p = h->buf;
    return h->len;
}
