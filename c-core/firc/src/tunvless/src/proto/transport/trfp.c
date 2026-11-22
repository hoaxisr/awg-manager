/* FiniteProto v2 transport for tunvless.
 * Native HTTP-based multiplexed transport over TLS 1.3 keep-alive connections.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <stdint.h>
#include <stdbool.h>
#include <ctype.h>
#include <unistd.h>
#include <errno.h>
#include <poll.h>
#include <fcntl.h>
#include <pthread.h>
#include <sys/socket.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>

#include "transport.h"
#include "scrypto.h"
#include "vless.h"
#include "reality.h"
#include "tstack.h"

static volatile int g_fp_live_ctxs;

int tr_fp_live_contexts(void) {
    return __atomic_load_n(&g_fp_live_ctxs, __ATOMIC_ACQUIRE);
}

/* Wire protocol constants */
#define FP_PROTO_MAGIC       "FP01"
#define FP_PROTO_VERSION     "fp/1"
#define FP_REQ_HEADER_SIZE   54
#define FP_RESP_HEADER_SIZE  58

#define FP_OP_OPEN           1
#define FP_OP_UP             2
#define FP_OP_DOWN           3
#define FP_OP_CLOSE          4

#define FP_REQ_FLAG_EOF      (1 << 0)

#define FP_STATUS_OK              0
#define FP_STATUS_BAD_REQUEST     1
#define FP_STATUS_UNAUTHORIZED    2
#define FP_STATUS_FORBIDDEN       3
#define FP_STATUS_UNKNOWN_SESSION 4
#define FP_STATUS_RESOURCE_LIMIT  5
#define FP_STATUS_BACKPRESSURE    6
#define FP_STATUS_DIAL_ERROR      7

#define FP_RESP_FLAG_TARGET_EOF     (1 << 0)
#define FP_RESP_FLAG_TARGET_RESET   (1 << 1)
#define FP_RESP_FLAG_SESSION_CLOSED (1 << 2)

#define FP_MAX_REQ_PAYLOAD   (256 * 1024)
#define FP_MAX_RESP_PAYLOAD  (512 * 1024)

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

/* Big-endian helpers */
static inline void put_be32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)v;
}

static inline void put_be64(uint8_t *p, uint64_t v) {
    put_be32(p, (uint32_t)(v >> 32));
    put_be32(p + 4, (uint32_t)v);
}

static inline uint32_t get_be32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | (uint32_t)p[3];
}

static inline uint64_t get_be64(const uint8_t *p) {
    return ((uint64_t)get_be32(p) << 32) | (uint64_t)get_be32(p + 4);
}

static int hex_digit_val(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static int hex64_to_bytes(const char *hex, uint8_t out[32]) {
    if (!hex || strlen(hex) != 64) return -1;
    for (int i = 0; i < 32; i++) {
        int hi = hex_digit_val(hex[i * 2]);
        int lo = hex_digit_val(hex[i * 2 + 1]);
        if (hi < 0 || lo < 0) return -1;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return 0;
}

static void bytes_to_hex(const uint8_t *in, size_t len, char *out) {
    static const char hx[] = "0123456789abcdef";
    for (size_t i = 0; i < len; i++) {
        out[i * 2]     = hx[(in[i] >> 4) & 0x0F];
        out[i * 2 + 1] = hx[in[i] & 0x0F];
    }
    out[len * 2] = '\0';
}

static int base64url_encode(const uint8_t *in, size_t in_len, char *out, size_t max_out) {
    static const char b64u[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    if (!out || max_out == 0) return -1;
    if (in_len == 0) { out[0] = '\0'; return 0; }
    if (!in) return -1;
    size_t needed = (in_len / 3) * 4 + ((in_len % 3 == 0) ? 0 : (in_len % 3 + 1));
    if (needed + 1 > max_out) return -1;

    size_t i = 0, o = 0;
    while (i + 3 <= in_len) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8) | (uint32_t)in[i + 2];
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
        out[o++] = b64u[(v >> 6) & 0x3F];
        out[o++] = b64u[v & 0x3F];
        i += 3;
    }
    size_t rem = in_len - i;
    if (rem == 1) {
        uint32_t v = (uint32_t)in[i] << 16;
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
    } else if (rem == 2) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8);
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
        out[o++] = b64u[(v >> 6) & 0x3F];
    }
    out[o] = '\0';
    return (int)o;
}

static int b64url_val(char c) {
    if (c >= 'A' && c <= 'Z') return c - 'A';
    if (c >= 'a' && c <= 'z') return c - 'a' + 26;
    if (c >= '0' && c <= '9') return c - '0' + 52;
    if (c == '-' || c == '+') return 62;
    if (c == '_' || c == '/') return 63;
    return -1;
}

static int base64url_decode(const char *in, size_t in_len, uint8_t *out, size_t max_out) {
    if (!in || !out) return -1;
    uint8_t q[4];
    int qlen = 0;
    size_t o = 0;

    for (size_t i = 0; i < in_len; i++) {
        char c = in[i];
        if (c == '=' || c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ',') continue;
        int v = b64url_val(c);
        if (v < 0) return -1;
        q[qlen++] = (uint8_t)v;
        if (qlen == 4) {
            if (o + 3 > max_out) return -1;
            out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
            out[o++] = (uint8_t)(((q[1] & 0x0F) << 4) | (q[2] >> 2));
            out[o++] = (uint8_t)(((q[2] & 0x03) << 6) | q[3]);
            qlen = 0;
        }
    }
    if (qlen == 1) return -1;
    if (qlen == 2) {
        if (o + 1 > max_out) return -1;
        out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
    } else if (qlen == 3) {
        if (o + 2 > max_out) return -1;
        out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
        out[o++] = (uint8_t)(((q[1] & 0x0F) << 4) | (q[2] >> 2));
    }
    return (int)o;
}

static int64_t fp_now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000LL + (int64_t)(ts.tv_nsec / 1000000LL);
}

static int64_t fp_epoch_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000LL + (int64_t)(ts.tv_nsec / 1000000LL);
}

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

static void compute_fp_hmac_sig(const uint8_t secret[32], const char *method, const char *path,
                                const char *client_id, const char *ts_str, const char *nonce_hex,
                                const uint8_t body_sha256[32], uint8_t out_sig[32]) {
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
    sc_hmac(SC_SHA256, secret, 32, msg, off, out_sig);
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

/* TLS link buffered reading */
struct tr_reader {
    struct tr_link *l;
    const volatile int *stop;
    unsigned char buf[TLS13_MAX_REC];
    size_t off;
    size_t len;
};

static int tr_reader_fill(struct tr_reader *r, int timeout_ms) {
    int64_t deadline = fp_now_ms() + timeout_ms;
    while (r->off >= r->len) {
        if (r->stop && __atomic_load_n(r->stop, __ATOMIC_ACQUIRE)) return TR_ECLOSED;
        r->off = 0;
        r->len = 0;
        size_t got = 0;
        int rc = tr_link_read(r->l, r->buf, sizeof(r->buf), &got);
        if (rc) return rc;
        if (got > 0) {
            r->len = got;
            return 0;
        }
        if (!r->l->plain && tls13_has_record(&r->l->tls)) continue;
        int rem = (int)(deadline - fp_now_ms());
        if (rem <= 0) return TR_EIO;
        struct pollfd pfd = { .fd = r->l->fd, .events = POLLIN };
        int pr = poll(&pfd, 1, rem > 100 ? 100 : rem);
        if (pr < 0 && errno != EINTR) return TR_EIO;
    }
    return 0;
}

static int tr_reader_getc(struct tr_reader *r, unsigned char *out, int timeout_ms) {
    if (r->off >= r->len) {
        int rc = tr_reader_fill(r, timeout_ms);
        if (rc) return rc;
    }
    *out = r->buf[r->off++];
    return 0;
}

static int tr_read_http_line(struct tr_reader *r, char *out, size_t out_sz, int timeout_ms) {
    if (out_sz == 0) return -1;
    size_t idx = 0;
    while (idx + 1 < out_sz) {
        unsigned char c;
        int rc = tr_reader_getc(r, &c, timeout_ms);
        if (rc) return -1;
        if (c == '\r') continue;
        if (c == '\n') {
            out[idx] = '\0';
            return (int)idx;
        }
        out[idx++] = (char)c;
    }
    return -1;
}

static int tr_read_exact_buffered(struct tr_reader *r, unsigned char *dst, size_t len, int timeout_ms) {
    size_t done = 0;
    int64_t deadline = fp_now_ms() + timeout_ms;
    while (done < len) {
        if (r->off >= r->len) {
            int rem = (int)(deadline - fp_now_ms());
            if (rem <= 0) return TR_EIO;
            int rc = tr_reader_fill(r, rem);
            if (rc) return rc;
        }
        size_t avail = r->len - r->off;
        size_t take = (len - done < avail) ? (len - done) : avail;
        memcpy(dst + done, r->buf + r->off, take);
        r->off += take;
        done += take;
    }
    return 0;
}

/* Per-session HTTP/1.1 keep-alive pool of heap-allocated TLS 1.3 links.
 * Heap pointers avoid copying struct tls13 by value (see tls13.h) and ensure
 * deterministic teardown of every socket and cipher context on session close. */
#define FP_CTX_LINKS_MAX 4

struct fp_link_slot {
    struct tr_link *link;
    bool in_use;
    bool dead;
};

/* FiniteProto Context per connection */
struct fp_ctx {
    int app_fd;
    int worker_fd;
    pthread_t tid;
    pthread_t dtid;
    volatile int stop;
    volatile int aborted;
    volatile int started;
    volatile int refs;
    pthread_mutex_t pool_mu;
    struct fp_link_slot links[FP_CTX_LINKS_MAX];
    struct tr_node node;
    char *node_strs;
    uint8_t secret[32];
    char client_id[64];
    char path[256];
    char authority[128];
    fp_method_mode_t configured_method;
    volatile int active_method;
    volatile int method_locked;
};

static int fp_clone_tr_node(struct fp_ctx *ctx, const struct tr_node *src) {
    ctx->node = *src;
    ctx->node.dl = NULL;
    ctx->node_strs = NULL;

    const char **fields[] = {
        &ctx->node.proto, &ctx->node.uuid, &ctx->node.host,
        &ctx->node.type, &ctx->node.security, &ctx->node.sni,
        &ctx->node.fp, &ctx->node.pbk, &ctx->node.sid,
        &ctx->node.path, &ctx->node.http_host, &ctx->node.headers,
        &ctx->node.service, &ctx->node.mode, &ctx->node.pqv, &ctx->node.pcs,
        &ctx->node.pks, &ctx->node.vcn, &ctx->node.ech,
        &ctx->node.encryption, &ctx->node.xhx, &ctx->node.fm,
        &ctx->node.fp_id, &ctx->node.fp_secret, &ctx->node.fp_method,
    };
    const size_t nf = sizeof(fields) / sizeof(fields[0]);
    size_t total = 0;
    for (size_t i = 0; i < nf; i++) {
        if (*fields[i]) total += strlen(*fields[i]) + 1;
    }
    if (total == 0) return 0;

    char *buf = malloc(total);
    if (!buf) return -1;
    char *p = buf;
    for (size_t i = 0; i < nf; i++) {
        if (*fields[i]) {
            size_t len = strlen(*fields[i]) + 1;
            memcpy(p, *fields[i], len);
            *fields[i] = p;
            p += len;
        }
    }
    ctx->node_strs = buf;
    return 0;
}

static void fp_ctx_abort_links(struct fp_ctx *ctx) {
    pthread_mutex_lock(&ctx->pool_mu);
    for (int i = 0; i < FP_CTX_LINKS_MAX; i++) {
        if (ctx->links[i].link) {
            ctx->links[i].dead = true;
            if (ctx->links[i].link->fd >= 0) {
                shutdown(ctx->links[i].link->fd, SHUT_RDWR);
            }
        }
    }
    pthread_mutex_unlock(&ctx->pool_mu);
}

static int fp_pool_acquire(struct fp_ctx *ctx, int timeout_s, int *out_slot, bool *reused) {
    if (__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) return -1;

    pthread_mutex_lock(&ctx->pool_mu);
    for (int i = 0; i < FP_CTX_LINKS_MAX; i++) {
        struct fp_link_slot *s = &ctx->links[i];
        if (!s->link || s->in_use) continue;
        if (s->dead || s->link->fd < 0 ||
            (!s->link->plain && (tls13_has_record(&s->link->tls) || tls13_buffered(&s->link->tls) > 0))) {
            struct tr_link *old = s->link;
            s->link = NULL;
            s->dead = false;
            pthread_mutex_unlock(&ctx->pool_mu);
            tr_link_close(old);
            free(old);
            pthread_mutex_lock(&ctx->pool_mu);
            continue;
        }
        struct pollfd pfd = { .fd = s->link->fd, .events = POLLIN };
        if (poll(&pfd, 1, 0) > 0) {
            uint8_t dummy;
            if (recv(s->link->fd, &dummy, 1, MSG_PEEK | MSG_DONTWAIT) <= 0) {
                struct tr_link *old = s->link;
                s->link = NULL;
                s->dead = false;
                pthread_mutex_unlock(&ctx->pool_mu);
                tr_link_close(old);
                free(old);
                pthread_mutex_lock(&ctx->pool_mu);
                continue;
            }
        }
        s->in_use = true;
        *out_slot = i;
        pthread_mutex_unlock(&ctx->pool_mu);
        if (reused) *reused = true;
        return 0;
    }
    pthread_mutex_unlock(&ctx->pool_mu);

    if (reused) *reused = false;
    struct tr_link *nl = calloc(1, sizeof(*nl));
    if (!nl) return -1;
    nl->fd = -1;
    if (tr_link_open(nl, &ctx->node, "http/1.1", timeout_s) != 0) {
        free(nl);
        return -1;
    }

    pthread_mutex_lock(&ctx->pool_mu);
    if (__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
        pthread_mutex_unlock(&ctx->pool_mu);
        tr_link_close(nl);
        free(nl);
        return -1;
    }
    for (int i = 0; i < FP_CTX_LINKS_MAX; i++) {
        if (!ctx->links[i].link && !ctx->links[i].in_use) {
            ctx->links[i].link = nl;
            ctx->links[i].in_use = true;
            ctx->links[i].dead = false;
            *out_slot = i;
            pthread_mutex_unlock(&ctx->pool_mu);
            return 0;
        }
    }
    pthread_mutex_unlock(&ctx->pool_mu);
    tr_link_close(nl);
    free(nl);
    return -1;
}

static void fp_pool_release_slot(struct fp_ctx *ctx, int slot, bool keep_alive) {
    if (slot < 0 || slot >= FP_CTX_LINKS_MAX) return;
    struct tr_link *to_close = NULL;
    pthread_mutex_lock(&ctx->pool_mu);
    struct fp_link_slot *s = &ctx->links[slot];
    if (!keep_alive || s->dead || __atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
        to_close = s->link;
        s->link = NULL;
        s->dead = false;
    }
    s->in_use = false;
    pthread_mutex_unlock(&ctx->pool_mu);

    if (to_close) {
        tr_link_close(to_close);
        free(to_close);
    }
}

static void fp_ctx_release(struct fp_ctx *ctx) {
    if (!ctx) return;
    if (__atomic_sub_fetch(&ctx->refs, 1, __ATOMIC_ACQ_REL) == 0) {
        for (int i = 0; i < FP_CTX_LINKS_MAX; i++) {
            if (ctx->links[i].link) {
                tr_link_close(ctx->links[i].link);
                free(ctx->links[i].link);
                ctx->links[i].link = NULL;
            }
        }
        pthread_mutex_destroy(&ctx->pool_mu);
        if (ctx->app_fd >= 0) close(ctx->app_fd);
        if (ctx->worker_fd >= 0) close(ctx->worker_fd);
        free(ctx->node_strs);
        free(ctx);
        __atomic_sub_fetch(&g_fp_live_ctxs, 1, __ATOMIC_RELEASE);
    }
}

static int fp_rpc_once(struct fp_ctx *ctx, struct tr_link *l, const char *http_method,
                       bool send_in_header, const char *nonce_hex,
                       const uint8_t *wire_req, size_t wire_len, const char *auth_hdr,
                       fp_parsed_resp_t *out_resp, uint8_t *down_buf, size_t down_buf_cap) {
    const char *qpath = ctx->path[0] ? ctx->path : "/cdn-bridge";
    const char *host_hdr = ctx->authority[0] ? ctx->authority : ctx->node.host;

    if (send_in_header) {
        size_t b64_cap = (wire_len * 4) / 3 + 16;
        char *b64 = malloc(b64_cap);
        if (!b64) return -1;
        if (base64url_encode(wire_req, wire_len, b64, b64_cap) < 0) {
            free(b64);
            return -1;
        }
        size_t hdr_cap = b64_cap + 1024;
        char *req_hdr = malloc(hdr_cap);
        if (!req_hdr) { free(b64); return -1; }
        const char *qsep = strchr(qpath, '?') ? "&" : "?";
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
            http_method, qpath, qsep, nonce_hex,
            host_hdr, FP_PROTO_VERSION, auth_hdr, b64);
        free(b64);
        if (hlen < 0 || (size_t)hlen >= hdr_cap) {
            free(req_hdr);
            return -1;
        }
        int wrc = tr_link_write(l, (const unsigned char *)req_hdr, (size_t)hlen);
        free(req_hdr);
        if (wrc < 0) return -1;
    } else {
        char req_hdr[1024];
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
            http_method, qpath, host_hdr, FP_PROTO_VERSION, auth_hdr, wire_len);
        if (hlen < 0 || (size_t)hlen >= sizeof(req_hdr)) return -1;
        if (tr_link_write(l, (const unsigned char *)req_hdr, (size_t)hlen) < 0) return -1;
        if (tr_link_write(l, wire_req, wire_len) < 0) return -1;
    }

    struct tr_reader r;
    r.l = l;
    r.stop = &ctx->stop;
    r.off = 0;
    r.len = 0;

    char line[4096];
    if (tr_read_http_line(&r, line, sizeof(line), 20000) <= 0) return -1;
    int http_status = 0;
    if (sscanf(line, "HTTP/%*s %d", &http_status) != 1) return -1;

    int64_t content_len = -1;
    bool chunked = false;
    bool conn_close = false;
    bool has_fp_version = false;
    char resp_hdr_b64[128] = {0};
    char resp_data_b64[3200] = {0};

    while (1) {
        int len = tr_read_http_line(&r, line, sizeof(line), 15000);
        if (len < 0) return -1;
        if (len == 0) break;
        if (strncasecmp(line, "Content-Length:", 15) == 0) {
            errno = 0;
            char *endp = NULL;
            const char *vp = line + 15;
            while (*vp == ' ' || *vp == '\t') vp++;
            if (*vp < '0' || *vp > '9') return -1;
            long long cl = strtoll(vp, &endp, 10);
            while (endp && (*endp == ' ' || *endp == '\t')) endp++;
            if (errno != 0 || !endp || *endp != '\0' || cl < 0) return -1;
            content_len = (int64_t)cl;
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
        return -2;
    }

    if (is_head_req || (content_len == 0 && !chunked && resp_hdr_b64[0] != '\0')) {
        if (resp_hdr_b64[0] == '\0') return -2;
        uint8_t rhdr[FP_RESP_HEADER_SIZE + 8];
        int dlen = base64url_decode(resp_hdr_b64, strlen(resp_hdr_b64), rhdr, sizeof(rhdr));
        if (dlen != FP_RESP_HEADER_SIZE || memcmp(rhdr, FP_PROTO_MAGIC, 4) != 0) return -1;
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
            if (out_resp->payload_len > down_buf_cap || resp_data_b64[0] == '\0') return -1;
            int plen = base64url_decode(resp_data_b64, strlen(resp_data_b64), down_buf, down_buf_cap);
            if (plen < 0 || (uint32_t)plen != out_resp->payload_len) return -1;
        }
        if (r.off < r.len) conn_close = true;
        return conn_close ? 1 : 0;
    }

    uint8_t *full_body = NULL;
    size_t full_len = 0;

    if (chunked) {
        size_t cap = 4096;
        full_body = malloc(cap);
        if (!full_body) return -1;
        while (1) {
            char cl_line[64];
            if (tr_read_http_line(&r, cl_line, sizeof(cl_line), 15000) < 0) {
                free(full_body);
                return -1;
            }
            const char *cp = cl_line;
            while (*cp == ' ' || *cp == '\t') cp++;
            if (!isxdigit((unsigned char)*cp)) {
                free(full_body);
                return -1;
            }
            errno = 0;
            char *endp = NULL;
            unsigned long long raw_clen = strtoull(cp, &endp, 16);
            if (errno != 0 || endp == cp ||
                (*endp != '\0' && *endp != ';' && *endp != ' ' && *endp != '\t') ||
                raw_clen > (unsigned long long)(FP_RESP_HEADER_SIZE + FP_MAX_RESP_PAYLOAD)) {
                free(full_body);
                return -1;
            }
            size_t clen = (size_t)raw_clen;
            if (clen == 0) {
                char tr_line[1024];
                for (int tr = 0; tr < 32; tr++) {
                    int tlen = tr_read_http_line(&r, tr_line, sizeof(tr_line), 5000);
                    if (tlen < 0) {
                        free(full_body);
                        return -1;
                    }
                    if (tlen == 0) break;
                    if (tr == 31) {
                        free(full_body);
                        return -1;
                    }
                }
                break;
            }
            if (clen > (size_t)(FP_RESP_HEADER_SIZE + FP_MAX_RESP_PAYLOAD) - full_len) {
                free(full_body);
                return -1;
            }
            size_t needed = full_len + clen;
            if (needed > cap) {
                while (cap < needed) cap *= 2;
                uint8_t *nb = realloc(full_body, cap);
                if (!nb) { free(full_body); return -1; }
                full_body = nb;
            }
            if (tr_read_exact_buffered(&r, full_body + full_len, clen, 15000) < 0) {
                free(full_body);
                return -1;
            }
            full_len = needed;
            if (tr_read_http_line(&r, cl_line, sizeof(cl_line), 5000) != 0) {
                free(full_body);
                return -1;
            }
        }
    } else {
        if (content_len < (int64_t)FP_RESP_HEADER_SIZE || content_len > (int64_t)(FP_RESP_HEADER_SIZE + FP_MAX_RESP_PAYLOAD)) {
            return (!has_fp_version && http_status != 200) ? -2 : -1;
        }
        full_len = (size_t)content_len;
        full_body = malloc(full_len);
        if (!full_body) return -1;
        if (tr_read_exact_buffered(&r, full_body, full_len, 15000) < 0) {
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
    if (r.off < r.len) conn_close = true;
    return conn_close ? 1 : 0;
}

static int fp_rpc(struct fp_ctx *ctx, uint8_t op, uint8_t flags, const uint8_t sid[16],
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

    int cur_mode = __atomic_load_n(&ctx->active_method, __ATOMIC_ACQUIRE);
    int locked = __atomic_load_n(&ctx->method_locked, __ATOMIC_ACQUIRE);
    int max_modes_to_try = (ctx->configured_method == FP_METHOD_AUTO && !locked) ? num_candidates : 1;

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
        uint8_t *wire = malloc(wire_len);
        if (!wire) return -1;

        memcpy(wire, FP_PROTO_MAGIC, 4);
        wire[4] = op;
        wire[5] = flags;
        memcpy(wire + 6, sid, 16);
        put_be64(wire + 22, up_off);
        put_be64(wire + 30, ack_down_off);
        put_be64(wire + 38, want_down_off);
        put_be32(wire + 46, eff_max_down);
        put_be32(wire + 50, payload_len);
        if (payload_len > 0 && payload) {
            memcpy(wire + FP_REQ_HEADER_SIZE, payload, payload_len);
        }

        uint8_t nonce[16];
        if (xc_random(nonce, 16) != 0) memset(nonce, 0x5a, 16);
        char nonce_hex[33];
        bytes_to_hex(nonce, 16, nonce_hex);

        char ts_str[32];
        snprintf(ts_str, sizeof(ts_str), "%lld", (long long)fp_epoch_ms());

        uint8_t body_sha256[32];
        sc_hash(SC_SHA256, wire, wire_len, body_sha256);

        uint8_t sig[32];
        compute_fp_hmac_sig(ctx->secret, http_method, ctx->path[0] ? ctx->path : "/cdn-bridge",
                            ctx->client_id, ts_str, nonce_hex, body_sha256, sig);

        char sig_hex[65];
        bytes_to_hex(sig, 32, sig_hex);

        char auth_hdr[384];
        snprintf(auth_hdr, sizeof(auth_hdr), "%s:%s:%s:%s", ctx->client_id, ts_str, nonce_hex, sig_hex);

        bool try_next_method = false;
        for (int attempt = 0; attempt < 2; attempt++) {
            bool reused = false;
            int slot = -1;
            if (fp_pool_acquire(ctx, 10, &slot, &reused) != 0) {
                free(wire);
                return -1;
            }
            struct tr_link *l = ctx->links[slot].link;
            int rc = fp_rpc_once(ctx, l, http_method, send_in_header, nonce_hex,
                                 wire, wire_len, auth_hdr, out_resp, down_buf, down_buf_cap);
            if (rc == 0 || rc == 1) {
                fp_pool_release_slot(ctx, slot, rc == 0);
                free(wire);
                if (!locked) {
                    __atomic_store_n(&ctx->active_method, mode, __ATOMIC_RELEASE);
                    __atomic_store_n(&ctx->method_locked, 1, __ATOMIC_RELEASE);
                }
                return 0;
            }
            fp_pool_release_slot(ctx, slot, false);
            if (rc == -2 || !reused) {
                try_next_method = true;
                break;
            }
        }
        free(wire);
        if (!try_next_method) break;
    }
    return -1;
}

struct fp_downlink_arg {
    struct fp_ctx *ctx;
    uint8_t sid[16];
};

static void *fp_downlink_thread(void *arg) {
    struct fp_downlink_arg *da = arg;
    struct fp_ctx *ctx = da->ctx;
    uint8_t sid[16];
    memcpy(sid, da->sid, 16);
    free(da);

    size_t down_cap = 262144;
    uint8_t *down_buf = malloc(down_cap);
    if (!down_buf) {
        __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
        shutdown(ctx->worker_fd, SHUT_WR);
        return NULL;
    }

    uint64_t down_cursor = 0;
    while (!__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
        fp_parsed_resp_t resp;
        if (fp_rpc(ctx, FP_OP_DOWN, 0, sid, 0, down_cursor, down_cursor, (uint32_t)down_cap,
                   NULL, 0, &resp, down_buf, down_cap) < 0) {
            break;
        }
        if (resp.status != FP_STATUS_OK) break;
        if (resp.payload_len > 0 && resp.down_offset_end > down_cursor) {
            if (resp.down_offset_end < resp.down_offset_start ||
                (resp.down_offset_end - resp.down_offset_start) != (uint64_t)resp.payload_len) {
                __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
                break;
            }
            uint64_t skip64 = 0;
            if (resp.down_offset_start < down_cursor) {
                skip64 = down_cursor - resp.down_offset_start;
            }
            if (skip64 < (uint64_t)resp.payload_len) {
                size_t skip = (size_t)skip64;
                size_t to_write = (size_t)resp.payload_len - skip;
                size_t off = 0;
                while (off < to_write) {
                    ssize_t w = send(ctx->worker_fd, down_buf + skip + off, to_write - off, MSG_NOSIGNAL);
                    if (w <= 0) {
                        if (w < 0 && errno == EINTR) continue;
                        __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
                        break;
                    }
                    off += (size_t)w;
                }
                down_cursor += to_write;
            }
        }
        if (resp.flags & (FP_RESP_FLAG_TARGET_RESET | FP_RESP_FLAG_SESSION_CLOSED)) break;
        if ((resp.flags & FP_RESP_FLAG_TARGET_EOF) && down_cursor >= resp.target_eof_offset) break;
        if (resp.payload_len == 0 && !__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
            usleep(25000); /* 25 ms adaptive idle backoff */
        }
    }

    __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
    shutdown(ctx->worker_fd, SHUT_WR);
    free(down_buf);
    return NULL;
}

static int fp_recv_until(int fd, const volatile int *stop, unsigned char *buf, size_t cap,
                         size_t *have, size_t need, int64_t dl) {
    if (need > cap) return -1;
    while (*have < need) {
        if (stop && __atomic_load_n(stop, __ATOMIC_ACQUIRE)) return -1;
        int rem = (int)(dl - fp_now_ms());
        if (rem <= 0) return -1;
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int pr = poll(&pfd, 1, rem > 100 ? 100 : rem);
        if (pr < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (pr == 0) continue;
        ssize_t r = recv(fd, buf + *have, cap - *have, 0);
        if (r <= 0) {
            if (r < 0 && errno == EINTR) continue;
            return -1;
        }
        *have += (size_t)r;
    }
    return 0;
}

static void *fp_session_worker(void *arg) {
    struct fp_ctx *ctx = arg;
    int fd = ctx->worker_fd;

    /* 1. Read VLESS request header from worker_fd (up to 1 + 16 + 1 + 255 + 4 + 1 + 255 = 533B) */
    unsigned char req_hdr[4096];
    size_t req_n = 0;
    int64_t dl = fp_now_ms() + 10000;
    if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, 22, dl) < 0) {
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }

    /* Parse VLESS: [0x00][16B uuid][addons_len][addons][cmd=0x01 TCP | 0x02 UDP][port BE16][atyp][addr] */
    size_t off = 1 + 16;
    size_t addons_len = req_hdr[off++];
    off += addons_len;
    if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, off + 4, dl) < 0) {
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }
    uint8_t cmd = req_hdr[off++];
    if (cmd != 0x01 && cmd != 0x02) {
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }
    const char *prefix = (cmd == 0x02) ? "udp://" : "";
    uint16_t port = ((uint16_t)req_hdr[off] << 8) | req_hdr[off + 1];
    off += 2;
    uint8_t atyp = req_hdr[off++];

    char target[320] = {0};
    if (atyp == 0x01) { /* IPv4 */
        if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, off + 4, dl) < 0) {
            shutdown(fd, SHUT_RDWR);
            fp_ctx_release(ctx);
            return NULL;
        }
        snprintf(target, sizeof(target), "%s%u.%u.%u.%u:%u",
                 prefix, req_hdr[off], req_hdr[off + 1], req_hdr[off + 2], req_hdr[off + 3], port);
        off += 4;
    } else if (atyp == 0x02) { /* Domain */
        if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, off + 1, dl) < 0) {
            shutdown(fd, SHUT_RDWR);
            fp_ctx_release(ctx);
            return NULL;
        }
        size_t dlen = req_hdr[off++];
        if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, off + dlen, dl) < 0) {
            shutdown(fd, SHUT_RDWR);
            fp_ctx_release(ctx);
            return NULL;
        }
        char dom[256];
        if (dlen >= sizeof(dom)) dlen = sizeof(dom) - 1;
        memcpy(dom, req_hdr + off, dlen);
        dom[dlen] = '\0';
        off += dlen;
        snprintf(target, sizeof(target), "%s%s:%u", prefix, dom, port);
    } else if (atyp == 0x03) { /* IPv6 */
        if (fp_recv_until(fd, &ctx->stop, req_hdr, sizeof(req_hdr), &req_n, off + 16, dl) < 0) {
            shutdown(fd, SHUT_RDWR);
            fp_ctx_release(ctx);
            return NULL;
        }
        char ip6[128];
        inet_ntop(AF_INET6, req_hdr + off, ip6, sizeof(ip6));
        off += 16;
        snprintf(target, sizeof(target), "%s[%s]:%u", prefix, ip6, port);
    } else {
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }

    /* 2. Execute FP_OP_OPEN */
    uint8_t open_id[16];
    if (xc_random(open_id, 16) != 0) memset(open_id, 0x42, 16);
    open_id[0] |= 0x01;

    fp_parsed_resp_t open_resp;
    uint8_t dummy_down[64];
    if (fp_rpc(ctx, FP_OP_OPEN, 0, open_id, 0, 0, 0, 0, (const uint8_t *)target, (uint32_t)strlen(target),
               &open_resp, dummy_down, sizeof(dummy_down)) < 0 || open_resp.status != FP_STATUS_OK) {
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }

    /* 3. Send VLESS success reply "\x00\x00" to worker_fd */
    if (send(fd, "\x00\x00", 2, MSG_NOSIGNAL) != 2) {
        if (!__atomic_load_n(&ctx->aborted, __ATOMIC_ACQUIRE)) {
            fp_rpc(ctx, FP_OP_CLOSE, 0, open_resp.sid, 0, 0, 0, 0, NULL, 0, &open_resp, dummy_down, sizeof(dummy_down));
        }
        shutdown(fd, SHUT_RDWR);
        fp_ctx_release(ctx);
        return NULL;
    }

    /* 4. Start joinable downlink thread (joined below before ctx release) */
    int dtid_started = 0;
    struct fp_downlink_arg *da = malloc(sizeof(*da));
    if (da) {
        da->ctx = ctx;
        memcpy(da->sid, open_resp.sid, 16);
        pthread_attr_t attr;
        pthread_attr_init(&attr);
        if (tstack_set(&attr, 256 * 1024) == 0 &&
            pthread_create(&ctx->dtid, &attr, fp_downlink_thread, da) == 0) {
            dtid_started = 1;
        } else {
            free(da);
            __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
        }
        pthread_attr_destroy(&attr);
    } else {
        __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
    }

    /* 5. Uplink loop */
    uint64_t up_offset = 0;
    /* Extra payload bytes already read past the header? */
    if (!__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE) && off < req_n) {
        size_t initial_bytes = req_n - off;
        fp_parsed_resp_t uresp;
        if (fp_rpc(ctx, FP_OP_UP, 0, open_resp.sid, up_offset, 0, 0, 0,
                   req_hdr + off, (uint32_t)initial_bytes, &uresp, dummy_down, sizeof(dummy_down)) < 0 ||
            uresp.status != FP_STATUS_OK) {
            __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
        } else {
            up_offset += initial_bytes;
        }
    }

    uint8_t *up_buf = malloc(65536);
    if (up_buf) {
        while (!__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
            struct pollfd pfd = { .fd = fd, .events = POLLIN };
            int pr = poll(&pfd, 1, 100);
            if (pr < 0) {
                if (errno == EINTR) continue;
                break;
            }
            if (pr == 0) continue;
            int act_mode = __atomic_load_n(&ctx->active_method, __ATOMIC_ACQUIRE);
            size_t max_read = (act_mode == FP_METHOD_POST || act_mode == FP_METHOD_PUT) ? 65536 : 2048;
            ssize_t n = recv(fd, up_buf, max_read, 0);
            if (n > 0) {
                fp_parsed_resp_t uresp;
                if (fp_rpc(ctx, FP_OP_UP, 0, open_resp.sid, up_offset, 0, 0, 0,
                           up_buf, (uint32_t)n, &uresp, dummy_down, sizeof(dummy_down)) < 0 ||
                    uresp.status != FP_STATUS_OK) {
                    __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
                    break;
                }
                up_offset += (uint64_t)n;
            } else {
                if (!__atomic_load_n(&ctx->aborted, __ATOMIC_ACQUIRE) &&
                    !__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
                    fp_parsed_resp_t uresp;
                    fp_rpc(ctx, FP_OP_UP, FP_REQ_FLAG_EOF, open_resp.sid, up_offset, 0, 0, 0,
                           NULL, 0, &uresp, dummy_down, sizeof(dummy_down));
                }
                break;
            }
        }
        free(up_buf);
    } else {
        __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
    }

    if (!__atomic_load_n(&ctx->aborted, __ATOMIC_ACQUIRE) &&
        !__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
        fp_parsed_resp_t cresp;
        fp_rpc(ctx, FP_OP_CLOSE, 0, open_resp.sid, 0, 0, 0, 0, NULL, 0, &cresp, dummy_down, sizeof(dummy_down));
    }
    __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
    fp_ctx_abort_links(ctx);
    if (dtid_started) {
        pthread_join(ctx->dtid, NULL);
    }
    fp_ctx_release(ctx);
    return NULL;
}

/* Transport Operations for FiniteProto */

static int fp_dial(struct transport *t, const struct tr_node *n, int timeout_s) {
    if (!n->fp_id || !n->fp_id[0]) return TR_ENONODE;
    uint8_t secret[32];
    if (hex64_to_bytes(n->fp_secret, secret) != 0) return TR_ENONODE;

    /* Verify TLS connection to node and seed per-session keep-alive pool */
    struct tr_link *warm_link = calloc(1, sizeof(*warm_link));
    if (!warm_link) return TR_EIO;
    warm_link->fd = -1;
    int rc = tr_link_open(warm_link, n, "http/1.1", timeout_s);
    if (rc != 0) {
        free(warm_link);
        return rc;
    }

    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0, sv) < 0) {
        tr_link_close(warm_link);
        free(warm_link);
        return TR_ESOCK;
    }
    int fl = fcntl(sv[0], F_GETFL, 0);
    if (fl >= 0) fcntl(sv[0], F_SETFL, fl | O_NONBLOCK);

    struct fp_ctx *ctx = calloc(1, sizeof(*ctx));
    if (!ctx) {
        close(sv[0]);
        close(sv[1]);
        tr_link_close(warm_link);
        free(warm_link);
        return TR_EIO;
    }
    pthread_mutex_init(&ctx->pool_mu, NULL);
    ctx->links[0].link = warm_link;
    ctx->links[0].in_use = false;
    ctx->links[0].dead = false;
    ctx->app_fd = sv[0];
    ctx->worker_fd = sv[1];
    ctx->refs = 1; /* 1 for transport; worker ref added lazily in fp_write */
    if (fp_clone_tr_node(ctx, n) != 0) {
        pthread_mutex_destroy(&ctx->pool_mu);
        close(sv[0]);
        close(sv[1]);
        tr_link_close(warm_link);
        free(warm_link);
        free(ctx);
        return TR_EIO;
    }
    memcpy(ctx->secret, secret, 32);
    snprintf(ctx->client_id, sizeof(ctx->client_id), "%s", n->fp_id);
    snprintf(ctx->path, sizeof(ctx->path), "%s", n->path ? n->path : "/cdn-bridge");
    const char *auth_host = NULL;
    if (n->sni && n->sni[0]) {
        auth_host = n->sni;
    } else if (n->http_host && n->http_host[0]) {
        auth_host = n->http_host;
    } else {
        auth_host = n->host;
    }
    if (auth_host && strchr(auth_host, ':') != NULL && auth_host[0] != '[') {
        snprintf(ctx->authority, sizeof(ctx->authority), "[%s]", auth_host);
    } else {
        snprintf(ctx->authority, sizeof(ctx->authority), "%s", auth_host ? auth_host : "");
    }
    ctx->configured_method = parse_method_mode(n->fp_method);
    if (ctx->configured_method == FP_METHOD_AUTO) {
        ctx->active_method = FP_METHOD_POST;
        ctx->method_locked = 0;
    } else {
        ctx->active_method = (int)ctx->configured_method;
        ctx->method_locked = 1;
    }

    __atomic_add_fetch(&g_fp_live_ctxs, 1, __ATOMIC_RELEASE);
    t->fp = ctx;
    return 0;
}

static int fp_write(struct transport *t, const unsigned char *d, size_t n) {
    struct fp_ctx *ctx = t->fp;
    if (!ctx || ctx->app_fd < 0) return TR_ECLOSED;

    if (!ctx->started) {
        ctx->started = 1;
        __atomic_add_fetch(&ctx->refs, 1, __ATOMIC_ACQ_REL);
        pthread_attr_t attr;
        pthread_attr_init(&attr);
        if (pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED) != 0 ||
            tstack_set(&attr, 256 * 1024) != 0 ||
            pthread_create(&ctx->tid, &attr, fp_session_worker, ctx) != 0) {
            pthread_attr_destroy(&attr);
            ctx->started = 0;
            fp_ctx_release(ctx);
            return TR_EIO;
        }
        pthread_attr_destroy(&attr);
    }

    size_t sent = 0;
    while (sent < n) {
        ssize_t w = send(ctx->app_fd, d + sent, n - sent, MSG_NOSIGNAL);
        if (w <= 0) {
            if (w < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
                struct pollfd pfd = { .fd = ctx->app_fd, .events = POLLOUT };
                if (poll(&pfd, 1, 5000) > 0) continue;
                return TR_EIO;
            }
            if (w < 0 && errno == EINTR) continue;
            return TR_EIO;
        }
        sent += (size_t)w;
    }
    return 0;
}

static int fp_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    struct fp_ctx *ctx = t->fp;
    if (!ctx || ctx->app_fd < 0) return TR_ECLOSED;

    ssize_t r = recv(ctx->app_fd, d, cap, 0);
    if (r > 0) {
        *got = (size_t)r;
        return 0;
    }
    if (r == 0) return TR_ECLOSED;
    if (errno == EAGAIN || errno == EWOULDBLOCK) {
        *got = 0;
        return 0;
    }
    if (errno == EINTR) {
        *got = 0;
        return 0;
    }
    return TR_EIO;
}

static int fp_fd(const struct transport *t) {
    return (t && t->fp) ? t->fp->app_fd : -1;
}

static void fp_close(struct transport *t) {
    struct fp_ctx *ctx = t->fp;
    if (!ctx) return;
    t->fp = NULL;
    __atomic_store_n(&ctx->aborted, 1, __ATOMIC_RELEASE);
    __atomic_store_n(&ctx->stop, 1, __ATOMIC_RELEASE);
    fp_ctx_abort_links(ctx);
    if (ctx->app_fd >= 0) {
        close(ctx->app_fd);
        ctx->app_fd = -1;
    }
    if (ctx->worker_fd >= 0) {
        shutdown(ctx->worker_fd, SHUT_RDWR);
    }
    fp_ctx_release(ctx);
}

static int fp_pending(const struct transport *t) {
    (void)t;
    return 0;
}

const struct transport_ops tr_fp = {
    .name    = "xhttp",
    .alpn    = "http/1.1",
    .zc      = 0,
    .open    = NULL,
    .dial    = fp_dial,
    .write   = fp_write,
    .read    = fp_read,
    .fd      = fp_fd,
    .close   = fp_close,
    .pending = fp_pending,
    .moved   = NULL,
    .busy    = NULL,
    .room    = NULL,
};
