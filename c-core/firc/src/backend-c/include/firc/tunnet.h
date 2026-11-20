#ifndef FIRC_TUNNET_H
#define FIRC_TUNNET_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/ipset_to_link.h"
#include "firc/rtnl.h"
#include "firc/tunnels.h"

typedef struct {
    uint32_t mark;
    uint32_t table;
    bool routed;
    firc_iface_route_t route;
    firc_iface_route_t route6;
    char dev[16];
} firc_tunnet_uplink_t;

/* Writes the tunnel's uplink rule and table; auto writes nothing and leaves mark 0. */
firc_err_t firc_tunnet_uplink_apply(firc_rtnl_t *r, uint32_t start_table, const firc_tunnel_t *t,
                                    const firc_tunnels_t *all, firc_tunnet_uplink_t *out);

/* Removes what apply wrote. */
firc_err_t firc_tunnet_uplink_remove(firc_rtnl_t *r, const firc_tunnet_uplink_t *u);

/* The device t leaves through: its interface, or the device of the tunnel it names; NULL for auto or unknown. */
const char *firc_tunnet_uplink_device(const firc_tunnel_t *t, const firc_tunnels_t *all);

/* Rewrites the default route after the uplink device came back or changed; routed follows the result. */
firc_err_t firc_tunnet_uplink_refresh(firc_rtnl_t *r, const firc_tunnel_t *t, const firc_tunnels_t *all,
                                      firc_tunnet_uplink_t *u);

#endif
