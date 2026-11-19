#ifndef FIRC_AUTOLISTPROBES_H
#define FIRC_AUTOLISTPROBES_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/autolistcheck.h"

#define FIRC_AUTOLIST_PROBES_PER_HOUR 30
#define FIRC_AUTOLIST_PROBES_QUEUE 64
#define FIRC_AUTOLIST_PROBES_TICK_SECS 120
#define FIRC_AUTOLIST_PROBE_HOST_SECS 600
#define FIRC_AUTOLIST_CACHE_OPEN_SECS 86400
#define FIRC_AUTOLIST_CACHE_UNKNOWN_SECS 3600
#define FIRC_AUTOLIST_PROBE_CACHE 256

typedef struct firc_al_probes firc_al_probes_t;

typedef struct {
    char host[FIRC_AUTOLIST_NAME_MAX];
    firc_autolist_result_t result;
    int64_t at;
} firc_al_probe_seen_t;

typedef enum {
    FIRC_AL_ASK_QUEUED,
    FIRC_AL_ASK_CACHED,
    FIRC_AL_ASK_BUSY,
    FIRC_AL_ASK_FULL,
} firc_al_ask_t;

firc_al_probes_t *firc_al_probes_new(void);
void firc_al_probes_free(firc_al_probes_t *p);

/* Adds a note for host, by_domain as the latest nomination says; CACHED when a verdict is cached; FULL when the queue
 * holds only requests. */
firc_al_ask_t firc_al_probes_nominate(firc_al_probes_t *p, const char *host, bool by_domain, int64_t now);

/* A user request goes first and may push out a queued recheck; BUSY within the host window, FULL when the hour is spent or the queue holds only requests. */
firc_al_ask_t firc_al_probes_request(firc_al_probes_t *p, const char *host, int64_t now);

/* Bypasses the cache and queues after user requests, before nominations. */
firc_al_ask_t firc_al_probes_recheck(firc_al_probes_t *p, const char *host, int64_t now);

/* Takes the next host when the budget allows and counts it; nominations only with allow_notes; false when nothing is due.
 * *by_domain is what its last nomination said, false for a host never nominated. */
bool firc_al_probes_next(firc_al_probes_t *p, int64_t now, char *host, size_t cap, bool allow_notes, bool *recheck,
                         bool *by_domain);

/* Ends a probe handed out by next and caches its verdict. */
void firc_al_probes_done(firc_al_probes_t *p, const char *host, const firc_autolist_result_t *r, int64_t now);

/* The last result while it is still shown (an hour at least, any verdict), or NULL; at is when it was probed. */
const firc_autolist_result_t *firc_al_probes_result(const firc_al_probes_t *p, const char *host, int64_t now,
                                                    int64_t *at);

/* Every result still shown, newest first, at most cap; returns how many were written. */
size_t firc_al_probes_results(const firc_al_probes_t *p, int64_t now, firc_al_probe_seen_t *out, size_t cap);

/* True while a recheck waits in the queue; the running probe is the caller's to know. */
bool firc_al_probes_rechecking(const firc_al_probes_t *p);

void firc_al_probes_counts(const firc_al_probes_t *p, int64_t now, uint32_t *used_hour, uint32_t *queued);

#endif
