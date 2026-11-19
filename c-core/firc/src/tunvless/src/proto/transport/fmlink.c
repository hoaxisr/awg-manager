#define _GNU_SOURCE
#include <errno.h>
#include <poll.h>
#include <limits.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <time.h>
#include <sys/socket.h>

#include "fmlink.h"
#include "osrand.h"
#include "transport.h"

struct fm_piece { uint32_t n; int32_t gap; };

struct fm_link {
    size_t n;
    struct fm_layer *lay;
    int loop, err, fd, eof;
    uint64_t now, due, hs_due;
    fm_emit_fn emit;
    void *arg;
    const char *why;
    unsigned char *qd;
    size_t qd_head, qd_len, qd_cap;
    struct fm_piece *qp;
    size_t qp_head, qp_len, qp_cap;
};

#define AL(x) (((x) + _Alignof(max_align_t) - 1) / _Alignof(max_align_t) * _Alignof(max_align_t))

static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static __thread char g_last_why[96];
static struct fm_cached { const char *k; struct fm_cfg *c; } *g_tab;
static size_t g_n, g_cap;

const struct fm_cfg *fm_cfg_get(const char *json) {
    if (!json) return NULL;
    struct fm_cfg *r = NULL;
    pthread_mutex_lock(&g_mu);
    for (size_t i = 0; i < g_n; i++)
        if (g_tab[i].k == json) { r = g_tab[i].c; goto out; }
    if (g_n == g_cap) {
        size_t cap = g_cap ? g_cap * 2 : 16;
        struct fm_cached *t = realloc(g_tab, cap * sizeof *t);
        if (!t) goto out;
        g_tab = t;
        g_cap = cap;
    }
    r = calloc(1, sizeof *r);
    char why[128];
    if (!r || fm_parse(json, strlen(json), r, why, sizeof why)) {
        if (r) fm_cfg_free(r);
        free(r);
        r = NULL;
        goto out;
    }
    g_tab[g_n].k = json;
    g_tab[g_n].c = r;
    g_n++;
out:
    pthread_mutex_unlock(&g_mu);
    return r;
}

struct fm_link *fm_link_new_ops(const struct fm_ops *const *ops, const struct fm_mask *const *m, size_t n) {
    errno = 0;
    if (!n) return NULL;
    size_t sz = AL(sizeof(struct fm_link)) + AL(n * sizeof(struct fm_layer));
    for (size_t i = 0; i < n; i++) sz += AL(ops[i]->st_size);
    unsigned char *b = calloc(1, sz);
    if (!b) { errno = ENOMEM; return NULL; }
    struct fm_link *l = (struct fm_link *)b;
    l->lay = (struct fm_layer *)(b + AL(sizeof(struct fm_link)));
    l->fd = -1;
    unsigned char *p = (unsigned char *)l->lay + AL(n * sizeof(struct fm_layer));
    for (size_t i = 0; i < n; i++) {
        struct fm_layer *y = &l->lay[i];
        y->ops = ops[n - 1 - i];
        y->m = m[n - 1 - i];
        y->l = l;
        y->idx = i;
        if (y->ops->st_size) {
            y->st = p;
            p += AL(y->ops->st_size);
        }
        if (y->ops->init && y->ops->init(y->st, y->m) < 0) {
            l->n = i;
            fm_link_free(l);
            errno = EINVAL;
            return NULL;
        }
    }
    l->n = n;
    return l;
}

int fm_link_open(const char *fm, struct fm_link **out) {
    *out = NULL;
    if (!fm) return 0;
    const struct fm_cfg *c = fm_cfg_get(fm);
    if (!c) {
        snprintf(g_last_why, sizeof g_last_why, "%s", "the node's mask does not parse");
        return TR_EMASK;
    }
    size_t k = 0;
    for (size_t i = 0; i < c->n; i++) k += c->m[i].kind != FM_FILTER;
    if (!k) return 0;
    const struct fm_ops **ops = malloc(k * sizeof *ops);
    const struct fm_mask **ms = malloc(k * sizeof *ms);
    int rc = TR_EMASK;
    if (!ops || !ms) goto out;
    k = 0;
    for (size_t i = 0; i < c->n; i++) {
        switch (c->m[i].kind) {
            case FM_FILTER: continue;
            case FM_FRAGMENT: ops[k] = &fm_frag_ops; break;
            case FM_HEADER: ops[k] = &fm_hc_ops; break;
            case FM_SUDOKU: ops[k] = &fm_sd_ops; break;
            default: goto out;
        }
        ms[k++] = &c->m[i];
    }
    *out = fm_link_new_ops(ops, ms, k);
    if (*out) rc = 0;
out:
    if (rc) snprintf(g_last_why, sizeof g_last_why, "%s", "the mask chain cannot be built");
    free(ops);
    free(ms);
    return rc;
}

static void q_release(struct fm_link *l) {
    free(l->qd);
    free(l->qp);
    l->qd = NULL;
    l->qp = NULL;
    l->qd_head = l->qd_len = l->qd_cap = 0;
    l->qp_head = l->qp_len = l->qp_cap = 0;
}

void fm_link_free(struct fm_link *l) {
    if (!l) return;
    for (size_t i = 0; i < l->n; i++)
        if (l->lay[i].ops->free) l->lay[i].ops->free(l->lay[i].st);
    q_release(l);
    free(l);
}

static void note(struct fm_link *l, const struct fm_layer *y) {
    if (!l->why && y->ops->why) {
        const char *w = y->ops->why(y);
        if (w && *w) l->why = w;
    }
    snprintf(g_last_why, sizeof g_last_why, "%s", l->why ? l->why : "");
}

const char *fm_last_why(void) {
    return g_last_why;
}

static int write_at(struct fm_link *l, size_t top, const unsigned char *d, size_t n);

static int down(void *arg, const unsigned char *d, size_t n, int64_t gap) {
    struct fm_layer *me = arg;
    struct fm_link *l = me->l;
    size_t top = me->idx;
    while (top && !l->lay[top - 1].ops->wr) top--;
    if (!top) return n || gap > 0 ? l->emit(l->arg, d, n, gap) : 0;
    int rc = write_at(l, top, d, n);
    if (!rc && gap > 0) rc = l->emit(l->arg, d, 0, gap);
    return rc;
}

static int write_at(struct fm_link *l, size_t top, const unsigned char *d, size_t n) {
    while (top && !l->lay[top - 1].ops->wr) top--;
    if (!top) return l->emit(l->arg, d, n, 0);
    struct fm_layer *y = &l->lay[top - 1];
    int rc = y->ops->wr(y, d, n, down, y);
    if (rc) note(l, y);
    return rc;
}

int fm_lower_write(struct fm_layer *me, const unsigned char *d, size_t n, int64_t gap_ms) {
    if (!me->l->emit) return TR_EMASK;
    return down(me, d, n, gap_ms);
}

static int shape_at(struct fm_link *l, size_t top, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg) {
    fm_emit_fn se = l->emit;
    void *sa = l->arg;
    l->emit = emit;
    l->arg = arg;
    int rc = n ? write_at(l, top, d, n) : 0;
    l->emit = se;
    l->arg = sa;
    return rc;
}

int fm_shape(struct fm_link *l, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg) {
    return shape_at(l, l->n, d, n, emit, arg);
}

static size_t splice_top(const struct fm_link *l) {
    size_t top = l->n;
    while (top && l->lay[top - 1].ops->splice) top--;
    return top;
}

void fm_sleep_ms(int64_t ms) {
    struct timespec ts = { (time_t)(ms / 1000), (long)(ms % 1000) * 1000000L };
    while (nanosleep(&ts, &ts) && errno == EINTR) {}
}

static int hs_wait(struct fm_link *l, int64_t ms) {
    if (ms <= 0) return 0;
    if (l->hs_due && tr_mono_ms() + (uint64_t)ms > l->hs_due) return TR_ECONNTIMEOUT;
    fm_sleep_ms(ms);
    return 0;
}

int fm_lower_sleep(struct fm_layer *me, int64_t ms) {
    return hs_wait(me->l, ms);
}

static int emit_block(void *arg, const unsigned char *d, size_t n, int64_t gap) {
    struct fm_link *l = arg;
    size_t off = 0;
    while (off < n) {
        /* MSG_NOSIGNAL: a socket closed by the node fails the write instead of raising SIGPIPE. */
        ssize_t w = send(l->fd, d + off, n - off, MSG_NOSIGNAL);
        if (w <= 0) {
            if (w < 0 && errno == EINTR) continue;
            return TR_EIO;
        }
        off += (size_t)w;
    }
    return hs_wait(l, gap);
}

static int send_once(int fd, const unsigned char *d, size_t n, size_t *sent) {
    for (;;) {
        ssize_t w = send(fd, d, n, MSG_NOSIGNAL);
        if (w >= 0) { *sent = (size_t)w; return 0; }
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) { *sent = 0; return 0; }
        return TR_EIO;
    }
}

static int q_room(void **buf, size_t *head, size_t len, size_t *cap, size_t add, size_t el) {
    if (*head + len + add <= *cap) return 0;
    if (*head) {
        memmove(*buf, (unsigned char *)*buf + *head * el, len * el);
        *head = 0;
    }
    if (len + add <= *cap) return 0;
    size_t c = *cap ? *cap * 2 : 16;
    if (c < len + add) c = len + add;
    void *nb = realloc(*buf, c * el);
    if (!nb) return -1;
    *buf = nb;
    *cap = c;
    return 0;
}

static int32_t gap32(int64_t g) {
    return g <= 0 ? 0 : g > INT32_MAX ? INT32_MAX : (int32_t)g;
}

static int enqueue(struct fm_link *l, const unsigned char *d, size_t n, int64_t gap) {
    while (n > UINT32_MAX) {
        int rc = enqueue(l, d, UINT32_MAX, 0);
        if (rc) return rc;
        d += UINT32_MAX;
        n -= UINT32_MAX;
    }
    if (q_room((void **)&l->qd, &l->qd_head, l->qd_len, &l->qd_cap, n, 1) ||
        q_room((void **)&l->qp, &l->qp_head, l->qp_len, &l->qp_cap, 1, sizeof(struct fm_piece))) {
        l->why = "out of memory for the send queue";
        return TR_EIO;
    }
    memcpy(l->qd + l->qd_head + l->qd_len, d, n);
    l->qd_len += n;
    l->qp[l->qp_head + l->qp_len].n = (uint32_t)n;
    l->qp[l->qp_head + l->qp_len].gap = gap32(gap);
    l->qp_len++;
    return 0;
}

static size_t q_mem(const struct fm_link *l) {
    return l->qd_len + l->qp_len * sizeof(struct fm_piece);
}

static int emit_loop(void *arg, const unsigned char *d, size_t n, int64_t gap) {
    struct fm_link *l = arg;
    if (!n) {
        if (l->qp_len) {
            struct fm_piece *t = &l->qp[l->qp_head + l->qp_len - 1];
            t->gap = gap32((int64_t)t->gap + gap);
        } else {
            l->due = (l->due > l->now ? l->due : l->now) + (uint64_t)gap;
        }
        return 0;
    }
    if (!l->qp_len && l->due <= l->now) {
        size_t sent;
        int rc = send_once(l->fd, d, n, &sent);
        if (rc) return rc;
        if (sent == n) {
            if (gap > 0) l->due = l->now + (uint64_t)gap;
            return 0;
        }
        d += sent;
        n -= sent;
        l->due = l->now;
    }
    return enqueue(l, d, n, gap);
}

ssize_t fm_send(struct fm_link *l, int fd, const void *d, size_t n, int direct) {
    if (l->err) return l->err;
    if (!n) return 0;
    l->fd = fd;
    size_t top = direct ? splice_top(l) : l->n;
    int rc;
    if (l->loop) {
        if (q_mem(l) >= FM_Q_HARD) {
            l->why = "the send queue is full";
            return l->err = TR_EIO;
        }
        l->now = tr_mono_ms();
        rc = shape_at(l, top, d, n, emit_loop, l);
    } else {
        rc = shape_at(l, top, d, n, emit_block, l);
    }
    if (rc) return l->err = rc;
    return (ssize_t)n;
}

int fm_kick(struct fm_link *l, int fd, uint64_t now_ms_, int *next_ms) {
    *next_ms = -1;
    if (l->err) return l->err;
    while (l->qp_len) {
        if (l->due > now_ms_) {
            uint64_t w = l->due - now_ms_;
            *next_ms = w > INT_MAX ? INT_MAX : (int)w;
            return 0;
        }
        struct fm_piece *p = &l->qp[l->qp_head];
        size_t sent;
        int rc = send_once(fd, l->qd + l->qd_head, p->n, &sent);
        if (rc) return l->err = rc;
        l->qd_head += sent;
        l->qd_len -= sent;
        uint64_t at = tr_mono_ms();
        if (at < now_ms_) at = now_ms_;
        if (sent < p->n) {
            p->n -= (uint32_t)sent;
            l->due = at;
            *next_ms = 0;
            return 0;
        }
        int32_t g = p->gap;
        l->qp_head++;
        l->qp_len--;
        if (g > 0) l->due = at + (uint64_t)g;
    }
    q_release(l);
    return 0;
}

static int any_pending(const struct fm_link *l, size_t top) {
    for (size_t i = 0; i < top; i++)
        if (l->lay[i].ops->pending && l->lay[i].ops->pending(&l->lay[i])) return 1;
    return 0;
}

static ssize_t read_up(struct fm_link *l, int fd, size_t top, unsigned char *d, size_t cap, int flags) {
    for (;;) {
        int pend = any_pending(l, top);
        ssize_t r = recv(fd, d, cap, flags | (pend ? MSG_DONTWAIT : 0));
        if (r < 0) {
            if (errno == EINTR) continue;
            if (!pend || (errno != EAGAIN && errno != EWOULDBLOCK)) return -1;
            r = 0;
        } else if (r == 0) {
            l->eof = 1;
            if (!pend) return 0;
        }
        size_t got = (size_t)r;
        for (size_t i = 0; i < top; i++) {
            struct fm_layer *y = &l->lay[i];
            if (!y->ops->rd) continue;
            long o = y->ops->rd(y, d, got, cap);
            if (o < 0 || (size_t)o > cap) {
                note(l, y);
                errno = EPROTO;
                return -1;
            }
            got = (size_t)o;
        }
        if (got) return (ssize_t)got;
        if (l->eof) return 0;
        errno = r && !(flags & MSG_DONTWAIT) ? EINTR : EAGAIN;
        return -1;
    }
}

ssize_t fm_recv(struct fm_link *l, int fd, void *d, size_t cap, int flags, int direct) {
    return read_up(l, fd, direct ? splice_top(l) : l->n, d, cap, flags);
}

static int hs_poll(struct fm_link *l, int fd, size_t top) {
    if (!l->hs_due || any_pending(l, top)) return 0;
    for (;;) {
        uint64_t at = tr_mono_ms();
        if (at >= l->hs_due) return TR_ECONNTIMEOUT;
        uint64_t w = l->hs_due - at;
        struct pollfd pf = { fd, POLLIN, 0 };
        int r = poll(&pf, 1, w > INT_MAX ? INT_MAX : (int)w);
        if (r > 0) return 0;
        if (r == 0) return TR_ECONNTIMEOUT;
        if (errno != EINTR) return TR_EIO;
    }
}

int fm_lower_read_full(struct fm_layer *me, int fd, unsigned char *d, size_t n) {
    size_t got = 0;
    while (got < n) {
        int rc = hs_poll(me->l, fd, me->idx);
        if (rc) return rc;
        ssize_t r = read_up(me->l, fd, me->idx, d + got, n - got, 0);
        if (r == 0) return TR_ECLOSED;
        if (r < 0) {
            if (errno == EINTR) continue;
            if (errno == EAGAIN || errno == EWOULDBLOCK) return TR_ECONNTIMEOUT;
            return errno == EPROTO ? TR_EMASK : TR_EIO;
        }
        got += (size_t)r;
    }
    return 0;
}

int fm_link_hs(struct fm_link *l, int fd, int timeout_ms) {
    g_last_why[0] = 0;
    if (l->err) return l->err;
    l->hs_due = timeout_ms > 0 ? tr_mono_ms() + (uint64_t)timeout_ms : 0;
    fm_emit_fn se = l->emit;
    void *sa = l->arg;
    l->fd = fd;
    l->emit = emit_block;
    l->arg = l;
    int rc = 0;
    for (size_t i = 0; i < l->n && !rc; i++) {
        struct fm_layer *y = &l->lay[i];
        if (!y->ops->hs) continue;
        rc = y->ops->hs(y, fd);
        if (rc) note(l, y);
    }
    l->hs_due = 0;
    l->emit = se;
    l->arg = sa;
    return rc;
}

int fm_pending_any(const struct fm_link *l) {
    return l ? any_pending(l, l->n) : 0;
}

void fm_set_loop(struct fm_link *l, int on) {
    l->loop = on != 0;
}

static int rand_fill(unsigned char *b, size_t n) {
    while (n) {
        unsigned int k = n > 65536 ? 65536u : (unsigned int)n;
        if (os_rand_seed(b, k)) return -1;
        b += k;
        n -= k;
    }
    return 0;
}

int64_t fm_rand_between(int64_t from, int64_t to) {
    if (from > to) {
        int64_t t = from;
        from = to;
        to = t;
    }
    uint64_t d = (uint64_t)to - (uint64_t)from;
    if (d <= 1) return from;
    uint64_t lim = -d % d, r;
    do {
        if (rand_fill((unsigned char *)&r, sizeof r)) return from;
    } while (r < lim);
    return (int64_t)((uint64_t)from + r % d);
}

void fm_rand_bytes(unsigned char *b, size_t n, uint8_t from, uint8_t to) {
    if (rand_fill(b, n)) memset(b, 0, n);
    if (from > to) {
        uint8_t t = from;
        from = to;
        to = t;
    }
    if (to - from == 255) return;
    unsigned span = (unsigned)(to - from) + 1u;
    for (size_t i = 0; i < n; i++) b[i] = (uint8_t)(from + b[i] % span);
}

size_t fm_room(const struct fm_link *l) {
    size_t m = l ? q_mem(l) : 0;
    return m >= FM_Q_MAX ? 0 : (FM_Q_MAX - m) / fm_grow(l);
}

unsigned fm_grow(const struct fm_link *l) {
    unsigned g = 1;
    for (size_t i = 0; l && i < l->n; i++)
        if (l->lay[i].ops->grow) g *= l->lay[i].ops->grow(l->lay[i].m);
    return g;
}
