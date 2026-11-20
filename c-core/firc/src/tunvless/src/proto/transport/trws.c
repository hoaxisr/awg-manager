/* The ws transport: the protocol stream in WebSocket frames (RFC 6455) after an Upgrade request
 * (the top layer in transport.h). The Upgrade request and the 101 answer are shared with
 * httpupgrade (trupgrade.c); this file is only the frames.
 *
 * SENDING AS XRAY DOES. The Xray client writes each of its writes to gorilla/websocket as one
 * WriteMessage(BinaryMessage); its write buffer is 4096 bytes (websocket/dialer.go:
 * WriteBufferSize), and gorilla cuts a longer message into 4096-byte frames: the first binary
 * without FIN, then continuations, the last with FIN (messageWriter.flushFrame in conn.go). A
 * capture of Xray 26.3.27 uploading 30 KB shows 4096 without FIN, 4096 with FIN, 34 with FIN, ...
 * Each frame is a separate socket write, so a separate TLS record. This code cuts and writes the
 * same way, one frame per write, so record sizes on the wire match the Xray client instead of
 * giving away another implementation. (Go's dynamic record sizing, small TLS records at the
 * start of a connection, is not repeated: our TLS has its own layout, shared by all transports.)
 *
 * Every frame has its own mask from kernel randomness (RFC 6455, 5.3: the client must mask, and
 * the key must be unpredictable, or a caching intermediary can be poisoned with chosen bytes).
 * Keys come in batches, 64 frames per getrandom: a syscall per 4 KB of upload shows on a router,
 * and a per-thread batch is just as unpredictable.
 *
 * RECEIVING as a stream (tr_ws_parse): the server (Xray: WriteMessage without a buffer) sends a
 * message as one unmasked frame, but the RFC allows fragments, control frames between them, and
 * frames up to 2^63 long, so the parser assumes nothing about boundaries and buffers only a frame
 * header and a control frame's body. RFC violations — a masked server frame, RSV bits without
 * negotiated extensions, a control frame over 125 bytes or fragmented, a continuation outside a
 * message or a new message inside an unfinished one, an unknown opcode — break the connection
 * (TR_EWSFRAME), as in gorilla: the stream after such a frame cannot be followed. Text frames are
 * read as binary: Xray does the same (connection.go reads NextReader without looking at the
 * type), and checking VLESS bytes for UTF-8 makes no sense.
 *
 * ping — a pong with the same body; pong — nothing; close — a close with the same code back
 * (gorilla's default answer) and end of stream. Data that came in the same chunk BEFORE the close
 * is returned, and end of stream comes with the next read.
 *
 * Closing the connection sends our own close 1000, as Xray does (connection.Close), with a
 * non-blocking write, see ws_close.
 *
 * EARLY DATA (Ed > 0, `?ed=N` in the path), byte for byte as Xray on the wire: the Upgrade
 * request waits for the first write; if that write is no longer than Ed it goes in
 * Sec-WebSocket-Protocol (base64url without padding), otherwise as frames after the 101 answer.
 * Until the answer arrives writes are queued and go right after it (ws_write, ws_read): Xray
 * blocks the write in its own goroutine meanwhile, but the tunnel loop must not wait. An Xray
 * server always accepts early data, whatever its own Ed (hub.go reads Sec-WebSocket-Protocol
 * unconditionally). */
#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>

#include "transport.h"
#include "tscratch.h"

#define WS_FRAG 4096               /* send frame size: Xray's gorilla write buffer */

size_t tr_ws_frame(unsigned char *out, size_t cap, int opcode, int fin, const unsigned char key[4],
                   const unsigned char *d, size_t n) {
    size_t h = n > 65535 ? 10 : n > 125 ? 4 : 2;
    if (h + 4 + n > cap || h + 4 + n < n) return 0;
    out[0] = (unsigned char)((fin ? 0x80 : 0) | (opcode & 0x0f));
    if (n > 65535) {
        out[1] = 0x80 | 127;
        for (int i = 0; i < 8; i++) out[2 + i] = (unsigned char)((uint64_t)n >> (56 - 8 * i));
    } else if (n > 125) {
        out[1] = 0x80 | 126;
        out[2] = (unsigned char)(n >> 8);
        out[3] = (unsigned char)n;
    } else {
        out[1] = (unsigned char)(0x80 | n);
    }
    memcpy(out + h, key, 4);
    unsigned char *p = out + h + 4;
    for (size_t i = 0; i < n; i++) p[i] = (unsigned char)(d[i] ^ key[i & 3]);
    return h + 4 + n;
}

/* A control frame has been read in full. */
static int ws_ctl(struct ws_rx *r) {
    switch (r->op) {
    case 9:                                    /* ping */
        r->pong_due = 1;
        r->pong_n = r->ctl_n;
        memcpy(r->pong, r->ctl, r->ctl_n);
        return 0;
    case 10:                                   /* pong answers a ping; we send none */
        return 0;
    case 8:                                    /* close */
        /* A close body is empty, or a code (2 bytes) and a reason. One byte is a violation
         * (RFC 6455, 5.5.1). */
        if (r->ctl_n == 1) return TR_EWSFRAME;
        r->closed = 1;
        r->close_code = r->ctl_n >= 2 ? (uint16_t)((r->ctl[0] << 8) | r->ctl[1]) : 1005;
        return 0;
    default:
        return TR_EWSFRAME;
    }
}

int tr_ws_parse(struct ws_rx *r, const unsigned char *in, size_t n,
                unsigned char *out, size_t cap, size_t *out_n) {
    size_t i = 0, o = 0;
    *out_n = 0;
    while (i < n && !r->closed) {
        if (!r->in_payload) {
            r->hdr[r->hdr_n++] = in[i++];
            if (r->hdr_n < 2) continue;
            unsigned b0 = r->hdr[0], b1 = r->hdr[1];
            /* A server frame must not be masked (RFC 6455, 5.1): the client must close the
             * connection. Checked before the length: the rest of the frame does not matter. */
            if (b1 & 0x80) return TR_EWSFRAME;
            unsigned l7 = b1 & 0x7f;
            size_t need = 2 + (l7 == 126 ? 2 : l7 == 127 ? 8 : 0);
            if (r->hdr_n < need) continue;
            uint64_t len = l7;
            if (l7 == 126) {
                len = ((uint64_t)r->hdr[2] << 8) | r->hdr[3];
            } else if (l7 == 127) {
                len = 0;
                for (int k = 2; k < 10; k++) len = (len << 8) | r->hdr[k];
                if (len >> 63) return TR_EWSFRAME;     /* the length's top bit must be 0 */
            }
            r->hdr_n = 0;
            if (b0 & 0x70) return TR_EWSFRAME;         /* RSV: no extensions negotiated */
            unsigned op = b0 & 0x0f, fin = b0 >> 7;
            if (op & 8) {
                if (op > 10 || !fin || len > 125) return TR_EWSFRAME;
            } else if (op == 0) {
                if (!r->in_msg) return TR_EWSFRAME;
                r->in_msg = (uint8_t)!fin;
            } else if (op == 1 || op == 2) {
                if (r->in_msg) return TR_EWSFRAME;
                r->in_msg = (uint8_t)!fin;
            } else {
                return TR_EWSFRAME;
            }
            r->op = (uint8_t)op;
            r->left = len;
            r->ctl_n = 0;
            if (len) { r->in_payload = 1; continue; }
            if (op & 8) {
                int rc = ws_ctl(r);
                if (rc) return rc;
            }
            continue;
        }
        size_t take = n - i;
        if (take > r->left) take = (size_t)r->left;
        if (r->op & 8) {
            memcpy(r->ctl + r->ctl_n, in + i, take);
            r->ctl_n = (uint8_t)(r->ctl_n + take);
        } else {
            if (o + take > cap) return H2_ETOOBIG;
            memmove(out + o, in + i, take);
            o += take;
        }
        i += take;
        r->left -= take;
        if (!r->left) {
            r->in_payload = 0;
            if (r->op & 8) {
                int rc = ws_ctl(r);
                if (rc) return rc;
            }
        }
    }
    *out_n = o;
    return 0;
}

/* Mask key from a per-thread batch of randomness. */
static int mask_key(unsigned char key[4]) {
    static __thread unsigned char pool[256];
    static __thread unsigned left;
    if (left < 4) {
        if (tr_h1_random(pool, sizeof(pool)) != 0) return -1;
        left = sizeof(pool);
    }
    memcpy(key, pool + sizeof(pool) - left, 4);
    left -= 4;
    return 0;
}

static int ws_control(struct transport *t, int op, const unsigned char *d, size_t n) {
    unsigned char key[4], f[2 + 4 + 125];
    if (mask_key(key) != 0) return TR_EIO;
    size_t fl = tr_ws_frame(f, sizeof(f), op, 1, key, d, n);
    return fl ? tr_link_write(&t->link, f, fl) : TR_EWSFRAME;
}

/* Answer what the parser collected: a pong for a ping, a close for a close. */
static int ws_answer(struct transport *t) {
    struct ws_rx *r = &t->h1.rx;
    int rc = 0;
    if (r->pong_due && !r->closed) {
        r->pong_due = 0;
        rc = ws_control(t, 10, r->pong, r->pong_n);
    }
    if (r->closed && !r->close_sent) {
        r->close_sent = 1;
        unsigned char c[2] = { (unsigned char)(r->close_code >> 8), (unsigned char)r->close_code };
        /* A failed close answer does not matter: the connection is ending anyway. */
        (void)ws_control(t, 8, c, r->close_code == 1005 ? 0 : 2);
    }
    return rc;
}

static int ws_open(struct transport *t, const struct tr_node *n, int timeout_s) {
    return tr_h1_upgrade(t, n, 1, timeout_s);
}

/* One write is one message, in WS_FRAG frames (gorilla's WriteMessage). */
struct ws_scratch { unsigned char fb[WS_FRAG + 14]; };

static int ws_frames(struct transport *t, const unsigned char *d, size_t n) {
    struct ws_scratch *s = ts_get(TS_WS, sizeof(struct ws_scratch));
    if (!s) return TR_EIO;
    unsigned char *fb = s->fb;
    size_t off = 0;
    int op = 2;                                /* binary, then continuation */
    while (off < n) {
        size_t take = n - off > WS_FRAG ? WS_FRAG : n - off;
        unsigned char key[4];
        if (mask_key(key) != 0) return TR_EIO;
        size_t fl = tr_ws_frame(fb, sizeof s->fb, op, off + take == n, key, d + off, take);
        int rc = tr_link_write(&t->link, fb, fl);
        if (rc) return rc;
        off += take;
        op = 0;
    }
    return 0;
}

/* Queue limit before the 101 answer. Above it a write is refused WHOLE, not in part, with
 * H2_EWINDOW: as with a closed HTTP/2 window, the dialer takes it as "nothing sent" and the
 * client retransmits. */
#define WS_QMAX (256 * 1024)

static int q_push(struct h1_state *s, const unsigned char *d, size_t n) {
    size_t need = (size_t)s->q_n + 4 + n;
    if (need > WS_QMAX) return H2_EWINDOW;
    if (need > s->q_cap) {
        size_t cap = s->q_cap ? s->q_cap : 8192;
        while (cap < need) cap *= 2;
        unsigned char *q = realloc(s->q, cap);
        if (!q) return TR_EIO;
        s->q = q;
        s->q_cap = (uint32_t)cap;
    }
    unsigned char *p = s->q + s->q_n;
    p[0] = (unsigned char)(n >> 24); p[1] = (unsigned char)(n >> 16);
    p[2] = (unsigned char)(n >> 8);  p[3] = (unsigned char)n;
    memcpy(p + 4, d, n);
    s->q_n = (uint32_t)need;
    return 0;
}

/* Send the queue as frames, write by write, as Xray would after the 101 answer. */
static int q_flush(struct transport *t) {
    struct h1_state *s = &t->h1;
    size_t off = 0;
    int rc = 0;
    while (!rc && off + 4 <= s->q_n) {
        const unsigned char *p = s->q + off;
        size_t n = ((size_t)p[0] << 24) | ((size_t)p[1] << 16) | ((size_t)p[2] << 8) | p[3];
        rc = ws_frames(t, p + 4, n);
        off += 4 + n;
    }
    free(s->q);
    s->q = NULL;
    s->q_n = s->q_cap = 0;
    return rc;
}

/* Writing by phase (h1_state):
 *
 *   H1_DEFER  the first write with Ed > 0 — Xray's delayDialConn.Write: a write no longer than Ed
 *             goes as early data in the request itself and counts as written; a longer one sends
 *             the request without it, and the write goes as frames after the 101 (here: queued);
 *   H1_WAIT   no 101 yet: Xray blocks the write inside dialWebSocket, here it is queued, and the
 *             read side sends the queue right after the answer. The bytes on the wire are the
 *             same: request, answer, frames. Frames must not go before the answer: gorilla on the
 *             other side drops the connection ("client sent data before handshake is complete");
 *   H1_OPEN   frames at once. */
static int ws_write(struct transport *t, const unsigned char *d, size_t n) {
    struct h1_state *s = &t->h1;
    if (s->phase == H1_OPEN) return ws_frames(t, d, n);
    if (s->phase == H1_WAIT) return q_push(s, d, n);
    /* H1_DEFER: the first write. */
    int early = n <= s->ed;
    int rc = early ? 0 : q_push(s, d, n);
    if (rc) return rc;
    s->resp = calloc(1, sizeof(*s->resp));
    if (!s->resp) return TR_EIO;
    rc = tr_h1_send(t, 1, early ? d : NULL, n);
    if (rc) return rc;
    s->phase = H1_WAIT;
    return 0;
}

/* A whole input chunk goes to the parser, frame bodies go to d. The output is never longer than
 * the input (only frame headers are removed), so a TLS record that fits in d fits parsed too.
 *
 * In H1_WAIT the 101 answer is read first (tr_h1_lazy): a wrong answer fails with its code; once
 * accepted, the queued writes go out as frames, and whatever came after the answer in the same
 * chunk is already frames. In H1_DEFER the server must be silent: no request was sent yet. */
static int ws_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    struct h1_state *s = &t->h1;
    struct ws_rx *r = &s->rx;
    if (r->closed) return TR_ECLOSED;
    size_t on = 0;
    int rc;
    if (s->phase != H1_OPEN) {
        const unsigned char *in = d;
        size_t n = 0;
        if (t->link.plain) {
            ssize_t k = tr_lk_recv(&t->link, d, cap, 0);
            if (k < 0 && errno == EINTR) return 0;
            if (k <= 0) return tr_lk_err(&t->link, k);
            n = (size_t)k;
        } else {
            rc = tls13_read_ref(&t->link.tls, &in, &n);
            if (rc) return rc;
        }
        if (!n) return 0;
        if (s->phase == H1_DEFER) return TR_EWSFRAME;
        size_t used = 0;
        rc = tr_h1_lazy(t, 1, in, n, &used);
        if (rc <= 0) return rc;
        rc = q_flush(t);
        if (rc) return rc;
        rc = tr_ws_parse(r, in + used, n - used, d, cap, &on);
    } else if (s->stash) {
        /* First, what came together with the 101 answer. */
        size_t left = s->stash_n - s->stash_off;
        size_t take = left < cap ? left : cap;
        rc = tr_ws_parse(r, s->stash + s->stash_off, take, d, cap, &on);
        s->stash_off += (uint32_t)take;
        if (s->stash_off >= s->stash_n) tr_h1_free(t);
    } else if (!t->link.plain) {
        const unsigned char *in = NULL;
        size_t n = 0;
        rc = tls13_read_ref(&t->link.tls, &in, &n);
        if (rc) return rc;
        if (!n) return 0;
        rc = tr_ws_parse(r, in, n, d, cap, &on);
    } else {
        /* Bare socket: read straight into d and parse in place. */
        ssize_t k = tr_lk_recv(&t->link, d, cap, 0);
        if (k < 0 && errno == EINTR) return 0;
        if (k <= 0) return tr_lk_err(&t->link, k);
        rc = tr_ws_parse(r, d, (size_t)k, d, cap, &on);
    }
    if (rc) return rc;
    rc = ws_answer(t);
    *got = on;
    if (rc) return rc;
    if (r->closed && !on) return TR_ECLOSED;
    return 0;
}

/* Leftover bytes after the 101 and a deferred end of stream: the tunnel loop must collect both
 * with a read even though the socket may stay silent. */
static int ws_pending(const struct transport *t) {
    return t->h1.stash != NULL || t->h1.rx.closed;
}

/* Closing as Xray's connection.Close does: a close with code 1000 and no reason, then the socket.
 * Only after an accepted 101 (no frames before it), and only if no close has been sent yet (as
 * the answer to the server's close).
 *
 * The socket is non-blocking for this write. Closing runs in the tunnel loop, and the connection
 * may already be dead with a full send buffer: a blocking write would hang until the socket
 * timeout (Xray waits up to 5 seconds, but in the connection's own goroutine). An 8-byte frame
 * always goes into a healthy connection; if it does not, nobody was there to receive it. */
static void ws_close(struct transport *t) {
    struct h1_state *s = &t->h1;
    if (s->upgraded && !s->rx.close_sent && t->link.fd >= 0) {
        s->rx.close_sent = 1;
        int fl = fcntl(t->link.fd, F_GETFL, 0);
        if (fl >= 0 && fcntl(t->link.fd, F_SETFL, fl | O_NONBLOCK) == 0) {
            static const unsigned char c[2] = { 0x03, 0xE8 };
            (void)ws_control(t, 8, c, 2);
        }
    }
    tr_h1_free(t);
}

const struct transport_ops tr_ws = {
    .name = "ws", .alpn = "http/1.1", .zc = 0,
    .open = ws_open, .write = ws_write, .read = ws_read,
    .moved = NULL, .close = ws_close, .pending = ws_pending,
};
