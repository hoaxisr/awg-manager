#include <stdio.h>
#include <string.h>
#include <strings.h>
#include "xhopts.h"

struct xh_val { const char *p; size_t n; };

struct xh_raw {
    struct xh_val sess_id_place, sess_place, sess_id_key, sess_key, table, id_len, seq_place, seq_key,
        data_place, data_key, chunk, method, obfs, pad_key, pad_header, pad_place, pad_method, posts, no_grpc, max_hdr,
        interval, xm_conc, xm_conns, xm_reuse, xm_reqs, xm_secs, xm_keep;
};

static const struct { const char *name; size_t off; } xh_keys[] = {
    { "sessionIDPlacement", offsetof(struct xh_raw, sess_id_place) },
    { "sessionPlacement", offsetof(struct xh_raw, sess_place) },
    { "sessionIDKey", offsetof(struct xh_raw, sess_id_key) },
    { "sessionKey", offsetof(struct xh_raw, sess_key) },
    { "sessionIDTable", offsetof(struct xh_raw, table) },
    { "sessionIDLength", offsetof(struct xh_raw, id_len) },
    { "seqPlacement", offsetof(struct xh_raw, seq_place) },
    { "seqKey", offsetof(struct xh_raw, seq_key) },
    { "uplinkDataPlacement", offsetof(struct xh_raw, data_place) },
    { "uplinkDataKey", offsetof(struct xh_raw, data_key) },
    { "uplinkChunkSize", offsetof(struct xh_raw, chunk) },
    { "uplinkHTTPMethod", offsetof(struct xh_raw, method) },
    { "xPaddingObfsMode", offsetof(struct xh_raw, obfs) },
    { "xPaddingKey", offsetof(struct xh_raw, pad_key) },
    { "xPaddingHeader", offsetof(struct xh_raw, pad_header) },
    { "xPaddingPlacement", offsetof(struct xh_raw, pad_place) },
    { "xPaddingMethod", offsetof(struct xh_raw, pad_method) },
    { "scMaxBufferedPosts", offsetof(struct xh_raw, posts) },
    { "noGRPCHeader", offsetof(struct xh_raw, no_grpc) },
    { "serverMaxHeaderBytes", offsetof(struct xh_raw, max_hdr) },
    { "scMinPostsIntervalMs", offsetof(struct xh_raw, interval) },
    { "xmux.maxConcurrency", offsetof(struct xh_raw, xm_conc) },
    { "xmux.maxConnections", offsetof(struct xh_raw, xm_conns) },
    { "xmux.cMaxReuseTimes", offsetof(struct xh_raw, xm_reuse) },
    { "xmux.hMaxRequestTimes", offsetof(struct xh_raw, xm_reqs) },
    { "xmux.hMaxReusableSecs", offsetof(struct xh_raw, xm_secs) },
    { "xmux.hKeepAlivePeriod", offsetof(struct xh_raw, xm_keep) },
};

static const struct { const char *name, *alphabet; } xh_tables[] = {
    { "ALPHABET", "ABCDEFGHIJKLMNOPQRSTUVWXYZ" },
    { "Alphabet", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" },
    { "BASE36", "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ" },
    { "Base62", "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" },
    { "HEX", "0123456789ABCDEF" },
    { "alphabet", "abcdefghijklmnopqrstuvwxyz" },
    { "base36", "0123456789abcdefghijklmnopqrstuvwxyz" },
    { "hex", "0123456789abcdef" },
    { "number", "0123456789" },
};

static int unsupported(char *why, size_t why_n, const char *key, struct xh_val v) {
    snprintf(why, why_n, "xhttp: unsupported %s: %.*s", key, (int)v.n, v.p);
    return -1;
}

static int is(struct xh_val v, const char *s) {
    return v.n == strlen(s) && !memcmp(v.p, s, v.n);
}

static int put(char *dst, size_t cap, struct xh_val v) {
    if (v.n >= cap) return -1;
    memcpy(dst, v.p, v.n);
    dst[v.n] = '\0';
    return 0;
}

static int num(const char *p, size_t n, uint32_t *out) {
    uint64_t x = 0;
    if (!n) return -1;
    for (size_t i = 0; i < n; i++) {
        if (p[i] < '0' || p[i] > '9') return -1;
        x = x * 10 + (uint64_t)(p[i] - '0');
        if (x > 0x7fffffff) return -1;
    }
    *out = (uint32_t)x;
    return 0;
}

static int range(struct xh_val v, uint32_t *from, uint32_t *to) {
    const char *dash = memchr(v.p, '-', v.n);
    if (!dash) {
        if (num(v.p, v.n, from)) return -1;
        *to = *from;
        return 0;
    }
    size_t a = (size_t)(dash - v.p);
    if (num(v.p, a, from) || num(dash + 1, v.n - a - 1, to)) return -1;
    if (*from > *to) { uint32_t t = *from; *from = *to; *to = t; }
    return 0;
}

static int sint(const char *p, size_t n, int64_t hi, int64_t *out) {
    uint64_t x = 0;
    int neg = n && *p == '-';
    if (n && (*p == '-' || *p == '+')) p++, n--;
    if (!n) return -1;
    uint64_t lim = (uint64_t)hi + (uint64_t)neg;
    for (size_t i = 0; i < n; i++) {
        if (p[i] < '0' || p[i] > '9') return -1;
        uint64_t d = (uint64_t)(p[i] - '0');
        if (x > (lim - d) / 10) return -1;
        x = x * 10 + d;
    }
    *out = neg ? (int64_t)(0 - x) : (int64_t)x;
    return 0;
}

static int xm_range(struct xh_val v, struct xm_range *r) {
    int64_t a, b;
    if (!sint(v.p, v.n, INT32_MAX, &a)) {
        r->from = r->to = (int32_t)a;
        return 0;
    }
    size_t skip = v.n && v.p[0] == '-';
    const char *dash = memchr(v.p + skip, '-', v.n - skip);
    if (!dash) return -1;
    size_t at = (size_t)(dash - v.p);
    if (sint(v.p, at, INT32_MAX, &a) || sint(dash + 1, v.n - at - 1, INT32_MAX, &b)) return -1;
    r->from = (int32_t)(a < b ? a : b);
    r->to = (int32_t)(a < b ? b : a);
    return 0;
}

static int xmux(const struct xh_raw *r, struct xm_cfg *x, char *why, size_t why_n) {
    static const char *const names[] = { "xmux.maxConcurrency", "xmux.maxConnections", "xmux.cMaxReuseTimes",
                                         "xmux.hMaxRequestTimes", "xmux.hMaxReusableSecs" };
    const struct xh_val *vals[] = { &r->xm_conc, &r->xm_conns, &r->xm_reuse, &r->xm_reqs, &r->xm_secs };
    struct xm_range *dst[] = { &x->max_conc, &x->max_conns, &x->c_reuse, &x->h_reqs, &x->h_secs };
    for (size_t i = 0; i < 5; i++) {
        if (!vals[i]->n) continue;
        x->set = 1;
        if (xm_range(*vals[i], dst[i])) return unsupported(why, why_n, names[i], *vals[i]);
    }
    if (r->xm_keep.n) {
        x->set = 1;
        if (sint(r->xm_keep.p, r->xm_keep.n, INT64_MAX, &x->keepalive_s))
            return unsupported(why, why_n, "xmux.hKeepAlivePeriod", r->xm_keep);
    }
    if (x->max_conc.to > 0 && x->max_conns.to > 0) {
        snprintf(why, why_n, "xhttp: maxConnections cannot be specified together with maxConcurrency");
        return -1;
    }
    return 0;
}

static int xm_zero(struct xm_range r) {
    return !r.from && !r.to;
}

void xm_cfg_effective(const struct xm_cfg *in, struct xm_cfg *out) {
    *out = *in;
    if (!in->set || (xm_zero(in->max_conc) && xm_zero(in->max_conns) && xm_zero(in->c_reuse) &&
                     xm_zero(in->h_reqs) && xm_zero(in->h_secs) && !in->keepalive_s)) {
        memset(out, 0, sizeof *out);
        out->set = in->set;
        out->max_conns = (struct xm_range){ 3, 3 };
        out->h_reqs = (struct xm_range){ 600, 900 };
        out->h_secs = (struct xm_range){ 1800, 3000 };
    }
    int one = out->max_conns.from == out->max_conns.to;
    if (out->max_conns.from > 16) out->max_conns.from = 16;
    if (one) out->max_conns.to = out->max_conns.from;
    else if (out->max_conns.to > 17) out->max_conns.to = 17;
}

static int place(struct xh_val v, const char *const *names, const enum xh_place *vals, size_t n,
                 enum xh_place *out) {
    for (size_t i = 0; i < n; i++)
        if (is(v, names[i])) { *out = vals[i]; return 0; }
    return -1;
}

static int meta_place(struct xh_val v, enum xh_place *out) {
    static const char *const names[] = { "path", "query", "header", "cookie" };
    static const enum xh_place vals[] = { XHP_PATH, XHP_QUERY, XHP_HEADER, XHP_COOKIE };
    if (!v.n) { *out = XHP_PATH; return 0; }
    return place(v, names, vals, 4, out);
}

static void meta_key(char *dst, enum xh_place p, const char *hdr, const char *other) {
    snprintf(dst, 64, "%s", p == XHP_HEADER ? hdr : p == XHP_PATH ? "" : other);
}

static int tchar(char c) {
    return (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strchr("!#$%&'*+-.^_`|~", c);
}

static int token(const char *k) {
    if (!*k) return 0;
    for (; *k; k++)
        if (!(*k >= 'a' && *k <= 'z') && !tchar(*k)) return 0;
    return 1;
}

static int bad_header_key(int in_header, const char *k, char *why, size_t why_n, const char *name,
                          struct xh_val v) {
    return in_header && !token(k) ? unsupported(why, why_n, name, v) : 0;
}

static int table_char(char c) {
    return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strchr("._~-", c);
}

static uint64_t room(size_t base, uint32_t from, uint32_t to) {
    const uint64_t enough = (uint64_t)2 << 30;
    uint64_t sum = 0;
    for (uint64_t k = from; k <= to; k++) {
        uint64_t term = 1;
        for (uint64_t i = 0; i < k && term < enough; i++) term *= base;
        sum += term < enough ? term : enough;
        if (sum >= enough) return enough;
    }
    return sum;
}

static int split(const char *xhx, struct xh_raw *r, char *why, size_t why_n) {
    memset(r, 0, sizeof *r);
    for (const char *s = xhx; s && *s;) {
        const char *e = strchr(s, '\n');
        size_t len = e ? (size_t)(e - s) : strlen(s);
        const char *eq = memchr(s, '=', len);
        struct xh_val line = { s, len };
        if (!eq) return unsupported(why, why_n, "xhttp line", line);
        size_t kn = (size_t)(eq - s);
        struct xh_val v = { eq + 1, len - kn - 1 };
        size_t i = 0;
        while (i < sizeof xh_keys / sizeof xh_keys[0] &&
               !(strlen(xh_keys[i].name) == kn && !memcmp(xh_keys[i].name, s, kn))) i++;
        if (i == sizeof xh_keys / sizeof xh_keys[0]) return unsupported(why, why_n, "xhttp key", line);
        *(struct xh_val *)((char *)r + xh_keys[i].off) = v;
        s = e ? e + 1 : s + len;
    }
    return 0;
}

int xh_opts_parse(const char *xhx, const char *mode, uint16_t pad_to, struct xh_opts *o, char *why,
                  size_t why_n) {
    struct xh_raw r;
    int packet_up = mode && !strcmp(mode, "packet-up");
    memset(o, 0, sizeof *o);
    if (split(xhx, &r, why, why_n)) return -1;

    if (r.posts.n && num(r.posts.p, r.posts.n, &o->posts)) return unsupported(why, why_n, "scMaxBufferedPosts", r.posts);
    if (r.max_hdr.n && num(r.max_hdr.p, r.max_hdr.n, &o->max_hdr))
        return unsupported(why, why_n, "serverMaxHeaderBytes", r.max_hdr);
    if (r.interval.n && xm_range(r.interval, &o->interval))
        return unsupported(why, why_n, "scMinPostsIntervalMs", r.interval);
    if (!o->interval.to) o->interval = (struct xm_range){ 30, 30 };
    if (xmux(&r, &o->xmux, why, why_n)) return -1;

    struct xh_val sp = r.sess_id_place.n ? r.sess_id_place : r.sess_place;
    const char *sp_name = r.sess_id_place.n ? "sessionIDPlacement" : "sessionPlacement";
    if (meta_place(sp, &o->sess_place)) return unsupported(why, why_n, sp_name, sp);
    if (meta_place(r.seq_place, &o->seq_place)) return unsupported(why, why_n, "seqPlacement", r.seq_place);

    struct xh_val sk = r.sess_id_key.n ? r.sess_id_key : r.sess_key;
    const char *sk_name = r.sess_id_key.n ? "sessionIDKey" : "sessionKey";
    if (sk.n) { if (put(o->sess_key, sizeof o->sess_key, sk)) return unsupported(why, why_n, sk_name, sk); }
    else meta_key(o->sess_key, o->sess_place, "X-Session", "x_session");
    if (r.seq_key.n) { if (put(o->seq_key, sizeof o->seq_key, r.seq_key)) return unsupported(why, why_n, "seqKey", r.seq_key); }
    else meta_key(o->seq_key, o->seq_place, "X-Seq", "x_seq");
    if (bad_header_key(o->sess_place == XHP_HEADER, o->sess_key, why, why_n, sk_name, sk) ||
        bad_header_key(o->seq_place == XHP_HEADER, o->seq_key, why, why_n, "seqKey", r.seq_key))
        return -1;

    static const char *const dnames[] = { "auto", "body", "header", "cookie" };
    static const enum xh_place dvals[] = { XHP_AUTO, XHP_BODY, XHP_HEADER, XHP_COOKIE };
    o->data_place = XHP_AUTO;
    if (r.data_place.n && place(r.data_place, dnames, dvals, 4, &o->data_place))
        return unsupported(why, why_n, "uplinkDataPlacement", r.data_place);
    if ((o->data_place == XHP_HEADER || o->data_place == XHP_COOKIE) && !packet_up) {
        snprintf(why, why_n, "xhttp: uplinkDataPlacement needs mode=packet-up");
        return -1;
    }
    if (r.data_key.n) {
        if (put(o->data_key, sizeof o->data_key, r.data_key)) return unsupported(why, why_n, "uplinkDataKey", r.data_key);
    } else if (o->data_place != XHP_BODY) {
        snprintf(o->data_key, sizeof o->data_key, "%s", o->data_place == XHP_COOKIE ? "x_data" : "X-Data");
    }
    if (bad_header_key(o->data_place == XHP_HEADER, o->data_key, why, why_n, "uplinkDataKey", r.data_key))
        return -1;

    uint32_t cf = 0, ct = 0;
    if (r.chunk.n && range(r.chunk, &cf, &ct)) return unsupported(why, why_n, "uplinkChunkSize", r.chunk);
    if (ct) {
        o->chunk_from = cf < 64 ? 64 : cf;
        o->chunk_to = cf < 64 && ct < 64 ? 64 : ct;
    } else if (o->data_place == XHP_HEADER) {
        o->chunk_from = 3000, o->chunk_to = 4000;
    } else if (o->data_place == XHP_COOKIE) {
        o->chunk_from = 2048, o->chunk_to = 3072;
    }

    snprintf(o->method, sizeof o->method, "POST");
    if (r.method.n) {
        if (put(o->method, sizeof o->method, r.method)) return unsupported(why, why_n, "uplinkHTTPMethod", r.method);
        for (char *c = o->method; *c; c++) {
            if (*c >= 'a' && *c <= 'z') *c = (char)(*c - 'a' + 'A');
            if (!tchar(*c)) return unsupported(why, why_n, "uplinkHTTPMethod", r.method);
        }
    }
    if (!strcmp(o->method, "GET") && !packet_up) {
        snprintf(why, why_n, "xhttp: uplinkHTTPMethod needs mode=packet-up");
        return -1;
    }

    uint32_t lf = 0, lt = 0;
    if (r.id_len.n && range(r.id_len, &lf, &lt)) return unsupported(why, why_n, "sessionIDLength", r.id_len);
    if (r.table.n) {
        if (lt > 128) return unsupported(why, why_n, "sessionIDLength", r.id_len);
        o->id_len_from = (uint16_t)lf, o->id_len_to = (uint16_t)lt;
        struct xh_val t = r.table;
        for (size_t i = 0; i < sizeof xh_tables / sizeof xh_tables[0]; i++)
            if (is(r.table, xh_tables[i].name)) { t.p = xh_tables[i].alphabet; t.n = strlen(t.p); break; }
        if (put(o->table, sizeof o->table, t)) return unsupported(why, why_n, "sessionIDTable", r.table);
        if (room(t.n, lf, lt) < ((uint64_t)2 << 30)) {
            snprintf(why, why_n, "xhttp: sessionIDTable or sessionIDLength is too small");
            return -1;
        }
        if (!lf) return unsupported(why, why_n, "sessionIDLength", r.id_len);
        for (size_t i = 0; i < t.n; i++)
            if (!table_char(t.p[i])) return unsupported(why, why_n, "sessionIDTable", r.table);
    }

    if (r.obfs.n && !is(r.obfs, "true") && !is(r.obfs, "false")) return unsupported(why, why_n, "xPaddingObfsMode", r.obfs);
    o->obfs = is(r.obfs, "true");
    if (r.no_grpc.n && !is(r.no_grpc, "true") && !is(r.no_grpc, "false")) return unsupported(why, why_n, "noGRPCHeader", r.no_grpc);
    o->no_grpc = is(r.no_grpc, "true");

    snprintf(o->pad_key, sizeof o->pad_key, "x_padding");
    snprintf(o->pad_header, sizeof o->pad_header, "X-Padding");
    if (r.pad_key.n && put(o->pad_key, sizeof o->pad_key, r.pad_key)) return unsupported(why, why_n, "xPaddingKey", r.pad_key);
    if (r.pad_header.n && put(o->pad_header, sizeof o->pad_header, r.pad_header))
        return unsupported(why, why_n, "xPaddingHeader", r.pad_header);

    static const char *const pnames[] = { "queryInHeader", "header", "cookie", "query" };
    static const enum xh_place pvals[] = { XHP_QUERY_IN_HEADER, XHP_HEADER, XHP_COOKIE, XHP_QUERY };
    o->pad_place = XHP_QUERY_IN_HEADER;
    if (r.pad_place.n && place(r.pad_place, pnames, pvals, 4, &o->pad_place))
        return unsupported(why, why_n, "xPaddingPlacement", r.pad_place);
    int pad_in_header = o->obfs && (o->pad_place == XHP_HEADER || o->pad_place == XHP_QUERY_IN_HEADER);
    if (bad_header_key(pad_in_header, o->pad_header, why, why_n, "xPaddingHeader", r.pad_header))
        return -1;

    if (r.pad_method.n && !is(r.pad_method, "repeat-x") && !is(r.pad_method, "tokenish"))
        return unsupported(why, why_n, "xPaddingMethod", r.pad_method);
    o->tokenish = is(r.pad_method, "tokenish");

    size_t pad_chars = o->tokenish ? ((size_t)pad_to * 5 + 3) / 4 + 150 : pad_to;
    if (o->obfs && o->pad_place == XHP_QUERY && pad_chars + 128 + 24 + 16 + 512 > XH_PATH_MAX) {
        snprintf(why, why_n, "xhttp: padding too long for the path");
        return -1;
    }

    return 0;
}
