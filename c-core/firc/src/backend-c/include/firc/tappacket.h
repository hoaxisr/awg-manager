#ifndef FIRC_TAPPACKET_H
#define FIRC_TAPPACKET_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip.h"

typedef struct {
    firc_ip_t src;
    firc_ip_t dst;
    uint16_t dst_port;
    uint16_t src_port;
    uint32_t seq;
    uint32_t ack;
    uint8_t flags;
    const uint8_t *payload; /* into the caller's buffer; NULL when empty */
    size_t payload_len;
} firc_tap_packet_t;

/* Parses an IPv4/IPv6 TCP packet's addresses, port and payload; false for a
 * fragment, a truncated header, or any other protocol. */
bool firc_tap_packet_parse(const uint8_t *buf, size_t len, firc_tap_packet_t *out);

typedef struct {
    firc_ip_t src;
    firc_ip_t dst;
    uint8_t proto;     /* IPv4 protocol / IPv6 next header (no extension walk) */
    uint16_t src_port; /* TCP or UDP source port; 0 otherwise */
    uint16_t dst_port; /* TCP or UDP destination port; 0 otherwise */
} firc_tap_head_t;

/* Parses any IPv4/IPv6 packet's addresses and proto; dst_port stays 0 when
 * the header is truncated or option-shifted past the ports. */
bool firc_tap_head_parse(const uint8_t *buf, size_t len, firc_tap_head_t *out);

#endif /* FIRC_TAPPACKET_H */
