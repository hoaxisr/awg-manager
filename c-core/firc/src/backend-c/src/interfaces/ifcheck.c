#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */
#include "firc/ifcheck.h"

#include <arpa/inet.h>
#include <assert.h>
#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

#include <curl/curl.h>

#include "firc/dnswire.h"
#include "firc/duration.h"
#include "firc/json.h"
#include "firc/keenetic_rci.h"
#include "firc/rand.h"
#include "firc/sub_fetch.h"

struct firc_ifcheck {
    firc_loop_t *loop;
    firc_ifcheck_done_fn done;
    void *ud;
    firc_ifcheck_group_done_fn group_done;
    void *group_ud;
    pthread_t thread;
    pthread_t loop_thread;
    pthread_mutex_t mu;
    pthread_cond_t cv;
    bool started, dead;
    _Atomic bool stop;
    size_t posted;
    firc_ifcheck_settings_t s;
    firc_ifcheck_job_t q[FIRC_IFCHECK_BATCH];
    size_t n_q;
    firc_ifcheck_group_job_t gq[FIRC_IFCHECK_GROUP_BATCH];
    size_t n_gq;
    char seen[FIRC_IFCHECK_BATCH][16];
    bool seen_wan[FIRC_IFCHECK_BATCH];
    size_t n_seen, oldest;
};

typedef struct batch {
    firc_ifcheck_t *c;
    size_t n;
    firc_ifcheck_result_t r[FIRC_IFCHECK_BATCH];
} batch_t;

typedef struct group_batch {
    firc_ifcheck_t *c;
    firc_ifcheck_group_result_t r;
} group_batch_t;

static int64_t mono_now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static bool url_parts(const char *url, char *host, size_t cap, uint16_t *port) {
    CURLU *u = curl_url();
    if (u == NULL) { return false; }
    char *h = NULL, *p = NULL;
    bool ok = curl_url_set(u, CURLUPART_URL, url, 0) == CURLUE_OK &&
              curl_url_get(u, CURLUPART_HOST, &h, 0) == CURLUE_OK &&
              curl_url_get(u, CURLUPART_PORT, &p, CURLU_DEFAULT_PORT) == CURLUE_OK;
    if (ok) {
        size_t hl = strlen(h);
        if (hl >= 2 && h[0] == '[' && h[hl - 1] == ']') {
            snprintf(host, cap, "%.*s", (int)(hl - 2), h + 1);
        } else {
            snprintf(host, cap, "%s", h);
        }
        *port = (uint16_t)strtoul(p, NULL, 10);
    }
    curl_free(h);
    curl_free(p);
    curl_url_cleanup(u);
    return ok;
}

void firc_ifcheck_settings_from(const firc_app_config_t *a, firc_ifcheck_settings_t *out) {
    memset(out, 0, sizeof(*out));
    snprintf(out->url, sizeof(out->url), "%s", a->check.url != NULL ? a->check.url : "");
    snprintf(out->wan_url, sizeof(out->wan_url), "%s", a->check.wan_url != NULL ? a->check.wan_url : "");
    out->timeout_s = (uint32_t)(a->check.timeout / FIRC_DURATION_SEC);
    const char *u = a->dns_proxy.upstream.address != NULL ? a->dns_proxy.upstream.address : "";
    size_t n = strlen(u);
    if (n >= 2 && u[0] == '[' && u[n - 1] == ']') {
        snprintf(out->upstream, sizeof(out->upstream), "%.*s", (int)(n - 2), u + 1);
    } else {
        snprintf(out->upstream, sizeof(out->upstream), "%s", u);
    }
    out->upstream_port = a->dns_proxy.upstream.port;
}

static bool discard(void *ud, const uint8_t *p, size_t n) {
    (void)ud;
    (void)p;
    (void)n;
    return true;
}

static bool keep_going(void *ud, size_t bytes, size_t total) {
    (void)bytes;
    (void)total;
    _Atomic bool *stop = ud;
    return stop == NULL || !atomic_load(stop);
}

bool firc_ifcheck_once_ex(const char *url, const char *addr, uint32_t mark, uint32_t timeout_ms,
                          bool accept_http_2xx_4xx, _Atomic bool *stop, long *status, char *error, size_t cap) {
    char host[256], pin[320], curl_err[CURL_ERROR_SIZE] = "";
    uint16_t port = 0;
    *status = 0;
    if (!url_parts(url, host, sizeof(host), &port)) {
        snprintf(error, cap, "not a check URL");
        return false;
    }
    firc_sub_fetch_opts_t o = {.mark = mark,
                               .timeout_ms = timeout_ms > 0 ? (long)timeout_ms : 0L,
                               .no_redirects = true,
                               .progress = keep_going,
                               .progress_ud = stop,
                               .errbuf = curl_err,
                               .errbuf_cap = sizeof(curl_err)};
    if (addr != NULL && addr[0] != '\0' && strcmp(addr, host) != 0) {
        snprintf(pin, sizeof(pin), strchr(addr, ':') != NULL ? "%s:%u:[%s]" : "%s:%u:%s", host, (unsigned)port, addr);
        o.resolve = pin;
    }
    firc_err_t err = firc_sub_fetch_to(url, &o, discard, NULL, status);
    bool ok = accept_http_2xx_4xx
                  ? ((err == FIRC_OK || err == FIRC_ERR_PROTO) && *status >= 200 && *status <= 499)
                  : (err == FIRC_OK && *status == 204);
    if (ok) {
        error[0] = '\0';
        return true;
    }
    if (err == FIRC_OK || err == FIRC_ERR_PROTO) {
        snprintf(error, cap, "answered %ld%s", *status, accept_http_2xx_4xx ? "" : ", not 204");
    } else if (err == FIRC_ERR_IO) {
        snprintf(error, cap, "%s", curl_err[0] != '\0' ? curl_err : "the request failed");
    } else if (err == FIRC_ERR_SYS) {
        *status = -1;
        snprintf(error, cap, "the socket refused the check's mark");
    } else {
        snprintf(error, cap, "%s", firc_err_str(err));
    }
    return false;
}

bool firc_ifcheck_once(const char *url, const char *addr, uint32_t mark, uint32_t timeout_s, _Atomic bool *stop,
                       long *status, char *error, size_t cap) {
    uint32_t ms = timeout_s > UINT32_MAX / 1000u ? UINT32_MAX : timeout_s * 1000u;
    return firc_ifcheck_once_ex(url, addr, mark, ms, false, stop, status, error, cap);
}

static bool to_sockaddr(const char *server, uint16_t port, struct sockaddr_storage *ss, socklen_t *len) {
    memset(ss, 0, sizeof(*ss));
    struct sockaddr_in *s4 = (struct sockaddr_in *)ss;
    struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)ss;
    if (inet_pton(AF_INET, server, &s4->sin_addr) == 1) {
        s4->sin_family = AF_INET;
        s4->sin_port = htons(port);
        *len = sizeof(*s4);
        return true;
    }
    if (inet_pton(AF_INET6, server, &s6->sin6_addr) == 1) {
        s6->sin6_family = AF_INET6;
        s6->sin6_port = htons(port);
        *len = sizeof(*s6);
        return true;
    }
    return false;
}

static firc_err_t answer_a(const uint8_t *a, size_t n, uint16_t id, char *addr, size_t cap, bool *mine) {
    firc_dns_msg_t *msg = NULL;
    *mine = false;
    if (firc_dns_msg_parse(a, n, &msg) != FIRC_OK) { return FIRC_ERR_PROTO; }
    *mine = msg->id == id && (msg->flags & FIRC_DNS_FLAG_QR) != 0;
    firc_err_t err = FIRC_ERR_NOENT;
    for (size_t i = 0; *mine && i < msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        if (rr->rtype == FIRC_DNS_TYPE_A && rr->rdata_len == 4 &&
            inet_ntop(AF_INET, rr->rdata, addr, (socklen_t)cap) != NULL) {
            err = FIRC_OK;
            break;
        }
    }
    firc_dns_msg_free(msg);
    return err;
}

static bool extract_host(const char *url_or_host, char *host, size_t cap, uint16_t *port) {
    if (url_or_host == NULL || url_or_host[0] == '\0' || cap == 0) { return false; }
    if (strstr(url_or_host, "://") != NULL) { return url_parts(url_or_host, host, cap, port); }
    size_t n = strlen(url_or_host);
    if (n >= 2 && url_or_host[0] == '[' && url_or_host[n - 1] == ']') {
        snprintf(host, cap, "%.*s", (int)(n - 2), url_or_host + 1);
    } else {
        snprintf(host, cap, "%s", url_or_host);
    }
    *port = 443;
    return host[0] != '\0';
}

firc_err_t firc_ifcheck_resolve_marked(const char *server, uint16_t port, uint32_t mark, const char *url,
                                       uint32_t timeout_ms, char *addr, size_t cap, _Atomic bool *stop) {
    char host[256];
    uint16_t hp = 0;
    if (!extract_host(url, host, sizeof(host), &hp)) { return FIRC_ERR_INVAL; }
    struct in6_addr probe;
    if (inet_pton(AF_INET, host, &probe) == 1 || inet_pton(AF_INET6, host, &probe) == 1) {
        snprintf(addr, cap, "%s", host);
        return FIRC_OK;
    }
    struct sockaddr_storage ss;
    socklen_t sl = 0;
    if (!to_sockaddr(server, port, &ss, &sl)) { return FIRC_ERR_INVAL; }
    uint8_t q[FIRC_DNS_HEADER_LEN + FIRC_DNS_MAX_NAME + 4];
    memset(q, 0, sizeof(q));
    size_t nl = 0;
    if (!firc_dns_name_from_text(host, q + FIRC_DNS_HEADER_LEN, &nl)) { return FIRC_ERR_INVAL; }
    uint8_t idb[2];
    if (firc_random_bytes(idb, sizeof(idb)) != FIRC_OK) { return FIRC_ERR_SYS; }
    uint16_t id = (uint16_t)((idb[0] << 8) | idb[1]);
    q[0] = idb[0];
    q[1] = idb[1];
    q[2] = 0x01;
    q[5] = 1;
    size_t at = FIRC_DNS_HEADER_LEN + nl;
    q[at++] = 0;
    q[at++] = FIRC_DNS_TYPE_A;
    q[at++] = 0;
    q[at++] = FIRC_DNS_CLASS_IN;
    int fd = socket(ss.ss_family, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    if (fd < 0) { return firc_err_from_errno(errno); }
    if (mark != 0 && firc_sub_fetch_get_setsockopt()(fd, SOL_SOCKET, SO_MARK, &mark, sizeof(mark)) != 0) {
        close(fd);
        return FIRC_ERR_SYS;
    }
    struct timeval tv = {0, 200000};
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    uint32_t eff_ms = timeout_ms > 0 ? timeout_ms : 1000u;
    int64_t deadline = mono_now_ms() + (int64_t)eff_ms;
    firc_err_t err = FIRC_ERR_IO;
    if (connect(fd, (struct sockaddr *)&ss, sl) == 0 && send(fd, q, at, 0) == (ssize_t)at) {
        uint8_t a[1500];
        for (int tries = 0; tries < 32; tries++) {
            ssize_t got = recv(fd, a, sizeof(a), 0);
            if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR)) {
                if (mono_now_ms() < deadline && (stop == NULL || !atomic_load(stop))) {
                    tries--;
                    continue;
                }
                err = FIRC_ERR_TIMEOUT;
                break;
            }
            if (got < 0) {
                err = firc_err_from_errno(errno);
                break;
            }
            bool mine = false;
            err = answer_a(a, (size_t)got, id, addr, cap, &mine);
            if (mine) { break; }
            err = FIRC_ERR_TIMEOUT;
        }
    }
    close(fd);
    return err;
}

firc_err_t firc_ifcheck_resolve(const char *server, uint16_t port, const char *url, uint32_t timeout_s, char *addr,
                                size_t cap, _Atomic bool *stop) {
    uint32_t ms = timeout_s > UINT32_MAX / 1000u ? UINT32_MAX : timeout_s * 1000u;
    return firc_ifcheck_resolve_marked(server, port, 0, url, ms, addr, cap, stop);
}

typedef struct one_check {
    const char *url;
    const char *addr;
    uint32_t mark;
    uint32_t timeout_s;
    _Atomic bool *stop;
    firc_ifcheck_result_t *r;
} one_check_t;

static void *run_one(void *ud) {
    one_check_t *o = ud;
    o->r->passed = firc_ifcheck_once(o->url, o->addr, o->mark, o->timeout_s, o->stop, &o->r->status, o->r->error,
                                     sizeof(o->r->error));
    return NULL;
}

typedef struct pin {
    bool done, ok;
    char host[256];
    char addr[64];
    char why[FIRC_IF_ERROR_MAX];
} pin_t;

static const pin_t *pin_of(const firc_ifcheck_settings_t *s, const char *url, pin_t *p, const pin_t *other,
                           _Atomic bool *stop) {
    if (p->done) { return p; }
    uint16_t port = 0;
    if (!url_parts(url, p->host, sizeof(p->host), &port)) { p->host[0] = '\0'; }
    if (other->done && strcmp(other->host, p->host) == 0) {
        *p = *other;
        return p;
    }
    p->done = true;
    firc_err_t err =
        firc_ifcheck_resolve(s->upstream, s->upstream_port, url, s->timeout_s, p->addr, sizeof(p->addr), stop);
    p->ok = err == FIRC_OK;
    if (!p->ok) {
        char why[512];
        snprintf(why, sizeof(why), "the check's host %s does not resolve through %s: %s", p->host, s->upstream,
                 firc_err_str(err));
        snprintf(p->why, sizeof(p->why), "%.159s", why);
    }
    return p;
}

static firc_ifcheck_wan_fn g_wan_fn;
static void *g_wan_ud;

void firc_ifcheck_set_wan_fn_for_test(firc_ifcheck_wan_fn fn, void *ud) {
    g_wan_fn = fn;
    g_wan_ud = ud;
}

static firc_ifcheck_keendns_fn g_keendns_fn;
static void *g_keendns_ud;

void firc_ifcheck_set_keendns_fn_for_test(firc_ifcheck_keendns_fn fn, void *ud) {
    g_keendns_fn = fn;
    g_keendns_ud = ud;
}

bool firc_ifcheck_tcp_probe(const char *addr, uint16_t port, uint32_t mark, uint32_t timeout_ms, _Atomic bool *stop,
                            long *status, char *error, size_t cap) {
    *status = 0;
    if (addr == NULL || addr[0] == '\0' || port == 0) {
        snprintf(error, cap, "not a TCP probe target");
        return false;
    }
    struct sockaddr_storage ss;
    socklen_t sl = 0;
    if (!to_sockaddr(addr, port, &ss, &sl)) {
        snprintf(error, cap, "not an IP address");
        return false;
    }
    int fd = socket(ss.ss_family, SOCK_STREAM, 0);
    if (fd < 0) {
        int e = errno;
        snprintf(error, cap, "socket failed: %s", strerror(e));
        return false;
    }
    int fdf = fcntl(fd, F_GETFD, 0);
    if (fdf >= 0) { (void)fcntl(fd, F_SETFD, fdf | FD_CLOEXEC); }
    int fl = fcntl(fd, F_GETFL, 0);
    if (fl < 0 || fcntl(fd, F_SETFL, fl | O_NONBLOCK) != 0) {
        int e = errno;
        close(fd);
        snprintf(error, cap, "fcntl failed: %s", strerror(e));
        return false;
    }
    if (mark != 0 && firc_sub_fetch_get_setsockopt()(fd, SOL_SOCKET, SO_MARK, &mark, sizeof(mark)) != 0) {
        *status = -1;
        close(fd);
        snprintf(error, cap, "the socket refused the check's mark");
        return false;
    }
    int rc = connect(fd, (struct sockaddr *)&ss, sl);
    if (rc == 0) {
        *status = 200;
        if (cap > 0) { error[0] = '\0'; }
        close(fd);
        return true;
    }
    if (errno != EINPROGRESS) {
        int e = errno;
        close(fd);
        snprintf(error, cap, "TCP connect failed: %s", strerror(e));
        return false;
    }
    uint32_t eff_ms = timeout_ms > 0 ? timeout_ms : FIRC_GROUP_CHECK_DEFAULT_TIMEOUT_MS;
    int64_t deadline = mono_now_ms() + (int64_t)eff_ms;
    for (;;) {
        if (stop != NULL && atomic_load(stop)) {
            close(fd);
            snprintf(error, cap, "stopped");
            return false;
        }
        int64_t rem = deadline - mono_now_ms();
        if (rem <= 0) {
            close(fd);
            snprintf(error, cap, "TCP connect timed out");
            return false;
        }
        int step = rem > 100 ? 100 : (int)rem;
        struct pollfd pfd = {.fd = fd, .events = POLLOUT};
        int pr = poll(&pfd, 1, step);
        if (pr < 0) {
            if (errno == EINTR) { continue; }
            int e = errno;
            close(fd);
            snprintf(error, cap, "TCP poll failed: %s", strerror(e));
            return false;
        }
        if (pr > 0) {
            int soerr = 0;
            socklen_t len = sizeof(soerr);
            if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &soerr, &len) != 0) { soerr = errno; }
            close(fd);
            if (soerr == 0) {
                *status = 200;
                if (cap > 0) { error[0] = '\0'; }
                return true;
            }
            snprintf(error, cap, "TCP connect failed: %s", strerror(soerr));
            return false;
        }
    }
}

static void trim_copy(const char *src, char *dst, size_t cap) {
    if (cap == 0) { return; }
    dst[0] = '\0';
    if (src == NULL) { return; }
    while (*src != '\0' && isspace((unsigned char)*src)) { src++; }
    size_t len = strlen(src);
    while (len > 0 && isspace((unsigned char)src[len - 1])) { len--; }
    if (len >= cap) { len = cap - 1; }
    memcpy(dst, src, len);
    dst[len] = '\0';
}

static bool is_public_ipv4_str(const char *s) {
    struct in_addr addr;
    if (inet_pton(AF_INET, s, &addr) != 1) { return false; }
    uint32_t ip = ntohl(addr.s_addr);
    uint8_t o0 = (uint8_t)(ip >> 24);
    uint8_t o1 = (uint8_t)((ip >> 16) & 0xffu);
    if (o0 == 0 || o0 == 10 || o0 == 127 || o0 >= 224) { return false; }
    if (o0 == 100 && o1 >= 64 && o1 <= 127) { return false; }
    if (o0 == 169 && o1 == 254) { return false; }
    if (o0 == 172 && o1 >= 16 && o1 <= 31) { return false; }
    if (o0 == 192 && o1 == 168) { return false; }
    if ((ip & 0xffffff00u) == 0xc0000000u || (ip & 0xffffff00u) == 0xc0586300u) { return false; }
    if ((ip & 0xfffe0000u) == 0xc6120000u) { return false; }
    return true;
}

static bool parse_port_str(const char *s, uint16_t *out) {
    if (s == NULL || s[0] == '\0') { return false; }
    char *end = NULL;
    unsigned long v = strtoul(s, &end, 10);
    if (*end != '\0' || v == 0 || v > 65535UL) { return false; }
    *out = (uint16_t)v;
    return true;
}

static bool parse_relay_endpoint(const char *s, firc_ifcheck_target_t *t) {
    char buf[160];
    trim_copy(s, buf, sizeof(buf));
    if (buf[0] == '\0') { return false; }
    memset(t, 0, sizeof(*t));
    char ip[64] = "";
    const char *port_s = NULL;
    if (strncmp(buf, "[::ffff:", 8) == 0 || strncmp(buf, "[::FFFF:", 8) == 0) {
        const char *mid = strstr(buf + 8, "]:");
        if (mid == NULL) { return false; }
        size_t n = (size_t)(mid - (buf + 8));
        if (n == 0 || n >= sizeof(ip)) { return false; }
        memcpy(ip, buf + 8, n);
        ip[n] = '\0';
        port_s = mid + 2;
    } else {
        const char *colon = strchr(buf, ':');
        if (colon == NULL || strchr(colon + 1, ':') != NULL) { return false; }
        size_t n = (size_t)(colon - buf);
        if (n == 0 || n >= sizeof(ip)) { return false; }
        memcpy(ip, buf, n);
        ip[n] = '\0';
        port_s = colon + 1;
    }
    uint16_t port = 0;
    if (!parse_port_str(port_s, &port) || !is_public_ipv4_str(ip)) { return false; }
    snprintf(t->host, sizeof(t->host), "%s", ip);
    t->port = port;
    t->is_ip = true;
    return true;
}

static bool parse_domain_target(const char *s, firc_ifcheck_target_t *t) {
    char buf[160];
    trim_copy(s, buf, sizeof(buf));
    if (buf[0] == '\0' || strchr(buf, '[') != NULL) { return false; }
    for (const char *p = buf; *p != '\0'; p++) {
        if (isspace((unsigned char)*p)) { return false; }
    }
    memset(t, 0, sizeof(*t));
    uint16_t port = 443;
    char *colon = strchr(buf, ':');
    if (colon != NULL) {
        if (strchr(colon + 1, ':') != NULL || !parse_port_str(colon + 1, &port)) { return false; }
        *colon = '\0';
    }
    size_t len = strlen(buf);
    while (len > 0 && buf[len - 1] == '.') { buf[--len] = '\0'; }
    if (len == 0) { return false; }
    for (size_t i = 0; i < len; i++) { buf[i] = (char)tolower((unsigned char)buf[i]); }
    struct in_addr probe4;
    if (inet_pton(AF_INET, buf, &probe4) == 1) {
        if (!is_public_ipv4_str(buf)) { return false; }
        t->is_ip = true;
    }
    snprintf(t->host, sizeof(t->host), "%s", buf);
    t->port = port;
    return true;
}

static void append_unique_target(firc_ifcheck_target_t *out, size_t *n, size_t cap, const firc_ifcheck_target_t *t) {
    if (*n >= cap) { return; }
    for (size_t i = 0; i < *n; i++) {
        if (out[i].port == t->port && strcmp(out[i].host, t->host) == 0) { return; }
    }
    out[(*n)++] = *t;
}

size_t firc_ifcheck_parse_keendns_targets(const char *json, firc_ifcheck_target_t *out, size_t cap) {
    if (json == NULL || out == NULL || cap == 0) { return 0; }
    cJSON *root = cJSON_Parse(json);
    if (root == NULL) { return 0; }
    const cJSON *ttp = cJSON_GetObjectItemCaseSensitive(root, "ttp");
    const cJSON *tunnels = cJSON_IsObject(ttp) ? cJSON_GetObjectItemCaseSensitive(ttp, "tunnel") : NULL;
    size_t count = 0;
    if (cJSON_IsArray(tunnels)) {
        const cJSON *item = NULL;
        cJSON_ArrayForEach(item, tunnels) {
            const cJSON *tr = cJSON_IsObject(item) ? cJSON_GetObjectItemCaseSensitive(item, "target-remote") : NULL;
            firc_ifcheck_target_t t;
            if (cJSON_IsString(tr) && parse_relay_endpoint(tr->valuestring, &t)) {
                append_unique_target(out, &count, cap, &t);
            }
        }
        cJSON_ArrayForEach(item, tunnels) {
            const cJSON *tg = cJSON_IsObject(item) ? cJSON_GetObjectItemCaseSensitive(item, "target") : NULL;
            firc_ifcheck_target_t t;
            if (cJSON_IsString(tg) && parse_domain_target(tg->valuestring, &t)) {
                append_unique_target(out, &count, cap, &t);
            }
        }
    }
    cJSON_Delete(root);
    return count;
}

void firc_ifcheck_keendns_targets(firc_ifcheck_target_t *out, size_t cap, size_t *out_n, _Atomic bool *stop) {
    if (out_n == NULL || out == NULL || cap == 0) { return; }
    *out_n = 0;
    if (g_keendns_fn != NULL) {
        (void)g_keendns_fn(out, cap, out_n, g_keendns_ud);
    } else {
        char *body = NULL;
        if (firc_kn_rci_get_timeout(FIRC_KN_RCI_BASE_URL, "/rci/show/ndns", FIRC_KN_RCI_TIMEOUT_SECONDS, stop,
                                    &body) == FIRC_OK &&
            body != NULL) {
            *out_n = firc_ifcheck_parse_keendns_targets(body, out, cap);
            free(body);
        }
    }
    if (*out_n == 0) {
        static const char *const k_brokers[] = {"broker.keenetic.cloud", "cloud.keenetic.net", "broker.netcraze.cloud"};
        for (size_t i = 0; i < sizeof(k_brokers) / sizeof(k_brokers[0]) && *out_n < cap; i++) {
            firc_ifcheck_target_t t = {.port = 443, .is_ip = false};
            snprintf(t.host, sizeof(t.host), "%s", k_brokers[i]);
            append_unique_target(out, out_n, cap, &t);
        }
    }
}

typedef struct group_cand_task {
    const firc_ifcheck_settings_t *s;
    const firc_ifcheck_group_job_t *job;
    const firc_ifcheck_group_cand_t *cand;
    const firc_ifcheck_target_t *targets;
    size_t n_targets;
    _Atomic bool *stop;
    firc_ifcheck_group_cand_result_t *res;
} group_cand_task_t;

static void resolve_cand_dns(const firc_ifcheck_settings_t *s, const firc_ifcheck_group_cand_t *cand,
                             const char **dns_srv, uint16_t *dns_port, uint32_t *dns_mark) {
    if (cand->dns_server[0] != '\0' && cand->dns_port != 0) {
        *dns_srv = cand->dns_server;
        *dns_port = cand->dns_port;
        *dns_mark = cand->dns_mark;
    } else if (s != NULL) {
        *dns_srv = s->upstream;
        *dns_port = s->upstream_port;
        *dns_mark = 0;
    } else {
        *dns_srv = "";
        *dns_port = 0;
        *dns_mark = 0;
    }
}

static void run_group_cand_url(const group_cand_task_t *t, uint32_t timeout_ms, const char *dns_srv, uint16_t dns_port,
                               uint32_t dns_mark) {
    firc_ifcheck_group_cand_result_t *r = t->res;
    const char *url = t->job->url[0] != '\0'
                          ? t->job->url
                          : (t->s != NULL && t->s->url[0] != '\0' ? t->s->url : (t->s != NULL ? t->s->wan_url : ""));
    if (url[0] == '\0') {
        snprintf(r->error, sizeof(r->error), "no check URL configured");
        return;
    }
    char addr[64] = "";
    firc_err_t derr =
        firc_ifcheck_resolve_marked(dns_srv, dns_port, dns_mark, url, timeout_ms, addr, sizeof(addr), t->stop);
    if (derr == FIRC_ERR_SYS) {
        r->status = -1;
        snprintf(r->error, sizeof(r->error), "the socket refused the DNS mark");
        return;
    }
    if (derr != FIRC_OK) {
        snprintf(r->error, sizeof(r->error), "DNS lookup failed: %s", firc_err_str(derr));
        return;
    }
    bool accept_any = t->job->url[0] != '\0';
    r->passed = firc_ifcheck_once_ex(url, addr, t->cand->mark, timeout_ms, accept_any, t->stop, &r->status, r->error,
                                     sizeof(r->error));
}

static void run_group_cand_keendns(const group_cand_task_t *t, uint32_t timeout_ms, const char *dns_srv,
                                   uint16_t dns_port, uint32_t dns_mark) {
    firc_ifcheck_group_cand_result_t *r = t->res;
    size_t tries = t->n_targets < 3 ? t->n_targets : 3;
    if (tries == 0) {
        snprintf(r->error, sizeof(r->error), "no KeenDNS relay targets available");
        return;
    }
    int64_t deadline = mono_now_ms() + (int64_t)timeout_ms;
    for (size_t i = 0; i < tries; i++) {
        if (t->stop != NULL && atomic_load(t->stop)) {
            snprintf(r->error, sizeof(r->error), "stopped");
            return;
        }
        int64_t rem = deadline - mono_now_ms();
        if (rem <= 0) {
            if (r->error[0] == '\0') { snprintf(r->error, sizeof(r->error), "TCP connect timed out"); }
            return;
        }
        uint32_t step_ms = rem > (int64_t)UINT32_MAX ? UINT32_MAX : (uint32_t)rem;
        if (tries > 1 && i == 0 && step_ms > timeout_ms / 2u) {
            uint32_t half = timeout_ms / 2u;
            step_ms = half >= 500u ? half : (step_ms < 500u ? step_ms : 500u);
        }
        const firc_ifcheck_target_t *tgt = &t->targets[i];
        char ip[sizeof(tgt->host)] = "";
        if (tgt->is_ip) {
            snprintf(ip, sizeof(ip), "%s", tgt->host);
        } else {
            firc_err_t derr = firc_ifcheck_resolve_marked(dns_srv, dns_port, dns_mark, tgt->host, step_ms, ip,
                                                          sizeof(ip), t->stop);
            if (derr == FIRC_ERR_SYS) {
                r->status = -1;
                snprintf(r->error, sizeof(r->error), "the socket refused the DNS mark");
                return;
            }
            if (derr != FIRC_OK) {
                snprintf(r->error, sizeof(r->error), "DNS lookup failed: %s", firc_err_str(derr));
                continue;
            }
        }
        int64_t rem_tcp = deadline - mono_now_ms();
        uint32_t tcp_ms = rem_tcp > 0 ? (rem_tcp > (int64_t)step_ms ? step_ms : (uint32_t)rem_tcp) : 1u;
        if (firc_ifcheck_tcp_probe(ip, tgt->port, t->cand->mark, tcp_ms, t->stop, &r->status, r->error,
                                   sizeof(r->error))) {
            r->passed = true;
            return;
        }
        if (r->status == -1) { return; }
    }
}

static void *run_group_cand(void *ud) {
    group_cand_task_t *t = ud;
    uint32_t timeout_ms = t->job->timeout_ms > 0
                              ? t->job->timeout_ms
                              : (t->s != NULL && t->s->timeout_s > 0 ? t->s->timeout_s * 1000u
                                                                     : FIRC_GROUP_CHECK_DEFAULT_TIMEOUT_MS);
    const char *dns_srv = "";
    uint16_t dns_port = 0;
    uint32_t dns_mark = 0;
    resolve_cand_dns(t->s, t->cand, &dns_srv, &dns_port, &dns_mark);
    if (t->job->mode == FIRC_GROUP_CHECK_KEENDNS) {
        run_group_cand_keendns(t, timeout_ms, dns_srv, dns_port, dns_mark);
    } else if (t->job->mode == FIRC_GROUP_CHECK_URL) {
        run_group_cand_url(t, timeout_ms, dns_srv, dns_port, dns_mark);
    }
    return NULL;
}

void firc_ifcheck_group_round(const firc_ifcheck_settings_t *s, const firc_ifcheck_group_job_t *job, _Atomic bool *stop,
                              firc_ifcheck_group_result_t *out) {
    if (out == NULL) { return; }
    memset(out, 0, sizeof(*out));
    if (job == NULL) { return; }
    out->group_id = job->group_id;
    out->gen = job->gen;
    size_t n = job->n_cands <= FIRC_GROUP_IFACES_MAX ? job->n_cands : FIRC_GROUP_IFACES_MAX;
    out->n_cands = n;
    for (size_t i = 0; i < n; i++) { snprintf(out->cands[i].iface, sizeof(out->cands[i].iface), "%s", job->cands[i].iface); }
    if (n == 0 || job->mode == FIRC_GROUP_CHECK_NONE) { return; }

    firc_ifcheck_target_t targets[FIRC_IFCHECK_KEENDNS_MAX_TARGETS];
    size_t n_targets = 0;
    if (job->mode == FIRC_GROUP_CHECK_KEENDNS) {
        firc_ifcheck_keendns_targets(targets, FIRC_IFCHECK_KEENDNS_MAX_TARGETS, &n_targets, stop);
    }

    group_cand_task_t tasks[FIRC_GROUP_IFACES_MAX];
    pthread_t th[FIRC_GROUP_IFACES_MAX];
    bool started[FIRC_GROUP_IFACES_MAX] = {false};
    pthread_attr_t attr;
    bool attr_ok = pthread_attr_init(&attr) == 0;
    if (attr_ok) { (void)pthread_attr_setstacksize(&attr, 256 * 1024); }
    for (size_t i = 0; i < n; i++) {
        if (job->cands[i].skip) {
            out->cands[i].guarded = true;
            if (job->cands[i].skip_error[0] != '\0') {
                snprintf(out->cands[i].error, sizeof(out->cands[i].error), "%s", job->cands[i].skip_error);
            }
            continue;
        }
        tasks[i] = (group_cand_task_t){
            .s = s,
            .job = job,
            .cand = &job->cands[i],
            .targets = targets,
            .n_targets = n_targets,
            .stop = stop,
            .res = &out->cands[i],
        };
        started[i] = pthread_create(&th[i], attr_ok ? &attr : NULL, run_group_cand, &tasks[i]) == 0;
        if (!started[i]) { run_group_cand(&tasks[i]); }
    }
    for (size_t i = 0; i < n; i++) {
        if (started[i]) { pthread_join(th[i], NULL); }
    }
    if (attr_ok) { pthread_attr_destroy(&attr); }

    bool any_passed = false;
    for (size_t i = 0; i < n; i++) {
        if (out->cands[i].passed) {
            any_passed = true;
            break;
        }
    }
    for (size_t i = 0; i < n; i++) {
        if (!out->cands[i].passed) {
            out->cands[i].guarded = (job->cands[i].skip || !any_passed || out->cands[i].status == -1);
        }
    }
}

void firc_ifcheck_round(const firc_ifcheck_settings_t *s, const firc_ifcheck_job_t *jobs, size_t n, const bool *wan,
                        _Atomic bool *stop, firc_ifcheck_result_t *out) {
    if (n > FIRC_IFCHECK_BATCH) { n = FIRC_IFCHECK_BATCH; }
    const char *wan_url = s->wan_url[0] != '\0' ? s->wan_url : s->url;
    pin_t pins[2];
    memset(pins, 0, sizeof(pins));
    one_check_t one[FIRC_IFCHECK_BATCH];
    pthread_t th[FIRC_IFCHECK_BATCH];
    bool held[FIRC_IFCHECK_BATCH] = {false}, started[FIRC_IFCHECK_BATCH] = {false};
    for (size_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(out[i]));
        snprintf(out[i].iface, sizeof(out[i].iface), "%s", jobs[i].iface);
        out[i].gen = jobs[i].gen;
        size_t k = wan != NULL && wan[i] ? 1 : 0;
        const char *url = k == 1 ? wan_url : s->url;
        const pin_t *p = pin_of(s, url, &pins[k], &pins[1 - k], stop);
        held[i] = !p->ok;
        if (held[i]) {
            out[i].control_failed = true;
            snprintf(out[i].error, sizeof(out[i].error), "%s", p->why);
        }
        one[i] = (one_check_t){url, p->addr, jobs[i].mark, s->timeout_s, stop, &out[i]};
    }
    pthread_attr_t attr;
    bool attr_ok = pthread_attr_init(&attr) == 0;
    if (attr_ok) { (void)pthread_attr_setstacksize(&attr, 256 * 1024); }
    for (size_t i = 0; i < n; i++) {
        if (held[i]) { continue; }
        started[i] = pthread_create(&th[i], attr_ok ? &attr : NULL, run_one, &one[i]) == 0;
        if (!started[i]) { run_one(&one[i]); }
    }
    for (size_t i = 0; i < n; i++) {
        if (started[i]) { pthread_join(th[i], NULL); }
    }
    if (attr_ok) { pthread_attr_destroy(&attr); }
    bool any = false;
    for (size_t i = 0; i < n; i++) {
        if (held[i] || out[i].passed) { continue; }
        if (out[i].status == -1) {
            out[i].control_failed = true;
            held[i] = true;
            continue;
        }
        any = true;
    }
    if (!any) { return; }
    const pin_t *wp = pin_of(s, wan_url, &pins[1], &pins[0], stop);
    char why[FIRC_IF_ERROR_MAX];
    snprintf(why, sizeof(why), "%s", wp->why);
    long cs = 0;
    bool control = wp->ok && firc_ifcheck_once(wan_url, wp->addr, 0, s->timeout_s, stop, &cs, why, sizeof(why));
    for (size_t i = 0; i < n; i++) {
        if (held[i] || out[i].passed) { continue; }
        out[i].control_ran = wp->ok;
        if (control) { continue; }
        out[i].control_failed = true;
        snprintf(out[i].error, sizeof(out[i].error), "%s", why);
    }
}

static void destroy(firc_ifcheck_t *c) {
    pthread_cond_destroy(&c->cv);
    pthread_mutex_destroy(&c->mu);
    free(c);
}

static bool unpost_with(firc_ifcheck_t *c, bool *last, firc_ifcheck_group_done_fn *gdone, void **gud) {
    pthread_mutex_lock(&c->mu);
    c->posted--;
    bool quiet = c->dead || atomic_load(&c->stop);
    *last = c->dead && c->posted == 0;
    if (gdone != NULL) { *gdone = c->group_done; }
    if (gud != NULL) { *gud = c->group_ud; }
    pthread_mutex_unlock(&c->mu);
    return quiet;
}

static bool unpost(firc_ifcheck_t *c, bool *last) { return unpost_with(c, last, NULL, NULL); }

static void drop_batch(void *ud) {
    batch_t *b = ud;
    firc_ifcheck_t *c = b->c;
    free(b);
    bool last = false;
    (void)unpost(c, &last);
    if (last) { destroy(c); }
}

static void on_batch(firc_loop_t *loop, void *ud) {
    (void)loop;
    batch_t *b = ud;
    firc_ifcheck_t *c = b->c;
    bool last = false;
    if (!unpost(c, &last) && c->done != NULL) { c->done(c->ud, b->r, b->n); }
    free(b);
    if (last) { destroy(c); }
}

static void drop_group_batch(void *ud) {
    group_batch_t *b = ud;
    firc_ifcheck_t *c = b->c;
    free(b);
    bool last = false;
    (void)unpost(c, &last);
    if (last) { destroy(c); }
}

static void on_group_batch(firc_loop_t *loop, void *ud) {
    (void)loop;
    group_batch_t *b = ud;
    firc_ifcheck_t *c = b->c;
    bool last = false;
    firc_ifcheck_group_done_fn fn = NULL;
    void *gud = NULL;
    if (!unpost_with(c, &last, &fn, &gud) && fn != NULL) { fn(gud, &b->r); }
    free(b);
    if (last) { destroy(c); }
}

static void post_item(firc_ifcheck_t *c, void (*fn)(firc_loop_t *, void *), void (*drop)(void *), void *item) {
    pthread_mutex_lock(&c->mu);
    c->posted++;
    pthread_mutex_unlock(&c->mu);
    while (firc_loop_post_with_drop(c->loop, fn, item, drop) != FIRC_OK) {
        if (atomic_load(&c->stop)) {
            pthread_mutex_lock(&c->mu);
            c->posted--;
            pthread_mutex_unlock(&c->mu);
            free(item);
            return;
        }
        struct timespec ts = {0, 50 * 1000000L};
        nanosleep(&ts, NULL);
    }
}

static void post(firc_ifcheck_t *c, batch_t *b) { post_item(c, on_batch, drop_batch, b); }

static void post_group(firc_ifcheck_t *c, group_batch_t *b) { post_item(c, on_group_batch, drop_group_batch, b); }

static firc_err_t rci_wan(const firc_ifcheck_job_t *jobs, size_t n, bool *wan, void *ud) {
    const _Atomic bool *stop = ud;
    if (atomic_load(stop)) { return FIRC_ERR_CANCELED; }
    firc_kn_aliases_t a = {0};
    firc_err_t err = firc_kn_get_iface_aliases(&a);
    for (size_t i = 0; err == FIRC_OK && i < n; i++) { wan[i] = firc_kn_aliases_inet(&a, jobs[i].iface); }
    firc_kn_aliases_free(&a);
    return err;
}

static size_t seen_at(const firc_ifcheck_t *c, const char *iface) {
    size_t i = 0;
    while (i < c->n_seen && strcmp(c->seen[i], iface) != 0) { i++; }
    return i;
}

static void learn_wan(firc_ifcheck_t *c, const char *iface, bool wan) {
    size_t at = seen_at(c, iface);
    if (at == c->n_seen) {
        if (c->n_seen < FIRC_IFCHECK_BATCH) {
            c->n_seen++;
        } else {
            at = c->oldest;
            c->oldest = (c->oldest + 1) % FIRC_IFCHECK_BATCH;
        }
        snprintf(c->seen[at], sizeof(c->seen[0]), "%.15s", iface);
    }
    c->seen_wan[at] = wan;
}

static firc_err_t pick_wan(firc_ifcheck_t *c, const firc_ifcheck_job_t *jobs, size_t n, bool *wan) {
    firc_err_t err = g_wan_fn != NULL ? g_wan_fn(jobs, n, wan, g_wan_ud) : rci_wan(jobs, n, wan, &c->stop);
    for (size_t i = 0; i < n; i++) {
        if (err == FIRC_OK) {
            learn_wan(c, jobs[i].iface, wan[i]);
            continue;
        }
        size_t at = seen_at(c, jobs[i].iface);
        if (at == c->n_seen) { return err; }
        wan[i] = c->seen_wan[at];
    }
    return FIRC_OK;
}

static void hold_round(const firc_ifcheck_job_t *jobs, size_t n, firc_err_t err, firc_ifcheck_result_t *out) {
    for (size_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(out[i]));
        snprintf(out[i].iface, sizeof(out[i].iface), "%.15s", jobs[i].iface);
        out[i].gen = jobs[i].gen;
        out[i].control_failed = true;
        snprintf(out[i].error, sizeof(out[i].error), "which interfaces are the provider's is not known yet: %s",
                 firc_err_str(err));
    }
}

static void *worker(void *ud) {
    firc_ifcheck_t *c = ud;
    for (;;) {
        pthread_mutex_lock(&c->mu);
        while (!atomic_load(&c->stop) && c->n_q == 0 && c->n_gq == 0) { pthread_cond_wait(&c->cv, &c->mu); }
        if (atomic_load(&c->stop)) {
            pthread_mutex_unlock(&c->mu);
            return NULL;
        }
        firc_ifcheck_job_t jobs[FIRC_IFCHECK_BATCH];
        size_t n = c->n_q;
        if (n > 0) {
            memcpy(jobs, c->q, n * sizeof(jobs[0]));
            c->n_q = 0;
        }
        firc_ifcheck_group_job_t gjob;
        bool has_group = c->n_gq > 0;
        if (has_group) {
            gjob = c->gq[0];
            c->n_gq--;
            if (c->n_gq > 0) { memmove(c->gq, c->gq + 1, c->n_gq * sizeof(c->gq[0])); }
        }
        firc_ifcheck_settings_t s = c->s;
        pthread_mutex_unlock(&c->mu);

        if (n > 0) {
            batch_t *b = calloc(1, sizeof(*b));
            if (b != NULL) {
                b->c = c;
                b->n = n;
                bool wan[FIRC_IFCHECK_BATCH] = {false};
                bool split = s.wan_url[0] != '\0' && strcmp(s.wan_url, s.url) != 0;
                firc_err_t werr = split ? pick_wan(c, jobs, n, wan) : FIRC_OK;
                if (werr != FIRC_OK) {
                    hold_round(jobs, n, werr, b->r);
                } else {
                    firc_ifcheck_round(&s, jobs, n, split ? wan : NULL, &c->stop, b->r);
                }
                post(c, b);
            }
        }
        if (has_group && !atomic_load(&c->stop)) {
            group_batch_t *gb = calloc(1, sizeof(*gb));
            if (gb != NULL) {
                gb->c = c;
                firc_ifcheck_group_round(&s, &gjob, &c->stop, &gb->r);
                post_group(c, gb);
            }
        }
    }
}

firc_ifcheck_t *firc_ifcheck_new(firc_loop_t *loop, firc_ifcheck_done_fn done, void *ud) {
    firc_ifcheck_t *c = calloc(1, sizeof(*c));
    if (c == NULL) { return NULL; }
    if (pthread_mutex_init(&c->mu, NULL) != 0) {
        free(c);
        return NULL;
    }
    if (pthread_cond_init(&c->cv, NULL) != 0) {
        pthread_mutex_destroy(&c->mu);
        free(c);
        return NULL;
    }
    c->loop = loop;
    c->done = done;
    c->ud = ud;
    c->loop_thread = pthread_self();
    atomic_init(&c->stop, false);
    return c;
}

firc_err_t firc_ifcheck_start(firc_ifcheck_t *c) {
    if (c->started) { return FIRC_ERR_STATE; }
    if (pthread_create(&c->thread, NULL, worker, c) != 0) { return FIRC_ERR_SYS; }
    c->started = true;
    return FIRC_OK;
}

void firc_ifcheck_free(firc_ifcheck_t *c) {
    if (c == NULL) { return; }
    assert(pthread_equal(c->loop_thread, pthread_self()));
    if (!pthread_equal(c->loop_thread, pthread_self())) { return; }
    pthread_mutex_lock(&c->mu);
    atomic_store(&c->stop, true);
    pthread_cond_broadcast(&c->cv);
    pthread_mutex_unlock(&c->mu);
    if (c->started) { pthread_join(c->thread, NULL); }
    pthread_mutex_lock(&c->mu);
    c->dead = true;
    bool last = c->posted == 0;
    pthread_mutex_unlock(&c->mu);
    if (last) { destroy(c); }
}

void firc_ifcheck_configure(firc_ifcheck_t *c, const firc_ifcheck_settings_t *s) {
    pthread_mutex_lock(&c->mu);
    c->s = *s;
    pthread_mutex_unlock(&c->mu);
}

firc_err_t firc_ifcheck_queue(firc_ifcheck_t *c, const char *iface, uint32_t mark, uint32_t gen) {
    pthread_mutex_lock(&c->mu);
    if (c->n_q == FIRC_IFCHECK_BATCH) {
        pthread_mutex_unlock(&c->mu);
        return FIRC_ERR_LIMIT;
    }
    firc_ifcheck_job_t *j = &c->q[c->n_q++];
    snprintf(j->iface, sizeof(j->iface), "%s", iface);
    j->mark = mark;
    j->gen = gen;
    pthread_cond_signal(&c->cv);
    pthread_mutex_unlock(&c->mu);
    return FIRC_OK;
}

void firc_ifcheck_set_group_done(firc_ifcheck_t *c, firc_ifcheck_group_done_fn fn, void *ud) {
    if (c == NULL) { return; }
    pthread_mutex_lock(&c->mu);
    c->group_done = fn;
    c->group_ud = ud;
    pthread_mutex_unlock(&c->mu);
}

firc_err_t firc_ifcheck_queue_group(firc_ifcheck_t *c, const firc_ifcheck_group_job_t *job) {
    if (c == NULL || job == NULL) { return FIRC_ERR_INVAL; }
    pthread_mutex_lock(&c->mu);
    for (size_t i = 0; i < c->n_gq; i++) {
        if (firc_id_equal(c->gq[i].group_id, job->group_id)) {
            c->gq[i] = *job;
            pthread_cond_signal(&c->cv);
            pthread_mutex_unlock(&c->mu);
            return FIRC_OK;
        }
    }
    if (c->n_gq == FIRC_IFCHECK_GROUP_BATCH) {
        pthread_mutex_unlock(&c->mu);
        return FIRC_ERR_LIMIT;
    }
    c->gq[c->n_gq++] = *job;
    pthread_cond_signal(&c->cv);
    pthread_mutex_unlock(&c->mu);
    return FIRC_OK;
}
