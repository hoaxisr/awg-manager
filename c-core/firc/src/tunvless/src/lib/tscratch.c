#include <pthread.h>
#include <stdlib.h>

#include "tscratch.h"

__thread void *ts_g[TS_N];

static pthread_once_t ts_once = PTHREAD_ONCE_INIT;
static pthread_key_t ts_key;
static int ts_key_err;

static void ts_free(void *v) {
    void **g = v;
    for (int i = 0; i < TS_N; i++) {
        free(g[i]);
        g[i] = NULL;
    }
}

static void ts_init(void) {
    ts_key_err = pthread_key_create(&ts_key, ts_free);
}

void *ts_alloc(enum ts_slot s, size_t n) {
    void *p = calloc(1, n);
    if (!p) return NULL;
    pthread_once(&ts_once, ts_init);
    if (ts_key_err || (!pthread_getspecific(ts_key) && pthread_setspecific(ts_key, ts_g) != 0)) {
        free(p);
        return NULL;
    }
    ts_g[s] = p;
    return p;
}
