#ifndef FIRC_AUTOLISTSVC_H
#define FIRC_AUTOLISTSVC_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/autolist.h"
#include "firc/err.h"
#include "firc/loop.h"
#include "firc/nflogsock.h"

typedef struct firc_autolist_svc firc_autolist_svc_t;

typedef struct firc_autolist_deps {
    firc_loop_t *loop;
    firc_nflog_t *(*nflog_open)(uint16_t group, uint16_t copy_range);
    int64_t (*now)(void);
    bool (*eligible)(void *ud, const firc_ip_t *client);
    void (*listed)(void *ud, const firc_autolist_failure_t *f);
    void (*bypassed)(void *ud, const firc_autolist_failure_t *f);
    void (*nominated)(void *ud, const firc_autolist_nomination_t *n);
    void (*flow)(void *ud, const firc_ip_t *client, const firc_ip_t *server, uint8_t proto, uint16_t port,
                 const char *sni);
    void *ud;
    firc_autolist_net_t pools[FIRC_AUTOLIST_POOLS];
    size_t n_pools;
} firc_autolist_deps_t;

firc_autolist_svc_t *firc_autolist_svc_start(const firc_autolist_deps_t *deps, firc_err_t *err);
void firc_autolist_svc_stop(firc_autolist_svc_t *s);

void firc_autolist_svc_set_params(firc_autolist_svc_t *s, const firc_autolist_params_t *p);

void firc_autolist_svc_hint(firc_autolist_svc_t *s, const firc_ip_t *client, const char *name,
                            const firc_ip_t *reals, size_t n_reals);

void firc_autolist_svc_packet(firc_autolist_svc_t *s, int which, const uint8_t *pkt, size_t len);

firc_err_t firc_autolist_svc_drain(firc_autolist_svc_t *s, int which);

void firc_autolist_svc_counts(const firc_autolist_svc_t *s, uint64_t *failures, uint64_t *listed);

size_t firc_autolist_svc_addresses(const firc_autolist_svc_t *s, firc_autolist_addr_t *out, size_t cap);

bool firc_autolist_svc_addr_dismiss(firc_autolist_svc_t *s, const firc_ip_t *server, uint8_t proto, uint16_t port);

void firc_autolist_svc_bypass_seen(firc_autolist_svc_t *s, const firc_ip_t *client, const firc_ip_t *server,
                                   uint8_t proto, uint16_t port, const char *name,
                                   const char group[FIRC_ID_STR_LEN], uint8_t how);

bool firc_autolist_svc_hinted(const firc_autolist_svc_t *s, const firc_ip_t *client, const firc_ip_t *server);

bool firc_autolist_svc_hinted_name(const firc_autolist_svc_t *s, const firc_ip_t *client, const firc_ip_t *server,
                                   const char *name);

uint32_t firc_autolist_svc_failures_day(const firc_autolist_svc_t *s);

uint32_t firc_autolist_svc_ambiguous_day(const firc_autolist_svc_t *s);

#endif
