#define _GNU_SOURCE
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <time.h>

#include "trxmux.h"
#include "xconn.h"
#include "xmux.h"
#include "osrand.h"

#define XL_DIAL_SLOTS 100
#define XL_UP_BUDGET  (4u * 1024 * 1024)
#define XL_POST_MAX   1000000
#define XL_BAT_MIN    65536
#define XL_RES_KEEP   16384
#define XL_RES_IDLE_MS 1000
#define XL_TICK_MS   1000

struct xmm {
    struct xm_mgr m;
    unsigned flows;
    struct tr_node n;
    struct xmm *dl;
    struct xc_ref ref;
    uint8_t discard, has_ref;
};

struct xl {
    struct xconn *x;
    struct xm_client *cl;
    struct xmm *mm;
    struct transport *wait, *held;
    uint64_t used_ms;
    unsigned wn, hn, posters;
    uint8_t used, dialing, gone, watched, out, in;
};

struct xm_ctx { const struct tr_node *n; const struct xc_ref *ref; struct xmm *mm; };

static uint32_t rnd_os(uint32_t n) {
    uint32_t r = 0;
    if (os_getrandom(&r, sizeof r, 0) != (ssize_t)sizeof r) r = 0;
    return n ? r % n : 0;
}

unsigned tr_xmux_links_cap = XC_MAX, tr_xmux_mgrs_cap = XM_MGRS;
uint32_t (*tr_xmux_rnd)(uint32_t n) = rnd_os;
size_t tr_xmux_up_budget = XL_UP_BUDGET;

static __thread struct xl g_l[XC_MAX];
static __thread struct xmm *g_m[XM_MGRS];
static __thread struct mux_host g_h;
static __thread int (*g_stale)(const struct xc_ref *r);
static __thread uint64_t g_now, g_tick;
static __thread uint8_t g_ticked;
static __thread struct transport *g_bat;
static __thread size_t g_bat_used;
static __thread unsigned g_up_n;
static __thread const struct xm_client *g_pin;

static void *conn_new(void *ud, struct xm_client *c);
static void conn_close(void *ud, void *conn);
static void bat_off(struct transport *t);

static struct xm_env env(struct xm_ctx *cx) { return (struct xm_env){ tr_xmux_rnd, g_now, cx, conn_new, conn_close }; }

static unsigned idx(const struct xl *l) { return (unsigned)(l - g_l); }

static struct xl *find(const struct xconn *x) {
    for (unsigned i = 0; x && i < XC_MAX; i++)
        if (g_l[i].used && g_l[i].x == x) return &g_l[i];
    return NULL;
}

static void unwatch(struct xl *l) {
    if (l->watched && g_h.watch) g_h.watch(idx(l), l->x->link.fd, 0);
    l->watched = l->out = l->in = 0;
}

static int side(const struct transport *t, const struct xl *l) { return t->xh.f.mgr == &l->mm->m ? 0 : 1; }

static void holders_drop(struct xl *l) {
    for (struct transport *t = l->held; t;) {
        struct xh_flow *f = &t->xh.f;
        int s = side(t, l);
        struct transport *n = f->hnext[s];
        f->hold[s] = NULL;
        f->hnext[s] = NULL;
        t = n;
    }
    l->held = NULL;
    l->hn = 0;
}

static void link_free(struct xl *l) {
    holders_drop(l);
    unwatch(l);
    struct xconn *x = l->x;
    if (!l->dialing && x->state == XC_LIVE && x->link.fd >= 0) {
        unsigned char sink[4096];
        xconn_flush(x);
        for (int i = 0; i < 64 && tr_lk_recv(&x->link, sink, sizeof sink, MSG_DONTWAIT) > 0; i++) {}
    }
    xconn_free(x);
    memset(l, 0, sizeof *l);
}

static void drop_from_client(struct xl *l) {
    struct xm_client *c = l->cl;
    l->cl = NULL;
    if (!c) return;
    uint8_t k = 0;
    for (uint8_t i = 0; i < c->conn_n; i++)
        if (c->conn[i] != l) c->conn[k++] = c->conn[i];
    c->conn_n = k;
}

static int usable(const struct xl *l) {
    const struct xconn *x = l->x;
    return !l->dialing && !l->gone && x->state == XC_LIVE;
}

static unsigned need(const struct transport *t) {
    return t->xh.f.post_wait || t->xh.f.get_wait || t->xh.f.mgr_dl ? 1 : t->xh.mode == XH_STREAM_UP ? 2 : 1;
}

static int room(const struct xl *l, unsigned k) {
    return h2m_open_n(&l->x->h2) + l->wn + l->hn + k <= h2m_cap(&l->x->h2);
}

static int idle(const struct xl *l) {
    return l->used && !l->dialing && !l->wn && !l->hn && !l->posters && !xconn_streams(l->x);
}

static void link_check(struct xl *l) {
    if (idle(l) && (l->gone || l->x->state == XC_DEAD)) {
        drop_from_client(l);
        link_free(l);
    }
}

static void reap(void) {
    for (unsigned i = 0; i < XC_MAX; i++) link_check(&g_l[i]);
}

static struct xl *slot_for_link(void) {
    for (unsigned i = 0; i < tr_xmux_links_cap; i++)
        if (!g_l[i].used) return &g_l[i];
    reap();
    struct xl *lru = NULL;
    for (unsigned i = 0; i < tr_xmux_links_cap; i++) {
        struct xl *l = &g_l[i];
        if (!l->used) return l;
        if (idle(l) && l->x->state == XC_LIVE && (!g_pin || l->cl != g_pin) && (!lru || l->used_ms < lru->used_ms)) lru = l;
    }
    if (lru) {
        drop_from_client(lru);
        link_free(lru);
    }
    return lru;
}

static struct xl *link_new(struct xmm *mm, struct xm_client *cl, const struct tr_node *n, const struct xc_ref *ref) {
    struct xl *l = slot_for_link();
    if (!l) return NULL;
    struct xconn *x = xconn_new(n, mm->m.cfg.keepalive_s, 1);
    if (!x) return NULL;
    if (ref) x->ref = *ref;
    if (mm->discard) xconn_discard(x);
    if (!g_h.submit || g_h.submit(x, &x->done)) {
        xconn_free(x);
        return NULL;
    }
    memset(l, 0, sizeof *l);
    l->used = l->dialing = 1;
    l->x = x;
    l->cl = cl;
    l->mm = mm;
    l->used_ms = g_now;
    return l;
}

static void *conn_new(void *ud, struct xm_client *c) {
    struct xm_ctx *cx = ud;
    if (!cx->n) return NULL;
    return link_new(cx->mm, c, cx->n, cx->ref);
}

static void conn_close(void *ud, void *conn) {
    (void)ud;
    struct xl *l = conn;
    if (!l->used) return;
    l->cl = NULL;
    l->gone = 1;
    if (!l->dialing) l->x->retired = 1;
    if (idle(l)) link_free(l);
}

static void wake(uint16_t owner, int rc) {
    if (g_h.wake) g_h.wake(g_h.arg, owner, XW_DONE, rc);
}

static void done(struct transport *t, int rc) {
    struct xh_flow *f = &t->xh.f;
    if (!f->again) {
        wake(f->owner, rc);
        return;
    }
    if (rc) t->xh.err = rc;
    else f->again = 0;
    if (g_h.wake) g_h.wake(g_h.arg, f->owner, XW_READ | XW_ROOM, 0);
}

static void wait_add(struct xl *l, struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    f->wait = l->x;
    f->wnext = NULL;
    struct transport **p = &l->wait;
    while (*p) p = &(*p)->xh.f.wnext;
    *p = t;
    l->wn += need(t);
}

static void wait_del(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xl *l = find(f->wait);
    f->wait = NULL;
    if (!l) return;
    for (struct transport **p = &l->wait; *p; p = &(*p)->xh.f.wnext)
        if (*p == t) {
            *p = f->wnext;
            l->wn -= need(t);
            break;
        }
    f->wnext = NULL;
    f->post_wait = 0;
    f->get_wait = 0;
}

static struct xl *choose(struct xm_client *cl, struct xm_ctx *cx, unsigned k) {
    for (uint8_t i = 0; i < cl->conn_n; i++) {
        struct xl *l = cl->conn[i];
        if (usable(l) && room(l, k)) return l;
    }
    for (uint8_t i = 0; i < cl->conn_n; i++) {
        struct xl *l = cl->conn[i];
        if (l->dialing && !l->gone && l->wn + l->hn < XL_DIAL_SLOTS) return l;
    }
    if (cl->conn_n < XM_OVER) {
        struct xl *l = link_new(cx->mm, cl, cx->n, cx->ref);
        if (l) {
            cl->conn[cl->conn_n++] = l;
            return l;
        }
    }
    struct xl *best = NULL;
    unsigned load = 0;
    for (uint8_t i = 0; i < cl->conn_n; i++) {
        struct xl *l = cl->conn[i];
        if (!l->dialing && !usable(l)) continue;
        unsigned n = l->wn + l->hn + (l->dialing ? 0 : xconn_streams(l->x));
        if (!best || n < load) {
            best = l;
            load = n;
        }
    }
    return best;
}

static void post_use(struct xl *l, struct transport *t) {
    t->xh.f.post_x = l->x;
    l->posters++;
    l->used_ms = g_now;
}

static void post_drop(struct transport *t) {
    struct xl *l = find(t->xh.f.post_x);
    t->xh.f.post_x = NULL;
    if (!l || !l->posters) return;
    l->posters--;
    link_check(l);
}

static void post_err(struct transport *t, int rc) {
    if (!t->xh.err) t->xh.err = rc ? rc : TR_EIO;
    if (g_h.wake) g_h.wake(g_h.arg, t->xh.f.owner, XW_READ, 0);
}

static int start(struct xl *l, struct xl *d, struct transport *t) {
    l->used_ms = d->used_ms = g_now;
    if (t->xh.mode == XH_PACKET_UP) post_use(l, t);
    return xh_flow_start(t, l->x, d->x, t->xh.f.owner);
}

static void hold_on(struct xl *l, struct transport *t, int s) {
    struct xh_flow *f = &t->xh.f;
    f->hold[s] = l->x;
    f->hnext[s] = l->held;
    l->held = t;
    l->hn += need(t);
}

static struct xl *hold_off(struct transport *t, int s) {
    struct xh_flow *f = &t->xh.f;
    struct xl *l = find(f->hold[s]);
    f->hold[s] = NULL;
    if (!l) return NULL;
    for (struct transport **p = &l->held; *p; p = &(*p)->xh.f.hnext[s])
        if (*p == t) {
            *p = f->hnext[s];
            l->hn -= need(t);
            break;
        }
    f->hnext[s] = NULL;
    return l;
}

static void release(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr, *md = (struct xmm *)f->mgr_dl;
    for (int s = 0; s < 2; s++) {
        struct xl *l = hold_off(t, s);
        if (l) link_check(l);
    }
    struct xm_ctx cx = { NULL, NULL, mm };
    struct xm_env e = env(&cx);
    if (f->cl_post) xm_unref(&mm->m, f->cl_post, &e);
    if (f->cl) xm_done_running(&mm->m, f->cl, &e);
    if (f->cl_dl) xm_done_running(&md->m, f->cl_dl, &e);
    f->cl = f->cl_dl = f->cl_post = NULL;
}

static int ready(const struct xl *l) { return usable(l) && h2m_open_n(&l->x->h2) + l->hn <= h2m_cap(&l->x->h2); }

static int fresh(const struct xl *l, unsigned k) { return usable(l) && room(l, k); }

static void unpin(struct transport *t, int s) {
    struct xl *h = hold_off(t, s);
    if (h) link_check(h);
}

static int go(struct transport *t, const struct xc_ref *ref) {
    struct xh_flow *f = &t->xh.f;
    struct xc_ref r = *ref;
    unsigned k = need(t);
    struct xl *d = NULL;
    if (f->mgr_dl) {
        d = find(f->hold[1]);
        if (!d || !ready(d)) {
            unpin(t, 1);
            struct xmm *m = (struct xmm *)f->mgr_dl;
            struct xm_ctx cx = { &m->n, &r, m };
            g_pin = f->cl;
            d = choose(f->cl_dl, &cx, k);
            g_pin = NULL;
            if (!d) {
                release(t);
                return H2_EFULL;
            }
            if (!fresh(d, k)) {
                unpin(t, 0);
                wait_add(d, t);
                return XA_WAIT;
            }
            hold_on(d, t, 1);
        }
    }
    struct xl *l = find(f->hold[0]);
    if (!l) {
        unpin(t, 0);
        struct xmm *m = (struct xmm *)f->mgr;
        struct xm_ctx cx = { &m->n, &r, m };
        if (!(l = choose(f->cl, &cx, k))) {
            release(t);
            return H2_EFULL;
        }
        if (!fresh(l, k)) {
            wait_add(l, t);
            return XA_WAIT;
        }
    }
    hold_off(t, 0);
    hold_off(t, 1);
    int rc = start(l, d ? d : l, t);
    return rc ? rc : XA_READY;
}

static int place(struct transport *t, const struct xc_ref *ref) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr, *md = (struct xmm *)f->mgr_dl;
    struct xm_ctx cx = { &mm->n, ref, mm };
    struct xm_env e = env(&cx);
    struct xm_client *cl = xm_pick(&mm->m, &e);
    if (!cl) return H2_EFULL;
    xm_add_running(cl);
    f->cl = cl;
    f->wait_until = g_h.wait_ms ? g_now + g_h.wait_ms : 0;
    if (t->xh.mode == XH_STREAM_UP) xm_request(cl);
    if (t->xh.mode == XH_PACKET_UP) {
        f->cl_post = cl;
        xm_ref(cl);
    }
    if (!md) {
        xm_request(cl);
    } else {
        struct xm_ctx cd = { &md->n, ref, md };
        struct xm_env ed = env(&cd);
        struct xm_client *c = xm_pick(&md->m, &ed);
        if (!c) {
            release(t);
            return H2_EFULL;
        }
        xm_add_running(c);
        xm_request(c);
        f->cl_dl = c;
    }
    return go(t, ref);
}

static void retry(struct transport *t, const struct xc_ref *ref, int rc) {
    struct xh_flow *f = &t->xh.f;
    release(t);
    int r = rc;
    if (!f->wait_n++) {
        r = place(t, ref);
        if (r == XA_WAIT) return;
        if (r == XA_READY) r = 0;
    }
    done(t, r);
}

static void requeue(struct transport *w, const struct xc_ref *ref, int rc) {
    while (w) {
        struct transport *t = w;
        w = t->xh.f.wnext;
        t->xh.f.wait = NULL;
        t->xh.f.wnext = NULL;
        if (t->xh.f.post_wait) {
            t->xh.f.post_wait = 0;
            post_err(t, rc);
        } else if (t->xh.f.get_wait) {
            t->xh.f.get_wait = 0;
            done(t, rc);
        } else {
            retry(t, ref, rc);
        }
    }
}

static void link_dead(struct xl *l, int rc) {
    struct xconn *x = l->x;
    unwatch(l);
    if (l->cl) {
        if (x->cut_n || l->wn || l->hn) xm_failed(l->cl);
        drop_from_client(l);
    }
    struct transport *w = l->wait;
    l->wait = NULL;
    l->wn = 0;
    struct xc_ref ref = x->ref;
    if (!xconn_streams(x) && !l->posters) link_free(l);
    else l->gone = 1;
    requeue(w, &ref, rc);
}

static void serve(struct xl *l) {
    struct transport **p = &l->wait;
    while (*p && usable(l)) {
        struct transport *t = *p;
        struct xh_flow *f = &t->xh.f;
        unsigned k = need(t);
        if (h2m_open_n(&l->x->h2) + l->hn + k > h2m_cap(&l->x->h2)) {
            p = &f->wnext;
            continue;
        }
        *p = f->wnext;
        l->wn -= k;
        f->wait = NULL;
        f->wnext = NULL;
        if (f->post_wait) {
            f->post_wait = 0;
            post_use(l, t);
            continue;
        }
        if (f->get_wait) {
            f->get_wait = 0;
            l->used_ms = g_now;
            done(t, xh_get_open(t, l->x));
            p = &l->wait;
            continue;
        }
        hold_on(l, t, side(t, l));
        int r = go(t, &l->x->ref);
        if (r != XA_WAIT) done(t, r == XA_READY ? 0 : r);
        p = &l->wait;
    }
}

static void after(struct xl *l) {
    struct xconn *x = l->x;
    if (x->state == XC_DEAD) {
        link_dead(l, x->rc ? x->rc : TR_EIO);
        return;
    }
    if (x->state != XC_LIVE) return;
    if ((x->retired || x->h2.goaway) && l->cl) {
        if (x->refused) xm_failed(l->cl);
        drop_from_client(l);
        l->gone = 1;
    }
    if (l->wait && (x->retired || x->h2.goaway)) {
        struct transport *w = l->wait;
        l->wait = NULL;
        l->wn = 0;
        struct xc_ref ref = x->ref;
        requeue(w, &ref, XC_EGOAWAY);
        return;
    }
    serve(l);
    if (l->x != x) return;
    uint8_t out = xconn_wants_out(x) ? 1 : 0, in = xconn_wants_in(x) ? 1 : 0;
    if (l->watched && out == l->out && in == l->in) return;
    unsigned ev = (in ? EPOLLIN : 0u) | (out ? EPOLLOUT : 0u);
    if (g_h.watch && !g_h.watch(idx(l), x->link.fd, ev ? ev : EPOLLHUP)) {
        l->watched = 1;
        l->out = out;
        l->in = in;
        return;
    }
    xconn_fail(x, TR_EIO);
    link_dead(l, TR_EIO);
}

static void adopt(struct xl *l, uint64_t now) {
    l->dialing = 0;
    if (l->gone) {
        if (!l->wn) {
            link_free(l);
            return;
        }
        l->gone = 0;
    }
    struct xconn *x = l->x;
    if (xconn_adopt(x, now)) {
        link_dead(l, x->rc ? x->rc : TR_EIO);
        return;
    }
    l->used_ms = now;
    after(l);
    if (l->x != x || x->state != XC_LIVE) return;
    xconn_io(x, EPOLLIN, now);
    after(l);
}

static int mgr_find(const void *key) {
    for (unsigned i = 0; i < XM_MGRS; i++)
        if (g_m[i] && g_m[i]->m.key == key) return (int)i;
    return -1;
}

static int mgr_busy(const struct xmm *mm) {
    if (mm->flows) return 1;
    for (unsigned i = 0; i < XC_MAX; i++)
        if (g_l[i].used && g_l[i].mm == mm) return 1;
    return 0;
}

static void mgr_free(unsigned i) {
    struct xm_ctx cx = { NULL, NULL, g_m[i] };
    struct xm_env e = env(&cx);
    xm_retire_all(&g_m[i]->m, &e);
    for (unsigned j = 0; j < XM_MGRS; j++)
        if (g_m[j] && g_m[j]->dl == g_m[i]) g_m[j]->dl = NULL;
    free(g_m[i]);
    g_m[i] = NULL;
}

static int mgr_slot(int skip) {
    for (unsigned i = 0; i < tr_xmux_mgrs_cap; i++)
        if ((int)i != skip && !g_m[i]) return (int)i;
    for (unsigned i = 0; i < tr_xmux_mgrs_cap; i++)
        if ((int)i != skip && !mgr_busy(g_m[i])) return (int)i;
    return -1;
}

static struct xmm *mgr_get(int found, int slot, const void *key, const struct xm_cfg *xmux,
                           const struct tr_node *n, int discard) {
    if (found >= 0) return g_m[found];
    if (g_m[slot]) mgr_free((unsigned)slot);
    struct xmm *mm = calloc(1, sizeof *mm);
    if (!mm) return NULL;
    struct xm_cfg eff;
    xm_cfg_effective(xmux, &eff);
    struct xm_ctx cx = { NULL, NULL, mm };
    struct xm_env e = env(&cx);
    xm_mgr_init(&mm->m, key, &eff, &e);
    mm->n = *n;
    mm->discard = (uint8_t)discard;
    g_m[slot] = mm;
    return mm;
}

int tr_xmux_shares(const struct tr_node *n) {
    return n->type && !strcmp(n->type, "xhttp") && !(n->encryption && n->encryption[0]);
}

int tr_xmux_attach(struct transport *t, const struct tr_node *n, const void *key, const void *dl_key,
                   uint16_t owner, const struct xc_ref *ref) {
    if (!tr_xmux_shares(n)) return XA_NONE;
    int fa = mgr_find(key), fb = n->dl ? mgr_find(dl_key) : -1;
    int sa = fa >= 0 ? fa : mgr_slot(fb);
    if (sa < 0) return XA_NONE;
    int sb = !n->dl || fb >= 0 ? fb : mgr_slot(sa);
    if (n->dl && sb < 0) return XA_NONE;
    int rc = xh_attach(t, n, owner);
    if (rc) return rc;
    struct tr_node mn = *n;
    mn.dl = NULL;
    struct xmm *mm = mgr_get(fa, sa, key, &t->xh.main_side.opts.xmux, &mn, n->dl != NULL), *md = NULL;
    if (!mm) return TR_EIO;
    if (n->dl && !(md = mgr_get(fb, sb, dl_key, &t->xh.down_side.opts.xmux, n->dl, 0))) return TR_EIO;
    t->xh.f.mgr = &mm->m;
    mm->flows++;
    if (ref) {
        mm->ref = *ref;
        mm->has_ref = 1;
    }
    if (md) {
        mm->dl = md;
        t->xh.f.mgr_dl = &md->m;
        md->flows++;
    }
    return place(t, ref);
}

static void flow_close(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    xh_posts_drop(t);
    struct xs *st[3 + XH_POST_MAX] = { &f->one, &f->down, &f->up };
    unsigned n = 3;
    for (unsigned i = 0; i < f->post_n; i++) st[n++] = &f->post[i].s;
    struct xconn *xs[3 + XH_POST_MAX];
    unsigned k = 0;
    for (unsigned i = 0; i < n; i++) {
        struct xconn *x = st[i]->x;
        unsigned j = 0;
        while (j < k && xs[j] != x) j++;
        if (x && j == k) xs[k++] = x;
        xs_close(st[i]);
    }
    f->post_n = 0;
    post_drop(t);
    bat_off(t);
    for (unsigned i = 0; i < k; i++) {
        struct xl *l = find(xs[i]);
        if (!l) continue;
        l->used_ms = g_now;
        link_check(l);
    }
}

void tr_xmux_detach(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr, *md = (struct xmm *)f->mgr_dl;
    if (f->wait) wait_del(t);
    const struct xs *rd = t->xh.mode == XH_STREAM_ONE ? &f->one : &f->down;
    struct xm_client *rcl = md ? f->cl_dl : f->cl;
    if (rcl && f->end == H2_ERESET && rd->x && !xs_status(rd)) xm_failed(rcl);
    flow_close(t);
    if (mm) {
        release(t);
        mm->flows--;
    }
    if (md) md->flows--;
    f->cl = f->cl_dl = f->cl_post = NULL;
    f->mgr = f->mgr_dl = NULL;
    f->post_dec = 0;
    f->shared = 0;
}

static int post_link(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xl *l = find(f->post_x);
    if (l && usable(l)) return 0;
    if (t->xh.err) return t->xh.err < 0 ? t->xh.err : H2_ESTATUS;
    const struct xconn *g = f->down.x;
    if (!g) return TR_EIO;
    if (g->state != XC_LIVE) return g->rc ? g->rc : TR_EIO;
    post_drop(t);
    struct xmm *mm = (struct xmm *)f->mgr;
    struct xc_ref ref = g->ref;
    struct xm_ctx cx = { &mm->n, &ref, mm };
    l = choose(f->cl_post, &cx, 1);
    if (!l) return H2_EFULL;
    if (usable(l) && room(l, 1)) {
        post_use(l, t);
        return 0;
    }
    f->post_wait = 1;
    f->wait_until = g_h.wait_ms ? g_now + g_h.wait_ms : 0;
    wait_add(l, t);
    return H2_EWINDOW;
}

static int post_move(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr;
    struct xm_ctx cx = { &mm->n, &f->down.x->ref, mm };
    struct xm_env e = env(&cx);
    struct xm_client *c = xm_pick(&mm->m, &e);
    if (!c) return H2_EFULL;
    xm_ref(c);
    struct xm_client *old = f->cl_post;
    f->cl_post = c;
    post_drop(t);
    xm_unref(&mm->m, old, &e);
    return post_link(t);
}

static int post_decide(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    if (!f->cl_post) return TR_EIO;
    if (!f->post_dec) {
        f->post_dec = 1;
        if (xm_post_moves(f->cl_post, xm_request(f->cl_post), g_now)) return post_move(t);
    }
    return f->wait ? H2_EWINDOW : post_link(t);
}

static size_t bat_cap(const struct transport *t) {
    size_t c = t->xh.post_max ? t->xh.post_max : XL_POST_MAX;
    return c > XL_BAT_MIN ? c : XL_BAT_MIN;
}

static void bat_on(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    if (f->blisted) return;
    f->blisted = 1;
    f->bnext = g_bat;
    g_bat = t;
}

static void bat_drop(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    g_bat_used -= f->bat.n;
    xc_q_drop(&f->bat);
}

static size_t bat_free(void) { return g_bat_used < tr_xmux_up_budget ? tr_xmux_up_budget - g_bat_used : 0; }

static size_t bat_lim(const struct transport *t) {
    size_t cap = bat_cap(t), lim = t->xh.f.blim > XL_RES_KEEP ? t->xh.f.blim : XL_RES_KEEP;
    return lim < cap ? lim : cap;
}

static void bat_grow(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    int up = f->blim > XL_RES_KEEP;
    size_t cur = up ? f->blim : XL_RES_KEEP;
    unsigned n = g_up_n + (up ? 0u : 1u);
    size_t share = tr_xmux_up_budget / (n ? n : 1u), next = cur * 2;
    if (share < XL_RES_KEEP) share = XL_RES_KEEP;
    if (next > share) next = share;
    if (next > bat_cap(t)) next = bat_cap(t);
    if (next <= cur) return;
    if (!up) g_up_n++;
    f->blim = next;
}

static void bat_shrink(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    if (f->blim > XL_RES_KEEP && g_up_n) g_up_n--;
    f->blim = 0;
    if (f->bres <= XL_RES_KEEP) return;
    g_bat_used -= f->bres - XL_RES_KEEP;
    f->bres = XL_RES_KEEP;
}

static size_t bat_reserve(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    size_t lim = bat_lim(t);
    size_t un = f->bat.n - f->feed;
    size_t want = lim > un ? lim - un : 0;
    if (want > f->bres) {
        size_t add = want - f->bres, free = bat_free();
        if (add > free) add = free;
        f->bres += add;
        g_bat_used += add;
    }
    return f->bres;
}

static void bat_wakes(void) {
    for (struct transport *t = g_bat; t; t = t->xh.f.bnext) {
        struct xh_flow *f = &t->xh.f;
        if (!f->bstarved || !bat_reserve(t)) continue;
        f->bstarved = 0;
        if (g_h.wake) g_h.wake(g_h.arg, f->owner, XW_ROOM, 0);
    }
}

static void bat_off(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    size_t freed = f->bat.n + f->bres;
    bat_drop(t);
    g_bat_used -= f->bres;
    f->bres = 0;
    for (struct transport **p = &g_bat; f->blisted && *p; p = &(*p)->xh.f.bnext)
        if (*p == t) {
            *p = f->bnext;
            break;
        }
    f->bnext = NULL;
    f->blisted = f->bstarved = 0;
    if (f->blim > XL_RES_KEEP && g_up_n) g_up_n--;
    f->blim = 0;
    if (freed) bat_wakes();
}

long tr_xmux_bat_room(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    if (bat_reserve(t)) return (long)f->bres;
    f->bstarved = 1;
    bat_on(t);
    return 0;
}

int tr_xmux_batch(struct transport *t, const unsigned char *d, size_t n) {
    struct xh_flow *f = &t->xh.f;
    size_t take = n < f->bres ? n : f->bres;
    if (f->bat.n - f->feed + n > bat_cap(t) || n - take > bat_free()) return H2_EWINDOW;
    if (xc_q_push(&f->bat, d, n)) return TR_EIO;
    f->bres -= take;
    g_bat_used += n - take;
    f->bw_at = g_now;
    bat_on(t);
    return 0;
}

size_t tr_xmux_bat_used(void) { return g_bat_used; }

static void bat_flush(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    for (;;) {
        if (f->feed) {
            size_t had = f->bat.n;
            int fr = xh_post_feed(t);
            g_bat_used -= had - f->bat.n;
            if (fr < 0) {
                post_err(t, fr);
                return;
            }
            if (f->feed) return;
        }
        if (!(f->bat.n || f->redo_n) || g_now < f->post_at) return;
        int rc = t->xh.err ? t->xh.err : post_decide(t);
        if (rc == H2_EWINDOW) return;
        size_t before = f->bat.n;
        if (!rc) rc = xh_post_flush(t);
        g_bat_used -= before - f->bat.n;
        if (!rc) return;
        if (rc < 0) {
            post_err(t, rc);
            return;
        }
        if ((size_t)rc * 2 >= bat_lim(t)) bat_grow(t);
        f->post_dec = 0;
        struct xm_range iv = t->xh.main_side.opts.interval;
        f->post_at = iv.from > 0 ? g_now + (uint64_t)xm_draw(iv, tr_xmux_rnd) : 0;
        if (f->feed) return;
    }
}

static int bats(void) {
    uint64_t due = 0;
    struct transport *h = g_bat;
    size_t hn = h ? h->xh.f.bat.n : 0;
    uint64_t hs = h ? h->xh.seq : 0;
    unsigned hr = h ? h->xh.f.redo_n : 0;
    for (struct transport *t = g_bat; t; t = t->xh.f.bnext) {
        bat_flush(t);
        if (t->xh.f.blim > XL_RES_KEEP && g_now >= t->xh.f.bw_at + XL_RES_IDLE_MS) bat_shrink(t);
    }
    bat_wakes();
    struct transport **p = &g_bat;
    while (*p) {
        struct xh_flow *f = &(*p)->xh.f;
        if (!f->bat.n && !f->redo_n && !f->bstarved && f->blim <= XL_RES_KEEP) {
            *p = f->bnext;
            f->bnext = NULL;
            f->blisted = 0;
            continue;
        }
        if ((f->bat.n || f->redo_n) && f->post_at > g_now && (!due || f->post_at - g_now < due)) due = f->post_at - g_now;
        uint64_t idle_at = f->bw_at + XL_RES_IDLE_MS;
        if (f->blim > XL_RES_KEEP && idle_at > g_now && (!due || idle_at - g_now < due)) due = idle_at - g_now;
        p = &f->bnext;
    }
    if (g_bat == h && h && h->xh.f.bnext && (h->xh.f.bat.n != hn || h->xh.seq != hs || h->xh.f.redo_n < hr)) {
        *p = g_bat;
        g_bat = g_bat->xh.f.bnext;
        (*p)->xh.f.bnext = NULL;
    }
    return due > INT32_MAX ? INT32_MAX : (int)due;
}

void tr_xmux_post_failed(const struct xconn *x) {
    struct xl *l = find(x);
    if (l && l->cl) xm_failed(l->cl);
}

void tr_xmux_host(const struct mux_host *h, int (*stale)(const struct xc_ref *r)) {
    if (h) g_h = *h;
    else memset(&g_h, 0, sizeof g_h);
    g_stale = stale;
    struct xc_host xh = { g_h.wake, g_h.arg };
    xc_set_host(&xh);
    tr_xmux_up_budget = XL_UP_BUDGET / (g_h.loops ? g_h.loops : 1);
    xc_pend_set(XC_PEND_MAX / (g_h.loops ? g_h.loops : 1));
    unsigned loops = g_h.loops ? g_h.loops : 1;
    xc_budget_set(XC_RX_BUDGET / loops, XC_RX_HARD / loops, XC_RX_RESUME / loops);
}

int tr_xmux_dial(void *link, int timeout_s) { return xconn_dial(link, timeout_s); }

int tr_xmux_event(unsigned i, uint32_t ev) {
    if (i >= XC_MAX) return 0;
    struct xl *l = &g_l[i];
    if (!l->used || l->dialing || l->x->state != XC_LIVE) return 0;
    xconn_io(l->x, ev, g_now);
    after(l);
    return 0;
}

static int shared_live(const struct xmm *d) {
    for (unsigned i = 0; i < XM_MGRS; i++)
        if (g_m[i] && g_m[i]->dl == d && g_m[i]->has_ref && !g_stale(&g_m[i]->ref)) return 1;
    return 0;
}

static void deadlines(uint64_t now) {
    for (unsigned i = 0; i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (!l->used || !l->wait) continue;
        struct transport *due = NULL, **tail = &due;
        for (struct transport **p = &l->wait; *p;) {
            struct transport *t = *p;
            struct xh_flow *f = &t->xh.f;
            if (!f->wait_until || now < f->wait_until) {
                p = &f->wnext;
                continue;
            }
            *p = f->wnext;
            l->wn -= need(t);
            f->wnext = NULL;
            *tail = t;
            tail = &f->wnext;
        }
        if (!due) continue;
        struct xc_ref ref = l->x->ref;
        requeue(due, &ref, TR_ECONNTIMEOUT);
    }
}

static void tick(uint64_t now) {
    deadlines(now);
    for (unsigned i = 0; i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (!l->used || l->dialing || l->x->state != XC_LIVE) continue;
        if (xconn_tick(l->x, now) == XC_IDLE && idle(l)) {
            drop_from_client(l);
            link_free(l);
            continue;
        }
        after(l);
    }
    for (unsigned i = 0; g_stale && i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (l->used && l->cl && !l->cl->not_used && g_stale(&l->x->ref) && !shared_live(l->mm)) {
            struct xm_ctx cx = { NULL, NULL, l->mm };
            struct xm_env e = env(&cx);
            xm_retire_all(&l->mm->m, &e);
        }
    }
}

static void teardown(void) {
    for (unsigned i = 0; i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (!l->used || (l->dialing && !__atomic_load_n(&l->x->done, __ATOMIC_ACQUIRE))) continue;
        drop_from_client(l);
        link_free(l);
    }
    for (unsigned i = 0; i < XM_MGRS; i++)
        if (g_m[i]) mgr_free(i);
    g_ticked = 0;
    g_bat = NULL;
    g_bat_used = 0;
    g_up_n = 0;
}

int tr_xmux_pass(uint64_t now, int phase) {
    g_now = now;
    if (phase == MUXP_EXIT) {
        teardown();
        return 0;
    }
    for (unsigned i = 0; phase == MUXP_LATE && i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (l->used && !l->dialing && l->x->state == XC_LIVE) after(l);
    }
    int due = phase == MUXP_LATE ? bats() : 0;
    uint64_t mnow = 0;
    for (unsigned i = 0; i < XC_MAX; i++) {
        struct xl *l = &g_l[i];
        if (!l->used) continue;
        if (l->dialing) {
            if (phase == MUXP_EARLY && __atomic_load_n(&l->x->done, __ATOMIC_ACQUIRE)) adopt(l, now);
            continue;
        }
        if (l->x->state != XC_LIVE) continue;
        l->x->now_ms = now;
        if (phase == MUXP_LATE) xconn_flush(l->x);
        after(l);
        if (phase != MUXP_LATE || !l->used || l->x->state != XC_LIVE || !l->x->out_due) continue;
        if (!mnow) mnow = tr_mono_ms();
        int d = xconn_due(l->x, mnow);
        if (d >= 0 && (!due || (d ? d : 1) < due)) due = d ? d : 1;
    }
    if (phase == MUXP_LATE && (!g_ticked || now >= g_tick + XL_TICK_MS)) {
        g_ticked = 1;
        g_tick = now;
        tick(now);
    }
    if (phase == MUXP_LATE) reap();
    return due;
}

int tr_xmux_refused(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr;
    const struct xs *rd = t->xh.mode == XH_STREAM_ONE ? &f->one : &f->down;
    if (f->wait_n || !rd->x) return XC_EREFUSED;
    struct xc_ref ref = mm->has_ref ? mm->ref : rd->x->ref;
    flow_close(t);
    release(t);
    f->wait_n++;
    f->again = 1;
    int r = place(t, &ref);
    if (r == XA_READY) {
        f->again = 0;
        if (g_h.wake) g_h.wake(g_h.arg, f->owner, XW_ROOM, 0);
        return 0;
    }
    return r == XA_WAIT ? 0 : r;
}

int tr_xmux_reget(struct transport *t) {
    struct xh_flow *f = &t->xh.f;
    struct xmm *mm = (struct xmm *)f->mgr, *md = (struct xmm *)f->mgr_dl;
    if (f->wait_n || f->wait || !f->down.x) return XC_EREFUSED;
    struct xc_ref ref = f->down.x->ref;
    struct xl *old = find(f->down.x);
    xs_close(&f->down);
    if (old) link_check(old);
    f->wait_n++;
    struct xmm *m = md ? md : mm;
    struct xm_client *cl = md ? f->cl_dl : f->cl;
    struct xm_ctx cx = { &m->n, &ref, m };
    struct xl *l = cl ? choose(cl, &cx, 1) : NULL;
    if (!l) return XC_EREFUSED;
    if (fresh(l, 1)) {
        l->used_ms = g_now;
        return xh_get_open(t, l->x);
    }
    f->again = 1;
    f->get_wait = 1;
    f->wait_until = g_h.wait_ms ? g_now + g_h.wait_ms : 0;
    wait_add(l, t);
    return 0;
}

void tr_xmux_redo(struct transport *t) { bat_on(t); }

int tr_xmux_cut(const struct transport *t) {
    int e = t->xh.f.end;
    if (!t->xh.f.shared || !e || e == H2_ERESET || e == H2_ESTATUS) return 0;
    return e == XC_EGOAWAY || e == XC_EREFUSED ? 2 : 1;
}

int tr_xmux_busy(void) {
    int n = 0;
    for (unsigned i = 0; i < XC_MAX; i++)
        n += g_l[i].used && g_l[i].dialing && !__atomic_load_n(&g_l[i].x->done, __ATOMIC_ACQUIRE);
    return n;
}

void tr_xmux_dropped(void *link) {
    struct xl *l = find(link);
    if (!l) return;
    struct transport *w = l->wait;
    l->wait = NULL;
    l->wn = 0;
    drop_from_client(l);
    link_free(l);
    while (w) {
        struct transport *t = w;
        w = t->xh.f.wnext;
        t->xh.f.wait = NULL;
        t->xh.f.wnext = NULL;
        if (t->xh.f.post_wait) {
            t->xh.f.post_wait = 0;
            post_err(t, TR_ECLOSED);
        } else {
            t->xh.f.get_wait = 0;
            done(t, TR_ECLOSED);
        }
    }
}
