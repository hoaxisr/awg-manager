#include <stdlib.h>
#include <string.h>

#include "xmux.h"

int32_t xm_draw(struct xm_range r, uint32_t (*rnd)(uint32_t n)) {
    int64_t d = (int64_t)r.to - r.from;
    if (d <= 1) return r.from;
    return (int32_t)(r.from + (int64_t)rnd((uint32_t)d));
}

void xm_mgr_init(struct xm_mgr *m, const void *key, const struct xm_cfg *effective, const struct xm_env *e) {
    memset(m, 0, sizeof *m);
    m->key = key;
    m->cfg = *effective;
    m->concurrency = xm_draw(m->cfg.max_conc, e->rnd);
    m->connections = xm_draw(m->cfg.max_conns, e->rnd);
}

static void maybe_close(struct xm_client *c, const struct xm_env *e) {
    if (!c->not_used || c->running > 0 || c->refs > 0) return;
    for (uint8_t i = 0; i < c->conn_n; i++) e->conn_close(e->ud, c->conn[i]);
    free(c);
}

static struct xm_client *create(struct xm_mgr *m, const struct xm_env *e) {
    struct xm_client *c = calloc(1, sizeof *c);
    if (!c) return NULL;
    c->conn[0] = e->conn_new(e->ud, c);
    if (!c->conn[0]) {
        free(c);
        return NULL;
    }
    c->conn_n = 1;
    int32_t x = xm_draw(m->cfg.c_reuse, e->rnd);
    c->left_usage = x > 0 ? x - 1 : -1;
    x = xm_draw(m->cfg.h_reqs, e->rnd);
    c->left_requests = x > 0 ? x : INT32_MAX;
    x = xm_draw(m->cfg.h_secs, e->rnd);
    if (x > 0) c->unreusable_at_ms = e->now_ms + (uint64_t)x * 1000;
    m->c[m->n++] = c;
    return c;
}

static int used_up(const struct xm_client *c, uint64_t now_ms) {
    return c->closed || !c->left_usage || c->left_requests <= 0 ||
           (c->unreusable_at_ms && now_ms > c->unreusable_at_ms);
}

struct xm_client *xm_pick(struct xm_mgr *m, const struct xm_env *e) {
    uint8_t k = 0;
    for (uint8_t i = 0; i < m->n; i++) {
        struct xm_client *c = m->c[i];
        if (used_up(c, e->now_ms)) {
            c->not_used = 1;
            maybe_close(c, e);
        } else {
            m->c[k++] = c;
        }
    }
    m->n = k;
    if (m->connections > 0 && m->n < m->connections && m->n < XM_CLIENTS) return create(m, e);
    struct xm_client *free_c[XM_CLIENTS], *c;
    uint32_t cnt = 0;
    for (uint8_t i = 0; i < m->n; i++)
        if (m->concurrency <= 0 || m->c[i]->running < m->concurrency) free_c[cnt++] = m->c[i];
    if (cnt) {
        c = free_c[e->rnd(cnt)];
    } else if (m->n < XM_CLIENTS) {
        return create(m, e);
    } else {
        c = m->c[0];
        for (uint8_t i = 1; i < m->n; i++)
            if (m->c[i]->running < c->running) c = m->c[i];
    }
    if (c->left_usage > 0) c->left_usage--;
    return c;
}

void xm_add_running(struct xm_client *c) {
    c->running++;
}

void xm_ref(struct xm_client *c) {
    c->refs++;
}

void xm_done_running(struct xm_mgr *m, struct xm_client *c, const struct xm_env *e) {
    (void)m;
    c->running--;
    maybe_close(c, e);
}

int32_t xm_request(struct xm_client *c) {
    return --c->left_requests;
}

int xm_post_moves(const struct xm_client *c, int32_t left_after, uint64_t now_ms) {
    return left_after <= 0 || (c->unreusable_at_ms && now_ms > c->unreusable_at_ms);
}

void xm_failed(struct xm_client *c) {
    c->closed = 1;
}

void xm_unref(struct xm_mgr *m, struct xm_client *c, const struct xm_env *e) {
    (void)m;
    c->refs--;
    maybe_close(c, e);
}

void xm_retire_all(struct xm_mgr *m, const struct xm_env *e) {
    for (uint8_t i = 0; i < m->n; i++) {
        m->c[i]->not_used = 1;
        maybe_close(m->c[i], e);
    }
    m->n = 0;
}
