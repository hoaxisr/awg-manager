#include "firc/logrep.h"

#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

static uint64_t real_mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static uint64_t (*g_mono_ms)(void) = real_mono_ms;

void firc_logrep_set_clock_for_test(uint64_t (*mono_ms)(void))
{
    g_mono_ms = mono_ms != NULL ? mono_ms : real_mono_ms;
}

uint64_t firc_logrep_now_ms(void)
{
    return g_mono_ms();
}

static void lasted(uint64_t ms, char *out, size_t cap)
{
    if (ms < 60000u) {
        snprintf(out, cap, "%llu s", (unsigned long long)(ms / 1000u));
    } else {
        snprintf(out, cap, "%llu min", (unsigned long long)(ms / 60000u));
    }
}

FIRC_PRINTF(6, 0) static void fail(firc_logrep_t *r, firc_log_level_t level, uint64_t after_ms, bool quiet,
                                    const char *source, const char *fmt, va_list ap)
{
    char text[1024];
    vsnprintf(text, sizeof(text), fmt, ap);
    uint64_t now = g_mono_ms();
    r->count++;
    if (r->count == 1) {
        r->first_ms = now;
        r->said_ms = now;
        r->escalated = after_ms == 0;
        firc_log_from(r->escalated ? FIRC_LOG_ERROR : level, source, "%s", text);
        return;
    }
    bool escalate = !r->escalated && after_ms != UINT64_MAX && now - r->first_ms >= after_ms;
    if (!escalate && now - r->said_ms < FIRC_LOGREP_REMIND_MS) {
        if (!quiet) { firc_log_from(FIRC_LOG_DEBUG, source, "%s", text); }
        return;
    }
    r->escalated = r->escalated || escalate;
    r->said_ms = now;
    char how[32];
    lasted(now - r->first_ms, how, sizeof(how));
    firc_log_from(r->escalated ? FIRC_LOG_ERROR : level, source, "%s (still failing, %llu times for %s)", text,
                  (unsigned long long)r->count, how);
}

void firc_logrep_fail(firc_logrep_t *r, firc_log_level_t level, const char *source, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    fail(r, level, UINT64_MAX, false, source, fmt, ap);
    va_end(ap);
}

void firc_logrep_fail_for(firc_logrep_t *r, uint64_t after_ms, const char *source, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    fail(r, FIRC_LOG_WARN, after_ms, false, source, fmt, ap);
    va_end(ap);
}

bool firc_logrep_due(const firc_logrep_t *r, uint64_t after_ms)
{
    uint64_t now = g_mono_ms();
    if (r->count == 0) { return true; }
    if (!r->escalated && after_ms != UINT64_MAX && now - r->first_ms >= after_ms) { return true; }
    return now - r->said_ms >= FIRC_LOGREP_REMIND_MS;
}

void firc_logrep_fail_for_quiet(firc_logrep_t *r, uint64_t after_ms, const char *source, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    fail(r, FIRC_LOG_WARN, after_ms, true, source, fmt, ap);
    va_end(ap);
}

void firc_logrep_reset(firc_logrep_t *r)
{
    memset(r, 0, sizeof(*r));
}

bool firc_logrep_ok(firc_logrep_t *r, const char *source, const char *fmt, ...)
{
    if (r->count == 0) { return false; }
    char subject[512];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(subject, sizeof(subject), fmt, ap);
    va_end(ap);
    char how[32];
    lasted(g_mono_ms() - r->first_ms, how, sizeof(how));
    firc_log_from(FIRC_LOG_INFO, source, "%s: recovered after %llu failure%s over %s", subject,
                  (unsigned long long)r->count, r->count == 1 ? "" : "s", how);
    memset(r, 0, sizeof(*r));
    return true;
}

void firc_lograte_say(firc_lograte_t *r, uint64_t interval_ms, firc_log_level_t level, const char *source,
                      const char *fmt, ...)
{
    uint64_t now = g_mono_ms();
    if (r->next_ms != 0 && now < r->next_ms) {
        r->held++;
        return;
    }
    char text[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(text, sizeof(text), fmt, ap);
    va_end(ap);
    r->next_ms = now + interval_ms;
    if (r->next_ms == 0) { r->next_ms = 1; }
    if (r->held == 0) {
        firc_log_from(level, source, "%s", text);
        return;
    }
    firc_log_from(level, source, "%s (%llu more since the last)", text, (unsigned long long)r->held);
    r->held = 0;
}
