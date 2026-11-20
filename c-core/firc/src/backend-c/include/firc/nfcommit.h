#ifndef FIRC_NFCOMMIT_H
#define FIRC_NFCOMMIT_H

#include <stddef.h>
#include <stdbool.h>
#include <stdint.h>

#include "firc/cancel.h"
#include "firc/err.h"

typedef struct firc_nfcommit firc_nfcommit_t;

/* One pass on the committer thread; `full` writes every table again and puts our jumps back in place. Return CANCELED once cancel is raised. */
typedef firc_err_t (*firc_nfcommit_rebuild_fn)(void *ud, firc_cancel_t *cancel, bool full);

/* The thread is not started; requests made before start are served by the first pass. */
firc_nfcommit_t *firc_nfcommit_new(firc_nfcommit_rebuild_fn fn, void *ud);

/* Stops the thread if running and frees. NULL-safe. */
void firc_nfcommit_free(firc_nfcommit_t *c);

firc_err_t firc_nfcommit_start(firc_nfcommit_t *c);

/* Aborts the write in flight and joins. Call before anything the callback touches is torn down. */
void firc_nfcommit_stop(firc_nfcommit_t *c);

/* Thread-safe, non-blocking, NULL-safe: aborts the write in flight and asks for a full pass. */
void firc_nfcommit_request(firc_nfcommit_t *c);

/* Thread-safe, non-blocking, NULL-safe: asks for another pass without aborting (aborting here livelocks). */
void firc_nfcommit_request_more(firc_nfcommit_t *c);

/* Thread-safe, non-blocking, NULL-safe: a pass for new fake addresses; settles FIRC_NFCOMMIT_ADDR_DELAY_MS only if the last pass ended FIRC_NFCOMMIT_ADDR_IDLE_MS ago, else long. */
void firc_nfcommit_request_addresses(firc_nfcommit_t *c);

/* Asked on the committer thread between passes: did another writer change a table? */
typedef bool (*firc_nfcommit_watch_fn)(void *ud);

/* Call before start. Every interval_ms with nothing pending, fn gets the rebuild's ud; true asks for a full pass. */
void firc_nfcommit_set_watch(firc_nfcommit_t *c, firc_nfcommit_watch_fn fn, unsigned interval_ms);

#define FIRC_NFCOMMIT_WATCH_MS 1000u
#define FIRC_NFCOMMIT_ADDR_DELAY_MS 25u
#define FIRC_NFCOMMIT_ADDR_IDLE_MS 300u

/* Aborts the pass in flight, which reschedules itself. NULL-safe. */
void firc_nfcommit_interrupt(firc_nfcommit_t *c);

uint64_t firc_nfcommit_passes(const firc_nfcommit_t *c);

/* Ordinal of the last pass that finished FIRC_OK; 0 before any. */
uint64_t firc_nfcommit_last_completed_pass(const firc_nfcommit_t *c);

/* Passes have kept failing (any error) for FIRC_NFCOMMIT_FAILING_AFTER_MS; a completed pass clears it. */
typedef struct {
    bool first_pending;
    bool failing;
    firc_err_t err;
    int64_t since;  /* unix seconds */
} firc_nfcommit_health_t;

#define FIRC_NFCOMMIT_FAILING_AFTER_MS 5000u

/* Thread-safe, NULL-safe. */
void firc_nfcommit_health(const firc_nfcommit_t *c, firc_nfcommit_health_t *out);

void firc_nfcommit_set_failing_after_for_test(firc_nfcommit_t *c, unsigned ms);

/* Tests only; call before start. */
void firc_nfcommit_set_delays_for_test(firc_nfcommit_t *c, unsigned delay_ms, unsigned max_delay_ms);

/* Tests only; call before start. */
void firc_nfcommit_set_addr_delay_for_test(firc_nfcommit_t *c, unsigned ms);

/* Tests only; call before start. */
void firc_nfcommit_set_addr_idle_for_test(firc_nfcommit_t *c, unsigned ms);

size_t firc_nfcommit_requests_for_test(firc_nfcommit_t *c);

#endif /* FIRC_NFCOMMIT_H */
