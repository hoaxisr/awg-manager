#ifndef TUNVLESS_RATELOG_H
#define TUNVLESS_RATELOG_H

#include <stdatomic.h>
#include <stdio.h>
#include <time.h>

struct ratelog {
    atomic_uint next;
    atomic_uint held;
};

static inline int ratelog_take(struct ratelog *r, unsigned now, unsigned every_s, unsigned *held) {
    unsigned nx = atomic_load(&r->next);
    if ((nx != 0 && now < nx) || !atomic_compare_exchange_strong(&r->next, &nx, now + every_s)) {
        atomic_fetch_add(&r->held, 1u);
        return 0;
    }
    *held = atomic_exchange(&r->held, 0u);
    return 1;
}

static inline unsigned ratelog_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (unsigned)ts.tv_sec;
}

static inline const char *ratelog_more(char *buf, size_t cap, unsigned held) {
    if (held == 0) return "";
    snprintf(buf, cap, " (%u more since the last)", held);
    return buf;
}

#endif
