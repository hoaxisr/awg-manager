#ifndef FIRC_AUTOLIST_H
#define FIRC_AUTOLIST_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip.h"
#include "firc/id.h"

#define FIRC_AUTOLIST_FLOWS 1024
#define FIRC_AUTOLIST_HOSTS 512
#define FIRC_AUTOLIST_DOMAINS 512
#define FIRC_AUTOLIST_HINTS 4096
#define FIRC_AUTOLIST_FLOW_SECS 300
#define FIRC_AUTOLIST_HINT_SECS 600
#define FIRC_AUTOLIST_NAME_MAX 254

#define FIRC_AUTOLIST_FAIL_THRESHOLD 3
#define FIRC_AUTOLIST_FAIL_SECS 60
#define FIRC_AUTOLIST_RETRANS_THRESHOLD 3
#define FIRC_AUTOLIST_SYN_SPAN_SECS 2

#define FIRC_AUTOLIST_STALL_MIN_BYTES 8192
#define FIRC_AUTOLIST_STALL_MAX_BYTES 32768
#define FIRC_AUTOLIST_STALL_MIN_SECS 10
#define FIRC_AUTOLIST_NOTES 2
#define FIRC_AUTOLIST_NOTE_SECS 120

#define FIRC_AUTOLIST_ADDRS 64
#define FIRC_AUTOLIST_ADDR_CLIENTS 8
#define FIRC_AUTOLIST_PROTO_TCP 6
#define FIRC_AUTOLIST_DAY_SECS 86400
#define FIRC_AUTOLIST_DAY_HOURS 24
#define FIRC_AUTOLIST_POOLS 2

typedef struct firc_autolist firc_autolist_t;

typedef struct firc_autolist_net {
    firc_ip_t base;
    uint8_t prefix;
} firc_autolist_net_t;

typedef struct firc_autolist_params {
    uint32_t fail_threshold;
    uint32_t fail_secs;
    uint32_t retrans_threshold;
} firc_autolist_params_t;

typedef struct firc_autolist_flow {
    firc_ip_t client, server;
    uint16_t client_port, server_port;
} firc_autolist_flow_t;

typedef enum {
    FIRC_AUTOLIST_RETRANS = 1,
    FIRC_AUTOLIST_RESET,
    FIRC_AUTOLIST_NO_CONNECT,
} firc_autolist_why_t;

typedef enum { FIRC_AL_OUTCOME_FAILED = 0, FIRC_AL_OUTCOME_BYPASS = 1 } firc_al_outcome_t;

typedef enum { FIRC_AL_HOW_NONE = 0, FIRC_AL_HOW_ANSWER, FIRC_AL_HOW_SNI, FIRC_AL_HOW_PTR } firc_al_how_t;

typedef struct {
    firc_ip_t server;
    uint8_t proto;
    uint16_t port;
    char name[FIRC_AUTOLIST_NAME_MAX];
    uint8_t why;
    uint8_t outcome;
    uint8_t how;
    char group[FIRC_ID_STR_LEN];
    uint32_t fails;
    int64_t first_at, last_at;
    uint8_t n_clients;
    firc_ip_t clients[FIRC_AUTOLIST_ADDR_CLIENTS];
} firc_autolist_addr_t;

typedef enum {
    FIRC_AL_NOM_STALL = 1,
    FIRC_AL_NOM_NO_CONNECT = 2,
} firc_autolist_nom_kind_t;

typedef struct firc_autolist_failure {
    char host[FIRC_AUTOLIST_NAME_MAX];
    char domain[FIRC_AUTOLIST_NAME_MAX];
    firc_ip_t client;
    firc_ip_t server;
    uint32_t fails;
    uint8_t why;
    bool listed;
    bool bypass;
} firc_autolist_failure_t;

typedef struct {
    char host[FIRC_AUTOLIST_NAME_MAX];
    char domain[FIRC_AUTOLIST_NAME_MAX];
    firc_ip_t client;
    firc_ip_t server;
    uint32_t received;
    uint8_t kind;
    bool bypass;
} firc_autolist_nomination_t;

/* True for an address no site has: 0/8, private, link-local, loopback, CGNAT, 198.18.0.0/15, fc00::/7,
 * fe80::/10, :: and ::1 (v4-mapped too), or one of pools. */
bool firc_autolist_local(const firc_ip_t *ip, const firc_autolist_net_t *pools, size_t n_pools);

firc_autolist_t *firc_autolist_new(void);
void firc_autolist_free(firc_autolist_t *a);

void firc_autolist_set_params(firc_autolist_t *a, const firc_autolist_params_t *p);

void firc_autolist_hint(firc_autolist_t *a, const firc_ip_t *client, const char *name, const firc_ip_t *reals,
                        size_t n_reals, int64_t now);

bool firc_autolist_hello(firc_autolist_t *a, const firc_autolist_flow_t *f, uint32_t seq, uint32_t ack,
                         const char *sni, int64_t now, firc_autolist_failure_t *out);

bool firc_autolist_reply(firc_autolist_t *a, const firc_autolist_flow_t *f, bool rst, int64_t now,
                         firc_autolist_failure_t *out);

bool firc_autolist_fin(firc_autolist_t *a, const firc_autolist_flow_t *f, uint32_t ack, int64_t now,
                       firc_autolist_nomination_t *out);

bool firc_autolist_syn(firc_autolist_t *a, const firc_autolist_flow_t *f, uint32_t seq, int64_t now,
                       firc_autolist_nomination_t *out);

/* True for any unexpired hint of (client, server), whatever its name. */
bool firc_autolist_hinted(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server, int64_t now);

/* True for an unexpired hint of (client, server) under exactly name. */
bool firc_autolist_hinted_name(const firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server,
                               const char *name, int64_t now);

/* True the first time the 5-tuple is seen within FIRC_AUTOLIST_FLOW_SECS of its last sighting. */
bool firc_autolist_flow_first(firc_autolist_t *a, const firc_ip_t *client, uint16_t client_port,
                              const firc_ip_t *server, uint8_t proto, uint16_t port, int64_t now);

/* Counts a connection that went past firc: the failed row of the key if live, else the bypass quota. */
void firc_autolist_bypass_seen(firc_autolist_t *a, const firc_ip_t *client, const firc_ip_t *server, uint8_t proto,
                               uint16_t port, const char *name, const char group[FIRC_ID_STR_LEN], uint8_t how,
                               int64_t now);

/* Visible rows of both quotas, newest last_at first; out may be NULL to count. */
size_t firc_autolist_addresses(const firc_autolist_t *a, int64_t now, firc_autolist_addr_t *out, size_t cap);

/* Hides the row for a day; false when no visible row has the key. */
bool firc_autolist_addr_dismiss(firc_autolist_t *a, const firc_ip_t *server, uint8_t proto, uint16_t port,
                                int64_t now);

uint32_t firc_autolist_failures_day(const firc_autolist_t *a, int64_t now);

uint32_t firc_autolist_ambiguous_day(const firc_autolist_t *a, int64_t now);

#endif
