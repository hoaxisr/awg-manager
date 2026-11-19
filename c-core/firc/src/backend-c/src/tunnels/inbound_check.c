#include "firc/inbound_check.h"

#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include "firc/ifaddr.h"
#include "firc/keenetic_rci.h"
#include "firc/listen.h"
#include "firc/log.h"

bool firc_inbounds_wan_table(const char *const *link, size_t n, bool *wan, void *ud)
{
    (void)ud;
    firc_kn_aliases_t a = {0};
    memset(wan, 0, n * sizeof *wan);
    if (firc_kn_get_iface_aliases(&a) != FIRC_OK) {
        firc_kn_aliases_free(&a);
        FIRC_INFO("tunnels: interface roles unknown, inbounds skip the WAN check");
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        wan[i] = firc_kn_aliases_inet(&a, link[i]);
    }
    firc_kn_aliases_free(&a);
    return true;
}

static bool is_running(const firc_tunnels_t *running, const firc_tun_inbound_t *b)
{
    for (size_t i = 0; running != NULL && i < running->n; i++) {
        if (!running->t[i].enable) {
            continue;
        }
        for (size_t j = 0; j < running->t[i].n_inb; j++) {
            const firc_tun_inbound_t *r = &running->t[i].inb[j];
            if (r->port == b->port && strcmp(r->listen, b->listen) == 0) {
                return true;
            }
        }
    }
    return false;
}

static firc_err_t refuse(firc_tun_err_t *err, size_t ti, size_t i, const char *field, const char *why)
{
    snprintf(err->where, sizeof err->where, "tunnels[%zu].inbounds[%zu].%s", ti, i, field);
    snprintf(err->why, sizeof err->why, "%s", why);
    return FIRC_ERR_INVAL;
}

static bool busy(const firc_tun_inbound_t *b)
{
    char ip[64];
    struct sockaddr_storage sa;
    socklen_t len = 0;
    if (firc_iface_ipv4(b->listen, ip, sizeof ip) != 0 || !firc_listen_addr(ip, b->port, false, &sa, &len)) {
        return false;
    }
    int fd = firc_listen_open(NULL, SOCK_STREAM, &sa, len, 0, NULL, 0);
    if (fd >= 0) {
        close(fd);
        return false;
    }
    return fd == -EADDRINUSE;
}

static firc_err_t check_one(firc_tunrun_t *run, const firc_app_config_t *app, bool enabled,
                            const firc_tun_inbound_t *b, size_t ti, size_t i, firc_tun_err_t *err)
{
    if (!firc_tunrun_lan_ok(run, b->listen)) {
        char why[sizeof err->why];
        snprintf(why, sizeof why, "%s is not a LAN interface of the running app.link (a saved change needs a restart)",
                 b->listen);
        return refuse(err, ti, i, "listen", why);
    }
    if (b->port == app->http_web.host.port) {
        return refuse(err, ti, i, "port", "the WebUI's port");
    }
    if (b->port == app->dns_proxy.host.port || b->port == 53) {
        return refuse(err, ti, i, "port", "the DNS port");
    }
    if (enabled && !is_running(firc_tunrun_config(run), b) && busy(b)) {
        char why[sizeof err->why];
        snprintf(why, sizeof why, "port %u on %s is busy", b->port, b->listen);
        return refuse(err, ti, i, "port", why);
    }
    return FIRC_OK;
}

firc_err_t firc_inbounds_check(const firc_tunnels_t *want, firc_tunrun_t *run, const firc_app_config_t *app,
                               firc_tun_err_t *err)
{
    for (size_t ti = 0; ti < want->n; ti++) {
        for (size_t i = 0; i < want->t[ti].n_inb; i++) {
            firc_err_t r = check_one(run, app, want->t[ti].enable, &want->t[ti].inb[i], ti, i, err);
            if (r != FIRC_OK) {
                return r;
            }
        }
    }
    return FIRC_OK;
}
