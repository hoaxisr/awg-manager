#ifndef FIRC_TAPRULES_H
#define FIRC_TAPRULES_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip.h"
#include "firc/iptables.h"

/* One rule: args[0..n_args) in iptables argv form for `family`; strings are borrowed, valid only for the call. */
typedef void (*firc_tap_rule_fn)(void *ud, unsigned family, const char *const *args,
                                 size_t n_args);

#define FIRC_TAP_GROUP_HELLO 4 /* ClientHello rule; socket copy FIRC_TAP_COPY_RANGE */
#define FIRC_TAP_GROUP_NEW 5   /* new-connection rule; socket copy FIRC_TAP_HEAD_RANGE */

#define FIRC_TAP_HEAD_RANGE 64u

#define FIRC_TAP_LIMIT_PER_SEC 500

/* Kernel modules the capture rules need; read off firc_tap_rules_build's own argv. */
#define FIRC_TAP_LAN_MODULES \
    "xt_conntrack, xt_mark, xt_limit, xt_string, xt_length, xt_connbytes and xt_NFLOG"

/* Hands fn a new-connection + ClientHello rule pair per (lan interface, pool family). */
firc_err_t firc_tap_rules_build(const firc_fakeip_t *pool, const char *const *lan, size_t n_lan,
                                firc_tap_rule_fn fn, void *ud);

#define FIRC_TAP_CHAIN_SUFFIX "TAP"
#define FIRC_TAP_PROBE_SUFFIX "PROBE" /* "<prefix>TAPPROBE"; nothing jumps to it */

/* A later failure leaves the chain staged; the caller discards the staging. */
firc_err_t firc_tap_rules_install(firc_ipt_t *ipt, const char *chain_prefix,
                                  const firc_fakeip_t *pool, const char *const *lan,
                                  size_t n_lan);

/* A real write of an unjumped chain, taken away again; FIRC_ERR_NOSYS means the kernel refused a rule. */
firc_err_t firc_tap_rules_supported(firc_ipt_t *ipt, const char *chain_prefix,
                                    const firc_fakeip_t *pool, const char *const *lan,
                                    size_t n_lan);

/* Loads whichever capture-rule modules are not loaded yet from dir (NULL: /lib/modules/<release>); returns how many. */
size_t firc_tap_load_modules(const char *dir);

#endif /* FIRC_TAPRULES_H */
