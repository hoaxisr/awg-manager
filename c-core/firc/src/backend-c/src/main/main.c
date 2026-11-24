#include <arpa/inet.h>
#include <cjson/cJSON.h>
#include <errno.h>
#include <signal.h>
#include <sys/stat.h>
#include <unistd.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/app.h"
#include "firc/auth.h"
#include <linux/rtnetlink.h>

#include "firc/fakeip_from_config.h"
#include "firc/kmod.h"
#include "firc/instance_lock.h"
#include "firc/conntrack.h"
#include "firc/mark.h"
#include "firc/stale_marks.h"
#include "firc/stable_fields.h"
#include "firc/subnet.h"
#include "firc/pool_reject.h"
#include "firc/purge.h"
#include "firc/rci_token.h"
#include "firc/resolve_check.h"
#include "firc/devices.h"
#include "firc/keenetic_policy.h"
#include "firc/inbound_check.h"
#include "firc/keenetic_rci.h"
#include "firc/keenetic_resolvers.h"
#include "firc/resolveroute.h"
#include "firc/router_id.h"
#include "firc/dnspipeline.h"
#include "firc/dnsproxy.h"
#include "firc/groups.h"
#include "firc/httpd.h"
#include "firc/ifcheck.h"
#include "firc/ifwatch.h"
#include "firc/iptables.h"
#include "firc/xtables.h"
#include "firc/events.h"
#include "firc/log.h"
#include "firc/listen.h"
#include "firc/loop.h"
#include "firc/netfilter_cleaner.h"
#include "firc/netlink_watcher.h"
#include "firc/opkgtun.h"
#include "firc/settings.h"

/* Folds a reconnect's burst into one pass; short enough that a gateway change lands before a held answer expires. */
#define FIRC_IFACE_REFRESH_DELAY_MS 200
#include "firc/paths.h"
#include "firc/port_remap.h"
#include "firc/rtnl.h"
#include "firc/rulesnap.h"
#include "firc/ruleset.h"
#include "firc/staticfiles.h"
#include "firc/sub_fetch.h"
#include "firc/subhdr.h"
#include "firc/capture.h"
#include "firc/system.h"
#include "firc/tunnels_api.h"
#include "firc/tunrun.h"
#include "firc/tunsubs.h"
#include "firc/update.h"
#include "firc/update_api.h"
#include "firc/version.h"
#include "firc/yamlio.h"

#define FIRC_POOL_STATE_PATH FIRC_APP_RUN_DIR "/pool.state"
#define FIRC_INSTANCE_LOCK_PATH FIRC_APP_RUN_DIR "/fircd.lock"
#define FIRC_DNS_RETRY_MS 5000u
/* every 10 sweep ticks (30 s each) when dirty */
#define FIRC_POOL_SAVE_EVERY_TICKS 10

#define FIRC_POOL_SWEEP_INTERVAL_MS 30000
#define FIRC_LIST_AUTO_UPDATE_INTERVAL_MS 60000
#define FIRC_UPDATE_FIRST_CHECK_MS 600000
#define FIRC_UPDATE_CHECK_INTERVAL_MS 3600000

struct daemon {
    firc_loop_t *loop;
    firc_dnsproxy_t *proxy;
    firc_logrep_t pool_said;
    firc_dns_pipeline_t *pipeline;
    firc_kn_policies_t *policies;
    firc_kn_resolvers_t *resolvers;
    firc_resolve_router_t *router;
    unsigned sweep_ticks;
    bool pool_file_ok;
    /* Set only after a successful load: an empty pool cannot tell a gone group from one never known. */
    bool pool_state_trusted;
    bool started;
    int lock_fd;
    size_t pool_restored;
    firc_ipt_t *ipt4;
    firc_ipt_t *ipt6;
    firc_rtnl_t *rtnl;
    firc_fakeip_t *pool;
    firc_nl_watcher_t *watcher;
    firc_ct_t *ct;
    firc_setnl_t *sets;
    firc_stable_fields_t fields;
    /* Debounced interface refresh; more than 8 distinct names falls back to refreshing every group. */
    char refresh_ifaces[8][IF_NAMESIZE];
    size_t n_refresh_ifaces;
    bool refresh_all;
    int refresh_timer;
    int dns_retry_timer;
    firc_port_remap_t *port_remap;
    firc_app_t *app;
    firc_tunrun_t *tunrun;
    firc_opkgtun_t *opkg;
    firc_opkgtun_rci_t opkg_rci;
    firc_tunsubs_t *tunsubs;
    firc_update_t *update;
    int update_timer;
    firc_router_id_retry_t *rid_retry;
    int rid_timer;
    int cf_timer;
    firc_ifcheck_t *ifcheck;
    firc_ifwatch_t *ifwatch;
    firc_httpd_t *http_tcp;
    firc_httpd_t *http_unix;

    /* Not owned by daemon_teardown: borrowed so the SIGHUP handler can reach them. */
    firc_config_t *cfg;
    const char *config_path;
    /* Not owned: open list-sync streams hang off it and teardown ends them before the servers go. */
    firc_groups_ctx_t *groups_ctx;
    firc_system_ctx_t *system_ctx;
    firc_tunnels_ctx_t *tunnels_ctx;
    firc_update_api_ctx_t *update_ctx;
};

static int64_t now_unix(void)
{
    return (int64_t)time(NULL);
}

/* Every step is NULL-safe and idempotent: this runs from any partially built state. */
static void save_pool(struct daemon *d, bool clean);

static int mask_prefix(const uint8_t *m, size_t len)
{
    int bits = 0;
    bool seen_zero = false;
    for (size_t i = 0; i < len; i++) {
        for (int b = 7; b >= 0; b--) {
            bool one = ((unsigned)m[i] >> b) & 1u;
            if (one && seen_zero) { return -1; }
            if (one) { bits++; } else { seen_zero = true; }
        }
    }
    return bits;
}

/* A real interface inside the pool would put real hosts in the fake range: refused at startup. */
static bool pool_overlaps_an_interface(const firc_fakeip_t *pool, char *iface, size_t cap)
{
    struct ifaddrs *list = NULL;
    if (getifaddrs(&list) != 0) { return false; }
    bool hit = false;
    for (struct ifaddrs *a = list; a != NULL && !hit; a = a->ifa_next) {
        if (a->ifa_addr == NULL || a->ifa_netmask == NULL) { continue; }
        firc_ip_t addr;
        int prefix;
        if (a->ifa_addr->sa_family == AF_INET) {
            struct sockaddr_in sa, mask;
            memcpy(&sa, a->ifa_addr, sizeof(sa));
            memcpy(&mask, a->ifa_netmask, sizeof(mask));
            memcpy(addr.b, &sa.sin_addr, 4);
            addr.len = 4;
            prefix = mask_prefix((const uint8_t *)&mask.sin_addr, 4);
        } else if (a->ifa_addr->sa_family == AF_INET6) {
            struct sockaddr_in6 sa, mask;
            memcpy(&sa, a->ifa_addr, sizeof(sa));
            memcpy(&mask, a->ifa_netmask, sizeof(mask));
            memcpy(addr.b, &sa.sin6_addr, 16);
            addr.len = 16;
            prefix = mask_prefix((const uint8_t *)&mask.sin6_addr, 16);
        } else {
            continue;
        }
        if (prefix < 0) { continue; }
        if (firc_fakeip_overlaps(pool, &addr, (uint8_t)prefix)) {
            snprintf(iface, cap, "%s", a->ifa_name != NULL ? a->ifa_name : "?");
            hit = true;
        }
    }
    freeifaddrs(list);
    return hit;
}

static bool tunnel_down(void *ud, const char *iface)
{
    return firc_tunrun_device_down(ud, iface);
}

static void ktcp_tunnels(const uint16_t ports[FIRC_KTCP_DEVICES],
                         const char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP], void *ud)
{
    firc_app_set_tunnels(ud, ports, names);
}

static const firc_ktcp_image_t *ktcp_image(void *ud)
{
    return firc_app_ktcp_image(ud);
}

static bool ktcp_real_of(uint8_t dev, const uint8_t fake[4], firc_ip_t *real, void *ud)
{
    return firc_app_ktcp_real_of(ud, dev, fake, real);
}

static void ktcp_pushed(const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next, void *ud)
{
    firc_tunrun_map_pushed(ud, prev, next);
}

static void ktcp_closed(uint8_t dev, void *ud)
{
    firc_app_ktcp_closed(ud, dev);
}

static void ktcp_ready(uint8_t dev, void *ud)
{
    firc_app_ktcp_ready(ud, dev);
}

static int ktcp_close(uint8_t dev, const firc_nf_prefix_t *p, size_t n, void *ud)
{
    return firc_tunrun_close(ud, dev, p, n);
}

static size_t iw_n_groups(void *ud) { return firc_app_user_group_count(((struct daemon *)ud)->app); }

static const firc_group_t *iw_group_at(void *ud, size_t i) {
    return firc_ruleset_group(firc_app_user_group_at(((struct daemon *)ud)->app, i));
}

static firc_err_t iw_move(void *ud, firc_id_t id, size_t to, bool blocked) {
    return firc_app_move_group(((struct daemon *)ud)->app, id, to, blocked);
}

static bool iw_link_up(void *ud, const char *name) {
    firc_link_info_t li;
    bool found = false;
    return firc_rtnl_link_by_name(((struct daemon *)ud)->rtnl, name, &li, &found) == FIRC_OK && found && li.up;
}

static bool iw_tunnel_known(void *ud, const char *dev) {
    struct daemon *d = ud;
    return d->tunrun != NULL && firc_tunnels_by_device(firc_tunrun_config(d->tunrun), dev) != NULL;
}

static bool iw_tunnel_down(void *ud, const char *dev) {
    struct daemon *d = ud;
    return d->tunrun != NULL && firc_tunrun_device_down(d->tunrun, dev);
}

static bool iw_tunnel_starting(void *ud, const char *dev, int64_t *since_ms) {
    struct daemon *d = ud;
    return d->tunrun != NULL && firc_tunrun_device_starting(d->tunrun, dev, since_ms);
}

static firc_err_t iw_queue(void *ud, const char *name, uint32_t mark, uint32_t gen) {
    struct daemon *d = ud;
    return d->ifcheck != NULL ? firc_ifcheck_queue(d->ifcheck, name, mark, gen) : FIRC_ERR_STATE;
}

static firc_err_t iw_queue_group(void *ud, const firc_ifcheck_group_job_t *job) {
    struct daemon *d = ud;
    return d->ifcheck != NULL ? firc_ifcheck_queue_group(d->ifcheck, job) : FIRC_ERR_STATE;
}

static bool iw_resolve_for(void *ud, const firc_group_t *g, const char *iface, bool tunnel,
                           char *server, size_t cap, uint16_t *port, bool *mark_dns) {
    struct daemon *d = ud;
    if (d == NULL || server == NULL || cap == 0 || port == NULL || mark_dns == NULL) { return false; }
    *mark_dns = false;
    *port = 0;
    server[0] = '\0';
    if (tunnel) {
        if (g != NULL && g->resolve.server != NULL && g->resolve.server[0] != '\0') {
            firc_resolver_list_t list;
            if (firc_resolver_list_parse(g->resolve.server, &list, NULL, 0) == FIRC_RESOLVER_LIST_OK && list.n > 0) {
                const firc_resolver_addr_t *a = &list.servers[0];
                if (inet_ntop(a->ip.len == 4 ? AF_INET : AF_INET6, a->ip.b, server, (socklen_t)cap) != NULL) {
                    *port = a->port != 0 ? a->port : FIRC_RESOLVER_DEFAULT_PORT;
                    *mark_dns = g->resolve.tunnel && !firc_resolver_ip_is_sink(&a->ip);
                    return true;
                }
            }
        }
        snprintf(server, cap, "1.1.1.1");
        *port = 53;
        *mark_dns = true;
        return true;
    }
    if (d->resolvers != NULL && iface != NULL && iface[0] != '\0') {
        firc_resolver_addr_t out[FIRC_RESOLVE_MAX_SERVERS];
        size_t n = firc_kn_resolvers_for(d->resolvers, iface, out, FIRC_RESOLVE_MAX_SERVERS);
        if (n > 0 && inet_ntop(out[0].ip.len == 4 ? AF_INET : AF_INET6, out[0].ip.b, server, (socklen_t)cap) != NULL) {
            *port = out[0].port != 0 ? out[0].port : FIRC_RESOLVER_DEFAULT_PORT;
            *mark_dns = !firc_resolver_ip_is_sink(&out[0].ip);
            return true;
        }
    }
    return false;
}

static void iw_results(void *ud, const firc_ifcheck_result_t *r, size_t n) {
    struct daemon *d = ud;
    if (d->ifwatch != NULL) { firc_ifwatch_results(d->ifwatch, r, n); }
}

static void iw_group_results(void *ud, const firc_ifcheck_group_result_t *r) {
    struct daemon *d = ud;
    if (d->ifwatch != NULL) { firc_ifwatch_group_results(d->ifwatch, r); }
}

static firc_ifchoice_t iw_pick(void *ud, const firc_group_t *g) {
    return firc_ifwatch_pick(((struct daemon *)ud)->ifwatch, g);
}

static bool iw_view(void *ud, const char *name, firc_if_view_t *out) {
    return firc_ifwatch_view(((struct daemon *)ud)->ifwatch, name, out);
}

static bool iw_group_view(void *ud, firc_id_t id, const char *name, firc_if_view_t *out) {
    return firc_ifwatch_group_view(((struct daemon *)ud)->ifwatch, id, name, out);
}

static firc_err_t iw_move_up(void *ud, firc_id_t id) {
    return firc_ifwatch_move_up(((struct daemon *)ud)->ifwatch, id);
}

static void iw_settings(void *ud, const firc_app_config_t *run) {
    struct daemon *d = ud;
    firc_ifwatch_configure(d->ifwatch, run);
    firc_ifcheck_settings_t s;
    firc_ifcheck_settings_from(run, &s);
    firc_ifcheck_configure(d->ifcheck, &s);
}

static void daemon_teardown(struct daemon *d)
{
    firc_update_cancel(d->update);
    firc_app_set_ktcp_sink(d->app, NULL);
    if (d->tunrun != NULL) { firc_tunrun_set_ktcp(d->tunrun, NULL); }
    firc_app_set_ifaces(d->app, NULL);
    firc_ifcheck_free(d->ifcheck);
    d->ifcheck = NULL;
    firc_ifwatch_free(d->ifwatch);
    d->ifwatch = NULL;
    firc_tunnels_api_close(d->tunnels_ctx);
    firc_app_set_tunnel_down(d->app, NULL, NULL);
    if (d->tunrun != NULL) { firc_tunrun_set_subs(d->tunrun, NULL); }
    if (d->rid_timer != 0 && d->loop != NULL) {
        (void)firc_loop_del_timer(d->loop, d->rid_timer);
        d->rid_timer = 0;
    }
    firc_router_id_retry_stop(d->rid_retry);
    d->rid_retry = NULL;
    firc_tunsubs_free(d->tunsubs);
    d->tunsubs = NULL;
    if (d->tunrun != NULL) { firc_tunrun_set_router(d->tunrun, NULL); }
    firc_opkgtun_stop(d->opkg);
    d->opkg = NULL;
    firc_tunrun_stop(d->tunrun, 5000);
    firc_tunrun_free(d->tunrun);
    d->tunrun = NULL;
    if (d->cf_timer != 0 && d->loop != NULL) {
        (void)firc_loop_del_timer(d->loop, d->cf_timer);
        d->cf_timer = 0;
    }
    if (d->dns_retry_timer != 0 && d->loop != NULL) {
        (void)firc_loop_del_timer(d->loop, d->dns_retry_timer);
        d->dns_retry_timer = 0;
    }
    /* Ordering only: delete before anything its callback reads is freed. */
    if (d->refresh_timer != 0 && d->loop != NULL) {
        (void)firc_loop_del_timer(d->loop, d->refresh_timer);
        d->refresh_timer = 0;
    }
    /* Committer first: it drives the iptables engines from its own thread; stopping it aborts a write in flight. */
    firc_app_stop_netfilter_committer(d->app);
    if (d->update_timer != 0 && d->loop != NULL) {
        (void)firc_loop_del_timer(d->loop, d->update_timer);
        d->update_timer = 0;
    }
    firc_update_api_close(d->update_ctx);
    firc_update_free(d->update);
    d->update = NULL;
    /* List worker next: it posts results onto a loop that is about to go. */
    firc_app_stop_list_worker(d->app);

    /* After the worker has joined, before the servers go (httpd_destroy would drop the streams silently). */
    firc_groups_api_close_streams(d->groups_ctx);
    if (d->system_ctx != NULL) { firc_system_close(d->system_ctx); }

    firc_httpd_destroy(d->http_tcp);
    d->http_tcp = NULL;
    firc_httpd_destroy(d->http_unix);
    d->http_unix = NULL;

    firc_app_destroy(d->app);
    d->app = NULL;
    firc_setnl_free(d->sets);
    d->sets = NULL;

    /* Pool-reject routes stay on a clean stop: clients keep fake answers up to the TTL clamp; a restart adopts them. */
    /* Saved as exact only if the loop ran: a failed startup has nothing truer than the file. */
    if (d->started) { save_pool(d, true); }
    firc_fakeip_free(d->pool);
    d->pool = NULL;

    if (d->port_remap) {
        firc_port_remap_disable(d->port_remap);
        firc_port_remap_free(d->port_remap);
        d->port_remap = NULL;
    }

    firc_nl_watcher_destroy(d->watcher);
    d->watcher = NULL;
    firc_ct_close(d->ct);
    d->ct = NULL;
    firc_stable_fields_release(&d->fields);
    firc_dnsproxy_destroy(d->proxy);
    d->proxy = NULL;
    firc_resolve_router_free(d->router);
    d->router = NULL;
    firc_dns_pipeline_destroy(d->pipeline);
    d->pipeline = NULL;
    firc_kn_policies_stop(d->policies);
    d->policies = NULL;
    firc_kn_resolvers_stop(d->resolvers);
    d->resolvers = NULL;
    firc_ipt_free(d->ipt4);
    d->ipt4 = NULL;
    firc_ipt_free(d->ipt6);
    d->ipt6 = NULL;
    firc_rtnl_close(d->rtnl);
    d->rtnl = NULL;
    firc_loop_destroy(d->loop);
    d->loop = NULL;
    /* last: the pool file is written and the chains are gone */
    firc_instance_unlock(d->lock_fd);
    d->lock_fd = -1;
}

#ifndef FIRC_ENTWARE_KN
/* Non-_kn builds cannot expand policy names: such entries match nobody, and the operator is told. */
static void warn_policy_entries_of(const char *kind, const char *name, const firc_devsel_spec_t *ds)
{
    for (size_t k = 0; k < ds->n_allow + ds->n_deny; k++) {
        const char *e = k < ds->n_allow ? ds->allow[k] : ds->deny[k - ds->n_allow];
        if (strncmp(e, FIRC_DEVSEL_POLICY_PREFIX, strlen(FIRC_DEVSEL_POLICY_PREFIX)) == 0) {
            FIRC_WARN("%s %s names %s: this build has no Keenetic policy resolver, it matches no device",
                      kind, name ? name : "?", e);
        }
    }
}

static void warn_policy_entries(const firc_config_t *cfg)
{
    for (size_t i = 0; i < cfg->n_groups; i++) {
        warn_policy_entries_of("group", cfg->groups[i]->name, &cfg->groups[i]->devices);
    }
}
#endif

/* Settings first, groups second: groups.yaml, read last, decides the final group set. */
/* A loader refusal says only FIRC_ERR_INVAL: re-run the config check to name the field. */
static void explain_refusal(const firc_config_t *cfg, const char *path)
{
    const char *field = NULL, *why = NULL;
    if (firc_app_config_check(&cfg->app, &field, &why) != FIRC_OK) {
        FIRC_ERROR("%s: %s: %s", path, field, why);
    }
}

/* out_failed is copied, not pointed at: the groups path is a local here. */
static firc_err_t load_config_pair(firc_config_t *cfg, const char *conf_path, bool quiet,
                                   char *out_failed, size_t out_failed_cap)
{
    if (out_failed != NULL) { snprintf(out_failed, out_failed_cap, "%s", conf_path); }

    firc_err_t err = firc_config_load_file(cfg, conf_path);
    if (err == FIRC_ERR_NOENT) {
        /* Logged on reload too: a missing firc.conf resets every setting to its default. */
        FIRC_WARN("settings file %s not found; running with the defaults", conf_path);
    } else if (err != FIRC_OK) {
        return err;
    }

    char groups_path[4096];
    firc_err_t perr = firc_config_groups_path(conf_path, groups_path, sizeof(groups_path));
    if (perr != FIRC_OK) {
        FIRC_ERROR("cannot name the groups file next to %s", conf_path);
        return perr;
    }
    /* Names the file that failed, not always firc.conf. */
    if (out_failed != NULL) { snprintf(out_failed, out_failed_cap, "%s", groups_path); }
    firc_err_t gerr = firc_config_load_file(cfg, groups_path);
    if (gerr == FIRC_ERR_NOENT) {
        if (!quiet) {
            FIRC_INFO("no groups file at %s; %zu group(s) from %s", groups_path, cfg->n_groups,
                      conf_path);
        }
        if (out_failed != NULL) { snprintf(out_failed, out_failed_cap, "%s", conf_path); }
        return err == FIRC_ERR_NOENT ? FIRC_ERR_NOENT : FIRC_OK;
    }
    return gerr;
}

static void reload_config(struct daemon *d)
{
    firc_config_t reload_cfg;
    if (firc_config_init_defaults(&reload_cfg) != FIRC_OK) {
        FIRC_ERROR("failed to reload config: out of memory");
        return;
    }
    char failed[4096];
    firc_err_t err = load_config_pair(&reload_cfg, d->config_path, true, failed, sizeof(failed));
    if (err == FIRC_ERR_NOENT) {
        FIRC_INFO("reload: no config at %s, keeping current config",
                d->config_path);
        firc_config_clear(&reload_cfg);
        return;
    }
    if (err != FIRC_OK) {
        explain_refusal(&reload_cfg, failed);
        FIRC_ERROR("failed to reload config %s: %s", failed, firc_err_str(err));
        firc_config_clear(&reload_cfg);
        return;
    }

    if (reload_cfg.groups_present) {
        /* Checked before anything is applied: a resolver inside the pool refuses the whole reload. */
        char hit[FIRC_RESOLVER_ADDR_STRLEN];
        const firc_group_t *bad = d->pool != NULL
            ? firc_resolve_groups_in_pool(reload_cfg.groups, reload_cfg.n_groups, d->pool, hit, sizeof(hit)) : NULL;
        if (bad != NULL) {
            FIRC_ERROR("reload: group \"%s\": resolve.server \"%s\" is inside firc's address pool; "
                       "the reload was refused",
                       bad->name != NULL ? bad->name : "", hit);
            firc_config_clear(&reload_cfg);
            return;
        }
    }

    if (d->app != NULL) {
        /* Live settings go through the same apply as a settings PUT; the rest apply at the next start. */
        firc_err_t serr = firc_app_reload_settings(d->app, &reload_cfg.app, NULL);
        if (serr != FIRC_OK) {
            FIRC_ERROR("reload: settings not (all) applied: %s", firc_err_str(serr));
        }
    }

    if (reload_cfg.groups_present) {
        firc_group_t **groups = reload_cfg.groups;
        size_t n_groups = reload_cfg.n_groups;
#ifndef FIRC_ENTWARE_KN
        warn_policy_entries(&reload_cfg);
#endif
        reload_cfg.groups = NULL;
        reload_cfg.n_groups = 0;
        firc_err_t gerr = firc_app_replace_groups(d->app, groups, n_groups);
        if (gerr != FIRC_OK) { FIRC_ERROR("failed to reload groups: %s", firc_err_str(gerr)); }
    }

    firc_config_clear(&reload_cfg);
    FIRC_INFO("config reloaded from %s", d->config_path);
}

static void apply_identity(firc_tunsubs_t *subs, const firc_router_id_t *id)
{
    firc_kn_token_set_release(id->release);
    firc_subhdr_set_t defaults;
    firc_subhdr_defaults(id->hwid, id->release, id->model, &defaults);
    firc_tunsubs_set_defaults(subs, &defaults);
}

#ifdef FIRC_ENTWARE_KN
static firc_err_t rci_identity_get(void *ud, const char *path, char **body)
{
    (void)ud;
    long timeout_s = strcmp(path, "/rci/show/defaults") == 0 ? FIRC_KN_RCI_SERIAL_TIMEOUT_SECONDS
                                                              : FIRC_KN_RCI_TIMEOUT_SECONDS;
    return firc_kn_rci_get_timeout(FIRC_KN_RCI_BASE_URL, path, timeout_s, NULL, body);
}

static firc_err_t revoke_rci_token(void *ud)
{
    (void)ud;
    char *body = NULL;
    char release[64] = "";
    if (firc_kn_rci_get(FIRC_KN_RCI_BASE_URL, "/rci/show/version", &body) == FIRC_OK) {
        cJSON *v = cJSON_Parse(body);
        const cJSON *r = cJSON_GetObjectItemCaseSensitive(v, "release");
        if (cJSON_IsString(r)) { snprintf(release, sizeof release, "%s", r->valuestring); }
        cJSON_Delete(v);
    }
    free(body);
    firc_err_t err = firc_kn_token_revoke(FIRC_RCI_TOKEN_PATH, FIRC_NDMC_BIN, release);
    if (err != FIRC_OK) {
        FIRC_WARN("purge: firc's RCI token may be left on the router (%s); a new install deletes it",
                  firc_err_str(err));
    }
    return err;
}

static void on_identity_tick(firc_loop_t *loop, void *ud)
{
    struct daemon *d = ud;
    firc_router_id_t id;
    if (firc_router_id_next(&d->rid_retry, firc_hwid_path(), &id)) {
        apply_identity(d->tunsubs, &id);
    }
    if (d->rid_retry == NULL) {
        (void)firc_loop_del_timer(loop, d->rid_timer);
        d->rid_timer = 0;
    }
}
#endif

static void subscription_defaults(struct daemon *d)
{
    firc_router_id_t id;
#ifdef FIRC_ENTWARE_KN
    d->rid_retry = firc_router_id_begin(rci_identity_get, NULL, 30, 2000, firc_hwid_path(), &id);
    if (d->rid_retry != NULL) {
        FIRC_INFO("router id: asking RCI in the background; subscriptions go without X-HWID until an id exists");
        if (firc_loop_add_timer(d->loop, 1000, 1000, on_identity_tick, d, &d->rid_timer) != FIRC_OK) {
            firc_router_id_retry_stop(d->rid_retry);
            d->rid_retry = NULL;
            firc_router_id_settle(&id, firc_hwid_path(), true);
        }
    }
#else
    (void)firc_router_id_begin(NULL, NULL, 0, 0, firc_hwid_path(), &id);
#endif
    apply_identity(d->tunsubs, &id);
}

static bool load_tunnels(firc_tunnels_t *out, const char *keeping)
{
    firc_tun_err_t terr = {0};
    const char *path = firc_tunnels_path();
    firc_err_t err = firc_tunnels_load_file(out, path, &terr);
    if (err == FIRC_OK) { return true; }
    if (terr.where[0] != 0 || terr.why[0] != 0) {
        FIRC_ERROR("%s: %s: %s; %s", path, terr.where, terr.why, keeping);
    } else {
        FIRC_ERROR("%s: %s; %s", path, firc_err_str(err), keeping);
    }
    return false;
}

static void opkg_want(const firc_opkgtun_want_t *w, size_t n, void *ud)
{
    firc_opkgtun_want(((struct daemon *)ud)->opkg, w, n);
}

static size_t opkg_seen(firc_opkgtun_seen_t *out, size_t cap, uint32_t *swept, void *ud)
{
    struct daemon *d = ud;
    size_t n = firc_opkgtun_seen(d->opkg, out, cap);
    *swept = firc_opkgtun_swept(d->opkg);
    return n;
}

static void opkg_sweep(void *ud)
{
    firc_opkgtun_sweep_now(((struct daemon *)ud)->opkg);
}

static void opkg_trip(const char *name, void *ud)
{
    firc_opkgtun_trip(((struct daemon *)ud)->opkg, name);
}

static void opkg_pass(void *ud)
{
    (void)firc_app_force_commit_iptables(((struct daemon *)ud)->app);
}

static void reload_tunnels(struct daemon *d)
{
    if (d->tunrun == NULL) { return; }
    firc_tunnels_t t = {0};
    if (!load_tunnels(&t, "the running tunnels are kept")) { return; }
    (void)firc_tunrun_apply(d->tunrun, &t);
    firc_tunnels_free(&t);
}

static void on_signal(firc_loop_t *loop, int signo, void *ud)
{
    struct daemon *d = ud;
    if (signo == SIGHUP) {
        FIRC_INFO("received signal: hangup (reloading config)");
        reload_config(d);
        reload_tunnels(d);
        return;
    }
    FIRC_INFO("received signal: %s", strsignal(signo));
    firc_loop_stop(loop);
}

static void save_pool(struct daemon *d, bool clean)
{
    /* never overwrite an unreadable file with an empty pool */
    if (d->pool == NULL || !d->pool_file_ok) { return; }
    /* A removed run dir is not remade for the pool file alone; a restart is the fix. */
    (void)firc_fakeip_save_file_said(d->pool, FIRC_POOL_STATE_PATH, clean, &d->pool_said);
}

static void on_chunk_taken(void *ud, const firc_fakeip_t *f)
{
    (void)f;
    save_pool(ud, false);
}

static void on_dns_retry(firc_loop_t *loop, void *ud)
{
    struct daemon *d = ud;
    char before[192];
    snprintf(before, sizeof(before), "%s", firc_dnsproxy_listen_error(d->proxy));
    if (firc_dnsproxy_start(d->proxy) != FIRC_OK) {
        if (strcmp(before, firc_dnsproxy_listen_error(d->proxy)) != 0) {
            FIRC_ERROR("DNS proxy still cannot listen: %s", firc_dnsproxy_listen_error(d->proxy));
        }
        return;
    }
    (void)firc_loop_del_timer(loop, d->dns_retry_timer);
    d->dns_retry_timer = 0;
    firc_app_set_dns_down(d->app, false);
    FIRC_WARN("DNS proxy has its port now: answering DNS again");
}

static void on_cf_tick(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    firc_app_cf_tick(d->app);
}

static void on_pool_sweep_timer(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    for (size_t i = 0; d->app != NULL && i < firc_app_user_group_count(d->app); i++) {
        firc_ruleset_remind_link(firc_app_user_group_at(d->app, i));
    }
    if (d->pool != NULL && d->app != NULL) {
        uint64_t before = firc_fakeip_gen(d->pool);
        firc_fakeip_reclaim(d->pool, now_unix());
        if (firc_fakeip_gen(d->pool) != before) { firc_app_pool_changed(d->app); }
    }
    /* The file is megabytes at the cap: save at most every five minutes, not every tick. */
    if (++d->sweep_ticks % FIRC_POOL_SAVE_EVERY_TICKS == 0 && firc_fakeip_dirty_since_save(d->pool)) {
        save_pool(d, false);
    }
}

typedef struct {
    const firc_config_t *cfg;
    char (*ids)[FIRC_ID_STR_LEN];
    size_t n, cap;
} unknown_groups_t;

static void collect_unknown_group(void *ud, const char *group_id, unsigned family, const firc_ip_t *base,
                                  uint8_t prefix)
{
    (void)family; (void)base; (void)prefix;
    unknown_groups_t *u = ud;
    for (size_t i = 0; i < u->cfg->n_groups; i++) {
        char id[FIRC_ID_STR_LEN];
        firc_id_format(u->cfg->groups[i]->id, id);
        if (strcmp(id, group_id) == 0) { return; }
    }
    for (size_t i = 0; i < u->n; i++) { if (strcmp(u->ids[i], group_id) == 0) { return; } }
    if (u->n < u->cap) { snprintf(u->ids[u->n++], FIRC_ID_STR_LEN, "%s", group_id); }
}

static void forget_groups_not_in_config(firc_fakeip_t *pool, const firc_config_t *cfg, int64_t now)
{
    char ids[64][FIRC_ID_STR_LEN];
    unknown_groups_t u = {.cfg = cfg, .ids = ids, .n = 0, .cap = 64};
    firc_fakeip_walk_chunks(pool, NULL, collect_unknown_group, &u);
    for (size_t i = 0; i < u.n; i++) {
        FIRC_INFO("pool: group %s is no longer configured, releasing its names", ids[i]);
        firc_fakeip_drop_group(pool, ids[i], now);
    }
    if (u.n == u.cap) { FIRC_WARN("pool: more than %zu groups vanished from the config; the rest stay until they idle out", u.cap); }
}

static int64_t mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static void on_update_timer(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    if (d->update == NULL) { return; }
    firc_update_auto_check(d->update, firc_app_running_settings(d->app)->update.check, mono_ms());
}

static void on_list_auto_update_timer(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    firc_app_request_sync_due(d->app, now_unix());
    firc_tunsubs_due(d->tunsubs);
}

static firc_dns_verdict_t on_response(firc_dns_msg_t *msg, const firc_ip_t *client,
                                      const char *network, firc_dns_resolver_t resolver, void *ud)
{
    (void)network;
    struct daemon *d = ud;
    bool pool_changed = false;
    firc_dns_verdict_t v = firc_dns_pipeline_handle_message_from(d->pipeline, msg, now_unix(), client,
                                                                 resolver, &pool_changed);
    if (pool_changed) { firc_app_pool_changed(d->app); }
    return v;
}

static firc_dns_verdict_t on_cross(firc_dns_msg_t *a_msg, const firc_dns_msg_t *aaaa_msg, const firc_ip_t *client,
                                   firc_dns_resolver_t resolver, void *ud)
{
    struct daemon *d = ud;
    bool pool_changed = false;
    firc_dns_verdict_t v = firc_dns_pipeline_cross_answer(d->pipeline, a_msg, aaaa_msg, now_unix(), client, resolver,
                                                          &pool_changed);
    if (pool_changed) { firc_app_pool_changed(d->app); }
    return v;
}

static void on_blocked(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client, firc_block_answer_t answer)
{
    struct daemon *d = ud;
    firc_dns_pipeline_note_blocked(d->pipeline, query, client, answer);
}

static int64_t mono_s(void)
{
    return mono_ms() / 1000;
}

static void on_tally(void *ud, const firc_dns_msg_t *query, const firc_ip_t *client, firc_id_t owner, bool blocked)
{
    struct daemon *d = ud;
    if (d->system_ctx == NULL || d->system_ctx->dnsstat == NULL) { return; }
    char name[FIRC_DNS_MAX_NAME + 1];
    if (!firc_dns_msg_queried_name(query, name, sizeof(name))) { return; }
    firc_dnsstat_note(d->system_ctx->dnsstat, name, client, owner, blocked, mono_s());
}

static void on_dnsstat_sweep(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    if (d->system_ctx != NULL) { firc_dnsstat_sweep(d->system_ctx->dnsstat, mono_s()); }
}

static void on_iface_event(void *ud, const char *iface)
{
    struct daemon *d = ud;
    if (d->ifwatch != NULL) { firc_ifwatch_nudge(d->ifwatch, iface); }
    firc_tunrun_net_event(d->tunrun, iface);
}

static void resync(struct daemon *d, const char *iface_name)
{
    for (size_t i = 0; i < firc_app_user_group_count(d->app); i++) {
        firc_ruleset_t *rs = firc_app_user_group_at(d->app, i);
        const firc_group_t *g = firc_ruleset_group(rs);
        if (iface_name != NULL && strcmp(firc_ruleset_active_iface(rs), iface_name) != 0) {
            continue;
        }
        /* A connected tunnel socket keeps its old source address after the interface's address moves. */
        if (d->proxy != NULL) { firc_dnsproxy_close_group_pools(d->proxy, g->id); }
        firc_err_t err = firc_ruleset_on_addr_change(rs);
        if (err != FIRC_OK) {
            FIRC_WARN("error while re-reading interface %s: group=%s err=%s", firc_ruleset_active_iface(rs), g->name,
                       firc_err_str(err));
        }
    }
    firc_app_refresh_remap(d->app, iface_name);
    firc_tunrun_refresh_uplinks(d->tunrun, iface_name);
    (void)firc_app_retry_groups(d->app, iface_name);
    if (d->ifwatch != NULL) { firc_ifwatch_refresh(d->ifwatch, iface_name); }
}

static void on_refresh_due(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    d->refresh_timer = 0;
    bool all = d->refresh_all;
    size_t n = d->n_refresh_ifaces;
    char names[8][IF_NAMESIZE];
    memcpy(names, d->refresh_ifaces, sizeof(names));
    d->refresh_all = false;
    d->n_refresh_ifaces = 0;
    if (all) {
        resync(d, NULL);
    } else {
        for (size_t i = 0; i < n; i++) { resync(d, names[i]); }
    }
    firc_tunrun_refresh_inbounds(d->tunrun);
    /* After every interface: a retry's own republish may predate a later interface's refresh. */
    firc_app_republish_resolve_routes(d->app);
}

static void schedule_refresh(struct daemon *d, const char *iface_name)
{
    if (iface_name == NULL) {
        d->refresh_all = true;
    } else {
        bool known = d->refresh_all;
        for (size_t i = 0; i < d->n_refresh_ifaces && !known; i++) {
            if (strcmp(d->refresh_ifaces[i], iface_name) == 0) { known = true; }
        }
        if (!known) {
            if (d->n_refresh_ifaces < sizeof(d->refresh_ifaces) / sizeof(d->refresh_ifaces[0])) {
                snprintf(d->refresh_ifaces[d->n_refresh_ifaces], IF_NAMESIZE, "%s", iface_name);
                d->n_refresh_ifaces++;
            } else {
                d->refresh_all = true;
            }
        }
    }
    if (d->refresh_timer != 0) { return; }
    if (firc_loop_add_timer(d->loop, FIRC_IFACE_REFRESH_DELAY_MS, 0, on_refresh_due, d, &d->refresh_timer) !=
        FIRC_OK) {
        d->refresh_timer = 0;
        on_refresh_due(d->loop, d); /* no timer to be had: now, then */
    }
}

static void on_addr_change(const char *iface_name, void *ud)
{
    struct daemon *d = ud;
    FIRC_DEBUG("interface addressing changed: %s", iface_name);
    firc_tunrun_net_event(d->tunrun, iface_name);
    schedule_refresh(d, iface_name);
}

static void on_watcher_lost(void *ud)
{
    struct daemon *d = ud;
    FIRC_DEBUG("netlink watcher missed events; re-reading every interface");
    firc_tunrun_net_event(d->tunrun, NULL);
    schedule_refresh(d, NULL);
}

#ifdef FIRC_ENTWARE_KN
static void resolvers_changed_on_loop(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    if (d->app != NULL) { firc_app_republish_resolve_routes(d->app); }
}

static void on_resolvers_changed(void *ud)
{
    struct daemon *d = ud;
    if (firc_loop_post(d->loop, resolvers_changed_on_loop, d) != FIRC_OK) {
        FIRC_WARN("keenetic resolvers: the change could not be handed to the loop; the next one will be");
    }
}

/* Refresher's thread: only a post; the loop renders from the map itself. */
static void policies_changed_on_loop(firc_loop_t *loop, void *ud)
{
    (void)loop;
    struct daemon *d = ud;
    if (d->app != NULL) { firc_app_devices_changed(d->app); }
}

/* A failed post is reported; the refresher announces again at its next round. */
static bool on_policies_changed(void *ud)
{
    struct daemon *d = ud;
    if (firc_loop_post(d->loop, policies_changed_on_loop, d) != FIRC_OK) {
        FIRC_WARN("keenetic policies: the changed host table could not be handed to the loop; the next refresh "
                  "tries again");
        return false;
    }
    return true;
}
#endif

static void on_link_up(const char *iface_name, bool up, void *ud)
{
    struct daemon *d = ud;
    FIRC_DEBUG("interface %s: %s", up ? "up" : "down", iface_name);
    if (d->ifwatch != NULL) { firc_ifwatch_link(d->ifwatch, iface_name, up); }
    firc_app_refresh_remap(d->app, iface_name);
    if (!up) {
        firc_tunrun_link_down(d->tunrun, iface_name);
        return;
    }
    for (size_t i = 0; i < firc_app_user_group_count(d->app); i++) {
        firc_ruleset_t *rs = firc_app_user_group_at(d->app, i);
        const firc_group_t *g = firc_ruleset_group(rs);
        if (strcmp(firc_ruleset_active_iface(rs), iface_name) != 0) {
            continue;
        }
        if (d->proxy != NULL) { firc_dnsproxy_close_group_pools(d->proxy, g->id); }
        firc_err_t err = firc_ruleset_on_link_up(rs);
        if (err != FIRC_OK) {
            FIRC_WARN("error while handling interface up: %s group=%s err=%s", firc_ruleset_active_iface(rs),
                     g->name, firc_err_str(err));
        }
    }
    firc_tunrun_refresh_uplinks(d->tunrun, iface_name);
    firc_tunrun_net_event(d->tunrun, iface_name);
    /* An interface coming up is the likeliest reason a failed group's retry now holds. */
    (void)firc_app_retry_groups(d->app, iface_name);
    firc_app_republish_resolve_routes(d->app);
}

static const char *auth_state_dir_fn(void *ud)
{
    (void)ud;
    return FIRC_APP_CONF_DIR;
}

static bool groups_file_exists(const char *config_path) {
    char groups_path[4096];
    if (firc_config_groups_path(config_path, groups_path, sizeof(groups_path)) != FIRC_OK) { return true; }
    return access(groups_path, F_OK) == 0 || errno != ENOENT;
}

static bool field_kept_by_map(void *ud, const firc_group_t *g, const char *id, uint32_t field) {
    (void)g;
    return firc_stable_fields_kept(ud, id, field);
}

static void flush_stale_group_marks(struct daemon *d) {
    if (d->ct == NULL || d->pool == NULL || d->app == NULL) { return; }
    firc_app_stale_groups_t groups;
    if (!firc_app_stale_groups(d->app, field_kept_by_map, &d->fields, 0, &groups)) {
        FIRC_WARN("not enough memory to check for flows marked by a previous run; none were touched");
        return;
    }
    size_t dropped = 0;
    firc_err_t err = firc_stale_marks_sweep(d->ct, d->pool, d->pool_state_trusted, d->fields.loaded, groups.v,
                                            groups.n, FIRC_MARK_GROUP_MASK, &dropped);
    firc_app_stale_groups_free(&groups);
    firc_stable_fields_release(&d->fields);
    /* Count first: a sweep that failed halfway has still deleted flows. */
    if (dropped > 0) {
        FIRC_INFO("dropped %zu flow(s) still carrying a mark from a previous run", dropped);
    }
    if (err != FIRC_OK) {
        FIRC_WARN("the check for flows marked by a previous run did not finish: %s -- any left are "
                  "swept at the next start",
                  firc_err_str(err));
    }
}

static firc_ipt_t *open_engine(firc_ipt_proto_t fam, bool purge, const char *prefix, bool *no_family) {
    *no_family = false;
    const char *name = fam == FIRC_IPT_PROTO_IPV6 ? "ipv6" : "ipv4";
    firc_xt_t *xt = firc_xt_real_new(fam);
    if (!xt) {
        int e = errno;
        *no_family = fam == FIRC_IPT_PROTO_IPV6 && e == EAFNOSUPPORT;
        if (*no_family && purge) {
            FIRC_INFO("purge: this kernel has no IPv6, its tables are skipped");
        } else if (*no_family) {
            FIRC_ERROR("cannot open x_tables for ipv6: this kernel has no IPv6; set netfilter.disableIPv6 to run without it");
        } else {
            FIRC_ERROR("cannot open x_tables for %s: %s", name, strerror(e));
        }
        return NULL;
    }
    if (firc_xt_set_dump_dir(xt, FIRC_APP_RUN_DIR) != FIRC_OK) {
        FIRC_ERROR("netfilter helper init fail: out of memory");
        firc_xt_free(xt);
        return NULL;
    }
    if (!purge) {
        (void)firc_kmod_load_netfilter(NULL);
        (void)firc_xt_probe(xt);
    }
    firc_ipt_t *ipt = firc_ipt_new(xt, prefix);
    if (!ipt) {
        FIRC_ERROR("netfilter helper init fail: out of memory");
        firc_xt_free(xt);
    }
    return ipt;
}

static firc_setnl_t *open_sets(void)
{
    firc_setnl_t *s = NULL;
    firc_err_t err = firc_setnl_new(&s);
    if (err != FIRC_OK) {
        FIRC_WARN("cannot open ip_set netlink (%s): groups with a subnet rule naming no protocol are left out", firc_err_str(err));
        return NULL;
    }
    firc_setnl_caps_t caps;
    err = firc_setnl_probe(s, &caps);
    if (err == FIRC_ERR_NOSYS) {
        FIRC_WARN("this kernel has no ip_set: groups with a subnet rule naming no protocol are left out");
    } else if (err != FIRC_OK) {
        FIRC_WARN("ip_set did not answer its probe (%s): groups with a subnet rule naming no protocol may be left out", firc_err_str(err));
    } else if (!caps.has_type) {
        FIRC_WARN("this kernel has no hash:net set: groups with a subnet rule naming no protocol are left out");
    }
    return s;
}

int main(int argc, char **argv)
{
    /* init pipes stdout into logger: a dead reader must cost a dropped line, not the daemon. */
    signal(SIGPIPE, SIG_IGN);
    sigset_t set;
    sigemptyset(&set);
    sigaddset(&set, SIGINT);
    sigaddset(&set, SIGTERM);
    sigaddset(&set, SIGHUP);
    firc_log_nonblocking();
    /* --purge takes the lock like a start; an unknown argument prints usage and exits 2. */
    bool purge = false;
    const char *config_path = FIRC_CONFIG_PATH;
    firc_log_level_t out_floor = FIRC_LOG_DEBUG;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--version") == 0) {
            puts(FIRC_VERSION_LINE);
            return 0;
        } else if (strcmp(argv[i], "--purge") == 0) {
            purge = true;
        } else if (strcmp(argv[i], "--config") == 0 && i + 1 < argc) {
            config_path = argv[++i];
        } else if (strcmp(argv[i], "--output-floor") == 0 && i + 1 < argc &&
                   firc_log_level_parse(argv[i + 1], &out_floor)) {
            firc_log_set_output_floor(out_floor);
            i++;
        } else {
            fprintf(stderr, "usage: fircd [--config <path>] [--output-floor <level>] [--purge] | "
                            "--version\n");
            return 2;
        }
    }

    if (purge) {
        FIRC_INFO("purging firc's state, version=%s", FIRC_VERSION);
    } else {
        FIRC_INFO("starting firc daemon (C) version=%s", FIRC_VERSION);
    }

    /* libcurl: call once, before any thread performs a transfer. */
    firc_sub_fetch_global_init();
#ifdef FIRC_ENTWARE_KN
    firc_kn_token_setup(FIRC_RCI_TOKEN_PATH, FIRC_NDMC_BIN);
#endif

    firc_config_t cfg;
    if (firc_config_init_defaults(&cfg) != FIRC_OK) {
        FIRC_ERROR("failed to init config defaults");
        return 1;
    }
    char failed_path[4096];
    firc_err_t err =
        load_config_pair(&cfg, config_path, false, failed_path, sizeof(failed_path));
    if (err != FIRC_OK && err != FIRC_ERR_NOENT && purge) {
        /* the config may be the corrupt thing being removed: purge goes on with the defaults */
        FIRC_WARN("purge: config %s not loaded (%s); using the defaults", failed_path,
                  firc_err_str(err));
        firc_config_clear(&cfg);
        err = firc_config_init_defaults(&cfg);
    }
    if (err != FIRC_OK && err != FIRC_ERR_NOENT) {
        explain_refusal(&cfg, failed_path);
        FIRC_ERROR("failed to load config %s: %s", failed_path, firc_err_str(err));
        firc_config_clear(&cfg);
        return 1;
    }
    (void)firc_event_boot();

    /* Before any thread exists (refresher, committer, curl): an unblocked thread takes the signal's default action. */
    if (!purge && firc_loop_block_signals(&set) != FIRC_OK) {
        FIRC_ERROR("failed to block signals");
        firc_config_clear(&cfg);
        return 1;
    }

    struct daemon d = {0};
    d.cfg = &cfg;
    d.config_path = config_path;
    d.lock_fd = -1;

    /* Recreate the conf dir if removed: the ULA prefix is written into it on first start. */
    if (mkdir(FIRC_APP_CONF_DIR, 0755) == 0) {
        /* chmod after mkdir: the umask masks mkdir's mode. */
        (void)chmod(FIRC_APP_CONF_DIR, 0755);
        FIRC_WARN("%s did not exist and was created: firc's configuration is not there",
                  FIRC_APP_CONF_DIR);
    } else if (errno != EEXIST) {
        FIRC_WARN("cannot create %s: %s", FIRC_APP_CONF_DIR, strerror(errno));
    }

    /* Run dir is 0700: it holds every name the LAN resolved. */
    struct stat run_st;
    if (mkdir(FIRC_APP_RUN_DIR, 0700) == 0) {
        (void)chmod(FIRC_APP_RUN_DIR, 0700);
    } else if (errno != EEXIST) {
        FIRC_ERROR("cannot create %s: %s", FIRC_APP_RUN_DIR, strerror(errno));
    } else if (lstat(FIRC_APP_RUN_DIR, &run_st) != 0) {
        FIRC_ERROR("cannot stat %s: %s", FIRC_APP_RUN_DIR, strerror(errno));
    } else if (run_st.st_uid != geteuid()) {
        /* Owned by another uid: adopting it would hand them the API socket, the pool file and the lock. */
        FIRC_ERROR("%s belongs to uid %u, not to this daemon: the socket and the pool cannot be kept there",
                   FIRC_APP_RUN_DIR, (unsigned)run_st.st_uid);
        firc_config_clear(&cfg);
        return 1;
    } else if (!S_ISDIR(run_st.st_mode)) {
        /* Not a directory (a symlink too) is fatal: no lock could be taken and a second daemon could run. */
        FIRC_ERROR("%s is not a directory: nothing can be kept there", FIRC_APP_RUN_DIR);
        firc_config_clear(&cfg);
        return 1;
    } else if ((run_st.st_mode & 0777) != 0700 && chmod(FIRC_APP_RUN_DIR, 0700) != 0) {
        /* Wider than 0700 from an older build: tighten it. */
        FIRC_WARN("cannot make %s private: %s", FIRC_APP_RUN_DIR, strerror(errno));
    }
    if (purge) {
        const firc_purge_lock_t lock = {firc_instance_lock, FIRC_INSTANCE_LOCK_PATH};
#ifdef FIRC_ENTWARE_KN
        firc_opkgtun_io_t io;
        firc_opkgtun_rci_io(&d.opkg_rci, FIRC_KN_RCI_BASE_URL, FIRC_OPKGTUN_INTENT_PATH, &io);
        int held = firc_purge_begin(&lock, &io, revoke_rci_token, NULL, &d.lock_fd);
#else
        int held = firc_purge_begin(&lock, NULL, NULL, NULL, &d.lock_fd);
#endif
        if (held != 0) {
            firc_config_clear(&cfg);
            return 1;
        }
        err = FIRC_OK;
    } else {
        err = firc_instance_lock(FIRC_INSTANCE_LOCK_PATH, &d.lock_fd);
    }
    if (err == FIRC_ERR_EXIST) {
        FIRC_ERROR("another fircd holds %s: is the service already running?", FIRC_INSTANCE_LOCK_PATH);
        firc_config_clear(&cfg);
        return 1;
    }
    if (err != FIRC_OK) {
        /* No lock file and no way to make one: run unlocked rather than not at all. */
        FIRC_WARN("cannot take %s (%s); running without the instance lock", FIRC_INSTANCE_LOCK_PATH,
                  firc_err_str(err));
    }

    if (firc_loop_create(&d.loop) != FIRC_OK) {
        FIRC_ERROR("failed to create event loop");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    d.pipeline = firc_dns_pipeline_create();
    if (d.pipeline == NULL) {
        FIRC_ERROR("failed to create DNS pipeline");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    /* From the config for now: nothing is answered from it until firc_app_start_groups replaces it. */
    firc_dns_pipeline_set_snapshot(d.pipeline, firc_ruleset_snapshot_build(&cfg));

    int sweep_timer = 0;
    if (firc_loop_add_timer(d.loop, FIRC_POOL_SWEEP_INTERVAL_MS,
                          FIRC_POOL_SWEEP_INTERVAL_MS,
                          on_pool_sweep_timer, &d,
                          &sweep_timer) != FIRC_OK) {
        FIRC_ERROR("failed to schedule the pool sweep");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    if (!cfg.app.netfilter.disable_ipv4 || purge) {
        bool no_family = false;
        d.ipt4 = open_engine(FIRC_IPT_PROTO_IPV4, purge, cfg.app.netfilter.iptables.chain_prefix, &no_family);
        if (!d.ipt4) {
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
    }
    if (!cfg.app.netfilter.disable_ipv6 || purge) {
        bool no_family = false;
        d.ipt6 = open_engine(FIRC_IPT_PROTO_IPV6, purge, cfg.app.netfilter.iptables.chain_prefix, &no_family);
        if (!d.ipt6 && !(purge && no_family)) {
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
    }

    err = firc_netfilter_register_base_chains(d.ipt4, d.ipt6);
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to register chain patches: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    d.sets = open_sets();

    if (purge) {
        d.rtnl = firc_rtnl_open();
        if (d.rtnl == NULL) { FIRC_WARN("purge: rtnetlink not available"); }
        firc_purge_paths_t paths = {.pool_file = FIRC_POOL_STATE_PATH, .fields_file = FIRC_FIELDS_STATE_PATH,
                                    .sock = FIRC_SOCK_PATH, .lock = FIRC_INSTANCE_LOCK_PATH, .run_dir = FIRC_APP_RUN_DIR};
        firc_purge_report_t report;
        err = firc_purge(d.ipt4, d.ipt6, d.rtnl, d.sets, cfg.app.netfilter.iptables.chain_prefix, &paths, &report);
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return err == FIRC_OK ? 0 : 1;
    }

    firc_fakeip_cfg_t pool_cfg;
    err = firc_fakeip_cfg_from_app(&cfg.app, FIRC_FAKEIP_V6_PREFIX_FILE, &pool_cfg);
    if (err == FIRC_OK) { err = firc_fakeip_new(&pool_cfg, &d.pool); }
    if (err != FIRC_OK) {
        /* Two failures: a bad addressPool (INVAL) or an unwritable v6 prefix file in the conf dir. */
        if (err == FIRC_ERR_INVAL) {
            FIRC_ERROR("the address pool in %s is not one firc can use (%s): check addressPool",
                       config_path, firc_err_str(err));
        } else {
            FIRC_ERROR("the address pool could not be built (%s): its generated v6 prefix is kept "
                       "in %s, so %s has to exist and be writable",
                       firc_err_str(err), FIRC_FAKEIP_V6_PREFIX_FILE, FIRC_APP_CONF_DIR);
        }
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    {
        char iface[64];
        if (pool_overlaps_an_interface(d.pool, iface, sizeof(iface))) {
            FIRC_ERROR("the address pool overlaps a prefix of interface %s: real hosts would sit inside the "
                       "fake range; change addressPool in the config", iface);
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
    }
    {
        /* The resolver check the loader could not do before the pool existed. */
        char hit[FIRC_RESOLVER_ADDR_STRLEN];
        const firc_group_t *bad = firc_resolve_groups_in_pool(cfg.groups, cfg.n_groups, d.pool, hit, sizeof(hit));
        if (bad != NULL) {
            FIRC_ERROR("group \"%s\": resolve.server \"%s\" is inside firc's address pool, where no "
                       "resolver can be; change it in the groups file next to %s",
                       bad->name != NULL ? bad->name : "", hit, config_path);
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
    }
    err = firc_netfilter_clean_retrying(d.ipt4, d.ipt6, d.pool);
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to clear iptables: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    (void)firc_purge_sets(d.sets, cfg.app.netfilter.iptables.chain_prefix, NULL);
    /* Without conntrack every flush is a no-op, not an error. */
    d.ct = firc_ct_open();
    if (d.ct == NULL) {
        FIRC_WARN("conntrack is not available: a group's live flows will keep their old route until they end");
    }
    d.rtnl = firc_rtnl_open();
    if (d.rtnl == NULL) {
        FIRC_ERROR("failed to open rtnetlink");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    /* Stale ip rules of a killed instance would exhaust the 8-bit mark group field: swept at every start. */
    size_t stale = 0;
    err = firc_rtnl_clean_stale_rules(d.rtnl, &stale);
    if (err != FIRC_OK) {
        FIRC_WARN("failed to clear stale ip rules: %s", firc_err_str(err));
    } else if (stale > 0) {
        FIRC_INFO("removed %zu ip rule(s) left by a previous instance", stale);
    }
    (void)firc_stable_fields_start(&d.fields, FIRC_FIELDS_STATE_PATH, d.rtnl, d.ct, &cfg,
                                   groups_file_exists(config_path));

    /* Before any address is issued: else a fake address leaves by the group's default route in the clear. */
    err = firc_pool_reject_install(d.rtnl, d.pool, RT_TABLE_MAIN, FIRC_POOL_REJECT_METRIC_MAIN);
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to protect the address pool: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    /* Flush conntrack of the fail-open window: its replies come from a fake address with no NAT to undo it. */
    if (d.ct != NULL) {
        firc_ip_t b4, b6;
        uint8_t l4 = 0, l6 = 0;
        bool has4 = firc_fakeip_pool_prefix(d.pool, FIRC_FAM_V4, &b4, &l4);
        bool has6 = firc_fakeip_pool_prefix(d.pool, FIRC_FAM_V6, &b6, &l6);
        size_t dropped = 0;
        firc_err_t cerr = firc_ct_flush_pool_replies(d.ct, has4 ? b4.b : NULL, has4 ? l4 : 0,
                                                     has6 ? b6.b : NULL, has6 ? l6 : 0, &dropped);
        /* Count first: a sweep that failed halfway has still deleted flows. */
        if (dropped > 0) {
            FIRC_INFO("cleared %zu flow(s) answered with a fake address while the pool was "
                      "unprotected",
                      dropped);
        }
        if (cerr != FIRC_OK) {
            FIRC_WARN("could not clear the flows of the fail-open window: %s", firc_err_str(cerr));
        }
    }

    if (err == FIRC_OK) {
        /* Before anything is issued, so a client's old answer keeps its address when the first pass runs. */
        size_t restored = 0;
        firc_err_t lerr = firc_fakeip_load_file(d.pool, FIRC_POOL_STATE_PATH, now_unix(), &restored);
        /* the file must know every chunk in use, whatever it held */
        firc_fakeip_set_on_chunk(d.pool, on_chunk_taken, &d);
        if (lerr == FIRC_OK) {
            FIRC_INFO("pool: %zu entries restored from %s", restored, FIRC_POOL_STATE_PATH);
            forget_groups_not_in_config(d.pool, &cfg, now_unix());
            d.pool_restored = restored;
            d.pool_file_ok = true;
            /* Only after a successful load: an empty pool cannot tell a gone group from one never known. */
            d.pool_state_trusted = true;
            /* Re-save as not exact now, so a crash does not reload a stale "exact" file. */
            if (firc_fakeip_dirty_since_save(d.pool)) {
                save_pool(&d, false);
                if (firc_fakeip_dirty_since_save(d.pool)) {
                    /* If the re-save fails, distrust the pool rather than let it reload as exact. */
                    FIRC_WARN("pool: could not rewrite %s; treating the restored state as not exact", FIRC_POOL_STATE_PATH);
                    firc_fakeip_distrust(d.pool, now_unix());
                }
            }
        } else if (lerr == FIRC_ERR_NOENT) {
            d.pool_file_ok = true;
        } else if (lerr == FIRC_ERR_INVAL) {
            FIRC_WARN("pool: %s is not a pool state file this build reads (its version, its geometry, or "
                      "empty), starting over", FIRC_POOL_STATE_PATH);
            d.pool_file_ok = true;
        } else {
            FIRC_WARN("pool: %s not read: %s, starting empty and leaving the file", FIRC_POOL_STATE_PATH,
                      firc_err_str(lerr));
        }
    }
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to build the address pool: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

#ifdef FIRC_ENTWARE_KN
    d.policies = firc_kn_policies_start_notify(FIRC_KN_RCI_BASE_URL, FIRC_KN_POLICY_REFRESH_SECS,
                                               on_policies_changed, &d);
    if (d.policies == NULL) {
        FIRC_ERROR("failed to start the Keenetic policy resolver");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    firc_dns_pipeline_set_policy_resolver(d.pipeline, firc_kn_policies_resolve, firc_kn_policies_device,
                                          d.policies);

    /* A failed start is not fatal: groups without resolve.server use the common upstream. */
    d.resolvers = firc_kn_resolvers_start(FIRC_KN_RCI_BASE_URL, FIRC_KN_POLICY_REFRESH_SECS,
                                          on_resolvers_changed, &d);
    if (d.resolvers == NULL) {
        FIRC_WARN("keenetic resolvers: could not start the refresher; groups without resolve.server "
                  "use the common upstream");
    }
#else
    warn_policy_entries(&cfg);
#endif
    /* From here every matched answer is rewritten. */
    firc_dns_pipeline_set_pool(d.pipeline, d.pool, (uint32_t)pool_cfg.clamp_secs);
    firc_dns_pipeline_set_unmatched_ttl(
        d.pipeline, (uint32_t)(cfg.app.dns_proxy.unmatched_ttl / FIRC_DURATION_SEC));

    if (firc_nl_watcher_create(d.loop, on_link_up, &d, on_addr_change, &d, on_watcher_lost, &d,
                             &d.watcher) != FIRC_OK) {
        FIRC_ERROR("failed to subscribe to link/addr updates");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    firc_dnsproxy_config_t pcfg = {
        .listen_addr = cfg.app.dns_proxy.host.address,
        .listen_port = cfg.app.dns_proxy.host.port,
        .upstream_addr = cfg.app.dns_proxy.upstream.address,
        .upstream_port = cfg.app.dns_proxy.upstream.port,
        .timeout_ms = (uint32_t)(cfg.app.dns_proxy.timeout / FIRC_DURATION_MS),
        .max_concurrent = (uint32_t)cfg.app.dns_proxy.max_concurrent,
        .max_idle_conns = (uint32_t)cfg.app.dns_proxy.max_idle_conns,
        .disable_drop_aaaa = cfg.app.dns_proxy.disable_drop_aaaa,
        .hold_ms = FIRC_DNSPROXY_HOLD_MS,
    };
    if (pcfg.max_concurrent == 0) {
        pcfg.max_concurrent = 100;
    }
    if (pcfg.timeout_ms == 0) {
        pcfg.timeout_ms = 5000;
    }

    if (firc_dnsproxy_create(&pcfg, d.loop, on_response, &d, &d.proxy) !=
        FIRC_OK) {
        FIRC_ERROR("failed to create DNS proxy");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    {
        /* a group resolver answering with one of firc's own addresses is a sink */
        firc_ip_t b4, b6;
        uint8_t l4 = 0, l6 = 0;
        bool h4 = firc_fakeip_pool_prefix(d.pool, FIRC_FAM_V4, &b4, &l4);
        bool h6 = firc_fakeip_pool_prefix(d.pool, FIRC_FAM_V6, &b6, &l6);
        firc_dnsproxy_set_pool_prefixes(d.proxy, h4 ? &b4 : NULL, l4, h6 ? &b6 : NULL, l6);
    }
    firc_dnsproxy_set_pool(d.proxy, d.pool, (uint32_t)pool_cfg.clamp_secs);
    firc_dnsproxy_set_cross(d.proxy, on_cross, &d);
    d.router = firc_resolve_router_new(d.pipeline);
    if (d.router == NULL) {
        FIRC_ERROR("failed to create the resolver router");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    firc_dnsproxy_set_router(d.proxy, firc_resolve_router_decide, d.router);
    firc_dnsproxy_set_blocked(d.proxy, on_blocked, &d);
    firc_dnsproxy_set_tally(d.proxy, on_tally, &d);
    {
        const uint64_t hour_ms = (uint64_t)FIRC_DNSSTAT_HOUR * 1000u;
        uint64_t into = (uint64_t)mono_ms() % hour_ms;
        int dnsstat_timer = 0;
        if (firc_loop_add_timer(d.loop, hour_ms - into + 1000u, hour_ms, on_dnsstat_sweep, &d, &dnsstat_timer) !=
            FIRC_OK) {
            FIRC_WARN("dns stats: no hourly sweep, idle keys stay until evicted");
        }
    }
    firc_dns_pipeline_set_v6_fn(d.pipeline, firc_resolve_router_carries_v6, d.router);
    firc_dns_pipeline_set_cross_fn(d.pipeline, firc_resolve_router_cross_v6, d.router);
#ifdef FIRC_ENTWARE_KN
    /* Wait (3 s bound) for the first hotspot read: a policy host asking earlier would stay unrouted. */
    if (firc_config_selects_devices(&cfg) && !firc_kn_policies_wait_first(d.policies, 3000)) {
        FIRC_WARN("keenetic policies: no hotspot table yet, devices are their addresses until it arrives");
    }
#endif
    err = firc_dnsproxy_start(d.proxy);
    bool dns_down = false;
    if (err != FIRC_OK) {
        const char *why = firc_dnsproxy_listen_error(d.proxy);
        firc_dnsproxy_listen_t st;
        firc_dnsproxy_listen_state(d.proxy, &st);
        if (!st.busy) {
            FIRC_ERROR("failed to start DNS proxy: %s", why[0] != '\0' ? why : firc_err_str(err));
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
        FIRC_ERROR("DNS proxy cannot listen: %s; running without DNS, so no name gets a fake address until it can "
                   "(retried every %u s)",
                   why, FIRC_DNS_RETRY_MS / 1000u);
        dns_down = true;
    }

    if (firc_loop_add_signals(d.loop, &set, on_signal, &d) != FIRC_OK) {
        FIRC_ERROR("failed to subscribe to signals");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    if (!cfg.app.dns_proxy.disable_remap53) {
        firc_remap_addr_t *addrs = NULL;
        size_t n_addrs = 0;
        err = firc_port_remap_collect(cfg.app.link, cfg.app.n_link, true, &addrs, &n_addrs);
        if (err != FIRC_OK) {
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
        d.port_remap =
            firc_port_remap_new(cfg.app.netfilter.iptables.chain_prefix, 53,
                              cfg.app.dns_proxy.host.port, addrs, n_addrs, d.ipt4, d.ipt6);
        free(addrs);
        if (d.port_remap == NULL) {
            FIRC_ERROR("failed to override DNS: out of memory");
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
        err = dns_down ? FIRC_OK : firc_port_remap_enable(d.port_remap);
        if (err != FIRC_OK) {
            FIRC_ERROR("failed to override DNS: %s", firc_err_str(err));
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
    }

    firc_app_deps_t app_deps = {
        .cfg = &cfg,
        .pipeline = d.pipeline,
        .ipt4 = d.ipt4,
        .ipt6 = d.ipt6,
        .rtnl = d.rtnl,
        .ct = d.ct,
        .sets = d.sets,
        .pool = d.pool,
        .loop = d.loop,
        .proxy = d.proxy,
        .router = d.router,
        .resolvers = d.resolvers,
        .start_idx = cfg.app.netfilter.start_mark_table_index,
        /* The one file the app writes on its own: a sync that changed a group's list. */
        .config_path = d.config_path,
        .config_version = FIRC_VERSION,
    };
    d.app = firc_app_create(&app_deps);
    if (d.app == NULL) {
        FIRC_ERROR("failed to create app");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
#ifdef FIRC_ENTWARE_KN
    /* The capture must judge selectors with the DNS pipeline's resolvers or it reports the wrong clients. */
    firc_app_set_policy_resolver(d.app, firc_kn_policies_resolve, firc_kn_policies_device,
                                 d.policies);
    /* Before the groups start: a deny rendered without the device lookup writes nothing while DNS refuses. */
    firc_app_set_device_lookup(d.app, firc_kn_policies_mark, firc_kn_policies_hosts_in, firc_kn_policies_policy_hosts,
                               firc_kn_policies_policy_nets, d.policies);
    firc_app_set_policies_read(d.app, firc_kn_policies_read);
#endif
    firc_app_set_dns_down(d.app, dns_down);
    if (firc_loop_add_timer(d.loop, FIRC_CF_TICK_MS, FIRC_CF_TICK_MS, on_cf_tick, &d, &d.cf_timer) != FIRC_OK) {
        FIRC_ERROR("failed to schedule the Cloudflare target check");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    if (dns_down && firc_loop_add_timer(d.loop, FIRC_DNS_RETRY_MS, FIRC_DNS_RETRY_MS, on_dns_retry, &d,
                                        &d.dns_retry_timer) != FIRC_OK) {
        FIRC_ERROR("failed to schedule the DNS proxy's retry");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    firc_tunnels_t tunnels = {0};
    bool tunnels_ok = load_tunnels(&tunnels, "starting with no tunnels");
    /* No group stops the daemon: one that fails is logged and left out. */
    err = firc_app_start_groups(d.app, tunnels_ok ? &tunnels : NULL);
    if (err != FIRC_OK) {
        firc_tunnels_free(&tunnels);
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    /* After start_groups and the pool load: both halves of "which marks are stale" are in hand. */
    flush_stale_group_marks(&d);

    const char *tunvless_bin = getenv("FIRC_TUNVLESS_BIN");
    if (tunvless_bin == NULL || tunvless_bin[0] == 0) { tunvless_bin = FIRC_TUNVLESS_BIN; }
    {
        const firc_app_config_t *running = firc_app_running_settings(d.app);
        const firc_tunrun_lan_t lan = {(const char *const *)running->link, running->n_link, firc_inbounds_wan_table,
                                       NULL};
        d.tunrun = firc_tunrun_new(d.loop, d.rtnl, cfg.app.netfilter.start_mark_table_index, tunvless_bin, &lan);
        if (d.tunrun == NULL) {
            FIRC_ERROR("failed to create the tunnel supervisor");
            firc_tunnels_free(&tunnels);
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
        const char *cache = getenv("FIRC_TUNNELS_CACHE_DIR");
        d.tunsubs = firc_tunsubs_new(d.loop, cache != NULL && cache[0] != 0 ? cache : FIRC_TUNNELS_CACHE_DIR,
                                     firc_tunrun_bodies_changed, d.tunrun);
        if (d.tunsubs == NULL) {
            FIRC_WARN("tunnels: subscriptions cannot be fetched (no worker thread); links still work");
        }
        if (d.tunsubs != NULL) {
            subscription_defaults(&d);
        }
        firc_tunrun_set_subs(d.tunrun, d.tunsubs);
        firc_tunrun_set_path(d.tunrun, firc_tunnels_path());
        firc_app_set_tunnel_down(d.app, tunnel_down, d.tunrun);
        firc_ip_t pool4;
        uint8_t pool4_len = 0;
        if (firc_fakeip_pool_prefix(d.pool, FIRC_FAM_V4, &pool4, &pool4_len)) {
            firc_tunrun_set_tcp_pool(d.tunrun, pool4.b, pool4_len);
        }
        const firc_tunrun_ktcp_t hooks = {ktcp_tunnels, ktcp_image, ktcp_real_of, d.app, ktcp_closed, ktcp_ready};
        firc_tunrun_set_ktcp(d.tunrun, &hooks);
        firc_tunrun_set_ktcp_usable(d.tunrun, d.ipt4 != NULL && firc_ipt_has(d.ipt4, "REDIRECT") &&
                                                      firc_ipt_has(d.ipt4, "addrtype"));
        const firc_app_ktcp_sink_t sink = {ktcp_pushed, ktcp_close, d.tunrun};
        firc_app_set_ktcp_sink(d.app, &sink);
        firc_opkgtun_io_t io;
        firc_opkgtun_rci_io(&d.opkg_rci, FIRC_KN_RCI_BASE_URL, FIRC_OPKGTUN_INTENT_PATH, &io);
        d.opkg = firc_opkgtun_start(&io);
        if (d.opkg == NULL) {
            FIRC_ERROR("tunnels: the router's OpkgTun records cannot be kept; opkgtun tunnels will not start");
        } else {
            const firc_tunrun_router_t router = {opkg_want, opkg_seen, opkg_sweep, opkg_pass,
                                                 firc_opkgtun_fd(d.opkg), &d, opkg_trip};
            firc_tunrun_set_router(d.tunrun, &router);
        }
        (void)firc_tunrun_start(d.tunrun, tunnels_ok ? &tunnels : NULL);
        firc_tunnels_free(&tunnels);
    }

    d.ifcheck = firc_ifcheck_new(d.loop, iw_results, &d);
    if (d.ifcheck != NULL) { firc_ifcheck_set_group_done(d.ifcheck, iw_group_results, &d); }
    firc_ifwatch_deps_t iw = {.loop = d.loop,
                              .rtnl = d.rtnl,
                              .start_table = cfg.app.netfilter.start_mark_table_index,
                              .n_groups = iw_n_groups,
                              .group_at = iw_group_at,
                              .move = iw_move,
                              .link_up = iw_link_up,
                              .tunnel_known = iw_tunnel_known,
                              .tunnel_down = iw_tunnel_down,
                              .tunnel_starting = iw_tunnel_starting,
                              .queue = iw_queue,
                              .ud = &d,
                              .queue_group = iw_queue_group,
                              .resolve_for = iw_resolve_for};
    d.ifwatch = d.ifcheck != NULL ? firc_ifwatch_new(&iw) : NULL;
    if (d.ifwatch == NULL) {
        FIRC_ERROR("interface lists: no memory for their checks; every group stays on its first interface");
        firc_ifcheck_free(d.ifcheck);
        d.ifcheck = NULL;
    } else {
        iw_settings(&d, firc_app_running_settings(d.app));
        firc_app_ifaces_t hooks = {
            .pick = iw_pick,
            .view = iw_view,
            .group_view = iw_group_view,
            .move_up = iw_move_up,
            .settings = iw_settings,
            .ud = &d,
        };
        firc_app_set_ifaces(d.app, &hooks);
    }

    /* Initial delay is the interval, not 0: the boot sweep below already asks for missing lists. */
    int list_auto_update_timer = 0;
    if (firc_loop_add_timer(d.loop, FIRC_LIST_AUTO_UPDATE_INTERVAL_MS,
                          FIRC_LIST_AUTO_UPDATE_INTERVAL_MS,
                          on_list_auto_update_timer, &d,
                          &list_auto_update_timer) != FIRC_OK) {
        FIRC_ERROR("failed to schedule list auto-update");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    firc_groups_ctx_t groups_ctx = {
        .app = d.app,
        .config_path = config_path,
        .config_version = FIRC_VERSION,
    };
    firc_system_ctx_t system_ctx = {
        .app = d.app,
        .config_path = config_path,
        .config_version = FIRC_VERSION,
        .tunnels = firc_tunrun_config(d.tunrun),
        .dnsproxy = d.proxy,
        .iface_event = on_iface_event,
        .iface_event_ud = &d,
    };
#ifdef FIRC_ENTWARE_KN
    system_ctx.policies = d.policies;
    system_ctx.resolvers = d.resolvers;
#endif
    d.groups_ctx = &groups_ctx;
    d.system_ctx = &system_ctx;
    (void)firc_system_dnsstats_start(&system_ctx);
    firc_tunnels_ctx_t tunnels_ctx = {
        .run = d.tunrun,
        .subs = d.tunsubs,
        .app = d.app,
        .path = firc_tunnels_path(),
        .binary = tunvless_bin,
    };
    d.tunnels_ctx = &tunnels_ctx;
    firc_capture_ctx_t capture_ctx = {
        .app = d.app,
    };
    firc_auth_ctx_t auth_ctx = {
        .state_dir = auth_state_dir_fn,
        .ud = &cfg,
    };
    firc_update_api_ctx_t update_ctx = {
        .sys = &system_ctx,
    };
    const char *update_dir = getenv("FIRC_UPDATE_DIR");
    firc_update_deps_t update_deps = {
        .loop = d.loop,
        .dir = update_dir != NULL && update_dir[0] != 0 ? update_dir : FIRC_UPDATE_DIR,
        .api_url = FIRC_UPDATE_API_URL,
        .download_prefix = FIRC_UPDATE_DOWNLOAD_PREFIX,
        .version = FIRC_VERSION,
        .revision = FIRC_REVISION,
        .target = FIRC_TARGET,
        .sh = FIRC_SH_BIN,
        .opkg = FIRC_OPKG_BIN,
        .checked = firc_update_api_checked,
        .ud = &update_ctx,
    };
    d.update = firc_update_new(&update_deps);
    if (d.update == NULL) { FIRC_WARN("update: the checker could not be created; running without it"); }
    update_ctx.update = d.update;
    system_ctx.update = d.update;
    d.update_ctx = &update_ctx;
    if (d.update != NULL && firc_loop_add_timer(d.loop, FIRC_UPDATE_FIRST_CHECK_MS, FIRC_UPDATE_CHECK_INTERVAL_MS,
                                                on_update_timer, &d, &d.update_timer) != FIRC_OK) {
        FIRC_WARN("update: the daily check could not be scheduled; checks run only on request");
        d.update_timer = 0;
    }

    if (firc_httpd_create(d.loop, &d.http_unix) != FIRC_OK) {
        FIRC_ERROR("failed to create Unix socket server");
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    firc_system_register_routes(d.http_unix, &system_ctx);
    firc_groups_register_routes(d.http_unix, &groups_ctx);
    firc_tunnels_register_routes(d.http_unix, &tunnels_ctx);
    /* Capture routes on both sockets: a browser cannot reach the Unix socket. */
    firc_capture_register_routes(d.http_unix, &capture_ctx);
    firc_update_register_routes(d.http_unix, &update_ctx);
    firc_httpd_route(d.http_unix, "GET", "/api/v1/auth", firc_auth_status_handler, &auth_ctx);
    firc_httpd_route(d.http_unix, "POST", "/api/v1/auth", firc_auth_login_handler, &auth_ctx);
    err = firc_httpd_listen_unix(d.http_unix, FIRC_SOCK_PATH);
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to listen on Unix socket %s: %s", FIRC_SOCK_PATH, firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    FIRC_INFO("Unix socket API listening on %s", FIRC_SOCK_PATH);

    firc_static_ctx_t static_ctx = { .root = FIRC_APP_SHARE_DIR "/skins/default", };
    if (cfg.app.http_web.enabled) {
        if (firc_httpd_create(d.loop, &d.http_tcp) != FIRC_OK) {
            FIRC_ERROR("failed to create HTTP server");
            daemon_teardown(&d);
            firc_config_clear(&cfg);
            return 1;
        }
        firc_system_register_routes(d.http_tcp, &system_ctx);
        firc_groups_register_routes(d.http_tcp, &groups_ctx);
        firc_tunnels_register_routes(d.http_tcp, &tunnels_ctx);
        firc_capture_register_routes(d.http_tcp, &capture_ctx);
        firc_update_register_routes(d.http_tcp, &update_ctx);
        firc_httpd_route(d.http_tcp, "GET", "/api/v1/auth", firc_auth_status_handler, &auth_ctx);
        firc_httpd_route(d.http_tcp, "POST", "/api/v1/auth", firc_auth_login_handler, &auth_ctx);
        firc_httpd_set_middleware(d.http_tcp, firc_auth_middleware, &auth_ctx);
        firc_httpd_set_not_found(d.http_tcp, firc_static_handler, &static_ctx);
        uint16_t web_ports[FIRC_WEB_PORTS_MAX];
        size_t n_web_ports = firc_web_ports(cfg.app.http_web.host.port, cfg.app.dns_proxy.host.port,
                                            web_ports);
        if (firc_system_listen_web(&system_ctx, d.http_tcp, web_ports, n_web_ports) != FIRC_OK) {
            /* A WebUI bind failure is not fatal: DNS and routing must survive a port conflict. */
            firc_httpd_destroy(d.http_tcp);
            d.http_tcp = NULL;
        }
        /* Logged on every start: an operator who cannot log in has no other message to read. */
        FIRC_INFO("the WebUI requires a login from %s, or %s for an account that one does "
                  "not give a password hash",
                  FIRC_SHADOW_FILE, FIRC_PASSWD_FILE);
    } else {
        FIRC_INFO("HTTP WebUI disabled by configuration");
    }

    /* Committer last, once everything a rebuild reaches is up. */
    firc_app_set_port_remap(d.app, d.port_remap);
    /* List worker before the committer: starting it cannot leave the tables half written. */
    err = firc_app_start_list_worker(d.app);
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to start the list worker: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }
    if (d.update != NULL) {
        err = firc_update_start(d.update);
        if (err != FIRC_OK) {
            FIRC_WARN("update: the checker could not be started (%s); running without it", firc_err_str(err));
            if (d.update_timer != 0) {
                (void)firc_loop_del_timer(d.loop, d.update_timer);
                d.update_timer = 0;
            }
            firc_update_free(d.update);
            d.update = NULL;
            update_ctx.update = NULL;
            system_ctx.update = NULL;
        }
    }
    if (d.ifwatch != NULL && (firc_ifcheck_start(d.ifcheck) != FIRC_OK || firc_ifwatch_start(d.ifwatch) != FIRC_OK)) {
        FIRC_ERROR("interface lists: their checks could not start; every group stays where it is");
        firc_app_set_ifaces(d.app, NULL);
        firc_ifwatch_free(d.ifwatch);
        d.ifwatch = NULL;
        firc_ifcheck_free(d.ifcheck);
        d.ifcheck = NULL;
    }
    /* Boot sweep: no list body is kept on flash, so every enabled list needs a first fetch. */
    firc_app_request_sync_missing(d.app);

    err = firc_app_start_netfilter_committer(d.app);
    if (err == FIRC_OK) {
        /* One pass now: restored pairs have no kernel rule, and a rewrite during startup would go unrepaired. */
        firc_app_pool_changed(d.app);
        (void)firc_app_autolist_apply(d.app);
    }
    if (err != FIRC_OK) {
        FIRC_ERROR("failed to start netfilter committer: %s", firc_err_str(err));
        daemon_teardown(&d);
        firc_config_clear(&cfg);
        return 1;
    }

    FIRC_INFO("service started");
    d.started = true;
    err = firc_loop_run(d.loop);
    if (err != FIRC_OK) {
        FIRC_ERROR("event loop failed: %s", firc_err_str(err));
    }

    FIRC_INFO("service stopped");
    daemon_teardown(&d);
    firc_config_clear(&cfg);
    firc_sub_fetch_global_cleanup();
    return err == FIRC_OK ? 0 : 1;
}
