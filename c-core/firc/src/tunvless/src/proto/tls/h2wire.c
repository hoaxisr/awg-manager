#include <stdio.h>
#include <string.h>
#include <strings.h>

#include "h2wire.h"
#include "tscratch.h"

/* ---- HPACK on send for gRPC ---------------------------------------------------------
 *
 * :method and :scheme go as indexed fields; everything else as "literal without indexing":
 * the name by its static table index (or as a string when the table lacks it), the value as a
 * plain string. There is no dynamic table at all, which is legal: HPACK allows indexing
 * nothing. No Huffman either. xhttp encodes its requests with hpack.c instead.
 *
 * Static table indices (RFC 7541, appendix A) are plain numbers: the table is part of the
 * protocol and never changes. */
#define HP_AUTHORITY   1
#define HP_METHOD_GET  2
#define HP_METHOD_POST 3
#define HP_PATH        4
#define HP_SCHEME_HTTPS 7
#define HP_CONTENT_TYPE 31
#define HP_REFERER     51
#define HP_USER_AGENT  58

/* ---- browser look for xhttp ---------------------------------------------------------
 *
 * xhttp is a plain HTTP request to an ordinary-looking path, and it should come from what the
 * ClientHello already claims to be: a browser. gRPC headers (`te: trailers`,
 * `user-agent: grpc-go/1.60.0`) give xhttp away: nodes with CDN-cache-like paths
 * (`/static/v1/cache/<hash>`) answered them with RST/GOAWAY even though Reality accepted us;
 * something in front of the server cut them.
 *
 * The sets are Xray's (transport/internet/splithttp/config.go: GetRequestHeader →
 * TryDefaultHeadersWith(header, "fetch") → applyMasqueradedHeaders(browser, fetch)), one table
 * per look, repeated name by name. The strings are in h2.h, shared with ws and httpupgrade. */

struct wbuf { unsigned char *p; size_t n, cap; };

static void wb(struct wbuf *b, const void *d, size_t n) {
    if (b->n + n <= b->cap) memcpy(b->p + b->n, d, n);
    b->n += n;
}
static void wb8(struct wbuf *b, unsigned v) {
    unsigned char c = (unsigned char)v;
    wb(b, &c, 1);
}

/* An HPACK integer with an N-bit prefix, for indices and string lengths. */
static void hp_int(struct wbuf *b, unsigned prefix, unsigned bits, uint32_t v) {
    uint32_t max = (1u << bits) - 1;
    if (v < max) { wb8(b, prefix | v); return; }
    wb8(b, prefix | max);
    v -= max;
    while (v >= 128) { wb8(b, (v & 0x7F) | 0x80); v >>= 7; }
    wb8(b, v);
}

static void hp_str(struct wbuf *b, const char *s, size_t n) {
    hp_int(b, 0x00, 7, (uint32_t)n);     /* no Huffman: high bit clear */
    wb(b, s, n);
}

/* A field with a name from the static table and our own value. */
static void hp_field(struct wbuf *b, unsigned name_index, const char *value) {
    hp_int(b, 0x00, 4, name_index);      /* 0000 — literal without indexing */
    hp_str(b, value, strlen(value));
}

/* A field whose name is not in the table. */
static void hp_new(struct wbuf *b, const char *name, const char *value) {
    wb8(b, 0x00);
    hp_str(b, name, strlen(name));
    hp_str(b, value, strlen(value));
}

/* An indexed field: name AND value from the static table. */
static void hp_indexed(struct wbuf *b, unsigned index) {
    hp_int(b, 0x80, 7, index);
}

struct hf { unsigned idx; const char *name, *value; };
enum lk { LK_F, LK_X, LK_S };
struct lf { const char *name, *value; enum lk kind; };

#define LF_FETCH(lang, prio) \
    { "accept", "*/*", LK_S }, \
    { "sec-fetch-site", "same-origin", LK_F }, \
    { "sec-fetch-mode", "cors", LK_F }, \
    { "sec-fetch-dest", "empty", LK_F }, \
    { "accept-language", lang, LK_F }, \
    { "priority", prio, LK_S }, \
    { "cache-control", "no-cache", LK_S }, \
    { "pragma", "no-cache", LK_S }

static const struct lf lk_chrome[] = {
    { "sec-ch-ua", UA_CH_CHROME, LK_X },
    { "sec-ch-ua-mobile", "?0", LK_X },
    { "sec-ch-ua-platform", "\"Windows\"", LK_X },
    { "dnt", "1", LK_X },
    { "user-agent", UA_CHROME, LK_F },
    LF_FETCH("en-US,en;q=0.9", "u=1, i"),
};
static const struct lf lk_edge[] = {
    { "sec-ch-ua", UA_CH_EDGE, LK_X },
    { "sec-ch-ua-mobile", "?0", LK_X },
    { "sec-ch-ua-platform", "\"Windows\"", LK_X },
    { "dnt", "1", LK_X },
    { "user-agent", UA_EDGE, LK_F },
    LF_FETCH("en-US,en;q=0.9", "u=1, i"),
};
static const struct lf lk_firefox[] = {
    { "dnt", "1", LK_X },
    { "user-agent", UA_FIREFOX, LK_F },
    LF_FETCH("en-US,en;q=0.5", "u=4"),
};
static const struct lf lk_safari[] = {
    { "user-agent", UA_SAFARI, LK_F },
    LF_FETCH("en-US,en;q=0.9", "u=3, i"),
};
static const struct lf lk_curl[] = {
    { "user-agent", UA_CURL, LK_F },
};

struct look { const struct lf *l; size_t n, head; };

#define NELEM(a) (sizeof(a) / sizeof((a)[0]))
#define LOOK(t) { t, NELEM(t), NELEM(t) - 4 }

static const struct look looks[H2L_NONE + 1] = {
    [H2L_CHROME] = LOOK(lk_chrome),
    [H2L_EDGE] = LOOK(lk_edge),
    [H2L_FIREFOX] = LOOK(lk_firefox),
    [H2L_SAFARI] = LOOK(lk_safari),
    [H2L_CURL] = { lk_curl, NELEM(lk_curl), NELEM(lk_curl) },
};

static const struct hf grpc_tail[] = {
    { 0, "te", "trailers" },
    { HP_USER_AGENT, "user-agent", "grpc-go/1.60.0" },
};

static int hf_set(const struct h2_req *r, const char *name) {
    for (size_t i = 0; i < r->fields_n; i++)
        if (strcasecmp(r->fields[i].name, name) == 0) return r->fields[i].value[0] != '\0';
    return 0;
}

static const struct look *look_of(const struct h2_req *r) {
    return &looks[r->look];
}

static int look_takes(const struct look *k, const struct h2_req *r, const char *name) {
    for (size_t i = 0; i < k->n; i++)
        if (strcasecmp(k->l[i].name, name) == 0)
            return k->l[i].kind == LK_F || (k->l[i].kind == LK_S && !hf_set(r, name));
    return 0;
}

static void hp_list(struct wbuf *b, const struct hf *l, size_t n) {
    for (size_t i = 0; i < n; i++) {
        if (l[i].idx) hp_field(b, l[i].idx, l[i].value);
        else hp_new(b, l[i].name, l[i].value);
    }
}

static size_t hf_size(const struct hf *l, size_t n) {
    size_t s = 0;
    for (size_t i = 0; i < n; i++) s += strlen(l[i].name) + strlen(l[i].value) + 32;
    return s;
}

static void hp_user(struct wbuf *b, const struct h2_field *f) {
    size_t n = strlen(f->name);
    if (n == 7 && strncasecmp(f->name, "referer", 7) == 0) {
        hp_field(b, HP_REFERER, f->value);
        return;
    }
    wb8(b, 0x00);
    hp_int(b, 0x00, 7, (uint32_t)n);
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)f->name[i];
        wb8(b, c >= 'A' && c <= 'Z' ? c + ('a' - 'A') : c);
    }
    hp_str(b, f->value, strlen(f->value));
}

static const char *const go_drop[] = {
    "host", "content-length", "connection", "proxy-connection", "transfer-encoding", "upgrade", "keep-alive",
};

struct gl { struct h2w_list *o; struct h2wire_scratch *s; size_t rn, bn; int over; };

static struct h2wire_scratch *scratch(void) {
    return ts_get(TS_H2WIRE, sizeof(struct h2wire_scratch));
}

uint64_t h2w_rng(uint64_t *state) {
    uint64_t z = (*state += 0x9E3779B97F4A7C15ull);
    z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ull;
    z = (z ^ (z >> 27)) * 0x94D049BB133111EBull;
    return z ^ (z >> 31);
}

static void gl_rest(struct gl *g, const char *name, const char *value) {
    if (g->rn >= H2W_FIELDS_MAX) { g->over = 1; return; }
    g->s->rest[g->rn++] = (struct h2_field){ name, value };
}

static const char *gl_copy(struct gl *g, const char *s, size_t n, int lower) {
    if (g->bn + n + 1 > sizeof g->o->buf) { g->over = 1; return s; }
    char *c = g->o->buf + g->bn;
    for (size_t i = 0; i < n; i++) c[i] = (char)(lower && s[i] >= 'A' && s[i] <= 'Z' ? s[i] + ('a' - 'A') : s[i]);
    c[n] = '\0';
    g->bn += n + 1;
    return c;
}

static const char *gl_lower(struct gl *g, const char *name) {
    for (const char *c = name; *c; c++)
        if (*c >= 'A' && *c <= 'Z') return gl_copy(g, name, strlen(name), 1);
    return name;
}

static void gl_look(struct gl *g, const struct look *k, size_t from, size_t to, const struct h2_req *r) {
    for (size_t i = from; i < to; i++) {
        const struct lf *l = &k->l[i];
        if (l->kind != LK_S || !hf_set(r, l->name)) gl_rest(g, l->name, l->value);
    }
}

static void gl_cookie(struct gl *g, const char *v) {
    for (const char *p; (p = strchr(v, ';')); ) {
        gl_rest(g, "cookie", gl_copy(g, v, (size_t)(p - v), 0));
        for (v = p + 1; *v == ' '; v++) {}
    }
    if (*v) gl_rest(g, "cookie", v);
}

static int gl_user(struct gl *g, const struct h2_req *r) {
    int ua = 0;
    for (size_t i = 0; i < r->fields_n; i++) {
        const char *nm = r->fields[i].name, *v = r->fields[i].value;
        int drop = look_takes(look_of(r), r, nm);
        for (size_t k = 0; k < NELEM(go_drop); k++) drop |= strcasecmp(nm, go_drop[k]) == 0;
        if (strcasecmp(nm, "user-agent") == 0) {
            if (r->look == H2L_GOLANG) continue;
            drop |= ua || !v[0];
            ua = 1;
        }
        if (drop) continue;
        if (strcasecmp(nm, "cookie") == 0) gl_cookie(g, v);
        else gl_rest(g, gl_lower(g, nm), v);
    }
    return ua;
}

static void gl_out(struct gl *g, const char *name, const char *value) {
    if (g->o->n >= H2W_FIELDS_MAX) { g->over = 1; return; }
    g->o->f[g->o->n++] = (struct h2_field){ name, value };
}

static int gl_has(const struct gl *g, const char *name) {
    for (size_t i = 0; i < g->rn; i++)
        if (strcmp(g->s->rest[i].name, name) == 0) return 1;
    return 0;
}

static int go_cl(const struct h2_req *r) {
    if (r->body_len > 0) return 1;
    if (r->body_len < 0) return 0;
    return !strcmp(r->method, "POST") || !strcmp(r->method, "PUT") || !strcmp(r->method, "PATCH");
}

int h2w_go_fields(const struct h2_req *r, uint64_t *rng, struct h2w_list *out) {
    struct h2wire_scratch *s = scratch();
    if (!s) return H2_EIO;
    struct gl g = { out, s, 0, 0, 0 };
    out->n = 0;
    gl_out(&g, ":authority", r->authority);
    gl_out(&g, ":method", r->method);
    gl_out(&g, ":path", r->path);
    gl_out(&g, ":scheme", "https");
    const struct look *k = look_of(r);
    gl_look(&g, k, 0, k->head, r);
    int ua = gl_user(&g, r) || look_takes(k, r, "user-agent");
    gl_look(&g, k, k->head, k->n, r);
    if (r->content_type) gl_rest(&g, "content-type", r->content_type);
    if (g.over) return H2_ETOOBIG;
    size_t units = 0;
    for (size_t i = 0; i < g.rn; i++) {
        size_t u = 0;
        while (u < units && strcmp(s->rest[s->first[u]].name, s->rest[i].name)) u++;
        if (u == units) {
            s->first[units] = (uint16_t)i;
            s->perm[units] = (uint16_t)units;
            units++;
        }
        s->unit[i] = (uint16_t)u;
    }
    for (size_t i = units; i > 1; i--) {
        size_t j = (size_t)(h2w_rng(rng) % i);
        uint16_t t = s->perm[i - 1];
        s->perm[i - 1] = s->perm[j];
        s->perm[j] = t;
    }
    for (size_t j = 0; j < units; j++)
        for (size_t i = 0; i < g.rn; i++)
            if (s->unit[i] == s->perm[j]) gl_out(&g, s->rest[i].name, s->rest[i].value);
    if (go_cl(r)) {
        char n[24];
        int k = snprintf(n, sizeof n, "%lld", (long long)r->body_len);
        gl_out(&g, "content-length", gl_copy(&g, n, (size_t)k, 0));
    }
    if (!gl_has(&g, "accept-encoding") && !gl_has(&g, "range") && strcmp(r->method, "HEAD"))
        gl_out(&g, "accept-encoding", "gzip");
    if (!ua) gl_out(&g, "user-agent", "Go-http-client/2.0");
    return g.over ? H2_ETOOBIG : 0;
}

size_t h2w_list_size(const struct h2_req *r, int browser) {
    size_t s = 0;
    if (browser) {
        struct h2wire_scratch *x = scratch();
        uint64_t rng = 0;
        if (!x || h2w_go_fields(r, &rng, &x->l)) return SIZE_MAX;
        for (size_t i = 0; i < x->l.n; i++) s += strlen(x->l.f[i].name) + strlen(x->l.f[i].value) + 32;
        return s;
    }
    s = strlen(":method") + strlen(r->method) + 32;
    s += strlen(":scheme") + strlen("https") + 32;
    s += strlen(":path") + strlen(r->path) + 32;
    s += strlen(":authority") + strlen(r->authority) + 32;
    if (r->content_type) s += strlen("content-type") + strlen(r->content_type) + 32;
    for (size_t i = 0; i < r->fields_n; i++) s += strlen(r->fields[i].name) + strlen(r->fields[i].value) + 32;
    return s + hf_size(grpc_tail, NELEM(grpc_tail));
}

size_t h2w_headers(unsigned char *out, size_t cap, const struct h2_req *r) {
    struct wbuf hp = { out, 0, cap };
    if (strcmp(r->method, "GET") == 0) hp_indexed(&hp, HP_METHOD_GET);
    else if (strcmp(r->method, "POST") == 0) hp_indexed(&hp, HP_METHOD_POST);
    else hp_field(&hp, HP_METHOD_GET, r->method);
    hp_indexed(&hp, HP_SCHEME_HTTPS);
    hp_field(&hp, HP_PATH, r->path);
    hp_field(&hp, HP_AUTHORITY, r->authority);
    if (r->content_type) hp_field(&hp, HP_CONTENT_TYPE, r->content_type);
    for (size_t i = 0; i < r->fields_n; i++) hp_user(&hp, &r->fields[i]);
    /* te: trailers is the one connection-specific header HTTP/2 explicitly allows, and
     * gRPC requires it. */
    hp_list(&hp, grpc_tail, NELEM(grpc_tail));
    return hp.n > hp.cap ? 0 : hp.n;
}

/* The response status from the first bytes of HEADERS, without the dynamic table. In practice
 * the server sends ":status 200" as index 8, the single byte 0x88. Anything unknown gives -1,
 * NOT an error: a guessed status is worse than none, and the read shows whether data came. */
int h2w_status(const unsigned char *p, size_t n) {
    size_t i = 0;
    /* A dynamic table size update (001xxxxx) comes first, if at all. Its value is an integer
     * with a 5-bit prefix: with 0x1F in the low bits it continues in the next bytes, high bit
     * set. */
    while (i < n && (p[i] & 0xE0) == 0x20) {
        int cont = (p[i] & 0x1F) == 0x1F;
        i++;
        if (!cont) continue;
        while (i < n && (p[i] & 0x80)) i++;
        if (i < n) i++;
    }
    if (i >= n) return 0;
    switch (p[i]) {
        case 0x88: return 200;
        case 0x89: return 204;
        case 0x8A: return 206;
        case 0x8B: return 304;
        case 0x8C: return 400;
        case 0x8D: return 404;
        case 0x8E: return 500;
        default: {
            /* A literal with a :status name (indices 8 to 14): the value as three digits. */
            unsigned idx = (p[i] & 0xC0) == 0x40 ? p[i] & 0x3Fu : (p[i] & 0xE0) == 0 ? p[i] & 0x0Fu : 0;
            if (idx >= 8 && idx <= 14 && i + 2 < n) {
                size_t len = p[i + 1] & 0x7F;
                if (!(p[i + 1] & 0x80) && len == 3 && i + 4 < n) {
                    const unsigned char *v = p + i + 2;
                    if (v[0] < '0' || v[0] > '9' || v[1] < '0' || v[1] > '9' || v[2] < '0' || v[2] > '9')
                        return -1;
                    return (v[0] - '0') * 100 + (v[1] - '0') * 10 + (v[2] - '0');
                }
                /* The same value in Huffman code: three digits take 15 to 18 bits. */
                if ((p[i + 1] & 0x80) && len >= 2 && len <= 3 && i + 1 + len < n) {
                    int v = status_huff(p + i + 2, len);
                    if (v >= 100) return v;
                }
            }
            return -1;
        }
    }
}

size_t h2w_preface(unsigned char *out, size_t cap, int conn_update) {
    struct wbuf b = { out, 0, cap };

    /* The preface, byte for byte from RFC 7540 §3.5: the server compares it literally. */
    wb(&b, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", 24);

    /* SETTINGS: disable push (not needed, it would only complicate parsing) and announce our
     * stream window. */
    {
        unsigned char s[12];
        s[0] = 0; s[1] = 0x02; put32(s + 2, 0);              /* ENABLE_PUSH = 0 */
        s[6] = 0; s[7] = 0x04; put32(s + 8, OUR_WINDOW);     /* INITIAL_WINDOW_SIZE */
        unsigned char hdr[9] = { 0, 0, 12, FR_SETTINGS, 0, 0, 0, 0, 0 };
        wb(&b, hdr, 9);
        wb(&b, s, 12);
    }

    /* The CONNECTION window is not set by SETTINGS, only by WINDOW_UPDATE on stream 0. Without
     * this frame it stays at 65535 and caps the 1 MB stream window, the very limit OUR_WINDOW
     * is there to lift. */
    if (conn_update) {
        unsigned char wu[4];
        put32(wu, OUR_WINDOW - 65535);
        unsigned char hdr[9] = { 0, 0, 4, FR_WINDOW_UPDATE, 0, 0, 0, 0, 0 };
        wb(&b, hdr, 9);
        wb(&b, wu, 4);
    }
    return b.n > b.cap ? 0 : b.n;
}

size_t h2w_go_preface(unsigned char *out, size_t cap) {
    static const uint16_t id[4] = { 0x02, 0x04, 0x05, 0x06 };
    static const uint32_t val[4] = { 0, H2M_STREAM_WIN, H2M_FRAME_IN, H2M_HDR_LIST };
    if (cap < H2W_GO_PREFACE_LEN) return 0;
    memcpy(out, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", 24);
    h2w_frame_hdr(out + 24, 24, FR_SETTINGS, 0, 0);
    for (int i = 0; i < 4; i++) {
        unsigned char *e = out + 33 + 6 * i;
        e[0] = (unsigned char)(id[i] >> 8);
        e[1] = (unsigned char)id[i];
        put32(e + 2, val[i]);
    }
    h2w_frame_hdr(out + 57, 4, FR_WINDOW_UPDATE, 0, 0);
    put32(out + 66, H2M_CONN_INC);
    return H2W_GO_PREFACE_LEN;
}
