#ifndef FIRC_IFCHECK_H
#define FIRC_IFCHECK_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/id.h"
#include "firc/ifchoose.h"
#include "firc/loop.h"
#include "firc/models.h"

#define FIRC_IFCHECK_BATCH 16
#define FIRC_IFCHECK_GROUP_BATCH 64
#define FIRC_IFCHECK_KEENDNS_MAX_TARGETS 8

typedef struct firc_ifcheck_job {
    char iface[16];
    uint32_t mark;
    uint32_t gen;
} firc_ifcheck_job_t;

typedef struct firc_ifcheck_result {
    char iface[16];
    uint32_t gen;
    bool passed;
    bool control_failed;
    bool control_ran;
    long status;
    char error[FIRC_IF_ERROR_MAX];
} firc_ifcheck_result_t;

typedef struct firc_ifcheck_settings {
    char url[513];
    char wan_url[513];
    uint32_t timeout_s;
    char upstream[64];
    uint16_t upstream_port;
} firc_ifcheck_settings_t;

typedef struct firc_ifcheck firc_ifcheck_t;

typedef void (*firc_ifcheck_done_fn)(void *ud, const firc_ifcheck_result_t *r, size_t n);

typedef struct firc_ifcheck_target {
    char host[128];
    uint16_t port;
    bool is_ip;
} firc_ifcheck_target_t;

typedef struct firc_ifcheck_group_cand {
    char iface[16];
    uint32_t mark;
    char dns_server[64];
    uint16_t dns_port;
    uint32_t dns_mark;
    bool skip;
    char skip_error[FIRC_IF_ERROR_MAX];
} firc_ifcheck_group_cand_t;

typedef struct firc_ifcheck_group_job {
    firc_id_t group_id;
    uint32_t gen;
    firc_group_check_mode_t mode;
    char url[513];
    uint32_t timeout_ms;
    firc_ifcheck_group_cand_t cands[FIRC_GROUP_IFACES_MAX];
    size_t n_cands;
} firc_ifcheck_group_job_t;

typedef struct firc_ifcheck_group_cand_result {
    char iface[16];
    bool passed;
    bool guarded;
    long status;
    char error[FIRC_IF_ERROR_MAX];
} firc_ifcheck_group_cand_result_t;

typedef struct firc_ifcheck_group_result {
    firc_id_t group_id;
    uint32_t gen;
    size_t n_cands;
    firc_ifcheck_group_cand_result_t cands[FIRC_GROUP_IFACES_MAX];
} firc_ifcheck_group_result_t;

typedef void (*firc_ifcheck_group_done_fn)(void *ud, const firc_ifcheck_group_result_t *r);
typedef firc_err_t (*firc_ifcheck_keendns_fn)(firc_ifcheck_target_t *out, size_t cap, size_t *out_n, void *ud);

/* The check URLs, timeout and common upstream (brackets stripped) out of the app settings. */
void firc_ifcheck_settings_from(const firc_app_config_t *a, firc_ifcheck_settings_t *out);

/* One GET of url through mark (0: none), sent to addr when set; true only on a 204. *status is -1 when the socket
 * refused the mark. A raised stop ends it early. */
bool firc_ifcheck_once(const char *url, const char *addr, uint32_t mark, uint32_t timeout_s, _Atomic bool *stop,
                       long *status, char *error, size_t cap);

/* Millisecond-budget variant; accept_http_2xx_4xx treats HTTP 200..499 as reachable, false requires HTTP 204. */
bool firc_ifcheck_once_ex(const char *url, const char *addr, uint32_t mark, uint32_t timeout_ms,
                          bool accept_http_2xx_4xx, _Atomic bool *stop, long *status, char *error, size_t cap);

/* The A record of url's host from server:port, or a literal host as it is; a raised stop ends the wait. */
firc_err_t firc_ifcheck_resolve(const char *server, uint16_t port, const char *url, uint32_t timeout_s, char *addr,
                                size_t cap, _Atomic bool *stop);

/* Variant of firc_ifcheck_resolve that accepts bare hostnames, millisecond timeout, and optional SO_MARK. */
firc_err_t firc_ifcheck_resolve_marked(const char *server, uint16_t port, uint32_t mark, const char *url,
                                       uint32_t timeout_ms, char *addr, size_t cap, _Atomic bool *stop);

/* Non-blocking TCP connect probe to addr:port with optional SO_MARK; *status is -1 if SO_MARK was refused. */
bool firc_ifcheck_tcp_probe(const char *addr, uint16_t port, uint32_t mark, uint32_t timeout_ms, _Atomic bool *stop,
                            long *status, char *error, size_t cap);

/* Extracts active KeenDNS relay endpoints (target-remote IPs then target domains) from /rci/show/ndns JSON. */
size_t firc_ifcheck_parse_keendns_targets(const char *json, firc_ifcheck_target_t *out, size_t cap);

/* Discovers KeenDNS relay targets via test hook or RCI /rci/show/ndns, falling back to static cloud brokers. */
void firc_ifcheck_keendns_targets(firc_ifcheck_target_t *out, size_t cap, size_t *out_n, _Atomic bool *stop);

/* Tests only: replaces KeenDNS target lookup. NULL restores RCI + fallback. */
void firc_ifcheck_set_keendns_fn_for_test(firc_ifcheck_keendns_fn fn, void *ud);

/* One per-group service round: probes all candidates in parallel and guards failure unless at least one passes. */
void firc_ifcheck_group_round(const firc_ifcheck_settings_t *s, const firc_ifcheck_group_job_t *job, _Atomic bool *stop,
                              firc_ifcheck_group_result_t *out);

/* One round: each host resolved once, every job at once (a thread each, at most FIRC_IFCHECK_BATCH), then one
 * unmarked control on the WAN URL if a job failed; wan[i] true checks job i against the WAN URL, NULL means none is.
 * A refused mark guards its job without a control; control_ran marks the failures put to it. */
void firc_ifcheck_round(const firc_ifcheck_settings_t *s, const firc_ifcheck_job_t *jobs, size_t n, const bool *wan,
                        _Atomic bool *stop, firc_ifcheck_result_t *out);

/* Fills wan[i] for each job; an error leaves every job on the check URL. */
typedef firc_err_t (*firc_ifcheck_wan_fn)(const firc_ifcheck_job_t *jobs, size_t n, bool *wan, void *ud);

/* Tests only: replaces the RCI lookup. NULL restores it. */
void firc_ifcheck_set_wan_fn_for_test(firc_ifcheck_wan_fn fn, void *ud);

/* A check thread that posts each round's results to done on the loop thread; call on the loop thread (or before it
 * runs), which is then the only thread that may free it. NULL on no memory. */
firc_ifcheck_t *firc_ifcheck_new(firc_loop_t *loop, firc_ifcheck_done_fn done, void *ud);

/* Starts the thread; STATE when it already runs. */
firc_err_t firc_ifcheck_start(firc_ifcheck_t *c);

/* Loop thread only: stops and joins the thread; no done runs once it returns. A batch still queued on the loop is
 * dropped there. */
void firc_ifcheck_free(firc_ifcheck_t *c);

/* Settings for the rounds that start from now on. */
void firc_ifcheck_configure(firc_ifcheck_t *c, const firc_ifcheck_settings_t *s);

/* Adds a job to the next round, its result carrying gen back; LIMIT when FIRC_IFCHECK_BATCH are already waiting. */
firc_err_t firc_ifcheck_queue(firc_ifcheck_t *c, const char *iface, uint32_t mark, uint32_t gen);

/* Registers the loop-thread callback for group check rounds (call before firc_ifcheck_start). */
void firc_ifcheck_set_group_done(firc_ifcheck_t *c, firc_ifcheck_group_done_fn fn, void *ud);

/* Enqueues or updates a group check job (folding pending jobs for the same group_id); LIMIT when queue full. */
firc_err_t firc_ifcheck_queue_group(firc_ifcheck_t *c, const firc_ifcheck_group_job_t *job);

#endif
