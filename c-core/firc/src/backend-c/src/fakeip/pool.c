#include "firc/fakeip.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/log.h"

#define MAP_BUCKETS 4096u

#define FAM_V4 FIRC_FAM_V4
#define FAM_V6 FIRC_FAM_V6
#define N_FAM 2u

/* The quarantine is derived from the clamp, never configured. */
#define QUARANTINE_MULTIPLE 12
#define QUARANTINE_FLOOR 3600

/* Case-insensitive ASCII fold (RFC 4343), so 0x20 randomisation can't mint a fresh mapping per query. */
static unsigned char fold(unsigned char c) {
    return (c >= 'A' && c <= 'Z') ? (unsigned char)(c - 'A' + 'a') : c;
}

static uint64_t fnv1a(const char *s) {
    uint64_t h = UINT64_C(1469598103934665603);
    for (const unsigned char *p = (const unsigned char *)s; *p != 0; p++) {
        h ^= fold(*p);
        h *= UINT64_C(1099511628211);
    }
    return h;
}

static bool name_eq(const char *a, const char *b) {
    const unsigned char *x = (const unsigned char *)a;
    const unsigned char *y = (const unsigned char *)b;
    while (*x != 0 && fold(*x) == fold(*y)) {
        x++;
        y++;
    }
    return fold(*x) == fold(*y);
}

typedef struct group group_t;

typedef struct mapping {
    char *qname;
    firc_ip_t addr[N_FAM];
    firc_ip_t real[N_FAM];    /* what each fake address stands for */
    bool has_real[N_FAM];     /* "not yet learned" vs "none": distinct states */
    uint64_t pair_gen[N_FAM]; /* f->gen when this family's (fake, real) last changed */
    int64_t last_seen;
    group_t *owner;
    struct mapping *next;
    struct mapping *rnext[N_FAM];
} mapping_t;

/* Parked until free_at; the per-group FIFO is ordered by free_at (a backward clock step can break that; harmless). */
typedef struct quarantined {
    firc_ip_t addr;
    firc_ip_t real; /* what addr stood for when parked; its rule stays until the window is out */
    bool has_real;
    int64_t free_at;
    char *qname; /* NULL for a whole chunk */
    struct quarantined *next;
    struct quarantined *prev;
    struct quarantined *hnext;
    struct quarantined *hprev;
    struct group_fam *list; /* the FIFO this sits in, for the hash lookup */
} quarantined_t;

typedef struct group_fam group_fam_t;

typedef struct chunk_node {
    firc_ip_t base;
    uint32_t idx;      /* index in the family, for lookups without arithmetic */
    uint32_t n_mapped; /* names whose address of this family lies here */
    uint32_t n_parked; /* parked addresses that lie here */
    uint64_t *free_bits; /* offsets below the mark whose quarantine ran out, v4 only */
    uint32_t n_free;
    struct chunk_node *next;
} chunk_node_t;

struct group_fam {
    struct group *group; /* back-pointer, for reporting a parked address's owner */
    firc_ip_t chunk_base;
    uint64_t next_off;
    uint32_t next_off_chunk_idx; /* index of chunk_base; only the restore reads it */
    bool has_chunk; /* explicit flag: in a /64 chunk every uint64 offset is legitimate */
    chunk_node_t *chunks;
    quarantined_t *q_head; /* oldest expiry first — see quarantined_t */
    quarantined_t *q_tail;
};

struct group {
    char *id;
    group_fam_t fam[N_FAM];
    bool mark_restored[N_FAM]; /* load-time: a `mark` line named this family's chunk */
    struct group *next;
};

typedef struct family {
    firc_ip_t base;
    uint8_t pool_cidr;
    uint8_t chunk_cidr;
    uint32_t n_chunks;    /* 2^(chunk_cidr - pool_cidr), bounded under 32 bits for mipsel */
    uint32_t next_chunk;  /* index of the first not-yet-assigned chunk */
    uint64_t chunk_addrs; /* addresses per chunk, saturated at UINT64_MAX for >= 64 host bits */
    firc_ip_t blackhole;
    quarantined_t *chunk_quarantine;
} family_t;

struct firc_fakeip {
    family_t fam[N_FAM];
    mapping_t *buckets[MAP_BUCKETS];
    mapping_t *by_addr[N_FAM][MAP_BUCKETS];
    group_t *groups;
    quarantined_t *parked[MAP_BUCKETS]; /* by qname, across all groups */
    size_t n_names;
    size_t max_names;
    int64_t idle_secs;
    int64_t quarantine_secs;
    uint64_t gen; /* advances on every change a completed pass must commit */
    uint64_t committed_gen;
    bool dirty; /* changed since the last save */
    firc_fakeip_chunk_hook on_chunk;
    void *on_chunk_ud;
    bool chunk_taken; /* by the issue in progress: the hook is owed */
};

static bool addr_split(const family_t *fam, const firc_ip_t *addr, uint32_t *idx, uint64_t *off);
static bool chunk_free_take(family_t *fam, group_fam_t *gf, firc_ip_t *out);
static void chunk_count(firc_fakeip_t *f, group_fam_t *gf, const firc_ip_t *addr, int mapped, int parked);

static bool ip_equal(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

static size_t addr_bucket(const firc_ip_t *a) {
    uint64_t h = UINT64_C(1469598103934665603);
    for (uint8_t i = 0; i < a->len; i++) {
        h ^= a->b[i];
        h *= UINT64_C(1099511628211);
    }
    return (size_t)(h % MAP_BUCKETS);
}

static void rindex_add(firc_fakeip_t *f, mapping_t *m) {
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        mapping_t **head = &f->by_addr[fi][addr_bucket(&m->addr[fi])];
        m->rnext[fi] = *head;
        *head = m;
    }
}

static void rindex_del(firc_fakeip_t *f, mapping_t *m) {
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        mapping_t **pp = &f->by_addr[fi][addr_bucket(&m->addr[fi])];
        while (*pp != NULL && *pp != m) { pp = &(*pp)->rnext[fi]; }
        if (*pp == m) { *pp = m->rnext[fi]; }
    }
}

/* v4: some client stacks refuse a last octet of 0 or 255. v6: offset 0 is the /64's Subnet-Router anycast address. */
static bool usable(unsigned fam, const firc_ip_t *addr, uint64_t off) {
    if (fam == FAM_V4) {
        uint8_t last = addr->b[addr->len - 1];
        return last != 0 && last != 255;
    }
    return off != 0;
}

static uint8_t host_bits_of(const family_t *f) {
    return (uint8_t)((uint16_t)(f->base.len * 8u) - f->chunk_cidr);
}

static bool chunk_base_at(const family_t *f, uint32_t index, firc_ip_t *out) {
    *out = f->base;
    if (index == 0) { return true; }
    return firc_ip_add_shifted(out, index, host_bits_of(f));
}

firc_err_t firc_fakeip_check_windows(int64_t idle_secs, int64_t clamp_secs) {
    if (idle_secs <= 0 || clamp_secs <= 0) { return FIRC_ERR_INVAL; }
    /* Window * 12 is later added to a timestamp; a clamp near INT64_MAX would overflow it. */
    if (clamp_secs > FIRC_FAKEIP_MAX_WINDOW_SECS || idle_secs > FIRC_FAKEIP_MAX_WINDOW_SECS) {
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

firc_err_t firc_fakeip_check_geometry(const firc_ip_t *base, uint8_t pool_cidr,
                                      uint8_t chunk_cidr, uint8_t addr_len) {
    if (addr_len != 4 && addr_len != 16) { return FIRC_ERR_INVAL; }
    if (base != NULL && base->len != addr_len) { return FIRC_ERR_INVAL; }
    uint16_t bits = (uint16_t)(addr_len * 8u);

    /* A chunk under four addresses is degenerate: one rule per address beats nothing. */
    if (pool_cidr == 0 || chunk_cidr <= pool_cidr ||
        (uint16_t)(chunk_cidr + 2) > bits) {
        return FIRC_ERR_INVAL;
    }
    /* Must fit strictly under 32 bits (mipsel); saturating would hide a chunk. */
    if ((unsigned)(chunk_cidr - pool_cidr) >= 32) { return FIRC_ERR_INVAL; }

    /* base NULL (generated) is aligned by construction. */
    if (base != NULL && !firc_ip_aligned(base, pool_cidr)) { return FIRC_ERR_INVAL; }
    return FIRC_OK;
}

static firc_err_t family_init(family_t *f, const firc_fakeip_family_cfg_t *cfg, uint8_t want_len) {
    firc_err_t err = firc_fakeip_check_geometry(&cfg->base, cfg->pool_cidr,
                                                cfg->chunk_cidr, want_len);
    if (err != FIRC_OK) { return err; }

    f->base = cfg->base;
    f->pool_cidr = cfg->pool_cidr;
    f->chunk_cidr = cfg->chunk_cidr;
    f->n_chunks = (uint32_t)1 << (unsigned)(cfg->chunk_cidr - cfg->pool_cidr);
    f->next_chunk = 0;
    uint8_t hb = host_bits_of(f);
    f->chunk_addrs = (hb >= 64) ? UINT64_MAX : (UINT64_C(1) << hb);
    f->chunk_quarantine = NULL;

    /* Last chunk is reserved and rejected; its first usable address is the blackhole. */
    if (!chunk_base_at(f, f->n_chunks - 1, &f->blackhole)) { return FIRC_ERR_INVAL; }
    firc_ip_add(&f->blackhole, 1);
    return FIRC_OK;
}

firc_err_t firc_fakeip_new(const firc_fakeip_cfg_t *cfg, firc_fakeip_t **out) {
    if (out == NULL) { return FIRC_ERR_INVAL; }
    *out = NULL; /* cleared up front, before any refusal path */
    if (cfg == NULL) { return FIRC_ERR_INVAL; }

    firc_err_t werr = firc_fakeip_check_windows(cfg->idle_secs, cfg->clamp_secs);
    if (werr != FIRC_OK) { return werr; }

    firc_fakeip_t *f = calloc(1, sizeof(*f));
    if (f == NULL) { return FIRC_ERR_NOMEM; }

    firc_err_t err = family_init(&f->fam[FAM_V4], &cfg->v4, 4);
    if (err == FIRC_OK) { err = family_init(&f->fam[FAM_V6], &cfg->v6, 16); }
    if (err != FIRC_OK) {
        free(f);
        return err;
    }

    f->max_names = (cfg->max_names == 0) ? FIRC_FAKEIP_DEFAULT_MAX_NAMES : cfg->max_names;
    f->idle_secs = cfg->idle_secs;
    int64_t derived = cfg->clamp_secs * QUARANTINE_MULTIPLE;
    f->quarantine_secs = (derived > QUARANTINE_FLOOR) ? derived : QUARANTINE_FLOOR;

    *out = f;
    return FIRC_OK;
}

static void quarantine_list_free(quarantined_t *q) {
    while (q != NULL) {
        quarantined_t *n = q->next;
        free(q->qname);
        free(q);
        q = n;
    }
}

static void quarantine_drop(firc_fakeip_t *f, quarantined_t *q);

/* Takes the pool: a parked address sits on two lists (group FIFO, by-name index) and both must unlink. */
static void group_free(firc_fakeip_t *f, group_t *g) {
    for (unsigned i = 0; i < N_FAM; i++) {
        quarantined_t *q = g->fam[i].q_head;
        while (q != NULL) {
            quarantined_t *n = q->next;
            quarantine_drop(f, q);
            q = n;
        }
        chunk_node_t *c = g->fam[i].chunks;
        while (c != NULL) {
            chunk_node_t *n = c->next;
            free(c->free_bits);
            free(c);
            c = n;
        }
    }
    free(g->id);
    free(g);
}

void firc_fakeip_free(firc_fakeip_t *f) {
    if (f == NULL) { return; }
    for (size_t i = 0; i < MAP_BUCKETS; i++) {
        mapping_t *m = f->buckets[i];
        while (m != NULL) {
            mapping_t *next = m->next;
            free(m->qname);
            free(m);
            m = next;
        }
    }
    group_t *g = f->groups;
    while (g != NULL) {
        group_t *next = g->next;
        group_free(f, g);
        g = next;
    }
    for (unsigned i = 0; i < N_FAM; i++) { quarantine_list_free(f->fam[i].chunk_quarantine); }
    free(f);
}

/* Unlinks from both the FIFO and the by-name hash, and frees. */
static void quarantine_drop(firc_fakeip_t *f, quarantined_t *q) {
    group_fam_t *gf = q->list;
    chunk_count(f, gf, &q->addr, 0, -1);
    if (q->prev != NULL) {
        q->prev->next = q->next;
    } else {
        gf->q_head = q->next;
    }
    if (q->next != NULL) {
        q->next->prev = q->prev;
    } else {
        gf->q_tail = q->prev;
    }
    if (q->qname != NULL) {
        uint64_t h = fnv1a(q->qname) % MAP_BUCKETS;
        if (q->hprev != NULL) {
            q->hprev->hnext = q->hnext;
        } else {
            f->parked[h] = q->hnext;
        }
        if (q->hnext != NULL) { q->hnext->hprev = q->hprev; }
        free(q->qname);
    }
    free(q);
}

static void quarantine_put_until(firc_fakeip_t *f, group_fam_t *gf, const firc_ip_t *addr,
                                 const firc_ip_t *real, int64_t free_at, const char *qname);

static void quarantine_put(firc_fakeip_t *f, group_fam_t *gf, const firc_ip_t *addr,
                           const firc_ip_t *real, int64_t now, const char *qname) {
    quarantine_put_until(f, gf, addr, real, now + f->quarantine_secs, qname);
}

static void quarantine_put_until(firc_fakeip_t *f, group_fam_t *gf, const firc_ip_t *addr,
                                 const firc_ip_t *real, int64_t free_at, const char *qname) {
    quarantined_t *q = calloc(1, sizeof(*q));
    if (q == NULL) { return; } /* dropped, not propagated: losing one address beats handing it out early */
    q->qname = (qname == NULL) ? NULL : strdup(qname);
    if (qname != NULL && q->qname == NULL) {
        free(q);
        return;
    }
    q->addr = *addr;
    if (real != NULL) {
        q->real = *real;
        q->has_real = true;
    }
    q->free_at = free_at;
    q->list = gf;
    chunk_count(f, gf, addr, 0, +1);

    /* Appended: free_at is monotone with now, so the head stays the one that expires first. */
    q->prev = gf->q_tail;
    if (gf->q_tail != NULL) {
        gf->q_tail->next = q;
    } else {
        gf->q_head = q;
    }
    gf->q_tail = q;

    if (q->qname != NULL) {
        uint64_t h = fnv1a(q->qname) % MAP_BUCKETS;
        q->hnext = f->parked[h];
        if (q->hnext != NULL) { q->hnext->hprev = q; }
        f->parked[h] = q;
    }
}

static bool chunk_take(family_t *fam, int64_t now, firc_ip_t *out) {
    for (quarantined_t **pp = &fam->chunk_quarantine; *pp != NULL; pp = &(*pp)->next) {
        if ((*pp)->free_at > now) { continue; }
        quarantined_t *q = *pp;
        *pp = q->next;
        *out = q->addr;
        free(q);
        return true;
    }
    return false;
}

static void quarantine_put_chunk_until(family_t *fam, const firc_ip_t *base, int64_t free_at) {
    quarantined_t *q = calloc(1, sizeof(*q));
    if (q == NULL) { return; } /* dropped: shrinking the pool beats misrouting through another group */
    q->addr = *base;
    q->free_at = free_at;
    q->next = fam->chunk_quarantine;
    fam->chunk_quarantine = q;
}

static void quarantine_put_chunk(firc_fakeip_t *f, family_t *fam, const firc_ip_t *base,
                                 int64_t now) {
    quarantine_put_chunk_until(fam, base, now + f->quarantine_secs);
}

static bool chunk_is_parked(const family_t *fam, const firc_ip_t *base) {
    for (const quarantined_t *q = fam->chunk_quarantine; q != NULL; q = q->next) {
        if (ip_equal(&q->addr, base)) { return true; }
    }
    return false;
}

static bool take_from(firc_fakeip_t *f, group_fam_t *gf, int64_t now, const char *qname,
                      firc_ip_t *out) {
    /* Name's own parked address first (hash lookup, not a walk: this runs on every allocation). */
    if (qname != NULL) {
        uint64_t h = fnv1a(qname) % MAP_BUCKETS;
        for (quarantined_t *q = f->parked[h]; q != NULL; q = q->hnext) {
            if (q->list != gf || !name_eq(q->qname, qname)) { continue; }
            *out = q->addr;
            quarantine_drop(f, q);
            return true;
        }
    }
    /* Then the oldest; the list is sorted by expiry, so nothing behind the head can be expired. */
    if (gf->q_head != NULL && gf->q_head->free_at <= now) {
        *out = gf->q_head->addr;
        quarantine_drop(f, gf->q_head);
        return true;
    }
    return false;
}

static group_t *group_find_or_add(firc_fakeip_t *f, const char *group_id) {
    for (group_t *g = f->groups; g != NULL; g = g->next) {
        if (strcmp(g->id, group_id) == 0) { return g; }
    }
    group_t *g = calloc(1, sizeof(*g));
    if (g == NULL) { return NULL; }
    g->id = strdup(group_id);
    if (g->id == NULL) {
        free(g);
        return NULL;
    }
    for (unsigned i = 0; i < N_FAM; i++) { g->fam[i].group = g; }
    g->next = f->groups;
    f->groups = g;
    return g;
}

/* A chunk the group cannot record is a chunk that would leak on deletion. */
static bool chunk_remember(firc_fakeip_t *f, family_t *fam, group_fam_t *gf, const firc_ip_t *base) {
    chunk_node_t *c = calloc(1, sizeof(*c));
    if (c == NULL) { return false; }
    c->base = *base;
    uint64_t off = 0;
    if (!addr_split(fam, base, &c->idx, &off)) {
        free(c);
        return false;
    }
    c->next = gf->chunks;
    gf->chunks = c;
    f->gen++; /* a chunk is a rule the kernel does not have yet */
    return true;
}

uint64_t firc_fakeip_gen(const firc_fakeip_t *f) {
    return f == NULL ? 0 : f->gen;
}

static firc_err_t issue(firc_fakeip_t *f, unsigned fi, group_t *g, const char *qname, int64_t now,
                        firc_ip_t *out) {
    family_t *fam = &f->fam[fi];
    group_fam_t *gf = &g->fam[fi];

    if (take_from(f, gf, now, qname, out)) { return FIRC_OK; }
    if (chunk_free_take(fam, gf, out)) { return FIRC_OK; }

    for (;;) {
        while (gf->has_chunk && gf->next_off < fam->chunk_addrs) {
            firc_ip_t candidate = gf->chunk_base;
            firc_ip_add(&candidate, gf->next_off);
            uint64_t off = gf->next_off;
            gf->next_off++;
            if (usable(fi, &candidate, off)) {
                *out = candidate;
                return FIRC_OK;
            }
        }

        firc_ip_t base;
        if (!chunk_take(fam, now, &base)) {
            /* The pool's last chunk is the blackhole and is never handed out. */
            if (fam->next_chunk >= fam->n_chunks - 1) { return FIRC_ERR_LIMIT; }
            if (!chunk_base_at(fam, fam->next_chunk, &base)) { return FIRC_ERR_LIMIT; }
            fam->next_chunk++;
        }

        if (!chunk_remember(f, fam, gf, &base)) {
            /* Hand straight back: allocating from an unlisted chunk would leak it on delete. */
            quarantine_put_chunk(f, fam, &base, now);
            return FIRC_ERR_NOMEM;
        }
        gf->chunk_base = base;
        gf->next_off = 0;
        gf->has_chunk = true;
        f->dirty = true;
        f->chunk_taken = true; /* the hook fires once the name is in (firc_fakeip_get) */
    }
}

void firc_fakeip_set_on_chunk(firc_fakeip_t *f, firc_fakeip_chunk_hook fn, void *ud) {
    if (f == NULL) { return; }
    f->on_chunk = fn;
    f->on_chunk_ud = ud;
}

static void write_blackholes(firc_fakeip_t *f, firc_ip_t *v4_out, firc_ip_t *v6_out) {
    if (v4_out != NULL) { *v4_out = f->fam[FAM_V4].blackhole; }
    if (v6_out != NULL) { *v6_out = f->fam[FAM_V6].blackhole; }
}

firc_err_t firc_fakeip_get(firc_fakeip_t *f, const char *qname, const char *group_id, int64_t now,
                           firc_ip_t *v4_out, firc_ip_t *v6_out) {
    if (f == NULL || qname == NULL || group_id == NULL) { return FIRC_ERR_INVAL; }

    uint64_t h = fnv1a(qname) % MAP_BUCKETS;
    for (mapping_t *m = f->buckets[h]; m != NULL; m = m->next) {
        if (!name_eq(m->qname, qname) || strcmp(m->owner->id, group_id) != 0) { continue; }
        m->last_seen = now;
        f->dirty = true;
        if (v4_out != NULL) { *v4_out = m->addr[FAM_V4]; }
        if (v6_out != NULL) { *v6_out = m->addr[FAM_V6]; }
        return FIRC_OK;
    }

    /* At capacity, refuse like an exhausted pool; mappings already held were served above. */
    if (f->n_names >= f->max_names) {
        write_blackholes(f, v4_out, v6_out);
        return FIRC_ERR_LIMIT;
    }

    group_t *g = group_find_or_add(f, group_id);
    if (g == NULL) {
        write_blackholes(f, v4_out, v6_out);
        return FIRC_ERR_NOMEM;
    }

    /* Both or neither: one family alone would route over whichever interface Happy Eyeballs picked. */
    firc_ip_t issued[N_FAM];
    firc_err_t err = FIRC_OK;
    unsigned done = 0;
    for (; done < N_FAM; done++) {
        err = issue(f, done, g, qname, now, &issued[done]);
        if (err != FIRC_OK) { break; }
    }

    mapping_t *m = NULL;
    if (err == FIRC_OK) {
        m = calloc(1, sizeof(*m));
        if (m == NULL) {
            err = FIRC_ERR_NOMEM;
        } else {
            m->qname = strdup(qname);
            if (m->qname == NULL) {
                free(m);
                m = NULL;
                err = FIRC_ERR_NOMEM;
            }
        }
    }
    if (err != FIRC_OK) {
        for (unsigned i = 0; i < done; i++) {
            quarantine_put(f, &g->fam[i], &issued[i], NULL, now, qname);
        }
        write_blackholes(f, v4_out, v6_out);
        if (f->chunk_taken) {
            f->chunk_taken = false;
            if (f->on_chunk != NULL) { f->on_chunk(f->on_chunk_ud, f); }
        }
        return err;
    }

    for (char *c = m->qname; *c != 0; c++) { *c = (char)fold((unsigned char)*c); }
    for (unsigned i = 0; i < N_FAM; i++) {
        m->addr[i] = issued[i];
        chunk_count(f, &g->fam[i], &issued[i], +1, 0);
    }
    m->last_seen = now;
    f->dirty = true;
    m->owner = g;
    m->next = f->buckets[h];
    f->buckets[h] = m;
    rindex_add(f, m);
    f->n_names++;

    if (v4_out != NULL) { *v4_out = issued[FAM_V4]; }
    if (v6_out != NULL) { *v6_out = issued[FAM_V6]; }
    /* After the mapping is in, so a chunk-take save holds the name that took it. */
    if (f->chunk_taken) {
        f->chunk_taken = false;
        if (f->on_chunk != NULL) { f->on_chunk(f->on_chunk_ud, f); }
    }
    return FIRC_OK;
}

static mapping_t *mapping_find(firc_fakeip_t *f, const char *qname, const char *group_id) {
    uint64_t h = fnv1a(qname) % MAP_BUCKETS;
    for (mapping_t *m = f->buckets[h]; m != NULL; m = m->next) {
        if (name_eq(m->qname, qname) && strcmp(m->owner->id, group_id) == 0) { return m; }
    }
    return NULL;
}

const char *firc_fakeip_name_of(const firc_fakeip_t *f, const firc_ip_t *addr) {
    if (f == NULL || addr == NULL) { return NULL; }
    unsigned fi = addr->len == 4 ? FAM_V4 : addr->len == 16 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return NULL; }
    for (const mapping_t *m = f->by_addr[fi][addr_bucket(addr)]; m != NULL; m = m->rnext[fi]) {
        if (ip_equal(&m->addr[fi], addr)) { return m->qname; }
    }
    return NULL;
}

bool firc_fakeip_pair_of(const firc_fakeip_t *f, const firc_ip_t *fake, const char **group_id, firc_ip_t *real) {
    if (f == NULL || fake == NULL) { return false; }
    for (const mapping_t *m = f->by_addr[FAM_V4][addr_bucket(fake)]; m != NULL; m = m->rnext[FAM_V4]) {
        if (!ip_equal(&m->addr[FAM_V4], fake)) { continue; }
        unsigned fi = m->has_real[FAM_V4] ? FAM_V4 : FAM_V6;
        if (!m->has_real[fi]) { return false; }
        *group_id = m->owner->id;
        *real = m->real[fi];
        return true;
    }
    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        for (const quarantined_t *q = g->fam[FAM_V4].q_head; q != NULL; q = q->next) {
            if (q->has_real && ip_equal(&q->addr, fake)) {
                *group_id = g->id;
                *real = q->real;
                return true;
            }
        }
    }
    return false;
}

void firc_fakeip_clear_real(firc_fakeip_t *f, const char *qname, const char *group_id, int len) {
    if (f == NULL || qname == NULL || group_id == NULL) { return; }
    unsigned fi = len == 4 ? FAM_V4 : len == 16 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return; }
    mapping_t *m = mapping_find(f, qname, group_id);
    if (m == NULL || !m->has_real[fi]) { return; }
    m->has_real[fi] = false;
    m->pair_gen[fi] = ++f->gen;
    f->dirty = true;
}

bool firc_fakeip_overlaps(const firc_fakeip_t *f, const firc_ip_t *addr, uint8_t prefix_len) {
    if (f == NULL || addr == NULL) { return false; }
    unsigned fi = addr->len == 4 ? FAM_V4 : addr->len == 16 ? FAM_V6 : N_FAM;
    if (fi == N_FAM || prefix_len > addr->len * 8) { return false; }
    const family_t *fam = &f->fam[fi];
    unsigned bits = prefix_len < fam->pool_cidr ? prefix_len : fam->pool_cidr;
    for (unsigned bit = 0; bit < bits; bit++) {
        unsigned mask = 0x80u >> (bit % 8);
        if ((addr->b[bit / 8] & mask) != (fam->base.b[bit / 8] & mask)) { return false; }
    }
    return true;
}

bool firc_fakeip_pool_prefix(const firc_fakeip_t *f, unsigned family, firc_ip_t *base_out,
                             uint8_t *prefix_out) {
    if (f == NULL || family >= N_FAM || base_out == NULL || prefix_out == NULL) { return false; }
    *base_out = f->fam[family].base;
    *prefix_out = f->fam[family].pool_cidr;
    return true;
}

firc_err_t firc_fakeip_set_real(firc_fakeip_t *f, const char *qname, const char *group_id, const firc_ip_t *real) {
    if (f == NULL || qname == NULL || group_id == NULL || real == NULL) { return FIRC_ERR_INVAL; }
    unsigned fi;
    if (real->len == 4) {
        fi = FAM_V4;
    } else if (real->len == 16) {
        fi = FAM_V6;
    } else {
        return FIRC_ERR_INVAL;
    }

    mapping_t *m = mapping_find(f, qname, group_id);
    if (m == NULL) { return FIRC_ERR_NOENT; }
    if (!m->has_real[fi] || !ip_equal(&m->real[fi], real)) {
        m->real[fi] = *real;
        m->has_real[fi] = true;
        m->pair_gen[fi] = ++f->gen;
    }
    return FIRC_OK;
}

firc_err_t firc_fakeip_set_reals(firc_fakeip_t *f, const char *qname, const char *group_id,
                                 const firc_ip_t *reals, size_t n) {
    if (f == NULL || qname == NULL || group_id == NULL || (reals == NULL && n > 0)) { return FIRC_ERR_INVAL; }
    mapping_t *m = mapping_find(f, qname, group_id);
    if (m == NULL) { return FIRC_ERR_NOENT; }

    /* Per family: keep the stored address while still offered, else the first offered. */
    const firc_ip_t *first[N_FAM] = {NULL, NULL};
    bool still_there[N_FAM] = {false, false};
    for (size_t i = 0; i < n; i++) {
        unsigned fi;
        if (reals[i].len == 4) {
            fi = FAM_V4;
        } else if (reals[i].len == 16) {
            fi = FAM_V6;
        } else {
            return FIRC_ERR_INVAL;
        }
        if (first[fi] == NULL) { first[fi] = &reals[i]; }
        if (m->has_real[fi] && ip_equal(&m->real[fi], &reals[i])) { still_there[fi] = true; }
    }
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        if (first[fi] == NULL || still_there[fi]) { continue; }
        m->real[fi] = *first[fi];
        m->has_real[fi] = true;
        m->pair_gen[fi] = ++f->gen;
        f->dirty = true;
    }
    return FIRC_OK;
}

bool firc_fakeip_needs_commit(const firc_fakeip_t *f, const firc_ip_t *fake) {
    if (f == NULL || fake == NULL) { return false; }
    unsigned fi = fake->len == 4 ? FAM_V4 : fake->len == 16 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return false; }
    for (const mapping_t *m = f->by_addr[fi][addr_bucket(fake)]; m != NULL; m = m->rnext[fi]) {
        if (!ip_equal(&m->addr[fi], fake)) { continue; }
        uint64_t gen = m->pair_gen[fi];
        if (fi == FAM_V4 && !m->has_real[FAM_V4] && m->has_real[FAM_V6] && m->pair_gen[FAM_V6] > gen) {
            gen = m->pair_gen[FAM_V6];
        }
        return gen > f->committed_gen;
    }
    return false;
}

void firc_fakeip_mark_committed(firc_fakeip_t *f, uint64_t gen) {
    if (f == NULL) { return; }
    if (gen > f->committed_gen) { f->committed_gen = gen; }
}

size_t firc_fakeip_uncommit_group(firc_fakeip_t *f, const char *group_id) {
    if (f == NULL || group_id == NULL) { return 0; }
    uint64_t next = f->gen + 1;
    size_t marked = 0;
    for (size_t i = 0; i < MAP_BUCKETS; i++) {
        for (mapping_t *m = f->buckets[i]; m != NULL; m = m->next) {
            if (m->owner == NULL || strcmp(m->owner->id, group_id) != 0) { continue; }
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                if (!m->has_real[fi]) { continue; }
                m->pair_gen[fi] = next;
                marked++;
            }
        }
    }
    if (marked > 0) { f->gen = next; }
    return marked;
}


void firc_fakeip_walk(const firc_fakeip_t *f, firc_fakeip_walk_fn fn, void *ud) {
    if (f == NULL || fn == NULL) { return; }
    for (size_t i = 0; i < MAP_BUCKETS; i++) {
        for (const mapping_t *m = f->buckets[i]; m != NULL; m = m->next) {
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                fn(ud, m->qname, m->owner->id, &m->addr[fi],
                   m->has_real[fi] ? &m->real[fi] : NULL);
            }
        }
    }
}

void firc_fakeip_walk_chunks(const firc_fakeip_t *f, const char *group_id,
                             firc_fakeip_chunk_fn fn, void *ud) {
    if (f == NULL || fn == NULL) { return; }
    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        if (group_id != NULL && strcmp(g->id, group_id) != 0) { continue; }
        for (unsigned i = 0; i < N_FAM; i++) {
            for (const chunk_node_t *c = g->fam[i].chunks; c != NULL; c = c->next) {
                fn(ud, g->id, i, &c->base, f->fam[i].chunk_cidr);
            }
        }
    }
}

static chunk_node_t *chunk_of(const family_t *fam, group_fam_t *gf, const firc_ip_t *addr, uint64_t *off) {
    uint32_t idx = 0;
    if (!addr_split(fam, addr, &idx, off)) { return NULL; }
    for (chunk_node_t *c = gf->chunks; c != NULL; c = c->next) {
        if (c->idx == idx) { return c; }
    }
    return NULL;
}

static unsigned fam_index_of(const group_fam_t *gf) {
    return gf == &gf->group->fam[FAM_V6] ? FAM_V6 : FAM_V4;
}

static void chunk_count(firc_fakeip_t *f, group_fam_t *gf, const firc_ip_t *addr, int mapped, int parked) {
    uint64_t off = 0;
    chunk_node_t *c = chunk_of(&f->fam[fam_index_of(gf)], gf, addr, &off);
    if (c == NULL) { return; }
    c->n_mapped = (uint32_t)((int)c->n_mapped + mapped);
    c->n_parked = (uint32_t)((int)c->n_parked + parked);
}

/* v4 only: a v6 chunk never fills, so a lost offset there costs nothing. */
static void chunk_free_put(firc_fakeip_t *f, group_fam_t *gf, unsigned fi, const firc_ip_t *addr) {
    family_t *fam = &f->fam[fi];
    if (fi != FAM_V4 || fam->chunk_addrs > 65536) { return; }
    uint64_t off = 0;
    chunk_node_t *c = chunk_of(fam, gf, addr, &off);
    if (c == NULL) { return; }
    if (c->free_bits == NULL) {
        c->free_bits = calloc(((size_t)fam->chunk_addrs + 63) / 64, sizeof(uint64_t));
        if (c->free_bits == NULL) { return; }
    }
    uint64_t bit = (uint64_t)1 << (off % 64);
    if (!(c->free_bits[off / 64] & bit)) {
        c->free_bits[off / 64] |= bit;
        c->n_free++;
    }
}

static bool chunk_free_take(family_t *fam, group_fam_t *gf, firc_ip_t *out) {
    for (chunk_node_t *c = gf->chunks; c != NULL; c = c->next) {
        if (c->n_free == 0) { continue; }
        size_t words = ((size_t)fam->chunk_addrs + 63) / 64;
        for (size_t w = 0; w < words; w++) {
            if (c->free_bits[w] == 0) { continue; }
            unsigned b = 0;
            while (!(c->free_bits[w] & ((uint64_t)1 << b))) { b++; }
            c->free_bits[w] &= ~((uint64_t)1 << b);
            c->n_free--;
            *out = c->base;
            firc_ip_add(out, (uint64_t)w * 64 + b);
            return true;
        }
    }
    return false;
}

void firc_fakeip_reclaim(firc_fakeip_t *f, int64_t now) {
    if (f == NULL) { return; }
    for (size_t i = 0; i < MAP_BUCKETS; i++) {
        mapping_t **pp = &f->buckets[i];
        while (*pp != NULL) {
            mapping_t *m = *pp;
            /* idle_secs, not the quarantine window — a different quantity on purpose. */
            if (now - m->last_seen < f->idle_secs) {
                pp = &m->next;
                continue;
            }
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                chunk_count(f, &m->owner->fam[fi], &m->addr[fi], -1, 0);
                quarantine_put(f, &m->owner->fam[fi], &m->addr[fi],
                               m->has_real[fi] ? &m->real[fi] : NULL, now, m->qname);
            }
            *pp = m->next;
            rindex_del(f, m);
            free(m->qname);
            free(m);
            f->n_names--;
            f->dirty = true;
        }
    }

    /* Expired parks freed here, not lazily on allocation, or a quiet pool never frees anything. */
    for (group_t *g = f->groups; g != NULL; g = g->next) {
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            group_fam_t *gf = &g->fam[fi];
            while (gf->q_head != NULL && gf->q_head->free_at <= now) {
                bool had_rule = gf->q_head->has_real;
                firc_ip_t addr = gf->q_head->addr;
                quarantine_drop(f, gf->q_head);
                if (had_rule) { f->gen++; } /* a rule the kernel still has */
                chunk_free_put(f, gf, fi, &addr);
            }
            /* An empty chunk goes back to the pool, current chunk included. */
            size_t held = 0;
            for (const chunk_node_t *c = gf->chunks; c != NULL; c = c->next) { held++; }
            chunk_node_t **cp = &gf->chunks;
            while (*cp != NULL && held > 1) { /* the last chunk stays: its free set serves the next name */
                chunk_node_t *c = *cp;
                if (c->n_mapped != 0 || c->n_parked != 0) {
                    cp = &c->next;
                    continue;
                }
                if (gf->has_chunk && ip_equal(&c->base, &gf->chunk_base)) { gf->has_chunk = false; }
                held--;
                *cp = c->next;
                quarantine_put_chunk(f, &f->fam[fi], &c->base, now);
                free(c->free_bits);
                free(c);
                f->gen++; /* its mark rule goes */
                f->dirty = true;
            }
        }
    }
}

void firc_fakeip_drop_group(firc_fakeip_t *f, const char *group_id, int64_t now) {
    if (f == NULL || group_id == NULL) { return; }

    group_t **gp = &f->groups;
    while (*gp != NULL && strcmp((*gp)->id, group_id) != 0) { gp = &(*gp)->next; }
    if (*gp == NULL) { return; }
    group_t *g = *gp;

    /* Routed by nothing now: drop names and give capacity back; addresses go with the chunks below. */
    for (size_t i = 0; i < MAP_BUCKETS; i++) {
        mapping_t **pp = &f->buckets[i];
        while (*pp != NULL) {
            mapping_t *m = *pp;
            if (m->owner != g) {
                pp = &m->next;
                continue;
            }
            *pp = m->next;
            rindex_del(f, m);
            free(m->qname);
            free(m);
            f->n_names--;
        }
    }

    for (unsigned fi = 0; fi < N_FAM; fi++) {
        for (chunk_node_t *c = g->fam[fi].chunks; c != NULL; c = c->next) {
            quarantine_put_chunk(f, &f->fam[fi], &c->base, now);
        }
    }

    *gp = g->next;
    group_free(f, g);
    f->dirty = true;
}

/* ---- snapshots: so the committer thread never touches a mapping the DNS path may free. ---- */

typedef struct snap_map {
    uint16_t group_idx;
    firc_ip_t fake[N_FAM];
    firc_ip_t real[N_FAM];
    bool has_real[N_FAM];
} snap_map_t;

typedef struct snap_chunk {
    uint16_t group_idx;
    uint8_t family;
    uint8_t prefix;
    firc_ip_t base;
} snap_chunk_t;

struct firc_fakeip_snapshot {
    char **groups; /* unique ids, interned: one copy however many mappings */
    size_t n_groups;

    snap_map_t *maps;
    size_t n_maps;

    snap_chunk_t *chunks;
    size_t n_chunks;

    uint64_t gen; /* f->gen at the time of taking */
    firc_ip_t pool_base[N_FAM];
    uint8_t pool_prefix[N_FAM];
};

/* Interned, not copied per mapping: a handful of groups vs. tens of thousands of mappings. */
static bool snap_intern(firc_fakeip_snapshot_t *s, const char *id, uint16_t *out) {
    for (size_t i = 0; i < s->n_groups; i++) {
        if (strcmp(s->groups[i], id) == 0) {
            *out = (uint16_t)i;
            return true;
        }
    }
    char **g = realloc(s->groups, (s->n_groups + 1) * sizeof(*g));
    if (g == NULL) { return false; }
    s->groups = g;
    s->groups[s->n_groups] = strdup(id);
    if (s->groups[s->n_groups] == NULL) { return false; }
    *out = (uint16_t)s->n_groups++;
    return true;
}

firc_fakeip_snapshot_t *firc_fakeip_snapshot_take(const firc_fakeip_t *f) {
    if (f == NULL) { return NULL; }
    firc_fakeip_snapshot_t *s = calloc(1, sizeof(*s));
    if (s == NULL) { return NULL; }

    s->gen = f->gen;
    for (unsigned i = 0; i < N_FAM; i++) {
        s->pool_base[i] = f->fam[i].base;
        s->pool_prefix[i] = f->fam[i].pool_cidr;
    }

    /* Sized up front from exact counts: no realloc per mapping. */
    size_t n_parked = 0;
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const quarantined_t *q = f->parked[b]; q != NULL; q = q->hnext) {
            n_parked += q->has_real;
        }
    }
    size_t cap = f->n_names + n_parked;
    if (cap > 0) {
        s->maps = calloc(cap, sizeof(*s->maps));
        if (s->maps == NULL) {
            firc_fakeip_snapshot_free(s);
            return NULL;
        }
    }

    /* Parked pairs first: a released address keeps its rule until quarantined out. */
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const quarantined_t *q = f->parked[b]; q != NULL; q = q->hnext) {
            if (!q->has_real || s->n_maps >= cap) { continue; }
            snap_map_t *e = &s->maps[s->n_maps];
            if (!snap_intern(s, q->list->group->id, &e->group_idx)) {
                firc_fakeip_snapshot_free(s);
                return NULL;
            }
            unsigned fi = q->addr.len == 4 ? FAM_V4 : FAM_V6;
            e->fake[fi] = q->addr;
            e->real[fi] = q->real;
            e->has_real[fi] = true;
            s->n_maps++;
        }
    }

    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const mapping_t *m = f->buckets[b]; m != NULL; m = m->next) {
            if (s->n_maps >= cap) { break; } /* n_names is the bound */
            snap_map_t *e = &s->maps[s->n_maps];
            if (!snap_intern(s, m->owner->id, &e->group_idx)) {
                firc_fakeip_snapshot_free(s);
                return NULL;
            }
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                e->fake[fi] = m->addr[fi];
                e->real[fi] = m->real[fi];
                e->has_real[fi] = m->has_real[fi];
            }
            s->n_maps++;
        }
    }

    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        uint16_t gi = 0;
        if (!snap_intern(s, g->id, &gi)) {
            firc_fakeip_snapshot_free(s);
            return NULL;
        }
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            for (const chunk_node_t *c = g->fam[fi].chunks; c != NULL; c = c->next) {
                snap_chunk_t *v = realloc(s->chunks, (s->n_chunks + 1) * sizeof(*v));
                if (v == NULL) {
                    firc_fakeip_snapshot_free(s);
                    return NULL;
                }
                s->chunks = v;
                s->chunks[s->n_chunks++] = (snap_chunk_t){gi, (uint8_t)fi, f->fam[fi].chunk_cidr,
                                                          c->base};
            }
        }
    }
    return s;
}

void firc_fakeip_snapshot_free(firc_fakeip_snapshot_t *s) {
    if (s == NULL) { return; }
    for (size_t i = 0; i < s->n_groups; i++) { free(s->groups[i]); }
    free(s->groups);
    free(s->maps);
    free(s->chunks);
    free(s);
}

void firc_fakeip_snapshot_walk(const firc_fakeip_snapshot_t *s, firc_fakeip_snap_map_fn fn,
                               void *ud) {
    if (s == NULL || fn == NULL) { return; }
    for (size_t i = 0; i < s->n_maps; i++) {
        const snap_map_t *e = &s->maps[i];
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            if (e->fake[fi].len == 0) { continue; } /* a parked pair's other family */
            fn(ud, s->groups[e->group_idx], fi, &e->fake[fi],
               e->has_real[fi] ? &e->real[fi] : NULL);
        }
    }
}

void firc_fakeip_snapshot_cross_walk(const firc_fakeip_snapshot_t *s, firc_fakeip_cross_fn fn, void *ud) {
    if (s == NULL || fn == NULL) { return; }
    for (size_t i = 0; i < s->n_maps; i++) {
        const snap_map_t *e = &s->maps[i];
        if (e->fake[FAM_V4].len == 0 || e->has_real[FAM_V4] || !e->has_real[FAM_V6]) { continue; }
        fn(ud, s->groups[e->group_idx], &e->fake[FAM_V4], &e->real[FAM_V6]);
    }
}

void firc_fakeip_snapshot_walk_chunks(const firc_fakeip_snapshot_t *s, const char *group_id,
                                      firc_fakeip_chunk_fn fn, void *ud) {
    if (s == NULL || fn == NULL) { return; }
    for (size_t i = 0; i < s->n_chunks; i++) {
        const snap_chunk_t *c = &s->chunks[i];
        const char *gid = s->groups[c->group_idx];
        if (group_id != NULL && strcmp(gid, group_id) != 0) { continue; }
        fn(ud, gid, c->family, &c->base, c->prefix);
    }
}

uint64_t firc_fakeip_snapshot_gen(const firc_fakeip_snapshot_t *s) {
    return s == NULL ? 0 : s->gen;
}

bool firc_fakeip_snapshot_pool_prefix(const firc_fakeip_snapshot_t *s, unsigned family,
                                      firc_ip_t *base_out, uint8_t *prefix_out) {
    if (s == NULL || family >= N_FAM || base_out == NULL || prefix_out == NULL) { return false; }
    *base_out = s->pool_base[family];
    *prefix_out = s->pool_prefix[family];
    return true;
}


/* ---- persistence ------------------------------------------------------ */

static bool ip_text(const firc_ip_t *a, char *out, size_t cap) {
    if (a->len == 4) { return inet_ntop(AF_INET, a->b, out, (socklen_t)cap) != NULL; }
    if (a->len == 16) { return inet_ntop(AF_INET6, a->b, out, (socklen_t)cap) != NULL; }
    return false;
}

static bool ip_from_text(const char *text, uint8_t want_len, firc_ip_t *out) {
    memset(out, 0, sizeof(*out));
    if (want_len == 4 && inet_pton(AF_INET, text, out->b) == 1) {
        out->len = 4;
        return true;
    }
    if (want_len == 16 && inet_pton(AF_INET6, text, out->b) == 1) {
        out->len = 16;
        return true;
    }
    return false;
}

static void geometry_line(const family_t *fam, const char *tag, char *out, size_t cap) {
    char base[INET6_ADDRSTRLEN] = "?";
    ip_text(&fam->base, base, sizeof(base));
    snprintf(out, cap, "%s %s/%u chunk %u", tag, base, fam->pool_cidr, fam->chunk_cidr);
}

/* Chunk index = bits [pool_cidr, chunk_cidr); offset = host bits below. */
static bool addr_split(const family_t *fam, const firc_ip_t *addr, uint32_t *idx, uint64_t *off) {
    if (addr->len != fam->base.len) { return false; }
    unsigned total = (unsigned)addr->len * 8;
    for (unsigned bit = 0; bit < fam->pool_cidr; bit++) {
        unsigned mask = 0x80u >> (bit % 8);
        if ((addr->b[bit / 8] & mask) != (fam->base.b[bit / 8] & mask)) { return false; }
    }
    uint32_t i = 0;
    for (unsigned bit = fam->pool_cidr; bit < fam->chunk_cidr; bit++) {
        i = (i << 1) | (((unsigned)addr->b[bit / 8] >> (7 - bit % 8)) & 1u);
    }
    uint64_t o = 0;
    for (unsigned bit = fam->chunk_cidr; bit < total; bit++) {
        o = (o << 1) | (((unsigned)addr->b[bit / 8] >> (7 - bit % 8)) & 1u);
    }
    *idx = i;
    *off = o;
    return true;
}

firc_err_t firc_fakeip_save(const firc_fakeip_t *f, FILE *out, bool clean) {
    if (f == NULL || out == NULL) { return FIRC_ERR_INVAL; }
    char line[256];
    size_t skipped = 0; /* names the line format cannot carry: the file is then not exact */
    fprintf(out, "firc-pool 1\n");
    geometry_line(&f->fam[FAM_V4], "v4", line, sizeof(line));
    fprintf(out, "%s\n", line);
    geometry_line(&f->fam[FAM_V6], "v6", line, sizeof(line));
    fprintf(out, "%s\n", line);
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const mapping_t *m = f->buckets[b]; m != NULL; m = m->next) {
            if (strpbrk(m->qname, " \t\r\n") != NULL) {
                skipped++;
                continue;
            }
            char cols[N_FAM][2][INET6_ADDRSTRLEN];
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                if (!ip_text(&m->addr[fi], cols[fi][0], sizeof(cols[fi][0]))) { snprintf(cols[fi][0], sizeof(cols[fi][0]), "-"); }
                if (!m->has_real[fi] || !ip_text(&m->real[fi], cols[fi][1], sizeof(cols[fi][1]))) {
                    snprintf(cols[fi][1], sizeof(cols[fi][1]), "-");
                }
            }
            fprintf(out, "map %s %s %s %s %s %s %lld\n", m->qname, m->owner->id, cols[FAM_V4][0],
                    cols[FAM_V4][1], cols[FAM_V6][0], cols[FAM_V6][1], (long long)m->last_seen);
        }
    }
    /* Each group's mark: the chunk it issues from and the next offset there. */
    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            const group_fam_t *gf = &g->fam[fi];
            if (!gf->has_chunk) {
                /* "none": no frontier, or the load would derive one below addresses handed out. */
                if (gf->chunks != NULL) { fprintf(out, "mark %s %s none 0\n", g->id, fi == FAM_V4 ? "v4" : "v6"); }
                continue;
            }
            if (!ip_text(&gf->chunk_base, line, sizeof(line))) { continue; }
            fprintf(out, "mark %s %s %s %llu\n", g->id, fi == FAM_V4 ? "v4" : "v6", line,
                    (unsigned long long)gf->next_off);
        }
    }
    fprintf(out, "cursor v4 %u\ncursor v6 %u\n", f->fam[FAM_V4].next_chunk, f->fam[FAM_V6].next_chunk);
    /* Parked addresses with their expiry and real address, in FIFO (expiry) order. */
    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            for (const quarantined_t *q = g->fam[fi].q_head; q != NULL; q = q->next) {
                char addr_s[INET6_ADDRSTRLEN], real_s[INET6_ADDRSTRLEN];
                if (!ip_text(&q->addr, addr_s, sizeof(addr_s))) { continue; }
                if (!q->has_real || !ip_text(&q->real, real_s, sizeof(real_s))) { snprintf(real_s, sizeof(real_s), "-"); }
                if (q->qname != NULL && strpbrk(q->qname, " \t\r\n") != NULL) {
                    skipped++;
                    continue;
                }
                fprintf(out, "park %s %s %s %s %lld %s\n", g->id, fi == FAM_V4 ? "v4" : "v6", addr_s, real_s,
                        (long long)q->free_at, q->qname != NULL ? q->qname : "-");
            }
        }
    }
    /* Chunks given back: retaken after expiry instead of climbing the cursor. */
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        for (const quarantined_t *q = f->fam[fi].chunk_quarantine; q != NULL; q = q->next) {
            if (!ip_text(&q->addr, line, sizeof(line))) { continue; }
            fprintf(out, "qchunk %s %s %lld\n", fi == FAM_V4 ? "v4" : "v6", line, (long long)q->free_at);
        }
    }
    if (skipped != 0) { FIRC_WARN("pool: %zu name%s not written to the state file; it is not exact", skipped, skipped == 1 ? "" : "s"); }
    fprintf(out, "clean %d\n", (clean && skipped == 0) ? 1 : 0);
    fprintf(out, "end\n"); /* a file cut short before this is not exact */
    if (fflush(out) != 0 || ferror(out)) { return FIRC_ERR_IO; }
    ((firc_fakeip_t *)f)->dirty = false; /* the one thing a save changes */
    return FIRC_OK;
}

bool firc_fakeip_dirty_since_save(const firc_fakeip_t *f) {
    return f != NULL && f->dirty;
}

static bool gf_holds_chunk(const group_fam_t *gf, const firc_ip_t *base) {
    for (const chunk_node_t *c = gf->chunks; c != NULL; c = c->next) {
        if (ip_equal(&c->base, base)) { return true; }
    }
    return false;
}

static bool chunk_held_by_other(const firc_fakeip_t *f, unsigned fi, const group_t *g, const firc_ip_t *base) {
    for (const group_t *o = f->groups; o != NULL; o = o->next) {
        if (o != g && gf_holds_chunk(&o->fam[fi], base)) { return true; }
    }
    return false;
}

/* Refuses an address outside the pool or in the blackhole chunk, or a mapping already held (name and group). */
static bool restore_line(firc_fakeip_t *f, char *line, int64_t now) {
    char *save = NULL;
    char *qname = strtok_r(line, " ", &save);
    char *gid = strtok_r(NULL, " ", &save);
    char *col[4];
    for (int i = 0; i < 4; i++) { col[i] = strtok_r(NULL, " ", &save); }
    char *seen_s = strtok_r(NULL, " \n", &save);
    if (qname == NULL || gid == NULL || col[3] == NULL || seen_s == NULL) { return false; }
    (void)now;
    firc_ip_t addr[N_FAM], real[N_FAM];
    bool has_addr[N_FAM] = {false, false}, has_real[N_FAM] = {false, false};
    uint32_t idx[N_FAM] = {0, 0};
    uint64_t off[N_FAM] = {0, 0};
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        uint8_t len = f->fam[fi].base.len;
        const char *fake_s = col[(size_t)fi * 2], *real_s = col[(size_t)fi * 2 + 1];
        if (strcmp(fake_s, "-") != 0) {
            if (!ip_from_text(fake_s, len, &addr[fi])) { return false; }
            if (!addr_split(&f->fam[fi], &addr[fi], &idx[fi], &off[fi])) { return false; }
            if (idx[fi] >= f->fam[fi].n_chunks - 1) { return false; } /* the blackhole chunk */
            has_addr[fi] = true;
        }
        if (strcmp(real_s, "-") != 0) {
            if (!ip_from_text(real_s, len, &real[fi])) { return false; }
            has_real[fi] = true;
        }
    }
    if (!has_addr[FAM_V4] || !has_addr[FAM_V6]) { return false; } /* a mapping has both families */
    char *end = NULL;
    long long seen = strtoll(seen_s, &end, 10);
    if (end == seen_s || *end != '\0') { return false; }
    if (f->n_names >= f->max_names || mapping_find(f, qname, gid) != NULL) { return false; }

    group_t *g = group_find_or_add(f, gid);
    if (g == NULL) { return false; }
    mapping_t *m = calloc(1, sizeof(*m));
    if (m == NULL) { return false; }
    m->qname = strdup(qname);
    if (m->qname == NULL) {
        free(m);
        return false;
    }
    for (char *c = m->qname; *c != 0; c++) { *c = (char)fold((unsigned char)*c); }
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        family_t *fam = &f->fam[fi];
        group_fam_t *gf = &g->fam[fi];
        firc_ip_t base;
        if (!chunk_base_at(fam, idx[fi], &base)) {
            free(m->qname);
            free(m);
            return false;
        }
        if (!gf_holds_chunk(gf, &base)) {
            if (chunk_held_by_other(f, fi, g, &base) || !chunk_remember(f, fam, gf, &base)) {
                free(m->qname);
                free(m);
                return false;
            }
        }
        if (idx[fi] + 1 > fam->next_chunk) { fam->next_chunk = idx[fi] + 1; }
        /* Without a `mark` line, issue from the highest chunk after its highest address. */
        if (!g->mark_restored[fi] &&
            (!gf->has_chunk || idx[fi] > gf->next_off_chunk_idx ||
             (idx[fi] == gf->next_off_chunk_idx && off[fi] + 1 > gf->next_off))) {
            gf->chunk_base = base;
            gf->has_chunk = true;
            gf->next_off_chunk_idx = idx[fi];
            gf->next_off = off[fi] + 1;
        } else if (g->mark_restored[fi] && gf->has_chunk && idx[fi] == gf->next_off_chunk_idx &&
                   off[fi] + 1 > gf->next_off) {
            /* Mark below a mapping in its own chunk: not trusted, the writer never produces this. */
            gf->next_off = off[fi] + 1;
        }
        m->addr[fi] = addr[fi];
        if (has_real[fi]) {
            m->real[fi] = real[fi];
            m->has_real[fi] = true;
        }
    }
    m->last_seen = (int64_t)seen;
    m->owner = g;
    for (unsigned fi = 0; fi < N_FAM; fi++) { chunk_count(f, &g->fam[fi], &m->addr[fi], +1, 0); }
    /* a restored pair with a real address is one the kernel has no rule for */
    for (unsigned fi = 0; fi < N_FAM; fi++) { if (m->has_real[fi]) { m->pair_gen[fi] = UINT64_MAX; } }
    uint64_t h = fnv1a(m->qname) % MAP_BUCKETS;
    m->next = f->buckets[h];
    f->buckets[h] = m;
    rindex_add(f, m);
    f->n_names++;
    return true;
}

static bool restore_mark(firc_fakeip_t *f, char *line) {
    char *save = NULL;
    char *gid = strtok_r(line, " ", &save);
    char *tag = strtok_r(NULL, " ", &save);
    char *base_s = strtok_r(NULL, " ", &save);
    char *off_s = strtok_r(NULL, " \n", &save);
    if (gid == NULL || tag == NULL || base_s == NULL || off_s == NULL) { return false; }
    unsigned fi = strcmp(tag, "v4") == 0 ? FAM_V4 : strcmp(tag, "v6") == 0 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return false; }
    family_t *fam = &f->fam[fi];
    if (strcmp(base_s, "none") == 0) {
        group_t *gn = group_find_or_add(f, gid);
        if (gn == NULL) { return false; }
        gn->fam[fi].has_chunk = false;
        gn->mark_restored[fi] = true;
        return true;
    }
    firc_ip_t base;
    uint32_t idx = 0;
    uint64_t base_off = 0;
    if (!ip_from_text(base_s, fam->base.len, &base) || !addr_split(fam, &base, &idx, &base_off) ||
        base_off != 0 || idx >= fam->n_chunks - 1) {
        return false;
    }
    char *end = NULL;
    unsigned long long off = strtoull(off_s, &end, 10);
    if (end == off_s || *end != '\0' || off > fam->chunk_addrs) { return false; }
    group_t *g = group_find_or_add(f, gid);
    if (g == NULL) { return false; }
    group_fam_t *gf = &g->fam[fi];
    if (!gf_holds_chunk(gf, &base)) {
        if (chunk_held_by_other(f, fi, g, &base) || !chunk_remember(f, fam, gf, &base)) { return false; }
    }
    if (idx + 1 > fam->next_chunk) { fam->next_chunk = idx + 1; }
    /* The mark beats what the mappings implied (a returned chunk can have a lower index). */
    gf->chunk_base = base;
    gf->has_chunk = true;
    gf->next_off_chunk_idx = idx;
    gf->next_off = (uint64_t)off;
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const mapping_t *m = f->buckets[b]; m != NULL; m = m->next) {
            uint32_t mi = 0;
            uint64_t mo = 0;
            if (m->owner != g || !addr_split(fam, &m->addr[fi], &mi, &mo) || mi != idx) { continue; }
            if (mo + 1 > gf->next_off) { gf->next_off = mo + 1; }
        }
    }
    g->mark_restored[fi] = true;
    return true;
}

static bool restore_park(firc_fakeip_t *f, char *line) {
    char *save = NULL;
    char *gid = strtok_r(line, " ", &save);
    char *tag = strtok_r(NULL, " ", &save);
    char *addr_s = strtok_r(NULL, " ", &save);
    char *real_s = strtok_r(NULL, " ", &save);
    char *at_s = strtok_r(NULL, " ", &save);
    char *qname = strtok_r(NULL, " \n", &save);
    if (gid == NULL || tag == NULL || addr_s == NULL || real_s == NULL || at_s == NULL || qname == NULL) { return false; }
    unsigned fi = strcmp(tag, "v4") == 0 ? FAM_V4 : strcmp(tag, "v6") == 0 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return false; }
    family_t *fam = &f->fam[fi];
    firc_ip_t addr, real;
    uint32_t idx = 0;
    uint64_t off = 0;
    if (!ip_from_text(addr_s, fam->base.len, &addr) || !addr_split(fam, &addr, &idx, &off) || idx >= fam->n_chunks - 1) {
        return false;
    }
    bool has_real = strcmp(real_s, "-") != 0;
    if (has_real && !ip_from_text(real_s, fam->base.len, &real)) { return false; }
    char *end = NULL;
    long long free_at = strtoll(at_s, &end, 10);
    if (end == at_s || *end != '\0') { return false; }
    group_t *g = group_find_or_add(f, gid);
    if (g == NULL) { return false; }
    group_fam_t *gf = &g->fam[fi];
    firc_ip_t base;
    if (!chunk_base_at(fam, idx, &base)) { return false; }
    if (!gf_holds_chunk(gf, &base)) {
        if (chunk_held_by_other(f, fi, g, &base) || !chunk_remember(f, fam, gf, &base)) { return false; }
    }
    if (idx + 1 > fam->next_chunk) { fam->next_chunk = idx + 1; }
    /* above the mark in its chunk: not walked over, like a mapping */
    if (g->mark_restored[fi] && gf->has_chunk && idx == gf->next_off_chunk_idx && off + 1 > gf->next_off) {
        gf->next_off = off + 1;
    }
    quarantine_put_until(f, gf, &addr, has_real ? &real : NULL, (int64_t)free_at,
                         strcmp(qname, "-") == 0 ? NULL : qname);
    return true;
}

static bool restore_qchunk(firc_fakeip_t *f, char *line) {
    char *save = NULL;
    char *tag = strtok_r(line, " ", &save);
    char *base_s = strtok_r(NULL, " ", &save);
    char *at_s = strtok_r(NULL, " \n", &save);
    if (tag == NULL || base_s == NULL || at_s == NULL) { return false; }
    unsigned fi = strcmp(tag, "v4") == 0 ? FAM_V4 : strcmp(tag, "v6") == 0 ? FAM_V6 : N_FAM;
    if (fi == N_FAM) { return false; }
    family_t *fam = &f->fam[fi];
    firc_ip_t base;
    uint32_t idx = 0;
    uint64_t off = 0;
    if (!ip_from_text(base_s, fam->base.len, &base) || !addr_split(fam, &base, &idx, &off) || off != 0 ||
        idx >= fam->n_chunks - 1 || chunk_is_parked(fam, &base)) {
        return false;
    }
    for (const group_t *g = f->groups; g != NULL; g = g->next) {
        if (gf_holds_chunk(&g->fam[fi], &base)) { return false; } /* held by a mapping restored earlier */
    }
    char *end = NULL;
    long long free_at = strtoll(at_s, &end, 10);
    if (end == at_s || *end != '\0') { return false; }
    if (idx + 1 > fam->next_chunk) { fam->next_chunk = idx + 1; }
    quarantine_put_chunk_until(fam, &base, (int64_t)free_at);
    return true;
}

/* Every unused v4 address below the mark is parked; v6 gaps just stay behind the mark. */
static void park_gaps(firc_fakeip_t *f, group_t *g, int64_t now, bool clean, bool *wide_warned) {
    family_t *fam = &f->fam[FAM_V4];
    group_fam_t *gf = &g->fam[FAM_V4];
    if (fam->chunk_addrs > 65536) {
        /* No free set at this width: a freed address is not reissued. */
        if (!*wide_warned) {
            FIRC_INFO("pool: v4 chunks wider than /16 have no free set; addresses freed before a restart are not reissued");
            *wide_warned = true;
        }
        if (!clean && gf->has_chunk) { gf->next_off = fam->chunk_addrs; }
        return;
    }
    size_t words = ((size_t)fam->chunk_addrs + 63) / 64;
    size_t held = 0;
    for (chunk_node_t *c = gf->chunks; c != NULL; c = c->next) { held++; }
    if (held == 0) { return; }
    /* One pass over mappings + parked list, not one pass per chunk (quadratic at the cap). */
    chunk_node_t **nodes = calloc(held, sizeof(*nodes));
    uint64_t *used = calloc(held * words, sizeof(*used));
    if (nodes == NULL || used == NULL) {
        free(nodes);
        free(used);
        return;
    }
    size_t k = 0;
    for (chunk_node_t *c = gf->chunks; c != NULL; c = c->next) { nodes[k++] = c; }
    #define SLOT_OF(idx_, out_)                                                       \
        do {                                                                            \
            (out_) = held;                                                              \
            for (size_t s_ = 0; s_ < held; s_++) { if (nodes[s_]->idx == (idx_)) { (out_) = s_; break; } } \
        } while (0)
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (const mapping_t *m = f->buckets[b]; m != NULL; m = m->next) {
            uint32_t i = 0;
            uint64_t o = 0;
            if (m->owner != g || !addr_split(fam, &m->addr[FAM_V4], &i, &o)) { continue; }
            size_t slot;
            SLOT_OF(i, slot);
            if (slot < held) { used[slot * words + o / 64] |= (uint64_t)1 << (o % 64); }
        }
    }
    for (const quarantined_t *q = gf->q_head; q != NULL; q = q->next) {
        uint32_t i = 0;
        uint64_t o = 0;
        if (!addr_split(fam, &q->addr, &i, &o)) { continue; }
        size_t slot;
        SLOT_OF(i, slot);
        if (slot < held) { used[slot * words + o / 64] |= (uint64_t)1 << (o % 64); }
    }
    #undef SLOT_OF
    for (size_t slot = 0; slot < held; slot++) {
        chunk_node_t *c = nodes[slot];
        bool current = gf->has_chunk && ip_equal(&c->base, &gf->chunk_base);
        /* Clean: unused below the mark is free now. Crash: unused is parked, not freed. */
        uint64_t limit = (clean && current) ? gf->next_off : fam->chunk_addrs;
        /* Clear the free set first: an address can't be both free and parked at once. */
        if (!clean && c->free_bits != NULL) {
            memset(c->free_bits, 0, words * sizeof(*c->free_bits));
            c->n_free = 0;
        }
        for (uint64_t o = 0; o < limit; o++) {
            if (used[slot * words + o / 64] & ((uint64_t)1 << (o % 64))) { continue; }
            firc_ip_t a = c->base;
            firc_ip_add(&a, o);
            if (!usable(FAM_V4, &a, o)) { continue; }
            if (clean) {
                chunk_free_put(f, gf, FAM_V4, &a);
            } else {
                quarantine_put(f, gf, &a, NULL, now, NULL);
            }
        }
    }
    free(nodes);
    free(used);
    /* After a crash, the current chunk is abandoned to its end. */
    if (!clean && gf->has_chunk) { gf->next_off = fam->chunk_addrs; }
}

/* v6 after a crash: jumps the mark 2^32 past the saved offset; chunks of /96 or narrower have no room for it. */
static void v6_mark_past_the_unsaved(firc_fakeip_t *f, group_t *g, bool *warned) {
    group_fam_t *gf6 = &g->fam[FAM_V6];
    uint64_t span = f->fam[FAM_V6].chunk_addrs;
    if (!gf6->has_chunk) { return; }
    if (span <= ((uint64_t)1 << 32)) {
        if (!*warned) {
            FIRC_INFO("pool: v6 chunks of /96 or narrower get no crash protection on load; v6 addresses issued "
                      "since the last save may be reissued");
            *warned = true;
        }
        return;
    }
    if (gf6->next_off < span - ((uint64_t)1 << 32)) { gf6->next_off += (uint64_t)1 << 32; }
}

void firc_fakeip_distrust(firc_fakeip_t *f, int64_t now) {
    if (f == NULL) { return; }
    bool warned = false, wide_warned = false;
    for (group_t *g = f->groups; g != NULL; g = g->next) {
        park_gaps(f, g, now, false, &wide_warned);
        v6_mark_past_the_unsaved(f, g, &warned);
    }
    f->dirty = true;
}

firc_err_t firc_fakeip_load(firc_fakeip_t *f, FILE *in, int64_t now, size_t *restored) {
    if (restored != NULL) { *restored = 0; }
    if (f == NULL || in == NULL) { return FIRC_ERR_INVAL; }
    if (f->n_names != 0) { return FIRC_ERR_INVAL; }
    /* The longest line the writer produces is under 400 bytes; a longer one is refused whole. */
    char line[512], want[256];
    if (fgets(line, sizeof(line), in) == NULL || strcmp(line, "firc-pool 1\n") != 0) { return FIRC_ERR_INVAL; }
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        geometry_line(&f->fam[fi], fi == FAM_V4 ? "v4" : "v6", want, sizeof(want));
        if (fgets(line, sizeof(line), in) == NULL) { return FIRC_ERR_INVAL; }
        char *nl = strchr(line, '\n');
        if (nl != NULL) { *nl = '\0'; }
        if (strcmp(line, want) != 0) { return FIRC_ERR_INVAL; }
    }
    size_t n = 0, refused = 0;
    bool clean = false, seen_end = false;
    while (fgets(line, sizeof(line), in) != NULL) {
        if (strchr(line, '\n') == NULL && !feof(in)) {
            refused++;
            int ch;
            while ((ch = fgetc(in)) != EOF && ch != '\n') { }
            continue;
        }
        if (strncmp(line, "map ", 4) == 0) {
            if (restore_line(f, line + 4, now)) { n++; } else { refused++; }
        } else if (strncmp(line, "mark ", 5) == 0) {
            if (!restore_mark(f, line + 5)) { refused++; }
        } else if (strncmp(line, "park ", 5) == 0) {
            if (!restore_park(f, line + 5)) { refused++; }
        } else if (strncmp(line, "qchunk ", 7) == 0) {
            if (!restore_qchunk(f, line + 7)) { refused++; }
        } else if (strcmp(line, "end\n") == 0) {
            seen_end = true;
        } else if (strncmp(line, "clean ", 6) == 0) {
            clean = line[6] == '1';
        } else if (strncmp(line, "cursor ", 7) == 0) {
            char *save = NULL;
            char *tag = strtok_r(line + 7, " ", &save);
            char *cur_s = strtok_r(NULL, " \n", &save);
            char *end = NULL;
            unsigned long cur = cur_s != NULL ? strtoul(cur_s, &end, 10) : 0;
            unsigned fi = tag == NULL ? N_FAM : strcmp(tag, "v4") == 0 ? FAM_V4 : strcmp(tag, "v6") == 0 ? FAM_V6 : N_FAM;
            if (fi != N_FAM && cur_s != NULL && end != cur_s && *end == '\0' && cur < f->fam[fi].n_chunks) {
                if (cur > f->fam[fi].next_chunk) { f->fam[fi].next_chunk = (uint32_t)cur; }
            } else {
                refused++;
            }
        } else {
            refused++; /* not a line of this format */
        }
    }
    /* A file cut short is what it holds, but not exact. */
    if (!seen_end) {
        FIRC_WARN("pool: state file ends early; taken as not exact");
        clean = false;
    }
    /* A refused line may name an address a client holds: treat the file as not exact. */
    if (refused != 0) {
        FIRC_WARN("pool: %zu state file line%s refused; taken as not exact", refused, refused == 1 ? "" : "s");
        clean = false;
    }
    /* Unclean: every park waits a fresh window. Clean: its own expiry, capped at one window. */
    for (group_t *g = f->groups; g != NULL; g = g->next) {
        for (unsigned fi = 0; fi < N_FAM; fi++) {
            for (quarantined_t *q = g->fam[fi].q_head; q != NULL; q = q->next) {
                int64_t cap = now + f->quarantine_secs;
                q->free_at = clean ? (q->free_at < cap ? q->free_at : cap) : cap;
            }
        }
    }
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        for (quarantined_t *q = f->fam[fi].chunk_quarantine; q != NULL; q = q->next) {
            int64_t cap = now + f->quarantine_secs;
            q->free_at = clean ? (q->free_at < cap ? q->free_at : cap) : cap;
        }
    }
    /* An unheld, unrecorded chunk below the mark is parked, like a deleted group's chunk. */
    for (unsigned fi = 0; fi < N_FAM; fi++) {
        family_t *fam = &f->fam[fi];
        for (uint32_t i = 0; i < fam->next_chunk; i++) {
            firc_ip_t base;
            if (!chunk_base_at(fam, i, &base)) { continue; }
            bool held = chunk_is_parked(fam, &base);
            for (group_t *g = f->groups; g != NULL && !held; g = g->next) { held = gf_holds_chunk(&g->fam[fi], &base); }
            if (!held) { quarantine_put_chunk(f, fam, &base, now); }
        }
    }
    bool warned = false, wide_warned = false;
    for (group_t *g = f->groups; g != NULL; g = g->next) {
        park_gaps(f, g, now, clean, &wide_warned);
        /* A clean stop's file is exact: nothing was issued after it. */
        if (!clean) { v6_mark_past_the_unsaved(f, g, &warned); }
    }
    /* Every restored pair is one the kernel has no rule for yet. */
    f->gen++;
    for (size_t b = 0; b < MAP_BUCKETS; b++) {
        for (mapping_t *m = f->buckets[b]; m != NULL; m = m->next) {
            for (unsigned fi = 0; fi < N_FAM; fi++) {
                m->pair_gen[fi] = m->pair_gen[fi] == UINT64_MAX ? f->gen : 0;
            }
        }
    }
    /* A clean file is exact only until something is issued; mark dirty so it's rewritten. */
    f->dirty = clean;
    if (restored != NULL) { *restored = n; }
    return FIRC_OK;
}

firc_err_t firc_fakeip_save_file_said(const firc_fakeip_t *f, const char *path, bool clean, firc_logrep_t *said) {
    firc_err_t err = firc_fakeip_save_file(f, path, clean);
    if (err != FIRC_OK) {
        firc_logrep_fail_for(said, 0, NULL, "pool state not saved to %s: %s; a restart is the fix", path,
                             firc_err_str(err));
    } else {
        (void)firc_logrep_ok(said, NULL, "pool state saved to %s", path);
    }
    return err;
}

firc_err_t firc_fakeip_save_file(const firc_fakeip_t *f, const char *path, bool clean) {
    if (f == NULL || path == NULL) { return FIRC_ERR_INVAL; }
    char tmp[512];
    if (snprintf(tmp, sizeof(tmp), "%s.tmp", path) >= (int)sizeof(tmp)) { return FIRC_ERR_INVAL; }
    /* fsync before rename, or a power cut can leave an empty file where the good one was. */
    int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) { return firc_err_from_errno(errno); }
    FILE *out = fdopen(fd, "w");
    if (out == NULL) {
        close(fd);
        unlink(tmp);
        return FIRC_ERR_IO;
    }
    firc_err_t err = firc_fakeip_save(f, out, clean);
    if (err == FIRC_OK && fsync(fd) != 0) { err = firc_err_from_errno(errno); }
    if (fclose(out) != 0 && err == FIRC_OK) { err = FIRC_ERR_IO; }
    if (err == FIRC_OK && rename(tmp, path) != 0) { err = firc_err_from_errno(errno); }
    if (err != FIRC_OK) {
        unlink(tmp);
        /* the old file is still the one on disk: the pool still wants saving */
        ((firc_fakeip_t *)f)->dirty = true;
    }
    return err;
}

firc_err_t firc_fakeip_load_file(firc_fakeip_t *f, const char *path, int64_t now, size_t *restored) {
    if (restored != NULL) { *restored = 0; }
    if (f == NULL || path == NULL) { return FIRC_ERR_INVAL; }
    FILE *in = fopen(path, "re");
    if (in == NULL) { return errno == ENOENT ? FIRC_ERR_NOENT : firc_err_from_errno(errno); }
    firc_err_t err = firc_fakeip_load(f, in, now, restored);
    fclose(in);
    return err;
}
