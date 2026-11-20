#include "firc/dnsstat.h"

#include <stdlib.h>
#include <string.h>

#define NIL UINT32_MAX
#define NAME_CAP 256
#define HOURS FIRC_DNSSTAT_HOURS
#define FNV_OFFSET 2166136261u
#define FNV_PRIME 16777619u

typedef struct key {
    firc_ip_t client;
    bool last_blocked;
    firc_id_t owner;
    uint32_t hash, name, heap, total, total_blocked;
    int64_t hour;
    int64_t last;
    uint16_t q[HOURS], b[HOURS];
} stat_key_t;

typedef struct name {
    char *s;
    uint32_t hash, refs;
    bool arpa;
} name_t;

typedef struct slot_index {
    uint32_t *v;
    uint32_t mask;
} slot_index_t;

typedef struct hour_count {
    int64_t hour;
    uint32_t q, b;
} hour_count_t;

struct firc_dnsstat {
    uint32_t max;
    stat_key_t *keys;
    uint32_t *key_free, key_free_n;
    uint32_t *heap, count;
    slot_index_t key_ix;
    name_t *names;
    uint32_t *name_free, name_free_n, name_count;
    slot_index_t name_ix;
    hour_count_t tot[HOURS], arpa[HOURS];
    int64_t hour;
};

static uint32_t fnv(uint32_t h, const void *data, size_t n)
{
    const unsigned char *p = data;
    for (size_t i = 0; i < n; i++) {
        h ^= p[i];
        h *= FNV_PRIME;
    }
    return h;
}

static bool ip_ok(const firc_ip_t *ip)
{
    return ip != NULL && (ip->len == 4 || ip->len == 16);
}

static bool ip_eq(const firc_ip_t *a, const firc_ip_t *b)
{
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

static uint32_t key_hash(const firc_ip_t *client, uint32_t name)
{
    uint32_t h = fnv(FNV_OFFSET, &client->len, 1);
    h = fnv(h, client->b, client->len);
    return fnv(h, &name, sizeof(name));
}

static int64_t hour_of(int64_t now)
{
    return now < 0 ? 0 : now / FIRC_DNSSTAT_HOUR;
}

static size_t bucket(int64_t hour)
{
    return (size_t)(hour % HOURS);
}

static uint32_t sat_add(uint32_t a, uint32_t b)
{
    return a > UINT32_MAX - b ? UINT32_MAX : a + b;
}

static bool index_init(slot_index_t *ix, uint32_t max)
{
    size_t cap = 1;
    while (cap < 2 * (size_t)max) { cap <<= 1; }
    ix->v = calloc(cap, sizeof(uint32_t));
    ix->mask = (uint32_t)(cap - 1);
    return ix->v != NULL;
}

static void index_put(slot_index_t *ix, uint32_t h, uint32_t slot)
{
    uint32_t pos = h & ix->mask;
    while (ix->v[pos] != 0) { pos = (pos + 1) & ix->mask; }
    ix->v[pos] = slot + 1;
}

typedef uint32_t (*hash_at_fn)(const firc_dnsstat_t *s, uint32_t slot);

static uint32_t key_hash_at(const firc_dnsstat_t *s, uint32_t slot)
{
    return s->keys[slot].hash;
}

static uint32_t name_hash_at(const firc_dnsstat_t *s, uint32_t slot)
{
    return s->names[slot].hash;
}

static void index_del(const firc_dnsstat_t *s, slot_index_t *ix, uint32_t slot, hash_at_fn hash_at)
{
    uint32_t i = hash_at(s, slot) & ix->mask;
    while (ix->v[i] != slot + 1) { i = (i + 1) & ix->mask; }
    ix->v[i] = 0;
    for (uint32_t j = (i + 1) & ix->mask; ix->v[j] != 0; j = (j + 1) & ix->mask) {
        uint32_t home = hash_at(s, ix->v[j] - 1) & ix->mask;
        bool stays = i <= j ? (home > i && home <= j) : (home > i || home <= j);
        if (stays) { continue; }
        ix->v[i] = ix->v[j];
        ix->v[j] = 0;
        i = j;
    }
}

static size_t index_probe_max(const slot_index_t *ix)
{
    size_t worst = 0, run = 0;
    for (uint64_t k = 0; k < 2 * ((uint64_t)ix->mask + 1); k++) {
        if (ix->v[k & ix->mask] != 0) {
            run++;
            if (run > worst) { worst = run; }
        } else {
            run = 0;
        }
    }
    return worst < (size_t)ix->mask + 1 ? worst : (size_t)ix->mask + 1;
}

static uint32_t name_find(const firc_dnsstat_t *s, uint32_t h, const char *name)
{
    for (uint32_t pos = h & s->name_ix.mask;; pos = (pos + 1) & s->name_ix.mask) {
        uint32_t v = s->name_ix.v[pos];
        if (v == 0) { return NIL; }
        const name_t *n = &s->names[v - 1];
        if (n->hash == h && strcmp(n->s, name) == 0) { return v - 1; }
    }
}

static uint32_t key_find(const firc_dnsstat_t *s, uint32_t h, const firc_ip_t *client, uint32_t name)
{
    for (uint32_t pos = h & s->key_ix.mask;; pos = (pos + 1) & s->key_ix.mask) {
        uint32_t v = s->key_ix.v[pos];
        if (v == 0) { return NIL; }
        const stat_key_t *k = &s->keys[v - 1];
        if (k->hash == h && k->name == name && ip_eq(&k->client, client)) { return v - 1; }
    }
}

static uint32_t name_add(firc_dnsstat_t *s, uint32_t h, const char *name, size_t len, bool arpa)
{
    if (s->name_free_n == 0) { return NIL; }
    char *copy = malloc(len + 1);
    if (copy == NULL) { return NIL; }
    memcpy(copy, name, len + 1);
    uint32_t slot = s->name_free[--s->name_free_n];
    name_t *n = &s->names[slot];
    n->s = copy;
    n->hash = h;
    n->refs = 0;
    n->arpa = arpa;
    index_put(&s->name_ix, h, slot);
    s->name_count++;
    return slot;
}

static void name_unref(firc_dnsstat_t *s, uint32_t slot)
{
    name_t *n = &s->names[slot];
    if (--n->refs > 0) { return; }
    index_del(s, &s->name_ix, slot, name_hash_at);
    free(n->s);
    n->s = NULL;
    s->name_free[s->name_free_n++] = slot;
    s->name_count--;
}

static bool less(const firc_dnsstat_t *s, uint32_t a, uint32_t b)
{
    const stat_key_t *x = &s->keys[a], *y = &s->keys[b];
    return x->total < y->total || (x->total == y->total && x->last < y->last);
}

static void heap_set(firc_dnsstat_t *s, uint32_t pos, uint32_t k)
{
    s->heap[pos] = k;
    s->keys[k].heap = pos;
}

static void sift_up(firc_dnsstat_t *s, uint32_t pos)
{
    uint32_t k = s->heap[pos];
    while (pos > 0) {
        uint32_t parent = (pos - 1) / 2;
        if (!less(s, k, s->heap[parent])) { break; }
        heap_set(s, pos, s->heap[parent]);
        pos = parent;
    }
    heap_set(s, pos, k);
}

static void sift_down(firc_dnsstat_t *s, uint32_t pos)
{
    uint32_t k = s->heap[pos];
    for (;;) {
        uint32_t child = 2 * pos + 1;
        if (child >= s->count) { break; }
        if (child + 1 < s->count && less(s, s->heap[child + 1], s->heap[child])) { child++; }
        if (!less(s, s->heap[child], k)) { break; }
        heap_set(s, pos, s->heap[child]);
        pos = child;
    }
    heap_set(s, pos, k);
}

static void key_drop(firc_dnsstat_t *s, uint32_t k)
{
    index_del(s, &s->key_ix, k, key_hash_at);
    name_unref(s, s->keys[k].name);
    s->key_free[s->key_free_n++] = k;
}

static void evict_root(firc_dnsstat_t *s)
{
    uint32_t k = s->heap[0];
    s->count--;
    if (s->count > 0) {
        heap_set(s, 0, s->heap[s->count]);
        sift_down(s, 0);
    }
    key_drop(s, k);
}

static void roll(stat_key_t *k, int64_t hour)
{
    if (hour <= k->hour) { return; }
    if (hour - k->hour >= HOURS) {
        memset(k->q, 0, sizeof(k->q));
        memset(k->b, 0, sizeof(k->b));
        k->total = k->total_blocked = 0;
    } else {
        size_t i = bucket(k->hour);
        for (int64_t h = k->hour + 1; h <= hour; h++) {
            i = i + 1 == HOURS ? 0 : i + 1;
            k->total -= k->q[i];
            k->total_blocked -= k->b[i];
            k->q[i] = 0;
            k->b[i] = 0;
        }
    }
    k->hour = hour;
}

static firc_dnsstat_count_t key_count(const stat_key_t *k, int64_t hour)
{
    if (hour == k->hour) { return (firc_dnsstat_count_t){k->total, k->total_blocked}; }
    firc_dnsstat_count_t c = {0, 0};
    int64_t lo = (hour > k->hour ? hour : k->hour) - (HOURS - 1);
    int64_t hi = hour < k->hour ? hour : k->hour;
    if (lo < 0) { lo = 0; }
    size_t i = lo <= hi ? bucket(lo) : 0;
    for (int64_t h = lo; h <= hi; h++) {
        c.queries += k->q[i];
        c.blocked += k->b[i];
        i = i + 1 == HOURS ? 0 : i + 1;
    }
    return c;
}

firc_dnsstat_t *firc_dnsstat_new(size_t max_keys)
{
    if (max_keys == 0 || max_keys > (1u << 28)) { return NULL; }
    firc_dnsstat_t *s = calloc(1, sizeof(*s));
    if (s == NULL) { return NULL; }
    uint32_t max = (uint32_t)max_keys;
    s->max = max;
    s->keys = calloc(max, sizeof(stat_key_t));
    s->key_free = calloc(max, sizeof(uint32_t));
    s->heap = calloc(max, sizeof(uint32_t));
    s->names = calloc(max, sizeof(name_t));
    s->name_free = calloc(max, sizeof(uint32_t));
    bool ok = index_init(&s->key_ix, max);
    ok = index_init(&s->name_ix, max) && ok;
    if (!ok || s->keys == NULL || s->key_free == NULL || s->heap == NULL || s->names == NULL ||
        s->name_free == NULL) {
        firc_dnsstat_free(s);
        return NULL;
    }
    for (uint32_t i = 0; i < max; i++) {
        s->key_free[i] = max - 1 - i;
        s->name_free[i] = max - 1 - i;
    }
    s->key_free_n = s->name_free_n = max;
    for (size_t i = 0; i < HOURS; i++) { s->tot[i].hour = s->arpa[i].hour = INT64_MIN; }
    return s;
}

void firc_dnsstat_free(firc_dnsstat_t *s)
{
    if (s == NULL) { return; }
    if (s->names != NULL) {
        for (uint32_t i = 0; i < s->max; i++) { free(s->names[i].s); }
    }
    free(s->keys);
    free(s->key_free);
    free(s->heap);
    free(s->names);
    free(s->name_free);
    free(s->key_ix.v);
    free(s->name_ix.v);
    free(s);
}

static bool arpa_n(const char *name, size_t n)
{
    return (n == 4 && memcmp(name, "arpa", 4) == 0) || (n > 5 && memcmp(name + n - 5, ".arpa", 5) == 0);
}

static void tot_add(hour_count_t *tot, int64_t hour, size_t i, bool blocked)
{
    hour_count_t *t = &tot[i];
    if (t->hour != hour) {
        t->hour = hour;
        t->q = t->b = 0;
    }
    t->q = sat_add(t->q, 1);
    if (blocked) { t->b = sat_add(t->b, 1); }
}

void firc_dnsstat_note(firc_dnsstat_t *s, const char *name, const firc_ip_t *client, firc_id_t owner, bool blocked,
                       int64_t now_mono)
{
    if (s == NULL || !ip_ok(client) || name == NULL) { return; }
    size_t len = strnlen(name, NAME_CAP);
    if (len == 0 || len == NAME_CAP) { return; }
    int64_t hour = hour_of(now_mono);
    if (hour < s->hour) { hour = s->hour; }
    size_t i = bucket(hour);
    tot_add(s->tot, hour, i, blocked);
    bool arpa = arpa_n(name, len);
    if (arpa) { tot_add(s->arpa, hour, i, blocked); }
    uint32_t nh = fnv(FNV_OFFSET, name, len);
    uint32_t ns = name_find(s, nh, name);
    uint32_t k = ns != NIL ? key_find(s, key_hash(client, ns), client, ns) : NIL;
    if (k == NIL) {
        if (s->count == s->max) {
            evict_root(s);
            if (ns != NIL && s->names[ns].s == NULL) { ns = NIL; }
        }
        if (ns == NIL) {
            ns = name_add(s, nh, name, len, arpa);
            if (ns == NIL) { return; }
        }
        k = s->key_free[--s->key_free_n];
        stat_key_t *nk = &s->keys[k];
        memset(nk, 0, sizeof(*nk));
        nk->client = *client;
        nk->hash = key_hash(client, ns);
        nk->name = ns;
        nk->hour = hour;
        s->names[ns].refs++;
        index_put(&s->key_ix, nk->hash, k);
        heap_set(s, s->count++, k);
        sift_up(s, nk->heap);
    }
    stat_key_t *kp = &s->keys[k];
    roll(kp, hour);
    if (kp->q[i] < UINT16_MAX) {
        kp->q[i]++;
        kp->total++;
    }
    if (blocked && kp->b[i] < UINT16_MAX) {
        kp->b[i]++;
        kp->total_blocked++;
    }
    kp->last = now_mono;
    kp->owner = owner;
    kp->last_blocked = blocked;
    sift_up(s, kp->heap);
    sift_down(s, kp->heap);
}

void firc_dnsstat_sweep(firc_dnsstat_t *s, int64_t now_mono)
{
    if (s == NULL) { return; }
    int64_t hour = hour_of(now_mono);
    if (hour < s->hour) { hour = s->hour; }
    s->hour = hour;
    uint32_t kept = 0;
    for (uint32_t i = 0; i < s->count; i++) {
        uint32_t k = s->heap[i];
        roll(&s->keys[k], hour);
        if (s->keys[k].total == 0) {
            key_drop(s, k);
        } else {
            heap_set(s, kept++, k);
        }
    }
    s->count = kept;
    for (uint32_t i = kept / 2; i-- > 0;) { sift_down(s, i); }
}

static firc_dnsstat_count_t window_sum(const hour_count_t *tot, int64_t now_mono)
{
    firc_dnsstat_count_t c = {0, 0};
    int64_t hour = hour_of(now_mono);
    for (size_t i = 0; i < HOURS; i++) {
        const hour_count_t *t = &tot[i];
        if (t->hour > hour - HOURS && t->hour <= hour) {
            c.queries = sat_add(c.queries, t->q);
            c.blocked = sat_add(c.blocked, t->b);
        }
    }
    return c;
}

firc_dnsstat_count_t firc_dnsstat_totals(const firc_dnsstat_t *s, int64_t now_mono)
{
    firc_dnsstat_count_t c = {0, 0};
    return s != NULL ? window_sum(s->tot, now_mono) : c;
}

firc_dnsstat_count_t firc_dnsstat_totals_arpa(const firc_dnsstat_t *s, int64_t now_mono)
{
    firc_dnsstat_count_t c = {0, 0};
    return s != NULL ? window_sum(s->arpa, now_mono) : c;
}

size_t firc_dnsstat_names(const firc_dnsstat_t *s)
{
    return s != NULL ? s->name_count : 0;
}

bool firc_dnsstat_name_is_arpa(const firc_dnsstat_t *s, uint32_t name_slot)
{
    return s != NULL && name_slot < s->max && s->names[name_slot].arpa;
}

size_t firc_dnsstat_name_slots(const firc_dnsstat_t *s)
{
    return s != NULL ? s->max : 0;
}

size_t firc_dnsstat_keys(const firc_dnsstat_t *s)
{
    return s != NULL ? s->count : 0;
}

void firc_dnsstat_each(const firc_dnsstat_t *s, int64_t now_mono, firc_dnsstat_visit_fn fn, void *ud)
{
    if (s == NULL || fn == NULL) { return; }
    int64_t hour = hour_of(now_mono);
    for (uint32_t i = 0; i < s->count; i++) {
        const stat_key_t *k = &s->keys[s->heap[i]];
        firc_dnsstat_count_t c = key_count(k, hour);
        if (c.queries == 0) { continue; }
        if (!fn(ud, k->name, s->names[k->name].s, &k->client, c, k->last, k->owner, k->last_blocked)) { return; }
    }
}

size_t firc_dnsstat_probe_max_for_test(const firc_dnsstat_t *s)
{
    if (s == NULL) { return 0; }
    size_t a = index_probe_max(&s->key_ix), b = index_probe_max(&s->name_ix);
    return a > b ? a : b;
}

