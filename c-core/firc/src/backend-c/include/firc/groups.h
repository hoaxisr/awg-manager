#ifndef FIRC_GROUPS_H
#define FIRC_GROUPS_H

#include <stdint.h>

#include "firc/app.h"
#include "firc/httpd.h"

typedef struct list_subscriber list_subscriber_t; /* opaque, loop thread only */
typedef struct list_preview list_preview_t; /* opaque, loop thread only */
typedef struct list_catalog list_catalog_t;

typedef struct firc_groups_ctx {
    firc_app_t *app;
    const char *config_path; /* NULL makes ?save=true a no-op */
    const char *config_version;
    list_subscriber_t *subscribers; /* open list-sync streams, loop thread only */
    list_preview_t *previews; /* in-flight previews, at most FIRC_LIST_PREVIEW_MAX, loop thread only */
    size_t n_previews;
    const char *catalog_url;
    uint64_t (*catalog_now_ms)(void);
    list_catalog_t *catalog;
} firc_groups_ctx_t;

void firc_groups_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx);

/* ends every open list-sync stream and in-flight preview; call before firc_httpd_destroy/firc_loop_destroy */
void firc_groups_api_close_streams(firc_groups_ctx_t *ctx);

#endif /* FIRC_GROUPS_H */
