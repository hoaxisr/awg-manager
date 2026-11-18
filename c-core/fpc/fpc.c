#include "../xhttpd/xhttpd.h"
#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <signal.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/time.h>
#include <errno.h>
#include <poll.h>
#include <sys/socket.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#ifdef __linux__
#include <sys/prctl.h>
#endif

#define FPC_VERSION "1.0.0-c"
#define MAX_POOL_CONNS 24
#define MAX_FPC_CLIENTS 128

#ifndef FPC_NO_MAIN
static char     g_listen_host[64]  = "127.0.0.1";
static uint16_t g_listen_port      = 19100;
#endif
static char     g_connect_host[64] = "127.0.0.1";
static uint16_t g_connect_port     = 19200;
static char     g_http_host[256]   = "";
static char     g_http_path[256]   = "/cdn-bridge";
static char     g_client_id[64]    = "";
static uint8_t  g_fp_secret[32]    = {0};
static volatile sig_atomic_t g_stop = 0;
static int      g_active_clients   = 0;

typedef enum {
    FP_METHOD_AUTO     = 0,
    FP_METHOD_POST     = 1,
    FP_METHOD_GET      = 2,
    FP_METHOD_HEAD_GET = 3,
    FP_METHOD_HEAD     = 4,
    FP_METHOD_OPTIONS  = 5,
    FP_METHOD_DELETE   = 6,
    FP_METHOD_PUT      = 7
} fp_method_mode_t;

static fp_method_mode_t g_configured_method = FP_METHOD_AUTO;
static volatile int     g_active_method     = FP_METHOD_POST;
static volatile int     g_method_locked     = 0;

static fp_method_mode_t parse_method_mode(const char *s) {
    if (!s || !s[0]) return FP_METHOD_AUTO;
    if (strcasecmp(s, "auto") == 0) return FP_METHOD_AUTO;
    if (strcasecmp(s, "post") == 0) return FP_METHOD_POST;
    if (strcasecmp(s, "get") == 0) return FP_METHOD_GET;
    if (strcasecmp(s, "head_get") == 0 || strcasecmp(s, "head+get") == 0 || strcasecmp(s, "head-get") == 0) return FP_METHOD_HEAD_GET;
    if (strcasecmp(s, "head") == 0) return FP_METHOD_HEAD;
    if (strcasecmp(s, "options") == 0) return FP_METHOD_OPTIONS;
    if (strcasecmp(s, "delete") == 0) return FP_METHOD_DELETE;
    if (strcasecmp(s, "put") == 0) return FP_METHOD_PUT;
    return FP_METHOD_AUTO;
}

static void resolve_op_http_method(int mode, uint8_t op, uint32_t max_down,
                                   const char **out_method, bool *out_use_header) {
    switch (mode) {
        case FP_METHOD_GET:
            *out_method = "GET";
            *out_use_header = true;
            break;
        case FP_METHOD_HEAD_GET:
            if (op == FP_OP_DOWN || max_down > 0) {
                *out_method = "GET";
            } else {
                *out_method = "HEAD";
            }
            *out_use_header = true;
            break;
        case FP_METHOD_HEAD:
            *out_method = "HEAD";
            *out_use_header = true;
            break;
        case FP_METHOD_OPTIONS:
            *out_method = "OPTIONS";
            *out_use_header = true;
            break;
        case FP_METHOD_DELETE:
            *out_method = "DELETE";
            *out_use_header = true;
            break;
        case FP_METHOD_PUT:
            *out_method = "PUT";
            *out_use_header = false;
            break;
        case FP_METHOD_POST:
        default:
            *out_method = "POST";
            *out_use_header = false;
            break;
    }
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
            if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
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

static int read_exact_timeout(int fd, uint8_t *dst, size_t len, int timeout_ms) {
    size_t off = 0;
    uint64_t deadline = now_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 1);
    while (off < len) {
        uint64_t now = now_ms();
        if (now >= deadline) return -1;
        uint64_t rem_ms = deadline - now;
        int wait_ms = rem_ms > 30000ULL ? 30000 : (int)rem_ms;
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (pr == 0) return -1;
        ssize_t n = recv(fd, dst + off, len - off, 0);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            return -1;
        }
        off += (size_t)n;
    }
    return 0;
}

/* HTTP/1.1 Keep-Alive Pool to local TLS bridge (g_connect_host:g_connect_port) */
static int             g_pool_fds[MAX_POOL_CONNS];
static int             g_pool_count = 0;
static pthread_mutex_t g_pool_mu = PTHREAD_MUTEX_INITIALIZER;

static int dial_bridge_conn(void) {
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(g_connect_port);
    if (inet_pton(AF_INET, g_connect_host, &sa.sin_addr) != 1) {
        close(fd);
        return -1;
    }
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags >= 0) (void)fcntl(fd, F_SETFL, flags | O_NONBLOCK);
    int rc = connect(fd, (struct sockaddr *)&sa, sizeof(sa));
    if (rc < 0) {
        if (errno != EINPROGRESS) {
            close(fd);
            return -1;
        }
        uint64_t deadline = now_ms() + 5000ULL;
        for (;;) {
            uint64_t now = now_ms();
            if (now >= deadline) {
                close(fd);
                return -1;
            }
            int rem = (int)(deadline - now);
            struct pollfd pfd = { .fd = fd, .events = POLLOUT };
            int pr = poll(&pfd, 1, rem);
            if (pr < 0) {
                if (errno == EINTR) continue;
                close(fd);
                return -1;
            }
            if (pr == 0) {
                close(fd);
                return -1;
            }
            int so_err = 0;
            socklen_t elen = sizeof(so_err);
            if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &so_err, &elen) < 0 || so_err != 0) {
                close(fd);
                return -1;
            }
            break;
        }
    }
    if (flags >= 0) (void)fcntl(fd, F_SETFL, flags & ~O_NONBLOCK);
    struct timeval tv = { .tv_sec = 15, .tv_usec = 0 };
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
    return fd;
}

static int acquire_pool_conn(bool *reused) {
    pthread_mutex_lock(&g_pool_mu);
    while (g_pool_count > 0) {
        int fd = g_pool_fds[--g_pool_count];
        /* Quick non-blocking check if socket was closed while idle */
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        if (poll(&pfd, 1, 0) > 0) {
            uint8_t dummy;
            if (recv(fd, &dummy, 1, MSG_PEEK | MSG_DONTWAIT) <= 0) {
                close(fd);
                continue;
            }
        }
        pthread_mutex_unlock(&g_pool_mu);
        if (reused) *reused = true;
        return fd;
    }
    pthread_mutex_unlock(&g_pool_mu);
    if (reused) *reused = false;
    return dial_bridge_conn();
}

static void release_pool_conn(int fd) {
    if (fd < 0) return;
    pthread_mutex_lock(&g_pool_mu);
    if (g_pool_count < MAX_POOL_CONNS) {
        g_pool_fds[g_pool_count++] = fd;
        pthread_mutex_unlock(&g_pool_mu);
        return;
    }
    pthread_mutex_unlock(&g_pool_mu);
    close(fd);
}

static void close_all_pool_conns(void) {
    pthread_mutex_lock(&g_pool_mu);
    while (g_pool_count > 0) {
        int fd = g_pool_fds[--g_pool_count];
        if (fd >= 0) close(fd);
    }
    pthread_mutex_unlock(&g_pool_mu);
}

/* Big-endian helpers */
static void put_be32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)v;
}

static void put_be64(uint8_t *p, uint64_t v) {
    put_be32(p, (uint32_t)(v >> 32));
    put_be32(p + 4, (uint32_t)v);
}

static uint32_t get_be32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | (uint32_t)p[3];
}

static uint64_t get_be64(const uint8_t *p) {
    return ((uint64_t)get_be32(p) << 32) | (uint64_t)get_be32(p + 4);
}

static void compute_fp_hmac_sig(const char *method, const char *path, const char *client_id,
                                const char *ts_str, const char *nonce_hex, const uint8_t body_sha256[32],
                                uint8_t out_sig[32]) {
    uint8_t msg[1024];
    size_t off = 0;
    #define APPEND_STR0(s) do { \
        size_t l = strlen(s); \
        if (off + l + 1 < sizeof(msg)) { \
            memcpy(msg + off, (s), l); \
            off += l; \
            msg[off++] = 0; \
        } \
    } while (0)

    APPEND_STR0(FP_PROTO_VERSION);
    APPEND_STR0(method);
    APPEND_STR0(path);
    APPEND_STR0(client_id);
    APPEND_STR0(ts_str);
    APPEND_STR0(nonce_hex);
    #undef APPEND_STR0

    if (off + 32 <= sizeof(msg)) {
        memcpy(msg + off, body_sha256, 32);
        off += 32;
    }
    hmac_sha256(g_fp_secret, 32, msg, off, out_sig);
}

typedef struct {
    uint8_t  status;
    uint8_t  flags;
    uint8_t  sid[16];
    uint64_t acked_up_offset;
    uint64_t down_offset_start;
    uint64_t down_offset_end;
    uint64_t target_eof_offset;
    uint32_t payload_len;
} fp_parsed_resp_t;

static int read_http_line(int fd, char *out, size_t out_sz, int timeout_ms) {
    if (!out || out_sz < 2) return -1;
    size_t idx = 0;
    uint64_t deadline = now_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 1);
    while (idx + 1 < out_sz) {
        uint64_t now = now_ms();
        if (now >= deadline) return -1;
        uint64_t rem_ms = deadline - now;
        int wait_ms = rem_ms > 30000ULL ? 30000 : (int)rem_ms;
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (pr == 0) return -1;
        char c;
        ssize_t r = recv(fd, &c, 1, 0);
        if (r <= 0) {
            if (r < 0 && errno == EINTR) continue;
            return -1;
        }
        if (c == '\n') {
            if (idx > 0 && out[idx - 1] == '\r') idx--;
            out[idx] = '\0';
            return (int)idx;
        }
        out[idx++] = c;
    }
    return -1;
}

static int fp_rpc_once(int fd, const char *http_method, bool send_in_header, const char *nonce_hex,
                       const uint8_t *wire_req, size_t wire_len, const char *auth_hdr,
                       fp_parsed_resp_t *out_resp, uint8_t *down_buf, size_t down_buf_cap) {
    if (send_in_header) {
        size_t b64_cap = (wire_len * 4) / 3 + 16;
        char *b64 = (char *)malloc(b64_cap);
        if (!b64) return -1;
        if (base64url_encode(wire_req, wire_len, b64, b64_cap) < 0) {
            free(b64);
            return -1;
        }
        size_t hdr_cap = b64_cap + 1024;
        char *req_hdr = (char *)malloc(hdr_cap);
        if (!req_hdr) {
            free(b64);
            return -1;
        }
        const char *qsep = strchr(g_http_path, '?') ? "&" : "?";
        int hlen = snprintf(req_hdr, hdr_cap,
            "%s %s%s_=%s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36\r\n"
            "Accept: application/octet-stream, */*\r\n"
            "Cache-Control: no-store, no-cache\r\n"
            "Pragma: no-cache\r\n"
            "Connection: keep-alive\r\n"
            "X-FP-Version: %s\r\n"
            "X-FP-Auth: %s\r\n"
            "X-FP-Data: %s\r\n"
            "\r\n",
            http_method, g_http_path, qsep, nonce_hex,
            g_http_host, FP_PROTO_VERSION, auth_hdr, b64);
        free(b64);
        if (hlen < 0 || (size_t)hlen >= hdr_cap) {
            free(req_hdr);
            return -1;
        }
        int wrc = write_all(fd, req_hdr, (size_t)hlen);
        free(req_hdr);
        if (wrc < 0) return -1;
    } else {
        char req_hdr[2048];
        int hlen = snprintf(req_hdr, sizeof(req_hdr),
            "%s %s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36\r\n"
            "Content-Type: application/octet-stream\r\n"
            "Accept: application/octet-stream\r\n"
            "Cache-Control: no-store\r\n"
            "Connection: keep-alive\r\n"
            "X-FP-Version: %s\r\n"
            "X-FP-Auth: %s\r\n"
            "Content-Length: %zu\r\n"
            "\r\n",
            http_method, g_http_path, g_http_host, FP_PROTO_VERSION, auth_hdr, wire_len);
        if (hlen < 0 || (size_t)hlen >= sizeof(req_hdr)) return -1;
        if (write_all(fd, req_hdr, (size_t)hlen) < 0) return -1;
        if (wire_len > 0 && write_all(fd, wire_req, wire_len) < 0) return -1;
    }

    char line[4096];
    if (read_http_line(fd, line, sizeof(line), 20000) <= 0) return -1;
    int http_status = 0;
    if (sscanf(line, "HTTP/%*s %d", &http_status) != 1) return -1;

    int64_t content_len = -1;
    bool chunked = false;
    bool conn_close = false;
    bool has_fp_version = false;
    char resp_hdr_b64[128] = {0};
    char resp_data_b64[3200] = {0};

    while (1) {
        int l = read_http_line(fd, line, sizeof(line), 15000);
        if (l < 0) return -1;
        if (l == 0) break;
        if (strncasecmp(line, "Content-Length:", 15) == 0) {
            const char *cl_str = line + 15;
            while (*cl_str == ' ' || *cl_str == '\t') cl_str++;
            if (!isdigit((unsigned char)cl_str[0])) return -1;
            errno = 0;
            char *cl_end = NULL;
            long long parsed_cl = strtoll(cl_str, &cl_end, 10);
            if (errno != 0 || !cl_end || *cl_end != '\0' || parsed_cl < 0) return -1;
            content_len = (int64_t)parsed_cl;
        } else if (strncasecmp(line, "Transfer-Encoding:", 18) == 0) {
            if (strcasestr(line + 18, "chunked")) chunked = true;
        } else if (strncasecmp(line, "Connection:", 11) == 0) {
            if (strcasestr(line + 11, "close")) conn_close = true;
        } else if (strncasecmp(line, "X-FP-Version:", 13) == 0) {
            has_fp_version = true;
        } else if (strncasecmp(line, "X-FP-Resp:", 10) == 0) {
            const char *v = line + 10;
            while (*v == ' ' || *v == '\t') v++;
            size_t vlen = strlen(v);
            if (vlen >= sizeof(resp_hdr_b64)) return -1;
            memcpy(resp_hdr_b64, v, vlen + 1);
        } else if (strncasecmp(line, "X-FP-Data:", 10) == 0) {
            const char *v = line + 10;
            while (*v == ' ' || *v == '\t') v++;
            size_t vlen = strlen(v);
            if (vlen >= sizeof(resp_data_b64)) return -1;
            memcpy(resp_data_b64, v, vlen + 1);
        }
    }

    bool is_head_req = (strcasecmp(http_method, "HEAD") == 0);
    if (!has_fp_version && resp_hdr_b64[0] == '\0' && http_status != 200) {
        /* Rejected by intermediate CDN/WAF before reaching xhttpd */
        return -2;
    }

    if (is_head_req || (content_len == 0 && !chunked && resp_hdr_b64[0] != '\0')) {
        if (resp_hdr_b64[0] == '\0') return -2;
        uint8_t rhdr[FP_RESP_HEADER_SIZE + 8];
        int dlen = base64url_decode(resp_hdr_b64, strlen(resp_hdr_b64), rhdr, sizeof(rhdr));
        if (dlen != FP_RESP_HEADER_SIZE || memcmp(rhdr, FP_PROTO_MAGIC, 4) != 0) {
            return -1;
        }
        memset(out_resp, 0, sizeof(*out_resp));
        out_resp->status            = rhdr[4];
        out_resp->flags             = rhdr[5];
        memcpy(out_resp->sid, rhdr + 6, 16);
        out_resp->acked_up_offset   = get_be64(rhdr + 22);
        out_resp->down_offset_start = get_be64(rhdr + 30);
        out_resp->down_offset_end   = get_be64(rhdr + 38);
        out_resp->target_eof_offset = get_be64(rhdr + 46);
        out_resp->payload_len       = get_be32(rhdr + 54);

        if (out_resp->payload_len > 0) {
            if (out_resp->payload_len > down_buf_cap || resp_data_b64[0] == '\0') {
                return -1;
            }
            int plen = base64url_decode(resp_data_b64, strlen(resp_data_b64), down_buf, down_buf_cap);
            if (plen < 0 || (uint32_t)plen != out_resp->payload_len) {
                return -1;
            }
        }
        return conn_close ? 1 : 0;
    }

    uint8_t *full_body = NULL;
    size_t full_len = 0;

    if (chunked) {
        const size_t max_resp_body = FP_RESP_HEADER_SIZE + FP_MAX_RESP_PAYLOAD;
        size_t cap = 4096;
        full_body = (uint8_t *)malloc(cap);
        if (!full_body) return -1;
        while (1) {
            char cl_line[128];
            if (read_http_line(fd, cl_line, sizeof(cl_line), 15000) < 0) {
                free(full_body);
                return -1;
            }
            if (!isxdigit((unsigned char)cl_line[0])) {
                free(full_body);
                return -1;
            }
            errno = 0;
            char *endp = NULL;
            unsigned long long raw_clen = strtoull(cl_line, &endp, 16);
            if (errno != 0 || !endp || endp == cl_line) {
                free(full_body);
                return -1;
            }
            while (*endp == ' ' || *endp == '\t') endp++;
            if (*endp != '\0' && *endp != ';') {
                free(full_body);
                return -1;
            }
            if (raw_clen == 0) {
                int trailer_lines = 0;
                while (1) {
                    int tl = read_http_line(fd, cl_line, sizeof(cl_line), 5000);
                    if (tl < 0) { free(full_body); return -1; }
                    if (tl == 0) break;
                    if (++trailer_lines > 32) { free(full_body); return -1; }
                }
                break;
            }
            if (raw_clen > (unsigned long long)(max_resp_body - full_len)) {
                free(full_body);
                return -1;
            }
            size_t clen = (size_t)raw_clen;
            if (clen > cap - full_len) {
                while (cap - full_len < clen) {
                    if (cap >= max_resp_body) { cap = max_resp_body; break; }
                    cap *= 2;
                    if (cap > max_resp_body) cap = max_resp_body;
                }
                uint8_t *nb = (uint8_t *)realloc(full_body, cap);
                if (!nb) { free(full_body); return -1; }
                full_body = nb;
            }
            if (read_exact_timeout(fd, full_body + full_len, clen, 15000) < 0) {
                free(full_body);
                return -1;
            }
            full_len += clen;
            if (read_http_line(fd, cl_line, sizeof(cl_line), 5000) != 0) {
                free(full_body);
                return -1;
            }
        }
    } else {
        if (content_len < (int64_t)FP_RESP_HEADER_SIZE || content_len > (int64_t)(FP_RESP_HEADER_SIZE + FP_MAX_RESP_PAYLOAD)) {
            return (!has_fp_version && http_status != 200) ? -2 : -1;
        }
        full_len = (size_t)content_len;
        full_body = (uint8_t *)malloc(full_len);
        if (!full_body) return -1;
        if (read_exact_timeout(fd, full_body, full_len, 15000) < 0) {
            free(full_body);
            return -1;
        }
    }

    if (full_len < FP_RESP_HEADER_SIZE || memcmp(full_body, FP_PROTO_MAGIC, 4) != 0) {
        free(full_body);
        return (!has_fp_version && http_status != 200) ? -2 : -1;
    }

    const uint8_t *rhdr = full_body;
    memset(out_resp, 0, sizeof(*out_resp));
    out_resp->status            = rhdr[4];
    out_resp->flags             = rhdr[5];
    memcpy(out_resp->sid, rhdr + 6, 16);
    out_resp->acked_up_offset   = get_be64(rhdr + 22);
    out_resp->down_offset_start = get_be64(rhdr + 30);
    out_resp->down_offset_end   = get_be64(rhdr + 38);
    out_resp->target_eof_offset = get_be64(rhdr + 46);
    out_resp->payload_len       = get_be32(rhdr + 54);

    size_t rem_body = full_len - FP_RESP_HEADER_SIZE;
    if (out_resp->payload_len != rem_body || rem_body > down_buf_cap) {
        free(full_body);
        return -1;
    }
    if (rem_body > 0) {
        memcpy(down_buf, full_body + FP_RESP_HEADER_SIZE, rem_body);
    }
    free(full_body);
    return conn_close ? 1 : 0;
}

static int fp_rpc(uint8_t op, uint8_t flags, const uint8_t sid[16],
                  uint64_t up_off, uint64_t ack_down_off, uint64_t want_down_off, uint32_t max_down,
                  const uint8_t *payload, uint32_t payload_len,
                  fp_parsed_resp_t *out_resp, uint8_t *down_buf, size_t down_buf_cap) {
    static const int auto_candidates[] = {
        FP_METHOD_POST,
        FP_METHOD_GET,
        FP_METHOD_HEAD_GET,
        FP_METHOD_OPTIONS,
        FP_METHOD_DELETE,
        FP_METHOD_HEAD
    };
    const int num_candidates = (int)(sizeof(auto_candidates) / sizeof(auto_candidates[0]));

    int cur_mode = __atomic_load_n(&g_active_method, __ATOMIC_ACQUIRE);
    int locked = __atomic_load_n(&g_method_locked, __ATOMIC_ACQUIRE);
    int max_modes_to_try = (g_configured_method == FP_METHOD_AUTO && !locked) ? num_candidates : 1;

    int start_idx = 0;
    for (int i = 0; i < num_candidates; i++) {
        if (auto_candidates[i] == cur_mode) {
            start_idx = i;
            break;
        }
    }

    for (int m_step = 0; m_step < max_modes_to_try; m_step++) {
        int mode = (max_modes_to_try > 1) ? auto_candidates[(start_idx + m_step) % num_candidates] : cur_mode;
        uint32_t eff_max_down = (mode == FP_METHOD_HEAD && max_down > 2048) ? 2048 : max_down;

        const char *http_method = "POST";
        bool send_in_header = false;
        resolve_op_http_method(mode, op, eff_max_down, &http_method, &send_in_header);

        size_t wire_len = FP_REQ_HEADER_SIZE + payload_len;
        uint8_t *wire = (uint8_t *)malloc(wire_len);
        if (!wire) return -1;

        memcpy(wire, FP_PROTO_MAGIC, 4);
        wire[4] = op;
        wire[5] = flags;
        if (sid) memcpy(wire + 6, sid, 16);
        else memset(wire + 6, 0, 16);
        put_be64(wire + 22, up_off);
        put_be64(wire + 30, ack_down_off);
        put_be64(wire + 38, want_down_off);
        put_be32(wire + 46, eff_max_down);
        put_be32(wire + 50, payload_len);
        if (payload_len > 0 && payload) {
            memcpy(wire + FP_REQ_HEADER_SIZE, payload, payload_len);
        }

        uint8_t body_sum[32];
        sha256_hash(wire, wire_len, body_sum);

        uint8_t nonce_raw[12];
        if (csprng_bytes(nonce_raw, sizeof(nonce_raw)) < 0) {
            free(wire);
            return -1;
        }
        char nonce_hex[25];
        bytes_to_hex(nonce_raw, sizeof(nonce_raw), nonce_hex);

        char ts_str[32];
        snprintf(ts_str, sizeof(ts_str), "%llu", (unsigned long long)epoch_ms());

        uint8_t sig[32];
        compute_fp_hmac_sig(http_method, g_http_path, g_client_id, ts_str, nonce_hex, body_sum, sig);
        char sig_hex[65];
        bytes_to_hex(sig, 32, sig_hex);

        char auth_hdr[384];
        snprintf(auth_hdr, sizeof(auth_hdr), "%s:%s:%s:%s", g_client_id, ts_str, nonce_hex, sig_hex);

        bool try_next_method = false;
        for (int attempt = 0; attempt < 2; attempt++) {
            bool reused = false;
            int fd = acquire_pool_conn(&reused);
            if (fd < 0) {
                free(wire);
                return -1;
            }
            int rc = fp_rpc_once(fd, http_method, send_in_header, nonce_hex,
                                 wire, wire_len, auth_hdr, out_resp, down_buf, down_buf_cap);
            if (rc == 0 || rc == 1) {
                if (rc == 0) release_pool_conn(fd);
                else close(fd);
                free(wire);
                if (!locked) {
                    __atomic_store_n(&g_active_method, mode, __ATOMIC_RELEASE);
                    __atomic_store_n(&g_method_locked, 1, __ATOMIC_RELEASE);
                }
                return 0;
            }
            close(fd);
            if (rc == -2) {
                try_next_method = true;
                break;
            }
            if (!reused) {
                try_next_method = true;
                break;
            }
        }
        free(wire);
        if (!try_next_method) break;
    }

    return -1;
}

typedef struct {
    int             socks_fd;
    uint8_t         sid[16];
    bool            stop;
} fp_client_sess_t;

static void *downlink_worker(void *arg) {
    fp_client_sess_t *cs = (fp_client_sess_t *)arg;
    uint8_t *down_buf = (uint8_t *)malloc(262144);
    if (!down_buf) {
        __atomic_store_n(&cs->stop, true, __ATOMIC_RELEASE);
        shutdown(cs->socks_fd, SHUT_RDWR);
        return NULL;
    }

    uint64_t down_cursor = 0;
    while (!__atomic_load_n(&cs->stop, __ATOMIC_ACQUIRE) && !__atomic_load_n(&g_stop, __ATOMIC_ACQUIRE)) {
        fp_parsed_resp_t resp;
        if (fp_rpc(FP_OP_DOWN, 0, cs->sid, 0, down_cursor, down_cursor, 262144,
                   NULL, 0, &resp, down_buf, 262144) < 0) {
            break;
        }
        if (resp.status != FP_STATUS_OK) {
            break;
        }
        if (resp.payload_len > 0 && resp.down_offset_end > down_cursor) {
            size_t skip = 0;
            if (resp.down_offset_start < down_cursor) {
                skip = (size_t)(down_cursor - resp.down_offset_start);
            }
            if (skip < resp.payload_len) {
                size_t to_write = resp.payload_len - skip;
                if (write_all(cs->socks_fd, down_buf + skip, to_write) < 0) {
                    break;
                }
                down_cursor += to_write;
            }
        }
        if (resp.flags & (FP_RESP_FLAG_TARGET_RESET | FP_RESP_FLAG_SESSION_CLOSED)) {
            break;
        }
        if ((resp.flags & FP_RESP_FLAG_TARGET_EOF) && down_cursor >= resp.target_eof_offset) {
            break;
        }
    }

    __atomic_store_n(&cs->stop, true, __ATOMIC_RELEASE);
    shutdown(cs->socks_fd, SHUT_WR);
    free(down_buf);
    return NULL;
}

static void *socks_client_thread(void *arg) {
    int sfd = (int)(intptr_t)arg;
    int one = 1;
    setsockopt(sfd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

    /* 1. SOCKS5 Greeting */
    uint8_t hdr[2];
    if (read_exact_timeout(sfd, hdr, 2, 10000) < 0 || hdr[0] != 0x05) {
        goto out_close;
    }
    uint8_t nmethods = hdr[1];
    uint8_t methods[256];
    if (nmethods > 0 && read_exact_timeout(sfd, methods, nmethods, 10000) < 0) {
        goto out_close;
    }
    if (write_all(sfd, "\x05\x00", 2) < 0) {
        goto out_close;
    }

    /* 2. SOCKS5 Request */
    uint8_t req[4];
    if (read_exact_timeout(sfd, req, 4, 10000) < 0 || req[0] != 0x05 || req[1] != 0x01) {
        write_all(sfd, "\x05\x07\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }
    uint8_t atyp = req[3];
    char host[256] = {0};
    if (atyp == 0x01) {
        uint8_t ip4[4];
        if (read_exact_timeout(sfd, ip4, 4, 10000) < 0) goto out_close;
        snprintf(host, sizeof(host), "%u.%u.%u.%u", ip4[0], ip4[1], ip4[2], ip4[3]);
    } else if (atyp == 0x03) {
        uint8_t dlen = 0;
        if (read_exact_timeout(sfd, &dlen, 1, 10000) < 0 || dlen == 0) goto out_close;
        if (read_exact_timeout(sfd, (uint8_t *)host, dlen, 10000) < 0) goto out_close;
        host[dlen] = '\0';
    } else if (atyp == 0x04) {
        uint8_t ip6[16];
        if (read_exact_timeout(sfd, ip6, 16, 10000) < 0) goto out_close;
        char ip6_str[128] = {0};
        if (!inet_ntop(AF_INET6, ip6, ip6_str, sizeof(ip6_str))) goto out_close;
        snprintf(host, sizeof(host), "[%s]", ip6_str);
    } else {
        write_all(sfd, "\x05\x08\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }
    uint8_t pbuf[2];
    if (read_exact_timeout(sfd, pbuf, 2, 10000) < 0) goto out_close;
    uint16_t port = ((uint16_t)pbuf[0] << 8) | pbuf[1];
    if (port == 0) {
        write_all(sfd, "\x05\x04\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }

    /* 3. FPOpOpen */
    char target_spec[300];
    int tlen = snprintf(target_spec, sizeof(target_spec), "%s:%u", host, port);
    if (tlen < 0 || (size_t)tlen >= sizeof(target_spec)) {
        write_all(sfd, "\x05\x04\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }
    uint8_t open_id[16];
    if (csprng_bytes(open_id, 16) < 0) {
        write_all(sfd, "\x05\x01\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }
    open_id[0] |= 0x01;
    fp_parsed_resp_t open_resp;
    uint8_t dummy_down[64];
    if (fp_rpc(FP_OP_OPEN, 0, open_id, 0, 0, 0, 0, (const uint8_t *)target_spec, (uint32_t)tlen,
               &open_resp, dummy_down, sizeof(dummy_down)) < 0 || open_resp.status != FP_STATUS_OK) {
        write_all(sfd, "\x05\x04\x00\x01\x00\x00\x00\x00\x00\x00", 10);
        goto out_close;
    }

    if (write_all(sfd, "\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00", 10) < 0) {
        fp_rpc(FP_OP_CLOSE, 0, open_resp.sid, 0, 0, 0, 0, NULL, 0, &open_resp, dummy_down, sizeof(dummy_down));
        goto out_close;
    }

    fp_client_sess_t cs;
    memset(&cs, 0, sizeof(cs));
    cs.socks_fd = sfd;
    memcpy(cs.sid, open_resp.sid, 16);

    pthread_t dtid;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setstacksize(&attr, 524288);
    bool has_dthread = (pthread_create(&dtid, &attr, downlink_worker, &cs) == 0);
    pthread_attr_destroy(&attr);

    uint8_t *up_buf = has_dthread ? (uint8_t *)malloc(65536) : NULL;
    uint64_t up_offset = 0;
    if (!has_dthread || !up_buf) {
        __atomic_store_n(&cs.stop, true, __ATOMIC_RELEASE);
        shutdown(sfd, SHUT_RDWR);
        if (up_buf) free(up_buf);
    } else {
        while (!__atomic_load_n(&cs.stop, __ATOMIC_ACQUIRE) && !__atomic_load_n(&g_stop, __ATOMIC_ACQUIRE)) {
            struct pollfd pfd = { .fd = sfd, .events = POLLIN };
            int pr = poll(&pfd, 1, 1000);
            if (pr < 0) {
                if (errno == EINTR) continue;
                break;
            }
            if (pr == 0) continue;

            int act_mode = __atomic_load_n(&g_active_method, __ATOMIC_ACQUIRE);
            size_t max_read = (act_mode == FP_METHOD_POST || act_mode == FP_METHOD_PUT) ? 65536 : 2048;
            ssize_t n = recv(sfd, up_buf, max_read, 0);
            if (n > 0) {
                fp_parsed_resp_t uresp;
                if (fp_rpc(FP_OP_UP, 0, cs.sid, up_offset, 0, 0, 0,
                           up_buf, (uint32_t)n, &uresp, dummy_down, sizeof(dummy_down)) < 0 ||
                    uresp.status != FP_STATUS_OK) {
                    __atomic_store_n(&cs.stop, true, __ATOMIC_RELEASE);
                    break;
                }
                up_offset += (uint64_t)n;
            } else if (n < 0 && errno == EINTR) {
                continue;
            } else {
                /* Client sent FIN or socket closed */
                fp_parsed_resp_t uresp;
                fp_rpc(FP_OP_UP, FP_REQ_FLAG_EOF, cs.sid, up_offset, 0, 0, 0,
                       NULL, 0, &uresp, dummy_down, sizeof(dummy_down));
                break;
            }
        }
        free(up_buf);
    }

    if (has_dthread) {
        pthread_join(dtid, NULL);
    }
    fp_parsed_resp_t cresp;
    fp_rpc(FP_OP_CLOSE, 0, cs.sid, 0, 0, 0, 0, NULL, 0, &cresp, dummy_down, sizeof(dummy_down));

out_close:
    close(sfd);
    __atomic_sub_fetch(&g_active_clients, 1, __ATOMIC_RELEASE);
    return NULL;
}

static void parse_host_port_arg(const char *arg, char *out_host, size_t host_sz, uint16_t *out_port) {
    const char *colon = strrchr(arg, ':');
    if (!colon) return;
    size_t l = (size_t)(colon - arg);
    if (l >= host_sz) l = host_sz - 1;
    memcpy(out_host, arg, l);
    out_host[l] = '\0';
    int p = atoi(colon + 1);
    if (p > 0 && p <= 65535) *out_port = (uint16_t)p;
}

static void on_sig(int sig) {
    (void)sig;
    __atomic_store_n(&g_stop, 1, __ATOMIC_RELEASE);
}

#ifndef FPC_NO_MAIN
int main(int argc, char **argv) {
    signal(SIGPIPE, SIG_IGN);
    signal(SIGINT, on_sig);
    signal(SIGTERM, on_sig);
#ifdef __linux__
    prctl(PR_SET_PDEATHSIG, SIGTERM);
#endif

    char uuid_str[64] = "";
    char secret_hex[128] = "";
    char method_str[32] = "";
    const char *env_sec = getenv("FP_CLIENT_SECRET_HEX");
    if (!env_sec || !env_sec[0]) {
        env_sec = getenv("FP_SECRET_HEX");
    }
    if (env_sec && env_sec[0]) {
        snprintf(secret_hex, sizeof(secret_hex), "%s", env_sec);
    }
    const char *env_meth = getenv("FP_CLIENT_METHOD");
    if (env_meth && env_meth[0]) {
        snprintf(method_str, sizeof(method_str), "%s", env_meth);
    }

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "-version") == 0 || strcmp(argv[i], "--version") == 0 || strcmp(argv[i], "-v") == 0) {
            printf("awgm-fpc %s\n", FPC_VERSION);
            return 0;
        } else if (strcmp(argv[i], "-listen") == 0 && i + 1 < argc) {
            parse_host_port_arg(argv[++i], g_listen_host, sizeof(g_listen_host), &g_listen_port);
        } else if (strcmp(argv[i], "-connect") == 0 && i + 1 < argc) {
            parse_host_port_arg(argv[++i], g_connect_host, sizeof(g_connect_host), &g_connect_port);
        } else if (strcmp(argv[i], "-host") == 0 && i + 1 < argc) {
            snprintf(g_http_host, sizeof(g_http_host), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-path") == 0 && i + 1 < argc) {
            snprintf(g_http_path, sizeof(g_http_path), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-client-id") == 0 && i + 1 < argc) {
            snprintf(g_client_id, sizeof(g_client_id), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-secret-hex") == 0 && i + 1 < argc) {
            snprintf(secret_hex, sizeof(secret_hex), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-uuid") == 0 && i + 1 < argc) {
            snprintf(uuid_str, sizeof(uuid_str), "%s", argv[++i]);
        } else if (strcmp(argv[i], "-method") == 0 && i + 1 < argc) {
            snprintf(method_str, sizeof(method_str), "%s", argv[++i]);
        }
    }

    g_configured_method = parse_method_mode(method_str);
    if (g_configured_method == FP_METHOD_AUTO) {
        g_active_method = FP_METHOD_POST;
        g_method_locked = 0;
    } else {
        g_active_method = (int)g_configured_method;
        g_method_locked = 1;
    }

    if (!g_client_id[0] || strlen(secret_hex) != 64 || hex_to_bytes(secret_hex, g_fp_secret, 32) != 32) {
        fprintf(stderr, "awgm-fpc: FiniteProto v2 requires -client-id and 64-hex secret (via -secret-hex or FP_CLIENT_SECRET_HEX); legacy UUID-derived keys are rejected\n");
        return 1;
    }
    uint8_t nz = 0;
    for (int i = 0; i < 32; i++) nz |= g_fp_secret[i];
    if (!nz) {
        fprintf(stderr, "awgm-fpc: invalid all-zero FiniteProto secret\n");
        return 1;
    }
    uint8_t u[16];
    if ((uuid_str[0] && parse_uuid_str(uuid_str, u) && is_uuid_derived_fp_secret(g_fp_secret, u)) ||
        (parse_uuid_str(g_client_id, u) && is_uuid_derived_fp_secret(g_fp_secret, u))) {
        fprintf(stderr, "awgm-fpc: legacy UUID-derived FiniteProto secret rejected; migration required\n");
        return 1;
    }

    if (!g_http_path[0] || g_http_path[0] != '/') {
        size_t plen = strlen(g_http_path);
        if (plen >= sizeof(g_http_path) - 1) plen = sizeof(g_http_path) - 2;
        memmove(g_http_path + 1, g_http_path, plen);
        g_http_path[0] = '/';
        g_http_path[plen + 1] = '\0';
    }

    int srv_fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (srv_fd < 0) return 1;
    int one = 1;
    setsockopt(srv_fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));

    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(g_listen_port);
    if (inet_pton(AF_INET, g_listen_host, &sa.sin_addr) != 1) {
        sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    }
    if (bind(srv_fd, (struct sockaddr *)&sa, sizeof(sa)) < 0 || listen(srv_fd, 256) < 0) {
        close(srv_fd);
        return 1;
    }

    while (!__atomic_load_n(&g_stop, __ATOMIC_ACQUIRE)) {
        struct pollfd pfd = { .fd = srv_fd, .events = POLLIN };
        if (poll(&pfd, 1, 1000) <= 0) continue;
        struct sockaddr_in csa;
        socklen_t clen = sizeof(csa);
        int cfd = accept(srv_fd, (struct sockaddr *)&csa, &clen);
        if (cfd < 0) continue;

        int active = __atomic_add_fetch(&g_active_clients, 1, __ATOMIC_ACQ_REL);
        if (active > MAX_FPC_CLIENTS) {
            __atomic_sub_fetch(&g_active_clients, 1, __ATOMIC_RELEASE);
            close(cfd);
            continue;
        }

        pthread_t tid;
        pthread_attr_t attr;
        pthread_attr_init(&attr);
        pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
        pthread_attr_setstacksize(&attr, 524288);
        if (pthread_create(&tid, &attr, socks_client_thread, (void *)(intptr_t)cfd) != 0) {
            __atomic_sub_fetch(&g_active_clients, 1, __ATOMIC_RELEASE);
            close(cfd);
        }
        pthread_attr_destroy(&attr);
    }

    close(srv_fd);
    close_all_pool_conns();
    return 0;
}
#endif /* FPC_NO_MAIN */
