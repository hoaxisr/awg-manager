#include "firc/sub_worker.h"

#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/sub_fetch.h"
#include "firc/subparse.h"

/* at most one mid-transfer progress event per this many ms; each costs the loop a wakeup and a write */
#define FIRC_SUB_FETCH_PROGRESS_MIN_MS 200u

void firc_sub_event_free(firc_sub_event_t *ev)
{
    if (ev == NULL) {
        return;
    }
    firc_sub_rules_free(&ev->rules);
    free(ev);
}

typedef struct run_ctx {
    const firc_sub_job_t *job;
    firc_cancel_t *cancel;
    firc_sub_event_fn emit;
    void *ud;
    uint64_t last_ms;
} run_ctx_t;

static uint64_t mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000 + (uint64_t)ts.tv_nsec / 1000000;
}

/* check-then-emit, not atomic: a raise landing between the two still hands over one event, by design */
static void post(run_ctx_t *c, firc_sub_event_t *ev)
{
    if (ev == NULL) {
        return;
    }
    if (firc_cancel_raised(c->cancel)) {
        firc_sub_event_free(ev);
        return;
    }
    ev->group_id = c->job->group_id;
    ev->seq = c->job->seq;
    c->emit(c->ud, ev);
}

static bool fetch_progress(void *ud, size_t bytes, size_t total)
{
    run_ctx_t *c = ud;
    if (firc_cancel_raised(c->cancel)) {
        return false; /* aborts the transfer: FIRC_ERR_CANCELED */
    }
    uint64_t now = mono_ms();
    /* both ends are always reported; "0 of 0" is NOT the last callback (a stall would else flood events) */
    bool finished = total > 0 && bytes == total;
    if (!finished && now - c->last_ms < FIRC_SUB_FETCH_PROGRESS_MIN_MS) {
        return true;
    }
    c->last_ms = now;
    firc_sub_event_t *ev = calloc(1, sizeof(*ev));
    if (ev == NULL) {
        return true; /* a progress event nobody got is not a failed fetch */
    }
    ev->kind = FIRC_SUB_EV_PROGRESS;
    ev->stage = FIRC_SUB_STAGE_FETCH;
    ev->bytes = bytes;
    ev->total = total;
    post(c, ev);
    return true;
}

static bool parse_progress(void *ud, size_t lines)
{
    run_ctx_t *c = ud;
    if (firc_cancel_raised(c->cancel)) {
        return false; /* aborts the parse: FIRC_ERR_CANCELED */
    }
    /* no rate limit of its own: the parser already reports once every FIRC_SUB_PARSE_PROGRESS_LINES lines */
    firc_sub_event_t *ev = calloc(1, sizeof(*ev));
    if (ev == NULL) {
        return true;
    }
    ev->kind = FIRC_SUB_EV_PROGRESS;
    ev->stage = FIRC_SUB_STAGE_PARSE;
    ev->lines = lines;
    post(c, ev);
    return true;
}

void firc_sub_job_run(const firc_sub_job_t *job, firc_cancel_t *cancel, firc_sub_event_fn emit,
                      void *ud)
{
    if (job == NULL || emit == NULL) {
        return;
    }
    run_ctx_t c = {.job = job, .cancel = cancel, .emit = emit, .ud = ud, .last_ms = 0};

    /* result allocated before STARTED is sent, so a STARTED is always followed by a RESULT (cancellation aside) */
    firc_sub_event_t *res = calloc(1, sizeof(*res));
    if (res == NULL) {
        return;
    }
    res->kind = FIRC_SUB_EV_RESULT;
    firc_sub_rules_init(&res->rules);

    firc_sub_event_t *started = calloc(1, sizeof(*started));
    if (started != NULL) {
        started->kind = FIRC_SUB_EV_STARTED;
        post(&c, started);
    }

    char *body = NULL;
    size_t len = 0;
    long status = 0;
    firc_err_t ferr = firc_sub_fetch_list_ex(job->url, &body, &len, fetch_progress, &c, &status);
    if (ferr == FIRC_ERR_CANCELED) {
        firc_sub_event_free(res);
        return;
    }
    if (ferr != FIRC_OK) {
        res->result = FIRC_SUB_RESULT_ERROR;
        /* unflattened: INVAL is a url to fix, PROTO is a server answer (http_status says what), LIMIT/IO differ too */
        res->err = ferr;
        res->http_status = status;
        post(&c, res);
        return;
    }

    firc_sha256_ctx_t h;
    firc_sha256_init(&h);
    firc_sha256_update(&h, (const uint8_t *)body, len);
    firc_sha256_final(&h, res->hash);

    /* cheap hash test, off the loop: the same bytes are not parsed again */
    if (job->has_hash && memcmp(job->expected_hash, res->hash, sizeof(res->hash)) == 0) {
        free(body);
        res->result = FIRC_SUB_RESULT_UNCHANGED;
        post(&c, res);
        return;
    }

    firc_sub_parse_stats_t st;
    firc_err_t perr = firc_sub_parse_rules_stats(body, len, &res->rules, &st, parse_progress, &c);
    free(body);
    if (perr == FIRC_ERR_CANCELED) {
        firc_sub_event_free(res);
        return;
    }
    if (perr != FIRC_OK) {
        res->result = FIRC_SUB_RESULT_ERROR;
        res->err = perr; /* NOMEM stays NOMEM: it is not a bad list */
        res->why = st.why; /* set for FIRC_ERR_INVAL: an HTML page, or not a sing-box rule-set */
        post(&c, res);
        return;
    }
    res->dropped = st.dropped;
    res->unconstrained = st.unconstrained;

    res->result = FIRC_SUB_RESULT_PARSED;
    post(&c, res);
}

struct firc_sub_worker {
    firc_sub_event_fn emit;
    void *ud;

    firc_cancel_t *cancel;

    pthread_mutex_t mu;
    pthread_cond_t cv;
    pthread_t thread;

    bool started;
    bool stopping;

    firc_sub_job_t *waiting; /* one slot per id, replaced in place */
    size_t n_waiting;
    size_t cap;

    firc_sub_job_t running;
    bool has_running;

    firc_sub_job_t follow_up; /* for running.group_id only */
    bool has_follow_up;
};

firc_sub_worker_t *firc_sub_worker_new(size_t capacity, firc_sub_event_fn emit, void *ud)
{
    if (capacity == 0 || capacity == SIZE_MAX || emit == NULL) {
        return NULL; /* SIZE_MAX: the follow-up's extra slot would wrap */
    }
    firc_sub_worker_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        return NULL;
    }
    /* capacity+1: the follow-up's slot is real, since the running job's id isn't one of the waiting slots */
    w->waiting = calloc(capacity + 1, sizeof(*w->waiting));
    if (w->waiting == NULL) {
        free(w);
        return NULL;
    }
    w->cancel = firc_cancel_new();
    if (w->cancel == NULL) {
        free(w->waiting);
        free(w);
        return NULL;
    }
    if (pthread_mutex_init(&w->mu, NULL) != 0) {
        firc_cancel_free(w->cancel);
        free(w->waiting);
        free(w);
        return NULL;
    }
    if (pthread_cond_init(&w->cv, NULL) != 0) {
        pthread_mutex_destroy(&w->mu);
        firc_cancel_free(w->cancel);
        free(w->waiting);
        free(w);
        return NULL;
    }
    w->emit = emit;
    w->ud = ud;
    w->cap = capacity;
    return w;
}

size_t firc_sub_worker_waiting_for_test(firc_sub_worker_t *w)
{
    if (w == NULL) {
        return 0;
    }
    pthread_mutex_lock(&w->mu);
    /* the follow-up is a job waiting to start like any other, just held apart because its id is running */
    size_t n = w->n_waiting + (w->has_follow_up ? 1u : 0u);
    pthread_mutex_unlock(&w->mu);
    return n;
}

firc_err_t firc_sub_worker_enqueue(firc_sub_worker_t *w, firc_sub_job_t *job)
{
    if (w == NULL || job == NULL || job->url == NULL) {
        if (job != NULL) {
            free(job->url);
            job->url = NULL;
        }
        return FIRC_ERR_INVAL;
    }

    pthread_mutex_lock(&w->mu);
    if (w->stopping) {
        pthread_mutex_unlock(&w->mu);
        free(job->url);
        job->url = NULL;
        return FIRC_ERR_STATE;
    }

    if (w->has_running && firc_id_equal(w->running.group_id, job->group_id)) {
        /* already being fetched: a follow-up replaces any earlier one rather than queueing behind it */
        if (w->has_follow_up) {
            free(w->follow_up.url);
        }
        w->follow_up = *job;
        w->has_follow_up = true;
    } else {
        size_t slot = w->n_waiting;
        for (size_t i = 0; i < w->n_waiting; i++) {
            if (firc_id_equal(w->waiting[i].group_id, job->group_id)) {
                slot = i;
                break;
            }
        }
        if (slot < w->n_waiting) {
            /* the newer job's seq is the one that counts, so it replaces the older one in place */
            free(w->waiting[slot].url);
            w->waiting[slot] = *job;
        } else {
            /* a pending follow-up counts against capacity too, so the queue's bound stays constant */
            if (w->n_waiting + (w->has_follow_up ? 1u : 0u) >= w->cap) {
                pthread_mutex_unlock(&w->mu);
                free(job->url);
                job->url = NULL;
                return FIRC_ERR_LIMIT;
            }
            w->waiting[w->n_waiting++] = *job;
        }
    }

    job->url = NULL; /* the worker owns it now */
    pthread_cond_signal(&w->cv);
    pthread_mutex_unlock(&w->mu);
    return FIRC_OK;
}

static void *worker_main(void *arg)
{
    firc_sub_worker_t *w = arg;

    for (;;) {
        pthread_mutex_lock(&w->mu);
        while (!w->stopping && w->n_waiting == 0) {
            pthread_cond_wait(&w->cv, &w->mu);
        }
        if (w->stopping) {
            pthread_mutex_unlock(&w->mu);
            break;
        }
        w->running = w->waiting[0];
        w->has_running = true;
        w->n_waiting--;
        if (w->n_waiting > 0) {
            memmove(&w->waiting[0], &w->waiting[1], w->n_waiting * sizeof(w->waiting[0]));
        }
        pthread_mutex_unlock(&w->mu);

        firc_sub_job_run(&w->running, w->cancel, w->emit, w->ud);

        pthread_mutex_lock(&w->mu);
        free(w->running.url);
        memset(&w->running, 0, sizeof(w->running));
        w->has_running = false;
        if (w->has_follow_up) {
            /* promoted to the front: it was asked for while this job ran and would already have had its turn */
            memmove(&w->waiting[1], &w->waiting[0], w->n_waiting * sizeof(w->waiting[0]));
            w->waiting[0] = w->follow_up;
            w->n_waiting++;
            memset(&w->follow_up, 0, sizeof(w->follow_up));
            w->has_follow_up = false;
        }
        bool stopping = w->stopping;
        pthread_mutex_unlock(&w->mu);
        if (stopping) {
            break;
        }
    }

    return NULL;
}

firc_err_t firc_sub_worker_start(firc_sub_worker_t *w)
{
    if (w == NULL) {
        return FIRC_ERR_INVAL;
    }
    if (w->started) {
        return FIRC_ERR_STATE;
    }
    if (pthread_create(&w->thread, NULL, worker_main, w) != 0) {
        return FIRC_ERR_SYS;
    }
    w->started = true;
    return FIRC_OK;
}

void firc_sub_worker_free(firc_sub_worker_t *w)
{
    if (w == NULL) {
        return;
    }

    pthread_mutex_lock(&w->mu);
    w->stopping = true;
    pthread_cond_broadcast(&w->cv);
    pthread_mutex_unlock(&w->mu);

    /* Ends the transfer in flight and stops anything more being emitted. */
    firc_cancel_raise(w->cancel);

    if (w->started) {
        pthread_join(w->thread, NULL);
        w->started = false;
    }

    for (size_t i = 0; i < w->n_waiting; i++) {
        free(w->waiting[i].url);
    }
    if (w->has_follow_up) {
        free(w->follow_up.url);
    }
    if (w->has_running) {
        free(w->running.url); /* only reachable if the thread never ran */
    }
    free(w->waiting);
    pthread_cond_destroy(&w->cv);
    pthread_mutex_destroy(&w->mu);
    firc_cancel_free(w->cancel);
    free(w);
}
