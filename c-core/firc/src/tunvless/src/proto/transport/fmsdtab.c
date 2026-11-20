#include "fmsd.h"
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include "gorand.h"
#include "scrypto.h"

enum { SD_POS = 1820 };

struct sd_entry { struct sd_entry *next; struct sd_set *s; size_t kn; unsigned char key[]; };

static pthread_once_t base_once = PTHREAD_ONCE_INIT;
static const struct sd_base *base_ok;
static struct sd_base base;
static pthread_mutex_t cache_mu = PTHREAD_MUTEX_INITIALIZER;
static struct sd_entry *cache;

static int fits(const uint8_t *g, int idx, uint8_t num) {
    int row = idx / 4, col = idx % 4, br = row / 2 * 2, bc = col / 2 * 2;
    for (int i = 0; i < 4; i++)
        if (g[row * 4 + i] == num || g[i * 4 + col] == num) return 0;
    for (int r = 0; r < 2; r++)
        for (int c = 0; c < 2; c++)
            if (g[(br + r) * 4 + bc + c] == num) return 0;
    return 1;
}

static void walk(uint8_t (*out)[16], uint8_t *g, int idx, int *n) {
    if (idx == 16) {
        if (*n < SD_GRIDS) memcpy(out[*n], g, 16);
        (*n)++;
        return;
    }
    for (uint8_t num = 1; num <= 4; num++) {
        if (!fits(g, idx, num)) continue;
        g[idx] = num;
        walk(out, g, idx + 1, n);
        g[idx] = 0;
    }
}

static void swap8(uint8_t *a, uint8_t *b) {
    if (*a > *b) {
        uint8_t t = *a;
        *a = *b;
        *b = t;
    }
}

static uint32_t clue_key(const uint8_t *grid, const uint8_t *ps, uint8_t out[4]) {
    for (int k = 0; k < 4; k++) out[k] = (uint8_t)(((grid[ps[k]] - 1) << 4) | (ps[k] & 0x0f));
    swap8(&out[0], &out[1]);
    swap8(&out[2], &out[3]);
    swap8(&out[0], &out[2]);
    swap8(&out[1], &out[3]);
    swap8(&out[1], &out[2]);
    return (uint32_t)out[0] << 24 | (uint32_t)out[1] << 16 | (uint32_t)out[2] << 8 | out[3];
}

static int cmp64(const void *a, const void *b) {
    uint64_t x = *(const uint64_t *)a, y = *(const uint64_t *)b;
    return (x > y) - (x < y);
}

static void base_build(void) {
    static uint8_t grids[SD_GRIDS][16];
    uint8_t g[16] = {0}, (*pos)[4] = malloc(SD_POS * sizeof *pos), (*uniq)[(SD_GRIDS + 7) / 8] = calloc(SD_POS, sizeof *uniq);
    uint64_t keys[SD_GRIDS];
    uint32_t *off = calloc(SD_GRIDS + 1, sizeof *off);
    uint8_t (*sets)[4] = NULL;
    int n = 0, p = 0;
    walk(grids, g, 0, &n);
    if (n != SD_GRIDS || !pos || !uniq || !off) goto out;
    for (int a = 0; a < 13; a++)
        for (int b = a + 1; b < 14; b++)
            for (int c = b + 1; c < 15; c++)
                for (int d = c + 1; d < 16; d++, p++) {
                    pos[p][0] = (uint8_t)a;
                    pos[p][1] = (uint8_t)b;
                    pos[p][2] = (uint8_t)c;
                    pos[p][3] = (uint8_t)d;
                }
    if (p != SD_POS) goto out;
    for (int i = 0; i < SD_POS; i++) {
        uint8_t s[4];
        for (int gi = 0; gi < SD_GRIDS; gi++) keys[gi] = (uint64_t)clue_key(grids[gi], pos[i], s) << 16 | (unsigned)gi;
        qsort(keys, SD_GRIDS, sizeof keys[0], cmp64);
        for (int k = 0; k < SD_GRIDS; k++) {
            uint64_t key = keys[k] >> 16;
            unsigned gi = (unsigned)(keys[k] & 0xffff);
            if ((k && keys[k - 1] >> 16 == key) || (k + 1 < SD_GRIDS && keys[k + 1] >> 16 == key)) continue;
            uniq[i][gi / 8] |= (uint8_t)(1u << (gi % 8));
            off[gi + 1]++;
        }
    }
    for (int gi = 0; gi < SD_GRIDS; gi++) {
        if (!off[gi + 1]) goto out;
        off[gi + 1] += off[gi];
    }
    if (!(sets = malloc(off[SD_GRIDS] * sizeof *sets))) goto out;
    for (int gi = 0; gi < SD_GRIDS; gi++) {
        uint32_t w = off[gi];
        for (int i = 0; i < SD_POS; i++)
            if (uniq[i][gi / 8] & (1u << (gi % 8))) clue_key(grids[gi], pos[i], sets[w++]);
    }
    base.off = off;
    base.g = (const uint8_t (*)[4])sets;
    base_ok = &base;
    off = NULL;
out:
    free(pos);
    free(uniq);
    free(off);
}

const struct sd_base *sd_base(void) {
    pthread_once(&base_once, base_build);
    return base_ok;
}

static int ones(unsigned b) {
    int n = 0;
    for (; b; b &= b - 1) n++;
    return n;
}

static int custom(struct sd_layout *l, const char *pt) {
    uint8_t xb[2], pb[2], vb[4], xm = 0, seen[256] = {0};
    int nx = 0, np = 0, nv = 0;
    if (strlen(pt) != 8) return -1;
    for (int i = 0; i < 8; i++) {
        uint8_t bit = (uint8_t)(7 - i);
        if (pt[i] == 'x' && nx < 2) xb[nx++] = bit;
        else if (pt[i] == 'p' && np < 2) pb[np++] = bit;
        else if (pt[i] == 'v' && nv < 4) vb[nv++] = bit;
        else return -1;
    }
    xm = (uint8_t)(1u << xb[0] | 1u << xb[1]);
    for (int drop = -1; drop < 2; drop++)
        for (unsigned g = 0; g < 64; g++) {
            unsigned o = drop < 0 ? xm : xm & ~(1u << xb[drop]);
            if (g & 0x20) o |= 1u << pb[0];
            if (g & 0x10) o |= 1u << pb[1];
            for (int i = 0; i < 4; i++)
                if ((g >> (3 - i)) & 1) o |= 1u << vb[i];
            if (drop < 0) l->hint[g] = (uint8_t)o;
            else if (ones(o) >= 5) seen[o] = 1;
        }
    for (unsigned b = 0; b < 256; b++)
        if (seen[b]) {
            if (l->pool_n == sizeof l->pool) return -1;
            l->pool[l->pool_n++] = (uint8_t)b;
        }
    if (!l->pool_n) return -1;
    l->marker = l->pool[0];
    return 0;
}

int sd_layout_make(struct sd_layout *l, int ascii, const char *pattern) {
    memset(l, 0, sizeof *l);
    if (ascii) {
        for (int i = 0; i < 32; i++) l->pool[i] = (uint8_t)(0x20 + i);
        l->pool_n = 32;
        l->marker = 0x3f;
        for (unsigned g = 0; g < 64; g++) l->hint[g] = g == 0x3f ? '\n' : (uint8_t)(0x40 | g);
    } else if (!pattern || !*pattern) {
        for (int i = 0; i < 8; i++) {
            l->pool[2 * i] = (uint8_t)(0x80 + i);
            l->pool[2 * i + 1] = (uint8_t)(0x10 + i);
        }
        l->pool_n = 16;
        l->marker = 0x80;
        for (unsigned g = 0; g < 64; g++) l->hint[g] = (uint8_t)((g & 0x30) << 1 | (g & 0x0f));
    } else if (custom(l, pattern)) {
        return -1;
    }
    memset(l->cls, SD_PAD, sizeof l->cls);
    l->cls[l->marker] = SD_MARK;
    if (ascii)
        for (unsigned b = 0x40; b < 256; b++)
            if (b & 0x40) l->cls[b] = (uint8_t)(b & 0x3f);
    for (unsigned g = 0; g < 64; g++) l->cls[l->hint[g]] = (uint8_t)g;
    return 0;
}

int sd_order(const unsigned char *pw, size_t pw_n, uint16_t order[SD_GRIDS], int64_t *seed) {
    unsigned char h[32];
    uint64_t s = 0;
    struct go_rand *r;
    if (sc_hash(SC_SHA256, pw, pw_n, h)) return -1;
    for (int i = 0; i < 8; i++) s = s << 8 | h[i];
    if (!(r = malloc(sizeof *r))) return -1;
    *seed = (int64_t)s;
    go_rand_seed(r, *seed);
    for (int i = 0; i < SD_GRIDS; i++) order[i] = (uint16_t)i;
    go_rand_shuffle_u16(r, order, SD_GRIDS);
    free(r);
    return 0;
}

static unsigned char *set_key(const struct fm_sd *c, size_t *kn) {
    size_t np = c->ascii ? 1 : c->n_pat, n = 1 + sizeof c->pw_n + c->pw_n + np * 9;
    unsigned char *k = malloc(n), *w = k;
    if (!k) return NULL;
    *w++ = c->ascii ? 1 : 0;
    memcpy(w, &c->pw_n, sizeof c->pw_n);
    w += sizeof c->pw_n;
    if (c->pw_n) memcpy(w, c->pw, c->pw_n);
    w += c->pw_n;
    for (size_t i = 0; i < np; i++) {
        const char *p = c->ascii ? "" : c->pat[i];
        size_t l = strlen(p);
        memcpy(w, p, l);
        w += l;
        *w++ = '\0';
    }
    *kn = (size_t)(w - k);
    return k;
}

static struct sd_set *set_build(const struct fm_sd *c) {
    size_t n = c->ascii || !c->n_pat ? 1 : c->n_pat;
    struct sd_set *s = malloc(sizeof *s);
    struct sd_layout *lay = calloc(n, sizeof *lay);
    uint16_t o[SD_GRIDS];
    int64_t seed;
    if (!s || !lay || sd_order(c->pw, c->pw_n, o, &seed)) goto fail;
    for (size_t i = 0; i < n; i++)
        if (sd_layout_make(&lay[i], c->ascii, c->ascii || !c->n_pat ? "" : c->pat[i])) goto fail;
    s->n = n;
    s->lay = lay;
    memcpy(s->order, o, sizeof s->order);
    return s;
fail:
    free(s);
    free(lay);
    return NULL;
}

const struct sd_set *sd_tables(const struct fm_sd *c) {
    struct sd_entry *e;
    struct sd_set *s = NULL;
    size_t kn;
    unsigned char *k;
    if (!sd_base() || !(k = set_key(c, &kn))) return NULL;
    pthread_mutex_lock(&cache_mu);
    for (e = cache; e; e = e->next)
        if (e->kn == kn && !memcmp(e->key, k, kn)) break;
    if (e) s = e->s;
    else if ((e = malloc(sizeof *e + kn)) && (s = set_build(c))) {
        e->s = s;
        e->kn = kn;
        memcpy(e->key, k, kn);
        e->next = cache;
        cache = e;
    } else {
        free(e);
    }
    pthread_mutex_unlock(&cache_mu);
    free(k);
    return s;
}
