#ifndef FIRC_NETFILTER_CLEANER_H
#define FIRC_NETFILTER_CLEANER_H

#include "firc/err.h"
#include "firc/fakeip.h"
#include "firc/iptables.h"

/* Every chain and jump of ours out, in one full commit per non-NULL engine; the pool's FORWARD barrier staged when pool is non-NULL. */
firc_err_t firc_netfilter_clean(firc_ipt_t *ipt4, firc_ipt_t *ipt6, const firc_fakeip_t *pool);

/* Outside the committer: runs step again after a lost race or a cancel, three tries 200 ms apart. */
firc_err_t firc_netfilter_retry_races(firc_err_t (*step)(void *ud), void *ud);
/* The same, a lost race or a cancel retried as firc_netfilter_retry_races, for fircd's start and `--purge`. */
firc_err_t firc_netfilter_clean_retrying(firc_ipt_t *ipt4, firc_ipt_t *ipt6, const firc_fakeip_t *pool);

/* Registers the base chains as patch chains; one already registered keeps what it staged. */
firc_err_t firc_netfilter_register_base_chains(firc_ipt_t *ipt4, firc_ipt_t *ipt6);

#endif /* FIRC_NETFILTER_CLEANER_H */
