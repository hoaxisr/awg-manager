#include "firc/tappacket.h"

#include <string.h>

#define IPV4_MIN_HDR 20u
#define IPV6_HDR 40u
#define TCP_MIN_HDR 20u

static uint16_t be16(const uint8_t *p) { return (uint16_t)((p[0] << 8) | p[1]); }

/* len may be less than the packet claims: the capture copies truncated packets, so a short buffer is ordinary. */
static bool tcp_after(const uint8_t *buf, size_t len, firc_tap_packet_t *out) {
    if (len < TCP_MIN_HDR) { return false; }
    size_t hdr = (size_t)(buf[12] >> 4) * 4u;
    if (hdr < TCP_MIN_HDR || hdr > len) { return false; }
    out->src_port = be16(buf);
    out->dst_port = be16(buf + 2);
    out->seq = ((uint32_t)be16(buf + 4) << 16) | be16(buf + 6);
    out->ack = ((uint32_t)be16(buf + 8) << 16) | be16(buf + 10);
    out->flags = buf[13];
    out->payload_len = len - hdr;
    out->payload = out->payload_len > 0 ? buf + hdr : NULL;
    return true;
}

bool firc_tap_packet_parse(const uint8_t *buf, size_t len, firc_tap_packet_t *out) {
    if (buf == NULL || out == NULL || len < 1) { return false; }
    memset(out, 0, sizeof(*out));

    unsigned version = buf[0] >> 4;
    if (version == 4) {
        if (len < IPV4_MIN_HDR) { return false; }
        size_t hdr = (size_t)(buf[0] & 0x0fu) * 4u;
        if (hdr < IPV4_MIN_HDR || hdr > len) { return false; }
        if (buf[9] != 6) { return false; } /* not TCP */
        /* Bit 13 is more-fragments, the low 13 bits the offset; bit 14 (don't-fragment) is not a fragment. */
        uint16_t frag = be16(buf + 6);
        if ((frag & 0x2000u) != 0 || (frag & 0x1fffu) != 0) { return false; }

        out->src.len = 4;
        memcpy(out->src.b, buf + 12, 4);
        out->dst.len = 4;
        memcpy(out->dst.b, buf + 16, 4);
        return tcp_after(buf + hdr, len - hdr, out);
    }
    if (version == 6) {
        if (len < IPV6_HDR) { return false; }
        if (buf[6] != 6) { return false; } /* next header only, no extension chain */
        out->src.len = 16;
        memcpy(out->src.b, buf + 8, 16);
        out->dst.len = 16;
        memcpy(out->dst.b, buf + 24, 16);
        return tcp_after(buf + IPV6_HDR, len - IPV6_HDR, out);
    }
    return false;
}

static void head_ports(const uint8_t *l4, size_t l4_len, uint8_t proto, firc_tap_head_t *out) {
    if ((proto == 6 || proto == 17) && l4_len >= 4) {
        out->src_port = be16(l4);
        out->dst_port = be16(l4 + 2);
    }
}

bool firc_tap_head_parse(const uint8_t *buf, size_t len, firc_tap_head_t *out) {
    if (buf == NULL || out == NULL || len < 1) { return false; }
    memset(out, 0, sizeof(*out));

    unsigned version = buf[0] >> 4;
    if (version == 4) {
        if (len < IPV4_MIN_HDR) { return false; }
        /* A non-zero offset is a later fragment with no L4 header of its own. */
        uint16_t frag = be16(buf + 6);
        if ((frag & 0x1fffu) != 0) { return false; }

        out->proto = buf[9];
        out->src.len = 4;
        memcpy(out->src.b, buf + 12, 4);
        out->dst.len = 4;
        memcpy(out->dst.b, buf + 16, 4);

        size_t hdr = (size_t)(buf[0] & 0x0fu) * 4u;
        if (hdr >= IPV4_MIN_HDR && hdr <= len) {
            head_ports(buf + hdr, len - hdr, out->proto, out);
        }
        return true;
    }
    if (version == 6) {
        if (len < IPV6_HDR) { return false; }
        out->proto = buf[6];
        out->src.len = 16;
        memcpy(out->src.b, buf + 8, 16);
        out->dst.len = 16;
        memcpy(out->dst.b, buf + 24, 16);
        head_ports(buf + IPV6_HDR, len - IPV6_HDR, out->proto, out);
        return true;
    }
    return false;
}
