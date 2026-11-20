/* GET /api/v1/system/capture -> {"running":bool,"endsAt":N,"secondsLeft":N}; POST -> 200 + "token" (409 one runs,
 * 400 no LAN interface, 503 no pool/NFLOG bind, 501 kernel refuses rules); DELETE ?token= -> 200 (400 no token,
 * 409 token mismatch). The stop token is handed out once, on the starting POST; there is no override. */
#ifndef FIRC_CAPTURE_H
#define FIRC_CAPTURE_H

#include "firc/app.h"
#include "firc/httpd.h"

typedef struct firc_capture_ctx {
    firc_app_t *app;
    int64_t (*now)(void *ud); /* wall clock, injectable for tests */
    void *ud;
} firc_capture_ctx_t;

void firc_capture_register_routes(firc_httpd_t *h, firc_capture_ctx_t *ctx);

#endif /* FIRC_CAPTURE_H */
