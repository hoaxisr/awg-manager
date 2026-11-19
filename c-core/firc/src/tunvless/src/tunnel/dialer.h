/* Dialer: the protocol the tunnel stack (stack.c) uses to carry the client's TCP and UDP flows to a
 * node.
 *
 * The stack knows the TUN device, the client's connections (SYN-ACK, window, retransmits, early
 * data, fragment reassembly) and the connector threads. It knows nothing of the protocol: each
 * client connection has an opaque dialer session of sess_size bytes, and for everything about the
 * node (connecting, the request header, framing, parsing the answer) the stack calls this table.
 * The one dialer is VLESS (proto/vless/vldial.c); another protocol would be another dialer, not a
 * change to the stack.
 *
 * The boundary follows what the stack knows, not what suits VLESS, so the table has no "header" and
 * no Vision. send gets the client's bytes and the flow key and decides what to put in front;
 * deliver hands the stack the bare payload through emit: stream pieces for TCP, whole datagrams for
 * UDP. One session per client connection holds both the link to the node and the protocol state of
 * the flow; where one ends and the other begins is the dialer's business (see take).
 *
 * The table has more than VLESS needs: DC_UDP_OWN, DC_ACK_PACED and rcv_wnd_max serve dialers with
 * their own UDP sockets or a bounded send queue, and no dialer here sets them. The node questions
 * (peer_of, match, stale, lost) are answered by the node pool (pool.c), which wraps the dialer.
 *
 * The stack keeps one session per flow (see udp_packet in stack.c). A dialer with mux may carry
 * several of them as streams on one shared link the loop thread owns (struct dialer_mux); without
 * it every flow has a link of its own. For a node with Vision the VLESS dialer does frame UDP as
 * Mux.Cool/XUDP, but still with one flow per link.
 */
#ifndef STEER_DIALER_H
#define STEER_DIALER_H
#include <stddef.h>
#include <stdint.h>
#include <time.h>
#include "tun.h"

/* How many bytes the stack reads from a dialer at once and how much room it gives a send.
 *
 * At least a whole TLS record: transports over HTTP/2 return a record's full contents in one read,
 * and a smaller buffer would fail on a perfectly valid frame. Plus room for the request header and
 * Vision padding, which adds up to about 1400 bytes to the data. Xray happens to write in 8 KB
 * buffers, but TLS 1.3 lets a server send 16384-byte records at any time.
 *
 * A number, not an expression of H2_MIN_READ_CAP: the stack knows nothing of HTTP/2, and a dialer
 * that needs more checks it at build time (_Static_assert in vldial.c). The client window reserve
 * (CLIENT_ROOM_RESERVE in stack.c) is derived from it, so changing it changes flow control, not
 * just a buffer size. */
#define TUNNEL_BUF (16384 + 16 + 2048)

/* Result of a send: sent, "not now, try again", or broken. The middle one exists for HTTP/2, where
 * a closed flow-control window is normal, and treating it as a failure would cut a working
 * connection. */
#define SEND_OK    0
#define SEND_AGAIN 1
#define SEND_FATAL (-1)

enum dialer_cap {
    /* The link to the node can be set up BEFORE the destination is known: VLESS sends the address
     * in the request header with the first data, and until then the link belongs to nobody. Only
     * then does the stack keep spare sessions (spare_* in stack.c). */
    DC_PRECONNECT = 1 << 0,
    /* A UDP flow's link is the dialer's own UDP socket to the node, not a TCP stream. A spare
     * session holds a TCP stream, so a UDP flow never takes one; a connector thread opens its link
     * (connect after flow_open with udp). send gets one datagram framed by dgram_frame per call,
     * early data included (early_flush sends held datagrams one by one), so each can go out as its
     * own UDP datagram. Without DC_PRECONNECT the bit changes nothing: there are no spares. */
    DC_UDP_OWN = 1 << 1,
    /* Sending to the node goes through a BOUNDED queue (for example a SOCK_SEQPACKET pair to a
     * multiplexer that encrypts and sends), and its fill level is the only flow control towards
     * the node. The stack then does not acknowledge client data until the session fd is writable
     * and waits for that in epoll (EPOLLOUT); the ACK goes out once the queue drains to its low
     * mark.
     *
     * Without this, the ACK goes out as soon as data is queued, the client sends far faster than
     * the queue drains, and send returns SEND_AGAIN. On SEND_AGAIN the stack neither acknowledges
     * nor advances, so every later segment of that window arrives out of order and is dropped (no
     * reorder buffer, no SACK, one duplicate ACK per batch), and the client falls back to RTO
     * retransmits, one segment each.
     * Pacing the ACK by the queue makes the client window the room in the queue, with no loss.
     *
     * The dialer's queue must hold the client window above the low mark (for a unix socket a
     * quarter of the send buffer), or what the client sends before the next ACK still hits
     * SEND_AGAIN. The stack's window is sized by the machine's memory (rcv_window_init: up to
     * megabytes), so such a dialer sets rcv_wnd_max below. No effect on UDP: datagrams have no
     * ACKs. */
    DC_ACK_PACED = 1 << 2,
};

/* Hand the stack payload: a piece of the TCP stream or a whole UDP datagram. Returns 0 when taken;
 * otherwise the connection ends (the device write failed, or the retransmit ring could not be
 * allocated or is full). */
typedef int (*dialer_emit_fn)(void *arg, const unsigned char *p, size_t n);

enum { XA_NONE = 0, XA_READY = 1, XA_WAIT = 2 };
enum { XW_READ = 1, XW_ROOM = 2, XW_DONE = 4 };
enum { MUXP_EARLY = 0, MUXP_LATE = 1, MUXP_EXIT = 2 };

/* Connections per loop thread. Browsers keep connections alive between requests: with a wide set
 * of routes into the tunnel, two open sites filled a table of 64. An idle slot costs little (see
 * the connection table in stack.c and RTX_START). When the table is full, conn_new evicts an idle
 * connection or refuses the SYN and logs it, rather than growing until the OOM killer comes. */
#define MAX_CONNS 320

#define XC_MAX 64
#define MUX_HY2_LINKS 32
#define MUX_IDX_MAX (XC_MAX + MUX_HY2_LINKS)
#define DIAL_NOSPARE 1

struct xc_ref { const void *node; int slot; unsigned gen; };

struct mux_host {
    int (*submit)(void *link, int *done);
    int (*watch)(unsigned idx, int fd, unsigned ev);
    void (*wake)(void *arg, uint16_t owner, unsigned what, int rc);
    void *arg;
    unsigned loops;
    unsigned wait_ms;
    unsigned idx_base;
    void (*seen)(const struct xc_ref *r, int rc, uint64_t started_ms);
    int (*stale)(const struct xc_ref *r);
    void (*fd_extra)(int delta);
};

struct dialer_mux {
    void (*host)(const struct mux_host *h);
    int  (*attach)(const void *ctx, void *sess, uint16_t owner, const struct xc_ref *ref);
    int  (*dial)(const void *ctx, void *link, int timeout_s);
    int  (*event)(unsigned idx, uint32_t ev);
    int  (*pass)(uint64_t now_ms, int phase);
    int  (*cut)(const void *sess);
    int  (*busy)(void);
    void (*dropped)(void *link);
    int  (*shares)(const void *ctx);
    void (*stop)(void);
};

struct dialer_ops {
    const char *name;          /* for the log: vless */
    unsigned caps;             /* enum dialer_cap */
    size_t sess_size;          /* the stack allocates one session per client connection */

    /* ---- the node (ctx is what the module put into struct dialer) ---- */
    /* The node's name in log lines such as "flow to <peer> did not open". */
    const char *(*peer)(const void *ctx);
    /* For the startup line "<dev> -> <description>": name, address, transport. */
    void (*describe)(const void *ctx, char *out, size_t n);
    const char *(*strerror)(int rc);
    /* The text of connect's failure rc, kept in the session by the connector thread that failed:
     * some reasons live in that thread only. OPTIONAL: NULL means strerror(rc). */
    const char *(*why)(const void *sess, int rc);

    /* ---- the link to the node ---- */
    /* Connect (in a connector thread: blocks for up to timeout_s). Returns 0 when ready. On
     * failure the session keeps no descriptor and no keys. */
    int  (*connect)(const void *ctx, void *sess, int timeout_s);
    /* Move the established link from spare session src into connection session dst: the link in
     * dst is replaced, the flow state in dst (flow_open) is kept. Afterwards src no longer owns the
     * link and is not closed. */
    void (*take)(void *dst, void *src);
    /* Close the link: descriptors and keys. Called when a connection is freed and when a spare
     * session is dropped. */
    void (*close)(void *sess);
    /* Make the session empty for a new connection: no descriptor, no parser counters. Called on
     * every release and when the table is set up, so it must touch only the first bytes of the
     * session, not all of it (why: see conn_drop in stack.c). */
    void (*clear)(void *sess);
    int  (*fd)(const void *sess);
    /* Whether the session holds data already read, which epoll will not report. */
    int  (*has_data)(const void *sess);

    /* ---- the client's flow ---- */
    /* A new client connection: set up the protocol state for the flow. Non-zero: no flow to this
     * node is possible (the dialer logs why), and the stack drops the connection. */
    int  (*flow_open)(const void *ctx, void *sess, const struct flow_key *k, int udp);
    /* Client bytes to the node; for UDP, one datagram framed by dgram_frame per call. Returns
     * SEND_*. For TCP, n == 0 before any data means "the client is silent, the server may speak
     * first" (SSH, SMTP, FTP): send whatever the node needs to open the flow now (for VLESS the
     * request header alone). The stack makes that call once, SERVER_FIRST_MS after the later of
     * the client's handshake and the ready link. */
    int  (*send)(const void *ctx, void *sess, const struct flow_key *k, int udp,
                 const unsigned char *d, size_t n);
    /* How many client bytes send would take right now (TCP), after the dialer's own framing;
     * -1: no limit. NULL: never limited.
     *
     * The stack gives the client a window no larger than this. A transport with a flow-control
     * window of its own (HTTP/2: grpc, xhttp) otherwise refuses with SEND_AGAIN whatever goes
     * past it; the client, granted megabytes, reads each refusal as a loss and waits out a
     * retransmission timeout that doubles on every repeat. Against Xray's 94 KB gRPC window
     * that held uploads to 10-20 Mbit/s with multi-second stalls. When the room grows back
     * (WINDOW_UPDATE, read by drain_conn) the stack sends the client a window update. */
    long (*room)(const void *ctx, void *sess);
    int  (*finish)(const void *ctx, void *sess);
    /* Send what the link holds back for a delay (a finalmask piece queued behind its gap) once it
     * is due, at now_ms (CLOCK_MONOTONIC). *next_ms: in how many ms to ask again, 0 when the
     * socket must become writable first, -1 when nothing waits. Returns SEND_OK or SEND_FATAL.
     * The stack asks after every send and read and, while something waits, from its wait.
     * *next_ms == -2: nothing will ever be held back on this session; the stack stops asking.
     * OPTIONAL: NULL means nothing is ever held back. */
    int  (*due)(void *sess, uint64_t now_ms, int *next_ms);
    size_t (*up_write)(const void *ctx, void *sess);
    /* Frame a client datagram into stream bytes for the node. Returns the length, or 0 if it does
     * not fit. */
    size_t (*dgram_frame)(const void *ctx, const void *sess, const unsigned char *p, size_t n, unsigned char *out,
                          size_t cap);
    /* Read from the link into buf, or point *data at the bytes where they already lie. *got is
     * their count, before deliver parses them (the stack meters its per-pass budget by it). rc 0
     * with *got 0 is valid (a control frame); a non-zero rc means the link is over. */
    int  (*read)(void *sess, unsigned char *buf, size_t cap, const unsigned char **data,
                 size_t *got);
    /* Parse what was read and hand the payload out through emit. Non-zero: the connection ends. */
    int  (*deliver)(const void *ctx, void *sess, int udp, const unsigned char *d, size_t n,
                    dialer_emit_fn emit, void *arg);

    /* ---- the client window (OPTIONAL: 0 means the stack advertises its own) ----
     *
     * The largest receive window, in bytes, the stack may advertise to the client with this
     * dialer: the window is what the client may send between two ACKs, and a DC_ACK_PACED queue
     * must hold it above its low mark. The smaller of this and the stack's own cap
     * (rcv_win_field) is advertised. */
    unsigned rcv_wnd_max;

    /* ---- several nodes (all OPTIONAL: NULL means one node, the one in ctx) ----
     *
     * For the node pool: each connection has its own node, known to the session, not to ctx. The
     * stack needs just these four questions, none of them about the protocol. */
    /* The connection's node, for "flow to <node> did not open". */
    const char *(*peer_of)(const void *ctx, const void *sess);
    /* Whether spare session src suits connection dst: 1 to take it, 0 to leave it spare, -1 to
     * drop it (its node is no longer active). */
    int  (*match)(const void *ctx, const void *dst, const void *src);
    /* The connection's node is no longer active (declared dead and replaced): the stack resets
     * the connection (RST to the client) after stack_nodes_changed (stack.h). */
    int  (*stale)(const void *ctx, const void *sess);
    /* The connection's link was cut, not closed: the kernel gave up waiting for the node
     * (silence_s below) or the node reset it. A reason for the health check to probe the node
     * now. */
    void (*lost)(const void *ctx, const void *sess);
    int (*set_host)(void *sess, const char *host);
    int (*flow_open_on)(void *sess, int node, const struct flow_key *k, int udp, const char *host);
    const struct dialer_mux *mux;
};

/* An instance: the table and its ctx (for the VLESS dialer a node, for the node pool the pool).
 *
 * silence_s is the stall threshold of a node on a live connection (--silence), in seconds. A link
 * whose sent data stays unacknowledged longer, or that is silent longer and does not answer a TCP
 * keepalive probe, is cut by the kernel (TCP_USER_TIMEOUT, SO_KEEPALIVE); the stack then resets the
 * client connection (RST) and calls lost. 0: no threshold, the kernel's own timeouts (minutes). */
struct dialer {
    const struct dialer_ops *ops;
    const void *ctx;
    int silence_s;
};

/* The current second of the stack's loop pass, for rate-limiting log lines in a dialer. The same
 * clock the stack uses, so the two never disagree at a second boundary. */
time_t stack_now_s(void);

#endif
