#ifndef FIRC_EVENTS_H
#define FIRC_EVENTS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip_addr.h"
#include "firc/id.h"
#include "firc/log.h"

#define FIRC_EVENTS_RING 4096
#define FIRC_EVENTS_LOG_RING 512
#define FIRC_EVENTS_DEBUG_RING 512
#define FIRC_EVENTS_WARN_RING 256
#define FIRC_EVENT_TEXT 240
#define FIRC_EVENT_SOURCE 16
#define FIRC_EVENT_NAME 256
#define FIRC_EVENT_GROUP_NAME 48
#define FIRC_EVENT_REALS 4

typedef enum firc_event_kind {
    FIRC_EVENT_LOG = 0,
    FIRC_EVENT_DNS,
    FIRC_EVENT_BYPASS,
} firc_event_kind_t;

typedef enum firc_dns_decision {
    FIRC_DNS_ISSUED = 0,
    FIRC_DNS_NOT_COVERED,
    FIRC_DNS_NO_MATCH,
    FIRC_DNS_POOL_REFUSED,
    FIRC_DNS_PASSED,
    FIRC_DNS_BLOCKED,
} firc_dns_decision_t;

typedef enum firc_dns_resolver {
    FIRC_DNS_RESOLVER_UPSTREAM = 0,
    FIRC_DNS_RESOLVER_GROUP,
    FIRC_DNS_RESOLVER_FALLBACK_UNREACHABLE,
    FIRC_DNS_RESOLVER_FALLBACK_TIMEOUT,
    FIRC_DNS_RESOLVER_FALLBACK_SERVFAIL,
    FIRC_DNS_RESOLVER_FALLBACK_REFUSED,
    FIRC_DNS_RESOLVER_FALLBACK_SINK,
    FIRC_DNS_RESOLVER_HEALTH_SKIP,
    FIRC_DNS_RESOLVER_CACHE,
    FIRC_DNS_RESOLVER_LOCAL,
} firc_dns_resolver_t;

/* The journal's string for a firc_dns_resolver_t; an unknown byte (a newer daemon's) reads as upstream. */
const char *firc_dns_resolver_name(uint8_t r);

typedef enum firc_bypass_how { FIRC_BYPASS_BY_ADDR = 0, FIRC_BYPASS_BY_SNI } firc_bypass_how_t;

/* No recorded answer for (client, name) within the recall horizon. */
#define FIRC_BYPASS_NOT_ASKED 0xffu

typedef struct firc_event_log {
    firc_log_level_t level;
    char source[FIRC_EVENT_SOURCE]; /* "" for fircd itself, else a tunnel device */
    char text[FIRC_EVENT_TEXT];
} firc_event_log_t;

typedef struct firc_event_dns {
    firc_ip_t client;
    char name[FIRC_EVENT_NAME];      /* folded, no trailing dot */
    uint16_t qtype;
    uint8_t rcode;
    uint8_t decision;                /* firc_dns_decision_t */
    char group_id[FIRC_ID_STR_LEN];  /* "" when no group */
    char group_name[FIRC_EVENT_GROUP_NAME];
    firc_ip_t fake;                  /* len 0: none put in the answer */
    firc_ip_t reals[FIRC_EVENT_REALS];
    uint8_t n_reals;
    uint8_t resolver; /* firc_dns_resolver_t */
} firc_event_dns_t;

/* A flow the capture judged not routed by firc; last_* is the recall's last answer for (client, name). */
typedef struct firc_event_bypass {
    firc_ip_t client;
    firc_ip_t dst;
    uint16_t dst_port;              /* 0 when the protocol has no ports */
    uint8_t proto;                  /* IP protocol number */
    uint8_t how;                    /* firc_bypass_how_t */
    char name[FIRC_EVENT_NAME];
    char group_id[FIRC_ID_STR_LEN];
    char group_name[FIRC_EVENT_GROUP_NAME];
    uint8_t last_decision;          /* firc_dns_decision_t, or FIRC_BYPASS_NOT_ASKED */
    int64_t last_at;                /* 0 when not asked */
    firc_ip_t last_fake;            /* len 0 when none */
    uint32_t repeats;               /* flows folded since the last event */
} firc_event_bypass_t;

typedef struct firc_event {
    uint64_t seq; /* set by the ring */
    int64_t at;   /* unix seconds; set by the caller */
    firc_event_kind_t kind;
    union {
        firc_event_log_t log;
        firc_event_dns_t dns;
        firc_event_bypass_t bypass;
    } u;
} firc_event_t;

/* A bypass event shares the dns ring, so the union must not grow past 448 bytes. */
_Static_assert(sizeof(firc_event_t) <= 448, "a bypass event must not grow the journal ring");

/* Copies e into its kind's ring and stamps seq. Thread-safe; never allocates or logs. */
void firc_event_put(firc_event_t *e);

/* Records a log event; source NULL or "" is fircd itself. */
void firc_event_put_log(firc_log_level_t level, int64_t at, const char *source, const char *text);

/* Copies events with seq > since, oldest first, up to cap; *out_next is the cursor, *out_dropped counts lost seqs. */
size_t firc_event_read(uint64_t since, firc_event_t *out, size_t cap,
                       uint64_t *out_next, uint64_t *out_dropped);

/* Id of this run, fixed on first call; a changed id tells a reader its cursor is stale (seqs restart at 1). */
const char *firc_event_boot(void);

/* Tests only. */
void firc_event_reset_for_test(void);

#endif
