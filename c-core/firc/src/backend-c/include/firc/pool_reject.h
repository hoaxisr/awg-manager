#ifndef FIRC_POOL_REJECT_H
#define FIRC_POOL_REJECT_H

#include "firc/fakeip.h"
#include "firc/iptables.h"
#include "firc/rtnl.h"

/* Metrics only rank same-prefix routes; deliberately poor in main so an operator can override. */
#define FIRC_POOL_REJECT_METRIC_GROUP 5u
#define FIRC_POOL_REJECT_METRIC_MAIN 4096u

/* Stages the FORWARD barrier, inserted first: rejects pool traffic that foreign tables routed, then drops
 * original packets of a flow firc marked (handled bit and a group field) that lost the bit. No commit.
 * filter/FORWARD must already be a patch chain (firc_netfilter_register_base_chains), else FIRC_ERR_STATE. */
#define FIRC_POOL_REJECT_CHAIN_SUFFIX "POOLREJECT"
firc_err_t firc_pool_reject_build_rules(firc_ipt_t *ipt, const char *chain_prefix, const firc_ip_t *base,
                                        uint8_t prefix_len);

/* Unreachable route for both pool prefixes in table; idempotent both ways. */
firc_err_t firc_pool_reject_install(firc_rtnl_t *r, const firc_fakeip_t *pool, uint32_t table,
                                    uint32_t metric);

/* Test seam: calls fn once per pool family with what install/remove would send. */
typedef void (*firc_pool_reject_plan_fn)(void *ud, int family, uint32_t table, uint32_t metric,
                                         const firc_ip_t *base, uint8_t prefix);
void firc_pool_reject_plan(const firc_fakeip_t *pool, uint32_t table, uint32_t metric,
                           firc_pool_reject_plan_fn fn, void *ud);
firc_err_t firc_pool_reject_remove(firc_rtnl_t *r, const firc_fakeip_t *pool, uint32_t table,
                                   uint32_t metric);

#endif /* FIRC_POOL_REJECT_H */
