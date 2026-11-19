#ifndef FIRC_TUNWATCH_H
#define FIRC_TUNWATCH_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/tunsup.h"

#define FIRC_TUNWATCH_AFTER_MS (5 * 60 * 1000)

typedef struct {
    bool bad;
    bool alarmed;
    int64_t bad_since;
    int64_t said;
} firc_tunwatch_t;

/* ERROR once a tunnel has had no working node for FIRC_TUNWATCH_AFTER_MS, again every FIRC_LOGREP_REMIND_MS
   while it lasts, INFO when one works again; an off tunnel starts over without a word. */
void firc_tunwatch_see(firc_tunwatch_t *w, const char *device, firc_tun_status_t st, int64_t now_ms);

#endif
