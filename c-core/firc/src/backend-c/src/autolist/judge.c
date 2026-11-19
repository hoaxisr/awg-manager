#include "firc/autolist.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/regdomain.h"

typedef struct {
    bool used;
    bool judged;
    bool bypass;
    bool ambiguous;
    bool finned;
    bool syn;
    bool domain_named;
    uint8_t sent;
    uint32_t hash;
    uint32_t seq;
    uint32_t ack0;
    int64_t seen;
    int64_t hello_at;
    int64_t syn_at;
    int32_t host;
    uint32_t host_serial;
    firc_autolist_flow_t key;
    char sni[FIRC_AUTOLIST_NAME_MAX];
} flow_t;

#define BAD_ADDRS 8
#define DOMAIN_HOSTS 8

typedef struct {
    uint32_t n;
    int64_t last;
} tally_t;

typedef struct {
    uint8_t n;
    int64_t at[FIRC_AUTOLIST_NOTES];
} notes_t;

typedef struct {
    bool used;
    uint32_t hash;
    uint32_t serial;
    tally_t fails;
    tally_t no_connect;
    int64_t seen;
    firc_ip_t bad[BAD_ADDRS];
    uint8_t n_bad;
    uint8_t next_bad;
    notes_t notes;
    char name[FIRC_AUTOLIST_NAME_MAX];
} host_t;

typedef enum {
    KIND_FAIL = 0,
    KIND_NO_CONNECT,
    KIND_NOTE,
    KINDS,
} kind_t;

typedef struct {
    int32_t host;
    uint32_t serial;
    uint32_t n[KINDS];
} domain_host_t;

typedef struct {
    bool used;
    uint32_t hash;
    uint32_t serial;
    int64_t seen;
    tally_t fails;
    tally_t no_connect;
    notes_t notes;
    uint32_t hostless[KINDS];
    domain_host_t hosts[DOMAIN_HOSTS];
    uint8_t n_hosts;
    char name[FIRC_AUTOLIST_NAME_MAX];
} domain_t;

#define NIL UINT16_MAX

typedef struct {
    uint16_t prev, next;
    uint32_t hash;
} link_t;

typedef struct {
    unsigned char *slots;
    size_t stride;
    uint16_t *index;
    uint32_t mask;
    uint16_t max, count, head, tail;
} lru_t;

typedef struct {
    link_t l;
    int64_t at;
    firc_ip_t client, real;
    char name[FIRC_AUTOLIST_NAME_MAX];
} hint_t;

typedef struct {
    link_t l;
    int64_t at;
    firc_ip_t client, server;
    uint16_t client_port, port;
    uint8_t proto;
} seen_t;

#define ROWS (2 * FIRC_AUTOLIST_ADDRS)
#define NO_ROW SIZE_MAX

enum { QUOTA_FAILED = 0, QUOTA_BYPASS = 1 };

typedef struct {
    firc_ip_t server;
    uint8_t proto;
    uint16_t port;
    int64_t until;
} hidden_t;

struct firc_autolist {
    firc_autolist_params_t p;
    uint32_t serial;
    flow_t flows[FIRC_AUTOLIST_FLOWS];
    host_t hosts[FIRC_AUTOLIST_HOSTS];
    domain_t domains[FIRC_AUTOLIST_DOMAINS];
    hint_t hints[FIRC_AUTOLIST_HINTS];
    uint16_t hint_index[2 * FIRC_AUTOLIST_HINTS];
    lru_t hint_lru;
    seen_t seen[FIRC_AUTOLIST_FLOWS];
    uint16_t seen_index[2 * FIRC_AUTOLIST_FLOWS];
    lru_t seen_lru;
    firc_autolist_addr_t addr[ROWS];
    bool addr_used[ROWS];
    hidden_t hidden[ROWS];
    size_t hidden_next;
    int64_t day_hour[FIRC_AUTOLIST_DAY_HOURS];
    uint32_t day_count[FIRC_AUTOLIST_DAY_HOURS];
    int64_t ambiguous_hour[FIRC_AUTOLIST_DAY_HOURS];
    uint32_t ambiguous_count[FIRC_AUTOLIST_DAY_HOURS];
};

#define FNV_BASIS 2166136261u
#define FNV_PRIME 16777619u

static uint32_t fnv(uint32_t h, const void *p, size_t n) {
    const uint8_t *b = p;
    for (size_t i = 0; i < n; i++) {
        h ^= b[i];
        h *= FNV_PRIME;
    }
    return h;
}

static uint32_t ip_hash(uint32_t h, const firc_ip_t *ip) {
    h = fnv(h, &ip->len, 1);
    return fnv(h, ip->b, ip->len <= 16 ? ip->len : 16);
}

static bool ip_eq(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && a->len <= 16 && memcmp(a->b, b->b, a->len) == 0;
}

static bool in_net(const uint8_t *b, unsigned len, const uint8_t *base, unsigned prefix) {
    if (prefix > len * 8u) { return false; }
    unsigned whole = prefix / 8u, rest = prefix % 8u;
    if (memcmp(b, base, whole) != 0) { return false; }
    if (rest == 0) { return true; }
    uint8_t mask = (uint8_t)(0xffu << (8u - rest));
    return (b[whole] & mask) == (base[whole] & mask);
}

static bool local_v4(const uint8_t *b) {
    static const struct {
        uint8_t base[4];
        uint8_t prefix;
    } nets[] = {{{10, 0, 0, 0}, 8},    {{172, 16, 0, 0}, 12}, {{192, 168, 0, 0}, 16}, {{169, 254, 0, 0}, 16},
                {{127, 0, 0, 0}, 8},   {{100, 64, 0, 0}, 10}, {{198, 18, 0, 0}, 15}, {{0, 0, 0, 0}, 8}};
    for (size_t i = 0; i < sizeof(nets) / sizeof(nets[0]); i++) {
        if (in_net(b, 4, nets[i].base, nets[i].prefix)) { return true; }
    }
    return false;
}

bool firc_autolist_local(const firc_ip_t *ip, const firc_autolist_net_t *pools, size_t n_pools) {
    if (ip == NULL) { return false; }
    for (size_t i = 0; pools != NULL && i < n_pools; i++) {
        if (pools[i].base.len == ip->len && in_net(ip->b, ip->len, pools[i].base.b, pools[i].prefix)) { return true; }
    }
    if (ip->len == 4) { return local_v4(ip->b); }
    if (ip->len != 16) { return false; }
    static const uint8_t mapped[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff};
    static const uint8_t zero[15] = {0};
    static const uint8_t ula[1] = {0xfc}, link[2] = {0xfe, 0x80};
    if (memcmp(ip->b, mapped, sizeof(mapped)) == 0) { return local_v4(ip->b + 12); }
    if (memcmp(ip->b, zero, sizeof(zero)) == 0 && ip->b[15] <= 1) { return true; }
    return in_net(ip->b, 16, ula, 7) || in_net(ip->b, 16, link, 10);
}

static uint32_t flow_hash(const firc_autolist_flow_t *f) {
    uint32_t h = ip_hash(FNV_BASIS, &f->client);
    h = ip_hash(h, &f->server);
    h = fnv(h, &f->client_port, sizeof(f->client_port));
    return fnv(h, &f->server_port, sizeof(f->server_port));
}

static bool flow_eq(const firc_autolist_flow_t *a, const firc_autolist_flow_t *b) {
    return a->client_port == b->client_port && a->server_port == b->server_port && ip_eq(&a->client, &b->client) &&
           ip_eq(&a->server, &b->server);
}

static uint32_t name_hash(const char *s) { return fnv(FNV_BASIS, s, strlen(s)); }

static link_t *lnk(const lru_t *t, uint16_t s) { return (link_t *)(void *)(t->slots + (size_t)s * t->stride); }

static void lru_init(lru_t *t, void *slots, size_t stride, uint16_t max, uint16_t *index, size_t index_len) {
    t->slots = slots;
    t->stride = stride;
    t->max = max;
    t->index = index;
    t->mask = (uint32_t)(index_len - 1u);
    t->count = 0;
    t->head = t->tail = NIL;
}

static void lru_unlink(lru_t *t, uint16_t s) {
    link_t *l = lnk(t, s);
    if (l->prev != NIL) {
        lnk(t, l->prev)->next = l->next;
    } else {
        t->head = l->next;
    }
    if (l->next != NIL) {
        lnk(t, l->next)->prev = l->prev;
    } else {
        t->tail = l->prev;
    }
    l->prev = l->next = NIL;
}

static void lru_push_tail(lru_t *t, uint16_t s) {
    link_t *l = lnk(t, s);
    l->prev = t->tail;
    l->next = NIL;
    if (t->tail != NIL) {
        lnk(t, t->tail)->next = s;
    } else {
        t->head = s;
    }
    t->tail = s;
}

static void lru_touch(lru_t *t, uint16_t s) {
    lru_unlink(t, s);
    lru_push_tail(t, s);
}

static void lru_unindex(lru_t *t, uint16_t s) {
    uint32_t i = lnk(t, s)->hash & t->mask;
    while (t->index[i] != s + 1u) { i = (i + 1u) & t->mask; }
    t->index[i] = 0;
    for (uint32_t j = (i + 1u) & t->mask; t->index[j] != 0; j = (j + 1u) & t->mask) {
        uint32_t home = lnk(t, (uint16_t)(t->index[j] - 1u))->hash & t->mask;
        bool stays = i <= j ? (home > i && home <= j) : (home > i || home <= j);
        if (stays) { continue; }
        t->index[i] = t->index[j];
        t->index[j] = 0;
        i = j;
    }
}

static uint16_t lru_add(lru_t *t, uint32_t h) {
    uint16_t s;
    if (t->count < t->max) {
        s = t->count++;
    } else {
        s = t->head;
        lru_unindex(t, s);
        lru_unlink(t, s);
    }
    memset(lnk(t, s), 0, t->stride);
    lnk(t, s)->hash = h;
    uint32_t pos = h & t->mask;
    while (t->index[pos] != 0) { pos = (pos + 1u) & t->mask; }
    t->index[pos] = (uint16_t)(s + 1u);
    lru_push_tail(t, s);
    return s;
}

static uint16_t lru_next(const lru_t *t, uint32_t h, uint32_t *pos) {
    for (; t->index[*pos] != 0; *pos = (*pos + 1u) & t->mask) {
        uint16_t s = (uint16_t)(t->index[*pos] - 1u);
        if (lnk(t, s)->hash == h) {
            *pos = (*pos + 1u) & t->mask;
            return s;
        }
    }
    return NIL;
}

static const firc_autolist_params_t defaults = {
    FIRC_AUTOLIST_FAIL_THRESHOLD,
    FIRC_AUTOLIST_FAIL_SECS,
    FIRC_AUTOLIST_RETRANS_THRESHOLD,
};

firc_autolist_t *firc_autolist_new(void) {
    firc_autolist_t *a = calloc(1, sizeof(*a));
    if (a == NULL) { return NULL; }
    a->p = defaults;
    lru_init(&a->hint_lru, a->hints, sizeof(a->hints[0]), FIRC_AUTOLIST_HINTS, a->hint_index,
             sizeof(a->hint_index) / sizeof(a->hint_index[0]));
    lru_init(&a->seen_lru, a->seen, sizeof(a->seen[0]), FIRC_AUTOLIST_FLOWS, a->seen_index,
             sizeof(a->seen_index) / sizeof(a->seen_index[0]));
    return a;
}

void firc_autolist_free(firc_autolist_t *a) { free(a); }

void firc_autolist_set_params(firc_autolist_t *a, const firc_autolist_params_t *p) {
    if (a == NULL || p == NULL) { return; }
    a->p.fail_threshold = p->fail_threshold ? p->fail_threshold : defaults.fail_threshold;
    a->p.fail_secs = p->fail_secs ? p->fail_secs : defaults.fail_secs;
    a->p.retrans_threshold = p->retrans_threshold ? p->retrans_threshold : defaults.retrans_threshold;
}

static bool tally_live(const firc_autolist_t *a, const tally_t *t, int64_t now) {
    return t->n > 0 && now - t->last <= (int64_t)a->p.fail_secs;
}

static uint32_t tally_add(const firc_autolist_t *a, tally_t *t, int64_t now) {
    if (!tally_live(a, t, now)) { t->n = 0; }
    t->n++;
    t->last = now;
    return t->n;
}

static bool notes_live(const notes_t *n, int64_t now) {
    return n->n > 0 && now - n->at[n->n - 1u] <= FIRC_AUTOLIST_NOTE_SECS;
}

static uint8_t notes_add(notes_t *n, int64_t now) {
    uint8_t kept = 0;
    for (uint8_t i = 0; i < n->n; i++) {
        if (now - n->at[i] <= FIRC_AUTOLIST_NOTE_SECS) { n->at[kept++] = n->at[i]; }
    }
    if (kept == FIRC_AUTOLIST_NOTES) {
        memmove(n->at, n->at + 1, sizeof(n->at[0]) * (FIRC_AUTOLIST_NOTES - 1u));
        kept--;
    }
    n->at[kept++] = now;
    n->n = kept;
    return kept;
}

static bool host_counting(const firc_autolist_t *a, const host_t *h, int64_t now) {
    return h->used && (tally_live(a, &h->fails, now) || tally_live(a, &h->no_connect, now));
}

static bool domain_counting(const firc_autolist_t *a, const domain_t *d, int64_t now) {
    return d->used &&
           (tally_live(a, &d->fails, now) || tally_live(a, &d->no_connect, now) || notes_live(&d->notes, now));
}

static uint32_t pair_hash(const firc_ip_t *client, const firc_ip_t *real) {
    return ip_hash(ip_hash(FNV_BASIS, client), real);
}

static const hint_t *hint_next(const firc_autolist_t *a, uint32_t h, uint32_t *pos, const firc_ip_t *client,
                               const firc_ip_t *real, int64_t now) {
    for (uint16_t s = lru_next(&a->hint_lru, h, pos); s != NIL; s = lru_next(&a->hint_lru, h, pos)) {
        const hint_t *e = &a->hints[s];
        if (now - e->at <= FIRC_AUTOLIST_HINT_SECS && ip_eq(&e->client, client) && ip_eq(&e->real, real)) { return e; }
    }
    return NULL;
}

static uint16_t hint_find(const firc_autolist_t *a, uint32_t h, const firc_ip_t *client, const firc_ip_t *real,
                          const char *name) {
    uint32_t pos = h & a->hint_lru.mask;
    for (uint16_t s = lru_next(&a->hint_lru, h, &pos); s != NIL; s = lru_next(&a->hint_lru, h, &pos)) {
        const hint_t *e = &a->hints[s];
        if (ip_eq(&e->client, client) && ip_eq(&e->real, real) && strcmp(e->name, name) == 0) { return s; }
    }
    return NIL;
}

void firc_autolist_hint(firc_autolist_t *a, const firc_ip_t *client, const char *name, const firc_ip_t *reals,
                        size_t n_reals, int64_t now) {
    if (a == NULL || client == NULL || name == NULL || reals == NULL) { return; }
    size_t len = strlen(name);
    if (len == 0 || len >= FIRC_AUTOLIST_NAME_MAX) { return; }
    for (size_t r = 0; r < n_reals; r++) {
        uint32_t h = pair_hash(client, &reals[r]);
        uint16_t s = hint_find(a, h, client, &reals[r], name);
        if (s != NIL) {
            lru_touch(&a->hint_lru, s);
        } else {
            s = lru_add(&a->hint_lru, h);
            a->hints[s].client = *client;
            a->hints[s].real = reals[r];
            memcpy(a->hints[s].name, name, len + 1);
        }
        a->hints[s].at = now;
    }
}

static const char *hint_named(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *real,
                              const char *name, int64_t now) {
    uint32_t h = pair_hash(client, real);
    uint32_t pos = h & a->hint_lru.mask;
    for (const hint_t *e = hint_next(a, h, &pos, client, real, now); e != NULL;
         e = hint_next(a, h, &pos, client, real, now)) {
        if (strcmp(e->name, name) == 0) { return e->name; }
    }
    return NULL;
}

bool firc_autolist_hinted(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server, int64_t now) {
    if (a == NULL || client == NULL || server == NULL) { return false; }
    uint32_t h = pair_hash(client, server);
    uint32_t pos = h & a->hint_lru.mask;
    return hint_next(a, h, &pos, client, server, now) != NULL;
}

bool firc_autolist_hinted_name(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server,
                               const char *name, int64_t now) {
    if (a == NULL || client == NULL || server == NULL || name == NULL) { return false; }
    return hint_named(a, client, server, name, now) != NULL;
}

static uint32_t seen_hash(const firc_ip_t *client, uint16_t client_port, const firc_ip_t *server, uint8_t proto,
                          uint16_t port) {
    uint32_t h = pair_hash(client, server);
    h = fnv(h, &client_port, sizeof(client_port));
    h = fnv(h, &proto, sizeof(proto));
    return fnv(h, &port, sizeof(port));
}

bool firc_autolist_flow_first(firc_autolist_t *a, const firc_ip_t *client, uint16_t client_port,
                              const firc_ip_t *server, uint8_t proto, uint16_t port, int64_t now) {
    if (a == NULL || client == NULL || server == NULL) { return false; }
    uint32_t h = seen_hash(client, client_port, server, proto, port);
    uint32_t pos = h & a->seen_lru.mask;
    for (uint16_t s = lru_next(&a->seen_lru, h, &pos); s != NIL; s = lru_next(&a->seen_lru, h, &pos)) {
        seen_t *e = &a->seen[s];
        if (e->client_port != client_port || e->port != port || e->proto != proto || !ip_eq(&e->client, client) ||
            !ip_eq(&e->server, server)) {
            continue;
        }
        bool first = now - e->at > FIRC_AUTOLIST_FLOW_SECS;
        e->at = now;
        lru_touch(&a->seen_lru, s);
        return first;
    }
    uint16_t s = lru_add(&a->seen_lru, h);
    seen_t *e = &a->seen[s];
    e->at = now;
    e->client = *client;
    e->server = *server;
    e->client_port = client_port;
    e->port = port;
    e->proto = proto;
    return true;
}

typedef enum {
    HINT_NONE = 0,
    HINT_ONE,
    HINT_DOMAIN,
    HINT_MANY,
} hint_count_t;

static hint_count_t hint_only(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *real, int64_t now,
                              const char **name, char *domain, size_t cap) {
    uint32_t h = pair_hash(client, real);
    uint32_t pos = h & a->hint_lru.mask;
    const char *first = NULL;
    bool several = false;
    *name = NULL;
    for (const hint_t *e = hint_next(a, h, &pos, client, real, now); e != NULL;
         e = hint_next(a, h, &pos, client, real, now)) {
        if (first == NULL) {
            first = e->name;
            continue;
        }
        if (!several) {
            several = true;
            if (!firc_regdomain(first, domain, cap) || firc_regdomain_shared(domain)) { return HINT_MANY; }
        }
        char other[FIRC_AUTOLIST_NAME_MAX];
        if (!firc_regdomain(e->name, other, sizeof(other)) || strcmp(other, domain) != 0) { return HINT_MANY; }
    }
    if (first == NULL) { return HINT_NONE; }
    if (several) { return HINT_DOMAIN; }
    *name = first;
    return HINT_ONE;
}

static int32_t host_take(firc_autolist_t *a, const char *name, int64_t now) {
    size_t len = strlen(name);
    if (len == 0 || len >= FIRC_AUTOLIST_NAME_MAX) { return -1; }
    uint32_t h = name_hash(name);
    int32_t free_at = -1, idle = -1, oldest = -1;
    for (size_t i = 0; i < FIRC_AUTOLIST_HOSTS; i++) {
        host_t *e = &a->hosts[i];
        if (e->used && e->hash == h && strcmp(e->name, name) == 0) {
            e->seen = now;
            return (int32_t)i;
        }
        if (!e->used) {
            if (free_at < 0) { free_at = (int32_t)i; }
        } else if (!host_counting(a, e, now)) {
            if (idle < 0 || e->seen < a->hosts[idle].seen) { idle = (int32_t)i; }
        } else if (oldest < 0 || e->seen < a->hosts[oldest].seen) {
            oldest = (int32_t)i;
        }
    }
    int32_t at = free_at >= 0 ? free_at : idle >= 0 ? idle : oldest;
    if (at < 0) { return -1; }
    host_t *e = &a->hosts[at];
    memset(e, 0, sizeof(*e));
    e->used = true;
    e->hash = h;
    e->serial = ++a->serial;
    e->seen = now;
    memcpy(e->name, name, len + 1);
    return at;
}

static host_t *host_of(firc_autolist_t *a, const flow_t *f) {
    if (f->host < 0 || f->host >= FIRC_AUTOLIST_HOSTS) { return NULL; }
    host_t *h = &a->hosts[f->host];
    return h->used && h->serial == f->host_serial ? h : NULL;
}

static flow_t *flow_find(firc_autolist_t *a, const firc_autolist_flow_t *key, uint32_t hash, int64_t now) {
    for (size_t i = 0; i < FIRC_AUTOLIST_FLOWS; i++) {
        flow_t *e = &a->flows[i];
        if (e->used && e->hash == hash && now - e->seen <= FIRC_AUTOLIST_FLOW_SECS && flow_eq(&e->key, key)) {
            return e;
        }
    }
    return NULL;
}

static flow_t *flow_slot(firc_autolist_t *a, int64_t now) {
    flow_t *oldest = NULL;
    for (size_t i = 0; i < FIRC_AUTOLIST_FLOWS; i++) {
        flow_t *e = &a->flows[i];
        if (!e->used || now - e->seen > FIRC_AUTOLIST_FLOW_SECS) { return e; }
        if (oldest == NULL || e->seen < oldest->seen) { oldest = e; }
    }
    return oldest;
}

static void host_restart(host_t *h) {
    h->fails.n = 0;
    h->n_bad = 0;
    h->next_bad = 0;
}

static void domain_restart(domain_t *d) {
    memset(&d->fails, 0, sizeof(d->fails));
    memset(&d->no_connect, 0, sizeof(d->no_connect));
    memset(&d->notes, 0, sizeof(d->notes));
    memset(d->hostless, 0, sizeof(d->hostless));
    d->n_hosts = 0;
}

static host_t *domain_host(firc_autolist_t *a, const domain_host_t *e) {
    if (e->host < 0 || e->host >= FIRC_AUTOLIST_HOSTS) { return NULL; }
    host_t *h = &a->hosts[e->host];
    return h->used && h->serial == e->serial ? h : NULL;
}

static domain_t *domain_find(firc_autolist_t *a, const char *host, bool take, int64_t now) {
    char name[FIRC_AUTOLIST_NAME_MAX];
    if (!firc_regdomain(host, name, sizeof(name)) || firc_regdomain_shared(name)) { return NULL; }
    uint32_t h = name_hash(name);
    int32_t free_at = -1, idle = -1, oldest = -1;
    for (size_t i = 0; i < FIRC_AUTOLIST_DOMAINS; i++) {
        domain_t *e = &a->domains[i];
        if (e->used && e->hash == h && strcmp(e->name, name) == 0) {
            e->seen = now;
            return e;
        }
        if (!e->used) {
            if (free_at < 0) { free_at = (int32_t)i; }
        } else if (!domain_counting(a, e, now)) {
            if (idle < 0 || e->seen < a->domains[idle].seen) { idle = (int32_t)i; }
        } else if (oldest < 0 || e->seen < a->domains[oldest].seen) {
            oldest = (int32_t)i;
        }
    }
    if (!take) { return NULL; }
    int32_t at = free_at >= 0 ? free_at : idle >= 0 ? idle : oldest;
    if (at < 0) { return NULL; }
    domain_t *e = &a->domains[at];
    memset(e, 0, sizeof(*e));
    e->used = true;
    e->hash = h;
    e->serial = ++a->serial;
    e->seen = now;
    memcpy(e->name, name, strlen(name) + 1);
    return e;
}

static void domain_restart_kind(domain_t *d, kind_t kind) {
    if (kind == KIND_FAIL) {
        memset(&d->fails, 0, sizeof(d->fails));
    } else if (kind == KIND_NO_CONNECT) {
        memset(&d->no_connect, 0, sizeof(d->no_connect));
    } else {
        memset(&d->notes, 0, sizeof(d->notes));
    }
    d->hostless[kind] = 0;
    for (uint8_t i = 0; i < d->n_hosts; i++) { d->hosts[i].n[kind] = 0; }
}

static void domain_fire(firc_autolist_t *a, domain_t *d, kind_t kind) {
    for (uint8_t i = 0; i < d->n_hosts; i++) {
        host_t *h = domain_host(a, &d->hosts[i]);
        if (h == NULL) { continue; }
        if (kind == KIND_FAIL) {
            host_restart(h);
        } else if (kind == KIND_NO_CONNECT) {
            h->no_connect.n = 0;
        } else {
            h->notes.n = 0;
        }
    }
    domain_restart_kind(d, kind);
}

static uint32_t slot_total(const domain_host_t *e) { return e->n[KIND_FAIL] + e->n[KIND_NO_CONNECT] + e->n[KIND_NOTE]; }

static domain_t *flow_named_domain(firc_autolist_t *a, const flow_t *f, bool take, int64_t now) {
    if (!f->domain_named) { return NULL; }
    char domain[FIRC_AUTOLIST_NAME_MAX];
    const char *name = NULL;
    if (hint_only(a, &f->key.client, &f->key.server, now, &name, domain, sizeof(domain)) != HINT_DOMAIN) {
        return NULL;
    }
    return domain_find(a, domain, take, now);
}

static domain_t *domain_credit(firc_autolist_t *a, const flow_t *f, const host_t *h, kind_t kind, int64_t now) {
    domain_t *d = h != NULL ? domain_find(a, h->name, true, now) : flow_named_domain(a, f, true, now);
    if (d == NULL) { return NULL; }
    d->seen = now;
    if (!domain_counting(a, d, now)) { domain_restart(d); }
    if (h == NULL) {
        d->hostless[kind]++;
        return d;
    }
    int32_t idx = (int32_t)(h - a->hosts);
    domain_host_t *slot = NULL;
    for (uint8_t i = 0; i < d->n_hosts; i++) {
        if (d->hosts[i].host == idx && d->hosts[i].serial == h->serial) {
            slot = &d->hosts[i];
            break;
        }
    }
    if (slot == NULL) {
        if (d->n_hosts < DOMAIN_HOSTS) {
            slot = &d->hosts[d->n_hosts++];
        } else {
            slot = &d->hosts[0];
            for (uint8_t i = 1; i < DOMAIN_HOSTS; i++) {
                if (slot_total(&d->hosts[i]) < slot_total(slot)) { slot = &d->hosts[i]; }
            }
        }
        memset(slot, 0, sizeof(*slot));
    }
    slot->host = idx;
    slot->serial = h->serial;
    slot->n[kind]++;
    return d;
}

static const char *domain_marker(firc_autolist_t *a, const domain_t *d, kind_t kind) {
    const char *top = d->name;
    uint32_t most = d->hostless[kind];
    for (uint8_t i = 0; i < d->n_hosts; i++) {
        const domain_host_t *e = &d->hosts[i];
        if (e->n[kind] <= most) { continue; }
        const host_t *h = domain_host(a, e);
        if (h == NULL) { continue; }
        top = h->name;
        most = e->n[kind];
    }
    return top;
}

static bool host_bad_at(const host_t *h, const firc_ip_t *server) {
    for (uint8_t i = 0; i < h->n_bad; i++) {
        if (ip_eq(&h->bad[i], server)) { return true; }
    }
    return false;
}

static void host_note_bad(host_t *h, const firc_ip_t *server) {
    if (host_bad_at(h, server)) { return; }
    if (h->n_bad < BAD_ADDRS) {
        h->bad[h->n_bad++] = *server;
        return;
    }
    h->bad[h->next_bad] = *server;
    h->next_bad = (uint8_t)((h->next_bad + 1u) % BAD_ADDRS);
}

static int64_t hour_of(int64_t now) { return now >= 0 ? now / 3600 : (now - 3599) / 3600; }

static void day_bump(int64_t *hours, uint32_t *counts, int64_t now) {
    int64_t hour = hour_of(now);
    size_t at = (size_t)(((hour % FIRC_AUTOLIST_DAY_HOURS) + FIRC_AUTOLIST_DAY_HOURS) % FIRC_AUTOLIST_DAY_HOURS);
    if (hours[at] != hour) {
        hours[at] = hour;
        counts[at] = 0;
    }
    counts[at]++;
}

static uint32_t day_sum(const int64_t *hours, const uint32_t *counts, int64_t now) {
    int64_t hour = hour_of(now);
    uint32_t n = 0;
    for (size_t i = 0; i < FIRC_AUTOLIST_DAY_HOURS; i++) {
        if (hours[i] <= hour && hour - hours[i] < FIRC_AUTOLIST_DAY_HOURS) { n += counts[i]; }
    }
    return n;
}

static bool addr_key(const firc_autolist_addr_t *r, const firc_ip_t *server, uint8_t proto, uint16_t port) {
    return r->proto == proto && r->port == port && ip_eq(&r->server, server);
}

static bool addr_live(const firc_autolist_t *a, size_t i, int64_t now) {
    return a->addr_used[i] && now - a->addr[i].last_at < FIRC_AUTOLIST_DAY_SECS;
}

static size_t addr_find(const firc_autolist_t *a, size_t quota, const firc_ip_t *server, uint8_t proto,
                        uint16_t port) {
    for (size_t i = quota * FIRC_AUTOLIST_ADDRS; i < (quota + 1) * FIRC_AUTOLIST_ADDRS; i++) {
        if (a->addr_used[i] && addr_key(&a->addr[i], server, proto, port)) { return i; }
    }
    return NO_ROW;
}

static size_t addr_spare(const firc_autolist_t *a, size_t quota, int64_t now) {
    size_t spare = quota * FIRC_AUTOLIST_ADDRS;
    for (size_t i = spare; i < (quota + 1) * FIRC_AUTOLIST_ADDRS; i++) {
        if (!addr_live(a, i, now)) { return i; }
        if (a->addr[i].last_at < a->addr[spare].last_at) { spare = i; }
    }
    return spare;
}

static firc_autolist_addr_t *addr_start(firc_autolist_t *a, size_t at, const firc_ip_t *server, uint8_t proto,
                                        uint16_t port, int64_t now) {
    firc_autolist_addr_t *r = &a->addr[at];
    memset(r, 0, sizeof(*r));
    r->server = *server;
    r->proto = proto;
    r->port = port;
    r->first_at = now;
    a->addr_used[at] = true;
    return r;
}

static size_t addr_take(firc_autolist_t *a, size_t quota, const firc_ip_t *server, uint8_t proto, uint16_t port,
                        int64_t now, bool *fresh) {
    size_t at = addr_find(a, quota, server, proto, port);
    *fresh = at == NO_ROW || !addr_live(a, at, now);
    if (at == NO_ROW) { at = addr_spare(a, quota, now); }
    return at;
}

static uint32_t addr_note(firc_autolist_addr_t *r, const firc_ip_t *client, const char *name, int64_t now) {
    r->fails++;
    r->last_at = now;
    if (name != NULL && name[0] != '\0') { snprintf(r->name, sizeof(r->name), "%s", name); }
    bool known = false;
    for (uint8_t k = 0; k < r->n_clients; k++) { known = known || ip_eq(&r->clients[k], client); }
    if (!known && r->n_clients < FIRC_AUTOLIST_ADDR_CLIENTS) { r->clients[r->n_clients++] = *client; }
    return r->fails;
}

static uint32_t bypass_count(firc_autolist_t *a, const flow_t *f, uint8_t why, int64_t now) {
    const firc_ip_t *server = &f->key.server;
    uint16_t port = f->key.server_port;
    size_t from = addr_find(a, QUOTA_BYPASS, server, FIRC_AUTOLIST_PROTO_TCP, port);
    bool fresh = false;
    size_t at = addr_take(a, QUOTA_FAILED, server, FIRC_AUTOLIST_PROTO_TCP, port, now, &fresh);
    if (fresh && from != NO_ROW && addr_live(a, from, now)) {
        a->addr[at] = a->addr[from];
        a->addr_used[at] = true;
    } else if (fresh) {
        (void)addr_start(a, at, server, FIRC_AUTOLIST_PROTO_TCP, port, now);
    }
    if (from != NO_ROW) { a->addr_used[from] = false; }
    firc_autolist_addr_t *r = &a->addr[at];
    r->outcome = FIRC_AL_OUTCOME_FAILED;
    r->why = why;
    return addr_note(r, &f->key.client, f->sni, now);
}

void firc_autolist_bypass_seen(firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server, uint8_t proto,
                               uint16_t port, const char *name, const char group[FIRC_ID_STR_LEN], uint8_t how,
                               int64_t now) {
    if (a == NULL || client == NULL || server == NULL) { return; }
    size_t at = addr_find(a, QUOTA_FAILED, server, proto, port);
    if (at == NO_ROW || !addr_live(a, at, now)) {
        bool fresh = false;
        at = addr_take(a, QUOTA_BYPASS, server, proto, port, now, &fresh);
        if (fresh) { (void)addr_start(a, at, server, proto, port, now); }
    }
    firc_autolist_addr_t *r = &a->addr[at];
    r->outcome = FIRC_AL_OUTCOME_BYPASS;
    r->how = how;
    snprintf(r->group, sizeof(r->group), "%.*s", FIRC_ID_STR_LEN - 1, group != NULL ? group : "");
    (void)addr_note(r, client, name, now);
}

static bool unnamed(firc_autolist_t *a, const flow_t *f, int64_t now) {
    if (f->domain_named) { day_bump(a->ambiguous_hour, a->ambiguous_count, now); }
    return false;
}

static bool fail(firc_autolist_t *a, flow_t *f, firc_autolist_why_t why, int64_t now, firc_autolist_failure_t *out) {
    f->judged = true;
    if (f->ambiguous) {
        day_bump(a->ambiguous_hour, a->ambiguous_count, now);
        return false;
    }
    if (f->bypass) {
        uint32_t fails = bypass_count(a, f, (uint8_t)why, now);
        if (out != NULL) {
            memset(out, 0, sizeof(*out));
            out->client = f->key.client;
            out->server = f->key.server;
            snprintf(out->host, sizeof(out->host), "%s", f->sni);
            out->fails = fails;
            out->why = (uint8_t)why;
            out->bypass = true;
        }
        return true;
    }
    host_t *h = host_of(a, f);
    domain_t *d = domain_credit(a, f, h, KIND_FAIL, now);
    if (h == NULL && d == NULL) { return unnamed(a, f, now); }
    day_bump(a->day_hour, a->day_count, now);
    uint32_t fails = 0;
    if (h != NULL) {
        if (!tally_live(a, &h->fails, now)) { host_restart(h); }
        fails = tally_add(a, &h->fails, now);
        host_note_bad(h, &f->key.server);
    }
    uint32_t domain_fails = d != NULL ? tally_add(a, &d->fails, now) : 0;
    bool by_host = h != NULL && fails >= a->p.fail_threshold;
    bool by_domain = !by_host && domain_fails >= a->p.fail_threshold;
    if (out != NULL) {
        memset(out, 0, sizeof(*out));
        const char *named = by_domain ? domain_marker(a, d, KIND_FAIL) : h != NULL ? h->name : d->name;
        snprintf(out->host, sizeof(out->host), "%s", named);
        if (by_domain) { memcpy(out->domain, d->name, sizeof(out->domain)); }
        out->client = f->key.client;
        out->fails = h != NULL && !by_domain ? fails : domain_fails;
        out->why = (uint8_t)why;
        out->listed = by_host || by_domain;
    }
    if (by_host) { host_restart(h); }
    if (by_domain) {
        domain_fire(a, d, KIND_FAIL);
    } else if (by_host && d != NULL) {
        domain_restart_kind(d, KIND_FAIL);
    }
    return true;
}

static bool nominate(firc_autolist_t *a, const flow_t *f, const host_t *h, domain_t *d, bool by_host,
                     bool by_domain, firc_autolist_nom_kind_t kind, uint32_t received,
                     firc_autolist_nomination_t *out) {
    if (!by_host && !by_domain) { return false; }
    kind_t counted = kind == FIRC_AL_NOM_STALL ? KIND_NOTE : KIND_NO_CONNECT;
    if (out != NULL) {
        memset(out, 0, sizeof(*out));
        const char *named = by_host ? h->name : domain_marker(a, d, counted);
        snprintf(out->host, sizeof(out->host), "%s", named);
        if (!by_host) { memcpy(out->domain, d->name, sizeof(out->domain)); }
        out->client = f->key.client;
        out->received = received;
        out->kind = (uint8_t)kind;
    }
    if (!by_host) {
        domain_fire(a, d, counted);
    } else if (d != NULL) {
        domain_restart_kind(d, counted);
    }
    return true;
}

static void flow_start(flow_t *f, const firc_autolist_flow_t *key, uint32_t hash, uint32_t seq, int64_t now) {
    memset(f, 0, sizeof(*f));
    f->used = true;
    f->hash = hash;
    f->key = *key;
    f->seq = seq;
    f->seen = now;
    f->host = -1;
}

static void flow_name(firc_autolist_t *a, flow_t *f, const char *sni, int64_t now) {
    const char *name = NULL;
    snprintf(f->sni, sizeof(f->sni), "%s", sni != NULL ? sni : "");
    if (sni != NULL && sni[0] != '\0') {
        name = hint_named(a, &f->key.client, &f->key.server, sni, now);
        f->bypass = name == NULL;
    } else {
        char domain[FIRC_AUTOLIST_NAME_MAX];
        hint_count_t n = hint_only(a, &f->key.client, &f->key.server, now, &name, domain, sizeof(domain));
        f->bypass = n == HINT_NONE;
        f->ambiguous = n == HINT_MANY;
        f->domain_named = n == HINT_DOMAIN;
    }
    f->host = name != NULL ? host_take(a, name, now) : -1;
    f->host_serial = f->host >= 0 ? a->hosts[f->host].serial : 0;
}

bool firc_autolist_hello(firc_autolist_t *a, const firc_autolist_flow_t *key, uint32_t seq, uint32_t ack,
                         const char *sni, int64_t now, firc_autolist_failure_t *out) {
    if (a == NULL || key == NULL) { return false; }
    uint32_t hash = flow_hash(key);
    flow_t *f = flow_find(a, key, hash, now);
    if (f != NULL && f->seq == seq) {
        f->seen = now;
        if (f->judged) { return false; }
        if (f->sent < UINT8_MAX) { f->sent++; }
        if ((uint32_t)f->sent - 1u < a->p.retrans_threshold) { return false; }
        return fail(a, f, FIRC_AUTOLIST_RETRANS, now, out);
    }

    if (f == NULL) { f = flow_slot(a, now); }
    flow_start(f, key, hash, seq, now);
    f->ack0 = ack;
    f->sent = 1;
    f->hello_at = now;
    flow_name(a, f, sni, now);
    return false;
}

static bool no_connect(firc_autolist_t *a, flow_t *f, int64_t now, firc_autolist_nomination_t *out) {
    f->judged = true;
    if (f->ambiguous) {
        day_bump(a->ambiguous_hour, a->ambiguous_count, now);
        return false;
    }
    if (f->bypass) {
        (void)bypass_count(a, f, FIRC_AUTOLIST_NO_CONNECT, now);
        if (out != NULL) {
            memset(out, 0, sizeof(*out));
            out->client = f->key.client;
            out->server = f->key.server;
            out->kind = FIRC_AL_NOM_NO_CONNECT;
            out->bypass = true;
        }
        return true;
    }
    host_t *h = host_of(a, f);
    domain_t *d = domain_credit(a, f, h, KIND_NO_CONNECT, now);
    if (h == NULL && d == NULL) { return unnamed(a, f, now); }
    bool by_host = h != NULL && tally_add(a, &h->no_connect, now) >= a->p.fail_threshold;
    bool by_domain = d != NULL && tally_add(a, &d->no_connect, now) >= a->p.fail_threshold;
    if (by_host) { h->no_connect.n = 0; }
    return nominate(a, f, h, d, by_host, by_domain, FIRC_AL_NOM_NO_CONNECT, 0, out);
}

bool firc_autolist_syn(firc_autolist_t *a, const firc_autolist_flow_t *key, uint32_t seq, int64_t now,
                       firc_autolist_nomination_t *out) {
    if (a == NULL || key == NULL) { return false; }
    uint32_t hash = flow_hash(key);
    flow_t *f = flow_find(a, key, hash, now);
    if (f != NULL && !f->syn && seq + 1u == f->seq) { return false; }
    if (f != NULL && f->syn && f->seq == seq) {
        f->seen = now;
        if (f->judged || now - f->syn_at < FIRC_AUTOLIST_SYN_SPAN_SECS) { return false; }
        return no_connect(a, f, now, out);
    }
    if (f == NULL) { f = flow_slot(a, now); }
    flow_start(f, key, hash, seq, now);
    f->syn = true;
    f->syn_at = now;
    flow_name(a, f, NULL, now);
    return false;
}

bool firc_autolist_reply(firc_autolist_t *a, const firc_autolist_flow_t *key, bool rst, int64_t now,
                         firc_autolist_failure_t *out) {
    if (a == NULL || key == NULL) { return false; }
    flow_t *f = flow_find(a, key, flow_hash(key), now);
    if (f == NULL || f->syn || f->judged) { return false; }
    f->seen = now;
    if (rst) { return fail(a, f, FIRC_AUTOLIST_RESET, now, out); }
    f->judged = true;
    host_t *h = host_of(a, f);
    if (h != NULL && host_bad_at(h, &f->key.server)) { host_restart(h); }
    domain_t *d = h != NULL ? domain_find(a, h->name, false, now) : flow_named_domain(a, f, false, now);
    if (d != NULL) { d->fails.n = 0; }
    return false;
}

bool firc_autolist_fin(firc_autolist_t *a, const firc_autolist_flow_t *key, uint32_t ack, int64_t now,
                       firc_autolist_nomination_t *out) {
    if (a == NULL || key == NULL) { return false; }
    flow_t *f = flow_find(a, key, flow_hash(key), now);
    if (f == NULL || f->syn || f->finned) { return false; }
    f->finned = true;
    f->seen = now;
    uint32_t received = ack - f->ack0;
    if (received < FIRC_AUTOLIST_STALL_MIN_BYTES || received > FIRC_AUTOLIST_STALL_MAX_BYTES) { return false; }
    if (now - f->hello_at < FIRC_AUTOLIST_STALL_MIN_SECS) { return false; }
    host_t *h = host_of(a, f);
    domain_t *d = domain_credit(a, f, h, KIND_NOTE, now);
    if (h == NULL && d == NULL) { return false; }
    bool by_host = h != NULL && notes_add(&h->notes, now) >= FIRC_AUTOLIST_NOTES;
    bool by_domain = d != NULL && notes_add(&d->notes, now) >= FIRC_AUTOLIST_NOTES;
    if (by_host) { h->notes.n = 0; }
    return nominate(a, f, h, d, by_host, by_domain, FIRC_AL_NOM_STALL, received, out);
}

static bool addr_hidden(const firc_autolist_t *a, const firc_autolist_addr_t *r, int64_t now) {
    for (size_t i = 0; i < ROWS; i++) {
        const hidden_t *h = &a->hidden[i];
        if (h->until > now && h->proto == r->proto && h->port == r->port && ip_eq(&h->server, &r->server)) {
            return true;
        }
    }
    return false;
}

size_t firc_autolist_addresses(const firc_autolist_t *a, int64_t now, firc_autolist_addr_t *out, size_t cap) {
    if (a == NULL) { return 0; }
    size_t order[ROWS] = {0};
    size_t n = 0;
    for (size_t i = 0; i < ROWS; i++) {
        if (!addr_live(a, i, now) || addr_hidden(a, &a->addr[i], now)) { continue; }
        size_t k = n++;
        while (k > 0 && a->addr[order[k - 1]].last_at < a->addr[i].last_at) {
            order[k] = order[k - 1];
            k--;
        }
        order[k] = i;
    }
    if (n > cap) { n = cap; }
    if (out != NULL) {
        for (size_t k = 0; k < n; k++) { out[k] = a->addr[order[k]]; }
    }
    return n;
}

bool firc_autolist_addr_dismiss(firc_autolist_t *a, const firc_ip_t *server, uint8_t proto, uint16_t port,
                                int64_t now) {
    if (a == NULL || server == NULL) { return false; }
    bool visible = false;
    for (size_t i = 0; i < ROWS && !visible; i++) {
        visible = addr_live(a, i, now) && addr_key(&a->addr[i], server, proto, port) &&
                  !addr_hidden(a, &a->addr[i], now);
    }
    if (!visible) { return false; }
    hidden_t *h = &a->hidden[a->hidden_next];
    a->hidden_next = (a->hidden_next + 1) % ROWS;
    h->server = *server;
    h->proto = proto;
    h->port = port;
    h->until = now + FIRC_AUTOLIST_DAY_SECS;
    return true;
}

uint32_t firc_autolist_failures_day(const firc_autolist_t *a, int64_t now) {
    return a != NULL ? day_sum(a->day_hour, a->day_count, now) : 0;
}

uint32_t firc_autolist_ambiguous_day(const firc_autolist_t *a, int64_t now) {
    return a != NULL ? day_sum(a->ambiguous_hour, a->ambiguous_count, now) : 0;
}
