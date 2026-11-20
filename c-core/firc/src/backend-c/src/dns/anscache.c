#include "firc/anscache.h"

#include <stdlib.h>
#include <string.h>

#define FLAG_CD 0x0010u
#define MALLOC_OVERHEAD 16u
#define MAX_TTL_WIRE 0x7fffffffu
#define MAX_TTLS (FIRC_ANSCACHE_MAX_ANSWER / 11u + 1u)

typedef struct entry {
    struct entry *newer;
    struct entry *older;
    struct entry *chain;
    uint32_t hash;
    firc_id_t group_id;
    uint64_t gen;
    uint16_t qtype;
    uint16_t qclass;
    uint8_t variant;
    uint8_t name_len;
    bool prefetching;
    uint16_t wire_len;
    uint16_t n_ttls;
    uint32_t ttl;
    uint64_t stored_ms;
    size_t cost;
    uint8_t data[];
} entry_t;

struct firc_anscache {
    entry_t **buckets;
    size_t mask;
    size_t max_entries;
    size_t max_bytes;
    size_t count;
    size_t bytes;
    entry_t *newest;
    entry_t *oldest;
};

static uint32_t fnv(uint32_t h, const uint8_t *p, size_t n)
{
    for (size_t i = 0; i < n; i++) { h = (h ^ p[i]) * 16777619u; }
    return h;
}

static uint32_t key_hash(const firc_anscache_key_t *k)
{
    uint8_t fixed[17];
    memcpy(fixed, k->group_id.b, 4);
    for (unsigned i = 0; i < 8; i++) { fixed[4 + i] = (uint8_t)(k->gen >> (8u * i)); }
    fixed[12] = (uint8_t)(k->qtype >> 8);
    fixed[13] = (uint8_t)k->qtype;
    fixed[14] = (uint8_t)(k->qclass >> 8);
    fixed[15] = (uint8_t)k->qclass;
    fixed[16] = k->variant;
    return fnv(fnv(2166136261u, fixed, sizeof(fixed)), k->name, k->name_len);
}

static bool key_eq(const entry_t *e, const firc_anscache_key_t *k)
{
    return memcmp(e->group_id.b, k->group_id.b, sizeof(k->group_id.b)) == 0 && e->gen == k->gen &&
           e->qtype == k->qtype && e->qclass == k->qclass && e->variant == k->variant &&
           e->name_len == k->name_len && memcmp(e->data, k->name, k->name_len) == 0;
}

static uint8_t fold(uint8_t c)
{
    return (c >= 'A' && c <= 'Z') ? (uint8_t)(c + 32) : c;
}

static bool name_eq_fold(const uint8_t *wire, const uint8_t *lower, size_t len)
{
    for (size_t i = 0; i < len; i++) {
        if (fold(wire[i]) != lower[i]) { return false; }
    }
    return true;
}

static uint32_t rd32(const uint8_t *p)
{
    return (uint32_t)p[0] << 24 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 8 | p[3];
}

static void wr32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)v;
}

static bool skip_name(const uint8_t *w, size_t len, size_t *off)
{
    size_t o = *off;
    for (;;) {
        if (o >= len) { return false; }
        uint8_t b = w[o];
        if ((b & 0xc0u) == 0xc0u) {
            if (o + 2 > len) { return false; }
            *off = o + 2;
            return true;
        }
        if ((b & 0xc0u) != 0) { return false; }
        o += 1u + b;
        if (b == 0) {
            *off = o;
            return true;
        }
    }
}

static bool walk(const uint8_t *w, size_t len, uint16_t *offs, size_t *n_out, size_t *keep_len)
{
    size_t qd = (size_t)w[4] << 8 | w[5];
    size_t rr = ((size_t)w[6] << 8 | w[7]) + ((size_t)w[8] << 8 | w[9]) + ((size_t)w[10] << 8 | w[11]);
    size_t off = FIRC_DNS_HEADER_LEN;
    size_t n = 0;
    *keep_len = len;
    for (size_t i = 0; i < qd; i++) {
        if (!skip_name(w, len, &off) || off + 4 > len) { return false; }
        off += 4;
    }
    for (size_t i = 0; i < rr; i++) {
        if (!skip_name(w, len, &off) || off + 10 > len) { return false; }
        uint16_t type = (uint16_t)((unsigned)w[off] << 8 | w[off + 1]);
        size_t rdlen = (size_t)w[off + 8] << 8 | w[off + 9];
        size_t end = off + 10 + rdlen;
        if (end > len) { return false; }
        if (type == FIRC_DNS_TYPE_OPT) {
            if (rdlen != 0) {
                if (i + 1 != rr || end != len) { return false; }
                *keep_len = off + 10;
            }
        } else {
            if (n == MAX_TTLS) { return false; }
            offs[n++] = (uint16_t)(off + 4);
        }
        off = end;
    }
    if (off != len) { return false; }
    *n_out = n;
    return true;
}

static void detach_lru(firc_anscache_t *c, entry_t *e)
{
    if (e->newer != NULL) {
        e->newer->older = e->older;
    } else {
        c->newest = e->older;
    }
    if (e->older != NULL) {
        e->older->newer = e->newer;
    } else {
        c->oldest = e->newer;
    }
}

static void push_newest(firc_anscache_t *c, entry_t *e)
{
    e->newer = NULL;
    e->older = c->newest;
    if (c->newest != NULL) {
        c->newest->newer = e;
    } else {
        c->oldest = e;
    }
    c->newest = e;
}

static void drop(firc_anscache_t *c, entry_t *e)
{
    entry_t **pp = &c->buckets[e->hash & c->mask];
    while (*pp != NULL && *pp != e) { pp = &(*pp)->chain; }
    if (*pp != NULL) { *pp = e->chain; }
    detach_lru(c, e);
    c->count--;
    c->bytes -= e->cost;
    free(e);
}

static entry_t *find(const firc_anscache_t *c, const firc_anscache_key_t *k, uint32_t h)
{
    for (entry_t *e = c->buckets[h & c->mask]; e != NULL; e = e->chain) {
        if (e->hash == h && key_eq(e, k)) { return e; }
    }
    return NULL;
}

firc_anscache_t *firc_anscache_new(size_t max_entries, size_t max_bytes)
{
    if (max_entries == 0 || max_bytes == 0 || max_entries > (1u << 24)) { return NULL; }
    firc_anscache_t *c = calloc(1, sizeof(*c));
    if (c == NULL) { return NULL; }
    size_t n = 1;
    while (n < max_entries) { n <<= 1; }
    c->buckets = calloc(n, sizeof(*c->buckets));
    if (c->buckets == NULL) {
        free(c);
        return NULL;
    }
    c->mask = n - 1;
    c->max_entries = max_entries;
    c->max_bytes = max_bytes;
    return c;
}

void firc_anscache_free(firc_anscache_t *c)
{
    if (c == NULL) { return; }
    entry_t *e = c->oldest;
    while (e != NULL) {
        entry_t *next = e->newer;
        free(e);
        e = next;
    }
    free(c->buckets);
    free(c);
}

bool firc_anscache_key_of(const firc_dns_msg_t *query, firc_id_t group_id, uint64_t gen, firc_anscache_key_t *out)
{
    memset(out, 0, sizeof(*out));
    if (query->n_questions != 1 || query->questions[0].name_len == 0 ||
        query->questions[0].name_len > FIRC_DNS_MAX_NAME) {
        return false;
    }
    const firc_dns_question_t *q = &query->questions[0];
    out->group_id = group_id;
    out->gen = gen;
    out->qtype = q->qtype;
    out->qclass = q->qclass;
    out->name_len = (uint8_t)q->name_len;
    for (size_t i = 0; i < q->name_len; i++) { out->name[i] = fold(q->name[i]); }
    if ((query->flags & FLAG_CD) != 0) { out->variant |= FIRC_ANSCACHE_CD; }
    for (size_t i = 0; i < query->n_additional; i++) {
        if (query->additional[i].rtype != FIRC_DNS_TYPE_OPT) { continue; }
        out->variant |= FIRC_ANSCACHE_EDNS;
        if ((query->additional[i].ttl & 0x8000u) != 0) { out->variant |= FIRC_ANSCACHE_DO; }
    }
    return true;
}

static uint32_t negative_lifetime(const firc_dns_msg_t *m)
{
    for (size_t i = 0; i < m->n_authority; i++) {
        const firc_dns_rr_t *rr = &m->authority[i];
        if (rr->rtype != FIRC_DNS_TYPE_SOA || rr->rdata_len < 22) { continue; }
        uint32_t minimum = rd32(rr->rdata + rr->rdata_len - 4);
        uint32_t rttl = rr->ttl > MAX_TTL_WIRE ? 0 : rr->ttl;
        uint32_t ttl = rttl < minimum ? rttl : minimum;
        return ttl < FIRC_ANSCACHE_MAX_NEGATIVE_TTL ? ttl : FIRC_ANSCACHE_MAX_NEGATIVE_TTL;
    }
    return 0;
}

uint32_t firc_anscache_lifetime(const firc_dns_msg_t *answer)
{
    if ((answer->flags & FIRC_DNS_FLAG_TC) != 0) { return 0; }
    unsigned rcode = answer->flags & 0x0fu;
    if (rcode == FIRC_DNS_RCODE_NXDOMAIN || (rcode == 0 && answer->n_answers == 0)) {
        return negative_lifetime(answer);
    }
    if (rcode != 0) { return 0; }
    uint32_t ttl = FIRC_ANSCACHE_MAX_TTL;
    for (size_t i = 0; i < answer->n_answers; i++) {
        uint32_t t = answer->answers[i].ttl > MAX_TTL_WIRE ? 0 : answer->answers[i].ttl;
        if (t < ttl) { ttl = t; }
    }
    return ttl;
}

bool firc_anscache_put(firc_anscache_t *c, const firc_anscache_key_t *key, const uint8_t *wire, size_t len,
                       uint32_t ttl, uint64_t now_ms)
{
    if (c == NULL || ttl == 0 || len > FIRC_ANSCACHE_MAX_ANSWER ||
        len < FIRC_DNS_HEADER_LEN + (size_t)key->name_len + 4u) {
        return false;
    }
    if (wire[4] != 0 || wire[5] != 1 || !name_eq_fold(wire + FIRC_DNS_HEADER_LEN, key->name, key->name_len)) {
        return false;
    }
    const uint8_t *qt = wire + FIRC_DNS_HEADER_LEN + key->name_len;
    if ((uint16_t)((unsigned)qt[0] << 8 | qt[1]) != key->qtype ||
        (uint16_t)((unsigned)qt[2] << 8 | qt[3]) != key->qclass) {
        return false;
    }
    uint16_t offs[MAX_TTLS];
    size_t n = 0;
    size_t keep = len;
    if (!walk(wire, len, offs, &n, &keep)) { return false; }
    size_t need = sizeof(entry_t) + key->name_len + keep + n * sizeof(uint16_t);
    size_t cost = need + MALLOC_OVERHEAD;
    if (cost > c->max_bytes) { return false; }
    entry_t *e = malloc(need);
    if (e == NULL) { return false; }
    memset(e, 0, sizeof(*e));
    e->hash = key_hash(key);
    e->group_id = key->group_id;
    e->gen = key->gen;
    e->qtype = key->qtype;
    e->qclass = key->qclass;
    e->variant = key->variant;
    e->name_len = key->name_len;
    e->wire_len = (uint16_t)keep;
    e->n_ttls = (uint16_t)n;
    e->ttl = ttl < FIRC_ANSCACHE_MAX_TTL ? ttl : FIRC_ANSCACHE_MAX_TTL;
    e->stored_ms = now_ms;
    e->cost = cost;
    uint8_t *w = e->data + key->name_len;
    memcpy(e->data, key->name, key->name_len);
    memcpy(w, wire, keep);
    if (keep < len) {
        w[keep - 2] = 0;
        w[keep - 1] = 0;
    }
    memcpy(w + keep, offs, n * sizeof(uint16_t));
    entry_t *old = find(c, key, e->hash);
    if (old != NULL) { drop(c, old); }
    while (c->oldest != NULL && (c->count >= c->max_entries || c->bytes + cost > c->max_bytes)) {
        drop(c, c->oldest);
    }
    e->chain = c->buckets[e->hash & c->mask];
    c->buckets[e->hash & c->mask] = e;
    push_newest(c, e);
    c->count++;
    c->bytes += cost;
    return true;
}

bool firc_anscache_get(firc_anscache_t *c, const firc_anscache_key_t *key, const uint8_t *query, size_t query_len,
                       uint64_t now_ms, firc_anscache_hit_t *out)
{
    memset(out, 0, sizeof(*out));
    if (c == NULL || query_len < FIRC_DNS_HEADER_LEN + (size_t)key->name_len ||
        !name_eq_fold(query + FIRC_DNS_HEADER_LEN, key->name, key->name_len)) {
        return false;
    }
    entry_t *e = find(c, key, key_hash(key));
    if (e == NULL) { return false; }
    uint64_t age_ms = now_ms > e->stored_ms ? now_ms - e->stored_ms : 0;
    uint64_t life_ms = (uint64_t)e->ttl * 1000u;
    if (age_ms >= life_ms) {
        drop(c, e);
        return false;
    }
    uint8_t *w = malloc(e->wire_len);
    if (w == NULL) { return false; }
    const uint8_t *src = e->data + e->name_len;
    memcpy(w, src, e->wire_len);
    w[0] = query[0];
    w[1] = query[1];
    w[2] = (uint8_t)((w[2] & 0xfeu) | (query[2] & 0x01u));
    memcpy(w + FIRC_DNS_HEADER_LEN, query + FIRC_DNS_HEADER_LEN, e->name_len);
    uint32_t age_s = (uint32_t)(age_ms / 1000u);
    const uint8_t *offs = src + e->wire_len;
    for (size_t i = 0; i < e->n_ttls; i++) {
        uint16_t o;
        memcpy(&o, offs + i * sizeof(uint16_t), sizeof(o));
        uint32_t t = rd32(w + o);
        wr32(w + o, t > age_s ? t - age_s : 0);
    }
    out->wire = w;
    out->len = e->wire_len;
    uint64_t left_ms = life_ms - age_ms;
    if (!e->prefetching && e->ttl >= FIRC_ANSCACHE_PREFETCH_MIN_TTL && left_ms * 10u < life_ms) {
        e->prefetching = true;
        out->prefetch = true;
    }
    detach_lru(c, e);
    push_newest(c, e);
    return true;
}

void firc_anscache_prefetch_done(firc_anscache_t *c, const firc_anscache_key_t *key)
{
    if (c == NULL) { return; }
    entry_t *e = find(c, key, key_hash(key));
    if (e != NULL) { e->prefetching = false; }
}

void firc_anscache_drop_group(firc_anscache_t *c, firc_id_t group_id)
{
    if (c == NULL) { return; }
    entry_t *e = c->oldest;
    while (e != NULL) {
        entry_t *next = e->newer;
        if (memcmp(e->group_id.b, group_id.b, sizeof(group_id.b)) == 0) { drop(c, e); }
        e = next;
    }
}

size_t firc_anscache_count(const firc_anscache_t *c)
{
    return c == NULL ? 0 : c->count;
}

size_t firc_anscache_bytes(const firc_anscache_t *c)
{
    return c == NULL ? 0 : c->bytes;
}
