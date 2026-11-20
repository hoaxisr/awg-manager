#define _GNU_SOURCE
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include "fmlink.h"
#include "transport.h"

struct hc_st { char why[160]; };

struct hv { int isb; uint64_t u; unsigned char *b; size_t n; };

static const char *const g_meta[8] = { "local_port", "dst_port_u16", "local_ip4_u32", "dst_ip4_u32",
                                       "remote_port", "src_port_u16", "remote_ip4_u32", "src_ip4_u32" };

static int bad(struct fm_hc_env *e, const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(e->why, sizeof e->why, fmt, ap);
    va_end(ap);
    return -1;
}

static int too_long(struct fm_hc_env *e) {
    return bad(e, "a value is longer than %d bytes", FM_HC_MAX);
}

static int dup_bytes(struct fm_hc_env *e, struct hv *v, const unsigned char *d, size_t n) {
    v->isb = 1;
    v->n = n;
    v->b = malloc(n ? n : 1);
    if (!v->b) return bad(e, "out of memory");
    if (n) memcpy(v->b, d, n);
    return 0;
}

static void load_meta(struct fm_hc_env *e, int fd, int remote) {
    struct sockaddr_storage a;
    socklen_t al = sizeof a;
    int rc = remote ? getpeername(fd, (struct sockaddr *)&a, &al) : getsockname(fd, (struct sockaddr *)&a, &al);
    if (rc) return;
    unsigned at = remote ? 2u : 0u;
    const unsigned char *ip4 = NULL;
    uint16_t port;
    if (a.ss_family == AF_INET) {
        const struct sockaddr_in *s4 = (const struct sockaddr_in *)&a;
        port = ntohs(s4->sin_port);
        ip4 = (const unsigned char *)&s4->sin_addr;
    } else if (a.ss_family == AF_INET6) {
        const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)&a;
        port = ntohs(s6->sin6_port);
        if (IN6_IS_ADDR_V4MAPPED(&s6->sin6_addr)) ip4 = s6->sin6_addr.s6_addr + 12;
    } else {
        return;
    }
    e->mv[at] = port;
    e->meta |= 1u << at;
    if (ip4) {
        e->mv[at + 1] = (uint64_t)ip4[0] << 24 | (uint64_t)ip4[1] << 16 | (uint64_t)ip4[2] << 8 | ip4[3];
        e->meta |= 1u << (at + 1);
    }
}

void fm_hc_env_init(struct fm_hc_env *e, int fd) {
    memset(e, 0, sizeof *e);
    if (fd < 0) return;
    load_meta(e, fd, 0);
    load_meta(e, fd, 1);
}

void fm_hc_env_free(struct fm_hc_env *e) {
    for (size_t i = 0; i < e->n; i++) free(e->v[i].b);
    free(e->v);
    e->v = NULL;
    e->n = e->cap = 0;
}

static struct fm_hc_var *var_find(struct fm_hc_env *e, const char *name) {
    for (size_t i = 0; i < e->n; i++)
        if (!strcmp(e->v[i].name, name)) return &e->v[i];
    return NULL;
}

static int var_set(struct fm_hc_env *e, const char *name, unsigned char *b, size_t n) {
    struct fm_hc_var *v = var_find(e, name);
    size_t all = n;
    for (size_t i = 0; i < e->n; i++)
        if (&e->v[i] != v) all += e->v[i].n;
    if (all > FM_HC_VARS) {
        free(b);
        return bad(e, "captured values are longer than %d bytes", FM_HC_VARS);
    }
    if (!v) {
        if (e->n == e->cap) {
            size_t cap = e->cap ? e->cap * 2 : 8;
            struct fm_hc_var *nv = realloc(e->v, cap * sizeof *nv);
            if (!nv) {
                free(b);
                return bad(e, "out of memory");
            }
            e->v = nv;
            e->cap = cap;
        }
        v = &e->v[e->n++];
        v->name = name;
        v->b = NULL;
    }
    free(v->b);
    v->b = b;
    v->n = n;
    return 0;
}

static int eval_expr(struct fm_hc_env *e, const struct fm_hc_expr *x, struct hv *out);

static int eval_arg(struct fm_hc_env *e, const struct fm_hc_arg *a, struct hv *v) {
    memset(v, 0, sizeof *v);
    switch (a->kind) {
    case FMA_BYTES: return dup_bytes(e, v, a->bytes, a->bytes_n);
    case FMA_U64: v->u = a->u64; return 0;
    case FMA_VAR: {
        const struct fm_hc_var *s = var_find(e, a->name);
        if (!s) return bad(e, "unknown variable: %s", a->name);
        return dup_bytes(e, v, s->b, s->n);
    }
    case FMA_META:
        for (unsigned i = 0; i < 8; i++)
            if (!strcmp(a->name, g_meta[i])) {
                unsigned k = (i / 4) * 2 + (i % 4) / 2;
                if (!(e->meta & (1u << k))) break;
                v->u = e->mv[k];
                return 0;
            }
        return bad(e, "unknown metadata: %s", a->name);
    case FMA_EXPR: return eval_expr(e, a->expr, v);
    }
    return bad(e, "empty expr arg");
}

static void hv_free(struct hv *v, size_t n) {
    for (size_t i = 0; i < n; i++) free(v[i].b);
}

static int want_kinds(struct fm_hc_env *e, const struct hv *v, const char *kinds) {
    for (size_t i = 0; kinds[i]; i++) {
        if (kinds[i] == 'b' && !v[i].isb) return bad(e, "expr value is not bytes");
        if (kinds[i] == 'u' && v[i].isb) return bad(e, "expr value is not u64");
    }
    return 0;
}

static int eval_concat(struct fm_hc_env *e, const struct fm_hc_expr *x, struct hv *out) {
    unsigned char *b = NULL;
    size_t n = 0;
    for (size_t i = 0; i < x->n; i++) {
        struct hv a;
        if (eval_arg(e, &x->a[i], &a)) { free(b); return -1; }
        if (!a.isb || n + a.n > FM_HC_MAX) {
            free(a.b);
            free(b);
            return a.isb ? too_long(e) : bad(e, "expr value is not bytes");
        }
        unsigned char *nb = realloc(b, n + a.n ? n + a.n : 1);
        if (!nb) { free(a.b); free(b); return bad(e, "out of memory"); }
        b = nb;
        if (a.n) memcpy(b + n, a.b, a.n);
        n += a.n;
        free(a.b);
    }
    if (!b && !(b = malloc(1))) return bad(e, "out of memory");
    out->isb = 1;
    out->b = b;
    out->n = n;
    return 0;
}

static int pack(struct fm_hc_env *e, const char *name, uint64_t u, size_t w, int be, struct hv *out) {
    unsigned char t[8];
    if (w < 8 && u >> (8 * w)) return bad(e, "%s overflow", name);
    for (size_t i = 0; i < w; i++) t[be ? w - 1 - i : i] = (unsigned char)(u >> (8 * i));
    return dup_bytes(e, out, t, w);
}

static int eval_fixed(struct fm_hc_env *e, const struct fm_hc_expr *x, struct hv *out) {
    const char *name = fm_hc_op_name(x->op), *kinds;
    size_t want;
    switch (x->op) {
    case FMO_SLICE: want = 3; kinds = "buu"; break;
    case FMO_PAD: want = 3; kinds = "bub"; break;
    case FMO_TRUNCATE: want = 2; kinds = "bu"; break;
    case FMO_BE16: case FMO_BE32: case FMO_LE16: case FMO_LE32: case FMO_LE64: want = 1; kinds = "u"; break;
    default: want = 2; kinds = "uu"; break;
    }
    if (x->n != want) {
        if (x->op == FMO_XOR16 || x->op == FMO_XOR32) name = "xor";
        return bad(e, "%s expects %zu arg%s", name, want, want == 1 ? "" : "s");
    }
    struct hv v[3];
    size_t got = 0;
    int rc = -1;
    while (got < want) {
        if (eval_arg(e, &x->a[got], &v[got])) goto out;
        got++;
    }
    if (want_kinds(e, v, kinds)) goto out;
    uint64_t l = v[0].u, r = want > 1 ? v[1].u : 0, res = 0;
    switch (x->op) {
    case FMO_SLICE:
        if (v[1].u > v[0].n || v[2].u > v[0].n - v[1].u) { bad(e, "slice out of bounds"); goto out; }
        rc = dup_bytes(e, out, v[0].b + v[1].u, (size_t)v[2].u);
        goto out;
    case FMO_TRUNCATE:
        if (v[1].u > v[0].n) { bad(e, "truncate out of bounds"); goto out; }
        rc = dup_bytes(e, out, v[0].b, (size_t)v[1].u);
        goto out;
    case FMO_PAD: {
        if (!v[2].n) { bad(e, "pad fill must not be empty"); goto out; }
        if (v[1].u < v[0].n) { bad(e, "pad target shorter than source"); goto out; }
        if (v[1].u > FM_HC_MAX) { too_long(e); goto out; }
        size_t n = (size_t)v[1].u;
        unsigned char *b = malloc(n ? n : 1);
        if (!b) { bad(e, "out of memory"); goto out; }
        if (v[0].n) memcpy(b, v[0].b, v[0].n);
        for (size_t i = v[0].n; i < n; i++) b[i] = v[2].b[(i - v[0].n) % v[2].n];
        out->isb = 1;
        out->b = b;
        out->n = n;
        rc = 0;
        goto out;
    }
    case FMO_BE16: rc = pack(e, name, l, 2, 1, out); goto out;
    case FMO_BE32: rc = pack(e, name, l, 4, 1, out); goto out;
    case FMO_LE16: rc = pack(e, name, l, 2, 0, out); goto out;
    case FMO_LE32: rc = pack(e, name, l, 4, 0, out); goto out;
    case FMO_LE64: rc = pack(e, name, l, 8, 0, out); goto out;
    case FMO_XOR16:
    case FMO_XOR32: {
        uint64_t m = x->op == FMO_XOR16 ? 0xFFFFu : 0xFFFFFFFFu;
        if (l > m || r > m) { bad(e, "%s overflow", name); goto out; }
        res = (l ^ r) & m;
        break;
    }
    case FMO_ADD:
        if (l > UINT64_MAX - r) { bad(e, "add overflow"); goto out; }
        res = l + r;
        break;
    case FMO_SUB:
        if (l < r) { bad(e, "sub underflow"); goto out; }
        res = l - r;
        break;
    case FMO_AND: res = l & r; break;
    case FMO_OR: res = l | r; break;
    case FMO_SHL:
    case FMO_SHR:
        if (r >= 64) { bad(e, "shift out of range"); goto out; }
        if (x->op == FMO_SHL && l > (UINT64_MAX >> r)) { bad(e, "shl overflow"); goto out; }
        res = x->op == FMO_SHL ? l << r : l >> r;
        break;
    default:
        bad(e, "unsupported expr op: %s", name);
        goto out;
    }
    out->isb = 0;
    out->u = res;
    rc = 0;
out:
    hv_free(v, got);
    return rc;
}

static int eval_expr(struct fm_hc_env *e, const struct fm_hc_expr *x, struct hv *out) {
    memset(out, 0, sizeof *out);
    return x->op == FMO_CONCAT ? eval_concat(e, x, out) : eval_fixed(e, x, out);
}

int fm_hc_eval_item(struct fm_hc_env *e, const struct fm_hc_item *it, unsigned char **out, size_t *n) {
    struct hv v = { 1, 0, NULL, 0 };
    *out = NULL;
    *n = 0;
    if (it->rand > 0) {
        if (it->rand > FM_HC_MAX) return too_long(e);
        if (!(v.b = malloc((size_t)it->rand))) return bad(e, "out of memory");
        v.n = (size_t)it->rand;
        fm_rand_bytes(v.b, v.n, it->rmin, it->rmax);
    } else if (it->packet_n) {
        if (dup_bytes(e, &v, it->packet, it->packet_n)) return -1;
    } else if (it->reuse) {
        const struct fm_hc_var *s = var_find(e, it->reuse);
        if (!s) return bad(e, "unknown variable: %s", it->reuse);
        if (dup_bytes(e, &v, s->b, s->n)) return -1;
    } else if (it->expr) {
        if (eval_expr(e, it->expr, &v)) return -1;
        if (!v.isb) return bad(e, "expr value is not bytes");
    } else if (dup_bytes(e, &v, NULL, 0)) {
        return -1;
    }
    if (it->capture) {
        unsigned char *c = malloc(v.n ? v.n : 1);
        if (!c) { free(v.b); return bad(e, "out of memory"); }
        if (v.n) memcpy(c, v.b, v.n);
        if (var_set(e, it->capture, c, v.n)) { free(v.b); return -1; }
    }
    *out = v.b;
    *n = v.n;
    return 0;
}

static int env_size(const void *ctx, const char *name, uint64_t *sz) {
    const struct fm_hc_env *e = ctx;
    for (size_t i = 0; i < e->n; i++)
        if (!strcmp(e->v[i].name, name)) {
            *sz = e->v[i].n;
            return 0;
        }
    return -1;
}

static int measure_item(struct fm_hc_env *e, const struct fm_hc_item *it, uint64_t *sz) {
    if (fm_hc_measure_item(it, env_size, e, sz, e->why, sizeof e->why)) return -1;
    return *sz > FM_HC_MAX ? too_long(e) : 0;
}

static int put_seq(struct fm_layer *me, struct fm_hc_env *e, const struct fm_hc_seq *q) {
    unsigned char *b = NULL;
    size_t n = 0;
    int rc = 0;
    for (size_t i = 0; i < q->n && !rc; i++) {
        const struct fm_hc_item *it = &q->it[i];
        if (it->delay.to > 0) {
            int64_t ms = fm_rand_between(it->delay.from, it->delay.to);
            if (n) {
                rc = fm_lower_write(me, b, n, ms);
                n = 0;
                if (rc) break;
            } else {
                rc = fm_lower_sleep(me, ms);
                if (rc) break;
            }
        }
        unsigned char *v;
        size_t vn;
        if (fm_hc_eval_item(e, it, &v, &vn)) { rc = TR_EMASK; break; }
        if (n + vn > FM_HC_MAX) {
            free(v);
            too_long(e);
            rc = TR_EMASK;
            break;
        }
        unsigned char *nb = realloc(b, n + vn ? n + vn : 1);
        if (!nb) {
            free(v);
            bad(e, "out of memory");
            rc = TR_EMASK;
            break;
        }
        b = nb;
        if (vn) memcpy(b + n, v, vn);
        n += vn;
        free(v);
    }
    if (!rc && n) rc = fm_lower_write(me, b, n, 0);
    if (rc == TR_ECONNTIMEOUT) bad(e, "a delay would pass the deadline of the exchange");
    free(b);
    return rc;
}

static int get_seq(struct fm_layer *me, int fd, struct fm_hc_env *e, const struct fm_hc_seq *q) {
    for (size_t i = 0; i < q->n; i++) {
        const struct fm_hc_item *it = &q->it[i];
        uint64_t sz;
        if (measure_item(e, it, &sz)) return TR_EMASK;
        size_t n = (size_t)sz;
        unsigned char *b = malloc(n ? n : 1);
        if (!b) {
            bad(e, "out of memory");
            return TR_EMASK;
        }
        int rc = n ? fm_lower_read_full(me, fd, b, n) : 0, same = 1;
        if (rc) {
            if (rc == TR_ECONNTIMEOUT) bad(e, "no answer from the server");
            free(b);
            return rc;
        }
        if (it->rand <= 0 && it->packet_n) {
            same = !memcmp(b, it->packet, n);
        } else if (it->rand <= 0 && it->reuse) {
            const struct fm_hc_var *s = var_find(e, it->reuse);
            same = s && s->n == n && !memcmp(s->b, b, n);
        } else if (it->rand <= 0 && it->expr) {
            struct hv v;
            if (eval_expr(e, it->expr, &v)) {
                free(b);
                return TR_EMASK;
            }
            if (!v.isb) {
                free(v.b);
                free(b);
                bad(e, "expr value is not bytes");
                return TR_EMASK;
            }
            same = v.n == n && !memcmp(v.b, b, n);
            free(v.b);
        }
        if (!same) {
            free(b);
            bad(e, "the server's answer did not match");
            return TR_EMASK;
        }
        if (it->capture) {
            if (var_set(e, it->capture, b, n)) return TR_EMASK;
        } else {
            free(b);
        }
    }
    return 0;
}

static int hc_hs(struct fm_layer *me, int fd) {
    struct hc_st *s = me->st;
    const struct fm_hc *h = &me->m->u.hc;
    struct fm_hc_env e;
    fm_hc_env_init(&e, fd);
    size_t j = 0;
    int rc = 0;
    for (size_t i = 0; i < h->n_cl && !rc; i++) {
        rc = put_seq(me, &e, &h->cl[i]);
        if (!rc && j < h->n_sv) rc = get_seq(me, fd, &e, &h->sv[j++]);
    }
    while (!rc && j < h->n_sv) rc = get_seq(me, fd, &e, &h->sv[j++]);
    s->why[0] = 0;
    if (rc && e.why[0]) snprintf(s->why, sizeof s->why, "header-custom: %s", e.why);
    fm_hc_env_free(&e);
    return rc;
}

static const char *hc_why(const struct fm_layer *me) {
    const struct hc_st *s = me->st;
    return s->why[0] ? s->why : NULL;
}

const struct fm_ops fm_hc_ops = { .splice = 1, .st_size = sizeof(struct hc_st), .hs = hc_hs, .why = hc_why };
