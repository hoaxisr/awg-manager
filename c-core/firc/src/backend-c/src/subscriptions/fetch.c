#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */
#include "firc/sub_fetch.h"

#include <errno.h>
#include <fcntl.h>
#include <net/if.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include <curl/curl.h>

#include "firc/log.h"
#include "firc/subparse.h"
#include "firc/version.h"

static firc_sub_setsockopt_fn g_setsockopt = setsockopt;

void firc_sub_fetch_set_setsockopt_for_test(firc_sub_setsockopt_fn fn) { g_setsockopt = fn != NULL ? fn : setsockopt; }

typedef struct sock_cfg {
    uint32_t mark;
    bool mark_refused;
} sock_cfg_t;

static int cloexec_cb(void *ud, curl_socket_t fd, curlsocktype purpose)
{
    (void)purpose;
    sock_cfg_t *cfg = ud;
    int fl = fcntl(fd, F_GETFD);
    if (fl >= 0) { (void)fcntl(fd, F_SETFD, fl | FD_CLOEXEC); }
    if (cfg != NULL && cfg->mark != 0 && g_setsockopt(fd, SOL_SOCKET, SO_MARK, &cfg->mark, sizeof cfg->mark) != 0) {
        cfg->mark_refused = true;
        return CURL_SOCKOPT_ERROR;
    }
    return CURL_SOCKOPT_OK;
}

void firc_sub_url_redact(const char *url, char *buf, size_t cap)
{
    if (cap == 0) { return; }
    snprintf(buf, cap, "(bad url)");
    CURLU *cu = curl_url();
    if (cu == NULL) { return; }
    char *scheme = NULL;
    char *host = NULL;
    if (url != NULL && curl_url_set(cu, CURLUPART_URL, url, 0) == CURLUE_OK &&
        curl_url_get(cu, CURLUPART_SCHEME, &scheme, 0) == CURLUE_OK &&
        curl_url_get(cu, CURLUPART_HOST, &host, 0) == CURLUE_OK) {
        snprintf(buf, cap, "%s://%s/…", scheme, host);
    }
    curl_free(scheme);
    curl_free(host);
    curl_url_cleanup(cu);
}

void firc_sub_fetch_global_init(void) { curl_global_init(CURL_GLOBAL_DEFAULT); }

void firc_sub_fetch_global_cleanup(void) { curl_global_cleanup(); }

bool firc_sub_url_is_supported(const char *url) {
    size_t skip = strncasecmp(url, "http://", 7) == 0 ? 7 : strncasecmp(url, "https://", 8) == 0 ? 8 : 0;
    if (skip == 0 || url[skip] == '/') { return false; }
    CURLU *cu = curl_url();
    if (!cu) { return false; }
    bool ok = false;
    if (curl_url_set(cu, CURLUPART_URL, url, 0) == CURLUE_OK) {
        char *scheme = NULL;
        char *host = NULL;
        curl_url_get(cu, CURLUPART_SCHEME, &scheme, 0);
        curl_url_get(cu, CURLUPART_HOST, &host, 0);
        ok = scheme && host && host[0] != '\0' &&
             (strcasecmp(scheme, "http") == 0 || strcasecmp(scheme, "https") == 0);
        curl_free(scheme);
        curl_free(host);
    }
    curl_url_cleanup(cu);
    return ok;
}

typedef struct xfer {
    CURL *curl;
    firc_sub_fetch_sink_fn sink;
    void *sink_ud;
    const firc_sub_fetch_opts_t *o;
    size_t delivered;
    size_t drained;
    bool drain_over_cap;
    bool sink_stopped;
    bool aborted;
} xfer_t;

static size_t write_cb(char *ptr, size_t size, size_t nmemb, void *ud) {
    xfer_t *x = ud;
    size_t n = size * nmemb;
    long status = 0;
    curl_easy_getinfo(x->curl, CURLINFO_RESPONSE_CODE, &status);
    if (status < 200 || status > 299) {
        if (n > FIRC_SUB_FETCH_MAX_BODY_BYTES - x->drained) {
            x->drain_over_cap = true;
            return 0;
        }
        x->drained += n;
        return n;
    }
    if (!x->sink(x->sink_ud, (const uint8_t *)ptr, n)) {
        x->sink_stopped = true;
        return 0;
    }
    x->delivered += n;
    return n;
}

static size_t header_cb(char *ptr, size_t size, size_t nitems, void *ud) {
    xfer_t *x = ud;
    size_t n = size * nitems;
    char *colon = memchr(ptr, ':', n);
    if (colon == NULL || colon == ptr) { return n; }
    char *line = malloc(n + 1);
    if (line == NULL) { return n; }
    memcpy(line, ptr, n);
    line[n] = '\0';
    char *sep = line + (colon - ptr);
    *sep = '\0';
    char *value = sep + 1;
    while (*value == ' ' || *value == '\t') { value++; }
    size_t vlen = strlen(value);
    while (vlen > 0 && (value[vlen - 1] == '\r' || value[vlen - 1] == '\n' || value[vlen - 1] == ' ' ||
                        value[vlen - 1] == '\t')) {
        value[--vlen] = '\0';
    }
    x->o->on_header(x->o->header_ud, line, value);
    free(line);
    return n;
}

static int xferinfo_cb(void *ud, curl_off_t dltotal, curl_off_t dlnow, curl_off_t ultotal, curl_off_t ulnow) {
    (void)ultotal;
    (void)ulnow;
    xfer_t *x = ud;
    if (x->o->progress == NULL) { return 0; }
    if (!x->o->progress(x->o->progress_ud, (size_t)(dlnow < 0 ? 0 : dlnow), (size_t)(dltotal < 0 ? 0 : dltotal))) {
        x->aborted = true;
        return 1;
    }
    return 0;
}

typedef struct visited_set {
    char **urls;
    size_t n;
    size_t cap;
} visited_set_t;

static bool visited_contains(const visited_set_t *v, const char *url) {
    for (size_t i = 0; i < v->n; i++) {
        if (strcmp(v->urls[i], url) == 0) { return true; }
    }
    return false;
}

static firc_err_t visited_add(visited_set_t *v, const char *url) {
    if (v->n >= v->cap) { return FIRC_ERR_LIMIT; }
    char *copy = strdup(url);
    if (!copy) { return FIRC_ERR_NOMEM; }
    v->urls[v->n++] = copy;
    return FIRC_OK;
}

static void visited_clear(visited_set_t *v) {
    for (size_t i = 0; i < v->n; i++) { free(v->urls[i]); }
    free(v->urls);
    v->urls = NULL;
    v->n = 0;
}

static bool url_allowed(const firc_sub_fetch_opts_t *o, const char *url) {
    if (!firc_sub_url_is_supported(url)) { return false; }
    return o->url_ok == NULL || o->url_ok(o->url_ud, url);
}

static void set_err(const firc_sub_fetch_opts_t *o, const char *msg) {
    if (o->errbuf != NULL && o->errbuf_cap > 0) { snprintf(o->errbuf, o->errbuf_cap, "%s", msg); }
}

static bool line_is_credential(const char *line) {
    return strncasecmp(line, "Authorization:", 14) == 0 || strncasecmp(line, "Cookie:", 7) == 0;
}

static firc_err_t origin_of(const char *url, char **out) {
    *out = NULL;
    CURLU *u = curl_url();
    if (u == NULL) { return FIRC_ERR_NOMEM; }
    char *scheme = NULL, *host = NULL, *port = NULL;
    CURLUcode uc = curl_url_set(u, CURLUPART_URL, url, 0);
    if (uc == CURLUE_OK) { uc = curl_url_get(u, CURLUPART_SCHEME, &scheme, 0); }
    if (uc == CURLUE_OK) { uc = curl_url_get(u, CURLUPART_HOST, &host, 0); }
    if (uc == CURLUE_OK) { uc = curl_url_get(u, CURLUPART_PORT, &port, CURLU_DEFAULT_PORT); }
    firc_err_t err = uc == CURLUE_OK ? FIRC_OK : uc == CURLUE_OUT_OF_MEMORY ? FIRC_ERR_NOMEM : FIRC_ERR_INVAL;
    if (err == FIRC_OK) {
        size_t n = strlen(scheme) + strlen(host) + strlen(port) + 5;
        *out = malloc(n);
        if (*out == NULL) {
            err = FIRC_ERR_NOMEM;
        } else {
            snprintf(*out, n, "%s://%s:%s", scheme, host, port);
            for (char *c = *out; *c != '\0'; c++) {
                if (*c >= 'A' && *c <= 'Z') { *c = (char)(*c + 32); }
            }
        }
    }
    curl_free(scheme);
    curl_free(host);
    curl_free(port);
    curl_url_cleanup(u);
    return err;
}

static struct curl_slist *build_headers(const char *const *headers, bool drop_credentials, bool *ok) {
    struct curl_slist *list = NULL;
    *ok = true;
    for (size_t i = 0; headers != NULL && headers[i] != NULL; i++) {
        if (drop_credentials && line_is_credential(headers[i])) { continue; }
        struct curl_slist *next = curl_slist_append(list, headers[i]);
        if (next == NULL) {
            *ok = false;
            break;
        }
        list = next;
    }
    return list;
}

static void fill_info(CURL *curl, CURLcode rc, firc_sub_fetch_info_t *info) {
    if (info == NULL) { return; }
    long os_errno = 0;
    curl_easy_getinfo(curl, CURLINFO_OS_ERRNO, &os_errno);
#if LIBCURL_VERSION_NUM < 0x073d00
    double connect = 0, app = 0;
    curl_easy_getinfo(curl, CURLINFO_CONNECT_TIME, &connect);
    curl_easy_getinfo(curl, CURLINFO_APPCONNECT_TIME, &app);
#else
    curl_off_t connect = 0, app = 0;
    curl_easy_getinfo(curl, CURLINFO_CONNECT_TIME_T, &connect);
    curl_easy_getinfo(curl, CURLINFO_APPCONNECT_TIME_T, &app);
#endif
    info->connected = connect > 0;
    info->tls_done = app > 0;
    info->reset =
        (rc == CURLE_RECV_ERROR || rc == CURLE_SEND_ERROR || rc == CURLE_SSL_CONNECT_ERROR) && os_errno == ECONNRESET;
    info->timed_out = rc == CURLE_OPERATION_TIMEDOUT;
    info->refused = rc == CURLE_COULDNT_CONNECT && os_errno == ECONNREFUSED;
    info->unreachable = rc == CURLE_COULDNT_CONNECT && (os_errno == ENETUNREACH || os_errno == EHOSTUNREACH);
}

bool firc_sub_fetch_is_redirect(long status) {
    return status == 301 || status == 302 || status == 303 || status == 307 || status == 308;
}

firc_err_t firc_sub_fetch_to(const char *url, const firc_sub_fetch_opts_t *o, firc_sub_fetch_sink_fn sink,
                             void *sink_ud, long *out_status) {
    if (out_status) { *out_status = 0; }
    if (o->info != NULL) { *o->info = (firc_sub_fetch_info_t){0}; }
    set_err(o, "");
    if (url == NULL || !url_allowed(o, url)) { return FIRC_ERR_INVAL; }

    int max_redirects = o->max_redirects > 0 ? o->max_redirects : FIRC_SUB_FETCH_MAX_REDIRECTS;
    long timeout_s = o->timeout_s > 0 ? o->timeout_s : (long)FIRC_SUB_FETCH_TIMEOUT_SECONDS;
    bool headers_ok = true;
    struct curl_slist *headers = build_headers(o->headers, false, &headers_ok);
    bool stripped_ok = true;
    struct curl_slist *stripped = build_headers(o->headers, true, &stripped_ok);
    char *origin = NULL;
    firc_err_t oerr = origin_of(url, &origin);
    visited_set_t visited = {.urls = calloc((size_t)max_redirects + 1, sizeof(char *)),
                             .cap = (size_t)max_redirects + 1};
    char *current = strdup(url);
    struct curl_slist *pin = o->resolve != NULL ? curl_slist_append(NULL, o->resolve) : NULL;
    char iface_opt[IF_NAMESIZE + 4] = "";
    if (o->interface != NULL && o->interface[0] != '\0') {
        snprintf(iface_opt, sizeof(iface_opt), "if!%s", o->interface);
    }
    bool alloc_ok = headers_ok && stripped_ok && oerr != FIRC_ERR_NOMEM && visited.urls != NULL && current != NULL &&
                    (o->resolve == NULL || pin != NULL);
    if (!alloc_ok || oerr != FIRC_OK) {
        curl_slist_free_all(pin);
        curl_slist_free_all(headers);
        curl_slist_free_all(stripped);
        free(origin);
        visited_clear(&visited);
        free(current);
        return alloc_ok ? oerr : FIRC_ERR_NOMEM;
    }

    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    long budget_ms = timeout_s * 1000;
    firc_err_t result = FIRC_ERR_SYS;
    int redirects = 0;
    for (;;) {
        struct timespec now;
        clock_gettime(CLOCK_MONOTONIC, &now);
        long left_ms = budget_ms - ((long)(now.tv_sec - start.tv_sec) * 1000 + (now.tv_nsec - start.tv_nsec) / 1000000);
        if (left_ms <= 0) {
            set_err(o, "timed out following redirects");
            result = FIRC_ERR_IO;
            break;
        }
        firc_err_t verr = visited_add(&visited, current);
        if (verr != FIRC_OK) {
            result = verr;
            break;
        }

        CURL *curl = curl_easy_init();
        if (!curl) {
            result = FIRC_ERR_NOMEM;
            break;
        }
        char curl_err[CURL_ERROR_SIZE] = "";
        xfer_t x = {.curl = curl, .sink = sink, .sink_ud = sink_ud, .o = o};
        sock_cfg_t sock = {.mark = o->mark};
        curl_easy_setopt(curl, CURLOPT_URL, current);
        curl_easy_setopt(curl, CURLOPT_USERAGENT, "firc/" FIRC_VERSION);
        curl_easy_setopt(curl, CURLOPT_FOLLOWLOCATION, 0L);
        curl_easy_setopt(curl, CURLOPT_TIMEOUT_MS, left_ms);
        if (o->low_speed_bytes > 0 && o->low_speed_s > 0) {
            curl_easy_setopt(curl, CURLOPT_LOW_SPEED_LIMIT, o->low_speed_bytes);
            curl_easy_setopt(curl, CURLOPT_LOW_SPEED_TIME, o->low_speed_s);
        }
        curl_easy_setopt(curl, CURLOPT_ERRORBUFFER, curl_err);
        char *hop = NULL;
        (void)origin_of(current, &hop);
        struct curl_slist *sent = hop != NULL && strcmp(hop, origin) == 0 ? headers : stripped;
        free(hop);
        if (sent != NULL) { curl_easy_setopt(curl, CURLOPT_HTTPHEADER, sent); }
        if (pin != NULL) { curl_easy_setopt(curl, CURLOPT_RESOLVE, pin); }
        if (iface_opt[0] != '\0') { curl_easy_setopt(curl, CURLOPT_INTERFACE, iface_opt); }
        if (o->on_header != NULL) {
            curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, header_cb);
            curl_easy_setopt(curl, CURLOPT_HEADERDATA, &x);
        }
        curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, write_cb);
        curl_easy_setopt(curl, CURLOPT_WRITEDATA, &x);
        curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);
        curl_easy_setopt(curl, CURLOPT_SOCKOPTFUNCTION, cloexec_cb);
        curl_easy_setopt(curl, CURLOPT_SOCKOPTDATA, &sock);
        curl_easy_setopt(curl, CURLOPT_XFERINFOFUNCTION, xferinfo_cb);
        curl_easy_setopt(curl, CURLOPT_XFERINFODATA, &x);
        curl_easy_setopt(curl, CURLOPT_NOPROGRESS, o->progress ? 0L : 1L);

        CURLcode rc = curl_easy_perform(curl);
        fill_info(curl, rc, o->info);
        long status = 0;
        curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &status);
        if (out_status) { *out_status = status; }
        if (rc != CURLE_OK) {
            curl_easy_cleanup(curl);
            if (sock.mark_refused) {
                result = FIRC_ERR_SYS;
            } else if (x.drain_over_cap) {
                result = FIRC_ERR_LIMIT;
            } else if (x.aborted || x.sink_stopped) {
                result = FIRC_ERR_CANCELED;
            } else {
                set_err(o, curl_err[0] != '\0' ? curl_err : curl_easy_strerror(rc));
                result = FIRC_ERR_IO;
            }
            break;
        }

        if (firc_sub_fetch_is_redirect(status) && !o->no_redirects) {
            char *location = NULL;
            curl_easy_getinfo(curl, CURLINFO_REDIRECT_URL, &location);
            if (!location || !url_allowed(o, location) || visited_contains(&visited, location)) {
                curl_easy_cleanup(curl);
                result = FIRC_ERR_INVAL;
                break;
            }
            if (redirects >= max_redirects) {
                curl_easy_cleanup(curl);
                result = FIRC_ERR_LIMIT;
                break;
            }
            char *next = strdup(location);
            curl_easy_cleanup(curl);
            if (!next) {
                result = FIRC_ERR_NOMEM;
                break;
            }
            free(current);
            current = next;
            redirects++;
            continue;
        }

        curl_easy_cleanup(curl);
        if (status < 200 || status >= 300) {
            result = FIRC_ERR_PROTO;
            break;
        }
        if (o->progress) { o->progress(o->progress_ud, x.delivered, x.delivered); }
        result = FIRC_OK;
        break;
    }

    free(current);
    visited_clear(&visited);
    curl_slist_free_all(headers);
    curl_slist_free_all(stripped);
    free(origin);
    curl_slist_free_all(pin);
    return result;
}

typedef struct mem_sink {
    char *data;
    size_t len;
    size_t cap;
    size_t max;
    bool over_cap;
    bool nomem;
} mem_sink_t;

static bool mem_sink_write(void *ud, const uint8_t *data, size_t n) {
    mem_sink_t *m = ud;
    if (m->len + n > m->max) {
        m->over_cap = true;
        return false;
    }
    if (m->len + n + 1 > m->cap) {
        size_t new_cap = m->cap ? m->cap * 2 : 4096;
        while (new_cap < m->len + n + 1) { new_cap *= 2; }
        char *na = realloc(m->data, new_cap);
        if (!na) {
            m->nomem = true;
            return false;
        }
        m->data = na;
        m->cap = new_cap;
    }
    memcpy(m->data + m->len, data, n);
    m->len += n;
    m->data[m->len] = '\0';
    return true;
}

static firc_err_t fetch(const char *url, uint32_t mark, const char *const *headers, size_t max, char **out_body,
                        size_t *out_len, firc_sub_fetch_progress_fn progress, void *ud, long *out_http_status,
                        char *errbuf, size_t errbuf_cap, firc_sub_fetch_header_fn on_header, void *header_ud) {
    *out_body = NULL;
    *out_len = 0;
    firc_sub_fetch_opts_t o = {.mark = mark,
                               .headers = headers,
                               .on_header = on_header,
                               .header_ud = header_ud,
                               .progress = progress,
                               .progress_ud = ud,
                               .errbuf = errbuf,
                               .errbuf_cap = errbuf_cap};
    mem_sink_t m = {.max = max};
    firc_err_t err = firc_sub_fetch_to(url, &o, mem_sink_write, &m, out_http_status);
    if (err == FIRC_ERR_CANCELED && m.over_cap) { err = FIRC_ERR_LIMIT; }
    if (err == FIRC_ERR_CANCELED && m.nomem) { err = FIRC_ERR_NOMEM; }
    if (err != FIRC_OK) {
        free(m.data);
        return err;
    }
    if (!m.data) {
        m.data = calloc(1, 1);
        if (!m.data) { return FIRC_ERR_NOMEM; }
    }
    if (!firc_sub_body_is_srs(m.data, m.len) && memchr(m.data, '\0', m.len) != NULL) {
        char shown[160];
        firc_sub_url_redact(url, shown, sizeof shown);
        FIRC_WARN("the list at %s holds a NUL byte and is not text; refused rather "
                  "than read up to it",
                  shown);
        free(m.data);
        return FIRC_ERR_PROTO;
    }
    *out_body = m.data;
    *out_len = m.len;
    return FIRC_OK;
}

firc_err_t firc_sub_fetch_list(const char *url, char **out_body, size_t *out_len) {
    return firc_sub_fetch_list_ex(url, out_body, out_len, NULL, NULL, NULL);
}

firc_err_t firc_sub_fetch_list_ex(const char *url, char **out_body, size_t *out_len,
                                  firc_sub_fetch_progress_fn progress, void *ud, long *out_http_status) {
    return firc_sub_fetch_list_opts(url, FIRC_SUB_FETCH_MAX_BODY_BYTES, out_body, out_len, progress, ud,
                                    out_http_status, NULL, 0);
}

firc_err_t firc_sub_fetch_list_opts(const char *url, size_t cap, char **out_body, size_t *out_len,
                                    firc_sub_fetch_progress_fn progress, void *ud, long *out_status, char *errbuf,
                                    size_t errbuf_cap) {
    return fetch(url, 0, NULL, cap, out_body, out_len, progress, ud, out_status, errbuf, errbuf_cap, NULL, NULL);
}

void firc_sub_fetch_reason(firc_err_t err, long http_status, const char *errbuf, char *out, size_t cap) {
    if (cap == 0) { return; }
    if (err == FIRC_ERR_PROTO && http_status >= 200 && http_status <= 299) {
        snprintf(out, cap, "the list is not text");
    } else if (err == FIRC_ERR_PROTO && http_status > 0) {
        snprintf(out, cap, "the server answered %ld", http_status);
    } else if (err == FIRC_ERR_IO) {
        snprintf(out, cap, "%s", errbuf != NULL && errbuf[0] != '\0' ? errbuf : firc_err_str(FIRC_ERR_IO));
    } else if (err == FIRC_ERR_LIMIT && firc_sub_fetch_is_redirect(http_status)) {
        snprintf(out, cap, "too many redirects");
    } else if (err == FIRC_ERR_LIMIT) {
        snprintf(out, cap, "the list is too big");
    } else {
        snprintf(out, cap, "%s", firc_err_str(err));
    }
}

firc_err_t firc_sub_fetch_list_mark_ex(const char *url, uint32_t mark, const char *const *headers,
                                       char **out_body, size_t *out_len, int *out_http_status,
                                       firc_sub_fetch_progress_fn progress, void *ud) {
    return firc_sub_fetch_list_mark_hdr(url, mark, headers, out_body, out_len, out_http_status, progress, ud, NULL,
                                        NULL);
}

firc_err_t firc_sub_fetch_list_mark_hdr(const char *url, uint32_t mark, const char *const *headers,
                                        char **out_body, size_t *out_len, int *out_http_status,
                                        firc_sub_fetch_progress_fn progress, void *ud,
                                        firc_sub_fetch_header_fn on_header, void *header_ud) {
    long status = 0;
    firc_err_t err = fetch(url, mark, headers, FIRC_SUB_FETCH_MAX_BODY_BYTES, out_body, out_len, progress, ud, &status,
                           NULL, 0, on_header, header_ud);
    if (out_http_status != NULL) { *out_http_status = (int)status; }
    return err;
}

firc_err_t firc_sub_fetch_list_mark(const char *url, uint32_t mark, char **out_body, size_t *out_len,
                                    int *out_http_status) {
    return firc_sub_fetch_list_mark_ex(url, mark, NULL, out_body, out_len, out_http_status, NULL, NULL);
}

