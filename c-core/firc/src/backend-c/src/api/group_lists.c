#include "firc/groups.h"

#include <ctype.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

#include <cjson/cJSON.h>
#include <curl/curl.h>

#include "firc/bytebuf.h"
#include "firc/json.h"
#include "firc/sub_fetch.h"
#include "firc/subparse.h"

#include "groups_internal.h"

/* false with the answer already written when the id is bad, the group is not there, or it has no list */
static bool resolve_list(firc_groups_ctx_t *ctx, firc_http_req_t *req, firc_http_res_t *res,
                         firc_group_t **out) {
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return false; }
    firc_group_t *g = firc_ruleset_group_mut(rs);
    if (g->list == NULL) {
        firc_http_res_write_error(res, 404, "group has no list");
        return false;
    }
    *out = g;
    return true;
}

static cJSON *list_rule_to_json(const firc_sub_rules_t *rs, size_t i) {
    char id_buf[FIRC_ID_STR_LEN];
    firc_id_format(firc_sub_rules_id(rs, i), id_buf);
    cJSON *obj = cJSON_CreateObject();
    cJSON_AddStringToObject(obj, "id", id_buf);
    cJSON_AddStringToObject(obj, "rule", firc_sub_rules_text(rs, i));
    cJSON_AddStringToObject(obj, "type", firc_sub_rules_type(rs, i));
    cJSON_AddBoolToObject(obj, "enable", firc_sub_rules_enable(rs, i));
    const char *proto = firc_sub_rules_proto(rs, i), *ports = firc_sub_rules_ports(rs, i);
    if (proto != NULL) { cJSON_AddStringToObject(obj, "proto", proto); }
    if (ports != NULL) { cJSON_AddStringToObject(obj, "ports", ports); }
    return obj;
}

#define FIRC_LIST_PREVIEW_SAMPLE 100

/* counts over the whole list plus a bounded sample; dropped/unconstrained explain lines the parser couldn't use */
static cJSON *preview_json(const firc_sub_rules_t *rules, size_t dropped, size_t unconstrained) {
    cJSON *out = cJSON_CreateObject();
    cJSON *sample = cJSON_CreateArray();
    cJSON *by_type = cJSON_CreateObject();
    if (out == NULL || sample == NULL || by_type == NULL) {
        cJSON_Delete(out);
        cJSON_Delete(sample);
        cJSON_Delete(by_type);
        return NULL;
    }
    size_t n_sample = rules->n < FIRC_LIST_PREVIEW_SAMPLE ? rules->n : FIRC_LIST_PREVIEW_SAMPLE;
    for (size_t i = 0; i < n_sample; i++) { cJSON_AddItemToArray(sample, list_rule_to_json(rules, i)); }
    cJSON_AddItemToObject(out, "rules", sample);
    cJSON_AddNumberToObject(out, "total", (double)rules->n);
    for (size_t i = 0; i < rules->n; i++) {
        const char *type = firc_sub_rules_type(rules, i);
        cJSON *seen = cJSON_GetObjectItemCaseSensitive(by_type, type);
        if (seen != NULL) {
            cJSON_SetNumberValue(seen, cJSON_GetNumberValue(seen) + 1);
        } else {
            cJSON_AddNumberToObject(by_type, type, 1);
        }
    }
    cJSON_AddItemToObject(out, "byType", by_type);
    cJSON_AddNumberToObject(out, "dropped", (double)dropped);
    cJSON_AddNumberToObject(out, "unconstrained", (double)unconstrained);
    return out;
}

/* fetch+parse run on their own thread, never the loop: a slow list url would otherwise hold DNS for the whole LAN */
#define FIRC_LIST_PREVIEW_MAX 2

struct list_preview {
    firc_groups_ctx_t *ctx;
    list_preview_t *next;
    firc_loop_t *loop;
    pthread_t thread;
    char *url;
    firc_http_deferred_t *pending; /* loop thread only; NULL once released or on a failed defer */
    atomic_bool abort_fetch; /* set when the client leaves or the daemon stops; read by the progress callback */
    atomic_bool stopping; /* set only by the stop: the thread then gives up retrying a refused post */
    bool posted; /* owned by the thread until posted, then the loop; read by the stop only after the join */
    bool joined;
    int status;
    uint8_t *body;
    size_t body_len;
    firc_err_t fetch_err;
    firc_err_t parse_err;
};

static void preview_free(list_preview_t *p) {
    free(p->url);
    free(p->body);
    free(p);
}

static bool preview_progress(void *ud, size_t bytes, size_t total) {
    (void)bytes;
    (void)total;
    list_preview_t *p = ud;
    return !atomic_load(&p->abort_fetch);
}

/* deletes obj */
static void preview_set(list_preview_t *p, int status, cJSON *obj) {
    char *dump = obj != NULL ? firc_json_dump(obj) : NULL;
    cJSON_Delete(obj);
    if (dump == NULL) {
        static const char fallback[] = "{\"error\":\"failed to encode response\"}";
        p->status = 500;
        p->body = (uint8_t *)strdup(fallback);
        p->body_len = p->body != NULL ? sizeof(fallback) - 1 : 0;
        return;
    }
    p->status = status;
    p->body = (uint8_t *)dump;
    p->body_len = strlen(dump);
}

static void preview_done(firc_loop_t *loop, void *ud);

/* only reached after the stop took the preview off its list, joined the thread and dropped the answer */
static void preview_drop(void *ud) {
    list_preview_t *p = ud;
    if (!p->joined) {
        pthread_join(p->thread, NULL);
        p->joined = true;
    }
    firc_http_deferred_drop(p->pending);
    preview_free(p);
}

static void *preview_thread(void *arg) {
    list_preview_t *p = arg;
    char *body = NULL;
    size_t body_len = 0;
    long status = 0;
    char errbuf[CURL_ERROR_SIZE] = "";
    p->fetch_err = firc_sub_fetch_list_opts(p->url, FIRC_SUB_FETCH_MAX_BODY_BYTES, &body, &body_len,
                                            preview_progress, p, &status, errbuf, sizeof errbuf);
    p->parse_err = FIRC_OK;
    if (p->fetch_err != FIRC_OK) {
        char why[CURL_ERROR_SIZE + 32];
        firc_sub_fetch_reason(p->fetch_err, status, errbuf, why, sizeof why);
        preview_set(p, 502, firc_json_error(why));
    } else {
        firc_sub_rules_t rules;
        firc_sub_rules_init(&rules);
        firc_sub_parse_stats_t st;
        p->parse_err = firc_sub_parse_rules_stats(body, body_len, &rules, &st, NULL, NULL);
        if (p->parse_err == FIRC_ERR_INVAL) {
            preview_set(p, 422, firc_json_error(st.why));
        } else if (p->parse_err != FIRC_OK) {
            preview_set(p, 500, firc_json_error(firc_err_str(p->parse_err)));
        } else {
            preview_set(p, 200, preview_json(&rules, st.dropped, st.unconstrained));
        }
        firc_sub_rules_free(&rules);
    }
    free(body);

    /* nothing of p is touched after a post the loop took; a full queue is waited out, not given up on */
    p->posted = true;
    while (firc_loop_post_with_drop(p->loop, preview_done, p, preview_drop) != FIRC_OK) {
        if (atomic_load(&p->stopping)) {
            p->posted = false;
            return NULL;
        }
        struct timespec ts = {.tv_sec = 0, .tv_nsec = 20 * 1000 * 1000};
        nanosleep(&ts, NULL);
    }
    return NULL;
}

static void preview_unlink(firc_groups_ctx_t *ctx, list_preview_t *p) {
    for (list_preview_t **link = &ctx->previews; *link != NULL; link = &(*link)->next) {
        if (*link == p) {
            *link = p->next;
            ctx->n_previews--;
            return;
        }
    }
}

/* loop thread; the join is short since the thread has already posted and is on its way out */
static void preview_done(firc_loop_t *loop, void *ud) {
    (void)loop;
    list_preview_t *p = ud;
    pthread_join(p->thread, NULL);
    p->joined = true;
    preview_unlink(p->ctx, p);
    if (p->fetch_err != FIRC_OK && p->fetch_err != FIRC_ERR_CANCELED) {
        FIRC_WARN("failed to fetch a list to preview: %s", firc_err_str(p->fetch_err));
    }
    if (p->pending != NULL) {
        firc_http_deferred_finish(p->pending, p->status, "application/json; charset=utf-8", p->body,
                                  p->body_len);
        p->pending = NULL;
        p->body = NULL;
    }
    preview_free(p);
}

/* calls the fetch off; the slot returns once the thread notices via its progress callback */
static void preview_client_left(void *ud) {
    list_preview_t *p = ud;
    atomic_store(&p->abort_fetch, true);
}

static void handle_preview(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    const char *url = firc_http_req_query(req, "url");
    if (!url || url[0] == '\0') {
        firc_http_res_write_error(res, 400, "list url is required");
        return;
    }
    if (ctx->n_previews >= FIRC_LIST_PREVIEW_MAX) {
        firc_http_res_write_error(res, 503, "too many list previews at once");
        return;
    }
    list_preview_t *p = calloc(1, sizeof(*p));
    if (p != NULL) { p->url = strdup(url); }
    if (p == NULL || p->url == NULL) {
        free(p);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    p->ctx = ctx;
    p->loop = firc_http_res_loop(res);
    atomic_init(&p->abort_fetch, false);
    atomic_init(&p->stopping, false);
    if (pthread_create(&p->thread, NULL, preview_thread, p) != 0) {
        preview_free(p);
        firc_http_res_write_error(res, 500, "failed to start the preview");
        return;
    }
    /* after the thread starts: its result posts to this loop, so it cannot arrive before pending is set */
    p->next = ctx->previews;
    ctx->previews = p;
    ctx->n_previews++;
    p->pending = firc_http_res_defer(res, preview_client_left, p);
    if (p->pending == NULL) {
        atomic_store(&p->abort_fetch, true);
        firc_http_res_write_error(res, 500, "out of memory");
    }
}

/* ASCII case-insensitive; stored text is already folded except for regex, so nothing is allocated here */
static bool ci_contains(const char *hay, const char *needle, size_t needle_len) {
    if (needle_len == 0) { return true; }
    for (const char *p = hay; *p != '\0'; p++) {
        size_t i = 0;
        while (i < needle_len && p[i] != '\0' && tolower((unsigned char)p[i]) == (unsigned char)needle[i]) {
            i++;
        }
        if (i == needle_len) { return true; }
    }
    return false;
}

#define FIRC_LIST_PAGE_DEFAULT 50
#define FIRC_LIST_PAGE_MAX 500

/* each matching rule is dumped straight into the answer's bytes: a cJSON tree of the whole list has killed the router */
static void handle_get_page(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_group_t *g;
    if (!resolve_list(ud, req, res, &g)) { return; }
    size_t offset = 0, limit = FIRC_LIST_PAGE_DEFAULT;
    if (!firc_http_req_size(req, res, "offset", 0, &offset)) { return; }
    if (!firc_http_req_size(req, res, "limit", FIRC_LIST_PAGE_DEFAULT, &limit)) { return; }
    if (limit == 0) { limit = FIRC_LIST_PAGE_DEFAULT; } /* limit=0 means the default, not none */
    if (limit > FIRC_LIST_PAGE_MAX) { limit = FIRC_LIST_PAGE_MAX; }

    const char *q = firc_http_req_query(req, "q");
    char *needle = NULL;
    size_t needle_len = 0;
    if (q != NULL && q[0] != '\0') {
        needle = strdup(q);
        if (needle == NULL) {
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        needle_len = strlen(needle);
        for (size_t i = 0; i < needle_len; i++) { needle[i] = (char)tolower((unsigned char)needle[i]); }
    }

    const firc_sub_rules_t *rs = &g->list->rules;
    firc_bytebuf_t b;
    firc_bytebuf_init(&b);
    firc_err_t err = firc_bytebuf_append_str(&b, "{\"rules\":[");
    size_t matched = 0, written = 0;
    for (size_t i = 0; err == FIRC_OK && i < rs->n; i++) {
        if (needle_len > 0 && !ci_contains(firc_sub_rules_text(rs, i), needle, needle_len)) { continue; }
        if (matched >= offset && written < limit) {
            cJSON *o = list_rule_to_json(rs, i);
            char *d = o != NULL ? firc_json_dump(o) : NULL;
            cJSON_Delete(o);
            if (d == NULL) {
                err = FIRC_ERR_NOMEM;
                break;
            }
            if (written > 0) { err = firc_bytebuf_append_byte(&b, ','); }
            if (err == FIRC_OK) { err = firc_bytebuf_append_str(&b, d); }
            free(d);
            written++;
        }
        matched++;
    }
    if (err == FIRC_OK) {
        char tail[128];
        snprintf(tail, sizeof(tail), "],\"total\":%zu,\"matched\":%zu,\"offset\":%zu}", rs->n, matched,
                 offset);
        err = firc_bytebuf_append_str(&b, tail);
    }
    free(needle);
    firc_http_res_take_json(res, &b, err);
}

/* carries only the edited rules; every refusal is found before any edit applies */
static void handle_patch_rules(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_group_t *g;
    if (!resolve_list(ctx, req, res, &g)) { return; }
    firc_id_t gid = g->id;
    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    cJSON *rules_j = cJSON_GetObjectItemCaseSensitive(json, "rules");
    if (!rules_j || !cJSON_IsArray(rules_j)) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "no rules in request");
        return;
    }
    size_t n = (size_t)cJSON_GetArraySize(rules_j);
    firc_list_rule_edit_t *edits = n > 0 ? calloc(n, sizeof(*edits)) : NULL;
    if (n > 0 && edits == NULL) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    const char *bad = NULL;
    size_t i = 0;
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, rules_j) {
        if (i >= n) { break; } /* n is the array's size; said for the analyzer */
        bool has_id = false;
        if (parse_optional_id(e, "id", &edits[i].rule, &has_id) != FIRC_OK || !has_id) {
            bad = "each rule edit needs an id";
            break;
        }
        if (cJSON_GetObjectItemCaseSensitive(e, "rule") != NULL) {
            bad = "a list rule's pattern comes from its list and cannot be edited";
            break;
        }
        if (cJSON_GetObjectItemCaseSensitive(e, "proto") != NULL ||
            cJSON_GetObjectItemCaseSensitive(e, "ports") != NULL) {
            bad = "a list rule's protocol and ports come from its list and cannot be edited";
            break;
        }
        if (cJSON_GetObjectItemCaseSensitive(e, "comment") != NULL) {
            bad = "a list rule has no comment";
            break;
        }
        get_optional_bool(e, "enable", &edits[i].enable, &edits[i].has_enable);
        cJSON *type_j = cJSON_GetObjectItemCaseSensitive(e, "type");
        edits[i].type = cJSON_IsString(type_j) ? type_j->valuestring : NULL; /* borrowed from json */
        i++;
    }
    if (bad != NULL) {
        free(edits);
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, bad);
        return;
    }
    char msg[384] = "invalid rule edit";
    firc_err_t err = firc_app_patch_list_rules(ctx->app, gid, edits, n, msg, sizeof(msg));
    free(edits);
    cJSON_Delete(json);
    if (err == FIRC_ERR_NOENT) {
        firc_http_res_write_error(res, 404, "rule not found");
        return;
    }
    if (err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 400, msg);
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    if (!firc_groups_maybe_save(ctx, req, res)) { return; }
    cJSON *ok = cJSON_CreateObject();
    cJSON_AddStringToObject(ok, "status", "ok");
    firc_http_res_write_json(res, 200, ok);
}

/* one entry per open connection, loop thread only; a write finding the client gone frees the entry from inside itself */
struct list_subscriber {
    firc_id_t group_id;
    firc_http_stream_t *stream;
    firc_groups_ctx_t *ctx;
    struct list_subscriber *next;
};

static void subscriber_unlink(firc_groups_ctx_t *ctx, list_subscriber_t *e) {
    for (list_subscriber_t **p = &ctx->subscribers; *p != NULL; p = &(*p)->next) {
        if (*p == e) {
            *p = e->next;
            free(e);
            return;
        }
    }
}

static void on_subscriber_close(void *ud) {
    list_subscriber_t *e = ud;
    subscriber_unlink(e->ctx, e);
}

/* caller frees b whatever this returns; a failure to build leaves the connection alive, unlike a failure to send */
static firc_err_t build_event(firc_bytebuf_t *b, const char *event, const char *json) {
    firc_err_t err = firc_bytebuf_append_str(b, "event: ");
    if (err == FIRC_OK) { err = firc_bytebuf_append_str(b, event); }
    if (err == FIRC_OK) { err = firc_bytebuf_append_str(b, "\ndata: "); }
    if (err == FIRC_OK) { err = firc_bytebuf_append_str(b, json); }
    if (err == FIRC_OK) { err = firc_bytebuf_append_str(b, "\n\n"); }
    return err;
}

/* done means the job finished: last event, stream closes after; error is read off the list alone, owner may be gone */
static bool subscriber_send(list_subscriber_t *e, firc_id_t owner, const firc_group_list_t *list, bool done) {
    const char *name;
    cJSON *payload;
    if (!done) {
        name = "progress";
        payload = firc_api_list_progress_json(list);
    } else if (list->sync_state == FIRC_SUB_SYNC_ERROR) {
        name = "error";
        payload = cJSON_CreateObject();
        if (payload != NULL) { cJSON_AddStringToObject(payload, "error", list->sync_error); }
    } else {
        name = "done";
        firc_ruleset_t *rs = firc_app_find_group_by_id(e->ctx->app, owner);
        payload = rs != NULL ? firc_groups_group_json(e->ctx->app, firc_ruleset_group(rs), false) : NULL;
    }
    char *text = payload != NULL ? firc_json_dump(payload) : NULL;
    cJSON_Delete(payload);

    firc_bytebuf_t b;
    firc_bytebuf_init(&b);
    firc_err_t built = text != NULL ? build_event(&b, name, text) : FIRC_ERR_NOMEM;
    free(text);
    if (built != FIRC_OK) {
        firc_bytebuf_free(&b);
        FIRC_WARN("a list sync event could not be built: %s", firc_err_str(built));
        firc_http_stream_close(e->stream);
        subscriber_unlink(e->ctx, e);
        return false;
    }
    firc_err_t sent = firc_http_stream_write(e->stream, b.data, b.len);
    firc_bytebuf_free(&b);
    /* e is already freed via on_subscriber_close by now; do not touch it */
    if (sent != FIRC_OK) { return false; }
    if (!done) { return true; }
    firc_http_stream_close(e->stream);
    subscriber_unlink(e->ctx, e);
    return false;
}

/* every stream watching owner is told, even after the group left the config (a rollback can still end its stream) */
static void sync_listener(void *ud, firc_id_t owner, const firc_group_list_t *list, bool done) {
    firc_groups_ctx_t *ctx = ud;
    list_subscriber_t *e = ctx->subscribers;
    while (e != NULL) {
        list_subscriber_t *next = e->next; /* read before the send, which may free e */
        if (firc_id_equal(e->group_id, owner)) { (void)subscriber_send(e, owner, list, done); }
        e = next;
    }
}

/* holds the connection; the first event restates current state so a late/reconnecting client never waits forever */
static void handle_get_sync_events(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_group_t *g;
    if (!resolve_list(ctx, req, res, &g)) { return; }

    list_subscriber_t *e = calloc(1, sizeof(*e));
    if (e == NULL) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    e->group_id = g->id;
    e->ctx = ctx;
    e->next = ctx->subscribers;
    ctx->subscribers = e;

    firc_http_res_set_header(res, "Content-Type", "text/event-stream");
    firc_http_res_set_header(res, "Cache-Control", "no-cache");
    e->stream = firc_http_res_hold(res, on_subscriber_close, e);
    if (e->stream == NULL) {
        subscriber_unlink(ctx, e);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    bool running = g->list->sync_state == FIRC_SUB_SYNC_QUEUED || g->list->sync_state == FIRC_SUB_SYNC_FETCHING;
    (void)subscriber_send(e, g->id, g->list, !running);
}

void firc_groups_api_close_streams(firc_groups_ctx_t *ctx) {
    if (ctx == NULL) { return; }
    /* previews first: each fetch is called off and joined so nothing posts to the loop afterward */
    while (ctx->previews != NULL) {
        list_preview_t *p = ctx->previews;
        ctx->previews = p->next;
        ctx->n_previews--;
        atomic_store(&p->stopping, true);
        atomic_store(&p->abort_fetch, true);
        pthread_join(p->thread, NULL);
        p->joined = true;
        firc_http_deferred_drop(p->pending);
        p->pending = NULL;
        if (!p->posted) { preview_free(p); }
    }
    firc_list_catalog_close(ctx);
    while (ctx->subscribers != NULL) {
        list_subscriber_t *e = ctx->subscribers;
        ctx->subscribers = e->next;
        /* not through subscriber_send: this close does not call on_subscriber_close, so the entry frees exactly once, here */
        firc_http_stream_close(e->stream);
        free(e);
    }
}

/* an optional {"url"} body is stored before the enqueue: the job is built from the list, a new url must be on it */
static void handle_sync(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_group_t *g;
    if (!resolve_list(ctx, req, res, &g)) { return; }
    firc_id_t id = g->id;

    size_t body_len;
    (void)firc_http_req_body(req, &body_len);
    if (body_len > 0) {
        cJSON *json = parse_body_json(req);
        if (!json) {
            firc_http_res_write_error(res, 400, "failed to parse request");
            return;
        }
        const char *url = get_string(json, "url");
        firc_err_t uerr = url[0] != '\0' ? firc_app_set_list_url(ctx->app, id, url) : FIRC_OK;
        cJSON_Delete(json);
        if (uerr == FIRC_ERR_NOENT) {
            firc_http_res_write_error(res, 404, "group has no list");
            return;
        }
        if (uerr == FIRC_ERR_INVAL) {
            firc_http_res_write_error(res, 400, "list url is not supported");
            return;
        }
        if (uerr != FIRC_OK) {
            firc_http_res_write_error(res, 500, firc_err_str(uerr));
            return;
        }
    }

    firc_err_t err = firc_app_request_sync(ctx->app, id);
    if (err == FIRC_ERR_NOENT) {
        firc_http_res_write_error(res, 404, "group has no list");
        return;
    }
    if (err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 400, "list has no url to fetch");
        return;
    }
    if (err == FIRC_ERR_STATE || err == FIRC_ERR_LIMIT) {
        /* no worker, or the queue is full: a 202 would put the WebUI on a stream for a job that never runs */
        firc_http_res_write_error(res, 503, "sync unavailable");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    /* the job is accepted regardless; only the record below can still fail, answered as an error so the WebUI rereads it */
    cJSON *record = firc_groups_group_json(ctx->app, g, false);
    if (record == NULL) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 202, record);
}

void firc_group_lists_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx) {
    must_route(h, "GET", "/api/v1/groups/list/preview", handle_preview, ctx);
    firc_list_catalog_register_routes(h, ctx);
    must_route(h, "GET", "/api/v1/groups/{groupID}/list/rules", handle_get_page, ctx);
    must_route(h, "PATCH", "/api/v1/groups/{groupID}/list/rules", handle_patch_rules, ctx);
    must_route(h, "POST", "/api/v1/groups/{groupID}/list/sync", handle_sync, ctx);
    must_route(h, "GET", "/api/v1/groups/{groupID}/list/sync/events", handle_get_sync_events, ctx);
    firc_app_set_sync_listener(ctx->app, sync_listener, ctx); /* one listener; both servers share ctx */
}
