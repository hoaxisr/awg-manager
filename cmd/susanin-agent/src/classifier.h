#ifndef SUSANIN_CLASSIFIER_H
#define SUSANIN_CLASSIFIER_H

#include "config.h"
#include "conntrack.h"
#include "state.h"
#include <time.h>

typedef struct {
    const susanin_config *cfg;
    susanin_state *st;
} classifier_ctx;

void clr_fast(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now);
void clr_soft(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now);
void clr_judge(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now);

#endif
