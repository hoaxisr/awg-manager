#ifndef STEER_TRXMUX_H
#define STEER_TRXMUX_H
#include <stdint.h>

#include "dialer.h"
#include "transport.h"

#define XM_MGRS 16

int  tr_xmux_shares(const struct tr_node *n);
int  tr_xmux_attach(struct transport *t, const struct tr_node *n, const void *key, const void *dl_key,
                    uint16_t owner, const struct xc_ref *ref);
void tr_xmux_host(const struct mux_host *h, int (*stale)(const struct xc_ref *r));
int  tr_xmux_dial(void *link, int timeout_s);
int  tr_xmux_event(unsigned idx, uint32_t ev);
int  tr_xmux_pass(uint64_t now_ms, int phase);
int  tr_xmux_cut(const struct transport *t);
int  tr_xmux_refused(struct transport *t);
int  tr_xmux_reget(struct transport *t);
void tr_xmux_redo(struct transport *t);
int  tr_xmux_busy(void);
void tr_xmux_dropped(void *link);
void tr_xmux_detach(struct transport *t);
int  tr_xmux_batch(struct transport *t, const unsigned char *d, size_t n);
long tr_xmux_bat_room(struct transport *t);
void tr_xmux_post_failed(const struct xconn *x);

#endif
