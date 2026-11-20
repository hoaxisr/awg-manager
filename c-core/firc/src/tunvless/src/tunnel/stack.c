/* Tunnel stack: packets from the TUN device become flows to the node, and back.
 *
 * The kernel hands us IP packets; the node accepts connections. This file bridges the two: for
 * each TCP connection (and UDP flow) from the TUN device it opens a flow to the node and carries
 * the bytes, acknowledging them to the client the way a real stack would.
 *
 * There is no protocol here. Everything about the node belongs to the dialer (dialer.h): a
 * connection's session is opaque to the stack, and VLESS is the dialer in proto/vless/vldial.c.
 * The entry point is src/main.c.
 *
 * This is not a full TCP stack, and need not be. There is no congestion control: the client runs
 * its own on its side, the server on its side. The job is narrower: answer the SYN, take data in
 * order, pass on what arrives, retransmit what is unacknowledged, and close.
 *
 * Retransmission TOWARDS THE CLIENT is required: without it one lost segment hangs the connection
 * for good (the client keeps acknowledging the old data, we keep sending new). It costs a ring of
 * up to RTX_CAP bytes per connection that carries data.
 *
 * There is no REASSEMBLY buffer: an out-of-order segment from the client is dropped, and the
 * client, which has a full stack, retransmits it. Retransmitting is the sender's job and filling
 * holes the receiver's; we pass received data on at once, so only the first is needed.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <stddef.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <sys/epoll.h>
#include <sys/eventfd.h>
#include <time.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <pthread.h>
#include <sys/mman.h>
#include <sys/resource.h>
#include <sys/socket.h>

#include "tun.h"
#include "rtx.h"
#include "dialer.h"
#include "stack.h"
#include "ifcfg.h"
#include "../control.h"
#include "../ctlmap.h"
#include "ratelog.h"
#include "../ctlinb.h"
#include "ibhs.h"
#include "nodes.h"
#include "tstack.h"

#ifndef SO_ORIGINAL_DST
#define SO_ORIGINAL_DST 80
#endif

/* ---- leveled log ----------------------------------------------------------------------
 *
 * Format: `tunvless[warn] tunnel: ...`. The level comes first and in exactly this form: readers
 * filter by this prefix, never by the wording of the message, which changes. Two levels: `warn` —
 * traffic went wrong or did not go at all; `info` — normal operation. A process that cannot go
 * on exits with a non-zero code instead of logging an "error" level.
 */
#define LOG_W  "tunvless[warn] tunnel: "
#define LOG_I  "tunvless[info] tunnel: "
#define LOG_E  "tunvless[error] tunnel: "

/* How long a connection must be silent before it may be evicted (conn_new) or cleaned up as dead
 * (conn_deadlines): one notion of a dead connection for the whole file.
 *
 * Not less than two minutes: HTTP/2 (YouTube, for one) keeps a connection idle for tens of
 * seconds and then REUSES it for every request of the page. Evicting it sends RST, and the
 * browser fails all those multiplexed requests at once with ERR_CONNECTION_CLOSED. No browser
 * keeps an idle connection for two minutes. */
#define IDLE_EVICT_S 120

/* Idle time after which a DNS flow gives up its slot in a FULL table (see conn_new). Longer than
 * a resolver's reply timeout (glibc: 5 s per try), so a query whose answer is still on its way is
 * not evicted. */
#define DNS_IDLE_S 10

/* ---- connection table: hot fields apart from the bulky session ------------------------
 *
 * The loop walks the connections several times per turn (epoll set, pending jobs, deferred ACKs,
 * deadlines). With the session (TLS record buffers, cipher contexts, HTTP/2 state) in the same
 * record, the hot fields of each connection sat on pages kilobytes apart: 320 records of 19 KB is
 * 5.8 MB, against a 32 KB L1 and no L2 on a MIPS 24Kc. Measured on x86_64 with 8 of 320 slots
 * busy: 3.4 us per turn for the walks against 0.15 us over a compact live list; on the 24Kc the
 * walks alone capped the loop near 1500 turns per second, invisible in every other counter.
 *
 * Hence three rules that depend on each other:
 *
 *   1. the bulky half lives in a SEPARATE array (dialer sessions); the loop never touches it, and
 *      beyond its first page it becomes resident only for connections that actually do I/O;
 *   2. the hot record is small (checked below), and the fields the loop reads come first;
 *   3. no loop walks the WHOLE table: they walk g_live, which holds only busy slots, and lookup by
 *      flow goes through a hash.
 *
 * A live list alone would not help while a record is 19 KB, and vice versa. */

/* The bulky half is the dialer's session (dialer.h): the link to the node and the protocol state
 * of this flow. It is opaque to the stack, and the dialer names its size (sess_size); for VLESS it
 * is about 40 KB — TLS record buffers of both links, cipher contexts, HTTP/2, Vision, datagram
 * reassembly (struct vl_sess in proto/vless/vldial.c). The sessions are a separate array in the
 * loop thread's heap mapping, next to the hot table (see "loop thread tables" below). */

struct conn {
    /* The fields the loop walk reads come first, up to `last`. Keep them together: scattered
     * among the others they would add cache lines to every walk. */
    uint8_t used;
    /* A UDP flow, not a TCP connection. Read by the loop walk: UDP has neither a window nor a
     * retransmission ring, so whether to read more from the server is decided differently. */
    uint8_t is_udp;
    /* The handshake to the node runs in a connector thread, and while it runs the connection is
     * UNTOUCHABLE. (A handshake takes 250-400 ms; inside the loop it stalled every connection,
     * and Chrome gave up on requests and with them on whole multiplexed HTTP/2 connections.)
     *
     * pending=1: the job is with a connector, no answer yet. The connection may not be closed,
     * evicted or cleaned up as idle: the connector writes into its session, and freeing the
     * record under it is a use after free. Every place that removes a conn checks it. */
    uint8_t pending;
    /* The server closed the flow, but the client has not acknowledged everything yet. Details
     * at closed_at; the field is here because the loop walk reads it. */
    uint8_t srv_closed;
    /* What the connection's descriptor waits for in epoll: ARM_IN — readable, ARM_OUT —
     * writable (an ACK is held, see ack_hold); 0 — not in the set. Lets epoll_ctl run only when
     * this changes (the client's window opens or closes), not on each of thousands of turns. */
    uint8_t armed;
    /* A WHOLE record is already in the record buffer, but we stopped (the per-turn limit or the
     * client's window). epoll will not report it — the socket is empty, everything is already in
     * our buffer — so without this flag it would wait for the next data from the server, and the
     * tail of a response could sit until the client's retransmission timeout. */
    uint8_t rx_ready;
    /* The client is owed an ACK, deferred to the end of the batch of TUN packets: one ACK with
     * the last number acknowledges the whole batch, instead of a device write per packet. */
    uint8_t ack_due;
    /* The ACK is due but held: a DC_ACK_PACED dialer (dialer.h) cannot take more — its queue to
     * the node is above the low-water mark — and the client, without the ACK, stops at its
     * window. Writability of the descriptor (ARM_OUT in armed) releases it: flush_acks sends the
     * ACK and clears the field. Stored rather than polled again because the epoll set depends
     * on it, and the set changes only on transitions. */
    uint8_t ack_hold;
    uint8_t dup_acks;         /* ACKs of the same number in a row */
    /* A fast retransmit was done and no new ACK has come since. Without this flag the
     * retransmit feeds itself: the client answers the resent segment with an ACK of the same
     * number (it already has the data), three of those, and we resend again. One fast
     * retransmit per ACK advance, as in TCP. */
    uint8_t fast_done;
    uint8_t client_wscale;    /* from the SYN options: window = client_win << wscale */
    uint16_t mss;
    /* The client's SYN had the window scale option, so OUR window is scaled too: the window
     * field to it is the window >> g_rcv_shift (rcv_win_field). Without it, 16 bits as they are. */
    uint8_t ws_on;
    uint8_t sock;
    uint8_t carmed;
    /* The client's window field as received; scaled by client_wscale. */
    uint16_t client_win;
    /* Position in g_live and the next slot in the hash chain. Indexes, not pointers: two bytes
     * instead of eight, and the hot record must stay small. */
    int16_t livepos;
    int16_t hnext;
    /* Sequence numbers as the CLIENT sees them. */
    uint32_t our_seq;         /* the next one we send to the client */
    uint32_t client_ack;
    uint32_t client_seq;      /* the next one expected from the client */
    /* ---- retransmission to the client ----
     *
     * The ring starts exactly at client_ack and its length is exactly our_seq - client_ack:
     * unacknowledged bytes live nowhere else, so the two cannot drift apart.
     *
     * Allocated on the first data, not on SYN (emit_to_client), and freed on close or after the
     * connection idles (conn_deadlines). A pointer rather than an array inside struct conn: the
     * hot record must stay small, and only connections that carry data hold a ring. */
    struct rtx rtx;
    /* When the oldest unacknowledged byte was sent (or last resent). 0 — nothing is
     * unacknowledged. */
    uint64_t rtx_at;
    uint32_t rto_ms;          /* current timeout, doubles with every resend */
    time_t last;
    /* --- below: what the loop walk does NOT read; packet handling and setup only. --- */
    struct flow_key key;
    /* The loop turn the slot was given to this connection (g_turn). Events from that turn's
     * epoll_wait belong to the slot's previous connection. */
    uint32_t born_turn;
    int fd;                   /* copy of the session's socket, for epoll_ctl and poll */
    int cfd;
    uint32_t odst;
    int done;                 /* the connector has finished: read only with ACQUIRE */
    int rc;                   /* its result: 0 or the dialer's error code */
    /* When the server closed: the start of the wait for ACKs (CLOSE_DRAIN_MS).
     *
     * The connection must NOT be closed at that moment: that destroys the ring, and the last
     * lost piece can never be resent; the client sits with a hole until its own timeout (121 s
     * on the test bench, where one flow with twice the loss finished in 2 s). So the server is
     * no longer read, but the connection lives and retransmits until the client acknowledges
     * everything; then the FIN goes out. */
    uint64_t closed_at;
    /* ---- early data from the client ----
     *
     * The SYN-ACK goes out at once on SYN (see handle_packet), so the client sends its first
     * data — a ClientHello, one or two thousand bytes — while the handshake with the node is
     * still running. It is held here until the flow is ready. The buffer is allocated only if
     * the client did send something, and freed once all of it has gone to the server.
     *
     * early_off — how much of it has been sent: an HTTP/2 transport may report its window
     * closed midway, and the rest goes later. Until it has, FRESH client data is not
     * acknowledged: byte order in the stream matters more than speed. */
    unsigned char *early;
    uint32_t early_n;
    uint32_t early_off;
    /* When the client completed the handshake (its first ACK; 0 — not yet), moved to the moment
     * the link became ready if that came later, and whether anything went to the node yet: a
     * client silent for SERVER_FIRST_MS after that waits for the server
     * to speak first, and the node must be told where to connect without data (conn_deadlines).
     * Not counted from the SYN: a client whose SYN-ACK was lost is not silent, it does not have
     * the connection yet. Opening the flow then let the server's answer go out ahead of the
     * handshake, and the client's repeated SYN could no longer be answered (our_seq had moved):
     * the connection hung for good (run-tunnel.sh with 3% loss, about 1 run in 40). */
    uint64_t estab_ns;
    /* The node's room (dialer_ops.room) when the client last heard our window; UINT32_MAX —
     * not yet. room_watch compares the room now with it to tell the client it grew. */
    uint32_t room_seen;
    uint8_t sent_any;
    uint8_t fw_again;
    /* The client gave up (RST) while the connector was working: the record must not be
     * touched, so only mark it, and close once the connector is done. */
    uint8_t client_gone;
    /* The client sent a FIN, received in order: no more data from the client, but the server's
     * response is still going to it. */
    uint8_t client_fin;
    /* The link to the node was cut, not closed (the kernel gave up waiting for the node, or the
     * node reset it), or the connection's node is no longer active (dialer_ops.stale): the client
     * gets RST from the common pass, not FIN. A FIN would say "the response is complete", and a
     * truncated download would look whole. Set together with srv_closed. */
    uint8_t aborted;
    uint8_t ib;
    uint8_t ib_reply;
    uint8_t ib_udp;
    uint8_t mux;
    uint8_t fm_wait;
    uint8_t no_due;
    uint8_t fin_up;
};

/* The hot record must stay small: that is the whole point of the split. A build check rather
 * than a promise: a buffer added here "for debugging" would bring the table back to megabytes,
 * and only the speed on a router would show it. */
typedef char conn_hot_size_check[sizeof(struct conn) <= 224 ? 1 : -1];

/* Bits of conn.armed. */
#define ARM_IN  1
#define ARM_OUT 2

/* How long to wait for ACKs after the server closed. A fixed limit, not "as long as it takes":
 * the client may be gone, and then the ring never empties. */
#define CLOSE_DRAIN_MS 5000

/* Early data limit per connection, and the window a waiting flow offers. */
#define EARLY_CAP 8192
_Static_assert(EARLY_CAP <= 65535, "EARLY_CAP fits the unscaled window field of a SYN-ACK");

/* How long a client may stay silent before the flow is opened without its data: SSH, SMTP, FTP
 * and the like wait for the server's greeting, and VLESS carries the destination only with the
 * first data. 500 ms, as Xray's VLESS outbound, counted from the later of the client's handshake
 * and the ready link. */
#define SERVER_FIRST_MS 500

/* Unacknowledged bytes kept per connection, and therefore our in-flight limit.
 *
 * One number, not two: keeping more in flight than we can retransmit brings back the hang on a
 * lost segment. So the send window equals this buffer, and one cannot grow without the other.
 *
 * Why 64 KB is enough: throughput is in-flight divided by the ACK delay. The client is behind
 * the router (round trip under a millisecond) and the loop measured 1300-2000 turns per second,
 * so an ACK is handled within 0.5-0.8 ms: 64 KB at 0.7 ms is 90 MB/s per connection, ten times
 * what the whole tunnel carries on a router (3-10 MB/s). A larger ring would cut the number of
 * connections that fit in the same memory, and connections are what runs out first. */
#define RTX_CAP (64 * 1024)

/* The ring's size when the first data allocates it. Only a connection whose client advertises a
 * larger window grows it to RTX_CAP (rtx_widen_if_self_bound, emit_to_client), so the many
 * connections a browser keeps open with little traffic stay small.
 *
 * Not smaller: it must cover CLIENT_ROOM_RESERVE (room for a whole read) with space to spare.
 * Below that client_can_take_record would never be true, and the connection would never read
 * from the server and so never grow. */
#define RTX_START (32 * 1024)

/* Retransmission timeout. Doubles up to a second, in case the client is really gone.
 *
 * The floor is 200 ms, the minimum RTO of Linux itself. The client's ACK arrives quickly, but WE
 * read it on our own loop pass, and on a slow router a pass is long (measured: 22 passes per
 * second, 45 ms, nearly all of it decrypting records). With a 50 ms floor the timer fired on our
 * own slowness, not on loss: 14% of the bandwidth went to resending what the client already had.
 *
 * A high floor costs nothing: real loss is caught by three duplicate ACKs (DUP_ACK_TRIGGER)
 * within one round trip. The timer matters only for the last segment, after which no ACKs come,
 * and there 200 ms against 50 is not noticeable. */
#define RTO_MIN_MS 200
#define RTO_MAX_MS 1000

/* Even 200 ms is too little when a loop pass is longer. The floor is four passes: within one pass
 * we can only SEE the ACK, and the margin covers a previous pass busy with another connection.
 * The pass period is a smoothed measurement (g_loop_ms). */
#define RTO_LOOP_FACTOR 4

/* ACKs of the same number in a row that mean loss. Three, as in TCP: the client sends them on
 * out-of-order data, so it already sees the hole, a round trip before the timer would fire. */
#define DUP_ACK_TRIGGER 3

/* ---- the window WE advertise to the client ---------------------------------------------
 *
 * The client keeps at most the advertised window in flight, so an upload is capped at the window
 * divided by the round trip: the network to the client plus the time until we read its packets
 * and acknowledge them (once per loop pass, flush_acks). A fixed 65535 caps it at 65535/RTT
 * whatever the node and the CPU can do: 175 Mbit/s at 3 ms, 87 at 6, 22 at 24. Measured on an x86
 * bench (network namespaces, Xray 26.3.27 node with Reality and Vision, a 6.1 ms round trip to
 * the client), upload with one flow / eight flows in Mbit/s: 64 KB — 83 / 621, 256 KB —
 * 325 / 1340, 1 MiB — 1164 / 1521, 4 MiB — 1286 / 1477. At a 0.1 ms round trip the window does
 * not matter: the node is the limit.
 *
 * A client that offers window scaling in its SYN (ws_seen) gets our option with shift
 * g_rcv_shift in the SYN-ACK; the SYN-ACK's own window is never scaled
 * (RFC 7323, 2.2). After that its window field is g_rcv_wnd >> g_rcv_shift
 * (rcv_win_field). A client without the option gets no option back (it may only answer a SYN
 * that has it) and a window of 65535. The shift is the smallest whose 16-bit field holds the
 * ceiling, at most 14. The option matters both ways: scaling is on only if both sides send it,
 * and without ours the client would not scale the window it advertises to us, while client_room
 * scales it — we would keep up to 128 times too much in flight, and the receiver drops whatever
 * lies beyond its window.
 *
 * THE CEILING. The stack holds no client data: what arrives goes to the node in the same pass.
 * But it sits in kernel memory twice: in the TUN queue until the loop reads it, and in the send
 * buffer of the socket to the node until the node acknowledges it. So the window promises no
 * more than the kernel would give a real TCP receiver or sender: the smaller of the third values
 * of net.ipv4.tcp_rmem and tcp_wmem. The kernel derives them from memory (tcp_init in
 * net/ipv4/tcp.c: 1/128 of the pressure threshold; 128 KB to 6 MB for receive, 64 KB to 4 MB for
 * send): 4 MB on a desktop, about 700 KB on a 128 MB router (computed, not checked on one). The
 * owner changes it with a sysctl, or directly with STEER_TUN_RCVWND=N (0 — 65535 unscaled). If
 * the files cannot be read, 65535.
 *
 * THE COST. Behind a narrow link to the node the client fills the whole window, and the backlog
 * sits in the TUN queue and the socket buffer. Measured with a 50 Mbit/s link to the node, a
 * 4 MiB ceiling and 6 ms to the client: the upload uses the whole link, but the connection's RTT
 * at the client is 534-547 ms instead of 6. A short queue would need tracking the rate at which
 * the node accepts, which is not done here.
 *
 * NOT HERE. A window of "free space in the socket's send buffer" (SO_SNDBUF minus SIOCOUTQ)
 * follows the round trip to the NODE, not to the client (the kernel grows that buffer with the
 * node's congestion window): with a fast close node it stuck at 100-200 KB (148, 151 and 1382
 * Mbit/s in three runs instead of 1286-1349). A brake "65535 while more than 65535 bytes are
 * unsent in the socket" (SIOCOUTQNSD) made the window flap (1079 Mbit/s instead of 1404).
 * Behind a narrow link the loop, shared by all its connections, blocks in the write to the node
 * (SO_SNDTIMEO) and other connections' ACKs wait in the TUN queue; that happens with any window.
 * There is no reassembly buffer and no SACK (the SYN-ACK offers MSS and window scale only), so
 * the larger the window, the more the client resends after one loss (0.3% loss at 6 ms: 23
 * Mbit/s and 400 TCP retransmits with 65535, 24 Mbit/s and 3000-12000 with the large window).
 * The client's window towards us (client_room) is unaffected.
 */
#define RCV_WND_MIN 65535u        /* what fits in the window field unscaled */
#define RCV_SHIFT_MAX 14          /* RFC 7323, 2.3: the shift never exceeds 14 */

static uint32_t g_rcv_wnd = RCV_WND_MIN;   /* our window ceiling, bytes */
static uint8_t g_rcv_shift;                /* the shift we offer in the SYN-ACK */
static uint32_t g_rcv_wnd_max;             /* dialer_ops.rcv_wnd_max (stack_setup); 0 — none */

/* Set the window ceiling and derive the shift: the smallest one whose 16-bit field still holds
 * it. */
static void rcv_window_set(uint32_t wnd) {
    if (wnd < RCV_WND_MIN) wnd = RCV_WND_MIN;
    unsigned sh = 0;
    while (sh < RCV_SHIFT_MAX && ((uint64_t)RCV_WND_MIN << sh) < wnd) sh++;
    uint64_t room = (uint64_t)RCV_WND_MIN << sh;
    g_rcv_shift = (uint8_t)sh;
    g_rcv_wnd = wnd < room ? wnd : (uint32_t)room;
}

/* The window field of a packet to the client: what the early buffer still holds while the flow
 * waits for its link, then 65535 to a client without scaling, otherwise the window divided by
 * the shift named in the SYN-ACK. Rounded up because the window need not be a
 * multiple of 2^shift (a ceiling set by number, or the dialer's limit): rounding down, a limit
 * of 65535 at shift 7 would become 65408. */
static long conn_room(const struct conn *c);

/* The loop is behind the device: the last TUN batch hit TUN_DRAIN_MAX, so packets wait in the
 * kernel's queue. Set per loop thread after each batch (worker_loop).
 *
 * While it is, clients get at most RCV_WND_BACKLOG of window. What a client may send unacknowledged
 * is what waits in that queue, and the clients' ACKs for downloads wait behind it: with a window of
 * megabytes, two uploads kept the queue full and the ACKs late, and a download, which may have only
 * RTX_CAP in flight, moved RTX_CAP per queue wait (xhttp stream-one, 20 s: 0.6 GB down against
 * 10.3 GB up; 2.8 GB against 7.4 GB with this cap). Only while behind: a client on Wi-Fi with a few
 * milliseconds of round trip needs the full window to upload fast, and when the loop keeps up the
 * queue is empty and costs nothing. */
#define RCV_WND_BACKLOG (64u * 1024)
static __thread uint8_t g_tun_backlog;

static uint16_t rcv_win_field(const struct conn *c) {
    if (c->pending && !c->is_udp) {
        uint32_t e = EARLY_CAP - c->early_n;
        return (uint16_t)(c->ws_on ? e >> g_rcv_shift : e);
    }
    /* The dialer's limit (dialer_ops.rcv_wnd_max): with DC_ACK_PACED the window must fit the
     * queue to the node, and the ceiling, chosen by machine memory, would overflow it. */
    uint32_t cap = g_rcv_wnd_max;
    uint32_t w = c->ws_on ? g_rcv_wnd : RCV_WND_MIN;
    if (cap && w > cap) w = cap;
    if (g_tun_backlog && w > RCV_WND_BACKLOG) w = RCV_WND_BACKLOG;
    /* What the node can take now (dialer_ops.room): more would come back as SEND_AGAIN and cost
     * the client a retransmission timeout. Rounded DOWN, unlike the ceiling: a window a few
     * bytes past the room is a refused segment. */
    long room = conn_room(c);
    if (room >= 0 && (uint32_t)room < w) {
        w = (uint32_t)room;
        if (!c->ws_on) return (uint16_t)w;
        return (uint16_t)(w >> g_rcv_shift);
    }
    if (!c->ws_on) return (uint16_t)w;
    uint32_t f = (w + ((1u << g_rcv_shift) - 1)) >> g_rcv_shift;
    return (uint16_t)(f > 65535u ? 65535u : f);
}

/* The third number of a "min default max" sysctl file (tcp_rmem, tcp_wmem); 0 — unreadable. */
static uint64_t sysctl_max_of_three(const char *path) {
    FILE *f = fopen(path, "r");
    if (!f) return 0;
    unsigned long long a = 0, b = 0, m = 0;
    int n = fscanf(f, "%llu %llu %llu", &a, &b, &m);
    fclose(f);
    return n == 3 ? m : 0;
}

/* The window ceiling: the smaller of tcp_rmem and tcp_wmem (third numbers), or STEER_TUN_RCVWND.
 * Called from stack_run before the threads start. */
static void rcv_window_init(void) {
    uint64_t rmem = sysctl_max_of_three("/proc/sys/net/ipv4/tcp_rmem");
    uint64_t wmem = sysctl_max_of_three("/proc/sys/net/ipv4/tcp_wmem");
    uint64_t cap = rmem < wmem ? rmem : wmem;            /* a file missing: 0, i.e. 65535 */
    const char *e = getenv("STEER_TUN_RCVWND");
    if (e && *e) cap = strtoull(e, NULL, 10);
    if (cap > ((uint64_t)RCV_WND_MIN << RCV_SHIFT_MAX)) cap = (uint64_t)RCV_WND_MIN << RCV_SHIFT_MAX;
    rcv_window_set((uint32_t)cap);
    fprintf(stderr, LOG_I "receive window: up to %u KB, scale %u%s\n", (g_rcv_wnd + 1023) / 1024,
            g_rcv_shift, e && *e ? " (STEER_TUN_RCVWND)" : " (from tcp_rmem and tcp_wmem)");
}

/* How much the client can take NOW, with its window scale and our limit. One function, not
 * three copies of the computation: copies that drift apart would either stall for nothing or
 * overrun the client and lose a segment. */
static uint32_t client_room(const struct conn *c) {
    uint32_t win = (uint32_t)c->client_win << c->client_wscale;
    /* The limit is the ring's ACTUAL capacity (RTX_START before it exists), not RTX_CAP: the
     * ring grows on demand, and promising more than we can retransmit brings back a lost
     * segment nothing can repair. */
    uint32_t cap = c->rtx.cap ? c->rtx.cap : RTX_START;
    return win < cap ? win : cap;
}

/* Room that must stay free in the client's window before we take another record from the server.
 *
 * A record once read must go to the client WHOLE: there is no per-connection buffer to keep it
 * in. So "read or not" is decided in advance, with room for a whole record, not "while the window
 * has a byte". Without this reserve we overran the window by a record (measured: "window 262144,
 * in flight 268876"), and the receiver silently drops whatever lies beyond its window.
 *
 * Based on TUNNEL_BUF, not on the TLS record size: in direct copy mode one read returns all that
 * fits in the buffer, 18448 bytes, not 16384. */
#define CLIENT_ROOM_RESERVE (TUNNEL_BUF + 1024)

/* Grow the ring when in-flight is limited by ITS size rather than by the receiver.
 *
 * The reserve for a whole read comes out of the ring's capacity: with the initial 32 KB only
 * 32768 - 19472 = 13296 bytes remain for data in flight. In direct copy mode one read brings a
 * whole TUNNEL_BUF (18448 bytes), more than that, so a second read was never in flight (measured:
 * in flight up to 18448 with a 512 KB client window — the limit was ours, not the client's).
 * emit_to_client grows the ring only when a read does not fit, which never happens: 8-18 KB
 * always fits in 19 KB free.
 *
 * Allocation stays lazy: we get here only when the connection carries data and the client
 * advertised a window larger than our ring. An idle connection keeps the small ring. */
static void rtx_widen_if_self_bound(struct conn *c) {
    /* No ring yet means not a byte of data on the connection, nothing to grow: client_room
     * uses RTX_START in this state. Without this check a connection with a small INITIAL client
     * window (14600 is common) would get the full 64 KB before its first read — the greed
     * RTX_START exists to avoid. */
    if (!c->rtx.cap) return;
    if (c->rtx.cap >= RTX_CAP) return;
    /* Limited by the client, not by us: a bigger ring would not open its window. */
    if (((uint32_t)c->client_win << c->client_wscale) <= c->rtx.cap) return;
    rtx_grow(&c->rtx, RTX_CAP);
}

/* Can the client take one more whole record.
 *
 * Not const: on finding that the ring's size is the obstacle it grows the ring — otherwise the
 * check would keep answering "no" for a reason it creates itself. */
static int client_can_take_record(struct conn *c) {
    uint32_t inflight = c->our_seq - c->client_ack;
    uint32_t room = client_room(c);
    if (room <= inflight || room - inflight < CLIENT_ROOM_RESERVE) {
        rtx_widen_if_self_bound(c);
        room = client_room(c);
    }
    return room > inflight && room - inflight >= CLIENT_ROOM_RESERVE;
}

/* How much one connection gets per loop pass — in BYTES, not reads. Once the server switches to
 * direct copy (Vision's direct command) reads become small, ~300 bytes, and a limit of eight
 * reads meant 2.4 KB per pass (measured: 3600 reads/s at 1.1 MB/s). The read count limit stays,
 * set high: it guards against an endless loop on one-byte reads, not against speed. */
#define DRAIN_MAX_BYTES  (192 * 1024)
#define DRAIN_MAX_READS  256

/* Packets taken from TUN per pass. Client ACKs come densely, and one per pass tied the speed to
 * the number of passes. The limit keeps one client's flood from starving the reads from servers. */
#define TUN_DRAIN_MAX 64

/* ALL connection state is per loop thread (__thread). Shared are only the spare pool and the
 * connector queue (each under its mutex) and the node change counter (atomic).
 *
 * A shared table under a mutex would mean a lock per packet, some seven hundred per megabyte.
 * Hence the per-thread table, and hence ONE thread by default, see worker_count.
 *
 * The kernel does NOT promise that both directions of a TCP connection land in the same queue of
 * a multi-queue device: tun_open only opens N queues with IFF_MULTI_QUEUE and sets no steering
 * program (TUNSETSTEERINGEBPF). A half that lands in another thread's queue does not find its
 * connection: the client's ACKs never reach the owner, client_ack does not move,
 * client_can_take_record stays false, and the owner stops reading from the server. Measured on a
 * router (Filogic, two cores, speedtest through the tunnel): with two queues one thread held 44
 * connections at one turn per second and zero MB/s, the total was 12 Mbit/s and the watchdog
 * (mtk-wdt) reset the box; with one queue, 292 Mbit/s.
 *
 * LOOP THREAD TABLES are in the heap, not in __thread. As static TLS they were about 14 MB per
 * thread, which glibc zeroes with memset for every thread, connectors included (musl hands out
 * fresh pages). Now a loop thread has one mapping (mmap) for the hot table, lists, buckets and
 * sessions, whose pages become resident only when touched. Only the pointers are __thread. */
static __thread struct conn *g_conns;
/* Loop turns of this thread: one per epoll_wait. */
static __thread uint32_t g_turn;
/* Dialer sessions, one per slot, g_sess_stride apart. The stride is per process: set from
 * dialer_ops.sess_size in stack_run before the threads start, read-only after. */
static __thread unsigned char *g_sess;
static size_t g_sess_stride;
/* A connection's session. Computed from the index, NOT stored as a pointer: a pointer is eight
 * bytes the hot record has no room for.
 *
 * Valid only in the owner thread: g_conns and g_sess are per thread. That is why a connector gets
 * the session pointer in the job itself instead of computing it: it would address another
 * thread's table with our index. */
#define SESS(c) ((void *)(g_sess + (size_t)((c) - g_conns) * g_sess_stride))

/* Busy slots, packed. Every loop pass walks ONLY this array — why, see struct conn. */
static __thread uint16_t *g_live;
static __thread int g_live_n;
/* Free slots; without them finding a slot would scan the table on every SYN. */
static __thread uint16_t *g_freelist;
static __thread int g_free_n;

/* Flow -> slot hash. Separate chaining, not probing: removal leaves no tombstones, which with
 * 320 slots and tens of thousands of connections an hour would turn the table into a linear scan.
 * 512 buckets for 320 slots: load 0.63, chains under 1.5 on average, 1 KB that fits in L1.
 * conn_find runs on EVERY packet from TUN, up to seven thousand per second. */
#define CONN_BUCKETS 512
static __thread int16_t *g_bucket;

/* The process's dialer: protocol table and node. Set in stack_run before the threads start and
 * read-only after; not __thread because the connectors need the same one as the loop. */
static const struct dialer *g_dl;

/* How many times the set of active nodes has changed (stack_nodes_changed). A loop thread
 * compares it with its own count and, when they differ, asks the dialer about every connection
 * (stale). A counter, not a flag: each of several loop threads must notice the change itself. */
static unsigned g_nodes_epoch;

void stack_nodes_changed(void) { __atomic_add_fetch(&g_nodes_epoch, 1, __ATOMIC_RELEASE); }

/* One loop thread (the device has one queue): a connection missing from the table does not exist
 * at all, and its data can be answered with RST (handle_packet). With several threads half of a
 * connection may be in another queue (see worker_count), and RST would kill a live one. */
static int g_one_worker = 1;

/* NODE SILENCE THRESHOLD (struct dialer, silence_s), set on the link's socket once the link is
 * given to a connection.
 *
 * The kernel measures it, not us, and that avoids false alarms. At application level "sent and
 * waiting" cannot be told from a long poll: the request went out, the answer comes in a minute,
 * and that is normal. At TCP level it can: a live node ACKNOWLEDGES what it got at once, even
 * with no answer yet. Hence two kernel rules:
 *   - TCP_USER_TIMEOUT: data sent to the node unacknowledged longer than the threshold — the link
 *     is cut (the node died under an upload or a call, or DPI cuts the flow after N KB);
 *   - SO_KEEPALIVE with TCP_KEEPIDLE = threshold: nothing from the node longer than the threshold
 *     — a keepalive probe, and without an answer the link is cut a quarter threshold later (with
 *     TCP_USER_TIMEOUT the kernel cuts by time, not by probe count). This covers a download whose
 *     node died: the client sends nothing, so nothing is unacknowledged. A long poll or an idle
 *     connection answers the probe — the node's kernel does, without the application — and is
 *     left alone.
 * The cost is one empty TCP probe (52 bytes) per connection per idle threshold, only while the
 * connection lives (idle ones go after IDLE_EVICT_S).
 *
 * If the socket is not TCP, the first setsockopt fails and nothing more is tried. */
static void node_sock_silence(int fd) {
    int t = g_dl->silence_s;
    if (t <= 0 || fd < 0) return;
    unsigned ms = (unsigned)t * 1000u;
    if (setsockopt(fd, IPPROTO_TCP, TCP_USER_TIMEOUT, &ms, sizeof ms) != 0) return;
    int one = 1, idle = t, intvl = t / 4 ? t / 4 : 1;
    setsockopt(fd, SOL_SOCKET, SO_KEEPALIVE, &one, sizeof one);
    setsockopt(fd, IPPROTO_TCP, TCP_KEEPIDLE, &idle, sizeof idle);
    setsockopt(fd, IPPROTO_TCP, TCP_KEEPINTVL, &intvl, sizeof intvl);
}

/* The link was cut, not closed: the TCP socket is in CLOSE — the kernel cut it by time (the
 * silence threshold above) or the node sent RST. A close by the node (FIN) leaves CLOSE_WAIT and
 * a protocol parse error leaves ESTABLISHED; neither counts as cut. Not TCP: unknown, 0. */
static int node_sock_aborted(int fd) {
    struct tcp_info ti;
    socklen_t tl = sizeof ti;
    if (fd < 0 || getsockopt(fd, IPPROTO_TCP, TCP_INFO, &ti, &tl) != 0) return 0;
    return ti.tcpi_state == TCP_CLOSE;
}

/* Client data gathered for one send to the node: see "gathering the client's segments". */
#define UP_MAX (TUNNEL_BUF - 2048)          /* the dialer's room for a header and a Vision frame */
struct up_gather {
    struct conn *c;
    uint32_t n;
    unsigned char buf[UP_MAX];
};

struct defrag {
    uint32_t src, dst;
    uint16_t id;
    uint16_t next_off;         /* IP payload bytes collected so far */
    uint64_t at;               /* when the first fragment came */
    int active;
    unsigned char buf[8 + UDP_DGRAM_MAX];   /* UDP header plus data */
};

#define FIN_RECENT   64
struct fin_recent {
    union fk_addr src, dst;     /* the client's flow, as in its packets */
    uint8_t v6;
    uint16_t sport, dport;
    uint32_t fin_end;           /* our FIN's sequence number + 1: what the client acknowledges */
    uint16_t win;               /* the window field our FIN carried, repeated in the last ACK */
    time_t at;
};

struct loop_scratch {
    unsigned char pump[TUNNEL_BUF];
    struct up_gather up;
    unsigned char sock[UP_MAX];
    unsigned char dgram[UDP_DGRAM_MAX + 64];
    struct defrag defrag;
    unsigned char asm_pkt[20 + 8 + UDP_DGRAM_MAX];
    struct fin_recent fin_recent[FIN_RECENT];
};

/* One mapping per loop thread: the hot table, lists, buckets, loop buffers, then the sessions. */
struct conn_tables {
    struct conn conns[MAX_CONNS];
    uint16_t live[MAX_CONNS];
    uint16_t freelist[MAX_CONNS];
    int16_t bucket[CONN_BUCKETS];
    struct loop_scratch ls;
};
_Static_assert(offsetof(struct conn_tables, conns) == 0, "conn_table_free unmaps from g_conns");
/* Sessions start on a cache line boundary, and the stride keeps them there. */
#define SESS_ALIGN 64
#define TABLES_HEAD (((sizeof(struct conn_tables)) + SESS_ALIGN - 1) & ~(size_t)(SESS_ALIGN - 1))
static __thread size_t g_tables_len;
static __thread struct loop_scratch *g_ls;

static inline unsigned flow_hash(const struct flow_key *k) {
    /* Ports along with addresses: connections to one server differ only by source port, and a
     * hash without it would put all of a browser's connections into one bucket.
     *
     * The protocol is part of the key for correctness, not spreading: a TCP connection and a UDP
     * flow may have the same addresses and ports (a client may use one source port for both),
     * and a datagram must never land in a TCP connection or the other way round. */
    uint32_t h = ((uint32_t)k->sport << 16) ^ (uint32_t)k->dport ^ ((uint32_t)k->proto << 8) ^ k->v6;
    for (int i = 0; i < 4; i++) {
        uint32_t s, d;
        memcpy(&s, k->src.b + 4 * i, 4);
        memcpy(&d, k->dst.b + 4 * i, 4);
        h = (h ^ s) * 0x9E3779B1u ^ d;
    }
    h ^= h >> 15;
    h *= 0x2545F491u;
    h ^= h >> 13;
    return h & (CONN_BUCKETS - 1);
}

/* Thread number, for diagnostics only: it shows whether the kernel spreads the load or
 * everything sits in one queue. */
static __thread int g_worker;

/* Time read ONCE per loop turn. On OpenWrt mipsel kernels clock_gettime has no vDSO, so every
 * call is a real syscall, and per-use calls would mean two per TUN packet (up to seven thousand
 * per second). A turn's precision is enough: everything measured with these stamps is hundreds of
 * milliseconds (RTO at least 200 ms, idle 120 s), and a turn lasts a fraction of a millisecond.
 * The diagnostic counters read the clock themselves: they measure what happens inside a turn. */
static __thread uint64_t g_now_ns;
static __thread time_t g_now_s;

#define KB_PERIOD_NS 1000000000ull
#define KB_EARLY 0x80000000ull

struct kbc {
    uint64_t rx, tx, sent_rx, sent_tx, at;
};

static __thread struct kbc g_kb;

#define IB_ORPHAN 255

static struct ib_lst {
    int fd, lerr;
    uint8_t used;
    struct ib_cfg cfg;
    struct kbc kb;
} g_ib[IB_MAX];

#define IB_ASSOC_MAX 32
#define IB_UDP_DST 16
#define IB_UDP_IDLE_MS 60000

static struct ib_assoc {
    int tcp_fd, ufd;
    uint8_t ib;
    uint32_t client_ip;
    struct sockaddr_in client;
    int dst_conn[IB_UDP_DST];
    uint64_t last_ms[IB_UDP_DST];
} g_assoc[IB_ASSOC_MAX];

static void kb_add(const struct conn *c, uint64_t rx, uint64_t tx) {
    struct kbc *k = !c->ib ? &g_kb : c->ib <= IB_MAX ? &g_ib[c->ib - 1].kb : NULL;
    if (!k) return;
    k->rx += rx;
    k->tx += tx;
}

static int kb_take(struct kbc *k, uint64_t now, uint32_t *rx, uint32_t *tx) {
    uint64_t drx = k->rx - k->sent_rx, dtx = k->tx - k->sent_tx;
    if (drx < KB_EARLY && dtx < KB_EARLY && now - k->at < KB_PERIOD_NS) return 0;
    k->at = now;
    if (!drx && !dtx) return 0;
    k->sent_rx = k->rx;
    k->sent_tx = k->tx;
    *rx = (uint32_t)drx;
    *tx = (uint32_t)dtx;
    return 1;
}

/* ---- loop counters ----------------------------------------------------------------------
 *
 * Enabled by STEER_TUN_STATS=1, printed every two seconds. They answer "the CPU is idle and it is
 * still not faster — where do we WAIT": loop turns, time in poll, reads from the server and their
 * size, writes to the client and their cost, and how often the client's window stopped a read. */
static int g_stats;
/* Counted ALWAYS, not only with STEER_TUN_STATS: drain_conn measures its per-pass byte limit
 * (DRAIN_MAX_BYTES) with it, and behaviour must not depend on diagnostics. */
static __thread uint64_t g_rx_total;

/* Smoothed loop pass period, ms; the RTO floor comes from it. Starts at RTO_MIN_MS /
 * RTO_LOOP_FACTOR, as if a pass were already long: the first passes after start are the longest
 * (handshakes), and a floor that is too low is the riskier error there. */
static __thread uint32_t g_loop_ms = RTO_MIN_MS / RTO_LOOP_FACTOR;

static uint32_t rto_floor(void) {
    uint32_t byloop = g_loop_ms * RTO_LOOP_FACTOR;
    uint32_t f = byloop > RTO_MIN_MS ? byloop : RTO_MIN_MS;
    return f > RTO_MAX_MS ? RTO_MAX_MS : f;
}
static __thread struct {
    uint64_t iters, poll_ns, tun_reads, tun_writes, tun_write_ns;
    uint64_t recs, rec_bytes, win_skips, drain_full;
    uint64_t recv_ns, pkt_ns, tun_read_ns;
    /* Retransmissions. Read them next to the loss: zero should mean the client got everything
     * the first time. */
    uint64_t rtx_sends, rtx_bytes;
    /* Datagrams that could not be sent when they arrived. For TCP this case hides in "window
     * waits" and the client's retransmission fixes it; for UDP it is a LOSS, and this is the
     * only place it shows. Zero here next to complaints about QUIC means the cause is not here. */
    uint64_t udp_drops;
    /* ACKs held until the queue to the node is ready (DC_ACK_PACED): one per transition of a
     * connection into holding. Many: the node is slower than the client, and the client is held
     * back by the window, not by loss. */
    uint64_t acks_held;
    /* Connections alive at the last pass: a stalled flow and a closed one both show zeros in
     * the other numbers. */
    uint32_t conns;
    /* The client's window as we computed it, and the most in flight: without both, "window
     * waits" cannot be told from "misread the window". */
    uint32_t win_min, win_max, inflight_max;
    uint16_t mss_min, mss_max;
    uint8_t wscale_seen;
} g_st;


static uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static void stats_dump(uint64_t window_ns) {
    double s = (double)window_ns / 1e9;
    if (s <= 0) return;
    fprintf(stderr,
            "tunvless[debug] tun-stats[%d]: %.1f MB/s | turns %.0f/s | poll %.0f%% | server reads %.0f/s "
            "(%.1f KB each) | client writes %.0f/s (%.0f us each) | window waits %.0f/s | "
            "read limit hit %.0f/s | connections %u | retransmits %.0f/s (%.0f KB/s) | "
            "datagrams dropped %.0f/s | ACKs held %.0f/s | "
            "client window %u..%u (scale %u), in flight up to %u | client MSS %u..%u | "
            "RTO floor %u ms | "
            "server read %.0f%% | TUN parse %.0f%% | TUN read %.0f%%\n",
            g_worker,
            (double)g_st.rec_bytes / s / 1048576.0,
            (double)g_st.iters / s,
            100.0 * (double)g_st.poll_ns / (double)window_ns,
            (double)g_st.recs / s,
            g_st.recs ? (double)g_st.rec_bytes / (double)g_st.recs / 1024.0 : 0.0,
            (double)g_st.tun_writes / s,
            g_st.tun_writes ? (double)g_st.tun_write_ns / (double)g_st.tun_writes / 1000.0 : 0.0,
            (double)g_st.win_skips / s,
            (double)g_st.drain_full / s,
            g_st.conns,
            (double)g_st.rtx_sends / s, (double)g_st.rtx_bytes / s / 1024.0,
            (double)g_st.udp_drops / s, (double)g_st.acks_held / s,
            g_st.win_min == 0xFFFFFFFFu ? 0 : g_st.win_min, g_st.win_max,
            g_st.wscale_seen, g_st.inflight_max,
            g_st.mss_min == 0xFFFF ? 0u : (unsigned)g_st.mss_min, (unsigned)g_st.mss_max,
            rto_floor(),
            100.0 * (double)g_st.recv_ns / (double)window_ns,
            100.0 * (double)g_st.pkt_ns / (double)window_ns,
            100.0 * (double)g_st.tun_read_ns / (double)window_ns);
    memset(&g_st, 0, sizeof(g_st));
    g_st.win_min = 0xFFFFFFFFu;
    g_st.mss_min = 0xFFFF;
}

/* Per-packet trace (STEER_TUN_TRACE): too noisy for normal work, essential when a connection
 * does not come up. */
static int g_trace;
#define TR(...) do { if (g_trace) fprintf(stderr, "tunvless[debug] tun: " __VA_ARGS__); } while (0)

/* ---- connector pool ---------------------------------------------------------------------
 *
 * The handshake runs outside the loop, in connector threads. A pool rather than a thread per
 * connection, for two reasons: a page's burst of requests would mean hundreds of threads, and,
 * more important, the transport caches the node address that worked last time in a __thread
 * (proto/transport/trdial.c), which a short-lived thread would lose on every connection, trying
 * dead addresses all over again.
 *
 * The queue is short ON PURPOSE. A full queue means "this CPU cannot digest more handshakes at
 * once", and the honest answer is to refuse the SYN: the client retries in a second, when there
 * is room. Accepting more than we can serve only makes the queue invisible. */
/* A handshake mostly waits on the node, so connectors outnumber the cores. */
#define CONNECTORS 16
#define CONNECTOR_FDS 4
#define CONNQ 32
/* How long a connector waits for the link to the node; also the link's read and write timeout
 * (SO_RCVTIMEO, SO_SNDTIMEO). Eight seconds, the default of the startup probe (--timeout): a node
 * the probe would call alive must not fail here on a shorter limit. --timeout does not change
 * this value. */
#define CONNECT_TIMEOUT_S 8

/* ---- spare sessions ---------------------------------------------------------------------
 *
 * The handshake with the node takes 100-400 ms, and with a job per SYN it lies entirely on the
 * critical path of the first request. The only way around it is to handshake IN ADVANCE. That is
 * possible because VLESS carries the destination in the request header, with the first data (see
 * upstream_send): until then the TLS session to the node belongs to nobody. For the stack this is
 * the dialer's DC_PRECONNECT (dialer.h); a protocol that needs the destination to connect gets no
 * pool. A flow the dialer puts on a shared link (dialer_mux) takes no spare either: the node pool
 * opens spares only to nodes whose flows do not share, and a connect that finds none returns
 * DIAL_NOSPARE, which is no failure.
 *
 * The pool is refilled ONLY when a SYN arrives: a quiet router makes no background handshakes.
 * Browsers open connections in bursts: the first SYN after a quiet spell (SPARE_TTL_MS) goes the
 * usual way and starts no refill, the next one starts it, and the rest get ready sessions.
 *
 * The lifetime is short not by our choice: Xray closes a connection that has not sent a VLESS
 * request within its handshake timeout, 4 s by default. An older spare is taken as dead without
 * checking: a check costs a round trip, and opening a new session is cheaper.
 *
 * Memory: the live part of a link is ~19 KB of TLS buffers, so the default four spares cost up
 * to 76 KB. */
#define SPARE_MAX 8
#define SPARE_TTL_MS 2500
#define SPARE_EMPTY   0
#define SPARE_FILLING 1
#define SPARE_READY   2
/* A spare is a whole dialer session, of which only the link is taken (dialer_ops.take). The
 * sessions are one heap block (stack_setup), SPARE_MAX of them g_sess_stride apart, because the
 * dialer names the session size. */
struct spare {
    void *sess;
    uint64_t born_ns;
    uint8_t state;
};
static struct spare g_spares[SPARE_MAX];
static pthread_mutex_t g_spare_mu = PTHREAD_MUTEX_INITIALIZER;
/* How many to keep. Set by STEER_TUN_SPARES (0 disables), read once in stack_run before the
 * threads start, read-only after. Also 0 for a dialer without DC_PRECONNECT: a link that needs
 * the destination cannot be prepared in advance. */
static int g_spare_want;
/* The last failed connect: while the node does not answer, refilling the pool only keeps
 * the connectors busy with doomed attempts, racing live jobs. */
static uint64_t g_spare_fail_ns;
static unsigned g_spare_fail_n;
#define SPARE_PAUSE_MS 5000
#define SPARE_PAUSE_MAX_MS 60000
static uint64_t g_spare_syn_ns;
static uint64_t g_spare_clock_ns;

/* The session is passed as a pointer, not computed by the connector from an index: g_sess is
 * per thread, and SESS() in another thread would address another thread's table — a bug that
 * shows as pieces of a stranger's connection once a day.
 *
 * c == NULL means a pool refill: sp is the g_spares slot and sess its session; the result stays
 * there for the next SYN to find. efd is the eventfd of the requesting loop thread: when the job
 * is done the connector wakes that loop, so early data does not wait up to 20 ms for the poll.
 * The job carries no node: the dialer (g_dl) holds it. */
struct connjob { struct conn *c; void *sess; struct spare *sp; int efd; void *link; int *done; };
static struct {
    struct connjob q[CONNQ];
    unsigned head, n;
    pthread_mutex_t mu;
    pthread_cond_t cv;
    int started;
} g_cq = { .mu = PTHREAD_MUTEX_INITIALIZER, .cv = PTHREAD_COND_INITIALIZER };

static uint32_t g_conn_busy_ms[CONNECTORS];
static int g_cq_efd[CONNECTORS];

static uint32_t mono_ms(void) { return (uint32_t)(now_ns() / 1000000) | 1u; }

static uint32_t connq_busy_ms(void) {
    uint32_t now = mono_ms(), most = 0;
    for (int i = 0; i < CONNECTORS; i++) {
        uint32_t t = __atomic_load_n(&g_conn_busy_ms[i], __ATOMIC_RELAXED);
        if (t && now - t > most) most = now - t;
    }
    return most;
}

static const char *connq_full_cause(uint32_t busy_ms, char *b, size_t n) {
    if (busy_ms < 3000) return "the CPU cannot keep up";
    snprintf(b, n, "the node does not answer (a handshake waits %u s)", (unsigned)(busy_ms / 1000));
    return b;
}

static uint64_t spare_now(void) { return g_spare_clock_ns ? g_spare_clock_ns : now_ns(); }

static void spare_note(int ok) {
    pthread_mutex_lock(&g_spare_mu);
    if (ok) {
        g_spare_fail_n = 0;
        g_spare_fail_ns = 0;
    } else {
        if (g_spare_fail_n < 32) g_spare_fail_n++;
        g_spare_fail_ns = spare_now();
    }
    pthread_mutex_unlock(&g_spare_mu);
}

static void efd_wake(int efd) {
    if (efd < 0) return;
    uint64_t one = 1;
    ssize_t wr = write(efd, &one, sizeof(one));
    (void)wr;
}

static void *connector(void *arg) {
    int me = (int)(intptr_t)arg;
    for (;;) {
        pthread_mutex_lock(&g_cq.mu);
        while (g_cq.n == 0) pthread_cond_wait(&g_cq.cv, &g_cq.mu);
        struct connjob j = g_cq.q[g_cq.head];
        g_cq.head = (g_cq.head + 1) % CONNQ;
        g_cq.n--;
        __atomic_store_n(&g_cq_efd[me], j.efd, __ATOMIC_RELAXED);
        pthread_mutex_unlock(&g_cq.mu);

        if (j.link) {
            __atomic_store_n(&g_conn_busy_ms[me], mono_ms(), __ATOMIC_RELAXED);
            int rc = g_dl->ops->mux->dial(g_dl->ctx, j.link, CONNECT_TIMEOUT_S);
            __atomic_store_n(&g_conn_busy_ms[me], 0, __ATOMIC_RELAXED);
            __atomic_store_n(j.done, 1, __ATOMIC_RELEASE);
            spare_note(rc == 0);
            efd_wake(j.efd);
            __atomic_store_n(&g_cq_efd[me], -1, __ATOMIC_RELEASE);
            continue;
        }

        if (!j.c) {
            /* Pool refill: the session stays in its g_spares slot, there is nobody to wake —
             * the next SYN takes it. The slot is ours (FILLING) until we give it back. The
             * dialer reports the outcome to node health checking itself, inside connect. */
            struct spare *sp = j.sp;
            __atomic_store_n(&g_conn_busy_ms[me], mono_ms(), __ATOMIC_RELAXED);
            int rc = g_dl->ops->connect(g_dl->ctx, j.sess, CONNECT_TIMEOUT_S);
            __atomic_store_n(&g_conn_busy_ms[me], 0, __ATOMIC_RELAXED);
            if (rc == DIAL_NOSPARE) {
                pthread_mutex_lock(&g_spare_mu);
                sp->state = SPARE_EMPTY;
                pthread_mutex_unlock(&g_spare_mu);
                continue;
            }
            pthread_mutex_lock(&g_spare_mu);
            if (rc == 0) {
                sp->born_ns = spare_now();
                sp->state = SPARE_READY;
            } else {
                sp->state = SPARE_EMPTY;
            }
            pthread_mutex_unlock(&g_spare_mu);
            spare_note(rc == 0);
            continue;
        }

        /* The dialer also reports the outcome to node health checking (a series of failures
         * brings a check forward): whether the node is alive is its measure. */
        __atomic_store_n(&g_conn_busy_ms[me], mono_ms(), __ATOMIC_RELAXED);
        int rc = g_dl->ops->connect(g_dl->ctx, j.sess, CONNECT_TIMEOUT_S);
        __atomic_store_n(&g_conn_busy_ms[me], 0, __ATOMIC_RELAXED);
        spare_note(rc == 0);
        j.c->rc = rc;
        /* RELEASE: the loop reads done with ACQUIRE, and everything written into the session
         * above must be visible to it by then. */
        __atomic_store_n(&j.c->done, 1, __ATOMIC_RELEASE);
        /* Wake the requester's loop, or a finished handshake waits for its poll, up to 20 ms
         * per connection. The loop may see done before this write, so connq_release also waits
         * until no connector holds its eventfd (g_cq_efd) before the loop closes it. An eventfd
         * counter overflow means "already woken". */
        efd_wake(j.efd);
        __atomic_store_n(&g_cq_efd[me], -1, __ATOMIC_RELEASE);
    }
    return NULL;
}

/* Queue a job; starts the connector threads on the first call.
 * 0 — accepted, -1 — queue full, -2 — no connector thread could be created.
 *
 * The queue counts as started only once at least one connector exists. Otherwise jobs would pile
 * up in a queue nobody serves: the client gets a SYN-ACK and then neither data nor RST. On
 * failure the log says so, the job is refused, and the next job tries to start the connectors
 * again. */
static int connq_push(const struct connjob *j) {
    pthread_mutex_lock(&g_cq.mu);
    if (!g_cq.started) {
        pthread_attr_t a;
        pthread_attr_init(&a);
        tstack_set(&a, 128 * 1024);
        pthread_attr_setdetachstate(&a, PTHREAD_CREATE_DETACHED);
        int made = 0, err = 0;
        for (int i = 0; i < CONNECTORS; i++) g_cq_efd[i] = -1;
        for (int i = 0; i < CONNECTORS; i++) {
            pthread_t t;
            err = pthread_create(&t, &a, connector, (void *)(intptr_t)i);
            if (err != 0) break;
            made++;
        }
        pthread_attr_destroy(&a);
        if (!made) {
            static time_t said;
            if (g_now_s - said >= 5) {
                said = g_now_s;
                fprintf(stderr, LOG_W "cannot create connector threads (%s) — "
                                "new connections through the tunnel are refused\n", strerror(err));
            }
            pthread_mutex_unlock(&g_cq.mu);
            return -2;
        }
        g_cq.started = 1;
    }
    if (g_cq.n == CONNQ) { pthread_mutex_unlock(&g_cq.mu); return -1; }
    g_cq.q[(g_cq.head + g_cq.n) % CONNQ] = *j;
    g_cq.n++;
    pthread_cond_signal(&g_cq.cv);
    pthread_mutex_unlock(&g_cq.mu);
    return 0;
}

/* Take a ready spare session. 0 — out is filled and the session is ours, -1 — none.
 *
 * The OLDEST usable one is taken: fresh ones survive another SYN, while the old one would be
 * thrown away by its lifetime anyway. Stale ones are closed right here: this is not a hot path,
 * SYNs come by tens per second, not thousands. */
static int spare_checkout(void *out) {
    const struct dialer_ops *d = g_dl->ops;
    uint64_t now = spare_now();
    struct spare *best = NULL;
    pthread_mutex_lock(&g_spare_mu);
    for (int i = 0; i < SPARE_MAX; i++) {
        struct spare *sp = &g_spares[i];
        if (sp->state != SPARE_READY) continue;
        if ((now - sp->born_ns) / 1000000 >= SPARE_TTL_MS) {
            d->close(sp->sess);
            sp->state = SPARE_EMPTY;
            continue;
        }
        /* The server may have closed the parked link already — visible without reading. POLLIN
         * alone is no verdict: a NewSessionTicket may wait there, and the read skips it. */
        struct pollfd pd = { .fd = d->fd(sp->sess), .events = POLLIN | POLLRDHUP };
        if (poll(&pd, 1, 0) > 0 && (pd.revents & (POLLERR | POLLHUP | POLLRDHUP))) {
            d->close(sp->sess);
            sp->state = SPARE_EMPTY;
            continue;
        }
        /* Several nodes (dialer_ops.match): a spare link goes to its own node and does not suit
         * every connection; a link to a node that is no longer active is thrown away. */
        int m = d->match ? d->match(g_dl->ctx, out, sp->sess) : 1;
        if (m < 0) {
            d->close(sp->sess);
            sp->state = SPARE_EMPTY;
            continue;
        }
        if (!m) continue;
        if (!best || sp->born_ns < best->born_ns) best = sp;
    }
    /* Moving the link is the dialer's job: it knows what in the session is the link and what is
     * flow state, and which pointers in the link point into itself. Under the lock: the slot
     * becomes empty only after the move. */
    if (best) {
        d->take(out, best->sess);
        best->state = SPARE_EMPTY;
    }
    pthread_mutex_unlock(&g_spare_mu);
    return best ? 0 : -1;
}

/* Close stale spare sessions. Needed because spare_checkout runs only on SYN: once clients stop
 * opening connections the pool would stay full FOREVER. The server closes such parked links
 * itself within seconds, but on our side each is still an open descriptor and ~19 KB of TLS
 * buffers. Called from the deadline pass, once a second per thread. */
static void spare_sweep(void) {
    if (g_spare_want <= 0) return;
    uint64_t now = spare_now();
    pthread_mutex_lock(&g_spare_mu);
    for (int i = 0; i < SPARE_MAX; i++) {
        struct spare *sp = &g_spares[i];
        if (sp->state != SPARE_READY) continue;
        if ((now - sp->born_ns) / 1000000 < SPARE_TTL_MS) continue;
        g_dl->ops->close(sp->sess);
        sp->state = SPARE_EMPTY;
    }
    pthread_mutex_unlock(&g_spare_mu);
}

/* Refill the pool up to g_spare_want. Called on every SYN, i.e. exactly when connections are
 * being opened. Slots are reserved under the lock and the jobs are queued without it: connq_push
 * takes its own mutex, and there is no need to nest the two. */
static void spare_refill(void) {
    if (g_spare_want <= 0) return;
    int fill[SPARE_MAX];
    int nfill = 0;
    pthread_mutex_lock(&g_spare_mu);
    uint64_t now = spare_now(), prev = g_spare_syn_ns;
    g_spare_syn_ns = now;
    if (!prev || (now - prev) / 1000000 >= SPARE_TTL_MS) {
        pthread_mutex_unlock(&g_spare_mu);
        return;
    }
    if (g_spare_fail_ns) {
        uint64_t pause = g_spare_fail_n >= 5 ? SPARE_PAUSE_MAX_MS
                                             : (uint64_t)SPARE_PAUSE_MS << (g_spare_fail_n ? g_spare_fail_n - 1 : 0);
        if ((now - g_spare_fail_ns) / 1000000 < pause) {
            pthread_mutex_unlock(&g_spare_mu);
            return;
        }
    }
    int have = 0;
    for (int i = 0; i < SPARE_MAX; i++)
        if (g_spares[i].state != SPARE_EMPTY) have++;
    for (int i = 0; i < SPARE_MAX && have + nfill < g_spare_want; i++) {
        if (g_spares[i].state != SPARE_EMPTY) continue;
        g_spares[i].state = SPARE_FILLING;
        fill[nfill++] = i;
    }
    pthread_mutex_unlock(&g_spare_mu);

    for (int k = 0; k < nfill; k++) {
        struct connjob j = { .c = NULL, .sess = g_spares[fill[k]].sess, .sp = &g_spares[fill[k]],
                             .efd = -1 };
        if (connq_push(&j) != 0) {
            /* Queue full: live jobs come first. Give back ALL remaining reservations — a
             * FILLING slot without a job would never be filled by anyone. */
            pthread_mutex_lock(&g_spare_mu);
            for (; k < nfill; k++) g_spares[fill[k]].state = SPARE_EMPTY;
            pthread_mutex_unlock(&g_spare_mu);
            return;
        }
    }
}

/* This loop thread's eventfd: a connector writes to it when a job is done. Lives from entering
 * worker_loop to leaving it; -1 if the kernel gave no eventfd — then the loop polls every 20 ms
 * while jobs are pending. */
static __thread int g_conn_efd = -1;

/* Remove this thread's jobs from the queue and wait for those already being worked on.
 *
 * Needed when the loop exits (epoll returned an error). Otherwise draining the table would close
 * a session a connector is working with: the descriptor goes back to the kernel and is reused by
 * another thread while the connector still uses it, and the owner unmaps its tables while the
 * connector keeps writing there.
 *
 * Jobs not yet taken are simply dropped. Only taken ones are waited for: at most CONNECTORS, each
 * bounded by the connect timeout.
 *
 * 0 — all released; -1 — a connector never reported. On -1 the table must NOT be freed: freeing
 * it under a working connector is exactly what this guards against. Leaking is the lesser evil:
 * the loop leaves only on a fatal epoll error. */
static int conn_ready(struct conn *c, const struct tun_dev *tun);
static __thread const struct tun_dev *g_mux_tun;

static int connq_release(struct conn *base) {
    const struct dialer_mux *mx = g_dl->ops->mux;
    void *gone[CONNQ];
    unsigned ngone = 0;
    pthread_mutex_lock(&g_cq.mu);
    unsigned kept = 0;
    for (unsigned i = 0; i < g_cq.n; i++) {
        struct connjob j = g_cq.q[(g_cq.head + i) % CONNQ];
        if (j.link && j.sess == (void *)base) {
            gone[ngone++] = j.link;
            continue;
        }
        if (j.c >= base && j.c < base + MAX_CONNS) {
            j.c->pending = 0;            /* nobody will touch our record any more */
            continue;
        }
        g_cq.q[(g_cq.head + kept++) % CONNQ] = j;
    }
    g_cq.n = kept;
    pthread_mutex_unlock(&g_cq.mu);
    for (unsigned i = 0; i < ngone; i++) mx->dropped(gone[i]);
    for (int li = 0; g_mux_tun && li < g_live_n;) {
        struct conn *c = &g_conns[g_live[li]];
        if (c->mux && c->pending && __atomic_load_n(&c->done, __ATOMIC_ACQUIRE) && conn_ready(c, g_mux_tun)) continue;
        li++;
    }

    /* Wait for the taken jobs to report. A generous limit: the connect timeout is eight seconds,
     * and waiting it out is cheaper than chasing memory corruption on a live router. The check
     * also runs AFTER the last sleep, so a report in the last 10 ms is not missed. */
    for (int spin = 0;; spin++) {
        int busy = mx ? mx->busy() : 0;
        for (int i = 0; i < MAX_CONNS; i++)
            if (base[i].used && base[i].pending && !base[i].mux &&
                !__atomic_load_n(&base[i].done, __ATOMIC_ACQUIRE)) busy++;
        for (int i = 0; g_conn_efd >= 0 && i < CONNECTORS; i++)
            busy += __atomic_load_n(&g_cq_efd[i], __ATOMIC_ACQUIRE) == g_conn_efd;
        if (!busy) return 0;
        if (spin == 1500) break;
        struct timespec ts = { .tv_sec = 0, .tv_nsec = 10 * 1000 * 1000 };
        nanosleep(&ts, NULL);
    }
    fprintf(stderr, LOG_W "a connector did not report within 15 s, the table is not freed\n");
    return -1;
}

/* Queue a job for this connection; the result is connq_push's. */
static int conn_submit(struct conn *c) {
    struct connjob j = { .c = c, .sess = SESS(c), .sp = NULL, .efd = g_conn_efd };
    return connq_push(&j);
}

static int mux_submit(void *link, int *done) {
    struct connjob j = { .sess = g_conns, .efd = g_conn_efd, .link = link, .done = done };
    return connq_push(&j);
}

static int flow_link(struct conn *c, int spare_ok) {
    const struct dialer_ops *d = g_dl->ops;
    int a = d->mux ? d->mux->attach(g_dl->ctx, SESS(c), (uint16_t)(c - g_conns), NULL) : XA_NONE;
    if (a == XA_READY) {
        c->mux = 1;
        c->fd = -1;
        return 0;
    }
    if (a == XA_WAIT || a < 0) {
        c->mux = 1;
        c->fd = -1;
        c->rc = a < 0 ? a : 0;
        __atomic_store_n(&c->done, a < 0, __ATOMIC_RELEASE);
        c->pending = 1;
        return 1;
    }
    if (spare_ok && g_spare_want > 0 && spare_checkout(SESS(c)) == 0) {
        c->fd = d->fd(SESS(c));
        node_sock_silence(c->fd);
        return 0;
    }
    int qr = conn_submit(c);
    if (qr != 0) return qr;
    c->pending = 1;
    return 1;
}

/* Set up the table, once per thread: all slots free, buckets empty. 0 — ready, -1 — no memory.
 *
 * One mmap rather than malloc: the size is about ten megabytes, and what is wanted is fresh zero
 * pages that the kernel provides on first touch. Only the hot table and the first bytes of each
 * session (dialer_ops.clear) are touched here: one page per slot. */
static int conn_table_init(void) {
    if (!g_conns) {
        size_t len = TABLES_HEAD + (size_t)MAX_CONNS * g_sess_stride;
        void *m = mmap(NULL, len, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
        if (m == MAP_FAILED) return -1;
        struct conn_tables *t = m;
        g_conns = t->conns;
        g_live = t->live;
        g_freelist = t->freelist;
        g_bucket = t->bucket;
        g_sess = (unsigned char *)m + TABLES_HEAD;
        g_ls = &t->ls;
        g_tables_len = len;
    }
    g_live_n = 0;
    g_free_n = 0;
    for (int i = MAX_CONNS - 1; i >= 0; i--) {
        g_conns[i].livepos = -1;
        g_conns[i].hnext = -1;
        g_conns[i].fd = -1;
        g_dl->ops->clear(SESS(&g_conns[i]));
        g_freelist[g_free_n++] = (uint16_t)i;
    }
    for (int i = 0; i < CONN_BUCKETS; i++) g_bucket[i] = -1;
    return 0;
}

/* Give the thread's tables back to the system. Only once the connectors are released
 * (connq_release returned 0): otherwise they would write into an unmapped region. */
static void conn_table_free(void) {
    if (!g_conns) return;
    munmap(g_conns, g_tables_len);
    g_conns = NULL;
    g_live = g_freelist = NULL;
    g_bucket = NULL;
    g_sess = NULL;
    g_ls = NULL;
    g_tables_len = 0;
}

static struct conn *conn_find(const struct flow_key *k) {
    for (int16_t i = g_bucket[flow_hash(k)]; i >= 0; i = g_conns[i].hnext) {
        struct conn *c = &g_conns[i];
        if (fk_same_addr(&c->key.src, &k->src) && fk_same_addr(&c->key.dst, &k->dst) &&
            c->key.v6 == k->v6 &&
            c->key.sport == k->sport && c->key.dport == k->dport &&
            c->key.proto == k->proto)
            return c;
    }
    return NULL;
}

/* Add the slot to the live list and the hash; its key must already be set. */
static void conn_link(struct conn *c) {
    int16_t idx = (int16_t)(c - g_conns);
    c->livepos = (int16_t)g_live_n;
    g_live[g_live_n++] = (uint16_t)idx;
    unsigned b = flow_hash(&c->key);
    c->hnext = g_bucket[b];
    g_bucket[b] = idx;
}

/* Remove from the live list and the hash. The last element fills the hole in g_live: walk order
 * does not matter, and shifting the tail would cost more the more connections there are. */
static void conn_unlink(struct conn *c) {
    int16_t idx = (int16_t)(c - g_conns);
    if (c->livepos >= 0) {
        int last = --g_live_n;
        uint16_t moved = g_live[last];
        g_live[c->livepos] = moved;
        g_conns[moved].livepos = c->livepos;
        c->livepos = -1;
    }
    unsigned b = flow_hash(&c->key);
    int16_t *slot = &g_bucket[b];
    while (*slot >= 0) {
        if (*slot == idx) { *slot = c->hnext; break; }
        slot = &g_conns[*slot].hnext;
    }
    c->hnext = -1;
}

static void conn_drop(struct conn *c);

static void ctl_reply(const struct tun_dev *tun, const struct flow_key *k, uint32_t seq,
                      uint32_t ack, unsigned char flags, uint16_t win) {
    unsigned char pkt[64];
    size_t l = k->v6 ? tcp6_build(pkt, sizeof pkt, k->dst.b, k->src.b, k->dport, k->sport, seq, ack,
                                  flags, win, NULL, 0, NULL, 0)
                     : tcp_build(pkt, sizeof pkt, k->dst.v4, k->src.v4, k->dport, k->sport, seq, ack,
                                 flags, NULL, 0, win, 0, -1);
    if (l) tun_write_ctl(tun, pkt, l);
}

/* IP ID for TCP data and datagrams to the client. It matters only for fragmentation: the client
 * uses it to reassemble the fragments of one packet. Per thread: a shared counter would fight over
 * a cache line for a number only the reassembler cares about. */
static __thread uint16_t g_ip_id;

#define TUN_MSS_FLOOR 64
#define TUN_MSS_DEFAULT 536
#define TUN6_MSS_DEFAULT 1220

static uint16_t conn_mss(const struct flow_key *k) {
    unsigned top = k->v6 ? TUN6_MSS : TUN_MSS;
    unsigned m = k->mss ? k->mss : (k->v6 ? TUN6_MSS_DEFAULT : TUN_MSS_DEFAULT);
    if (m > top) m = top;
    if (m < TUN_MSS_FLOOR) m = TUN_MSS_FLOOR;
    return (uint16_t)m;
}

static size_t seg_max(const struct conn *c, const struct tun_dev *tun) {
    if (tun->gso) return TUN_GSO_MAX;
    return c->mss;
}

static int seg_write(const struct tun_dev *tun, const struct conn *c, uint32_t seq,
                     const unsigned char *p, size_t n) {
    unsigned char hdr[TUN6_HDR_LEN];
    if (c->key.v6) {
        tcp6_hdr_build(hdr, c->key.dst.b, c->key.src.b, c->key.dport, c->key.sport, seq,
                       c->client_seq, TCP_ACK | TCP_PSH, rcv_win_field(c));
        return tun_write_data6(tun, hdr, p, n, c->mss);
    }
    uint16_t id = (uint16_t)(g_ip_id + 1);
    g_ip_id = (uint16_t)(g_ip_id + tun_pieces(n, c->mss));
    tcp_hdr_build(hdr, c->key.dst.v4, c->key.src.v4, c->key.dport, c->key.sport, seq,
                  c->client_seq, TCP_ACK | TCP_PSH, n, rcv_win_field(c), id);
    return tun_write_data(tun, hdr, p, n, c->mss);
}

/* A free slot; if there is none, evict the longest idle connection and take its slot.
 *
 * Refusing while idle connections hold the table is the worst choice: browsers keep connections
 * ALIVE between requests (keep-alive) for minutes without sending a byte, so with a wide set of
 * routes the table fills up and every new connection is dropped while the tunnel looks healthy.
 *
 * A new connection matters more than the longest idle one: what the user opens now beats a
 * keep-alive to a site closed five minutes ago. The evicted connection's client gets RST and
 * reconnects; silence would leave it waiting for an answer that never comes.
 *
 * Returns NULL only when there is nothing to evict: all connections are fresh. */
static struct conn *conn_new(const struct tun_dev *tun) {
    if (g_free_n) return &g_conns[g_freelist[--g_free_n]];

    time_t now = g_now_s;
    struct conn *old = NULL;
    for (int li = 0; li < g_live_n; li++) {
        struct conn *c = &g_conns[g_live[li]];
        /* Connections closed by the server go first at any age: they bring nothing more and
         * live only to finish delivering to the client. */
        if (c->pending) continue;        /* a connector writes into this record — do not touch */
        int cheap = c->srv_closed != 0;
        /* The others only if REALLY idle. Evicting the oldest LIVE connection is worse than
         * refusing: every SYN costs a handshake (100-400 ms), and taking it at the price of a
         * working connection turns one core into a thrasher — the loop busy with handshakes,
         * existing connections unserved, clients retrying, more SYNs. If none is idle, refuse:
         * the client retries the SYN in a second, when a slot has likely freed up.
         *
         * A DNS flow (UDP to port 53) is an exception to the threshold. A resolver takes a new
         * source port per query, so such a flow is one query and one answer, and after
         * DNS_IDLE_S of silence it is done: the answer came, or the client asked again from
         * another port. By the general threshold a table full of them would refuse new
         * connections, plain TCP included, for 120 s. The idle timeout itself does NOT change:
         * the exception applies only here, when no slot is free and the choice is to refuse. */
        int dns_done = c->is_udp && c->key.dport == 53 && now - c->last >= DNS_IDLE_S;
        if (!cheap && !dns_done && now - c->last < IDLE_EVICT_S) continue;
        if (!old || cheap > (old->srv_closed != 0) ||
            (cheap == (old->srv_closed != 0) && c->last < old->last))
            old = c;
    }
    if (!old) {
        static struct ratelog said;
        unsigned held;
        char more[48];
        if (ratelog_take(&said, (unsigned)now, 10, &held))
            fprintf(stderr, LOG_W "all %d connection slots hold active connections, new ones "
                            "are refused — too much is routed into the tunnel%s\n", MAX_CONNS,
                    ratelog_more(more, sizeof more, held));
        return NULL;
    }

    /* RST only to an evicted TCP connection: its client waits for an answer on an open
     * connection and without a refusal would sit until its timeout. A UDP flow has no such wait,
     * and an RST for a datagram is a packet the client's stack would not understand. */
    if (!old->is_udp && !old->sock)
        ctl_reply(tun, &old->key, old->our_seq, old->client_seq, TCP_RST | TCP_ACK, 0);
    /* Evicting a dead connection is housekeeping, not an event, and a log that shouts during normal work
     * stops being read. */
    static struct ratelog said_evict;
    unsigned held;
    char more[48];
    if (ratelog_take(&said_evict, (unsigned)now, 10, &held))
        fprintf(stderr, LOG_W "table full (%d), evicting open connections "
                        "(this one idle %lu s) — too much is routed into the tunnel%s\n",
                MAX_CONNS, (unsigned long)(now - old->last), ratelog_more(more, sizeof more, held));
    conn_drop(old);
    /* conn_drop put the slot back in the free list: take it from there, not by the old pointer.
     * Otherwise the slot would be both ours and in the list, and the next SYN would get a BUSY
     * record — one record for two connections, and mixed-up data. */
    return &g_conns[g_freelist[--g_free_n]];
}

/* Free the slot. close() removes the descriptor from epoll by itself: no epoll_ctl is needed,
 * and one after close would hand the kernel a closed number. */
static void sock_linger(int fd, int rst) {
    struct linger lg = { rst, 0 };
    if (fd >= 0) setsockopt(fd, SOL_SOCKET, SO_LINGER, &lg, sizeof lg);
}

static void sock_rst(int fd) {
    sock_linger(fd, 1);
    close(fd);
}

static void sock_soft_close(int fd) {
    unsigned char sink[4096];
    sock_linger(fd, 0);
    shutdown(fd, SHUT_WR);
    for (int i = 0; i < 16 && recv(fd, sink, sizeof sink, MSG_DONTWAIT) > 0; i++) {}
    close(fd);
}

static void ib_udp_forget(const struct conn *c) {
    struct ib_assoc *a = &g_assoc[c->ib_udp - 1];
    for (int k = 0; k < IB_UDP_DST; k++)
        if (a->dst_conn[k] == c - g_conns) a->dst_conn[k] = -1;
}

static void conn_drop(struct conn *c) {
    TR("closing conn#%ld fd=%d\n", (long)(c - g_conns), c->fd);
    if (c->ib_udp) ib_udp_forget(c);
    if (g_ls->up.c == c) g_ls->up.c = NULL;                 /* gathered bytes: never acknowledged */
    if (c->sock && c->cfd >= 0) sock_rst(c->cfd);
    if (c->used) {
        g_dl->ops->close(SESS(c));
        conn_unlink(c);
        g_freelist[g_free_n++] = (uint16_t)(c - g_conns);
    }
    rtx_done(&c->rtx);
    free(c->early);
    /* The bulky half is NOT wiped whole: tens of kilobytes of memset per close would push zeros
     * through the cache that the next connect overwrites anyway. The dialer prepares the session
     * for the next connection (clear), touching only its start. */
    memset(c, 0, sizeof(*c));
    c->fd = -1;
    c->cfd = -1;
    c->livepos = -1;
    c->hnext = -1;
    g_dl->ops->clear(SESS(c));
}

/* Send the client's data to the node. Its form — request header, wrappers, transport framing —
 * is the dialer's business (dialer_ops.send); the result is SEND_* (dialer.h). */
/* dialer_ops.room for an established TCP connection; -1: no limit or does not apply (UDP, the
 * connector still working). */
static long conn_room(const struct conn *c) {
    if (c->is_udp || c->pending || (c->fd < 0 && !c->mux) || !g_dl->ops->room) return -1;
    return g_dl->ops->room(g_dl->ctx, SESS(c));
}

static int conn_due(struct conn *c, uint64_t now_ms, int *next_ms) {
    *next_ms = -1;
    if (c->no_due || !g_dl->ops->due) return 0;
    if (g_dl->ops->due(SESS(c), now_ms, next_ms) != SEND_OK) return -1;
    if (*next_ms == -2) {
        c->no_due = 1;
        *next_ms = -1;
    }
    c->fm_wait = (uint8_t)(*next_ms < 0 ? 0 : *next_ms ? 1 : 2);
    return 0;
}

static int upstream_send(struct conn *c, const unsigned char *data, size_t n) {
    int sr = g_dl->ops->send(g_dl->ctx, SESS(c), &c->key, c->is_udp, data, n);
    if (sr == SEND_OK) c->sent_any = 1;
    int next;
    if (sr == SEND_OK && conn_due(c, g_now_ns / 1000000, &next)) return SEND_FATAL;
    return sr;
}

/* SYN-ACK to the client. The ISN is always 1 and is not kept in our_seq: our_seq starts at 2 (the
 * next data byte), so resending the SYN-ACK is literally the same packet and moves no counter. */
static void send_synack(struct conn *c, const struct tun_dev *tun) {
    unsigned char sa[80];
    size_t sl;
    uint16_t win = c->pending ? EARLY_CAP : RCV_WND_MIN;
    if (c->key.v6) {
        unsigned char opt[8] = { 2, 4, TUN6_MSS >> 8, TUN6_MSS & 0xFF, 1, 3, 3, g_rcv_shift };
        sl = tcp6_build(sa, sizeof(sa), c->key.dst.b, c->key.src.b, c->key.dport, c->key.sport,
                        1, c->client_seq, TCP_SYN | TCP_ACK, win, opt,
                        c->ws_on ? 8 : 4, NULL, 0);
    } else {
        sl = tcp_build(sa, sizeof(sa), c->key.dst.v4, c->key.src.v4,
                       c->key.dport, c->key.sport,
                       1, c->client_seq, TCP_SYN | TCP_ACK,
                       NULL, 0, win, TUN_MSS, c->ws_on ? (int)g_rcv_shift : -1);
    }
    if (sl) tun_write_ctl(tun, sa, sl);
    TR("SYN-ACK sent (ack=%u)\n", c->client_seq);
}

/* RST to the client, and free the record. A UDP flow has nothing to reset: the client has no
 * connection, and learns of the close by the lack of answers, as of any lost datagram. */
static void conn_rst(const struct conn *c, const struct tun_dev *tun) {
    /* The client accepts RST only with a sequence number EQUAL to the expected one (RFC 5961);
     * one inside the window but not equal draws a challenge ACK, and the connection stays. With
     * data in flight the expected number is either our_seq (all arrived, the ACK is on its way)
     * or client_ack (nothing arrived): send RST with the second, then with the first. If part
     * arrived, the client answers with a challenge ACK, and handle_packet answers that ACK
     * without a connection with RST. */
    if (c->client_ack != c->our_seq)
        ctl_reply(tun, &c->key, c->client_ack, c->client_seq, TCP_RST | TCP_ACK, 0);
    ctl_reply(tun, &c->key, c->our_seq, c->client_seq, TCP_RST | TCP_ACK, 0);
}

static void conn_reset(struct conn *c, const struct tun_dev *tun) {
    if (!c->is_udp && !c->sock) conn_rst(c, tun);
    conn_drop(c);
}

/* The connection's link to the node ended with an error: if it was cut (node_sock_aborted), tell
 * the dialer, whose node health checking takes it as a reason to check the node now. 1 — cut. A
 * shared link's cut of 2 (the server refused the stream) resets the client too, but says nothing
 * about the node's health, so lost is not called. */
static int conn_node_lost(struct conn *c) {
    int cut = c->mux ? g_dl->ops->mux->cut(SESS(c)) : node_sock_aborted(c->fd);
    if (!cut) return 0;
    if (cut == 1 && g_dl->ops->lost) g_dl->ops->lost(g_dl->ctx, SESS(c));
    return 1;
}

/* Send the early data to the server. 0 — the buffer is empty and freed; 1 — a tail is left (the
 * HTTP/2 window is closed, it goes later); -1 — the flow is broken, the connection is over. Until
 * the tail is sent no fresh client data goes to the server: the bytes would swap places, and a
 * TCP stream does not forgive that. */
static int early_flush(struct conn *c) {
    while (c->early_off < c->early_n) {
        if (c->is_udp) {
            /* Datagrams one at a time, a call each (each has its length in front, see
             * udp_send_dgram): the dialer may send each datagram as its own message, and joining
             * them would merge them into one. The tail starts at a datagram boundary. */
            uint32_t dl;
            memcpy(&dl, c->early + c->early_off, sizeof(dl));
            if (dl > c->early_n - c->early_off - sizeof(dl)) return -1;
            int sr = upstream_send(c, c->early + c->early_off + sizeof(dl), dl);
            if (sr == SEND_AGAIN) return 1;
            if (sr != SEND_OK) return -1;
            c->early_off += (uint32_t)sizeof(dl) + dl;
            continue;
        }
        size_t chunk = c->early_n - c->early_off;
        /* Room for the request header and wrapper (for VLESS, a Vision frame): the dialer
         * assembles them in one TUNNEL_BUF buffer, and a full-size chunk would not fit. */
        if (chunk > TUNNEL_BUF - 2048) chunk = TUNNEL_BUF - 2048;
        int sr = upstream_send(c, c->early + c->early_off, chunk);
        if (sr == SEND_AGAIN) return 1;
        if (sr != SEND_OK) return -1;
        c->early_off += (uint32_t)chunk;
    }
    free(c->early);
    c->early = NULL;
    c->early_n = c->early_off = 0;
    return 0;
}

/* Hold data until the flow is ready. 0 — taken, -1 — does not fit or no memory.
 *
 * One function for TCP and UDP, because both hold EXACTLY what will go to the server: for TCP the
 * byte stream as is, for UDP datagrams already framed by the dialer (dgram_frame), each with its
 * own length in front (udp_send_dgram), since not every framing keeps datagram boundaries. */
static int early_hold(struct conn *c, const unsigned char *d, size_t n) {
    if (n > EARLY_CAP - c->early_n) return -1;
    if (!c->early) {
        c->early = malloc(EARLY_CAP);
        if (!c->early) return -1;
    }
    memcpy(c->early + c->early_n, d, n);
    c->early_n += (uint32_t)n;
    return 0;
}

/* ---- UDP -------------------------------------------------------------------- */
/* A datagram to the server: the dialer's framing and the data in ONE piece.
 *
 * One piece because h2_write sends all or nothing: a datagram split across two calls could go out
 * half-way with the window closed, the server would read the length and wait for a tail that never
 * comes, and the next datagram would land inside the previous one.
 *
 * 64 bytes of room for the framing: for VLESS it is a two-byte length, but the stack's buffer
 * should not depend on whose framing it is. */
static int udp_send_dgram(struct conn *c, const unsigned char *p, size_t n) {
    unsigned char *fr = g_ls->dgram;
    if (n > UDP_DGRAM_MAX) return SEND_FATAL;
    size_t fn = g_dl->ops->dgram_frame(g_dl->ctx, SESS(c), p, n, fr, sizeof g_ls->dgram);
    if (!fn) return SEND_FATAL;
    /* The session is still being set up: it must not be written to — a connector is working in
     * it — but the datagram can be held. The check is HERE, in the only place that knows the
     * framing; spread over the callers it would be a second rule of who may touch the session,
     * and the two would one day diverge. */
    if (c->pending) {
        uint32_t dl = (uint32_t)fn;
        if (sizeof(dl) + fn > EARLY_CAP - c->early_n) return SEND_AGAIN;
        if (early_hold(c, (const unsigned char *)&dl, sizeof(dl)) != 0) return SEND_AGAIN;
        return early_hold(c, fr, fn) == 0 ? SEND_OK : SEND_AGAIN;
    }
    return upstream_send(c, fr, fn);
}

/* Where the dialer delivers the payload (dialer_ops.deliver): the connection and its device, plus
 * a count of what was delivered, for the trace line. */
struct emit_ctx {
    struct conn *c;
    const struct tun_dev *tun;
    size_t total;
};

/* A whole datagram from the node to the client. */
static int emit_dgram(void *arg, const unsigned char *p, size_t n) {
    struct emit_ctx *e = arg;
    struct conn *c = e->c;
    if (c->key.v6 && n > TUN_MTU - 48) {
        if (g_stats) g_st.udp_drops++;
        return 0;
    }
    if (c->key.v6 ? udp6_write_to_client(e->tun, c->key.dst.b, c->key.src.b, c->key.dport,
                                         c->key.sport, p, n)
                  : udp_write_to_client(e->tun, c->key.dst.v4, c->key.src.v4, c->key.dport,
                                        c->key.sport, p, n, ++g_ip_id))
        return -1;
    TR("datagram to client, %zu bytes\n", n);
    /* The timestamp moves on EVERY datagram, not only on client packets: a receive-only flow
     * (common for UDP, e.g. updates from a game server) would otherwise be removed as idle while
     * working. */
    c->last = g_now_s;
    return 0;
}

static void ib_udp_touch(struct ib_assoc *a, const struct conn *c) {
    for (int k = 0; k < IB_UDP_DST; k++)
        if (a->dst_conn[k] == c - g_conns) a->last_ms[k] = g_now_ns / 1000000;
}

static int emit_ib_dgram(void *arg, const unsigned char *p, size_t n) {
    static unsigned char b[10 + 65535];
    struct emit_ctx *e = arg;
    struct conn *c = e->c;
    struct ib_assoc *a = &g_assoc[c->ib_udp - 1];
    c->last = g_now_s;
    ib_udp_touch(a, c);
    if (n > sizeof b - 10 || !a->client.sin_family) return 0;
    b[0] = b[1] = b[2] = 0;
    b[3] = 1;
    memcpy(b + 4, &c->key.dst.v4, 4);
    b[8] = (unsigned char)(c->key.dport >> 8);
    b[9] = (unsigned char)c->key.dport;
    memcpy(b + 10, p, n);
    if (sendto(a->ufd, b, 10 + n, MSG_DONTWAIT, (const struct sockaddr *)&a->client, sizeof a->client) ==
        (ssize_t)(10 + n))
        kb_add(c, n, 0);
    return 0;
}

/* Send a piece of the stream to the client as TCP.
 *
 * One call per piece and no intermediate buffer: the data goes to the device straight from where
 * it lies after decryption.
 *
 * The piece size depends on offload (GSO): with it a whole record goes in one write and the
 * kernel segments it by the client's MSS; without it we cut segments of that MSS ourselves and pay
 * a write and a checksum pass per segment. */
static int emit_to_client(struct conn *c, const struct tun_dev *tun,
                          const unsigned char *p, size_t n) {
    /* Copy for retransmission BEFORE sending. Room was checked in advance
     * (client_can_take_record); if there is none, do not send: what is sent without a copy can
     * never be retransmitted.
     *
     * The ring appears here, on the first data rather than on SYN, and grows on demand. A failed
     * allocation is safe: we do not send, so we do not read further from the server and lose
     * nothing. A failed growth is not an error either: the old ring works, the window is just
     * smaller. */
    if (!c->rtx.cap && rtx_init(&c->rtx, RTX_START) != 0) {
        TR("no memory for the retransmission ring — not sending %zu bytes\n", n);
        return -1;
    }
    if (rtx_room(&c->rtx) < n && c->rtx.cap < RTX_CAP) rtx_grow(&c->rtx, RTX_CAP);
    if (rtx_room(&c->rtx) < n) {
        TR("retransmission ring full (%u of %u) — not sending %zu bytes\n",
           c->rtx.len, c->rtx.cap, n);
        return -1;
    }
    if (!c->rtx.len) { c->rtx_at = g_now_ns; c->rto_ms = rto_floor(); }
    rtx_push(&c->rtx, p, (uint32_t)n);

    size_t seg = seg_max(c, tun);
    size_t sent = 0;
    while (sent < n) {
        size_t chunk = n - sent > seg ? seg : n - sent;
        uint64_t w0 = g_stats ? now_ns() : 0;
        if (seg_write(tun, c, c->our_seq, p + sent, chunk) != 0) {
            TR("TUN write failed (%zu bytes): %s\n", chunk, strerror(errno));
            return -1;
        }
        if (g_stats) { g_st.tun_writes++; g_st.tun_write_ns += now_ns() - w0; }
        c->our_seq += (uint32_t)chunk;
        sent += chunk;
        /* Data carries the ACK in its header, so no separate ACK to the client is needed. */
        c->ack_due = 0;
    }
    return 0;
}

/* Resend from the start of the unacknowledged data.
 *
 * One segment, not the whole ring: sending it all on every timeout is an avalanche that creates
 * its own losses; TCP does the same for the same reason. If more was lost, the next round of ACKs
 * shows it and the retransmission goes on.
 *
 * The segment size is the one used for sending: with offload a whole 16 KB piece may have been
 * lost, and resending it one MSS at a time would stretch recovery over ten timeouts. */
static void rtx_resend(struct conn *c, const struct tun_dev *tun, const char *why) {
    const unsigned char *body = NULL;
    uint32_t n = rtx_peek(&c->rtx, (uint32_t)seg_max(c, tun), &body);
    if (!n) return;

    TR("resend conn#%ld: %u bytes from seq=%u (%s)\n",
       (long)(c - g_conns), n, c->client_ack, why);
    if (seg_write(tun, c, c->client_ack, body, n) != 0) return;
    if (g_stats) { g_st.rtx_sends++; g_st.rtx_bytes += n; }
    c->rtx_at = g_now_ns;
    c->rto_ms = c->rto_ms * 2 > RTO_MAX_MS ? RTO_MAX_MS : c->rto_ms * 2;
}

/* A piece of the stream from the node to the client, as TCP. */
static int emit_stream(void *arg, const unsigned char *p, size_t n) {
    struct emit_ctx *e = arg;
    if (emit_to_client(e->c, e->tun, p, n) != 0) return -1;
    e->total += n;
    return 0;
}

static void sock_cut(struct conn *c) {
    c->aborted = 1;
    if (!c->srv_closed) {
        c->srv_closed = 1;
        c->closed_at = g_now_ns;
    }
}

static int sock_again(void) { return errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR; }

static int emit_sock(void *arg, const unsigned char *p, size_t n) {
    struct emit_ctx *e = arg;
    struct conn *c = e->c;
    e->total += n;
    if (!c->rtx.len) {
        ssize_t w = send(c->cfd, p, n, MSG_DONTWAIT | MSG_NOSIGNAL);
        if (w < 0 && !sock_again()) {
            sock_cut(c);
            return -1;
        }
        if (w > 0) {
            kb_add(c, (uint64_t)w, 0);
            p += w;
            n -= (size_t)w;
            c->last = g_now_s;
        }
    }
    if (!n) return 0;
    if (!c->rtx.cap && rtx_init(&c->rtx, RTX_START) != 0) {
        sock_cut(c);
        return -1;
    }
    if (rtx_room(&c->rtx) < n && c->rtx.cap < RTX_CAP) rtx_grow(&c->rtx, RTX_CAP);
    if (rtx_room(&c->rtx) < n) {
        sock_cut(c);
        return -1;
    }
    rtx_push(&c->rtx, p, (uint32_t)n);
    return 0;
}

static void sock_flush(struct conn *c) {
    while (c->rtx.len) {
        const unsigned char *p = NULL;
        uint32_t n = rtx_peek(&c->rtx, c->rtx.len, &p);
        ssize_t w = send(c->cfd, p, n, MSG_DONTWAIT | MSG_NOSIGNAL);
        if (w < 0) {
            if (!sock_again()) sock_cut(c);
            return;
        }
        rtx_drop(&c->rtx, (uint32_t)w);
        kb_add(c, (uint64_t)w, 0);
        c->last = g_now_s;
        if ((uint32_t)w < n) return;
    }
}

static int conn_can_take(struct conn *c) {
    if (c->sock) return !c->rtx.len;
    return c->is_udp || client_can_take_record(c);
}

/* Read from the server and pass it to the client as TCP (or as UDP datagrams).
 *
 * Two dialer steps, not one: read, then deliver. Between them the stack counts bytes from the
 * node per pass (DRAIN_MAX_BYTES) and time spent reading (STEER_TUN_STATS), and those numbers
 * must not depend on what parsing the protocol costs. */
#define PUMP_DUE (-1000)

static int downstream_pump(struct conn *c, const struct tun_dev *tun) {
    unsigned char *buf = g_ls->pump;
    size_t got = 0;
    void *s = SESS(c);
    TR("read conn#%ld fd=%d\n", (long)(c - g_conns), c->fd);
    uint64_t r0 = g_stats ? now_ns() : 0;
    const unsigned char *rx = buf;
    int rc = g_dl->ops->read(s, buf, sizeof g_ls->pump, &rx, &got);
    if (g_stats) g_st.recv_ns += now_ns() - r0;
    g_rx_total += got;
    if (g_stats) { g_st.recs++; g_st.rec_bytes += got; }
    if (rc) { TR("read from server: rc=%d\n", rc); return rc; }
    int next;
    /* Zero bytes is legitimate: an HTTP/2 control frame arrived and no data yet. Taking it for
     * the end of the stream would break the connection on the first SETTINGS. */
    if (!got) {
        TR("control frame, no data\n");
        return conn_due(c, g_now_ns / 1000000, &next) ? PUMP_DUE : 0;
    }
    TR("%zu bytes from server\n", got);

    /* Parsing — response header, wrappers, datagram reassembly — is the dialer's. It delivers the
     * payload at once, piece by piece once Vision's filter is done, not into a common buffer, which
     * would cost a copy of all traffic and a "did not fit" limit. */
    struct emit_ctx e = { .c = c, .tun = tun, .total = 0 };
    if (g_dl->ops->deliver(g_dl->ctx, s, c->is_udp, rx, got,
                           c->is_udp ? (c->ib_udp ? emit_ib_dgram : emit_dgram) : c->sock ? emit_sock : emit_stream,
                           &e) != 0)
        return -1;
    if (conn_due(c, g_now_ns / 1000000, &next)) return PUMP_DUE;
    if (c->is_udp) return 0;

    if (!e.total) { TR("no data after parsing\n"); return 0; }
    TR("%zu bytes to client (seq up to %u)\n", e.total, c->our_seq);
    return 0;
}

/* ---- reassembly of a fragmented client datagram ---------------------------------------
 *
 * A client that sends a datagram larger than the MTU fragments it itself. The fragments cannot be
 * carried one by one: only the first has the UDP header, and the server needs a WHOLE datagram.
 *
 * ONE slot per loop thread, not per connection: the fragments of a datagram come back to back —
 * one stack has just cut them — so more than one unfinished reassembly at a time almost never
 * happens, and a buffer per connection would cost over a megabyte per thread for nothing.
 *
 * Fragments are taken ONLY IN ORDER. Reordering between the client and the router would need
 * different queues on one LAN segment; in exchange there is no map of received pieces, and none
 * of the overlapping-fragment bugs other implementations have CVEs for. Out of order, the
 * reassembly is dropped and the datagram lost, a normal outcome for UDP.
 *
 * A reassembly lives one second: an abandoned tail would otherwise hold the slot until the next
 * datagram with the same ID, and poison it. */
#define DEFRAG_TTL_NS 1000000000ull

/* Reassemble a datagram from fragments. Returns the length of the finished packet in out (it
 * looks like an ordinary unfragmented packet and goes through the same ip_parse); 0 while waiting
 * for the next fragment, and 0 on refusal too: for UDP both mean no datagram. */
static size_t udp_defrag(const unsigned char *pkt, size_t n, unsigned char *out,
                         size_t out_cap) {
    size_t ihl = (size_t)(pkt[0] & 0x0F) * 4;
    if (ihl < 20 || n < ihl) return 0;
    size_t total = (size_t)((pkt[2] << 8) | pkt[3]);
    if (total > n || total < ihl) return 0;           /* declared length larger than what arrived */
    size_t payload_n = total - ihl;
    unsigned frag = (unsigned)((pkt[6] << 8) | pkt[7]);
    size_t off = (size_t)(frag & 0x1FFF) * 8;
    int more = (frag & 0x2000) != 0;
    uint16_t id = (uint16_t)((pkt[4] << 8) | pkt[5]);
    uint32_t src, dst;
    memcpy(&src, pkt + 12, 4);
    memcpy(&dst, pkt + 16, 4);

    if (g_ls->defrag.active && g_now_ns - g_ls->defrag.at > DEFRAG_TTL_NS)
        g_ls->defrag.active = 0;                    /* the tail was abandoned — the slot is free */

    if (off == 0) {
        /* First fragment: start over even if something is in the slot. Only an abandoned
         * reassembly can be there; a live one's continuation follows right after. */
        if (payload_n < 8 || payload_n > sizeof(g_ls->defrag.buf)) return 0;
        memcpy(g_ls->defrag.buf, pkt + ihl, payload_n);
        g_ls->defrag.src = src;
        g_ls->defrag.dst = dst;
        g_ls->defrag.id = id;
        g_ls->defrag.next_off = (uint16_t)payload_n;
        g_ls->defrag.at = g_now_ns;
        g_ls->defrag.active = 1;
    } else {
        if (!g_ls->defrag.active || g_ls->defrag.src != src || g_ls->defrag.dst != dst ||
            g_ls->defrag.id != id || g_ls->defrag.next_off != off) {
            /* Someone else's, or out of order. A foreign packet does NOT drop the reassembly in
             * progress: it has nothing to do with it, and that datagram would be lost for
             * nothing. */
            if (g_ls->defrag.active && g_ls->defrag.src == src && g_ls->defrag.dst == dst &&
                g_ls->defrag.id == id)
                g_ls->defrag.active = 0;            /* ours, but out of order — reassembly over */
            return 0;
        }
        if (payload_n > sizeof(g_ls->defrag.buf) - g_ls->defrag.next_off) {
            g_ls->defrag.active = 0;                     /* the datagram exceeds our limit */
            return 0;
        }
        memcpy(g_ls->defrag.buf + g_ls->defrag.next_off, pkt + ihl, payload_n);
        g_ls->defrag.next_off = (uint16_t)(g_ls->defrag.next_off + payload_n);
    }
    if (more) return 0;                              /* the rest is still on its way */

    /* Done. Return it as an ordinary packet: a 20-byte IP header, the actual length, no
     * fragmentation flags; from here it takes the same path as everything else. */
    size_t asm_total = 20 + g_ls->defrag.next_off;
    g_ls->defrag.active = 0;
    if (asm_total > out_cap) return 0;
    /* The UDP header's length must match what was assembled: the server reads it, and a
     * fragment lost by the client's kernel would give a short datagram with a wrong length. */
    size_t udp_len = (size_t)((g_ls->defrag.buf[4] << 8) | g_ls->defrag.buf[5]);
    if (udp_len != g_ls->defrag.next_off) return 0;
    memset(out, 0, 20);
    out[0] = 0x45;
    out[2] = (unsigned char)(asm_total >> 8);
    out[3] = (unsigned char)asm_total;
    out[4] = (unsigned char)(id >> 8);
    out[5] = (unsigned char)id;
    out[8] = 64;
    out[9] = 17;
    memcpy(out + 12, &src, 4);
    memcpy(out + 16, &dst, 4);
    memcpy(out + 20, g_ls->defrag.buf, g_ls->defrag.next_off);
    return asm_total;
}

/* A datagram from TUN: a separate session for each "address:port -> address:port" flow.
 *
 * Why a session per flow and not one for all: VLESS carries the destination in the request
 * header, ONCE for the whole flow. Several destinations in one flow need XUDP (Mux.Cool over
 * VLESS, with its own framing and IDs), which wins where there are many destinations with one
 * datagram each, i.e. DNS. For what UDP is carried for here — QUIC, WireGuard, games — there is
 * one destination and the flow lives for minutes, and XUDP adds nothing but a format to get
 * wrong.
 *
 * A handshake per flow is cheap: the first datagram waits for it in the early data buffer, and
 * the spare pool hands out a ready link at once, as for a SYN.
 *
 * "A session per flow" is the stack's rule, not only VLESS's: the dialer gets one whole flow; a
 * dialer with mux may put that flow on a stream of a shared link (see dialer.h). */
static int udp_out(struct conn *c, const unsigned char *p, size_t dn);

static void udp_packet(const struct tun_dev *tun, struct conn *c, const struct flow_key *k,
                       const unsigned char *pkt, size_t n, size_t off) {
    size_t dn = n - off;
    /* Only whole datagrams get here: udp_defrag reassembles fragments before the header is
     * parsed. The check stays anyway: sending the server a stump disguised as a whole datagram is
     * worse than losing it. */
    if (k->frag || !dn || dn > UDP_DGRAM_MAX) return;

    if (!c) {
        c = conn_new(tun);
        if (!c) return;                             /* nothing to evict: the datagram is lost */
        memset(c, 0, sizeof(*c));
        c->used = 1;
        c->born_turn = g_turn;
        c->is_udp = 1;
        c->key = *k;
        c->fd = -1;
        conn_link(c);                               /* after the key: the hash uses it */
        c->last = g_now_s;
        /* On failure the dialer logs the reason itself (for VLESS, an invalid node UUID). */
        if (g_dl->ops->flow_open(g_dl->ctx, SESS(c), k, 1) != 0) {
            conn_drop(c);
            return;
        }

        /* DC_UDP_OWN: the UDP flow has its own link, a UDP socket, while a spare session carries
         * a TCP flow (dialer.h). */
        int qr = flow_link(c, !(g_dl->ops->caps & DC_UDP_OWN));
        if (qr < 0) {
            /* The connector queue is full. Say so: from outside it looks like "QUIC does not
             * work through the tunnel", while the cause is the CPU or a silent node, not the
             * protocol. connq_push has already logged -2 (no connector threads). */
            static struct ratelog said_q;
            unsigned held;
            char more[48], why[64];
            if (qr == -1 && ratelog_take(&said_q, (unsigned)g_now_s, 10, &held))
                fprintf(stderr, LOG_W "connector queue full (%d), datagram dropped — %s%s\n", CONNQ,
                        connq_full_cause(connq_busy_ms(), why, sizeof why),
                        ratelog_more(more, sizeof more, held));
            conn_drop(c);
            return;
        }
        spare_refill();
    }

    c->last = g_now_s;
    udp_out(c, pkt + off, dn);
}

static int udp_out(struct conn *c, const unsigned char *p, size_t dn) {
    /* The tail of early datagrams that did not fit the HTTP/2 window when the flow became ready
     * must go FIRST. A fresh datagram ahead of it is not "reordered datagrams", which QUIC
     * survives, but a frame cut in half: towards the node the datagrams travel as one byte
     * stream, and anything inserted between two halves breaks the server's parsing FOREVER. The
     * raw tcp transport never does this (a send is whole or fails), but on grpc and xhttp the
     * window does close. */
    if (!c->pending && c->early) {
        int fr = early_flush(c);
        if (fr < 0) { conn_drop(c); return 0; }
        if (fr > 0) {
            if (g_stats) g_st.udp_drops++;
            TR("early datagram tail not sent — dropping the fresh one\n");
            return 0;
        }
    }

    int sr = udp_send_dgram(c, p, dn);
    if (sr == SEND_OK) {
        TR("client datagram %zu bytes -> server\n", dn);
        return 1;
    }
    if (sr == SEND_AGAIN) {
        /* The HTTP/2 window is closed or the early data buffer is full. For TCP the packet would
         * just not be acknowledged and the client would resend it; UDP has no ACKs and nowhere
         * to hold the datagram, so it is lost. That is fine: every protocol over UDP survives a
         * lost datagram, while sending OUT OF ORDER would break QUIC, for one. */
        if (g_stats) g_st.udp_drops++;
        TR("datagram dropped: cannot send now\n");
        return 0;
    }
    TR("UDP flow broken, closing\n");
    conn_drop(c);
    return 0;
}

/* ---- flows we closed with our own FIN --------------------------------------------------
 *
 * When the server closes first, the stack sends the client a FIN and frees the slot at once
 * (conn_deadlines). The client's kernel still answers: an ACK of that FIN right away, and its own
 * FIN when the application closes. Without a memory of the flow, both look like segments for an
 * unknown connection and get RST — and the application, which did nothing wrong, sees its next
 * shutdown() or write() fail with ENOTCONN or ECONNRESET instead of an orderly close
 * (tests/run-tunnel-fin.sh, `separate`). So each loop thread keeps the last FIN_RECENT such flows
 * for FIN_RECENT_S seconds: the ACK of our FIN is taken silently, the client's FIN is
 * acknowledged, everything else for the flow still gets RST. Searched only for segments without a
 * connection, which are rare. */
#define FIN_RECENT_S 60

static __thread unsigned g_fin_recent_i;

static void fin_recent_add(const struct conn *c) {
    struct fin_recent *r = &g_ls->fin_recent[g_fin_recent_i++ % FIN_RECENT];
    r->src = c->key.src;
    r->dst = c->key.dst;
    r->v6 = c->key.v6;
    r->sport = c->key.sport;
    r->dport = c->key.dport;
    r->fin_end = c->our_seq + 1;
    r->win = rcv_win_field(c);
    r->at = g_now_s ? g_now_s : 1;
}

/* A segment without a connection that belongs to a flow we closed with FIN: 1 — handled (taken
 * silently or its FIN acknowledged), 0 — not ours or carrying data, the caller answers RST. */
static int fin_recent_take(const struct tun_dev *tun, const struct flow_key *k, size_t data_n) {
    for (unsigned i = 0; i < FIN_RECENT; i++) {
        struct fin_recent *r = &g_ls->fin_recent[i];
        if (!r->at || !fk_same_addr(&r->src, &k->src) || !fk_same_addr(&r->dst, &k->dst) || r->v6 != k->v6 || r->sport != k->sport ||
            r->dport != k->dport || g_now_s - r->at > FIN_RECENT_S ||
            !(k->tcp_flags & TCP_ACK) || k->ack != r->fin_end)
            continue;
        /* DATA after our FIN goes nowhere: the server has closed. Taken silently, it was
         * neither acknowledged nor refused, and an uploading client blocked until its own
         * timeout (sigpipe.sh: a write stalled for 10 s). RST, as the closed server would
         * answer it. */
        if (data_n) return 0;
        if (k->tcp_flags & TCP_FIN) {
            ctl_reply(tun, k, r->fin_end, k->seq + (uint32_t)data_n + 1, TCP_ACK, r->win);
            r->at = 0;          /* both halves closed: the flow is over */
        }
        return 1;
    }
    return 0;
}

static void drain_conn(struct conn *c, const struct tun_dev *tun);

/* send_or_wait: the node closed while we waited; the data will never go. */
#define SEND_CLOSED 2

/* Send client data to the node; on SEND_AGAIN (the HTTP/2 window is closed) wait briefly for it
 * to open. Returns SEND_OK, SEND_AGAIN (still closed: do not acknowledge), SEND_FATAL or
 * SEND_CLOSED.
 *
 * Before shifting the delay onto the client, process what is already in the socket: the
 * WINDOW_UPDATE comes from there and is usually ALREADY there, sent as soon as the server freed
 * its buffer. Without this each closed window costs the client a retransmission timeout.
 *
 * Wait 5 ms, not zero: the WINDOW_UPDATE sometimes lags by a fraction of a round trip. Not more:
 * one loop serves all connections, and every millisecond here stalls the rest.
 *
 * Read with drain_conn, as the loop does: only it checks the client's window and, at the end of
 * the stream, sets srv_closed instead of closing (closing would destroy the ring with the tail of
 * the response). If the client has no window, do not read at all: the client resends.
 *
 * DC_ACK_PACED does not wait: for it a refusal means "the queue to the node is full", which its
 * multiplexer drains, and reading from the node does not help. */
static int send_or_wait(struct conn *c, const struct tun_dev *tun, const unsigned char *d,
                        size_t n) {
    int sr = upstream_send(c, d, n);
    if (sr != SEND_AGAIN || c->mux) return sr;
    struct pollfd sp = { .fd = c->fd, .events = POLLIN };
    if (!(g_dl->ops->caps & DC_ACK_PACED) && client_can_take_record(c) &&
        poll(&sp, 1, 5) > 0 && (sp.revents & POLLIN)) {
        drain_conn(c, tun);
        if (c->srv_closed) return SEND_CLOSED;
        sr = upstream_send(c, d, n);
    }
    return sr;
}

/* ---- gathering the client's segments ------------------------------------------------------
 *
 * The kernel hands the device's reader coalesced frames (receive offload, tun.h rx_gso), and
 * tun_read_packet splits them back into MTU-sized packets. Sent one by one, each 1.4 KB segment
 * cost the node link a TLS record, an HTTP/2 frame and a write: with uploads flowing at all
 * (dialer_ops.room), they took 80% of the loop and downloads alongside fell from 4 Gbit/s to
 * 160 Mbit/s. Consecutive in-order data segments of one connection within one TUN batch are
 * therefore gathered here and go to the node in pieces of the dialer's size (dialer_ops.up_write),
 * acknowledged once.
 *
 * One connection at a time per loop thread. Whatever is not a continuation (another flow, a FIN,
 * a bare ACK, a gap) sends the gathered bytes first, and so does the end of the batch
 * (up_flush in worker_loop, before flush_acks). Until then client_seq does not include them:
 * the bytes are not acknowledged until the node took them, as before. */
/* (UP_MAX and g_ls->up: declared with the loop's tables, before conn_drop.) */

/* Send what is gathered, piece by piece. Each piece the node took is acknowledged; at the first
 * refused piece the rest is not, and the client resends it (SEND_AGAIN) or gets RST (failure). */
static void up_flush(const struct tun_dev *tun) {
    struct conn *c = g_ls->up.c;
    if (!c) return;
    g_ls->up.c = NULL;
    TR("client data %u bytes (gathered) -> server\n", g_ls->up.n);
    for (size_t off = 0; off < g_ls->up.n;) {
        size_t cut = g_dl->ops->up_write ? g_dl->ops->up_write(g_dl->ctx, SESS(c)) : 0;
        if (!cut) cut = g_ls->up.n;
        size_t n = g_ls->up.n - off < cut ? g_ls->up.n - off : cut;
        int sr = send_or_wait(c, tun, g_ls->up.buf + off, n);
        if (sr == SEND_CLOSED) return;
        if (sr == SEND_AGAIN) {
            TR("window closed, %u gathered bytes not acknowledged — the client will resend\n",
               (unsigned)(g_ls->up.n - off));
            return;
        }
        if (sr != SEND_OK) {
            conn_node_lost(c);
            conn_reset(c, tun);
            return;
        }
        c->client_seq += (uint32_t)n;
        c->ack_due = 1;
        off += n;
    }
}

/* Gather the in-order segment (seq, d, n) of c (the caller checked the order). Sends what was
 * gathered first when the segment would not fit after it. 0 — the segment is gathered
 * (g_ls->up.c == c), or too big to gather and the caller sends it itself; -1 — it does not go now:
 * c closed, or the send before it failed and it no longer lines up (not acknowledged, the client
 * resends it). */
static int up_add(struct conn *c, const struct tun_dev *tun, uint32_t seq, const unsigned char *d,
                  size_t n) {
    long room = conn_room(c);
    if (g_ls->up.c == c && (g_ls->up.n + n > UP_MAX || (room >= 0 && g_ls->up.n + n > (size_t)room)))
        up_flush(tun);
    if (g_ls->up.c && g_ls->up.c != c) up_flush(tun);
    if (!c->used || c->srv_closed || c->aborted) return -1;
    if (g_ls->up.c != c && seq != c->client_seq) return -1;
    if (n > UP_MAX) return 0;
    if (!g_ls->up.c) {
        g_ls->up.c = c;
        g_ls->up.n = 0;
    } else if (g_ls->up.c != c) {
        return 0;
    }
    memcpy(g_ls->up.buf + g_ls->up.n, d, n);
    g_ls->up.n += (uint32_t)n;
    return 0;
}

static void handle_packet(const struct tun_dev *tun, const unsigned char *pkt, size_t n) {
    struct flow_key k;
    size_t off = 0;

    /* UDP fragments are reassembled BEFORE parsing: a continuation has no UDP header, and
     * ip_parse would not accept it. The result looks like an ordinary packet and takes the common
     * path, so nothing else needs to know about fragments. */
    if (n >= 20 && (pkt[0] >> 4) == 4 && pkt[9] == 17 &&
        (((pkt[6] << 8) | pkt[7]) & 0x3FFF)) {
        unsigned char *asm_pkt = g_ls->asm_pkt;
        size_t an = udp_defrag(pkt, n, asm_pkt, sizeof g_ls->asm_pkt);
        if (!an) { TR("UDP fragment: %s\n", "waiting for the rest, or refused"); return; }
        TR("datagram reassembled from fragments: %zu bytes\n", an);
        pkt = asm_pkt;
        n = an;
    }

    if (ip_parse(pkt, n, &k, &off) != 0) { TR("packet not parsed (%zu bytes)\n", n); return; }
    if (k.v6) n = 40 + (((size_t)pkt[4] << 8) | pkt[5]);
    /* Anything but the next plain data segment of the gathered flow sends the gathered bytes
     * first: they come before it in the stream, or belong to another flow. */
    if (g_ls->up.c) {
        const struct conn *u = g_ls->up.c;
        if (k.proto != 6 || !fk_same_addr(&k.src, &u->key.src) || !fk_same_addr(&k.dst, &u->key.dst) || k.v6 != u->key.v6 ||
            k.sport != u->key.sport || k.dport != u->key.dport || n == off ||
            (k.tcp_flags & (TCP_SYN | TCP_RST | TCP_FIN)) || k.seq != u->client_seq + g_ls->up.n)
            up_flush(tun);
    }
    TR("%u.%u.%u.%u:%u -> %u.%u.%u.%u:%u proto=%u flags=0x%02x len=%zu\n",
       k.src.v4&255,(k.src.v4>>8)&255,(k.src.v4>>16)&255,(k.src.v4>>24)&255, k.sport,
       k.dst.v4&255,(k.dst.v4>>8)&255,(k.dst.v4>>16)&255,(k.dst.v4>>24)&255, k.dport,
       k.proto, k.tcp_flags, n - off);

    /* Anything but TCP and UDP the tunnel cannot carry: ICMP, ESP and the rest have no command
     * in VLESS (or in any flow dialer).
     *
     * And we do NOT stay silent: the client reads silence as "no answer yet" and keeps waiting
     * and retrying, while an ICMP refusal ends the wait at once. For ICMP it is also the right
     * answer in substance: emulating ping through a proxy misleads, since a successful ping
     * would not mean a working path. */
    if (k.proto != 6 && k.proto != 17) {
        unsigned char un[128];
        size_t ul = icmp_unreach_build(un, sizeof(un), pkt, n);
        if (ul) tun_write_ctl(tun, un, ul);
        TR("neither TCP nor UDP (proto=%u), refused\n", k.proto);
        return;
    }

    struct conn *c = conn_find(&k);

    if (k.proto == 17) { udp_packet(tun, c, &k, pkt, n, off); return; }

    if (k.tcp_flags & TCP_SYN) {
        if (c) {
            /* A repeated SYN: the SYN-ACK may have been lost. While no data byte has gone to the
             * client (our_seq has not moved), resending it is safe: the packet is byte for byte
             * the same. Silence would make the client sit out its one-second SYN retry. */
            if (c->our_seq == 2) send_synack(c, tun);
            return;
        }
        c = conn_new(tun);
        if (!c) return;                             /* all connections fresh: the client retries */
        memset(c, 0, sizeof(*c));
        c->used = 1;
        c->born_turn = g_turn;
        c->room_seen = UINT32_MAX;
        c->key = k;
        c->fd = -1;
        /* Into the lists AFTER memset and AFTER the key: the hash uses the key, and memset would
         * wipe the chain position. */
        conn_link(c);
        c->client_seq = k.seq + 1;                  /* the SYN takes one sequence number */
        /* ISN = 1 and our SYN takes it, so the first data byte is number 2 — exactly what the
         * client acknowledges after the handshake. client_ack must start at 2 as well: one
         * behind, it never catches up (an advance by the SYN's "byte" releases nothing from the
         * ring), so three duplicate ACKs never equal client_ack and fast retransmit never fires,
         * and a timeout resend goes out one number too LOW — the receiver trims its first byte
         * as already received, and the stream after the first resend is shifted by a byte. */
        c->our_seq = 2;
        c->client_ack = 2;
        c->client_win = k.window ? k.window : 8192;
        c->client_wscale = k.wscale;
        c->mss = conn_mss(&k);
        c->ws_on = k.ws_seen;
        c->rto_ms = rto_floor();
        c->last = g_now_s;
        /* The retransmission ring is NOT allocated on SYN. It holds UNACKNOWLEDGED bytes, and
         * until there are some there is nothing to hold. A failed allocation then comes right
         * BEFORE reading from the server: no ring, no read, and nothing is lost because nothing
         * was taken. Browsers keep dozens of connections alive without traffic (HTTP/2
         * keep-alive), and an idle one now costs only its table record. */
        /* Protocol state on the flow (for VLESS, UUID and Vision). On failure the dialer logs
         * the reason itself (an invalid node UUID). */
        if (g_dl->ops->flow_open(g_dl->ctx, SESS(c), &k, 0) != 0) {
            if (k.v6) ctl_reply(tun, &k, 0, k.seq + 1, TCP_RST | TCP_ACK, 0);
            conn_drop(c);
            return;
        }

        /* The SYN-ACK goes out AT ONCE, without waiting for the handshake with the node
         * (100-400 ms): the client starts its own TLS only AFTER the SYN-ACK, so otherwise the
         * handshake would add to every connection. The client's early data waits in the early
         * buffer until the flow is ready, and a flow that fails is reported with RST after the
         * handshake. */

        int qr = flow_link(c, 1);
        if (qr < 0) {
            /* Queue full: the CPU cannot digest that many handshakes at once, or the node holds
             * them. Do not drop the SYN silently: log it and free the record; the client retries
             * the SYN itself. connq_push has already logged -2 (no connector threads). */
            static struct ratelog said_q;
            unsigned held;
            char more[48], why[64];
            if (qr == -1 && ratelog_take(&said_q, (unsigned)g_now_s, 10, &held))
                fprintf(stderr, LOG_W "connector queue full (%d), SYN refused — %s%s\n", CONNQ,
                        connq_full_cause(connq_busy_ms(), why, sizeof why),
                        ratelog_more(more, sizeof more, held));
            conn_drop(c);
            return;
        }
        send_synack(c, tun);
        spare_refill();
        return;
    }

    if (c && c->pending) {
        /* The connector is still working, but the client already has the SYN-ACK and sends its
         * first data. The session must not be touched (the connector writes into it), but our
         * half — the TCP counters and the early buffer — is ours: take the data here and
         * acknowledge it. A client RST is only marked: closing under the connector is not
         * allowed. A FIN is not a refusal but a half-close: the segment's data goes into the
         * buffer, the FIN is counted, and the server's response is still wanted. */
        if (k.tcp_flags & TCP_RST) { c->client_gone = 1; return; }
        int fin = (k.tcp_flags & TCP_FIN) != 0;
        if (c->client_fin) { if (fin) c->ack_due = 1; return; }
        if (k.tcp_flags & TCP_ACK) {
            c->client_win = k.window;
            if (!c->estab_ns) c->estab_ns = g_now_ns ? g_now_ns : 1;
        }
        size_t dn = n - off;
        if ((!dn && !fin) || k.seq != c->client_seq) return;
        /* Does not fit the early buffer: not acknowledged. */
        if (dn && early_hold(c, pkt + off, dn) != 0) return;
        c->client_seq += (uint32_t)dn;
        if (fin) { c->client_seq += 1; c->client_fin = 1; }
        c->last = g_now_s;
        c->ack_due = 1;
        TR("early data: %zu bytes buffered (%u total)\n", dn, c->early_n);
        return;
    }


    if (!c) {
        /* A segment without a connection. This happens after tunvless restarts (application
         * connections outlived the old process, and our table does not have them) and after an
         * RST of ours the client did not accept: the number did not match, and it answered with
         * a challenge ACK (RFC 5961, see conn_reset). Silence leaves the application waiting for
         * its timeout on a connection that is gone; an RST by TCP rules (RFC 793: its number is
         * the segment's ACK, and an RST is never answered) makes it reconnect at once. The ACK of
         * our own FIN after close and the client's FIN after it are not stray segments:
         * fin_recent_take handles them. Only with one loop thread (g_one_worker). */
        if (g_one_worker && !(k.tcp_flags & TCP_RST) && (k.tcp_flags & TCP_ACK) &&
            !fin_recent_take(tun, &k, n - off)) {
            ctl_reply(tun, &k, k.ack, 0, TCP_RST, 0);
            TR("segment without a connection — RST\n");
        }
        return;
    }
    c->last = g_now_s;

    /* Take the client's ACK and window from ANY of its packets, pure ACKs included: otherwise we
     * do not know how much it has taken and keep pouring into the device. Pure ACKs are exactly
     * what comes during a download. */
    size_t data_n = n - off;

    if (k.tcp_flags & TCP_ACK) {
        if (!c->estab_ns) c->estab_ns = g_now_ns ? g_now_ns : 1;
        /* Wraparound-safe comparison: the difference as signed. */
        if ((int32_t)(k.ack - c->client_ack) > 0) {
            /* Advance client_ack ONLY by what is acknowledged, and never past our_seq.
             *
             * Ring invariant: start == client_ack, length == our_seq - client_ack. A client
             * acknowledging bytes BEYOND what we sent (malicious or broken) would otherwise push
             * client_ack past our_seq; then inflight = our_seq - client_ack wraps to a huge
             * uint32, client_can_take_record is false forever and the connection hangs. So
             * advance by exactly what rtx_drop released, and never past our_seq. */
            uint32_t advance = k.ack - c->client_ack;
            if ((int32_t)(k.ack - c->our_seq) > 0) advance = c->our_seq - c->client_ack;
            advance = rtx_drop(&c->rtx, advance);   /* BEFORE moving client_ack: counts from it */
            c->client_ack += advance;
            c->dup_acks = 0;
            c->fast_done = 0;
            /* Progress: the timeout is short again and counts from the new oldest byte, not
             * from the old one. */
            c->rtx_at = c->rtx.len ? g_now_ns : 0;
            c->rto_ms = rto_floor();
        } else if (k.ack == c->client_ack && c->rtx.len && !data_n && !c->fast_done &&
                   k.window == c->client_win && !(k.tcp_flags & (TCP_SYN | TCP_FIN))) {
            /* A duplicate ACK as RFC 5681 defines it: the same number, no data, NO WINDOW CHANGE,
             * and unacknowledged data in flight.
             *
             * The window condition matters. While the client reads, its kernel sends window
             * updates — the same ACK number, a bigger window — and counting them as duplicates
             * would resend data in the middle of a healthy transfer. */
            if (++c->dup_acks >= DUP_ACK_TRIGGER) {
                c->dup_acks = 0;
                c->fast_done = 1;
                rtx_resend(c, tun, "three duplicate ACKs");
            }
        }
        c->client_win = k.window;
    }

    if (k.tcp_flags & TCP_RST) { conn_drop(c); return; }

    /* A client FIN is a half-close, not the end of the connection. It is one more byte of the
     * stream: the segment's data goes to the server (the tail of a request often comes with the
     * FIN: nc -q0, HTTP/1.0, TCP_CORK), the FIN is counted in client_seq and acknowledged by the
     * same deferred ACK, and the connection lives until the server closes its side (the FIN to
     * the client goes from the common pass, as with srv_closed) or the client stays silent for
     * CLOSE_DRAIN_MS. After the client's FIN only ACKs are expected; a repeated FIN means our ACK
     * was lost, so acknowledge again. */
    int fin = (k.tcp_flags & TCP_FIN) != 0;
    if (c->client_fin) {
        if (fin) c->ack_due = 1;
        return;
    }

    /* A zero-window probe (or keepalive): no data, one number BELOW the expected one. The window
     * may be zero because the node's room is (dialer_ops.room); without an answer the client
     * only learns that it opened from our window update, and if that is lost, it waits. */
    if (!data_n && !fin && k.seq == c->client_seq - 1) { c->ack_due = 1; return; }
    if (!data_n && !fin) return;                    /* pure ACK */

    /* Out of order: dropped. A reassembly buffer per connection is exactly the memory a weak box
     * does not have; the client retransmits.
     *
     * But not silently: answer with an ACK of the expected number. A segment beyond a hole means
     * the one before it was lost, and duplicate ACKs trigger the client's fast retransmit;
     * without them the hole closes only on timeout. A repeat of data already taken means our ACK
     * was lost, and without an answer the client would retry until it gives up. */
    uint32_t exp = c->client_seq + (g_ls->up.c == c ? g_ls->up.n : 0);
    if ((int32_t)(k.seq - exp) < 0 && (int32_t)(k.seq + (uint32_t)data_n + (uint32_t)fin - exp) > 0) {
        uint32_t head = exp - k.seq;
        off += head;
        data_n -= head;
        k.seq = exp;
    }
    if (k.seq != exp) { c->ack_due = 1; return; }

    /* The early data tail has not gone yet (the HTTP/2 window was closed when the flow became
     * ready): fresh data must not go ahead of it, or bytes swap places. Do not acknowledge, the
     * client retransmits; the same trick as SEND_AGAIN below. */
    if (c->early) {
        int fr = early_flush(c);
        if (fr < 0) { conn_reset(c, tun); return; }
        if (fr > 0) return;
    }

    /* Plain data, no FIN: gather it with the segments after it (see g_ls->up). */
    if (data_n && !fin) {
        if (up_add(c, tun, k.seq, pkt + off, data_n) != 0) return;
        if (g_ls->up.c == c) return;                    /* gathered: sent and acknowledged later */
        /* Larger than the gathering buffer: on its own, below. */
    }

    TR("client data %zu bytes -> server%s\n", data_n, fin ? " (and FIN)" : "");
    int sr = data_n ? send_or_wait(c, tun, pkt + off, data_n) : SEND_OK;
    if (sr == SEND_CLOSED) return;                  /* the packet did not go and never will */
    if (sr == SEND_AGAIN) {
        /* Still not possible: do not acknowledge and do not move the counter, as if the packet
         * never came. The client resends it; this is the only way to hold the flow back without
         * storing unsent data. */
        TR("window closed, packet not acknowledged — the client will resend\n");
        return;
    }
    if (sr != SEND_OK) {
        /* RST, not silence: otherwise the application waits on a connection that is gone. */
        TR("send to server failed\n");
        conn_node_lost(c);
        conn_reset(c, tun);
        return;
    }
    c->client_seq += (uint32_t)data_n;
    if (fin) { c->client_seq += 1; c->client_fin = 1; }

    /* The ACK is DEFERRED to the end of the TUN batch: up to 64 packets come per pass, and one
     * ACK with the last number acknowledges them all. */
    c->ack_due = 1;
}

/* Whether to hold the ACK to the client (DC_ACK_PACED, dialer.h): the queue to the node is above
 * its low-water mark, i.e. the session's descriptor is not writable. Decided here, at the ACK, not
 * per packet: a poll costs a syscall, and there is one ACK per TUN batch anyway. A batch (up to
 * TUN_DRAIN_MAX packets, about a hundred kilobytes) cannot overflow the queue while the client is
 * bound by the window (for DC_ACK_PACED at most rcv_wnd_max, rcv_win_field) and gets no ACKs.
 *
 * Not held when there is no descriptor (the connector is still working: early data), for a UDP
 * flow (no ACKs), and when poll itself reports an error or a socket error: a broken connection
 * must break on its own path, not hang on an ACK. */
static int ack_must_wait(struct conn *c) {
    if (!(g_dl->ops->caps & DC_ACK_PACED) || c->is_udp || c->pending || c->mux || c->fd < 0) return 0;
    struct pollfd p = { .fd = c->fd, .events = POLLOUT };
    if (poll(&p, 1, 0) != 0) { c->ack_hold = 0; return 0; }
    if (!c->ack_hold && g_stats) g_st.acks_held++;
    c->ack_hold = 1;
    return 1;
}

/* Send the deferred ACKs. Called after a batch of TUN packets, and after a connection holding
 * its ACK became writable (ARM_OUT). */
static void flush_acks(const struct tun_dev *tun) {
    for (int li = 0; li < g_live_n; li++) {
        struct conn *c = &g_conns[g_live[li]];
        if (!c->ack_due) continue;
        if (ack_must_wait(c)) continue;
        c->ack_due = 0;
        if (!c->is_udp) {
            long r = conn_room(c);
            if (r >= 0) c->room_seen = r > 0x7FFFFFFF ? 0x7FFFFFFFu : (uint32_t)r;
        }
        ctl_reply(tun, &c->key, c->our_seq, c->client_seq, TCP_ACK, rcv_win_field(c));
    }
}

/* DRAIN the connection's socket rather than read one record per loop turn. While we do not read,
 * the server's window closes, and reopening it costs a round trip to the server; a record per
 * turn gives a record per round trip, latency instead of bandwidth (measured: 3 Mbit/s where the
 * crypto does 800).
 *
 * Three limits: the client's window (or a segment is lost), and bytes and reads per turn, so one
 * busy connection does not starve the rest.
 *
 * A function because it is called from two places, on an epoll event and on rx_ready, and both
 * must keep the same limits. */
/* The room (dialer_ops.room) the client's window is capped by grows when the node lets more in:
 * a WINDOW_UPDATE read from the node, or, on a transport with a link of its own for the upload
 * (xhttp stream-up, packet-up), an answer read there, which no socket event of ours reports. The
 * client must hear of it from us: it has no data from us to carry a new window, and waits in its
 * persist timer, which backs off like a retransmission (packet-up uploads: 11 MB in 20 s against
 * 400 MB). So the room is compared, after each read and on each loop pass, with what the client
 * last heard, and a grown room sends a window update. WIN_UPDATE_MIN: smaller growth waits for
 * the next ACK, so one WINDOW_UPDATE per gRPC frame does not become one packet each. Sent by the
 * loop (flush_acks). */
#define WIN_UPDATE_MIN 4096
static __thread int g_win_woke;

static void room_watch(struct conn *c) {
    if (c->is_udp || c->sock || c->srv_closed || !c->used) return;
    long r = conn_room(c);
    if (r < 0) return;
    uint32_t now_r = r > 0x7FFFFFFF ? 0x7FFFFFFFu : (uint32_t)r;
    uint32_t was = c->room_seen;
    if (was == UINT32_MAX) { c->room_seen = now_r; return; }
    if (now_r >= was + WIN_UPDATE_MIN || (was < WIN_UPDATE_MIN && now_r > was)) {
        c->room_seen = now_r;
        c->ack_due = 1;
        g_win_woke = 1;
    }
}

static void drain_conn_reads(struct conn *c, const struct tun_dev *tun);

static void drain_conn(struct conn *c, const struct tun_dev *tun) {
    drain_conn_reads(c, tun);
    room_watch(c);
}

static void drain_conn_reads(struct conn *c, const struct tun_dev *tun) {
    const struct dialer_ops *d = g_dl->ops;
    void *s = SESS(c);
    int reads = 0;
    uint64_t drained_from = g_rx_total;
    c->rx_ready = 0;
    for (;;) {
        int pr = downstream_pump(c, tun);
        if (pr != 0) {
            /* The server closed. Do NOT close the connection until the client acknowledges
             * everything: unacknowledged data in the ring must still be resent, and closing would
             * destroy the ring. The FIN goes from the common pass. */
            c->srv_closed = 1;
            c->closed_at = g_now_ns;
            /* Cut, not closed: the client gets RST from the common pass (the aborted field). */
            if ((conn_node_lost(c) || pr == PUMP_DUE) && !c->is_udp) c->aborted = 1;
            TR("server closed conn#%ld, waiting for ACK of %u bytes\n",
               (long)(c - g_conns), c->rtx.len);
            return;
        }
        if (++reads >= DRAIN_MAX_READS || g_rx_total - drained_from >= DRAIN_MAX_BYTES) {
            if (g_stats) g_st.drain_full++;
            /* Stopped at our own limit while data may still be in the buffer: mark that no
             * socket event is needed for it. */
            c->rx_ready = (uint8_t)(d->has_data(s) != 0);
            return;
        }
        if (!conn_can_take(c)) {
            c->rx_ready = (uint8_t)(d->has_data(s) != 0);
            return;
        }
        /* A whole record is already read from the socket: no need to ask the kernel. */
        if (d->has_data(s)) continue;
        if (c->mux) return;
        /* Anything more to read? Without this check the next read blocks for the socket timeout
         * (eight seconds) and stops the whole loop. */
        struct pollfd sp = { .fd = c->fd, .events = POLLIN };
        if (poll(&sp, 1, 0) <= 0 || !(sp.revents & POLLIN)) return;
    }
}

static int sock_read(struct conn *c, const struct tun_dev *tun) {
    unsigned char *buf = g_ls->sock;
    size_t total = 0;
    while (!c->early && !c->client_fin && !c->srv_closed && total < DRAIN_MAX_BYTES) {
        size_t cap = EARLY_CAP - c->early_n;
        if (cap > UP_MAX) cap = UP_MAX;
        long room = conn_room(c);
        if (room >= 0 && (size_t)room < cap) cap = (size_t)room;
        if (!cap) return 0;
        ssize_t n = recv(c->cfd, buf, cap, MSG_DONTWAIT);
        if (n < 0) {
            if (sock_again()) return 0;
            conn_reset(c, tun);
            return 1;
        }
        c->last = g_now_s;
        if (n == 0) {
            c->client_fin = 1;
            return 0;
        }
        kb_add(c, 0, (uint64_t)n);
        int sr = upstream_send(c, buf, (size_t)n);
        if (sr == SEND_AGAIN) {
            if (early_hold(c, buf, (size_t)n) == 0) return 0;
            conn_reset(c, tun);
            return 1;
        }
        if (sr != SEND_OK) {
            conn_node_lost(c);
            conn_reset(c, tun);
            return 1;
        }
        total += (size_t)n;
    }
    return 0;
}

static int ib_send(int fd, const unsigned char *p, size_t n) {
    return n && send(fd, p, n, MSG_DONTWAIT | MSG_NOSIGNAL) != (ssize_t)n ? -1 : 0;
}

static void ib_refuse(struct conn *c, enum ibhs_end end) {
    unsigned char r[IBHS_OUT];
    ib_send(c->cfd, r, ibhs_done(c->ib_reply == 2, end, r, sizeof r));
    sock_soft_close(c->cfd);
    c->cfd = -1;
    conn_drop(c);
}

/* A connector reported on connection c (done): the link is ready or failed. Called by the loop's
 * pass over jobs (worker_loop); a separate function so tests/tunnelmatch.c can drive it. 1 — the
 * connection is closed (another took its place in g_live), 0 — the flow is ready. */
static struct ratelog g_said_open;

static int conn_ready(struct conn *c, const struct tun_dev *tun) {
    const struct dialer_ops *d = g_dl->ops;
    c->pending = 0;
    if (!c->mux) {
        c->fd = d->fd(SESS(c));
        if (c->rc == 0) node_sock_silence(c->fd);
    }
    if (c->client_gone) {
        /* The client gave up during the handshake. Nobody to answer: close both the flow to the
         * node and the record. */
        conn_drop(c);
        return 1;
    }
    if (c->rc != 0) {
        /* With the reason, not just "did not open": the text tells "TCP did not connect", "the
         * server rejected the key" and "the server refused HTTP/2" apart. */
        unsigned held;
        char more[48];
        if (ratelog_take(&g_said_open, ratelog_now(), 10, &held))
            fprintf(stderr, LOG_W "flow to %s did not open: %s%s\n",
                    d->peer_of ? d->peer_of(g_dl->ctx, SESS(c)) : d->peer(g_dl->ctx),
                    d->why ? d->why(SESS(c), c->rc) : d->strerror(c->rc),
                    ratelog_more(more, sizeof more, held));
        if (c->ib_reply) {
            ib_refuse(c, IBHS_END_DIAL);
            return 1;
        }
        /* RST, not silence: otherwise the client waits for its timeout. With silence a video
         * page stopped opening at all: a browser handles a fast refusal better than a wait. */
        conn_reset(c, tun);
        return 1;
    }
    /* During the handshake the connection's node stopped being active (it was replaced): there is
     * nothing to carry through it, and the client reconnects to a live one. */
    if (d->stale && d->stale(g_dl->ctx, SESS(c))) {
        if (c->ib_reply) ib_refuse(c, IBHS_END_DIAL);
        else conn_reset(c, tun);
        return 1;
    }
    if (c->ib_reply) {
        unsigned char r[IBHS_OUT];
        size_t n = ibhs_done(c->ib_reply == 2, IBHS_END_OK, r, sizeof r);
        c->ib_reply = 0;
        if (ib_send(c->cfd, r, n) != 0) {
            conn_reset(c, tun);
            return 1;
        }
    }
    /* Early data the client sent during the handshake goes to the server first, before any
     * fresh packets. */
    if (c->early && early_flush(c) < 0) {
        conn_reset(c, tun);
        return 1;
    }
    if (c->estab_ns) c->estab_ns = g_now_ns ? g_now_ns : 1;
    c->last = g_now_s;
    if (!c->sock && !c->is_udp) {
        c->ack_due = 1;
        g_win_woke = 1;
    }
    TR("flow ready (conn#%ld), early data sent\n", (long)(c - g_conns));
    if (c->sock) return sock_read(c, tun);
    return 0;
}

/* The node set changed (stack_nodes_changed): connections through a node that is no longer
 * active are reset by the next deadline pass (aborted: RST, not FIN). The application reconnects
 * at once and lands on a live node instead of waiting for its timeout on a dead one. seen — the
 * count of changes this loop thread has already handled. */
static void nodes_sweep(unsigned *seen, uint64_t now) {
    unsigned ep_now = __atomic_load_n(&g_nodes_epoch, __ATOMIC_ACQUIRE);
    if (ep_now == *seen) return;
    *seen = ep_now;
    const struct dialer_ops *d = g_dl->ops;
    for (int li = 0; d->stale && li < g_live_n; li++) {
        struct conn *c = &g_conns[g_live[li]];
        if (c->pending || c->aborted || !d->stale(g_dl->ctx, SESS(c))) continue;
        c->srv_closed = 1;
        c->aborted = 1;
        c->closed_at = now;
    }
}

static int64_t first_wait_left(const struct conn *c, uint64_t now) {
    if (c->is_udp || c->sent_any || c->early || c->srv_closed || !c->estab_ns) return -1;
    uint64_t due = c->estab_ns + SERVER_FIRST_MS * 1000000ull;
    return now >= due ? 0 : (int64_t)((due - now + 999999) / 1000000);
}

/* Deadlines of one connection: retransmission, the early data tail, close after the server or a
 * cut, idle cleanup. The loop's single pass calls it for each live connection (worker_loop); a
 * separate function so tests/tunnelmatch.c can drive it, since deadlines show from outside only
 * as packets into the device. 1 — the connection is closed (another took its place in g_live),
 * 0 — alive. */
static int conn_deadlines(struct conn *c, const struct tun_dev *tun, uint64_t now) {
    /* The early data tail that did not fit the HTTP/2 window when the flow became ready: send it.
     * There may be no new client data to push it out. */
    if (c->early && early_flush(c) < 0) {
        conn_reset(c, tun);
        return 1;
    }

    /* A client silent since the handshake and the ready link: open the flow without data, so a server that speaks
     * first can (dialer.h, send with n == 0). The early data path above covers a client that
     * spoke. */
    if (first_wait_left(c, now) == 0) {
        c->fw_again = 0;
        int sr = upstream_send(c, NULL, 0);
        if (sr == SEND_FATAL) {
            conn_reset(c, tun);
            return 1;
        }
        if (sr != SEND_OK) c->fw_again = 1;
    }

    room_watch(c);

    /* Expired timers: resend. After packet handling, not before: an ACK that came in this turn may
     * have made it unnecessary. */
    if (c->rtx.len && c->rtx_at &&
        (now - c->rtx_at) / 1000000 >= c->rto_ms)
        rtx_resend(c, tun, "timeout");

    /* A cut link, or a node that is no longer active: RST to the client at once, without waiting
     * for ACKs — nothing comes after a cut anyway (for UDP, just a close). */
    if (c->srv_closed && c->aborted) {
        TR("conn#%ld: link to the node cut — RST to the client\n", (long)(c - g_conns));
        conn_reset(c, tun);
        return 1;
    }

    /* A UDP flow closed by the node: nothing to wait for and no way to say goodbye, since
     * datagrams have no ring and no FIN. Free it now; another datagram from the client opens a
     * new flow. */
    if (c->srv_closed && c->is_udp) {
        TR("node closed UDP flow conn#%ld\n", (long)(c - g_conns));
        conn_drop(c);
        return 1;
    }

    if (c->client_fin && !c->fin_up && !c->early && !c->srv_closed && !c->is_udp && g_dl->ops->finish) {
        int fr = g_dl->ops->finish(g_dl->ctx, SESS(c));
        if (fr == SEND_FATAL) {
            conn_reset(c, tun);
            return 1;
        }
        if (fr == SEND_OK) {
            c->fin_up = 1;
            c->last = g_now_s;
        }
    }

    /* The client closed its half and went silent, and the server does not close its own (it
     * cannot know about the client's FIN): nothing more to wait for, close the same way as after
     * the server. */
    if (c->client_fin && !c->srv_closed && (c->fin_up || !g_dl->ops->finish) && g_now_s - c->last > CLOSE_DRAIN_MS / 1000) {
        c->srv_closed = 1;
        c->closed_at = now;
    }

    if (c->srv_closed && c->sock) {
        if (c->rtx.len && (now - c->closed_at) / 1000000 < CLOSE_DRAIN_MS) return 0;
        if (c->rtx.len) {
            conn_reset(c, tun);
            return 1;
        }
        sock_soft_close(c->cfd);
        c->cfd = -1;
        conn_drop(c);
        return 1;
    }

    /* Closed by the server: once the client has acknowledged everything, FIN and close. If it has
     * not within the allowed time, close anyway: the client may be gone. */
    if (c->srv_closed) {
        int drained = c->rtx.len == 0;
        if (!drained && (now - c->closed_at) / 1000000 < CLOSE_DRAIN_MS) return 0;
        if (!drained)
            TR("conn#%ld: client did not acknowledge %u bytes in %d ms, closing\n",
               (long)(c - g_conns), c->rtx.len, CLOSE_DRAIN_MS);
        /* FIN, or the client keeps waiting for data that will never come. With an open window:
         * a FIN takes a sequence number, and Linux does not send one into a zero window. The
         * client's own FIN then waited for a window update that never came (LAST-ACK for good,
         * tests/run-tunnel-fin.sh `separate`). */
        ctl_reply(tun, &c->key, c->our_seq, c->client_seq, TCP_FIN | TCP_ACK, rcv_win_field(c));
        if (!c->is_udp) fin_recent_add(c);
        conn_drop(c);
        return 1;
    }

    /* Give an empty ring back to the system. The connection stays fully working: the ring comes
     * back with the next data. Only after idling, not as soon as it empties, or every burst of
     * ACKs would malloc and free. */
    if (c->rtx.cap && !c->rtx.len && g_now_s - c->last > 5) rtx_done(&c->rtx);

    /* Idle cleanup: without it the table fills with connections the client dropped without a
     * FIN, and new ones stop opening.
     *
     * The threshold is IDLE_EVICT_S, the same as for eviction: a dead connection must mean the
     * same in both places (60 s gave RST to live HTTP/2 connections that a browser reuses after
     * a minute idle).
     *
     * RST is required: a silent drop leaves the client with a connection it thinks is open, and
     * its next request there waits for a timeout — a hung site. */
    if (g_now_s - c->last > IDLE_EVICT_S) {
        /* The same threshold for UDP, deliberately. An idle UDP flow is usually QUIC between
         * requests: its own idle timeout is shorter than ours, so the client drops it first.
         * Removing the flow earlier would change the next datagram's source port AT THE NODE —
         * a new path for the receiver, which QUIC would have to validate again. The kernel's
         * conntrack keeps UDP as long. */
        conn_reset(c, tun);                 /* for UDP just a close, no RST */
        return 1;
    }
    return 0;
}

/* What a loop thread needs: its device queue and its number. Nothing else: the dialer is shared
 * (g_dl), and the rest of the state is the thread's own (__thread and its tables). */
struct worker {
    struct tun_dev tun;
    int id;
    /* Whether the thread reached its loop, i.e. this queue carried traffic at all. Only to tell
     * "the tunnel did not come up" from "the tunnel worked and died" in the log; the exit code is
     * non-zero either way. */
    int served;
};

#define TUN_TOKEN ((uint32_t)MAX_CONNS)
#define EFD_TOKEN ((uint32_t)MAX_CONNS + 1)
#define LST_TOKEN ((uint32_t)MAX_CONNS + 2)
#define CTL_TOKEN ((uint32_t)MAX_CONNS + 3)
#define STOP_TOKEN ((uint32_t)MAX_CONNS + 4)
#define IBW_TOKEN ((uint32_t)MAX_CONNS + 5)
#define CFD_BASE ((uint32_t)MAX_CONNS + 6)
#define IB_HS_MAX 64
#define IB_HS_MS 5000
#define IB_CONNS_MAX 128
#define IB_RESERVE 64
#define IB_LST_BASE (CFD_BASE + (uint32_t)MAX_CONNS)
#define IB_HS_BASE (IB_LST_BASE + (uint32_t)IB_MAX)
#define IB_CTL_BASE (IB_HS_BASE + (uint32_t)IB_HS_MAX)
#define IB_UDP_BASE (IB_CTL_BASE + (uint32_t)IB_ASSOC_MAX)
#define IB_TOKEN_END (IB_UDP_BASE + (uint32_t)IB_ASSOC_MAX)
#define XC_BASE IB_TOKEN_END
#define EVS_MAX (2 * MAX_CONNS + 6 + 144 + MUX_IDX_MAX)
_Static_assert(XC_BASE + MUX_IDX_MAX < UINT32_MAX, "shared link tokens fit");
_Static_assert(CFD_BASE > IBW_TOKEN && IBW_TOKEN > STOP_TOKEN, "socket slots start after the fixed tokens");
_Static_assert(IB_LST_BASE >= CFD_BASE + MAX_CONNS && IB_HS_BASE >= IB_LST_BASE + IB_MAX &&
                   IB_CTL_BASE >= IB_HS_BASE + IB_HS_MAX && IB_UDP_BASE >= IB_CTL_BASE + IB_ASSOC_MAX &&
                   IB_TOKEN_END > IB_UDP_BASE && IB_TOKEN_END < UINT32_MAX &&
                   IB_TOKEN_END - IB_LST_BASE == 144,
               "inbound token ranges are disjoint, above the socket slots");
#define ACCEPT_MAX 64
#define PARK_MAX 256
#define PARK_ROOM_MAX (1 << 20)
#define PARK_MS 300
#define FD_RESERVE (2 * MAX_CONNS + SPARE_MAX + CONNECTORS * CONNECTOR_FDS + 32 + MUX_IDX_MAX)
#define MISS_WINDOW_NS 60000000000ull

struct loop {
    struct tun_dev tun;
    int ep, efd, ctl, ibw;
    uint8_t lst_off, own, stop_in, ib_off;
    uint64_t stats_at, loop_at, spare_at;
    unsigned nodes_seen;
    struct mux_host mh;
    struct epoll_event evs[EVS_MAX];
    unsigned char pkt[TUNNEL_BUF];
};

struct parked {
    int fd;
    uint32_t peer, od;
    uint16_t pport, oport;
    uint64_t at;
};

static int g_lst_fd = -1;
static int g_tcp_port;
static int g_park_room = PARK_MAX;
static int g_fd_extra;
static struct parked g_park[PARK_MAX];
static int g_park_n;
static struct {
    uint64_t since, max_ms;
    unsigned miss, answered, timed_out;
} g_miss;

int stack_tcp_port(void) { return g_tcp_port; }

static int g_stop_efd = -1;
static int g_stop_loops;

static int stop_fd(void) {
    int fd = __atomic_load_n(&g_stop_efd, __ATOMIC_ACQUIRE);
    if (fd >= 0) return fd;
    int nfd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    if (nfd < 0) return -1;
    int none = -1;
    if (__atomic_compare_exchange_n(&g_stop_efd, &none, nfd, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) return nfd;
    close(nfd);
    return none;
}

static struct ib_hs {
    int fd;
    uint8_t ib;
    uint16_t pport;
    uint32_t peer;
    uint64_t at;
    struct ibhs h;
} g_hs[IB_HS_MAX];

static int ib_live(void) {
    int n = 0;
    for (int li = 0; li < g_live_n; li++) n += g_conns[g_live[li]].ib != 0;
    return n;
}

static void ib_hs_free(struct ib_hs *s, int rst) {
    if (rst) sock_rst(s->fd);
    else sock_soft_close(s->fd);
    s->fd = -1;
    s->ib = 0;
}

static void ib_hs_end(struct ib_hs *s, enum ibhs_end end) {
    unsigned char r[IBHS_OUT];
    ib_send(s->fd, r, ibhs_done(s->h.http, end, r, sizeof r));
    ib_hs_free(s, 0);
}

static int ib_node_of(uint8_t ib) {
    int node = ib && ib <= IB_MAX ? g_ib[ib - 1].cfg.node : -1;
    if (!g_dl->ops->flow_open_on) return -1;
    if (node == IB_NODE_POOL) return node;
    return node < 0 || node >= nodes_count() ? -1 : node;
}

static void ib_connect(struct loop *L, struct ib_hs *s) {
    int node = ib_node_of(s->ib);
    if (node == -1) {
        ib_hs_end(s, IBHS_END_GONE);
        return;
    }
    if (ib_live() >= IB_CONNS_MAX || g_free_n < IB_RESERVE) {
        ib_hs_end(s, IBHS_END_FULL);
        return;
    }
    epoll_ctl(L->ep, EPOLL_CTL_DEL, s->fd, NULL);
    struct conn *c = conn_new(&L->tun);
    if (!c) {
        ib_hs_end(s, IBHS_END_FULL);
        return;
    }
    memset(c, 0, sizeof(*c));
    c->used = 1;
    c->sock = 1;
    c->cfd = s->fd;
    c->fd = -1;
    c->hnext = -1;
    c->born_turn = g_turn;
    c->room_seen = UINT32_MAX;
    c->key.src.v4 = s->peer;
    c->key.dst.v4 = htonl(s->h.ip);
    c->key.sport = s->pport;
    c->key.dport = s->h.port;
    c->key.proto = 6;
    c->ib = s->ib;
    c->ib_reply = s->h.http ? 2 : 1;
    c->livepos = (int16_t)g_live_n;
    g_live[g_live_n++] = (uint16_t)(c - g_conns);
    c->last = g_now_s;
    c->estab_ns = g_now_ns ? g_now_ns : 1;
    s->fd = -1;
    s->ib = 0;
    int one = 1;
    setsockopt(c->cfd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    const unsigned char *rest;
    size_t rn = ibhs_rest(&s->h, &rest);
    if (rn && early_hold(c, rest, rn) != 0) {
        ib_refuse(c, IBHS_END_FULL);
        return;
    }
    if (g_dl->ops->flow_open_on(SESS(c), node, &c->key, 0, s->h.host) != 0) {
        ib_refuse(c, IBHS_END_DIAL);
        return;
    }
    int qr = flow_link(c, 0);
    if (qr < 0) ib_refuse(c, IBHS_END_FULL);
    else if (qr == 0) conn_ready(c, &L->tun);
}

static void ib_associate(struct loop *L, struct ib_hs *s) {
    if (ib_node_of(s->ib) == -1) {
        ib_hs_end(s, IBHS_END_GONE);
        return;
    }
    int i = 0;
    while (i < IB_ASSOC_MAX && g_assoc[i].ib) i++;
    if (i == IB_ASSOC_MAX) {
        ib_hs_end(s, IBHS_END_FULL);
        return;
    }
    struct sockaddr_in me = { .sin_family = AF_INET, .sin_addr = { .s_addr = g_ib[s->ib - 1].cfg.addr } };
    socklen_t ml = sizeof me;
    struct epoll_event eu = { .events = EPOLLIN, .data = { .u32 = IB_UDP_BASE + (uint32_t)i } };
    int u = socket(AF_INET, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (u < 0 || bind(u, (struct sockaddr *)&me, sizeof me) != 0 ||
        getsockname(u, (struct sockaddr *)&me, &ml) != 0 || epoll_ctl(L->ep, EPOLL_CTL_ADD, u, &eu) != 0) {
        if (u >= 0) close(u);
        ib_hs_end(s, IBHS_END_FULL);
        return;
    }
    unsigned char r[10] = { 5, 0, 0, 1 };
    memcpy(r + 4, &me.sin_addr.s_addr, 4);
    memcpy(r + 8, &me.sin_port, 2);
    struct epoll_event ec = { .events = EPOLLIN, .data = { .u32 = IB_CTL_BASE + (uint32_t)i } };
    if (ib_send(s->fd, r, sizeof r) != 0 || epoll_ctl(L->ep, EPOLL_CTL_MOD, s->fd, &ec) != 0) {
        close(u);
        ib_hs_free(s, 1);
        return;
    }
    struct ib_assoc *a = &g_assoc[i];
    memset(a, 0, sizeof *a);
    a->ib = s->ib;
    a->tcp_fd = s->fd;
    a->ufd = u;
    a->client_ip = s->peer;
    for (int k = 0; k < IB_UDP_DST; k++) a->dst_conn[k] = -1;
    s->fd = -1;
    s->ib = 0;
}

static void ib_assoc_end(struct ib_assoc *a, int rst) {
    if (!a->ib) return;
    for (int k = 0; k < IB_UDP_DST; k++) {
        int idx = a->dst_conn[k];
        if (idx < 0) continue;
        struct conn *c = &g_conns[idx];
        a->dst_conn[k] = -1;
        c->ib_udp = 0;
        if (c->pending) c->client_gone = 1;
        else conn_drop(c);
    }
    close(a->ufd);
    if (rst) sock_rst(a->tcp_fd);
    else close(a->tcp_fd);
    memset(a, 0, sizeof *a);
    a->tcp_fd = a->ufd = -1;
}

static void ib_ctl_event(struct ib_assoc *a) {
    unsigned char sink[512];
    ssize_t n = a->ib ? recv(a->tcp_fd, sink, sizeof sink, MSG_DONTWAIT) : 1;
    if (n > 0 || (n < 0 && sock_again())) return;
    ib_assoc_end(a, n < 0);
}

static int ib_udp_slot(struct ib_assoc *a, uint32_t dst, uint16_t port, struct conn **found) {
    int free_k = -1, idle_k = -1;
    uint64_t now = g_now_ns / 1000000;
    *found = NULL;
    for (int k = 0; k < IB_UDP_DST; k++) {
        int idx = a->dst_conn[k];
        if (idx < 0) {
            if (free_k < 0) free_k = k;
            continue;
        }
        struct conn *c = &g_conns[idx];
        if (c->key.dst.v4 == dst && c->key.dport == port) {
            *found = c;
            return k;
        }
        if (!c->pending && now - a->last_ms[k] >= IB_UDP_IDLE_MS &&
            (idle_k < 0 || a->last_ms[k] < a->last_ms[idle_k]))
            idle_k = k;
    }
    if (free_k >= 0 || idle_k < 0) return free_k;
    conn_drop(&g_conns[a->dst_conn[idle_k]]);
    return idle_k;
}

static void ib_udp_out(struct loop *L, struct ib_assoc *a, uint32_t dst, uint16_t port, const unsigned char *p,
                       size_t n) {
    struct conn *c;
    int k = ib_udp_slot(a, dst, port, &c);
    if (k < 0) return;
    if (!c) {
        int node = ib_node_of(a->ib);
        if (node == -1 || ib_live() >= IB_CONNS_MAX || g_free_n < IB_RESERVE || !(c = conn_new(&L->tun))) return;
        memset(c, 0, sizeof(*c));
        c->used = 1;
        c->is_udp = 1;
        c->fd = c->cfd = -1;
        c->hnext = -1;
        c->born_turn = g_turn;
        c->key.src.v4 = a->client_ip;
        c->key.dst.v4 = dst;
        c->key.sport = ntohs(a->client.sin_port);
        c->key.dport = port;
        c->key.proto = 17;
        c->ib = a->ib;
        c->ib_udp = (uint8_t)(a - g_assoc + 1);
        c->livepos = (int16_t)g_live_n;
        g_live[g_live_n++] = (uint16_t)(c - g_conns);
        a->dst_conn[k] = (int)(c - g_conns);
        if (g_dl->ops->flow_open_on(SESS(c), node, &c->key, 1, NULL) != 0 || flow_link(c, 0) < 0) {
            conn_drop(c);
            return;
        }
    }
    a->last_ms[k] = g_now_ns / 1000000;
    c->last = g_now_s;
    if (udp_out(c, p, n)) kb_add(c, 0, n);
}

static void ib_udp_event(struct loop *L, struct ib_assoc *a) {
    static unsigned char b[10 + UDP_DGRAM_MAX + 1];
    for (int k = 0; a->ib && k < ACCEPT_MAX; k++) {
        struct sockaddr_in from;
        socklen_t fl = sizeof from;
        ssize_t n = recvfrom(a->ufd, b, sizeof b, MSG_DONTWAIT, (struct sockaddr *)&from, &fl);
        if (n < 0) return;
        if (from.sin_addr.s_addr != a->client_ip || n <= 10 || (size_t)n > 10 + UDP_DGRAM_MAX || b[0] || b[1] ||
            b[2] || b[3] != 1)
            continue;
        if (a->client.sin_family && a->client.sin_port != from.sin_port) continue;
        a->client = from;
        uint32_t dst;
        memcpy(&dst, b + 4, 4);
        ib_udp_out(L, a, dst, (uint16_t)(b[8] << 8 | b[9]), b + 10, (size_t)n - 10);
    }
}

static void ib_hs_event(struct loop *L, struct ib_hs *s) {
    if (!s->ib) return;
    unsigned char in[sizeof s->h.buf], out[IBHS_OUT];
    size_t room = sizeof s->h.buf - s->h.len, on = 0;
    ssize_t n = room ? recv(s->fd, in, room, MSG_DONTWAIT) : 0;
    if (n < 0 && sock_again()) return;
    if (n <= 0) {
        ib_hs_free(s, 1);
        return;
    }
    enum ibhs_rc rc = ibhs_feed(&s->h, &g_ib[s->ib - 1].cfg, in, (size_t)n, out, &on);
    if (rc == IBHS_MORE) return;
    if (ib_send(s->fd, out, on) != 0) {
        ib_hs_free(s, 1);
        return;
    }
    if (rc == IBHS_REPLY) return;
    if (rc == IBHS_CONNECT) {
        ib_connect(L, s);
        return;
    }
    if (rc == IBHS_UDP) {
        ib_associate(L, s);
        return;
    }
    ib_hs_free(s, 0);
}

static void ib_arm(struct loop *L) {
    L->ib_off = 0;
    for (int i = 0; i < IB_MAX; i++) {
        struct epoll_event e = { .events = EPOLLIN, .data = { .u32 = IB_LST_BASE + (uint32_t)i } };
        if (g_ib[i].used && g_ib[i].fd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, g_ib[i].fd, &e) != 0 &&
            errno != EEXIST)
            L->ib_off = 1;
    }
}

static void ib_accept(struct loop *L, int i) {
    struct ib_lst *ib = &g_ib[i];
    for (int k = 0; ib->used && ib->fd >= 0 && k < ACCEPT_MAX; k++) {
        struct sockaddr_in pa;
        socklen_t pl = sizeof pa;
        int fd = accept4(ib->fd, (struct sockaddr *)&pa, &pl, SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (fd < 0) {
            if (errno == EINTR || errno == ECONNABORTED) continue;
            if (errno == EMFILE || errno == ENFILE) {
                epoll_ctl(L->ep, EPOLL_CTL_DEL, ib->fd, NULL);
                L->ib_off = 1;
            }
            return;
        }
        struct ib_hs *s = NULL;
        for (int j = 0; j < IB_HS_MAX && !s; j++)
            if (!g_hs[j].ib) s = &g_hs[j];
        if (!s) {
            sock_rst(fd);
            continue;
        }
        memset(s, 0, offsetof(struct ib_hs, h));
        ibhs_init(&s->h, &ib->cfg);
        s->fd = fd;
        s->ib = (uint8_t)(i + 1);
        s->peer = pa.sin_addr.s_addr;
        s->pport = ntohs(pa.sin_port);
        s->at = g_now_ns;
        struct epoll_event e = { .events = EPOLLIN, .data = { .u32 = IB_HS_BASE + (uint32_t)(s - g_hs) } };
        if (epoll_ctl(L->ep, EPOLL_CTL_ADD, fd, &e) != 0) ib_hs_free(s, 1);
    }
}

static int ib_bind(const struct ib_cfg *c, int *err) {
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
    int one = 1;
    struct sockaddr_in a = { .sin_family = AF_INET, .sin_port = htons(c->port), .sin_addr = { .s_addr = c->addr } };
    if (fd < 0 || setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one) != 0 ||
        bind(fd, (struct sockaddr *)&a, sizeof a) != 0 || listen(fd, 128) != 0) {
        *err = errno;
        if (fd >= 0) close(fd);
        return -1;
    }
    return fd;
}

static void ib_drop(int i) {
    struct ib_lst *ib = &g_ib[i];
    if (ib->fd >= 0) close(ib->fd);
    for (int j = 0; j < IB_HS_MAX; j++)
        if (g_hs[j].ib == i + 1) ib_hs_free(&g_hs[j], 1);
    for (int a = 0; a < IB_ASSOC_MAX; a++)
        if (g_assoc[a].ib == i + 1) ib_assoc_end(&g_assoc[a], 0);
    for (int li = 0; li < g_live_n; li++)
        if (g_conns[g_live[li]].ib == i + 1) g_conns[g_live[li]].ib = IB_ORPHAN;
    memset(ib, 0, sizeof(*ib));
    ib->fd = -1;
}

static void ib_listen(struct loop *L, int i, const struct ib_cfg *c) {
    struct ib_lst *ib = &g_ib[i];
    if (!ib->used) {
        memset(ib, 0, sizeof(*ib));
        ib->used = 1;
        ib->fd = -1;
        ib->kb.at = g_now_ns;
    }
    if (ib->fd >= 0 && ib->cfg.addr != c->addr) {
        for (int j = 0; j < IB_ASSOC_MAX; j++)
            if (g_assoc[j].ib == i + 1) ib_assoc_end(&g_assoc[j], 1);
        close(ib->fd);
        ib->fd = -1;
    }
    if (strcmp(ib->cfg.user, c->user) || strcmp(ib->cfg.pass, c->pass))
        for (int j = 0; j < IB_HS_MAX; j++)
            if (g_hs[j].ib == i + 1) ib_hs_free(&g_hs[j], 0);
    ib->cfg = *c;
    int err = 0;
    if (ib->fd < 0) {
        ib->fd = ib_bind(c, &err);
        struct epoll_event e = { .events = EPOLLIN, .data = { .u32 = IB_LST_BASE + (uint32_t)i } };
        if (ib->fd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, ib->fd, &e) != 0) {
            err = errno;
            close(ib->fd);
            ib->fd = -1;
        }
    }
    if (ib->fd < 0) {
        if (err != ib->lerr) fprintf(stderr, LOG_E "inbound :%u cannot listen: %s\n", c->port, strerror(err));
        ib->lerr = err;
        ctl_emit_inbound(c->port, "busy", strerror(err));
        return;
    }
    ib->lerr = 0;
    ctl_emit_inbound(c->port, c->node == -1 ? "no-node" : "up", "");
}

static void ib_apply(struct loop *L) {
    uint64_t v;
    ssize_t rd = read(L->ibw, &v, sizeof v);
    (void)rd;
    struct ib_cfg set[IB_MAX];
    size_t n = ctlinb_take(set, IB_MAX, NULL);
    int at[IB_MAX];
    uint8_t taken[IB_MAX] = { 0 };
    for (size_t e = 0; e < n; e++) at[e] = -1;
    for (int loose = 0; loose < 2; loose++)
        for (size_t e = 0; e < n; e++)
            for (int i = 0; at[e] < 0 && i < IB_MAX; i++)
                if (g_ib[i].used && !taken[i] && g_ib[i].cfg.port == set[e].port &&
                    (loose || g_ib[i].cfg.addr == set[e].addr)) {
                    at[e] = i;
                    taken[i] = 1;
                }
    for (int i = 0; i < IB_MAX; i++)
        if (g_ib[i].used && !taken[i]) ib_drop(i);
    for (size_t e = 0; e < n; e++)
        for (int i = 0; at[e] < 0 && i < IB_MAX; i++)
            if (!g_ib[i].used && !taken[i]) {
                at[e] = i;
                taken[i] = 1;
            }
    for (size_t e = 0; e < n; e++)
        if (at[e] >= 0) ib_listen(L, at[e], &set[e]);
}

static void ib_deadlines(uint64_t now) {
    for (int j = 0; j < IB_HS_MAX; j++)
        if (g_hs[j].ib && now - g_hs[j].at >= (uint64_t)IB_HS_MS * 1000000ull) ib_hs_free(&g_hs[j], 1);
}

static void ib_stop(void) {
    for (int i = 0; i < IB_MAX; i++)
        if (g_ib[i].used && g_ib[i].fd >= 0) {
            close(g_ib[i].fd);
            g_ib[i].fd = -1;
        }
    for (int j = 0; j < IB_HS_MAX; j++)
        if (g_hs[j].ib) ib_hs_free(&g_hs[j], 1);
    for (int a = 0; a < IB_ASSOC_MAX; a++) ib_assoc_end(&g_assoc[a], 1);
}

static void stop_reset(const struct tun_dev *tun, int own) {
    if (own && g_lst_fd >= 0) {
        close(g_lst_fd);
        g_lst_fd = -1;
    }
    if (own) ib_stop();
    for (int li = 0; li < g_live_n; li++) {
        const struct conn *c = &g_conns[g_live[li]];
        if (c->sock) {
            sock_linger(c->cfd, !c->srv_closed || c->rtx.len);
        } else if (!c->is_udp) {
            conn_rst(c, tun);
        }
    }
    for (int i = 0; own && i < g_park_n; i++) sock_linger(g_park[i].fd, 1);
}

void stack_stop(int wait_ms) {
    int fd = __atomic_load_n(&g_stop_efd, __ATOMIC_ACQUIRE);
    if (fd < 0) return;
    uint64_t one = 1;
    if (write(fd, &one, sizeof one) != (ssize_t)sizeof one) return;
    struct timespec ts = { 0, 1000000 };
    for (int i = 0; i < wait_ms && __atomic_load_n(&g_stop_loops, __ATOMIC_ACQUIRE) > 0; i++) nanosleep(&ts, NULL);
}

static int lst_open(int port) {
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
    int one = 1;
    struct sockaddr_in a = { .sin_family = AF_INET, .sin_port = htons((uint16_t)port), .sin_addr = { .s_addr = htonl(INADDR_ANY) } };
    socklen_t al = sizeof a;
    if (fd < 0 || setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one) != 0 ||
        bind(fd, (struct sockaddr *)&a, sizeof a) != 0 || listen(fd, 128) != 0 ||
        getsockname(fd, (struct sockaddr *)&a, &al) != 0) {
        int err = errno;
        if (fd >= 0) close(fd);
        fprintf(stderr, LOG_W "cannot listen on port %d: %s, TCP stays on the TUN\n", port,
                strerror(err));
        return -1;
    }
    g_lst_fd = fd;
    g_tcp_port = ntohs(a.sin_port);
    return 0;
}

static void park_cap_init(void) {
    struct rlimit rl;
    if (getrlimit(RLIMIT_NOFILE, &rl) != 0 || rl.rlim_cur >= (rlim_t)(FD_RESERVE + PARK_ROOM_MAX))
        g_park_room = PARK_ROOM_MAX;
    else if (rl.rlim_cur <= (rlim_t)FD_RESERVE)
        g_park_room = 0;
    else
        g_park_room = (int)(rl.rlim_cur - FD_RESERVE);
}

static int park_cap(void) {
    int room = g_park_room - __atomic_load_n(&g_fd_extra, __ATOMIC_RELAXED);
    return room < 0 ? 0 : room > PARK_MAX ? PARK_MAX : room;
}

static void mux_fd_extra(int delta) { __atomic_add_fetch(&g_fd_extra, delta, __ATOMIC_RELAXED); }

static void sock_open(const struct tun_dev *tun, int fd, uint32_t peer, uint16_t pport,
                      const struct cm_real *dst, uint32_t od, uint16_t oport) {
    struct conn *c = conn_new(tun);
    if (!c) {
        sock_rst(fd);
        return;
    }
    memset(c, 0, sizeof(*c));
    c->used = 1;
    c->sock = 1;
    c->cfd = fd;
    c->fd = -1;
    c->hnext = -1;
    c->born_turn = g_turn;
    c->room_seen = UINT32_MAX;
    c->key.src.v4 = peer;
    memcpy(c->key.dst.b, dst->b, sizeof c->key.dst.b);
    c->key.v6 = dst->v6;
    c->key.sport = pport;
    c->key.dport = oport;
    c->key.proto = 6;
    c->odst = od;
    c->livepos = (int16_t)g_live_n;
    g_live[g_live_n++] = (uint16_t)(c - g_conns);
    c->last = g_now_s;
    c->estab_ns = g_now_ns ? g_now_ns : 1;
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    if (g_dl->ops->flow_open(g_dl->ctx, SESS(c), &c->key, 0) != 0) {
        conn_drop(c);
        return;
    }
    if (flow_link(c, 1) < 0) conn_drop(c);
    spare_refill();
}

static int park_has(uint32_t od) {
    for (int i = 0; i < g_park_n; i++)
        if (g_park[i].od == od) return 1;
    return 0;
}

static struct parked park_take(int i) {
    struct parked q = g_park[i];
    g_park[i] = g_park[--g_park_n];
    if (!park_has(q.od)) ctlmap_unask(q.od);
    return q;
}

static void park(const struct tun_dev *tun, int fd, uint32_t peer, uint16_t pport, uint32_t od,
                 uint16_t oport) {
    g_miss.miss++;
    if (g_park_n >= park_cap()) {
        sock_rst(fd);
        return;
    }
    int a = ctlmap_ask(od);
    if (a < 0) {
        struct cm_real real;
        if (ctlmap_lookup(od, &real) == CM_REAL) sock_open(tun, fd, peer, pport, &real, od, oport);
        else sock_rst(fd);
        return;
    }
    if (a > 0) {
        char s[INET_ADDRSTRLEN];
        if (inet_ntop(AF_INET, &od, s, sizeof s)) ctl_emit_miss(s);
    }
    g_park[g_park_n++] = (struct parked){ fd, peer, od, pport, oport, g_now_ns };
}

static void park_pass(const struct tun_dev *tun) {
    for (int i = 0; i < g_park_n;) {
        struct cm_real real;
        int st = ctlmap_answer(g_park[i].od, &real);
        uint64_t age = (g_now_ns - g_park[i].at) / 1000000;
        if (st == CM_REAL || st == CM_DENIED) {
            g_miss.answered++;
            if (age > g_miss.max_ms) g_miss.max_ms = age;
        } else if (age < PARK_MS) {
            i++;
            continue;
        } else {
            g_miss.timed_out++;
        }
        struct parked q = park_take(i);
        if (st == CM_REAL) sock_open(tun, q.fd, q.peer, q.pport, &real, q.od, q.oport);
        else sock_rst(q.fd);
    }
}

static int park_wait_ms(int wait_ms) {
    for (int i = 0; i < g_park_n; i++) {
        uint64_t age = (g_now_ns - g_park[i].at) / 1000000;
        int left = age >= PARK_MS ? 0 : (int)(PARK_MS - age);
        if (left < wait_ms) wait_ms = left;
    }
    return wait_ms;
}

static void miss_report(uint64_t now) {
    if (now - g_miss.since < MISS_WINDOW_NS) return;
    if (g_miss.miss)
        fprintf(stderr, LOG_W "%u misses in the last minute, %u answered (max %llu ms), %u timed out\n",
                g_miss.miss, g_miss.answered, (unsigned long long)g_miss.max_ms, g_miss.timed_out);
    memset(&g_miss, 0, sizeof g_miss);
    g_miss.since = now;
}

static void bytes_report(uint64_t now) {
    uint32_t rx, tx;
    if (kb_take(&g_kb, now, &rx, &tx)) ctl_emit_bytes(rx, tx);
    for (int i = 0; i < IB_MAX; i++)
        if (g_ib[i].used && kb_take(&g_ib[i].kb, now, &rx, &tx)) ctl_emit_ibytes(g_ib[i].cfg.port, rx, tx);
}

static void sock_take(const struct tun_dev *tun, int fd, const struct sockaddr_in *pa) {
    struct sockaddr_in od, me;
    socklen_t ol = sizeof od, ml = sizeof me;
    sock_linger(fd, 1);
    if (getsockopt(fd, SOL_IP, SO_ORIGINAL_DST, &od, &ol) != 0 || od.sin_family != AF_INET ||
        getsockname(fd, (struct sockaddr *)&me, &ml) != 0 ||
        (od.sin_addr.s_addr == me.sin_addr.s_addr && od.sin_port == me.sin_port)) {
        sock_rst(fd);
        return;
    }
    struct cm_real dst = { { 0 }, 0 };
    memcpy(dst.b, &od.sin_addr.s_addr, 4);
    uint16_t pport = ntohs(pa->sin_port), oport = ntohs(od.sin_port);
    if (ctlmap_in_pool(od.sin_addr.s_addr) && ctlmap_lookup(od.sin_addr.s_addr, &dst) != CM_REAL) {
        park(tun, fd, pa->sin_addr.s_addr, pport, od.sin_addr.s_addr, oport);
        return;
    }
    sock_open(tun, fd, pa->sin_addr.s_addr, pport, &dst, od.sin_addr.s_addr, oport);
}

static void lst_arm(struct loop *L) {
    struct epoll_event e = { .events = EPOLLIN, .data = { .u32 = LST_TOKEN } };
    L->lst_off = g_lst_fd >= 0 && !(epoll_ctl(L->ep, EPOLL_CTL_ADD, g_lst_fd, &e) == 0 || errno == EEXIST);
}

static void lst_accept(struct loop *L) {
    for (int i = 0; i < ACCEPT_MAX; i++) {
        struct sockaddr_in pa;
        socklen_t pl = sizeof pa;
        int fd = accept4(g_lst_fd, (struct sockaddr *)&pa, &pl, SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (fd < 0) {
            if (errno == EINTR || errno == ECONNABORTED) continue;
            if (errno == EMFILE || errno == ENFILE) {
                epoll_ctl(L->ep, EPOLL_CTL_DEL, g_lst_fd, NULL);
                L->lst_off = 1;
            }
            return;
        }
        sock_take(&L->tun, fd, &pa);
    }
}

static int in_prefixes(uint32_t a, const uint32_t *base, const uint8_t *len, size_t k) {
    for (size_t i = 0; i < k; i++) {
        uint32_t m = len[i] ? htonl(~0u << (32 - len[i])) : 0;
        if ((a & m) == base[i]) return 1;
    }
    return 0;
}

static void ctl_pass(struct loop *L) {
    uint64_t v;
    ssize_t rd = read(L->ctl, &v, sizeof v);
    (void)rd;
    TR("map applied at %llu ms\n", (unsigned long long)(g_now_ns / 1000000));
    uint32_t base[64];
    uint8_t len[64];
    size_t k;
    int n = 0, ends = 0;
    while ((k = ctlmap_take_closes(base, len, 64, &ends)) > 0 || ends > 0) {
        for (int li = 0; li < g_live_n;) {
            struct conn *c = &g_conns[g_live[li]];
            if (!c->sock || c->ib || !in_prefixes(c->odst, base, len, k)) {
                li++;
                continue;
            }
            if (c->pending) {
                if (!c->client_gone) n++;
                c->client_gone = 1;
                li++;
                continue;
            }
            conn_reset(c, &L->tun);
            n++;
        }
        for (int i = 0; i < g_park_n;) {
            if (!in_prefixes(g_park[i].od, base, len, k)) {
                i++;
                continue;
            }
            sock_rst(park_take(i).fd);
            n++;
        }
        for (; ends > 0; ends--) {
            ctl_emit_close(n);
            n = 0;
        }
    }
}

static void sock_arm(struct loop *L, struct conn *c) {
    long room = conn_room(c);
    unsigned arm = (!c->early && !c->client_fin && !c->srv_closed && room != 0 ? ARM_IN : 0u) |
                   (c->rtx.len ? ARM_OUT : 0u);
    if (arm == c->carmed) return;
    struct epoll_event e = { .events = (arm & ARM_IN ? EPOLLIN : 0u) | (arm & ARM_OUT ? EPOLLOUT : 0u),
                             .data = { .u32 = CFD_BASE + (uint32_t)(c - g_conns) } };
    int op = !c->carmed ? EPOLL_CTL_ADD : !arm ? EPOLL_CTL_DEL : EPOLL_CTL_MOD;
    if (epoll_ctl(L->ep, op, c->cfd, &e) == 0 || !arm) c->carmed = (uint8_t)arm;
}

static void sock_event(struct conn *c, uint32_t events, const struct tun_dev *tun) {
    if ((events & (EPOLLOUT | EPOLLERR | EPOLLHUP)) && c->rtx.len) sock_flush(c);
    if ((events & (EPOLLIN | EPOLLERR | EPOLLHUP)) && (c->carmed & ARM_IN)) sock_read(c, tun);
}

static __thread struct loop *g_loop;
static __thread unsigned g_mux_woke;
static unsigned g_loops = 1;

static int mux_watch(unsigned idx, int fd, unsigned ev) {
    if (!g_loop || idx >= MUX_IDX_MAX) return -1;
    if (!ev) return epoll_ctl(g_loop->ep, EPOLL_CTL_DEL, fd, NULL) == 0 || errno == ENOENT ? 0 : -1;
    struct epoll_event e = { .events = ev, .data = { .u32 = XC_BASE + idx } };
    if (epoll_ctl(g_loop->ep, EPOLL_CTL_MOD, fd, &e) == 0) return 0;
    return errno == ENOENT && epoll_ctl(g_loop->ep, EPOLL_CTL_ADD, fd, &e) == 0 ? 0 : -1;
}

static void mux_wake(void *arg, uint16_t owner, unsigned what, int rc) {
    (void)arg;
    if (owner >= MAX_CONNS) return;
    struct conn *c = &g_conns[owner];
    if (!c->used || !c->mux) return;
    if (c->pending) {
        if (!(what & XW_DONE)) return;
        c->rc = rc;
        __atomic_store_n(&c->done, 1, __ATOMIC_RELEASE);
        return;
    }
    if ((what & XW_READ) && !c->srv_closed && !c->rx_ready) {
        c->rx_ready = 1;
        g_mux_woke++;
    }
    if (what & XW_ROOM) room_watch(c);
}

static void loop_mux(struct loop *L) {
    g_loop = L;
    g_mux_tun = &L->tun;
    const struct dialer_mux *mx = g_dl->ops->mux;
    if (!mx) return;
    L->mh = (struct mux_host){ .submit = mux_submit, .watch = mux_watch, .wake = mux_wake, .arg = L, .loops = g_loops,
                              .wait_ms = CONNECT_TIMEOUT_S * 1000, .fd_extra = mux_fd_extra };
    mx->host(&L->mh);
}

static __thread int g_mux_due;

static void mux_pass(int phase) {
    const struct dialer_mux *mx = g_dl->ops->mux;
    if (!mx) return;
    int due = mx->pass(g_now_ns / 1000000, phase);
    if (phase == MUXP_LATE) g_mux_due = due;
}

/* epoll, not poll. poll takes the whole descriptor LIST on every call: with 320 live
 * connections and two thousand turns a second that is 640 thousand descriptor checks a
 * second, of which a few matter, and on a router one poll over 320 descriptors costs hundreds
 * of microseconds — the loop hit it before the crypto. epoll keeps the set in the kernel and
 * returns ONLY what is ready; in exchange the set must be maintained, which is what the armed
 * field is for: epoll_ctl on a state change, not on every turn.
 *
 * The TUN and eventfd tokens cannot collide with a connection index: there are MAX_CONNS
 * slots, the tokens are above.
 *
 * Wakeups from connectors: without them a finished handshake waits for the 20 ms poll, adding
 * up to 20 ms to EVERY new connection. If the kernel gives no eventfd, polling remains the
 * fallback, at that cost. */
static int loop_open(struct loop *L, const struct tun_dev *tun, int own) {
    memset(L, 0, offsetof(struct loop, evs));
    L->tun = *tun;
    L->efd = L->ctl = L->ibw = -1;
    L->own = (uint8_t)(own != 0);
    L->stats_at = g_stats ? now_ns() : 0;
    L->nodes_seen = __atomic_load_n(&g_nodes_epoch, __ATOMIC_ACQUIRE);
    L->ep = epoll_create1(0);
    if (L->ep < 0) return -1;
    struct epoll_event e = { .events = EPOLLIN, .data = { .u32 = TUN_TOKEN } };
    if (tun->fd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, tun->fd, &e) != 0) {
        int err = errno;
        close(L->ep);
        errno = err;
        return -2;
    }
    L->efd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    e.data.u32 = EFD_TOKEN;
    if (L->efd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, L->efd, &e) != 0) {
        close(L->efd);
        L->efd = -1;
    }
    g_conn_efd = L->efd;
    int sfd = stop_fd();
    e.data.u32 = STOP_TOKEN;
    if (sfd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, sfd, &e) == 0) {
        L->stop_in = 1;
        __atomic_add_fetch(&g_stop_loops, 1, __ATOMIC_ACQ_REL);
    }
    if (own) {
        if (g_lst_fd >= 0) lst_arm(L);
        int cfd = ctlmap_wake_fd();
        e.data.u32 = CTL_TOKEN;
        if (cfd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, cfd, &e) == 0) L->ctl = cfd;
        int ifd = ctlinb_wake_fd();
        e.data.u32 = IBW_TOKEN;
        if (ifd >= 0 && epoll_ctl(L->ep, EPOLL_CTL_ADD, ifd, &e) == 0) L->ibw = ifd;
    }
    loop_mux(L);
    return 0;
}

static void loop_close(struct loop *L) {
    if (g_loop == L) {
        g_loop = NULL;
        g_mux_tun = NULL;
    }
    if (L->stop_in) __atomic_sub_fetch(&g_stop_loops, 1, __ATOMIC_ACQ_REL);
    if (L->efd >= 0) close(L->efd);
    g_conn_efd = -1;
    close(L->ep);
}

static int loop_turn(struct loop *L, int max_ms) {
    struct tun_dev *tun = &L->tun;
    /* Time is read ONCE per turn into g_now_ns/g_now_s; why, see their declaration. */
    uint64_t now = now_ns();
    g_now_ns = now;
    g_now_s = (time_t)(now / 1000000000ull);

    /* Turn period: the time since the last wait returned, i.e. the WORKING part, not the time
     * waiting: the RTO floor must cover how long we are busy, and a sleep is cut short by an
     * arriving ACK anyway. 1/8 smoothing, as in TCP's RTT estimate, so that one long turn
     * does not raise the floor for long. */
    if (L->loop_at) {
        uint64_t busy64 = (now - L->loop_at) / 1000000;
        uint32_t busy = busy64 > 5000 ? 5000 : (uint32_t)busy64;
        /* Up with rounding up, so on noticing load the floor rises by at least a
         * millisecond at once; down with rounding down, to avoid jitter. */
        if (busy > g_loop_ms) g_loop_ms += (busy - g_loop_ms + 7) / 8;
        else                  g_loop_ms -= (g_loop_ms - busy) / 8;
    }

    /* ONE pass over the live connections before the wait (see struct conn for why). It
     * computes everything the wait needs: the epoll set, the number of pending jobs, the
     * connections with data already read, and the nearest retransmission or first-payload
     * deadline. */
    int pend = 0, forced = 0;
    /* Wait until the nearest retransmission deadline, not always a second: otherwise a
     * resend waits for the loop to wake by itself. With live traffic that is at once, but
     * after the last segment is lost nothing wakes it — exactly the case the timer is for.
     * The same holds for the bytes a link keeps back for a mask delay (fm_wait). */
    int wait_ms = max_ms;
    for (int li = 0; li < g_live_n; li++) {
        struct conn *c = &g_conns[g_live[li]];
        if (c->pending) {
            pend++;
            if (c->mux && __atomic_load_n(&c->done, __ATOMIC_ACQUIRE)) wait_ms = 0;
            continue;
        }
        if (c->fm_wait && c->srv_closed) c->fm_wait = 0;
        if (c->fm_wait) {
            int next;
            if (conn_due(c, now / 1000000, &next)) {
                conn_node_lost(c);
                conn_reset(c, tun);
                li--;
                continue;
            }
            if (next > 0 && next < wait_ms) wait_ms = next;
        }

        /* Window and in-flight are recorded ALWAYS, not only on a refused read: a flow can
         * also stall with the window open, and only both numbers tell the two cases
         * apart. */
        if (g_stats) {
            uint32_t room = client_room(c);
            uint32_t infl = c->our_seq - c->client_ack;
            if (room < g_st.win_min) g_st.win_min = room;
            if (room > g_st.win_max) g_st.win_max = room;
            if (infl > g_st.inflight_max) g_st.inflight_max = infl;
            g_st.wscale_seen = c->client_wscale;
            if (c->mss && c->mss < g_st.mss_min) g_st.mss_min = c->mss;
            if (c->mss > g_st.mss_max) g_st.mss_max = c->mss;
        }

        /* Do not ask the server for more while the client has not taken what it got. This is
         * the only flow control we have: what we read must go to the device at once, and the
         * device queue is finite; overflowing it loses a segment, and the client waits for a
         * retransmission (measured on a router: without this check a transfer stopped at the
         * second megabyte). Connections closed by the server have nothing to read: they live
         * only to finish delivering.
         *
         * A UDP flow has no window and so no reason not to read: a datagram goes to the client
         * at once and whole. */
        int want = !c->srv_closed && conn_can_take(c);
        if (!want && !c->srv_closed && g_stats) g_st.win_skips++;
        /* The ACK is held (ack_hold): wait until the descriptor becomes writable, which is
         * the client's window. If the ACK left another way (data to the client carries it in
         * its header, emit_to_client), there is nothing to wait for: a level-triggered
         * EPOLLOUT on a writable descriptor would wake the loop on every turn for nothing. */
        if (c->ack_hold && !c->ack_due) c->ack_hold = 0;
        unsigned arm = (want ? ARM_IN : 0u) | (c->ack_hold || c->fm_wait == 2 ? ARM_OUT : 0u);
        if (!c->mux && arm != c->armed) {
            struct epoll_event e = { .events = (arm & ARM_IN ? EPOLLIN : 0u) |
                                               (arm & ARM_OUT ? EPOLLOUT : 0u),
                                     .data = { .u32 = (uint32_t)(c - g_conns) } };
            int op = !c->armed ? EPOLL_CTL_ADD : !arm ? EPOLL_CTL_DEL : EPOLL_CTL_MOD;
            if (epoll_ctl(L->ep, op, c->fd, &e) == 0 || !arm)
                c->armed = (uint8_t)arm;
        }
        if (c->sock) sock_arm(L, c);
        /* Data is already here and the kernel will not report it: process it without
         * waiting. A send or a room query may have read it off the socket too. */
        if (want && !c->rx_ready && g_dl->ops->has_data(SESS(c))) c->rx_ready = 1;
        if (want && c->rx_ready) forced++;

        if (c->rtx.len && c->rtx_at) {
            int64_t left = (int64_t)c->rto_ms - (int64_t)((now - c->rtx_at) / 1000000);
            if (left < 0) left = 0;
            if (left < wait_ms) wait_ms = (int)left;
        }
        if (!c->fw_again) {
            int64_t left = first_wait_left(c, now);
            if (left >= 0 && left < wait_ms) wait_ms = (int)left;
        }
    }
    if (g_stats) g_st.conns = (uint32_t)g_live_n;

    /* Pending jobs do not force frequent wakeups: a connector wakes the loop through the
     * eventfd, and the result is handled at once. Polling every 20 ms remains the FALLBACK
     * for when the kernel gave no eventfd, so its failure costs latency, not a hung
     * handshake. */
    if (g_mux_due > 0 && g_mux_due < wait_ms) wait_ms = g_mux_due;
    if (pend && L->efd < 0 && wait_ms > 20) wait_ms = 20;
    if (L->own) wait_ms = park_wait_ms(wait_ms);
    if ((L->lst_off || L->ib_off) && wait_ms > 10) wait_ms = 10;
    if (forced) wait_ms = 0;

    /* Up to two more events per turn than connections: TUN and the wakeup are descriptors
     * too. A smaller limit would not lose an event (level-triggered epoll reports it again)
     * but would delay it by a turn. */
    int r = epoll_wait(L->ep, L->evs, EVS_MAX, wait_ms);
    g_turn++;
    uint64_t after_poll = now_ns();
    L->loop_at = after_poll;
    g_now_ns = after_poll;
    g_now_s = (time_t)(after_poll / 1000000000ull);
    if (g_stats) {
        g_st.poll_ns += after_poll - now;
        g_st.iters++;
        if (after_poll - L->stats_at >= 2000000000ull) {
            stats_dump(after_poll - L->stats_at);
            L->stats_at = after_poll;
        }
    }
    if (r < 0) return errno == EINTR ? 0 : -1;
    mux_pass(MUXP_EARLY);

    /* Finished jobs. The SYN-ACK went to the client on SYN: here the flow to the node is
     * either ready (and gets the early data from the buffer) or failed (and the client gets
     * RST). Done BEFORE reading TUN, so fresh client data goes in this same turn. Walks the
     * live connections only when there are jobs at all.
     *
     * The index moves BY HAND: conn_drop moves the last element of g_live into the freed
     * place, and a plain li++ would skip it. */
    for (int li = 0; pend && li < g_live_n; ) {
        struct conn *c = &g_conns[g_live[li]];
        if (!c->pending || !__atomic_load_n(&c->done, __ATOMIC_ACQUIRE)) { li++; continue; }
        pend--;
        if (!conn_ready(c, tun)) li++;
    }

    /* Look for TUN readiness among the events. TUN is handled FIRST, before reading from the
     * servers: client ACKs open the window, and handled earlier they let us take the
     * server's next portion in this same turn. The wakeup counter is just drained: the event
     * has done its job by waking epoll_wait. */
    int tun_ready = 0, lst = 0, ctl = 0, stop = 0, ibw = 0;
    if (L->lst_off) {
        lst_arm(L);
        lst = 1;
    }
    if (L->ib_off) ib_arm(L);
    for (int i = 0; i < r; i++) {
        if (L->evs[i].data.u32 == TUN_TOKEN) tun_ready = 1;
        else if (L->evs[i].data.u32 == EFD_TOKEN && L->efd >= 0) {
            uint64_t drained;
            ssize_t rd = read(L->efd, &drained, sizeof(drained));
            (void)rd;
        } else if (L->evs[i].data.u32 == LST_TOKEN) lst = 1;
        else if (L->evs[i].data.u32 == CTL_TOKEN) ctl = 1;
        else if (L->evs[i].data.u32 == STOP_TOKEN) stop = 1;
        else if (L->evs[i].data.u32 == IBW_TOKEN) ibw = 1;
    }
    if (stop && L->stop_in) {
        stop_reset(tun, L->own);
        if (g_dl->ops->mux && g_dl->ops->mux->stop) g_dl->ops->mux->stop();
        epoll_ctl(L->ep, EPOLL_CTL_DEL, g_stop_efd, NULL);
        L->stop_in = 0;
        __atomic_sub_fetch(&g_stop_loops, 1, __ATOMIC_ACQ_REL);
    }

    if (tun_ready) {
        /* DRAIN the device rather than read one packet per turn. During a download the
         * client's ACKs come from TUN, and they open the window; one per turn tied the speed
         * to the turn rate (measured: 1900 turns/s x ~2920 bytes per ACK = 5.5 MB/s, exactly
         * the ceiling seen). The device is non-blocking, so "nothing more" comes as EAGAIN.
         * The per-turn limit keeps one client's packets from starving the server reads. */
        int k;
        for (k = 0; k < TUN_DRAIN_MAX; k++) {
            uint64_t t0 = g_stats ? now_ns() : 0;
            ssize_t rn = tun_read_packet(tun, L->pkt, sizeof(L->pkt));
            uint64_t t1 = g_stats ? now_ns() : 0;
            if (rn <= 0) break;
            if (g_stats) { g_st.tun_reads++; g_st.tun_read_ns += t1 - t0; }
            handle_packet(tun, L->pkt, (size_t)rn);
            if (g_stats) g_st.pkt_ns += now_ns() - t1;
        }
        /* Before the ACKs of this batch, so they already carry the window it calls for. */
        g_tun_backlog = k == TUN_DRAIN_MAX;
        up_flush(tun);
        mux_pass(MUXP_LATE);
        flush_acks(tun);
    }
    if (lst && g_lst_fd >= 0 && !L->lst_off) lst_accept(L);
    if (ctl) ctl_pass(L);
    if (ibw && L->ibw >= 0) ib_apply(L);
    int out_woke = 0;
    for (int i = 0; i < r; i++) {
        uint32_t t = L->evs[i].data.u32;
        if (t >= CFD_BASE && t < CFD_BASE + MAX_CONNS) {
            struct conn *c = &g_conns[t - CFD_BASE];
            if (c->used && c->sock && !c->pending && c->born_turn != g_turn)
                sock_event(c, L->evs[i].events, tun);
            continue;
        }
        if (t >= IB_LST_BASE && t < IB_LST_BASE + IB_MAX) {
            ib_accept(L, (int)(t - IB_LST_BASE));
            continue;
        }
        if (t >= IB_HS_BASE && t < IB_HS_BASE + IB_HS_MAX) {
            ib_hs_event(L, &g_hs[t - IB_HS_BASE]);
            continue;
        }
        if (t >= IB_CTL_BASE && t < IB_CTL_BASE + IB_ASSOC_MAX) {
            ib_ctl_event(&g_assoc[t - IB_CTL_BASE]);
            continue;
        }
        if (t >= IB_UDP_BASE && t < IB_UDP_BASE + IB_ASSOC_MAX) {
            ib_udp_event(L, &g_assoc[t - IB_UDP_BASE]);
            continue;
        }
        if (t >= XC_BASE && t < XC_BASE + MUX_IDX_MAX) {
            if (g_dl->ops->mux) g_dl->ops->mux->event(t - XC_BASE, L->evs[i].events);
            continue;
        }
        if (t >= (uint32_t)MAX_CONNS) continue;  /* TUN and eventfd */
        struct conn *c = &g_conns[t];
        /* The slot may have been freed in this same turn (an RST from the client, a failed
         * send, an eviction) and even given to a new connection: conn_new hands out the most
         * recently freed slot first. Skip a free slot, a connection waiting for its connector
         * (its session must not be touched), and any connection born in this turn: the event
         * was its predecessor's. Without the last check a new connection that took a spare
         * session read its fresh link, which is blocking with no data yet, and stalled the
         * whole loop for up to CONNECT_TIMEOUT_S. */
        if (!c->used || c->pending || c->born_turn == g_turn) continue;
        /* The queue to the node has room: the held ACK can go (flush_acks below rechecks
         * readiness itself). Writability alone means nothing to read, so skip the read. */
        if (L->evs[i].events & EPOLLOUT) out_woke = 1;
        if (!(L->evs[i].events & ~(uint32_t)EPOLLOUT)) continue;
        /* Data already read is handled by the second pass below; skipped here so that one
         * connection does not get a double share per turn at the expense of the others. */
        if (c->rx_ready) continue;
        /* Readiness was computed BEFORE the TUN batch, which may have carried an ACK with a
         * smaller window. Then reading is not allowed: the receiver silently drops what goes
         * beyond its window, and the hole costs a retransmission. The check is cheap and does
         * not depend on whether epoll has learned of the change.
         *
         * A UDP flow has no window: asking about it would mean NEVER reading, since its
         * window is zero by definition (see client_room). */
        if (c->srv_closed || !conn_can_take(c)) continue;
        drain_conn(c, tun);
    }
    if (out_woke) flush_acks(tun);

    /* Connections with data already in the buffer: the kernel will not report them, and
     * without this pass the record would wait for new bytes from the server. Done only when
     * there are any; they were counted before the wait. */
    forced += (int)g_mux_woke;
    g_mux_woke = 0;
    for (int li = 0; forced && li < g_live_n; li++) {
        struct conn *c = &g_conns[g_live[li]];
        if (!c->rx_ready || c->pending) continue;
        if (c->srv_closed || !conn_can_take(c)) continue;
        drain_conn(c, tun);
    }
    mux_pass(MUXP_LATE);
    /* The node's room grew back (drain_conn): the window update to the client now, not with
     * the next TUN batch, which an upload stopped by a zero window will never send. */
    if (g_win_woke) {
        g_win_woke = 0;
        flush_acks(tun);
    }

    /* One pass for all deadlines: retransmission, close after the server, idle cleanup.
     * The time is refreshed: the TUN batch and the server reads since the wait may have
     * taken milliseconds. */
    g_now_ns = now_ns();
    g_now_s = (time_t)(g_now_ns / 1000000000ull);
    now = g_now_ns;
    /* A stale spare is a deadline too. Once a second, not every turn: there are thousands of
     * turns under traffic, and a spare goes stale in seconds. */
    if (now - L->spare_at >= 1000000000ull) {
        L->spare_at = now;
        spare_sweep();
    }
    nodes_sweep(&L->nodes_seen, now);
    if (L->own) {
        park_pass(tun);
        miss_report(now);
        bytes_report(now);
        ib_deadlines(now);
    }
    for (int li = 0; li < g_live_n; ) {
        struct conn *c = &g_conns[g_live[li]];
        /* A job in progress: the connector writes into the session, do not touch. */
        if (c->pending || !conn_deadlines(c, tun, now)) li++;
    }
    mux_pass(MUXP_LATE);
    /* Window updates for rooms that grew without a read of ours (room_watch). */
    if (g_win_woke) {
        g_win_woke = 0;
        flush_acks(tun);
    }
    return 0;
}

/* Upper limit for STEER_TUN_THREADS. More threads than cores would only add context switches:
 * the loop is almost always either waiting in epoll or computing. */
#define MAX_WORKERS 4

static void worker_init(int id) {
    g_worker = id;
    g_st.win_min = 0xFFFFFFFFu;
    g_st.mss_min = 0xFFFF;
    g_ip_id = (uint16_t)(id * (65536 / MAX_WORKERS));
}

static void *worker_loop(void *arg) {
    struct worker *w = arg;
    int tun_fd = w->tun.fd;
    worker_init(w->id);
    /* The thread's tables are in the heap (see "loop thread tables" at g_conns), so allocating
     * them can fail. The failure is handled like the epoll one below: the thread does not start,
     * and its queue is left to the others. */
    if (conn_table_init() != 0) {
        fprintf(stderr, LOG_W "no memory for the connection table (%s) — thread not started, "
                        "its queue is left to the others\n", strerror(errno));
        close(tun_fd);
        return NULL;
    }

    /* The queue MUST be closed on every exit from here. The kernel spreads flows over queues by
     * a flow hash scaled to numqueues, and numqueues counts ATTACHED queues, not read ones. A
     * descriptor left open keeps a share of the traffic going into a queue nobody reads: the
     * connections in it silently vanish, and the same sites (the hash is stable) never open.
     * close() detaches the queue (tun_detach in the kernel decrements numqueues), and the flows
     * spread over the live ones; checked: after closing the second of two queues all 40 flows
     * came to the first. */
    struct loop lp;
    struct loop *L = &lp;
    int rc = loop_open(L, &w->tun, w->id == 0);
    if (rc != 0) {
        fprintf(stderr, LOG_W "%s (%s) — thread not started, its queue is left to the others\n",
                rc == -1 ? "epoll unavailable" : "cannot add TUN to epoll", strerror(errno));
        conn_table_free();
        close(tun_fd);
        return NULL;
    }
    w->served = 1;      /* the queue is in epoll: from now on it carries traffic */

    while (loop_turn(L, 1000) == 0) {}
    /* Release the connectors first, and only then close the sessions; otherwise we close what
     * they work with (see connq_release). The eventfd too: a connector writes to it when done,
     * and a number closed too early could already belong to someone else. */
    if (w->id == 0)
        while (g_park_n) sock_rst(park_take(g_park_n - 1).fd);
    if (connq_release(g_conns) == 0) {
        while (g_live_n) conn_drop(&g_conns[g_live[0]]);
        mux_pass(MUXP_EXIT);
        conn_table_free();
    }
    loop_close(L);
    close(tun_fd);
    return NULL;
}

/* How many loop threads to start, and why ONE by default. A thread per core looks like an
 * obvious win, but it assumes both halves of a TCP connection land in the same device queue,
 * which the kernel does not promise (see g_conns): the owner then never sees the client's ACKs
 * and stops reading from the server. On a router that gave 12 Mbit/s against 292 with one queue,
 * and a hung box under speedtest.
 *
 * So more than one thread is an explicit choice (STEER_TUN_THREADS), sensible only where flows
 * are steered to queues by something else; it also lets a speedup be rechecked against one
 * thread on the same hardware. */
static int worker_count(void) {
    const char *env = getenv("STEER_TUN_THREADS");
    if (env) {
        int n = atoi(env);
        if (n >= 1) return n < MAX_WORKERS ? n : MAX_WORKERS;
    }
    return 1;
}

time_t stack_now_s(void) { return g_now_s; }

/* Session stride and the spare pool, by the dialer's session size. Before any thread starts:
 * after that g_dl, g_sess_stride and g_spares[].sess are only read. */
static void stack_setup(const struct dialer *d) {
    g_dl = d;
    g_rcv_wnd_max = d->ops->rcv_wnd_max;
    g_sess_stride = (d->ops->sess_size + SESS_ALIGN - 1) & ~(size_t)(SESS_ALIGN - 1);
    if (!(d->ops->caps & DC_PRECONNECT)) g_spare_want = 0;
    if (g_spare_want <= 0) return;
    /* The whole pool in one block: SPARE_MAX sessions, not g_spare_want, since the g_spares
     * slots do not depend on the count. A large calloc does not touch the pages of slots that
     * are never filled. */
    unsigned char *m = calloc(SPARE_MAX, g_sess_stride);
    if (!m) {
        fprintf(stderr, LOG_W "no memory for spare sessions — every SYN pays "
                        "for a handshake\n");
        g_spare_want = 0;
        return;
    }
    for (int i = 0; i < SPARE_MAX; i++) g_spares[i].sess = m + (size_t)i * g_sess_stride;
}

int stack_run(const struct tun_cfg *tc, const struct dialer *d, stack_ready_fn ready, void *arg) {
    const char *dev = tc->dev;
    g_trace = getenv("STEER_TUN_TRACE") != NULL;
    g_stats = getenv("STEER_TUN_STATS") != NULL;

    /* Read HERE, before the threads start: after that the field is only read, and getenv does
     * not belong on the packet path. Zero disables the pool: every SYN pays for a handshake (the
     * SYN-ACK still goes at once). */
    const char *sp = getenv("STEER_TUN_SPARES");
    g_spare_want = sp ? atoi(sp) : 4;
    if (g_spare_want < 0) g_spare_want = 0;
    if (g_spare_want > SPARE_MAX) g_spare_want = SPARE_MAX;
    rcv_window_init();
    stack_setup(d);

    /* A node that is certainly unusable (for VLESS, an invalid UUID) is rejected by the protocol
     * module BEFORE this function, while it can still be named: here the device comes up. */

    static struct worker workers[MAX_WORKERS];
    struct tun_dev queues[MAX_WORKERS];
    int want = worker_count();
    int n = tun_open(queues, want, dev);
    g_one_worker = n == 1;
    g_loops = n > 0 ? (unsigned)n : 1;
    /* Failure is 1, never a negative code: it becomes the process exit code, where -40 would read
     * as a meaningless 216. tun_open has already logged the reason. */
    if (n < 0) return 1;

    for (int i = 0; i < n; i++) {
        /* Non-blocking reads: the loop drains the device until EAGAIN, and without this flag
         * the last read would sleep and stall every connection. */
        int fl = fcntl(queues[i].fd, F_GETFL, 0);
        if (fl >= 0) fcntl(queues[i].fd, F_SETFL, fl | O_NONBLOCK);
        workers[i].tun = queues[i];
        workers[i].id = i;
    }
    ifcfg_bring_up(dev, tc->addr, tc->prefix);
    park_cap_init();
    if (tc->tcp_port) lst_open(tc->tcp_port);

    /* The device is up and has its address: the caller adds its routes into it now (tunvless:
     * --route), before the loop threads start — first the device, then the routes into it. */
    if (ready) ready(arg, dev);
    char desc[512];
    d->ops->describe(d->ctx, desc, sizeof(desc));
    fprintf(stderr, LOG_I "%s -> %s\n", dev, desc);
    /* Always printed: without offload the speed drops several times, and what we got (offload,
     * threads) must be known before measuring, not after. */
    fprintf(stderr, LOG_I "write offload on %s %s; threads %d of %d requested\n",
            dev, queues[0].gso ? "enabled (segments up to 16 KB, checksums by the kernel)"
                               : "UNAVAILABLE — segments of the client's MSS, checksums computed here",
            n, want);
    fprintf(stderr, LOG_I "spare sessions to the node: %d (STEER_TUN_SPARES)\n", g_spare_want);
    if (n < want)
        fprintf(stderr, LOG_W "fewer queues than requested — the kernel lacks "
                        "IFF_MULTI_QUEUE or gave no more; one thread per queue\n");

    /* Threads two to last are separate; the first runs in this one. So the process stays what it
     * is for its supervisor: when the loop ends, the process ends. */
    for (int i = 0; i < n; i++) workers[i].served = 0;
    pthread_t tids[MAX_WORKERS];
    int started = 0;
    for (int i = 1; i < n; i++) {
        if (pthread_create(&tids[started], NULL, worker_loop, &workers[i]) != 0) {
            fprintf(stderr, LOG_W "thread %d could not be created, running without it\n", i);
            close(workers[i].tun.fd);
            continue;
        }
        started++;
    }
    worker_loop(&workers[0]);
    for (int i = 0; i < started; i++) pthread_join(tids[i], NULL);

    /* We get here ONLY when all queues have ended, and they end only on an error: worker_loop has
     * no successful exit. A normal stop is a signal, and the process never returns here. So zero
     * is always wrong here: it would report success while the tunnel carries no traffic. */
    int served = 0;
    for (int i = 0; i < n; i++) served += workers[i].served;
    if (!served)
        fprintf(stderr, LOG_E "%s did not come up on any of its %d queues\n", dev, n);
    else
        fprintf(stderr, LOG_E "%s no longer carries traffic — all its queues (%d) "
                        "have ended\n", dev, n);
    return 1;
}

