#include "fmlink.h"
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define FJ_DEPTH 32

enum { FJ_OBJ = 1, FJ_ARR, FJ_STR, FJ_NUM, FJ_TRUE, FJ_FALSE, FJ_NULL };
enum { FJE_SYNTAX = 1, FJE_DEEP, FJE_MEM, FJE_BIG };

static const struct { const char *name; enum fm_op op; } fm_ops[] = {
    {"concat", FMO_CONCAT}, {"slice", FMO_SLICE}, {"xor16", FMO_XOR16}, {"xor32", FMO_XOR32},
    {"be16", FMO_BE16}, {"be32", FMO_BE32}, {"le16", FMO_LE16}, {"le32", FMO_LE32}, {"le64", FMO_LE64},
    {"pad", FMO_PAD}, {"truncate", FMO_TRUNCATE}, {"add", FMO_ADD}, {"sub", FMO_SUB}, {"and", FMO_AND},
    {"or", FMO_OR}, {"shl", FMO_SHL}, {"shr", FMO_SHR},
};

const char *fm_hc_op_name(enum fm_op op) {
    for (size_t i = 0; i < sizeof fm_ops / sizeof fm_ops[0]; i++)
        if (fm_ops[i].op == op) return fm_ops[i].name;
    return "";
}

struct fj_node { unsigned char t; size_t kid, next, key; const char *p; size_t n; };

struct fj { const char *s, *e; struct fj_node *v; size_t n, cap, ws; int err; };

struct ctx { struct fj j; char *why; size_t why_n; int failed; const char *mask; };

static int fj_big(struct fj *j, const char *p) {
    if ((size_t)(p - j->s) - j->ws <= FM_JSON_MAX) return 0;
    j->err = FJE_BIG;
    return 1;
}

static size_t fj_new(struct fj *j, unsigned char t, const char *p) {
    if (fj_big(j, p)) return 0;
    if (j->n == j->cap) {
        size_t cap = j->cap ? j->cap * 2 : 64;
        struct fj_node *v = realloc(j->v, cap * sizeof *v);
        if (!v) { j->err = FJE_MEM; return 0; }
        j->v = v;
        j->cap = cap;
    }
    j->v[j->n] = (struct fj_node){t, 0, 0, 0, p, 0};
    return j->n++;
}

static void fj_ws(struct fj *j, const char **p) {
    while (*p < j->e && (**p == ' ' || **p == '\t' || **p == '\n' || **p == '\r')) { (*p)++; j->ws++; }
}

static int hexv(unsigned char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static const char *fj_str_end(const char *p, const char *e) {
    for (p++; p < e; p++) {
        unsigned char c = (unsigned char)*p;
        if (c == '"') return p + 1;
        if (c < 0x20) return NULL;
        if (c != '\\') continue;
        if (++p >= e) return NULL;
        if (*p == 'u') {
            if (e - p < 5) return NULL;
            for (int i = 1; i <= 4; i++)
                if (hexv((unsigned char)p[i]) < 0) return NULL;
            p += 4;
        } else if (!strchr("\"\\/bfnrt", *p) || !*p) {
            return NULL;
        }
    }
    return NULL;
}

static int isdig(char c) { return c >= '0' && c <= '9'; }

static const char *fj_num_end(const char *p, const char *e) {
    if (p < e && *p == '-') p++;
    if (p >= e || !isdig(*p)) return NULL;
    if (*p == '0') p++;
    else while (p < e && isdig(*p)) p++;
    if (p < e && *p == '.') {
        p++;
        if (p >= e || !isdig(*p)) return NULL;
        while (p < e && isdig(*p)) p++;
    }
    if (p < e && (*p == 'e' || *p == 'E')) {
        p++;
        if (p < e && (*p == '+' || *p == '-')) p++;
        if (p >= e || !isdig(*p)) return NULL;
        while (p < e && isdig(*p)) p++;
    }
    return p;
}

static size_t fj_value(struct fj *j, const char **pp, int depth);

static size_t fj_container(struct fj *j, const char **pp, int depth, int obj) {
    const char *p = *pp + 1;
    size_t me, last = 0;
    if (depth > FJ_DEPTH) { j->err = FJE_DEEP; return 0; }
    me = fj_new(j, obj ? FJ_OBJ : FJ_ARR, *pp);
    if (!me) return 0;
    fj_ws(j, &p);
    if (p < j->e && *p == (obj ? '}' : ']')) { *pp = p + 1; return me; }
    for (;;) {
        size_t key = 0, val;
        fj_ws(j, &p);
        if (obj) {
            const char *ke;
            if (p >= j->e || *p != '"' || !(ke = fj_str_end(p, j->e))) { j->err = FJE_SYNTAX; return 0; }
            if (fj_big(j, ke) || !(key = fj_new(j, FJ_STR, p))) return 0;
            j->v[key].n = (size_t)(ke - p);
            p = ke;
            fj_ws(j, &p);
            if (p >= j->e || *p != ':') { j->err = FJE_SYNTAX; return 0; }
            p++;
            fj_ws(j, &p);
        }
        if (!(val = fj_value(j, &p, depth + 1))) return 0;
        j->v[val].key = key;
        if (last) j->v[last].next = val;
        else j->v[me].kid = val;
        last = val;
        fj_ws(j, &p);
        if (p < j->e && *p == ',') { p++; continue; }
        if (p < j->e && *p == (obj ? '}' : ']')) { *pp = p + 1; return me; }
        j->err = FJE_SYNTAX;
        return 0;
    }
}

static size_t fj_value(struct fj *j, const char **pp, int depth) {
    const char *p = *pp, *e = NULL;
    unsigned char t;
    size_t me;
    if (p >= j->e) { j->err = FJE_SYNTAX; return 0; }
    if (*p == '{' || *p == '[') return fj_container(j, pp, depth, *p == '{');
    if (*p == '"') { e = fj_str_end(p, j->e); t = FJ_STR; }
    else if (*p == '-' || isdig(*p)) { e = fj_num_end(p, j->e); t = FJ_NUM; }
    else if (j->e - p >= 4 && !memcmp(p, "true", 4)) { e = p + 4; t = FJ_TRUE; }
    else if (j->e - p >= 5 && !memcmp(p, "false", 5)) { e = p + 5; t = FJ_FALSE; }
    else if (j->e - p >= 4 && !memcmp(p, "null", 4)) { e = p + 4; t = FJ_NULL; }
    else t = 0;
    if (!e) { j->err = FJE_SYNTAX; return 0; }
    if (fj_big(j, e) || !(me = fj_new(j, t, p))) return 0;
    j->v[me].n = (size_t)(e - p);
    *pp = e;
    return me;
}

static size_t fj_parse(struct fj *j, const char *s, size_t n) {
    const char *p = s;
    size_t root;
    memset(j, 0, sizeof *j);
    j->s = s;
    j->e = s + n;
    fj_new(j, 0, s);
    if (j->err) return 0;
    fj_ws(j, &p);
    root = fj_value(j, &p, 1);
    if (!root) return 0;
    fj_ws(j, &p);
    if (p != j->e) { j->err = FJE_SYNTAX; return 0; }
    return root;
}

static size_t utf8_put(unsigned char *o, uint32_t r) {
    if (r < 0x80) { o[0] = (unsigned char)r; return 1; }
    if (r < 0x800) { o[0] = (unsigned char)(0xc0 | r >> 6); o[1] = (unsigned char)(0x80 | (r & 0x3f)); return 2; }
    if (r < 0x10000) {
        o[0] = (unsigned char)(0xe0 | r >> 12);
        o[1] = (unsigned char)(0x80 | ((r >> 6) & 0x3f));
        o[2] = (unsigned char)(0x80 | (r & 0x3f));
        return 3;
    }
    o[0] = (unsigned char)(0xf0 | r >> 18);
    o[1] = (unsigned char)(0x80 | ((r >> 12) & 0x3f));
    o[2] = (unsigned char)(0x80 | ((r >> 6) & 0x3f));
    o[3] = (unsigned char)(0x80 | (r & 0x3f));
    return 4;
}

static size_t utf8_len(const unsigned char *s, size_t n) {
    unsigned char c = s[0], lo = 0x80, hi = 0xbf;
    size_t need;
    if (c < 0x80) return 1;
    if (c >= 0xc2 && c <= 0xdf) need = 2;
    else if (c >= 0xe0 && c <= 0xef) { need = 3; if (c == 0xe0) lo = 0xa0; if (c == 0xed) hi = 0x9f; }
    else if (c >= 0xf0 && c <= 0xf4) { need = 4; if (c == 0xf0) lo = 0x90; if (c == 0xf4) hi = 0x8f; }
    else return 0;
    if (n < need || s[1] < lo || s[1] > hi) return 0;
    for (size_t i = 2; i < need; i++)
        if (s[i] < 0x80 || s[i] > 0xbf) return 0;
    return need;
}

static uint32_t u4(const char *p) {
    uint32_t r = 0;
    for (int i = 0; i < 4; i++) r = r << 4 | (uint32_t)hexv((unsigned char)p[i]);
    return r;
}

static unsigned char *fj_unq(struct ctx *c, size_t node, size_t *on) {
    const struct fj_node *nd = &c->j.v[node];
    const char *p = nd->p + 1, *e = nd->p + nd->n - 1;
    unsigned char *o = malloc(nd->n * 3 + 1), *w = o;
    if (!o) return NULL;
    while (p < e) {
        unsigned char ch = (unsigned char)*p;
        if (ch == '\\') {
            char x = p[1];
            if (x == 'u') {
                uint32_t r = u4(p + 2);
                p += 6;
                if (r >= 0xd800 && r < 0xe000) {
                    if (r < 0xdc00 && e - p >= 6 && p[0] == '\\' && p[1] == 'u') {
                        uint32_t r2 = u4(p + 2);
                        if (r2 >= 0xdc00 && r2 < 0xe000) {
                            r = 0x10000 + ((r - 0xd800) << 10) + (r2 - 0xdc00);
                            p += 6;
                        } else {
                            r = 0xfffd;
                        }
                    } else {
                        r = 0xfffd;
                    }
                }
                w += utf8_put(w, r);
                continue;
            }
            *w++ = (unsigned char)(x == 'b' ? '\b' : x == 'f' ? '\f' : x == 'n' ? '\n' : x == 'r' ? '\r'
                                   : x == 't' ? '\t' : x);
            p += 2;
            continue;
        }
        size_t l = utf8_len((const unsigned char *)p, (size_t)(e - p));
        if (!l) { w += utf8_put(w, 0xfffd); p++; continue; }
        memcpy(w, p, l);
        w += l;
        p += l;
    }
    *on = (size_t)(w - o);
    *w = 0;
    return o;
}

static int fail(struct ctx *c, const char *fmt, ...) {
    va_list ap;
    int k = 0;
    if (c->failed) return -1;
    c->failed = 1;
    if (!c->why || !c->why_n) return -1;
    if (c->mask) k = snprintf(c->why, c->why_n, "%s: ", c->mask);
    if (k < 0 || (size_t)k >= c->why_n) return -1;
    va_start(ap, fmt);
    vsnprintf(c->why + k, c->why_n - (size_t)k, fmt, ap);
    va_end(ap);
    return -1;
}

static int oom(struct ctx *c) {
    c->mask = NULL;
    return fail(c, "out of memory");
}

static void key_fold(struct ctx *c, size_t key, char *k, size_t cap) {
    size_t n, w = 0;
    unsigned char *s = fj_unq(c, key, &n);
    k[0] = '\0';
    if (!s) return;
    for (size_t i = 0; i < n && w + 1 < cap; i++) {
        unsigned char ch = s[i];
        if (ch >= 'a' && ch <= 'z') ch = (unsigned char)(ch - 32);
        else if (ch == 0xe2 && i + 2 < n && s[i + 1] == 0x84 && s[i + 2] == 0xaa) { ch = 'K'; i += 2; }
        else if (ch == 0xc5 && i + 1 < n && s[i + 1] == 0xbf) { ch = 'S'; i += 1; }
        else if (ch >= 0x80 || ch == 0) ch = 0x01;
        k[w++] = (char)ch;
    }
    if (w + 1 >= cap) w = 0;
    k[w] = '\0';
    free(s);
}

#define FJ_EACH(c, obj, m) for (size_t m = (c)->j.v[obj].kid; m; m = (c)->j.v[m].next)

static unsigned char ty(struct ctx *c, size_t node) { return c->j.v[node].t; }

static int get_str(struct ctx *c, size_t node, char **dst, const char *field) {
    size_t n;
    char *s;
    if (ty(c, node) == FJ_NULL) return 0;
    if (ty(c, node) != FJ_STR) return fail(c, "invalid %s", field);
    s = (char *)fj_unq(c, node, &n);
    if (!s) return oom(c);
    for (size_t i = 0; i < n; i++)
        if (!s[i]) s[i] = 0x01;
    free(*dst);
    *dst = s;
    return 0;
}

static int go_atoi(const char *s, size_t n, int64_t *v) {
    size_t i = 0;
    int neg = 0;
    uint64_t r = 0, lim;
    if (n && (s[0] == '+' || s[0] == '-')) { neg = s[0] == '-'; i = 1; }
    if (i == n) return -1;
    lim = neg ? (uint64_t)INT64_MAX + 1 : (uint64_t)INT64_MAX;
    for (; i < n; i++) {
        if (!isdig(s[i])) return -1;
        if (r > (lim - (uint64_t)(s[i] - '0')) / 10) return -1;
        r = r * 10 + (uint64_t)(s[i] - '0');
    }
    *v = neg ? (r == (uint64_t)INT64_MAX + 1 ? INT64_MIN : -(int64_t)r) : (int64_t)r;
    return 0;
}

static int go_range(const char *s, size_t n, int64_t *a, int64_t *b) {
    const char *d;
    if (!go_atoi(s, n, a)) { *b = *a; return 0; }
    if (!n) { *a = *b = 0; return 0; }
    d = memchr(s + 1, '-', n - 1);
    if (s[0] != '-') d = memchr(s, '-', n);
    if (!d) return -1;
    if (go_atoi(s, (size_t)(d - s), a) || go_atoi(d + 1, (size_t)(s + n - d - 1), b)) return -1;
    return 0;
}

static int num_int(struct ctx *c, size_t node, int64_t lo, int64_t hi, int64_t *v) {
    const struct fj_node *nd = &c->j.v[node];
    if (nd->t != FJ_NUM || memchr(nd->p, '.', nd->n) || memchr(nd->p, 'e', nd->n) || memchr(nd->p, 'E', nd->n))
        return -1;
    if (go_atoi(nd->p, nd->n, v) || *v < lo || *v > hi) return -1;
    return 0;
}

static int num_u64(struct ctx *c, size_t node, uint64_t *v) {
    const struct fj_node *nd = &c->j.v[node];
    uint64_t r = 0;
    if (nd->t != FJ_NUM) return -1;
    for (size_t i = 0; i < nd->n; i++) {
        uint64_t d = (uint64_t)(nd->p[i] - '0');
        if (!isdig(nd->p[i]) || r > (UINT64_MAX - d) / 10) return -1;
        r = r * 10 + d;
    }
    *v = r;
    return 0;
}

static int64_t wrap32(int64_t v) {
    uint32_t u = (uint32_t)(uint64_t)v;
    return u >= 0x80000000u ? (int64_t)u - 4294967296LL : (int64_t)u;
}

static int get_rng(struct ctx *c, size_t node, struct fm_rng *r) {
    int64_t a, b;
    if (ty(c, node) == FJ_STR || ty(c, node) == FJ_NULL) {
        size_t n = 0;
        char *s = NULL;
        int bad;
        if (ty(c, node) == FJ_STR && !(s = (char *)fj_unq(c, node, &n))) return oom(c);
        bad = go_range(s ? s : "", n, &a, &b);
        free(s);
        if (bad) return fail(c, "Invalid integer range");
        a = wrap32(a);
        b = wrap32(b);
    } else if (!num_int(c, node, INT32_MIN, INT32_MAX, &a)) {
        b = a;
    } else {
        return fail(c, "Invalid integer range");
    }
    r->from = a < b ? a : b;
    r->to = a < b ? b : a;
    return 0;
}

static int get_rngs(struct ctx *c, size_t node, struct fm_rng **v, size_t *n, const char *field) {
    size_t k = 0;
    free(*v);
    *v = NULL;
    *n = 0;
    if (ty(c, node) == FJ_NULL) return 0;
    if (ty(c, node) != FJ_ARR) return fail(c, "invalid %s", field);
    FJ_EACH(c, node, m) k++;
    if (!k) return 0;
    if (!(*v = calloc(k, sizeof **v))) return oom(c);
    FJ_EACH(c, node, m) if (get_rng(c, m, &(*v)[(*n)++])) return -1;
    return 0;
}

static int b64_dec(const unsigned char *s, size_t n, unsigned char *o, size_t *on) {
    static const char al[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    uint32_t q = 0;
    int k = 0;
    size_t w = 0, i = 0;
    for (; i < n; i++) {
        const char *f;
        if (s[i] == '\r' || s[i] == '\n') continue;
        if (s[i] == '=') {
            if (k < 2) return -1;
            if (k == 2) {
                for (i++; i < n && (s[i] == '\r' || s[i] == '\n'); i++) {}
                if (i >= n || s[i] != '=') return -1;
            }
            for (i++; i < n; i++)
                if (s[i] != '\r' && s[i] != '\n') return -1;
            o[w++] = (unsigned char)(q >> (k == 2 ? 4 : 10));
            if (k == 3) o[w++] = (unsigned char)(q >> 2);
            *on = w;
            return 0;
        }
        if (!s[i] || !(f = strchr(al, s[i]))) return -1;
        q = q << 6 | (uint32_t)(f - al);
        if (++k == 4) {
            o[w++] = (unsigned char)(q >> 16);
            o[w++] = (unsigned char)(q >> 8);
            o[w++] = (unsigned char)q;
            q = 0;
            k = 0;
        }
    }
    if (k) return -1;
    *on = w;
    return 0;
}

static int hex_dec(const unsigned char *s, size_t n, unsigned char *o, size_t *on) {
    if (n % 2) return -1;
    for (size_t i = 0; i < n; i += 2) {
        int h = hexv(s[i]), l = hexv(s[i + 1]);
        if (h < 0 || l < 0) return -1;
        o[i / 2] = (unsigned char)(h << 4 | l);
    }
    *on = n / 2;
    return 0;
}

static int lower_is(const char *s, const char *want) {
    for (; *s && *want; s++, want++) {
        char ch = *s;
        if (ch >= 'A' && ch <= 'Z') ch = (char)(ch + 32);
        if (ch != *want) return 0;
    }
    return !*s && !*want;
}

static int get_bytes(struct ctx *c, size_t node, const char *type, unsigned char **out, size_t *on, const char *field) {
    const char *t = type ? type : "";
    int arr = lower_is(t, "") || lower_is(t, "array");
    int mode = arr ? 0 : lower_is(t, "str") ? 1 : lower_is(t, "hex") ? 2 : lower_is(t, "base64") ? 3 : -1;
    unsigned char *s, *o;
    size_t n;
    *out = NULL;
    *on = 0;
    if (mode < 0) return fail(c, "unknown type");
    if (!node) return mode == 0 ? 0 : fail(c, "invalid %s", field);
    if (ty(c, node) == FJ_NULL) return 0;
    if (mode == 0 && ty(c, node) == FJ_ARR) {
        size_t k = 0;
        FJ_EACH(c, node, m) k++;
        if (!(o = malloc(k + 1))) return oom(c);
        *out = o;
        FJ_EACH(c, node, m) {
            int64_t v = 0;
            if (ty(c, m) != FJ_NULL && num_int(c, m, 0, 255, &v)) return fail(c, "invalid %s", field);
            o[(*on)++] = (unsigned char)v;
        }
        return 0;
    }
    if (ty(c, node) != FJ_STR) return fail(c, "invalid %s", field);
    if (!(s = fj_unq(c, node, &n))) return oom(c);
    if (mode == 1) { *out = s; *on = n; return 0; }
    if (!(o = malloc(n + 1))) { free(s); return oom(c); }
    *out = o;
    if ((mode == 2 ? hex_dec(s, n, o, on) : b64_dec(s, n, o, on))) { free(s); return fail(c, "invalid %s", field); }
    free(s);
    return 0;
}

static int var_ok(struct ctx *c, const char *v) {
    if (!v || !*v) return 0;
    if (!((*v >= 'A' && *v <= 'Z') || (*v >= 'a' && *v <= 'z') || *v == '_')) return fail(c, "invalid variable name");
    for (v++; *v; v++)
        if (!((*v >= 'A' && *v <= 'Z') || (*v >= 'a' && *v <= 'z') || isdig(*v) || *v == '_'))
            return fail(c, "invalid variable name");
    return 0;
}

static void expr_free(struct fm_hc_expr *e) {
    if (!e) return;
    for (size_t i = 0; i < e->n; i++) {
        free(e->a[i].bytes);
        free(e->a[i].name);
        expr_free(e->a[i].expr);
    }
    free(e->a);
    free(e);
}

static struct fm_hc_expr *build_expr(struct ctx *c, size_t node);

static int build_arg(struct ctx *c, size_t node, struct fm_hc_arg *a) {
    char *type = NULL, *reuse = NULL, *meta = NULL, k[24];
    size_t bytes = 0, tf = 0;
    uint64_t u64 = 0;
    int kinds = 0, has_u64 = 0, rc = -1;
    if (ty(c, node) == FJ_NULL) return fail(c, "transform arg must set exactly one value");
    if (ty(c, node) != FJ_OBJ) return fail(c, "invalid args");
    FJ_EACH(c, node, m) {
        key_fold(c, c->j.v[m].key, k, sizeof k);
        if (!strcmp(k, "TYPE")) { if (get_str(c, m, &type, "type")) goto out; }
        else if (!strcmp(k, "BYTES")) bytes = m;
        else if (!strcmp(k, "U64")) {
            has_u64 = ty(c, m) != FJ_NULL;
            if (has_u64 && num_u64(c, m, &u64)) { fail(c, "invalid u64"); goto out; }
        } else if (!strcmp(k, "REUSE")) { if (get_str(c, m, &reuse, "reuse")) goto out; }
        else if (!strcmp(k, "METADATA")) { if (get_str(c, m, &meta, "metadata")) goto out; }
        else if (!strcmp(k, "TRANSFORM")) {
            if (ty(c, m) != FJ_NULL && ty(c, m) != FJ_OBJ) { fail(c, "invalid transform"); goto out; }
            tf = ty(c, m) == FJ_NULL ? 0 : m;
        }
    }
    kinds = (bytes != 0) + has_u64 + (reuse && *reuse) + (meta && *meta) + (tf != 0);
    if (kinds != 1) { fail(c, "transform arg must set exactly one value"); goto out; }
    if (bytes) {
        a->kind = FMA_BYTES;
        if (get_bytes(c, bytes, type, &a->bytes, &a->bytes_n, "bytes")) goto out;
    } else if (has_u64) {
        a->kind = FMA_U64;
        a->u64 = u64;
    } else if (reuse && *reuse) {
        if (var_ok(c, reuse)) goto out;
        a->kind = FMA_VAR;
        a->name = reuse;
        reuse = NULL;
    } else if (meta && *meta) {
        a->kind = FMA_META;
        a->name = meta;
        meta = NULL;
    } else {
        a->kind = FMA_EXPR;
        if (!(a->expr = build_expr(c, tf))) goto out;
    }
    rc = 0;
out:
    free(type);
    free(reuse);
    free(meta);
    return rc;
}

static struct fm_hc_expr *build_expr(struct ctx *c, size_t node) {
    char *op = NULL, k[24];
    size_t args = 0, n = 0;
    struct fm_hc_expr *e = NULL;
    FJ_EACH(c, node, m) {
        key_fold(c, c->j.v[m].key, k, sizeof k);
        if (!strcmp(k, "OP")) { if (get_str(c, m, &op, "op")) goto bad; }
        else if (!strcmp(k, "ARGS")) {
            if (ty(c, m) != FJ_NULL && ty(c, m) != FJ_ARR) { fail(c, "invalid args"); goto bad; }
            args = ty(c, m) == FJ_NULL ? 0 : m;
        }
    }
    if (!op || !*op) { fail(c, "transform op is required"); goto bad; }
    if (args) FJ_EACH(c, args, m) n++;
    if (!n) { fail(c, "transform args are required"); goto bad; }
    if (!(e = calloc(1, sizeof *e)) || !(e->a = calloc(n, sizeof *e->a))) { oom(c); goto bad; }
    for (size_t i = 0; i < sizeof fm_ops / sizeof fm_ops[0]; i++)
        if (!strcmp(op, fm_ops[i].name)) e->op = fm_ops[i].op;
    if (!e->op) { fail(c, "unsupported expr op: %s", op); goto bad; }
    FJ_EACH(c, args, m) {
        if (build_arg(c, m, &e->a[e->n])) { e->n++; goto bad; }
        e->n++;
    }
    free(op);
    return e;
bad:
    free(op);
    expr_free(e);
    return NULL;
}

struct raw_item { size_t packet, transform; char *type; int rr_set; struct fm_rng rr; };
struct raw_seq { size_t n; struct fm_hc_item *it; struct raw_item *raw; };

static int read_item(struct ctx *c, size_t node, struct fm_hc_item *it, struct raw_item *r) {
    char k[24];
    if (ty(c, node) == FJ_NULL) return 0;
    if (ty(c, node) != FJ_OBJ) return fail(c, "invalid item");
    FJ_EACH(c, node, m) {
        key_fold(c, c->j.v[m].key, k, sizeof k);
        if (!strcmp(k, "DELAY")) { if (get_rng(c, m, &it->delay)) return -1; }
        else if (!strcmp(k, "RAND")) {
            int64_t v;
            if (ty(c, m) == FJ_NULL) continue;
            if (num_int(c, m, INT32_MIN, INT32_MAX, &v)) return fail(c, "invalid rand");
            it->rand = (int32_t)v;
        } else if (!strcmp(k, "RANDRANGE")) {
            r->rr_set = ty(c, m) != FJ_NULL;
            if (r->rr_set && get_rng(c, m, &r->rr)) return -1;
        } else if (!strcmp(k, "CAPTURE")) { if (get_str(c, m, &it->capture, "capture")) return -1; }
        else if (!strcmp(k, "TYPE")) { if (get_str(c, m, &r->type, "type")) return -1; }
        else if (!strcmp(k, "REUSE")) { if (get_str(c, m, &it->reuse, "reuse")) return -1; }
        else if (!strcmp(k, "TRANSFORM")) {
            if (ty(c, m) != FJ_NULL && ty(c, m) != FJ_OBJ) return fail(c, "invalid transform");
            r->transform = ty(c, m) == FJ_NULL ? 0 : m;
        } else if (!strcmp(k, "PACKET")) r->packet = m;
    }
    return 0;
}

static void seqs_free(struct fm_hc_seq *s, size_t n) {
    for (size_t i = 0; s && i < n; i++) {
        for (size_t k = 0; k < s[i].n; k++) {
            free(s[i].it[k].packet);
            free(s[i].it[k].capture);
            free(s[i].it[k].reuse);
            expr_free(s[i].it[k].expr);
        }
        free(s[i].it);
    }
    free(s);
}

static void raws_free(struct raw_seq *r, size_t n) {
    for (size_t i = 0; r && i < n; i++) {
        for (size_t k = 0; k < r[i].n; k++) free(r[i].raw[k].type);
        free(r[i].raw);
    }
    free(r);
}

static int read_seqs(struct ctx *c, size_t node, struct fm_hc_seq **out, struct raw_seq **raw, size_t *n,
                     const char *field) {
    size_t k = 0;
    seqs_free(*out, *n);
    raws_free(*raw, *n);
    *out = NULL;
    *raw = NULL;
    *n = 0;
    if (ty(c, node) == FJ_NULL) return 0;
    if (ty(c, node) != FJ_ARR) return fail(c, "invalid %s", field);
    FJ_EACH(c, node, m) k++;
    if (!k) return 0;
    if (!(*out = calloc(k, sizeof **out)) || !(*raw = calloc(k, sizeof **raw))) return oom(c);
    FJ_EACH(c, node, s) {
        struct fm_hc_seq *q = &(*out)[*n];
        struct raw_seq *rq = &(*raw)[*n];
        size_t ni = 0;
        (*n)++;
        if (ty(c, s) == FJ_NULL) continue;
        if (ty(c, s) != FJ_ARR) return fail(c, "invalid %s", field);
        FJ_EACH(c, s, i) ni++;
        if (!ni) continue;
        if (!(q->it = calloc(ni, sizeof *q->it)) || !(rq->raw = calloc(ni, sizeof *rq->raw))) return oom(c);
        FJ_EACH(c, s, i) {
            q->n++;
            rq->n++;
            if (read_item(c, i, &q->it[q->n - 1], &rq->raw[rq->n - 1])) return -1;
        }
    }
    return 0;
}

static int item_spec(struct ctx *c, const struct fm_hc_item *it, const struct raw_item *r) {
    int kinds;
    if (var_ok(c, it->capture) || var_ok(c, it->reuse)) return -1;
    kinds = (r->packet != 0) + (it->rand > 0) + (it->reuse && *it->reuse) + (r->transform != 0);
    if (kinds > 1 || (kinds == 0 && it->capture && *it->capture)) return fail(c, "exactly one item kind must be set");
    return 0;
}

static int item_build(struct ctx *c, struct fm_hc_item *it, struct raw_item *r) {
    struct fm_rng rr = r->rr_set ? r->rr : (struct fm_rng){0, 255};
    if (rr.from < 0 || rr.to > 255) return fail(c, "invalid randRange");
    it->rmin = (uint8_t)rr.from;
    it->rmax = (uint8_t)rr.to;
    if (get_bytes(c, r->packet, r->type, &it->packet, &it->packet_n, "packet")) return -1;
    if (r->transform && !(it->expr = build_expr(c, r->transform))) return -1;
    if (it->capture && !*it->capture) { free(it->capture); it->capture = NULL; }
    if (it->reuse && !*it->reuse) { free(it->reuse); it->reuse = NULL; }
    return 0;
}

struct vars { size_t n, cap; const char **v; };

static int var_add(struct ctx *c, struct vars *vs, const char *name) {
    if (!name) return 0;
    if (vs->n == vs->cap) {
        size_t cap = vs->cap ? vs->cap * 2 : 8;
        const char **v = realloc(vs->v, cap * sizeof *v);
        if (!v) return oom(c);
        vs->v = v;
        vs->cap = cap;
    }
    vs->v[vs->n++] = name;
    return 0;
}

static int var_known(const struct vars *vs, const char *name) {
    for (size_t i = 0; i < vs->n; i++)
        if (!strcmp(vs->v[i], name)) return 1;
    return 0;
}

static int mfail(char *why, size_t why_n, const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(why, why_n, fmt, ap);
    va_end(ap);
    return -1;
}

static int measure_arg(const struct fm_hc_arg *a, fm_hc_size_fn size, const void *ctx, uint64_t *sz, char *why,
                       size_t why_n) {
    switch (a->kind) {
    case FMA_BYTES: *sz = a->bytes_n; return 0;
    case FMA_U64: return mfail(why, why_n, "u64 arg has no byte width");
    case FMA_VAR: return size(ctx, a->name, sz) ? mfail(why, why_n, "unknown variable: %s", a->name) : 0;
    case FMA_META: return mfail(why, why_n, "metadata not implemented: %s", a->name);
    case FMA_EXPR: return fm_hc_measure_expr(a->expr, size, ctx, sz, why, why_n);
    }
    return mfail(why, why_n, "empty expr arg");
}

int fm_hc_measure_expr(const struct fm_hc_expr *e, fm_hc_size_fn size, const void *ctx, uint64_t *sz, char *why,
                       size_t why_n) {
    const char *name = fm_hc_op_name(e->op);
    size_t want, at;
    switch (e->op) {
    case FMO_CONCAT:
        *sz = 0;
        for (size_t i = 0; i < e->n; i++) {
            uint64_t s;
            if (measure_arg(&e->a[i], size, ctx, &s, why, why_n)) return -1;
            *sz = s > UINT64_MAX - *sz ? UINT64_MAX : *sz + s;
        }
        return 0;
    case FMO_BE16: case FMO_LE16: *sz = 2; return 0;
    case FMO_BE32: case FMO_LE32: *sz = 4; return 0;
    case FMO_LE64: *sz = 8; return 0;
    case FMO_SLICE: want = 3; at = 2; break;
    case FMO_PAD: want = 3; at = 1; break;
    case FMO_TRUNCATE: want = 2; at = 1; break;
    default:
        return mfail(why, why_n, "expr size is not bytes for op: %s", name);
    }
    if (e->n != want) return mfail(why, why_n, "%s expects %zu args", name, want);
    if (e->a[at].kind != FMA_U64) return mfail(why, why_n, "%s length must be u64", name);
    *sz = e->a[at].u64;
    return 0;
}

int fm_hc_measure_item(const struct fm_hc_item *it, fm_hc_size_fn size, const void *ctx, uint64_t *sz, char *why,
                       size_t why_n) {
    *sz = 0;
    if (it->rand > 0) *sz = (uint64_t)it->rand;
    else if (it->packet_n) *sz = it->packet_n;
    else if (it->reuse) return size(ctx, it->reuse, sz) ? mfail(why, why_n, "unknown variable: %s", it->reuse) : 0;
    else if (it->expr) return fm_hc_measure_expr(it->expr, size, ctx, sz, why, why_n);
    return 0;
}

static int known_size(const void *ctx, const char *name, uint64_t *sz) {
    *sz = 0;
    return var_known(ctx, name) ? 0 : -1;
}

static int measure_seq(struct ctx *c, const struct fm_hc_seq *s, struct vars *vs, int server) {
    for (size_t i = 0; i < s->n; i++) {
        const struct fm_hc_item *it = &s->it[i];
        char why[128];
        uint64_t sz;
        if (server && fm_hc_measure_item(it, known_size, vs, &sz, why, sizeof why)) return fail(c, "%s", why);
        if (var_add(c, vs, it->capture)) return -1;
    }
    return 0;
}

static int hc_measure(struct ctx *c, const struct fm_hc *h) {
    struct vars vs = {0, 0, NULL};
    size_t j = 0;
    int rc = 0;
    for (size_t i = 0; i < h->n_cl && !rc; i++) {
        rc = measure_seq(c, &h->cl[i], &vs, 0);
        if (!rc && j < h->n_sv) rc = measure_seq(c, &h->sv[j++], &vs, 1);
    }
    while (!rc && j < h->n_sv) rc = measure_seq(c, &h->sv[j++], &vs, 1);
    free(vs.v);
    return rc;
}

static int build_hc(struct ctx *c, size_t node, struct fm_hc *h) {
    struct raw_seq *raw[3] = {NULL, NULL, NULL};
    struct fm_hc_seq **seq[3] = {&h->cl, &h->sv, &h->er};
    size_t *cnt[3] = {&h->n_cl, &h->n_sv, &h->n_er};
    char k[24];
    int rc = -1;
    if (node && ty(c, node) != FJ_NULL) {
        if (ty(c, node) != FJ_OBJ) return fail(c, "invalid settings");
        FJ_EACH(c, node, m) {
            int w;
            key_fold(c, c->j.v[m].key, k, sizeof k);
            w = !strcmp(k, "CLIENTS") ? 0 : !strcmp(k, "SERVERS") ? 1 : !strcmp(k, "ERRORS") ? 2 : -1;
            if (w >= 0 && read_seqs(c, m, seq[w], &raw[w], cnt[w], w == 0 ? "clients" : w == 1 ? "servers" : "errors"))
                goto out;
        }
    }
    for (int w = 0; w < 3; w++)
        for (size_t s = 0; s < *cnt[w]; s++)
            for (size_t i = 0; i < (*seq[w])[s].n; i++)
                if (item_spec(c, &(*seq[w])[s].it[i], &raw[w][s].raw[i])) goto out;
    for (int w = 0; w < 3; w++)
        for (size_t s = 0; s < *cnt[w]; s++)
            for (size_t i = 0; i < (*seq[w])[s].n; i++)
                if (item_build(c, &(*seq[w])[s].it[i], &raw[w][s].raw[i])) goto out;
    rc = hc_measure(c, h);
out:
    for (int w = 0; w < 3; w++) raws_free(raw[w], *cnt[w]);
    return rc;
}

static int build_frag(struct ctx *c, size_t node, struct fm_frag *f) {
    char *packets = NULL, k[24];
    struct fm_rng length = {0, 0}, delay = {0, 0};
    int rc = -1;
    if (node && ty(c, node) != FJ_NULL) {
        if (ty(c, node) != FJ_OBJ) return fail(c, "invalid settings");
        FJ_EACH(c, node, m) {
            int bad = 0;
            key_fold(c, c->j.v[m].key, k, sizeof k);
            if (!strcmp(k, "PACKETS")) bad = get_str(c, m, &packets, "packets");
            else if (!strcmp(k, "LENGTH")) bad = get_rng(c, m, &length);
            else if (!strcmp(k, "DELAY")) bad = get_rng(c, m, &delay);
            else if (!strcmp(k, "LENGTHS")) bad = get_rngs(c, m, &f->len, &f->n_len, "lengths");
            else if (!strcmp(k, "DELAYS")) bad = get_rngs(c, m, &f->delay, &f->n_delay, "delays");
            else if (!strcmp(k, "MAXSPLIT")) bad = get_rng(c, m, &f->split);
            if (bad) goto out;
        }
    }
    if (!packets || !*packets) {
        f->pk_from = f->pk_to = 0;
    } else if (lower_is(packets, "tlshello")) {
        f->pk_from = 0;
        f->pk_to = 1;
    } else {
        if (go_range(packets, strlen(packets), &f->pk_from, &f->pk_to)) { fail(c, "Invalid PacketsFrom"); goto out; }
        if (f->pk_from == 0) { fail(c, "PacketsFrom can't be 0"); goto out; }
    }
    if (!f->n_len) {
        if (!(f->len = malloc(sizeof *f->len))) { oom(c); goto out; }
        f->len[0] = length;
        f->n_len = 1;
    }
    if (f->len[f->n_len - 1].from == 0) { fail(c, "last lengths entry min can't be 0"); goto out; }
    for (size_t i = 0; i < f->n_len; i++)
        if (f->len[i].from < 0) { fail(c, "lengths min below 0"); goto out; }
    if (!f->n_delay) {
        if (!(f->delay = malloc(sizeof *f->delay))) { oom(c); goto out; }
        f->delay[0] = delay;
        f->n_delay = 1;
    }
    rc = 0;
out:
    free(packets);
    return rc;
}

struct sd_str { unsigned char *s; size_t n; };
struct sd_list { size_t n; struct sd_str *v; };

static int get_bstr(struct ctx *c, size_t node, struct sd_str *d, const char *field) {
    size_t n;
    unsigned char *s;
    if (ty(c, node) == FJ_NULL) return 0;
    if (ty(c, node) != FJ_STR) return fail(c, "invalid %s", field);
    if (!(s = fj_unq(c, node, &n))) return oom(c);
    free(d->s);
    d->s = s;
    d->n = n;
    return 0;
}

static void list_free(struct sd_list *l) {
    for (size_t i = 0; i < l->n; i++) free(l->v[i].s);
    free(l->v);
    l->v = NULL;
    l->n = 0;
}

static int get_list(struct ctx *c, size_t node, struct sd_list *l, const char *field) {
    size_t k = 0;
    if (ty(c, node) != FJ_NULL && ty(c, node) != FJ_ARR) return fail(c, "invalid %s", field);
    list_free(l);
    if (ty(c, node) == FJ_NULL) return 0;
    FJ_EACH(c, node, m) k++;
    if (!k) return 0;
    if (!(l->v = calloc(k, sizeof *l->v))) return oom(c);
    FJ_EACH(c, node, m) {
        struct sd_str *e = &l->v[l->n++];
        if (get_bstr(c, m, e, field)) return -1;
    }
    return 0;
}

static int get_u32(struct ctx *c, size_t node, uint32_t *v, const char *field) {
    uint64_t u;
    if (ty(c, node) == FJ_NULL) return 0;
    if (num_u64(c, node, &u) || u > UINT32_MAX) return fail(c, "invalid %s", field);
    *v = (uint32_t)u;
    return 0;
}

static uint32_t rune_at(const unsigned char *s, size_t n, size_t *len) {
    size_t l = utf8_len(s, n);
    *len = l ? l : 1;
    if (!l) return 0xfffd;
    if (l == 1) return s[0];
    if (l == 2) return (uint32_t)(s[0] & 0x1f) << 6 | (uint32_t)(s[1] & 0x3f);
    if (l == 3) return (uint32_t)(s[0] & 0x0f) << 12 | (uint32_t)(s[1] & 0x3f) << 6 | (uint32_t)(s[2] & 0x3f);
    return (uint32_t)(s[0] & 0x07) << 18 | (uint32_t)(s[1] & 0x3f) << 12 | (uint32_t)(s[2] & 0x3f) << 6 |
           (uint32_t)(s[3] & 0x3f);
}

static int go_space(uint32_t r) {
    return (r >= 9 && r <= 13) || r == ' ' || r == 0x85 || r == 0xa0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) ||
           r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000;
}

static void go_trim(const unsigned char *s, size_t n, size_t *a, size_t *b) {
    size_t l;
    *a = 0;
    *b = n;
    while (*a < *b && go_space(rune_at(s + *a, *b - *a, &l))) *a += l;
    while (*b > *a) {
        size_t st = *b - 1;
        while (st > *a && (s[st] & 0xc0) == 0x80) st--;
        if (!go_space(rune_at(s + st, *b - st, &l)) || st + l != *b) break;
        *b = st;
    }
}

static size_t go_lower(const unsigned char *s, size_t n, unsigned char *o, int nospace) {
    size_t w = 0, l;
    for (size_t i = 0; i < n; i += l) {
        uint32_t r = rune_at(s + i, n - i, &l);
        if (r >= 'A' && r <= 'Z') o[w++] = (unsigned char)(r + 32);
        else if (r == 0x130) o[w++] = 'i';
        else if (r == 0x212a) o[w++] = 'k';
        else if (!nospace || r != ' ') { memcpy(o + w, s + i, l); w += l; }
    }
    return w;
}

static int sd_mode(struct ctx *c, const struct sd_str *a, uint8_t *ascii) {
    static const char *const word[] = {"", "entropy", "prefer_entropy", "ascii", "prefer_ascii"};
    unsigned char m[48];
    size_t x, y, w;
    if (!a->n) { *ascii = 0; return 0; }
    go_trim(a->s, a->n, &x, &y);
    if (y - x <= sizeof m) {
        w = go_lower(a->s + x, y - x, m, 0);
        for (size_t i = 0; i < sizeof word / sizeof *word; i++)
            if (w == strlen(word[i]) && !memcmp(m, word[i], w)) { *ascii = i >= 3; return 0; }
    }
    return fail(c, "invalid sudoku ascii mode: %.*s", (int)a->n, a->s ? (const char *)a->s : "");
}

static void go_quote(const unsigned char *s, size_t l, char *q, size_t qn) {
    static const char esc[] = "\a\b\f\n\r\t\v", let[] = "abfnrtv";
    const char *e = s[0] ? memchr(esc, s[0], sizeof esc - 1) : NULL;
    if (l > 1) snprintf(q, qn, "%.*s", (int)l, (const char *)s);
    else if (e) snprintf(q, qn, "\\%c", let[e - esc]);
    else if (s[0] == '\'' || s[0] == '\\') snprintf(q, qn, "\\%c", s[0]);
    else if (s[0] < 0x20 || s[0] == 0x7f) snprintf(q, qn, "\\x%02x", s[0]);
    else snprintf(q, qn, "%c", s[0]);
}

static int sd_pat(struct ctx *c, const struct sd_str *raw, char out[9]) {
    size_t x, y, w, l, nx = 0, np = 0, nv = 0;
    unsigned char *b;
    char q[8];
    out[0] = '\0';
    go_trim(raw->s, raw->n, &x, &y);
    if (x == y) return 0;
    if (!(b = malloc(y - x))) return oom(c);
    w = go_lower(raw->s + x, y - x, b, 1);
    if (w != 8) { free(b); return fail(c, "customTable must be 8 chars, got %zu", w); }
    for (size_t i = 0; i < w; i += l) {
        uint32_t r = rune_at(b + i, w - i, &l);
        if (r == 'x') nx++;
        else if (r == 'p') np++;
        else if (r == 'v') nv++;
        else {
            go_quote(b + i, l, q, sizeof q);
            free(b);
            return fail(c, "customTable has invalid char '%s'", q);
        }
    }
    memcpy(out, b, 8);
    out[8] = '\0';
    free(b);
    if (nx != 2 || np != 2 || nv != 4) return fail(c, "customTable must contain exactly 2 x, 2 p and 4 v");
    return 0;
}

static int build_sd(struct ctx *c, size_t node, struct fm_sd *d) {
    static const char *const pk[4] = {"PADDINGMIN", "PADDING_MIN", "PADDINGMAX", "PADDING_MAX"};
    static const char *const pf[4] = {"paddingMin", "padding_min", "paddingMax", "padding_max"};
    struct sd_str pw = {NULL, 0}, ascii = {NULL, 0}, ct = {NULL, 0}, lct = {NULL, 0};
    struct sd_list cts = {0, NULL}, lcts = {0, NULL};
    const struct sd_list *tabs;
    uint32_t pad[4] = {0, 0, 0, 0};
    size_t nraw;
    char k[24];
    int rc = -1;
    if (node && ty(c, node) != FJ_NULL) {
        if (ty(c, node) != FJ_OBJ) return fail(c, "invalid settings");
        FJ_EACH(c, node, m) {
            int bad = 0;
            key_fold(c, c->j.v[m].key, k, sizeof k);
            if (!strcmp(k, "PASSWORD")) bad = get_bstr(c, m, &pw, "password");
            else if (!strcmp(k, "ASCII")) bad = get_bstr(c, m, &ascii, "ascii");
            else if (!strcmp(k, "CUSTOMTABLE")) bad = get_bstr(c, m, &ct, "customTable");
            else if (!strcmp(k, "CUSTOM_TABLE")) bad = get_bstr(c, m, &lct, "custom_table");
            else if (!strcmp(k, "CUSTOMTABLES")) bad = get_list(c, m, &cts, "customTables");
            else if (!strcmp(k, "CUSTOM_TABLES")) bad = get_list(c, m, &lcts, "custom_tables");
            else
                for (int i = 0; i < 4; i++)
                    if (!strcmp(k, pk[i])) bad = get_u32(c, m, &pad[i], pf[i]);
            if (bad) goto out;
        }
    }
    if (sd_mode(c, &ascii, &d->ascii)) goto out;
    tabs = cts.n ? &cts : &lcts;
    nraw = d->ascii ? 0 : tabs->n ? tabs->n : 1;
    if (!(d->pat = calloc(nraw ? nraw : 1, sizeof *d->pat))) { oom(c); goto out; }
    for (size_t i = 0; i < nraw; i++) {
        char p[9];
        size_t j = 0;
        if (sd_pat(c, tabs->n ? &tabs->v[i] : ct.n ? &ct : &lct, p)) goto out;
        while (j < d->n_pat && strcmp(d->pat[j], p)) j++;
        if (j == d->n_pat) memcpy(d->pat[d->n_pat++], p, sizeof p);
    }
    if (!d->n_pat) d->n_pat = 1;
    d->pmin = pad[0] ? pad[0] : pad[1];
    d->pmax = pad[2] ? pad[2] : pad[3];
    if (d->pmin > 100) d->pmin = 100;
    if (d->pmax > 100) d->pmax = 100;
    if (d->pmax < d->pmin) d->pmax = d->pmin;
    d->pw = pw.s;
    d->pw_n = pw.n;
    pw.s = NULL;
    rc = 0;
out:
    free(pw.s);
    free(ascii.s);
    free(ct.s);
    free(lct.s);
    list_free(&cts);
    list_free(&lcts);
    return rc;
}

static void mask_free(struct fm_mask *m) {
    if (m->kind == FM_FRAGMENT) {
        free(m->u.frag.len);
        free(m->u.frag.delay);
    } else if (m->kind == FM_HEADER) {
        seqs_free(m->u.hc.cl, m->u.hc.n_cl);
        seqs_free(m->u.hc.sv, m->u.hc.n_sv);
        seqs_free(m->u.hc.er, m->u.hc.n_er);
    } else if (m->kind == FM_SUDOKU) {
        free(m->u.sd.pw);
        free(m->u.sd.pat);
    }
}

void fm_cfg_free(struct fm_cfg *c) {
    if (!c) return;
    for (size_t i = 0; i < c->n; i++) mask_free(&c->m[i]);
    free(c->m);
    c->m = NULL;
    c->n = 0;
}

static int type_is(const char *t, const char *want) {
    for (; *want; want++) {
        char ch = *t;
        if ((unsigned char)ch == 0xe2 && (unsigned char)t[1] == 0x84 && (unsigned char)t[2] == 0xaa) { ch = 'k'; t += 2; }
        else if (ch >= 'A' && ch <= 'Z') ch = (char)(ch + 32);
        if (ch != *want) return 0;
        t++;
    }
    return !*t;
}

static int build_mask(struct ctx *c, const char *type, size_t settings, struct fm_mask *m) {
    const char *t = type ? type : "";
    memset(m, 0, sizeof *m);
    if (type_is(t, "sudoku")) { m->kind = FM_SUDOKU; c->mask = "sudoku"; return build_sd(c, settings, &m->u.sd); }
    if (type_is(t, "xmc")) return fail(c, "xmc is not supported yet");
    if (type_is(t, "filter")) { m->kind = FM_FILTER; return 0; }
    if (type_is(t, "fragment")) { m->kind = FM_FRAGMENT; c->mask = "fragment"; return build_frag(c, settings, &m->u.frag); }
    if (type_is(t, "header-custom")) { m->kind = FM_HEADER; c->mask = "header-custom"; return build_hc(c, settings, &m->u.hc); }
    return fail(c, "unknown tcp mask type %s", t);
}

struct raw_mask { char *type; size_t settings; };

static int build_cfg(struct ctx *c, size_t root, struct fm_cfg *cfg) {
    struct raw_mask *rm = NULL;
    size_t tcp = 0, n = 0, sd = 0;
    char k[24];
    int rc = -1;
    if (ty(c, root) == FJ_NULL) return 0;
    if (ty(c, root) != FJ_OBJ) return fail(c, "invalid finalmask");
    FJ_EACH(c, root, m) {
        key_fold(c, c->j.v[m].key, k, sizeof k);
        if (strcmp(k, "TCP")) continue;
        if (ty(c, m) != FJ_NULL && ty(c, m) != FJ_ARR) return fail(c, "invalid tcp");
        tcp = ty(c, m) == FJ_NULL ? 0 : m;
    }
    if (!tcp) return 0;
    FJ_EACH(c, tcp, m) n++;
    if (!n) return 0;
    if (!(rm = calloc(n, sizeof *rm)) || !(cfg->m = calloc(n, sizeof *cfg->m))) { oom(c); goto out; }
    n = 0;
    FJ_EACH(c, tcp, e) {
        struct raw_mask *r = &rm[n++];
        if (ty(c, e) == FJ_NULL) continue;
        if (ty(c, e) != FJ_OBJ) { fail(c, "invalid tcp"); goto out; }
        FJ_EACH(c, e, m) {
            key_fold(c, c->j.v[m].key, k, sizeof k);
            if (!strcmp(k, "TYPE")) { if (get_str(c, m, &r->type, "type")) goto out; }
            else if (!strcmp(k, "SETTINGS")) r->settings = ty(c, m) == FJ_NULL ? 0 : m;
        }
    }
    for (size_t i = 0; i < n; i++) {
        c->mask = NULL;
        cfg->n++;
        if (rm[i].type && type_is(rm[i].type, "sudoku") && sd++) {
            fail(c, "sudoku: more than one sudoku mask is not supported");
            goto out;
        }
        if (build_mask(c, rm[i].type, rm[i].settings, &cfg->m[i])) goto out;
    }
    rc = 0;
out:
    for (size_t i = 0; rm && i < n; i++) free(rm[i].type);
    free(rm);
    return rc;
}

int fm_parse(const char *json, size_t n, struct fm_cfg *out, char *why, size_t why_n) {
    struct ctx c;
    struct fm_cfg cfg = {0, NULL};
    size_t root;
    int rc = -1;
    memset(&c, 0, sizeof c);
    c.why = why;
    c.why_n = why_n;
    if (why && why_n) why[0] = '\0';
    if (out) { out->n = 0; out->m = NULL; }
    root = fj_parse(&c.j, json, n);
    if ((!root && c.j.err == FJE_BIG) || (root && n - c.j.ws > FM_JSON_MAX)) fail(&c, "over %d bytes", FM_JSON_MAX);
    else if (!root) fail(&c, c.j.err == FJE_DEEP ? "too deep" : c.j.err == FJE_MEM ? "out of memory" : "does not parse");
    else rc = build_cfg(&c, root, &cfg);
    free(c.j.v);
    if (rc || !out) fm_cfg_free(&cfg);
    else *out = cfg;
    return rc;
}

size_t fm_compact(const char *json, size_t n, char *out, size_t cap) {
    struct fj j;
    size_t len, w = 0;
    int in = 0;
    if (!fj_parse(&j, json, n)) { free(j.v); return 0; }
    free(j.v);
    len = n - j.ws;
    if (len > cap || len > FM_JSON_MAX) return 0;
    for (size_t i = 0; i < n; i++) {
        char ch = json[i];
        if (in) {
            out[w++] = ch;
            if (ch == '\\') out[w++] = json[++i];
            else if (ch == '"') in = 0;
        } else if (ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r') {
            out[w++] = ch;
            in = ch == '"';
        }
    }
    if (w < cap) out[w] = '\0';
    return w;
}
