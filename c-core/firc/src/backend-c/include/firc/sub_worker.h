#ifndef FIRC_SUB_WORKER_H
#define FIRC_SUB_WORKER_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/cancel.h"
#include "firc/err.h"
#include "firc/hash.h"
#include "firc/id.h"
#include "firc/models.h"

/* one waiting slot per group id; jobs fold by id, so syncing one list repeatedly fills one slot */
#define FIRC_SUB_WORKER_CAPACITY 256

/* every field is a copy taken at enqueue time; url is owned by the job */
typedef struct firc_sub_job {
    firc_id_t group_id;
    char *url;
    bool has_hash;
    uint8_t expected_hash[FIRC_SHA256_DIGEST_LEN];
    uint64_t seq;
} firc_sub_job_t;

typedef enum firc_sub_event_kind {
    FIRC_SUB_EV_STARTED,  /* the worker took the job */
    FIRC_SUB_EV_PROGRESS, /* stage + counters */
    FIRC_SUB_EV_RESULT,   /* one of the three results below */
} firc_sub_event_kind_t;

/* firc_sub_stage_t lives in models.h: a list carries the last stage it was told about */

typedef enum firc_sub_result_kind {
    FIRC_SUB_RESULT_ERROR,
    FIRC_SUB_RESULT_UNCHANGED, /* the body hashed to what the job expected */
    FIRC_SUB_RESULT_PARSED,
} firc_sub_result_kind_t;

typedef struct firc_sub_event {
    firc_sub_event_kind_t kind;
    firc_id_t group_id;
    uint64_t seq;

    /* PROGRESS */
    firc_sub_stage_t stage;
    size_t bytes, total; /* FETCH: `total` is 0 while the size is unknown */
    size_t lines;        /* PARSE */

    /* RESULT */
    firc_sub_result_kind_t result;
    firc_err_t err;      /* ERROR: unflattened fetch/parse error (INVAL/PROTO/LIMIT/IO/NOMEM); why gives detail */
    long http_status;    /* ERROR from an answered fetch: the code; 0 if the transfer never got an answer */
    uint8_t hash[FIRC_SHA256_DIGEST_LEN]; /* UNCHANGED and PARSED */
    firc_sub_rules_t rules;               /* PARSED: moved to the receiver */
    size_t dropped;                       /* PARSED: lines, or sing-box objects/values, nothing was made of */
    size_t unconstrained;                 /* PARSED: names taken without their object's network/port */
    const char *why; /* ERROR from the parse: static text, or NULL */
} firc_sub_event_t;

/* called on the WORKER thread; the receiver takes ownership (firc_sub_event_free when done) */
typedef void (*firc_sub_event_fn)(void *ud, firc_sub_event_t *ev);

/* Frees the event and the rules it carries. NULL-safe. */
void firc_sub_event_free(firc_sub_event_t *ev);

/* Runs one job on the calling thread: emits STARTED, PROGRESS, then one RESULT via emit (no queue).
 * cancel may be NULL; once raised, the loop frees further events (this job's result included) instead of emitting. */
void firc_sub_job_run(const firc_sub_job_t *job, firc_cancel_t *cancel, firc_sub_event_fn emit,
                      void *ud);

typedef struct firc_sub_worker firc_sub_worker_t;

/* capacity bounds jobs WAITING (a follow-up counts, the running job doesn't); NULL on OOM or capacity 0. */
firc_sub_worker_t *firc_sub_worker_new(size_t capacity, firc_sub_event_fn emit, void *ud);

firc_err_t firc_sub_worker_start(firc_sub_worker_t *w);

/* Raises the cancel token, joins the thread, frees whatever was still queued. NULL-safe. */
void firc_sub_worker_free(firc_sub_worker_t *w);

/* Takes ownership of job->url even on error; folds by group_id (waiting replaced, running gets a follow-up). */
firc_err_t firc_sub_worker_enqueue(firc_sub_worker_t *w, firc_sub_job_t *job);

/* How many jobs are waiting to start (waiting + follow-up); tests only. */
size_t firc_sub_worker_waiting_for_test(firc_sub_worker_t *w);

#endif /* FIRC_SUB_WORKER_H */
