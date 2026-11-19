#ifndef STEER_XMUX_H
#define STEER_XMUX_H
#include <stdint.h>

#include "xhopts.h"

#define XM_CLIENTS 16
#define XM_OVER    4

struct xm_client {
    void *conn[XM_OVER];
    uint8_t conn_n, closed, not_used;
    int32_t running, left_usage, left_requests;
    uint64_t unreusable_at_ms;
    int32_t refs;
};

struct xm_env {
    uint32_t (*rnd)(uint32_t n);
    uint64_t now_ms;
    void *ud;
    void *(*conn_new)(void *ud, struct xm_client *c);
    void (*conn_close)(void *ud, void *conn);
};

struct xm_mgr {
    const void *key;
    struct xm_cfg cfg;
    int32_t concurrency, connections;
    struct xm_client *c[XM_CLIENTS];
    uint8_t n;
};

int32_t xm_draw(struct xm_range r, uint32_t (*rnd)(uint32_t n));
void xm_mgr_init(struct xm_mgr *m, const void *key, const struct xm_cfg *effective, const struct xm_env *e);
struct xm_client *xm_pick(struct xm_mgr *m, const struct xm_env *e);
void xm_add_running(struct xm_client *c);
void xm_ref(struct xm_client *c);
void xm_done_running(struct xm_mgr *m, struct xm_client *c, const struct xm_env *e);
int32_t xm_request(struct xm_client *c);
int xm_post_moves(const struct xm_client *c, int32_t left_after, uint64_t now_ms);
void xm_failed(struct xm_client *c);
void xm_unref(struct xm_mgr *m, struct xm_client *c, const struct xm_env *e);
void xm_retire_all(struct xm_mgr *m, const struct xm_env *e);

#endif
