#include "firc/update_api.h"

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <cjson/cJSON.h>

#include "firc/json.h"
#include "firc/utf8.h"
#include "firc/log.h"

#define JSON_TYPE "application/json; charset=utf-8"
#define NOT_RUNNING "the update checker is not running"

static void must_route(firc_httpd_t *h, const char *method, const char *pattern, firc_http_handler_fn fn,
                       void *ud) {
    if (firc_httpd_route(h, method, pattern, fn, ud) != FIRC_OK) {
        FIRC_ERROR("failed to register route %s %s", method, pattern);
    }
}

static bool put(cJSON *o, const char *key, cJSON *v) {
    if (v == NULL) { return false; }
    if (!cJSON_AddItemToObject(o, key, v)) {
        cJSON_Delete(v);
        return false;
    }
    return true;
}

static cJSON *text_or_null(const char *s) { return s != NULL ? cJSON_CreateString(s) : cJSON_CreateNull(); }

static bool names_its_asset(const firc_release_t *r) {
    if (r->asset.name[0] == '\0') { return false; }
    return r->reason == NULL || (strcmp(r->reason, "no-asset") != 0 && strcmp(r->reason, "many-assets") != 0);
}

static cJSON *asset_json(const firc_release_t *r) {
    if (!names_its_asset(r)) { return cJSON_CreateNull(); }
    cJSON *a = cJSON_CreateObject();
    if (a == NULL || !put(a, "name", cJSON_CreateString(r->asset.name)) ||
        !put(a, "size", cJSON_CreateNumber((double)r->asset.size))) {
        cJSON_Delete(a);
        return NULL;
    }
    return a;
}

static cJSON *release_json(const firc_release_t *r) {
    if (r == NULL) { return cJSON_CreateNull(); }
    cJSON *l = cJSON_CreateObject();
    if (l == NULL || !put(l, "version", cJSON_CreateString(r->known ? r->version_text : r->tag)) ||
        !put(l, "revision", cJSON_CreateNumber(r->known ? r->version.revision : 0)) ||
        !put(l, "tag", cJSON_CreateString(r->tag)) || !put(l, "publishedAt", cJSON_CreateString(r->published_at)) ||
        !put(l, "notes", cJSON_CreateString(r->notes != NULL ? r->notes : "")) ||
        !put(l, "asset", asset_json(r))) {
        cJSON_Delete(l);
        return NULL;
    }
    return l;
}

static cJSON *check_json(const firc_update_view_t *v) {
    if (!v->attempted) { return cJSON_CreateNull(); }
    const firc_release_t *r = v->latest;
    cJSON *c = cJSON_CreateObject();
    if (c == NULL || !put(c, "checkedAt", cJSON_CreateNumber((double)v->checked_at)) ||
        !put(c, "ageS", cJSON_CreateNumber((double)v->age_s)) || !put(c, "latest", release_json(r)) ||
        !put(c, "newer", cJSON_CreateBool(v->newer)) ||
        !put(c, "installable", cJSON_CreateBool(r != NULL && r->installable)) ||
        !put(c, "reason", text_or_null(r != NULL ? r->reason : NULL)) ||
        !put(c, "error", text_or_null(v->check_error))) {
        cJSON_Delete(c);
        return NULL;
    }
    return c;
}

static cJSON *progress_json(const firc_update_view_t *v) {
    if (v->state != FIRC_UPDATE_DOWNLOADING) { return cJSON_CreateNull(); }
    cJSON *p = cJSON_CreateObject();
    if (p == NULL || !put(p, "bytes", cJSON_CreateNumber((double)v->bytes)) ||
        !put(p, "total", cJSON_CreateNumber((double)v->total))) {
        cJSON_Delete(p);
        return NULL;
    }
    return p;
}

static cJSON *result_json(const firc_update_view_t *v) {
    if (!v->has_result) { return cJSON_CreateNull(); }
    cJSON *r = cJSON_CreateObject();
    if (r == NULL || !put(r, "version", cJSON_CreateString(v->result.version)) ||
        !put(r, "opkgExit", cJSON_CreateNumber(v->result.opkg_exit)) ||
        !put(r, "finishedAt", cJSON_CreateNumber((double)v->result.finished_at)) ||
        !put(r, "log", cJSON_CreateString(v->log))) {
        cJSON_Delete(r);
        return NULL;
    }
    return r;
}

static cJSON *status_json(firc_update_t *u) {
    firc_update_view_t v;
    firc_update_view(u, &v);
    cJSON *o = cJSON_CreateObject();
    cJSON *inst = cJSON_CreateObject();
    bool ok = o != NULL && inst != NULL && put(inst, "version", cJSON_CreateString(v.version)) &&
              put(inst, "revision", cJSON_CreateNumber(v.revision)) &&
              put(inst, "target", cJSON_CreateString(v.target));
    if (ok) {
        ok = put(o, "installed", inst);
        inst = NULL;
    }
    ok = ok && put(o, "canInstall", cJSON_CreateBool(v.cannot_install == NULL)) &&
         put(o, "reason", text_or_null(v.cannot_install)) && put(o, "check", check_json(&v)) &&
         put(o, "state", cJSON_CreateString(firc_update_state_name(v.state))) &&
         put(o, "progress", progress_json(&v)) && put(o, "error", text_or_null(v.error)) &&
         put(o, "result", result_json(&v));
    cJSON_Delete(inst);
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static void handle_get(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_update_api_ctx_t *ctx = ud;
    if (ctx->update == NULL) {
        firc_http_res_write_error(res, 503, NOT_RUNNING);
        return;
    }
    cJSON *o = status_json(ctx->update);
    if (o == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static void handle_check(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_update_api_ctx_t *ctx = ud;
    if (ctx->update == NULL) {
        firc_http_res_write_error(res, 503, NOT_RUNNING);
        return;
    }
    if (ctx->n_waiting >= FIRC_UPDATE_API_WAITING_MAX) {
        firc_http_res_write_error(res, 503, "too many checks waiting");
        return;
    }
    firc_err_t err = firc_update_check(ctx->update);
    if (err == FIRC_ERR_STATE) {
        firc_http_res_write_error(res, 409, "an install is under way");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    firc_http_deferred_t *d = firc_http_res_defer(res, NULL, NULL);
    if (d == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    ctx->waiting[ctx->n_waiting++] = d;
}

static cJSON *limited_json(int64_t reset_at) {
    cJSON *o = cJSON_CreateObject();
    char msg[96];
    firc_update_limited_text(reset_at, msg, sizeof(msg));
    if (o == NULL || !put(o, "error", cJSON_CreateString(msg)) ||
        !put(o, "resetAt", reset_at > 0 ? cJSON_CreateNumber((double)reset_at) : cJSON_CreateNull())) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static void write_refusal(firc_http_res_t *res, int status, const char *msg, const char *reason) {
    cJSON *o = firc_json_error(msg);
    if (o == NULL || (reason != NULL && !put(o, "reason", cJSON_CreateString(reason)))) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, status, o);
}

static void write_no_space(firc_http_res_t *res, const firc_update_install_ans_t *ans) {
    char msg[160];
    snprintf(msg, sizeof(msg), "the update directory's filesystem has %llu bytes free; the install needs %llu",
             (unsigned long long)ans->free_bytes, (unsigned long long)ans->need_bytes);
    cJSON *o = firc_json_error(msg);
    if (o == NULL || !put(o, "free", cJSON_CreateNumber((double)ans->free_bytes)) ||
        !put(o, "need", cJSON_CreateNumber((double)ans->need_bytes))) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, 507, o);
}

static void write_accepted(firc_http_res_t *res, const firc_update_install_ans_t *ans) {
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || !put(o, "version", cJSON_CreateString(ans->tag)) ||
        !put(o, "asset", cJSON_CreateString(ans->asset)) ||
        !put(o, "size", cJSON_CreateNumber((double)ans->size))) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    firc_http_res_write_json(res, 202, o);
}

static void handle_install(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_update_api_ctx_t *ctx = ud;
    if (ctx->update == NULL) {
        firc_http_res_write_error(res, 503, NOT_RUNNING);
        return;
    }
    firc_update_view_t v;
    firc_update_view(ctx->update, &v);
    char msg[256];
    if (v.cannot_install != NULL) {
        snprintf(msg, sizeof(msg), "this build cannot install updates: %s", v.cannot_install);
        write_refusal(res, 403, msg, v.cannot_install);
        return;
    }
    size_t body_len = 0;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    const cJSON *ver = cJSON_IsObject(json) ? cJSON_GetObjectItemCaseSensitive(json, "version") : NULL;
    const char *tag = cJSON_GetStringValue(ver);
    if (tag == NULL || tag[0] == '\0') {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "the body is {\"version\": \"<tag>\"}");
        return;
    }
    int shown = firc_utf8_clamp(tag, 64);
    if (ctx->sys != NULL && firc_system_restart_under_way(ctx->sys)) {
        firc_http_res_write_error(res, 409, "a restart is under way");
        cJSON_Delete(json);
        return;
    }
    firc_update_install_ans_t ans;
    switch (firc_update_install(ctx->update, tag, &ans)) {
    case FIRC_UPDATE_OK: write_accepted(res, &ans); break;
    case FIRC_UPDATE_CANNOT:
        snprintf(msg, sizeof(msg), "this build cannot install updates: %s", ans.reason);
        write_refusal(res, 403, msg, ans.reason);
        break;
    case FIRC_UPDATE_BUSY: firc_http_res_write_error(res, 409, "an install is under way"); break;
    case FIRC_UPDATE_CHECK_RUNNING:
        firc_http_res_write_error(res, 409, "a check is under way; try again in a moment");
        break;
    case FIRC_UPDATE_STALE_CHECK:
        snprintf(msg, sizeof(msg), "check again: the last check did not offer %.*s", shown, tag);
        firc_http_res_write_error(res, 409, msg);
        break;
    case FIRC_UPDATE_NOT_NEWER:
        snprintf(msg, sizeof(msg), "%.*s is not newer than %s", shown, tag, v.version);
        firc_http_res_write_error(res, 400, msg);
        break;
    case FIRC_UPDATE_NOT_INSTALLABLE:
        snprintf(msg, sizeof(msg), "%.*s cannot be installed: %s", shown, tag, ans.reason);
        write_refusal(res, 400, msg, ans.reason);
        break;
    case FIRC_UPDATE_NO_SPACE: write_no_space(res, &ans); break;
    case FIRC_UPDATE_IO: firc_http_res_write_error(res, 500, "the update directory could not be made"); break;
    }
    cJSON_Delete(json);
}

void firc_update_api_checked(void *ud, const firc_update_outcome_t *o) {
    firc_update_api_ctx_t *ctx = ud;
    size_t n = ctx->n_waiting;
    if (n == 0) { return; }
    firc_http_deferred_t *waiting[FIRC_UPDATE_API_WAITING_MAX];
    memcpy(waiting, ctx->waiting, n * sizeof(waiting[0]));
    ctx->n_waiting = 0;
    int status = 200;
    cJSON *body = NULL;
    if (o->err == FIRC_OK) {
        body = status_json(ctx->update);
    } else if (o->err == FIRC_ERR_AGAIN) {
        status = 429;
        body = limited_json(o->reset_at);
    } else {
        status = 502;
        body = firc_json_error(o->message);
    }
    char *text = body != NULL ? firc_json_dump(body) : NULL;
    cJSON_Delete(body);
    if (text == NULL) {
        status = 500;
        text = strdup("{\"error\":\"out of memory\"}");
    }
    size_t len = text != NULL ? strlen(text) : 0;
    for (size_t i = 0; i < n; i++) {
        uint8_t *copy = NULL;
        if (text != NULL) {
            copy = malloc(len);
            if (copy != NULL) { memcpy(copy, text, len); }
        }
        firc_http_deferred_finish(waiting[i], copy != NULL ? status : 500, JSON_TYPE, copy, copy != NULL ? len : 0);
    }
    free(text);
}

void firc_update_api_close(firc_update_api_ctx_t *ctx) {
    if (ctx == NULL) { return; }
    size_t n = ctx->n_waiting;
    ctx->n_waiting = 0;
    for (size_t i = 0; i < n; i++) { firc_http_deferred_drop(ctx->waiting[i]); }
}

void firc_update_register_routes(firc_httpd_t *h, firc_update_api_ctx_t *ctx) {
    must_route(h, "GET", "/api/v1/system/update", handle_get, ctx);
    must_route(h, "POST", "/api/v1/system/update/check", handle_check, ctx);
    must_route(h, "POST", "/api/v1/system/update/install", handle_install, ctx);
}
