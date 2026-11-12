/* awgm-warpscout: minimal IPv4 / ICMP / TCP packet helpers. */
#include "netpkt.h"

#include <string.h>

static void st16(uint8_t *p, uint16_t v) { p[0] = (uint8_t)(v >> 8); p[1] = (uint8_t)v; }
static void st32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)(v >> 24); p[1] = (uint8_t)(v >> 16); p[2] = (uint8_t)(v >> 8); p[3] = (uint8_t)v;
}
static uint16_t ld16(const uint8_t *p) { return (uint16_t)((p[0] << 8) | p[1]); }
static uint32_t ld32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | p[3];
}

static uint32_t csum_add(const uint8_t *p, size_t len, uint32_t s) {
    size_t i;
    for (i = 0; i + 1 < len; i += 2) s += (uint32_t)((p[i] << 8) | p[i + 1]);
    if (len & 1) s += (uint32_t)p[len - 1] << 8;
    return s;
}

static uint16_t csum_fold(uint32_t s) {
    while (s >> 16) s = (s & 0xffff) + (s >> 16);
    return (uint16_t)~s;
}

uint16_t inet_csum(const uint8_t *p, size_t len, uint32_t init) {
    return csum_fold(csum_add(p, len, init));
}

static uint32_t pseudo_sum(uint32_t src, uint32_t dst, uint8_t proto, size_t l4len) {
    uint32_t s = 0;
    s += src >> 16; s += src & 0xffff;
    s += dst >> 16; s += dst & 0xffff;
    s += proto;
    s += (uint32_t)l4len;
    return s;
}

static void ip_hdr(uint8_t *out, uint32_t src, uint32_t dst, uint16_t id, uint8_t proto, size_t total) {
    memset(out, 0, 20);
    out[0] = 0x45;
    st16(out + 2, (uint16_t)total);
    st16(out + 4, id);
    out[6] = 0x40; /* DF */
    out[8] = 64;
    out[9] = proto;
    st32(out + 12, src);
    st32(out + 16, dst);
    st16(out + 10, inet_csum(out, 20, 0));
}

size_t pkt_icmp_echo(uint8_t *out, uint32_t src, uint32_t dst, uint16_t ip_id,
                     uint16_t ident, uint16_t seq, const uint8_t *payload, size_t plen) {
    uint8_t *ic = out + 20;
    size_t total = 20 + 8 + plen;
    ic[0] = 8;
    ic[1] = 0;
    ic[2] = ic[3] = 0;
    st16(ic + 4, ident);
    st16(ic + 6, seq);
    if (plen) memcpy(ic + 8, payload, plen);
    st16(ic + 2, inet_csum(ic, 8 + plen, 0));
    ip_hdr(out, src, dst, ip_id, 1, total);
    return total;
}

size_t pkt_tcp(uint8_t *out, uint32_t src, uint32_t dst, uint16_t ip_id,
               uint16_t sport, uint16_t dport, uint32_t seq, uint32_t ack, uint8_t flags,
               uint16_t mss, const uint8_t *data, size_t dlen) {
    uint8_t *t = out + 20;
    size_t hl = mss ? 24 : 20;
    size_t l4 = hl + dlen;
    memset(t, 0, hl);
    st16(t, sport);
    st16(t + 2, dport);
    st32(t + 4, seq);
    st32(t + 8, (flags & TCPF_ACK) ? ack : 0);
    t[12] = (uint8_t)((hl / 4) << 4);
    t[13] = flags;
    st16(t + 14, 65535);
    if (mss) {
        t[20] = 2;
        t[21] = 4;
        st16(t + 22, mss);
    }
    if (dlen) memcpy(t + hl, data, dlen);
    st16(t + 16, inet_csum(t, l4, pseudo_sum(src, dst, 6, l4)));
    ip_hdr(out, src, dst, ip_id, 6, 20 + l4);
    return 20 + l4;
}

int pkt_parse(const uint8_t *p, size_t len, pkt_info *pi) {
    memset(pi, 0, sizeof(*pi));
    if (len < 20 || (p[0] >> 4) != 4) return -1;
    size_t ihl = (size_t)(p[0] & 15) * 4;
    size_t tot = ld16(p + 2);
    if (ihl < 20 || tot < ihl || tot > len) return -1;
    if (inet_csum(p, ihl, 0) != 0) return -1;
    if ((ld16(p + 6) & 0x3fff) != 0) return -1; /* fragments unsupported */
    pi->proto = p[9];
    pi->src = ld32(p + 12);
    pi->dst = ld32(p + 16);
    const uint8_t *l4 = p + ihl;
    size_t l4len = tot - ihl;
    if (pi->proto == 1) {
        if (l4len < 8 || inet_csum(l4, l4len, 0) != 0) return -1;
        pi->icmp_type = l4[0];
        pi->icmp_id = ld16(l4 + 4);
        pi->icmp_seq = ld16(l4 + 6);
        pi->data = l4 + 8;
        pi->dlen = l4len - 8;
        return 0;
    }
    if (pi->proto == 6) {
        if (l4len < 20) return -1;
        if (inet_csum(l4, l4len, pseudo_sum(pi->src, pi->dst, 6, l4len)) != 0) return -1;
        size_t off = (size_t)(l4[12] >> 4) * 4;
        if (off < 20 || off > l4len) return -1;
        pi->sport = ld16(l4);
        pi->dport = ld16(l4 + 2);
        pi->seq = ld32(l4 + 4);
        pi->ack = ld32(l4 + 8);
        pi->flags = l4[13];
        pi->data = l4 + off;
        pi->dlen = l4len - off;
        return 0;
    }
    return -1;
}
