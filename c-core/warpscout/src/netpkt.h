/* awgm-warpscout: minimal IPv4 / ICMP / TCP packet helpers. */
#ifndef WS_NETPKT_H
#define WS_NETPKT_H

#include <stddef.h>
#include <stdint.h>

#define TCPF_FIN 0x01
#define TCPF_SYN 0x02
#define TCPF_RST 0x04
#define TCPF_PSH 0x08
#define TCPF_ACK 0x10

uint16_t inet_csum(const uint8_t *p, size_t len, uint32_t init);

/* src/dst are host-order IPv4 addresses. Return total packet length. */
size_t pkt_icmp_echo(uint8_t *out, uint32_t src, uint32_t dst, uint16_t ip_id,
                     uint16_t ident, uint16_t seq, const uint8_t *payload, size_t plen);
size_t pkt_tcp(uint8_t *out, uint32_t src, uint32_t dst, uint16_t ip_id,
               uint16_t sport, uint16_t dport, uint32_t seq, uint32_t ack, uint8_t flags,
               uint16_t mss /* 0 = no option */, const uint8_t *data, size_t dlen);

typedef struct {
    uint32_t src, dst;
    uint8_t proto;
    /* ICMP */
    uint8_t icmp_type;
    uint16_t icmp_id, icmp_seq;
    /* TCP */
    uint16_t sport, dport;
    uint32_t seq, ack;
    uint8_t flags;
    const uint8_t *data;
    size_t dlen;
} pkt_info;

/* Parses an inner IPv4 packet; verifies IP header and L4 checksums.
 * Returns 0 if parsed as ICMP or TCP, -1 otherwise. */
int pkt_parse(const uint8_t *p, size_t len, pkt_info *pi);

#endif
