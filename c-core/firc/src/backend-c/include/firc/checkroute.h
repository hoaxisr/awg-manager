#ifndef FIRC_CHECKROUTE_H
#define FIRC_CHECKROUTE_H

#include <stdint.h>

#include "firc/err.h"
#include "firc/ipset_to_link.h"
#include "firc/rtnl.h"

typedef struct firc_checkroute {
    uint32_t mark;
    uint32_t table;
    firc_iface_route_t route;
    char dev[16];
} firc_checkroute_t;

/* A table of dev's own: a blackhole, a default through dev, and a rule at FIRC_RULE_PRIORITY_TUNNEL from a fresh
 * unpersisted field; mark is that field. A default that cannot be written yet comes on refresh. */
firc_err_t firc_checkroute_apply(firc_rtnl_t *r, uint32_t start_table, const char *dev, firc_checkroute_t *out);

/* Writes the default again after the link came back or its gateway moved. */
firc_err_t firc_checkroute_refresh(firc_rtnl_t *r, firc_checkroute_t *c);

/* Takes back the rule, the default and the blackhole, and zeroes c. */
firc_err_t firc_checkroute_remove(firc_rtnl_t *r, firc_checkroute_t *c);

#endif
