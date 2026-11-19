#ifndef TUNVLESS_TSCRATCH_H
#define TUNVLESS_TSCRATCH_H
#include <stddef.h>

enum ts_slot { TS_H2, TS_H2MUX, TS_H2WIRE, TS_XCONN, TS_GRPC, TS_VENC, TS_WS, TS_XHTTP, TS_QUIC, TS_N };

extern __thread void *ts_g[TS_N];

void *ts_alloc(enum ts_slot s, size_t n);

static inline void *ts_get(enum ts_slot s, size_t n) {
    void *p = ts_g[s];
    return __builtin_expect(p != NULL, 1) ? p : ts_alloc(s, n);
}

#endif
