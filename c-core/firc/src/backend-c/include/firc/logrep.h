#ifndef FIRC_LOGREP_H
#define FIRC_LOGREP_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/log.h"

#define FIRC_LOGREP_REMIND_MS (10u * 60u * 1000u)

typedef struct firc_logrep {
    uint64_t count;
    uint64_t said_ms;
    uint64_t first_ms;
    bool escalated;
} firc_logrep_t;

/* A failure that may repeat: the first at level, repeats at DEBUG, and at level again with
   "(still failing, N times for <duration>)" once FIRC_LOGREP_REMIND_MS passed since the last line at level. */
void firc_logrep_fail(firc_logrep_t *r, firc_log_level_t level, const char *source, const char *fmt, ...)
    FIRC_PRINTF(4, 5);

/* As firc_logrep_fail, said at WARN until the run has lasted after_ms, then once at ERROR and its reminders at
   ERROR; after_ms 0 says ERROR from the first. */
void firc_logrep_fail_for(firc_logrep_t *r, uint64_t after_ms, const char *source, const char *fmt, ...)
    FIRC_PRINTF(4, 5);

/* True when the next failure of the run would be said above DEBUG (a reminder or the raise to ERROR is due). */
bool firc_logrep_due(const firc_logrep_t *r, uint64_t after_ms);

/* As firc_logrep_fail_for without the DEBUG repeats, for a caller on a per-query path; after_ms UINT64_MAX never
   raises to ERROR. */
void firc_logrep_fail_for_quiet(firc_logrep_t *r, uint64_t after_ms, const char *source, const char *fmt, ...)
    FIRC_PRINTF(4, 5);

/* Ends a run without a word, for a caller that says the recovery in its own words. */
void firc_logrep_reset(firc_logrep_t *r);

/* After failures: INFO "<subject>: recovered after N failures over <duration>", and r starts over; true when said. */
bool firc_logrep_ok(firc_logrep_t *r, const char *source, const char *fmt, ...) FIRC_PRINTF(3, 4);

typedef struct firc_lograte {
    uint64_t next_ms;
    uint64_t held;
} firc_lograte_t;

/* A line a client or a burst can trigger: said at once, then at most once per interval_ms with
   "(N more since the last)" for the lines held back in between. */
void firc_lograte_say(firc_lograte_t *r, uint64_t interval_ms, firc_log_level_t level, const char *source,
                      const char *fmt, ...) FIRC_PRINTF(5, 6);

/* The monotonic ms the helpers read (the test clock when one is set). */
uint64_t firc_logrep_now_ms(void);

/* Test seam: the monotonic ms both read; NULL restores the real clock. */
void firc_logrep_set_clock_for_test(uint64_t (*mono_ms)(void));

#endif
