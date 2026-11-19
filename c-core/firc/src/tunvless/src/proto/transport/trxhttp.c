/* The xhttp transport: the protocol stream in HTTP/2 request bodies, on HTTP/2 connections of
 * the flow's own (xconn.h), or on connections the flows of a node share (trxmux.c).
 *
 * In stream-one mode xhttp wraps nothing: the request body is the stream up, the response body
 * the stream down. But it needs padding: the server checks the x_padding length in Referer and
 * answers 400 without it (see Xray hub.go). The stream-up and packet-up modes are described at
 * enum xhttp_mode in transport.h.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#include <errno.h>
#include <poll.h>
#include "osrand.h"
#include "tscratch.h"

#include <sys/epoll.h>
#include <time.h>

#include "transport.h"
#include "xconn.h"
#include "fmlink.h"
#include "xhreq.h"
#include "trxmux.h"

/* The node's xhttp mode. Empty and "auto" resolve as in Xray's client (splithttp/dialer.go):
 * with reality stream-one, or stream-up when the download goes to its own endpoint; packet-up
 * otherwise (tls, none). A server may accept only the mode it was set to, and a packet-up server
 * answers 400 to stream-one, so auto must pick what Xray's client would. Other modes are named
 * in the link, and the node parser has dropped what we do not support (sl_link_usable_post in
 * sublink.c), so only these three arrive here. */
static enum xhttp_mode xhttp_mode_of(const struct tr_node *n) {
    if (!strcmp(n->mode, "packet-up")) return XH_PACKET_UP;
    if (!strcmp(n->mode, "stream-up")) return XH_STREAM_UP;
    if (!strcmp(n->mode, "stream-one")) return XH_STREAM_ONE;
    if (!n->security || strcmp(n->security, "reality")) return XH_PACKET_UP;
    return n->dl ? XH_STREAM_UP : XH_STREAM_ONE;
}

#define XH_BLOCK 16384
#define XH_MAIN 72
#define XH_PIECES 256
#define XH_PAIRS 72
#define XH_ARENA 32768
#define XH_SEQ_WORST "18446744073709551615"

enum xh_kind { XK_ONE, XK_DOWN, XK_UP, XK_PACKET };

struct xh_ar { char *p; size_t n, cap; int over; };

static char *ar_begin(struct xh_ar *a) { return a->p + a->n; }

static void ar_cat(struct xh_ar *a, const char *s, size_t n) {
    if (a->over || a->n + n + 1 > a->cap) { a->over = 1; return; }
    memcpy(a->p + a->n, s, n);
    a->n += n;
}

static char *ar_end(struct xh_ar *a, char *start) {
    if (a->over || a->n + 1 > a->cap) { a->over = 1; return NULL; }
    a->p[a->n++] = '\0';
    return start;
}

static const char *ar_str(struct xh_ar *a, const char *s, size_t n) {
    char *b = ar_begin(a);
    ar_cat(a, s, n);
    return ar_end(a, b);
}

static const char *ar_join(struct xh_ar *a, const char *x, const char *sep, const char *y) {
    char *b = ar_begin(a);
    ar_cat(a, x, strlen(x));
    ar_cat(a, sep, strlen(sep));
    ar_cat(a, y, strlen(y));
    return ar_end(a, b);
}

struct xh_kv { const char *k, *v; };

struct xh_rq {
    struct h2_req r;
    struct h2_field main[XH_MAIN], ck[4], dat[XH_PIECES], all[XH_MAIN + 4 + XH_PIECES];
    size_t main_n, ck_n, dat_n, lead;
    struct xh_kv kv[XH_PAIRS];
    size_t kv_n;
    char path[XH_PATH_MAX];
    struct xh_ar a;
};

struct xh_scratch { struct xh_rq q; char arena[XH_ARENA]; unsigned char blk[XH_BLOCK]; };

static struct xh_scratch *rq_held(void) { return ts_g[TS_XHTTP]; }

static struct xh_rq *rq_get(void) {
    struct xh_scratch *s = ts_get(TS_XHTTP, sizeof(struct xh_scratch));
    return s ? &s->q : NULL;
}

static void rq_field(struct xh_rq *q, const char *name, const char *value) {
    if (!name || !value || q->main_n >= XH_MAIN) { q->a.over = 1; return; }
    q->main[q->main_n++] = (struct h2_field){ name, value };
}

static void rq_cookie(struct xh_rq *q, const char *name, const char *value) {
    const char *c = ar_join(&q->a, name, "=", value);
    if (!c || q->ck_n >= 4) { q->a.over = 1; return; }
    q->ck[q->ck_n++] = (struct h2_field){ "cookie", c };
}

static int rq_shadowed(const struct xh_rq *q, const char *name, int with_data) {
    if (q->r.content_type && !strcasecmp(name, "Content-Type")) return 1;
    for (size_t i = q->lead; i < q->main_n; i++)
        if (!strcasecmp(name, q->main[i].name)) return 1;
    for (size_t i = 0; with_data && i < q->dat_n; i++)
        if (strcmp(q->dat[i].name, "cookie") && !strcasecmp(name, q->dat[i].name)) return 1;
    return 0;
}

static void rq_join(struct xh_rq *q, int with_data) {
    size_t n = 0;
    for (size_t i = 0; i < q->lead; i++)
        if (!rq_shadowed(q, q->main[i].name, with_data)) q->all[n++] = q->main[i];
    for (size_t i = q->lead; i < q->main_n; i++) q->all[n++] = q->main[i];
    for (size_t i = 0; with_data && i < q->dat_n; i++) q->all[n++] = q->dat[i];
    for (size_t i = 0; i < q->ck_n; i++) q->all[n++] = q->ck[i];
    q->r.fields = q->all;
    q->r.fields_n = n;
}

static int hexv(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static const char *q_unescape(struct xh_ar *a, const char *s, size_t n) {
    char *b = ar_begin(a);
    for (size_t i = 0; i < n; i++) {
        char c = s[i];
        if (c == '+') c = ' ';
        else if (c == '%') {
            if (i + 2 >= n) return NULL;
            int h = hexv(s[i + 1]), l = hexv(s[i + 2]);
            if (h < 0 || l < 0) return NULL;
            c = (char)(h << 4 | l);
            i += 2;
        }
        ar_cat(a, &c, 1);
    }
    return ar_end(a, b);
}

static void q_set(struct xh_rq *q, const char *k, const char *v) {
    size_t j = 0;
    for (size_t i = 0; i < q->kv_n; i++)
        if (strcmp(q->kv[i].k, k)) q->kv[j++] = q->kv[i];
    q->kv_n = j;
    if (q->kv_n >= XH_PAIRS) { q->a.over = 1; return; }
    q->kv[q->kv_n++] = (struct xh_kv){ k, v };
}

static void q_parse(struct xh_rq *q, const char *raw) {
    while (*raw) {
        const char *amp = strchr(raw, '&');
        size_t len = amp ? (size_t)(amp - raw) : strlen(raw);
        const char *eq = memchr(raw, '=', len);
        if (len && !memchr(raw, ';', len) && q->kv_n < XH_PAIRS) {
            size_t kn = eq ? (size_t)(eq - raw) : len;
            const char *k = q_unescape(&q->a, raw, kn);
            const char *v = k ? q_unescape(&q->a, eq ? eq + 1 : raw + len, eq ? len - kn - 1 : 0) : NULL;
            if (k && v) q->kv[q->kv_n++] = (struct xh_kv){ k, v };
        }
        raw += len + (amp ? 1 : 0);
    }
}

static void q_escape(struct xh_ar *a, const char *s) {
    static const char hx[] = "0123456789ABCDEF";
    for (; *s; s++) {
        unsigned char c = (unsigned char)*s;
        if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
            c == '-' || c == '_' || c == '.' || c == '~') {
            ar_cat(a, s, 1);
        } else if (c == ' ') {
            ar_cat(a, "+", 1);
        } else {
            char e[3] = { '%', hx[c >> 4], hx[c & 15] };
            ar_cat(a, e, 3);
        }
    }
}

static const char *q_encode(struct xh_rq *q) {
    for (size_t i = 1; i < q->kv_n; i++)
        for (size_t j = i; j > 0 && strcmp(q->kv[j - 1].k, q->kv[j].k) > 0; j--) {
            struct xh_kv t = q->kv[j];
            q->kv[j] = q->kv[j - 1];
            q->kv[j - 1] = t;
        }
    char *b = ar_begin(&q->a);
    for (size_t i = 0; i < q->kv_n; i++) {
        if (i) ar_cat(&q->a, "&", 1);
        q_escape(&q->a, q->kv[i].k);
        ar_cat(&q->a, "=", 1);
        q_escape(&q->a, q->kv[i].v);
    }
    return ar_end(&q->a, b);
}

static void path_add(char *path, const char *v) {
    size_t n = strlen(path);
    snprintf(path + n, XH_PATH_MAX - n, "%s%s", n && path[n - 1] == '/' ? "" : "/", v);
}

static void trim_sp(const char **v, size_t *vn) {
    while (*vn && (**v == ' ' || **v == '\t')) { (*v)++; (*vn)--; }
    while (*vn && ((*v)[*vn - 1] == ' ' || (*v)[*vn - 1] == '\t')) (*vn)--;
}

static int ua_look(const char *v, size_t vn) {
    static const struct { const char *w; int look; } W[] = {
        { "chrome", H2L_CHROME }, { "edge", H2L_EDGE }, { "firefox", H2L_FIREFOX },
        { "safari", H2L_SAFARI }, { "curl", H2L_CURL }, { "golang", H2L_GOLANG },
    };
    for (size_t i = 0; i < sizeof W / sizeof *W; i++)
        if (vn == strlen(W[i].w) && !memcmp(v, W[i].w, vn)) return W[i].look;
    return H2L_NONE;
}

static void rq_custom(struct xh_rq *q, const char *h) {
    int seen_ua = 0;
    while (h && *h) {
        const char *eol = strchr(h, '\n');
        size_t ln = eol ? (size_t)(eol - h) : strlen(h);
        const char *colon = memchr(h, ':', ln);
        if (colon) {
            size_t kn = (size_t)(colon - h);
            const char *v = colon + 1;
            size_t vn = ln - kn - 1;
            trim_sp(&v, &vn);
            int host = kn == 4 && !strncasecmp(h, "Host", 4);
            int ua = kn == 10 && !strncasecmp(h, "User-Agent", 10);
            if (ua && !seen_ua) q->r.look = ua_look(v, vn);
            seen_ua |= ua;
            int drop = host || (ua && q->r.look != H2L_NONE);
            if (!drop) rq_field(q, ar_str(&q->a, h, kn), ar_str(&q->a, v, vn));
        }
        h += ln + (eol ? 1 : 0);
    }
    q->lead = q->main_n;
}

static size_t pad_worst(const struct xh_side *x) {
    size_t hi = x->pad_to ? x->pad_to : 1000;
    return x->opts.obfs && x->opts.tokenish ? (hi * 5 + 3) / 4 + 150 : hi;
}

/* Each request gets its own length, not one per connection: packet-up sends a series of
 * requests, and the same padding length in all of them would turn the padding itself into a
 * signature, the very thing it is there against. */
static size_t pad_len(const struct xh_side *x) {
    /* THE SERVER SETS THE RANGE, NOT US. It comes in the link as `xPaddingBytes` (see
     * sl_pad_range in sublink.c), and the server CHECKS it: outside the range means 400. A
     * fixed range that fits Xray's default fails every xhttp node of a provider that announces,
     * say, "50-150": TLS passes, Reality accepts, then 400, and the nodes look dead.
     *
     * Not announced: Xray's default 100..1000 (GetNormalizedXPaddingBytes), to match upstream in
     * both the bounds and the middle. */
    size_t lo = x->pad_to ? x->pad_from : 100;
    size_t hi = x->pad_to ? x->pad_to : 1000;
    unsigned char r = 0;
    if (os_getrandom(&r, 1, 0) != 1) r = 128;
    size_t pad = lo + (size_t)r * (hi - lo + 1) / 256;
    if (pad < lo) pad = lo;
    if (pad > hi) pad = hi;
    return pad;
}

static const char *rq_pad(struct xh_rq *q, const struct xh_side *x, const char *prefix, int worst) {
    char *b = ar_begin(&q->a);
    ar_cat(&q->a, prefix, strlen(prefix));
    size_t len = worst ? pad_worst(x) : pad_len(x);
    int tok = x->opts.obfs && x->opts.tokenish && !worst;
    size_t cap = tok ? (len * 5 + 3) / 4 + 152 : len + 1;
    if (len > XH_BLOCK || q->a.over || q->a.n + cap > q->a.cap) { q->a.over = 1; return NULL; }
    if (!tok) memset(q->a.p + q->a.n, 'X', len);
    else len = xh_padding(&x->opts, len, q->a.p + q->a.n, cap);
    q->a.n += len;
    return ar_end(&q->a, b);
}

static void rq_meta(struct xh_rq *q, enum xh_place p, const char *key, const char *v) {
    if (p == XHP_PATH) path_add(q->path, v);
    else if (p == XHP_QUERY) q_set(q, key, v);
    else if (p == XHP_HEADER) rq_field(q, key, v);
    else if (p == XHP_COOKIE) rq_cookie(q, key, v);
}

static int rq_build(struct transport *t, enum xh_kind k, const char *seq, int worst) {
    const struct xh_side *x = k == XK_DOWN ? &t->xh.down_side : &t->xh.main_side;
    const struct xh_opts *o = &x->opts;
    struct xh_rq *q = rq_get();
    if (!q) return TR_EIO;
    memset(&q->r, 0, sizeof q->r);
    q->main_n = q->ck_n = q->dat_n = q->kv_n = q->lead = 0;
    q->a = (struct xh_ar){ rq_held()->arena, 0, sizeof rq_held()->arena, 0 };
    rq_custom(q, x->headers);

    const char *sid = k == XK_ONE ? NULL : t->xh.sid;
    int padq = o->obfs && o->pad_place == XHP_QUERY;
    int anyq = padq || (sid && o->sess_place == XHP_QUERY) || (seq && o->seq_place == XHP_QUERY);
    if (anyq) q_parse(q, x->query);

    char prefix[600];
    const char *pad;
    if (!o->obfs || o->pad_place == XHP_QUERY_IN_HEADER) {
        const char *key = o->obfs ? o->pad_key : "x_padding";
        snprintf(prefix, sizeof prefix, "https://%s%s?%s=", x->authority, x->base_path, key);
        pad = rq_pad(q, x, prefix, worst);
        rq_field(q, o->obfs ? o->pad_header : "Referer", pad);
    } else {
        pad = rq_pad(q, x, "", worst);
        if (o->pad_place == XHP_HEADER) rq_field(q, o->pad_header, pad);
        else if (pad && pad[0] && o->pad_place == XHP_COOKIE) rq_cookie(q, o->pad_key, pad);
        else if (pad && pad[0]) q_set(q, o->pad_key, pad);
    }

    snprintf(q->path, sizeof q->path, "%s", x->base_path);
    if (sid) rq_meta(q, o->sess_place, o->sess_key, sid);
    if (seq) rq_meta(q, o->seq_place, o->seq_key, seq);
    const char *query = anyq ? q_encode(q) : x->query;
    if (query && query[0]) {
        size_t n = strlen(q->path);
        snprintf(q->path + n, sizeof q->path - n, "?%s", query);
    }
    if (q->a.over || !query || strlen(q->path) + 1 >= sizeof q->path) return H2_ETOOBIG;

    q->r.authority = x->authority;
    q->r.path = q->path;
    q->r.method = k == XK_DOWN ? "GET" : o->method;
    q->r.content_type = (k == XK_ONE || k == XK_UP) && !o->no_grpc ? "application/grpc" : NULL;
    q->r.end_stream = k == XK_DOWN;
    q->r.body_len = k == XK_ONE || k == XK_UP ? -1 : 0;
    rq_join(q, 0);
    return 0;
}

static int data_meta(const struct xh_opts *o) {
    return o->data_place == XHP_HEADER || o->data_place == XHP_COOKIE;
}

static size_t b64_len(size_t m) { return m / 3 * 4 + (m % 3 ? m % 3 + 1 : 0); }

static size_t ndig(size_t i) {
    size_t d = 1;
    while (i >= 10) { i /= 10; d++; }
    return d;
}

static size_t data_cost(const struct xh_opts *o, size_t e, const uint32_t *cs) {
    size_t kl = strlen(o->data_key), extra = o->data_place == XHP_COOKIE ? 7 : 0, s = 0;
    for (size_t i = 0; e; i++) {
        size_t c = cs && i < XH_PIECES ? cs[i] : o->chunk_from;
        if (c > e) c = e;
        s += c + kl + 1 + ndig(i) + 32 + extra;
        e -= c;
    }
    return s;
}

static size_t data_fit(const struct xh_opts *o, size_t base, size_t budget, size_t hi,
                       const uint32_t *cs) {
    size_t lo = 0;
    if (hi > XH_BLOCK / 4 * 3) hi = XH_BLOCK / 4 * 3;
    if (base >= budget) return 0;
    while (lo < hi) {
        size_t mid = lo + (hi - lo + 1) / 2;
        if (base + data_cost(o, b64_len(mid), cs) <= budget) lo = mid;
        else hi = mid - 1;
    }
    return lo;
}

static struct xconn *x_post(const struct transport *t) {
    return t->xh.f.shared ? t->xh.f.post_x : t->xh.priv[0];
}

static size_t meta_budget(const struct transport *t) {
    const struct xconn *m = x_post(t);
    uint32_t b = m ? h2m_header_budget(&m->h2, t->xh.main_side.opts.max_hdr) : 8192;
    return b < XH_BLOCK ? b : XH_BLOCK;
}

static size_t packet_cap(struct transport *t) {
    struct xh_state *x = &t->xh;
    if (!x->worst_base) {
        if (rq_build(t, XK_PACKET, XH_SEQ_WORST, 1)) return 0;
        size_t s = h2w_list_size(&rq_held()->q.r, 1);
        if (s == SIZE_MAX) return 0;
        x->worst_base = s;
    }
    size_t hi = x->post_max ? x->post_max : XH_BLOCK;
    return data_fit(&x->main_side.opts, x->worst_base, meta_budget(t), hi, NULL);
}

static void b64_put(struct xh_ar *a, const unsigned char *d, size_t m, size_t from, size_t cnt) {
    static const char al[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    for (size_t p = from; p < from + cnt; p++) {
        size_t bit = p * 6, i = bit / 8;
        unsigned v = (unsigned)d[i] << 8 | (i + 1 < m ? d[i + 1] : 0);
        char c = al[(v >> (10 - bit % 8)) & 63];
        ar_cat(a, &c, 1);
    }
}

static int rq_data(struct transport *t, const unsigned char *d, size_t n, size_t *took) {
    struct xh_state *x = &t->xh;
    const struct xh_opts *o = &x->main_side.opts;
    struct xh_rq *q = &rq_held()->q;
    uint32_t cs[XH_PIECES], r[XH_PIECES];
    uint32_t span = o->chunk_to - o->chunk_from + 1;
    if (os_getrandom(r, sizeof r, 0) != (ssize_t)sizeof r) memset(r, 0, sizeof r);
    for (size_t i = 0; i < XH_PIECES; i++) cs[i] = o->chunk_from + (span ? r[i] % span : 0);
    size_t hi = x->post_max && x->post_max < n ? x->post_max : n;
    size_t m = data_fit(o, h2w_list_size(&q->r, 1), meta_budget(t), hi, cs);
    if (!m) {
        t->why = "xhttp: the server's header limit leaves no room for data";
        return TR_EIO;
    }
    size_t e = b64_len(m), at = 0;
    for (size_t i = 0; at < e; i++) {
        if (i >= XH_PIECES) return H2_ETOOBIG;
        size_t c = cs[i] < e - at ? cs[i] : e - at;
        char name[80];
        snprintf(name, sizeof name, o->data_place == XHP_COOKIE ? "%s_%zu" : "%s-%zu", o->data_key, i);
        for (char *c = name; o->data_place == XHP_HEADER && *c; c++)
            if (*c >= 'A' && *c <= 'Z') *c = (char)(*c + ('a' - 'A'));
        char *b = ar_begin(&q->a);
        if (o->data_place == XHP_COOKIE) {
            ar_cat(&q->a, name, strlen(name));
            ar_cat(&q->a, "=", 1);
        }
        b64_put(&q->a, d, m, at, c);
        const char *v = ar_end(&q->a, b);
        const char *nm = o->data_place == XHP_COOKIE ? "cookie" : ar_str(&q->a, name, strlen(name));
        if (!v || !nm) return H2_ETOOBIG;
        q->dat[q->dat_n++] = (struct h2_field){ nm, v };
        at += c;
    }
    q->r.end_stream = 1;
    rq_join(q, 1);
    *took = m;
    return 0;
}

static int side_init(struct xh_side *s, const struct tr_node *n, const char *mode) {
    /* :authority as Xray's client builds the URL: the node's host setting, else the SNI (the
     * camouflage domain the server hides behind), else the address. A server with host set
     * compares it with :authority and answers 404 to anything else (hub.go). */
    char hostbuf[160];
    const char *authority = n->http_host && n->http_host[0] ? n->http_host
                          : n->sni[0] ? n->sni : tr_host_literal(n->host, hostbuf, sizeof(hostbuf));
    if (xh_opts_parse(n->xhx, mode, n->pad_to, &s->opts, NULL, 0)) return TR_EIO;
    snprintf(s->authority, sizeof(s->authority), "%s", authority);
    const char *p = n->path ? n->path : "";
    const char *qm = strchr(p, '?');
    size_t pn = qm ? (size_t)(qm - p) : strlen(p);
    int slash = s->opts.sess_place == XHP_PATH || s->opts.seq_place == XHP_PATH;
    if (snprintf(s->base_path, sizeof s->base_path, "%s%.*s", pn && p[0] == '/' ? "" : "/", (int)pn, p)
        >= (int)sizeof s->base_path)
        return H2_ETOOBIG;
    size_t bn = strlen(s->base_path);
    if (slash && s->base_path[bn - 1] != '/') {
        if (bn + 1 >= sizeof s->base_path) return H2_ETOOBIG;
        s->base_path[bn] = '/';
        s->base_path[bn + 1] = '\0';
    }
    snprintf(s->query, sizeof s->query, "%s", qm ? qm + 1 : "");
    s->headers = n->headers;
    s->pad_from = n->pad_from;
    s->pad_to = n->pad_to;
    return 0;
}

static int xh_init(struct transport *t, const struct tr_node *n) {
    struct xh_state *x = &t->xh;
    x->mode = xhttp_mode_of(n);
    int rc = side_init(&x->main_side, n, n->mode);
    if (!rc) rc = side_init(&x->down_side, n->dl ? n->dl : n, n->dl ? "packet-up" : n->mode);
    if (rc) return rc;
    x->worst_base = 0;
    /* One value per connection from the node's range, as Xray's client picks it (dialer.go). */
    x->post_max = 0;
    if (n->post_to) {
        uint32_t r = 0;
        if (os_getrandom(&r, sizeof r, 0) != (ssize_t)sizeof r) r = 0;
        x->post_max = n->post_from + r % (n->post_to - n->post_from + 1);
    }
    x->sid[0] = '\0';
    if (x->mode != XH_STREAM_ONE) xh_session_id(&x->main_side.opts, x->sid, sizeof(x->sid));
    return 0;
}

static const struct xs *x_rd(const struct transport *t) {
    return t->xh.mode == XH_STREAM_ONE ? &t->xh.f.one : &t->xh.f.down;
}

static struct xconn *x_rdconn(const struct transport *t) {
    const struct xs *s = x_rd(t);
    return s->x ? s->x : t->xh.priv[0];
}

static int out_wait(struct transport *t, struct xconn *x) {
    if (x->shared) return x->state == XC_DEAD ? x->rc : 0;
    while (xconn_wants_out(x)) {
        struct pollfd p = { .fd = x->link.fd, .events = POLLOUT };
        int pr = poll(&p, 1, t->xh.wait_ms);
        if (pr < 0 && errno == EINTR) continue;
        if (pr <= 0) {
            xconn_fail(x, TR_EIO);
            return TR_EIO;
        }
        int rc = xconn_flush(x);
        if (rc) return rc;
    }
    return x->state == XC_DEAD ? x->rc : 0;
}

static int xh_err(struct transport *t) {
    int e = t->xh.err;
    if (e > 0) {
        h2_note_end(H2_ESTATUS, e);
        return H2_ESTATUS;
    }
    return e;
}

static int sent(int rc, const struct xs *s) {
    if (rc == H2_ESTATUS || rc == H2_ERESET) h2_note_end(rc, xs_status(s));
    return rc;
}

static int post_reap(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    for (unsigned i = 0; i < f->post_n;) {
        struct xh_post *p = &f->post[i];
        if (p->redo) {
            i++;
            continue;
        }
        unsigned char sink[64];
        size_t got = 0;
        int rc = 0;
        while (xs_ready(&p->s) && !(rc = xs_read(&p->s, sink, sizeof sink, &got))) {}
        if (!rc) {
            i++;
            continue;
        }
        int st = xs_status(&p->s);
        if (rc == XC_EREFUSED && !st && f->shared && p->keep.n) {
            xs_close(&p->s);
            p->redo = 1;
            p->tries++;
            f->redo_n++;
            tr_xmux_redo(t);
            i++;
            continue;
        }
        if (!st && f->shared && rc != XC_EREFUSED) tr_xmux_post_failed(p->s.x);
        if (st != 200 && !t->xh.err) t->xh.err = st ? (st > 0 ? st : -1) : rc;
        if (t->xh.err == -1) t->xh.err = H2_ESTATUS;
        xs_close(&p->s);
        xc_q_drop(&p->keep);
        f->post[i] = f->post[--f->post_n];
    }
    return xh_err(t);
}

static uint64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static int post_pull(struct transport *t) {
    struct xconn *m = x_post(t);
    if (m && !m->shared && m->state == XC_LIVE) xconn_io(m, EPOLLIN | EPOLLOUT, mono_ms());
    return post_reap(t);
}

static int up_reap(struct transport *t) {
    struct xs *u = &t->xh.f.up;
    unsigned char sink[64];
    size_t got = 1;
    int rc = 0;
    while (u->x && got && xs_ready(u) && !(rc = xs_read(u, sink, sizeof sink, &got))) {}
    return rc && xs_failed(u) ? rc : 0;
}

static int up_open(struct transport *t, struct xconn *main) {
    int rc = rq_build(t, XK_UP, NULL, 0);
    return rc ? rc : xs_open(main, &rq_held()->q.r, t->xh.f.owner, &t->xh.f.up);
}

int xh_get_open(struct transport *t, struct xconn *x) {
    int rc = rq_build(t, XK_DOWN, NULL, 0);
    return rc ? rc : xs_open(x, &rq_held()->q.r, t->xh.f.owner, &t->xh.f.down);
}

void xh_posts_drop(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    for (unsigned i = 0; i < f->post_n; i++) xc_q_drop(&f->post[i].keep);
    f->redo_n = 0;
    f->feed = 0;
}

int xh_flow_start(struct transport *t, struct xconn *main, struct xconn *down, uint16_t owner) {
    struct xh_state *x = &t->xh;
    memset(&x->f.one, 0, sizeof x->f.one);
    memset(&x->f.down, 0, sizeof x->f.down);
    memset(&x->f.up, 0, sizeof x->f.up);
    x->f.post_n = 0;
    x->f.feed = 0;
    x->f.owner = owner;
    x->seq = 0;
    x->err = 0;
    /* stream-one sends its one POST on main. Otherwise the GET goes on down, and its request is a
     * GET without a body. The method matters: the server tells the upload from the download by it
     * (hub.go: a GET without a chunk number is stream-down). The server would take a bodiless POST
     * for an upload, wait for bytes that never come, and send nothing down.
     *
     * stream-up opens its POST at once and keeps it open for the life of the flow: the body of that
     * request is the upstream channel. packet-up opens NOTHING in advance. A request there lives
     * for exactly one chunk; opening it before the chunk exists would hold an empty upload on the
     * server, with number 0, which would then have to be skipped. The first request opens on the
     * first send. */
    int one = x->mode == XH_STREAM_ONE;
    int rc = rq_build(t, one ? XK_ONE : XK_DOWN, NULL, 0);
    if (!rc) rc = xs_open(one ? main : down, &rq_held()->q.r, owner, one ? &x->f.one : &x->f.down);
    if (!rc && x->mode == XH_STREAM_UP) rc = up_open(t, main);
    if (!rc) rc = out_wait(t, main);
    if (!rc && down != main) rc = out_wait(t, down);
    return rc;
}

static void xhttp_close(struct transport *t);

int xh_attach(struct transport *t, const struct tr_node *n, uint16_t owner) {
    t->fr = &tr_xhttp;
    memset((char *)t + offsetof(struct transport, enc), 0, transport_used(t) - offsetof(struct transport, enc));
    t->xh.wait_ms = 1000;
    t->xh.f.owner = owner;
    t->xh.f.shared = 1;
    return xh_init(t, n);
}

static int xhttp_open(struct transport *t, const struct tr_node *n, int timeout_s) {
    (void)timeout_s;
    int rc = xh_init(t, n);
    struct xconn *m = t->xh.priv[0];
    return rc ? rc : xh_flow_start(t, m, t->xh.priv[1] ? t->xh.priv[1] : m, 0);
}

/* packet-up: how far ahead of the oldest unanswered chunk a new one may go, in chunks. The
 * server takes each POST in a goroutine of its own and puts the chunks back in order by seq,
 * holding at most scMaxBufferedPosts (default 30) past a missing one; beyond that it tears the
 * connection down without an HTTP error ("packet queue is too large", upload_queue.go), and the
 * flow simply ends. Sent back to back, our chunks got there under load: one slow goroutine is a
 * gap, and everything sent meanwhile piles up behind it. Bounding the number of unanswered
 * chunks is not enough: later ones get answered and free their slots while the gap stays. So the
 * bound is a window over seq, as in TCP: no chunk more than PACKET_INFLIGHT past the oldest
 * unanswered one: the node's scMaxBufferedPosts when the link carries it, else PACKET_INFLIGHT,
 * and never more streams than the flow's ring or the server's MAX_CONCURRENT_STREAMS leave. */
#define PACKET_INFLIGHT 24
#define PACKET_SLOT 8192

/* Chunks that may still be sent before the window over seq is full (see PACKET_INFLIGHT). */
static long packet_win(const struct transport *t) {
    const struct xh_state *x = &t->xh;
    long w = x->main_side.opts.posts ? (long)x->main_side.opts.posts : PACKET_INFLIGHT;
    const struct xconn *m = x_post(t);
    long c = m ? (long)h2m_cap(&m->h2) - (m == x->f.down.x ? 1 : 0) : XH_POST_MAX;
    if (c > XH_POST_MAX) c = XH_POST_MAX;
    return w < c ? w : c;
}

static long packet_slots(const struct transport *t) {
    const struct xh_flow *f = &t->xh.f;
    long win = packet_win(t);
    uint64_t oldest = f->post_n ? f->post[0].seq : t->xh.seq;
    for (unsigned i = 1; i < f->post_n; i++)
        if (f->post[i].seq < oldest) oldest = f->post[i].seq;
    long ahead = (long)(t->xh.seq - oldest);
    long s = ahead >= win ? 0 : win - ahead;
    const struct xconn *m = x_post(t);
    if (!f->shared || !m) return s;
    long free_n = (long)h2m_cap(&m->h2) - (long)h2m_open_n(&m->h2);
    return free_n < s ? (free_n > 0 ? free_n : 0) : s;
}

static long packet_room(const struct transport *t) {
    if (packet_slots(t) <= 0) return 0;
    const struct xconn *m = x_post(t);
    if (!m) return 0;
    int32_t r = m->h2.send_win_conn < m->h2.peer_init_win ? m->h2.send_win_conn : m->h2.peer_init_win;
    return r > 0 ? r : 0;
}

/* packet-up: what all chunks of a write may carry together (the connection window). */
static long packet_conn_room(const struct transport *t) {
    const struct xconn *m = x_post(t);
    return m ? h2m_conn_room(&m->h2) : 0;
}

static int post_open(struct transport *t, const unsigned char *d, size_t n, int meta, size_t *took) {
    struct xh_state *x = &t->xh;
    struct xh_flow *f = &x->f;
    if (f->post_n >= XH_POST_MAX) return H2_EWINDOW;
    char seq[24];
    snprintf(seq, sizeof seq, "%llu", (unsigned long long)x->seq);
    int rc = rq_build(t, XK_PACKET, seq, 0);
    if (!rc && meta) rc = rq_data(t, d, n, took);
    struct xh_post *p = &f->post[f->post_n];
    if (!rc && !meta) rq_held()->q.r.body_len = (int64_t)n;
    if (!rc) rc = xs_open(x_post(t), &rq_held()->q.r, f->owner, &p->s);
    if (rc) return rc;
    p->seq = x->seq++;
    f->post_n++;
    if (meta) return 0;
    *took = n;
    return sent(xs_send(&p->s, d, n, 1), &p->s);
}

static int packet_meta_write(struct transport *t, const unsigned char *d, size_t n) {
    struct xh_state *x = &t->xh;
    int rc = post_pull(t);
    if (rc) return rc;
    size_t w = packet_cap(t);
    if (!w) {
        t->why = rq_held() && ts_g[TS_H2WIRE] ? "xhttp: the server's header limit leaves no room for data"
                                              : "xhttp: no memory for the request";
        return TR_EIO;
    }
    long pieces = (long)((n + w - 1) / w);
    if (packet_slots(t) < pieces && !(pieces > packet_win(t) && !x->f.post_n)) return H2_EWINDOW;
    for (size_t off = 0; off < n && !rc;) {
        size_t took = 0;
        rc = post_open(t, d + off, n - off, 1, &took);
        if (!rc) off += took;
    }
    return rc ? rc : out_wait(t, x_post(t));
}

int (*tr_xh_send_q)(const struct xs *s, struct xc_q *q, size_t n, int end) = xs_send_q;

static int post_redo(struct transport *t, struct xconn *m, struct xh_post *p) {
    struct xh_state *x = &t->xh;
    struct xh_flow *f = &x->f;
    size_t n = p->keep.n;
    int meta = data_meta(&x->main_side.opts);
    if (!xc_slot_free(m) || (meta && n > XH_BLOCK)) return 0;
    if (!meta) {
        long w = m->h2.peer_init_win < h2m_conn_room(&m->h2) ? m->h2.peer_init_win : h2m_conn_room(&m->h2);
        size_t now = w > 0 ? ((size_t)w < n ? (size_t)w : n) : 0;
        long iw = m->h2.peer_init_win;
        size_t fresh = iw <= 0 ? 0 : (size_t)iw < n ? (size_t)iw : n;
        if (n - fresh > xc_pend_limit()) return XC_EREFUSED;
        if (n - now > xc_pend_free()) return 0;
    }
    char seq[24];
    snprintf(seq, sizeof seq, "%llu", (unsigned long long)p->seq);
    int rc = rq_build(t, XK_PACKET, seq, 0);
    if (!rc && meta) {
        size_t took = n;
        xc_q_peek(&p->keep, rq_held()->blk, n);
        rc = rq_data(t, rq_held()->blk, n, &took);
        if (!rc && took != n) rc = TR_EIO;
    }
    if (!rc && !meta) rq_held()->q.r.body_len = (int64_t)n;
    if (!rc) rc = xs_open(m, &rq_held()->q.r, f->owner, &p->s);
    if (rc) return rc;
    p->redo = 0;
    f->redo_n--;
    int hold = p->tries < XH_POST_RETRY;
    if (meta) {
        if (!hold) xc_q_drop(&p->keep);
        return 1;
    }
    struct xc_q again = { 0 };
    if (hold && xc_q_copy(&again, &p->keep, n)) xc_q_drop(&again);
    rc = sent(tr_xh_send_q(&p->s, &p->keep, n, 1), &p->s);
    xc_q_drop(&p->keep);
    if (rc) {
        xc_q_drop(&again);
        return rc;
    }
    p->keep = again;
    return 1;
}

int xh_post_flush(struct transport *t) {
    struct xh_state *x = &t->xh;
    struct xh_flow *f = &x->f;
    struct xconn *m = f->post_x;
    int rc = post_reap(t);
    if (rc) return rc;
    for (unsigned i = 0; m && f->redo_n && i < f->post_n; i++)
        if (f->post[i].redo) return post_redo(t, m, &f->post[i]);
    if (!m || f->post_n >= XH_POST_MAX || packet_slots(t) <= 0) return 0;
    int keep = f->shared && !m->h2.peer_settings_seen;
    int meta = data_meta(&x->main_side.opts);
    size_t n = f->bat.n;
    if (meta) {
        size_t w = packet_cap(t);
        if (!w) return TR_EIO;
        if (n > w) n = w;
        if (n > XH_BLOCK) n = XH_BLOCK;
    } else {
        size_t cap = x->post_max ? x->post_max : 1000000;
        if (n > cap) n = cap;
        long r = xc_post_room(m, n);
        if (r <= 0) return 0;
        n = (size_t)r;
    }
    char seq[24];
    snprintf(seq, sizeof seq, "%llu", (unsigned long long)x->seq);
    rc = rq_build(t, XK_PACKET, seq, 0);
    size_t took = n;
    if (!rc && meta) {
        xc_q_peek(&f->bat, rq_held()->blk, n);
        rc = rq_data(t, rq_held()->blk, n, &took);
    }
    struct xh_post *p = &f->post[f->post_n];
    if (!rc && !meta) rq_held()->q.r.body_len = (int64_t)n;
    if (!rc) rc = xs_open(m, &rq_held()->q.r, f->owner, &p->s);
    if (rc) return rc;
    p->seq = x->seq++;
    p->keep = (struct xc_q){ 0 };
    p->tries = p->redo = 0;
    f->post_n++;
    if (meta) {
        xc_q_pop(&f->bat, rq_held()->blk, took);
        if (keep && xc_q_push(&p->keep, rq_held()->blk, took)) xc_q_drop(&p->keep);
        return (int)took;
    }
    if (keep && xc_q_copy(&p->keep, &f->bat, n)) xc_q_drop(&p->keep);
    f->feed = (uint32_t)n;
    f->feed_seq = p->seq;
    rc = xh_post_feed(t);
    return rc < 0 ? rc : (int)n;
}

int xh_post_feed(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    int rc = post_reap(t);
    if (rc) return rc;
    if (!f->feed) return 1;
    struct xh_post *p = NULL;
    for (unsigned i = 0; !p && i < f->post_n; i++)
        if (f->post[i].seq == f->feed_seq && !f->post[i].redo) p = &f->post[i];
    if (!p) {
        xc_q_skip(&f->bat, f->feed);
        f->feed = 0;
        return 1;
    }
    if (xc_queued() >= XC_OUT_HIGH) return 0;
    long r = xs_room(&p->s);
    size_t k = r > 0 ? ((size_t)r < f->feed ? (size_t)r : f->feed) : 0;
    if (!k) return 0;
    rc = sent(tr_xh_send_q(&p->s, &f->bat, k, k == f->feed), &p->s);
    if (rc) return rc;
    f->feed -= (uint32_t)k;
    return !f->feed;
}

static int xhttp_write_mode(struct transport *t, const unsigned char *d, size_t n);

static int xhttp_write(struct transport *t, const unsigned char *d, size_t n) {
    struct xh_flow *f = &t->xh.f;
    if (f->shared && f->again && f->wait) return H2_EWINDOW;
    int rc = xhttp_write_mode(t, d, n);
    if (!rc && n && f->shared) f->wrote = 1;
    return rc;
}

static int xhttp_write_mode(struct transport *t, const unsigned char *d, size_t n) {
    struct xh_state *x = &t->xh;
    switch (x->mode) {
        case XH_STREAM_ONE: {
            int rc = sent(xs_send(&x->f.one, d, n, 0), &x->f.one);
            return rc ? rc : out_wait(t, x->f.one.x);
        }
        case XH_STREAM_UP: {
            /* One long POST for the whole flow. What the server answered meanwhile is read first:
             * a refusal ends the stream, and a closed window may only mean an unread
             * WINDOW_UPDATE. */
            post_pull(t);
            int rc = sent(xs_send(&x->f.up, d, n, 0), &x->f.up);
            return rc ? rc : out_wait(t, x->f.up.x);
        }
        case XH_PACKET_UP: {
            if (x->f.shared) {
                int rc = post_reap(t);
                return rc ? rc : tr_xmux_batch(t, d, n);
            }
            if (data_meta(&x->main_side.opts)) return packet_meta_write(t, d, n);
            /* A chunk is a request of its own: open, write with END_STREAM. */
            int rc = post_pull(t);
            if (rc) return rc;
            /* A chunk the windows cannot take now must not open its request: the send would be
             * refused after the HEADERS went out, leaving a stream without END_STREAM, and the
             * retry would open another one for the same seq.
             *
             * The server refuses a POST body over its scMaxEachPostBytes with 413: a write larger
             * than post_max goes as several chunks. All or nothing still holds (Vision above relies
             * on it): the windows are checked for the whole write before the first chunk opens,
             * the connection window for the sum and each new stream's window for one chunk. The
             * chunks this write needs must fit among the unanswered ones; a write that needs more
             * than the whole window alone (a tiny scMaxEachPostBytes) waits for none. */
            size_t piece = x->post_max && x->post_max < n ? x->post_max : n;
            long pieces = (long)((n + piece - 1) / piece);
            if ((long)piece > packet_room(t) || (long)n > packet_conn_room(t) ||
                (packet_slots(t) < pieces && !(pieces > packet_win(t) && !x->f.post_n)))
                return H2_EWINDOW;
            for (size_t off = 0; off < n && !rc; off += piece) {
                size_t took = 0;
                rc = post_open(t, d + off, n - off < piece ? n - off : piece, 0, &took);
            }
            return rc ? rc : out_wait(t, x_post(t));
        }
    }
    return H2_EPROTO;
}

static int refused(struct transport *t, int rc, int up) {
    struct xh_flow *f = &t->xh.f;
    if (rc != XC_EREFUSED || !f->shared) return rc;
    if (!up && t->xh.mode != XH_STREAM_ONE) return tr_xmux_reget(t);
    return f->wrote ? rc : tr_xmux_refused(t);
}

static int xhttp_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    struct xh_flow *f = &t->xh.f;
    *got = 0;
    if (f->shared && f->again && t->xh.err) return f->end = t->xh.err;
    if (f->shared && f->again && f->wait) return 0;
    if (f->shared && t->xh.mode != XH_STREAM_ONE) {
        int rc = t->xh.mode == XH_STREAM_UP ? refused(t, up_reap(t), 1) : post_reap(t);
        if (rc) f->end = rc;
        if (rc || f->again) return rc;
    }
    const struct xs *s = x_rd(t);
    int rc = sent(xs_read(s, d, cap, got), s);
    if (rc) rc = refused(t, rc, 0);
    if (rc) f->end = rc;
    return rc;
}

/* The download's end is known but only the next read can report it, and a record read with an
 * earlier one sits in the TLS buffer where epoll does not see it. */
static int xhttp_pending(const struct transport *t) {
    if (t->xh.f.shared) return x_rd(t)->x && xs_ready(x_rd(t));
    const struct xconn *x = x_rdconn(t);
    if (!x) return 0;
    if (x_rd(t)->x && xs_ready(x_rd(t))) return 1;
    return (!x->link.plain && tls13_has_record(&x->link.tls)) || fm_pending(x->link.tls.fm);
}

static int xhttp_due(struct transport *t, uint64_t now_ms, int *next_ms) {
    *next_ms = -1;
    for (int i = 0; i < 2; i++) {
        struct xconn *x = t->xh.priv[i];
        if (!x) continue;
        int d = xconn_due(x, now_ms);
        if (!d) {
            int rc = xconn_flush(x);
            if (!rc) rc = out_wait(t, x);
            if (rc) return rc;
            d = xconn_due(x, mono_ms());
            if (!d) d = 1;
        }
        if (d > 0 && (*next_ms < 0 || d < *next_ms)) *next_ms = d;
    }
    return 0;
}

static int xhttp_fd(const struct transport *t) {
    if (t->xh.f.shared) return -1;
    const struct xconn *x = x_rdconn(t);
    return x ? x->link.fd : -1;
}

static int xhttp_dial(struct transport *t, const struct tr_node *n, int timeout_s) {
    struct xh_state *x = &t->xh;
    x->wait_ms = timeout_s > 0 ? timeout_s * 1000 : 1000;
    x->priv[0] = xconn_new(n, 0, 0);
    if (n->dl) x->priv[1] = xconn_new(n->dl, 0, 0);
    int rc = !x->priv[0] || (n->dl && !x->priv[1]) ? TR_EIO
           : n->dl ? xconn_dial2(x->priv[1], x->priv[0], timeout_s)
           : xconn_dial(x->priv[0], timeout_s);
    if (rc) xhttp_close(t);
    return rc;
}

static void xhttp_close(struct transport *t) {
    if (t->xh.f.shared) tr_xmux_detach(t);
    for (int i = 0; i < 2; i++) {
        xconn_free(t->xh.priv[i]);
        t->xh.priv[i] = NULL;
    }
    memset(&t->xh.f, 0, sizeof t->xh.f);
}

/* What xhttp_write takes now, mode by mode: the same windows it checks. The connection of the
 * POSTs is read when the room runs low: the tunnel waits only on the download's descriptor, and
 * once the client's window was sized by the room (dialer_ops.room), a closed room meant no writes,
 * so no reads, so the room stayed closed: stream-up uploads fell from 3 GB to 14 MB in 20 s. */
#define UP_ROOM_LOW 65536
static long xhttp_room(struct transport *t) {
    struct xh_state *x = &t->xh;
    switch (x->mode) {
        case XH_STREAM_ONE: return xs_room(&x->f.one);
        case XH_STREAM_UP:
            if (xs_room(&x->f.up) < UP_ROOM_LOW / (long)fm_grow(x->f.up.x ? x->f.up.x->link.tls.fm : NULL))
                post_pull(t);
            return xs_room(&x->f.up);
        case XH_PACKET_UP: {
            if (x->f.shared) {
                post_reap(t);
                return tr_xmux_bat_room(t);
            }
            if (data_meta(&x->main_side.opts)) {
                if (packet_slots(t) <= packet_win(t) / 2) post_pull(t);
                long slots = packet_slots(t);
                long per = (long)packet_cap(t);
                if (slots < 0) slots = 0;
                if (!per) per = 1;
                if (per > PACKET_SLOT) per = PACKET_SLOT;
                return slots * per;
            }
            /* In BYTES the free chunk slots can carry, not the HTTP/2 window: the window may
             * be megabytes while only packet_win chunks can be unanswered. Granted the
             * window, the client sent far more than the slots took, and every refused segment
             * waited out a retransmission timeout (11 MB uploaded in 20 s). A slot is counted
             * at PACKET_SLOT bytes, below what the stack gathers into one send (up to 16 KB),
             * so a granted window fills whole chunks; post_max when the node's is smaller. */
            if (packet_room(t) < UP_ROOM_LOW || packet_slots(t) <= packet_win(t) / 2) post_pull(t);
            long r = packet_room(t);
            long slots = packet_slots(t);
            long per = x->post_max && x->post_max < PACKET_SLOT ? (long)x->post_max : PACKET_SLOT;
            if (slots < 0) slots = 0;
            if (r > slots * per) r = slots * per;
            if (r > packet_conn_room(t)) r = packet_conn_room(t);
            return r;
        }
    }
    return 0;
}

const struct transport_ops tr_xhttp = {
    .name = "xhttp", .alpn = "h2", .zc = 0,
    .open = xhttp_open, .write = xhttp_write, .read = xhttp_read, .dial = xhttp_dial, .fd = xhttp_fd,
    .close = xhttp_close, .pending = xhttp_pending, .room = xhttp_room, .due = xhttp_due,
};

int transport_whole_writes(const struct transport *t) {
    return t->fr == &tr_xhttp && t->xh.mode == XH_PACKET_UP && !t->xh.f.shared;
}
