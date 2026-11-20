#include "xhttpd.h"
#include <stdio.h>
#include <stdlib.h>
#include <stddef.h>
#include <string.h>
#include <errno.h>
#ifndef _WIN32
#include <strings.h>
#include <unistd.h>
#include <fcntl.h>
#include <poll.h>
#include <sys/socket.h>
#endif

typedef struct {
    uint8_t  op;
    uint8_t  flags;
    uint8_t  sid[16];
    uint64_t up_offset;
    uint64_t ack_down_offset;
    uint64_t want_down_offset;
    uint32_t max_down_bytes;
    const uint8_t *payload;
    uint32_t payload_len;
} fp_req_env_t;

typedef struct {
    uint8_t  status;
    uint8_t  flags;
    uint8_t  sid[16];
    uint64_t acked_up_offset;
    uint64_t down_offset_start;
    uint64_t down_offset_end;
    uint64_t target_eof_offset;
    const uint8_t *payload;
    uint32_t payload_len;
} fp_resp_env_t;

#define FP_MAX_PENDING_UP 16

typedef struct {
    bool     used;
    uint64_t offset;
    uint8_t *data;
    size_t   len;
} fp_pending_chunk_t;

typedef struct {
    int      egress_mode;
    char     interface_name[32];
    char     socks_host[64];
    uint16_t socks_port;
    char     dns_proxy_host[64];
    uint16_t dns_proxy_port;
    bool     allow_private;
    int      idle_timeout_sec;
} fp_egress_snap_t;

static void fp_snapshot_egress(fp_egress_snap_t *dst, const xhttpd_config_t *src) {
    memset(dst, 0, sizeof(*dst));
    if (!src) return;
    dst->egress_mode = src->egress_mode;
    snprintf(dst->interface_name, sizeof(dst->interface_name), "%s", src->interface_name);
    snprintf(dst->socks_host, sizeof(dst->socks_host), "%s", src->socks_host);
    dst->socks_port = src->socks_port;
    snprintf(dst->dns_proxy_host, sizeof(dst->dns_proxy_host), "%s", src->dns_proxy_host);
    dst->dns_proxy_port = src->dns_proxy_port;
    dst->allow_private = src->allow_private;
    dst->idle_timeout_sec = src->idle_timeout_sec;
}

static void fp_populate_egress_cfg(xhttpd_config_t *dst, const fp_egress_snap_t *src) {
    memset(dst, 0, sizeof(*dst));
    if (!src) return;
    dst->egress_mode = src->egress_mode;
    snprintf(dst->interface_name, sizeof(dst->interface_name), "%s", src->interface_name);
    snprintf(dst->socks_host, sizeof(dst->socks_host), "%s", src->socks_host);
    dst->socks_port = src->socks_port;
    snprintf(dst->dns_proxy_host, sizeof(dst->dns_proxy_host), "%s", src->dns_proxy_host);
    dst->dns_proxy_port = src->dns_proxy_port;
    dst->allow_private = src->allow_private;
    dst->idle_timeout_sec = src->idle_timeout_sec;
}

typedef struct fp_session {
    bool            active;
    bool            dialing;
    uint64_t        generation;
    int             ref_count;
    uint8_t         id[16];
    uint8_t         open_id[16];
    char            owner_id[64];
    uint8_t         owner_secret[32];
    char            orig_host[256];
    uint16_t        port;
    bool            vless_framed; /* true if opened with a VLESS stream header */
    fp_egress_snap_t egress_cfg;

    pthread_mutex_t up_mu;
    pthread_mutex_t mu;
    pthread_cond_t  cond;

    uint64_t        cred_gen;
    int             target_fd;
    bool            deferred_dial;
    bool            closed;
    bool            reader_running;
    uint64_t        last_active_ms;

    uint64_t        next_up_offset;
    bool            client_eof_rcvd;
    uint64_t        client_eof_offset;
    bool            client_half_closed;
    fp_pending_chunk_t pending_up[FP_MAX_PENDING_UP];
    size_t          pending_up_bytes;

    uint64_t        base_acked_down;
    uint64_t        generated_down;
    uint64_t        max_sent_down;
    uint8_t        *unacked_down;
    size_t          unacked_len;
    size_t          unacked_cap;
    bool            target_eof;
    bool            target_reset;
} fp_session_t;

/*
 * Lock Order Contract:
 *   1. g_fp_table_mu -> sess->mu
 *   2. sess->up_mu   -> sess->mu
 * g_fp_table_mu and sess->up_mu are never held simultaneously.
 * Every field of fp_session_t (active, dialing, closed, reader_running,
 * ref_count, generation, id, open_id, owner_id, target_fd, and all buffers)
 * is accessed exclusively while holding sess->mu.
 */
static fp_session_t g_fp_sessions[FP_MAX_SESSIONS];
static pthread_mutex_t g_fp_table_mu = PTHREAD_MUTEX_INITIALIZER;
static fp_cred_t g_active_fp_creds[MAX_FP_CREDS];
static int g_num_active_fp_creds = 0;
static uint64_t g_fp_cred_gen = 1;
static size_t g_fp_global_buf_bytes = 0;
static size_t g_fp_max_global_buf = FP_MAX_GLOBAL_BUF;
static int g_fp_alloc_fail_after = -1;
static void (*g_fp_reader_exit_hook)(void) = NULL;
static void (*g_fp_test_pre_auth_hook)(void) = NULL;
static void (*g_fp_test_pre_dial_hook)(void) = NULL;
static void (*g_fp_test_post_dial_hook)(void) = NULL;
static void (*g_fp_test_write_hook)(void) = NULL;
static int g_fp_initial_write_timeout_ms = 5000;
static int g_fp_test_target_sndbuf = 0;
static int g_fp_test_write_wouldblock_count = 0;

void fp_test_set_pre_auth_hook(void (*hook)(void)) {
    __atomic_store_n(&g_fp_test_pre_auth_hook, hook, __ATOMIC_RELEASE);
}

void fp_test_set_dial_hooks(void (*pre_dial_hook)(void), void (*post_dial_hook)(void)) {
    __atomic_store_n(&g_fp_test_pre_dial_hook, pre_dial_hook, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_post_dial_hook, post_dial_hook, __ATOMIC_RELEASE);
}

void fp_test_set_write_hook(void (*hook)(void)) {
    __atomic_store_n(&g_fp_test_write_hook, hook, __ATOMIC_RELEASE);
}

void fp_test_set_initial_write_timeout_ms(int timeout_ms) {
    int v = (timeout_ms > 0) ? timeout_ms : 5000;
    __atomic_store_n(&g_fp_initial_write_timeout_ms, v, __ATOMIC_RELEASE);
}

void fp_test_set_target_sndbuf(int sndbuf_bytes) {
    __atomic_store_n(&g_fp_test_target_sndbuf, sndbuf_bytes > 0 ? sndbuf_bytes : 0, __ATOMIC_RELEASE);
}

int fp_test_get_write_wouldblock_count(void) {
    return __atomic_load_n(&g_fp_test_write_wouldblock_count, __ATOMIC_ACQUIRE);
}

size_t fp_engine_get_global_buf_bytes(void) {
    return __atomic_load_n(&g_fp_global_buf_bytes, __ATOMIC_ACQUIRE);
}

int fp_engine_get_active_sessions(void) {
    int count = 0;
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if ((s->active || s->dialing) && !s->closed) {
            count++;
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
    return count;
}

void fp_test_set_alloc_fail_after(int count) {
    __atomic_store_n(&g_fp_alloc_fail_after, count, __ATOMIC_RELEASE);
}

void fp_test_set_max_global_buf(size_t max_bytes) {
    size_t v = (max_bytes > 0) ? max_bytes : FP_MAX_GLOBAL_BUF;
    __atomic_store_n(&g_fp_max_global_buf, v, __ATOMIC_RELEASE);
}

void fp_test_set_reader_exit_hook(void (*hook)(void)) {
    __atomic_store_n(&g_fp_reader_exit_hook, hook, __ATOMIC_RELEASE);
}

int fp_test_find_session_slot(const uint8_t sid[16]) {
    int found = -1;
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if ((s->active || s->dialing) && !s->closed && memcmp(s->id, sid, 16) == 0) {
            found = i;
            pthread_mutex_unlock(&s->mu);
            break;
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
    return found;
}

int fp_test_get_active_creds_count(void) {
    pthread_mutex_lock(&g_fp_table_mu);
    int cnt = g_num_active_fp_creds;
    pthread_mutex_unlock(&g_fp_table_mu);
    return cnt;
}

bool fp_test_trylock_table(void) {
    if (pthread_mutex_trylock(&g_fp_table_mu) == 0) {
        pthread_mutex_unlock(&g_fp_table_mu);
        return true;
    }
    return false;
}

static bool fp_should_fail_alloc(void) {
    int cur = __atomic_load_n(&g_fp_alloc_fail_after, __ATOMIC_ACQUIRE);
    while (cur >= 0) {
        if (cur == 0) {
            __atomic_store_n(&g_fp_alloc_fail_after, -1, __ATOMIC_RELEASE);
            return true;
        }
        if (__atomic_compare_exchange_n(&g_fp_alloc_fail_after, &cur, cur - 1, false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
            return false;
        }
    }
    return false;
}

static void *fp_malloc(size_t sz) {
    if (fp_should_fail_alloc()) return NULL;
    return malloc(sz);
}

static void *fp_realloc(void *ptr, size_t sz) {
    if (fp_should_fail_alloc()) return NULL;
    return realloc(ptr, sz);
}

static void fp_wake_all_sessions(void) {
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        pthread_cond_broadcast(&g_fp_sessions[i].cond);
    }
}

static bool fp_global_buf_try_reserve(size_t n) {
    if (n == 0) return true;
    size_t max_buf = __atomic_load_n(&g_fp_max_global_buf, __ATOMIC_ACQUIRE);
    size_t cur = __atomic_load_n(&g_fp_global_buf_bytes, __ATOMIC_ACQUIRE);
    while (1) {
        if (n > max_buf || cur > max_buf - n) {
            return false;
        }
        if (__atomic_compare_exchange_n(&g_fp_global_buf_bytes, &cur, cur + n, false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
            return true;
        }
    }
}

static size_t fp_global_buf_try_reserve_up_to(size_t want_n) {
    if (want_n == 0) return 0;
    size_t max_buf = __atomic_load_n(&g_fp_max_global_buf, __ATOMIC_ACQUIRE);
    size_t cur = __atomic_load_n(&g_fp_global_buf_bytes, __ATOMIC_ACQUIRE);
    while (1) {
        if (cur >= max_buf) {
            return 0;
        }
        size_t avail = max_buf - cur;
        size_t take = want_n < avail ? want_n : avail;
        if (__atomic_compare_exchange_n(&g_fp_global_buf_bytes, &cur, cur + take, false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
            return take;
        }
    }
}

static void fp_global_buf_release(size_t n) {
    if (n == 0) return;
    size_t cur = __atomic_load_n(&g_fp_global_buf_bytes, __ATOMIC_ACQUIRE);
    while (1) {
        size_t next = (cur >= n) ? (cur - n) : 0;
        if (__atomic_compare_exchange_n(&g_fp_global_buf_bytes, &cur, next, false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
            break;
        }
    }
    fp_wake_all_sessions();
}

static inline uint64_t get_be64(const uint8_t *p) {
    return ((uint64_t)p[0] << 56) | ((uint64_t)p[1] << 48) |
           ((uint64_t)p[2] << 40) | ((uint64_t)p[3] << 32) |
           ((uint64_t)p[4] << 24) | ((uint64_t)p[5] << 16) |
           ((uint64_t)p[6] << 8)  | ((uint64_t)p[7]);
}

static inline void put_be64(uint8_t *p, uint64_t v) {
    p[0] = (uint8_t)(v >> 56);
    p[1] = (uint8_t)(v >> 48);
    p[2] = (uint8_t)(v >> 40);
    p[3] = (uint8_t)(v >> 32);
    p[4] = (uint8_t)(v >> 24);
    p[5] = (uint8_t)(v >> 16);
    p[6] = (uint8_t)(v >> 8);
    p[7] = (uint8_t)(v);
}

static inline uint32_t get_be32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8)  | ((uint32_t)p[3]);
}

static inline void put_be32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)(v);
}

static bool sid_is_zero(const uint8_t sid[16]) {
    uint8_t z = 0;
    for (int i = 0; i < 16; i++) z |= sid[i];
    return z == 0;
}

static bool g_fp_sessions_inited = false;
static void close_fp_session_locked(fp_session_t *sess);

void fp_engine_init(void) {
    pthread_mutex_lock(&g_fp_table_mu);
    __atomic_store_n(&g_fp_max_global_buf, FP_MAX_GLOBAL_BUF, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_alloc_fail_after, -1, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_reader_exit_hook, NULL, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_pre_auth_hook, NULL, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_pre_dial_hook, NULL, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_post_dial_hook, NULL, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_write_hook, NULL, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_initial_write_timeout_ms, 5000, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_target_sndbuf, 0, __ATOMIC_RELEASE);
    __atomic_store_n(&g_fp_test_write_wouldblock_count, 0, __ATOMIC_RELEASE);
    g_num_active_fp_creds = 0;

    if (!g_fp_sessions_inited) {
        __atomic_store_n(&g_fp_global_buf_bytes, 0, __ATOMIC_RELEASE);
        __atomic_store_n(&g_fp_cred_gen, 1, __ATOMIC_RELEASE);
        for (int i = 0; i < FP_MAX_SESSIONS; i++) {
            memset(&g_fp_sessions[i], 0, sizeof(fp_session_t));
            g_fp_sessions[i].target_fd = -1;
            pthread_mutex_init(&g_fp_sessions[i].up_mu, NULL);
            pthread_mutex_init(&g_fp_sessions[i].mu, NULL);
            pthread_cond_init(&g_fp_sessions[i].cond, NULL);
        }
        g_fp_sessions_inited = true;
        pthread_mutex_unlock(&g_fp_table_mu);
        return;
    }

    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        close_fp_session_locked(s);
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);

    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        while (s->reader_running || s->ref_count > 0) {
            pthread_cond_wait(&s->cond, &s->mu);
        }
        pthread_mutex_unlock(&s->mu);
    }

    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        close_fp_session_locked(s);
        memset(s, 0, offsetof(fp_session_t, up_mu));
        memset((char *)s + offsetof(fp_session_t, cred_gen), 0,
               sizeof(fp_session_t) - offsetof(fp_session_t, cred_gen));
        s->target_fd = -1;
        pthread_mutex_unlock(&s->mu);
    }
    __atomic_store_n(&g_fp_global_buf_bytes, 0, __ATOMIC_RELEASE);
    g_num_active_fp_creds = 0;
    __atomic_store_n(&g_fp_cred_gen, 1, __ATOMIC_RELEASE);
    pthread_mutex_unlock(&g_fp_table_mu);
}

static __thread bool g_tl_fp_is_head = false;

static bool is_supported_fp_method(const char *m) {
    if (!m || !m[0]) return false;
    return strcasecmp(m, "POST") == 0 ||
           strcasecmp(m, "GET") == 0 ||
           strcasecmp(m, "HEAD") == 0 ||
           strcasecmp(m, "OPTIONS") == 0 ||
           strcasecmp(m, "DELETE") == 0 ||
           strcasecmp(m, "PUT") == 0 ||
           strcasecmp(m, "PATCH") == 0;
}

bool is_fp_http_request(const http_req_t *req) {
    if (!req || !is_supported_fp_method(req->method)) return false;
    return req->fp_version[0] != '\0' || req->fp_auth[0] != '\0';
}

static int decode_fp_req(const uint8_t *raw, size_t len, fp_req_env_t *out) {
    if (len < FP_REQ_HEADER_SIZE) return -1;
    if (memcmp(raw, FP_PROTO_MAGIC, 4) != 0) return -2;
    uint32_t plen = get_be32(raw + 50);
    if (plen > FP_MAX_REQ_PAYLOAD) return -3;
    if (len != FP_REQ_HEADER_SIZE + (size_t)plen) return -1;

    out->op               = raw[4];
    out->flags            = raw[5];
    memcpy(out->sid, raw + 6, 16);
    out->up_offset        = get_be64(raw + 22);
    out->ack_down_offset  = get_be64(raw + 30);
    out->want_down_offset = get_be64(raw + 38);
    out->max_down_bytes   = get_be32(raw + 46);
    out->payload          = plen > 0 ? (raw + FP_REQ_HEADER_SIZE) : NULL;
    out->payload_len      = plen;
    return 0;
}

static int send_fp_response(int fd, int http_status, const char *status_text, const fp_resp_env_t *env, bool keep_alive) {
    bool is_head = g_tl_fp_is_head;
    size_t body_len = is_head ? 0 : (FP_RESP_HEADER_SIZE + (size_t)env->payload_len);
    uint8_t hdr_frame[FP_RESP_HEADER_SIZE];
    memcpy(hdr_frame, FP_PROTO_MAGIC, 4);
    hdr_frame[4] = env->status;
    hdr_frame[5] = env->flags;
    memcpy(hdr_frame + 6, env->sid, 16);
    put_be64(hdr_frame + 22, env->acked_up_offset);
    put_be64(hdr_frame + 30, env->down_offset_start);
    put_be64(hdr_frame + 38, env->down_offset_end);
    put_be64(hdr_frame + 46, env->target_eof_offset);
    put_be32(hdr_frame + 54, env->payload_len);

    char resp_b64[96];
    base64url_encode(hdr_frame, FP_RESP_HEADER_SIZE, resp_b64, sizeof(resp_b64));

    char data_hdr[3200];
    data_hdr[0] = '\0';
    if (is_head && env->payload_len > 0 && env->payload && env->payload_len <= 2048) {
        char payload_b64[2800];
        if (base64url_encode(env->payload, env->payload_len, payload_b64, sizeof(payload_b64)) > 0) {
            snprintf(data_hdr, sizeof(data_hdr), "X-FP-Data: %s\r\n", payload_b64);
        }
    }

    char http_hdr[3840];
    int hlen = snprintf(http_hdr, sizeof(http_hdr),
        "HTTP/1.1 %d %s\r\n"
        "Content-Type: application/octet-stream\r\n"
        "Cache-Control: no-store, no-cache, must-revalidate, max-age=0\r\n"
        "Pragma: no-cache\r\n"
        "Expires: 0\r\n"
        "X-Accel-Buffering: no\r\n"
        "X-FP-Version: fp/1\r\n"
        "X-FP-Resp: %s\r\n"
        "%s"
        "Content-Length: %zu\r\n"
        "Connection: %s\r\n"
        "\r\n",
        http_status, status_text, resp_b64, data_hdr, body_len, keep_alive ? "keep-alive" : "close");
    if (hlen < 0 || (size_t)hlen >= sizeof(http_hdr)) return -1;

    if (write_all(fd, http_hdr, (size_t)hlen) < 0) return -1;
    if (!is_head) {
        if (write_all(fd, hdr_frame, FP_RESP_HEADER_SIZE) < 0) return -1;
        if (env->payload_len > 0 && env->payload) {
            if (write_all(fd, env->payload, env->payload_len) < 0) return -1;
        }
    }
    return keep_alive ? 0 : -1;
}

static void compute_fp_hmac(const uint8_t *secret, size_t secret_len,
                            const char *method, const char *path,
                            const char *client_id, const char *ts_str,
                            const char *nonce_hex, const uint8_t body_sha256[32],
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
    hmac_sha256(secret, secret_len, msg, off, out_sig);
}

static int verify_fp_auth(const http_req_t *req, const xhttpd_config_t *cfg,
                          char *out_client_id, size_t cid_sz, uint8_t out_secret[32]) {
    if (req->query[0]) {
        if (strcasestr(req->query, "token") || strcasestr(req->query, "secret") ||
            strcasestr(req->query, "key")   || strcasestr(req->query, "auth") ||
            strcasestr(req->query, "password")) {
            return -2; /* Forbidden secret in URL -> 400 */
        }
    }
    if (strcmp(req->fp_version, FP_PROTO_VERSION) != 0) return -2;
    if (!req->fp_auth[0]) return -1;

    char buf[256];
    snprintf(buf, sizeof(buf), "%s", req->fp_auth);
    char *p1 = strchr(buf, ':');
    if (!p1) return -1;
    *p1++ = '\0';
    char *p2 = strchr(p1, ':');
    if (!p2) return -1;
    *p2++ = '\0';
    char *p3 = strchr(p2, ':');
    if (!p3) return -1;
    *p3++ = '\0';
    if (strchr(p3, ':')) return -1;

    const char *client_id = buf;
    const char *ts_str    = p1;
    const char *nonce_hex = p2;
    const char *sig_hex   = p3;

    if (!client_id[0] || strlen(nonce_hex) < 16) return -1;
    char *endptr = NULL;
    int64_t ts_ms = strtoll(ts_str, &endptr, 10);
    if (!endptr || *endptr != '\0') return -1;

    int64_t cur_ms = (int64_t)epoch_ms();
    int64_t diff = cur_ms > ts_ms ? (cur_ms - ts_ms) : (ts_ms - cur_ms);
    if (diff > 300000LL) return -1; /* 5 min max skew */

    uint8_t got_sig[32];
    if (hex_to_bytes(sig_hex, got_sig, 32) != 32) return -1;

    uint8_t body_sum[32];
    sha256_hash(req->body, req->body_len, body_sum);

    /* Also prepare path without trailing slash in case cdndispatcher appended '/' */
    char alt_path[512];
    snprintf(alt_path, sizeof(alt_path), "%s", req->path);
    size_t plen = strlen(alt_path);
    if (plen > 1 && alt_path[plen - 1] == '/') {
        alt_path[plen - 1] = '\0';
    }

    for (int i = 0; i < cfg->num_fp_creds; i++) {
        if (cfg->fp_creds[i].secret_len != 32) continue;
        if (strcasecmp(cfg->fp_creds[i].client_id, client_id) != 0) continue;
        uint8_t want_sig[32];
        compute_fp_hmac(cfg->fp_creds[i].secret, 32,
                        req->method, req->path, client_id, ts_str, nonce_hex, body_sum, want_sig);
        if (ct_memeq(got_sig, want_sig, 32)) {
            snprintf(out_client_id, cid_sz, "%s", cfg->fp_creds[i].client_id);
            memcpy(out_secret, cfg->fp_creds[i].secret, 32);
            return 0;
        }
        if (strcmp(alt_path, req->path) != 0) {
            compute_fp_hmac(cfg->fp_creds[i].secret, 32,
                            req->method, alt_path, client_id, ts_str, nonce_hex, body_sum, want_sig);
            if (ct_memeq(got_sig, want_sig, 32)) {
                snprintf(out_client_id, cid_sz, "%s", cfg->fp_creds[i].client_id);
                memcpy(out_secret, cfg->fp_creds[i].secret, 32);
                return 0;
            }
        }
    }
    return -1;
}

static int fp_session_append_down_locked(fp_session_t *sess, const uint8_t *data, size_t len) {
    if (len == 0) return 0;
    if (len > FP_MAX_SESS_BUF || sess->unacked_len + sess->pending_up_bytes > FP_MAX_SESS_BUF - len) {
        return -1;
    }
    if (sess->unacked_len + len > sess->unacked_cap) {
        size_t new_cap = sess->unacked_cap ? sess->unacked_cap * 2 : 16384;
        while (new_cap < sess->unacked_len + len) {
            if (new_cap >= FP_MAX_SESS_BUF) {
                new_cap = FP_MAX_SESS_BUF;
                break;
            }
            new_cap *= 2;
        }
        if (new_cap > FP_MAX_SESS_BUF) new_cap = FP_MAX_SESS_BUF;
        if (new_cap < sess->unacked_len + len) return -1;

        size_t delta_cap = new_cap - sess->unacked_cap;
        if (!fp_global_buf_try_reserve(delta_cap)) {
            return -1;
        }
        uint8_t *nb = (uint8_t *)fp_realloc(sess->unacked_down, new_cap);
        if (!nb) {
            fp_global_buf_release(delta_cap);
            return -1;
        }
        sess->unacked_down = nb;
        sess->unacked_cap = new_cap;
    }
    memcpy(sess->unacked_down + sess->unacked_len, data, len);
    sess->unacked_len += len;
    sess->generated_down += len;
    return 0;
}

/*
 * Reserve downstream buffer capacity BEFORE calling recv().
 * Returns:
 *   0  on success (*out_free > 0 if space is available in sess->unacked_down,
 *      or *out_free == 0 if session/global budget is currently full -> caller waits on cond)
 *  -1  on hard memory allocation failure (OOM).
 */
static int fp_session_reserve_down_cap_locked(fp_session_t *sess, size_t want_free, size_t *out_free) {
    *out_free = 0;
    size_t used_sess = sess->unacked_len + sess->pending_up_bytes;
    if (used_sess >= FP_MAX_SESS_BUF) {
        return 0;
    }
    size_t avail_sess = FP_MAX_SESS_BUF - used_sess;
    if (want_free > avail_sess) {
        want_free = avail_sess;
    }
    size_t cur_free = (sess->unacked_cap > sess->unacked_len) ? (sess->unacked_cap - sess->unacked_len) : 0;
    if (cur_free > avail_sess) {
        cur_free = avail_sess;
    }
    if (cur_free < want_free) {
        size_t need_delta = want_free - cur_free;
        size_t reserved = fp_global_buf_try_reserve_up_to(need_delta);
        if (reserved > 0) {
            size_t new_cap = sess->unacked_cap + reserved;
            uint8_t *nb = (uint8_t *)fp_realloc(sess->unacked_down, new_cap);
            if (!nb) {
                fp_global_buf_release(reserved);
                return -1;
            }
            sess->unacked_down = nb;
            sess->unacked_cap = new_cap;
            cur_free += reserved;
        }
    }
    *out_free = cur_free;
    return 0;
}

static void fp_session_trim_unused_down_cap_locked(fp_session_t *sess) {
    if (sess->unacked_cap <= sess->unacked_len) return;
    if (sess->unacked_len == 0) {
        size_t rel = sess->unacked_cap;
        free(sess->unacked_down);
        sess->unacked_down = NULL;
        sess->unacked_cap = 0;
        fp_global_buf_release(rel);
    } else {
        size_t new_cap = sess->unacked_len;
        uint8_t *nb = (uint8_t *)realloc(sess->unacked_down, new_cap);
        if (nb) {
            size_t rel = sess->unacked_cap - new_cap;
            sess->unacked_down = nb;
            sess->unacked_cap = new_cap;
            fp_global_buf_release(rel);
        }
    }
}

static void fp_reader_finish_locked(fp_session_t *sess) {
    fp_session_trim_unused_down_cap_locked(sess);
    sess->reader_running = false;
    if (sess->closed && sess->ref_count == 0 && sess->target_fd >= 0) {
        close(sess->target_fd);
        sess->target_fd = -1;
    }
    pthread_cond_broadcast(&sess->cond);
}

static void *fp_target_reader_thread(void *arg) {
    fp_session_t *sess = (fp_session_t *)arg;

    while (1) {
        pthread_mutex_lock(&sess->mu);
        while (!sess->closed && sess->target_fd >= 0) {
            size_t used_sess = sess->unacked_len + sess->pending_up_bytes;
            size_t free_in_cap = (sess->unacked_cap > sess->unacked_len) ? (sess->unacked_cap - sess->unacked_len) : 0;
            size_t max_glob = __atomic_load_n(&g_fp_max_global_buf, __ATOMIC_ACQUIRE);
            bool sess_full = (used_sess >= FP_MAX_SESS_BUF);
            /* Allow reading into already-reserved free capacity even when global budget is full */
            bool glob_full_and_no_local_cap = (free_in_cap == 0 && fp_engine_get_global_buf_bytes() >= max_glob);
            if (!sess_full && !glob_full_and_no_local_cap) {
                break;
            }
            struct timespec ts;
            clock_gettime(CLOCK_REALTIME, &ts);
            ts.tv_nsec += 20 * 1000000L;
            if (ts.tv_nsec >= 1000000000L) {
                ts.tv_sec += 1;
                ts.tv_nsec -= 1000000000L;
            }
            pthread_cond_timedwait(&sess->cond, &sess->mu, &ts);
        }
        if (sess->closed || sess->target_fd < 0) {
            pthread_mutex_unlock(&sess->mu);
            break;
        }
        int fd = sess->target_fd;
        pthread_mutex_unlock(&sess->mu);

        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int pr = poll(&pfd, 1, 25);
        if (pr < 0) {
            if (errno == EINTR) continue;
            pthread_mutex_lock(&sess->mu);
            sess->target_reset = true;
            pthread_mutex_unlock(&sess->mu);
            break;
        }
        if (pr == 0) {
            continue;
        }

        pthread_mutex_lock(&sess->mu);
        if (sess->closed || sess->target_fd < 0) {
            pthread_mutex_unlock(&sess->mu);
            break;
        }

        size_t free_cap = 0;
        int res_rc = fp_session_reserve_down_cap_locked(sess, 16384, &free_cap);
        if (res_rc < 0) {
            sess->target_reset = true;
            if (sess->target_fd >= 0) {
                shutdown(sess->target_fd, SHUT_RDWR);
            }
            pthread_mutex_unlock(&sess->mu);
            break;
        }
        if (free_cap == 0) {
            struct timespec ts;
            clock_gettime(CLOCK_REALTIME, &ts);
            ts.tv_nsec += 20 * 1000000L;
            if (ts.tv_nsec >= 1000000000L) {
                ts.tv_sec += 1;
                ts.tv_nsec -= 1000000000L;
            }
            pthread_cond_timedwait(&sess->cond, &sess->mu, &ts);
            pthread_mutex_unlock(&sess->mu);
            continue;
        }

        ssize_t n = recv(sess->target_fd, sess->unacked_down + sess->unacked_len, free_cap, MSG_DONTWAIT);
        if (n > 0) {
            sess->unacked_len += (size_t)n;
            sess->generated_down += (size_t)n;
            sess->last_active_ms = now_ms();
            if ((size_t)n < free_cap) {
                fp_session_trim_unused_down_cap_locked(sess);
            }
            pthread_cond_broadcast(&sess->cond);
            pthread_mutex_unlock(&sess->mu);
            continue;
        }
        if (n < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK)) {
            pthread_mutex_unlock(&sess->mu);
            continue;
        }
        if (n == 0) {
            sess->target_eof = true;
        } else {
            sess->target_reset = true;
        }
        pthread_mutex_unlock(&sess->mu);
        break;
    }

    void (*exit_hook)(void) = __atomic_load_n(&g_fp_reader_exit_hook, __ATOMIC_ACQUIRE);
    if (exit_hook) {
        exit_hook();
    }

    pthread_mutex_lock(&sess->mu);
    fp_reader_finish_locked(sess);
    pthread_mutex_unlock(&sess->mu);
    return NULL;
}

static int spawn_fp_reader_locked(fp_session_t *sess) {
    if (sess->reader_running) return 0;
    if (sess->target_fd < 0) return -1;
    if (fp_should_fail_alloc()) return -1;
    sess->reader_running = true;
    pthread_t tid;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
    pthread_attr_setstacksize(&attr, 262144);
    int rc = pthread_create(&tid, &attr, fp_target_reader_thread, sess);
    pthread_attr_destroy(&attr);
    if (rc != 0) {
        sess->reader_running = false;
        return -1;
    }
    return 0;
}

static void close_fp_session_locked(fp_session_t *sess) {
    sess->closed = true;
    sess->active = false;
    sess->dialing = false;
    if (sess->target_fd >= 0) {
        shutdown(sess->target_fd, SHUT_RDWR);
        if (sess->ref_count == 0 && !sess->reader_running) {
            close(sess->target_fd);
            sess->target_fd = -1;
        }
    }
    for (int i = 0; i < FP_MAX_PENDING_UP; i++) {
        if (sess->pending_up[i].used && sess->pending_up[i].data) {
            fp_global_buf_release(sess->pending_up[i].len);
            free(sess->pending_up[i].data);
        }
        sess->pending_up[i].used = false;
        sess->pending_up[i].data = NULL;
        sess->pending_up[i].len = 0;
    }
    sess->pending_up_bytes = 0;
    if (sess->unacked_down) {
        fp_global_buf_release(sess->unacked_cap);
        free(sess->unacked_down);
        sess->unacked_down = NULL;
    }
    sess->unacked_len = 0;
    sess->unacked_cap = 0;
    pthread_cond_broadcast(&sess->cond);
}

static void release_fp_session(fp_session_t *sess) {
    if (!sess) return;
    pthread_mutex_lock(&g_fp_table_mu);
    pthread_mutex_lock(&sess->mu);
    if (sess->ref_count > 0) {
        sess->ref_count--;
    }
    if (sess->closed && sess->ref_count == 0 && !sess->reader_running && sess->target_fd >= 0) {
        close(sess->target_fd);
        sess->target_fd = -1;
    }
    pthread_cond_broadcast(&sess->cond);
    pthread_mutex_unlock(&sess->mu);
    pthread_mutex_unlock(&g_fp_table_mu);
}

static bool session_matches_locked(const fp_session_t *sess, uint64_t expected_gen, const uint8_t sid[16],
                                   const char *client_id, const uint8_t auth_secret[32]) {
    if (!sess || !sess->active || sess->closed) return false;
    if (sess->generation != expected_gen) return false;
    if (memcmp(sess->id, sid, 16) != 0) return false;
    if (strcasecmp(sess->owner_id, client_id) != 0) return false;
    if (!ct_memeq(sess->owner_secret, auth_secret, 32)) return false;
    return true;
}

static bool apply_ack_down_locked(fp_session_t *sess, uint64_t ack_off) {
    if (ack_off > sess->max_sent_down) return false;
    if (ack_off > sess->base_acked_down) {
        size_t delta = (size_t)(ack_off - sess->base_acked_down);
        if (delta >= sess->unacked_len) {
            sess->unacked_len = 0;
        } else {
            size_t rem = sess->unacked_len - delta;
            memmove(sess->unacked_down, sess->unacked_down + delta, rem);
            sess->unacked_len = rem;
        }
        sess->base_acked_down = ack_off;
    }
    if (sess->unacked_len == 0 && sess->unacked_cap > 0) {
        size_t rel = sess->unacked_cap;
        free(sess->unacked_down);
        sess->unacked_down = NULL;
        sess->unacked_cap = 0;
        fp_global_buf_release(rel);
    } else if (sess->unacked_cap > sess->unacked_len &&
               (sess->unacked_cap - sess->unacked_len) >= 16384) {
        size_t new_cap = sess->unacked_len;
        uint8_t *nb = (uint8_t *)realloc(sess->unacked_down, new_cap);
        if (nb) {
            size_t rel = sess->unacked_cap - new_cap;
            sess->unacked_down = nb;
            sess->unacked_cap = new_cap;
            fp_global_buf_release(rel);
        }
    }
    pthread_cond_broadcast(&sess->cond);
    return true;
}

static int build_down_slice_locked(fp_session_t *sess, uint64_t want_off, uint32_t max_bytes,
                                   fp_resp_env_t *out, uint8_t **allocated_payload) {
    *allocated_payload = NULL;
    memset(out, 0, sizeof(*out));
    out->status = FP_STATUS_OK;
    memcpy(out->sid, sess->id, 16);
    out->acked_up_offset = sess->next_up_offset;

    uint64_t start_off = want_off;
    if (start_off < sess->base_acked_down) start_off = sess->base_acked_down;
    if (start_off > sess->generated_down)  start_off = sess->generated_down;
    out->down_offset_start = start_off;
    out->down_offset_end   = start_off;

    if (max_bytes > FP_MAX_RESP_PAYLOAD) max_bytes = FP_MAX_RESP_PAYLOAD;
    uint64_t avail = sess->generated_down - start_off;
    if (max_bytes > 0 && avail > 0) {
        uint64_t take = avail < max_bytes ? avail : max_bytes;
        size_t rel_start = (size_t)(start_off - sess->base_acked_down);
        uint8_t *cp = (uint8_t *)fp_malloc((size_t)take);
        if (!cp) {
            return -1;
        }
        memcpy(cp, sess->unacked_down + rel_start, (size_t)take);
        *allocated_payload = cp;
        out->payload = cp;
        out->payload_len = (uint32_t)take;
        out->down_offset_end = start_off + take;
        if (out->down_offset_end > sess->max_sent_down) {
            sess->max_sent_down = out->down_offset_end;
        }
    }
    if (sess->target_eof) {
        out->flags |= FP_RESP_FLAG_TARGET_EOF;
        out->target_eof_offset = sess->generated_down;
    }
    if (sess->target_reset) {
        out->flags |= FP_RESP_FLAG_TARGET_RESET;
    }
    if (sess->closed) {
        out->flags |= FP_RESP_FLAG_SESSION_CLOSED;
    }
    return 0;
}

static int parse_host_port_str(const uint8_t *payload, size_t len, char *out_host, size_t host_sz, uint16_t *out_port) {
    char tmp[320];
    if (len == 0 || len >= sizeof(tmp)) return -1;
    memcpy(tmp, payload, len);
    tmp[len] = '\0';

    char *s = tmp;
    while (*s == ' ' || *s == '\t' || *s == '\r' || *s == '\n') s++;
    size_t slen = strlen(s);
    while (slen > 0 && (s[slen - 1] == ' ' || s[slen - 1] == '\t' || s[slen - 1] == '\r' || s[slen - 1] == '\n')) {
        s[--slen] = '\0';
    }
    char *colon = strrchr(s, ':');
    if (!colon || colon == s) return -1;
    *colon++ = '\0';
    int p = atoi(colon);
    if (p < 1 || p > 65535) return -1;
    *out_port = (uint16_t)p;

    if (s[0] == '[' && colon[-2] == ']') {
        s++;
        colon[-2] = '\0';
    }
    size_t host_len = strlen(s);
    if (!host_len || host_len >= host_sz) return -1;
    memcpy(out_host, s, host_len + 1);
    return 0;
}

static int set_socket_nonblocking(int fd, bool nonblock) {
#ifdef _WIN32
    u_long mode = nonblock ? 1 : 0;
    return ioctlsocket((SOCKET)fd, FIONBIO, &mode) == 0 ? 0 : -1;
#else
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags < 0) return -1;
    if (nonblock) {
        flags |= O_NONBLOCK;
    } else {
        flags &= ~O_NONBLOCK;
    }
    return fcntl(fd, F_SETFL, flags) == 0 ? 0 : -1;
#endif
}

static bool sock_err_is_retryable(bool *out_is_eintr) {
#ifdef _WIN32
    int werr = WSAGetLastError();
    int cerr = errno;
    if (out_is_eintr) *out_is_eintr = (werr == WSAEINTR || cerr == EINTR);
    return (werr == WSAEWOULDBLOCK || werr == WSAEINTR ||
            cerr == EAGAIN || cerr == EWOULDBLOCK || cerr == EINTR);
#else
    int cerr = errno;
    if (out_is_eintr) *out_is_eintr = (cerr == EINTR);
    return (cerr == EAGAIN || cerr == EWOULDBLOCK || cerr == EINTR);
#endif
}

static int write_cancellable(fp_session_t *sess, uint64_t open_gen, int fd, const void *buf, size_t len, int timeout_ms) {
    const uint8_t *p = (const uint8_t *)buf;
    size_t rem = len;
    uint64_t deadline = now_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 5000);

    int sndbuf = __atomic_load_n(&g_fp_test_target_sndbuf, __ATOMIC_ACQUIRE);
    if (sndbuf > 0) {
        (void)setsockopt(fd, SOL_SOCKET, SO_SNDBUF, (const char *)&sndbuf, sizeof(sndbuf));
    }

    if (set_socket_nonblocking(fd, true) < 0) {
        return -1;
    }

    if (sndbuf > 0 && rem > 4096) {
        uint8_t pad[4096];
        memset(pad, 0x5A, sizeof(pad));
        for (int i = 0; i < 1024; i++) {
            ssize_t pn = send(fd, (const char *)pad, (int)sizeof(pad), MSG_NOSIGNAL);
            if (pn > 0) continue;
            struct pollfd spfd = { .fd = fd, .events = POLLOUT };
            if (poll(&spfd, 1, 10) == 0) break;
        }
    }

    while (rem > 0) {
        pthread_mutex_lock(&sess->mu);
        bool cancelled = sess->closed || sess->generation != open_gen ||
                         sess->cred_gen != __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE);
        pthread_mutex_unlock(&sess->mu);
        if (cancelled) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }

        void (*whook)(void) = __atomic_load_n(&g_fp_test_write_hook, __ATOMIC_ACQUIRE);
        if (whook) whook();

        pthread_mutex_lock(&sess->mu);
        cancelled = sess->closed || sess->generation != open_gen ||
                    sess->cred_gen != __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE);
        pthread_mutex_unlock(&sess->mu);
        if (cancelled) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }

        uint64_t now = now_ms();
        if (now >= deadline) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }

        char peek_byte;
        ssize_t prc = recv(fd, &peek_byte, 1, MSG_PEEK | MSG_DONTWAIT);
        if (prc == 0 || (prc < 0 && !sock_err_is_retryable(NULL))) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }

        size_t chunk = rem > 4096 ? 4096 : rem;
        ssize_t n = send(fd, (const char *)p, (int)chunk, MSG_NOSIGNAL);
        if (n > 0) {
            p += n;
            rem -= (size_t)n;
            continue;
        }
        if (n == 0) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }

        bool is_eintr = false;
        if (!sock_err_is_retryable(&is_eintr)) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }
        if (is_eintr) {
            continue;
        }

        __atomic_add_fetch(&g_fp_test_write_wouldblock_count, 1, __ATOMIC_RELAXED);

        now = now_ms();
        if (now >= deadline) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }
        uint64_t rem_ms = deadline - now;
        int wait_ms = (rem_ms < 50) ? (int)rem_ms : 50;

        struct pollfd pfd = { .fd = fd, .events = POLLOUT };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            bool poll_eintr = false;
            if (sock_err_is_retryable(&poll_eintr) && poll_eintr) {
                continue;
            }
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }
        if (pr > 0 && (pfd.revents & (POLLERR | POLLHUP | POLLNVAL))) {
            (void)set_socket_nonblocking(fd, false);
            return -1;
        }
    }

    if (set_socket_nonblocking(fd, false) < 0) {
        return -1;
    }
    return 0;
}

static int handle_fp_open(int client_fd, const http_req_t *http_req, const xhttpd_config_t *cfg,
                          const char *client_id, const uint8_t auth_secret[32], const fp_req_env_t *req) {
    fp_resp_env_t resp;
    memset(&resp, 0, sizeof(resp));
    memcpy(resp.sid, req->sid, 16);

    if (sid_is_zero(req->sid) || req->payload_len == 0) {
        resp.status = FP_STATUS_BAD_REQUEST;
        return send_fp_response(client_fd, 400, "Bad Request", &resp, http_req->keep_alive);
    }

    char host[256] = {0};
    uint16_t port = 0;
    bool vless_framed = false;
    const uint8_t *initial_up = NULL;
    size_t initial_up_len = 0;

    /* Support both VLESS header inside FPOpOpen and plain "host:port" string */
    vless_req_t vreq;
    if (req->payload[0] == 0x00 && req->payload_len >= 22 &&
        parse_vless_header(req->payload, req->payload_len, cfg, &vreq) == 0) {
        if (vreq.command != VLESS_CMD_TCP || !vreq.host[0] || vreq.port == 0) {
            resp.status = FP_STATUS_BAD_REQUEST;
            return send_fp_response(client_fd, 400, "Bad Request", &resp, http_req->keep_alive);
        }
        snprintf(host, sizeof(host), "%s", vreq.host);
        port = vreq.port;
        vless_framed = true;
        if (req->payload_len > vreq.header_len) {
            initial_up = req->payload + vreq.header_len;
            initial_up_len = req->payload_len - vreq.header_len;
        }
    } else if (parse_host_port_str(req->payload, req->payload_len, host, sizeof(host), &port) != 0) {
        resp.status = FP_STATUS_BAD_REQUEST;
        return send_fp_response(client_fd, 400, "Bad Request", &resp, http_req->keep_alive);
    }

    if (!(cfg->egress_mode == EGRESS_MODE_ROUTER && port == 53 && cfg->dns_proxy_host[0])) {
        if (!check_destination_acl_ex(host, cfg->allow_private, cfg->egress_mode == EGRESS_MODE_ROUTER)) {
            resp.status = FP_STATUS_FORBIDDEN;
            return send_fp_response(client_fd, 403, "Forbidden", &resp, http_req->keep_alive);
        }
    }

    void (*pre_auth_hook)(void) = __atomic_load_n(&g_fp_test_pre_auth_hook, __ATOMIC_ACQUIRE);
    if (pre_auth_hook) pre_auth_hook();

    /* Idempotency check: if open_id already exists (or is dialing) for this client_id, return existing sid */
    pthread_mutex_lock(&g_fp_table_mu);

    /* Atomic credential validation: verify against currently active credentials under g_fp_table_mu */
    bool auth_valid = false;
    for (int j = 0; j < g_num_active_fp_creds; j++) {
        if (strcasecmp(g_active_fp_creds[j].client_id, client_id) == 0 &&
            ct_memeq(g_active_fp_creds[j].secret, auth_secret, 32)) {
            auth_valid = true;
            break;
        }
    }
    if (!auth_valid) {
        pthread_mutex_unlock(&g_fp_table_mu);
        resp.status = FP_STATUS_UNAUTHORIZED;
        return send_fp_response(client_fd, 401, "Unauthorized", &resp, http_req->keep_alive);
    }

    int free_slot = -1;
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if ((s->active || s->dialing) && !s->closed) {
            if (strcasecmp(s->owner_id, client_id) == 0 &&
                ct_memeq(s->owner_secret, auth_secret, 32) &&
                memcmp(s->open_id, req->sid, 16) == 0) {
                s->ref_count++;
                uint64_t expected_gen = s->generation;
                pthread_mutex_unlock(&g_fp_table_mu);

                while (s->dialing && !s->closed && s->generation == expected_gen) {
                    struct timespec ts;
                    clock_gettime(CLOCK_REALTIME, &ts);
                    ts.tv_sec += 1;
                    pthread_cond_timedwait(&s->cond, &s->mu, &ts);
                }
                if (s->active && !s->closed && s->generation == expected_gen) {
                    s->last_active_ms = now_ms();
                    memcpy(resp.sid, s->id, 16);
                    resp.status = FP_STATUS_OK;
                    pthread_mutex_unlock(&s->mu);
                    release_fp_session(s);
                    return send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
                }
                pthread_mutex_unlock(&s->mu);
                release_fp_session(s);
                resp.status = FP_STATUS_DIAL_ERROR;
                return send_fp_response(client_fd, 502, "Bad Gateway", &resp, http_req->keep_alive);
            }
        } else if (free_slot < 0 && !s->active && !s->dialing && !s->reader_running && s->ref_count == 0) {
            free_slot = i;
        }
        pthread_mutex_unlock(&s->mu);
    }
    if (free_slot < 0) {
        pthread_mutex_unlock(&g_fp_table_mu);
        resp.status = FP_STATUS_RESOURCE_LIMIT;
        return send_fp_response(client_fd, 503, "Service Unavailable", &resp, http_req->keep_alive);
    }

    bool defer_dial = (initial_up_len == 0) && should_defer_dial_for_sniff(host, port, cfg->egress_mode);

    fp_session_t *sess = &g_fp_sessions[free_slot];
    pthread_mutex_lock(&sess->mu);
    close_fp_session_locked(sess);
    if (csprng_bytes(sess->id, 16) != 0 || sid_is_zero(sess->id)) {
        memset(sess->id, 0, sizeof(sess->id));
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_fp_table_mu);
        resp.status = FP_STATUS_RESOURCE_LIMIT;
        return send_fp_response(client_fd, 503, "Service Unavailable", &resp, http_req->keep_alive);
    }
    sess->generation++;
    if (sess->generation == 0) sess->generation = 1;
    uint64_t open_gen = sess->generation;
    sess->cred_gen = __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE);
    sess->ref_count = 1;
    sess->closed = false;
    sess->dialing = !defer_dial;
    sess->active = defer_dial;
    memcpy(sess->open_id, req->sid, 16);
    snprintf(sess->owner_id, sizeof(sess->owner_id), "%s", client_id);
    memcpy(sess->owner_secret, auth_secret, 32);
    snprintf(sess->orig_host, sizeof(sess->orig_host), "%s", host);
    sess->port = port;
    sess->vless_framed = vless_framed;
    fp_snapshot_egress(&sess->egress_cfg, cfg);
    sess->last_active_ms = now_ms();
    sess->next_up_offset = 0;
    sess->client_eof_rcvd = false;
    sess->client_eof_offset = 0;
    sess->client_half_closed = false;
    sess->base_acked_down = 0;
    sess->generated_down = 0;
    sess->max_sent_down = 0;
    sess->target_eof = false;
    sess->target_reset = false;
    if (vless_framed) {
        /* Prepend 2-byte VLESS response header (0x00 0x00) to downlink stream */
        uint8_t vresp[2] = { 0x00, 0x00 };
        if (fp_session_append_down_locked(sess, vresp, 2) < 0) {
            close_fp_session_locked(sess);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            release_fp_session(sess);
            resp.status = FP_STATUS_RESOURCE_LIMIT;
            return send_fp_response(client_fd, 503, "Service Unavailable", &resp, http_req->keep_alive);
        }
    }
    sess->deferred_dial = defer_dial;
    uint8_t assigned_sid[16];
    memcpy(assigned_sid, sess->id, 16);
    pthread_mutex_unlock(&sess->mu);
    pthread_mutex_unlock(&g_fp_table_mu);

    void (*pre_hook)(void) = __atomic_load_n(&g_fp_test_pre_dial_hook, __ATOMIC_ACQUIRE);
    if (pre_hook) pre_hook();

    /* Pre-dial verification: ensure credential was not revoked while waiting on pre-dial barrier */
    pthread_mutex_lock(&g_fp_table_mu);
    bool pre_auth = (sess->cred_gen == __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE) && !sess->closed);
    if (pre_auth) {
        pre_auth = false;
        for (int j = 0; j < g_num_active_fp_creds; j++) {
            if (strcasecmp(g_active_fp_creds[j].client_id, sess->owner_id) == 0 &&
                ct_memeq(g_active_fp_creds[j].secret, sess->owner_secret, 32)) {
                pre_auth = true;
                break;
            }
        }
    }
    if (!pre_auth) {
        pthread_mutex_lock(&sess->mu);
        if (sess->generation == open_gen) {
            close_fp_session_locked(sess);
        }
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_fp_table_mu);
        release_fp_session(sess);
        resp.status = FP_STATUS_UNAUTHORIZED;
        return send_fp_response(client_fd, 401, "Unauthorized", &resp, http_req->keep_alive);
    }
    pthread_mutex_unlock(&g_fp_table_mu);

    if (!defer_dial) {
        int tfd = dial_target_tcp(host, port, initial_up, initial_up_len, cfg, 8000);
        if (tfd < 0) {
            pthread_mutex_lock(&g_fp_table_mu);
            pthread_mutex_lock(&sess->mu);
            if (sess->generation == open_gen) {
                close_fp_session_locked(sess);
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            release_fp_session(sess);
            resp.status = FP_STATUS_DIAL_ERROR;
            return send_fp_response(client_fd, 502, "Bad Gateway", &resp, http_req->keep_alive);
        }

        /* Post-dial check BEFORE transmitting any data: verify credentials remain valid */
        pthread_mutex_lock(&g_fp_table_mu);
        pthread_mutex_lock(&sess->mu);

        bool post_dial_auth = (sess->generation == open_gen &&
                               !sess->closed &&
                               sess->cred_gen == __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE));
        if (post_dial_auth) {
            post_dial_auth = false;
            for (int j = 0; j < g_num_active_fp_creds; j++) {
                if (strcasecmp(g_active_fp_creds[j].client_id, sess->owner_id) == 0 &&
                    ct_memeq(g_active_fp_creds[j].secret, sess->owner_secret, 32)) {
                    post_dial_auth = true;
                    break;
                }
            }
        }

        if (!post_dial_auth) {
            close(tfd);
            if (sess->generation == open_gen) {
                close_fp_session_locked(sess);
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            release_fp_session(sess);
            resp.status = FP_STATUS_UNAUTHORIZED;
            return send_fp_response(client_fd, 401, "Unauthorized", &resp, http_req->keep_alive);
        }

        /* Register pending socket in session for cancellation/shutdown if revoked during write */
        sess->target_fd = tfd;
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_fp_table_mu);

        /* Cancellable bounded initial write outside mutexes */
        int write_rc = 0;
        if (initial_up_len > 0) {
            int write_timeout_ms = __atomic_load_n(&g_fp_initial_write_timeout_ms, __ATOMIC_ACQUIRE);
            write_rc = write_cancellable(sess, open_gen, tfd, initial_up, initial_up_len, write_timeout_ms);
        }

        void (*post_hook)(void) = __atomic_load_n(&g_fp_test_post_dial_hook, __ATOMIC_ACQUIRE);
        if (post_hook) post_hook();

        /* Final atomic publication check before publishing session to active table */
        pthread_mutex_lock(&g_fp_table_mu);
        pthread_mutex_lock(&sess->mu);

        bool cred_valid = (!sess->closed &&
                           sess->generation == open_gen &&
                           sess->cred_gen == __atomic_load_n(&g_fp_cred_gen, __ATOMIC_ACQUIRE));
        if (cred_valid) {
            cred_valid = false;
            for (int j = 0; j < g_num_active_fp_creds; j++) {
                if (strcasecmp(g_active_fp_creds[j].client_id, sess->owner_id) == 0 &&
                    ct_memeq(g_active_fp_creds[j].secret, sess->owner_secret, 32)) {
                    cred_valid = true;
                    break;
                }
            }
        }

        bool publish_ok = (cred_valid && write_rc == 0);
        if (!publish_ok) {
            int sock_to_close = sess->target_fd >= 0 ? sess->target_fd : tfd;
            sess->target_fd = -1;
            if (sock_to_close >= 0) close(sock_to_close);
            if (sess->generation == open_gen) {
                close_fp_session_locked(sess);
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            release_fp_session(sess);
            if (!cred_valid) {
                resp.status = FP_STATUS_UNAUTHORIZED;
                return send_fp_response(client_fd, 401, "Unauthorized", &resp, http_req->keep_alive);
            }
            resp.status = FP_STATUS_DIAL_ERROR;
            return send_fp_response(client_fd, 502, "Bad Gateway", &resp, http_req->keep_alive);
        }

        if (spawn_fp_reader_locked(sess) < 0) {
            sess->target_fd = -1;
            close(tfd);
            close_fp_session_locked(sess);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            release_fp_session(sess);
            resp.status = FP_STATUS_RESOURCE_LIMIT;
            return send_fp_response(client_fd, 503, "Service Unavailable", &resp, http_req->keep_alive);
        }
        sess->dialing = false;
        sess->active = true;
        pthread_cond_broadcast(&sess->cond);
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_fp_table_mu);
    }

    release_fp_session(sess);
    resp.status = FP_STATUS_OK;
    memcpy(resp.sid, assigned_sid, 16);
    return send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
}

static fp_session_t *lookup_fp_session(const uint8_t sid[16], const char *client_id, const uint8_t auth_secret[32],
                                       uint64_t *out_gen, int *err_http, uint8_t *err_fp) {
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (s->active && !s->closed && memcmp(s->id, sid, 16) == 0) {
            if (strcasecmp(s->owner_id, client_id) != 0 || !ct_memeq(s->owner_secret, auth_secret, 32)) {
                pthread_mutex_unlock(&s->mu);
                pthread_mutex_unlock(&g_fp_table_mu);
                *err_http = 403;
                *err_fp = FP_STATUS_FORBIDDEN;
                return NULL;
            }
            s->ref_count++;
            *out_gen = s->generation;
            pthread_mutex_unlock(&s->mu);
            pthread_mutex_unlock(&g_fp_table_mu);
            return s;
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
    *err_http = 410;
    *err_fp = FP_STATUS_UNKNOWN_SESSION;
    return NULL;
}

static int handle_fp_up(int client_fd, const http_req_t *http_req, const char *client_id,
                        const uint8_t auth_secret[32], const fp_req_env_t *req) {
    int err_http = 410;
    uint8_t err_fp = FP_STATUS_UNKNOWN_SESSION;
    uint64_t expected_gen = 0;
    fp_session_t *sess = lookup_fp_session(req->sid, client_id, auth_secret, &expected_gen, &err_http, &err_fp);
    if (!sess) {
        fp_resp_env_t eresp = { .status = err_fp, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, err_http, "Error", &eresp, http_req->keep_alive);
    }

    if (req->up_offset + (uint64_t)req->payload_len < req->up_offset) {
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_BAD_REQUEST };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 400, "Bad Request", &eresp, http_req->keep_alive);
    }

    pthread_mutex_lock(&sess->up_mu);
    pthread_mutex_lock(&sess->mu);
    if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&sess->up_mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
    }
    sess->last_active_ms = now_ms();

    if (!apply_ack_down_locked(sess, req->ack_down_offset)) {
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&sess->up_mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_BAD_REQUEST };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 400, "Bad Request", &eresp, http_req->keep_alive);
    }

    if (req->flags & FP_REQ_FLAG_EOF) {
        uint64_t eof_off = req->up_offset + req->payload_len;
        if (sess->client_eof_rcvd && sess->client_eof_offset != eof_off) {
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            fp_resp_env_t eresp = { .status = FP_STATUS_BAD_REQUEST };
            memcpy(eresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 400, "Bad Request", &eresp, http_req->keep_alive);
        }
        sess->client_eof_rcvd = true;
        sess->client_eof_offset = eof_off;
    }

    const uint8_t *payload = req->payload;
    size_t plen = req->payload_len;
    uint64_t up_off = req->up_offset;

    if (up_off < sess->next_up_offset) {
        uint64_t overlap = sess->next_up_offset - up_off;
        if (overlap >= plen) {
            payload = NULL;
            plen = 0;
            up_off = sess->next_up_offset;
        } else {
            payload += overlap;
            plen -= (size_t)overlap;
            up_off = sess->next_up_offset;
        }
    }

    /* Out-of-order uplink segment -> store in pending_up */
    if (up_off > sess->next_up_offset && plen > 0) {
        bool exists = false;
        int free_idx = -1;
        for (int i = 0; i < FP_MAX_PENDING_UP; i++) {
            if (sess->pending_up[i].used && sess->pending_up[i].offset == up_off) {
                exists = true;
                break;
            }
            if (!sess->pending_up[i].used && free_idx < 0) free_idx = i;
        }
        if (!exists) {
            if (free_idx < 0 ||
                sess->unacked_len + sess->pending_up_bytes > FP_MAX_SESS_BUF - plen ||
                !fp_global_buf_try_reserve(plen)) {
                uint64_t acked = sess->next_up_offset;
                pthread_mutex_unlock(&sess->mu);
                pthread_mutex_unlock(&sess->up_mu);
                release_fp_session(sess);
                fp_resp_env_t bresp = { .status = FP_STATUS_BACKPRESSURE, .acked_up_offset = acked };
                memcpy(bresp.sid, req->sid, 16);
                return send_fp_response(client_fd, 429, "Too Many Requests", &bresp, http_req->keep_alive);
            }
            uint8_t *cp = (uint8_t *)fp_malloc(plen);
            if (!cp) {
                fp_global_buf_release(plen);
                uint64_t acked = sess->next_up_offset;
                pthread_mutex_unlock(&sess->mu);
                pthread_mutex_unlock(&sess->up_mu);
                release_fp_session(sess);
                fp_resp_env_t rresp = { .status = FP_STATUS_RESOURCE_LIMIT, .acked_up_offset = acked };
                memcpy(rresp.sid, req->sid, 16);
                return send_fp_response(client_fd, 503, "Service Unavailable", &rresp, http_req->keep_alive);
            }
            memcpy(cp, payload, plen);
            sess->pending_up[free_idx].used = true;
            sess->pending_up[free_idx].offset = up_off;
            sess->pending_up[free_idx].data = cp;
            sess->pending_up[free_idx].len = plen;
            sess->pending_up_bytes += plen;
        }
        uint64_t acked = sess->next_up_offset;
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&sess->up_mu);
        release_fp_session(sess);
        fp_resp_env_t okresp = { .status = FP_STATUS_OK, .acked_up_offset = acked };
        memcpy(okresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 200, "OK", &okresp, http_req->keep_alive);
    }

    /* Assemble contiguous chunks starting at next_up_offset */
    uint8_t *to_write = NULL;
    size_t to_write_len = 0;
    if (up_off == sess->next_up_offset && plen > 0) {
        to_write = (uint8_t *)fp_malloc(plen + sess->pending_up_bytes);
        if (!to_write) {
            uint64_t acked = sess->next_up_offset;
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            fp_resp_env_t rresp = { .status = FP_STATUS_RESOURCE_LIMIT, .acked_up_offset = acked };
            memcpy(rresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 503, "Service Unavailable", &rresp, http_req->keep_alive);
        }
        memcpy(to_write, payload, plen);
        to_write_len = plen;
        uint64_t cursor = sess->next_up_offset + plen;
        bool found_more = true;
        while (found_more) {
            found_more = false;
            for (int i = 0; i < FP_MAX_PENDING_UP; i++) {
                if (sess->pending_up[i].used && sess->pending_up[i].offset == cursor) {
                    memcpy(to_write + to_write_len, sess->pending_up[i].data, sess->pending_up[i].len);
                    to_write_len += sess->pending_up[i].len;
                    cursor += sess->pending_up[i].len;
                    sess->pending_up_bytes -= sess->pending_up[i].len;
                    fp_global_buf_release(sess->pending_up[i].len);
                    free(sess->pending_up[i].data);
                    sess->pending_up[i].data = NULL;
                    sess->pending_up[i].len = 0;
                    sess->pending_up[i].used = false;
                    found_more = true;
                    break;
                }
            }
        }
        pthread_cond_broadcast(&sess->cond);
    }

    bool need_dial = sess->deferred_dial && (sess->target_fd < 0) && (to_write_len > 0 || sess->client_eof_rcvd);
    char orig_host[256] = {0};
    uint16_t port = sess->port;
    fp_egress_snap_t esnap = sess->egress_cfg;
    if (need_dial) {
        snprintf(orig_host, sizeof(orig_host), "%s", sess->orig_host);
    }
    pthread_mutex_unlock(&sess->mu);

    if (need_dial) {
        int tfd = -1;
        xhttpd_config_t *ecfg = (xhttpd_config_t *)fp_malloc(sizeof(*ecfg));
        if (ecfg) {
            fp_populate_egress_cfg(ecfg, &esnap);
            tfd = dial_target_tcp(orig_host, port, to_write, to_write_len, ecfg, 8000);
            free(ecfg);
        }
        pthread_mutex_lock(&sess->mu);
        if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
            if (tfd >= 0) close(tfd);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            if (to_write) free(to_write);
            fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
            memcpy(eresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
        }
        sess->deferred_dial = false;
        if (tfd < 0) {
            sess->target_reset = true;
            pthread_cond_broadcast(&sess->cond);
            uint64_t acked = sess->next_up_offset;
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            if (to_write) free(to_write);
            fp_resp_env_t dresp = {
                .status = FP_STATUS_DIAL_ERROR,
                .flags = FP_RESP_FLAG_TARGET_RESET,
                .acked_up_offset = acked
            };
            memcpy(dresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 200, "OK", &dresp, http_req->keep_alive);
        }
        sess->target_fd = tfd;
        if (spawn_fp_reader_locked(sess) < 0) {
            sess->target_reset = true;
            shutdown(tfd, SHUT_RDWR);
            uint64_t acked = sess->next_up_offset;
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            if (to_write) free(to_write);
            fp_resp_env_t rresp = {
                .status = FP_STATUS_RESOURCE_LIMIT,
                .flags = FP_RESP_FLAG_TARGET_RESET,
                .acked_up_offset = acked
            };
            memcpy(rresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 503, "Service Unavailable", &rresp, http_req->keep_alive);
        }
        pthread_mutex_unlock(&sess->mu);
    }

    if (to_write_len > 0) {
        pthread_mutex_lock(&sess->mu);
        if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            free(to_write);
            fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
            memcpy(eresp.sid, req->sid, 16);
            return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
        }
        int tfd = sess->target_fd;
        pthread_mutex_unlock(&sess->mu);

        int up_write_to = __atomic_load_n(&g_fp_initial_write_timeout_ms, __ATOMIC_ACQUIRE);
        if (up_write_to <= 0) up_write_to = 15000;
        if (tfd < 0 || write_cancellable(sess, expected_gen, tfd, to_write, to_write_len, up_write_to) < 0) {
            free(to_write);
            pthread_mutex_lock(&sess->mu);
            sess->target_reset = true;
            fp_resp_env_t resp;
            uint8_t *alloc_p = NULL;
            build_down_slice_locked(sess, req->want_down_offset, req->max_down_bytes, &resp, &alloc_p);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_mu);
            release_fp_session(sess);
            int rc = send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
            if (alloc_p) free(alloc_p);
            return rc;
        }
        free(to_write);
        pthread_mutex_lock(&sess->mu);
        sess->next_up_offset += to_write_len;
        pthread_mutex_unlock(&sess->mu);
    }

    pthread_mutex_lock(&sess->mu);
    if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&sess->up_mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
    }
    if (sess->client_eof_rcvd && !sess->client_half_closed && sess->next_up_offset == sess->client_eof_offset) {
        sess->client_half_closed = true;
        if (sess->target_fd >= 0) {
            shutdown(sess->target_fd, SHUT_WR);
        }
    }
    fp_resp_env_t resp;
    uint8_t *alloc_p = NULL;
    if (build_down_slice_locked(sess, req->want_down_offset, req->max_down_bytes, &resp, &alloc_p) < 0) {
        uint64_t acked = sess->next_up_offset;
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&sess->up_mu);
        release_fp_session(sess);
        fp_resp_env_t rresp = { .status = FP_STATUS_RESOURCE_LIMIT, .acked_up_offset = acked };
        memcpy(rresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 503, "Service Unavailable", &rresp, http_req->keep_alive);
    }
    pthread_mutex_unlock(&sess->mu);
    pthread_mutex_unlock(&sess->up_mu);
    release_fp_session(sess);

    int rc = send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
    if (alloc_p) free(alloc_p);
    return rc;
}

static int handle_fp_down(int client_fd, const http_req_t *http_req, const char *client_id,
                          const uint8_t auth_secret[32], const fp_req_env_t *req) {
    int err_http = 410;
    uint8_t err_fp = FP_STATUS_UNKNOWN_SESSION;
    uint64_t expected_gen = 0;
    fp_session_t *sess = lookup_fp_session(req->sid, client_id, auth_secret, &expected_gen, &err_http, &err_fp);
    if (!sess) {
        fp_resp_env_t eresp = { .status = err_fp, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, err_http, "Error", &eresp, http_req->keep_alive);
    }

    pthread_mutex_lock(&sess->mu);
    if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
        pthread_mutex_unlock(&sess->mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
    }
    sess->last_active_ms = now_ms();
    if (!apply_ack_down_locked(sess, req->ack_down_offset)) {
        pthread_mutex_unlock(&sess->mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_BAD_REQUEST };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 400, "Bad Request", &eresp, http_req->keep_alive);
    }

    uint64_t want = req->want_down_offset;
    if (want < sess->base_acked_down) want = sess->base_acked_down;

    if (sess->generated_down <= want && !sess->target_eof && !sess->target_reset && !sess->closed) {
        struct timespec ts;
        clock_gettime(CLOCK_REALTIME, &ts);
        ts.tv_nsec += FP_POLL_WAIT_MS * 1000000L;
        if (ts.tv_nsec >= 1000000000L) {
            ts.tv_sec += 1;
            ts.tv_nsec -= 1000000000L;
        }
        pthread_cond_timedwait(&sess->cond, &sess->mu, &ts);
    }

    if (!session_matches_locked(sess, expected_gen, req->sid, client_id, auth_secret)) {
        pthread_mutex_unlock(&sess->mu);
        release_fp_session(sess);
        fp_resp_env_t eresp = { .status = FP_STATUS_UNKNOWN_SESSION, .flags = FP_RESP_FLAG_SESSION_CLOSED };
        memcpy(eresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 410, "Gone", &eresp, http_req->keep_alive);
    }

    fp_resp_env_t resp;
    uint8_t *alloc_p = NULL;
    if (build_down_slice_locked(sess, want, req->max_down_bytes, &resp, &alloc_p) < 0) {
        uint64_t acked = sess->next_up_offset;
        pthread_mutex_unlock(&sess->mu);
        release_fp_session(sess);
        fp_resp_env_t rresp = { .status = FP_STATUS_RESOURCE_LIMIT, .acked_up_offset = acked };
        memcpy(rresp.sid, req->sid, 16);
        return send_fp_response(client_fd, 503, "Service Unavailable", &rresp, http_req->keep_alive);
    }
    pthread_mutex_unlock(&sess->mu);
    release_fp_session(sess);

    int rc = send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
    if (alloc_p) free(alloc_p);
    return rc;
}

static int handle_fp_close(int client_fd, const http_req_t *http_req, const char *client_id,
                           const uint8_t auth_secret[32], const fp_req_env_t *req) {
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if ((s->active || s->dialing) && !s->closed && memcmp(s->id, req->sid, 16) == 0) {
            if (strcasecmp(s->owner_id, client_id) != 0 || !ct_memeq(s->owner_secret, auth_secret, 32)) {
                pthread_mutex_unlock(&s->mu);
                pthread_mutex_unlock(&g_fp_table_mu);
                fp_resp_env_t eresp = { .status = FP_STATUS_FORBIDDEN };
                memcpy(eresp.sid, req->sid, 16);
                return send_fp_response(client_fd, 403, "Forbidden", &eresp, http_req->keep_alive);
            }
            close_fp_session_locked(s);
            pthread_mutex_unlock(&s->mu);
            break;
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);

    fp_resp_env_t resp = { .status = FP_STATUS_OK, .flags = FP_RESP_FLAG_SESSION_CLOSED };
    memcpy(resp.sid, req->sid, 16);
    return send_fp_response(client_fd, 200, "OK", &resp, http_req->keep_alive);
}

int handle_fp_http_request(int client_fd, const http_req_t *req, const xhttpd_config_t *cfg) {
    g_tl_fp_is_head = (req && strcasecmp(req->method, "HEAD") == 0);

    fp_resp_env_t eresp;
    memset(&eresp, 0, sizeof(eresp));

    char client_id[64] = {0};
    uint8_t auth_secret[32] = {0};
    int auth_rc = verify_fp_auth(req, cfg, client_id, sizeof(client_id), auth_secret);
    if (auth_rc != 0) {
        if (auth_rc == -2) {
            eresp.status = FP_STATUS_BAD_REQUEST;
            return send_fp_response(client_fd, 400, "Bad Request", &eresp, req->keep_alive);
        }
        eresp.status = FP_STATUS_UNAUTHORIZED;
        return send_fp_response(client_fd, 401, "Unauthorized", &eresp, req->keep_alive);
    }

    fp_req_env_t fpreq;
    if (decode_fp_req(req->body, req->body_len, &fpreq) != 0) {
        eresp.status = FP_STATUS_BAD_REQUEST;
        return send_fp_response(client_fd, 400, "Bad Request", &eresp, req->keep_alive);
    }

    if (g_tl_fp_is_head && fpreq.max_down_bytes > 2048) {
        fpreq.max_down_bytes = 2048;
    }

    switch (fpreq.op) {
        case FP_OP_OPEN:  return handle_fp_open(client_fd, req, cfg, client_id, auth_secret, &fpreq);
        case FP_OP_UP:    return handle_fp_up(client_fd, req, client_id, auth_secret, &fpreq);
        case FP_OP_DOWN:  return handle_fp_down(client_fd, req, client_id, auth_secret, &fpreq);
        case FP_OP_CLOSE: return handle_fp_close(client_fd, req, client_id, auth_secret, &fpreq);
        default:
            eresp.status = FP_STATUS_BAD_REQUEST;
            memcpy(eresp.sid, fpreq.sid, 16);
            return send_fp_response(client_fd, 400, "Bad Request", &eresp, req->keep_alive);
    }
}

void fp_engine_revoke_unauthorised(const xhttpd_config_t *cfg) {
    pthread_mutex_lock(&g_fp_table_mu);
    g_num_active_fp_creds = 0;
    if (cfg) {
        for (int j = 0; j < cfg->num_fp_creds && j < MAX_FP_CREDS; j++) {
            if (cfg->fp_creds[j].secret_len == 32) {
                g_active_fp_creds[g_num_active_fp_creds++] = cfg->fp_creds[j];
            }
        }
    }
    __atomic_add_fetch(&g_fp_cred_gen, 1, __ATOMIC_ACQ_REL);

    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if ((s->active || s->dialing) && !s->closed) {
            bool authorized = false;
            for (int j = 0; j < g_num_active_fp_creds; j++) {
                if (strcasecmp(g_active_fp_creds[j].client_id, s->owner_id) == 0 &&
                    ct_memeq(g_active_fp_creds[j].secret, s->owner_secret, 32)) {
                    authorized = true;
                    break;
                }
            }
            if (!authorized) {
                close_fp_session_locked(s);
            }
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
}

void fp_engine_reap_stale(void) {
    uint64_t now = now_ms();
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        fp_session_t *s = &g_fp_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (!s->active || s->closed) {
            pthread_mutex_unlock(&s->mu);
            continue;
        }
        uint64_t idle_ms = now > s->last_active_ms ? (now - s->last_active_ms) : 0;
        uint64_t limit_ms = (uint64_t)s->egress_cfg.idle_timeout_sec * 1000ULL;
        if (limit_ms == 0) limit_ms = 120000ULL;
        if ((s->target_reset || (s->target_eof && s->unacked_len == 0)) && limit_ms > 10000ULL) {
            limit_ms = 10000ULL;
        }
        if (idle_ms > limit_ms) {
            close_fp_session_locked(s);
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
}

void fp_engine_close_all(void) {
    pthread_mutex_lock(&g_fp_table_mu);
    for (int i = 0; i < FP_MAX_SESSIONS; i++) {
        pthread_mutex_lock(&g_fp_sessions[i].mu);
        if (g_fp_sessions[i].active) {
            close_fp_session_locked(&g_fp_sessions[i]);
        }
        pthread_mutex_unlock(&g_fp_sessions[i].mu);
    }
    pthread_mutex_unlock(&g_fp_table_mu);
}
