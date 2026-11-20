#ifndef FIRC_FAKEIP_FROM_CONFIG_H
#define FIRC_FAKEIP_FROM_CONFIG_H

#include "firc/fakeip.h"
#include "firc/models.h"

/* Builds the pool config from `app`; *out is not usable on a refusal (FIRC_ERR_INVAL/FIRC_ERR_SYS). */
firc_err_t firc_fakeip_cfg_from_app(const firc_app_config_t *app, const char *ula_path,
                                    firc_fakeip_cfg_t *out);

/* The pool's idle window in seconds: twice the clamp, never under an hour. */
int64_t firc_fakeip_idle_for_clamp(int64_t clamp_secs);

#endif /* FIRC_FAKEIP_FROM_CONFIG_H */
