#ifndef FIRC_RULESET_H
#define FIRC_RULESET_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/fakeip.h"
#include "firc/err.h"
#include "firc/ipset_to_link.h"
#include "firc/iptables.h"
#include "firc/logrep.h"
#include "firc/models.h"
#include "firc/conntrack.h"
#include "firc/devices.h"
#include "firc/rtnl.h"

typedef struct firc_ruleset firc_ruleset_t;

/* Selector lookups; every function nullable. Called on the loop thread only. */
typedef struct firc_ruleset_lookup {
    firc_devsel_mark_fn policy_mark;
    firc_devsel_hosts_fn host_addrs;
    firc_devsel_policy_hosts_fn policy_hosts;
    firc_devsel_policy_nets_fn policy_nets;
    void *ud;
    bool (*policies_read)(void *ud);
} firc_ruleset_lookup_t;

/* All pointers borrowed and must outlive every ruleset built with them; pool, lookup nullable. */
typedef struct firc_ruleset_deps {
    const firc_fakeip_t *pool;
    const firc_fakeip_snapshot_t *(*snap)(void *ud);
    void *snap_ud;
    firc_ipt_t *ipt4;
    firc_ipt_t *ipt6;
    firc_rtnl_t *rtnl;
    const char *chain_prefix;
    uint32_t start_idx;
    const firc_ruleset_lookup_t *lookup;
    firc_tunnel_dev_fn tunnel_dev;
    void *tunnel_ud;
} firc_ruleset_deps_t;

/* group borrowed (must outlive the ruleset); deps copied. Mutated on the loop thread only. */
firc_ruleset_t *firc_ruleset_new(const firc_group_t *group, const firc_ruleset_deps_t *deps);
void firc_ruleset_free(firc_ruleset_t *rs);

const firc_group_t *firc_ruleset_group(const firc_ruleset_t *rs);
/* Shifted group field (firc_mark_group_value), or 0 when not in the kernel. */
uint32_t firc_ruleset_mark_field(const firc_ruleset_t *rs);
bool firc_ruleset_has_iface_route(const firc_ruleset_t *rs, int family);
firc_group_t *firc_ruleset_group_mut(firc_ruleset_t *rs);
bool firc_ruleset_runtime_enabled(const firc_ruleset_t *rs);
/* Retry state for a failed enable; kept here, read and written by the app on the loop. */
typedef struct firc_ruleset_retry {
    uint32_t backoff_ms; /* 0: no failure since the last enable that held */
    uint64_t at_ms;      /* monotonic ms */
    firc_logrep_t said;
} firc_ruleset_retry_t;
firc_ruleset_retry_t *firc_ruleset_retry(firc_ruleset_t *rs);
/* Enabled at runtime and in config, holding its link; only routed groups get DNAT rules. */
bool firc_ruleset_routed(const firc_ruleset_t *rs);
/* Enabled at runtime and in config; may be in the DNS view without being routed. */
bool firc_ruleset_in_view(const firc_ruleset_t *rs);
void firc_ruleset_chain_name_for(const char *prefix, firc_id_t id, char *out, size_t cap);
/* "" when it does not fit in cap. */
void firc_ruleset_devices_chain_name_for(const char *prefix, firc_id_t id, char *out, size_t cap);

/* `step` as firc_ipset_to_link_enable, plus "memory". */
firc_err_t firc_ruleset_enable(firc_ruleset_t *rs, const char **step);
/* Ip rule and routes only; the next pass drops the chains, the app's queue the flows. */
firc_err_t firc_ruleset_disable(firc_ruleset_t *rs);
/* As firc_ruleset_disable, but its ip rules and routes stay: the link is handed back (NULL when none) to disable and free. */
firc_ipset_to_link_t *firc_ruleset_disable_hold(firc_ruleset_t *rs);
/* The entry and block state the next enable starts a list on; out of range starts on entry 0. */
void firc_ruleset_set_start(firc_ruleset_t *rs, size_t active, bool blocked);
/* The link's active entry; 0 and not blocked while there is no link. */
size_t firc_ruleset_active(const firc_ruleset_t *rs, bool *blocked);
/* The link's active interface, else the group's entry 0. */
const char *firc_ruleset_active_iface(const firc_ruleset_t *rs);
/* See firc_ipset_to_link_set_active; FIRC_ERR_STATE while not routed. */
firc_err_t firc_ruleset_set_active(firc_ruleset_t *rs, size_t to, bool blocked);
/* The routed group's link, else NULL; loop thread only. */
const firc_ipset_to_link_t *firc_ruleset_link(const firc_ruleset_t *rs);

/* Re-stages the chains for a full rebuild; ip rule and routes are left alone. No-op unless enabled. */
firc_err_t firc_ruleset_prepare_iptables(firc_ruleset_t *rs);

typedef enum firc_ruleset_pass {
    FIRC_RULESET_PASS_IN = 0,
    FIRC_RULESET_PASS_LEFT_OUT,
    FIRC_RULESET_PASS_ON_TRIAL,
} firc_ruleset_pass_t;
firc_ruleset_pass_t firc_ruleset_pass(const firc_ruleset_t *rs);
void firc_ruleset_set_pass(firc_ruleset_t *rs, firc_ruleset_pass_t pass);
void firc_ruleset_owe_flush(firc_ruleset_t *rs);
bool firc_ruleset_take_flush(firc_ruleset_t *rs);
/* Whether a set of the group failed to take its current prefixes; any thread. */
bool firc_ruleset_sets_stale(const firc_ruleset_t *rs);
void firc_ruleset_set_sets_stale(firc_ruleset_t *rs, bool stale);

/* Hands the subnet prefixes and devices to the link; the next pass writes them. *chain_changed is set even on error. */
firc_err_t firc_ruleset_sync(firc_ruleset_t *rs, bool *chain_changed);

/* Renders a selector into a devices chain; *out is filled from zero and the caller clears it. */
firc_err_t firc_ruleset_render_devices(const firc_group_t *g, const firc_ruleset_lookup_t *lk,
                                       firc_nf_devices_t *out);
bool firc_ruleset_has_devices(const firc_ruleset_t *rs);
bool firc_ruleset_devices_follow_table(const firc_ruleset_t *rs);
/* Re-renders a routed group's devices chain; loop thread, under the netfilter lock. */
firc_err_t firc_ruleset_refresh_devices(firc_ruleset_t *rs, bool *changed);
/* Render-and-compare only; loop thread, no lock needed. */
firc_err_t firc_ruleset_devices_would_move(const firc_ruleset_t *rs, bool *moved);
/* Whether the last sync or refresh dropped a device the old chain marked; false after enable. */
bool firc_ruleset_devices_narrowed(const firc_ruleset_t *rs);
/* NULL while not routed; loop thread only. */
const firc_nf_devices_t *firc_ruleset_devices(const firc_ruleset_t *rs);
/* The prefixes the link holds, empty while not routed; out is overwritten. */
firc_err_t firc_ruleset_prefixes(const firc_ruleset_t *rs, firc_nf_prefixes_t *out);

/* The routed group's bare prefixes of family 4 or 6, sorted and unique; empty while not routed. */
firc_err_t firc_ruleset_bare(const firc_ruleset_t *rs, uint8_t family, firc_nf_prefixes_t *out);
/* The routed group's bare print of family (see firc_ipset_to_link_bare_print); n 0 while not routed. */
firc_nf_print_t firc_ruleset_bare_print(const firc_ruleset_t *rs, uint8_t family);
/* Whether the routed group holds a bare prefix of family; false while not routed. */
bool firc_ruleset_has_bare(const firc_ruleset_t *rs, uint8_t family);
/* The routed group's live or scratch set name for family; FIRC_ERR_STATE while not routed. */
firc_err_t firc_ruleset_set_name(const firc_ruleset_t *rs, uint8_t family, bool scratch, char *out, size_t cap);

/* Test seam: proto is 0, IPPROTO_TCP or IPPROTO_UDP; ports in iptables form, "" for all. */
typedef void (*firc_ruleset_plan_fn)(int family, const uint8_t *addr, uint8_t cidr, uint8_t proto, const char *ports,
                                     void *ud);

firc_err_t firc_ruleset_plan_subnets(const firc_group_t *g, firc_ruleset_plan_fn fn, void *ud);

/* Call only for rulesets whose interface the netlink event names. */
firc_err_t firc_ruleset_on_link_up(firc_ruleset_t *rs);
firc_err_t firc_ruleset_on_addr_change(firc_ruleset_t *rs);
/* See firc_ipset_to_link_remind. */
void firc_ruleset_remind_link(firc_ruleset_t *rs);

#endif /* FIRC_RULESET_H */
