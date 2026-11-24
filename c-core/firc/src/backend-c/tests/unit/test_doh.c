#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */
#include "greatest.h"

#include <arpa/inet.h>
#include <curl/curl.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <spawn.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#include "firc/doh.h"
#include "firc/fakeip.h"
#include "firc/loop.h"
#include "firc/sub_fetch.h"

#define MAX_CONNS 8

typedef struct conn {
    pthread_t t;
    int fd;
    int idx;
    struct srv *s;
} conn_t;

typedef struct srv {
    int lfd;
    uint16_t port;
    pthread_t acceptor;
    _Atomic bool stop;
    _Atomic int accepts;
    _Atomic int replies;
    _Atomic bool h2;
    _Atomic int asked;
    _Atomic int silent_conns;
    _Atomic int swallow;
    int status;
    const char *ctype;
    char location[64];
    _Atomic int delay_ms;
    int accept_delay_ms;
    uint8_t reply[65536];
    _Atomic size_t reply_len;
    pthread_mutex_t mu;
    char method[8];
    char req_ctype[64];
    char req_accept[64];
    uint8_t body[512];
    size_t body_len;
    int n_conns;
    conn_t conns[MAX_CONNS];
} srv_t;

static void nap_ms(int ms)
{
    struct timespec t = {ms / 1000, (long)(ms % 1000) * 1000000L};
    nanosleep(&t, NULL);
}

static bool nap_unless_stop(srv_t *s, int ms)
{
    for (int i = 0; i < ms / 10; i++) {
        if (atomic_load(&s->stop)) {
            return false;
        }
        nap_ms(10);
    }
    return !atomic_load(&s->stop);
}

static void header_value(const char *hdrs, const char *name, char *out, size_t cap)
{
    out[0] = '\0';
    size_t nl = strlen(name);
    for (const char *p = hdrs; p != NULL && *p != '\0';) {
        if (strncasecmp(p, name, nl) == 0 && p[nl] == ':') {
            const char *v = p + nl + 1;
            while (*v == ' ') {
                v++;
            }
            size_t n = strcspn(v, "\r\n");
            snprintf(out, cap, "%.*s", (int)n, v);
            return;
        }
        p = strstr(p, "\r\n");
        if (p != NULL) {
            p += 2;
        }
    }
}

static bool read_some(srv_t *s, int fd, char *buf, size_t *len, size_t cap)
{
    while (!atomic_load(&s->stop)) {
        struct pollfd pfd = {.fd = fd, .events = POLLIN};
        int r = poll(&pfd, 1, 20);
        if (r < 0) {
            return false;
        }
        if (r == 0) {
            continue;
        }
        ssize_t n = recv(fd, buf + *len, cap - *len, 0);
        if (n <= 0) {
            return false;
        }
        *len += (size_t)n;
        return true;
    }
    return false;
}

static bool read_upto(srv_t *s, int fd, char *buf, size_t *len, size_t cap, size_t need)
{
    while (*len < need) {
        if (need > cap || !read_some(s, fd, buf, len, cap)) { return false; }
    }
    return true;
}

static bool send_frame(int fd, uint8_t type, uint8_t flags, const uint8_t stream[4], const void *body, size_t n)
{
    uint8_t f[256] = {0, 0, (uint8_t)n, type, flags};
    memcpy(f + 5, stream, 4);
    memcpy(f + 9, body, n);
    return send(fd, f, 9 + n, MSG_NOSIGNAL) == (ssize_t)(9 + n);
}

/* HTTP/2 with prior knowledge, answering every request alike, so no header of one is decoded. */
static void *h2_main(void *ud)
{
    conn_t *c = ud;
    srv_t *s = c->s;
    static const uint8_t zero[4] = {0};
    static const uint8_t hdrs[] = "\x88\x0f\x10\x17" "application/dns-message";
    char buf[4096];
    size_t len = 0;
    if (!read_upto(s, c->fd, buf, &len, sizeof buf, 24)) { return NULL; }
    memmove(buf, buf + 24, len -= 24);
    if (!send_frame(c->fd, 4, 0, zero, zero, 0)) { return NULL; }
    for (;;) {
        if (!read_upto(s, c->fd, buf, &len, sizeof buf, 9)) { return NULL; }
        size_t need = 9 + (size_t)((uint8_t)buf[1] << 8 | (uint8_t)buf[2]);
        if (!read_upto(s, c->fd, buf, &len, sizeof buf, need)) { return NULL; }
        uint8_t type = (uint8_t)buf[3], flags = (uint8_t)buf[4], stream[4];
        memcpy(stream, buf + 5, 4);
        memmove(buf, buf + need, len -= need);
        if (type == 4 && (flags & 1) == 0 && !send_frame(c->fd, 4, 1, zero, zero, 0)) { return NULL; }
        if (type > 1 || (flags & 1) == 0) { continue; }
        atomic_fetch_add(&s->asked, 1);
        if (c->idx < atomic_load(&s->silent_conns)) { continue; }
        if (atomic_load(&s->swallow) > 0) {
            atomic_fetch_sub(&s->swallow, 1);
            continue;
        }
        if (!nap_unless_stop(s, s->delay_ms) || !send_frame(c->fd, 1, 4, stream, hdrs, sizeof hdrs - 1) ||
            !send_frame(c->fd, 0, 1, stream, s->reply, s->reply_len)) {
            return NULL;
        }
        atomic_fetch_add(&s->replies, 1);
    }
}

static void *conn_main(void *ud)
{
    conn_t *c = ud;
    srv_t *s = c->s;
    if (s->h2) { return h2_main(c); }
    char buf[4096];
    size_t len = 0;
    for (;;) {
        char *end;
        while ((end = memmem(buf, len, "\r\n\r\n", 4)) == NULL) {
            if (len == sizeof buf - 1 || !read_some(s, c->fd, buf, &len, sizeof buf - 1)) {
                return NULL;
            }
        }
        size_t head = (size_t)(end - buf) + 4;
        char hdrs[2048];
        snprintf(hdrs, sizeof hdrs, "%.*s", (int)head, buf);
        char cl[16];
        header_value(hdrs, "Content-Length", cl, sizeof cl);
        size_t body = (size_t)strtoul(cl, NULL, 10);
        while (len < head + body) {
            if (!read_some(s, c->fd, buf, &len, sizeof buf - 1)) {
                return NULL;
            }
        }
        pthread_mutex_lock(&s->mu);
        snprintf(s->method, sizeof s->method, "%.*s", (int)strcspn(hdrs, " "), hdrs);
        header_value(hdrs, "Content-Type", s->req_ctype, sizeof s->req_ctype);
        header_value(hdrs, "Accept", s->req_accept, sizeof s->req_accept);
        s->body_len = body < sizeof s->body ? body : sizeof s->body;
        memcpy(s->body, buf + head, s->body_len);
        pthread_mutex_unlock(&s->mu);
        memmove(buf, buf + head + body, len - head - body);
        len -= head + body;
        if (!nap_unless_stop(s, s->delay_ms)) {
            return NULL;
        }
        char out[512];
        size_t reply_len = atomic_load(&s->reply_len);
        pthread_mutex_lock(&s->mu);
        int n = snprintf(out, sizeof out, "HTTP/1.1 %d X\r\n%s%s%sContent-Type: %s\r\nContent-Length: %zu\r\n\r\n",
                         s->status, s->location[0] != '\0' ? "Location: " : "", s->location,
                         s->location[0] != '\0' ? "\r\n" : "", s->ctype, reply_len);
        pthread_mutex_unlock(&s->mu);
        if (send(c->fd, out, (size_t)n, MSG_NOSIGNAL | MSG_MORE) < 0 ||
            send(c->fd, s->reply, reply_len, MSG_NOSIGNAL) < 0) {
            return NULL;
        }
        atomic_fetch_add(&s->replies, 1);
    }
}

static void *accept_main(void *ud)
{
    srv_t *s = ud;
    while (!atomic_load(&s->stop)) {
        struct pollfd pfd = {.fd = s->lfd, .events = POLLIN};
        if (poll(&pfd, 1, 20) <= 0) {
            continue;
        }
        if (!nap_unless_stop(s, s->accept_delay_ms)) {
            break;
        }
        int fd = accept4(s->lfd, NULL, NULL, SOCK_CLOEXEC);
        if (fd < 0) {
            continue;
        }
        atomic_fetch_add(&s->accepts, 1);
        pthread_mutex_lock(&s->mu);
        if (s->n_conns == MAX_CONNS) {
            pthread_mutex_unlock(&s->mu);
            close(fd);
            continue;
        }
        conn_t *c = &s->conns[s->n_conns];
        c->idx = s->n_conns++;
        c->fd = fd;
        c->s = s;
        pthread_create(&c->t, NULL, conn_main, c);
        pthread_mutex_unlock(&s->mu);
    }
    return NULL;
}

static const uint8_t k_query[] = {0xab, 0xcd, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'c', 'o', 'm', 0, 0, 1, 0, 1};
static const uint8_t k_reply[] = {0x00, 0x00, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'c', 'o', 'm', 0, 0, 1, 0, 1};

static bool srv_start(srv_t *s, int status, const char *ctype, int delay_ms, int accept_delay_ms)
{
    memset(s, 0, sizeof *s);
    pthread_mutex_init(&s->mu, NULL);
    s->status = status;
    s->ctype = ctype;
    s->delay_ms = delay_ms;
    s->accept_delay_ms = accept_delay_ms;
    memcpy(s->reply, k_reply, sizeof k_reply);
    s->reply_len = sizeof k_reply;
    s->lfd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    struct sockaddr_in a = {.sin_family = AF_INET};
    inet_pton(AF_INET, "127.0.0.1", &a.sin_addr);
    socklen_t al = sizeof a;
    if (s->lfd < 0 || bind(s->lfd, (struct sockaddr *)&a, sizeof a) != 0 || listen(s->lfd, 8) != 0 ||
        getsockname(s->lfd, (struct sockaddr *)&a, &al) != 0) {
        return false;
    }
    s->port = ntohs(a.sin_port);
    return pthread_create(&s->acceptor, NULL, accept_main, s) == 0;
}

static void srv_stop(srv_t *s)
{
    atomic_store(&s->stop, true);
    pthread_join(s->acceptor, NULL);
    for (int i = 0; i < s->n_conns; i++) {
        shutdown(s->conns[i].fd, SHUT_RDWR);
        pthread_join(s->conns[i].t, NULL);
        close(s->conns[i].fd);
    }
    close(s->lfd);
    pthread_mutex_destroy(&s->mu);
}

typedef struct waiter {
    bool (*cond)(void *);
    void *ud;
    bool met;
} waiter_t;

static void tick(firc_loop_t *loop, void *ud)
{
    waiter_t *w = ud;
    if (w->cond != NULL && w->cond(w->ud)) {
        w->met = true;
        firc_loop_stop(loop);
    }
}

static void deadline(firc_loop_t *loop, void *ud)
{
    (void)ud;
    firc_loop_stop(loop);
}

static bool run_until(firc_loop_t *loop, bool (*cond)(void *), void *ud, int ms)
{
    waiter_t w = {.cond = cond, .ud = ud};
    if (cond != NULL && cond(ud)) {
        return true;
    }
    int t1 = 0, t2 = 0;
    firc_loop_add_timer(loop, 5, 5, tick, &w, &t1);
    firc_loop_add_timer(loop, (uint64_t)ms, 0, deadline, NULL, &t2);
    firc_loop_run(loop);
    firc_loop_del_timer(loop, t1);
    firc_loop_del_timer(loop, t2);
    return w.met;
}

static void run_for(firc_loop_t *loop, int ms) { (void)run_until(loop, NULL, NULL, ms); }

typedef struct got {
    int calls;
    bool ok;
    uint8_t resp[512];
    size_t len, full_len;
    int replies_at_call;
    srv_t *s;
} got_t;

static void on_done(void *ud, const uint8_t *resp, size_t len, bool ok)
{
    got_t *g = ud;
    g->calls++;
    g->ok = ok;
    g->full_len = len;
    g->len = len < sizeof g->resp ? len : sizeof g->resp;
    if (resp != NULL) {
        memcpy(g->resp, resp, g->len);
    }
    g->replies_at_call = g->s != NULL ? atomic_load(&g->s->replies) : -1;
}

static bool called(void *ud) { return ((got_t *)ud)->calls > 0; }

typedef struct pair {
    got_t *a, *b;
} pair_t;

static bool both_called(void *ud)
{
    pair_t *p = ud;
    return p->a->calls > 0 && p->b->calls > 0;
}

static bool replied_once(void *ud) { return atomic_load(&((srv_t *)ud)->replies) >= 1; }

static bool wait_replied(srv_t *s, int ms)
{
    for (int i = 0; i < ms / 10; i++) {
        if (atomic_load(&s->replies) >= 1) {
            return true;
        }
        nap_ms(10);
    }
    return false;
}

CURLcode __real_curl_easy_setopt(CURL *e, CURLoption opt, ...);
CURLcode __wrap_curl_easy_setopt(CURL *e, CURLoption opt, ...);
CURLMcode __real_curl_multi_poll(CURLM *m, struct curl_waitfd fds[], unsigned n, int ms, int *ret);
CURLMcode __wrap_curl_multi_poll(CURLM *m, struct curl_waitfd fds[], unsigned n, int ms, int *ret);

static _Atomic bool g_h2c;
static _Atomic long g_timeout_ms;
static const char *_Atomic g_cafile;
static _Atomic int g_poll_ms;

/* What only a test needs from libcurl: HTTP/2 without TLS, a short transfer cap, a CA of the test's own. */
CURLcode __wrap_curl_easy_setopt(CURL *e, CURLoption opt, ...)
{
    va_list ap;
    va_start(ap, opt);
    CURLcode rc;
    if (opt >= CURLOPTTYPE_OFF_T) {
        rc = __real_curl_easy_setopt(e, opt, va_arg(ap, curl_off_t));
    } else if (opt >= CURLOPTTYPE_OBJECTPOINT) {
        rc = __real_curl_easy_setopt(e, opt, va_arg(ap, void *));
    } else {
        long v = va_arg(ap, long);
        long cap = atomic_load(&g_timeout_ms);
        rc = __real_curl_easy_setopt(e, opt, opt == CURLOPT_TIMEOUT_MS && cap > 0 ? cap : v);
    }
    va_end(ap);
    if (opt == CURLOPT_URL && atomic_load(&g_h2c)) {
        (void)__real_curl_easy_setopt(e, CURLOPT_HTTP_VERSION, (long)CURL_HTTP_VERSION_2_PRIOR_KNOWLEDGE);
    }
    const char *ca = atomic_load(&g_cafile);
    if (opt == CURLOPT_URL && ca != NULL) { (void)__real_curl_easy_setopt(e, CURLOPT_CAINFO, ca); }
    return rc;
}

CURLMcode __wrap_curl_multi_poll(CURLM *m, struct curl_waitfd fds[], unsigned n, int ms, int *ret)
{
    atomic_store(&g_poll_ms, ms);
    return __real_curl_multi_poll(m, fds, n, ms, ret);
}

typedef struct env {
    srv_t s;
    firc_loop_t *loop;
    firc_doh_t *d;
    uint16_t id;
} env_t;

static bool env_up_with(env_t *e, int status, const char *ctype, int delay_ms, int accept_delay_ms, bool http,
                        bool loopback, const firc_fakeip_t *pool)
{
    memset(e, 0, sizeof *e);
    atomic_store(&g_h2c, false);
    atomic_store(&g_timeout_ms, 0);
    atomic_store(&g_cafile, NULL);
    if (!srv_start(&e->s, status, ctype, delay_ms, accept_delay_ms) || firc_loop_create(&e->loop) != FIRC_OK) {
        return false;
    }
    e->d = firc_doh_new(e->loop);
    if (e->d == NULL) {
        return false;
    }
    firc_doh_allow_for_test(e->d, http, loopback);
    if (pool != NULL) {
        firc_doh_set_pool(e->d, pool);
    }
    if (firc_doh_start(e->d) != FIRC_OK) {
        return false;
    }
    char url[64];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/dns-query", e->s.port);
    e->id = firc_doh_intern(e->d, url);
    return e->id != 0;
}

static bool env_up(env_t *e, int status, const char *ctype, int delay_ms, int accept_delay_ms)
{
    return env_up_with(e, status, ctype, delay_ms, accept_delay_ms, true, true, NULL);
}

static void env_down(env_t *e)
{
    firc_doh_free(e->d);
    firc_loop_destroy(e->loop);
    srv_stop(&e->s);
}

static firc_err_t ask(env_t *e, uint32_t mark, uint32_t timeout_ms, got_t *g, uint64_t *ticket)
{
    g->s = &e->s;
    uint8_t q[sizeof k_query];
    memcpy(q, k_query, sizeof q);
    firc_err_t err = firc_doh_query(e->d, e->id, mark, q, sizeof q, timeout_ms, on_done, g, ticket);
    memset(q, 0xee, sizeof q);
    return err;
}

TEST a_query_is_posted_as_rfc8484(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t g = {0};
    uint64_t t = 0;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, &t));
    ASSERT(t != 0);
    ASSERT(run_until(e.loop, called, &g, 5000));
    pthread_mutex_lock(&e.s.mu);
    ASSERT_STR_EQ("POST", e.s.method);
    ASSERT_STR_EQ("application/dns-message", e.s.req_ctype);
    ASSERT_STR_EQ("application/dns-message", e.s.req_accept);
    uint8_t want_body[sizeof k_query];
    memcpy(want_body, k_query, sizeof want_body);
    want_body[0] = 0;
    want_body[1] = 0;
    ASSERT_EQ(sizeof want_body, e.s.body_len);
    ASSERT_MEM_EQ(want_body, e.s.body, sizeof want_body);
    pthread_mutex_unlock(&e.s.mu);
    ASSERT_EQ(1, g.calls);
    ASSERT(g.ok);
    uint8_t want[sizeof k_reply];
    memcpy(want, k_reply, sizeof want);
    want[0] = 0xab;
    want[1] = 0xcd;
    ASSERT_EQ(sizeof want, g.len);
    ASSERT_MEM_EQ(want, g.resp, sizeof want);
    env_down(&e);
    PASS();
}

/* Catches: the shipped client posting a query in the clear. */
TEST plain_http_is_never_spoken(void)
{
    env_t e;
    ASSERT(env_up_with(&e, 200, "application/dns-message", 0, 0, false, true, NULL));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_FALSE(g.ok);
    ASSERT_EQ(0, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

/* Catches: the address check left to the URL, so a name resolving to the router itself is connected to. */
TEST a_loopback_peer_is_never_connected(void)
{
    env_t e;
    ASSERT(env_up_with(&e, 200, "application/dns-message", 0, 0, false, false, NULL));
    char url[64];
    snprintf(url, sizeof url, "https://localhost:%u/dns-query", e.s.port);
    e.id = firc_doh_intern(e.d, url);
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_FALSE(g.ok);
    ASSERT_EQ(0, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

/* Catches: a name that resolved to one of firc's own fakes connected to, also as ::ffff:a.b.c.d. */
TEST a_peer_in_the_pool_is_never_connected(void)
{
    firc_fakeip_cfg_t c;
    memset(&c, 0, sizeof c);
    c.v4.base = (firc_ip_t){.len = 4, .b = {127, 0, 0, 0}};
    c.v4.pool_cidr = 15;
    c.v4.chunk_cidr = 24;
    c.v6.base = (firc_ip_t){.len = 16, .b = {0xfd, 0, 0x12, 0x34}};
    c.v6.pool_cidr = 48;
    c.v6.chunk_cidr = 64;
    c.idle_secs = 3600;
    c.clamp_secs = 60;
    c.max_names = 16;
    firc_fakeip_t *pool = NULL;
    ASSERT_EQ(FIRC_OK, firc_fakeip_new(&c, &pool));
    env_t e;
    ASSERT(env_up_with(&e, 200, "application/dns-message", 0, 0, true, true, pool));
    firc_fakeip_free(pool);
    got_t g = {0}, mapped = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_FALSE(g.ok);
    char url[64];
    snprintf(url, sizeof url, "http://[::ffff:127.0.0.1]:%u/dns-query", e.s.port);
    e.id = firc_doh_intern(e.d, url);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &mapped, NULL));
    ASSERT(run_until(e.loop, called, &mapped, 5000));
    ASSERT_FALSE(mapped.ok);
    ASSERT_EQ(0, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

/* Catches: a redirect followed, so the query goes where no one configured it to go. */
TEST a_redirect_is_never_followed(void)
{
    env_t e;
    srv_t to;
    ASSERT(srv_start(&to, 200, "application/dns-message", 0, 0));
    ASSERT(env_up(&e, 307, "application/dns-message", 0, 0));
    pthread_mutex_lock(&e.s.mu);
    snprintf(e.s.location, sizeof e.s.location, "http://127.0.0.1:%u/dns-query", to.port);
    pthread_mutex_unlock(&e.s.mu);
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_FALSE(g.ok);
    ASSERT_EQ(0, atomic_load(&to.accepts));
    env_down(&e);
    srv_stop(&to);
    PASS();
}

/* Catches: a proxy of the environment taking the query past the group's mark. */
TEST an_environment_proxy_is_never_used(void)
{
    env_t e;
    srv_t proxy;
    ASSERT(srv_start(&proxy, 200, "application/dns-message", 0, 0));
    char url[64];
    snprintf(url, sizeof url, "http://127.0.0.1:%u", proxy.port);
    setenv("http_proxy", url, 1);
    unsetenv("no_proxy");
    unsetenv("NO_PROXY");
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT(g.ok);
    ASSERT_EQ(0, atomic_load(&proxy.accepts));
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    env_down(&e);
    unsetenv("http_proxy");
    srv_stop(&proxy);
    PASS();
}

TEST an_answer_longer_than_a_dns_message_is_a_failure(void)
{
    for (size_t n = 65535; n <= 65536; n++) {
        env_t e;
        ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
        e.s.reply_len = n;
        got_t g = {0};
        ASSERT_EQ(FIRC_OK, ask(&e, 0, 5000, &g, NULL));
        ASSERT(run_until(e.loop, called, &g, 8000));
        ASSERT_EQ(n == 65535, g.ok);
        ASSERT_EQ(n == 65535 ? n : 0, g.full_len);
        env_down(&e);
    }
    PASS();
}

#define TLS_OUT 4096

typedef struct tls_srv {
    pid_t pid;
    int out;
    uint16_t port;
    char dir[32], cert[48], key[48];
} tls_srv_t;

static pid_t run_openssl(char *const argv[], int io)
{
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_adddup2(&fa, io, 0);
    posix_spawn_file_actions_adddup2(&fa, io, 1);
    posix_spawn_file_actions_addopen(&fa, 2, "/dev/null", O_WRONLY, 0);
    pid_t pid = -1;
    int rc = posix_spawnp(&pid, "openssl", &fa, NULL, argv, environ);
    posix_spawn_file_actions_destroy(&fa);
    return rc == 0 ? pid : -1;
}

static bool tls_accepts(uint16_t port)
{
    struct sockaddr_in a = {.sin_family = AF_INET};
    a.sin_port = htons(port);
    inet_pton(AF_INET, "127.0.0.1", &a.sin_addr);
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    bool up = fd >= 0 && connect(fd, (struct sockaddr *)&a, sizeof a) == 0;
    if (fd >= 0) { close(fd); }
    return up;
}

static void tls_stop(tls_srv_t *t)
{
    if (t->pid > 0) {
        kill(t->pid, SIGKILL);
        waitpid(t->pid, NULL, 0);
    }
    if (t->out >= 0) { close(t->out); }
    unlink(t->cert);
    unlink(t->key);
    rmdir(t->dir);
}

/* openssl s_server with a self-signed certificate for that name; what a client sends it comes out of t->out. Its stdin
   has to stay open: at EOF it hangs up on the client. */
static bool tls_start(tls_srv_t *t, const char *san)
{
    *t = (tls_srv_t){.pid = -1, .out = -1, .dir = "/tmp/firc-doh-XXXXXX"};
    if (mkdtemp(t->dir) == NULL) { return false; }
    snprintf(t->cert, sizeof t->cert, "%s/cert.pem", t->dir);
    snprintf(t->key, sizeof t->key, "%s/key.pem", t->dir);
    char ext[64], accept[32];
    snprintf(ext, sizeof ext, "subjectAltName=%s", san);
    char *gen[] = {"openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
                   "-subj", "/CN=firc-test", "-addext", ext, "-days", "2", "-keyout", t->key, "-out", t->cert, NULL};
    int p[2];
    if (socketpair(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0, p) != 0) { return false; }
    t->out = p[0];
    pid_t pid = run_openssl(gen, p[1]);
    int status = 1;
    if (pid < 0 || waitpid(pid, &status, 0) != pid || status != 0) { return false; }
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    struct sockaddr_in a = {.sin_family = AF_INET};
    inet_pton(AF_INET, "127.0.0.1", &a.sin_addr);
    socklen_t al = sizeof a;
    if (fd < 0 || bind(fd, (struct sockaddr *)&a, sizeof a) != 0 || getsockname(fd, (struct sockaddr *)&a, &al) != 0) {
        return false;
    }
    close(fd);
    t->port = ntohs(a.sin_port);
    snprintf(accept, sizeof accept, "127.0.0.1:%u", t->port);
    char *srv[] = {"openssl", "s_server", "-quiet", "-accept", accept, "-cert", t->cert, "-key", t->key, NULL};
    t->pid = run_openssl(srv, p[1]);
    close(p[1]);
    for (int i = 0; t->pid > 0 && i < 500; i++) {
        if (tls_accepts(t->port)) { return true; }
        nap_ms(10);
    }
    return false;
}

static bool tls_got_post(tls_srv_t *t, int ms)
{
    char buf[TLS_OUT] = "";
    size_t len = 0;
    for (int i = 0; i <= ms / 10; i++) {
        ssize_t n = recv(t->out, buf + len, sizeof buf - 1 - len, MSG_DONTWAIT);
        if (n > 0) { len += (size_t)n; }
        buf[len] = '\0';
        if (strstr(buf, "POST /dns-query") != NULL) { return true; }
        nap_ms(10);
    }
    return false;
}

/* Catches: SSL_VERIFYPEER or SSL_VERIFYHOST off. The first case proves the server reachable once it is trusted. */
TEST a_certificate_is_checked_and_its_name_too(void)
{
    static const struct {
        const char *san;
        bool trusted, post;
    } cases[] = {
        {"IP:127.0.0.1", true, true},
        {"IP:127.0.0.1", false, false},
        {"DNS:other.example", true, false},
    };
    for (size_t i = 0; i < sizeof cases / sizeof cases[0]; i++) {
        tls_srv_t t;
        env_t e;
        ASSERT(env_up_with(&e, 200, "application/dns-message", 0, 0, false, true, NULL));
        bool up = tls_start(&t, cases[i].san);
        atomic_store(&g_timeout_ms, 1000);
        atomic_store(&g_cafile, cases[i].trusted ? t.cert : NULL);
        char url[64];
        snprintf(url, sizeof url, "https://127.0.0.1:%u/dns-query", t.port);
        e.id = firc_doh_intern(e.d, url);
        got_t g = {0};
        bool asked = up && ask(&e, 0, 2000, &g, NULL) == FIRC_OK && run_until(e.loop, called, &g, 5000);
        bool post = asked && tls_got_post(&t, 300);
        env_down(&e);
        tls_stop(&t);
        ASSERTm("no openssl s_server", up);
        ASSERT(asked);
        ASSERT_EQ(cases[i].post, post);
    }
    PASS();
}

TEST a_non_200_is_a_failure(void)
{
    env_t e;
    ASSERT(env_up(&e, 503, "application/dns-message", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_EQ(1, g.calls);
    ASSERT_FALSE(g.ok);
    env_down(&e);
    PASS();
}

TEST a_wrong_content_type_is_a_failure(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "text/plain", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_EQ(1, g.calls);
    ASSERT_FALSE(g.ok);
    env_down(&e);
    PASS();
}

TEST a_slow_server_times_out(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 1000, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 200, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_EQ(1, g.calls);
    ASSERT_FALSE(g.ok);
    ASSERT_EQ(0, g.replies_at_call);
    ASSERT(run_until(e.loop, replied_once, &e.s, 5000));
    run_for(e.loop, 200);
    ASSERT_EQ(1, g.calls);
    env_down(&e);
    PASS();
}

TEST two_sequential_queries_share_a_connection(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t a = {0}, b = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &a, NULL));
    ASSERT(run_until(e.loop, called, &a, 5000));
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT(a.ok && b.ok);
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

TEST two_concurrent_queries_over_http1_open_two_connections(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 300, 0));
    got_t w = {0}, c = {0}, d = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &w, NULL));
    ASSERT(run_until(e.loop, called, &w, 5000));
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &c, NULL));
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &d, NULL));
    pair_t p = {&c, &d};
    ASSERT(run_until(e.loop, both_called, &p, 5000));
    ASSERT(c.ok && d.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

TEST a_cancelled_query_never_calls_back(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 50, 0));
    got_t a = {0}, b = {0};
    uint64_t ta = 0, tb = 0;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &a, &ta));
    firc_doh_cancel(e.d, ta);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &b, &tb));
    ASSERT(ta != tb);
    ASSERT(run_until(e.loop, called, &b, 5000));
    run_for(e.loop, 100);
    ASSERT_EQ(2, atomic_load(&e.s.replies));
    ASSERT_EQ(0, a.calls);
    ASSERT_EQ(1, b.calls);
    ASSERT(b.ok);
    env_down(&e);
    PASS();
}

TEST a_cancelled_transfer_still_warms_its_connection(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 1000));
    got_t a = {0}, b = {0};
    uint64_t ta = 0;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 5000, &a, &ta));
    run_for(e.loop, 100);
    firc_doh_cancel(e.d, ta);
    run_for(e.loop, 1400);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 5000, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT(b.ok);
    ASSERT_EQ(0, a.calls);
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

static pthread_mutex_t g_set_mu = PTHREAD_MUTEX_INITIALIZER;
static int g_set_calls;
static int g_set_fd;
static uint32_t g_set_mark;
static unsigned g_set_dead_ms;

static int record_mark(int fd, int level, int name, const void *val, socklen_t len)
{
    if (level != SOL_SOCKET || name != SO_MARK || len != sizeof(uint32_t)) {
        return setsockopt(fd, level, name, val, len);
    }
    pthread_mutex_lock(&g_set_mu);
    g_set_calls++;
    g_set_fd = fd;
    memcpy(&g_set_mark, val, sizeof g_set_mark);
    socklen_t dl = sizeof g_set_dead_ms;
    (void)getsockopt(fd, IPPROTO_TCP, TCP_USER_TIMEOUT, &g_set_dead_ms, &dl);
    pthread_mutex_unlock(&g_set_mu);
    return 0;
}

TEST different_marks_never_share_a_connection(void)
{
    g_set_calls = 0;
    firc_sub_fetch_set_setsockopt_for_test(record_mark);
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 200, 0));
    got_t a = {0}, b = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &a, NULL));
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 3000, &b, NULL));
    pair_t p = {&a, &b};
    ASSERT(run_until(e.loop, both_called, &p, 5000));
    ASSERT(a.ok && b.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    pthread_mutex_lock(&g_set_mu);
    ASSERT_EQ(1, g_set_calls);
    ASSERT_EQ(7u, g_set_mark);
    ASSERT(g_set_fd >= 0);
    ASSERT_EQ(FIRC_DOH_DETACH_MS, g_set_dead_ms);
    pthread_mutex_unlock(&g_set_mu);
    env_down(&e);
    firc_sub_fetch_set_setsockopt_for_test(NULL);
    PASS();
}

TEST a_forgotten_mark_opens_a_new_connection(void)
{
    firc_sub_fetch_set_setsockopt_for_test(record_mark);
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t a = {0}, b = {0}, c = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 2000, &a, NULL));
    ASSERT(run_until(e.loop, called, &a, 5000));
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 2000, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    firc_doh_forget_mark(e.d, 7);
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 2000, &c, NULL));
    ASSERT(run_until(e.loop, called, &c, 5000));
    ASSERT(a.ok && b.ok && c.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    env_down(&e);
    firc_sub_fetch_set_setsockopt_for_test(NULL);
    PASS();
}

TEST a_mark_forgotten_mid_transfer_drops_its_share_after(void)
{
    firc_sub_fetch_set_setsockopt_for_test(record_mark);
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 300, 0));
    got_t a = {0}, b = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 3000, &a, NULL));
    run_for(e.loop, 50);
    firc_doh_forget_mark(e.d, 7);
    ASSERT(run_until(e.loop, called, &a, 5000));
    ASSERT(a.ok);
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 3000, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT(b.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    env_down(&e);
    firc_sub_fetch_set_setsockopt_for_test(NULL);
    PASS();
}

static bool h2_up(env_t *e, int delay_ms)
{
    bool up = env_up(e, 200, "application/dns-message", delay_ms, 0);
    atomic_store(&g_h2c, true);
    e->s.h2 = true;
    return up;
}

static bool asked_three(void *ud) { return atomic_load(&((srv_t *)ud)->asked) == 3; }

/* Catches: a query put on the connection of a mark already forgotten, while another still runs on it. */
TEST a_forgotten_mark_is_not_reused_by_a_query_beside_one_in_flight(void)
{
    firc_sub_fetch_set_setsockopt_for_test(record_mark);
    env_t e;
    ASSERT(h2_up(&e, 300));
    got_t a = {0}, b = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 3000, &a, NULL));
    run_for(e.loop, 100);
    firc_doh_forget_mark(e.d, 7);
    ASSERT_EQ(FIRC_OK, ask(&e, 7, 3000, &b, NULL));
    pair_t p = {&a, &b};
    ASSERT(run_until(e.loop, both_called, &p, 5000));
    ASSERT(a.ok && b.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    env_down(&e);
    firc_sub_fetch_set_setsockopt_for_test(NULL);
    PASS();
}

/* Catches: an HTTP/2 connection that went silent (the WAN address changed under it) kept for the next query. */
TEST a_silent_connection_is_dropped_after_a_timeout(void)
{
    env_t e;
    srv_t other;
    ASSERT(srv_start(&other, 200, "application/dns-message", 0, 0));
    other.h2 = true;
    ASSERT(h2_up(&e, 0));
    char url[64];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/dns-query", other.port);
    uint16_t id = e.id, other_id = firc_doh_intern(e.d, url);
    got_t a = {0}, b = {0}, c = {0}, o1 = {0}, o2 = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &a, NULL));
    ASSERT(run_until(e.loop, called, &a, 5000));
    e.id = other_id;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &o1, NULL));
    ASSERT(run_until(e.loop, called, &o1, 5000));
    e.id = id;
    ASSERT(a.ok && o1.ok);
    atomic_store(&e.s.silent_conns, 1);
    atomic_store(&g_timeout_ms, 300);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT_FALSE(b.ok);
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    atomic_store(&g_timeout_ms, 0);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &c, NULL));
    ASSERT(run_until(e.loop, called, &c, 5000));
    ASSERT(c.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    e.id = other_id;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &o2, NULL));
    ASSERT(run_until(e.loop, called, &o2, 5000));
    ASSERT(o2.ok);
    ASSERT_EQm("another server keeps its connection", 1, atomic_load(&other.accepts));
    env_down(&e);
    srv_stop(&other);
    PASS();
}

/* Catches: a timeout taking the other queries of its connection with it. */
TEST a_query_beside_one_that_timed_out_is_still_answered(void)
{
    env_t e;
    ASSERT(h2_up(&e, 0));
    got_t a = {0}, b = {0}, c = {0}, d = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &a, NULL));
    ASSERT(run_until(e.loop, called, &a, 5000));
    atomic_store(&e.s.swallow, 1);
    atomic_store(&g_timeout_ms, 300);
    e.s.delay_ms = 800;
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &b, NULL));
    run_for(e.loop, 100);
    atomic_store(&g_timeout_ms, 0);
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &c, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT_FALSE(b.ok);
    ASSERT_EQ(0, c.calls);
    ASSERT(run_until(e.loop, called, &c, 5000));
    ASSERT(c.ok);
    ASSERT_EQ(1, atomic_load(&e.s.accepts));
    ASSERT(run_until(e.loop, asked_three, &e.s, 1000));
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &d, NULL));
    ASSERT(run_until(e.loop, called, &d, 5000));
    ASSERT(d.ok);
    ASSERT_EQ(2, atomic_load(&e.s.accepts));
    env_down(&e);
    PASS();
}

TEST an_idle_client_does_not_wake(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 2000, &g, NULL));
    ASSERT(run_until(e.loop, called, &g, 5000));
    run_for(e.loop, 200);
    ASSERT_GT(atomic_load(&g_poll_ms), 60000);
    env_down(&e);
    PASS();
}

static void noop_post(firc_loop_t *loop, void *ud)
{
    (void)loop;
    (void)ud;
}

TEST a_full_loop_queue_delays_the_answer_not_loses_it(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    int filled = 0;
    while (filled < 4096 && firc_loop_post(e.loop, noop_post, NULL) == FIRC_OK) {
        filled++;
    }
    ASSERT(filled < 4096);
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &g, NULL));
    ASSERT(wait_replied(&e.s, 5000));
    nap_ms(200);
    ASSERT(run_until(e.loop, called, &g, 5000));
    ASSERT_EQ(1, g.calls);
    ASSERT(g.ok);
    env_down(&e);
    PASS();
}

TEST the_same_url_interns_to_one_id(void)
{
    firc_loop_t *loop = NULL;
    ASSERT_EQ(FIRC_OK, firc_loop_create(&loop));
    firc_doh_t *d = firc_doh_new(loop);
    ASSERT(d != NULL);
    char url[64];
    snprintf(url, sizeof url, "https://a.example/dns-query");
    uint16_t a = firc_doh_intern(d, url);
    url[8] = 'b';
    uint16_t b = firc_doh_intern(d, url);
    uint16_t a2 = firc_doh_intern(d, "https://a.example/dns-query");
    ASSERT(a != 0);
    ASSERT(b != 0);
    ASSERT(a != b);
    ASSERT_EQ(a, a2);
    ASSERT_STR_EQ("https://a.example/dns-query", firc_doh_url(d, a));
    ASSERT_STR_EQ("https://b.example/dns-query", firc_doh_url(d, b));
    ASSERT_EQ(NULL, firc_doh_url(d, 0));
    for (int i = 2; i < FIRC_DOH_MAX_URLS; i++) {
        snprintf(url, sizeof url, "https://h%d.example/q", i);
        ASSERT(firc_doh_intern(d, url) != 0);
    }
    ASSERT_EQ(0, firc_doh_intern(d, "https://full.example/q"));
    ASSERT_EQ(a, firc_doh_intern(d, "https://a.example/dns-query"));
    ASSERT_EQ(0, firc_doh_intern(d, NULL));
    got_t g = {0};
    ASSERT_EQ(FIRC_ERR_INVAL, firc_doh_query(d, 0, 0, k_query, sizeof k_query, 1000, on_done, &g, NULL));
    ASSERT_EQ(FIRC_ERR_INVAL, firc_doh_query(d, a, 0, k_query, 11, 1000, on_done, &g, NULL));
    firc_doh_free(d);
    firc_loop_destroy(loop);
    PASS();
}

TEST free_with_a_query_in_flight_calls_nothing(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 500, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &g, NULL));
    run_for(e.loop, 100);
    firc_doh_free(e.d);
    e.d = NULL;
    run_for(e.loop, 800);
    ASSERT_EQ(0, g.calls);
    firc_loop_destroy(e.loop);
    srv_stop(&e.s);
    PASS();
}

TEST free_with_a_result_queued_calls_nothing(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &g, NULL));
    ASSERT(wait_replied(&e.s, 5000));
    nap_ms(200);
    firc_doh_free(e.d);
    run_for(e.loop, 100);
    ASSERT_EQ(0, g.calls);
    firc_loop_destroy(e.loop);
    srv_stop(&e.s);
    PASS();
}

TEST a_result_queued_at_loop_destroy_is_dropped(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 0, 0));
    got_t g = {0};
    ASSERT_EQ(FIRC_OK, ask(&e, 0, 3000, &g, NULL));
    ASSERT(wait_replied(&e.s, 5000));
    nap_ms(200);
    firc_doh_free(e.d);
    firc_loop_destroy(e.loop);
    ASSERT_EQ(0, g.calls);
    srv_stop(&e.s);
    PASS();
}

TEST the_ops_seam_reaches_the_client(void)
{
    env_t e;
    ASSERT(env_up(&e, 200, "application/dns-message", 50, 0));
    firc_doh_ops_t ops = firc_doh_ops(e.d);
    ASSERT_EQ(e.d, ops.ud);
    got_t a = {.s = &e.s}, b = {.s = &e.s};
    uint64_t ta = 0;
    ASSERT_EQ(FIRC_OK, ops.query(ops.ud, e.id, 0, k_query, sizeof k_query, 2000, on_done, &a, &ta));
    ops.cancel(ops.ud, ta);
    ASSERT_EQ(FIRC_OK, ops.query(ops.ud, e.id, 0, k_query, sizeof k_query, 2000, on_done, &b, NULL));
    ASSERT(run_until(e.loop, called, &b, 5000));
    ASSERT(b.ok);
    ASSERT_EQ(0, a.calls);
    env_down(&e);
    PASS();
}

SUITE(doh)
{
    RUN_TEST(a_query_is_posted_as_rfc8484);
    RUN_TEST(plain_http_is_never_spoken);
    RUN_TEST(a_loopback_peer_is_never_connected);
    RUN_TEST(a_peer_in_the_pool_is_never_connected);
    RUN_TEST(a_redirect_is_never_followed);
    RUN_TEST(an_environment_proxy_is_never_used);
    RUN_TEST(an_answer_longer_than_a_dns_message_is_a_failure);
    RUN_TEST(a_certificate_is_checked_and_its_name_too);
    RUN_TEST(a_non_200_is_a_failure);
    RUN_TEST(a_wrong_content_type_is_a_failure);
    RUN_TEST(a_slow_server_times_out);
    RUN_TEST(two_sequential_queries_share_a_connection);
    RUN_TEST(two_concurrent_queries_over_http1_open_two_connections);
    RUN_TEST(a_cancelled_query_never_calls_back);
    RUN_TEST(a_cancelled_transfer_still_warms_its_connection);
    RUN_TEST(different_marks_never_share_a_connection);
    RUN_TEST(a_forgotten_mark_opens_a_new_connection);
    RUN_TEST(a_mark_forgotten_mid_transfer_drops_its_share_after);
    RUN_TEST(a_forgotten_mark_is_not_reused_by_a_query_beside_one_in_flight);
    RUN_TEST(a_silent_connection_is_dropped_after_a_timeout);
    RUN_TEST(a_query_beside_one_that_timed_out_is_still_answered);
    RUN_TEST(an_idle_client_does_not_wake);
    RUN_TEST(a_full_loop_queue_delays_the_answer_not_loses_it);
    RUN_TEST(the_same_url_interns_to_one_id);
    RUN_TEST(free_with_a_query_in_flight_calls_nothing);
    RUN_TEST(free_with_a_result_queued_calls_nothing);
    RUN_TEST(a_result_queued_at_loop_destroy_is_dropped);
    RUN_TEST(the_ops_seam_reaches_the_client);
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    firc_sub_fetch_global_init();
    GREATEST_MAIN_BEGIN();
    RUN_SUITE(doh);
    GREATEST_MAIN_END();
}
