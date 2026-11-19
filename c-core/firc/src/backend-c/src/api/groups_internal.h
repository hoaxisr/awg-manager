#ifndef FIRC_API_GROUPS_INTERNAL_H
#define FIRC_API_GROUPS_INTERNAL_H

#include <stdbool.h>
#include <stdint.h>

#include <cjson/cJSON.h>

#include "firc/groups.h"
#include "firc/id.h"
#include "firc/log.h"

/* *out_present false if absent/null; FIRC_ERR_INVAL if present but not an 8 hex char string */
static inline firc_err_t parse_optional_id(const cJSON *obj, const char *key, firc_id_t *out,
                                           bool *out_present) {
    *out_present = false;
    cJSON *item = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!item || cJSON_IsNull(item)) { return FIRC_OK; }
    if (!cJSON_IsString(item) || firc_id_parse(item->valuestring, out) != FIRC_OK) { return FIRC_ERR_INVAL; }
    *out_present = true;
    return FIRC_OK;
}

/* anything that is not a string reads as "" */
static inline const char *get_string(const cJSON *obj, const char *key) {
    cJSON *item = cJSON_GetObjectItemCaseSensitive(obj, key);
    return cJSON_IsString(item) ? item->valuestring : "";
}

/* *out_present false when absent, null or not a bool */
static inline void get_optional_bool(const cJSON *obj, const char *key, bool *out_val, bool *out_present) {
    cJSON *item = cJSON_GetObjectItemCaseSensitive(obj, key);
    *out_present = item && cJSON_IsBool(item);
    if (*out_present) { *out_val = cJSON_IsTrue(item); }
}

/* name "must_route" is read literally by tools/ci/check_swagger_routes.py; do not rename */
static inline void must_route(firc_httpd_t *h, const char *method, const char *pattern,
                              firc_http_handler_fn fn, void *ud) {
    if (firc_httpd_route(h, method, pattern, fn, ud) != FIRC_OK) {
        FIRC_ERROR("failed to register route %s %s", method, pattern);
    }
}

/* NULL for an empty or unparsable body */
static inline cJSON *parse_body_json(firc_http_req_t *req) {
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    return body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
}

/* resolves {groupID}; on false a 400 or 404 is already written to res */
bool firc_groups_resolve(firc_groups_ctx_t *ctx, firc_http_req_t *req, firc_http_res_t *res,
                         firc_ruleset_t **out);

/* a group's record; NULL on OOM (fails whole); app nullable, omits the read-only resolver block */
cJSON *firc_groups_group_json(firc_app_t *app, const firc_group_t *g, bool with_rules);

/* writes the groups file when the request says ?save=true; false after answering 500 when the write failed */
bool firc_groups_maybe_save(firc_groups_ctx_t *ctx, firc_http_req_t *req, firc_http_res_t *res);

/* must be called before firc_groups_register_routes, so the literal /groups/list/preview matches first */
void firc_group_lists_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx);

void firc_list_catalog_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx);
void firc_list_catalog_close(firc_groups_ctx_t *ctx);

#endif /* FIRC_API_GROUPS_INTERNAL_H */
