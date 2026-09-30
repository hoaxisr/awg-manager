#ifndef SUSANIN_HEALTH_H
#define SUSANIN_HEALTH_H

#include "config.h"

/* Returns 0 on success. *ok = number of probes that got a reply, *total = sent.
 * src — source address for the probes (tunnel address of the current egress);
 * if NULL/empty, cfg->egress_address is used. */
int health_probe(const susanin_config *c, const char *src, int *ok, int *total);

#endif
