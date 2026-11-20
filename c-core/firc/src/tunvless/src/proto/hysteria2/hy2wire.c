#include <limits.h>
#include <stdlib.h>
#include <string.h>

#include "hy2wire.h"
#include "hpack.h"
#include "scrypto.h"

#define RASM_PIECE 2048
#define ADDR_MAX 2048
#define PAD_MAX 4096
#define MSG_MAX 2048

static const struct { const char *name, *value; } qstatic[99] = {
    { ":authority", "" },
    { ":path", "/" },
    { "age", "0" },
    { "content-disposition", "" },
    { "content-length", "0" },
    { "cookie", "" },
    { "date", "" },
    { "etag", "" },
    { "if-modified-since", "" },
    { "if-none-match", "" },
    { "last-modified", "" },
    { "link", "" },
    { "location", "" },
    { "referer", "" },
    { "set-cookie", "" },
    { ":method", "CONNECT" },
    { ":method", "DELETE" },
    { ":method", "GET" },
    { ":method", "HEAD" },
    { ":method", "OPTIONS" },
    { ":method", "POST" },
    { ":method", "PUT" },
    { ":scheme", "http" },
    { ":scheme", "https" },
    { ":status", "103" },
    { ":status", "200" },
    { ":status", "304" },
    { ":status", "404" },
    { ":status", "503" },
    { "accept", "*/*" },
    { "accept", "application/dns-message" },
    { "accept-encoding", "gzip, deflate, br" },
    { "accept-ranges", "bytes" },
    { "access-control-allow-headers", "cache-control" },
    { "access-control-allow-headers", "content-type" },
    { "access-control-allow-origin", "*" },
    { "cache-control", "max-age=0" },
    { "cache-control", "max-age=2592000" },
    { "cache-control", "max-age=604800" },
    { "cache-control", "no-cache" },
    { "cache-control", "no-store" },
    { "cache-control", "public, max-age=31536000" },
    { "content-encoding", "br" },
    { "content-encoding", "gzip" },
    { "content-type", "application/dns-message" },
    { "content-type", "application/javascript" },
    { "content-type", "application/json" },
    { "content-type", "application/x-www-form-urlencoded" },
    { "content-type", "image/gif" },
    { "content-type", "image/jpeg" },
    { "content-type", "image/png" },
    { "content-type", "text/css" },
    { "content-type", "text/html; charset=utf-8" },
    { "content-type", "text/plain" },
    { "content-type", "text/plain;charset=utf-8" },
    { "range", "bytes=0-" },
    { "strict-transport-security", "max-age=31536000" },
    { "strict-transport-security", "max-age=31536000; includesubdomains" },
    { "strict-transport-security", "max-age=31536000; includesubdomains; preload" },
    { "vary", "accept-encoding" },
    { "vary", "origin" },
    { "x-content-type-options", "nosniff" },
    { "x-xss-protection", "1; mode=block" },
    { ":status", "100" },
    { ":status", "204" },
    { ":status", "206" },
    { ":status", "302" },
    { ":status", "400" },
    { ":status", "403" },
    { ":status", "421" },
    { ":status", "425" },
    { ":status", "500" },
    { "accept-language", "" },
    { "access-control-allow-credentials", "FALSE" },
    { "access-control-allow-credentials", "TRUE" },
    { "access-control-allow-headers", "*" },
    { "access-control-allow-methods", "get" },
    { "access-control-allow-methods", "get, post, options" },
    { "access-control-allow-methods", "options" },
    { "access-control-expose-headers", "content-length" },
    { "access-control-request-headers", "content-type" },
    { "access-control-request-method", "get" },
    { "access-control-request-method", "post" },
    { "alt-svc", "clear" },
    { "authorization", "" },
    { "content-security-policy", "script-src 'none'; object-src 'none'; base-uri 'none'" },
    { "early-data", "1" },
    { "expect-ct", "" },
    { "forwarded", "" },
    { "if-range", "" },
    { "origin", "" },
    { "purpose", "prefetch" },
    { "server", "" },
    { "timing-allow-origin", "*" },
    { "upgrade-insecure-requests", "1" },
    { "user-agent", "" },
    { "x-forwarded-for", "" },
    { "x-frame-options", "deny" },
    { "x-frame-options", "sameorigin" },
};

static size_t vlen(uint64_t v) {
    return v < 64 ? 1 : v < 16384 ? 2 : v < (1ull << 30) ? 4 : 8;
}

size_t hy2_varint_put(uint8_t *out, size_t cap, uint64_t v) {
    if (v >= (1ull << 62)) return 0;
    size_t n = vlen(v);
    if (cap < n) return 0;
    for (size_t i = 0; i < n; i++) out[i] = (uint8_t)(v >> (8 * (n - 1 - i)));
    out[0] |= n == 1 ? 0x00 : n == 2 ? 0x40 : n == 4 ? 0x80 : 0xc0;
    return n;
}

size_t hy2_varint_get(const uint8_t *d, size_t n, uint64_t *v) {
    if (n == 0) return 0;
    size_t len = (size_t)1 << (d[0] >> 6);
    if (n < len) return 0;
    uint64_t x = d[0] & 0x3f;
    for (size_t i = 1; i < len; i++) x = (x << 8) | d[i];
    *v = x;
    return len;
}

size_t hy2_h3_control(uint8_t *out, size_t cap) {
    static const uint8_t c[8] = { 0x00, 0x04, 0x05, 0x06, 0x80, 0xa0, 0x00, 0x00 };
    if (cap < sizeof c) return 0;
    memcpy(out, c, sizeof c);
    return sizeof c;
}

struct wr { uint8_t *p; size_t cap, n; };

static void w_put(struct wr *w, const void *d, size_t k) {
    if (w->p && w->n + k <= w->cap) memcpy(w->p + w->n, d, k);
    w->n += k;
}

static void w_byte(struct wr *w, uint8_t b) {
    w_put(w, &b, 1);
}

static void w_pint(struct wr *w, unsigned bits, uint8_t flags, uint64_t v) {
    uint64_t k = ((uint64_t)1 << bits) - 1;
    if (v < k) { w_byte(w, (uint8_t)(flags | v)); return; }
    w_byte(w, (uint8_t)(flags | k));
    v -= k;
    for (; v >= 128; v >>= 7) w_byte(w, (uint8_t)(0x80 | (v & 0x7f)));
    w_byte(w, (uint8_t)v);
}

static void w_huff(struct wr *w, const char *s, size_t n, size_t hl) {
    if (w->p && w->n + hl <= w->cap) hpe_huff(w->p + w->n, s, n);
    w->n += hl;
}

static void q_field(struct wr *w, const char *name, const char *val, size_t vn) {
    size_t nl = strlen(name);
    int ni = -1, ei = -1;
    for (int i = 0; i < 99; i++) {
        if (strlen(qstatic[i].name) != nl || memcmp(qstatic[i].name, name, nl) != 0) continue;
        if (ni < 0) ni = i;
        if (strlen(qstatic[i].value) == vn && memcmp(qstatic[i].value, val, vn) == 0) { ei = i; break; }
    }
    if (ei >= 0) { w_pint(w, 6, 0xc0, (uint64_t)ei); return; }
    if (ni >= 0) w_pint(w, 4, 0x50, (uint64_t)ni);
    else {
        size_t hl = hpe_huff_len(name, nl);
        w_pint(w, 3, 0x28, hl);
        w_huff(w, name, nl, hl);
    }
    size_t hl = hpe_huff_len(val, vn);
    w_pint(w, 7, 0x80, hl);
    w_huff(w, val, vn, hl);
}

static void auth_section(struct wr *w, const char *auth, uint64_t rx_bps, const char *pad, size_t pad_n) {
    char rx[24];
    size_t k = sizeof rx;
    rx[--k] = 0;
    do { rx[--k] = (char)('0' + rx_bps % 10); rx_bps /= 10; } while (rx_bps);
    w_byte(w, 0);
    w_byte(w, 0);
    q_field(w, ":authority", "hysteria", 8);
    q_field(w, ":method", "POST", 4);
    q_field(w, ":path", "/auth", 5);
    q_field(w, ":scheme", "https", 5);
    q_field(w, "hysteria-auth", auth, strlen(auth));
    q_field(w, "hysteria-cc-rx", rx + k, strlen(rx + k));
    q_field(w, "hysteria-padding", pad, pad_n);
    q_field(w, "content-length", "0", 1);
    q_field(w, "accept-encoding", "gzip", 4);
    q_field(w, "user-agent", "quic-go HTTP/3", 14);
}

size_t hy2_auth_request(uint8_t *out, size_t cap, const char *auth, uint64_t rx_bps, const char *pad, size_t pad_n) {
    struct wr c = { NULL, 0, 0 };
    auth_section(&c, auth, rx_bps, pad, pad_n);
    size_t hdr = 1 + vlen(c.n);
    if (hdr + c.n > cap) return 0;
    out[0] = 0x01;
    hy2_varint_put(out + 1, cap - 1, c.n);
    struct wr w = { out + hdr, c.n, 0 };
    auth_section(&w, auth, rx_bps, pad, pad_n);
    return hdr + c.n;
}

static int q_int(const uint8_t **p, const uint8_t *end, unsigned bits, uint32_t *v) {
    uint64_t k = ((uint64_t)1 << bits) - 1;
    uint64_t x = **p & k;
    (*p)++;
    if (x < k) { *v = (uint32_t)x; return 0; }
    unsigned shift = 0;
    for (;;) {
        if (*p >= end || shift > 56) return -1;
        uint8_t b = *(*p)++;
        x += (uint64_t)(b & 0x7f) << shift;
        if (x > 0xffffffffu) return -1;
        shift += 7;
        if (!(b & 0x80)) break;
    }
    *v = (uint32_t)x;
    return 0;
}

static int q_str(const uint8_t **p, const uint8_t *end, unsigned bits, char *out, size_t cap) {
    if (*p >= end) return -1;
    int huff = (**p >> bits) & 1;
    uint32_t len;
    if (q_int(p, end, bits, &len) < 0) return -1;
    if (len > (size_t)(end - *p)) return -1;
    if (huff) {
        int r = hpack_huff_decode(*p, len, (uint8_t *)out, cap - 1);
        if (r < 0 || memchr(out, 0, (size_t)r)) return -1;
        out[r] = 0;
    } else {
        if (len > cap - 1 || memchr(*p, 0, len)) return -1;
        memcpy(out, *p, len);
        out[len] = 0;
    }
    *p += len;
    return 0;
}

int hy2_qpack_start(const uint8_t **p, const uint8_t *end) {
    if (end - *p < 2 || (*p)[0] != 0 || ((*p)[1] & 0x7f) != 0) return -1;
    *p += 2;
    return 0;
}

int hy2_qpack_next(const uint8_t **p, const uint8_t *end, struct hy2_field *f) {
    if (*p >= end) return 0;
    uint8_t b = **p;
    uint32_t i;
    if (b & 0x80) {
        if (!(b & 0x40) || q_int(p, end, 6, &i) < 0 || i >= 99) return -1;
        strcpy(f->name, qstatic[i].name);
        strcpy(f->value, qstatic[i].value);
        return 1;
    }
    if (b & 0x40) {
        if (!(b & 0x10) || q_int(p, end, 4, &i) < 0 || i >= 99) return -1;
        strcpy(f->name, qstatic[i].name);
        return q_str(p, end, 7, f->value, sizeof f->value) < 0 ? -1 : 1;
    }
    if (b & 0x20) {
        if (q_str(p, end, 3, f->name, sizeof f->name) < 0) return -1;
        return q_str(p, end, 7, f->value, sizeof f->value) < 0 ? -1 : 1;
    }
    return -1;
}

static int parse_status(const char *s, int *v) {
    int neg = *s == '-';
    if (*s == '-' || *s == '+') s++;
    if (!*s) return -1;
    long long x = 0;
    for (; *s; s++) {
        if (*s < '0' || *s > '9') return -1;
        x = x * 10 + (*s - '0');
        if (x > (long long)INT_MAX + neg) return -1;
    }
    *v = (int)(neg ? -x : x);
    return 0;
}

static uint64_t parse_rate(const char *s) {
    size_t n = strlen(s);
    if (n == 0) return 0;
    uint64_t v = 0;
    for (size_t i = 0; i < n; i++) {
        if (s[i] < '0' || s[i] > '9') return 0;
        uint64_t d = (uint64_t)(s[i] - '0');
        if (v > (UINT64_MAX - d) / 10) return UINT64_MAX;
        v = v * 10 + d;
    }
    return v;
}

static int auth_headers(const uint8_t *sec, size_t len, struct hy2_auth_resp *r) {
    const uint8_t *p = sec, *end = sec + len;
    struct hy2_field f;
    if (hy2_qpack_start(&p, end) < 0) return -1;
    int status = 0, st_ok = 0, rc, udp = 1, st_seen = 0, rx_seen = 0, udp_seen = 0;
    uint64_t rx = 0;
    while ((rc = hy2_qpack_next(&p, end, &f)) == 1) {
        if (strcmp(f.name, ":status") == 0) {
            if (st_seen) return -1;
            st_seen = f.value[0] != 0;
            st_ok = parse_status(f.value, &status) == 0;
        } else if (!rx_seen && strcmp(f.name, "hysteria-cc-rx") == 0) {
            rx = parse_rate(f.value);
            rx_seen = 1;
        } else if (!udp_seen && strcmp(f.name, "hysteria-udp") == 0) {
            udp = strcmp(f.value, "false") != 0;
            udp_seen = 1;
        }
    }
    if (rc < 0 || !st_ok) return -1;
    r->status = status;
    r->rx_bps = rx;
    r->udp = udp;
    return 1;
}

int hy2_auth_response(const uint8_t *d, size_t n, struct hy2_auth_resp *r) {
    size_t o = 0;
    for (;;) {
        uint64_t type, len;
        size_t a = hy2_varint_get(d + o, n - o, &type);
        if (!a) break;
        size_t b = hy2_varint_get(d + o + a, n - o - a, &len);
        if (!b) break;
        size_t hdr = a + b;
        if (type == 0x00 || type == 0x02 || type == 0x04 || type == 0x06 || type == 0x07 || type == 0x08 || type == 0x09) return -1;
        if (len > n - o - hdr) break;
        if (type == 0x01) return auth_headers(d + o + hdr, (size_t)len, r);
        o += hdr + (size_t)len;
    }
    return n > HY2_AUTH_RESP_MAX ? -1 : 0;
}

static size_t addr_len(const char *host, uint16_t port) {
    size_t hl = strlen(host);
    if (hl == 0) return 0;
    size_t dig = port >= 10000 ? 5 : port >= 1000 ? 4 : port >= 100 ? 3 : port >= 10 ? 2 : 1;
    size_t n = hl + (strchr(host, ':') && host[0] != '[' ? 2 : 0) + 1 + dig;
    return n > ADDR_MAX ? 0 : n;
}

static size_t addr_put(uint8_t *o, const char *host, uint16_t port) {
    size_t hl = strlen(host), k = 0;
    int br = strchr(host, ':') && host[0] != '[';
    if (br) o[k++] = '[';
    memcpy(o + k, host, hl);
    k += hl;
    if (br) o[k++] = ']';
    o[k++] = ':';
    char dg[5];
    size_t m = sizeof dg;
    unsigned v = port;
    do { dg[--m] = (char)('0' + v % 10); v /= 10; } while (v);
    memcpy(o + k, dg + m, sizeof dg - m);
    return k + sizeof dg - m;
}

size_t hy2_tcp_request(uint8_t *out, size_t cap, const char *host, uint16_t port, const char *pad, size_t pad_n) {
    size_t al = addr_len(host, port);
    if (al == 0 || pad_n > PAD_MAX) return 0;
    size_t total = vlen(HY2_FRAME_TCP) + vlen(al) + al + vlen(pad_n) + pad_n;
    if (cap < total) return 0;
    size_t o = hy2_varint_put(out, cap, HY2_FRAME_TCP);
    o += hy2_varint_put(out + o, cap - o, al);
    o += addr_put(out + o, host, port);
    o += hy2_varint_put(out + o, cap - o, pad_n);
    if (pad_n) memcpy(out + o, pad, pad_n);
    return total;
}

int hy2_tcp_request_parse(const uint8_t *d, size_t n, char *addr, size_t addr_cap, size_t *used) {
    uint64_t al, pl;
    size_t k = hy2_varint_get(d, n, &al);
    if (!k) return 0;
    if (al < 1 || al > ADDR_MAX || al + 1 > addr_cap) return -1;
    size_t o = k;
    if (al > n - o) return 0;
    const uint8_t *a = d + o;
    o += (size_t)al;
    k = hy2_varint_get(d + o, n - o, &pl);
    if (!k) return 0;
    if (pl > PAD_MAX) return -1;
    o += k;
    if (pl > n - o) return 0;
    o += (size_t)pl;
    memcpy(addr, a, (size_t)al);
    addr[al] = 0;
    *used = o;
    return 1;
}

int hy2_tcp_response(const uint8_t *d, size_t n, struct hy2_tcp_resp *r) {
    uint64_t ml, pl;
    if (n < 1) return 0;
    size_t o = 1, k = hy2_varint_get(d + o, n - o, &ml);
    if (!k) return 0;
    if (ml > MSG_MAX) return -1;
    o += k;
    if (ml > n - o) return 0;
    const uint8_t *m = d + o;
    o += (size_t)ml;
    k = hy2_varint_get(d + o, n - o, &pl);
    if (!k) return 0;
    if (pl > PAD_MAX) return -1;
    o += k;
    if (pl > n - o) return 0;
    o += (size_t)pl;
    size_t c = ml < sizeof r->msg - 1 ? (size_t)ml : sizeof r->msg - 1;
    for (size_t i = 0; i < c; i++) r->msg[i] = m[i] < 0x20 || m[i] == 0x7f ? ' ' : (char)m[i];
    r->msg[c] = 0;
    r->ok = d[0] == 0;
    r->used = o;
    return 1;
}

size_t hy2_tcp_response_put(uint8_t *out, size_t cap, int ok, const char *msg, size_t pad_n) {
    size_t ml = msg ? strlen(msg) : 0;
    if (ml > MSG_MAX || pad_n > PAD_MAX) return 0;
    size_t total = 1 + vlen(ml) + ml + vlen(pad_n) + pad_n;
    if (cap < total) return 0;
    size_t o = 0;
    out[o++] = ok ? 0 : 1;
    o += hy2_varint_put(out + o, cap - o, ml);
    if (ml) memcpy(out + o, msg, ml);
    o += ml;
    o += hy2_varint_put(out + o, cap - o, pad_n);
    memset(out + o, 'a', pad_n);
    return total;
}

size_t hy2_udp_header(uint8_t *out, size_t cap, uint32_t sid, uint16_t pkt, uint8_t frag, uint8_t count, const char *host, uint16_t port) {
    size_t al = addr_len(host, port);
    if (al == 0) return 0;
    size_t total = 8 + vlen(al) + al;
    if (cap < total) return 0;
    out[0] = (uint8_t)(sid >> 24);
    out[1] = (uint8_t)(sid >> 16);
    out[2] = (uint8_t)(sid >> 8);
    out[3] = (uint8_t)sid;
    out[4] = (uint8_t)(pkt >> 8);
    out[5] = (uint8_t)pkt;
    out[6] = frag;
    out[7] = count;
    size_t o = 8 + hy2_varint_put(out + 8, cap - 8, al);
    addr_put(out + o, host, port);
    return total;
}

int hy2_udp_parse(const uint8_t *d, size_t n, struct hy2_udp_msg *m) {
    if (n < 9) return -1;
    uint64_t al;
    size_t k = hy2_varint_get(d + 8, n - 8, &al);
    if (!k || al < 1 || al > ADDR_MAX || al > n - 8 - k) return -1;
    size_t o = 8 + k + (size_t)al;
    if (n - o < 1) return -1;
    m->sid = ((uint32_t)d[0] << 24) | ((uint32_t)d[1] << 16) | ((uint32_t)d[2] << 8) | d[3];
    m->pkt = (uint16_t)(((unsigned)d[4] << 8) | d[5]);
    m->frag = d[6];
    m->count = d[7];
    m->addr = (const char *)d + 8 + k;
    m->addr_n = (size_t)al;
    m->data = d + o;
    m->data_n = n - o;
    return 0;
}

unsigned hy2_udp_frags(size_t data_n, size_t header_n, size_t dgram_max) {
    if (dgram_max <= header_n) return 0;
    size_t payload = dgram_max - header_n;
    size_t f = data_n / payload + (data_n % payload != 0);
    return f > HY2_UDP_FRAG_MAX ? 0 : (unsigned)f;
}

static void rasm_reset(struct hy2_rasm *r) {
    free(r->buf);
    r->buf = NULL;
    r->count = 0;
    r->have = 0;
}

const uint8_t *hy2_rasm_feed(struct hy2_rasm *r, const struct hy2_udp_msg *m, size_t *out_n) {
    if (m->count <= 1) {
        *out_n = m->data_n;
        return m->data;
    }
    if (m->count > HY2_UDP_FRAG_MAX) {
        rasm_reset(r);
        return NULL;
    }
    if (m->frag >= m->count) return NULL;
    if (r->count == 0 || m->pkt != r->pkt || m->count != r->count) {
        rasm_reset(r);
        r->buf = malloc((size_t)m->count * RASM_PIECE);
        if (!r->buf) return NULL;
        r->pkt = m->pkt;
        r->count = m->count;
        r->piece = RASM_PIECE;
    }
    uint32_t bit = (uint32_t)1 << m->frag;
    if (r->have & bit) return NULL;
    if (m->data_n > RASM_PIECE) {
        rasm_reset(r);
        return NULL;
    }
    if (m->data_n) memcpy(r->buf + (size_t)m->frag * RASM_PIECE, m->data, m->data_n);
    r->len[m->frag] = (uint16_t)m->data_n;
    r->have |= bit;
    uint32_t full = r->count == 32 ? 0xffffffffu : (((uint32_t)1 << r->count) - 1);
    if (r->have != full) return NULL;
    size_t o = 0;
    for (unsigned i = 0; i < r->count; i++) {
        memmove(r->buf + o, r->buf + (size_t)i * RASM_PIECE, r->len[i]);
        o += r->len[i];
    }
    *out_n = o;
    return r->buf;
}

void hy2_rasm_free(struct hy2_rasm *r) {
    rasm_reset(r);
}

void hy2_pad_fill(char *out, size_t n, const uint8_t *rnd) {
    static const char a[] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    for (size_t i = 0; i < n; i++) out[i] = a[rnd[i] % 62];
}

size_t hy2_pad_len(uint16_t rnd, unsigned lo, unsigned hi) {
    if (hi <= lo) return lo;
    return lo + rnd % (hi - lo);
}

int hy2_salamander_init(struct hy2_salamander *s, const char *password) {
    size_t n = strlen(password);
    if (n < 4 || n > sizeof s->psk) return -1;
    memcpy(s->psk, password, n);
    s->psk_n = n;
    return 0;
}

static void sal_key(const struct hy2_salamander *s, const uint8_t *salt, uint8_t key[32]) {
    uint8_t b[sizeof s->psk + 8];
    memcpy(b, s->psk, s->psk_n);
    memcpy(b + s->psk_n, salt, 8);
    sc_blake2b256(b, s->psk_n + 8, key);
}

size_t hy2_salamander_wrap(const struct hy2_salamander *s, const uint8_t salt[8], uint8_t *dst, const uint8_t *src, size_t n) {
    uint8_t key[32];
    sal_key(s, salt, key);
    for (size_t i = n; i-- > 0;) dst[8 + i] = src[i] ^ key[i % 32];
    memcpy(dst, salt, 8);
    return n + 8;
}

size_t hy2_salamander_unwrap(const struct hy2_salamander *s, uint8_t *dst, const uint8_t *src, size_t n) {
    if (n < 9) return 0;
    uint8_t key[32];
    sal_key(s, src, key);
    for (size_t i = 0; i + 8 < n; i++) dst[i] = src[8 + i] ^ key[i % 32];
    return n - 8;
}
