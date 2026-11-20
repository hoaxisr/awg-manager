#ifndef FIRC_DNAT_H
#define FIRC_DNAT_H

#include "firc/fakeip.h"
#include "firc/iptables.h"

/* The chain's name is the configured prefix plus this. */
#define FIRC_DNAT_CHAIN_SUFFIX "DNAT"

/* Whether a group's mappings get DNAT rules; a group not routed gets none. */
typedef bool (*firc_dnat_routed_fn)(const char *group_id, void *ud);

/* Stages the chain and a rule per mapping with a real address; reads a snapshot, never the live pool.
 * nat/PREROUTING must already be a patch chain (firc_netfilter_register_base_chains), else FIRC_ERR_STATE. */
firc_err_t firc_dnat_build_rules(firc_ipt_t *ipt, const char *chain_prefix,
                                 const firc_fakeip_snapshot_t *snap,
                                 firc_dnat_routed_fn routed, void *routed_ud);

#endif /* FIRC_DNAT_H */
