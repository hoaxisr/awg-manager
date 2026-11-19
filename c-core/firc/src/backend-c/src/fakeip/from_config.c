#include "firc/fakeip_from_config.h"

#include <string.h>

#include "firc/duration.h"
#include "firc/fakeip_ula.h"

int64_t firc_fakeip_idle_for_clamp(int64_t clamp_secs) { return clamp_secs * 2 > 3600 ? clamp_secs * 2 : 3600; }

static firc_err_t family_from_text(const char *text, uint8_t chunk, uint8_t want_len,
                                   firc_fakeip_family_cfg_t *out) {
    /* Zeroed: firc_ip_parse_cidr leaves outputs untouched on failure. */
    firc_ip_t base = {{0}, 0};
    uint8_t prefix = 0;
    if (!firc_ip_parse_cidr(text, &base, &prefix)) { return FIRC_ERR_INVAL; }
    if (base.len != want_len) { return FIRC_ERR_INVAL; }
    out->base = base;
    out->pool_cidr = prefix;
    out->chunk_cidr = chunk;
    return FIRC_OK;
}

firc_err_t firc_fakeip_cfg_from_app(const firc_app_config_t *app, const char *ula_path,
                                    firc_fakeip_cfg_t *out) {
    if (app == NULL || out == NULL) { return FIRC_ERR_INVAL; }

    firc_fakeip_cfg_t cfg;
    memset(&cfg, 0, sizeof(cfg));

    if (app->fakeip.v4.pool == NULL) { return FIRC_ERR_INVAL; }
    firc_err_t err = family_from_text(app->fakeip.v4.pool, app->fakeip.v4.chunk, 4, &cfg.v4);
    if (err != FIRC_OK) { return err; }

    if (app->fakeip.v6.pool == NULL) { return FIRC_ERR_INVAL; }
    if (app->fakeip.v6.pool[0] == '\0') {
        /* Generated and persisted, so it survives a restart. */
        if (ula_path == NULL) { return FIRC_ERR_INVAL; }
        err = firc_fakeip_ula_load(ula_path, &cfg.v6.base, &cfg.v6.pool_cidr);
        if (err != FIRC_OK) { return err; }
        cfg.v6.chunk_cidr = app->fakeip.v6.chunk;
    } else {
        err = family_from_text(app->fakeip.v6.pool, app->fakeip.v6.chunk, 16, &cfg.v6);
        if (err != FIRC_OK) { return err; }
    }

    /* Division truncates; refuse sub-second values instead of rounding. */
    if (app->fakeip.ttl_clamp < FIRC_DURATION_SEC || app->fakeip.ttl_clamp % FIRC_DURATION_SEC != 0) {
        return FIRC_ERR_INVAL;
    }
    cfg.clamp_secs = app->fakeip.ttl_clamp / FIRC_DURATION_SEC;
    cfg.idle_secs = firc_fakeip_idle_for_clamp(cfg.clamp_secs);
    err = firc_fakeip_check_windows(cfg.idle_secs, cfg.clamp_secs);
    if (err != FIRC_OK) { return err; }
    cfg.max_names = app->fakeip.max_names;

    *out = cfg;
    return FIRC_OK;
}
