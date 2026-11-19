#ifndef TUNVLESS_HY2RUN_H
#define TUNVLESS_HY2RUN_H
#include <stdint.h>

#include "hy2conn.h"

#define HY2_RUN_TIMEOUT (-70)
#define HY2_RUN_CLOSED  (-71)

struct hy2_run_stats { uint64_t sleeps, short_sleeps; };

typedef int (*hy2_step_fn)(void *u);

int hy2_run(struct hy2c *c, uint64_t until_ns, hy2_step_fn step, void *u, struct hy2_run_stats *st);

#endif
