#ifndef TUNVLESS_HY2DIAL_H
#define TUNVLESS_HY2DIAL_H
#include <stddef.h>
#include <stdint.h>

#include "dialer.h"

#define HY2_ELINKS   (-80)
#define HY2_EDIAL    (-81)
#define HY2_ENOUDP   (-82)

struct hy2_rxc;

struct hy2_sess {
    struct hy2_sess *prev, *next;
    struct hy2_rxc *head, *tail, *pre;
    int64_t sid;
    uint32_t bytes, lgen, usid;
    int32_t why_rc;
    uint16_t owner, port, dg_n;
    uint8_t st, li, how, said, fin_seen, up_stopped, want_stream, udp, gone;
    char host[256];
    char why[160];
};

_Static_assert(sizeof(struct hy2_sess) <= 512, "a Hysteria2 session must fit the space the pool zeroes");

extern const struct dialer_ops hy2_dialer;
extern const struct dialer_mux hy2_mux;

#ifdef QC_WITH_SERVER
struct hy2c;
size_t hy2d_rx_held(const struct hy2_sess *s);
struct hy2c *hy2d_link_conn(unsigned i);
unsigned long hy2d_udp_in(void);
int hy2d_link_dead(const struct hy2_sess *s);
void hy2d_loss_said_reset(void);
#endif

#endif
