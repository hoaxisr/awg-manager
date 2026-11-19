#ifndef FIRC_UPDATE_API_H
#define FIRC_UPDATE_API_H

#include <stddef.h>

#include "firc/httpd.h"
#include "firc/system.h"
#include "firc/update.h"

#define FIRC_UPDATE_API_WAITING_MAX 8

typedef struct firc_update_api_ctx {
    firc_update_t *update;
    firc_system_ctx_t *sys;
    firc_http_deferred_t *waiting[FIRC_UPDATE_API_WAITING_MAX];
    size_t n_waiting;
} firc_update_api_ctx_t;

/* GET /api/v1/system/update, POST /api/v1/system/update/check and /api/v1/system/update/install */
void firc_update_register_routes(firc_httpd_t *h, firc_update_api_ctx_t *ctx);

/* deps.checked of ctx->update (ud is ctx): answers every waiting check with o */
void firc_update_api_checked(void *ud, const firc_update_outcome_t *o);

/* drops every waiting check unanswered; before firc_httpd_destroy */
void firc_update_api_close(firc_update_api_ctx_t *ctx);

#endif
