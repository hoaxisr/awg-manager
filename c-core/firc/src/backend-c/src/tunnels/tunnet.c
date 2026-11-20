#include "firc/tunnet.h"

#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <linux/rtnetlink.h>

#include "firc/log.h"
#include "firc/mark.h"

const char *firc_tunnet_uplink_device(const firc_tunnel_t *t, const firc_tunnels_t *all) {
    if (t->uplink == FIRC_UPLINK_IFACE) { return t->uplink_ref; }
    if (t->uplink != FIRC_UPLINK_TUNNEL) { return NULL; }
    for (size_t i = 0; all != NULL && i < all->n; i++) {
        if (strcmp(all->t[i].id, t->uplink_ref) == 0) { return all->t[i].device; }
    }
    return NULL;
}

static void write_default6(firc_rtnl_t *r, const char *dev, const firc_link_info_t *li, firc_tunnet_uplink_t *u) {
    bool carries = false;
    firc_err_t err = firc_rtnl_iface_carries_v6(r, li->ifindex, &carries);
    if (err == FIRC_OK) {
        err = carries ? firc_iface_route_update(r, AF_INET6, u->table, dev, li->ifindex, li->point_to_point,
                                                &u->route6)
                      : firc_iface_route_remove(r, AF_INET6, u->table, dev, &u->route6);
    }
    if (err != FIRC_OK) {
        FIRC_WARN("tunnel uplink %s: the IPv6 default was not written: %s", dev, firc_err_str(err));
    }
}

static firc_err_t write_default(firc_rtnl_t *r, const char *dev, firc_tunnet_uplink_t *u) {
    u->routed = false;
    if (dev == NULL) { return FIRC_OK; }
    firc_link_info_t li;
    bool found;
    firc_err_t err = firc_rtnl_link_by_name(r, dev, &li, &found);
    if (err != FIRC_OK || !found || !li.up) { return err; }
    snprintf(u->dev, sizeof(u->dev), "%s", dev);
    err = firc_iface_route_update(r, AF_INET, u->table, dev, li.ifindex, li.point_to_point, &u->route);
    u->routed = err == FIRC_OK && u->route.present;
    write_default6(r, dev, &li, u);
    return err;
}

static firc_err_t fixed_step(firc_rtnl_t *r, unsigned step, bool add, uint32_t table, uint32_t mark) {
    int family = step % 2 == 0 ? AF_INET : AF_INET6;
    if (step < 2) {
        uint8_t type = family == AF_INET ? RTN_BLACKHOLE : RTN_UNREACHABLE;
        return add ? firc_rtnl_route_add_reject(r, family, table, 20, type)
                   : firc_rtnl_route_del_reject(r, family, table, 20, type);
    }
    return add ? firc_rtnl_rule_add(r, family, mark, FIRC_MARK_GROUP_MASK, table, FIRC_RULE_PRIORITY_TUNNEL)
               : firc_rtnl_rule_del(r, family, mark, FIRC_MARK_GROUP_MASK, table, FIRC_RULE_PRIORITY_TUNNEL);
}

firc_err_t firc_tunnet_uplink_apply(firc_rtnl_t *r, uint32_t start_table, const firc_tunnel_t *t,
                                    const firc_tunnels_t *all, firc_tunnet_uplink_t *out) {
    memset(out, 0, sizeof(*out));
    if (t->uplink == FIRC_UPLINK_AUTO) {
        out->routed = true;
        return FIRC_OK;
    }
    char owner[24];
    snprintf(owner, sizeof(owner), FIRC_MARK_TUNNEL_OWNER "%s", t->id);
    uint32_t field = 0, table = 0;
    firc_err_t err = firc_rtnl_alloc_mark_table(r, start_table, &table);
    if (err == FIRC_OK) { err = firc_rtnl_alloc_mark_field_for(r, owner, &field); }
    if (err != FIRC_OK) { return err; }
    uint32_t mark = firc_mark_group_value(field);
    for (unsigned step = 0; step < 4; step++) {
        err = fixed_step(r, step, true, table, mark);
        if (err != FIRC_OK) {
            while (step-- > 0) { (void)fixed_step(r, step, false, table, mark); }
            return err;
        }
    }
    out->table = table;
    out->mark = mark;
    return write_default(r, firc_tunnet_uplink_device(t, all), out);
}

static void keep_first(firc_err_t *first, firc_err_t err) {
    if (*first == FIRC_OK) { *first = err; }
}

firc_err_t firc_tunnet_uplink_remove(firc_rtnl_t *r, const firc_tunnet_uplink_t *u) {
    if (u->mark == 0) { return FIRC_OK; }
    firc_iface_route_t route = u->route, route6 = u->route6;
    firc_err_t first = fixed_step(r, 2, false, u->table, u->mark);
    keep_first(&first, fixed_step(r, 3, false, u->table, u->mark));
    keep_first(&first, firc_iface_route_remove(r, AF_INET, u->table, u->dev, &route));
    keep_first(&first, firc_iface_route_remove(r, AF_INET6, u->table, u->dev, &route6));
    keep_first(&first, fixed_step(r, 0, false, u->table, u->mark));
    keep_first(&first, fixed_step(r, 1, false, u->table, u->mark));
    return first;
}

firc_err_t firc_tunnet_uplink_refresh(firc_rtnl_t *r, const firc_tunnel_t *t, const firc_tunnels_t *all,
                                      firc_tunnet_uplink_t *u) {
    if (u->mark == 0) { return FIRC_OK; }
    const char *dev = firc_tunnet_uplink_device(t, all);
    if (u->dev[0] != 0 && (dev == NULL || strcmp(dev, u->dev) != 0)) {
        firc_err_t err = firc_iface_route_remove(r, AF_INET, u->table, u->dev, &u->route);
        keep_first(&err, firc_iface_route_remove(r, AF_INET6, u->table, u->dev, &u->route6));
        if (err != FIRC_OK) { return err; }
        memset(&u->route, 0, sizeof(u->route));
        memset(&u->route6, 0, sizeof(u->route6));
        u->dev[0] = 0;
        u->routed = false;
    }
    return write_default(r, dev, u);
}
