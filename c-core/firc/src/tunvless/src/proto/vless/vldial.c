/* VLESS dialer: how the tunnel stack (stack.c) carries client flows to a VLESS node. The stack
 * sees only the vless_dialer table.
 *
 * The session is struct vl_sess (vldial.h): the flow state (header, Vision, datagram assembly)
 * and the link to the node (struct transport). The link can be set up in advance: VLESS sends
 * the destination in the request header with the first data, and until then the link belongs
 * to nobody (DC_PRECONNECT). So the stack keeps spare links, and take moves only the link out
 * of a spare.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <poll.h>
#include <pthread.h>
#include "osrand.h"

#include "vless.h"
#include "vless_proto.h"
#include "vision.h"
#include "client.h"
#include "vldial.h"
#include "stack.h"
#include "pool.h"
#include "ratelog.h"
#include "sublink.h"
#include "trxmux.h"

#define LOG_W  "tunvless[warn] tunnel: "
#define VL_UP_WRITE 8192
#define VL_GRPC_FRAME 9

/* The stack's buffer must hold a whole transport record plus room for the VLESS header and
 * Vision padding. TUNNEL_BUF (dialer.h) is a plain number; it is checked here. */
_Static_assert(TUNNEL_BUF >= VLESS_MIN_RECV_CAP + 2048,
               "TUNNEL_BUF is smaller than a transport record plus room for VLESS and Vision");

static int g_trace;
#define TR(...) do { if (g_trace) fprintf(stderr, "tunvless[debug] tun: " __VA_ARGS__); } while (0)

void vl_set_trace(int on) { g_trace = on; }

/* ---- node ------------------------------------------------------------------------------ */

static const char *vl_peer(const void *ctx) {
    const struct vless_node *node = ctx;
    return node->host;
}

static void vl_describe(const void *ctx, char *out, size_t n) {
    const struct vless_node *node = ctx;
    const char *pr = (node->proto_str[0] && strcmp(node->proto_str, "vless")) ? node->proto_str : (node->proto == VLESS_PROTO_HY2 ? "hy2" : node->type);
    snprintf(out, n, "%s (%s:%u %s%s)", node->name, node->host, node->port, pr,
             node->flow[0] ? " +vision" : "");
}

/* ---- link ------------------------------------------------------------------------------ */

static int vl_connect(const void *ctx, void *sess, int timeout_s) {
    struct vl_sess *s = sess;
    int rc = vless_connect(ctx, &s->t, timeout_s);
    if (!rc) transport_set_loop(&s->t);
    else {
        s->why_rc = rc;
        snprintf(s->why, sizeof s->why, "%s", vless_strerror(rc));
    }
    return rc;
}

static const char *vl_why(const void *sess, int rc) {
    const struct vl_sess *s = sess;
    return rc && s->why_rc == rc ? s->why : vless_strerror(rc);
}

/* Moves the link from a spare session into the connection's session. Only the part of struct
 * transport its kind uses is copied (transport_used): the UUID and Vision state in dst are already set up (flow_open), and overwriting them
 * would lose the flow.
 *
 * gRPC's h2 keeps a pointer to its own link (io.ctx), which after the move points into the
 * abandoned spare slot. transport_moved fixes it, not the dialer. xhttp keeps its connections on
 * the heap and needs nothing. */
static void vl_take(void *dst, void *src) {
    struct vl_sess *d = dst;
    struct vl_sess *s = src;
    memcpy(&d->t, &s->t, transport_used(&s->t));
    transport_moved(&d->t);
}

static void vl_close(void *sess) {
    struct vl_sess *s = sess;
    transport_close(&s->t);
}

/* Clears only what the dialer itself reads. A memset of the transport (about 21 KB) on every
 * close would pull 21 KB of zeros through the cache, and transport_open overwrites them anyway
 * (it starts with a memset of what its kind uses). Likewise only the datagram counters are reset, not
 * the 4 KB buffer. Everything touched here lies at the start of the session (field order in
 * vldial.h). */
static void vl_clear(void *sess) {
    struct vl_sess *s = sess;
    s->header_sent = 0;
    s->established = 0;
    if (!(s->t.fr && s->t.fr->dial)) s->t.link.fd = -1;
    memset(&s->vis, 0, sizeof(s->vis));
    s->dg_want = s->dg_have = 0;
    s->dg_skip = 0;
    s->lenb_n = 0;
    s->xs = 0;
    s->xdiscard = 0;
}

static int vl_fd(const void *sess) {
    const struct vl_sess *s = sess;
    return transport_fd(&s->t);
}

static int vl_has_data(const void *sess) {
    const struct vl_sess *s = sess;
    return transport_has_data(&s->t);
}

/* ---- flow ------------------------------------------------------------------------------ */

/* The node's id does not parse, and the connection is closed. Say why: from outside a silent
 * close looks like "no traffic". It cannot happen today (a node with a bad UUID is dropped when
 * the nodes are parsed, and vless_tunnel_run checks the first node's, when it is VLESS, before
 * the device comes up), so the line is rate limited rather than per packet. The UUID is not printed: it is the
 * key to the node. */
static void node_id_refused(const struct vless_node *node, const char *what) {
    static __thread time_t said;
    time_t now = stack_now_s();
    if (now - said < 5) return;
    said = now;
    fprintf(stderr, LOG_W "the UUID of node %s does not parse — %s refused; "
                    "check the node's link\n", node->name, what);
}

static int vl_set_host(void *sess, const char *host) {
    struct vl_sess *s = sess;
    size_t n = host ? strlen(host) : 0;
    if (n >= sizeof s->host) return -1;
    memcpy(s->host, host ? host : "", n + 1);
    return 0;
}

static int vl_flow_open(const void *ctx, void *sess, const struct flow_key *k, int udp) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    s->header_sent = 0;
    s->v6 = k->v6;
    s->established = 0;
    s->host[0] = 0;
    if (node->proto != VLESS_PROTO_HY2 && (!node->proto_str[0] || !strcmp(node->proto_str, "vless"))) {
        if (vless_uuid_parse(node->uuid, s->uuid) != 0) {
            node_id_refused(node, udp ? "UDP connection" : "TCP connection");
            return -1;
        }
        /* For UDP, Vision is set up only for a node with flow: there UDP goes as a Mux command with
         * XUDP frames over the Vision stream (vl_send). Without flow the UDP request declares none,
         * and there are no Vision frames in either direction. */
        if (!udp || node->flow[0]) vision_init(&s->vis, s->uuid);
    }
    return 0;
}

/* ---- XUDP: UDP over Vision (Mux.Cool, as the Xray client does) ----------------------------
 *
 * A UDP request (command 2) to an account with flow=xtls-rprx-vision does not work everywhere:
 * Xray-core refuses it with the vision flow ("doesn't support UDP") and accepts it only with an
 * empty flow, while sing-box checks the flow against the account for any command and rejects
 * both an empty flow and vision. Xray's own client does it differently
 * (proxy/vless/outbound/outbound.go: with vision the UDP command becomes Mux with the
 * v1.mux.cool:666 service): a Mux.Cool stream of XUDP frames (common/xudp), the whole stream
 * wrapped in Vision as for TCP. So does this code, for nodes with flow. Nodes without flow stay
 * on command 2: it is simpler, shorter on the wire and accepted by every server.
 *
 * Frame (the general Mux.Cool layout, common/mux/frame.go):
 *   [metadata length u16][session id u16 = 0][status u8][options u8][...]
 *   [data length u16][data]
 *   status: 1 New, 2 Keep, 3 End, 4 KeepAlive. Options: bit 0 has data, bit 1 error.
 * The first datagram of a flow is New: after the options come the network (2 = UDP), port,
 * address type and destination address, then the GlobalID (8 bytes; by it an Xray server
 * returns the same UDP socket for a new stream, "full cone"). Then Keep without an address: the
 * destination does not change, and for Keep without an address the server uses New's
 * destination (sing-box: metadata length 4, no address). One flow, one destination, as with
 * command 2: a single mux session, no multiplexing of destinations.
 *
 * Not implemented: several sessions in one stream, and Mux for TCP (concurrency). For TCP they
 * only save handshakes, and the stack has spare links for that. */
enum { XS_LEN = 0, XS_META, XS_DLEN, XS_DATA, XS_SKIP };

/* GlobalID: an opaque 8-byte key for "this source", by which the server picks the socket. Made
 * from the client's source and a random per-process key: the same for all flows of one source,
 * different between sources, unpredictable from outside. It needs no cryptographic strength:
 * it is not a secret, only a key in the server's table. */
static unsigned char g_xudp_key[16];
static pthread_once_t g_xudp_once = PTHREAD_ONCE_INIT;
static void xudp_key_init(void) {
    if (os_getrandom(g_xudp_key, sizeof g_xudp_key, 0) != (ssize_t)sizeof g_xudp_key) {
        struct timespec t;
        clock_gettime(CLOCK_REALTIME, &t);
        memcpy(g_xudp_key, &t, sizeof t < sizeof g_xudp_key ? sizeof t : sizeof g_xudp_key);
    }
}

static void xudp_gid(const struct flow_key *k, unsigned char gid[8]) {
    pthread_once(&g_xudp_once, xudp_key_init);
    uint64_t h = 1469598103934665603ULL;               /* FNV-1a, 64 bits */
    unsigned char in[16 + 16 + 2];
    size_t al = k->v6 ? 16 : 4, n = 16 + al + 2;
    memcpy(in, g_xudp_key, 16);
    memcpy(in + 16, k->src.b, al);
    in[16 + al] = (unsigned char)(k->sport >> 8);
    in[17 + al] = (unsigned char)k->sport;
    for (size_t i = 0; i < n; i++) { h ^= in[i]; h *= 1099511628211ULL; }
    h ^= h >> 32;
    for (int i = 0; i < 8; i++) gid[i] = (unsigned char)(h >> (56 - 8 * i));
}

/* Datagrams [length u16][data] (as dgram_frame frames them) to XUDP frames. first: the flow's
 * first frame (New with the address); the rest are Keep. Returns the result length, 0 if it does
 * not fit or the input is malformed. */
static size_t xudp_frames(const struct flow_key *k, int first, const unsigned char *in, size_t n,
                          unsigned char *out, size_t cap) {
    size_t o = 0;
    while (n) {
        if (n < 2) return 0;
        size_t dl = ((size_t)in[0] << 8) | in[1];
        in += 2;
        n -= 2;
        if (dl > n) return 0;
        unsigned char meta[40];
        size_t m = 0;
        meta[m++] = 0; meta[m++] = 0;                  /* session id: a single one, 0 */
        if (first) {
            meta[m++] = 1;                             /* New */
            meta[m++] = 1;                             /* options: has data */
            meta[m++] = 2;                             /* network: UDP */
            meta[m++] = (unsigned char)(k->dport >> 8);
            meta[m++] = (unsigned char)k->dport;       /* port, then address type and address */
            meta[m++] = k->v6 ? VLESS_ADDR_IPV6 : VLESS_ADDR_IPV4;
            memcpy(meta + m, k->dst.b, k->v6 ? 16 : 4);
            m += k->v6 ? 16 : 4;
            xudp_gid(k, meta + m);
            m += 8;
            first = 0;
        } else {
            meta[m++] = 2;                             /* Keep */
            meta[m++] = 1;
        }
        if (o + 2 + m + 2 + dl > cap) return 0;
        out[o++] = (unsigned char)(m >> 8);
        out[o++] = (unsigned char)m;
        memcpy(out + o, meta, m);
        o += m;
        out[o++] = (unsigned char)(dl >> 8);
        out[o++] = (unsigned char)dl;
        memcpy(out + o, in, dl);
        o += dl;
        in += dl;
        n -= dl;
    }
    return o;
}

/* Parses the stream of XUDP frames from the node (Vision already removed) and passes the
 * datagrams to the client. Streaming, like udp_downstream: XUDP frame, Vision frame and TLS
 * record boundaries do not coincide. Returns 0 or -1.
 *
 * Rules follow PacketReader in common/xudp: Keep with data is a datagram; KeepAlive is control,
 * its data (if any) is discarded; End, New and anything else end the stream. The sender address
 * in Keep is skipped: the flow's destination is fixed, and the answer reaches the client from
 * it, as with command 2. */
static int xudp_downstream(struct vl_sess *s, const unsigned char *d, size_t n,
                           dialer_emit_fn emit, void *arg) {
    while (n) {
        switch (s->xs) {
        case XS_LEN:
        case XS_DLEN: {
            unsigned v;
            if (!s->lenb_n && n >= 2) { v = (unsigned)(d[0] << 8 | d[1]); d += 2; n -= 2; }
            else if (!s->lenb_n) { s->lenb = d[0]; s->lenb_n = 1; return 0; }
            else { v = (unsigned)(s->lenb << 8 | d[0]); s->lenb_n = 0; d++; n--; }
            if (s->xs == XS_LEN) {
                if (v < 4 || v > 512) return -1;        /* Xray: < 4 ends, > 512 is malformed */
                s->xneed = (uint16_t)v;
                s->dg_have = 0;
                s->xs = XS_META;
            } else if (!v) {
                s->xs = XS_LEN;
            } else if (v > UDP_DGRAM_MAX) {
                s->dg_skip = v;                          /* drop exactly v bytes (udp_downstream) */
                s->xdiscard = 1;
                s->xs = XS_SKIP;
            } else {
                s->dg_want = (uint16_t)v;
                s->dg_have = 0;
                s->xs = XS_DATA;
            }
            break;
        }
        case XS_META: {
            size_t take = (size_t)s->xneed - s->dg_have;
            if (take > n) take = n;
            memcpy(s->dg + s->dg_have, d, take);
            s->dg_have = (uint16_t)(s->dg_have + take);
            d += take;
            n -= take;
            if (s->dg_have < s->xneed) return 0;
            unsigned status = s->dg[2], opt = s->dg[3];
            if (status != 2 && status != 4) return -1;   /* End, New, unknown: end of stream */
            if (opt & 2) return -1;                      /* the "error" option */
            s->xdiscard = status == 4;
            s->xs = (opt & 1) ? XS_DLEN : XS_LEN;
            break;
        }
        case XS_DATA: {
            size_t take = (size_t)s->dg_want - s->dg_have;
            if (take > n) take = n;
            memcpy(s->dg + s->dg_have, d, take);
            s->dg_have = (uint16_t)(s->dg_have + take);
            d += take;
            n -= take;
            if (s->dg_have < s->dg_want) return 0;
            s->xs = XS_LEN;
            if (!s->xdiscard && emit(arg, s->dg, s->dg_want) != 0) return -1;
            s->dg_want = 0;
            s->dg_have = 0;
            break;
        }
        default: {                                       /* XS_SKIP */
            uint32_t take = s->dg_skip < n ? s->dg_skip : (uint32_t)n;
            d += take;
            n -= take;
            s->dg_skip -= take;
            if (!s->dg_skip) { s->xs = XS_LEN; s->xdiscard = 0; }
            break;
        }
        }
    }
    return 0;
}

/* ---- TrustTunnel _udp2 framing --------------------------------------------------------------
 *
 * Upstream (_udp2):
 *   [total_len u32 = 27 + dlen][app_name_len u16 = 0][src_ip 16B (IPv4-mapped)][src_port u16]
 *   [dst_ip_len u8 = 4][dst_ip 4B][dst_port u16][payload]
 *
 * Downstream (_udp2):
 *   [total_len u32 = 36 + dlen][src_ip 16B][src_port u16][dst_ip 16B][dst_port u16][payload]
 */
static size_t tt_udp_frames(const struct flow_key *k, const unsigned char *in, size_t n,
                            unsigned char *out, size_t cap) {
    size_t o = 0;
    while (n) {
        if (n < 2) return 0;
        size_t dl = ((size_t)in[0] << 8) | in[1];
        in += 2;
        n -= 2;
        if (dl > n || o + 4 + 27 + dl > cap) return 0;
        uint32_t total = (uint32_t)(27 + dl);
        out[o++] = (unsigned char)(total >> 24);
        out[o++] = (unsigned char)(total >> 16);
        out[o++] = (unsigned char)(total >> 8);
        out[o++] = (unsigned char)total;
        out[o++] = 0; out[o++] = 0;                     /* app_name_len = 0 */
        memset(out + o, 0, 10); o += 10;                /* IPv4-mapped src_ip ::ffff:a.b.c.d */
        out[o++] = 0xFF; out[o++] = 0xFF;
        memcpy(out + o, &k->src, 4); o += 4;
        out[o++] = (unsigned char)(k->sport >> 8);
        out[o++] = (unsigned char)k->sport;
        out[o++] = 4;                                   /* dst_ip_len = 4 (IPv4) */
        memcpy(out + o, &k->dst, 4); o += 4;
        out[o++] = (unsigned char)(k->dport >> 8);
        out[o++] = (unsigned char)k->dport;
        memcpy(out + o, in, dl);
        o += dl;
        in += dl;
        n -= dl;
    }
    return o;
}

static int tt_udp_downstream(struct vl_sess *s, const unsigned char *d, size_t n,
                             dialer_emit_fn emit, void *arg) {
    while (n) {
        switch (s->xs) {
        case XS_LEN: {
            while (s->lenb_n < 4 && n) {
                s->dg[s->lenb_n++] = *d++;
                n--;
            }
            if (s->lenb_n < 4) return 0;
            s->lenb_n = 0;
            uint32_t total = ((uint32_t)s->dg[0] << 24) | ((uint32_t)s->dg[1] << 16) |
                             ((uint32_t)s->dg[2] << 8) | s->dg[3];
            if (total < 36 || total > 36 + 65535) return -1;
            s->xneed = 36;
            s->dg_want = (uint16_t)(total - 36);
            s->dg_have = 0;
            s->xs = XS_META;
            break;
        }
        case XS_META: {
            size_t take = (size_t)s->xneed - s->dg_have;
            if (take > n) take = n;
            s->dg_have = (uint16_t)(s->dg_have + take);
            d += take;
            n -= take;
            if (s->dg_have < s->xneed) return 0;
            s->dg_have = 0;
            if (!s->dg_want) {
                s->xs = XS_LEN;
            } else if (s->dg_want > UDP_DGRAM_MAX) {
                s->dg_skip = s->dg_want;
                s->dg_want = 0;
                s->xs = XS_SKIP;
            } else {
                s->xs = XS_DATA;
            }
            break;
        }
        case XS_DATA: {
            size_t take = (size_t)s->dg_want - s->dg_have;
            if (take > n) take = n;
            memcpy(s->dg + s->dg_have, d, take);
            s->dg_have = (uint16_t)(s->dg_have + take);
            d += take;
            n -= take;
            if (s->dg_have < s->dg_want) return 0;
            s->xs = XS_LEN;
            if (emit(arg, s->dg, s->dg_want) != 0) return -1;
            s->dg_want = 0;
            s->dg_have = 0;
            break;
        }
        default: {
            uint32_t take = s->dg_skip < n ? s->dg_skip : (uint32_t)n;
            d += take;
            n -= take;
            s->dg_skip -= take;
            if (!s->dg_skip) s->xs = XS_LEN;
            break;
        }
        }
    }
    return 0;
}

/* ---- Trojan UDP framing ---------------------------------------------------------------------
 *
 * Frame layout (both upstream and downstream):
 *   [ATYP u8][DST.ADDR][DST.PORT u16][Length u16][CRLF 0x0d 0x0a][Payload]
 */
static size_t trojan_udp_frames(const struct flow_key *k, const unsigned char *in, size_t n,
                                unsigned char *out, size_t cap) {
    size_t o = 0;
    while (n) {
        if (n < 2) return 0;
        size_t dl = ((size_t)in[0] << 8) | in[1];
        in += 2;
        n -= 2;
        if (dl > n || o + 11 + dl > cap) return 0;
        out[o++] = 0x01;                                /* IPv4 */
        memcpy(out + o, &k->dst, 4); o += 4;
        out[o++] = (unsigned char)(k->dport >> 8);
        out[o++] = (unsigned char)k->dport;
        out[o++] = (unsigned char)(dl >> 8);
        out[o++] = (unsigned char)dl;
        out[o++] = '\r';
        out[o++] = '\n';
        memcpy(out + o, in, dl);
        o += dl;
        in += dl;
        n -= dl;
    }
    return o;
}

static int trojan_udp_downstream(struct vl_sess *s, const unsigned char *d, size_t n,
                                 dialer_emit_fn emit, void *arg) {
    while (n) {
        switch (s->xs) {
        case XS_LEN: {
            if (!s->lenb_n) {
                s->dg[s->lenb_n++] = *d++;
                n--;
                if (s->dg[0] == 0x01) s->xneed = 11;
                else if (s->dg[0] == 0x04) s->xneed = 23;
                else if (s->dg[0] == 0x03) s->xneed = 2;
                else return -1;
            }
            if (s->dg[0] == 0x03 && s->lenb_n == 1 && n) {
                s->dg[s->lenb_n++] = *d++;
                n--;
                s->xneed = (uint16_t)(2 + s->dg[1] + 6);
            }
            while (s->lenb_n < s->xneed && n) {
                s->dg[s->lenb_n++] = *d++;
                n--;
            }
            if (s->lenb_n < s->xneed) return 0;
            uint16_t plen = ((uint16_t)s->dg[s->xneed - 4] << 8) | s->dg[s->xneed - 3];
            s->lenb_n = 0;
            s->dg_have = 0;
            if (!plen) {
                s->xs = XS_LEN;
            } else if (plen > UDP_DGRAM_MAX) {
                s->dg_skip = plen;
                s->dg_want = 0;
                s->xs = XS_SKIP;
            } else {
                s->dg_want = plen;
                s->xs = XS_DATA;
            }
            break;
        }
        case XS_DATA: {
            size_t take = (size_t)s->dg_want - s->dg_have;
            if (take > n) take = n;
            memcpy(s->dg + s->dg_have, d, take);
            s->dg_have = (uint16_t)(s->dg_have + take);
            d += take;
            n -= take;
            if (s->dg_have < s->dg_want) return 0;
            s->xs = XS_LEN;
            if (emit(arg, s->dg, s->dg_want) != 0) return -1;
            s->dg_want = 0;
            s->dg_have = 0;
            break;
        }
        default: {
            uint32_t take = s->dg_skip < n ? s->dg_skip : (uint32_t)n;
            d += take;
            n -= take;
            s->dg_skip -= take;
            if (!s->dg_skip) s->xs = XS_LEN;
            break;
        }
        }
    }
    return 0;
}

/* Sends data to the node in the right form: with the VLESS header before the first data, and
 * wrapped in Vision if the node requires it. */
static int vl_send(const void *ctx, void *sess, const struct flow_key *k, int udp,
                   const unsigned char *data, size_t n) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    /* out is needed only to join the header or the Vision frames and the data into one buffer.
     * Usually there is nothing to join (the header goes once per connection, and Vision pads
     * only the first few writes), and the data goes straight from the packet, uncopied. */
    unsigned char out[TUNNEL_BUF];
    const unsigned char *body = data;
    size_t len = 0;
    const int is_tt = !strcmp(node->proto_str, "tt");
    const int is_naive = !strcmp(node->proto_str, "naive");
    const int is_socks = !strcmp(node->proto_str, "socks5");
    const int is_trojan = !strcmp(node->proto_str, "trojan");
    const int is_ss = !strcmp(node->proto_str, "ss");
    const int is_vmess = !strcmp(node->proto_str, "vmess");
    const int is_mieru = !strcmp(node->proto_str, "mieru");
    const int is_anytls = !strcmp(node->proto_str, "anytls");

    unsigned char xb[TUNNEL_BUF];
    if (udp && !strcmp(node->mode, "finiteproto")) return SEND_FATAL;

    if (is_tt || is_naive) {
        if (udp) {
            if (is_naive) return SEND_FATAL;
            size_t xn = tt_udp_frames(k, data, n, xb, sizeof xb);
            if (!xn) return SEND_FATAL;
            data = xb;
            n = xn;
            body = xb;
            if (!s->header_sent) {
                len = tt_build_connect(node->uuid, "_udp2", "udp", 0, out, sizeof(out));
                if (!len) return SEND_FATAL;
            }
        } else if (!s->header_sent) {
            unsigned char ip4[4];
            memcpy(ip4, k->dst.b, 4);
            char auth[280];
            if (s->host[0])
                snprintf(auth, sizeof(auth), "%s:%u", s->host, k->dport);
            else
                snprintf(auth, sizeof(auth), "%u.%u.%u.%u:%u",
                         ip4[0], ip4[1], ip4[2], ip4[3], k->dport);
            len = tt_build_connect(node->uuid, auth, is_naive ? "Chrome/128.0.0.0" : "tcp", 0, out, sizeof(out));
            if (!len) return SEND_FATAL;
        }
    } else if (is_trojan) {
        unsigned char ip4[4];
        memcpy(ip4, k->dst.b, 4);
        if (udp) {
            size_t xn = trojan_udp_frames(k, data, n, xb, sizeof xb);
            if (!xn) return SEND_FATAL;
            data = xb;
            n = xn;
            body = xb;
            if (!s->header_sent) {
                len = trojan_build_request(node->uuid, 0x03, NULL, ip4, k->dport, out, sizeof(out));
                if (!len) return SEND_FATAL;
            }
        } else if (!s->header_sent) {
            len = trojan_build_request(node->uuid, 0x01, s->host[0] ? s->host : NULL, ip4, k->dport, out, sizeof(out));
            if (!len) return SEND_FATAL;
        }
    } else if (is_ss || is_vmess || is_anytls) {
        if (udp && is_ss) return SEND_FATAL;
        if (udp && is_vmess) {
            if (n < 2 || (((size_t)data[0] << 8) | data[1]) + 2 != n) return SEND_FATAL;
            data += 2;
            n -= 2;
            body = data;
        }
        /* Shadowsocks AEAD / SS-2022, VMess AEAD, AnyTLS, or Hysteria2: prepend the target
         * address header before the first payload bytes. */
        if (!s->header_sent) {
            unsigned char ip4[4];
            memcpy(ip4, k->dst.b, 4);
            if (udp && is_anytls) {
                static const unsigned char uot_magic[28] = {
                    0x03, 23,
                    's','p','.','v','2','.','u','d','p','-','o','v','e','r','-','t','c','p','.','a','r','p','a',
                    0x00, 0x00, 0x01
                };
                memcpy(out, uot_magic, 28);
                size_t al = ss_build_addr(s->host[0] ? s->host : NULL, ip4, k->dport, out + 28, sizeof(out) - 28);
                if (!al) return SEND_FATAL;
                len = 28 + al;
            } else {
                len = ss_build_addr(s->host[0] ? s->host : NULL, ip4, k->dport, out, sizeof(out));
                if (!len) return SEND_FATAL;
                if (udp && is_vmess) out[0] |= 0x80u;
            }
        }
    } else if (is_socks || is_mieru) {
        if (udp) return SEND_FATAL;
        if (!s->header_sent) {
            unsigned char ip4[4];
            memcpy(ip4, k->dst.b, 4);
            size_t clen = socks5_build_connect(s->host[0] ? s->host : NULL, ip4, k->dport, out, sizeof(out));
            if (!clen) return SEND_FATAL;
            if (is_socks) {
                /* RFC 1928 §4/§6: wait for the SOCKS5 CONNECT reply before sending payload so
                 * the server's handshake read buffer never consumes pipelined stream bytes. */
                if (transport_write(&s->t, out, clen) != 0) return SEND_FATAL;
                unsigned char rep[264];
                size_t rhave = 0, skip = 0;
                for (int tries = 0; tries < 25; tries++) {
                    size_t got = 0;
                    if (transport_read(&s->t, rep + rhave, sizeof(rep) - rhave, &got) != 0) return SEND_FATAL;
                    rhave += got;
                    int pr = socks5_parse_reply(rep, rhave, &skip);
                    if (pr == 0) break;
                    if (pr != VLESS_EAGAIN) return SEND_FATAL;
                    struct pollfd pw = { .fd = transport_fd(&s->t), .events = POLLIN, .revents = 0 };
                    poll(&pw, 1, 200);
                }
                if (!skip) return SEND_FATAL;
                s->header_sent = 1;
                s->established = 1;
                if (!n) return SEND_OK;
            } else {
                len = clen;
            }
        }
    } else {
        /* UDP to a node with flow: XUDP frames instead of length-prefixed datagrams (see XUDP above).
         * The data framed by dgram_frame is repacked into xb; from there the path is shared with
         * TCP: Vision, then the write. */
        const int xudp = udp && node->flow[0];
        if (xudp) {
            size_t xn = xudp_frames(k, !s->header_sent, data, n, xb, sizeof xb);
            if (!xn) return SEND_FATAL;
            data = xb;
            n = xn;
            body = xb;
            if (!s->header_sent) {
                /* Mux has no port and address in the header (vless_build_request): the
                 * v1.mux.cool:666 service is implied by the command. */
                len = vless_build_request(s->uuid, VLESS_CMD_MUX, NULL, NULL, 0, 0, node->flow,
                                          out, sizeof(out));
                if (!len) return SEND_FATAL;
            }
        } else if (!s->header_sent) {
            /* The destination comes from the packet: there is no name, the client has already
             * resolved it. */
            /* UDP gets here only for a node without flow (see XUDP above) and is declared with
             * command 2 and no flow: Vision (xtls-rprx-vision) is about TCP, it replaces the copying
             * of the stream after the handshake, and datagrams have no such stream.
             *
             * dport is already in host order (struct flow_key in tun.h). */
            len = vless_build_request(s->uuid, udp ? VLESS_CMD_UDP : VLESS_CMD_TCP,
                                      s->host[0] ? s->host : NULL, k->dst.b, k->v6 ? 16 : 4, k->dport,
                                      udp ? NULL : node->flow, out, sizeof(out));
            if (!len) return SEND_FATAL;
        }
    }

    /* n == 0 (the stack's call for a silent client, dialer.h): the header alone, as Xray's client
     * flushes it after 500 ms without data. Vision starts with the first real data. */
    if (!n && !len) return SEND_OK;

    /* Wrap only while Vision pads or still reads the stream: after that vision_wrap leaves the
     * data as is, and it would be copied for nothing (tls13_write copies it once more, which
     * it must: encryption is done in place in the record). So there are zero or one copies
     * before encryption.
     *
     * The Vision state is saved before wrapping and restored if the send fails. Otherwise the
     * first frame is lost for good on a closed HTTP/2 window: vision_wrap has marked the UUID
     * sent and moved its padding state, while h2_write sent nothing (it sends all or nothing).
     * The client retransmits the same packet, it goes out without the frame and the UUID, and
     * the server closes the stream. */
    struct vision vis_before = s->vis;
    int wr = VISION_PLAIN;
    size_t fn = 0;
    if (node->flow[0] && n && (s->vis.is_padding || vision_filtering(&s->vis)))
        wr = vision_wrap(&s->vis, data, n, transport_can_tx_direct(&s->t), out + len,
                         sizeof(out) - len, &fn);
    if (wr == VISION_EFIT) return SEND_FATAL;
    if (!wr) {
        len += fn;
        body = out;
    } else if (len) {
        if (len + n > sizeof(out)) return SEND_FATAL;
        if (n) memcpy(out + len, data, n);
        len += n;
        body = out;
    } else {
        len = n;
    }

    /* The transport knows the framing (tcp, grpc, xhttp), not the dialer. The header's write is
     * at most VL_UP_WRITE, the rest follows; the room is checked for both before the first. */
    size_t first = len;
    if (!udp && !s->header_sent && len > VL_UP_WRITE && !transport_whole_writes(&s->t)) {
        long room = transport_room(&s->t);
        size_t need = len + (s->t.fr == &tr_grpc ? VL_GRPC_FRAME : 0);
        if (room >= 0 && (size_t)room < need) {
            s->vis = vis_before;
            return SEND_AGAIN;
        }
        first = VL_UP_WRITE;
    }
    int rc = transport_write(&s->t, body, first);
    if (rc == H2_EWINDOW) {
        /* The HTTP/2 window is closed: the server is not keeping up. This is not a failure,
         * it is what flow control is for. Nothing was sent (h2_write sends all or nothing), so
         * it is enough not to acknowledge the packet: the client retransmits it as after a
         * loss, and by then the window has usually opened. Treating it as an error would cut
         * uploads at a random point. */
        s->vis = vis_before;                /* the frame did not leave: undo the wrapping */
        return SEND_AGAIN;
    }
    if (!rc && first < len) {
        rc = transport_write(&s->t, body + first, len - first);
        if (rc == H2_EWINDOW) return SEND_FATAL;
    }
    if (rc == H2_ESTATUS) {
        /* The xhttp server refused the upload (stream-up, packet-up): the chunk is lost, and
         * the stream after it cannot be whole. Close as on any send failure, but name the
         * reason: otherwise a node that refuses every chunk looks alive. */
        static struct ratelog said_refused;
        unsigned held;
        char more[48];
        if (ratelog_take(&said_refused, (unsigned)stack_now_s(), 10, &held))
            fprintf(stderr, LOG_W "node %s refused the data: %s — connection closed; "
                            "check the node's xhttp settings%s\n", node->name, vless_strerror(rc),
                    ratelog_more(more, sizeof more, held));
    }
    if (rc) return SEND_FATAL;
    if (s->vis.tx_switch) {
        transport_tx_direct(&s->t);
        s->vis.tx_switch = 0;
        TR("client switched to direct copy\n");
    }
    /* Mark the header sent only now: marked earlier, a retry would go without it, and the
     * server would not know where to connect. */
    s->header_sent = 1;
    return SEND_OK;
}

/* A datagram for the node: the two-byte length and the data in one piece. h2_write sends all
 * or nothing, and a datagram split over two calls could leave half-sent on a closed window: the
 * server would read the length and wait for a tail that never comes, and the next datagram
 * would land inside the previous one. */
static size_t vl_dgram_frame(const void *ctx, const void *sess, const unsigned char *p, size_t n, unsigned char *out,
                             size_t cap) {
    (void)ctx;
    (void)sess;
    if (n > UDP_DGRAM_MAX || 2 + n > cap) return 0;
    out[0] = (unsigned char)(n >> 8);
    out[1] = (unsigned char)n;
    memcpy(out + 2, p, n);
    return 2 + n;
}

/* Through the transport, not tls13_read directly: for grpc and xhttp there is HTTP/2 between
 * TLS and VLESS, and reading past it would return frames instead of data.
 *
 * The _zc variant returns a pointer to the decrypted record where no copy is needed: on plain
 * tcp the data stays in the connection's buffer. The stack's buffer is still needed for
 * transports over HTTP/2, where a frame is assembled from several records. */
static int vl_read(void *sess, unsigned char *buf, size_t cap, const unsigned char **data,
                   size_t *got) {
    struct vl_sess *s = sess;
    return transport_read_zc(&s->t, buf, cap, data, got);
}

/* Parses the stream of datagrams from the node and passes them to the client.
 *
 * The assembly state lives in the session between calls: datagram, HTTP/2 frame and TLS record
 * boundaries need not coincide, and reading on to the end of a datagram would block and stall
 * the whole loop. Returns 0 or -1. */
static int udp_downstream(struct vl_sess *s, const unsigned char *d, size_t n,
                          dialer_emit_fn emit, void *arg) {
    while (n) {
        /* An oversized datagram is discarded by exactly its length. Stopping early would read
         * its tail as the next length and desync the stream for good: one such datagram would
         * kill the connection. */
        if (s->dg_skip) {
            uint32_t take = s->dg_skip < n ? s->dg_skip : (uint32_t)n;
            d += take;
            n -= take;
            s->dg_skip -= take;
            continue;
        }
        if (!s->dg_want) {
            if (s->lenb_n) {                        /* the first length byte came earlier */
                s->dg_want = (uint16_t)((s->lenb << 8) | d[0]);
                s->lenb_n = 0;
                d++;
                n--;
            } else if (n == 1) {
                /* The record ended exactly between the two length bytes. Rare, but a lost
                 * length byte shifts the whole stream after it, not just one datagram. */
                s->lenb = d[0];
                s->lenb_n = 1;
                return 0;
            } else {
                s->dg_want = (uint16_t)((d[0] << 8) | d[1]);
                d += 2;
                n -= 2;
            }
            if (!s->dg_want) continue;              /* length 0: nothing to pass on */
            if (s->dg_want > UDP_DGRAM_MAX) {
                static struct ratelog said;
                unsigned held;
                char more[48];
                if (ratelog_take(&said, (unsigned)stack_now_s(), 10, &held))
                    fprintf(stderr, LOG_W "datagram of %u bytes exceeds the limit %d — "
                            "dropped%s\n", s->dg_want, UDP_DGRAM_MAX, ratelog_more(more, sizeof more, held));
                s->dg_skip = s->dg_want;
                s->dg_want = 0;
                continue;
            }
            s->dg_have = 0;
        }
        size_t need = (size_t)s->dg_want - s->dg_have;
        size_t take = n < need ? n : need;
        memcpy(s->dg + s->dg_have, d, take);
        s->dg_have = (uint16_t)(s->dg_have + take);
        d += take;
        n -= take;
        if (s->dg_have < s->dg_want) return 0;      /* the tail comes in a later record */

        /* A whole datagram goes to the client. The stack refreshes the connection's timestamp
         * on every datagram passed; otherwise a receive-only flow would be evicted as idle while
         * it works. */
        if (emit(arg, s->dg, s->dg_want) != 0) return -1;
        s->dg_want = 0;
        s->dg_have = 0;
    }
    return 0;
}

static int vl_pass(struct vl_sess *s, int udp, const unsigned char *p, size_t n,
                   dialer_emit_fn emit, void *arg) {
    return udp ? xudp_downstream(s, p, n, emit, arg) : emit(arg, p, n);
}

static int vl_pass_filtered(struct vl_sess *s, int udp, const unsigned char *p, size_t n,
                            dialer_emit_fn emit, void *arg) {
    if (vision_filtering(&s->vis)) vision_filter(&s->vis, p, n);
    return vl_pass(s, udp, p, n, emit, arg);
}

static int vl_unwrap(struct vl_sess *s, int udp, const unsigned char *cur, size_t left,
                     unsigned char *gb, size_t *gn, dialer_emit_fn emit, void *arg) {
    while (left) {
        size_t used = 0, pl_n = 0;
        const unsigned char *pl = NULL;
        int ur = vision_unwrap(&s->vis, cur, left, &used, &pl, &pl_n);
        /* An invalid frame command ends the connection; it is not a pause. The parser
         * returns EPROTO without resetting the collected header, so every later call
         * rereads the same bad frame and consumes nothing. A plain break would keep the
         * connection alive: the tunnel would read and decrypt records (the costliest work
         * on this hardware) and throw them away, while the client waits until the idle
         * eviction. */
        if (ur == VISION_EPROTO) {
            TR("invalid Vision frame: closing the connection, %zu left\n", left);
            return -1;
        }
        if (ur != 0 || (!used && !pl_n)) {
            /* A short read is not an error: the parser is streaming and collects the
             * start of the stream itself (rx_pre in vision.h). Nothing consumed and
             * nothing returned means there is no progress to make. */
            TR("frame not parsed: ur=%d, %zu left\n", ur, left);
            return 1;
        }
        cur += used;
        left -= used;
        if (!pl_n) continue;
        /* Past the filter each piece goes out at once: collecting them in one buffer would
         * copy all traffic and add a size limit. */
        if (!gb) {
            if (vl_pass(s, udp, pl, pl_n, emit, arg) != 0) return -1;
            continue;
        }
        if (*gn + pl_n > VISION_BUF) {
            if (vl_pass_filtered(s, udp, gb, *gn, emit, arg) != 0) return -1;
            *gn = 0;
        }
        memcpy(gb + *gn, pl, pl_n);
        *gn += pl_n;
    }
    return 0;
}

static int vl_deliver(const void *ctx, void *sess, int udp, const unsigned char *rx, size_t got,
                      dialer_emit_fn emit, void *arg) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    /* Parse order: first the VLESS response header, then Vision frames. The server answers
     * [version|addons_len|addons] and only then the stream in Vision frames: the response
     * header is not wrapped, just as the request header goes before the first frame, not
     * inside it. */
    const unsigned char *cur = rx;
    size_t left = got;
    const int is_tt = !strcmp(node->proto_str, "tt");
    const int is_naive = !strcmp(node->proto_str, "naive");
    const int is_socks = !strcmp(node->proto_str, "socks5");
    const int is_trojan = !strcmp(node->proto_str, "trojan");
    const int is_ss = !strcmp(node->proto_str, "ss");
    const int is_vmess = !strcmp(node->proto_str, "vmess");
    const int is_mieru = !strcmp(node->proto_str, "mieru");
    const int is_anytls = !strcmp(node->proto_str, "anytls");

    if (udp && !strcmp(node->mode, "finiteproto")) return SEND_FATAL;

    if (!s->established) {
        if (is_tt || is_naive || is_trojan || is_ss || is_vmess || is_anytls) {
            /* No protocol-level response header on the stream. */
            s->established = 1;
        } else if (is_socks || is_mieru) {
            size_t skip = 0;
            if (socks5_parse_reply(cur, left, &skip) != 0) {
                TR("SOCKS5 reply not parsed (%zu bytes)\n", left);
                return -1;
            }
            cur += skip;
            left -= skip;
            s->established = 1;
        } else {
            size_t skip = 0;
            if (vless_parse_response(cur, left, &skip) != 0) {
                TR("VLESS response not parsed (%zu bytes)\n", left);
                return -1;
            }
            cur += skip;
            left -= skip;
            s->established = 1;
            TR("response header stripped (%zu bytes), %zu left\n", skip, left);
        }
    }

    if (is_tt && udp)
        return left ? tt_udp_downstream(s, cur, left, emit, arg) : 0;
    if (is_trojan && udp)
        return left ? trojan_udp_downstream(s, cur, left, emit, arg) : 0;
    if (is_vmess && udp)
        return left ? emit(arg, cur, left) : 0;

    /* Here the paths split: TCP is a stream in Vision frames, UDP without flow is datagrams with
     * a two-byte length and no Vision (the UDP request did not declare it). UDP to a node with
     * flow: Vision frames carrying XUDP frames. */
    if (node->flow[0]) {
        unsigned char gb[VISION_BUF];
        while (left) {
            const int filt = vision_filtering(&s->vis) && !s->vis.recv_direct;
            size_t sn = filt && left > VISION_BUF ? VISION_BUF : left, gn = 0;
            int ur = vl_unwrap(s, udp, cur, sn, filt ? gb : NULL, &gn, emit, arg);
            if (ur < 0) return -1;
            cur += sn;
            left -= sn;
            if (gn && vl_pass_filtered(s, udp, gb, gn, emit, arg) != 0) return -1;
            if (ur) break;
        }
        if (udp) return 0;
    } else if (udp) {
        return left ? udp_downstream(s, cur, left, emit, arg) : 0;
    } else if (left && emit(arg, cur, left) != 0) {
        return -1;
    }

    /* The server announced direct copy: tell the link, so the next read bypasses decryption.
     * It is set here because the command lives in Vision frames, which only this code knows.
     * Under VLESS encryption there is no direct copy: the encryption layer's records stay
     * records to the end of the connection (Xray has CanSpliceCopy = 3 there, and the server
     * never sends the command). */
    if (s->vis.recv_direct && !transport_rx_direct(&s->t) && !s->t.enc) {
        transport_direct(&s->t);
        TR("server switched to direct copy — reading the socket as is\n");
    }
    return 0;
}

/* ---- startup --------------------------------------------------------------------------- */

int vless_tunnel_run(const struct tun_cfg *tc, const struct pool_cfg *pc, stack_ready_fn ready,
                     void *arg) {
    /* The node's id is checked before the device and the threads exist, while the node can still be
     * named: past this point it is parsed per connection (vl_flow_open), and a failure there would
     * mean a tunnel that is up but closes everything. */
    const struct vless_node *node = pc->node(pc->first);
    if (node->proto != VLESS_PROTO_HY2 && (!node->proto_str[0] || !strcmp(node->proto_str, "vless"))) {
        unsigned char id[16];
        if (vless_uuid_parse(node->uuid, id) != 0) {
            fprintf(stderr, "tunvless[error]: the UUID of node %s does not parse — %s is not brought up; "
                            "check the node's link\n", node->name, tc->dev);
            return 1;
        }
    }
    g_trace = getenv("STEER_TUN_TRACE") != NULL;
    return pool_run(tc, pc, ready, arg);
}

/* What vl_send takes now (dialer_ops.room): the transport's room, or its mask queue's when that is
 * smaller, less what vl_send adds to the data — the request header until it is sent, the Vision
 * frames' headers and padding (up to about 1700 bytes on a write) while Vision pads. */
static long vl_room(const void *ctx, void *sess) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    long r = transport_room(&s->t), q = s->t.link.tls.fm ? transport_mask_room(&s->t) : -1;
    if (q >= 0 && (r < 0 || q < r)) r = q;
    if (r < 0) return -1;
    if (s->t.fr == &tr_grpc) r -= VL_GRPC_FRAME * (r / VL_UP_WRITE);
    if (!s->header_sent) {
        long hdr = (!strcmp(node->proto_str, "tt") || !strcmp(node->proto_str, "naive")) ? 384 : 64;
        r -= hdr + (s->host[0] ? 1 + (long)strlen(s->host) : 0);
    }
    if (node->flow[0] && s->vis.is_padding) r -= 2048;
    return r > 0 ? r : 0;
}

static int vl_due(void *sess, uint64_t now_ms, int *next_ms) {
    struct vl_sess *s = sess;
    if (transport_idle(&s->t)) {
        *next_ms = -2;
        return SEND_OK;
    }
    return transport_due(&s->t, now_ms, next_ms) ? SEND_FATAL : SEND_OK;
}

static size_t vl_up_write(const void *ctx, void *sess) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    if (transport_whole_writes(&s->t)) return 0;
    if (s->header_sent || node->flow[0]) return VL_UP_WRITE;
    static const unsigned char ip[16];
    unsigned char h[512];
    size_t hl = vless_build_request(s->uuid, VLESS_CMD_TCP, s->host[0] ? s->host : NULL, ip,
                                    s->v6 ? 16 : 4, 0, node->flow, h, sizeof h);
    return hl && hl < VL_UP_WRITE ? VL_UP_WRITE - hl : VL_UP_WRITE;
}

static void vl_mux_host(const struct mux_host *h) { tr_xmux_host(h, pool_ref_stale); }

static int vl_mux_attach(const void *ctx, void *sess, uint16_t owner, const struct xc_ref *ref) {
    const struct vless_node *node = ctx;
    struct vl_sess *s = sess;
    struct tr_node tn, td;
    sl_tr_nodes(node, &tn, &td);
    return tr_xmux_attach(&s->t, &tn, node, node->dl, owner, ref);
}

static int vl_mux_dial(const void *ctx, void *link, int timeout_s) {
    (void)ctx;
    return tr_xmux_dial(link, timeout_s);
}

static int vl_mux_cut(const void *sess) { return tr_xmux_cut(&((const struct vl_sess *)sess)->t); }

static int vl_mux_shares(const void *ctx) {
    struct tr_node tn, td;
    sl_tr_nodes(ctx, &tn, &td);
    return tr_xmux_shares(&tn);
}

static const struct dialer_mux vless_mux = {
    .host = vl_mux_host, .attach = vl_mux_attach, .dial = vl_mux_dial, .event = tr_xmux_event,
    .pass = tr_xmux_pass, .cut = vl_mux_cut, .busy = tr_xmux_busy, .dropped = tr_xmux_dropped,
    .shares = vl_mux_shares,
};

const struct dialer_ops vless_dialer = {
    .name = "vless",
    .caps = DC_PRECONNECT,
    .sess_size = sizeof(struct vl_sess),
    .peer = vl_peer,
    .describe = vl_describe,
    .strerror = vless_strerror,
    .why = vl_why,
    .connect = vl_connect,
    .take = vl_take,
    .close = vl_close,
    .clear = vl_clear,
    .fd = vl_fd,
    .has_data = vl_has_data,
    .flow_open = vl_flow_open,
    .send = vl_send,
    .room = vl_room,
    .due = vl_due,
    .up_write = vl_up_write,
    .dgram_frame = vl_dgram_frame,
    .read = vl_read,
    .deliver = vl_deliver,
    .set_host = vl_set_host,
    .mux = &vless_mux,
};
