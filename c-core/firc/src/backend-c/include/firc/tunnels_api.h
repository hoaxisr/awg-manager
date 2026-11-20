#ifndef FIRC_TUNNELS_API_H
#define FIRC_TUNNELS_API_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/app.h"
#include "firc/httpd.h"
#include "firc/tunnels.h"
#include "firc/tunprobe.h"
#include "firc/tunrun.h"
#include "firc/tunsubs.h"

typedef struct tun_probe_call tun_probe_call_t;

typedef struct {
    char dev[32];
    uint64_t rx, tx;
    int64_t at_ms;
} firc_tun_rate_t;

typedef struct firc_tunnels_ctx {
    firc_tunrun_t *run;
    firc_tunsubs_t *subs;
    firc_app_t *app;
    const char *path;
    const char *binary;
    const char *sysfs_net;
    tun_probe_call_t *probe;
    firc_tun_rate_t rates[FIRC_TUN_MAX * (2 + FIRC_TUN_MAX_INBOUNDS)];
} firc_tunnels_ctx_t;

/* Registers /api/v1/tunnels and its sub-routes, run on the loop thread that owns ctx->run. A NULL subs holds
   no bodies, a NULL app lists no groups, a NULL sysfs_net reads /sys/class/net; probe starts NULL. */
void firc_tunnels_register_routes(firc_httpd_t *h, firc_tunnels_ctx_t *ctx);

/* Kills a probe in flight and drops its answer; call before firc_httpd_destroy. NULL-safe. */
void firc_tunnels_api_close(firc_tunnels_ctx_t *ctx);

#endif
