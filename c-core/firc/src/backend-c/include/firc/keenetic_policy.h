#ifndef FIRC_KEENETIC_POLICY_H
#define FIRC_KEENETIC_POLICY_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <cjson/cJSON.h>

#include "firc/devices.h"
#include "firc/err.h"
#include "firc/fakeip_addr.h"

typedef struct firc_kn_policy_map firc_kn_policy_map_t;

/* Parses the hotspot and policy documents; policies_json may be NULL; FIRC_ERR_PROTO on a bad shape. */
firc_err_t firc_kn_policy_map_parse(const char *hotspot_json, const char *policies_json,
                                    firc_kn_policy_map_t **out);
void firc_kn_policy_map_free(firc_kn_policy_map_t *m);

typedef struct firc_kn_segment {
    char iface[64];
    char policy[64];
} firc_kn_segment_t;

size_t firc_kn_hotspot_segments_parse(const char *rc_hotspot_json, firc_kn_segment_t *out, size_t cap);
/* Adds an interface's IPv4 network as a segment of policy; FIRC_ERR_INVAL for a bad address or mask. */
firc_err_t firc_kn_policy_map_add_segment(firc_kn_policy_map_t *m, const char *policy, const char *address,
                                          const char *mask);
/* Adds each binding in seg whose interface has an IPv4 address and mask in interfaces_json; others are skipped. */
firc_err_t firc_kn_policy_map_add_segments(firc_kn_policy_map_t *m, const firc_kn_segment_t *seg, size_t n,
                                           const char *interfaces_json);
/* Whether client is in the policy (name or description): its rc entry's, none for a bare one, else own, else segment's. */
bool firc_kn_policy_map_has(const firc_kn_policy_map_t *m, const char *policy, const firc_ip_t *client);
/* The other addresses of the host at client, at most cap; 0 when the table does not list it. */
size_t firc_kn_policy_map_device(const firc_kn_policy_map_t *m, const firc_ip_t *client, firc_ip_t *out,
                                 size_t cap);
/* MAC of the host picked at client (the active one on a shared address); false and *mac untouched if none. */
bool firc_kn_policy_map_mac(const firc_kn_policy_map_t *m, const firc_ip_t *client, firc_mac_t *mac);
/* Adds firmware marks from GET /rci/show/ip/policy; after FIRC_ERR_NOMEM the map is unfit and the caller frees it. */
firc_err_t firc_kn_policy_map_add_marks(firc_kn_policy_map_t *m, const char *ip_policy_json);
/* Firmware mark of the policy (name or description); false, *mark untouched, when it has none. */
bool firc_kn_policy_map_mark(const firc_kn_policy_map_t *m, const char *policy, uint32_t *mark);
/* Calls fn for each address of each host with an address inside net/prefix, each host once, in table order. */
firc_err_t firc_kn_policy_map_hosts_in(const firc_kn_policy_map_t *m, const firc_ip_t *net, uint8_t prefix,
                                       bool deny, firc_devsel_addr_fn fn, void *ud);
/* policy_hosts: fn per address of the policy's hosts; policy_nets: fn per segment bound to it. FIRC_OK always. */
firc_err_t firc_kn_policy_map_policy_hosts(const firc_kn_policy_map_t *m, const char *policy, bool deny,
                                           firc_devsel_addr_fn fn, void *ud);
firc_err_t firc_kn_policy_map_policy_nets(const firc_kn_policy_map_t *m, const char *policy, bool deny,
                                          firc_devsel_net_fn fn, void *ud);
typedef firc_err_t (*firc_kn_rci_get_fn)(void *ud, const char *path, char **body);
firc_err_t firc_kn_policy_map_fetch_for_test(firc_kn_rci_get_fn get, void *ud, firc_kn_policy_map_t **out);

/* Every host with a MAC, in table order, as GET /api/v1/system/hosts shows it; NULL only on allocation failure. */
cJSON *firc_kn_policy_map_hosts_json(const firc_kn_policy_map_t *m);
bool firc_kn_policy_map_knows(const firc_kn_policy_map_t *m, const char *policy);
size_t firc_kn_policy_map_count(const firc_kn_policy_map_t *m, const char *policy);

/* One policy as the WebUI offers it; description is empty when it equals the name. */
#define FIRC_KN_POLICY_NAME_MAX 64

typedef struct firc_kn_policy_info {
    char name[FIRC_KN_POLICY_NAME_MAX];
    char description[FIRC_KN_POLICY_NAME_MAX];
    size_t devices;
} firc_kn_policy_info_t;

/* Every policy, at most cap, empty ones included; returns the count written. */
size_t firc_kn_policy_map_list(const firc_kn_policy_map_t *m, firc_kn_policy_info_t *out,
                               size_t cap);

#define FIRC_KN_POLICY_REFRESH_SECS 30u

typedef struct firc_kn_policies firc_kn_policies_t;

firc_kn_policies_t *firc_kn_policies_start(const char *base_url, unsigned refresh_secs);
/* Runs on the refresher's thread after a changed map; false means the announcement was lost and is retried. */
typedef bool (*firc_kn_policies_changed_fn)(void *ud);
/* As start, announcing every changed map to on_change (nullable). */
firc_kn_policies_t *firc_kn_policies_start_notify(const char *base_url, unsigned refresh_secs,
                                                  firc_kn_policies_changed_fn on_change, void *ud);
/* As start, reading RCI through get instead of HTTP. */
firc_kn_policies_t *firc_kn_policies_start_for_test(firc_kn_rci_get_fn get, void *ud, unsigned refresh_secs);
/* 0 when the last finished round ended <= fresh_ms ago (or no refresher); else wakes it, returns the round to wait for. */
uint64_t firc_kn_policies_wake(firc_kn_policies_t *p, unsigned fresh_ms);
/* Rounds finished so far, failed ones included. */
uint64_t firc_kn_policies_rounds(firc_kn_policies_t *p);
/* Ends the refresher and frees everything; blocks until it and every wait_first caller are gone; p is dead after. */
void firc_kn_policies_stop(firc_kn_policies_t *p);
/* Hands over the next map (takes ownership, any thread); the reading thread collects it on its next check. */
void firc_kn_policies_swap(firc_kn_policies_t *p, firc_kn_policy_map_t *m);
/* Refresher round: swaps m in unless it equals the newest handed over (then freed, false). Takes ownership. */
bool firc_kn_policies_offer(firc_kn_policies_t *p, firc_kn_policy_map_t *m);
firc_err_t firc_kn_policies_round_for_test(firc_kn_policies_t *p, firc_kn_rci_get_fn get, void *ud);
size_t firc_kn_policies_retired_for_test(firc_kn_policies_t *p);
const firc_kn_policy_map_t *firc_kn_policies_live_for_test(firc_kn_policies_t *p);
uint32_t firc_kn_policy_map_seed_for_test(const firc_kn_policy_map_t *m);
size_t firc_kn_policy_map_home_slots_for_test(const firc_kn_policy_map_t *m, size_t *slots);
/* Blocks until a map exists or ms pass; any thread; firc_kn_policies_stop waits for it. */
bool firc_kn_policies_wait_first(firc_kn_policies_t *p, unsigned ms);
unsigned firc_kn_policies_waiters_for_test(firc_kn_policies_t *p);

/* Live-map reads below take no lock: all of them must run on one thread (the loop thread). */
/* Every policy of the live map, at most cap; empty before the first read. */
size_t firc_kn_policies_list(firc_kn_policies_t *p, firc_kn_policy_info_t *out, size_t cap);

/* hosts_json of the live map; an empty array for NULL p or before the first read. */
cJSON *firc_kn_policies_hosts_json(firc_kn_policies_t *p);

/* A firc_devsel_policy_fn over the live map; no map yet means nobody is in any policy. */
bool firc_kn_policies_resolve(const char *policy, const firc_ip_t *client, void *ud);
/* A firc_devsel_device_fn over the live map: the host's other addresses and its MAC. */
size_t firc_kn_policies_device(const firc_ip_t *client, firc_ip_t *out, size_t cap, firc_mac_t *mac, void *ud);
/* Whether a map has been read since start; true for NULL ud (no policies to wait for). */
bool firc_kn_policies_read(void *ud);
/* A firc_devsel_mark_fn over the live map; false before the first map. */
bool firc_kn_policies_mark(const char *policy, uint32_t *mark, void *ud);
firc_err_t firc_kn_policies_hosts_in(const firc_ip_t *net, uint8_t prefix, bool deny, firc_devsel_addr_fn fn,
                                     void *fn_ud, void *ud);
firc_err_t firc_kn_policies_policy_hosts(const char *policy, bool deny, firc_devsel_addr_fn fn, void *fn_ud,
                                         void *ud);
firc_err_t firc_kn_policies_policy_nets(const char *policy, bool deny, firc_devsel_net_fn fn, void *fn_ud, void *ud);

#endif
