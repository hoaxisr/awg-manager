#ifndef FIRC_APP_H
#define FIRC_APP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/autolistcand.h"
#include "firc/autolistcheck.h"
#include "firc/autolistprobes.h"
#include "firc/cancel.h"
#include "firc/fakeip.h"
#include "firc/dnspipeline.h"
#include "firc/dnsproxy.h"
#include "firc/loop.h"
#include "firc/nfcommit.h"
#include "firc/err.h"
#include "firc/id.h"
#include "firc/ifchoose.h"
#include "firc/iptables.h"
#include "firc/models.h"
#include "firc/sub_worker.h"
#include "firc/tunnels.h"
#include "firc/port_remap.h"
#include "firc/ptr.h"
#include "firc/conntrack.h"
#include "firc/rtnl.h"
#include "firc/setnl.h"
#include "firc/ruleset.h"
#include "firc/settings.h"
#include "firc/stale_marks.h"
#include "firc/yamlio.h"
#include "firc/nflogsock.h"
#include "firc/resolveroute.h"
#include "firc/keenetic_resolvers.h"
#include "firc/keenetic_rci.h"
#include "firc/ktcp.h"
#include "firc/xtables.h"

typedef struct firc_app firc_app_t;

/* all pointers borrowed, must outlive the app; pipeline is republished after every mutation here, or by the caller
 * if it edits rules in place outside this module's mutators */
typedef struct firc_app_deps {
    firc_config_t *cfg;
    firc_ipt_t *ipt4;
    firc_ipt_t *ipt6;
    firc_rtnl_t *rtnl;
    firc_ct_t *ct; /* optional */
    firc_setnl_t *sets; /* nullable; without it a group with bare prefixes is left out */
    firc_dns_pipeline_t *pipeline;
    firc_fakeip_t *pool; /* nullable; without it the group logs its table unprotected */
    firc_loop_t *loop; /* nullable; without it a completed pass reports synchronously */
    firc_dnsproxy_t *proxy;
    firc_resolve_router_t *router; /* nullable; without it no resolve routes publish */
    firc_kn_resolvers_t *resolvers; /* nullable; without it only resolve.server groups get a resolver */
    uint32_t start_idx;
    firc_nflog_t *(*nflog_open)(uint16_t group, uint16_t copy_range); /* NULL takes firc_nflog_open */
    unsigned (*iface_index)(const char *name); /* NULL takes if_nametoindex */
    int64_t capture_seconds; /* 0 takes FIRC_CAPTURE_SECONDS */
    uint64_t (*mono_ms)(void); /* NULL takes CLOCK_MONOTONIC */
    int64_t (*wall_s)(void); /* NULL takes time() */
    const char *config_path; /* nullable; NULL means write nothing */
    const char *config_version;
} firc_app_deps_t;

/* rebuilds/republishes the DNS-matching snapshot (no-op if pipeline NULL); an in-place rule edit must call this */
firc_err_t firc_app_republish_dns_snapshot(firc_app_t *app);

/* rebuilds every group's resolve route and publishes it to the router; loop thread */
void firc_app_republish_resolve_routes(firc_app_t *app);

const firc_fakeip_t *firc_app_pool(const firc_app_t *app);

typedef struct firc_app_resolver_info {
    firc_resolve_source_t source;
    size_t n_servers;
    firc_resolver_addr_t servers[FIRC_RESOLVE_MAX_SERVERS];
    firc_dnsproxy_health_t health;
    int64_t wall_s;
} firc_app_resolver_info_t;
void firc_app_group_resolver(const firc_app_t *app, const firc_group_t *g, firc_app_resolver_info_t *out);

/* loop thread only; takes the netfilter lock so a publish cannot land mid-pass; call after a pool change */
firc_err_t firc_app_refresh_pool_snapshot(firc_app_t *app);

/* publishes a fresh pool snapshot and asks for a pass that does not abort one in flight; loop thread */
void firc_app_pool_changed(firc_app_t *app);

/* a pass that wrote snapshot generation gen completed: tells the pool, releases every held answer now in the kernel */
void firc_app_pass_committed(firc_app_t *app, uint64_t gen);

/* whether every pair a held answer depends on is in the kernel; public for tests */
bool firc_app_answer_ready(firc_app_t *app, const firc_dns_msg_t *msg);

/* the snapshot a group's rules build from: the running pass's, or the current one with none running; public for tests */
const firc_fakeip_snapshot_t *firc_app_rules_snapshot(firc_app_t *app);

/* takes the netfilter lock, yielding the committer's pass first; every iptables-driving path must hold it; recursive */
void firc_app_nf_enter(firc_app_t *app);
void firc_app_nf_leave(firc_app_t *app);

firc_app_t *firc_app_create(const firc_app_deps_t *deps);
/* disables and frees every group's ruleset; does not touch cfg */
void firc_app_destroy(firc_app_t *app);

/* marks the app running; once true firc_app_add_group enables at once; firc_app_start_groups calls this at startup */
void firc_app_set_running(firc_app_t *app, bool running);
bool firc_app_is_running(const firc_app_t *app);

/* startup: enables every group (chains left to the committer), marks the app running, publishes the view and routes;
 * a failed enable is logged and left out; only a failed view publish returns an error */
firc_err_t firc_app_start_groups(firc_app_t *app, const firc_tunnels_t *ts);

size_t firc_app_user_group_count(const firc_app_t *app);
firc_ruleset_t *firc_app_user_group_at(const firc_app_t *app, size_t idx);
firc_ruleset_t *firc_app_find_group_by_id(const firc_app_t *app, firc_id_t id); /* linear search; NULL if not found */

typedef bool (*firc_app_field_kept_fn)(void *ud, const firc_group_t *g, const char *id, uint32_t field);

typedef struct {
    firc_stale_group_t *v;
    size_t n;
    char (*ids)[FIRC_ID_STR_LEN];
} firc_app_stale_groups_t;

/* Every group's id, field, chunk exactness and subnets for a stale-mark sweep; kept decides field_kept,
 * only_field (0: all) limits the subnets to the groups holding it. False only on allocation failure. */
bool firc_app_stale_groups(const firc_app_t *app, firc_app_field_kept_fn kept, void *ud, uint32_t only_field,
                           firc_app_stale_groups_t *out);
void firc_app_stale_groups_free(firc_app_stale_groups_t *t);

/* takes ownership of group either way; FIRC_ERR_EXIST duplicate id, FIRC_ERR_INVAL dup rule id, or enable's error */
firc_err_t firc_app_add_group(firc_app_t *app, firc_group_t *group);

#define FIRC_APP_WHY_MAX 256 /* bytes for firc_app_add_group_why's message */
/* like firc_app_add_group; on a failed enable, why gets group "<name>": <step>: <error> */
firc_err_t firc_app_add_group_why(firc_app_t *app, firc_group_t *group, char *why, size_t why_len);

/* whether the group is routed right now; *reason is disabled/not-enabled/dns-down/no-interface/tunnel-down/not-written
 * in that order, or NULL when live; before firc_app_set_running every enabled group reads not-enabled */
bool firc_app_group_live(const firc_app_t *app, firc_id_t id, const char **reason);

/* the DNS proxy is not listening: no group is live; going up arms the port remap and asks for a full pass */
void firc_app_set_dns_down(firc_app_t *app, bool down);
bool firc_app_dns_down_for_test(const firc_app_t *app);

/* down(ud, iface): iface is a tunnel's device and the tunnel carries no traffic; read on the loop thread; NULL: none */
typedef bool (*firc_app_tunnel_down_fn)(void *ud, const char *iface);
void firc_app_set_tunnel_down(firc_app_t *app, firc_app_tunnel_down_fn down, void *ud);

typedef struct firc_app_ifaces {
    firc_ifchoice_t (*pick)(void *ud, const firc_group_t *g);
    bool (*view)(void *ud, const char *name, firc_if_view_t *out);
    firc_err_t (*move_up)(void *ud, firc_id_t id);
    void (*settings)(void *ud, const firc_app_config_t *run);
    void *ud;
} firc_app_ifaces_t;
/* The interface-list watcher's hooks, copied; NULL detaches. pick is asked at every enable, settings after every
 * live apply. */
void firc_app_set_ifaces(firc_app_t *app, const firc_app_ifaces_t *h);
/* on(ud, true) starts the DNS stats afresh, on(ud, false) frees them; a live apply calls it when disableStats differs
 * between saved and running, and an error leaves the running value as it was. Setting fn switches once as the running
 * copy says; its error marks the running copy off and is returned. NULL: nothing to switch. */
typedef firc_err_t (*firc_app_dnsstats_fn)(void *ud, bool on);
firc_err_t firc_app_set_dnsstats(firc_app_t *app, firc_app_dnsstats_fn fn, void *ud);
/* The watcher's view of one interface; false when nothing watches it. */
bool firc_app_iface_view(const firc_app_t *app, const char *name, firc_if_view_t *out);
/* Moves a listed group back to its first entry: NOENT unknown group, INVAL one entry, NOSYS nothing checks it,
 * AGAIN not routed, STATE first entry not alive; takes nf_mu through the move, so never call it holding nf_mu. */
firc_err_t firc_app_move_up(firc_app_t *app, firc_id_t id);
/* Points a routed group's table at entry `to` of its list, or at its blackhole when blocked; flushes its flows when
 * the exit changed and republishes its resolver. NOENT when not routed, INVAL past the list. Loop thread. */
firc_err_t firc_app_move_group(firc_app_t *app, firc_id_t id, size_t to, bool blocked);

/* retries every group whose enable failed and is due, spaced 1s to 60s; true if one came up; no-op if not running */
bool firc_app_retry_groups(firc_app_t *app, const char *iface);
bool firc_app_retry_timer_for_test(const firc_app_t *app, uint64_t *delay_ms); /* test seam */
bool firc_app_set_retry_armed_for_test(const firc_app_t *app); /* test seam */
#define FIRC_APP_RETRY_MIN_MS 1000u
#define FIRC_APP_RETRY_MAX_MS 60000u

/* the committer's health: first-pass-pending / failing-since; both false without a committer; loop thread */
void firc_app_netfilter_health(const firc_app_t *app, firc_nfcommit_health_t *out);

/* re-syncs rs's chain after its rules changed, asking for a pass only if its subnet prefixes changed */
firc_err_t firc_app_sync_group(firc_app_t *app, firc_ruleset_t *rs);

/* Gives rs the hand rules in next (built with firc_group_add_rule, may share pointers with the current
 * ones) and syncs it; a failed sync puts the old rules back and syncs again, saving nothing. Owns both
 * arrays from the call on. Saves groups.yaml beside save_path on success; *unsaved: only the save failed. */
firc_err_t firc_app_set_hand_rules(firc_app_t *app, firc_ruleset_t *rs, firc_group_t *next, const char *save_path,
                                   const char *version, bool *unsaved);

/* disables, frees and removes every group */
void firc_app_clear_groups(firc_app_t *app);

/* clears and re-adds groups under one lock; takes ownership of the array and its elements either way. A group that
 * fails to add (duplicate id) stops the batch, keeping the ones before it; a list sharing a live id+url keeps its
 * rules/hash/sync in flight; ends by requesting every list with no rules yet. */
firc_err_t firc_app_replace_groups(firc_app_t *app, firc_group_t **groups, size_t n);

/* PUT /groups/{id}: moves built's fields into the live group in place, taking it down and up around the move;
 * takes ownership of built either way. FIRC_ERR_NOENT if id is unknown, FIRC_OK otherwise. */
firc_err_t firc_app_update_group(firc_app_t *app, firc_id_t id, firc_group_t *built);

typedef struct {
    firc_id_t rule;
    bool has_enable, enable;
    const char *type; /* NULL: unchanged */
} firc_list_rule_edit_t;

/* edits the list of group id in place; two passes: first validates every edit, second applies. FIRC_ERR_NOENT unknown
 * id/list/rule, FIRC_ERR_INVAL bad type (msg set), FIRC_ERR_NOMEM otherwise. Caller saves the file. */
firc_err_t firc_app_patch_list_rules(firc_app_t *app, firc_id_t id, const firc_list_rule_edit_t *edits,
                                     size_t n, char *msg, size_t msg_len);

uint64_t firc_app_nf_enters_for_test(const firc_app_t *app); /* test seam */
uint64_t firc_app_nf_passes_for_test(const firc_app_t *app); /* test seam */
uint64_t firc_app_nf_last_completed_pass_for_test(const firc_app_t *app); /* test seam */
uint64_t firc_app_nf_change_for_test(const firc_app_t *app); /* test seam, under firc_app_nf_enter */
void firc_app_pay_flushes_for_test(firc_app_t *app, uint64_t ticket); /* test seam */

/* removes the group from the kernel (ip rules/routes at once, its chains with the next pass) then the config and DNS view */
void firc_app_remove_group_by_index(firc_app_t *app, size_t idx);
bool firc_app_remove_group_by_id(firc_app_t *app, firc_id_t id);

/* starts the worker thread; needs deps->loop since every result comes back via it; FIRC_ERR_STATE if already started */
firc_err_t firc_app_start_list_worker(firc_app_t *app);

/* raises the cancel token, aborts the transfer in flight, joins; idempotent */
void firc_app_stop_list_worker(firc_app_t *app);

/* test seam: posted/result/dropped sync-event counts, process-wide, monotonic; any out-pointer may be NULL */
void firc_app_sync_event_counts_for_test(size_t *out_posted, size_t *out_results,
                                         size_t *out_dropped);

/* builds a job and enqueues it (list goes QUEUED); answer arrives as events. FIRC_ERR_NOENT unknown id/no list,
 * FIRC_ERR_INVAL no url, FIRC_ERR_STATE no worker, FIRC_ERR_LIMIT queue full (jobs fold by id) */
firc_err_t firc_app_request_sync(firc_app_t *app, firc_id_t id);

/* stores url on the list and, if changed, writes the groups file under the lock. FIRC_ERR_NOENT unknown id/no list,
 * FIRC_ERR_INVAL unsupported url; a failed file write is logged, not returned. */
firc_err_t firc_app_set_list_url(firc_app_t *app, firc_id_t id, const char *url);

/* requests every enabled list with a url but no body yet; not a re-fetch of everything the ticker already owns */
void firc_app_request_sync_missing(firc_app_t *app);

/* requests every list firc_sub_is_due says is due; a list already in flight is asked again, not skipped: a lost
 * result would otherwise never be retried; the queue folds the duplicate by id */
void firc_app_request_sync_due(firc_app_t *app, int64_t now_unix);

/* consumes one worker event (frees it and any rules it carries); STARTED/PROGRESS move state, RESULT applies it */
void firc_app_on_sync_event(firc_app_t *app, firc_sub_event_t *ev, int64_t now_unix);

/* the apply half of a sync under the lock: inherit ids, apply overrides, compare, and on a real change swap the
 * arena, resync, republish, save. Does not free ev. Stale seq is FIRC_OK, *out_changed false; gone group/list is
 * FIRC_ERR_NOENT; an ERROR result returns the worker's own error. */
firc_err_t firc_app_apply_sync_result(firc_app_t *app, firc_sub_event_t *ev, int64_t now_unix,
                                      bool *out_changed);

/* runs a sync on the calling thread, no worker/queue; a test seam and the CLI path, never called by the daemon.
 * Listener-silent. url_override replaces the list's url for this run only, restored on failure. */
firc_err_t firc_app_sync_list_now(firc_app_t *app, firc_id_t id, int64_t now_unix,
                                  const char *url_override, bool *out_changed);

/* told after every sync event for owner; done means the job finished; list must not be kept past the call */
typedef void (*firc_app_sync_listener_fn)(void *ud, firc_id_t owner, const firc_group_list_t *list,
                                          bool done);
void firc_app_set_sync_listener(firc_app_t *app, firc_app_sync_listener_fn fn, void *ud);

/* running is cfg->app (what the daemon reads at run time); saved is the app's own copy of firc.conf; a live
 * setting is copied to running when applied, a restart setting never is */
const firc_app_config_t *firc_app_saved_settings(const firc_app_t *app);
const firc_app_config_t *firc_app_running_settings(const firc_app_t *app);

/* writes next to conf_path as firc.conf alone (atomic), makes it the saved copy, applies every differing live
 * setting; NULL conf_path writes nothing. FIRC_OK takes and zeroes next; error leaves it the caller's. live_err
 * (may be NULL) reports a live-apply failure that happens after the write already landed. */
firc_err_t firc_app_put_settings(firc_app_t *app, firc_app_config_t *next, const char *conf_path,
                                 const char *version, bool applied[FIRC_SETTINGS_COUNT],
                                 firc_err_t *live_err);

/* the WebUI took port instead of the saved one: running and saved both say port, even when rewriting firc.conf fails */
firc_err_t firc_app_move_web_port(firc_app_t *app, uint16_t port, const char *conf_path,
                                  const char *version);

/* SIGHUP: the re-read app block becomes the saved copy, live settings apply as in a PUT; writes nothing */
firc_err_t firc_app_reload_settings(firc_app_t *app, const firc_app_config_t *from_file,
                                    bool applied[FIRC_SETTINGS_COUNT]);

/* pending[i] true for each restart row whose saved value differs from running; returns the count */
size_t firc_app_pending_restart(const firc_app_t *app, bool pending[FIRC_SETTINGS_COUNT]);

typedef enum firc_iface_role {
    FIRC_IFACE_OTHER,
    FIRC_IFACE_LAN,
    FIRC_IFACE_WAN,
    FIRC_IFACE_TUNNEL,
} firc_iface_role_t;

typedef struct firc_iface_info {
    char id[16];        /* IFNAMSIZ */
    char name[64];      /* NDMS description; empty unless built with FIRC_ENTWARE_KN */
    char ndms_name[64]; /* NDMS interface-name; empty unless built with FIRC_ENTWARE_KN */
    firc_iface_role_t role;
} firc_iface_info_t;

/* enumerates system interfaces (getifaddrs, deduped) but lo, ezcfg0 and the radios ra<N>/rai<N>[.<M>];
 * names and roles from aliases, NULL for none; a device ts names is a tunnel's */
firc_err_t firc_app_list_interfaces(const firc_kn_aliases_t *aliases, const firc_tunnels_t *ts, firc_iface_info_t **out,
                                    size_t *out_n);

/* "tunnel", "wan", "lan" or "other" */
const char *firc_iface_role_name(firc_iface_role_t role);

firc_iface_role_t firc_iface_role_for_test(const char *name, unsigned flags, bool inet,
                                           const firc_tunnels_t *ts); /* test seam */

bool firc_iface_listed_for_test(const char *name); /* test seam: the list's filter */

/* two files sharing one path; _save_groups writes only groups.yaml; _save_config writes both, from the saved copy */
firc_err_t firc_app_save_groups(firc_app_t *app, const char *conf_path,
                                const char *version);
firc_err_t firc_app_save_config(firc_app_t *app, const char *conf_path, const char *version);

/* asks the committer to abort and rebuild; FIRC_ERR_STATE without one */
firc_err_t firc_app_force_commit_iptables(firc_app_t *app);

/* lets the rebuild reach the port-53 DNAT chain owned by main(); NULL disables remap53 */
void firc_app_set_port_remap(firc_app_t *app, firc_port_remap_t *remap);
/* re-reads the app.link addresses when iface is one of them (NULL: always); only a difference takes the lock
 * and asks for a pass; loop thread */
void firc_app_refresh_remap(firc_app_t *app, const char *iface);

/* The ports each tunnel's tunvless reported and the devices tunnels.yaml names, by N (0 or "": none); only a device
   named here is a tunnel's; a change asks for a pass. Loop thread. */
void firc_app_set_tunnels(firc_app_t *app, const uint16_t ports[FIRC_KTCP_DEVICES],
                          const char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP]);

typedef struct {
    void (*pushed)(const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next, void *ud);
    int (*close)(uint8_t dev, const firc_nf_prefix_t *p, size_t n, void *ud);
    void *ud;
} firc_app_ktcp_sink_t;

/* Hands every completed pass's image to sink before its held answers are released, and a withdrawn tunnel group's
   prefixes to close ahead of its flush, never at the stop; close returns the commands a live child took. NULL detaches.
   Loop thread. */
void firc_app_set_ktcp_sink(firc_app_t *app, const firc_app_ktcp_sink_t *sink);

/* the tunvless of dev acked one close command: the oldest flush waiting on that device's acks counts it. Loop thread. */
void firc_app_ktcp_closed(firc_app_t *app, uint8_t dev);

/* the tunvless of dev said ready: a new child, so no ack its predecessor owed will come. Loop thread. */
void firc_app_ktcp_ready(firc_app_t *app, uint8_t dev);

void firc_app_ktcp_due_for_test(firc_app_t *app); /* test seam: what the wait timer does when it fires */

/* The image of the last completed pass, sorted by (dev, fake). Loop thread. */
const firc_ktcp_image_t *firc_app_ktcp_image(const firc_app_t *app);

typedef enum { FIRC_KTCP_OFF_NONE = 0, FIRC_KTCP_OFF_ACL, FIRC_KTCP_OFF_KERNEL, FIRC_KTCP_OFF_REFUSED } firc_ktcp_off_t;

/* The interfaces the last completed pass kept on the TUN path. Loop thread. */
const firc_xt_acl_t *firc_app_ktcp_skipped(const firc_app_t *app);
/* Why the last completed pass staged no redirect for groups that would have had one; NONE otherwise. Loop thread. */
firc_ktcp_off_t firc_app_ktcp_off(const firc_app_t *app);
void firc_app_set_ktcp_skipped_for_test(firc_app_t *app, const firc_xt_acl_t *acl,
                                        firc_ktcp_off_t off); /* test seam: as a report would */

/* real of fake when its group is routed into the device of N dev with a port; false answers "-". Loop thread. */
bool firc_app_ktcp_real_of(firc_app_t *app, uint8_t dev, const uint8_t fake[4], firc_ip_t *real);

/* stages the pool's FORWARD reject barrier on both engines without committing; every pass does this */
firc_err_t firc_app_stage_pool_reject(firc_app_t *app);

#define FIRC_CAPTURE_SECONDS 300 /* the only length a capture ever runs */
#define FIRC_TAP_TOKEN_CHARS 16 /* hex chars, no terminator counted; 8 random bytes */

/* readable by anything that can reach the API, which is why the stop token is deliberately not in here */
typedef struct firc_capture_status {
    bool running;
    int64_t ends_at; /* wall clock at start + length, for display only */
    int64_t seconds_left; /* monotonic countdown, never below zero */
} firc_capture_status_t;

#define FIRC_CAPTURE_LAST_SENTENCES 13
#define FIRC_CAPTURE_LAST_SENTENCE_BYTES 240

/* the last finished capture, kept until the next one starts or the daemon restarts */
typedef struct firc_capture_last {
    int64_t started_at;
    int64_t ended_at;
    const char *ended; /* "reached-end" | "stopped" | "failed" */
    uint64_t leaks;
    size_t n_summary;
    char summary[FIRC_CAPTURE_LAST_SENTENCES][FIRC_CAPTURE_LAST_SENTENCE_BYTES];
} firc_capture_last_t;

/* copies the last finished capture; false when none has finished since the start. Loop thread. */
bool firc_app_capture_last(const firc_app_t *app, firc_capture_last_t *out);

/* starts a FIRC_CAPTURE_SECONDS capture on the LAN interfaces of app.link; FIRC_ERR_EXIST one runs, FIRC_ERR_INVAL
 * bad/empty link, FIRC_ERR_STATE no pool, FIRC_ERR_NOSYS kernel lacks an extension or refuses the rules, FIRC_ERR_SYS NFLOG bind/watch
 * failed, FIRC_ERR_IO no token entropy, FIRC_ERR_AGAIN the extension probe went blind, FIRC_ERR_NOMEM deadline not armed. On success token_out holds the stop token. */
firc_err_t firc_app_capture_start(firc_app_t *app, int64_t now_unix,
                                  char token_out[FIRC_TAP_TOKEN_CHARS + 1]);

/* stops it: the next pass drops the rules; takes the readers down, asks for that pass, logs the summary; not an error when
 * nothing runs. token must match the running capture's (FIRC_ERR_NOENT otherwise); NULL is the daemon's own stop. */
firc_err_t firc_app_capture_stop(firc_app_t *app, const char *token);

void firc_app_capture_status(const firc_app_t *app, firc_capture_status_t *out); /* out is always filled */

/* the resolvers a policy-named selector is answered with, for the capture's judgement; must match the DNS pipeline's */
void firc_app_set_policy_resolver(firc_app_t *app, firc_devsel_policy_fn policy,
                                  firc_devsel_device_fn device, void *ud);

/* the packet path's selector lookups, wired in before the groups start; without them a policy entry marks nothing */
void firc_app_set_device_lookup(firc_app_t *app, firc_devsel_mark_fn mark, firc_devsel_hosts_fn hosts,
                                firc_devsel_policy_hosts_fn policy_hosts, firc_devsel_policy_nets_fn policy_nets,
                                void *ud);

/* Until read(ud) holds (ud as given to set_device_lookup; NULL read: always), a chain naming a policy marks nobody. */
void firc_app_set_policies_read(firc_app_t *app, bool (*read)(void *ud));

/* the firmware's host table changed: every routed group with a selector is re-rendered and compared before the
 * lock is taken, which is taken only if something moved; a narrowed chain flushes the group's flows by mark after
 * the pass. */
void firc_app_devices_changed(firc_app_t *app);

/* stages the capture's chain when one runs; every pass does this */
firc_err_t firc_app_stage_tap(firc_app_t *app);

firc_err_t firc_app_stage_autolist(firc_app_t *app);

firc_err_t firc_app_autolist_apply(firc_app_t *app);

#define FIRC_AUTOLIST_GROUPS_MAX 32

typedef struct firc_autolist_status {
    bool enabled;
    bool running;
    const char *why_off;
    const char *mode;
    const char *group;
    uint64_t failures;
    uint64_t listed;
    uint32_t failures_day;
    uint32_t ambiguous_day;
    uint32_t added_hour;
    const char *wan_warning;
    bool checking;
    int64_t checked_at;
    uint64_t switched_off;
    uint64_t switched_on;
    size_t n_addresses;
    firc_autolist_addr_t addresses[2 * FIRC_AUTOLIST_ADDRS];
    firc_ptr_state_t ptr_state[2 * FIRC_AUTOLIST_ADDRS];
    char ptr[2 * FIRC_AUTOLIST_ADDRS][FIRC_AUTOLIST_NAME_MAX];
    size_t n_groups[2 * FIRC_AUTOLIST_ADDRS];
    firc_id_t groups[2 * FIRC_AUTOLIST_ADDRS][FIRC_AUTOLIST_GROUPS_MAX];
    size_t n_candidates;
    firc_al_cand_t candidates[FIRC_AUTOLIST_CANDIDATES];
    uint32_t probes_used_hour;
    uint32_t probes_queued;
    bool probe_running;
    char probe_host[FIRC_AUTOLIST_NAME_MAX];
    size_t n_results;
    firc_al_probe_seen_t results[FIRC_AUTOLIST_PROBE_CACHE];
} firc_autolist_status_t;

/* Loop thread; the status is large, keep it off the stack. */
void firc_app_autolist_status(const firc_app_t *app, firc_autolist_status_t *out);

/* Queues a user probe: STATE not running, CLOSED check interface in the group, INVAL bad name, LIMIT queue full;
 * otherwise OK with *ask BUSY or FULL (the hour spent). */
firc_err_t firc_app_autolist_probe(firc_app_t *app, const char *host, firc_al_ask_t *ask);

/* Adds the candidate's domain to the target group: STATE when not running, NOENT for no such candidate. */
firc_err_t firc_app_autolist_candidate_add(firc_app_t *app, const char *domain);

/* Hides a candidate for a day: NOENT for no such candidate. */
firc_err_t firc_app_autolist_candidate_dismiss(firc_app_t *app, const char *domain);

/* Hides an address row for a day: STATE when not running, NOENT for no visible row. */
firc_err_t firc_app_autolist_address_dismiss(firc_app_t *app, const firc_ip_t *server, uint8_t proto, uint16_t port);

/* Queues the recheck: STATE when not running, CLOSED when the check interface belongs to the group. */
firc_err_t firc_app_autolist_recheck_now(firc_app_t *app);

void firc_app_set_autolist_probe_for_test(firc_app_t *app, firc_autolist_probe_fn probe);
void firc_app_autolist_tick_for_test(firc_app_t *app); /* test seam: what the probe queue's timer does when it fires */

/* What the DNS pipeline tells the automatic list about an unmatched answer; loop thread. */
void firc_app_autolist_hint_for_test(firc_app_t *app, const firc_ip_t *client, const char *name,
                                     const firc_ip_t *reals, size_t n_reals);

firc_err_t firc_app_rebuild_netfilter(firc_app_t *app, firc_cancel_t *cancel);

/* full writes every table again and puts our jumps back in place (firmware rewrite, first pass); otherwise a table unchanged since firc wrote it is skipped */
firc_err_t firc_app_rebuild_netfilter_kind(firc_app_t *app, firc_cancel_t *cancel, bool full);

/* starts the committer thread and with it the netfilter lock; call once everything the rebuild touches is up */
firc_err_t firc_app_start_netfilter_committer(firc_app_t *app);

/* stops and joins the committer, aborting a write in flight; must run before the rebuild's dependencies are torn down */
void firc_app_stop_netfilter_committer(firc_app_t *app);

firc_nfcommit_t *firc_app_committer_for_test(firc_app_t *app); /* test seam */

#endif /* FIRC_APP_H */
