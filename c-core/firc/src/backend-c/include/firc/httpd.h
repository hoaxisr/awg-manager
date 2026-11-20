/* bounded HTTP/1.1 server on firc_loop, shared by the TCP and Unix-socket listeners; keep-alive is non-pipelined,
 * chunked request bodies are not supported, and a leading/trailing slash mismatch is tolerated, not distinct */
#ifndef FIRC_HTTPD_H
#define FIRC_HTTPD_H

#include <cjson/cJSON.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/bytebuf.h"
#include "firc/err.h"
#include "firc/loop.h"

#define FIRC_HTTPD_MAX_HEADER_BYTES 8192
#define FIRC_HTTPD_MAX_BODY_BYTES ((size_t)1024 * 1024)
#define FIRC_HTTPD_MAX_CONNS 64
#define FIRC_HTTPD_MAX_PARAMS 4
#define FIRC_HTTPD_IDLE_TIMEOUT_MS 30000
#define FIRC_HTTPD_MAX_STREAM_BUFFER ((size_t)64 * 1024) /* includes the terminator's reserved bytes */
#define FIRC_HTTPD_STREAM_TERMINATOR_LEN ((size_t)5) /* "0\r\n\r\n" */

typedef struct firc_httpd firc_httpd_t;
typedef struct firc_http_req firc_http_req_t;
typedef struct firc_http_res firc_http_res_t;
typedef struct firc_http_conn firc_http_stream_t; /* the handle IS the held connection, see firc_http_res_hold */

typedef void (*firc_http_handler_fn)(firc_http_req_t *req, firc_http_res_t *res, void *ud);

/* true lets the request proceed to its route handler; false means the middleware already wrote a response */
typedef bool (*firc_http_middleware_fn)(firc_http_req_t *req, firc_http_res_t *res, void *ud);

firc_err_t firc_httpd_create(firc_loop_t *loop, firc_httpd_t **out);
void firc_httpd_destroy(firc_httpd_t *h);

/* on failure errno is the socket call's (0 if addr is not an address at all) */
firc_err_t firc_httpd_listen_tcp(firc_httpd_t *h, const char *addr, uint16_t port);

/* Why the last TCP listen failed, in firc_listen_describe's words; "" when the address did not parse. */
const char *firc_httpd_listen_error(const firc_httpd_t *h);

/* "<addr>:<port>/tcp" the TCP listener is bound to. */
void firc_httpd_tcp_name(const firc_httpd_t *h, char *out, size_t cap);
/* tries ports in order, moving on only past EADDRINUSE and EACCES; *bound is the port taken, or the last one tried on failure; errno as above */
firc_err_t firc_httpd_listen_tcp_first(firc_httpd_t *h, const char *addr, const uint16_t *ports,
                                       size_t n, uint16_t *bound);
/* removes any existing socket file at path first */
firc_err_t firc_httpd_listen_unix(firc_httpd_t *h, const char *path);

/* method is an exact match; routes are matched in registration order, first (method, pattern) match wins */
firc_err_t firc_httpd_route(firc_httpd_t *h, const char *method, const char *pattern,
                        firc_http_handler_fn fn, void *ud);

void firc_httpd_set_middleware(firc_httpd_t *h, firc_http_middleware_fn fn, void *ud);

/* called when no route matches; unset means a plain 404 JSON error */
void firc_httpd_set_not_found(firc_httpd_t *h, firc_http_handler_fn fn, void *ud);

const char *firc_http_req_method(const firc_http_req_t *req);
const char *firc_http_req_path(const firc_http_req_t *req); /* percent-decoded, path-cleaned, no query string */
/* NULL if absent; a name is truncated to 63 bytes, a value to 511, silently; at most 16 params parsed per request */
const char *firc_http_req_query(const firc_http_req_t *req, const char *key);
bool firc_http_req_query_is_true(const firc_http_req_t *req, const char *key); /* true iff the value is exactly "true" */
/* *out = the whole number in query `name`, dflt when absent or empty; false after writing a 400 for anything else */
bool firc_http_req_size(const firc_http_req_t *req, firc_http_res_t *res, const char *name, size_t dflt, size_t *out);
const char *firc_http_req_param(const firc_http_req_t *req, const char *name);
const char *firc_http_req_header(const firc_http_req_t *req, const char *name);
const uint8_t *firc_http_req_body(const firc_http_req_t *req, size_t *len);

void firc_http_res_set_header(firc_http_res_t *res, const char *name, const char *value);
/* copies data; overwrites any previously set body */
void firc_http_res_write(firc_http_res_t *res, int status, const char *content_type,
                       const uint8_t *data, size_t len);
/* like firc_http_res_write but takes ownership of body (malloc'd) instead of copying it */
void firc_http_res_take(firc_http_res_t *res, int status, const char *content_type,
                        uint8_t *body, size_t len);
void firc_http_res_write_json(firc_http_res_t *res, int status, cJSON *obj); /* deletes obj */
/* takes b's bytes as a 200 JSON body; when built failed, frees them and writes 507 (LIMIT) or 500 instead */
void firc_http_res_take_json(firc_http_res_t *res, firc_bytebuf_t *b, firc_err_t built);
void firc_http_res_write_error(firc_http_res_t *res, int status, const char *msg);
/* NULL field writes the plain error */
void firc_http_res_write_field_error(firc_http_res_t *res, int status, const char *msg,
                                     const char *field);

/* runs fn(ud) on the loop thread once every byte is sent, or once the connection ends first; ignored on a held
 * response; not run when firc_httpd_destroy closes it instead; fn must not touch req or res */
void firc_http_res_after_sent(firc_http_res_t *res, void (*fn)(void *ud), void *ud);

/* holds the response: headers flush at once as chunked/Connection:close, and the handler may write chunks (loop
 * thread only) for as long as the client stays; on_close runs once for any end the app did not ask for, never for
 * firc_http_stream_close; NULL on no memory or headers that do not fit, connection left untouched. */
firc_http_stream_t *firc_http_res_hold(firc_http_res_t *res, void (*on_close)(void *ud),
                                       void *ud);

/* FIRC_ERR_NOENT for a NULL handle or a gone client (on_close already ran); FIRC_ERR_LIMIT/NOMEM close the same
 * way. A handle must be dropped inside on_close: a stream already ended cannot otherwise be detected. */
firc_err_t firc_http_stream_write(firc_http_stream_t *s, const void *buf, size_t len);

/* ends the body and closes once it drains; on_close is NOT called */
void firc_http_stream_close(firc_http_stream_t *s);

/* a handler defers work that must not run on the loop; answered later via firc_http_deferred_finish. Meanwhile the
 * connection is watched only for the client leaving; on_close(ud) then runs once but the handle stays valid
 * until finished or dropped. NULL on no memory. Loop thread only, at most once per response. */
typedef struct firc_http_deferred firc_http_deferred_t;

firc_http_deferred_t *firc_http_res_defer(firc_http_res_t *res, void (*on_close)(void *ud),
                                          void *ud);

firc_loop_t *firc_http_res_loop(const firc_http_res_t *res);

/* answers status with body (malloc'd, always taken; NULL+0 for none); not callable from inside the deferring handler */
void firc_http_deferred_finish(firc_http_deferred_t *d, int status, const char *content_type,
                               uint8_t *body, size_t len);

/* releases the handle with no answer, closing a connection still waiting; for a stopping daemon */
void firc_http_deferred_drop(firc_http_deferred_t *d);

#endif /* FIRC_HTTPD_H */
