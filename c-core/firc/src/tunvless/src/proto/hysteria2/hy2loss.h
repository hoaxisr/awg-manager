#ifndef TUNVLESS_HY2LOSS_H
#define TUNVLESS_HY2LOSS_H
#include <stdint.h>

#define HY2LOSS_NONE  0
#define HY2LOSS_LOSSY 1

#define HY2LOSS_MIN_PKTS 500
#define HY2LOSS_MAX_MS   10000
#define HY2LOSS_RUN      2

struct hy2loss {
    uint64_t sent, lost, ms;
    uint64_t run_sent, run_lost;
    unsigned run;
};

int hy2loss_step(struct hy2loss *s, uint64_t sent, uint64_t lost, int hopped, uint64_t span_ms);

#endif
