#ifndef STEER_H2WIRE_H
#define STEER_H2WIRE_H
#include <stddef.h>
#include <stdint.h>

#include "h2.h"

#define FR_DATA          0x00
#define FR_HEADERS       0x01
#define FR_RST_STREAM    0x03
#define FR_SETTINGS      0x04
#define FR_PING          0x06
#define FR_GOAWAY        0x07
#define FR_WINDOW_UPDATE 0x08
#define FR_CONTINUATION  0x09

#define FLAG_END_STREAM  0x01
#define FLAG_ACK         0x01
#define FLAG_END_HEADERS 0x04
#define FLAG_PADDED      0x08
#define FLAG_PRIORITY    0x20

/* Our receive window. Large on purpose: with the default 65535 every 64 KB of download waits
 * for a WINDOW_UPDATE, which on a 100 ms link caps speed at about 5 Mbit/s whatever the
 * bandwidth. A megabyte costs nothing: it only lets the server send; we hold no memory for it. */
#define OUR_WINDOW       (1024 * 1024)
/* Refill every 32 KB, not every frame: a refill per frame is an extra TLS record per 16 KB of
 * data, a visible pattern in the stream. */
#define WINDOW_REFILL    (32 * 1024)

/* The largest frame a server must accept by default (RFC 7540 §4.2); more only if the server
 * allows it in SETTINGS. */
#define DEFAULT_MAX_FRAME 16384

#define H2_PREFACE_LEN (24 + 9 + 12 + 9 + 4)

#define H2W_GO_PREFACE_LEN 70
#define H2M_STREAM_WIN  4194304
#define H2M_CONN_INC    1073741824
#define H2M_MIN_REFRESH 4096
#define H2M_FRAME_IN    1048576
#define H2M_HDR_LIST    10485760

static inline void put32(unsigned char *p, uint32_t v) {
    p[0] = (unsigned char)(v >> 24); p[1] = (unsigned char)(v >> 16);
    p[2] = (unsigned char)(v >> 8);  p[3] = (unsigned char)v;
}
static inline uint32_t get32(const unsigned char *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | p[3];
}

static inline void h2w_frame_hdr(unsigned char *p, uint32_t len, unsigned char type,
                                 unsigned char flags, uint32_t sid) {
    p[0] = (unsigned char)(len >> 16); p[1] = (unsigned char)(len >> 8); p[2] = (unsigned char)len;
    p[3] = type;
    p[4] = flags;
    put32(p + 5, sid);
}

static inline uint32_t h2w_frame_len(const unsigned char *p) {
    return ((uint32_t)p[0] << 16) | ((uint32_t)p[1] << 8) | p[2];
}

/* Three status digits in HPACK Huffman code (RFC 7541, appendix B). A Go server (Xray)
 * Huffman-codes a value whenever that is shorter: "502", "401", "301" take two bytes instead of
 * three. Without this the status stays -1, and a refusal looks like an answer without data.
 *
 * Only digits are needed: "0", "1", "2" have 5-bit codes 00000…00010, "3"…"9" 6-bit codes
 * 011001…011111. Any other symbol means this is not a status: -1. After three digits only
 * padding of ones shorter than a byte is allowed (§5.2). */
static inline int status_huff(const unsigned char *p, size_t n) {
    uint32_t acc = 0;
    unsigned bits = 0;
    size_t i = 0;
    int v = 0;
    for (int d = 0; d < 3; d++) {
        while (bits < 6 && i < n) { acc = (acc << 8) | p[i++]; bits += 8; }
        if (bits < 5) return -1;
        unsigned c5 = (acc >> (bits - 5)) & 0x1F;
        if (c5 <= 2) { v = v * 10 + (int)c5; bits -= 5; }
        else {
            if (bits < 6) return -1;
            unsigned c6 = (acc >> (bits - 6)) & 0x3F;
            if (c6 < 0x19 || c6 > 0x1F) return -1;
            v = v * 10 + (int)(c6 - 0x19 + 3);
            bits -= 6;
        }
        acc &= (1u << bits) - 1;
    }
    if (i != n || bits >= 8 || acc != (1u << bits) - 1) return -1;
    return v;
}

#define H2W_FIELDS_MAX 384

struct h2w_list {
    struct h2_field f[H2W_FIELDS_MAX];
    size_t n;
    char buf[2048];
};

struct h2wire_scratch {
    struct h2w_list l;
    struct h2_field rest[H2W_FIELDS_MAX];
    uint16_t unit[H2W_FIELDS_MAX], first[H2W_FIELDS_MAX], perm[H2W_FIELDS_MAX];
};

uint64_t h2w_rng(uint64_t *state);
int h2w_go_fields(const struct h2_req *r, uint64_t *rng, struct h2w_list *out);
size_t h2w_headers(unsigned char *out, size_t cap, const struct h2_req *r);
size_t h2w_list_size(const struct h2_req *r, int browser);
int h2w_status(const unsigned char *block, size_t n);
size_t h2w_preface(unsigned char *out, size_t cap, int conn_update);
size_t h2w_go_preface(unsigned char *out, size_t cap);

#endif
