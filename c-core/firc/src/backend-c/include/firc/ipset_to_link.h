#ifndef FIRC_IPSET_TO_LINK_H
#define FIRC_IPSET_TO_LINK_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip.h"
#include "firc/subnet.h"
#include "firc/iptables.h"
#include "firc/conntrack.h"
#include "firc/rtnl.h"

#define FIRC_IPSET_TO_LINK_BLACKHOLE "blackhole"

/* True for the two pseudo-interfaces that route nowhere: blackhole and block. */
bool firc_iface_is_sink(const char *iface);

#define FIRC_IPSET_TO_LINK_IFACES_MAX 4

typedef struct firc_ipset_to_link firc_ipset_to_link_t;

#define FIRC_IFACE_ROUTE_METRIC 10u

typedef struct firc_iface_route {
    bool present;
    bool has_gw;
    uint8_t gw[16];
    uint8_t gw_len;
} firc_iface_route_t;

/* Writes the table's default via the interface (gateway if it has one), keeping the last gateway when none is found. */
firc_err_t firc_iface_route_update(firc_rtnl_t *r, int family, uint32_t table, const char *iface_name,
                                  int ifindex, bool point_to_point, firc_iface_route_t *st);

/* Deletes the default update wrote, if any. */
firc_err_t firc_iface_route_remove(firc_rtnl_t *r, int family, uint32_t table, const char *iface_name,
                                  firc_iface_route_t *st);

typedef enum {
    FIRC_NF_SRC_ADDR = 1,
    FIRC_NF_SRC_MAC = 2,
    FIRC_NF_SRC_MARK = 3,
} firc_nf_src_kind_t;

/* Compared by what it renders, so padding and unused fields need no memset. */
typedef struct firc_nf_source {
    uint8_t kind;     /* firc_nf_src_kind_t */
    uint8_t family;   /* 4 or 6 */
    uint8_t prefix;   /* host bits cleared */
    uint8_t addr[16];
    uint8_t mac[6];
    uint32_t mark;
} firc_nf_source_t;

typedef struct firc_nf_devices {
    bool active;
    bool allow_all;
    firc_nf_source_t *deny;
    size_t n_deny;
    firc_nf_source_t *allow;
    size_t n_allow;
    size_t cap_deny;
    size_t cap_allow;
} firc_nf_devices_t;

/* FIRC_ERR_INVAL if `<chain>D` does not fit in cap. */
firc_err_t firc_ipset_to_link_devices_chain_of(const char *chain, char *out, size_t cap);

/* Pointers borrowed; ipt4/ipt6 nullable (not both), base chains already registered. Rules read from snap. */
firc_ipset_to_link_t *firc_ipset_to_link_new(const char *chain_name, const char *iface_name,
                                         firc_ipt_t *ipt4, firc_ipt_t *ipt6,
                                         firc_rtnl_t *rtnl, uint32_t start_idx,
                                             const firc_fakeip_t *pool, const char *group_id,
                                             const firc_fakeip_snapshot_t *(*snap)(void *ud),
                                             void *snap_ud);
void firc_ipset_to_link_free(firc_ipset_to_link_t *l);

typedef bool (*firc_tunnel_dev_fn)(void *ud, const char *iface);
/* fn says whether iface is a configured tunnel's device, whose absence is said at INFO; NULL: none is. */
void firc_ipset_to_link_set_tunnel_dev(firc_ipset_to_link_t *l, firc_tunnel_dev_fn fn, void *ud);

/* Drops flows whose mark has group_value plus the handled bit; no-op for NULL ct or zero value. */
void firc_ipset_to_link_flush_mark(firc_ct_t *ct, uint32_t group_value, const char *who);

/* The group field given at enable, or 0. */
uint32_t firc_ipset_to_link_mark_field(const firc_ipset_to_link_t *l);

bool firc_ipset_to_link_has_iface_route(const firc_ipset_to_link_t *l, int family);

/* Takes a copy of the subnet rules; call under the netfilter lock. *changed is nullable. */
firc_err_t firc_ipset_to_link_set_subnets(firc_ipset_to_link_t *l, const firc_ipv4_subnet_t *v4,
                                          size_t n4, const firc_ipv6_subnet_t *v6, size_t n6,
                                          bool *changed);

typedef struct firc_nf_prefix {
    uint8_t family;
    uint8_t cidr;
    uint8_t addr[16];
} firc_nf_prefix_t;

typedef struct firc_nf_prefixes {
    firc_nf_prefix_t *v;
    size_t n;
} firc_nf_prefixes_t;

#define FIRC_SET_NAME_MAX 32

typedef struct firc_nf_print {
    size_t n;
    uint64_t hash;
} firc_nf_print_t;

/* The prefixes of the subnets, proto and ports dropped; out is overwritten, FIRC_ERR_NOMEM leaves it empty. */
firc_err_t firc_nf_prefixes_of(const firc_ipv4_subnet_t *v4, size_t n4, const firc_ipv6_subnet_t *v6, size_t n6,
                               firc_nf_prefixes_t *out);
/* firc_nf_prefixes_of the subnets set_subnets last took. */
firc_err_t firc_ipset_to_link_prefixes(const firc_ipset_to_link_t *l, firc_nf_prefixes_t *out);
/* True for a rule with no protocol and a length of at least 1: what a hash:net member can say. */
bool firc_subnet4_is_bare(const firc_ipv4_subnet_t *s);
bool firc_subnet6_is_bare(const firc_ipv6_subnet_t *s);
/* The print of the bare prefixes of family 4 or 6, taken when set_subnets stored them; n 0 when none. */
firc_nf_print_t firc_ipset_to_link_bare_print(const firc_ipset_to_link_t *l, uint8_t family);
/* Test seam: how many times firc_ipset_to_link_bare built a list. */
size_t firc_ipset_to_link_bare_builds_for_test(void);
/* The bare prefixes of one family (4 or 6), sorted and unique; out overwritten, free with firc_nf_prefixes_clear. */
firc_err_t firc_ipset_to_link_bare(const firc_ipset_to_link_t *l, uint8_t family, firc_nf_prefixes_t *out);
/* The count and an FNV-1a 64 over each member's cidr byte then its address bytes, in list order. */
firc_nf_print_t firc_nf_prefixes_print(const firc_nf_prefixes_t *p);
/* "<chain>S4" or "<chain>T6" (scratch); FIRC_ERR_INVAL if family is not 4 or 6 or the name does not fit cap. */
firc_err_t firc_ipset_to_link_set_name_of(const char *chain, uint8_t family, bool scratch, char *out, size_t cap);
/* The chain name the link was made with. */
const char *firc_ipset_to_link_chain(const firc_ipset_to_link_t *l);
/* Whether a prefix of `was` is covered by no prefix of `now` in its family. */
bool firc_nf_prefixes_narrow(const firc_nf_prefixes_t *was, const firc_nf_prefixes_t *now);
/* Prefixes of was not covered by now (same family rules as firc_nf_prefixes_narrow); out overwritten. */
firc_err_t firc_nf_prefixes_lost(const firc_nf_prefixes_t *was, const firc_nf_prefixes_t *now, firc_nf_prefixes_t *out);
/* Joins the prefixes of p in family into to, which stays sorted and unique; n log n plus linear in to->n.
   FIRC_ERR_NOMEM leaves to as it was. */
firc_err_t firc_nf_prefixes_join(firc_nf_prefixes_t *to, const firc_nf_prefix_t *p, size_t n, uint8_t family);
void firc_nf_prefixes_clear(firc_nf_prefixes_t *p);

void firc_nf_devices_clear(firc_nf_devices_t *d);
/* Appends unless an equal-rendering source is there; FIRC_ERR_NOMEM. */
firc_err_t firc_nf_devices_push(firc_nf_devices_t *d, bool deny, const firc_nf_source_t *s);
bool firc_nf_devices_equal(const firc_nf_devices_t *a, const firc_nf_devices_t *b);
/* Whether `now` stops marking a device `was` marked (compared as rendered sets). */
bool firc_nf_devices_narrows(const firc_nf_devices_t *was, const firc_nf_devices_t *now);
/* dst is overwritten, not freed; FIRC_ERR_NOMEM leaves it zeroed. */
firc_err_t firc_nf_devices_copy(firc_nf_devices_t *dst, const firc_nf_devices_t *src);
/* Takes a copy; call under the netfilter lock. *changed is nullable. */
firc_err_t firc_ipset_to_link_set_devices(firc_ipset_to_link_t *l, const firc_nf_devices_t *d, bool *changed);
/* The copy set_devices last took; the loop thread may read it without the lock. */
const firc_nf_devices_t *firc_ipset_to_link_devices(const firc_ipset_to_link_t *l);

/* Test seam: stages the rules as a rebuild would, without enable. */
firc_err_t firc_ipset_to_link_stage_for_test(firc_ipset_to_link_t *l);

/* For the engine's family: one MARK and CONNMARK on the set <chain>S4|S6 when a bare prefix exists, then one per other prefix. */
firc_err_t firc_ipset_to_link_build_subnet_rules(firc_ipt_t *ipt, const char *chain_name,
                                                 uint32_t mark, const firc_ipv4_subnet_t *v4,
                                                 size_t n4, const firc_ipv6_subnet_t *v6,
                                                 size_t n6);

/* Same, each rule jumping to the devices chain when dev is active. */
firc_err_t firc_ipset_to_link_build_subnet_rules_dev(firc_ipt_t *ipt, const char *chain_name, uint32_t mark,
                                                     const firc_ipv4_subnet_t *v4, size_t n4,
                                                     const firc_ipv6_subnet_t *v6, size_t n6,
                                                     const firc_nf_devices_t *dev);

/* Routes and ip rules only; the chains are the next pass's. On failure *step (nullable) names what failed and everything put in is taken back. */
firc_err_t firc_ipset_to_link_enable(firc_ipset_to_link_t *l, const char **step);
/* Takes the ip rule, then the routes; flows and chains are the caller's. */
firc_err_t firc_ipset_to_link_disable(firc_ipset_to_link_t *l);
/* Disables l and frees it, leaving each reply rule newer (nullable) holds on the same field and iif. */
firc_err_t firc_ipset_to_link_release(firc_ipset_to_link_t *l, const firc_ipset_to_link_t *newer);

/* Replaces the list (1..FIRC_IPSET_TO_LINK_IFACES_MAX) before enable; entry `active` carries the default unless blocked. */
firc_err_t firc_ipset_to_link_set_ifaces(firc_ipset_to_link_t *l, const char *const *names, size_t n, size_t active,
                                         bool blocked);
/* Moves the default to entry `to`, deleting the old one before writing the new; blocked leaves only the blackhole. */
firc_err_t firc_ipset_to_link_set_active(firc_ipset_to_link_t *l, size_t to, bool blocked);
/* The active entry's index; *blocked (nullable) says whether the table is down to its blackhole. */
size_t firc_ipset_to_link_active(const firc_ipset_to_link_t *l, bool *blocked);
const char *firc_ipset_to_link_active_iface(const firc_ipset_to_link_t *l);

/* Re-stages the group's chains for a full rebuild without committing; no-op unless enabled. */
firc_err_t firc_ipset_to_link_prepare_iptables(firc_ipset_to_link_t *l);

/* The rules a group's chains carry, built without netlink (for tests). */
firc_err_t firc_ipset_to_link_build_rules(firc_ipt_t *ipt, const char *chain_name,
                                          const char *iface_name,
                                          uint32_t mark, uint32_t mark_field,
                                          const firc_fakeip_snapshot_t *snap,
                                          const char *group_id);

/* Same, with each chunk jumping to the devices chain when dev is active. */
firc_err_t firc_ipset_to_link_build_rules_dev(firc_ipt_t *ipt, const char *chain_name, const char *iface_name,
                                              uint32_t mark, uint32_t mark_field,
                                              const firc_fakeip_snapshot_t *snap, const char *group_id,
                                              const firc_nf_devices_t *dev);

/* Same, for every entry of a list of 1..FIRC_IPSET_TO_LINK_IFACES_MAX; a list of 2 or more guards through `<chain>E`. */
firc_err_t firc_ipset_to_link_build_rules_list(firc_ipt_t *ipt, const char *chain_name, const char *const *ifaces,
                                               size_t n, uint32_t mark, uint32_t mark_field,
                                               const firc_fakeip_snapshot_t *snap, const char *group_id,
                                               const firc_nf_devices_t *dev);

/* Call for each link whose interface the netlink event was about. */
firc_err_t firc_ipset_to_link_on_link_up(firc_ipset_to_link_t *l);
firc_err_t firc_ipset_to_link_on_addr_change(firc_ipset_to_link_t *l);
/* Says again, at most every FIRC_LOGREP_REMIND_MS, that the interface is missing or down; for a periodic timer. */
void firc_ipset_to_link_remind(firc_ipset_to_link_t *l);

#endif /* FIRC_IPSET_TO_LINK_H */
