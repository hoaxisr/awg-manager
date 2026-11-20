#include "xhttpd.h"
#include <stdio.h>
#include <stdlib.h>
#include <stdarg.h>
#include <string.h>
#include <ctype.h>
#include <signal.h>
#include <errno.h>
#ifndef _WIN32
#include <strings.h>
#include <unistd.h>
#include <poll.h>
#include <sys/socket.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#endif
#ifdef __linux__
#include <sys/prctl.h>
#endif

xhttpd_config_t g_cfg;
pthread_rwlock_t g_cfg_lock = PTHREAD_RWLOCK_INITIALIZER;
bool g_diag_enabled = false;
static volatile sig_atomic_t g_reload_flag = 0;
static volatile sig_atomic_t g_stop_flag = 0;
static char g_config_path[512] = {0};
static int g_active_http_workers = 0;
static uint64_t g_http_req_timeout_ms = 30000ULL;
static pthread_mutex_t g_reload_mu = PTHREAD_MUTEX_INITIALIZER;
static char g_reload_token[65] = {0};

void xhttpd_test_set_reload_token(const char *token) {
    pthread_mutex_lock(&g_reload_mu);
    if (token) {
        snprintf(g_reload_token, sizeof(g_reload_token), "%s", token);
    } else {
        g_reload_token[0] = '\0';
    }
    pthread_mutex_unlock(&g_reload_mu);
}

void xhttpd_test_set_config_path(const char *path) {
    pthread_mutex_lock(&g_reload_mu);
    if (path) {
        snprintf(g_config_path, sizeof(g_config_path), "%s", path);
    } else {
        g_config_path[0] = '\0';
    }
    pthread_mutex_unlock(&g_reload_mu);
}

void xhttpd_set_http_req_timeout_ms(uint64_t timeout_ms) {
    uint64_t v = timeout_ms > 0 ? timeout_ms : 30000ULL;
    __atomic_store_n(&g_http_req_timeout_ms, v, __ATOMIC_RELEASE);
}

int xhttpd_get_active_http_workers(void) {
    return __atomic_load_n(&g_active_http_workers, __ATOMIC_ACQUIRE);
}

uint64_t now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000ULL + (uint64_t)(ts.tv_nsec / 1000000ULL);
}

uint64_t epoch_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (uint64_t)ts.tv_sec * 1000ULL + (uint64_t)(ts.tv_nsec / 1000000ULL);
}

void diag_log(const char *fmt, ...) {
    if (!__atomic_load_n(&g_diag_enabled, __ATOMIC_RELAXED)) return;
    FILE *fp = fopen("/tmp/awgm-xhttp-diag.log", "a");
    if (!fp) return;
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    struct tm tm_info;
    memset(&tm_info, 0, sizeof(tm_info));
    localtime_r(&ts.tv_sec, &tm_info);
    fprintf(fp, "%02d:%02d:%02d.%03ld [C] ",
            tm_info.tm_hour, tm_info.tm_min, tm_info.tm_sec, ts.tv_nsec / 1000000L);
    va_list ap;
    va_start(ap, fmt);
    vfprintf(fp, fmt, ap);
    va_end(ap);
    fprintf(fp, "\n");
    fclose(fp);
}

int write_all(int fd, const void *buf, size_t len) {
    const uint8_t *p = (const uint8_t *)buf;
    size_t off = 0;
    uint64_t deadline = now_ms() + 15000ULL;
    while (off < len) {
        ssize_t n = write(fd, p + off, len - off);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) {
                if (now_ms() >= deadline) return -1;
                continue;
            }
#ifdef _WIN32
            if (n < 0 && (WSAGetLastError() == WSAEWOULDBLOCK || errno == EAGAIN || errno == EWOULDBLOCK)) {
#else
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
#endif
                uint64_t now = now_ms();
                if (now >= deadline) return -1;
                int rem = (int)(deadline - now);
                if (rem > 1000) rem = 1000;
                struct pollfd pfd = { .fd = fd, .events = POLLOUT };
                if (poll(&pfd, 1, rem) < 0 && errno != EINTR) return -1;
                continue;
            }
            return -1;
        }
        off += (size_t)n;
        deadline = now_ms() + 15000ULL;
    }
    return 0;
}

int send_http_error(int fd, int status, const char *status_text, const char *msg, bool keep_alive) {
    size_t mlen = msg ? strlen(msg) : 0;
    char hdr[512];
    int hlen = snprintf(hdr, sizeof(hdr),
        "HTTP/1.1 %d %s\r\n"
        "Content-Type: text/plain; charset=utf-8\r\n"
        "Cache-Control: no-store\r\n"
        "Content-Length: %zu\r\n"
        "Connection: %s\r\n"
        "\r\n",
        status, status_text, mlen, keep_alive ? "keep-alive" : "close");
    if (hlen < 0 || (size_t)hlen >= sizeof(hdr)) return -1;
    if (write_all(fd, hdr, (size_t)hlen) < 0) return -1;
    if (mlen > 0 && write_all(fd, msg, mlen) < 0) return -1;
    return keep_alive ? 0 : -1;
}

/* Perform an RFC 9112 section 9.6 bounded lingering close when sending an early
 * HTTP error response while unread request body bytes remain in flight, avoiding
 * a Linux TCP RST that would discard the HTTP response before the client reads it. */
static void graceful_lingering_close(int fd) {
    if (fd < 0) return;
    (void)shutdown(fd, SHUT_WR);
    uint8_t discard[8192];
    size_t drained = 0;
    const size_t max_drain = 4U * 1024U * 1024U;
    uint64_t total_deadline = now_ms() + 500ULL;
    uint64_t idle_deadline = now_ms() + 120ULL;
    while (!__atomic_load_n(&g_stop_flag, __ATOMIC_ACQUIRE) && drained < max_drain) {
        uint64_t now = now_ms();
        uint64_t eff_deadline = total_deadline < idle_deadline ? total_deadline : idle_deadline;
        if (now >= eff_deadline) break;
        int wait_ms = (int)(eff_deadline - now);
        if (wait_ms > 50) wait_ms = 50;
        struct pollfd pfd = { .fd = fd, .events = POLLIN, .revents = 0 };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (pr == 0) continue;
        if (pfd.revents & (POLLERR | POLLNVAL)) break;
        if (pfd.revents & (POLLIN | POLLHUP)) {
            ssize_t r = recv(fd, (char *)discard, sizeof(discard), MSG_DONTWAIT);
            if (r == 0) break;
            if (r < 0) {
                if (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) continue;
                break;
            }
            drained += (size_t)r;
            idle_deadline = now_ms() + 120ULL;
        }
    }
    close(fd);
}

static ssize_t cr_fill(conn_reader_t *cr, int timeout_ms) {
    if (cr->pos < cr->len) return (ssize_t)(cr->len - cr->pos);
    cr->pos = 0;
    cr->len = 0;
    if (timeout_ms < 0) return -1;
    struct pollfd pfd = { .fd = cr->fd, .events = POLLIN };
    int pr = poll(&pfd, 1, timeout_ms);
    if (pr <= 0) return -1;
    ssize_t r = recv(cr->fd, cr->buf, sizeof(cr->buf), 0);
    if (r <= 0) return -1;
    cr->len = (size_t)r;
    return r;
}

static int rem_deadline_ms(uint64_t deadline_ms, int max_step_ms) {
    uint64_t now = now_ms();
    if (now >= deadline_ms) return -1;
    uint64_t rem = deadline_ms - now;
    if (rem > (uint64_t)max_step_ms) return max_step_ms;
    return (int)rem;
}

static int cr_read_line(conn_reader_t *cr, char *out, size_t out_sz, uint64_t deadline_ms, int step_timeout_ms) {
    size_t idx = 0;
    while (idx + 1 < out_sz) {
        if (cr->pos >= cr->len) {
            int tmo = rem_deadline_ms(deadline_ms, step_timeout_ms);
            if (tmo < 0) return -1;
            if (cr_fill(cr, tmo) <= 0) return -1;
        }
        char c = (char)cr->buf[cr->pos++];
        if (c == '\n') {
            if (idx > 0 && out[idx - 1] == '\r') idx--;
            out[idx] = '\0';
            return (int)idx;
        }
        out[idx++] = c;
    }
    return -1;
}

static int cr_read_exact(conn_reader_t *cr, uint8_t *dst, size_t n, uint64_t deadline_ms, int step_timeout_ms) {
    size_t off = 0;
    while (off < n) {
        if (cr->pos < cr->len) {
            size_t avail = cr->len - cr->pos;
            size_t take = (n - off) < avail ? (n - off) : avail;
            memcpy(dst + off, cr->buf + cr->pos, take);
            cr->pos += take;
            off += take;
        } else {
            int tmo = rem_deadline_ms(deadline_ms, step_timeout_ms);
            if (tmo < 0) return -1;
            if (n - off >= sizeof(cr->buf)) {
                struct pollfd pfd = { .fd = cr->fd, .events = POLLIN };
                if (poll(&pfd, 1, tmo) <= 0) return -1;
                ssize_t r = recv(cr->fd, dst + off, n - off, 0);
                if (r <= 0) return -1;
                off += (size_t)r;
            } else {
                if (cr_fill(cr, tmo) <= 0) return -1;
            }
        }
    }
    return 0;
}

#define MAX_HTTP_HEADERS      64
#define MAX_HTTP_HEADER_BYTES 16384
#define MAX_HTTP_TRAILERS     32

static bool is_streaming_vless_post(const http_req_t *req) {
    if (!req || strcasecmp(req->method, "POST") != 0) return false;
    if (req->fp_version[0] != '\0' || req->fp_auth[0] != '\0') return false;
    if (strncmp(req->path, "/.xhttpd/", 9) == 0) return false;
    if (strstr(req->query, "seq=") != NULL || strstr(req->query, "page=") != NULL) return false;

    /* Check if path has >= 3 non-empty segments ending in all digits (/<prefix>/<sid>/<seq>) */
    int segs = 0;
    const char *last_seg = NULL;
    size_t last_len = 0;
    const char *p = req->path;
    while (*p) {
        while (*p == '/') p++;
        if (!*p) break;
        const char *start = p;
        while (*p && *p != '/') p++;
        segs++;
        last_seg = start;
        last_len = (size_t)(p - start);
    }
    if (segs >= 3 && last_seg && last_len > 0) {
        bool all_digits = true;
        for (size_t i = 0; i < last_len; i++) {
            if (!isdigit((unsigned char)last_seg[i])) {
                all_digits = false;
                break;
            }
        }
        if (all_digits) return false;
    }
    return true;
}

int pump_remaining_http_chunks(conn_reader_t *cr, int out_pipe_fd) {
    if (!cr || out_pipe_fd < 0) return -1;
    uint8_t chunk_buf[16384];
    char cl_line[128];
    while (!__atomic_load_n(&g_stop_flag, __ATOMIC_ACQUIRE)) {
        uint64_t deadline = now_ms() + 120000ULL;
        if (cr_read_line(cr, cl_line, sizeof(cl_line), deadline, 15000) < 0) return -1;
        if (!isxdigit((unsigned char)cl_line[0])) return -1;
        errno = 0;
        char *endp = NULL;
        unsigned long long raw_clen = strtoull(cl_line, &endp, 16);
        if (errno != 0 || !endp || endp == cl_line) return -1;
        while (*endp == ' ' || *endp == '\t') endp++;
        if (*endp != '\0' && *endp != ';') return -1;
        if (raw_clen > 16ULL * 1024ULL * 1024ULL) return -1;

        size_t rem = (size_t)raw_clen;
        if (rem == 0) {
            int trailer_lines = 0;
            while (1) {
                int tl = cr_read_line(cr, cl_line, sizeof(cl_line), now_ms() + 5000ULL, 5000);
                if (tl < 0) return -1;
                if (tl == 0) break;
                if (++trailer_lines > MAX_HTTP_TRAILERS) return -1;
            }
            return 0;
        }
        while (rem > 0) {
            size_t take = rem < sizeof(chunk_buf) ? rem : sizeof(chunk_buf);
            if (cr_read_exact(cr, chunk_buf, take, now_ms() + 30000ULL, 15000) < 0) return -1;
            if (write_all(out_pipe_fd, chunk_buf, take) < 0) return -1;
            rem -= take;
        }
        if (cr_read_line(cr, cl_line, sizeof(cl_line), now_ms() + 5000ULL, 5000) != 0) return -1;
    }
    return -1;
}

int read_http_request(conn_reader_t *cr, http_req_t *req) {
    memset(req, 0, sizeof(*req));
    req->content_length = -1;
    req->keep_alive = true;

    uint64_t budget_ms = __atomic_load_n(&g_http_req_timeout_ms, __ATOMIC_ACQUIRE);
    uint64_t deadline_ms = now_ms() + budget_ms;
    int step_ms = (budget_ms < 15000ULL) ? (int)budget_ms : 15000;
    int short_step_ms = (budget_ms < 5000ULL) ? (int)budget_ms : 5000;

    char line[8192];
    int l = cr_read_line(cr, line, sizeof(line), deadline_ms, step_ms);
    if (l <= 0) return -1;

    char url[1024] = {0};
    char proto[32] = {0};
    char extra[8]  = {0};
    if (sscanf(line, "%15s %1023s %31s %7s", req->method, url, proto, extra) != 3) {
        return -1;
    }
    if (strcasecmp(proto, "HTTP/1.0") == 0) {
        req->keep_alive = false;
    } else if (strcasecmp(proto, "HTTP/1.1") != 0) {
        return -1;
    }

    char *qmark = strchr(url, '?');
    if (qmark) {
        *qmark++ = '\0';
        size_t qlen = strlen(qmark);
        if (qlen >= sizeof(req->query)) qlen = sizeof(req->query) - 1;
        memcpy(req->query, qmark, qlen);
        req->query[qlen] = '\0';
    }
    size_t ulen = strlen(url);
    if (ulen >= sizeof(req->path)) ulen = sizeof(req->path) - 1;
    memcpy(req->path, url, ulen);
    req->path[ulen] = '\0';

    const size_t max_body = 2 * 1024 * 1024;
    int hdr_count = 0;
    size_t hdr_bytes = (size_t)l;
    bool seen_cl = false;
    bool seen_te = false;
    char *fp_data_b64 = NULL;
    size_t fp_data_len = 0;
    size_t fp_data_cap = 0;

    while (1) {
        l = cr_read_line(cr, line, sizeof(line), deadline_ms, step_ms);
        if (l < 0) { free(fp_data_b64); return -1; }
        if (l == 0) break; /* End of headers */

        hdr_count++;
        hdr_bytes += (size_t)l;
        if (hdr_count > MAX_HTTP_HEADERS || hdr_bytes > MAX_HTTP_HEADER_BYTES) {
            free(fp_data_b64);
            return -1;
        }

        char *colon = strchr(line, ':');
        if (!colon || colon == line) { free(fp_data_b64); return -1; }
        *colon++ = '\0';
        while (*colon == ' ' || *colon == '\t') colon++;
        size_t vlen = strlen(colon);
        while (vlen > 0 && (colon[vlen - 1] == ' ' || colon[vlen - 1] == '\t')) {
            colon[--vlen] = '\0';
        }

        if (strcasecmp(line, "Content-Length") == 0) {
            if (seen_cl || seen_te || !isdigit((unsigned char)colon[0])) { free(fp_data_b64); return -1; }
            errno = 0;
            char *endp = NULL;
            unsigned long long cl_val = strtoull(colon, &endp, 10);
            if (errno != 0 || !endp || *endp != '\0') {
                free(fp_data_b64);
                return -1;
            }
            if (cl_val > (unsigned long long)max_body) {
                free(fp_data_b64);
                return -413;
            }
            req->content_length = (int64_t)cl_val;
            seen_cl = true;
        } else if (strcasecmp(line, "Transfer-Encoding") == 0) {
            if (seen_te || seen_cl) { free(fp_data_b64); return -1; }
            if (strcasecmp(colon, "chunked") != 0) { free(fp_data_b64); return -1; }
            req->chunked = true;
            seen_te = true;
        } else if (strcasecmp(line, "Connection") == 0) {
            if (strcasestr(colon, "close")) req->keep_alive = false;
            else if (strcasestr(colon, "keep-alive")) req->keep_alive = true;
        } else if (strcasecmp(line, "X-FP-Version") == 0) {
            snprintf(req->fp_version, sizeof(req->fp_version), "%s", colon);
        } else if (strcasecmp(line, "X-FP-Auth") == 0) {
            snprintf(req->fp_auth, sizeof(req->fp_auth), "%s", colon);
        } else if (strcasecmp(line, "X-FP-Data") == 0 || strncasecmp(line, "X-FP-Data-", 10) == 0) {
            if (vlen > 0 && fp_data_len + vlen <= MAX_HTTP_HEADER_BYTES) {
                if (fp_data_len + vlen + 1 > fp_data_cap) {
                    size_t ncap = fp_data_cap ? fp_data_cap * 2 : 4096;
                    while (ncap < fp_data_len + vlen + 1) ncap *= 2;
                    char *nb = (char *)realloc(fp_data_b64, ncap);
                    if (!nb) { free(fp_data_b64); return -1; }
                    fp_data_b64 = nb;
                    fp_data_cap = ncap;
                }
                memcpy(fp_data_b64 + fp_data_len, colon, vlen);
                fp_data_len += vlen;
                fp_data_b64[fp_data_len] = '\0';
            }
        } else if (strcasecmp(line, "Content-Type") == 0) {
            snprintf(req->content_type, sizeof(req->content_type), "%s", colon);
        } else if (strcasecmp(line, "X-Awgm-Reload-Token") == 0 || strcasecmp(line, "X-Reload-Token") == 0) {
            snprintf(req->reload_token, sizeof(req->reload_token), "%s", colon);
        } else if (strcasecmp(line, "X-Session-ID") == 0) {
            snprintf(req->session_id, sizeof(req->session_id), "%s", colon);
        } else if (strcasecmp(line, "X-Forwarded-For") == 0) {
            snprintf(req->xff, sizeof(req->xff), "%s", colon);
        } else if (strcasecmp(line, "CF-Connecting-IP") == 0) {
            snprintf(req->cf_ip, sizeof(req->cf_ip), "%s", colon);
        }
    }

    if (req->chunked) {
        free(fp_data_b64);
        fp_data_b64 = NULL;
        bool stream_first_only = is_streaming_vless_post(req);
        size_t cap = 16384;
        req->body = (uint8_t *)malloc(cap);
        if (!req->body) return -1;
        while (1) {
            char cl_line[128];
            if (cr_read_line(cr, cl_line, sizeof(cl_line), deadline_ms, step_ms) < 0) {
                free(req->body); req->body = NULL; return -1;
            }
            if (!isxdigit((unsigned char)cl_line[0])) {
                free(req->body); req->body = NULL; return -1;
            }
            errno = 0;
            char *endp = NULL;
            unsigned long long raw_clen = strtoull(cl_line, &endp, 16);
            if (errno != 0 || !endp || endp == cl_line) {
                free(req->body); req->body = NULL; return -1;
            }
            while (*endp == ' ' || *endp == '\t') endp++;
            if (*endp != '\0' && *endp != ';') {
                free(req->body); req->body = NULL; return -1;
            }
            if (raw_clen > (unsigned long long)(max_body - req->body_len)) {
                free(req->body); req->body = NULL; return -413;
            }
            size_t clen = (size_t)raw_clen;
            if (clen == 0) {
                /* Consume optional trailer headers up to terminating empty CRLF */
                int trailer_lines = 0;
                while (1) {
                    int tl = cr_read_line(cr, cl_line, sizeof(cl_line), deadline_ms, short_step_ms);
                    if (tl < 0) {
                        free(req->body); req->body = NULL; return -1;
                    }
                    if (tl == 0) break;
                    if (++trailer_lines > MAX_HTTP_TRAILERS) {
                        free(req->body); req->body = NULL; return -1;
                    }
                }
                break;
            }
            size_t needed = req->body_len + clen;
            if (needed > cap) {
                while (cap < needed) {
                    if (cap > max_body / 2) {
                        cap = max_body;
                        break;
                    }
                    cap *= 2;
                }
                uint8_t *nb = (uint8_t *)realloc(req->body, cap);
                if (!nb) { free(req->body); req->body = NULL; return -1; }
                req->body = nb;
            }
            if (cr_read_exact(cr, req->body + req->body_len, clen, deadline_ms, step_ms) < 0) {
                free(req->body); req->body = NULL; return -1;
            }
            req->body_len += clen;
            if (cr_read_line(cr, cl_line, sizeof(cl_line), deadline_ms, short_step_ms) != 0) {
                free(req->body); req->body = NULL; return -1;
            }
            if (stream_first_only && req->body_len >= 24) {
                req->chunked_incomplete = true;
                req->keep_alive = false;
                break;
            }
        }
    } else if (req->content_length > 0) {
        free(fp_data_b64);
        fp_data_b64 = NULL;
        if ((size_t)req->content_length > max_body) return -1;
        req->body_len = (size_t)req->content_length;
        req->body = (uint8_t *)malloc(req->body_len);
        if (!req->body) return -1;
        if (cr_read_exact(cr, req->body, req->body_len, deadline_ms, step_ms) < 0) {
            free(req->body);
            req->body = NULL;
            return -1;
        }
    } else if (fp_data_b64 && fp_data_len > 0) {
        uint8_t *decoded = (uint8_t *)malloc(fp_data_len + 4);
        if (decoded) {
            int dlen = base64url_decode(fp_data_b64, fp_data_len, decoded, fp_data_len + 4);
            if (dlen > 0) {
                req->body = decoded;
                req->body_len = (size_t)dlen;
            } else {
                free(decoded);
            }
        }
        free(fp_data_b64);
        fp_data_b64 = NULL;
    } else if (fp_data_b64) {
        free(fp_data_b64);
    }
    return 0;
}

static const char *match_path_prefix(const char *path, const xhttpd_config_t *cfg) {
    for (int i = 0; i < cfg->num_prefixes; i++) {
        const char *p = cfg->prefixes[i];
        size_t plen = strlen(p);
        if (strcmp(path, p) == 0 || (strncmp(path, p, plen) == 0 && path[plen] == '/')) {
            return p;
        }
    }
    return NULL;
}

static void *client_conn_thread(void *arg) {
    int fd = (int)(intptr_t)arg;
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = fd;

    char peer_ip[64] = "127.0.0.1";
    struct sockaddr_in psa;
    socklen_t plen = sizeof(psa);
    if (getpeername(fd, (struct sockaddr *)&psa, &plen) == 0 && psa.sin_family == AF_INET) {
        inet_ntop(AF_INET, &psa.sin_addr, peer_ip, sizeof(peer_ip));
    }

    xhttpd_config_t *cfg_snap = (xhttpd_config_t *)malloc(sizeof(*cfg_snap));
    if (!cfg_snap) {
        send_http_error(fd, 503, "Service Unavailable", "out of memory", false);
        graceful_lingering_close(fd);
        __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
        return NULL;
    }

    bool linger_before_close = false;
    while (!__atomic_load_n(&g_stop_flag, __ATOMIC_ACQUIRE)) {
        http_req_t req;
        int rrc = read_http_request(&cr, &req);
        if (rrc < 0) {
            if (rrc == -413) {
                send_http_error(fd, 413, "Request Entity Too Large", "request body too large", false);
                linger_before_close = true;
            }
            break;
        }
        snprintf(req.peer_ip, sizeof(req.peer_ip), "%s", peer_ip);

        if (strcmp(req.path, "/.xhttpd/health") == 0) {
            int vl_active = 0, vl_pre_auth = 0;
            size_t vl_buf = 0, vl_pa_buf = 0;
            vless_engine_get_stats(&vl_active, &vl_pre_auth, &vl_buf, &vl_pa_buf);
            int fp_active = fp_engine_get_active_sessions();
            char body[384];
            int blen = snprintf(body, sizeof(body),
                "{\"status\":\"ok\",\"engine\":\"c/1\",\"version\":\"%s\",\"pid\":%d,"
                "\"capabilities\":[\"vless-xhttp\",\"finiteproto-v2\"],"
                "\"active_sessions\":%d,\"pre_auth_sessions\":%d,"
                "\"buffered_bytes\":%zu,\"pre_auth_bytes\":%zu,\"fp_sessions\":%d}\n",
                XHTTPD_VERSION, (int)getpid(), vl_active, vl_pre_auth, vl_buf, vl_pa_buf, fp_active);
            if (blen < 0 || (size_t)blen >= sizeof(body)) {
                if (req.body) free(req.body);
                break;
            }
            char resp[576];
            int rlen = snprintf(resp, sizeof(resp),
                "HTTP/1.1 200 OK\r\n"
                "Content-Type: application/json\r\n"
                "Content-Length: %d\r\n"
                "Connection: close\r\n\r\n%s",
                blen, body);
            if (rlen > 0 && (size_t)rlen < sizeof(resp)) {
                write_all(fd, resp, (size_t)rlen);
            }
            if (req.body) free(req.body);
            break;
        }

        if (strcmp(req.path, "/.xhttpd/reload") == 0) {
            pthread_mutex_lock(&g_reload_mu);
            char tok_snap[65];
            snprintf(tok_snap, sizeof(tok_snap), "%s", g_reload_token);
            pthread_mutex_unlock(&g_reload_mu);

            if (tok_snap[0] == '\0') {
                send_http_error(fd, 404, "Not Found", "endpoint not found", false);
                if (req.body) free(req.body);
                break;
            }
            if (strcmp(req.method, "POST") != 0) {
                char resp[] = "HTTP/1.1 405 Method Not Allowed\r\nAllow: POST\r\nContent-Type: text/plain\r\nContent-Length: 18\r\nConnection: close\r\n\r\nMethod Not Allowed";
                write_all(fd, resp, sizeof(resp) - 1);
                if (req.body) free(req.body);
                break;
            }
            size_t exp_len = strlen(tok_snap);
            size_t got_len = strlen(req.reload_token);
            if (exp_len == 0 || got_len != exp_len || !ct_memeq(req.reload_token, tok_snap, exp_len)) {
                send_http_error(fd, 401, "Unauthorized", "reload unauthorized", false);
                if (req.body) free(req.body);
                break;
            }

            pthread_mutex_lock(&g_reload_mu);
            int rc = -1;
            if (g_config_path[0]) {
                rc = load_config_file(g_config_path, &g_cfg);
            }
            pthread_mutex_unlock(&g_reload_mu);

            if (rc != 0) {
                send_http_error(fd, 500, "Internal Server Error", "reload failed", false);
                if (req.body) free(req.body);
                break;
            }

            char resp[] = "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 9\r\nConnection: close\r\n\r\nReload OK";
            write_all(fd, resp, sizeof(resp) - 1);
            if (req.body) free(req.body);
            break;
        }

        pthread_rwlock_rdlock(&g_cfg_lock);
        *cfg_snap = g_cfg;
        pthread_rwlock_unlock(&g_cfg_lock);

        const char *matched_prefix = match_path_prefix(req.path, cfg_snap);
        if (!matched_prefix) {
            send_http_error(fd, 404, "Not Found", "not found", req.keep_alive);
            if (req.body) free(req.body);
            if (!req.keep_alive) {
                if (req.chunked_incomplete) linger_before_close = true;
                break;
            }
            continue;
        }

        if (cfg_snap->num_clients == 0) {
            send_http_error(fd, 403, "Forbidden", "server has no authorized clients", req.keep_alive);
            if (req.body) free(req.body);
            if (!req.keep_alive) {
                if (req.chunked_incomplete) linger_before_close = true;
                break;
            }
            continue;
        }

        int rc = 0;
        if (is_fp_http_request(&req)) {
            rc = handle_fp_http_request(fd, &req, cfg_snap);
        } else {
            rc = handle_vless_http_request(fd, &cr, &req, matched_prefix, cfg_snap);
        }

        bool ka = req.keep_alive;
        bool chunked_inc = req.chunked_incomplete;
        if (req.body) free(req.body);
        if (rc < 0 || !ka) {
            if (rc < 0 && chunked_inc) linger_before_close = true;
            break;
        }
    }

    free(cfg_snap);
    if (linger_before_close) {
        graceful_lingering_close(fd);
    } else {
        close(fd);
    }
    __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
    return NULL;
}

int xhttpd_spawn_client_worker_for_test(int fd) {
    int active = __atomic_add_fetch(&g_active_http_workers, 1, __ATOMIC_ACQ_REL);
    if (active > MAX_HTTP_WORKERS) {
        __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
        send_http_error(fd, 503, "Service Unavailable", "server busy", false);
        close(fd);
        return -1;
    }
    pthread_t tid;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
    pthread_attr_setstacksize(&attr, 524288);
    if (pthread_create(&tid, &attr, client_conn_thread, (void *)(intptr_t)fd) != 0) {
        __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
        pthread_attr_destroy(&attr);
        close(fd);
        return -1;
    }
    pthread_attr_destroy(&attr);
    return 0;
}

/* Lightweight config parser for xhttpd-runtime.conf (key=value lines) */
static void normalize_prefix(const char *raw, char *out, size_t out_sz) {
    while (*raw == ' ' || *raw == '\t' || *raw == '/') raw++;
    snprintf(out, out_sz, "/%s", raw);
    size_t len = strlen(out);
    while (len > 1 && (out[len - 1] == '/' || out[len - 1] == ' ' || out[len - 1] == '\r' || out[len - 1] == '\n')) {
        out[--len] = '\0';
    }
}

int load_config_file(const char *path, xhttpd_config_t *cfg) {
    FILE *fp = fopen(path, "r");
    if (!fp) return -1;

    xhttpd_config_t *next = (xhttpd_config_t *)calloc(1, sizeof(*next));
    if (!next) {
        fclose(fp);
        return -1;
    }
    snprintf(next->listen_host, sizeof(next->listen_host), "127.0.0.1");
    next->listen_port = 9008;
    snprintf(next->prefixes[0], sizeof(next->prefixes[0]), "/cdn-bridge");
    next->num_prefixes = 1;
    next->egress_mode = EGRESS_MODE_ROUTER;
    snprintf(next->socks_host, sizeof(next->socks_host), "127.0.0.1");
    next->socks_port = 1099;
    snprintf(next->dns_proxy_host, sizeof(next->dns_proxy_host), "127.0.0.1");
    next->dns_proxy_port = 3553;
    next->allow_private = false;
    next->idle_timeout_sec = 120;
    next->max_sessions_per_ip = 16;
    next->max_pre_auth_sessions = 64;
    next->max_pre_auth_per_ip = 8;
    next->max_pre_auth_bytes_per_ip = 256 * 1024;
    next->max_pre_auth_bytes_global = 4 * 1024 * 1024;
    next->pre_auth_ttl_ms = 10000;

    bool seen_prefix_override = false;
    char line[1024];
    while (fgets(line, sizeof(line), fp)) {
        char *s = line;
        while (*s == ' ' || *s == '\t') s++;
        if (*s == '#' || *s == '\0' || *s == '\r' || *s == '\n') continue;
        size_t slen = strlen(s);
        while (slen > 0 && (s[slen - 1] == '\r' || s[slen - 1] == '\n' || s[slen - 1] == ' ')) {
            s[--slen] = '\0';
        }
        char *eq = strchr(s, '=');
        if (!eq) continue;
        *eq++ = '\0';
        const char *k = s;
        const char *v = eq;

        if (strcmp(k, "listen_host") == 0) {
            snprintf(next->listen_host, sizeof(next->listen_host), "%s", v);
        } else if (strcmp(k, "listen_port") == 0) {
            int p = atoi(v);
            if (p > 0 && p <= 65535) next->listen_port = (uint16_t)p;
        } else if (strcmp(k, "prefix") == 0) {
            if (!seen_prefix_override) {
                next->num_prefixes = 0;
                seen_prefix_override = true;
            }
            if (next->num_prefixes < MAX_PREFIXES) {
                normalize_prefix(v, next->prefixes[next->num_prefixes++], sizeof(next->prefixes[0]));
            }
        } else if (strcmp(k, "client") == 0) {
            uint8_t u[16];
            if (!parse_uuid_str(v, u)) continue;
            if (next->num_clients >= MAX_CLIENTS) {
                fclose(fp);
                free(next);
                return -1;
            }
            memcpy(next->clients[next->num_clients].uuid, u, 16);
            next->num_clients++;
        } else if (strcmp(k, "fp_cred") == 0) {
            const char *colon = strchr(v, ':');
            if (!colon || colon == v) continue;
            const char *sec_hex = colon + 1;
            if (strchr(sec_hex, ':') != NULL || strlen(sec_hex) != 64) continue;
            size_t id_len = (size_t)(colon - v);
            if (id_len == 0 || id_len >= sizeof(next->cfg_fp_creds[0].client_id)) continue;
            char cid[64];
            memcpy(cid, v, id_len);
            cid[id_len] = '\0';
            uint8_t sec[32];
            if (hex_to_bytes(sec_hex, sec, 32) != 32) continue;
            if (next->num_cfg_fp_creds >= MAX_FP_CREDS) {
                fclose(fp);
                free(next);
                return -1;
            }
            (void)add_explicit_fp_cred(next, cid, sec);
        } else if (strcmp(k, "mode") == 0) {
            if (strcasecmp(v, "router") == 0) next->egress_mode = EGRESS_MODE_ROUTER;
            else if (strcasecmp(v, "direct") == 0) next->egress_mode = EGRESS_MODE_DIRECT;
            else if (strcasecmp(v, "socks") == 0) next->egress_mode = EGRESS_MODE_SOCKS;
            else if (strcasecmp(v, "interface") == 0 || strcasecmp(v, "outbound") == 0) next->egress_mode = EGRESS_MODE_INTERFACE;
            else {
                fclose(fp);
                free(next);
                return -1;
            }
        } else if (strcmp(k, "interface") == 0) {
            snprintf(next->interface_name, sizeof(next->interface_name), "%s", v);
        } else if (strcmp(k, "socks_host") == 0) {
            snprintf(next->socks_host, sizeof(next->socks_host), "%s", v);
        } else if (strcmp(k, "socks_port") == 0) {
            int p = atoi(v);
            if (p > 0 && p <= 65535) next->socks_port = (uint16_t)p;
        } else if (strcmp(k, "dns_proxy_host") == 0) {
            snprintf(next->dns_proxy_host, sizeof(next->dns_proxy_host), "%s", v);
        } else if (strcmp(k, "dns_proxy_port") == 0) {
            int p = atoi(v);
            if (p >= 0 && p <= 65535) next->dns_proxy_port = (uint16_t)p;
        } else if (strcmp(k, "allow_private") == 0) {
            next->allow_private = (strcmp(v, "1") == 0 || strcasecmp(v, "true") == 0);
        } else if (strcmp(k, "idle_timeout_sec") == 0) {
            int t = atoi(v);
            if (t > 5) next->idle_timeout_sec = t;
        } else if (strcmp(k, "max_sessions_per_ip") == 0) {
            int n = atoi(v);
            if (n > 0) next->max_sessions_per_ip = n;
        } else if (strcmp(k, "max_pre_auth_sessions") == 0) {
            int n = atoi(v);
            if (n > 0) next->max_pre_auth_sessions = n;
        } else if (strcmp(k, "max_pre_auth_per_ip") == 0) {
            int n = atoi(v);
            if (n > 0) next->max_pre_auth_per_ip = n;
        } else if (strcmp(k, "max_pre_auth_bytes_per_ip") == 0) {
            unsigned long long n = strtoull(v, NULL, 10);
            if (n > 0) next->max_pre_auth_bytes_per_ip = (size_t)n;
        } else if (strcmp(k, "max_pre_auth_bytes_global") == 0) {
            unsigned long long n = strtoull(v, NULL, 10);
            if (n > 0) next->max_pre_auth_bytes_global = (size_t)n;
        } else if (strcmp(k, "pre_auth_ttl_ms") == 0) {
            unsigned long long n = strtoull(v, NULL, 10);
            if (n > 0) next->pre_auth_ttl_ms = (uint64_t)n;
        } else if (strcmp(k, "trusted_proxy") == 0) {
            if (v[0] && next->num_trusted_proxies < 16) {
                snprintf(next->trusted_proxies[next->num_trusted_proxies++],
                         sizeof(next->trusted_proxies[0]), "%s", v);
            }
        }
    }
    fclose(fp);

    if (next->egress_mode == EGRESS_MODE_INTERFACE && next->interface_name[0] == '\0') {
        free(next);
        return -1;
    }
    if (next->egress_mode == EGRESS_MODE_SOCKS && (next->socks_host[0] == '\0' || next->socks_port == 0)) {
        free(next);
        return -1;
    }
    if (rebuild_fp_credentials(next) < 0) {
        free(next);
        return -1;
    }
    dnat_bump_generation();
    pthread_rwlock_wrlock(&g_cfg_lock);
    *cfg = *next;
    fp_engine_revoke_unauthorised(next);
    vless_engine_revoke_unauthorised(next);
    pthread_rwlock_unlock(&g_cfg_lock);
    free(next);
    return 0;
}

static void sig_handler(int sig) {
    if (sig == SIGHUP) {
        __atomic_store_n(&g_reload_flag, 1, __ATOMIC_RELEASE);
    } else if (sig == SIGINT || sig == SIGTERM) {
        __atomic_store_n(&g_stop_flag, 1, __ATOMIC_RELEASE);
    }
}

static void *reaper_thread_fn(void *arg) {
    (void)arg;
    int ticks = 0;
    while (!__atomic_load_n(&g_stop_flag, __ATOMIC_ACQUIRE)) {
        usleep(100000);
        if (__atomic_load_n(&g_reload_flag, __ATOMIC_ACQUIRE) && g_config_path[0]) {
            __atomic_store_n(&g_reload_flag, 0, __ATOMIC_RELEASE);
            pthread_mutex_lock(&g_reload_mu);
            load_config_file(g_config_path, &g_cfg);
            pthread_mutex_unlock(&g_reload_mu);
        }
        vless_engine_reap_stale();
        if (++ticks < 20) {
            continue;
        }
        ticks = 0;
        if (access("/tmp/awgm-xhttp-diag.enable", F_OK) == 0) {
            __atomic_store_n(&g_diag_enabled, true, __ATOMIC_RELAXED);
        } else {
            __atomic_store_n(&g_diag_enabled, false, __ATOMIC_RELAXED);
        }
        fp_engine_reap_stale();
    }
    return NULL;
}

#ifndef XHTTPD_NO_MAIN
int main(int argc, char **argv) {
#ifdef _WIN32
    init_winsock();
#endif
    signal(SIGPIPE, SIG_IGN);
    signal(SIGHUP, sig_handler);
    signal(SIGINT, sig_handler);
    signal(SIGTERM, sig_handler);
#ifdef __linux__
    prctl(PR_SET_PDEATHSIG, SIGTERM);
#endif

    memset(&g_cfg, 0, sizeof(g_cfg));
    snprintf(g_cfg.listen_host, sizeof(g_cfg.listen_host), "127.0.0.1");
    g_cfg.listen_port = 9008;
    snprintf(g_cfg.prefixes[0], sizeof(g_cfg.prefixes[0]), "/cdn-bridge");
    g_cfg.num_prefixes = 1;
    g_cfg.egress_mode = EGRESS_MODE_ROUTER;
    snprintf(g_cfg.socks_host, sizeof(g_cfg.socks_host), "127.0.0.1");
    g_cfg.socks_port = 1099;
    snprintf(g_cfg.dns_proxy_host, sizeof(g_cfg.dns_proxy_host), "127.0.0.1");
    g_cfg.dns_proxy_port = 3553;
    g_cfg.allow_private = false;
    g_cfg.idle_timeout_sec = 120;
    g_cfg.max_sessions_per_ip = 16;
    g_cfg.max_pre_auth_sessions = 64;
    g_cfg.max_pre_auth_per_ip = 8;
    g_cfg.max_pre_auth_bytes_per_ip = 256 * 1024;
    g_cfg.max_pre_auth_bytes_global = 4 * 1024 * 1024;
    g_cfg.pre_auth_ttl_ms = 10000;

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "-version") == 0 || strcmp(argv[i], "--version") == 0 || strcmp(argv[i], "-v") == 0) {
            printf("awgm-xhttpd %s\n", XHTTPD_VERSION);
            return 0;
        } else if (strcmp(argv[i], "-h") == 0 || strcmp(argv[i], "--help") == 0 || strcmp(argv[i], "-help") == 0) {
            printf("Usage: awgm-xhttpd -config <path> [-version]\n");
            return 0;
        } else if ((strcmp(argv[i], "-config") == 0 || strcmp(argv[i], "-c") == 0) && i + 1 < argc) {
            snprintf(g_config_path, sizeof(g_config_path), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-reload-token") == 0 && i + 1 < argc) {
            snprintf(g_reload_token, sizeof(g_reload_token), "%s", argv[++i]);
        }
    }

    if (g_reload_token[0] == '\0') {
        const char *env_tok = getenv("AWGM_XHTTPD_RELOAD_TOKEN");
        if (env_tok && env_tok[0]) {
            snprintf(g_reload_token, sizeof(g_reload_token), "%s", env_tok);
        }
    }

    if (!g_config_path[0]) {
        fprintf(stderr, "awgm-xhttpd: configuration file required (-config <path>)\n");
        return 1;
    }

    fp_engine_init();
    vless_engine_init();
    __atomic_store_n(&g_diag_enabled, (access("/tmp/awgm-xhttp-diag.enable", F_OK) == 0), __ATOMIC_RELAXED);

    if (load_config_file(g_config_path, &g_cfg) != 0) {
        fprintf(stderr, "awgm-xhttpd: failed to load config %s\n", g_config_path);
        return 1;
    }

    int srv_fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (srv_fd < 0) {
        perror("socket");
        return 1;
    }
    int one = 1;
    setsockopt(srv_fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));

    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(g_cfg.listen_port);
    if (inet_pton(AF_INET, g_cfg.listen_host, &sa.sin_addr) != 1) {
        sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    }

    if (bind(srv_fd, (struct sockaddr *)&sa, sizeof(sa)) < 0) {
        perror("bind");
        close(srv_fd);
        return 1;
    }
    if (listen(srv_fd, 256) < 0) {
        perror("listen");
        close(srv_fd);
        return 1;
    }

    pthread_t reaper_tid;
    pthread_attr_t reaper_attr;
    pthread_attr_init(&reaper_attr);
    pthread_attr_setdetachstate(&reaper_attr, PTHREAD_CREATE_DETACHED);
    pthread_attr_setstacksize(&reaper_attr, 262144);
    if (pthread_create(&reaper_tid, &reaper_attr, reaper_thread_fn, NULL) != 0) {
        pthread_attr_destroy(&reaper_attr);
        close(srv_fd);
        return 1;
    }
    pthread_attr_destroy(&reaper_attr);

    while (!__atomic_load_n(&g_stop_flag, __ATOMIC_ACQUIRE)) {
        struct pollfd pfd = { .fd = srv_fd, .events = POLLIN };
        int pr = poll(&pfd, 1, 1000);
        if (pr <= 0) continue;

        struct sockaddr_in csa;
        socklen_t clen = sizeof(csa);
        int cfd = accept(srv_fd, (struct sockaddr *)&csa, &clen);
        if (cfd < 0) continue;

        int active = __atomic_add_fetch(&g_active_http_workers, 1, __ATOMIC_ACQ_REL);
        if (active > MAX_HTTP_WORKERS) {
            __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
            send_http_error(cfd, 503, "Service Unavailable", "server busy", false);
            close(cfd);
            continue;
        }

        pthread_t tid;
        pthread_attr_t attr;
        pthread_attr_init(&attr);
        pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
        pthread_attr_setstacksize(&attr, 524288);
        if (pthread_create(&tid, &attr, client_conn_thread, (void *)(intptr_t)cfd) != 0) {
            __atomic_sub_fetch(&g_active_http_workers, 1, __ATOMIC_RELEASE);
            close(cfd);
        }
        pthread_attr_destroy(&attr);
    }

    close(srv_fd);
    fp_engine_close_all();
    vless_engine_close_all();
    return 0;
}
#endif /* XHTTPD_NO_MAIN */
