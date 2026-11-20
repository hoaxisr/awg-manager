#include <string.h>

#include "hpack.h"

_Static_assert(sizeof(struct hpe) <= 5200, "struct hpe size");

static const uint32_t huff_code[256] = {
    0x1ff8, 0x7fffd8, 0xfffffe2, 0xfffffe3, 0xfffffe4, 0xfffffe5, 0xfffffe6, 0xfffffe7,
    0xfffffe8, 0xffffea, 0x3ffffffc, 0xfffffe9, 0xfffffea, 0x3ffffffd, 0xfffffeb, 0xfffffec,
    0xfffffed, 0xfffffee, 0xfffffef, 0xffffff0, 0xffffff1, 0xffffff2, 0x3ffffffe, 0xffffff3,
    0xffffff4, 0xffffff5, 0xffffff6, 0xffffff7, 0xffffff8, 0xffffff9, 0xffffffa, 0xffffffb,
    0x14, 0x3f8, 0x3f9, 0xffa, 0x1ff9, 0x15, 0xf8, 0x7fa,
    0x3fa, 0x3fb, 0xf9, 0x7fb, 0xfa, 0x16, 0x17, 0x18,
    0x0, 0x1, 0x2, 0x19, 0x1a, 0x1b, 0x1c, 0x1d,
    0x1e, 0x1f, 0x5c, 0xfb, 0x7ffc, 0x20, 0xffb, 0x3fc,
    0x1ffa, 0x21, 0x5d, 0x5e, 0x5f, 0x60, 0x61, 0x62,
    0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a,
    0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72,
    0xfc, 0x73, 0xfd, 0x1ffb, 0x7fff0, 0x1ffc, 0x3ffc, 0x22,
    0x7ffd, 0x3, 0x23, 0x4, 0x24, 0x5, 0x25, 0x26,
    0x27, 0x6, 0x74, 0x75, 0x28, 0x29, 0x2a, 0x7,
    0x2b, 0x76, 0x2c, 0x8, 0x9, 0x2d, 0x77, 0x78,
    0x79, 0x7a, 0x7b, 0x7ffe, 0x7fc, 0x3ffd, 0x1ffd, 0xffffffc,
    0xfffe6, 0x3fffd2, 0xfffe7, 0xfffe8, 0x3fffd3, 0x3fffd4, 0x3fffd5, 0x7fffd9,
    0x3fffd6, 0x7fffda, 0x7fffdb, 0x7fffdc, 0x7fffdd, 0x7fffde, 0xffffeb, 0x7fffdf,
    0xffffec, 0xffffed, 0x3fffd7, 0x7fffe0, 0xffffee, 0x7fffe1, 0x7fffe2, 0x7fffe3,
    0x7fffe4, 0x1fffdc, 0x3fffd8, 0x7fffe5, 0x3fffd9, 0x7fffe6, 0x7fffe7, 0xffffef,
    0x3fffda, 0x1fffdd, 0xfffe9, 0x3fffdb, 0x3fffdc, 0x7fffe8, 0x7fffe9, 0x1fffde,
    0x7fffea, 0x3fffdd, 0x3fffde, 0xfffff0, 0x1fffdf, 0x3fffdf, 0x7fffeb, 0x7fffec,
    0x1fffe0, 0x1fffe1, 0x3fffe0, 0x1fffe2, 0x7fffed, 0x3fffe1, 0x7fffee, 0x7fffef,
    0xfffea, 0x3fffe2, 0x3fffe3, 0x3fffe4, 0x7ffff0, 0x3fffe5, 0x3fffe6, 0x7ffff1,
    0x3ffffe0, 0x3ffffe1, 0xfffeb, 0x7fff1, 0x3fffe7, 0x7ffff2, 0x3fffe8, 0x1ffffec,
    0x3ffffe2, 0x3ffffe3, 0x3ffffe4, 0x7ffffde, 0x7ffffdf, 0x3ffffe5, 0xfffff1, 0x1ffffed,
    0x7fff2, 0x1fffe3, 0x3ffffe6, 0x7ffffe0, 0x7ffffe1, 0x3ffffe7, 0x7ffffe2, 0xfffff2,
    0x1fffe4, 0x1fffe5, 0x3ffffe8, 0x3ffffe9, 0xffffffd, 0x7ffffe3, 0x7ffffe4, 0x7ffffe5,
    0xfffec, 0xfffff3, 0xfffed, 0x1fffe6, 0x3fffe9, 0x1fffe7, 0x1fffe8, 0x7ffff3,
    0x3fffea, 0x3fffeb, 0x1ffffee, 0x1ffffef, 0xfffff4, 0xfffff5, 0x3ffffea, 0x7ffff4,
    0x3ffffeb, 0x7ffffe6, 0x3ffffec, 0x3ffffed, 0x7ffffe7, 0x7ffffe8, 0x7ffffe9, 0x7ffffea,
    0x7ffffeb, 0xffffffe, 0x7ffffec, 0x7ffffed, 0x7ffffee, 0x7ffffef, 0x7fffff0, 0x3ffffee,
};

static const uint8_t huff_len[256] = {
    13, 23, 28, 28, 28, 28, 28, 28, 28, 24, 30, 28, 28, 30, 28, 28,
    28, 28, 28, 28, 28, 28, 30, 28, 28, 28, 28, 28, 28, 28, 28, 28,
    6, 10, 10, 12, 13, 6, 8, 11, 10, 10, 8, 11, 8, 6, 6, 6,
    5, 5, 5, 6, 6, 6, 6, 6, 6, 6, 7, 8, 15, 6, 12, 10,
    13, 6, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
    7, 7, 7, 7, 7, 7, 7, 7, 8, 7, 8, 13, 19, 13, 14, 6,
    15, 5, 6, 5, 6, 5, 6, 6, 6, 5, 7, 7, 6, 6, 6, 5,
    6, 7, 6, 5, 5, 6, 7, 7, 7, 7, 7, 15, 11, 14, 13, 28,
    20, 22, 20, 20, 22, 22, 22, 23, 22, 23, 23, 23, 23, 23, 24, 23,
    24, 24, 22, 23, 24, 23, 23, 23, 23, 21, 22, 23, 22, 23, 23, 24,
    22, 21, 20, 22, 22, 23, 23, 21, 23, 22, 22, 24, 21, 22, 23, 23,
    21, 21, 22, 21, 23, 22, 23, 23, 20, 22, 22, 22, 23, 22, 22, 23,
    26, 26, 20, 19, 22, 23, 22, 25, 26, 26, 26, 27, 27, 26, 24, 25,
    19, 21, 26, 27, 27, 26, 27, 24, 21, 21, 26, 26, 28, 27, 27, 27,
    20, 24, 20, 21, 22, 21, 21, 23, 22, 22, 25, 25, 24, 24, 26, 23,
    26, 27, 26, 26, 27, 27, 27, 27, 27, 28, 27, 27, 27, 27, 27, 26,
};

static const char *const stable[62][2] = {
    { NULL, NULL },
    { ":authority", "" },
    { ":method", "GET" },
    { ":method", "POST" },
    { ":path", "/" },
    { ":path", "/index.html" },
    { ":scheme", "http" },
    { ":scheme", "https" },
    { ":status", "200" },
    { ":status", "204" },
    { ":status", "206" },
    { ":status", "304" },
    { ":status", "400" },
    { ":status", "404" },
    { ":status", "500" },
    { "accept-charset", "" },
    { "accept-encoding", "gzip, deflate" },
    { "accept-language", "" },
    { "accept-ranges", "" },
    { "accept", "" },
    { "access-control-allow-origin", "" },
    { "age", "" },
    { "allow", "" },
    { "authorization", "" },
    { "cache-control", "" },
    { "content-disposition", "" },
    { "content-encoding", "" },
    { "content-language", "" },
    { "content-length", "" },
    { "content-location", "" },
    { "content-range", "" },
    { "content-type", "" },
    { "cookie", "" },
    { "date", "" },
    { "etag", "" },
    { "expect", "" },
    { "expires", "" },
    { "from", "" },
    { "host", "" },
    { "if-match", "" },
    { "if-modified-since", "" },
    { "if-none-match", "" },
    { "if-range", "" },
    { "if-unmodified-since", "" },
    { "last-modified", "" },
    { "link", "" },
    { "location", "" },
    { "max-forwards", "" },
    { "proxy-authenticate", "" },
    { "proxy-authorization", "" },
    { "range", "" },
    { "referer", "" },
    { "refresh", "" },
    { "retry-after", "" },
    { "server", "" },
    { "set-cookie", "" },
    { "strict-transport-security", "" },
    { "transfer-encoding", "" },
    { "user-agent", "" },
    { "vary", "" },
    { "via", "" },
    { "www-authenticate", "" },
};

void hpe_init(struct hpe *e) {
    e->max = HPE_TABLE;
    e->min = UINT32_MAX;
    e->size = 0;
    e->n = 0;
    e->pending = 0;
}

static void evict(struct hpe *e, uint32_t limit) {
    uint16_t k = 0;
    size_t bytes = 0;
    while (k < e->n && e->size > limit) {
        e->size -= (uint32_t)e->ent[k].nl + e->ent[k].vl + 32;
        bytes += (size_t)e->ent[k].nl + e->ent[k].vl;
        k++;
    }
    if (!k) return;
    size_t used = (size_t)e->ent[e->n - 1].off + e->ent[e->n - 1].nl + e->ent[e->n - 1].vl;
    memmove(e->data, e->data + bytes, used - bytes);
    for (uint16_t i = k; i < e->n; i++) {
        e->ent[i - k] = e->ent[i];
        e->ent[i - k].off = (uint16_t)(e->ent[i].off - bytes);
    }
    e->n = (uint16_t)(e->n - k);
}

void hpe_set_max(struct hpe *e, uint32_t v) {
    if (v > HPE_TABLE) v = HPE_TABLE;
    if (v < e->min) e->min = v;
    e->pending = 1;
    evict(e, v);
    e->max = v;
}

size_t hpe_bound(const struct h2_field *f, size_t n) {
    size_t b = 6;
    for (size_t i = 0; i < n; i++) {
        size_t s = 16 + strlen(f[i].name) + strlen(f[i].value);
        if (s < 16 || b + s < b) return SIZE_MAX;
        b += s;
    }
    return b;
}

size_t hpe_huff_len(const char *s, size_t n) {
    uint64_t bits = 0;
    for (size_t i = 0; i < n; i++) bits += huff_len[(unsigned char)s[i]];
    return (size_t)((bits + 7) / 8);
}

size_t hpe_huff(unsigned char *out, const char *s, size_t n) {
    uint64_t acc = 0;
    unsigned bits = 0;
    size_t o = 0;
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)s[i];
        acc = (acc << huff_len[c]) | huff_code[c];
        bits += huff_len[c];
        while (bits >= 8) {
            bits -= 8;
            out[o++] = (unsigned char)(acc >> bits);
        }
    }
    if (bits) out[o++] = (unsigned char)((acc << (8 - bits)) | (0xFFu >> bits));
    return o;
}

static size_t put_int(unsigned char *out, size_t p, unsigned bits, unsigned char flags, uint64_t v) {
    uint64_t k = (1u << bits) - 1;
    if (v < k) {
        out[p++] = (unsigned char)(flags | v);
        return p;
    }
    out[p++] = (unsigned char)(flags | k);
    v -= k;
    for (; v >= 128; v >>= 7) out[p++] = (unsigned char)(0x80 | (v & 0x7F));
    out[p++] = (unsigned char)v;
    return p;
}

static size_t put_str(unsigned char *out, size_t p, const char *s, size_t n) {
    size_t h = hpe_huff_len(s, n);
    if (h < n) {
        p = put_int(out, p, 7, 0x80, h);
        return p + hpe_huff(out + p, s, n);
    }
    p = put_int(out, p, 7, 0, n);
    memcpy(out + p, s, n);
    return p + n;
}

static int dyn_is(const struct hpe *e, uint16_t i, const char *s, size_t n, int value) {
    const struct hpe_ent *x = &e->ent[i];
    size_t l = value ? x->vl : x->nl;
    const unsigned char *d = e->data + x->off + (value ? x->nl : 0);
    return l == n && memcmp(d, s, n) == 0;
}

static void add(struct hpe *e, const char *name, size_t nl, const char *value, size_t vl) {
    uint32_t sz = (uint32_t)(nl + vl + 32);
    evict(e, e->max - sz);
    size_t off = e->n ? (size_t)e->ent[e->n - 1].off + e->ent[e->n - 1].nl + e->ent[e->n - 1].vl : 0;
    memcpy(e->data + off, name, nl);
    memcpy(e->data + off + nl, value, vl);
    e->ent[e->n++] = (struct hpe_ent){ (uint16_t)off, (uint16_t)nl, (uint16_t)vl };
    e->size += sz;
}

static size_t field(struct hpe *e, const struct h2_field *f, unsigned char *out, size_t p) {
    size_t nl = strlen(f->name), vl = strlen(f->value);
    unsigned sname = 0;
    for (unsigned i = 1; i <= 61; i++) {
        if (strcmp(stable[i][0], f->name)) continue;
        if (strcmp(stable[i][1], f->value) == 0) return put_int(out, p, 7, 0x80, i);
        sname = i;
    }
    unsigned dname = 0;
    for (uint16_t k = 0; k < e->n; k++) {
        uint16_t i = (uint16_t)(e->n - 1 - k);
        if (!dyn_is(e, i, f->name, nl, 0)) continue;
        if (dyn_is(e, i, f->value, vl, 1)) return put_int(out, p, 7, 0x80, 62u + k);
        if (!dname) dname = 62u + k;
    }
    unsigned idx = sname ? sname : dname;
    int indexing = nl + vl + 32 <= e->max;
    if (indexing) add(e, f->name, nl, f->value, vl);
    p = indexing ? put_int(out, p, 6, 0x40, idx) : put_int(out, p, 4, 0x00, idx);
    if (!idx) p = put_str(out, p, f->name, nl);
    return put_str(out, p, f->value, vl);
}

size_t hpe_block(struct hpe *e, const struct h2_field *f, size_t n, unsigned char *out, size_t cap) {
    if (!n) return (size_t)-1;
    if (cap < hpe_bound(f, n)) return 0;
    size_t p = 0;
    if (n && e->pending) {
        if (e->min < e->max) p = put_int(out, p, 5, 0x20, e->min);
        p = put_int(out, p, 5, 0x20, e->max);
        e->min = UINT32_MAX;
        e->pending = 0;
    }
    for (size_t i = 0; i < n; i++) p = field(e, &f[i], out, p);
    return p;
}

int hpack_huff_decode(const uint8_t *in, size_t n, uint8_t *out, size_t cap) {
    uint16_t cnt[31] = { 0 }, off[31], sym[256];
    for (unsigned c = 0; c < 256; c++) cnt[huff_len[c]]++;
    off[1] = 0;
    for (unsigned l = 1; l < 30; l++) off[l + 1] = (uint16_t)(off[l] + cnt[l]);
    for (unsigned c = 0; c < 256; c++) sym[off[huff_len[c]]++] = (uint16_t)c;
    uint32_t code = 0, first = 0, acc = 0;
    unsigned idx = 0, len = 0;
    size_t o = 0;
    for (size_t i = 0; i < n; i++) {
        for (int b = 7; b >= 0; b--) {
            unsigned bit = (in[i] >> b) & 1u;
            code |= bit;
            acc = (acc << 1) | bit;
            len++;
            uint32_t c = cnt[len];
            if (code < first + c) {
                unsigned s = sym[idx + (code - first)];
                if (o >= cap) return -1;
                out[o++] = (uint8_t)s;
                code = first = acc = 0;
                idx = len = 0;
            } else {
                if (len == 30) return -1;
                idx += c;
                first = (first + c) << 1;
                code <<= 1;
            }
        }
    }
    if (len > 7 || acc != (((uint32_t)1 << len) - 1)) return -1;
    return (int)o;
}
