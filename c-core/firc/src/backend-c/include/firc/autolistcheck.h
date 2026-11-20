#ifndef FIRC_AUTOLISTCHECK_H
#define FIRC_AUTOLISTCHECK_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/autolist.h"
#include "firc/id.h"
#include "firc/loop.h"
#include "firc/sub_fetch.h"

typedef enum {
    FIRC_AUTOLIST_UNKNOWN = 0,
    FIRC_AUTOLIST_REACHABLE,
    FIRC_AUTOLIST_BLOCKED,
} firc_autolist_verdict_t;

typedef struct firc_autolist_result {
    uint8_t verdict;
    uint8_t why;
    bool short_body;
    uint32_t bytes;
    bool wan_unreachable;
    char error[256];
} firc_autolist_result_t;

typedef struct firc_autolist_probe {
    firc_id_t group;
    firc_id_t rule;
    char host[FIRC_AUTOLIST_NAME_MAX];
    firc_autolist_result_t result;
} firc_autolist_probe_t;

typedef struct firc_autolist_check_opts {
    char iface[16];
    char dns[64];
    uint16_t dns_port;
    uint32_t timeout_s;
    char wan_url[513];
    bool wan_expects_204;
    firc_autolist_net_t pools[FIRC_AUTOLIST_POOLS];
    size_t n_pools;
} firc_autolist_check_opts_t;

typedef void (*firc_autolist_probe_fn)(const char *host, const firc_autolist_check_opts_t *o, _Atomic bool *stop,
                                      firc_autolist_result_t *out);

typedef void (*firc_autolist_checked_fn)(void *ud, uint64_t gen, const firc_autolist_probe_t *probes, size_t n);

typedef struct firc_autolist_check firc_autolist_check_t;

/* Judges one probe from how far it got; why is a firc_al_why_t, 0 unless blocked. wan_ok says the WAN check
 * passed; only a timed-out connect with it is blocked. Pure. */
void firc_autolist_classify(const firc_sub_fetch_info_t *info, uint32_t bytes, bool stalled, bool complete,
                            bool wan_ok, firc_autolist_result_t *out);

/* A 5 s GET of o->wan_url through o->iface and o->dns: 204 only when wan_expects_204, else any HTTP status.
 * An empty wan_url is false. *unreachable is set when the network has no route; said gets "wan <host>: <outcome>". */
bool firc_autolist_wan_check(const firc_autolist_check_opts_t *o, _Atomic bool *stop, bool *unreachable, char *said,
                             size_t said_cap);

/* Fetches https://host/ through o and judges it into out. */
void firc_autolist_probe_once(const char *host, const firc_autolist_check_opts_t *o, _Atomic bool *stop,
                              firc_autolist_result_t *out);

firc_autolist_check_t *firc_autolist_check_start(firc_loop_t *loop, const firc_autolist_check_opts_t *o,
                                                 const firc_autolist_probe_t *probes, size_t n, uint64_t gen,
                                                 firc_autolist_probe_fn probe, firc_autolist_checked_fn done,
                                                 void *ud);

void firc_autolist_check_free(firc_autolist_check_t *c);

typedef firc_err_t (*firc_autolist_post_fn)(firc_loop_t *loop, firc_post_cb cb, void *ud, firc_post_drop_cb drop);

/* NULL restores firc_loop_post_with_drop. */
void firc_autolist_check_set_post_for_test(firc_autolist_post_fn post);

#endif
