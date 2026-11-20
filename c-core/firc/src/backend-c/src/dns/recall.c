#include "firc/recall.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define NIL UINT32_MAX
#define NAME_CAP 256

typedef struct link {
    uint32_t prev, next; /* NIL at either end */
    uint32_t hash;
} link_t;

typedef struct addr_slot {
    link_t l;
    firc_ip_t real;
    char group[FIRC_ID_STR_LEN];
    char name[NAME_CAP];
} addr_slot_t;

typedef struct pair_slot {
    link_t l;
    firc_ip_t client;
    uint8_t family;
    firc_recall_answer_t answer;
    char name[NAME_CAP];
} pair_slot_t;

typedef bool (*eq_fn)(const void *slot, const void *key);

typedef struct table {
    void *slots;          /* max slots of `stride` bytes, each opening with a link_t */
    size_t stride;
    uint32_t *index;      /* slot + 1; 0 = empty */
    uint32_t mask;
    uint32_t max, count;
    uint32_t head, tail;
    eq_fn eq;
} table_t;

struct firc_recall {
    table_t addrs;
    table_t pairs;
    int64_t horizon;
};

static link_t *lnk(const table_t *t, uint32_t s)
{
    return (link_t *)((unsigned char *)t->slots + (size_t)s * t->stride);
}

static bool table_init(table_t *t, size_t max, size_t stride, eq_fn eq)
{
    memset(t, 0, sizeof(*t));
    if (max == 0 || max > (1u << 28)) { return false; }
    size_t cap = 1;
    while (cap < 2 * max) { cap <<= 1; }
    t->slots = calloc(max, stride);
    t->index = calloc(cap, sizeof(uint32_t));
    if (t->slots == NULL || t->index == NULL) {
        free(t->slots);
        free(t->index);
        t->slots = NULL;
        t->index = NULL;
        return false;
    }
    t->stride = stride;
    t->mask = (uint32_t)(cap - 1);
    t->max = (uint32_t)max;
    t->head = t->tail = NIL;
    t->eq = eq;
    return true;
}

static void table_fini(table_t *t)
{
    free(t->slots);
    free(t->index);
}

static void lru_unlink(table_t *t, uint32_t s)
{
    link_t *l = lnk(t, s);
    if (l->prev != NIL) { lnk(t, l->prev)->next = l->next; } else { t->head = l->next; }
    if (l->next != NIL) { lnk(t, l->next)->prev = l->prev; } else { t->tail = l->prev; }
    l->prev = l->next = NIL;
}

static void lru_push_tail(table_t *t, uint32_t s)
{
    link_t *l = lnk(t, s);
    l->prev = t->tail;
    l->next = NIL;
    if (t->tail != NIL) { lnk(t, t->tail)->next = s; } else { t->head = s; }
    t->tail = s;
}

/* The index is at most half full, so an empty position always ends the probe. */
static uint32_t table_find(const table_t *t, uint32_t h, const void *key)
{
    for (uint32_t pos = h & t->mask;; pos = (pos + 1) & t->mask) {
        uint32_t v = t->index[pos];
        if (v == 0) { return NIL; }
        const link_t *l = lnk(t, v - 1);
        if (l->hash == h && t->eq(l, key)) {
            return v - 1;
        }
    }
}

/* Backward-shift deletion: no deleted-slot markers, so a probe never stops early at a gap. */
static void table_unindex(table_t *t, uint32_t s)
{
    uint32_t i = lnk(t, s)->hash & t->mask;
    while (t->index[i] != s + 1) { i = (i + 1) & t->mask; }
    t->index[i] = 0;
    for (uint32_t j = (i + 1) & t->mask; t->index[j] != 0; j = (j + 1) & t->mask) {
        uint32_t home = lnk(t, t->index[j] - 1)->hash & t->mask;
        bool stays = i <= j ? (home > i && home <= j) : (home > i || home <= j);
        if (stays) { continue; }
        t->index[i] = t->index[j];
        t->index[j] = 0;
        i = j;
    }
}

static uint32_t table_put(table_t *t, uint32_t h, const void *key)
{
    uint32_t s = table_find(t, h, key);
    if (s != NIL) {
        lru_unlink(t, s);
        lru_push_tail(t, s);
        return s;
    }
    if (t->count == t->max) {
        s = t->head;
        table_unindex(t, s);
        lru_unlink(t, s);
    } else {
        s = t->count++;
    }
    lnk(t, s)->hash = h;
    uint32_t pos = h & t->mask;
    while (t->index[pos] != 0) { pos = (pos + 1) & t->mask; }
    t->index[pos] = s + 1;
    lru_push_tail(t, s);
    return s;
}

static size_t table_probe_max(const table_t *t)
{
    size_t worst = 0, run = 0;
    for (uint64_t k = 0; k < 2 * ((uint64_t)t->mask + 1); k++) {
        if (t->index[k & t->mask] != 0) {
            run++;
            if (run > worst) { worst = run; }
        } else {
            run = 0;
        }
    }
    return worst < (size_t)t->mask + 1 ? worst : (size_t)t->mask + 1;
}

#define FNV_OFFSET 2166136261u
#define FNV_PRIME 16777619u

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

/* The length is part of the key: v4 and v6 can share their first four bytes. */
static uint32_t ip_hash(uint32_t h, const firc_ip_t *ip)
{
    h = fnv(h, &ip->len, 1);
    return fnv(h, ip->b, ip->len);
}

static bool ip_eq(const firc_ip_t *a, const firc_ip_t *b)
{
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

static bool name_key(const char *name, char out[NAME_CAP])
{
    size_t n = 0;
    for (; name != NULL && name[n] != '\0' && n < NAME_CAP - 1; n++) {
        char c = name[n];
        out[n] = c >= 'A' && c <= 'Z' ? (char)(c - 'A' + 'a') : c;
    }
    out[n] = '\0';
    return n > 0;
}

typedef struct pair_key {
    const firc_ip_t *client;
    const char *name;
    uint8_t family;
} pair_key_t;

static bool family_ok(unsigned family)
{
    return family == FIRC_RECALL_V4 || family == FIRC_RECALL_V6;
}

static bool addr_eq(const void *slot, const void *key)
{
    const addr_slot_t *s = slot;
    return ip_eq(&s->real, key);
}

static bool pair_eq(const void *slot, const void *key)
{
    const pair_slot_t *s = slot;
    const pair_key_t *k = key;
    return s->family == k->family && ip_eq(&s->client, k->client) && strcmp(s->name, k->name) == 0;
}

static uint32_t pair_hash(const pair_key_t *k)
{
    uint32_t h = fnv(ip_hash(FNV_OFFSET, k->client), &k->family, 1);
    return fnv(h, k->name, strlen(k->name));
}

firc_recall_t *firc_recall_new(size_t max_addrs, size_t max_pairs, int64_t horizon)
{
    firc_recall_t *r = calloc(1, sizeof(*r));
    if (r == NULL) { return NULL; }
    if (!table_init(&r->addrs, max_addrs, sizeof(addr_slot_t), addr_eq)) {
        free(r);
        return NULL;
    }
    if (!table_init(&r->pairs, max_pairs, sizeof(pair_slot_t), pair_eq)) {
        table_fini(&r->addrs);
        free(r);
        return NULL;
    }
    r->horizon = horizon;
    return r;
}

void firc_recall_free(firc_recall_t *r)
{
    if (r == NULL) { return; }
    table_fini(&r->addrs);
    table_fini(&r->pairs);
    free(r);
}

void firc_recall_real(firc_recall_t *r, const firc_ip_t *real, const char *name,
                      const char group_id[FIRC_ID_STR_LEN], int64_t now)
{
    (void)now;
    char key[NAME_CAP];
    if (r == NULL || !ip_ok(real) || !name_key(name, key)) { return; }
    uint32_t s = table_put(&r->addrs, ip_hash(FNV_OFFSET, real), real);
    addr_slot_t *slot = &((addr_slot_t *)r->addrs.slots)[s];
    slot->real = *real;
    memcpy(slot->name, key, sizeof(key));
    snprintf(slot->group, sizeof(slot->group), "%s", group_id != NULL ? group_id : "");
}

bool firc_recall_by_real(const firc_recall_t *r, const firc_ip_t *real, char *name_out,
                         size_t name_cap, char group_out[FIRC_ID_STR_LEN])
{
    if (r == NULL || !ip_ok(real)) { return false; }
    uint32_t s = table_find(&r->addrs, ip_hash(FNV_OFFSET, real), real);
    if (s == NIL) { return false; }
    const addr_slot_t *slot = &((const addr_slot_t *)r->addrs.slots)[s];
    if (name_out != NULL && name_cap > 0) { snprintf(name_out, name_cap, "%s", slot->name); }
    if (group_out != NULL) { memcpy(group_out, slot->group, FIRC_ID_STR_LEN); }
    return true;
}

void firc_recall_answer(firc_recall_t *r, const firc_ip_t *client, const char *name,
                        unsigned family, uint8_t decision, const firc_ip_t *fake, int64_t now)
{
    char name_k[NAME_CAP];
    if (r == NULL || !ip_ok(client) || !family_ok(family) || !name_key(name, name_k)) { return; }
    pair_key_t key = {client, name_k, (uint8_t)family};
    uint32_t s = table_put(&r->pairs, pair_hash(&key), &key);
    pair_slot_t *slot = &((pair_slot_t *)r->pairs.slots)[s];
    slot->client = *client;
    slot->family = (uint8_t)family;
    memcpy(slot->name, name_k, sizeof(name_k));
    slot->answer.at = now;
    slot->answer.decision = decision;
    if (ip_ok(fake) && fake->len == (family == FIRC_RECALL_V4 ? 4 : 16)) {
        slot->answer.fake = *fake;
    } else {
        memset(&slot->answer.fake, 0, sizeof(slot->answer.fake));
    }
}

bool firc_recall_last_answer(const firc_recall_t *r, const firc_ip_t *client, const char *name,
                             unsigned family, int64_t now, firc_recall_answer_t *out)
{
    char name_k[NAME_CAP];
    if (r == NULL || !ip_ok(client) || !family_ok(family) || !name_key(name, name_k)) {
        return false;
    }
    pair_key_t key = {client, name_k, (uint8_t)family};
    uint32_t s = table_find(&r->pairs, pair_hash(&key), &key);
    if (s == NIL) { return false; }
    const pair_slot_t *slot = &((const pair_slot_t *)r->pairs.slots)[s];
    if (now - slot->answer.at > r->horizon) { return false; }
    if (out != NULL) { *out = slot->answer; }
    return true;
}

size_t firc_recall_real_count(const firc_recall_t *r)
{
    return r != NULL ? r->addrs.count : 0;
}

size_t firc_recall_probe_max_for_test(const firc_recall_t *r)
{
    if (r == NULL) { return 0; }
    size_t a = table_probe_max(&r->addrs), p = table_probe_max(&r->pairs);
    return a > p ? a : p;
}
