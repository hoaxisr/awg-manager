/* The TUN device: opening it, reading packets, parsing IPv4 and IPv6 headers and building the
 * packets sent back to the client. */
#ifndef STEER_TUN_H
#define STEER_TUN_H
#include <stdint.h>
#include <string.h>
#include <sys/uio.h>
#include <stddef.h>
#include <sys/types.h>

#define TUN_ENODEV (-40)   /* no TUN clone device (/dev/net/tun) */
#define TUN_ESETUP (-41)

union fk_addr {
    uint32_t v4;
    unsigned char b[16];
};

struct flow_key {
    union fk_addr src, dst; /* network order, as in the packet; an IPv4 key keeps b[4..15] zero */
    uint8_t v6;
    /* Ports are in HOST order: ip_parse assembles them from the bytes by hand. ntohs on them swaps
     * twice (port 80 becomes 20480) and sends the connection to the wrong place. */
    uint16_t sport, dport;
    uint8_t proto;
    uint8_t tcp_flags;
    uint32_t seq, ack;
    /* The window the packet's sender advertises. Data sent to the client must stay within it:
     * the client drops what lies beyond. */
    uint16_t window;
    /* Window scale from the SYN options. Set only for a SYN: later segments carry no options and
     * the scale is fixed per connection. The real window is window << wscale. */
    uint8_t wscale;
    /* The SYN carried a window scale option (possibly with shift 0). This differs from "no option"
     * not for the client's window (both mean shift 0) but for ours: RFC 7323 (2.2) allows the
     * option in a SYN-ACK only in answer to a SYN that has it, and the window is scaled only when
     * both sides sent it. A client without it sees our window as plain 16 bits. */
    uint8_t ws_seen;
    uint16_t mss;
    /* The packet is the FIRST fragment of an incomplete datagram (MF set). ip_parse rejects later
     * fragments outright: they carry no header. This matters for UDP, where an incomplete datagram
     * would silently reach the server truncated (stack.c reassembles UDP fragments before
     * parsing). */
    uint8_t frag;
};

#define TCP_FIN 0x01
#define TCP_SYN 0x02
#define TCP_RST 0x04
#define TCP_PSH 0x08
#define TCP_ACK 0x10

/* The MSS we advertise to the client in the SYN-ACK, and the largest segment we send an IPv4
 * client: device MTU minus the IP and TCP headers.
 *
 * It must be advertised: without the option the client has to assume 536 bytes (RFC 1122), and a
 * megabyte then takes 1860 packets instead of 690, each a full parse, send and ACK cycle on one
 * thread. */
#define TUN_MSS 1460

/* Data bytes handed to the client in ONE write when offload is on: one TLS record (16384 bytes),
 * the usual size of a read from the node. A larger read (in Vision direct copy, up to TUNNEL_BUF)
 * takes two writes; a smaller limit would cost extra calls where the kernel would take it all. */
#define TUN_GSO_MAX 16384

/* IP+TCP header without options. The data is not appended to it but goes as a separate writev
 * element, so the stream is never copied. */
#define TUN_HDR_LEN 40

/* Device MTU. Set here, not asked of the kernel: we bring the device up and never change its MTU,
 * and TUN_MSS above is derived from the same number (1500 - 20 - 20). Keep the two together. */
#define TUN_MTU 1500

/* IP payload that fits in one packet: MTU minus the IPv4 header. A larger datagram is fragmented
 * (udp_write_to_client). */
#define UDP_MTU_PAYLOAD (TUN_MTU - 20)

/* The largest datagram we carry.
 *
 * Not 65535: an incomplete datagram needs a buffer PER FLOW (reassembly spans reads), and 64 KB per
 * flow at 320 flows is 20 MB on a router with 240 MB. 4 KB covers real traffic: QUIC and game
 * traffic keep within the MTU, and the largest legitimate case is a DNS answer with EDNS0, where
 * clients advertise a 4096-byte buffer. A larger datagram from the node is skipped whole WITHOUT
 * losing the stream's framing; otherwise one such datagram would break the flow for good. */
#define UDP_DGRAM_MAX 4096

/* The largest frame the kernel can hand us with receive offload on: the limit of skb->len, since
 * the IP total length is 16 bits. */
#define TUN_FRAME_MAX 65535

struct tun_dev {
    int fd;
    /* gso: the device carries a virtio header on every read and write (IFF_VNET_HDR). One write
     * then hands the kernel up to 16 KB as ONE segment marked "split by the client's MSS", and
     * the kernel splits it inside its stack and completes the checksums (on many LAN cards in
     * hardware). Without it the same 16 KB take a write and a pass over the data for the TCP
     * checksum per segment. sing-box and wireguard-go work the same way: most of the speed
     * difference comes from the cost of handing packets to the client, not from crypto. */
    int gso;
    /* gro: whether we coalesce adjacent segments into one write (tun_gro_push). Separate from gso:
     * gso says the device accepts the split mark, gro says we use it to coalesce. STEER_TUN_NOGRO
     * turns it off, so its gain can be checked on the same hardware (as STEER_TUN_NOGSO does for
     * gso). */
    int gro;
    /* rx_gso: whether WE accept coalesced frames from the kernel (TUNSETOFFLOAD). One read then
     * returns what would otherwise be some forty-five packets; a Go implementation measured nearly
     * twice the receive speed with it. The buffer for it is one per queue, inside tun.c:
     * tun_read_packet still hands out one packet at a time, so the data path never sees coalesced
     * frames. STEER_TUN_NORXGSO turns it off, for the same reason as its neighbours.
     *
     * Only TUN_F_CSUM and TUN_F_TSO4 are asked for. Not TSO6: splitting here is IPv4 only, and a
     * frame we cannot split would be dropped instead of arriving as ordinary packets. */
    int rx_gso;
    /* ---- splitting coalesced frames (tun_read_packet) ----
     *
     * rx: the receive buffer, virtio header plus super-frame. Allocated LAZILY on the first read
     * with receive offload: there can be up to four queues, and 64 KB for each is wasted where
     * offload did not come up. */
    unsigned char *rx;
    size_t   seg_hdr;          /* header length of the super-frame (IP and TCP) */
    size_t   seg_body;         /* total payload length */
    size_t   seg_gso;          /* segment size, as the kernel gave it */
    int      seg_i, seg_n;     /* segments handed out, and in total */
    uint32_t seg_seq;          /* sequence number of the FIRST segment, host order */
    uint16_t seg_id;           /* IP id of the first segment */
    unsigned char seg_flags;   /* TCP flags of the super-frame; FIN and PSH go to the last one */
    size_t   single;           /* length of a single packet waiting to be handed out, or 0 */
    /* Frames that could not be split or did not fit. Not a failure, and not always zero on a
     * healthy system; steady growth means the kernel hands us frames we do not handle. */
    unsigned long long rx_dropped;
};

/* Open the device, asking for up to max_queues queues. Returns how many opened (at least one), or a
 * negative code.
 *
 * With IFF_MULTI_QUEUE each loop thread gets its own descriptor on the same device and keeps its
 * own connection table, with no locks on the packet path. tun_open sets no steering program
 * (TUNSETSTEERINGEBPF), so the kernel does not promise that both directions of a connection reach
 * the same queue (see g_conns in stack.c). A kernel without IFF_MULTI_QUEUE is not an error: one
 * queue opens and one thread runs. */
int tun_open(struct tun_dev *d, int max_queues, const char *name);
static inline int fk_same_addr(const union fk_addr *a, const union fk_addr *b) {
    return memcmp(a->b, b->b, 16) == 0;
}

int ip_parse(const unsigned char *p, size_t n, struct flow_key *k, size_t *payload_off);

/* Read one packet: strips the offload header the device adds and splits coalesced frames. */
ssize_t tun_read_packet(struct tun_dev *d, unsigned char *buf, size_t cap);

/* Header for a data segment. The IP checksum is set, the TCP checksum is NOT: tun_write_data sets
 * it, since only it knows whether we or the kernel compute it. */
void tcp_hdr_build(unsigned char out[TUN_HDR_LEN],
                   uint32_t src, uint32_t dst, uint16_t sport, uint16_t dport,
                   uint32_t seq, uint32_t ack, unsigned char flags,
                   size_t data_n, uint16_t window, uint16_t id);

/* Hand the client a header with its data. The data is NOT copied.
 *
 * When the device queue is full it waits for room and retries (for up to about 200 ms): the queue
 * drains in microseconds, while a dropped segment would wait for a retransmit timeout. Returns 0
 * or -1. */
int tun_write_data(const struct tun_dev *d, unsigned char hdr[TUN_HDR_LEN],
                   const unsigned char *data, size_t data_n, unsigned mss);

static inline size_t tun_pieces(size_t data_n, unsigned mss) {
    return data_n > mss ? (data_n + mss - 1) / mss : 1;
}

/* A control packet (tcp_build, icmp_unreach_build) with its checksums already set. Not retried:
 * the client's own retransmit recovers a lost SYN-ACK or ACK. */
void tun_write_ctl(const struct tun_dev *d, const unsigned char *pkt, size_t n);

/* ---- COALESCING ADJACENT SEGMENTS INTO ONE WRITE (GRO in reverse) ----------------
 *
 * Meant for a protocol that carries whole IP packets. stack.c does not use it (it builds its own
 * large segments for tun_write_data); tests/tungromatch.c covers it.
 *
 * WHY. A write to TUN is a full pass through the kernel's stack: per write the kernel builds an
 * skb, runs it through ip_rcv and tcp_v4_rcv, queues it on the socket and wakes the reader. A
 * whole-machine perf profile of a live tunnel showed the sending side spending 1% of its time on
 * its own crypto and the rest in tun_get_user, __alloc_skb and syscall overhead, one pass per 1439
 * bytes. Marked as GSO, one pass covers eight packets: the kernel takes them as one large segment
 * and splits them inside its stack (and often hands them to the receiving socket unsplit).
 * wireguard-go uses the same trick; tun_write_data uses the same offload.
 *
 * NO BUFFER. Collecting packets into a 16-64 KB buffer would need one buffer per connection, real
 * memory on a 128 MB router. It is not needed: the frames of one decrypted record already lie
 * contiguously in memory, so a single writev lists their payloads as vectors. Only the FIRST
 * packet's header is edited in place.
 *
 * NO ADDED DELAY. Only frames already read from one record are coalesced; nothing waits for the
 * next one. The caller must call tun_gro_flush at the end of each record, so a packet lives no
 * longer than one loop pass. There are no timers: on a router latency costs more than throughput.
 *
 * WHAT IS COALESCED: only what the kernel will split back byte for byte. IPv4 without options, the
 * same flow (addresses and ports), consecutive sequence numbers, equal ttl, tos, ack, window and
 * TCP options, flags exactly ACK (or ACK|PSH on the last one: the kernel puts PSH and FIN only on
 * the last piece). Everything else (ICMP, UDP, fragments, SYN, RST, a changed window) goes as its
 * own write. The rules are strict on purpose: coalescing too much hands the client a stream with
 * reordered or corrupted bytes, which only a packet dump reveals. */
#define TUN_GRO_FRAMES 8      /* most frames coalesced into one write */

struct tun_gro {
    /* vnet_hdr + the whole first packet + payloads of the rest: exactly TUN_GRO_FRAMES frames, as
     * the first packet takes one vector together with its header (2 + TUN_GRO_FRAMES would allow
     * nine). */
    struct iovec iov[1 + TUN_GRO_FRAMES];
    int      nio;
    unsigned char *first;     /* the first packet: its length and checksums are edited */
    size_t   total_pay;       /* payload of all coalesced packets */
    size_t   seg;             /* payload of the first; also gso_size */
    size_t   hdr_n;           /* IP + TCP of the first packet: can be 52 with options */
    int      frames;
    uint32_t next_seq;        /* sequence number the next packet must have, host order */
    unsigned char vh[16];     /* offload header; must live until writev */
};

static inline void tun_gro_reset(struct tun_gro *g) { g->frames = 0; g->nio = 0; }

/* Write a packet, coalescing it with the pending ones when that is valid. The packet MUST stay in
 * memory unchanged until tun_gro_flush: it is not copied. */
void tun_gro_push(const struct tun_dev *d, struct tun_gro *g, unsigned char *pkt, size_t n);

/* Write out what is pending. Always called at the end of a record. */
void tun_gro_flush(const struct tun_dev *d, struct tun_gro *g);

/* Send the client a datagram on behalf of the address it talked to. A datagram larger than the MTU
 * is fragmented; the client's stack reassembles it. Returns 0 or -1.
 *
 * ip_id is the IP identification: it matters only when fragmenting (the client groups the
 * fragments of one datagram by it). */
int udp_write_to_client(const struct tun_dev *d, uint32_t src, uint32_t dst,
                        uint16_t sport, uint16_t dport,
                        const unsigned char *data, size_t n, uint16_t ip_id);

/* ICMP port unreachable in answer to a packet the tunnel cannot carry: anything but TCP and UDP
 * (ICMP, ESP and so on). The client gets a REFUSAL instead of silence, which it would take as "no
 * answer yet" and wait. Details in tun.c. */
size_t icmp_unreach_build(unsigned char *out, size_t cap,
                          const unsigned char *orig, size_t orig_n);

/* mss != 0 adds the MSS option, wscale >= 0 the window scale option. Both belong only in SYN and
 * SYN-ACK. */
size_t tcp_build(unsigned char *out, size_t cap,
                 uint32_t src, uint32_t dst, uint16_t sport, uint16_t dport,
                 uint32_t seq, uint32_t ack, unsigned char flags,
                 const unsigned char *data, size_t data_n, uint16_t window,
                 unsigned mss, int wscale);

#define TUN6_HDR_LEN 60
#define TUN6_MSS 1440

void tcp6_hdr_build(unsigned char out[TUN6_HDR_LEN], const unsigned char src[16],
                    const unsigned char dst[16], uint16_t sport, uint16_t dport, uint32_t seq,
                    uint32_t ack, uint8_t flags, uint16_t win);
size_t tcp6_build(unsigned char *out, size_t cap, const unsigned char src[16],
                  const unsigned char dst[16], uint16_t sport, uint16_t dport, uint32_t seq,
                  uint32_t ack, uint8_t flags, uint16_t win, const unsigned char *opt,
                  size_t opt_n, const unsigned char *data, size_t data_n);
int tun_write_data6(const struct tun_dev *d, unsigned char hdr[TUN6_HDR_LEN],
                    const unsigned char *data, size_t data_n, unsigned mss);
int udp6_write_to_client(const struct tun_dev *d, const unsigned char src[16],
                         const unsigned char dst[16], uint16_t sport, uint16_t dport,
                         const unsigned char *data, size_t n);
#endif
