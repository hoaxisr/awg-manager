#include "firc/list_catalog.h"

#include <pthread.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <curl/curl.h>

#include "firc/json.h"
#include "firc/loop.h"
#include "firc/sub_fetch.h"

#include "groups_internal.h"

#define SOURCE_URL_PREFIX "https://raw.githubusercontent.com/"
#define ID_MAX 32
#define NAME_MAX_LEN 128

static firc_err_t refuse(char *why, size_t cap, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

static firc_err_t refuse(char *why, size_t cap, const char *fmt, ...) {
    if (why != NULL && cap > 0) {
        va_list ap;
        va_start(ap, fmt);
        vsnprintf(why, cap, fmt, ap);
        va_end(ap);
    }
    return FIRC_ERR_INVAL;
}

static bool id_ok(const char *s) {
    size_t n = strlen(s);
    if (n == 0 || n > ID_MAX) { return false; }
    for (; *s != '\0'; s++) {
        if (!((*s >= 'a' && *s <= 'z') || (*s >= '0' && *s <= '9') || *s == '-')) { return false; }
    }
    return true;
}

static bool name_ok(const char *s) {
    size_t n = strlen(s);
    if (n == 0 || n > NAME_MAX_LEN) { return false; }
    for (; *s != '\0'; s++) {
        char c = *s;
        bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' ||
                  c == '_' || c == '@' || c == '!' || c == '-';
        if (!ok) { return false; }
    }
    return true;
}

static int count_braces(const char *url) {
    int n = 0;
    for (const char *p = url; (p = strstr(p, "{}")) != NULL; p += 2) { n++; }
    return n;
}

static firc_err_t check_source(const cJSON *src, int at, cJSON *dst, char *why, size_t cap) {
    const cJSON *id = cJSON_GetObjectItemCaseSensitive(src, "id");
    const cJSON *url = cJSON_GetObjectItemCaseSensitive(src, "url");
    const cJSON *names = cJSON_GetObjectItemCaseSensitive(src, "names");
    if (!cJSON_IsString(id) || !id_ok(id->valuestring)) {
        return refuse(why, cap, "source %d has no valid id", at + 1);
    }
    if (!cJSON_IsString(url)) { return refuse(why, cap, "source %d has no url", at + 1); }
    if (!firc_sub_url_is_supported(url->valuestring) ||
        strncmp(url->valuestring, SOURCE_URL_PREFIX, strlen(SOURCE_URL_PREFIX)) != 0) {
        return refuse(why, cap, "source %d has no url on raw.githubusercontent.com", at + 1);
    }
    int braces = count_braces(url->valuestring);
    if (braces == 0) { return refuse(why, cap, "source %d has no {} in its url", at + 1); }
    if (braces > 1) { return refuse(why, cap, "source %d has more than one {} in its url", at + 1); }
    if (!cJSON_IsArray(names)) { return refuse(why, cap, "source %d has no names array", at + 1); }
    int i = 0;
    const cJSON *name;
    cJSON_ArrayForEach(name, names) {
        if (!cJSON_IsString(name) || !name_ok(name->valuestring)) {
            return refuse(why, cap, "name %d of %s is not a rule-set name", i + 1, id->valuestring);
        }
        i++;
    }
    if (!cJSON_AddStringToObject(dst, "id", id->valuestring) ||
        !cJSON_AddStringToObject(dst, "url", url->valuestring)) {
        return FIRC_ERR_NOMEM;
    }
    cJSON *copy = cJSON_Duplicate(names, true);
    if (copy == NULL) { return FIRC_ERR_NOMEM; }
    if (!cJSON_AddItemToObject(dst, "names", copy)) {
        cJSON_Delete(copy);
        return FIRC_ERR_NOMEM;
    }
    return FIRC_OK;
}

firc_err_t firc_list_catalog_parse(const char *body, size_t len, cJSON **out, char *why, size_t why_cap) {
    *out = NULL;
    cJSON *root = cJSON_ParseWithLength(body, len);
    if (root == NULL) { return refuse(why, why_cap, "not JSON"); }
    const cJSON *sources = cJSON_GetObjectItemCaseSensitive(root, "sources");
    if (!cJSON_IsArray(sources)) {
        cJSON_Delete(root);
        return refuse(why, why_cap, "sources is not an array");
    }
    const cJSON *gen = cJSON_GetObjectItemCaseSensitive(root, "generated");
    cJSON *res = cJSON_CreateObject();
    cJSON *list = res != NULL ? cJSON_AddArrayToObject(res, "sources") : NULL;
    if (list == NULL || !cJSON_AddStringToObject(res, "generated", cJSON_IsString(gen) ? gen->valuestring : "")) {
        cJSON_Delete(res);
        cJSON_Delete(root);
        return FIRC_ERR_NOMEM;
    }
    firc_err_t err = FIRC_OK;
    int at = 0;
    const cJSON *src;
    cJSON_ArrayForEach(src, sources) {
        cJSON *dst = cJSON_CreateObject();
        if (dst == NULL) {
            err = FIRC_ERR_NOMEM;
            break;
        }
        cJSON_AddItemToArray(list, dst);
        err = check_source(src, at++, dst, why, why_cap);
        if (err != FIRC_OK) { break; }
    }
    cJSON_Delete(root);
    if (err != FIRC_OK) {
        cJSON_Delete(res);
        return err;
    }
    *out = res;
    return FIRC_OK;
}

#define CATALOG_FRESH_MS ((uint64_t)6 * 3600 * 1000)
#define CATALOG_FAILED_MS ((uint64_t)60 * 1000)
#define CATALOG_WHY_CAP 256
#define CATALOG_REASON_CAP (CURL_ERROR_SIZE + CATALOG_WHY_CAP)

typedef struct catalog_waiter catalog_waiter_t;
typedef struct catalog_fetch catalog_fetch_t;

struct catalog_waiter {
    list_catalog_t *cat;
    catalog_waiter_t *next;
    firc_http_deferred_t *d;
};

struct catalog_fetch {
    list_catalog_t *cat;
    firc_loop_t *loop;
    pthread_t thread;
    char *url;
    atomic_bool stopping;
    bool posted;
    bool joined;
    cJSON *parsed;
    firc_err_t err;
    char reason[CATALOG_REASON_CAP];
};

struct list_catalog {
    firc_groups_ctx_t *ctx;
    cJSON *copy;
    uint64_t fetched_ms;
    double fetched_at;
    bool failed;
    uint64_t failed_ms;
    char reason[CATALOG_REASON_CAP];
    catalog_fetch_t *fetch;
    catalog_waiter_t *waiters;
};

static uint64_t catalog_now(const firc_groups_ctx_t *ctx) {
    if (ctx->catalog_now_ms != NULL) { return ctx->catalog_now_ms(); }
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000 + (uint64_t)ts.tv_nsec / 1000000;
}

static void fetch_free(catalog_fetch_t *f) {
    free(f->url);
    cJSON_Delete(f->parsed);
    free(f);
}

static bool fetch_progress(void *ud, size_t bytes, size_t total) {
    (void)bytes;
    (void)total;
    catalog_fetch_t *f = ud;
    return !atomic_load(&f->stopping);
}

static void fetch_done(firc_loop_t *loop, void *ud);

static void fetch_drop(void *ud) {
    catalog_fetch_t *f = ud;
    if (!f->joined) {
        pthread_join(f->thread, NULL);
        f->joined = true;
    }
    fetch_free(f);
}

static void *fetch_thread(void *arg) {
    catalog_fetch_t *f = arg;
    char *body = NULL;
    size_t len = 0;
    long status = 0;
    char errbuf[CURL_ERROR_SIZE] = "";
    f->err = firc_sub_fetch_list_opts(f->url, FIRC_LIST_CATALOG_MAX_BYTES, &body, &len, fetch_progress, f, &status,
                                      errbuf, sizeof errbuf);
    if (f->err != FIRC_OK) {
        firc_sub_fetch_reason(f->err, status, errbuf, f->reason, sizeof f->reason);
    } else {
        char why[CATALOG_WHY_CAP] = "";
        f->err = firc_list_catalog_parse(body, len, &f->parsed, why, sizeof why);
        if (f->err == FIRC_ERR_INVAL) {
            snprintf(f->reason, sizeof f->reason, "catalog refused: %s", why);
        } else if (f->err != FIRC_OK) {
            snprintf(f->reason, sizeof f->reason, "%s", firc_err_str(f->err));
        }
    }
    free(body);
    f->posted = true;
    while (firc_loop_post_with_drop(f->loop, fetch_done, f, fetch_drop) != FIRC_OK) {
        if (atomic_load(&f->stopping)) {
            f->posted = false;
            return NULL;
        }
        struct timespec ts = {.tv_sec = 0, .tv_nsec = 20 * 1000 * 1000};
        nanosleep(&ts, NULL);
    }
    return NULL;
}

static uint8_t *catalog_answer(const list_catalog_t *cat, int *status, size_t *len) {
    cJSON *obj;
    if (cat->copy != NULL) {
        *status = 200;
        obj = cJSON_CreateObject();
        bool ok = obj != NULL &&
                  cJSON_AddItemReferenceToObject(obj, "generated",
                                                 cJSON_GetObjectItemCaseSensitive(cat->copy, "generated")) &&
                  cJSON_AddNumberToObject(obj, "fetchedAt", cat->fetched_at) != NULL &&
                  cJSON_AddBoolToObject(obj, "stale", cat->failed) != NULL &&
                  cJSON_AddItemReferenceToObject(obj, "sources",
                                                 cJSON_GetObjectItemCaseSensitive(cat->copy, "sources"));
        if (!ok) {
            cJSON_Delete(obj);
            obj = NULL;
        }
    } else {
        *status = 502;
        obj = firc_json_error(cat->reason);
    }
    char *dump = obj != NULL ? firc_json_dump(obj) : NULL;
    cJSON_Delete(obj);
    if (dump == NULL) {
        static const char fallback[] = "{\"error\":\"failed to encode response\"}";
        *status = 500;
        dump = strdup(fallback);
    }
    *len = dump != NULL ? strlen(dump) : 0;
    return (uint8_t *)dump;
}

static void fetch_done(firc_loop_t *loop, void *ud) {
    (void)loop;
    catalog_fetch_t *f = ud;
    pthread_join(f->thread, NULL);
    f->joined = true;
    list_catalog_t *cat = f->cat;
    cat->fetch = NULL;
    uint64_t now = catalog_now(cat->ctx);
    if (f->err == FIRC_OK) {
        cJSON_Delete(cat->copy);
        cat->copy = f->parsed;
        f->parsed = NULL;
        cat->fetched_ms = now;
        cat->fetched_at = (double)time(NULL);
        cat->failed = false;
    } else {
        cat->failed = true;
        cat->failed_ms = now;
        snprintf(cat->reason, sizeof cat->reason, "%s", f->reason);
        FIRC_WARN("failed to fetch the list catalog: %s", f->reason);
    }
    fetch_free(f);
    catalog_waiter_t *w = cat->waiters;
    cat->waiters = NULL;
    while (w != NULL) {
        catalog_waiter_t *next = w->next;
        int status;
        size_t len;
        uint8_t *body = catalog_answer(cat, &status, &len);
        firc_http_deferred_finish(w->d, status, "application/json; charset=utf-8", body, len);
        free(w);
        w = next;
    }
}

static void waiter_left(void *ud) {
    catalog_waiter_t *w = ud;
    for (catalog_waiter_t **link = &w->cat->waiters; *link != NULL; link = &(*link)->next) {
        if (*link == w) {
            *link = w->next;
            break;
        }
    }
    firc_http_deferred_drop(w->d);
    free(w);
}

static bool fetch_start(list_catalog_t *cat, firc_loop_t *loop) {
    catalog_fetch_t *f = calloc(1, sizeof(*f));
    const char *url = cat->ctx->catalog_url != NULL ? cat->ctx->catalog_url : FIRC_LIST_CATALOG_URL;
    if (f != NULL) { f->url = strdup(url); }
    if (f == NULL || f->url == NULL) {
        free(f);
        return false;
    }
    f->cat = cat;
    f->loop = loop;
    atomic_init(&f->stopping, false);
    if (pthread_create(&f->thread, NULL, fetch_thread, f) != 0) {
        fetch_free(f);
        return false;
    }
    cat->fetch = f;
    return true;
}

static void handle_catalog(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    firc_groups_ctx_t *ctx = ud;
    list_catalog_t *cat = ctx->catalog;
    if (cat == NULL) {
        cat = calloc(1, sizeof(*cat));
        if (cat == NULL) {
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        cat->ctx = ctx;
        ctx->catalog = cat;
    }
    uint64_t now = catalog_now(ctx);
    bool fresh = cat->copy != NULL && now - cat->fetched_ms < CATALOG_FRESH_MS;
    bool resting = cat->failed && now - cat->failed_ms < CATALOG_FAILED_MS;
    if (cat->fetch == NULL && (fresh || resting)) {
        int status;
        size_t len;
        uint8_t *body = catalog_answer(cat, &status, &len);
        firc_http_res_take(res, status, "application/json; charset=utf-8", body, len);
        return;
    }
    catalog_waiter_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (cat->fetch == NULL && !fetch_start(cat, firc_http_res_loop(res))) {
        free(w);
        firc_http_res_write_error(res, 500, "failed to start the catalog fetch");
        return;
    }
    w->cat = cat;
    w->d = firc_http_res_defer(res, waiter_left, w);
    if (w->d == NULL) {
        free(w);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    w->next = cat->waiters;
    cat->waiters = w;
}

void firc_list_catalog_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx) {
    must_route(h, "GET", "/api/v1/groups/list/catalog", handle_catalog, ctx);
}

void firc_list_catalog_close(firc_groups_ctx_t *ctx) {
    list_catalog_t *cat = ctx->catalog;
    if (cat == NULL) { return; }
    ctx->catalog = NULL;
    catalog_fetch_t *f = cat->fetch;
    if (f != NULL) {
        atomic_store(&f->stopping, true);
        pthread_join(f->thread, NULL);
        f->joined = true;
        if (!f->posted) { fetch_free(f); }
    }
    while (cat->waiters != NULL) {
        catalog_waiter_t *w = cat->waiters;
        cat->waiters = w->next;
        firc_http_deferred_drop(w->d);
        free(w);
    }
    cJSON_Delete(cat->copy);
    free(cat);
}
