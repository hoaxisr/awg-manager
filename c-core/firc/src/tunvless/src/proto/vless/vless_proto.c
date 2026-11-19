/* VLESS: the request header and the response.
 *
 * VLESS is deliberately bare: no encryption and no checksums of its own. The TLS below already
 * does that, and doing it twice would add a distinguishable feature to a stream that must look
 * like plain HTTPS.
 *
 * Request (integers big-endian):
 *   version(1) | UUID(16) | addons_len(1) | addons | command(1) | port(2) | addr_type(1) |
 *   address | data...
 *
 * The server's response is two bytes (version, addons_len) plus the addons, then the data.
 */
#define _GNU_SOURCE
#include <string.h>
#include <stdio.h>
#include <arpa/inet.h>

#include "vless_proto.h"

/* SHA-1 of a short message, used only to derive a UUID from a non-UUID id (see below).
 *
 * Own code rather than the crypto library: vless_proto.c needs no library, so the unit tests
 * (tests/submatch.c) build it without one. SHA-1 is not a protection here but a fixed
 * derivation the server must reproduce, and any error shows at once as a rejected user. It is
 * pinned to the NIST "abc" vector in tests/submatch.c.
 *
 * The message is always shorter than a block: 16 zero bytes plus at most 30 characters, 46
 * bytes at most. The padding fits one 64-byte block, so there is no multi-block loop; longer
 * input is refused. */
static int sha1_short(const unsigned char *msg, size_t n, unsigned char out[20]) {
    unsigned char b[64];
    uint32_t w[80], h[5] = { 0x67452301u, 0xEFCDAB89u, 0x98BADCFEu,
                             0x10325476u, 0xC3D2E1F0u };
    if (n > 55) return -1;              /* 55 = 64 - 1 padding byte - 8 length bytes */
    memset(b, 0, sizeof(b));
    memcpy(b, msg, n);
    b[n] = 0x80;
    uint64_t bits = (uint64_t)n * 8;
    for (int i = 0; i < 8; i++) b[63 - i] = (unsigned char)(bits >> (8 * i));

    for (int i = 0; i < 16; i++)
        w[i] = (uint32_t)b[4 * i] << 24 | (uint32_t)b[4 * i + 1] << 16 |
               (uint32_t)b[4 * i + 2] << 8 | (uint32_t)b[4 * i + 3];
    for (int i = 16; i < 80; i++) {
        uint32_t v = w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16];
        w[i] = (v << 1) | (v >> 31);
    }

    uint32_t a = h[0], bb = h[1], c = h[2], d = h[3], e = h[4];
    for (int i = 0; i < 80; i++) {
        uint32_t f, k;
        if (i < 20)      { f = (bb & c) | (~bb & d);              k = 0x5A827999u; }
        else if (i < 40) { f = bb ^ c ^ d;                        k = 0x6ED9EBA1u; }
        else if (i < 60) { f = (bb & c) | (bb & d) | (c & d);     k = 0x8F1BBCDCu; }
        else             { f = bb ^ c ^ d;                        k = 0xCA62C1D6u; }
        uint32_t t = ((a << 5) | (a >> 27)) + f + e + k + w[i];
        e = d; d = c; c = (bb << 30) | (bb >> 2); bb = a; a = t;
    }
    h[0] += a; h[1] += bb; h[2] += c; h[3] += d; h[4] += e;

    for (int i = 0; i < 5; i++) {
        out[4 * i]     = (unsigned char)(h[i] >> 24);
        out[4 * i + 1] = (unsigned char)(h[i] >> 16);
        out[4 * i + 2] = (unsigned char)(h[i] >> 8);
        out[4 * i + 3] = (unsigned char)h[i];
    }
    return 0;
}

static int hexval(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if ((c | 32) >= 'a' && (c | 32) <= 'f') return (c | 32) - 'a' + 10;
    return -1;
}

/* Groups 8-4-4-4-12 as in Xray: at most one hyphen before each group, only hex digits inside.
 * out may be NULL to only validate.
 *
 * Like Xray, the tail after the last group is ignored: the length is already capped at 36, so
 * "32 hex digits plus 4 arbitrary characters" parses by the first 32. Being stricter would
 * reject links every other client accepts, and cannot cause a mismatch with the server, which
 * never sees our string. */
static int hex_groups(const char *s, size_t n, unsigned char *out) {
    static const size_t groups[5] = { 8, 4, 4, 4, 12 };
    size_t i = 0, o = 0;
    for (int g = 0; g < 5; g++) {
        if (i < n && s[i] == '-') i++;
        if (n - i < groups[g]) return -1;
        for (size_t k = 0; k < groups[g]; k += 2) {
            int hi = hexval(s[i + k]), lo = hexval(s[i + k + 1]);
            if (hi < 0 || lo < 0) return -1;
            if (out) out[o] = (unsigned char)((hi << 4) | lo);
            o++;
        }
        i += groups[g];
    }
    return 0;
}

/* The form of the id is decided by the string's length, as in Xray (common/uuid/uuid.go,
 * ParseString). A panel id such as "TMG_74317ba5f91" is valid VLESS: the UUID is derived from
 * it by a hash, and both ends must derive the same bytes. */
int vless_uuid_form(const char *s) {
    size_t n = s ? strlen(s) : 0;
    if (n >= 32 && n <= 36)
        return hex_groups(s, n, NULL) == 0 ? VLESS_UUID_HEX : VLESS_UUID_NOTHEX;
    if (n == 0) return VLESS_UUID_EMPTY;
    if (n == 31) return VLESS_UUID_GAP;
    if (n > 36) return VLESS_UUID_TOOLONG;
    return VLESS_UUID_DERIVED;
}

/* The branch is chosen by the one rule above: two separate decisions on the length could
 * silently disagree. */
int vless_uuid_parse(const char *s, unsigned char out[16]) {
    switch (vless_uuid_form(s)) {
    case VLESS_UUID_HEX:
        return hex_groups(s, strlen(s), out);
    case VLESS_UUID_DERIVED: {
        /* The first 16 bytes of sha1(16 zero bytes || string), then version 5 in the high
         * nibble of byte 6 and the RFC 4122 variant in byte 8, exactly as in Xray. The zero
         * bytes are not a salt: Xray hashes the string into an empty UUID (h.Write(uuid[:])
         * before the text), and without them the result differs. */
        size_t n = strlen(s);
        unsigned char msg[16 + 30], dg[20];
        memset(msg, 0, 16);
        memcpy(msg + 16, s, n);
        if (sha1_short(msg, 16 + n, dg) != 0) return -1;
        memcpy(out, dg, 16);
        out[6] = (unsigned char)((out[6] & 0x0f) | 0x50);
        out[8] = (unsigned char)((out[8] & 0x3f) | 0x80);
        return 0;
    }
    default:
        return -1;
    }
}

/* The request header. The address goes as a name when one is known, so the server resolves
 * it and the lookup does not leak through local DNS. Traffic from TUN has only an address.
 * Returns the header length, or 0 if it does not fit in cap or the flow or host is too long. */
size_t vless_build_request(const unsigned char uuid[16], enum vless_cmd cmd,
                           const char *host, const unsigned char *ip, int ip_len,
                           uint16_t port, const char *flow,
                           unsigned char *out, size_t cap) {
    size_t i = 0;
    if (cap < 24) return 0;
    out[i++] = 0;                       /* protocol version */
    memcpy(out + i, uuid, 16); i += 16;

    /* Addons. For XTLS-Vision this is the protobuf message Addons with the flow in field 1,
     * schema from Xray's addons.proto:
     *
     *   message Addons { string Flow = 1; bytes Seed = 2; }
     *
     * Encoded by hand, since a message of one string field is three bytes plus the name:
     *   0x0A (field 1, wire type 2) | length | string bytes
     *
     * Without it a server with flow=xtls-rprx-vision does not answer at all: it expects
     * Vision and gets plain VLESS. */
    if (flow && flow[0]) {
        size_t fl = strlen(flow);
        if (fl > 120 || i + 3 + fl > cap) return 0;
        out[i++] = (unsigned char)(2 + fl);   /* protobuf message length */
        out[i++] = 0x0A;                      /* field 1, wire type 2 (string) */
        out[i++] = (unsigned char)fl;
        memcpy(out + i, flow, fl); i += fl;
    } else {
        out[i++] = 0;                         /* no addons */
    }
    out[i++] = (unsigned char)cmd;
    /* Mux (command 3) has no port and address in the header: the v1.mux.cool:666 service is
     * implied by the command. Xray-core (proxy/vless/encoding, EncodeRequestHeader) writes
     * nothing after the command and sing-box (vless.ReadRequest) reads nothing after it, so
     * extra bytes would become the start of the Mux stream. */
    if (cmd == VLESS_CMD_MUX) return i;
    out[i++] = (unsigned char)(port >> 8);
    out[i++] = (unsigned char)port;

    if (host && host[0]) {
        size_t hl = strlen(host);
        if (hl > 255 || i + 2 + hl > cap) return 0;
        out[i++] = VLESS_ADDR_DOMAIN;
        out[i++] = (unsigned char)hl;
        memcpy(out + i, host, hl); i += hl;
    } else if (ip_len == 16) {
        if (i + 17 > cap) return 0;
        out[i++] = VLESS_ADDR_IPV6;
        memcpy(out + i, ip, 16); i += 16;
    } else if (ip_len == 4) {
        if (i + 5 > cap) return 0;
        out[i++] = VLESS_ADDR_IPV4;
        memcpy(out + i, ip, 4); i += 4;
    } else {
        return 0;
    }
    return i;
}

/* Response: version and addons length. Returns 0 with the bytes to drop before the data in
 * *skip, VLESS_EAGAIN if more is needed, VLESS_EPROTO on a bad response.
 *
 * A bad response is the key signal: when Reality does not accept us, the server proxies to the
 * real site, and the first bytes are HTTP or TLS, not VLESS. This check is what tells success
 * from silent failure. */
int vless_parse_response(const unsigned char *buf, size_t n, size_t *skip) {
    if (n < 2) return VLESS_EAGAIN;
    if (buf[0] != 0) return VLESS_EPROTO;      /* not our version: almost surely the real site */
    size_t extra = buf[1];
    if (n < 2 + extra) return VLESS_EAGAIN;
    *skip = 2 + extra;
    return 0;
}

/* ---- TrustTunnel (HTTP/2 CONNECT) and SOCKS5 framing helpers -------------------------------- */

size_t tt_basic_auth(const char *userpass, char *out, size_t cap) {
    static const char b64[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    if (!cap) return 0;
    out[0] = '\0';
    if (!userpass || !userpass[0]) return 0;
    size_t len = strlen(userpass);
    size_t enc_len = 4 * ((len + 2) / 3);
    if (6 + enc_len + 1 > cap) return 0;
    memcpy(out, "Basic ", 6);
    size_t o = 6;
    for (size_t i = 0; i < len; i += 3) {
        uint32_t v = (uint32_t)(unsigned char)userpass[i] << 16;
        if (i + 1 < len) v |= (uint32_t)(unsigned char)userpass[i + 1] << 8;
        if (i + 2 < len) v |= (uint32_t)(unsigned char)userpass[i + 2];
        out[o++] = b64[(v >> 18) & 0x3F];
        out[o++] = b64[(v >> 12) & 0x3F];
        out[o++] = (i + 1 < len) ? b64[(v >> 6) & 0x3F] : '=';
        out[o++] = (i + 2 < len) ? b64[v & 0x3F] : '=';
    }
    out[o] = '\0';
    return o;
}

size_t tt_build_connect(const char *userpass, const char *authority,
                        const char *user_agent, int end_stream,
                        unsigned char *out, size_t cap) {
    char pauth[256];
    tt_basic_auth(userpass, pauth, sizeof(pauth));
    const char *auth = authority ? authority : "";
    const char *ua = user_agent ? user_agent : "";
    size_t alen = strlen(auth) + 1;
    size_t plen = strlen(pauth) + 1;
    size_t ulen = strlen(ua) + 1;
    if (alen > 255 || plen > 65535 || ulen > 255 || 7 + alen + plen + ulen > cap) return 0;
    out[0] = 'T';
    out[1] = 'T';
    out[2] = (unsigned char)(end_stream ? 1 : 0);
    out[3] = (unsigned char)alen;
    out[4] = (unsigned char)(plen >> 8);
    out[5] = (unsigned char)plen;
    out[6] = (unsigned char)ulen;
    memcpy(out + 7, auth, alen);
    memcpy(out + 7 + alen, pauth, plen);
    memcpy(out + 7 + alen + plen, ua, ulen);
    return 7 + alen + plen + ulen;
}

size_t socks5_build_connect(const char *host, const unsigned char ip4[4],
                            uint16_t port, unsigned char *out, size_t cap) {
    if (host && host[0]) {
        size_t hl = strlen(host);
        if (hl > 255 || 7 + hl > cap) return 0;
        out[0] = 0x05;
        out[1] = 0x01;
        out[2] = 0x00;
        out[3] = 0x03;
        out[4] = (unsigned char)hl;
        memcpy(out + 5, host, hl);
        out[5 + hl] = (unsigned char)(port >> 8);
        out[6 + hl] = (unsigned char)port;
        return 7 + hl;
    }
    if (cap < 10 || !ip4) return 0;
    out[0] = 0x05;
    out[1] = 0x01;
    out[2] = 0x00;
    out[3] = 0x01;
    memcpy(out + 4, ip4, 4);
    out[8] = (unsigned char)(port >> 8);
    out[9] = (unsigned char)port;
    return 10;
}

int socks5_parse_reply(const unsigned char *buf, size_t n, size_t *skip) {
    if (n < 4) return VLESS_EAGAIN;
    if (buf[0] != 0x05 || buf[1] != 0x00) return VLESS_EPROTO;
    size_t need = 0;
    if (buf[3] == 0x01) need = 10;
    else if (buf[3] == 0x04) need = 22;
    else if (buf[3] == 0x03) {
        if (n < 5) return VLESS_EAGAIN;
        need = 5 + (size_t)buf[4] + 2;
    } else {
        return VLESS_EPROTO;
    }
    if (n < need) return VLESS_EAGAIN;
    *skip = need;
    return 0;
}

/* ---- Trojan and Shadowsocks framing helpers ------------------------------------------------ */

static uint32_t rotr32(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }

void trojan_sha224_hex(const char *password, char out[57]) {
    static const uint32_t K[64] = {
        0x428a2f98u, 0x71374491u, 0xb5c0fbcfu, 0xe9b5dba5u, 0x3956c25bu, 0x59f111f1u, 0x923f82a4u, 0xab1c5ed5u,
        0xd807aa98u, 0x12835b01u, 0x243185beu, 0x550c7dc3u, 0x72be5d74u, 0x80deb1feu, 0x9bdc06a7u, 0xc19bf174u,
        0xe49b69c1u, 0xefbe4786u, 0x0fc19dc6u, 0x240ca1ccu, 0x2de92c6fu, 0x4a7484aau, 0x5cb0a9dcu, 0x76f988dau,
        0x983e5152u, 0xa831c66du, 0xb00327c8u, 0xbf597fc7u, 0xc6e00bf3u, 0xd5a79147u, 0x06ca6351u, 0x14292967u,
        0x27b70a85u, 0x2e1b2138u, 0x4d2c6dfcu, 0x53380d13u, 0x650a7354u, 0x766a0abbu, 0x81c2c92eu, 0x92722c85u,
        0xa2bfe8a1u, 0xa81a664bu, 0xc24b8b70u, 0xc76c51a3u, 0xd192e819u, 0xd6990624u, 0xf40e3585u, 0x106aa070u,
        0x19a4c116u, 0x1e376c08u, 0x2748774cu, 0x34b0bcb5u, 0x391c0cb3u, 0x4ed8aa4au, 0x5b9cca4fu, 0x682e6ff3u,
        0x748f82eeu, 0x78a5636fu, 0x84c87814u, 0x8cc70208u, 0x90befffau, 0xa4506cebu, 0xbef9a3f7u, 0xc67178f2u
    };
    uint32_t h[8] = {
        0xc1059ed8u, 0x367cd507u, 0x3070dd17u, 0xf70e5939u,
        0xffc00b31u, 0x68581511u, 0x64f98fa7u, 0xbefa4fa4u
    };
    const unsigned char *msg = (const unsigned char *)(password ? password : "");
    size_t len = password ? strlen(password) : 0;
    size_t total = ((len + 9 + 63) / 64) * 64;
    unsigned char blk[256];
    if (total > sizeof(blk)) {
        out[0] = '\0';
        return;
    }
    memset(blk, 0, total);
    if (len) memcpy(blk, msg, len);
    blk[len] = 0x80;
    uint64_t bits = (uint64_t)len * 8;
    for (int i = 0; i < 8; i++) blk[total - 1 - (size_t)i] = (unsigned char)(bits >> (8 * i));

    for (size_t off = 0; off < total; off += 64) {
        uint32_t w[64];
        const unsigned char *b = blk + off;
        for (int i = 0; i < 16; i++)
            w[i] = ((uint32_t)b[4 * i] << 24) | ((uint32_t)b[4 * i + 1] << 16) |
                   ((uint32_t)b[4 * i + 2] << 8) | (uint32_t)b[4 * i + 3];
        for (int i = 16; i < 64; i++) {
            uint32_t s0 = rotr32(w[i - 15], 7) ^ rotr32(w[i - 15], 18) ^ (w[i - 15] >> 3);
            uint32_t s1 = rotr32(w[i - 2], 17) ^ rotr32(w[i - 2], 19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16] + s0 + w[i - 7] + s1;
        }
        uint32_t a = h[0], bb = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
        for (int i = 0; i < 64; i++) {
            uint32_t S1 = rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25);
            uint32_t ch = (e & f) ^ ((~e) & g);
            uint32_t temp1 = hh + S1 + ch + K[i] + w[i];
            uint32_t S0 = rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22);
            uint32_t maj = (a & bb) ^ (a & c) ^ (bb & c);
            uint32_t temp2 = S0 + maj;
            hh = g; g = f; f = e; e = d + temp1;
            d = c; c = bb; bb = a; a = temp1 + temp2;
        }
        h[0] += a; h[1] += bb; h[2] += c; h[3] += d;
        h[4] += e; h[5] += f;  h[6] += g; h[7] += hh;
    }
    static const char hex[] = "0123456789abcdef";
    for (int i = 0; i < 7; i++) {
        for (int j = 0; j < 4; j++) {
            unsigned char byte = (unsigned char)(h[i] >> (24 - 8 * j));
            out[8 * i + 2 * j]     = hex[byte >> 4];
            out[8 * i + 2 * j + 1] = hex[byte & 0x0f];
        }
    }
    out[56] = '\0';
}

size_t ss_build_addr(const char *host, const unsigned char ip4[4],
                     uint16_t port, unsigned char *out, size_t cap) {
    unsigned char tmp[270];
    size_t n = socks5_build_connect(host, ip4, port, tmp, sizeof(tmp));
    if (n < 7 || n - 3 > cap) return 0;
    memcpy(out, tmp + 3, n - 3);
    return n - 3;
}

size_t trojan_build_request(const char *password, uint8_t cmd,
                            const char *host, const unsigned char ip4[4],
                            uint16_t port, unsigned char *out, size_t cap) {
    unsigned char addr[264];
    size_t alen = ss_build_addr(host, ip4, port, addr, sizeof(addr));
    if (!alen || 56 + 2 + 1 + alen + 2 > cap) return 0;
    char hex[57];
    trojan_sha224_hex(password, hex);
    if (!hex[0]) return 0;
    memcpy(out, hex, 56);
    out[56] = '\r';
    out[57] = '\n';
    out[58] = cmd;
    memcpy(out + 59, addr, alen);
    out[59 + alen] = '\r';
    out[60 + alen] = '\n';
    return 61 + alen;
}
