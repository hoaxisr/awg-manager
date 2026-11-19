#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include "nodes.h"

#define NODES_CHUNK 64
#define NODES_CHUNKS 16384

struct nodes_key { char *key; uint32_t hash; };

static struct {
    size_t size;
    int n;
    unsigned char *chunk[NODES_CHUNKS];
    struct nodes_key *keys[NODES_CHUNKS];
    char **str;
    size_t *str_len;
    size_t str_n, str_cap;
} g_ns;

static uint32_t nodes_hash(const char *s) {
    uint32_t h = 2166136261u;
    for (; *s; s++) h = (h ^ (unsigned char)*s) * 16777619u;
    return h;
}

int nodes_init(size_t size) {
    for (int c = 0; c < NODES_CHUNKS && g_ns.chunk[c]; c++) {
        for (int k = 0; k < NODES_CHUNK; k++) free(g_ns.keys[c][k].key);
        free(g_ns.chunk[c]);
        free(g_ns.keys[c]);
        g_ns.chunk[c] = NULL;
        g_ns.keys[c] = NULL;
    }
    for (size_t i = 0; i < g_ns.str_n; i++) free(g_ns.str[i]);
    free(g_ns.str);
    free(g_ns.str_len);
    g_ns.str = NULL;
    g_ns.str_len = NULL;
    g_ns.str_n = g_ns.str_cap = 0;
    g_ns.size = size;
    __atomic_store_n(&g_ns.n, 0, __ATOMIC_RELEASE);
    return 0;
}

int nodes_find(const char *key) {
    if (!key) return -1;
    uint32_t h = nodes_hash(key);
    int n = __atomic_load_n(&g_ns.n, __ATOMIC_ACQUIRE);
    for (int i = 0; i < n; i++) {
        const struct nodes_key *k = &g_ns.keys[i / NODES_CHUNK][i % NODES_CHUNK];
        if (k->key && k->hash == h && !strcmp(k->key, key)) return i;
    }
    return -1;
}

int nodes_add(const void *node, const char *key) {
    int i = nodes_find(key);
    if (i >= 0) return i;
    i = __atomic_load_n(&g_ns.n, __ATOMIC_RELAXED);
    int c = i / NODES_CHUNK, k = i % NODES_CHUNK;
    if (c >= NODES_CHUNKS) return -1;
    if (!g_ns.chunk[c]) {
        unsigned char *p = malloc(g_ns.size * NODES_CHUNK);
        struct nodes_key *ks = calloc(NODES_CHUNK, sizeof *ks);
        if (!p || !ks) { free(p); free(ks); return -1; }
        g_ns.keys[c] = ks;
        g_ns.chunk[c] = p;
    }
    char *dup = NULL;
    if (key && !(dup = strdup(key))) return -1;
    memcpy(g_ns.chunk[c] + (size_t)k * g_ns.size, node, g_ns.size);
    g_ns.keys[c][k].key = dup;
    g_ns.keys[c][k].hash = key ? nodes_hash(key) : 0;
    __atomic_store_n(&g_ns.n, i + 1, __ATOMIC_RELEASE);
    return i;
}

int nodes_count(void) { return __atomic_load_n(&g_ns.n, __ATOMIC_ACQUIRE); }

const void *node_at(int i) {
    return g_ns.chunk[i / NODES_CHUNK] + (size_t)(i % NODES_CHUNK) * g_ns.size;
}

const char *nodes_intern(const char *v, size_t n) {
    for (size_t i = 0; i < g_ns.str_n; i++)
        if (g_ns.str_len[i] == n && !memcmp(g_ns.str[i], v, n)) return g_ns.str[i];
    if (g_ns.str_n == g_ns.str_cap) {
        size_t cap = g_ns.str_cap ? g_ns.str_cap * 2 : 16;
        char **p = realloc(g_ns.str, cap * sizeof *p);
        if (!p) return NULL;
        g_ns.str = p;
        size_t *l = realloc(g_ns.str_len, cap * sizeof *l);
        if (!l) return NULL;
        g_ns.str_len = l;
        g_ns.str_cap = cap;
    }
    char *c = malloc(n + 1);
    if (!c) return NULL;
    memcpy(c, v, n);
    c[n] = '\0';
    g_ns.str_len[g_ns.str_n] = n;
    g_ns.str[g_ns.str_n++] = c;
    return c;
}
