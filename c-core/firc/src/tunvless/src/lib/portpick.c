#include "portpick.h"

static int num(const char **pp, unsigned *out) {
    const char *p = *pp;
    unsigned v = 0;
    if (*p < '0' || *p > '9') return 0;
    for (; *p >= '0' && *p <= '9'; p++) {
        v = v * 10 + (unsigned)(*p - '0');
        if (v > 65535) return 0;
    }
    *pp = p;
    *out = v;
    return 1;
}

size_t port_ranges(const char *ports, struct port_range *out, size_t cap) {
    size_t n = 0;
    const char *p = ports;
    for (;;) {
        unsigned lo, hi;
        if (!num(&p, &lo) || !lo) return 0;
        hi = lo;
        if (*p == '-') {
            p++;
            if (!num(&p, &hi) || hi < lo) return 0;
        }
        if (n == cap) return 0;
        out[n].lo = (uint16_t)lo;
        out[n].hi = (uint16_t)hi;
        n++;
        if (!*p) return n;
        if (*p++ != ',') return 0;
    }
}

uint16_t port_pick(const struct port_range *r, size_t n, uint32_t x) {
    uint64_t total = 0;
    for (size_t i = 0; i < n; i++) total += (uint64_t)r[i].hi - r[i].lo + 1;
    if (!total) return 0;
    uint64_t k = x % total;
    for (size_t i = 0; i < n; i++) {
        uint64_t w = (uint64_t)r[i].hi - r[i].lo + 1;
        if (k < w) return (uint16_t)(r[i].lo + k);
        k -= w;
    }
    return 0;
}
