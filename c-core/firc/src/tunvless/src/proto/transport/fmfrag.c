#include <stdlib.h>
#include <string.h>

#include "fmlink.h"
#include "transport.h"

struct frag_st { uint64_t count; int oom; };

static struct fm_rng pick(const struct fm_rng *r, size_t n, size_t i) {
    struct fm_rng z = { 0, 0 };
    if (!n) return z;
    return r[i < n ? i : n - 1];
}

static size_t cut(const struct fm_frag *f, uint64_t k, int64_t split, size_t from, size_t n) {
    struct fm_rng r = pick(f->len, f->n_len, (size_t)(k < SIZE_MAX ? k : SIZE_MAX));
    int64_t v = fm_rand_between(r.from, r.to);
    if (v < 0 || (uint64_t)v > n - from || (split > 0 && k + 1 >= (uint64_t)split)) return n;
    return from + (size_t)v;
}

static int64_t gap(const struct fm_frag *f, uint64_t k) {
    struct fm_rng r = pick(f->delay, f->n_delay, (size_t)(k < SIZE_MAX ? k : SIZE_MAX));
    if (r.to <= 0) return 0;
    int64_t v = fm_rand_between(r.from, r.to);
    return v > 0 ? v : 0;
}

static int hello(struct fm_layer *me, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg) {
    const struct fm_frag *f = &me->m->u.frag;
    struct frag_st *s = me->st;
    size_t rl = 5 + ((size_t)d[3] << 8 | d[4]);
    if (n < rl) return emit(arg, d, n, 0);
    const unsigned char *body = d + 5;
    size_t bn = rl - 5;
    int merge = f->n_delay == 1 && f->delay[0].to == 0;
    size_t cap = merge ? bn + 5 * (bn + f->n_len) : 5 + bn;
    unsigned char *b = malloc(cap);
    if (!b) {
        s->oom = 1;
        return TR_EIO;
    }
    int64_t split = fm_rand_between(f->split.from, f->split.to);
    size_t from = 0, len = 0;
    int rc = 0;
    for (uint64_t k = 0; !rc; k++) {
        size_t to = cut(f, k, split, from, bn), l = to - from;
        unsigned char *p = b + (merge ? len : 0);
        memcpy(p, d, 3);
        p[3] = (unsigned char)(l >> 8);
        p[4] = (unsigned char)l;
        memcpy(p + 5, body + from, l);
        if (merge) len += 5 + l;
        else rc = emit(arg, p, 5 + l, gap(f, k));
        from = to;
        if (from == bn) break;
    }
    if (!rc && merge) rc = emit(arg, b, len, 0);
    free(b);
    if (!rc && n > rl) rc = emit(arg, d + rl, n - rl, 0);
    return rc;
}

static int frag_wr(struct fm_layer *me, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg) {
    const struct fm_frag *f = &me->m->u.frag;
    struct frag_st *s = me->st;
    uint64_t c = ++s->count;
    if (f->pk_from == 0 && f->pk_to == 1) {
        if (c != 1 || n <= 5 || d[0] != 22) return emit(arg, d, n, 0);
        return hello(me, d, n, emit, arg);
    }
    if (f->pk_from != 0 && (c < (uint64_t)f->pk_from || c > (uint64_t)f->pk_to)) return emit(arg, d, n, 0);
    int64_t split = fm_rand_between(f->split.from, f->split.to);
    size_t from = 0;
    for (uint64_t k = 0;; k++) {
        size_t to = cut(f, k, split, from, n);
        int rc = emit(arg, d + from, to - from, gap(f, k));
        if (rc) return rc;
        from = to;
        if (from >= n) return 0;
    }
}

static const char *frag_why(const struct fm_layer *me) {
    const struct frag_st *s = me->st;
    return s->oom ? "fragment: out of memory" : NULL;
}

const struct fm_ops fm_frag_ops = { .splice = 1, .st_size = sizeof(struct frag_st), .wr = frag_wr, .why = frag_why };
