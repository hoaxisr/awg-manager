/* XTLS-Vision: the VLESS stream wrapped in padded frames.
 *
 * Plain VLESS inside TLS is recognisable: right after the handshake comes a record exactly the
 * size of the VLESS header, then the data. That sequence of record lengths tells the connection
 * from real HTTPS without decrypting it. Vision adds random padding to each frame, so record
 * lengths stop being predictable.
 *
 * Frame format (XtlsPadding in Xray's proxy/proxy.go), integers big-endian:
 *
 *   [UUID 16 bytes]   only in the very first frame
 *   command  1 byte   0=continue, 1=end, 2=direct
 *   length   2 bytes  payload length
 *   padding  2 bytes  number of random bytes after the payload
 *   payload  N
 *   padding  M random bytes
 *
 * `end` tells the peer that padding is over and a plain stream follows. `direct` also switches
 * it to direct copy.
 */
#define _GNU_SOURCE
#include <string.h>
#include "osrand.h"
#include <errno.h>

#include "vision.h"

/* Padding length bounds, the same as Xray's (testseed there). They are tuned so that record
 * lengths look like HTTPS; changing them weakens the camouflage. */
#define PAD_SHORT_THRESHOLD 900
#define PAD_LONG_RANGE      500
#define PAD_LONG_BASE       900
#define PAD_SHORT_RANGE     256

static int rnd_bytes(unsigned char *b, size_t n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = os_getrandom(b + got, n - got, 0);
        if (r < 0) { if (errno == EINTR) continue; return -1; }
        got += (size_t)r;
    }
    return 0;
}

static unsigned rnd_below(unsigned limit) {
    if (limit == 0) return 0;
    unsigned char b[2];
    if (rnd_bytes(b, 2) != 0) return limit / 2;
    return (((unsigned)b[0] << 8) | b[1]) % limit;
}

void vision_init(struct vision *v, const unsigned char uuid[16]) {
    memset(v, 0, sizeof(*v));
    memcpy(v->uuid, uuid, 16);
    v->need_uuid = 1;
    v->filter_left = VISION_FILTER_N;
    v->sh_left = -1;
    v->is_padding = 1;
}

static int has_tls13_version(const unsigned char *b, size_t n) {
    static const unsigned char sv[6] = {0x00, 0x2b, 0x00, 0x02, 0x03, 0x04};
    for (size_t i = 0; i + sizeof sv <= n; i++)
        if (memcmp(b + i, sv, sizeof sv) == 0) return 1;
    return 0;
}

int vision_filter(struct vision *v, const unsigned char *b, size_t n) {
    v->filter_left--;
    if (n >= 6) {
        if (b[0] == 0x16 && b[1] == 0x03 && b[2] == 0x03 && b[5] == 0x02) {
            v->sh_left = (int32_t)(((unsigned)b[3] << 8) | b[4]) + 5;
            v->tls12_or_above = 1;
            v->is_tls = 1;
            if (n >= 79 && v->sh_left >= 79) {
                size_t at = 44 + (size_t)b[43];
                v->cipher = at + 2 <= n ? (uint16_t)((b[at] << 8) | b[at + 1]) : 0;
            }
        } else if (b[0] == 0x16 && b[1] == 0x03 && b[5] == 0x01) {
            v->is_tls = 1;
        }
    }
    if (v->sh_left > 0) {
        size_t end = (size_t)v->sh_left < n ? (size_t)v->sh_left : n;
        v->sh_left -= n > INT32_MAX ? INT32_MAX : (int32_t)n;
        if (has_tls13_version(b, end)) {
            if (v->cipher >= 0x1301 && v->cipher <= 0x1304) v->enable_xtls = 1;
            v->filter_left = 0;
            return 1;
        }
        if (v->sh_left <= 0) {
            v->filter_left = 0;
            return 1;
        }
    }
    return 0;
}

static int whole_records(const unsigned char *b, size_t n) {
    size_t i = 0;
    while (i < n) {
        if (n - i < 5 || b[i] != 0x17 || b[i + 1] != 0x03 || b[i + 2] != 0x03) return 0;
        size_t len = ((size_t)b[i + 3] << 8) | b[i + 4];
        i += 5;
        if (!len || n - i < len) return 0;
        i += len;
    }
    return 1;
}

static size_t reshape_at(const unsigned char *b, size_t n) {
    if (n < VISION_BUF - 21) return n;
    size_t i = n - 3;
    for (;;) {
        if (b[i] == 0x17 && b[i + 1] == 0x03 && b[i + 2] == 0x03) break;
        if (i == 0) return VISION_BUF / 2;
        i--;
    }
    return i < 21 || i > VISION_BUF - 21 ? VISION_BUF / 2 : i;
}

static int put_frame(struct vision *v, unsigned char *out, size_t cap, size_t *w, int cmd,
                     const unsigned char *b, size_t bn, int lng) {
    long pad;
    if (bn < PAD_SHORT_THRESHOLD && lng)
        pad = (long)rnd_below(PAD_LONG_RANGE) + PAD_LONG_BASE - (long)bn;
    else
        pad = (long)rnd_below(PAD_SHORT_RANGE);
    if (pad > (long)VISION_BUF - 21 - (long)bn) pad = (long)VISION_BUF - 21 - (long)bn;
    size_t head = (v->need_uuid ? 16u : 0u) + 5u;
    if (cap - *w < head + bn + (size_t)pad) return -1;
    unsigned char *o = out + *w;
    if (v->need_uuid) {
        memcpy(o, v->uuid, 16);
        o += 16;
        v->need_uuid = 0;
    }
    o[0] = (unsigned char)cmd;
    o[1] = (unsigned char)(bn >> 8);
    o[2] = (unsigned char)bn;
    o[3] = (unsigned char)((unsigned long)pad >> 8);
    o[4] = (unsigned char)pad;
    memcpy(o + 5, b, bn);
    memset(o + 5 + bn, 0, (size_t)pad);
    *w += head + bn + (size_t)pad;
    v->sent_frames++;
    return 0;
}

int vision_wrap(struct vision *v, const unsigned char *d, size_t n, int may_direct,
                unsigned char *out, size_t cap, size_t *out_n) {
    struct vision save;
    memcpy(&save, v, sizeof save);
    *out_n = 0;
    if (vision_filtering(v))
        for (size_t o = 0; o < n; o += VISION_BUF)
            if (vision_filter(v, d + o, n - o < VISION_BUF ? n - o : VISION_BUF)) break;
    if (!v->is_padding) return VISION_PLAIN;
    const int xtls = may_direct && v->enable_xtls;
    const int ender = xtls ? VISION_CMD_DIRECT : VISION_CMD_END;
    const int complete = whole_records(d, n);
    int lng = v->is_tls;
    size_t w = 0;
    for (size_t s = 0; s < n; s += VISION_BUF) {
        size_t sn = n - s < VISION_BUF ? n - s : VISION_BUF;
        size_t cut = reshape_at(d + s, sn);
        for (int h = 0; h < 2; h++) {
            const unsigned char *b = d + s + (h ? cut : 0);
            size_t bn = h ? sn - cut : cut;
            if (!bn) continue;
            const int last = b + bn == d + n;
            int cmd, frame_lng = lng;
            if (v->is_tls && bn >= 6 && b[0] == 0x17 && b[1] == 0x03 && b[2] == 0x03 && complete) {
                if (xtls) v->tx_switch = 1;
                cmd = last ? ender : VISION_CMD_CONTINUE;
                frame_lng = 1;
                v->is_padding = 0;
                lng = 0;
            } else if (!v->tls12_or_above && v->filter_left <= 1) {
                v->is_padding = 0;
                if (put_frame(v, out, cap, &w, VISION_CMD_END, b, bn, lng) != 0) goto efit;
                size_t rest = (size_t)(d + n - (b + bn));
                if (cap - w < rest) goto efit;
                memcpy(out + w, b + bn, rest);
                w += rest;
                *out_n = w;
                return 0;
            } else {
                cmd = last && !v->is_padding ? ender : VISION_CMD_CONTINUE;
            }
            if (put_frame(v, out, cap, &w, cmd, b, bn, frame_lng) != 0) goto efit;
        }
    }
    *out_n = w;
    return 0;
efit:
    memcpy(v, &save, sizeof *v);
    return VISION_EFIT;
}

static void frame_done(struct vision *v) {
    v->recv_done = 1;
    v->recv_direct = v->rx_direct_after;
}

/* Unwraps the incoming stream. Each call consumes part of in (*consumed) and returns at most
 * one piece of payload; the caller calls again until in is used up.
 *
 * The server answers with the same frames until `end`; after it the stream is plain. State is
 * kept in v because TLS record and Vision frame boundaries need not coincide: a frame may span
 * several calls. */
int vision_unwrap(struct vision *v, const unsigned char *in, size_t n,
                  size_t *consumed, const unsigned char **payload, size_t *payload_n) {
    *consumed = 0;
    *payload = NULL;
    *payload_n = 0;
    if (!n) return 0;

    if (v->recv_done) {
        *consumed = n;
        *payload = in;
        *payload_n = n;
        return 0;
    }

    /* The server's first frame also starts with the UUID, just like ours: it marks the start
     * of the wrapped stream in both directions. */
    if (!v->recv_uuid_seen) {
        /* Collect the start of the stream byte by byte, like the frame header below: a short
         * read is a normal state, not an error. */
        while (v->rx_pre_n < sizeof(v->rx_pre) && *consumed < n)
            v->rx_pre[v->rx_pre_n++] = in[(*consumed)++];
        if (v->rx_pre_n < sizeof(v->rx_pre)) return 0;   /* the rest comes in a later call */

        if (memcmp(v->rx_pre, v->uuid, 16) != 0) {
            /* Not our UUID: the stream is not wrapped at all, which is legal. The collected
             * bytes are returned from rx_pre, since they are no longer contiguous with in;
             * the caller passes the rest of this record in the next call, under recv_done. */
            v->recv_done = 1;
            *payload = v->rx_pre;
            *payload_n = v->rx_pre_n;
            return 0;
        }
        v->recv_uuid_seen = 1;
        /* The 5 bytes after the UUID are the first frame header: put them into rx_hdr so the
         * header has a single parse path below. */
        memcpy(v->rx_hdr, v->rx_pre + 16, 5);
        v->rx_hdr_n = 5;
        in += *consumed;
        n -= *consumed;
    }

    /* Streaming from here on: a frame's length may cover more data than one TLS record
     * carries, which is common on any transfer of more than a couple of kilobytes. Dropping a
     * partial frame would lose sync for the rest of the stream. */
    size_t i = 0;

    if (v->rx_data_left) {
        size_t take = v->rx_data_left < n - i ? v->rx_data_left : n - i;
        *payload = in + i;
        *payload_n = take;
        v->rx_data_left -= (uint32_t)take;
        i += take;
        *consumed += i;
        if (!v->rx_data_left && !v->rx_pad_left && v->rx_end_after) frame_done(v);
        return 0;
    }

    /* Padding is discarded. */
    if (v->rx_pad_left) {
        size_t take = v->rx_pad_left < n - i ? v->rx_pad_left : n - i;
        v->rx_pad_left -= (uint32_t)take;
        i += take;
        *consumed += i;
        if (!v->rx_pad_left && v->rx_end_after) frame_done(v);
        return 0;
    }

    /* The next frame header, byte by byte too: a record boundary may split it. */
    while (v->rx_hdr_n < 5 && i < n) v->rx_hdr[v->rx_hdr_n++] = in[i++];
    *consumed += i;
    if (v->rx_hdr_n < 5) return 0;              /* the rest comes in a later call */

    unsigned char cmd = v->rx_hdr[0];
    if (cmd > VISION_CMD_DIRECT) return VISION_EPROTO;
    v->rx_data_left = ((uint32_t)v->rx_hdr[1] << 8) | v->rx_hdr[2];
    v->rx_pad_left = ((uint32_t)v->rx_hdr[3] << 8) | v->rx_hdr[4];
    v->rx_end_after = (cmd == VISION_CMD_END || cmd == VISION_CMD_DIRECT);
    v->rx_direct_after = cmd == VISION_CMD_DIRECT;
    v->rx_hdr_n = 0;
    /* A frame with neither payload nor padding: the server ends padding this way. */
    if (!v->rx_data_left && !v->rx_pad_left && v->rx_end_after) frame_done(v);
    return 0;
}
