#ifndef FIRC_RTNL_H
#define FIRC_RTNL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <linux/netlink.h>

#include "firc/err.h"

typedef struct firc_rtnl firc_rtnl_t;

firc_rtnl_t *firc_rtnl_open(void);
/* Test seam: wraps a socket the caller holds; takes ownership of fd. */
firc_rtnl_t *firc_rtnl_open_fd(int fd);
void firc_rtnl_close(firc_rtnl_t *r);

/* ip rule fwmark mark/mask at priority; both are required. Deleting a missing rule is FIRC_OK. */
firc_err_t firc_rtnl_rule_add(firc_rtnl_t *r, int family, uint32_t mark, uint32_t mask,
                              uint32_t table, uint32_t priority);
firc_err_t firc_rtnl_rule_del(firc_rtnl_t *r, int family, uint32_t mark, uint32_t mask,
                              uint32_t table, uint32_t priority);
/* Kernel ABI value; the SDK uapi headers predate the attribute (3.12). */
#define FIRC_FRA_SUPPRESS_PREFIXLEN 14

/* Rule for packets in on iif, with suppress_prefixlength 0; firc_rtnl_rule_del removes it too. */
firc_err_t firc_rtnl_rule_add_reply(firc_rtnl_t *r, int family, uint32_t mark, uint32_t mask,
                                    const char *iif, uint32_t table, uint32_t priority);

/* Deletes the reply rule of mark that names iif; another iif's twin of the same mark stays. ENOENT is success. */
firc_err_t firc_rtnl_rule_del_reply(firc_rtnl_t *r, int family, uint32_t mark, uint32_t mask, const char *iif,
                                    uint32_t table, uint32_t priority);

/* Builds the rule request without sending it (for tests). */
struct nlmsghdr *firc_rtnl_build_rule(void *buf, bool add, uint32_t seq, int family, uint32_t mark,
                                      uint32_t mask, uint32_t table, uint32_t priority);

/* Reject-type (blackhole, unreachable) default route; written with NLM_F_REPLACE. ESRCH on delete is FIRC_OK. */
firc_err_t firc_rtnl_route_add_reject(firc_rtnl_t *r, int family, uint32_t table, uint32_t priority,
                                      uint8_t rtm_type);
firc_err_t firc_rtnl_route_del_reject(firc_rtnl_t *r, int family, uint32_t table, uint32_t priority,
                                      uint8_t rtm_type);

/* Default route via oif and optional gw; *unavailable reports an interface down or not ready for the family. */
firc_err_t firc_rtnl_route_add_iface(firc_rtnl_t *r, int family, uint32_t table, uint32_t priority,
                                 int oif, const uint8_t *gw, uint8_t gw_len, bool *unavailable);
firc_err_t firc_rtnl_route_del_iface(firc_rtnl_t *r, int family, uint32_t table, uint32_t priority,
                                 int oif, const uint8_t *gw, uint8_t gw_len);

/* rtm_protocol on every route firc writes; removal touches only routes carrying it. */
#define FIRC_RTPROT 102
/* Unreachable route to a pool prefix (dst in wire order); FIRC_ERR_EXIST if a foreign route holds it. */
firc_err_t firc_rtnl_route_add_unreachable(firc_rtnl_t *r, int family, uint32_t table,
                                           uint32_t priority, const uint8_t *dst,
                                           uint8_t prefix_len);
firc_err_t firc_rtnl_route_del_unreachable(firc_rtnl_t *r, int family, uint32_t table,
                                           uint32_t priority, const uint8_t *dst,
                                           uint8_t prefix_len);

#define FIRC_RTNL_REQBUF 1024

/* Builds the unreachable-route request into a zeroed FIRC_RTNL_REQBUF buf, unsent; NULL on bad args. */
struct nlmsghdr *firc_rtnl_build_unreachable(void *buf, bool add, uint32_t seq, int family,
                                             uint32_t table, uint32_t priority,
                                             const uint8_t *dst, uint8_t prefix_len);

typedef struct firc_link_info {
    int ifindex;
    bool up;
    bool carrier;
    bool point_to_point;
} firc_link_info_t;

/* Test seam: the name-to-index lookup firc_rtnl_link_by_name uses; NULL restores if_nametoindex. */
void firc_rtnl_set_nametoindex_for_test(unsigned (*fn)(const char *name));

/* *found=false with FIRC_OK when the interface does not exist yet. */
firc_err_t firc_rtnl_link_by_name(firc_rtnl_t *r, const char *name, firc_link_info_t *out, bool *found);

/* First gateway of the interface's default route; *found=false with FIRC_OK when none. */
firc_err_t firc_rtnl_gateway_for_iface(firc_rtnl_t *r, int family, int ifindex, bool *found, uint8_t *gw,
                                   uint8_t *gw_len);

/* Also sets *gatewayless when the default route on the interface has no next hop. */
firc_err_t firc_rtnl_gateway_for_iface2(firc_rtnl_t *r, int family, int ifindex, bool *found, uint8_t *gw,
                                        uint8_t *gw_len, bool *gatewayless);

/* The oif of every IPv4 default in table main, from a dump filtered here (not a lookup through the ip rules), any
   metric, each next hop of a multipath route too; *n counts them all, at most cap are written. */
firc_err_t firc_rtnl_main_defaults(firc_rtnl_t *r, int *oifs, size_t cap, size_t *n);

/* Whether the interface carries IPv6: a usable global-scope address on it, or a v6 default through it. */
firc_err_t firc_rtnl_iface_carries_v6(firc_rtnl_t *r, int ifindex, bool *carries);

/* Lowest unused mark/table index from start_idx; tables 0/253/254/255 count as used. */
#define FIRC_RTNL_TABLE_END 0x7ffffffeu
firc_err_t firc_rtnl_alloc_mark_table(firc_rtnl_t *r, uint32_t start_idx, uint32_t *out_idx);

/* Lowest group field no ip rule uses; FIRC_ERR_LIMIT when all are taken. */
firc_err_t firc_rtnl_alloc_mark_field(firc_rtnl_t *r, uint32_t *out_field);

/* Same, but an owner gets back the field it had (or was seeded with) while that field is free. */
firc_err_t firc_rtnl_alloc_mark_field_for(firc_rtnl_t *r, const char *owner, uint32_t *out_field);

/* Call only when the owner leaves the config, not on a teardown before re-enable. */
void firc_rtnl_forget_mark_field(firc_rtnl_t *r, const char *owner);

typedef struct {
    const char *owner;
    uint32_t field;
} firc_rtnl_field_t;
typedef void (*firc_rtnl_fields_fn)(void *ud, const firc_rtnl_field_t *v, size_t n);

/* Pre-assigns a field to an owner from a loaded map; EXIST when either is already in the table,
 * INVAL outside 1..255 or for an owner too long. Does not notify. */
firc_err_t firc_rtnl_seed_mark_field(firc_rtnl_t *r, const char *owner, uint32_t field);
/* Called with the whole table after every change of an assignment; NULL stops it. */
void firc_rtnl_watch_mark_fields(firc_rtnl_t *r, firc_rtnl_fields_fn fn, void *ud);
/* Calls fn once with the whole table as it is now. */
void firc_rtnl_fields_now(const firc_rtnl_t *r, firc_rtnl_fields_fn fn, void *ud);

/* Removes ip rules a previous instance left (firc's mask at firc's priority); call at startup. */
firc_err_t firc_rtnl_clean_stale_rules(firc_rtnl_t *r, size_t *removed);

/* Removes every route carrying FIRC_RTPROT; *left is what a second dump still shows. */
firc_err_t firc_rtnl_purge_tagged_routes(firc_rtnl_t *r, size_t *removed, size_t *left);

#endif /* FIRC_RTNL_H */
