#include "firc/doh.h"

#include <limits.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>

#include <curl/curl.h>

#include "firc/log.h"
#include "firc/resolver_addr.h"
#include "firc/sub_sock.h"

#define DOH_MIN_MSG 12u
#define DOH_MAX_MSG 65535u
#define DOH_BUSY_WAIT_MS 1000
#define DOH_REPOST_WAIT_MS 50
#define DOH_CTYPE "application/dns-message"

typedef struct live {
    uint64_t ticket;
    firc_doh_done_fn fn;
    void *ud;
} live_t;

typedef struct tickets {
    atomic_uint refs;
    live_t *v;
    size_t n, cap;
} tickets_t;

typedef struct result {
    struct result *next;
    tickets_t *t;
    uint64_t ticket;
    bool ok;
    size_t len;
    uint8_t data[];
} result_t;

typedef enum { CMD_QUERY, CMD_FORGET } cmd_kind_t;

typedef struct cmd {
    struct cmd *next;
    cmd_kind_t kind;
    uint64_t ticket;
    const char *url;
    uint32_t mark;
    uint64_t deadline_ms;
    uint64_t cap_ms;
    uint8_t id[2];
    size_t len;
    uint8_t body[];
} cmd_t;

typedef struct share {
    struct share *next;
    uint32_t mark;
    const char *url;
    CURLSH *sh;
    size_t users;
    bool retired;
} share_t;

typedef struct xfer {
    struct xfer *next, *prev;
    CURL *easy;
    cmd_t *cmd;
    share_t *share;
    firc_sub_sock_t sock;
    bool reported;
    bool over;
    size_t len, cap;
    uint8_t *buf;
} xfer_t;

struct firc_doh {
    firc_loop_t *loop;
    CURLM *multi;
    struct curl_slist *hdrs;
    firc_ip_t pool_base[2];
    uint8_t pool_len[2];
    bool allow_http, allow_loopback;
    tickets_t *live;
    uint64_t next_ticket;
    char *urls[FIRC_DOH_MAX_URLS];
    uint16_t n_urls;
    pthread_t thread;
    bool started;
    pthread_mutex_t mu;
    cmd_t *head, *tail;
    bool stop;
    xfer_t *xfers;
    share_t *shares;
    result_t *pend;
    result_t **pend_tail;
};

static uint64_t now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static size_t tickets_find(const tickets_t *t, uint64_t ticket)
{
    size_t lo = 0, hi = t->n;
    while (lo < hi) {
        size_t mid = lo + (hi - lo) / 2;
        if (t->v[mid].ticket < ticket) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    return lo < t->n && t->v[lo].ticket == ticket ? lo : SIZE_MAX;
}

static firc_err_t tickets_add(tickets_t *t, uint64_t ticket, firc_doh_done_fn fn, void *ud)
{
    if (t->n == t->cap) {
        size_t cap = t->cap == 0 ? 16 : t->cap * 2;
        live_t *v = realloc(t->v, cap * sizeof *v);
        if (v == NULL) {
            return FIRC_ERR_NOMEM;
        }
        t->v = v;
        t->cap = cap;
    }
    t->v[t->n++] = (live_t){.ticket = ticket, .fn = fn, .ud = ud};
    return FIRC_OK;
}

static void tickets_erase(tickets_t *t, size_t i)
{
    memmove(&t->v[i], &t->v[i + 1], (t->n - i - 1) * sizeof t->v[0]);
    t->n--;
}

static void tickets_unref(tickets_t *t)
{
    if (atomic_fetch_sub(&t->refs, 1) == 1) {
        free(t->v);
        free(t);
    }
}

static void result_free(result_t *r)
{
    tickets_unref(r->t);
    free(r);
}

static void drop_result(void *ud) { result_free(ud); }

static void on_result(firc_loop_t *loop, void *ud)
{
    (void)loop;
    result_t *r = ud;
    size_t i = tickets_find(r->t, r->ticket);
    if (i != SIZE_MAX) {
        live_t l = r->t->v[i];
        tickets_erase(r->t, i);
        l.fn(l.ud, r->ok ? r->data : NULL, r->ok ? r->len : 0, r->ok);
    }
    result_free(r);
}

static result_t *result_new(firc_doh_t *d, uint64_t ticket, const uint8_t *data, size_t len)
{
    result_t *r = malloc(sizeof *r + len);
    if (r == NULL) {
        return NULL;
    }
    r->next = NULL;
    r->t = d->live;
    atomic_fetch_add(&r->t->refs, 1);
    r->ticket = ticket;
    r->ok = data != NULL;
    r->len = len;
    if (len > 0) {
        memcpy(r->data, data, len);
    }
    return r;
}

static void flush_pend(firc_doh_t *d)
{
    while (d->pend != NULL) {
        result_t *r = d->pend;
        result_t *next = r->next;
        if (firc_loop_post_with_drop(d->loop, on_result, r, drop_result) != FIRC_OK) {
            return;
        }
        d->pend = next;
        if (next == NULL) {
            d->pend_tail = &d->pend;
        }
    }
}

static void report(firc_doh_t *d, xfer_t *x, const uint8_t *data, size_t len)
{
    x->reported = true;
    result_t *r = result_new(d, x->cmd->ticket, data, data != NULL ? len : 0);
    if (r == NULL && data != NULL) {
        r = result_new(d, x->cmd->ticket, NULL, 0);
    }
    if (r == NULL) {
        firc_log(FIRC_LOG_WARN, "doh: out of memory, a DoH answer was lost");
        return;
    }
    *d->pend_tail = r;
    d->pend_tail = &r->next;
    flush_pend(d);
}

static void share_destroy(firc_doh_t *d, share_t *s)
{
    for (share_t **p = &d->shares; *p != NULL; p = &(*p)->next) {
        if (*p == s) {
            *p = s->next;
            break;
        }
    }
    curl_share_cleanup(s->sh);
    free(s);
}

/* One per mark and URL: a server that went silent costs no other server its connection. */
static share_t *share_for(firc_doh_t *d, uint32_t mark, const char *url)
{
    for (share_t *s = d->shares; s != NULL; s = s->next) {
        if (s->mark == mark && s->url == url && !s->retired) {
            return s;
        }
    }
    share_t *s = calloc(1, sizeof *s);
    if (s == NULL) {
        return NULL;
    }
    s->sh = curl_share_init();
    if (s->sh == NULL || curl_share_setopt(s->sh, CURLSHOPT_SHARE, CURL_LOCK_DATA_CONNECT) != CURLSHE_OK) {
        curl_share_cleanup(s->sh);
        free(s);
        return NULL;
    }
    s->mark = mark;
    s->url = url;
    s->next = d->shares;
    d->shares = s;
    return s;
}

static void retire_mark(firc_doh_t *d, uint32_t mark)
{
    share_t *s = d->shares;
    while (s != NULL) {
        share_t *next = s->next;
        if (s->mark == mark && !s->retired) {
            s->retired = true;
            if (s->users == 0) {
                share_destroy(d, s);
            }
        }
        s = next;
    }
}

static bool prefix_has(const firc_ip_t *base, uint8_t len, const firc_ip_t *ip)
{
    if (len == 0 || base->len != ip->len) { return false; }
    for (unsigned bit = 0; bit < len; bit++) {
        uint8_t mask = (uint8_t)(0x80u >> (bit % 8u));
        if ((base->b[bit / 8u] & mask) != (ip->b[bit / 8u] & mask)) { return false; }
    }
    return true;
}

/* The URL's literal is checked at parse; this covers what its name resolves to. */
static bool peer_allowed(const firc_doh_t *d, const struct sockaddr *sa)
{
    static const uint8_t mapped[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff};
    firc_ip_t ip;
    memset(&ip, 0, sizeof ip);
    if (sa->sa_family == AF_INET) {
        struct sockaddr_in s4;
        memcpy(&s4, sa, sizeof s4);
        ip.len = 4;
        memcpy(ip.b, &s4.sin_addr, 4);
    } else if (sa->sa_family == AF_INET6) {
        struct sockaddr_in6 s6;
        memcpy(&s6, sa, sizeof s6);
        ip.len = 16;
        memcpy(ip.b, &s6.sin6_addr, 16);
        if (memcmp(ip.b, mapped, sizeof mapped) == 0) {
            memmove(ip.b, ip.b + 12, 4);
            memset(ip.b + 4, 0, 12);
            ip.len = 4;
        }
    } else {
        return false;
    }
    if (prefix_has(&d->pool_base[0], d->pool_len[0], &ip) || prefix_has(&d->pool_base[1], d->pool_len[1], &ip)) {
        return false;
    }
    return d->allow_loopback || !firc_resolver_ip_is_sink(&ip);
}

static curl_socket_t open_cb(void *ud, curlsocktype purpose, struct curl_sockaddr *a)
{
    (void)purpose;
    if (!peer_allowed(ud, &a->addr)) { return CURL_SOCKET_BAD; }
    int fd = socket(a->family, a->socktype | SOCK_CLOEXEC, a->protocol);
    const unsigned dead_ms = FIRC_DOH_DETACH_MS;
    if (fd >= 0) { (void)setsockopt(fd, IPPROTO_TCP, TCP_USER_TIMEOUT, &dead_ms, sizeof dead_ms); }
    return fd;
}

static size_t write_cb(char *ptr, size_t size, size_t nmemb, void *ud)
{
    xfer_t *x = ud;
    size_t n = size * nmemb;
    if (n > DOH_MAX_MSG - x->len) {
        x->over = true;
        return 0;
    }
    if (x->len + n > x->cap) {
        size_t cap = x->cap == 0 ? 512 : x->cap;
        while (cap < x->len + n) {
            cap *= 2;
        }
        uint8_t *b = realloc(x->buf, cap);
        if (b == NULL) {
            x->over = true;
            return 0;
        }
        x->buf = b;
        x->cap = cap;
    }
    memcpy(x->buf + x->len, ptr, n);
    x->len += n;
    return n;
}

static void xfer_free(firc_doh_t *d, xfer_t *x)
{
    if (x->prev != NULL) {
        x->prev->next = x->next;
    } else {
        d->xfers = x->next;
    }
    if (x->next != NULL) {
        x->next->prev = x->prev;
    }
    if (x->easy != NULL) {
        curl_multi_remove_handle(d->multi, x->easy);
        curl_easy_cleanup(x->easy);
    }
    if (x->share != NULL) {
        x->share->users--;
        if (x->share->retired && x->share->users == 0) {
            share_destroy(d, x->share);
        }
    }
    free(x->buf);
    free(x->cmd);
    free(x);
}

static bool start_xfer(firc_doh_t *d, cmd_t *c, uint64_t now)
{
    xfer_t *x = calloc(1, sizeof *x);
    if (x == NULL) {
        return false;
    }
    x->cmd = c;
    x->next = d->xfers;
    if (d->xfers != NULL) {
        d->xfers->prev = x;
    }
    d->xfers = x;
    x->share = share_for(d, c->mark, c->url);
    if (x->share != NULL) {
        x->share->users++;
    }
    x->easy = x->share != NULL ? curl_easy_init() : NULL;
    if (x->easy == NULL) {
        report(d, x, NULL, 0);
        xfer_free(d, x);
        return true;
    }
    x->sock.mark = c->mark;
    long cap_ms = c->cap_ms > now ? (long)(c->cap_ms - now) : 1;
    CURL *e = x->easy;
    curl_easy_setopt(e, CURLOPT_URL, c->url);
    curl_easy_setopt(e, CURLOPT_POSTFIELDS, (const char *)c->body);
    curl_easy_setopt(e, CURLOPT_POSTFIELDSIZE, (long)c->len);
    curl_easy_setopt(e, CURLOPT_HTTPHEADER, d->hdrs);
    curl_easy_setopt(e, CURLOPT_NOSIGNAL, 1L);
    /* libcurl's defaults, pinned: the certificate and its name are always checked, a redirect is never followed. */
    curl_easy_setopt(e, CURLOPT_SSL_VERIFYPEER, 1L);
    curl_easy_setopt(e, CURLOPT_SSL_VERIFYHOST, 2L);
    curl_easy_setopt(e, CURLOPT_FOLLOWLOCATION, 0L);
#if LIBCURL_VERSION_NUM >= 0x075500
    curl_easy_setopt(e, CURLOPT_PROTOCOLS_STR, d->allow_http ? "http,https" : "https");
#else
    curl_easy_setopt(e, CURLOPT_PROTOCOLS, d->allow_http ? (long)(CURLPROTO_HTTP | CURLPROTO_HTTPS) : (long)CURLPROTO_HTTPS);
#endif
    /* An environment proxy would take the query past the group's mark. */
    curl_easy_setopt(e, CURLOPT_PROXY, "");
    curl_easy_setopt(e, CURLOPT_OPENSOCKETFUNCTION, open_cb);
    curl_easy_setopt(e, CURLOPT_OPENSOCKETDATA, d);
    curl_easy_setopt(e, CURLOPT_SHARE, x->share->sh);
    curl_easy_setopt(e, CURLOPT_PIPEWAIT, 1L);
    curl_easy_setopt(e, CURLOPT_TIMEOUT_MS, cap_ms);
    curl_easy_setopt(e, CURLOPT_WRITEFUNCTION, write_cb);
    curl_easy_setopt(e, CURLOPT_WRITEDATA, x);
    curl_easy_setopt(e, CURLOPT_SOCKOPTFUNCTION, firc_sub_sock_cb);
    curl_easy_setopt(e, CURLOPT_SOCKOPTDATA, &x->sock);
    curl_easy_setopt(e, CURLOPT_PRIVATE, x);
    if (curl_multi_add_handle(d->multi, e) != CURLM_OK) {
        curl_easy_cleanup(e);
        x->easy = NULL;
        report(d, x, NULL, 0);
        xfer_free(d, x);
    }
    return true;
}

static bool answer_ok(xfer_t *x, CURLcode rc)
{
    long status = 0;
    char *ct = NULL;
    if (rc != CURLE_OK || x->over || x->len < DOH_MIN_MSG) {
        return false;
    }
    curl_easy_getinfo(x->easy, CURLINFO_RESPONSE_CODE, &status);
    curl_easy_getinfo(x->easy, CURLINFO_CONTENT_TYPE, &ct);
    size_t n = strlen(DOH_CTYPE);
    return status == 200 && ct != NULL && strncasecmp(ct, DOH_CTYPE, n) == 0 &&
           (ct[n] == '\0' || ct[n] == ';' || ct[n] == ' ');
}

static void finish(firc_doh_t *d, xfer_t *x, CURLcode rc)
{
    /* libcurl keeps an HTTP/2 connection whose stream timed out; its other streams still run to their end. */
    if (rc == CURLE_OPERATION_TIMEDOUT) { x->share->retired = true; }
    if (!x->reported) {
        if (answer_ok(x, rc)) {
            x->buf[0] = x->cmd->id[0];
            x->buf[1] = x->cmd->id[1];
            report(d, x, x->buf, x->len);
        } else {
            report(d, x, NULL, 0);
        }
    }
    xfer_free(d, x);
}

static void harvest(firc_doh_t *d)
{
    CURLMsg *m;
    int left;
    while ((m = curl_multi_info_read(d->multi, &left)) != NULL) {
        if (m->msg != CURLMSG_DONE) {
            continue;
        }
        char *p = NULL;
        curl_easy_getinfo(m->easy_handle, CURLINFO_PRIVATE, &p);
        if (p != NULL) {
            finish(d, (xfer_t *)p, m->data.result);
        }
    }
}

static int expire(firc_doh_t *d, uint64_t now)
{
    uint64_t wait = d->xfers != NULL ? DOH_BUSY_WAIT_MS : INT_MAX;
    for (xfer_t *x = d->xfers; x != NULL; x = x->next) {
        if (x->reported) {
            continue;
        }
        if (now >= x->cmd->deadline_ms) {
            report(d, x, NULL, 0);
        } else if (x->cmd->deadline_ms - now < wait) {
            wait = x->cmd->deadline_ms - now;
        }
    }
    return (int)wait;
}

static void apply(firc_doh_t *d, cmd_t *c, uint64_t now)
{
    while (c != NULL) {
        cmd_t *next = c->next;
        c->next = NULL;
        if (c->kind == CMD_FORGET) {
            retire_mark(d, c->mark);
            free(c);
        } else if (!start_xfer(d, c, now)) {
            firc_log(FIRC_LOG_WARN, "doh: out of memory, a DoH query was lost");
            free(c);
        }
        c = next;
    }
}

static void free_cmds(cmd_t *c)
{
    while (c != NULL) {
        cmd_t *next = c->next;
        free(c);
        c = next;
    }
}

static void *worker_main(void *ud)
{
    firc_doh_t *d = ud;
    for (;;) {
        pthread_mutex_lock(&d->mu);
        bool stop = d->stop;
        cmd_t *c = d->head;
        d->head = d->tail = NULL;
        pthread_mutex_unlock(&d->mu);
        if (stop) {
            free_cmds(c);
            return NULL;
        }
        apply(d, c, now_ms());
        int running = 0;
        curl_multi_perform(d->multi, &running);
        harvest(d);
        int wait = expire(d, now_ms());
        flush_pend(d);
        if (d->pend != NULL && wait > DOH_REPOST_WAIT_MS) {
            wait = DOH_REPOST_WAIT_MS;
        }
        curl_multi_poll(d->multi, NULL, 0, wait, NULL);
    }
}

static void enqueue(firc_doh_t *d, cmd_t *c)
{
    pthread_mutex_lock(&d->mu);
    if (d->tail != NULL) {
        d->tail->next = c;
    } else {
        d->head = c;
    }
    d->tail = c;
    pthread_mutex_unlock(&d->mu);
    curl_multi_wakeup(d->multi);
}

firc_doh_t *firc_doh_new(firc_loop_t *loop)
{
    firc_doh_t *d = calloc(1, sizeof *d);
    if (d == NULL) {
        return NULL;
    }
    d->loop = loop;
    d->next_ticket = 1;
    d->pend_tail = &d->pend;
    pthread_mutex_init(&d->mu, NULL);
    d->live = calloc(1, sizeof *d->live);
    if (d->live != NULL) {
        atomic_init(&d->live->refs, 1);
    }
    d->multi = curl_multi_init();
    struct curl_slist *h = curl_slist_append(NULL, "Content-Type: " DOH_CTYPE);
    struct curl_slist *h2 = h != NULL ? curl_slist_append(h, "Accept: " DOH_CTYPE) : NULL;
    d->hdrs = h2 != NULL ? h2 : h;
    if (d->live == NULL || d->multi == NULL || h2 == NULL) {
        firc_doh_free(d);
        return NULL;
    }
    curl_multi_setopt(d->multi, CURLMOPT_PIPELINING, (long)CURLPIPE_MULTIPLEX);
    return d;
}

void firc_doh_set_pool(firc_doh_t *d, const firc_fakeip_t *pool)
{
    static const unsigned fams[2] = {FIRC_FAM_V4, FIRC_FAM_V6};
    for (size_t i = 0; i < 2; i++) {
        if (!firc_fakeip_pool_prefix(pool, fams[i], &d->pool_base[i], &d->pool_len[i])) { d->pool_len[i] = 0; }
    }
}

void firc_doh_allow_for_test(firc_doh_t *d, bool http, bool loopback)
{
    d->allow_http = http;
    d->allow_loopback = loopback;
}

firc_err_t firc_doh_start(firc_doh_t *d)
{
    if (d == NULL) {
        return FIRC_ERR_INVAL;
    }
    if (d->started) {
        return FIRC_ERR_STATE;
    }
    const curl_version_info_data *v = curl_version_info(CURLVERSION_NOW);
    if (v->version_num < 0x074400) {
        firc_log(FIRC_LOG_ERROR, "doh: libcurl %s is older than 7.68, DoH is off", v->version);
        return FIRC_ERR_NOSYS;
    }
    if ((v->features & CURL_VERSION_ASYNCHDNS) == 0) {
        firc_log(FIRC_LOG_WARN, "doh: libcurl resolves names synchronously, a slow lookup stalls every DoH query");
    }
    if (pthread_create(&d->thread, NULL, worker_main, d) != 0) {
        return FIRC_ERR_SYS;
    }
    d->started = true;
    return FIRC_OK;
}

void firc_doh_free(firc_doh_t *d)
{
    if (d == NULL) {
        return;
    }
    if (d->started) {
        pthread_mutex_lock(&d->mu);
        d->stop = true;
        pthread_mutex_unlock(&d->mu);
        curl_multi_wakeup(d->multi);
        pthread_join(d->thread, NULL);
    }
    while (d->xfers != NULL) {
        xfer_free(d, d->xfers);
    }
    while (d->shares != NULL) {
        share_destroy(d, d->shares);
    }
    while (d->pend != NULL) {
        result_t *r = d->pend;
        d->pend = r->next;
        result_free(r);
    }
    free_cmds(d->head);
    if (d->live != NULL) {
        d->live->n = 0;
        tickets_unref(d->live);
    }
    if (d->multi != NULL) {
        curl_multi_cleanup(d->multi);
    }
    curl_slist_free_all(d->hdrs);
    for (uint16_t i = 0; i < d->n_urls; i++) {
        free(d->urls[i]);
    }
    pthread_mutex_destroy(&d->mu);
    free(d);
}

uint16_t firc_doh_intern(firc_doh_t *d, const char *url)
{
    if (d == NULL || url == NULL) {
        return 0;
    }
    for (uint16_t i = 0; i < d->n_urls; i++) {
        if (strcmp(d->urls[i], url) == 0) {
            return (uint16_t)(i + 1);
        }
    }
    /* ponytail: 64 URLs per process life, then new ones are refused; ids sit in route tables and the pointers in the
       worker, so freeing one needs a reference count across both. */
    if (d->n_urls == FIRC_DOH_MAX_URLS) {
        return 0;
    }
    char *copy = strdup(url);
    if (copy == NULL) {
        return 0;
    }
    d->urls[d->n_urls++] = copy;
    return d->n_urls;
}

const char *firc_doh_url(const firc_doh_t *d, uint16_t id)
{
    if (d == NULL || id == 0 || id > d->n_urls) {
        return NULL;
    }
    return d->urls[id - 1];
}

firc_err_t firc_doh_query(firc_doh_t *d, uint16_t url_id, uint32_t mark, const uint8_t *query, size_t len,
                          uint32_t timeout_ms, firc_doh_done_fn fn, void *ud, uint64_t *ticket)
{
    const char *url = firc_doh_url(d, url_id);
    if (url == NULL || query == NULL || fn == NULL || len < DOH_MIN_MSG || len > DOH_MAX_MSG ||
        timeout_ms == 0) {
        return FIRC_ERR_INVAL;
    }
    cmd_t *c = malloc(sizeof *c + len);
    if (c == NULL) {
        return FIRC_ERR_NOMEM;
    }
    uint64_t now = now_ms();
    *c = (cmd_t){
        .kind = CMD_QUERY,
        .ticket = d->next_ticket,
        .url = url,
        .mark = mark,
        .deadline_ms = now + (timeout_ms < FIRC_DOH_DETACH_MS ? timeout_ms : FIRC_DOH_DETACH_MS),
        .cap_ms = now + FIRC_DOH_DETACH_MS,
        .id = {query[0], query[1]},
        .len = len,
    };
    memcpy(c->body, query, len);
    c->body[0] = 0;
    c->body[1] = 0;
    if (tickets_add(d->live, c->ticket, fn, ud) != FIRC_OK) {
        free(c);
        return FIRC_ERR_NOMEM;
    }
    d->next_ticket++;
    if (ticket != NULL) {
        *ticket = c->ticket;
    }
    enqueue(d, c);
    return FIRC_OK;
}

void firc_doh_cancel(firc_doh_t *d, uint64_t ticket)
{
    if (d == NULL) {
        return;
    }
    size_t i = tickets_find(d->live, ticket);
    if (i != SIZE_MAX) {
        tickets_erase(d->live, i);
    }
}

void firc_doh_forget_mark(firc_doh_t *d, uint32_t mark)
{
    if (d == NULL) {
        return;
    }
    cmd_t *c = calloc(1, sizeof *c);
    if (c == NULL) {
        firc_log(FIRC_LOG_WARN, "doh: out of memory, mark %u keeps its DoH connections", (unsigned)mark);
        return;
    }
    c->kind = CMD_FORGET;
    c->mark = mark;
    enqueue(d, c);
}

static firc_err_t ops_query(void *ud, uint16_t url_id, uint32_t mark, const uint8_t *query, size_t len,
                            uint32_t timeout_ms, firc_doh_done_fn fn, void *fn_ud, uint64_t *ticket)
{
    return firc_doh_query(ud, url_id, mark, query, len, timeout_ms, fn, fn_ud, ticket);
}

static void ops_cancel(void *ud, uint64_t ticket) { firc_doh_cancel(ud, ticket); }

firc_doh_ops_t firc_doh_ops(firc_doh_t *d)
{
    return (firc_doh_ops_t){.query = ops_query, .cancel = ops_cancel, .ud = d};
}
