#ifndef FIRC_PORT_REMAP_H
#define FIRC_PORT_REMAP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/iptables.h"

typedef struct firc_remap_addr {
    int family; /* AF_INET or AF_INET6 */
    uint8_t ip[16];
    uint8_t iplen; /* 4 or 16 */
} firc_remap_addr_t;

typedef struct firc_port_remap firc_port_remap_t;

/* Chain is "<chain_prefix>DNSOR"; ipt4/ipt6 borrowed, nullable. */
firc_port_remap_t *firc_port_remap_new(const char *chain_prefix, uint16_t from, uint16_t to,
                                   const firc_remap_addr_t *addrs, size_t n_addrs, firc_ipt_t *ipt4,
                                   firc_ipt_t *ipt6);
void firc_port_remap_free(firc_port_remap_t *p);

firc_err_t firc_port_remap_enable(firc_port_remap_t *p);
/* Writes nat with nothing of ours in it: for the daemon's teardown, after every group is gone. */
firc_err_t firc_port_remap_disable(firc_port_remap_t *p);

/* Marks it on without writing: the next full pass stages it. */
void firc_port_remap_arm(firc_port_remap_t *p);

/* Every address of the named interfaces; a name with none is skipped, and logged with warn. *out is the caller's. */
firc_err_t firc_port_remap_collect(char *const *names, size_t n_names, bool warn, firc_remap_addr_t **out,
                                   size_t *out_n);

/* Whether the next pass already stages exactly these addresses. Takes no lock: the loop thread is the only writer. */
bool firc_port_remap_same_addrs(const firc_port_remap_t *p, const firc_remap_addr_t *addrs, size_t n);

/* Replaces the addresses the next pass stages; hold the netfilter lock. */
firc_err_t firc_port_remap_set_addrs(firc_port_remap_t *p, const firc_remap_addr_t *addrs, size_t n);

/* Re-stages the chain and its jump for a full rebuild without committing; no-op when p is NULL or disabled. */
firc_err_t firc_port_remap_prepare_iptables(firc_port_remap_t *p);

#endif /* FIRC_PORT_REMAP_H */
