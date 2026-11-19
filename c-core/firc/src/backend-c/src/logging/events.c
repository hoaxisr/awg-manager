#include "firc/events.h"

#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "firc/rand.h"

/* One lock, held for a struct copy; nothing inside allocates or logs (a log call there would deadlock). */
typedef struct log_slot {
    uint64_t seq;
    int64_t at;
    firc_log_level_t level;
    char source[FIRC_EVENT_SOURCE];
    char text[FIRC_EVENT_TEXT];
} log_slot_t;

typedef struct log_ring {
    log_slot_t *slots;
    size_t size, head, used;
} log_ring_t;

static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static firc_event_t g_dns[FIRC_EVENTS_RING];
static log_slot_t g_debug_slots[FIRC_EVENTS_DEBUG_RING];
static log_slot_t g_info_slots[FIRC_EVENTS_LOG_RING];
static log_slot_t g_warn_slots[FIRC_EVENTS_WARN_RING];
enum { RING_DEBUG, RING_INFO, RING_WARN, N_LOG_RINGS };
static log_ring_t g_logs[N_LOG_RINGS] = {
    [RING_DEBUG] = {g_debug_slots, FIRC_EVENTS_DEBUG_RING, 0, 0},
    [RING_INFO] = {g_info_slots, FIRC_EVENTS_LOG_RING, 0, 0},
    [RING_WARN] = {g_warn_slots, FIRC_EVENTS_WARN_RING, 0, 0},
};
static size_t g_dns_head, g_dns_used;
static uint64_t g_next_seq = 1;

static size_t slot(size_t head, size_t used, size_t size, size_t i)
{
    return (head + size - used + i) % size;
}

static log_slot_t *log_at(const log_ring_t *r, size_t i)
{
    return &r->slots[slot(r->head, r->used, r->size, i)];
}

static void log_push(log_ring_t *r, const firc_event_t *e)
{
    log_slot_t *s = &r->slots[r->head];
    s->seq = e->seq;
    s->at = e->at;
    s->level = e->u.log.level;
    memcpy(s->source, e->u.log.source, sizeof(s->source));
    memcpy(s->text, e->u.log.text, sizeof(s->text));
    r->head = (r->head + 1) % r->size;
    if (r->used < r->size) { r->used++; }
}

void firc_event_put(firc_event_t *e)
{
    pthread_mutex_lock(&g_mu);
    e->seq = g_next_seq++;
    if (e->kind == FIRC_EVENT_LOG) {
        firc_log_level_t lv = e->u.log.level;
        log_push(&g_logs[lv >= FIRC_LOG_WARN ? RING_WARN : lv == FIRC_LOG_INFO ? RING_INFO : RING_DEBUG], e);
    } else {
        g_dns[g_dns_head] = *e;
        g_dns_head = (g_dns_head + 1) % FIRC_EVENTS_RING;
        if (g_dns_used < FIRC_EVENTS_RING) { g_dns_used++; }
    }
    pthread_mutex_unlock(&g_mu);
}

void firc_event_put_log(firc_log_level_t level, int64_t at, const char *source, const char *text)
{
    firc_event_t e;
    memset(&e, 0, sizeof(e));
    e.kind = FIRC_EVENT_LOG;
    e.at = at;
    e.u.log.level = level;
    snprintf(e.u.log.source, sizeof(e.u.log.source), "%s", source != NULL ? source : "");
    snprintf(e.u.log.text, sizeof(e.u.log.text), "%s", text);
    firc_event_put(&e);
}

static uint64_t dns_seq(const void *ring, size_t i)
{
    (void)ring;
    return g_dns[slot(g_dns_head, g_dns_used, FIRC_EVENTS_RING, i)].seq;
}

static uint64_t log_seq(const void *ring, size_t i)
{
    return log_at(ring, i)->seq;
}

static size_t first_after(uint64_t (*seq_at)(const void *, size_t), const void *ring, size_t used,
                          uint64_t since)
{
    size_t lo = 0, hi = used;
    while (lo < hi) {
        size_t mid = lo + (hi - lo) / 2;
        if (seq_at(ring, mid) <= since) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    return lo;
}

static void log_out(const log_ring_t *r, size_t i, firc_event_t *e)
{
    const log_slot_t *s = log_at(r, i);
    memset(e, 0, sizeof(*e));
    e->seq = s->seq;
    e->at = s->at;
    e->kind = FIRC_EVENT_LOG;
    e->u.log.level = s->level;
    memcpy(e->u.log.source, s->source, sizeof(e->u.log.source));
    memcpy(e->u.log.text, s->text, sizeof(e->u.log.text));
}

size_t firc_event_read(uint64_t since, firc_event_t *out, size_t cap,
                       uint64_t *out_next, uint64_t *out_dropped)
{
    if (out == NULL || cap == 0) { return 0; }
    pthread_mutex_lock(&g_mu);
    uint64_t newest = g_next_seq - 1;
    size_t d = first_after(dns_seq, NULL, g_dns_used, since);
    size_t at[N_LOG_RINGS];
    for (size_t r = 0; r < N_LOG_RINGS; r++) { at[r] = first_after(log_seq, &g_logs[r], g_logs[r].used, since); }
    size_t n = 0;
    while (n < cap) {
        uint64_t best = d < g_dns_used ? dns_seq(NULL, d) : UINT64_MAX;
        size_t pick = N_LOG_RINGS;
        for (size_t r = 0; r < N_LOG_RINGS; r++) {
            if (at[r] < g_logs[r].used && log_at(&g_logs[r], at[r])->seq < best) {
                best = log_at(&g_logs[r], at[r])->seq;
                pick = r;
            }
        }
        if (best == UINT64_MAX) { break; }
        if (pick < N_LOG_RINGS) {
            log_out(&g_logs[pick], at[pick]++, &out[n++]);
        } else {
            out[n++] = g_dns[slot(g_dns_head, g_dns_used, FIRC_EVENTS_RING, d++)];
        }
    }
    pthread_mutex_unlock(&g_mu);
    /* A reader at 0 has never asked, so nothing is reported dropped for it. */
    uint64_t dropped = 0;
    if (since != 0) {
        if (n > 0) {
            dropped = out[n - 1].seq - since - n;
        } else if (newest > since) {
            dropped = newest - since;
        }
    }
    if (out_dropped != NULL) { *out_dropped = dropped; }
    if (out_next != NULL && n > 0) { *out_next = out[n - 1].seq; }
    return n;
}

/* The random part tells apart two starts in one second (a crash and the init script's restart). */
static char g_boot[32];
static pthread_once_t g_boot_once = PTHREAD_ONCE_INIT;

static void boot_init(void)
{
    uint8_t r[4] = {0, 0, 0, 0};
    (void)firc_random_bytes(r, sizeof(r));
    snprintf(g_boot, sizeof(g_boot), "%llx-%02x%02x%02x%02x", (unsigned long long)time(NULL), r[0],
             r[1], r[2], r[3]);
}

const char *firc_event_boot(void)
{
    pthread_once(&g_boot_once, boot_init);
    return g_boot;
}

void firc_event_reset_for_test(void)
{
    pthread_mutex_lock(&g_mu);
    g_next_seq = 1;
    g_dns_head = g_dns_used = 0;
    for (size_t r = 0; r < N_LOG_RINGS; r++) { g_logs[r].head = g_logs[r].used = 0; }
    pthread_mutex_unlock(&g_mu);
}

const char *firc_dns_resolver_name(uint8_t r)
{
    switch ((firc_dns_resolver_t)r) {
    case FIRC_DNS_RESOLVER_UPSTREAM: return "upstream";
    case FIRC_DNS_RESOLVER_GROUP: return "group";
    case FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE: return "fallback_unreachable";
    case FIRC_DNS_RESOLVER_FALLBACK_TIMEOUT: return "fallback_timeout";
    case FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL: return "fallback_servfail";
    case FIRC_DNS_RESOLVER_FALLBACK_REFUSED: return "fallback_refused";
    case FIRC_DNS_RESOLVER_FALLBACK_SINK: return "fallback_sink";
    case FIRC_DNS_RESOLVER_HEALTH_SKIP: return "health_skip";
    case FIRC_DNS_RESOLVER_CACHE: return "cache";
    case FIRC_DNS_RESOLVER_LOCAL: return "local";
    }
    return "upstream";
}
