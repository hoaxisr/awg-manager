#include "firc/staticfiles.h"

#include <errno.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>

static const char NO_SKIN_PLACEHOLDER[] =
    "<!DOCTYPE html><html><head><title>firc</title></head><body><h1>firc</h1>"
    "<p>Please install firc skin before using WebUI!</p></body></html>";

static const char *content_type_for_ext(const char *path) {
    const char *dot = strrchr(path, '.');
    if (!dot) { return "text/plain"; }
    if (strcasecmp(dot, ".html") == 0) { return "text/html"; }
    if (strcasecmp(dot, ".css") == 0) { return "text/css"; }
    if (strcasecmp(dot, ".js") == 0) { return "application/javascript"; }
    if (strcasecmp(dot, ".ico") == 0) { return "image/x-icon"; }
    if (strcasecmp(dot, ".png") == 0) { return "image/png"; }
    if (strcasecmp(dot, ".svg") == 0) { return "image/svg+xml"; }
    return "text/plain";
}

static bool is_tab_path(const char *path) {
    static const char *const tabs[] = {"/groups",        "/tunnels",         "/journal",
                                       "/journal/stats", "/journal/unopened", "/journal/capture", "/settings"};
    for (size_t i = 0; i < sizeof(tabs) / sizeof(tabs[0]); i++) {
        if (strcmp(path, tabs[i]) == 0) { return true; }
    }
    return false;
}

void firc_static_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_static_ctx_t *ctx = ud;
    if (strcmp(firc_http_req_method(req), "GET") != 0) {
        firc_http_res_write_error(res, 404, "not found");
        return;
    }

    const char *original = firc_http_req_path(req);
    if (is_tab_path(original)) { original = "/"; }

    char file_path[1024];
    if (snprintf(file_path, sizeof(file_path), "%s%s", ctx->root, original) >=
        (int)sizeof(file_path)) {
        firc_http_res_write_error(res, 500, "path too long");
        return;
    }

    /* a directory retries once with /index.html appended; a second directory falls through to a doomed fopen() below */
    struct stat st;
    for (int i = 0; i < 2; i++) {
        if (stat(file_path, &st) != 0) {
            if (errno == ENOENT) {
                if (strcmp(original, "/") == 0) {
                    firc_http_res_write(res, 404, "text/html", (const uint8_t *)NO_SKIN_PLACEHOLDER,
                                      sizeof(NO_SKIN_PLACEHOLDER) - 1);
                } else {
                    firc_http_res_write_error(res, 404, "file not found");
                }
                return;
            }
            firc_http_res_write_error(res, 500, "failed to stat file");
            return;
        }
        if (S_ISDIR(st.st_mode)) {
            size_t len = strlen(file_path);
            if (snprintf(file_path + len, sizeof(file_path) - len, "/index.html") >=
                (int)(sizeof(file_path) - len)) {
                firc_http_res_write_error(res, 500, "path too long");
                return;
            }
            continue;
        }
        break;
    }

    FILE *f = fopen(file_path, "rbe");
    if (!f) {
        firc_http_res_write_error(res, 500, "failed to read file");
        return;
    }
    /* fstat the opened fd: the loop's last st can describe a stale path in the double-directory case above */
    struct stat fst;
    if (fstat(fileno(f), &fst) != 0) {
        fclose(f);
        firc_http_res_write_error(res, 500, "failed to read file");
        return;
    }
    size_t size = (size_t)fst.st_size;
    uint8_t *data = malloc(size > 0 ? size : 1);
    if (!data) {
        fclose(f);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    size_t n = fread(data, 1, size, f);
    fclose(f);
    if (n != size) {
        free(data);
        firc_http_res_write_error(res, 500, "failed to read file");
        return;
    }
    firc_http_res_write(res, 200, content_type_for_ext(file_path), data, size);
    free(data);
}
