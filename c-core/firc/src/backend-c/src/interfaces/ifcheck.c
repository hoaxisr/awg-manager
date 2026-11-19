#include "firc/ifcheck.h"

#include <arpa/inet.h>
#include <assert.h>
#include <errno.h>
#include <netinet/in.h>
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
#include "firc/keenetic_rci.h"
#include "firc/rand.h"
#include "firc/sub_fetch.h"

struct firc_ifcheck {
    firc_loop_t *loop;
    firc_ifcheck_done_fn done;
    void *ud;
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
    char seen[FIRC_IFCHECK_BATCH][16];
    bool seen_wan[FIRC_IFCHECK_BATCH];
    size_t n_seen, oldest;
};

typedef struct batch {
    firc_ifcheck_t *c;
    size_t n;
    firc_ifcheck_result_t r[FIRC_IFCHECK_BATCH];
} batch_t;

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

bool firc_ifcheck_once(const char *url, const char *addr, uint32_t mark, uint32_t timeout_s, _Atomic bool *stop,
                       long *status, char *error, size_t cap) {
    char host[256], pin[320], curl_err[CURL_ERROR_SIZE] = "";
    uint16_t port = 0;
    *status = 0;
    if (!url_parts(url, host, sizeof(host), &port)) {
        snprintf(error, cap, "not a check URL");
        return false;
    }
    firc_sub_fetch_opts_t o = {.mark = mark,
                               .timeout_s = (long)timeout_s,
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
    if (err == FIRC_OK && *status == 204) {
        error[0] = '\0';
        return true;
    }
    if (err == FIRC_OK || err == FIRC_ERR_PROTO) {
        snprintf(error, cap, "answered %ld, not 204", *status);
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

firc_err_t firc_ifcheck_resolve(const char *server, uint16_t port, const char *url, uint32_t timeout_s, char *addr,
                                size_t cap, _Atomic bool *stop) {
    char host[256];
    uint16_t hp = 0;
    if (!url_parts(url, host, sizeof(host), &hp)) { return FIRC_ERR_INVAL; }
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
    struct timeval tv = {1, 0};
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    firc_err_t err = FIRC_ERR_IO;
    if (connect(fd, (struct sockaddr *)&ss, sl) == 0 && send(fd, q, at, 0) == (ssize_t)at) {
        uint8_t a[1500];
        uint32_t waited = 0;
        for (int tries = 0; tries < 8; tries++) {
            ssize_t got = recv(fd, a, sizeof(a), 0);
            if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK) && ++waited < timeout_s &&
                (stop == NULL || !atomic_load(stop))) {
                tries--;
                continue;
            }
            if (got < 0) {
                err = errno == EAGAIN || errno == EWOULDBLOCK ? FIRC_ERR_TIMEOUT : firc_err_from_errno(errno);
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

static bool unpost(firc_ifcheck_t *c, bool *last) {
    pthread_mutex_lock(&c->mu);
    c->posted--;
    bool quiet = c->dead || atomic_load(&c->stop);
    *last = c->dead && c->posted == 0;
    pthread_mutex_unlock(&c->mu);
    return quiet;
}

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
    if (!unpost(c, &last)) { c->done(c->ud, b->r, b->n); }
    free(b);
    if (last) { destroy(c); }
}

static void post(firc_ifcheck_t *c, batch_t *b) {
    pthread_mutex_lock(&c->mu);
    c->posted++;
    pthread_mutex_unlock(&c->mu);
    while (firc_loop_post_with_drop(c->loop, on_batch, b, drop_batch) != FIRC_OK) {
        if (atomic_load(&c->stop)) {
            pthread_mutex_lock(&c->mu);
            c->posted--;
            pthread_mutex_unlock(&c->mu);
            free(b);
            return;
        }
        struct timespec ts = {0, 50 * 1000000L};
        nanosleep(&ts, NULL);
    }
}

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
        while (!atomic_load(&c->stop) && c->n_q == 0) { pthread_cond_wait(&c->cv, &c->mu); }
        if (atomic_load(&c->stop)) {
            pthread_mutex_unlock(&c->mu);
            return NULL;
        }
        firc_ifcheck_job_t jobs[FIRC_IFCHECK_BATCH];
        size_t n = c->n_q;
        memcpy(jobs, c->q, n * sizeof(jobs[0]));
        c->n_q = 0;
        firc_ifcheck_settings_t s = c->s;
        pthread_mutex_unlock(&c->mu);
        batch_t *b = calloc(1, sizeof(*b));
        if (b == NULL) { continue; }
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
