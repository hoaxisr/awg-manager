#ifndef FIRC_MSS_H
#define FIRC_MSS_H

#include <stddef.h>

#include "firc/err.h"
#include "firc/iptables.h"

#define FIRC_MSS_CHAIN_SUFFIX "MSS"

/* Stages <prefix>MSS in mangle, one TCPMSS clamp per device devs[i] on SYNs leaving by it, in order, and its jump
   at mangle/FORWARD 1; n == 0 stages nothing, so the write drops both. */
firc_err_t firc_mss_build_rules(firc_ipt_t *ipt, const char *chain_prefix, const char *const *devs, size_t n);

#endif
