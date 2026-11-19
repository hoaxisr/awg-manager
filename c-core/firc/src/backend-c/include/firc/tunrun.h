#ifndef FIRC_TUNRUN_H
#define FIRC_TUNRUN_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/ifaddr.h"
#include "firc/ktcp.h"
#include "firc/log.h"
#include "firc/loop.h"
#include "firc/opkgtun.h"
#include "firc/rtnl.h"
#include "firc/tunnels.h"
#include "firc/tunnodes.h"
#include "firc/tunsubs.h"
#include "firc/tunsup.h"

typedef struct firc_tunrun firc_tunrun_t;

/* Fills wan[i] for link[i] from the firmware's interface roles; false when they cannot be read. */
typedef bool (*firc_tunrun_wan_fn)(const char *const *link, size_t n, bool *wan, void *ud);

typedef struct firc_tunrun_lan {
    const char *const *link;
    size_t n_link;
    firc_tunrun_wan_fn wan_of;
    void *wan_ud;
} firc_tunrun_lan_t;

/* Loop thread only, like every call below: tunvless children are spawned from this thread. lan holds the running
   app.link and wan_of, which reads once, when first needed, which entries are WAN (NULL: none is); copied. NULL leaves
   every inbound outside the LAN. */
firc_tunrun_t *firc_tunrun_new(firc_loop_t *loop, firc_rtnl_t *r, uint32_t start_table, const char *binary,
                               const firc_tunrun_lan_t *lan);

/* True when name is in the running app.link and not a WAN interface: what an inbound's listen is judged by. The
   roles are read through lan.wan_of on the first call, not at start. */
bool firc_tunrun_lan_ok(firc_tunrun_t *t, const char *name);

/* Rebuilds every tunnel's inbound lines from the interfaces as they are now and sends the ones that changed. */
void firc_tunrun_refresh_inbounds(firc_tunrun_t *t);

/* Takes want over (left empty); writes uplinks, then starts, restarts and stops tunvless to match.
   An uplink a child no longer needs is removed only once that child has been reaped. */
firc_err_t firc_tunrun_apply(firc_tunrun_t *t, firc_tunnels_t *want);

typedef struct firc_tunrun_router {
    void (*want)(const firc_opkgtun_want_t *w, size_t n, void *ud);
    size_t (*seen)(firc_opkgtun_seen_t *out, size_t cap, uint32_t *swept, void *ud);
    void (*sweep)(void *ud);
    void (*pass)(void *ud);
    int fd;
    void *ud;
    void (*trip)(const char *name, void *ud);
} firc_tunrun_router_t;

/* Takes a copy of r: the opkgtun<N> records are wanted through it, its fd is watched for their states, and every
   spawn asks first; NULL detaches. */
void firc_tunrun_set_router(firc_tunrun_t *t, const firc_tunrun_router_t *r);

/* The first apply; want is NULL when tunnels.yaml did not load. The router sweeps firc's records no tunnel wants
   only after a good load and a good apply. */
firc_err_t firc_tunrun_start(firc_tunrun_t *t, firc_tunnels_t *want);

/* device went down: a ready opkgtun<N> record is checked again. */
void firc_tunrun_link_down(firc_tunrun_t *t, const char *device);

/* The OpkgTun record of tunnel id as last seen, PENDING before any; false when id is not configured or its device
   is not opkgtun<N>. */
bool firc_tunrun_router_of(const firc_tunrun_t *t, const char *id, firc_opkgtun_seen_t *out);

/* Takes subscription bodies from subs from the next apply on; NULL detaches. */
void firc_tunrun_set_subs(firc_tunrun_t *t, firc_tunsubs_t *subs);

/* Where a refresh that drops keys of gone nodes writes the running set; kept, not copied; NULL writes nothing. */
void firc_tunrun_set_path(firc_tunrun_t *t, const char *path);
/* The changed() of firc_tunsubs_new, ud the tunrun: rebuilds every node list from the running config and
   sends the new list to the tunnels whose list changed. */
void firc_tunrun_bodies_changed(void *ud);

/* Copies up to cap states, one per configured tunnel in config order, waiting and failed uplinks included. */
size_t firc_tunrun_states(const firc_tunrun_t *t, firc_tun_state_t *out, size_t cap);

/* True when device is a configured tunnel's and that tunnel carries no traffic: any status but UP. */
bool firc_tunrun_device_down(const firc_tunrun_t *t, const char *device);

/* True when device is a configured tunnel's that is starting or waiting for its first nodes; since_ms (monotonic)
   is when that status began. */
bool firc_tunrun_device_starting(const firc_tunrun_t *t, const char *device, int64_t *since_ms);

/* The running configuration; valid until the next apply. */
const firc_tunnels_t *firc_tunrun_config(const firc_tunrun_t *t);

/* Restarts the child of tunnel id even when nothing changed; FIRC_ERR_NOENT unknown, FIRC_ERR_STATE no child. */
firc_err_t firc_tunrun_restart(firc_tunrun_t *t, const char *id);

/* See firc_tunsup_start_seq, firc_tunsup_started and firc_tunsup_updated. */
uint64_t firc_tunrun_start_seq(const firc_tunrun_t *t);
uint64_t firc_tunrun_started(const firc_tunrun_t *t, const char *id);
uint64_t firc_tunrun_updated(const firc_tunrun_t *t, const char *id);

/* The node rows last built for tunnel id, links removed; NULL when none were built. */
const firc_tun_nodes_t *firc_tunrun_nodes(const firc_tunrun_t *t, const char *id);

/* True while tunvless reports the node with this key of tunnel id down; since_ms is monotonic. */
bool firc_tunrun_node_down(const firc_tunrun_t *t, const char *id, const char *key, int64_t *since_ms);

/* Forgets that the node with this key of tunnel id was reported down: it answered a probe. */
void firc_tunrun_node_answered(firc_tunrun_t *t, const char *id, const char *key);

/* Sends tunnel id's ready child a wake with these node links, nothing when n is 0. */
void firc_tunrun_wake(firc_tunrun_t *t, const char *id, const char *const *links, size_t n);

/* True while the node with this key of tunnel id is among tunvless's active ones. */
bool firc_tunrun_node_active(const firc_tunrun_t *t, const char *id, const char *key);

/* True while the node with this key of tunnel id is active and tunvless reports IPv6 through it. */
bool firc_tunrun_node_v6(const firc_tunrun_t *t, const char *id, const char *key);

/* False when id is not configured; mark is its uplink mark (0 for auto), held while a child runs with it. */
bool firc_tunrun_mark_of(const firc_tunrun_t *t, const char *id, uint32_t *mark, bool *held);

/* Rewrites the default route of every tunnel leaving through ifname, of every tunnel with an uplink when NULL. */
void firc_tunrun_refresh_uplinks(firc_tunrun_t *t, const char *ifname);

/* A network event on iface (NULL: cause unknown) restarts, once events settle, every starved tunnel (see
   firc_tunsup_net_changed); nothing for one of firc's own tunnel devices, nothing once stopped. */
void firc_tunrun_net_event(firc_tunrun_t *t, const char *iface);

/* Stops every child (SIGKILL after grace_ms), then removes every uplink. */
void firc_tunrun_stop(firc_tunrun_t *t, int grace_ms);

void firc_tunrun_free(firc_tunrun_t *t);

typedef struct firc_tunrun_ktcp {
    void (*tunnels)(const uint16_t ports[FIRC_KTCP_DEVICES], const char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP],
                    void *ud);
    const firc_ktcp_image_t *(*image)(void *ud);
    bool (*real_of)(uint8_t dev, const uint8_t fake[4], firc_ip_t *real, void *ud);
    void *ud;
    void (*closed)(uint8_t dev, void *ud);
    void (*ready)(uint8_t dev, void *ud);
} firc_tunrun_ktcp_t;

/* Takes a copy of k and calls its tunnels() with the ports and the configured devices, by N, at once and on every
   change; NULL detaches. */
void firc_tunrun_set_ktcp(firc_tunrun_t *t, const firc_tunrun_ktcp_t *k);

/* Sends each ready tunvless with a port the lines of its device that changed from prev to next (both sorted). */
void firc_tunrun_map_pushed(firc_tunrun_t *t, const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next);

/* Passes base/len as --tcp-pool to kernel-TCP tunnels from their next start; len 0 passes none. */
void firc_tunrun_set_tcp_pool(firc_tunrun_t *t, const uint8_t base[4], uint8_t len);

/* Sends the v4 prefixes of p as close to tunvless<dev> when it is ready with a port; dropped otherwise. Returns the
   commands sent, each acked by one closed() call; 0 when dropped. */
int firc_tunrun_close(firc_tunrun_t *t, uint8_t dev, const firc_nf_prefix_t *p, size_t n);

/* false: the kernel cannot redirect (no REDIRECT or addrtype), so states report tcpPort 0. True by default. */
void firc_tunrun_set_ktcp_usable(firc_tunrun_t *t, bool usable);

/* Redirected bytes tunvless reported for device since fircd started, summed across child restarts. */
bool firc_tunrun_ktcp_bytes(const firc_tunrun_t *t, const char *device, uint64_t *rx, uint64_t *tx);
void firc_tunrun_ktcp_bytes_add_for_test(firc_tunrun_t *t, const char *device, uint32_t rx, uint32_t tx); /* test seam */

void firc_tunrun_set_list_max_for_test(firc_tunrun_t *t, size_t bytes); /* test seam: tunvless's stdin limit */
/* Test seam: one pass of the main-default guard now, on the clock firc_tunrun_set_watch_for_test sets. */
void firc_tunrun_guard_for_test(firc_tunrun_t *t);
/* Test seam: the clock and the period of the check for a tunnel with no working node; returns its timer, 0 none. */
int firc_tunrun_set_watch_for_test(firc_tunrun_t *t, int64_t (*now_ms)(void), int period_ms);

/* The journal line for a node event, without the device (the journal keeps it as the source); false when there is none. */
bool firc_tun_journal_line(const firc_tev_t *ev, char *buf, size_t cap, firc_log_level_t *level);

#endif
