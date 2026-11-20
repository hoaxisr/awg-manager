#ifndef FIRC_STATICFILES_H
#define FIRC_STATICFILES_H

#include "firc/httpd.h"

typedef struct firc_static_ctx {
    const char *root; /* borrowed, must outlive the server */
} firc_static_ctx_t;

/* serves <root>/<path>, retrying once against index.html if <path> is a directory; missing file -> 404 JSON,
 * except "/" -> 404 HTML placeholder */
void firc_static_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud);

#endif /* FIRC_STATICFILES_H */
