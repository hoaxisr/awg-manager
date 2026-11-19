#ifndef FIRC_UPDATE_H
#define FIRC_UPDATE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <curl/curl.h>

#include "firc/err.h"
#include "firc/hash.h"
#include "firc/loop.h"
#include "firc/release.h"
#include "firc/sub_fetch.h"
#include "firc/update_handoff.h"

typedef enum firc_update_state {
    FIRC_UPDATE_IDLE,
    FIRC_UPDATE_CHECKING,
    FIRC_UPDATE_DOWNLOADING,
    FIRC_UPDATE_VERIFYING,
    FIRC_UPDATE_INSTALLING,
    FIRC_UPDATE_FAILED
} firc_update_state_t;

/* "idle", "checking", "downloading", "verifying", "installing", "failed" */
const char *firc_update_state_name(firc_update_state_t s);

/* The rate limit's text: until reset_at as UTC, or "for now" when reset_at is 0. */
void firc_update_limited_text(int64_t reset_at, char *buf, size_t cap);

typedef struct firc_update_outcome {
    firc_err_t err;
    long http_status;
    int64_t reset_at;
    char message[CURL_ERROR_SIZE + 32];
} firc_update_outcome_t;

typedef struct firc_update_deps {
    firc_loop_t *loop;
    const char *dir, *api_url, *download_prefix, *version, *target;
    const char *sh, *opkg;
    uint32_t revision;
    firc_sub_fetch_url_fn url_ok;
    void (*checked)(void *ud, const firc_update_outcome_t *o);
    void *ud;
    uint64_t (*free_bytes)(const char *dir);
    int64_t handoff_grace_ms;
    void (*verified)(void *ud);
} firc_update_deps_t;

typedef struct firc_update_view {
    const char *version, *target;
    uint32_t revision;
    firc_update_state_t state;
    const char *cannot_install;
    bool attempted;
    int64_t checked_at;
    int64_t age_s;
    const firc_release_t *latest;
    bool newer;
    const char *check_error;
    uint64_t bytes, total;
    const char *error;
    bool has_result;
    firc_update_result_t result;
    char log[FIRC_UPDATE_LOG_MAX];
} firc_update_view_t;

typedef struct firc_update firc_update_t;

/* Loop thread, like every call below; url_ok NULL means the API and download URL policies, free_bytes NULL means
 * statvfs, handoff_grace_ms 0 means 10 s; verified (tests only) runs on the job thread once the package is saved. */
firc_update_t *firc_update_new(const firc_update_deps_t *d);

/* Starts the thread; after firc_loop_block_signals. */
firc_err_t firc_update_start(firc_update_t *u);

/* Cancels a transfer in flight, joins the thread, drops results still queued to the loop. NULL-safe. */
void firc_update_free(firc_update_t *u);

/* Queues a check, folded into one queued or running; FIRC_ERR_STATE while an install is under way or settled.
 * deps.checked runs on the loop thread once the check ends: err FIRC_OK, FIRC_ERR_AGAIN rate limited,
 * FIRC_ERR_UPSTREAM anything else. */
firc_err_t firc_update_check(firc_update_t *u);

/* Checks when enabled and no successful check since start, or the last one is >= 23.5 h old; now is
 * CLOCK_MONOTONIC in ms. */
void firc_update_auto_check(firc_update_t *u, bool enabled, int64_t now_mono_ms);

/* Pointers stay valid until the next call into u. The state is the settled one. */
void firc_update_view(firc_update_t *u, firc_update_view_t *out);

typedef enum firc_update_refusal {
    FIRC_UPDATE_OK,
    FIRC_UPDATE_CANNOT,
    FIRC_UPDATE_BUSY,
    FIRC_UPDATE_CHECK_RUNNING,
    FIRC_UPDATE_STALE_CHECK,
    FIRC_UPDATE_NOT_NEWER,
    FIRC_UPDATE_NOT_INSTALLABLE,
    FIRC_UPDATE_NO_SPACE,
    FIRC_UPDATE_IO
} firc_update_refusal_t;

typedef struct firc_update_install_ans {
    const char *reason;
    uint64_t free_bytes, need_bytes;
    char tag[64];
    char asset[160];
    uint64_t size;
} firc_update_install_ans_t;

/* 3 * size + 8 MiB, saturating at UINT64_MAX. */
uint64_t firc_update_space_need(uint64_t size);

/* Checks in order and queues the download of the checked release's asset; on FIRC_UPDATE_OK ans names it, on
 * CANNOT and NOT_INSTALLABLE ans->reason says why, on NO_SPACE ans carries both numbers. */
firc_update_refusal_t firc_update_install(firc_update_t *u, const char *tag, firc_update_install_ans_t *ans);

typedef enum firc_update_settled {
    FIRC_UPDATE_SETTLED_IDLE,
    FIRC_UPDATE_SETTLED_INSTALLING,
    FIRC_UPDATE_SETTLED_FAILED
} firc_update_settled_t;

/* In-memory installing wins while the marker is NO_PID and the hand-off is younger than grace_ms; then ALIVE is
 * INSTALLING, NONE with a result at least as new as epoch (or epoch 0) is IDLE, anything else FAILED. */
firc_update_settled_t firc_update_settle(bool mem_installing, int64_t since_handoff_ms, int64_t grace_ms,
                                         firc_update_marker_t marker, bool has_result, int64_t result_finished_at,
                                         int64_t epoch);

/* The settled state is INSTALLING. */
bool firc_update_installing(firc_update_t *u);

/* An install job is queued or running, or the settled state is INSTALLING: what install refuses as BUSY. */
bool firc_update_busy(firc_update_t *u);

/* Any thread, NULL-safe: stops a transfer in flight and every hand-off after it; the start of a stop. */
void firc_update_cancel(firc_update_t *u);

typedef struct firc_update_file_sink {
    int fd;
    uint64_t want, got;
    firc_sha256_ctx_t sha;
    int err;
    _Atomic uint64_t *progress;
} firc_update_file_sink_t;

/* A firc_sub_fetch_sink_fn: writes and hashes; false on a write error (err set) or past want. */
bool firc_update_file_sink_write(void *ud, const uint8_t *p, size_t n);

#endif
