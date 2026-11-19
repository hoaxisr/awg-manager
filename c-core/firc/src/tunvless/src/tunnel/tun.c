/* The TUN device: opening its queues, reading packets (with receive offload), parsing IPv4 headers
 * and building the packets sent to the client.
 *
 * The kernel hands us whole IP packets, not connections; stack.c rebuilds the connections and
 * carries each over its own VLESS flow. What is carried:
 *   TCP  — a VLESS flow per connection, with its state tracked;
 *   UDP  — a VLESS flow per address-port pair, datagrams with a 2-byte length (VLESS command 2),
 *          or XUDP frames for a node with Vision (vldial.c); QUIC and WireGuard go this way;
 *   ICMP — not carried: ping through a proxy needs emulation, which only helps diagnostics and
 *          misleads (a ping that answers does not mean the path works).
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <linux/if.h>
#include <linux/if_tun.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <poll.h>
#include <sys/uio.h>

#include "tun.h"

/* The TUN clone device. On Entware it comes from the firmware's tun kernel module, never from a
 * package. */
#define TUN_DEV  "/dev/net/tun"
#define TUN_HINT "load the tun kernel module (modprobe tun, or the firmware component that ships it)"

/* Linux 3.8. Entware's mips and mipsel SDKs carry 3.4 headers; such a kernel refuses the flag with
 * EINVAL and tun_open falls back to one queue. */
#ifndef IFF_MULTI_QUEUE
#define IFF_MULTI_QUEUE 0x0100
#endif

/* The virtio offload header that IFF_VNET_HDR adds.
 *
 * Declared here, not taken from <linux/virtio_net.h>: that pulls in virtio_types and does not
 * build with musl on every version of the kernel headers, and we need just these ten bytes.
 *
 * Fields are in HOST order, not network order: tun negotiates byte order only through
 * VIRTIO_F_VERSION_1, which it does not offer, so the kernel reads them as legacy __virtio16, in
 * native order. mips_24kc is a big-endian build target; a fixed little-endian layout would break
 * offload there alone. */
struct vnet_hdr {
    uint8_t flags;
    uint8_t gso_type;
    uint16_t hdr_len;
    uint16_t gso_size;
    uint16_t csum_start;
    uint16_t csum_offset;
};
#define VNET_HDR_LEN 10
#define VNET_F_NEEDS_CSUM 1
#define VNET_GSO_NONE     0
#define VNET_GSO_TCPV4    1
#define VNET_GSO_TCPV6    4
/* ECN comes in the top bit of the offload type. It does not change splitting, but the type must be
 * compared without it, or a coalesced frame with ECN would be rejected as unknown. */
#define VNET_GSO_ECN      0x80
/* The layout must match the kernel's byte for byte: one padding byte would shift everything, and
 * the kernel would read gso_size where csum_start is. */
typedef char vnet_hdr_size_check[sizeof(struct vnet_hdr) == VNET_HDR_LEN ? 1 : -1];

/* One attempt to open a queue with a given set of flags.
 *
 * g_open_errno keeps errno of the FIRST failure: later attempts would overwrite it, and the first
 * one tells why there is no device at all, while the last only tells why the last optional
 * feature was refused. */
static int g_open_errno;
static int g_open_stage;      /* 1 — the TUN clone device did not open, 2 — TUNSETIFF failed,
                                 3 — the kernel created the device under another name */
static char g_open_got[IFNAMSIZ];   /* the name the kernel returned, for stage 3 */

static int queue_open(const char *name, short flags) {
    int fd = open(TUN_DEV, O_RDWR);
    if (fd < 0) {
        if (!g_open_errno) { g_open_errno = errno; g_open_stage = 1; }
        return -1;
    }
    struct ifreq ifr;
    memset(&ifr, 0, sizeof(ifr));
    snprintf(ifr.ifr_name, IFNAMSIZ, "%s", name);
    ifr.ifr_flags = flags;
    if (ioctl(fd, TUNSETIFF, &ifr) < 0) {
        if (!g_open_errno) { g_open_errno = errno; g_open_stage = 2; }
        close(fd);
        return -1;
    }
    /* The kernel returns in ifr_name the name it CREATED, which need not be the one asked for: a
     * name longer than IFNAMSIZ-1 is truncated silently (xs-abcdefghijklm -> xs-abcdefghijkl).
     * Everything after this (address, link, routes) addresses the device by the asked name and
     * would hit a device that does not exist: queues open, tunnel alive, no traffic.
     *
     * Refuse rather than adopt the kernel's name: other configuration (firewall rules, routes)
     * refers to the device by the name asked for. */
    if (strncmp(ifr.ifr_name, name, IFNAMSIZ) != 0) {
        if (!g_open_errno) {
            g_open_errno = ENAMETOOLONG;
            g_open_stage = 3;
            snprintf(g_open_got, sizeof(g_open_got), "%.*s", IFNAMSIZ - 1, ifr.ifr_name);
        }
        close(fd);
        return -1;
    }
    return fd;
}

/* Ask the kernel to hand us COALESCED frames. A refusal does not fail the setup: packets then come
 * one at a time, only slower.
 *
 * TUN_F_CSUM is required: without it the kernel hands over neither coalesced frames nor partial
 * checksums, and refuses TUN_F_TSO4. No TSO6: see rx_gso in tun.h. */
static int queue_rx_gso(int fd) {
    unsigned feat = TUN_F_CSUM | TUN_F_TSO4;
    return ioctl(fd, TUNSETOFFLOAD, feat) == 0 ? 0 : -1;
}

int tun_open(struct tun_dev *d, int max_queues, const char *name) {
    /* IFF_NO_PI: no 4-byte protocol prefix. It only helps to tell address families apart on one
     * device; we parse the IP header anyway.
     *
     * IFF_VNET_HDR: lets us write a segment larger than the MTU marked "split by this much" and
     * leave the TCP checksum incomplete.
     *
     * IFF_MULTI_QUEUE: several descriptors on one device, one per thread.
     *
     * The attempts go from best to worst. A kernel without some feature must not cost the tunnel:
     * losing ACCELERATION is fine, losing the tunnel is not. Receive offload (TUNSETOFFLOAD): see
     * rx_gso in tun.h. */
    short base = IFF_TUN | IFF_NO_PI;
    /* STEER_TUN_NOGSO forces offload off, so a speedup can be rechecked on the same hardware. */
    int want_gso = getenv("STEER_TUN_NOGSO") == NULL;
    /* Coalescing has its own switch, so a measurement compares one change at a time. */
    int want_gro = getenv("STEER_TUN_NOGRO") == NULL;
    /* Receive offload has a third switch, for the same reason. */
    int want_rx = getenv("STEER_TUN_NORXGSO") == NULL;
    if (max_queues < 1) max_queues = 1;

    static const struct { int gso, multi; } order[] = {
        { 1, 1 }, { 1, 0 }, { 0, 1 }, { 0, 0 },
    };
    for (size_t i = 0; i < sizeof(order) / sizeof(*order); i++) {
        if (order[i].gso && !want_gso) continue;
        if (order[i].multi && max_queues < 2) continue;
        short flags = base;
        if (order[i].gso) flags |= IFF_VNET_HDR;
        if (order[i].multi) flags |= IFF_MULTI_QUEUE;

        int fd = queue_open(name, flags);
        /* A different name has nothing to do with the flags: the next attempt gets the same
         * truncation. Stop now instead of creating and removing the device four times. */
        if (fd < 0 && g_open_stage == 3) break;
        if (fd < 0) continue;
        /* Zero the WHOLE struct, not just the known fields. Callers declare the queue array
         * without initialisation (`struct tun_dev tq[...]` on the stack), and the rx buffer
         * pointer, the segment counters and the pending single-packet length must start at zero.
         * Stack garbage there sends the parser through a random pointer, and whether it crashes
         * depends on compiler flags (at -O0 the garbage tends to be zeros; with LTO it is not). */
        memset(&d[0], 0, sizeof(d[0]));
        d[0].fd = fd;
        d[0].gso = order[i].gso;
        d[0].gro = order[i].gso && want_gro;
        d[0].rx_gso = order[i].gso && want_rx && queue_rx_gso(fd) == 0;

        int n = 1;
        if (order[i].multi) {
            /* The other queues, with the same flags. Take as many as the kernel gives: a failure
             * on the fifth queue is no reason to give up the four already open. */
            while (n < max_queues) {
                int extra = queue_open(name, flags);
                if (extra < 0) break;
                memset(&d[n], 0, sizeof(d[n]));
                d[n].fd = extra;
                d[n].gso = order[i].gso;
                d[n].gro = order[i].gso && want_gro;
                d[n].rx_gso = order[i].gso && want_rx && queue_rx_gso(extra) == 0;
                n++;
            }
        }
        return n;
    }
    /* No set of flags worked. Log why: the caller only gets a code. */
    if (g_open_stage == 1 && (g_open_errno == ENOENT || g_open_errno == ENXIO ||
                              g_open_errno == ENODEV)) {
        fprintf(stderr, "tunvless[error] tunnel: no %s (%s) — %s\n", TUN_DEV,
                strerror(g_open_errno), TUN_HINT);
        return TUN_ENODEV;
    }
    if (g_open_stage == 3) {
        fprintf(stderr, "tunvless[error] tunnel: the kernel created the device under another name: "
                "asked for %s, got %s%s\n", name, g_open_got,
                strlen(name) >= IFNAMSIZ
                    ? " — longer than the kernel's limit (IFNAMSIZ, 15 characters)" : "");
        return TUN_ESETUP;
    }
    fprintf(stderr, "tunvless[error] tunnel: device %s was not created: %s (%s%s)\n", name,
            strerror(g_open_errno ? g_open_errno : EINVAL),
            g_open_stage == 1 ? "cannot open " : "TUNSETIFF failed",
            g_open_stage == 1 ? TUN_DEV : "");
    return TUN_ESETUP;
}

/* Defined further down, next to their explanation. */
static uint32_t csum_add(const unsigned char *d, size_t n, uint32_t acc);
static uint16_t csum_fin(uint32_t acc);

/* TCP checksum of the segment at th, with the pseudo-header from the IPv4 header at ip4. */
static uint16_t seg_tcp_csum(const unsigned char *ip4, const unsigned char *th, size_t th_n) {
    unsigned char ph[12];
    memcpy(ph, ip4 + 12, 4);
    memcpy(ph + 4, ip4 + 16, 4);
    ph[8] = 0;
    ph[9] = 6;                       /* IPPROTO_TCP */
    ph[10] = (unsigned char)(th_n >> 8);
    ph[11] = (unsigned char)(th_n & 0xFF);
    return csum_fin(csum_add(th, th_n, csum_add(ph, sizeof(ph), 0)));
}

/* ---- splitting COALESCED frames from the kernel ---------------------------------
 *
 * The kernel hands over one frame instead of some forty-five: shared headers, all the payload in a
 * row and the segment size in the metadata. We must hand out exactly the packets that would have
 * come without coalescing, or the other side's stack sees a stream that cannot exist: tunnel up,
 * traffic flowing, some connections stalling.
 *
 * The fix-ups follow the kernel's tcp_gso_segment and inet_gso_segment: the sequence number moves
 * by the OFFSET IN THE PAYLOAD, the IP id grows by one per segment, FIN and PSH go ONLY to the last
 * segment, and both checksums are recomputed. FIN on every segment would close the connection on
 * the first piece. */
static ssize_t seg_emit(struct tun_dev *d, unsigned char *buf, size_t cap) {
    size_t i = (size_t)d->seg_i;
    d->seg_i++;
    size_t off = i * d->seg_gso;
    size_t end = off + d->seg_gso;
    if (end > d->seg_body) end = d->seg_body;
    int last = d->seg_i >= d->seg_n;
    size_t n = d->seg_hdr + (end - off);
    if (n > cap) { d->rx_dropped++; return 0; }
    unsigned char *pkt = d->rx + VNET_HDR_LEN;
    memcpy(buf, pkt, d->seg_hdr);
    memcpy(buf + d->seg_hdr, pkt + d->seg_hdr + off, end - off);

    buf[2] = (unsigned char)(n >> 8);
    buf[3] = (unsigned char)(n & 0xFF);
    uint16_t id = (uint16_t)(d->seg_id + (uint16_t)i);
    buf[4] = (unsigned char)(id >> 8);
    buf[5] = (unsigned char)(id & 0xFF);
    buf[10] = buf[11] = 0;
    uint16_t ipck = csum_fin(csum_add(buf, 20, 0));
    buf[10] = (unsigned char)(ipck >> 8);
    buf[11] = (unsigned char)(ipck & 0xFF);

    unsigned char *th = buf + 20;
    uint32_t seq = d->seg_seq + (uint32_t)off;
    th[4] = (unsigned char)(seq >> 24); th[5] = (unsigned char)(seq >> 16);
    th[6] = (unsigned char)(seq >> 8);  th[7] = (unsigned char)(seq & 0xFF);
    th[13] = last ? d->seg_flags : (unsigned char)(d->seg_flags & ~(unsigned char)0x09);
    th[16] = th[17] = 0;
    uint16_t tck = seg_tcp_csum(buf, th, n - 20);
    th[16] = (unsigned char)(tck >> 8);
    th[17] = (unsigned char)(tck & 0xFF);
    return (ssize_t)n;
}

/* Take a frame just read: either a single packet (maybe with a PARTIAL checksum the kernel left to
 * the device, which we must complete) or a coalesced super-frame. */
static void seg_take(struct tun_dev *d, size_t frame_n) {
    const struct vnet_hdr *vh = (const struct vnet_hdr *)(const void *)d->rx;
    unsigned char *pkt = d->rx + VNET_HDR_LEN;
    size_t pkt_n = frame_n - VNET_HDR_LEN;
    d->seg_i = d->seg_n = 0;
    d->single = 0;

    unsigned char gso_type = (unsigned char)(vh->gso_type & (unsigned char)~VNET_GSO_ECN);
    if (gso_type == VNET_GSO_NONE) {
        /* Partial checksum: the field holds the pseudo-header sum, the body is not summed. The
         * kernel hands packets of its OWN sockets this way; sent into the tunnel as they are, they
         * would be dropped silently by the stack on the other side. The field lies inside the
         * summed range, so no extra term is needed (as in the kernel's skb_checksum_help). */
        if (vh->flags & VNET_F_NEEDS_CSUM) {
            size_t st = vh->csum_start, of = vh->csum_offset;
            if (st + of + 2 > pkt_n) { d->rx_dropped++; return; }
            uint16_t ck = csum_fin(csum_add(pkt + st, pkt_n - st, 0));
            pkt[st + of] = (unsigned char)(ck >> 8);
            pkt[st + of + 1] = (unsigned char)(ck & 0xFF);
        }
        d->single = pkt_n;
        return;
    }
    /* Coalesced. Accept only what we can split: TSO6 was not asked for, and anything else goes to
     * the counter, not to guesswork. */
    if (gso_type != VNET_GSO_TCPV4) { d->rx_dropped++; return; }
    size_t hdr_n = vh->hdr_len, gso = vh->gso_size;
    if (!gso || hdr_n < 40 || hdr_n > pkt_n) { d->rx_dropped++; return; }
    if (pkt[0] != 0x45 || pkt[9] != 6) { d->rx_dropped++; return; }
    /* The header length is taken FROM THE PACKET, not from hdr_len. The kernel puts skb_headlen
     * there, the size of the linear part, which is a hint, not the header length: for a packet of
     * a local socket it is exactly the headers, but for a FORWARDED coalesced frame (LAN client ->
     * router -> TUN) the whole first packet may be linear. Requiring `20 + doff == hdr_len` would
     * drop every such frame, over and over as the client retransmits. hdr_len is only a lower
     * bound: it cannot be shorter than the headers. */
    size_t doff = (size_t)(pkt[32] >> 4) * 4;
    size_t seg_hdr = 20 + doff;
    if (doff < 20 || seg_hdr > hdr_n) { d->rx_dropped++; return; }
    d->seg_hdr = seg_hdr;
    d->seg_body = pkt_n - seg_hdr;
    d->seg_gso = gso;
    d->seg_n = (int)((d->seg_body + gso - 1) / gso);
    if (d->seg_n <= 0) { d->rx_dropped++; d->seg_n = 0; return; }
    uint32_t sq;
    memcpy(&sq, pkt + 24, 4);
    d->seg_seq = ntohl(sq);
    d->seg_id = (uint16_t)(((uint16_t)pkt[4] << 8) | pkt[5]);
    d->seg_flags = pkt[33];
}

ssize_t tun_read_packet(struct tun_dev *d, unsigned char *buf, size_t cap) {
    if (!d->gso) return read(d->fd, buf, cap);
    if (!d->rx_gso) {
        /* The offload header comes on reads too. Without TUNSETOFFLOAD the packets behind it are
         * ordinary, but it still has to be stripped, or parsing is off by ten bytes. */
        struct vnet_hdr vh;
        struct iovec iov[2] = { { &vh, sizeof(vh) }, { buf, cap } };
        ssize_t r = readv(d->fd, iov, 2);
        if (r <= (ssize_t)sizeof(vh)) return r <= 0 ? r : 0;
        return r - (ssize_t)sizeof(vh);
    }
    /* Allocated LAZILY: there can be up to four queues, and 64 KB for each is wasted where offload
     * is not used. If allocation fails, fall back to one packet at a time. */
    if (!d->rx) {
        d->rx = malloc(VNET_HDR_LEN + TUN_FRAME_MAX);
        if (!d->rx) { d->rx_gso = 0; return tun_read_packet(d, buf, cap); }
    }
    for (;;) {
        if (d->single) {
            size_t n = d->single;
            d->single = 0;
            if (n > cap) { d->rx_dropped++; continue; }
            memcpy(buf, d->rx + VNET_HDR_LEN, n);
            return (ssize_t)n;
        }
        if (d->seg_i < d->seg_n) {
            ssize_t n = seg_emit(d, buf, cap);
            if (n > 0) return n;
            continue;
        }
        ssize_t r = read(d->fd, d->rx, VNET_HDR_LEN + TUN_FRAME_MAX);
        if (r <= (ssize_t)VNET_HDR_LEN) return r <= 0 ? r : 0;
        seg_take(d, (size_t)r);
    }
}

/* ---- IP header parsing --------------------------------------------------- */
/* Only what a connection needs: addresses, ports, TCP flags. No full parse, on purpose: the less
 * code touches untrusted bytes from the network, the less room for a mistake in it. */
int ip_parse(const unsigned char *p, size_t n, struct flow_key *k, size_t *payload_off) {
    if (n < 20) return -1;
    int ver = p[0] >> 4;
    if (ver != 4 && ver != 6) return -1;
    size_t ihl;

    memset(k->src.b, 0, sizeof k->src.b);
    memset(k->dst.b, 0, sizeof k->dst.b);
    if (ver == 6) {
        if (n < 40) return -1;
        size_t plen = ((size_t)p[4] << 8) | p[5];
        if (n < 40 + plen) return -1;
        if (p[6] != 6 && p[6] != 17) return -1;
        n = 40 + plen;
        ihl = 40;
        k->proto = p[6];
        k->v6 = 1;
        k->frag = 0;
        memcpy(k->src.b, p + 8, 16);
        memcpy(k->dst.b, p + 24, 16);
    } else {
        ihl = (size_t)(p[0] & 0x0F) * 4;
        if (ihl < 20 || n < ihl) return -1;

        k->proto = p[9];
        k->v6 = 0;
        memcpy(&k->src.v4, p + 12, 4);
        memcpy(&k->dst.v4, p + 16, 4);

        /* Fragments. A later fragment has data where the transport header would be; read as a header
         * it yields random ports, and for UDP random ports mean a NEW flow and a handshake with the
         * node. So a later fragment (non-zero offset) is rejected. The first fragment has the header
         * but the datagram is incomplete: it is flagged, and the caller decides (handle_packet in
         * stack.c). */
        unsigned frag = (unsigned)((p[6] << 8) | p[7]);
        if (frag & 0x1FFF) return -1;                    /* not the first fragment: no header */
        k->frag = (frag & 0x2000) ? 1 : 0;               /* MF: more fragments follow */
    }

    if (k->proto == 6) {                             /* TCP */
        if (n < ihl + 20) return -1;
        const unsigned char *t = p + ihl;
        k->sport = (uint16_t)((t[0] << 8) | t[1]);
        k->dport = (uint16_t)((t[2] << 8) | t[3]);
        size_t doff = (size_t)(t[12] >> 4) * 4;
        if (doff < 20 || n < ihl + doff) return -1;
        k->tcp_flags = t[13];
        k->window = (uint16_t)((t[14] << 8) | t[15]);
        /* Window scale from the SYN options (kind 3, RFC 7323).
         *
         * Without it the advertised window reads as 16 bits, at most 64 KB, while a modern client
         * sends 65535 with a scale and means megabytes. Taken literally, it caps the data in
         * flight at 64 KB: with ACKs every 5 ms, a ceiling of 13 MB/s.
         *
         * Options are parsed only in a SYN: the scale option appears nowhere else and is fixed for
         * the whole connection. */
        k->wscale = 0;
        k->ws_seen = 0;
        k->mss = 0;
        if ((k->tcp_flags & TCP_SYN) && doff > 20) {
            size_t o = ihl + 20, end = ihl + doff;
            while (o < end && o < n) {
                unsigned char kind = p[o];
                if (kind == 0) break;                 /* end of option list */
                if (kind == 1) { o++; continue; }     /* NOP */
                if (o + 1 >= end) break;
                unsigned char olen = p[o + 1];
                if (olen < 2 || o + olen > end) break;
                if (kind == 3 && olen == 3) {
                    k->wscale = p[o + 2] > 14 ? 14 : p[o + 2];
                    k->ws_seen = 1;
                }
                if (kind == 2 && olen == 4)
                    k->mss = (uint16_t)((p[o + 2] << 8) | p[o + 3]);
                o += olen;
            }
        }
        k->seq = ((uint32_t)t[4] << 24) | ((uint32_t)t[5] << 16) |
                 ((uint32_t)t[6] << 8) | t[7];
        k->ack = ((uint32_t)t[8] << 24) | ((uint32_t)t[9] << 16) |
                 ((uint32_t)t[10] << 8) | t[11];
        if (payload_off) *payload_off = ihl + doff;
        return 0;
    }
    if (k->proto == 17) {                            /* UDP */
        if (n < ihl + 8) return -1;
        const unsigned char *u = p + ihl;
        k->sport = (uint16_t)((u[0] << 8) | u[1]);
        k->dport = (uint16_t)((u[2] << 8) | u[3]);
        if (payload_off) *payload_off = ihl + 8;
        return 0;
    }
    return -1;
}

/* ---- checksums ----------------------------------------------------------- */
/* Computed here, not left to the kernel: the kernel does not check the packets we synthesise as
 * they leave TUN, but the client's stack does. A wrong checksum means a silently dropped packet,
 * a connection that hangs without an error. */
static uint32_t csum_add(const unsigned char *d, size_t n, uint32_t acc) {
    /* Four-byte words rather than byte pairs: memcpy becomes one unaligned load and ntohl a byte
     * swap (nothing on big-endian), leaving two additions per 4 bytes instead of four shifted
     * loads. Every UDP packet to the client goes through here, and the whole stream when offload
     * is off, which shows on a router CPU.
     *
     * The sum is THE SAME, not just equal after folding: the same 16-bit words are added, two per
     * step, and uint32 cannot overflow (even 64 KB of data stays below 2^31), so even the raw acc
     * matches a byte-pair loop bit for bit. */
    size_t i = 0;
    for (; i + 4 <= n; i += 4) {
        uint32_t w;
        memcpy(&w, d + i, 4);
        w = ntohl(w);
        acc += (w >> 16) + (w & 0xFFFF);
    }
    for (; i + 1 < n; i += 2)
        acc += ((uint32_t)d[i] << 8) | d[i + 1];
    if (n & 1) acc += (uint32_t)d[n - 1] << 8;
    return acc;
}
static uint16_t csum_fin(uint32_t acc) {
    while (acc >> 16) acc = (acc & 0xFFFF) + (acc >> 16);
    return (uint16_t)~acc;
}

/* IP and TCP header without options for a packet to the client; the TCP checksum is left zero.
 *
 * Separate from tcp_build because a data stream may NOT need the TCP checksum computed: with
 * offload the kernel completes it, which is the whole saving offload is there for. */
void tcp_hdr_build(unsigned char out[TUN_HDR_LEN],
                   uint32_t src, uint32_t dst, uint16_t sport, uint16_t dport,
                   uint32_t seq, uint32_t ack, unsigned char flags,
                   size_t data_n, uint16_t window, uint16_t id) {
    size_t total = TUN_HDR_LEN + data_n;
    memset(out, 0, TUN_HDR_LEN);
    out[0] = 0x45;
    out[2] = (unsigned char)(total >> 8);
    out[3] = (unsigned char)total;
    out[4] = (unsigned char)(id >> 8);
    out[5] = (unsigned char)id;
    out[8] = 64;
    out[9] = 6;
    memcpy(out + 12, &src, 4);
    memcpy(out + 16, &dst, 4);
    uint16_t ipsum = csum_fin(csum_add(out, 20, 0));
    out[10] = (unsigned char)(ipsum >> 8);
    out[11] = (unsigned char)ipsum;

    unsigned char *t = out + 20;
    t[0] = (unsigned char)(sport >> 8); t[1] = (unsigned char)sport;
    t[2] = (unsigned char)(dport >> 8); t[3] = (unsigned char)dport;
    t[4] = (unsigned char)(seq >> 24); t[5] = (unsigned char)(seq >> 16);
    t[6] = (unsigned char)(seq >> 8);  t[7] = (unsigned char)seq;
    t[8] = (unsigned char)(ack >> 24); t[9] = (unsigned char)(ack >> 16);
    t[10] = (unsigned char)(ack >> 8); t[11] = (unsigned char)ack;
    t[12] = 0x50;                                    /* data offset 5 words, no options */
    t[13] = flags;
    t[14] = (unsigned char)(window >> 8); t[15] = (unsigned char)window;
}

static uint32_t tcp_pseudo_sum(uint32_t src, uint32_t dst, size_t tcp_len) {
    unsigned char pseudo[12];
    memcpy(pseudo, &src, 4);
    memcpy(pseudo + 4, &dst, 4);
    pseudo[8] = 0; pseudo[9] = 6;
    pseudo[10] = (unsigned char)(tcp_len >> 8); pseudo[11] = (unsigned char)tcp_len;
    return csum_add(pseudo, 12, 0);
}

/* Wait for room in the device queue and retry the write.
 *
 * The queue fills on any burst and write returns EAGAIN (the device is non-blocking, or the
 * loop's last read would sleep). That is not a connection failure: the queue drains in
 * microseconds.
 *
 * The wait is one millisecond, not ten: ten was measured to become a speed cap (110 loop passes
 * a second with poll idle at 0%). */
static int tun_writev(const struct tun_dev *d, struct iovec *iov, int n) {
    for (int attempts = 0;;) {
        if (writev(d->fd, iov, n) >= 0) return 0;
        if (errno == EINTR) continue;
        if ((errno != EAGAIN && errno != EWOULDBLOCK) || ++attempts > 200) return -1;
        struct pollfd wp = { .fd = d->fd, .events = POLLOUT };
        poll(&wp, 1, 1);
    }
}

int tun_write_data(const struct tun_dev *d, unsigned char hdr[TUN_HDR_LEN],
                   const unsigned char *data, size_t data_n, unsigned mss) {
    struct iovec iov[3];
    int n = 0;
    struct vnet_hdr vh;
    size_t tcp_len = 20 + data_n;
    /* The addresses are copied from the built header with memcpy, not assembled with shifts: they
     * are in NETWORK order inside a uint32_t, and shifts would be right only on little-endian
     * (mips_24kc would sum byte-swapped addresses). */
    uint32_t src, dst;
    memcpy(&src, hdr + 12, 4);
    memcpy(&dst, hdr + 16, 4);

    if (d->gso) {
        /* The checksum is left PARTIAL: the pseudo-header only, no pass over the data. The kernel
         * completes it, as for any socket with CHECKSUM_PARTIAL.
         *
         * The check field gets the folded but NOT inverted pseudo-header sum with the REAL length
         * of the whole segment: the form the kernel prepares in __tcp_v4_send_check,
         * `th->check = ~tcp_v4_check(skb->len, saddr, daddr, 0)`. tcp_gso_segment adjusts it for
         * each piece's length when splitting. Inverting it once more here would give a sum that
         * matches no piece: packets leave, the client never sees them. */
        uint16_t partial = (uint16_t)~csum_fin(tcp_pseudo_sum(
                src, dst, tcp_len));
        hdr[20 + 16] = (unsigned char)(partial >> 8);
        hdr[20 + 17] = (unsigned char)partial;

        memset(&vh, 0, sizeof(vh));
        vh.flags = VNET_F_NEEDS_CSUM;
        vh.csum_start = 20;                          /* start of the TCP header */
        vh.csum_offset = 16;                         /* the check field within it */
        vh.hdr_len = TUN_HDR_LEN;
        /* Ask for splitting only when there is something to split: a GSO mark on a segment of
         * one MSS means nothing. */
        if (tun_pieces(data_n, mss) > 1) {
            vh.gso_type = VNET_GSO_TCPV4;
            vh.gso_size = (uint16_t)mss;
        }
        iov[n].iov_base = &vh;
        iov[n].iov_len = sizeof(vh);
        n++;
    } else {
        /* Without offload we compute the checksum ourselves: a pass over ALL the data. */
        uint32_t acc = tcp_pseudo_sum(src, dst, tcp_len);
        acc = csum_add(hdr + 20, 20, acc);
        acc = csum_add(data, data_n, acc);
        uint16_t tsum = csum_fin(acc);
        hdr[20 + 16] = (unsigned char)(tsum >> 8);
        hdr[20 + 17] = (unsigned char)tsum;
    }

    iov[n].iov_base = hdr;
    iov[n].iov_len = TUN_HDR_LEN;
    n++;
    if (data_n) {
        iov[n].iov_base = (void *)(uintptr_t)data;
        iov[n].iov_len = data_n;
        n++;
    }
    return tun_writev(d, iov, n);
}

void tun_write_ctl(const struct tun_dev *d, const unsigned char *pkt, size_t n) {
    struct iovec iov[2];
    int i = 0;
    struct vnet_hdr vh;
    if (d->gso) {
        /* The checksums are already set: tell the kernel there is nothing to do. */
        memset(&vh, 0, sizeof(vh));
        vh.gso_type = VNET_GSO_NONE;
        iov[i].iov_base = &vh;
        iov[i].iov_len = sizeof(vh);
        i++;
    }
    iov[i].iov_base = (void *)(uintptr_t)pkt;
    iov[i].iov_len = n;
    i++;
    /* No retry and no check: the client's own retransmit recovers a lost SYN-ACK or ACK, and
     * waiting here would hold up the whole loop for a packet without data. */
    (void)!writev(d->fd, iov, i);
}

/* ---- coalescing adjacent segments into one write (rationale and rules in tun.h) ---- */

/* Parse a packet as far as coalescing needs. 0 — it can be coalesced, -1 — not.
 *
 * Strict: IPv4 without options, not a fragment, flags exactly ACK or ACK|PSH. Anything else is
 * written on its own: coalescing too much corrupts the client's stream, which only a dump shows. */
static int gro_parse(const unsigned char *p, size_t n, size_t *hdr_n, size_t *pay_n,
                    uint32_t *seq) {
    if (n < 40) return -1;
    if (p[0] != 0x45) return -1;                       /* IPv4, ihl 20, no IP options */
    if (p[9] != 6) return -1;                          /* TCP */
    /* Fragments: the offset must be zero and MF clear. DF (0x40) is fine. */
    if ((p[6] & 0x3F) != 0 || p[7] != 0) return -1;
    if ((p[6] & 0x20) != 0) return -1;                 /* MF */
    size_t tot = ((size_t)p[2] << 8) | p[3];
    if (tot != n || tot < 40) return -1;
    /* TCP OPTIONS ARE ALLOWED: Linux enables timestamps by default, so an ordinary segment has a
     * 32-byte header, not 20, and requiring no options would coalesce nothing. Options do no harm
     * because the kernel copies the whole header into every piece when splitting; gro_same
     * requires the option bytes to be IDENTICAL, so the split result is what the sender would have
     * sent as one super-frame. Hardware GRO does the same. */
    size_t doff = (size_t)(p[32] >> 4) * 4;
    if (doff < 20 || doff > 60 || 20 + doff >= tot) return -1;
    *hdr_n = 20 + doff;
    unsigned char fl = p[33];
    /* SYN, FIN, RST, URG change the connection state and are never coalesced, though the kernel
     * can put FIN on the last piece: a mistake here costs far more than one packet per connection
     * gains. */
    if (fl & ~(unsigned char)0x18) return -1;          /* only ACK and PSH allowed */
    if (!(fl & 0x10)) return -1;                       /* ACK required */
    *pay_n = tot - *hdr_n;
    if (*pay_n == 0) return -1;                        /* a bare ACK has nothing to coalesce */
    uint32_t s;
    memcpy(&s, p + 24, 4);
    *seq = ntohl(s);
    return 0;
}

/* Same flow and same state as the pending packets? Addresses, ports, ttl, tos, ack number and
 * window are compared: when splitting, the kernel COPIES the first packet's header into every
 * piece, changing only the sequence number, the checksum and the last piece's flags. Packets with
 * a different window or ack cannot be coalesced: the receiver would see later values in earlier
 * segments. */
static int gro_same(const unsigned char *a, const unsigned char *b, size_t hdr_n) {
    if (a[1] != b[1] || a[8] != b[8]) return 0;        /* tos, ttl */
    if (memcmp(a + 12, b + 12, 8) != 0) return 0;      /* addresses */
    if (memcmp(a + 20, b + 20, 4) != 0) return 0;      /* ports */
    if (memcmp(a + 28, b + 28, 4) != 0) return 0;      /* ack number */
    if (a[32] != b[32]) return 0;                      /* TCP header length */
    if (memcmp(a + 34, b + 34, 2) != 0) return 0;      /* window */
    /* TCP options byte for byte: the kernel copies the FIRST packet's options into every piece,
     * so different timestamps in one batch would show the receiver something that was not sent. */
    if (hdr_n > 40 && memcmp(a + 40, b + 40, hdr_n - 40) != 0) return 0;
    return 1;
}

void tun_gro_flush(const struct tun_dev *d, struct tun_gro *g) {
    if (g->frames <= 0) { tun_gro_reset(g); return; }
    if (g->frames == 1) {
        /* A single packet goes as it is: its checksums are already right (it came that way from
         * the tunnel), so the kernel is told to compute nothing. */
        tun_write_ctl(d, g->first, g->hdr_n + g->seg);
        tun_gro_reset(g);
        return;
    }
    unsigned char *p = g->first;
    size_t tot = g->hdr_n + g->total_pay;
    /* Total length in the IP header: tells the kernel what to split. */
    p[2] = (unsigned char)(tot >> 8);
    p[3] = (unsigned char)(tot & 0xFF);
    p[10] = p[11] = 0;
    uint16_t ipsum = csum_fin(csum_add(p, 20, 0));
    p[10] = (unsigned char)(ipsum >> 8);
    p[11] = (unsigned char)ipsum;
    /* PARTIAL TCP checksum, pseudo-header only, in the form of the kernel's __tcp_v4_send_check
     * (tun_write_data explains why it must not be inverted again). */
    uint32_t src, dst;
    memcpy(&src, p + 12, 4);
    memcpy(&dst, p + 16, 4);
    uint16_t partial = (uint16_t)~csum_fin(tcp_pseudo_sum(src, dst,
                                                         g->hdr_n - 20 + g->total_pay));
    p[20 + 16] = (unsigned char)(partial >> 8);
    p[20 + 17] = (unsigned char)partial;

    struct vnet_hdr vh;
    memset(&vh, 0, sizeof(vh));
    vh.flags = VNET_F_NEEDS_CSUM;
    vh.csum_start = 20;
    vh.csum_offset = 16;
    vh.hdr_len = (uint16_t)g->hdr_n;
    vh.gso_type = VNET_GSO_TCPV4;
    vh.gso_size = (uint16_t)g->seg;
    memcpy(g->vh, &vh, sizeof(vh));
    g->iov[0].iov_base = g->vh;
    g->iov[0].iov_len = sizeof(vh);
    /* No retry and no check, as in tun_write_ctl: a full device queue is congestion, not a
     * failure, and the inner TCP recovers the loss itself. */
    (void)!writev(d->fd, g->iov, g->nio);
    tun_gro_reset(g);
}

void tun_gro_push(const struct tun_dev *d, struct tun_gro *g, unsigned char *pkt, size_t n) {
    size_t pay_n = 0, hdr_n = 0;
    uint32_t seq = 0;
    /* Without offload there is nothing to coalesce with: the kernel would not understand the
     * mark, and computing checksums over all the data costs more than the call saved. */
    if (!d->gro || gro_parse(pkt, n, &hdr_n, &pay_n, &seq) != 0) {
        tun_gro_flush(d, g);
        tun_write_ctl(d, pkt, n);
        return;
    }
    if (g->frames > 0) {
        int fits = g->nio < (int)(sizeof(g->iov) / sizeof(g->iov[0]));
        /* Append only if the sequence number continues the previous packet, the size is not
         * above the first one's (the last piece may be shorter, all others must equal the first)
         * and the flow is the same. */
        if (fits && seq == g->next_seq && pay_n <= g->seg && hdr_n == g->hdr_n &&
            gro_same(g->first, pkt, hdr_n)) {
            g->iov[g->nio].iov_base = pkt + hdr_n;
            g->iov[g->nio].iov_len = pay_n;
            g->nio++;
            g->total_pay += pay_n;
            g->frames++;
            g->next_seq = seq + (uint32_t)pay_n;
            /* A short piece closes the batch: splitting would not come out right after it. So
             * does PSH: the kernel puts it only on the last piece, so it must be last.
             *
             * PSH IS COLLECTED INTO THE BATCH HEADER. The coalesced frame carries the FIRST
             * packet's header while PSH sits on the last one; without this line it would be lost
             * and the receiver would never see the end of an application write. Splitting (ours
             * and the kernel's) gives the flags to the last piece, so the PSH collected here ends
             * up there. The kernel's GRO does the same. */
            g->first[33] |= (unsigned char)(pkt[33] & 0x08);
            if (pay_n < g->seg || (pkt[33] & 0x08)) tun_gro_flush(d, g);
            return;
        }
        tun_gro_flush(d, g);
    }
    /* Start a new batch: the first packet goes whole in one vector (its header and payload are
     * already contiguous), the rest as payloads only. */
    g->first = pkt;
    g->seg = pay_n;
    g->hdr_n = hdr_n;
    g->total_pay = pay_n;
    g->frames = 1;
    g->next_seq = seq + (uint32_t)pay_n;
    g->nio = 2;
    g->iov[1].iov_base = pkt;
    g->iov[1].iov_len = hdr_n + pay_n;
    /* PSH on the very first packet means no batch: write it now. */
    if (pkt[33] & 0x08) tun_gro_flush(d, g);
}

/* ICMP port unreachable for a packet we cannot carry.
 *
 * The tunnel carries TCP and UDP (see the `k.proto != 6 && k.proto != 17` branch in stack.c).
 * What is left (ICMP, ESP, GRE) has no VLESS command, and nothing can be done for it.
 *
 * Everything routed into the device arrives here whatever its protocol, and a packet dropped
 * silently leaves the client waiting: to it silence means "no answer yet", so it waits and
 * retries before trying something else. A refusal can be handled at once; silence can only be
 * waited out. The ICMP error turns the wait into an immediate refusal.
 *
 * The source is the address the client talked to, as in the synthesised RSTs: we answer on behalf
 * of the destination, since for the client we are the path to it.
 *
 * Format (RFC 792): an 8-byte ICMP header, then the original IP header and the first 8 bytes of
 * its data, by which the client finds which of its connections was refused. */
size_t icmp_unreach_build(unsigned char *out, size_t cap,
                          const unsigned char *orig, size_t orig_n) {
    if (orig_n < 20) return 0;
    size_t ihl = (size_t)(orig[0] & 0x0F) * 4;
    if (ihl < 20 || ihl > orig_n) return 0;
    size_t quote = ihl + 8 > orig_n ? orig_n : ihl + 8;   /* header + 8 bytes of data */
    size_t total = 20 + 8 + quote;
    if (cap < total) return 0;

    memset(out, 0, 28);
    out[0] = 0x45;                                  /* IPv4, 20-byte header */
    out[2] = (unsigned char)(total >> 8);
    out[3] = (unsigned char)total;
    out[6] = 0x40;                                  /* don't fragment */
    out[8] = 64;                                    /* TTL */
    out[9] = 1;                                     /* ICMP */
    memcpy(out + 12, orig + 16, 4);                 /* on behalf of the destination */
    memcpy(out + 16, orig + 12, 4);                 /* to the client */
    uint16_t ipc = csum_fin(csum_add(out, 20, 0));
    out[10] = (unsigned char)(ipc >> 8);
    out[11] = (unsigned char)ipc;

    out[20] = 3;                                    /* destination unreachable */
    out[21] = 3;                                    /* port unreachable */
    /* The next 4 bytes stay zero: unused for code 3. */
    memcpy(out + 28, orig, quote);
    uint16_t ic = csum_fin(csum_add(out + 20, 8 + quote, 0));
    out[22] = (unsigned char)(ic >> 8);
    out[23] = (unsigned char)ic;
    return total;
}

size_t tcp_build(unsigned char *out, size_t cap,
                 uint32_t src, uint32_t dst, uint16_t sport, uint16_t dport,
                 uint32_t seq, uint32_t ack, unsigned char flags,
                 const unsigned char *data, size_t data_n, uint16_t window,
                 unsigned mss, int wscale) {
    /* Options: MSS takes 4 bytes and window scale 3, 7 together, while the TCP header length
     * counts 32-bit words: a NOP pads them to eight. */
    size_t opt_n = 0;
    if (mss) opt_n += 4;
    if (wscale >= 0) opt_n += 4;                     /* NOP + kind + len + shift */
    size_t total = 20 + 20 + opt_n + data_n;
    if (total > cap) return 0;
    memset(out, 0, 40 + opt_n);

    out[0] = 0x45;                                   /* IPv4, ihl=5 */
    out[2] = (unsigned char)(total >> 8);
    out[3] = (unsigned char)total;
    out[8] = 64;                                     /* TTL */
    out[9] = 6;                                      /* TCP */
    memcpy(out + 12, &src, 4);
    memcpy(out + 16, &dst, 4);
    out[10] = 0; out[11] = 0;
    uint16_t ipsum = csum_fin(csum_add(out, 20, 0));
    out[10] = (unsigned char)(ipsum >> 8);
    out[11] = (unsigned char)ipsum;

    unsigned char *t = out + 20;
    t[0] = (unsigned char)(sport >> 8); t[1] = (unsigned char)sport;
    t[2] = (unsigned char)(dport >> 8); t[3] = (unsigned char)dport;
    t[4] = (unsigned char)(seq >> 24); t[5] = (unsigned char)(seq >> 16);
    t[6] = (unsigned char)(seq >> 8);  t[7] = (unsigned char)seq;
    t[8] = (unsigned char)(ack >> 24); t[9] = (unsigned char)(ack >> 16);
    t[10] = (unsigned char)(ack >> 8); t[11] = (unsigned char)ack;
    t[12] = (unsigned char)(((20 + opt_n) / 4) << 4); /* data offset in 32-bit words */
    t[13] = flags;
    t[14] = (unsigned char)(window >> 8); t[15] = (unsigned char)window;
    size_t o = 20;
    if (mss) {
        t[o++] = 2;                                  /* kind: MSS */
        t[o++] = 4;                                  /* option length */
        t[o++] = (unsigned char)(mss >> 8);
        t[o++] = (unsigned char)mss;
    }
    if (wscale >= 0) {
        t[o++] = 1;                                  /* NOP: pad to a word */
        t[o++] = 3;                                  /* kind: window scale */
        t[o++] = 3;                                  /* option length */
        t[o++] = (unsigned char)wscale;
    }
    if (data_n) memcpy(t + 20 + opt_n, data, data_n);

    uint32_t acc = tcp_pseudo_sum(src, dst, 20 + opt_n + data_n);
    acc = csum_add(t, 20 + opt_n + data_n, acc);
    uint16_t tsum = csum_fin(acc);
    t[16] = (unsigned char)(tsum >> 8);
    t[17] = (unsigned char)tsum;
    return total;
}

/* ---- UDP to the client ---------------------------------------------------- */
static uint32_t udp_pseudo_sum(uint32_t src, uint32_t dst, size_t udp_len) {
    unsigned char pseudo[12];
    memcpy(pseudo, &src, 4);
    memcpy(pseudo + 4, &dst, 4);
    pseudo[8] = 0; pseudo[9] = 17;
    pseudo[10] = (unsigned char)(udp_len >> 8); pseudo[11] = (unsigned char)udp_len;
    return csum_add(pseudo, 12, 0);
}

/* IPv4 header with the fragmentation fields. Separate from tcp_build because a datagram may not
 * fit the MTU, and then the same header is written several times with different offsets. */
static void ip4_hdr(unsigned char *out, size_t total, uint32_t src, uint32_t dst,
                    unsigned char proto, uint16_t id, unsigned frag_off, int more) {
    memset(out, 0, 20);
    out[0] = 0x45;
    out[2] = (unsigned char)(total >> 8);
    out[3] = (unsigned char)total;
    out[4] = (unsigned char)(id >> 8);
    out[5] = (unsigned char)id;
    unsigned flags = (frag_off / 8) | (more ? 0x2000u : 0u);
    out[6] = (unsigned char)(flags >> 8);
    out[7] = (unsigned char)flags;
    out[8] = 64;                                     /* TTL */
    out[9] = proto;
    memcpy(out + 12, &src, 4);
    memcpy(out + 16, &dst, 4);
    uint16_t ipsum = csum_fin(csum_add(out, 20, 0));
    out[10] = (unsigned char)(ipsum >> 8);
    out[11] = (unsigned char)ipsum;
}

/* Send the client a datagram. The caller has already swapped addresses and ports: we answer on
 * behalf of the address the client talked to.
 *
 * Fragmentation is not exotic here. The node delivers the datagram WHOLE (the server's kernel
 * reassembled it on the way), and it may not fit our device's MTU: the classic case is a DNS
 * answer with EDNS0, where the client itself advertised a 4096-byte buffer. Without fragmenting,
 * such answers would be lost: "UDP works, but large answers vanish". The client's stack
 * reassembles the fragments.
 *
 * Fragment offsets count 8-byte blocks, so every piece but the last must be a multiple of eight.
 * Returns 0 or -1. */
int udp_write_to_client(const struct tun_dev *d, uint32_t src, uint32_t dst,
                        uint16_t sport, uint16_t dport,
                        const unsigned char *data, size_t n, uint16_t ip_id) {
    /* UDP header plus the datagram: the UDP checksum covers the WHOLE datagram, not a piece, so
     * it is built whole first and split afterwards. */
    unsigned char l4[8 + UDP_DGRAM_MAX];
    if (n > UDP_DGRAM_MAX) return -1;

    size_t udp_len = 8 + n;
    l4[0] = (unsigned char)(sport >> 8); l4[1] = (unsigned char)sport;
    l4[2] = (unsigned char)(dport >> 8); l4[3] = (unsigned char)dport;
    l4[4] = (unsigned char)(udp_len >> 8); l4[5] = (unsigned char)udp_len;
    l4[6] = 0; l4[7] = 0;
    memcpy(l4 + 8, data, n);
    uint32_t acc = udp_pseudo_sum(src, dst, udp_len);
    acc = csum_add(l4, udp_len, acc);
    uint16_t usum = csum_fin(acc);
    /* Zero in the checksum field means "no checksum", so RFC 768 sends 0xFFFF instead: both
     * verify the same. */
    if (!usum) usum = 0xFFFF;
    l4[6] = (unsigned char)(usum >> 8);
    l4[7] = (unsigned char)usum;

    unsigned char pkt[20 + UDP_MTU_PAYLOAD];
    size_t off = 0;
    while (off < udp_len) {
        size_t chunk = udp_len - off;
        if (chunk > UDP_MTU_PAYLOAD) chunk = UDP_MTU_PAYLOAD & ~7u;
        int more = off + chunk < udp_len;
        ip4_hdr(pkt, 20 + chunk, src, dst, 17, ip_id, (unsigned)off, more);
        memcpy(pkt + 20, l4 + off, chunk);
        /* Through tun_writev, NOT tun_write_ctl. tun_write_ctl neither checks nor retries: it is
         * for control packets, whose loss is cheap. These are DATA, and with the device queue
         * full writev returns EAGAIN and the datagram vanishes. The queue fills exactly during a
         * download, so a whole run of datagrams is lost, not one: QUIC survives single losses but
         * not that (an HTTP/3 page over the tunnel stalled or broke off on a live router while
         * the node sent everything). Waiting is cheap: the kernel drains the TUN queue into the
         * LAN within a millisecond. The TCP path (tun_write_data) does the same.
         *
         * With offload on, the vnet header is required ALWAYS, not only when splitting: without
         * it the kernel reads the first ten bytes of our IP header as that header. No splitting
         * is asked for: the checksums are set and the packet already fits the MTU. */
        struct iovec iov[2];
        int nio = 0;
        struct vnet_hdr vh;
        if (d->gso) {
            memset(&vh, 0, sizeof(vh));
            vh.gso_type = VNET_GSO_NONE;
            iov[nio].iov_base = &vh;
            iov[nio].iov_len = sizeof(vh);
            nio++;
        }
        iov[nio].iov_base = pkt;
        iov[nio].iov_len = 20 + chunk;
        nio++;
        if (tun_writev(d, iov, nio) != 0) return -1;
        off += chunk;
    }
    return 0;
}

static void ip6_hdr(unsigned char *out, size_t plen, unsigned char nh,
                    const unsigned char src[16], const unsigned char dst[16]) {
    memset(out, 0, 40);
    out[0] = 0x60;
    out[4] = (unsigned char)(plen >> 8);
    out[5] = (unsigned char)plen;
    out[6] = nh;
    out[7] = 64;
    memcpy(out + 8, src, 16);
    memcpy(out + 24, dst, 16);
}

static uint32_t pseudo6_sum(const unsigned char src[16], const unsigned char dst[16],
                            size_t len, unsigned char nh) {
    unsigned char pseudo[40];
    memcpy(pseudo, src, 16);
    memcpy(pseudo + 16, dst, 16);
    pseudo[32] = (unsigned char)(len >> 24); pseudo[33] = (unsigned char)(len >> 16);
    pseudo[34] = (unsigned char)(len >> 8);  pseudo[35] = (unsigned char)len;
    pseudo[36] = 0; pseudo[37] = 0; pseudo[38] = 0; pseudo[39] = nh;
    return csum_add(pseudo, 40, 0);
}

static void tcp6_fields(unsigned char *t, uint16_t sport, uint16_t dport, uint32_t seq,
                        uint32_t ack, uint8_t flags, uint16_t win, size_t doff_words) {
    t[0] = (unsigned char)(sport >> 8); t[1] = (unsigned char)sport;
    t[2] = (unsigned char)(dport >> 8); t[3] = (unsigned char)dport;
    t[4] = (unsigned char)(seq >> 24); t[5] = (unsigned char)(seq >> 16);
    t[6] = (unsigned char)(seq >> 8);  t[7] = (unsigned char)seq;
    t[8] = (unsigned char)(ack >> 24); t[9] = (unsigned char)(ack >> 16);
    t[10] = (unsigned char)(ack >> 8); t[11] = (unsigned char)ack;
    t[12] = (unsigned char)(doff_words << 4);
    t[13] = flags;
    t[14] = (unsigned char)(win >> 8); t[15] = (unsigned char)win;
}

void tcp6_hdr_build(unsigned char out[TUN6_HDR_LEN], const unsigned char src[16],
                    const unsigned char dst[16], uint16_t sport, uint16_t dport, uint32_t seq,
                    uint32_t ack, uint8_t flags, uint16_t win) {
    ip6_hdr(out, 0, 6, src, dst);
    memset(out + 40, 0, 20);
    tcp6_fields(out + 40, sport, dport, seq, ack, flags, win, 5);
}

size_t tcp6_build(unsigned char *out, size_t cap, const unsigned char src[16],
                  const unsigned char dst[16], uint16_t sport, uint16_t dport, uint32_t seq,
                  uint32_t ack, uint8_t flags, uint16_t win, const unsigned char *opt,
                  size_t opt_n, const unsigned char *data, size_t data_n) {
    if ((opt_n & 3) || opt_n > 40) return 0;
    size_t tcp_len = 20 + opt_n + data_n;
    size_t total = 40 + tcp_len;
    if (total > cap || tcp_len > 0xFFFF) return 0;
    ip6_hdr(out, tcp_len, 6, src, dst);
    unsigned char *t = out + 40;
    memset(t, 0, 20);
    tcp6_fields(t, sport, dport, seq, ack, flags, win, (20 + opt_n) / 4);
    if (opt_n) memcpy(t + 20, opt, opt_n);
    if (data_n) memcpy(t + 20 + opt_n, data, data_n);
    uint16_t tsum = csum_fin(csum_add(t, tcp_len, pseudo6_sum(src, dst, tcp_len, 6)));
    t[16] = (unsigned char)(tsum >> 8);
    t[17] = (unsigned char)tsum;
    return total;
}

int tun_write_data6(const struct tun_dev *d, unsigned char hdr[TUN6_HDR_LEN],
                    const unsigned char *data, size_t data_n, unsigned mss) {
    struct iovec iov[3];
    int n = 0;
    struct vnet_hdr vh;
    size_t tcp_len = 20 + data_n;
    if (tcp_len > 0xFFFF) return -1;
    hdr[4] = (unsigned char)(tcp_len >> 8);
    hdr[5] = (unsigned char)tcp_len;

    if (d->gso) {
        uint16_t partial = (uint16_t)~csum_fin(pseudo6_sum(hdr + 8, hdr + 24, tcp_len, 6));
        hdr[40 + 16] = (unsigned char)(partial >> 8);
        hdr[40 + 17] = (unsigned char)partial;
        memset(&vh, 0, sizeof(vh));
        vh.flags = VNET_F_NEEDS_CSUM;
        vh.csum_start = 40;
        vh.csum_offset = 16;
        vh.hdr_len = TUN6_HDR_LEN;
        if (tun_pieces(data_n, mss) > 1) {
            vh.gso_type = VNET_GSO_TCPV6;
            vh.gso_size = (uint16_t)mss;
        }
        iov[n].iov_base = &vh;
        iov[n].iov_len = sizeof(vh);
        n++;
    } else {
        uint32_t acc = pseudo6_sum(hdr + 8, hdr + 24, tcp_len, 6);
        acc = csum_add(hdr + 40, 20, acc);
        acc = csum_add(data, data_n, acc);
        uint16_t tsum = csum_fin(acc);
        hdr[40 + 16] = (unsigned char)(tsum >> 8);
        hdr[40 + 17] = (unsigned char)tsum;
    }
    iov[n].iov_base = hdr;
    iov[n].iov_len = TUN6_HDR_LEN;
    n++;
    if (data_n) {
        iov[n].iov_base = (void *)(uintptr_t)data;
        iov[n].iov_len = data_n;
        n++;
    }
    return tun_writev(d, iov, n);
}

int udp6_write_to_client(const struct tun_dev *d, const unsigned char src[16],
                         const unsigned char dst[16], uint16_t sport, uint16_t dport,
                         const unsigned char *data, size_t n) {
    if (n > TUN_MTU - 48) return -1;
    unsigned char pkt[TUN_MTU];
    size_t udp_len = 8 + n;
    ip6_hdr(pkt, udp_len, 17, src, dst);
    unsigned char *u = pkt + 40;
    u[0] = (unsigned char)(sport >> 8); u[1] = (unsigned char)sport;
    u[2] = (unsigned char)(dport >> 8); u[3] = (unsigned char)dport;
    u[4] = (unsigned char)(udp_len >> 8); u[5] = (unsigned char)udp_len;
    u[6] = 0; u[7] = 0;
    if (n) memcpy(u + 8, data, n);
    uint16_t usum = csum_fin(csum_add(u, udp_len, pseudo6_sum(src, dst, udp_len, 17)));
    if (!usum) usum = 0xFFFF;
    u[6] = (unsigned char)(usum >> 8);
    u[7] = (unsigned char)usum;

    struct iovec iov[2];
    int nio = 0;
    struct vnet_hdr vh;
    if (d->gso) {
        memset(&vh, 0, sizeof(vh));
        vh.gso_type = VNET_GSO_NONE;
        iov[nio].iov_base = &vh;
        iov[nio].iov_len = sizeof(vh);
        nio++;
    }
    iov[nio].iov_base = pkt;
    iov[nio].iov_len = 40 + udp_len;
    nio++;
    return tun_writev(d, iov, nio);
}
