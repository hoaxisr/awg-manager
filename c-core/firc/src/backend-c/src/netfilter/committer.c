#include "firc/nfcommit.h"

#include <errno.h>
#include <pthread.h>
#include <stdlib.h>
#include <time.h>

#include "firc/log.h"
#include "firc/logrep.h"

/* Folds a firmware burst (one event per table) into one pass. */
#define FIRC_NFCOMMIT_DELAY_MS 150u
/* Backoff ceiling for hard failures; races never back off. */
#define FIRC_NFCOMMIT_MAX_DELAY_MS 5000u
/* Hard refusals in a row before a WARN; a lost COMMIT race is one and its retry usually fixes it. */
#define FIRC_NFCOMMIT_WARN_AFTER 3u
#define FIRC_NFCOMMIT_ERROR_AFTER_MS 60000u

struct firc_nfcommit {
    firc_nfcommit_rebuild_fn rebuild;
    void *ud;
    firc_nfcommit_watch_fn watch;
    unsigned watch_ms;

    firc_cancel_t *cancel;

    pthread_mutex_t mu;
    pthread_cond_t cv;
    pthread_t thread;

    bool started;
    bool stopping;
    bool pending;
    bool full_owed;
    bool long_owed;
    size_t n_requests;
    bool running;
    uint64_t passes;
    uint64_t last_ok;

    unsigned delay_ms;
    unsigned max_delay_ms;
    unsigned addr_delay_ms;
    unsigned addr_idle_ms;
    uint64_t last_end_ms;

    /* 0: no failing run. Guarded by mu. */
    uint64_t fail_mono_ms;
    int64_t fail_wall;
    unsigned hard_run;
    firc_err_t fail_err;
    bool said_run;
    uint64_t log_start;
    uint64_t log_after;
    unsigned failing_after_ms;
    firc_logrep_t said;
};

static uint64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    /* +1: a run that starts at 0 ms must not read as no run */
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u + 1u;
}

static bool is_race(firc_err_t err) { return err == FIRC_ERR_CANCELED || err == FIRC_ERR_AGAIN; }

typedef enum { NOTE_QUIET, NOTE_FAIL, NOTE_RACES, NOTE_OK } note_say_t;

static note_say_t note_result(firc_nfcommit_t *c, firc_err_t err, uint64_t *after) {
    if (err == FIRC_OK) {
        bool said = c->said_run;
        c->fail_mono_ms = 0;
        c->hard_run = 0;
        c->fail_err = FIRC_OK;
        c->said_run = false;
        return said ? NOTE_OK : NOTE_QUIET;
    }
    if (c->fail_mono_ms == 0) {
        c->fail_mono_ms = mono_ms();
        c->fail_wall = (int64_t)time(NULL);
        c->log_start = firc_logrep_now_ms();
    }
    bool race = is_race(err);
    if (race) {
        if (c->hard_run == 0) { c->fail_err = err; }
    } else {
        c->hard_run++;
        c->fail_err = err;
    }
    if (!c->said_run) {
        bool hard_due = !race && c->hard_run >= FIRC_NFCOMMIT_WARN_AFTER;
        bool race_due = err == FIRC_ERR_AGAIN && mono_ms() - c->fail_mono_ms >= c->failing_after_ms;
        if (!hard_due && !race_due) { return NOTE_QUIET; }
        uint64_t gone = firc_logrep_now_ms() - c->log_start;
        c->log_after = gone >= FIRC_NFCOMMIT_ERROR_AFTER_MS ? 0 : FIRC_NFCOMMIT_ERROR_AFTER_MS - gone;
        c->said_run = true;
    }
    *after = c->log_after;
    return race ? NOTE_RACES : NOTE_FAIL;
}

/* Said outside `mu` (a write(2) may block on a pipe); the tracker is the committer thread's own. */
static void say_note(firc_nfcommit_t *c, note_say_t say, firc_err_t err, uint64_t after) {
    if (say == NOTE_FAIL) {
        firc_logrep_fail_for(&c->said, after, NULL, "netfilter pass keeps failing: %s", firc_err_str(err));
    } else if (say == NOTE_RACES) {
        firc_logrep_fail_for(&c->said, after, NULL,
                             "netfilter pass keeps losing races: a table changed while it was read, "
                             "or another writer held the xtables lock");
    } else if (say == NOTE_OK) {
        (void)firc_logrep_ok(&c->said, NULL, "netfilter pass");
    }
}

firc_nfcommit_t *firc_nfcommit_new(firc_nfcommit_rebuild_fn fn, void *ud) {
    if (!fn) { return NULL; }

    firc_nfcommit_t *c = calloc(1, sizeof(*c));
    if (!c) { return NULL; }

    c->cancel = firc_cancel_new();
    if (!c->cancel) {
        free(c);
        return NULL;
    }
    if (pthread_mutex_init(&c->mu, NULL) != 0) {
        firc_cancel_free(c->cancel);
        free(c);
        return NULL;
    }

    pthread_condattr_t attr;
    int attr_rc = pthread_condattr_init(&attr);
    bool cv_ok = false;
    if (attr_rc == 0) {
        cv_ok = pthread_condattr_setclock(&attr, CLOCK_MONOTONIC) == 0 &&
                pthread_cond_init(&c->cv, &attr) == 0;
        pthread_condattr_destroy(&attr);
    }
    if (!cv_ok) {
        pthread_mutex_destroy(&c->mu);
        firc_cancel_free(c->cancel);
        free(c);
        return NULL;
    }

    c->rebuild = fn;
    c->ud = ud;
    c->full_owed = true;
    c->delay_ms = FIRC_NFCOMMIT_DELAY_MS;
    c->max_delay_ms = FIRC_NFCOMMIT_MAX_DELAY_MS;
    c->addr_delay_ms = FIRC_NFCOMMIT_ADDR_DELAY_MS;
    c->addr_idle_ms = FIRC_NFCOMMIT_ADDR_IDLE_MS;
    c->failing_after_ms = FIRC_NFCOMMIT_FAILING_AFTER_MS;
    return c;
}

size_t firc_nfcommit_requests_for_test(firc_nfcommit_t *c) {
    if (!c) { return 0; }
    pthread_mutex_lock(&c->mu);
    size_t n = c->n_requests;
    pthread_mutex_unlock(&c->mu);
    return n;
}

void firc_nfcommit_set_delays_for_test(firc_nfcommit_t *c, unsigned delay_ms, unsigned max_delay_ms) {
    if (!c) { return; }
    c->delay_ms = delay_ms;
    c->max_delay_ms = max_delay_ms;
}

void firc_nfcommit_set_addr_delay_for_test(firc_nfcommit_t *c, unsigned ms) {
    if (!c) { return; }
    c->addr_delay_ms = ms;
}

void firc_nfcommit_set_addr_idle_for_test(firc_nfcommit_t *c, unsigned ms) {
    if (!c) { return; }
    c->addr_idle_ms = ms;
}

void firc_nfcommit_set_failing_after_for_test(firc_nfcommit_t *c, unsigned ms) {
    if (c == NULL) { return; }
    pthread_mutex_lock(&c->mu);
    c->failing_after_ms = ms;
    pthread_mutex_unlock(&c->mu);
}

/* Judged at read time, so a pass hung after failures still trips the bound. */
void firc_nfcommit_health(const firc_nfcommit_t *c, firc_nfcommit_health_t *out) {
    *out = (firc_nfcommit_health_t){.first_pending = false, .failing = false, .err = FIRC_OK, .since = 0};
    if (c == NULL) { return; }
    firc_nfcommit_t *m = (firc_nfcommit_t *)c;
    pthread_mutex_lock(&m->mu);
    out->first_pending = m->last_ok == 0;
    if (m->fail_mono_ms != 0 && mono_ms() - m->fail_mono_ms >= (uint64_t)m->failing_after_ms) {
        out->failing = true;
        out->err = m->fail_err;
        out->since = m->fail_wall;
    }
    pthread_mutex_unlock(&m->mu);
}

uint64_t firc_nfcommit_passes(const firc_nfcommit_t *c) {
    if (!c) { return 0; }
    firc_nfcommit_t *m = (firc_nfcommit_t *)c;
    pthread_mutex_lock(&m->mu);
    uint64_t n = m->passes;
    pthread_mutex_unlock(&m->mu);
    return n;
}

uint64_t firc_nfcommit_last_completed_pass(const firc_nfcommit_t *c) {
    if (!c) { return 0; }
    firc_nfcommit_t *m = (firc_nfcommit_t *)c;
    pthread_mutex_lock(&m->mu);
    uint64_t n = m->last_ok;
    pthread_mutex_unlock(&m->mu);
    return n;
}

void firc_nfcommit_request(firc_nfcommit_t *c) {
    if (!c) { return; }

    pthread_mutex_lock(&c->mu);
    c->n_requests++;
    c->pending = true;
    c->full_owed = true;
    /* Raised under the lock so decide-and-raise is one step. */
    if (c->running) { firc_cancel_raise(c->cancel); }
    pthread_cond_signal(&c->cv);
    pthread_mutex_unlock(&c->mu);
}

void firc_nfcommit_request_more(firc_nfcommit_t *c) {
    if (!c) { return; }

    pthread_mutex_lock(&c->mu);
    c->pending = true;
    c->long_owed = true;
    pthread_cond_signal(&c->cv);
    pthread_mutex_unlock(&c->mu);
    /* No raise: the pass in flight finishes and `pending` makes another follow. */
}

void firc_nfcommit_request_addresses(firc_nfcommit_t *c) {
    if (!c) { return; }

    pthread_mutex_lock(&c->mu);
    c->pending = true;
    if (c->running || (c->last_end_ms != 0 && mono_ms() - c->last_end_ms < (uint64_t)c->addr_idle_ms)) {
        c->long_owed = true;
    }
    pthread_cond_signal(&c->cv);
    pthread_mutex_unlock(&c->mu);
}

void firc_nfcommit_set_watch(firc_nfcommit_t *c, firc_nfcommit_watch_fn fn, unsigned interval_ms) {
    if (!c) { return; }
    c->watch = fn;
    c->watch_ms = interval_ms;
}

void firc_nfcommit_interrupt(firc_nfcommit_t *c) {
    if (!c) { return; }

    /* No pending flag: an aborted pass re-arms itself. */
    pthread_mutex_lock(&c->mu);
    if (c->running) { firc_cancel_raise(c->cancel); }
    pthread_mutex_unlock(&c->mu);
}

static struct timespec deadline_after(unsigned delay_ms) {
    struct timespec deadline;
    clock_gettime(CLOCK_MONOTONIC, &deadline);
    deadline.tv_sec += (time_t)(delay_ms / 1000u);
    deadline.tv_nsec += (long)(delay_ms % 1000u) * 1000000L;
    if (deadline.tv_nsec >= 1000000000L) {
        deadline.tv_sec += 1;
        deadline.tv_nsec -= 1000000000L;
    }
    return deadline;
}

/* Returns false on exit; a new request does not cut the wait short. */
static bool settle(firc_nfcommit_t *c, unsigned delay_ms) {
    struct timespec deadline = deadline_after(delay_ms);

    pthread_mutex_lock(&c->mu);
    while (!c->stopping) {
        int rc = pthread_cond_timedwait(&c->cv, &c->mu, &deadline);
        if (rc == ETIMEDOUT) { break; }
    }
    bool go_on = !c->stopping;
    pthread_mutex_unlock(&c->mu);
    return go_on;
}

static void wait_for_work(firc_nfcommit_t *c) {
    struct timespec deadline = deadline_after(c->watch_ms);
    while (!c->pending && !c->stopping) {
        if (c->watch == NULL) {
            pthread_cond_wait(&c->cv, &c->mu);
            continue;
        }
        if (pthread_cond_timedwait(&c->cv, &c->mu, &deadline) != ETIMEDOUT) { continue; }
        pthread_mutex_unlock(&c->mu);
        bool moved = c->watch(c->ud);
        pthread_mutex_lock(&c->mu);
        if (moved) {
            FIRC_DEBUG("a netfilter table changed with no hook, writing it again");
            c->pending = true;
            c->full_owed = true;
        }
        deadline = deadline_after(c->watch_ms);
    }
}

static void *committer_main(void *arg) {
    firc_nfcommit_t *c = arg;
    unsigned backoff_ms = 0;

    for (;;) {
        pthread_mutex_lock(&c->mu);
        wait_for_work(c);
        bool stopping = c->stopping;
        pthread_mutex_unlock(&c->mu);
        if (stopping) { break; }

        unsigned short_ms = c->addr_delay_ms < c->delay_ms ? c->addr_delay_ms : c->delay_ms;
        if (!settle(c, backoff_ms != 0 ? backoff_ms : short_ms)) { break; }

        pthread_mutex_lock(&c->mu);
        if (backoff_ms == 0 && short_ms < c->delay_ms && !c->stopping && (c->long_owed || c->full_owed)) {
            pthread_mutex_unlock(&c->mu);
            if (!settle(c, c->delay_ms - short_ms)) { break; }
            pthread_mutex_lock(&c->mu);
        }
        if (c->stopping) {
            pthread_mutex_unlock(&c->mu);
            break;
        }
        c->pending = false;
        bool full = c->full_owed;
        bool was_long = c->long_owed || full;
        c->full_owed = false;
        c->long_owed = false;
        /* Cleared before `running` is published, under mu, or a raise in the gap is lost. */
        firc_cancel_clear(c->cancel);
        c->running = true;
        uint64_t ordinal = ++c->passes;
        pthread_mutex_unlock(&c->mu);

        firc_err_t err = c->rebuild(c->ud, c->cancel, full);

        pthread_mutex_lock(&c->mu);
        c->running = false;
        c->last_end_ms = mono_ms();
        if (err == FIRC_OK) { c->last_ok = ordinal; }
        uint64_t after = 0;
        note_say_t say = note_result(c, err, &after);
        stopping = c->stopping;
        pthread_mutex_unlock(&c->mu);
        say_note(c, say, err, after);
        if (stopping) { break; }

        if (err == FIRC_OK) {
            backoff_ms = 0;
            continue;
        }

        if (err == FIRC_ERR_CANCELED) {
            FIRC_DEBUG("netfilter table rebuild interrupted, starting over");
            backoff_ms = 0;
        } else if (err == FIRC_ERR_AGAIN) {
            FIRC_DEBUG("netfilter table changed during rebuild, starting over");
            backoff_ms = 0;
            was_long = true;
        } else {
            FIRC_DEBUG("failed to rebuild netfilter table (%s), starting over", firc_err_str(err));
            unsigned base = backoff_ms != 0 ? backoff_ms : c->delay_ms;
            backoff_ms = base * 2 < c->max_delay_ms ? base * 2 : c->max_delay_ms;
        }

        pthread_mutex_lock(&c->mu);
        c->pending = true;
        c->full_owed = c->full_owed || full;
        c->long_owed = c->long_owed || was_long;
        pthread_mutex_unlock(&c->mu);
    }

    return NULL;
}

firc_err_t firc_nfcommit_start(firc_nfcommit_t *c) {
    if (!c) { return FIRC_ERR_INVAL; }
    if (c->started) { return FIRC_ERR_STATE; }

    if (pthread_create(&c->thread, NULL, committer_main, c) != 0) { return FIRC_ERR_SYS; }
    c->started = true;
    return FIRC_OK;
}

void firc_nfcommit_stop(firc_nfcommit_t *c) {
    if (!c || !c->started) { return; }

    pthread_mutex_lock(&c->mu);
    c->stopping = true;
    pthread_cond_broadcast(&c->cv);
    pthread_mutex_unlock(&c->mu);

    firc_cancel_raise(c->cancel);

    pthread_join(c->thread, NULL);
    c->started = false;
}

void firc_nfcommit_free(firc_nfcommit_t *c) {
    if (!c) { return; }
    firc_nfcommit_stop(c);
    pthread_cond_destroy(&c->cv);
    pthread_mutex_destroy(&c->mu);
    firc_cancel_free(c->cancel);
    free(c);
}
