/* VLESS dialer for the tunnel stack (dialer.h). Details in vldial.c. */
#ifndef STEER_VLDIAL_H
#define STEER_VLDIAL_H
#include <stdint.h>
#include "vless.h"
#include "dialer.h"
#include "transport.h"
#include "vision.h"
#include "stack.h"
#include "pool.h"

/* The VLESS session of one client connection: the flow state and the link to the node.
 *
 * Field order matters. Everything the stack touches on every slot release and at table setup
 * (dialer_ops.clear), the counters, flags and the link's descriptor, lies in the first few
 * hundred bytes, on one page. The TLS buffers inside the link and the datagram buffer come
 * later: their pages are touched only by connections that actually do I/O. */
struct vl_sess {
    /* ---- flow: VLESS and Vision ---- */
    uint8_t header_sent;       /* the VLESS request header has been sent */
    uint8_t established;       /* the VLESS response header has been stripped */
    uint8_t v6;
    /* ---- UDP datagram assembly (UDP flows only) ----
     *
     * VLESS carries datagrams as a stream: [length(2)][data], repeated. TLS records know
     * nothing of these boundaries (a datagram may come in two records, one record may bring
     * one and a half), so a partial datagram is kept between reads.
     *
     * dg_want == 0 means "waiting for the length". lenb holds the first length byte when a
     * record ended exactly between the two: rare, but losing it desyncs the stream for good.
     *
     * dg_skip is how many bytes of an oversized datagram are left to discard. It must be
     * discarded whole and exactly: stopping early would read its tail as the next length. */
    unsigned char lenb, lenb_n;
    uint16_t dg_want, dg_have;
    uint32_t dg_skip;
    /* The node's parsed UUID, needed once per connection: in the VLESS request header and to
     * set up Vision. */
    unsigned char uuid[16];
    struct vision vis;
    /* ---- XUDP frame parsing (UDP with vision only, see xudp_downstream in vldial.c) ----
     * xs: what is being read now (XS_*); xneed: the frame's metadata length; xdiscard: the
     * data of a KeepAlive frame is read and discarded. Two-byte lengths use lenb/lenb_n, the
     * buffer is dg. */
    uint8_t xs, xdiscard;
    uint16_t xneed;
    char host[256];
    int why_rc;
    char why[160];
    /* ---- link to the node ---- */
    struct transport t;
    unsigned char dg[UDP_DGRAM_MAX];
};

extern const struct dialer_ops vless_dialer;

/* Parse trace (STEER_TUN_TRACE): the same switch as the stack's, to the same log stream. */
void vl_set_trace(int on);

/* Bring the tunnel up over the node pool pc (src/tunnel/pool.h) with this dialer as its protocol:
 * first the check that cannot wait for the first connection (the UUID of pc->first when it is a
 * VLESS node), then
 * pool_run. Returns the process exit code, never 0: the loop has no successful exit. */
int vless_tunnel_run(const struct tun_cfg *tc, const struct pool_cfg *pc, stack_ready_fn ready,
                     void *arg);

#endif
