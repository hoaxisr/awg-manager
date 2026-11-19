#ifndef FIRC_SUB_FETCH_H
#define FIRC_SUB_FETCH_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/socket.h>

#include "firc/err.h"

#define FIRC_SUB_FETCH_TIMEOUT_SECONDS 15
#define FIRC_SUB_FETCH_MAX_REDIRECTS 5
#define FIRC_SUB_FETCH_MAX_BODY_BYTES ((size_t)8 * 1024 * 1024)

/* Call once at startup before any thread calls firc_sub_fetch_list*; pair with the cleanup call. */
void firc_sub_fetch_global_init(void);
void firc_sub_fetch_global_cleanup(void);

/* True for a well-formed http/https URL with a non-empty host; the same test the fetch itself makes. */
bool firc_sub_url_is_supported(const char *url);

/* Called with bytes received so far and the declared total (0 if unknown); false aborts as FIRC_ERR_CANCELED. */
typedef bool (*firc_sub_fetch_progress_fn)(void *ud, size_t bytes, size_t total);

typedef bool (*firc_sub_fetch_sink_fn)(void *ud, const uint8_t *data, size_t len);
typedef bool (*firc_sub_fetch_url_fn)(void *ud, const char *url);
typedef void (*firc_sub_fetch_header_fn)(void *ud, const char *name, const char *value);

/* What the last hop got to: a TCP connect, a finished TLS handshake, a reset by the peer, the call's timeout,
 * a refused or unroutable connect. */
typedef struct firc_sub_fetch_info {
    bool connected;
    bool tls_done;
    bool reset;
    bool timed_out;
    bool refused;
    bool unreachable;
} firc_sub_fetch_info_t;

typedef struct firc_sub_fetch_opts {
    uint32_t mark;
    const char *const *headers;
    long timeout_s;
    long low_speed_bytes, low_speed_s;
    int max_redirects;
    firc_sub_fetch_url_fn url_ok;
    void *url_ud;
    firc_sub_fetch_header_fn on_header;
    void *header_ud;
    firc_sub_fetch_progress_fn progress;
    void *progress_ud;
    char *errbuf;
    size_t errbuf_cap;
    bool no_redirects;
    const char *resolve;
    const char *interface;
    firc_sub_fetch_info_t *info;
} firc_sub_fetch_opts_t;

/* Streams url's 2xx body into sink; every URL must pass firc_sub_url_is_supported and url_ok (which only narrows
 * it) before it is fetched. INVAL refused/loop, LIMIT redirects or a non-2xx body over the cap, PROTO non-2xx,
 * CANCELED sink or progress said stop, IO transport (libcurl's words in errbuf), SYS mark refused.
 * no_redirects answers a redirect as PROTO with its status; resolve pins one host:port:addr; timeout_s bounds the
 * whole call, every hop included. */
firc_err_t firc_sub_fetch_to(const char *url, const firc_sub_fetch_opts_t *o, firc_sub_fetch_sink_fn sink,
                             void *sink_ud, long *out_status);

/* whether status is a redirect a fetch follows: 301, 302, 303, 307 or 308 */
bool firc_sub_fetch_is_redirect(long status);

/* Fetches url, following redirects, up to FIRC_SUB_FETCH_MAX_REDIRECTS hops; *out_body is a
 * NUL-terminated malloc'd buffer the caller frees. FIRC_ERR_INVAL/LIMIT/PROTO/IO/NOMEM/CANCELED on failure. */
firc_err_t firc_sub_fetch_list_ex(const char *url, char **out_body, size_t *out_len,
                                  firc_sub_fetch_progress_fn progress, void *ud, long *out_http_status);

/* firc_sub_fetch_list_ex with its own body cap and a buffer for libcurl's words on an IO failure. */
firc_err_t firc_sub_fetch_list_opts(const char *url, size_t cap, char **out_body, size_t *out_len,
                                    firc_sub_fetch_progress_fn progress, void *ud, long *out_status, char *errbuf,
                                    size_t errbuf_cap);

/* Writes a human reason for a failed fetch into out: the status for PROTO, errbuf for IO, a size verdict for LIMIT. */
void firc_sub_fetch_reason(firc_err_t err, long http_status, const char *errbuf, char *out, size_t cap);

/* firc_sub_fetch_list(url, body, len) == firc_sub_fetch_list_ex(url, body, len, NULL, NULL, NULL). */
firc_err_t firc_sub_fetch_list(const char *url, char **out_body, size_t *out_len);

/* Like firc_sub_fetch_list_ex without progress; every socket carries SO_MARK mark (0 = none). A mark the
 * socket refuses fails the fetch as FIRC_ERR_SYS before anything is sent. */
firc_err_t firc_sub_fetch_list_mark(const char *url, uint32_t mark, char **out_body, size_t *out_len,
                                    int *out_http_status);

/* firc_sub_fetch_list_mark with header lines ("Name: value", NULL-terminated, NULL for none) and a progress
 * callback that can abort the transfer; a line replaces curl's own header of that name, "Name:" removes it. */
firc_err_t firc_sub_fetch_list_mark_ex(const char *url, uint32_t mark, const char *const *headers,
                                       char **out_body, size_t *out_len, int *out_http_status,
                                       firc_sub_fetch_progress_fn progress, void *ud);

/* firc_sub_fetch_list_mark_ex that also hands every response header of every hop to on_header (NULL for none). */
firc_err_t firc_sub_fetch_list_mark_hdr(const char *url, uint32_t mark, const char *const *headers,
                                        char **out_body, size_t *out_len, int *out_http_status,
                                        firc_sub_fetch_progress_fn progress, void *ud,
                                        firc_sub_fetch_header_fn on_header, void *header_ud);

/* Writes url as scheme://host/… into buf: paths and queries may carry tokens. */
void firc_sub_url_redact(const char *url, char *buf, size_t cap);

typedef int (*firc_sub_setsockopt_fn)(int fd, int level, int name, const void *val, socklen_t len);
void firc_sub_fetch_set_setsockopt_for_test(firc_sub_setsockopt_fn fn); /* test seam; NULL restores setsockopt */

#endif /* FIRC_SUB_FETCH_H */
