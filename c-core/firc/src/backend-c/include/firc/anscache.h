#ifndef FIRC_ANSCACHE_H
#define FIRC_ANSCACHE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/dnswire.h"
#include "firc/id.h"

#define FIRC_ANSCACHE_ENTRIES 8192u
#define FIRC_ANSCACHE_BYTES (4u * 1024u * 1024u)
#define FIRC_ANSCACHE_MAX_ANSWER 4096u
#define FIRC_ANSCACHE_MAX_TTL 86400u
#define FIRC_ANSCACHE_MAX_NEGATIVE_TTL 60u
#define FIRC_ANSCACHE_PREFETCH_MIN_TTL 10u

#define FIRC_ANSCACHE_EDNS 0x01u
#define FIRC_ANSCACHE_DO 0x02u
#define FIRC_ANSCACHE_CD 0x04u

typedef struct firc_anscache firc_anscache_t;

typedef struct firc_anscache_key {
    firc_id_t group_id;
    uint64_t gen;
    uint16_t qtype;
    uint16_t qclass;
    uint8_t variant;
    uint8_t name_len;
    uint8_t name[FIRC_DNS_MAX_NAME + 1];
} firc_anscache_key_t;

typedef struct firc_anscache_hit {
    uint8_t *wire;
    size_t len;
    bool prefetch;
} firc_anscache_hit_t;

/* NULL when a bound is 0 or out of memory. Loop thread only; every call takes a NULL cache. */
firc_anscache_t *firc_anscache_new(size_t max_entries, size_t max_bytes);
void firc_anscache_free(firc_anscache_t *c);

/* False unless the query has exactly one question. The name is folded to lower case. */
bool firc_anscache_key_of(const firc_dns_msg_t *query, firc_id_t group_id, uint64_t gen, firc_anscache_key_t *out);

/* Seconds the answer may be served from the cache; 0 when it may not be cached. */
uint32_t firc_anscache_lifetime(const firc_dns_msg_t *answer);

/* Replaces the key's entry, evicting the least recently used; false when not stored. */
bool firc_anscache_put(firc_anscache_t *c, const firc_anscache_key_t *key, const uint8_t *wire, size_t len,
                       uint32_t ttl, uint64_t now_ms);

/* On a hit out->wire (caller frees) carries the query's id, RD bit and name case, its TTLs lowered by the age;
 * out->prefetch is true once per entry in its last tenth. */
bool firc_anscache_get(firc_anscache_t *c, const firc_anscache_key_t *key, const uint8_t *query, size_t query_len,
                       uint64_t now_ms, firc_anscache_hit_t *out);

/* Lets the key's entry ask for a refresh again. */
void firc_anscache_prefetch_done(firc_anscache_t *c, const firc_anscache_key_t *key);

void firc_anscache_drop_group(firc_anscache_t *c, firc_id_t group_id);
size_t firc_anscache_count(const firc_anscache_t *c);
size_t firc_anscache_bytes(const firc_anscache_t *c);

#endif /* FIRC_ANSCACHE_H */
