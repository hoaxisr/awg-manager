#include "fmsd.h"
#include "fmlink.h"
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include "osrand.h"
#include "transport.h"

static const uint8_t perm4[24][4] = {
    {0, 1, 2, 3}, {0, 1, 3, 2}, {0, 2, 1, 3}, {0, 2, 3, 1}, {0, 3, 1, 2}, {0, 3, 2, 1},
    {1, 0, 2, 3}, {1, 0, 3, 2}, {1, 2, 0, 3}, {1, 2, 3, 0}, {1, 3, 0, 2}, {1, 3, 2, 0},
    {2, 0, 1, 3}, {2, 0, 3, 1}, {2, 1, 0, 3}, {2, 1, 3, 0}, {2, 3, 0, 1}, {2, 3, 1, 0},
    {3, 0, 1, 2}, {3, 0, 2, 1}, {3, 1, 0, 2}, {3, 1, 2, 0}, {3, 2, 0, 1}, {3, 2, 1, 0},
};

static uint32_t ld32(const unsigned char *p) {
    return (uint32_t)p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 | (uint32_t)p[3] << 24;
}

static void st32(unsigned char *p, uint32_t v) {
    p[0] = (unsigned char)v;
    p[1] = (unsigned char)(v >> 8);
    p[2] = (unsigned char)(v >> 16);
    p[3] = (unsigned char)(v >> 24);
}

#define ROTL(v, n) ((v) << (n) | (v) >> (32 - (n)))
#define QR(a, b, c, d)                                                                                       \
    a += b, d = ROTL(d ^ a, 16), c += d, b = ROTL(b ^ c, 12), a += b, d = ROTL(d ^ a, 8), c += d,             \
    b = ROTL(b ^ c, 7)

static void chacha8(const uint32_t in[16], unsigned char out[64]) {
    uint32_t x[16];
    memcpy(x, in, sizeof x);
    for (int i = 0; i < 4; i++) {
        QR(x[0], x[4], x[8], x[12]);
        QR(x[1], x[5], x[9], x[13]);
        QR(x[2], x[6], x[10], x[14]);
        QR(x[3], x[7], x[11], x[15]);
        QR(x[0], x[5], x[10], x[15]);
        QR(x[1], x[6], x[11], x[12]);
        QR(x[2], x[7], x[8], x[13]);
        QR(x[3], x[4], x[9], x[14]);
    }
    for (int i = 0; i < 16; i++) st32(out + 4 * i, x[i] + in[i]);
}

static void chacha8_at(const unsigned char key[32], uint32_t ctr, unsigned char out[64]) {
    uint32_t st[16] = {0x61707865, 0x3320646e, 0x79622d32, 0x6b206574};
    for (int i = 0; i < 8; i++) st[4 + i] = ld32(key + 4 * i);
    st[12] = ctr;
    chacha8(st, out);
}

static void refill(struct sd_rng *r) {
    if (r->ctr > UINT32_MAX - 4) {
        unsigned char k[64];
        chacha8_at(r->key, r->ctr, k);
        memcpy(r->key, k, sizeof r->key);
        memset(k, 0, sizeof k);
        r->ctr = 0;
    }
    for (unsigned i = 0; i < sizeof r->buf / 64; i++) chacha8_at(r->key, r->ctr++, r->buf + 64 * i);
    r->pos = 0;
}

static uint8_t byte(struct sd_rng *r) {
    if (r->pos == sizeof r->buf) refill(r);
    return r->buf[r->pos++];
}

int sd_rng_init(struct sd_rng *r) {
    r->ctr = 0;
    if (os_rand_seed(r->key, sizeof r->key)) return -1;
    refill(r);
    return 0;
}

unsigned sd_rng_below(struct sd_rng *r, unsigned n) {
    if (n <= 1) return 0;
    unsigned m = byte(r) * n;
    if ((m & 0xff) < n) {
        unsigned t = (256 - n) % n;
        while ((m & 0xff) < t) m = byte(r) * n;
    }
    return m >> 8;
}

int sd_enc_init(struct sd_enc *e, const struct sd_set *s, uint32_t pmin, uint32_t pmax) {
    e->s = s;
    e->ti = 0;
    e->chance = 0;
    if (sd_rng_init(&e->rng)) return -1;
    if (pmin > 100) pmin = 100;
    if (pmax > 100) pmax = 100;
    if (pmax < pmin) pmax = pmin;
    e->chance = pmin + (pmax == pmin ? 0 : sd_rng_below(&e->rng, pmax - pmin + 1));
    return 0;
}

size_t sd_enc_bound(const struct sd_enc *e, size_t n) {
    return e->chance ? 9 * n + 1 : 4 * n;
}

static int pad(struct sd_enc *e, const struct sd_layout *l, unsigned char **w) {
    if (!e->chance || (e->chance < 100 && sd_rng_below(&e->rng, 100) >= e->chance)) return 0;
    *(*w)++ = l->pool[sd_rng_below(&e->rng, l->pool_n)];
    return 1;
}

size_t sd_encode(struct sd_enc *e, const unsigned char *in, size_t n, unsigned char *out) {
    const struct sd_base *b = sd_base();
    unsigned char *w = out;
    if (!n || !b) return 0;
    for (size_t i = 0; i < n; i++) {
        const struct sd_layout *l = &e->s->lay[e->ti];
        unsigned o = e->s->order[in[i]];
        pad(e, l, &w);
        const uint8_t *g = b->g[b->off[o] + sd_rng_below(&e->rng, b->off[o + 1] - b->off[o])];
        const uint8_t *p = perm4[sd_rng_below(&e->rng, 24)];
        for (int k = 0; k < 4; k++) {
            pad(e, l, &w);
            *w++ = l->hint[g[p[k]]];
        }
        e->ti = e->ti + 1 == e->s->n ? 0 : e->ti + 1;
    }
    pad(e, &e->s->lay[e->ti], &w);
    return (size_t)(w - out);
}

size_t sd_unpack(struct sd_dec *d, unsigned char *buf, size_t n) {
    size_t w = 0;
    for (size_t i = 0; i < n; i++) {
        uint8_t c = d->s->lay[d->gi].cls[buf[i]];
        if (c == SD_MARK) {
            d->bits = 0;
            d->nbits = 0;
            continue;
        }
        if (c == SD_PAD) continue;
        d->gi = d->gi + 1 == d->s->n ? 0 : d->gi + 1;
        d->bits = d->bits << 6 | c;
        d->nbits += 6;
        if (d->nbits >= 8) {
            d->nbits -= 8;
            buf[w++] = (uint8_t)(d->bits >> d->nbits);
            d->bits &= (1u << d->nbits) - 1;
        }
    }
    return w;
}

struct sd_st { struct sd_enc enc; struct sd_dec dec; int oom; };
struct sd_buf { unsigned char *b; size_t cap; };

static pthread_once_t buf_once = PTHREAD_ONCE_INIT;
static pthread_key_t buf_key;
static int buf_key_err;
static __thread struct sd_buf g_buf;

static void buf_free(void *v) {
    struct sd_buf *b = v;
    free(b->b);
    b->b = NULL;
    b->cap = 0;
}

static void buf_init(void) {
    buf_key_err = pthread_key_create(&buf_key, buf_free);
}

static unsigned char *scratch(size_t n) {
    if (n <= g_buf.cap) return g_buf.b;
    pthread_once(&buf_once, buf_init);
    if (buf_key_err || (!pthread_getspecific(buf_key) && pthread_setspecific(buf_key, &g_buf))) return NULL;
    size_t c = g_buf.cap * 2 > n ? g_buf.cap * 2 : n;
    unsigned char *b = realloc(g_buf.b, c);
    if (!b) return NULL;
    g_buf.b = b;
    g_buf.cap = c;
    return b;
}

static int sd_init(void *st, const struct fm_mask *m) {
    struct sd_st *s = st;
    const struct sd_set *t = sd_tables(&m->u.sd);
    if (!t || sd_enc_init(&s->enc, t, m->u.sd.pmin, m->u.sd.pmax)) return -1;
    s->dec = (struct sd_dec){ t, 0, 0, 0 };
    s->oom = 0;
    return 0;
}

static int sd_wr(struct fm_layer *me, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg) {
    struct sd_st *s = me->st;
    if (!n) return 0;
    unsigned char *b = scratch(sd_enc_bound(&s->enc, n));
    if (!b) {
        s->oom = 1;
        return TR_EIO;
    }
    return emit(arg, b, sd_encode(&s->enc, d, n, b), 0);
}

static long sd_rd(struct fm_layer *me, unsigned char *d, size_t n, size_t cap) {
    (void)cap;
    return (long)sd_unpack(&((struct sd_st *)me->st)->dec, d, n);
}

static const char *sd_why(const struct fm_layer *me) {
    return ((const struct sd_st *)me->st)->oom ? "sudoku: out of memory" : NULL;
}

static unsigned sd_grow(const struct fm_mask *m) {
    return m->u.sd.pmax ? 9 : 4;
}

const struct fm_ops fm_sd_ops = { .splice = 0, .st_size = sizeof(struct sd_st), .init = sd_init, .wr = sd_wr,
                                  .rd = sd_rd, .why = sd_why, .grow = sd_grow };
