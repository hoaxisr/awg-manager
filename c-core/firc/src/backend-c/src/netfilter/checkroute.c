#include "firc/checkroute.h"

#include <linux/rtnetlink.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

#include "firc/mark.h"

static firc_err_t write_default(firc_rtnl_t *r, firc_checkroute_t *c) {
    firc_link_info_t li;
    bool found = false;
    firc_err_t err = firc_rtnl_link_by_name(r, c->dev, &li, &found);
    if (err != FIRC_OK || !found || !li.up) {
        c->route.present = false;
        return err;
    }
    return firc_iface_route_update(r, AF_INET, c->table, c->dev, li.ifindex, li.point_to_point, &c->route);
}

firc_err_t firc_checkroute_apply(firc_rtnl_t *r, uint32_t start_table, const char *dev, firc_checkroute_t *out) {
    memset(out, 0, sizeof(*out));
    if (dev == NULL || dev[0] == '\0' || strlen(dev) >= sizeof(out->dev)) { return FIRC_ERR_INVAL; }
    snprintf(out->dev, sizeof(out->dev), "%s", dev);
    uint32_t table = 0, field = 0;
    firc_err_t err = firc_rtnl_alloc_mark_table(r, start_table, &table);
    if (err == FIRC_OK) { err = firc_rtnl_alloc_mark_field(r, &field); }
    if (err != FIRC_OK) { return err; }
    err = firc_rtnl_route_add_reject(r, AF_INET, table, 20, RTN_BLACKHOLE);
    if (err != FIRC_OK) { return err; }
    err = firc_rtnl_rule_add(r, AF_INET, firc_mark_group_value(field), FIRC_MARK_GROUP_MASK, table,
                             FIRC_RULE_PRIORITY_TUNNEL);
    if (err != FIRC_OK) {
        (void)firc_rtnl_route_del_reject(r, AF_INET, table, 20, RTN_BLACKHOLE);
        return err;
    }
    out->table = table;
    out->mark = firc_mark_group_value(field);
    (void)write_default(r, out);
    return FIRC_OK;
}

firc_err_t firc_checkroute_refresh(firc_rtnl_t *r, firc_checkroute_t *c) {
    if (c->mark == 0) { return FIRC_OK; }
    return write_default(r, c);
}

firc_err_t firc_checkroute_remove(firc_rtnl_t *r, firc_checkroute_t *c) {
    if (c->mark == 0) { return FIRC_OK; }
    firc_err_t first = firc_rtnl_rule_del(r, AF_INET, c->mark, FIRC_MARK_GROUP_MASK, c->table, FIRC_RULE_PRIORITY_TUNNEL);
    firc_err_t err = firc_iface_route_remove(r, AF_INET, c->table, c->dev, &c->route);
    if (first == FIRC_OK) { first = err; }
    err = firc_rtnl_route_del_reject(r, AF_INET, c->table, 20, RTN_BLACKHOLE);
    if (first == FIRC_OK) { first = err; }
    memset(c, 0, sizeof(*c));
    return first;
}
