#include "firc/capture.h"

#include <time.h>

#include <cjson/cJSON.h>

#include "firc/log.h"
#include "firc/taprules.h" /* FIRC_TAP_LAN_MODULES */

static void must_route(firc_httpd_t *h, const char *method, const char *pattern,
                       firc_http_handler_fn fn, void *ud) {
    if (firc_httpd_route(h, method, pattern, fn, ud) != FIRC_OK) {
        FIRC_ERROR("failed to register route %s %s", method, pattern);
    }
}

static int64_t ctx_now(const firc_capture_ctx_t *ctx) {
    if (ctx->now != NULL) { return ctx->now(ctx->ud); }
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec;
}

static void write_status(firc_http_res_t *res, firc_app_t *app, const char *token) {
    cJSON *out = cJSON_CreateObject();
    if (out == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_capture_status_t st;
    firc_app_capture_status(app, &st);
    cJSON_AddBoolToObject(out, "running", st.running);
    if (st.running) {
        cJSON_AddNumberToObject(out, "endsAt", (double)st.ends_at);
        cJSON_AddNumberToObject(out, "secondsLeft", (double)st.seconds_left);
        if (token != NULL) { cJSON_AddStringToObject(out, "token", token); }
    }
    firc_capture_last_t last;
    if (firc_app_capture_last(app, &last)) {
        cJSON *obj = cJSON_AddObjectToObject(out, "last");
        cJSON *summary = obj != NULL ? cJSON_CreateArray() : NULL;
        if (obj == NULL || summary == NULL) {
            cJSON_Delete(summary);
            cJSON_Delete(out);
            firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
            return;
        }
        cJSON_AddNumberToObject(obj, "startedAt", (double)last.started_at);
        cJSON_AddNumberToObject(obj, "endedAt", (double)last.ended_at);
        cJSON_AddStringToObject(obj, "ended", last.ended);
        cJSON_AddNumberToObject(obj, "leaks", (double)last.leaks);
        cJSON_AddItemToObject(obj, "summary", summary);
        for (size_t i = 0; i < last.n_summary; i++) {
            cJSON_AddItemToArray(summary, cJSON_CreateString(last.summary[i]));
        }
    }
    firc_http_res_write_json(res, 200, out);
}

static void handle_get(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_capture_ctx_t *ctx = ud;
    write_status(res, ctx->app, NULL);
}

static void handle_start(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_capture_ctx_t *ctx = ud;
    char token[FIRC_TAP_TOKEN_CHARS + 1] = {0};
    firc_err_t err = firc_app_capture_start(ctx->app, ctx_now(ctx), token);
    switch (err) {
    case FIRC_OK:
        write_status(res, ctx->app, token);
        return;
    case FIRC_ERR_EXIST:
        firc_http_res_write_error(res, 409, "a capture is already running");
        return;
    case FIRC_ERR_INVAL:
        firc_http_res_write_error(res, 400,
                                  "no LAN interface to capture on: `link` in the settings is "
                                  "empty or names something that is not an interface");
        return;
    case FIRC_ERR_STATE:
        firc_http_res_write_error(res, 503, "no address pool: the daemon is not routing yet");
        return;
    case FIRC_ERR_NOSYS:
        firc_http_res_write_error(res, 501,
                                  "this kernel will not take the capture rules: they need "
                                  FIRC_TAP_LAN_MODULES ". The daemon's log names what it "
                                  "lacks");
        return;
    case FIRC_ERR_AGAIN:
        firc_http_res_write_error(res, 503,
                                  "the kernel could not be asked which capture extensions it has; "
                                  "the daemon's log names the error. Routing is not affected");
        return;
    case FIRC_ERR_IO:
        firc_http_res_write_error(res, 503,
                                  "no random bytes for the capture's token: the router could "
                                  "not read /dev/urandom");
        return;
    case FIRC_ERR_SYS:
        firc_http_res_write_error(res, 503,
                                  "could not start the capture's NFLOG readers: a group would "
                                  "not bind -- another tool may be holding one, or the kernel "
                                  "has no nfnetlink_log (on Keenetic, the kmod_ndms package) -- "
                                  "or the event loop would not watch its socket; the daemon's "
                                  "log says which");
        return;
    default:
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
}

static void handle_stop(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_capture_ctx_t *ctx = ud;
    const char *token = firc_http_req_query(req, "token");
    if (token == NULL) {
        firc_http_res_write_error(res, 400, "token is required: it is what the start returned");
        return;
    }
    firc_err_t err = firc_app_capture_stop(ctx->app, token);
    /* any error but NOENT means the capture has already stopped; only the unstaging failed */
    if (err == FIRC_ERR_NOENT) {
        firc_http_res_write_error(res, 409, "the running capture is not the one this token names");
        return;
    }
    write_status(res, ctx->app, NULL);
}

void firc_capture_register_routes(firc_httpd_t *h, firc_capture_ctx_t *ctx) {
    must_route(h, "GET", "/api/v1/system/capture", handle_get, ctx);
    must_route(h, "POST", "/api/v1/system/capture", handle_start, ctx);
    must_route(h, "DELETE", "/api/v1/system/capture", handle_stop, ctx);
}
