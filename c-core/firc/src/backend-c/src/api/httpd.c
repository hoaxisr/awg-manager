#include "firc/httpd.h"

#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include "firc/json.h"
#include "firc/listen.h"
#include "firc/log.h"

#define FIRC_HTTPD_MAX_ROUTES 64
#define FIRC_HTTPD_MAX_REQ_HEADERS 32
#define FIRC_HTTPD_MAX_RES_HEADERS 8
#define FIRC_HTTPD_MAX_QUERY 16
#define FIRC_HTTPD_MAX_SEGMENTS 8

typedef struct kv {
    char name[64];
    char value[512];
} kv_t;

typedef struct firc_http_conn firc_http_conn_t;

struct firc_http_req {
    char method[8];
    char path[512];
    kv_t query[FIRC_HTTPD_MAX_QUERY];
    size_t n_query;
    kv_t params[FIRC_HTTPD_MAX_PARAMS];
    size_t n_params;
    kv_t headers[FIRC_HTTPD_MAX_REQ_HEADERS];
    size_t n_headers;
    const uint8_t *body;
    size_t body_len;
};

struct firc_http_res {
    int status;
    kv_t headers[FIRC_HTTPD_MAX_RES_HEADERS];
    size_t n_headers;
    uint8_t *body;
    size_t body_len;
    bool responded;
    firc_http_conn_t *conn;
    bool held; /* true once build_response_bytes queued a held response's head: the dispatcher must not also finish it */
    firc_http_deferred_t *deferred; /* set by firc_http_res_defer: the answer comes later, from outside the dispatcher */
    void (*after_sent)(void *ud);
    void *after_sent_ud;
};

typedef struct route {
    char method[8];
    char pattern[256];
    firc_http_handler_fn fn;
    void *ud;
} route_t;

/* weak handle: conn is NULL once the connection is gone, and the handle outlives it until finished or dropped */
struct firc_http_deferred {
    firc_http_conn_t *conn;
    void (*on_close)(void *ud);
    void *ud;
};

struct firc_httpd {
    char listen_error[192];
    firc_loop_t *loop;
    int tcp_fd;
    int unix_fd;
    char unix_path[256];
    route_t routes[FIRC_HTTPD_MAX_ROUTES];
    size_t n_routes;
    firc_http_middleware_fn middleware;
    void *middleware_ud;
    firc_http_handler_fn not_found;
    void *not_found_ud;
    size_t n_conns;
    firc_http_conn_t *conns;
    bool destroying; /* firc_httpd_destroy is closing every connection */
};

struct firc_http_conn {
    firc_httpd_t *server;
    firc_http_conn_t *next;
    int fd;
    uint8_t *rbuf;
    size_t rbuf_len;
    size_t rbuf_cap;
    bool headers_done;
    size_t header_end;
    size_t content_length;
    bool admitted;
    size_t discard;
    firc_http_req_t req;
    uint8_t *wbuf;
    size_t wbuf_len;
    size_t wbuf_sent;
    bool close_after_write;
    bool http_1_0;
    int idle_timer_id;
    bool streaming; /* a held response: head is out, no request is ever read from here again */
    bool app_closing; /* true for the one stream end the app itself asked for; on_close is not reported for it */
    bool waiting_writable;
    void (*on_close)(void *ud);
    void *on_close_ud;
    void (*after_sent)(void *ud); /* fired once the write buffer empties, or by conn_close if the connection ends first */
    void *after_sent_ud;
    /* while in_dispatch, conn_close does everything but the free; the dispatcher still holds a request into rbuf */
    bool in_dispatch;
    bool closed;
    firc_http_deferred_t *deferred; /* the other end of the handle's weak pointer, cut by conn_close */
};

typedef struct seg {
    const char *p;
    size_t len;
} seg_t;

static size_t split_segments(const char *s, seg_t *out, size_t max) {
    size_t n = 0;
    size_t i = 0;
    size_t slen = strlen(s);
    while (i < slen && n < max) {
        while (i < slen && s[i] == '/') { i++; }
        if (i >= slen) { break; }
        size_t start = i;
        while (i < slen && s[i] != '/') { i++; }
        out[n].p = s + start;
        out[n].len = i - start;
        n++;
    }
    return n;
}

static size_t percent_decode(const char *in, size_t in_len, char *out, size_t out_cap,
                             bool plus_as_space) {
    size_t o = 0;
    for (size_t i = 0; i < in_len && o + 1 < out_cap; i++) {
        char c = in[i];
        if (c == '%' && i + 2 < in_len && isxdigit((unsigned char)in[i + 1]) &&
            isxdigit((unsigned char)in[i + 2])) {
            char hex[3] = {in[i + 1], in[i + 2], '\0'};
            out[o++] = (char)strtol(hex, NULL, 16);
            i += 2;
        } else if (plus_as_space && c == '+') {
            out[o++] = ' ';
        } else {
            out[o++] = c;
        }
    }
    out[o] = '\0';
    return o;
}

static const char *find_header(const kv_t *headers, size_t n, const char *name) {
    for (size_t i = 0; i < n; i++) {
        if (strcasecmp(headers[i].name, name) == 0) { return headers[i].value; }
    }
    return NULL;
}

const char *firc_http_req_method(const firc_http_req_t *req) { return req->method; }
const char *firc_http_req_path(const firc_http_req_t *req) { return req->path; }

const char *firc_http_req_query(const firc_http_req_t *req, const char *key) {
    for (size_t i = 0; i < req->n_query; i++) {
        if (strcmp(req->query[i].name, key) == 0) { return req->query[i].value; }
    }
    return NULL;
}

bool firc_http_req_query_is_true(const firc_http_req_t *req, const char *key) {
    const char *v = firc_http_req_query(req, key);
    return v != NULL && strcmp(v, "true") == 0;
}

/* refused rather than guessed: strtoul silently wraps "-1" and parses "abc" to 0, so both are rejected explicitly */
bool firc_http_req_size(const firc_http_req_t *req, firc_http_res_t *res, const char *name, size_t dflt, size_t *out) {
    *out = dflt;
    const char *s = firc_http_req_query(req, name);
    if (s == NULL || s[0] == '\0') { return true; }
    char *end = NULL;
    errno = 0;
    unsigned long v = strtoul(s, &end, 10);
    if (end == s || *end != '\0' || errno == ERANGE || strchr(s, '-') != NULL) {
        char msg[96];
        snprintf(msg, sizeof(msg), "invalid %s", name);
        firc_http_res_write_error(res, 400, msg);
        return false;
    }
    *out = (size_t)v;
    return true;
}

const char *firc_http_req_param(const firc_http_req_t *req, const char *name) {
    for (size_t i = 0; i < req->n_params; i++) {
        if (strcmp(req->params[i].name, name) == 0) { return req->params[i].value; }
    }
    return NULL;
}

const char *firc_http_req_header(const firc_http_req_t *req, const char *name) {
    return find_header(req->headers, req->n_headers, name);
}

const uint8_t *firc_http_req_body(const firc_http_req_t *req, size_t *len) {
    *len = req->body_len;
    return req->body;
}

void firc_http_res_set_header(firc_http_res_t *res, const char *name, const char *value) {
    if (res->n_headers >= FIRC_HTTPD_MAX_RES_HEADERS) { return; }
    snprintf(res->headers[res->n_headers].name, sizeof(res->headers[0].name), "%s", name);
    snprintf(res->headers[res->n_headers].value, sizeof(res->headers[0].value), "%s", value);
    res->n_headers++;
}

void firc_http_res_write(firc_http_res_t *res, int status, const char *content_type,
                       const uint8_t *data, size_t len) {
    free(res->body);
    res->body = NULL;
    res->body_len = 0;
    if (len > 0) {
        res->body = malloc(len);
        if (res->body) {
            memcpy(res->body, data, len);
            res->body_len = len;
        }
    }
    res->status = status;
    if (content_type) { firc_http_res_set_header(res, "Content-Type", content_type); }
    res->responded = true;
}

void firc_http_res_take(firc_http_res_t *res, int status, const char *content_type,
                        uint8_t *body, size_t len) {
    free(res->body);
    res->body = body;
    res->body_len = body != NULL ? len : 0;
    res->status = status;
    if (content_type) { firc_http_res_set_header(res, "Content-Type", content_type); }
    res->responded = true;
}

void firc_http_res_take_json(firc_http_res_t *res, firc_bytebuf_t *b, firc_err_t built) {
    if (built != FIRC_OK) {
        firc_bytebuf_free(b);
        firc_http_res_write_error(res, built == FIRC_ERR_LIMIT ? 507 : 500, "failed to encode response");
        return;
    }
    firc_http_res_take(res, 200, "application/json; charset=utf-8", b->data, b->len);
    b->data = NULL;
    b->len = b->cap = 0;
}

void firc_http_res_write_json(firc_http_res_t *res, int status, cJSON *obj) {
    char *dump = firc_json_dump(obj);
    cJSON_Delete(obj);
    if (!dump) {
        firc_http_res_write_error(res, 500, "failed to encode response");
        return;
    }
    firc_http_res_write(res, status, "application/json; charset=utf-8", (const uint8_t *)dump,
                      strlen(dump));
    free(dump);
}

void firc_http_res_write_error(firc_http_res_t *res, int status, const char *msg) {
    cJSON *obj = firc_json_error(msg);
    if (!obj) {
        static const char fallback[] = "{\"error\":\"internal error\"}";
        firc_http_res_write(res, 500, "application/json; charset=utf-8", (const uint8_t *)fallback,
                          strlen(fallback));
        return;
    }
    firc_http_res_write_json(res, status, obj);
}

void firc_http_res_write_field_error(firc_http_res_t *res, int status, const char *msg,
                                     const char *field) {
    cJSON *obj = firc_json_error(msg);
    if (obj != NULL && field != NULL && cJSON_AddStringToObject(obj, "field", field) == NULL) {
        cJSON_Delete(obj);
        obj = NULL;
    }
    if (obj == NULL) {
        firc_http_res_write_error(res, 500, "internal error");
        return;
    }
    firc_http_res_write_json(res, status, obj);
}

void firc_http_res_after_sent(firc_http_res_t *res, void (*fn)(void *ud), void *ud) {
    res->after_sent = fn;
    res->after_sent_ud = ud;
}

firc_err_t firc_httpd_route(firc_httpd_t *h, const char *method, const char *pattern,
                        firc_http_handler_fn fn, void *ud) {
    if (h->n_routes >= FIRC_HTTPD_MAX_ROUTES) { return FIRC_ERR_LIMIT; }
    route_t *r = &h->routes[h->n_routes++];
    snprintf(r->method, sizeof(r->method), "%s", method);
    snprintf(r->pattern, sizeof(r->pattern), "%s", pattern);
    r->fn = fn;
    r->ud = ud;
    return FIRC_OK;
}

void firc_httpd_set_middleware(firc_httpd_t *h, firc_http_middleware_fn fn, void *ud) {
    h->middleware = fn;
    h->middleware_ud = ud;
}

void firc_httpd_set_not_found(firc_httpd_t *h, firc_http_handler_fn fn, void *ud) {
    h->not_found = fn;
    h->not_found_ud = ud;
}

static route_t *match_route(firc_httpd_t *h, const char *method, const char *path,
                            firc_http_req_t *req) {
    seg_t path_segs[FIRC_HTTPD_MAX_SEGMENTS];
    size_t n_path = split_segments(path, path_segs, FIRC_HTTPD_MAX_SEGMENTS);

    for (size_t ri = 0; ri < h->n_routes; ri++) {
        route_t *r = &h->routes[ri];
        if (strcmp(r->method, method) != 0) { continue; }
        seg_t pat_segs[FIRC_HTTPD_MAX_SEGMENTS];
        size_t n_pat = split_segments(r->pattern, pat_segs, FIRC_HTTPD_MAX_SEGMENTS);
        if (n_pat != n_path) { continue; }

        size_t n_params = 0;
        bool ok = true;
        for (size_t i = 0; i < n_pat && ok; i++) {
            if (pat_segs[i].len >= 2 && pat_segs[i].p[0] == '{' &&
                pat_segs[i].p[pat_segs[i].len - 1] == '}') {
                if (n_params >= FIRC_HTTPD_MAX_PARAMS) {
                    ok = false;
                    break;
                }
                size_t name_len = pat_segs[i].len - 2;
                snprintf(req->params[n_params].name, sizeof(req->params[0].name), "%.*s",
                         (int)name_len, pat_segs[i].p + 1);
                snprintf(req->params[n_params].value, sizeof(req->params[0].value), "%.*s",
                         (int)path_segs[i].len, path_segs[i].p);
                n_params++;
            } else if (pat_segs[i].len != path_segs[i].len ||
                      strncmp(pat_segs[i].p, path_segs[i].p, pat_segs[i].len) != 0) {
                ok = false;
            }
        }
        if (ok) {
            req->n_params = n_params;
            return r;
        }
    }
    return NULL;
}

static void conn_free(firc_http_conn_t *c) {
    free(c->rbuf);
    free(c->wbuf);
    free(c);
}

/* takes the hook off before calling it, so it runs once whichever of two paths gets there first */
static void fire_after_sent(firc_http_conn_t *c) {
    void (*fn)(void *ud) = c->after_sent;
    void *ud = c->after_sent_ud;
    c->after_sent = NULL;
    c->after_sent_ud = NULL;
    if (fn != NULL) { fn(ud); }
}

static void conn_close(firc_http_conn_t *c) {
    if (c->closed) { return; }
    c->closed = true;
    /* reported once, after the connection is already gone, so the handle it holds is dead when it hears about it */
    void (*on_close)(void *ud) = NULL;
    void *on_close_ud = NULL;
    if (c->streaming && !c->app_closing) {
        on_close = c->on_close;
        on_close_ud = c->on_close_ud;
    }
    c->on_close = NULL;
    /* a client-ended connection still owes the after_sent hook; the server's own destruction does not */
    void (*after_sent)(void *ud) = c->server->destroying ? NULL : c->after_sent;
    void *after_sent_ud = c->after_sent_ud;
    c->after_sent = NULL;
    c->streaming = false; /* every stream call checks this, so a handle outliving its connection is refused, not followed */
    void (*deferred_close)(void *ud) = NULL;
    void *deferred_ud = NULL;
    if (c->deferred != NULL) {
        deferred_close = c->deferred->on_close;
        deferred_ud = c->deferred->ud;
        c->deferred->conn = NULL;
        c->deferred->on_close = NULL;
        c->deferred = NULL;
    }

    firc_http_conn_t **link = &c->server->conns;
    while (*link != NULL && *link != c) { link = &(*link)->next; }
    if (*link == c) {
        *link = c->next;
        c->server->n_conns--;
    }
    if (c->idle_timer_id) {
        firc_loop_del_timer(c->server->loop, c->idle_timer_id);
        c->idle_timer_id = 0;
    }
    firc_loop_del_fd(c->server->loop, c->fd);
    close(c->fd);
    c->fd = -1;
    if (!c->in_dispatch) { conn_free(c); } /* else the dispatcher frees it: its request still points into c->rbuf */
    if (on_close) { on_close(on_close_ud); }
    if (deferred_close != NULL) { deferred_close(deferred_ud); }
    if (after_sent != NULL) { after_sent(after_sent_ud); }
}

static void on_idle_timeout(firc_loop_t *loop, void *ud) {
    (void)loop;
    firc_http_conn_t *c = ud;
    c->idle_timer_id = 0;
    conn_close(c);
}

static void reset_idle_timer(firc_http_conn_t *c) {
    if (c->streaming || c->deferred != NULL) { return; } /* waiting on the app, not idle */
    if (c->idle_timer_id) {
        firc_loop_del_timer(c->server->loop, c->idle_timer_id);
        c->idle_timer_id = 0;
    }
    firc_loop_add_timer(c->server->loop, FIRC_HTTPD_IDLE_TIMEOUT_MS, 0, on_idle_timeout, c,
                      &c->idle_timer_id);
}

static void conn_reset_for_next_request(firc_http_conn_t *c) {
    if (c->rbuf_cap > FIRC_HTTPD_MAX_HEADER_BYTES + 1) {
        free(c->rbuf);
        c->rbuf = NULL;
        c->rbuf_cap = 0;
    }
    c->rbuf_len = 0;
    c->headers_done = false;
    c->header_end = 0;
    c->content_length = 0;
    c->admitted = false;
    memset(&c->req, 0, sizeof(c->req));
    free(c->wbuf);
    c->wbuf = NULL;
    c->wbuf_len = 0;
    c->wbuf_sent = 0;
}

static void on_conn_writable(firc_loop_t *loop, int fd, uint32_t events, void *ud);
static void on_conn_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud);

/* switching callbacks needs del_fd+add_fd, not firc_loop_mod_fd (mask only); false if it closed c on its way out */
static bool start_write(firc_http_conn_t *c) {
    while (c->wbuf_sent < c->wbuf_len) {
        ssize_t n = send(c->fd, c->wbuf + c->wbuf_sent, c->wbuf_len - c->wbuf_sent, MSG_NOSIGNAL);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
                if (!c->waiting_writable) {
                    firc_loop_del_fd(c->server->loop, c->fd);
                    if (firc_loop_add_fd(c->server->loop, c->fd, EPOLLOUT, on_conn_writable, c) !=
                        FIRC_OK) {
                        conn_close(c);
                        return false;
                    }
                    c->waiting_writable = true;
                }
                return true;
            }
            conn_close(c); /* EPIPE or ECONNRESET: the client is gone mid-write */
            return false;
        }
        c->wbuf_sent += (size_t)n;
    }
    if (c->streaming) {
        if (c->app_closing) {
            conn_close(c); /* the terminating chunk is out */
            return false;
        }
        free(c->wbuf);
        c->wbuf = NULL;
        c->wbuf_len = 0;
        c->wbuf_sent = 0;
        if (c->waiting_writable) { /* back to watching for the client leaving, not for room */
            c->waiting_writable = false;
            firc_loop_del_fd(c->server->loop, c->fd);
            if (firc_loop_add_fd(c->server->loop, c->fd, EPOLLIN | EPOLLRDHUP, on_conn_readable,
                                 c) != FIRC_OK) {
                conn_close(c);
                return false;
            }
        }
        return true;
    }
    fire_after_sent(c);
    c->waiting_writable = false;
    if (c->close_after_write && c->discard == 0) {
        conn_close(c);
        return false;
    }
    conn_reset_for_next_request(c);
    firc_loop_del_fd(c->server->loop, c->fd);
    if (firc_loop_add_fd(c->server->loop, c->fd, EPOLLIN, on_conn_readable, c) != FIRC_OK) {
        conn_close(c);
        return false;
    }
    reset_idle_timer(c);
    return true;
}

static void on_conn_writable(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    (void)fd;
    firc_http_conn_t *c = ud;
    if (events & (EPOLLERR | EPOLLHUP)) {
        conn_close(c);
        return;
    }
    (void)start_write(c); /* nothing follows it here: c may be gone */
}

static const char *reason_phrase(int status) {
    switch (status) {
    case 200: return "OK";
    case 202: return "Accepted";
    case 400: return "Bad Request";
    case 401: return "Unauthorized";
    case 403: return "Forbidden";
    case 404: return "Not Found";
    case 405: return "Method Not Allowed";
    case 409: return "Conflict";
    case 413: return "Payload Too Large";
    case 429: return "Too Many Requests";
    case 431: return "Request Header Fields Too Large";
    case 500: return "Internal Server Error";
    case 501: return "Not Implemented";
    case 502: return "Bad Gateway";
    case 503: return "Service Unavailable";
    default: return "";
    }
}

/* a bare off += snprintf(...) walks off past the buffer on the first truncation, underflowing the next call's size */
static bool head_append(char *head, size_t cap, size_t *off, const char *fmt, ...) {
    if (*off >= cap) { return false; }
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(head + *off, cap - *off, fmt, ap);
    va_end(ap);
    if (n < 0 || (size_t)n >= cap - *off) {
        *off = cap;
        return false;
    }
    *off += (size_t)n;
    return true;
}

/* chunked: a held response's head declares no length; false leaves c->wbuf NULL, same shape every caller handles */
static bool build_response_bytes(firc_http_conn_t *c, const firc_http_res_t *res, bool chunked) {
    char head[2048];
    size_t off = 0;
    bool ok = head_append(head, sizeof(head), &off, "HTTP/1.1 %d %s\r\n", res->status,
                          reason_phrase(res->status));
    for (size_t i = 0; ok && i < res->n_headers; i++) {
        ok = head_append(head, sizeof(head), &off, "%s: %s\r\n", res->headers[i].name,
                         res->headers[i].value);
    }
    if (ok && chunked) {
        ok = head_append(head, sizeof(head), &off, "Transfer-Encoding: chunked\r\n");
    } else if (ok) {
        ok = head_append(head, sizeof(head), &off, "Content-Length: %zu\r\n", res->body_len);
    }
    if (ok) {
        ok = head_append(head, sizeof(head), &off, "Connection: %s\r\n",
                         c->close_after_write ? "close" : "keep-alive");
    }
    if (ok) { ok = head_append(head, sizeof(head), &off, "\r\n"); }
    if (!ok) {
        c->wbuf = NULL;
        c->wbuf_len = 0;
        c->wbuf_sent = 0;
        return false;
    }

    size_t head_len = off;
    size_t body_len = chunked ? 0 : res->body_len;
    c->wbuf = malloc(head_len + body_len);
    if (!c->wbuf) {
        c->wbuf_len = 0;
        return false;
    }
    memcpy(c->wbuf, head, head_len);
    if (body_len > 0) { memcpy(c->wbuf + head_len, res->body, body_len); }
    c->wbuf_len = head_len + body_len;
    c->wbuf_sent = 0;
    return true;
}

/* makes room for add more bytes after what is still unsent, dropping the prefix the kernel already took */
static uint8_t *wbuf_reserve(firc_http_conn_t *c, size_t add) {
    if (c->wbuf_sent > 0) {
        size_t pending = c->wbuf_len - c->wbuf_sent;
        if (pending > 0) { memmove(c->wbuf, c->wbuf + c->wbuf_sent, pending); }
        c->wbuf_len = pending;
        c->wbuf_sent = 0;
    }
    uint8_t *nb = realloc(c->wbuf, c->wbuf_len + add);
    if (!nb) { return NULL; }
    c->wbuf = nb;
    uint8_t *room = nb + c->wbuf_len;
    c->wbuf_len += add;
    return room;
}

firc_http_stream_t *firc_http_res_hold(firc_http_res_t *res, void (*on_close)(void *ud),
                                       void *ud) {
    firc_http_conn_t *c = res->conn;
    if (!c || c->streaming) { return NULL; }

    bool was_close_after_write = c->close_after_write;
    c->close_after_write = true; /* a chunked body ends with the connection */
    if (!build_response_bytes(c, res, true)) {
        /* restore close_after_write: the 500 that follows must not get Connection:close for a keep-alive client */
        c->close_after_write = was_close_after_write;
        return NULL;
    }

    res->responded = true;
    res->held = true;
    c->streaming = true;
    c->on_close = on_close;
    c->on_close_ud = ud;
    if (c->idle_timer_id) {
        firc_loop_del_timer(c->server->loop, c->idle_timer_id);
        c->idle_timer_id = 0;
    }
    firc_loop_mod_fd(c->server->loop, c->fd, EPOLLIN | EPOLLRDHUP); /* read only to learn the client left */
    return c;
}

firc_err_t firc_http_stream_write(firc_http_stream_t *s, const void *buf, size_t len) {
    firc_http_conn_t *c = s;
    if (!c || !c->streaming || c->app_closing) { return FIRC_ERR_NOENT; }
    if (len == 0) { return FIRC_OK; } /* a zero-length write must not accidentally end the body like the real terminator does */
    /* checked before add is computed, so the sum below cannot wrap; the terminator is never part of a chunk's claim */
    if (len > FIRC_HTTPD_MAX_STREAM_BUFFER - FIRC_HTTPD_STREAM_TERMINATOR_LEN) {
        conn_close(c);
        return FIRC_ERR_LIMIT;
    }

    char hdr[32];
    int hdr_len = snprintf(hdr, sizeof(hdr), "%zx\r\n", len);
    size_t add = (size_t)hdr_len + len + 2;
    /* the terminator's bytes are reserved on every write, so closing never exceeds the bound or leaves it unterminated */
    if (c->wbuf_len - c->wbuf_sent + add + FIRC_HTTPD_STREAM_TERMINATOR_LEN >
        FIRC_HTTPD_MAX_STREAM_BUFFER) {
        conn_close(c); /* client stopped reading and the answer is piling up here: end the stream rather than grow for it */
        return FIRC_ERR_LIMIT;
    }
    uint8_t *room = wbuf_reserve(c, add);
    if (!room) {
        conn_close(c);
        return FIRC_ERR_NOMEM;
    }
    memcpy(room, hdr, (size_t)hdr_len);
    memcpy(room + hdr_len, buf, len);
    room[(size_t)hdr_len + len] = '\r';
    room[(size_t)hdr_len + len + 1] = '\n';

    /* while EPOLLOUT is pending the writable callback sends what's queued; sending here too would fight it for errno */
    if (!c->waiting_writable && !start_write(c)) { return FIRC_ERR_NOENT; }
    return FIRC_OK;
}

void firc_http_stream_close(firc_http_stream_t *s) {
    firc_http_conn_t *c = s;
    if (!c || !c->streaming || c->app_closing) { return; }
    c->app_closing = true;
    c->on_close = NULL; /* the app is the one ending it: nothing to report */

    /* no bound check: firc_http_stream_write already reserves these bytes on every write, so this was paid for in advance */
    static const uint8_t terminator[FIRC_HTTPD_STREAM_TERMINATOR_LEN] = {'0', '\r', '\n', '\r',
                                                                         '\n'};
    uint8_t *room = wbuf_reserve(c, sizeof(terminator));
    if (!room) {
        conn_close(c);
        return;
    }
    memcpy(room, terminator, sizeof(terminator));
    /* a client gone mid-send closes the connection here too, which is what this call wanted anyway */
    if (!c->waiting_writable) { (void)start_write(c); }
}

firc_http_deferred_t *firc_http_res_defer(firc_http_res_t *res, void (*on_close)(void *ud),
                                          void *ud) {
    firc_http_conn_t *c = res->conn;
    if (c == NULL || c->deferred != NULL || c->streaming || res->deferred != NULL) { return NULL; }
    firc_http_deferred_t *d = calloc(1, sizeof(*d));
    if (d == NULL) { return NULL; }
    d->conn = c;
    d->on_close = on_close;
    d->ud = ud;
    c->deferred = d;
    res->deferred = d;
    res->responded = true;
    return d;
}

firc_loop_t *firc_http_res_loop(const firc_http_res_t *res) {
    return res->conn != NULL ? res->conn->server->loop : NULL;
}

void firc_http_deferred_finish(firc_http_deferred_t *d, int status, const char *content_type,
                               uint8_t *body, size_t len) {
    if (d == NULL) {
        free(body);
        return;
    }
    firc_http_conn_t *c = d->conn;
    free(d);
    if (c == NULL) {
        free(body); /* the client left, or the server is gone */
        return;
    }
    c->deferred = NULL;
    firc_http_res_t res = {0};
    res.conn = c;
    firc_http_res_take(&res, status, content_type, body, len);
    if (!build_response_bytes(c, &res, false)) {
        firc_http_res_t small = {0};
        small.conn = c;
        firc_http_res_write_error(&small, 500, "headers too large");
        (void)build_response_bytes(c, &small, false);
        free(small.body);
    }
    free(res.body);
    (void)start_write(c); /* same path as an answer given in the handler */
}

void firc_http_deferred_drop(firc_http_deferred_t *d) {
    if (d == NULL) { return; }
    firc_http_conn_t *c = d->conn;
    free(d);
    if (c == NULL) { return; }
    c->deferred = NULL; /* so conn_close reports nothing: the app asked */
    conn_close(c);
}

/* collapses "." and ".." for a rooted path; a ".." at or above the root is dropped, keeping staticfiles.c's join safe */
#define FIRC_HTTPD_MAX_CLEAN_SEGMENTS 64
static void path_clean(const char *in, char *out, size_t out_cap) {
    seg_t kept[FIRC_HTTPD_MAX_CLEAN_SEGMENTS];
    size_t n_kept = 0;
    size_t len = strlen(in);
    size_t i = 0;
    while (i < len) {
        while (i < len && in[i] == '/') { i++; }
        if (i >= len) { break; }
        size_t start = i;
        while (i < len && in[i] != '/') { i++; }
        size_t seg_len = i - start;
        if (seg_len == 1 && in[start] == '.') {
            continue;
        } else if (seg_len == 2 && in[start] == '.' && in[start + 1] == '.') {
            if (n_kept > 0) { n_kept--; }
        } else if (n_kept < FIRC_HTTPD_MAX_CLEAN_SEGMENTS) {
            kept[n_kept].p = in + start;
            kept[n_kept].len = seg_len;
            n_kept++;
        }
    }
    size_t o = 0;
    if (o + 1 < out_cap) { out[o++] = '/'; }
    for (size_t k = 0; k < n_kept; k++) {
        if (k > 0 && o + 1 < out_cap) { out[o++] = '/'; }
        size_t copy = kept[k].len;
        if (o + copy >= out_cap) { copy = out_cap > o + 1 ? out_cap - 1 - o : 0; }
        memcpy(out + o, kept[k].p, copy);
        o += copy;
    }
    out[o < out_cap ? o : out_cap - 1] = '\0';
}

/* parses c->rbuf[0..c->header_end) into *req; body left for the caller to attach once fully read */
static bool parse_headers(firc_http_conn_t *c, firc_http_req_t *req) {
    const char *buf = (const char *)c->rbuf;
    size_t pos = 0;

    /* request line: "METHOD path HTTP/1.x\r\n" */
    const char *line_end = memchr(buf, '\n', c->header_end);
    if (!line_end) { return false; }
    size_t line_len = (size_t)(line_end - buf);
    if (line_len > 0 && buf[line_len - 1] == '\r') { line_len--; }

    const char *sp1 = memchr(buf, ' ', line_len);
    if (!sp1) { return false; }
    size_t method_len = (size_t)(sp1 - buf);
    if (method_len >= sizeof(req->method)) { return false; }
    memcpy(req->method, buf, method_len);
    req->method[method_len] = '\0';

    const char *path_start = sp1 + 1;
    const char *sp2 = memchr(path_start, ' ', line_len - method_len - 1);
    if (!sp2) { return false; }
    size_t full_path_len = (size_t)(sp2 - path_start);

    if (line_len - method_len - 1 - full_path_len >= 9 &&
        strncmp(sp2 + 1, "HTTP/1.0", 8) == 0) {
        c->http_1_0 = true;
    }

    const char *qmark = memchr(path_start, '?', full_path_len);
    size_t path_only_len = qmark ? (size_t)(qmark - path_start) : full_path_len;
    char decoded_path[sizeof(req->path)] = {0};
    percent_decode(path_start, path_only_len, decoded_path, sizeof(decoded_path), false);
    path_clean(decoded_path, req->path, sizeof(req->path));

    if (qmark) {
        const char *q = qmark + 1;
        size_t qlen = full_path_len - path_only_len - 1;
        size_t i = 0;
        while (i < qlen && req->n_query < FIRC_HTTPD_MAX_QUERY) {
            size_t start = i;
            while (i < qlen && q[i] != '&') { i++; }
            size_t pair_len = i - start;
            const char *eq = memchr(q + start, '=', pair_len);
            size_t name_raw_len = eq ? (size_t)(eq - (q + start)) : pair_len;
            percent_decode(q + start, name_raw_len, req->query[req->n_query].name,
                          sizeof(req->query[0].name), true);
            if (eq) {
                size_t val_start = start + name_raw_len + 1;
                size_t val_len = pair_len - name_raw_len - 1;
                percent_decode(q + val_start, val_len, req->query[req->n_query].value,
                              sizeof(req->query[0].value), true);
            } else {
                req->query[req->n_query].value[0] = '\0';
            }
            req->n_query++;
            if (i < qlen) { i++; /* skip '&' */ }
        }
    }

    pos = (size_t)(line_end - buf) + 1;

    while (pos < c->header_end && req->n_headers < FIRC_HTTPD_MAX_REQ_HEADERS) {
        const char *hline = buf + pos;
        const char *hend = memchr(hline, '\n', c->header_end - pos);
        if (!hend) { break; }
        size_t hlen = (size_t)(hend - hline);
        if (hlen > 0 && hline[hlen - 1] == '\r') { hlen--; }
        pos = (size_t)(hend - buf) + 1;
        if (hlen == 0) { break; /* blank line: end of headers */ }

        const char *colon = memchr(hline, ':', hlen);
        if (!colon) { continue; }
        size_t name_len = (size_t)(colon - hline);
        const char *vstart = colon + 1;
        size_t vlen = hlen - name_len - 1;
        while (vlen > 0 && (*vstart == ' ' || *vstart == '\t')) {
            vstart++;
            vlen--;
        }
        while (vlen > 0 && (vstart[vlen - 1] == ' ' || vstart[vlen - 1] == '\t')) { vlen--; }
        if (name_len >= sizeof(req->headers[0].name)) { continue; }
        kv_t *h = &req->headers[req->n_headers];
        memcpy(h->name, hline, name_len);
        h->name[name_len] = '\0';
        if (vlen >= sizeof(h->value) && strcasecmp(h->name, "Content-Length") == 0) { return false; }
        size_t copy_len = vlen < sizeof(h->value) - 1 ? vlen : sizeof(h->value) - 1;
        memcpy(h->value, vstart, copy_len);
        h->value[copy_len] = '\0';
        req->n_headers++;
    }
    return true;
}

static void handle_complete_request(firc_http_conn_t *c) {
    firc_http_req_t *req = &c->req;
    req->body = c->content_length > 0 ? c->rbuf + c->header_end : NULL;
    req->body_len = c->content_length;

    const char *conn_hdr = find_header(req->headers, req->n_headers, "Connection");
    bool want_close = c->http_1_0;
    if (conn_hdr) {
        if (strcasecmp(conn_hdr, "close") == 0) { want_close = true; }
        if (strcasecmp(conn_hdr, "keep-alive") == 0) { want_close = false; }
    }
    if (c->server->n_conns > FIRC_HTTPD_MAX_CONNS) { want_close = true; }
    c->close_after_write = want_close;

    firc_http_res_t res = {0};
    res.status = 200;
    res.conn = c;
    c->in_dispatch = true;

    bool proceed = true;
    if (c->server->middleware && !c->admitted) {
        proceed = c->server->middleware(req, &res, c->server->middleware_ud);
    }
    if (proceed) {
        route_t *r = match_route(c->server, req->method, req->path, req);
        if (r) {
            r->fn(req, &res, r->ud);
        } else if (c->server->not_found) {
            c->server->not_found(req, &res, c->server->not_found_ud);
        } else {
            firc_http_res_write_error(&res, 404, "not found");
        }
    }
    c->in_dispatch = false;
    if (c->closed) {
        /* the handler ended the connection from inside itself; everything but this function's own memory is already gone */
        if (res.after_sent != NULL) { res.after_sent(res.after_sent_ud); }
        free(res.body);
        conn_free(c);
        return;
    }
    if (res.deferred != NULL) {
        /* answered later via firc_http_deferred_finish; meanwhile the fd is watched only for the client leaving */
        free(res.body);
        if (c->idle_timer_id) {
            firc_loop_del_timer(c->server->loop, c->idle_timer_id);
            c->idle_timer_id = 0;
        }
        if (firc_loop_mod_fd(c->server->loop, c->fd, EPOLLRDHUP) != FIRC_OK) { conn_close(c); }
        return;
    }
    if (res.held) { /* the head is already queued; the stream belongs to whoever holds it now */
        free(res.body);
        (void)start_write(c);
        return;
    }
    if (!res.responded) { firc_http_res_write_error(&res, 500, "handler produced no response"); }
    if (res.after_sent != NULL) { c->close_after_write = true; }

    if (!build_response_bytes(c, &res, false)) { /* the handler's own headers do not fit or had no memory: answer with our own head instead */
        firc_http_res_t small = {0};
        small.status = 500;
        small.conn = c;
        firc_http_res_write_error(&small, 500, "headers too large");
        (void)build_response_bytes(c, &small, false);
        free(small.body);
    }
    free(res.body);
    c->after_sent = res.after_sent;
    c->after_sent_ud = res.after_sent_ud;
    (void)start_write(c);
}

static bool parse_length(const char *s, size_t *out) {
    if (*s == '\0') { return false; }
    size_t n = 0;
    for (; *s != '\0'; s++) {
        if (*s < '0' || *s > '9') { return false; }
        size_t d = (size_t)(*s - '0');
        if (n > (SIZE_MAX - d) / 10) { return false; }
        n = n * 10 + d;
    }
    *out = n;
    return true;
}

static bool admit_body(firc_http_conn_t *c) {
    if (c->server->middleware == NULL || c->content_length == 0) { return true; }
    firc_http_res_t res = {0};
    res.status = 200;
    res.conn = c;
    if (c->server->middleware(&c->req, &res, c->server->middleware_ud)) {
        free(res.body);
        c->admitted = true;
        return true;
    }
    c->close_after_write = true;
    size_t arrived = c->rbuf_len - c->header_end;
    c->discard = arrived < c->content_length ? c->content_length - arrived : 0;
    if (!build_response_bytes(c, &res, false)) {
        free(res.body);
        conn_close(c);
        return false;
    }
    free(res.body);
    (void)start_write(c);
    return false;
}

static void on_conn_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    firc_http_conn_t *c = ud;
    if (c->streaming || c->deferred != NULL) {
        /* no request is ever read from a held or deferred connection: bytes, EOF and a hangup all mean it's done */
        conn_close(c);
        return;
    }
    if (events & (EPOLLERR | EPOLLHUP)) {
        conn_close(c);
        return;
    }
    reset_idle_timer(c);

    while (c->discard > 0) {
        uint8_t sink[4096];
        ssize_t n = recv(fd, sink, c->discard < sizeof(sink) ? c->discard : sizeof(sink), 0);
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) { return; }
        if (n <= 0) {
            conn_close(c);
            return;
        }
        c->discard -= (size_t)n;
        if (c->discard == 0) {
            conn_close(c);
            return;
        }
    }

    for (;;) {
        size_t cap_needed = c->headers_done ? c->header_end + c->content_length
                                            : FIRC_HTTPD_MAX_HEADER_BYTES;
        if (c->rbuf_cap < cap_needed + 1) {
            size_t new_cap = cap_needed + 1;
            uint8_t *nb = realloc(c->rbuf, new_cap);
            if (!nb) {
                conn_close(c);
                return;
            }
            c->rbuf = nb;
            c->rbuf_cap = new_cap;
        }

        size_t want = cap_needed - c->rbuf_len;
        if (want == 0) { break; }
        ssize_t n = recv(fd, c->rbuf + c->rbuf_len, want, 0);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) { return; }
            conn_close(c);
            return;
        }
        if (n == 0) {
            conn_close(c);
            return;
        }
        c->rbuf_len += (size_t)n;

        if (!c->headers_done) {
            /* end-of-headers accepts bare LFLF too: the netfilter.d hook posts through a here-doc with bare-LF endings */
            uint8_t *marker = NULL;
            size_t marker_len = 0;
            for (size_t i = 0; i + 1 < c->rbuf_len; i++) {
                if (i + 3 < c->rbuf_len && c->rbuf[i] == '\r' && c->rbuf[i + 1] == '\n' &&
                    c->rbuf[i + 2] == '\r' && c->rbuf[i + 3] == '\n') {
                    marker = c->rbuf + i;
                    marker_len = 4;
                    break;
                }
                if (c->rbuf[i] == '\n' && c->rbuf[i + 1] == '\n') {
                    marker = c->rbuf + i;
                    marker_len = 2;
                    break;
                }
            }
            if (marker) {
                c->header_end = (size_t)(marker - c->rbuf) + marker_len;
                c->headers_done = true;

                if (!parse_headers(c, &c->req)) {
                    static const char bad[] = "HTTP/1.1 400 Bad Request\r\nContent-Length: "
                                              "0\r\nConnection: close\r\n\r\n";
                    send(fd, bad, strlen(bad), MSG_NOSIGNAL);
                    conn_close(c);
                    return;
                }
                const char *cl = find_header(c->req.headers, c->req.n_headers, "Content-Length");
                if (cl && !parse_length(cl, &c->content_length)) {
                    static const char bad[] = "HTTP/1.1 400 Bad Request\r\nContent-Length: "
                                              "0\r\nConnection: close\r\n\r\n";
                    send(fd, bad, strlen(bad), MSG_NOSIGNAL);
                    conn_close(c);
                    return;
                }
                if (c->content_length > FIRC_HTTPD_MAX_BODY_BYTES) {
                    static const char resp[] =
                        "HTTP/1.1 413 Payload Too Large\r\nContent-Length: 0\r\n"
                        "Connection: close\r\n\r\n";
                    send(fd, resp, strlen(resp), MSG_NOSIGNAL);
                    conn_close(c);
                    return;
                }
                if (!admit_body(c)) { return; }
            } else if (c->rbuf_len >= FIRC_HTTPD_MAX_HEADER_BYTES) {
                static const char resp[] =
                    "HTTP/1.1 431 Request Header Fields Too Large\r\nContent-Length: "
                    "0\r\nConnection: close\r\n\r\n";
                send(fd, resp, strlen(resp), MSG_NOSIGNAL);
                conn_close(c);
                return;
            }
        }

        if (c->headers_done && c->rbuf_len >= c->header_end + c->content_length) {
            handle_complete_request(c);
            return;
        }
    }
}

static firc_http_conn_t *conn_new(firc_httpd_t *h, int fd) {
    firc_http_conn_t *c = calloc(1, sizeof(*c));
    if (!c) { return NULL; }
    c->server = h;
    c->fd = fd;
    return c;
}

static void on_accept(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    firc_httpd_t *h = ud;
    if (events & (EPOLLERR | EPOLLHUP)) { return; }
    for (;;) {
        int cfd = accept(fd, NULL, NULL);
        if (cfd < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) { return; }
            if (errno == EINTR) { continue; }
            return;
        }
        int flags = fcntl(cfd, F_GETFL, 0);
        if (flags < 0 || fcntl(cfd, F_SETFL, flags | O_NONBLOCK) < 0 ||
            fcntl(cfd, F_SETFD, FD_CLOEXEC) < 0) {
            close(cfd);
            continue;
        }
        if (h->n_conns >= FIRC_HTTPD_MAX_CONNS) {
            close(cfd); /* backpressure: no queue, matches DNS proxy's semaphore model */
            continue;
        }
        int one = 1;
        setsockopt(cfd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

        firc_http_conn_t *c = conn_new(h, cfd);
        if (!c) {
            close(cfd);
            continue;
        }
        if (firc_loop_add_fd(loop, cfd, EPOLLIN, on_conn_readable, c) != FIRC_OK) {
            close(cfd);
            free(c);
            continue;
        }
        h->n_conns++;
        c->next = h->conns;
        h->conns = c;
        reset_idle_timer(c);
    }
}

firc_err_t firc_httpd_create(firc_loop_t *loop, firc_httpd_t **out) {
    firc_httpd_t *h = calloc(1, sizeof(*h));
    if (!h) { return FIRC_ERR_NOMEM; }
    h->loop = loop;
    h->tcp_fd = -1;
    h->unix_fd = -1;
    *out = h;
    return FIRC_OK;
}

void firc_httpd_destroy(firc_httpd_t *h) {
    if (!h) { return; }
    h->destroying = true;
    while (h->conns != NULL) { conn_close(h->conns); }
    if (h->tcp_fd >= 0) {
        firc_loop_del_fd(h->loop, h->tcp_fd);
        close(h->tcp_fd);
    }
    if (h->unix_fd >= 0) {
        firc_loop_del_fd(h->loop, h->unix_fd);
        close(h->unix_fd);
        unlink(h->unix_path);
    }
    free(h);
}

firc_err_t firc_httpd_listen_tcp(firc_httpd_t *h, const char *addr, uint16_t port) {
    struct sockaddr_storage sa;
    socklen_t sa_len;
    if (!firc_listen_addr(addr, port, true, &sa, &sa_len)) {
        errno = 0; /* httpd.h: not the kernel's */
        return FIRC_ERR_INVAL;
    }
    int fd = firc_listen_open("WebUI", SOCK_STREAM, &sa, sa_len, FIRC_WEB_LISTEN, h->listen_error,
                              sizeof(h->listen_error));
    if (fd < 0) {
        errno = -fd; /* httpd.h: the caller reads it */
        return firc_err_from_errno(-fd);
    }
    firc_err_t err = firc_loop_add_fd(h->loop, fd, EPOLLIN, on_accept, h);
    if (err != FIRC_OK) {
        close(fd);
        return err;
    }
    h->tcp_fd = fd;
    return FIRC_OK;
}

const char *firc_httpd_listen_error(const firc_httpd_t *h) { return h->listen_error; }

void firc_httpd_tcp_name(const firc_httpd_t *h, char *out, size_t cap) {
    firc_listen_name(h->tcp_fd, SOCK_STREAM, out, cap);
}

firc_err_t firc_httpd_listen_tcp_first(firc_httpd_t *h, const char *addr, const uint16_t *ports,
                                       size_t n, uint16_t *bound) {
    firc_err_t err = FIRC_ERR_INVAL;
    errno = 0;
    for (size_t i = 0; i < n; i++) {
        err = firc_httpd_listen_tcp(h, addr, ports[i]);
        *bound = ports[i];
        if (err == FIRC_OK) { return FIRC_OK; }
        if (errno != EADDRINUSE && errno != EACCES) { return err; }
    }
    return err;
}

firc_err_t firc_httpd_listen_unix(firc_httpd_t *h, const char *path) {
    if (unlink(path) != 0 && errno != ENOENT) { return firc_err_from_errno(errno); }

    struct sockaddr_un sa;
    memset(&sa, 0, sizeof(sa));
    sa.sun_family = AF_UNIX;
    if (strlen(path) >= sizeof(sa.sun_path)) { return FIRC_ERR_INVAL; }
    snprintf(sa.sun_path, sizeof(sa.sun_path), "%s", path);

    int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd < 0) { return firc_err_from_errno(errno); }
    if (bind(fd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        firc_err_t e = firc_err_from_errno(errno);
        close(fd);
        return e;
    }
    if (listen(fd, 128) != 0) {
        firc_err_t e = firc_err_from_errno(errno);
        close(fd);
        return e;
    }
    firc_err_t err = firc_loop_add_fd(h->loop, fd, EPOLLIN, on_accept, h);
    if (err != FIRC_OK) {
        close(fd);
        return err;
    }
    h->unix_fd = fd;
    snprintf(h->unix_path, sizeof(h->unix_path), "%s", path);
    return FIRC_OK;
}
