#ifndef FIRC_IFCHECK_H
#define FIRC_IFCHECK_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/ifchoose.h"
#include "firc/loop.h"
#include "firc/models.h"

#define FIRC_IFCHECK_BATCH 16

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

/* The check URLs, timeout and common upstream (brackets stripped) out of the app settings. */
void firc_ifcheck_settings_from(const firc_app_config_t *a, firc_ifcheck_settings_t *out);

/* One GET of url through mark (0: none), sent to addr when set; true only on a 204. *status is -1 when the socket
 * refused the mark. A raised stop ends it early. */
bool firc_ifcheck_once(const char *url, const char *addr, uint32_t mark, uint32_t timeout_s, _Atomic bool *stop,
                       long *status, char *error, size_t cap);

/* The A record of url's host from server:port, or a literal host as it is; a raised stop ends the wait. */
firc_err_t firc_ifcheck_resolve(const char *server, uint16_t port, const char *url, uint32_t timeout_s, char *addr,
                                size_t cap, _Atomic bool *stop);

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

#endif
