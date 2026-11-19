#ifndef FIRC_SYSTEM_H
#define FIRC_SYSTEM_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/app.h"
#include "firc/dnsstat.h"
#include "firc/httpd.h"
#include "firc/ifaddr.h"
#include "firc/tunnels.h"

typedef struct firc_update firc_update_t;

typedef struct firc_system_ctx {
    void *policies; /* Keenetic policy resolver; NULL before main starts it, empty before its first read */
    void *resolvers; /* firmware per-interface resolvers; NULL before main starts it, empty before its first read */
    firc_app_t *app;
    const char *config_path; /* NULL makes a save a no-op write (still 200) */
    const char *config_version;
    const char *init_script; /* NULL is FIRC_INIT_SCRIPT; a test gives a stand-in */
    int64_t restart_started_ms; /* monotonic ms a restart was asked for, 0 if none; loop thread only */
    int64_t restart_timeout_ms; /* 0 takes FIRC_RESTART_TIMEOUT_MS; a test shortens it */
    uint16_t web_port; /* the WebUI's listening port, 0 if none */
    uint16_t web_moved_from; /* the port it was configured on when that one was busy, else 0 */
    const firc_tunnels_t *tunnels; /* the running tunnels, read on the loop thread; NULL if none */
    const struct firc_dnsproxy *dnsproxy; /* its listening state for the settings answer; NULL: not reported */
    firc_update_t *update; /* the updater, whose install refuses a restart; NULL if none */
    struct ifaces_read *ifaces_read; /* the interface read in flight, NULL if none; loop thread only */
    struct fresh_waiter *fresh_waiters; /* hosts and policies answers waiting for a refresh; loop thread only */
    void (*iface_event)(void *ud, const char *iface); /* an NDM layer event on iface; NULL ignores them */
    void *iface_event_ud;
    firc_dnsstat_t *dnsstat; /* the day's DNS counts, owned, loop thread only; NULL while the stats are off */
    int64_t dnsstat_since_mono; /* monotonic seconds the counts started */
    struct dnsstat_scratch *dnsstat_scratch; /* the stats route's, owned, freed with the store */
} firc_system_ctx_t;

void firc_system_register_routes(firc_httpd_t *h, firc_system_ctx_t *ctx);

/* adds segment (the net holding a host's ip, when one does) and intercepted to every host object; intercepted is
 * false only when remap_on and the segment is not in link */
void firc_system_hosts_mark(struct cJSON *hosts, const firc_ifnet4_t *nets, size_t n, char *const *link,
                            size_t n_link, bool remap_on);

/* hosts_mark against the running settings, their LAN nets first; NULL running marks every host intercepted */
void firc_system_hosts_mark_running(struct cJSON *hosts, firc_ifnet4_t *nets, size_t n,
                                    const firc_app_config_t *running);

/* the DNS stats on or off as the running settings say, and switched by every live apply after; loop thread.
 * FIRC_ERR_NOMEM: no store, the stats stay off */
firc_err_t firc_system_dnsstats_start(firc_system_ctx_t *ctx);

/* waits out an interface read in flight and drops its callers and those waiting for a policy refresh, frees the
 * DNS stats; after the loop stops, before the servers go */
void firc_system_close(firc_system_ctx_t *ctx);

/* names each tunnel device in ifaces by its tunnel's description, over any alias; ts NULL changes nothing */
void firc_system_name_tunnels(firc_iface_info_t *ifaces, size_t n, const firc_tunnels_t *ts);

#define FIRC_RESTART_TIMEOUT_MS 90000

/* listens h on the running WebUI address at the first free of ports (ports[0] is the configured one); a move
 * is saved to firc.conf; sets ctx->web_port and ctx->web_moved_from; logs the outcome */
firc_err_t firc_system_listen_web(firc_system_ctx_t *ctx, firc_httpd_t *h, const uint16_t *ports,
                                  size_t n);

void firc_settings_register_routes(firc_httpd_t *h, firc_system_ctx_t *ctx);

/* a POST /system/restart was answered and has not stopped this daemon within its timeout; loop thread */
bool firc_system_restart_under_way(firc_system_ctx_t *ctx);

/* whether next's listeners could be bound now (one matching running's is skipped); FIRC_ERR_INVAL sets *field/why */
firc_err_t firc_settings_probe_listeners(const firc_app_config_t *next,
                                         const firc_app_config_t *running, const char **field,
                                         char *why, size_t why_cap);

#endif /* FIRC_SYSTEM_H */
