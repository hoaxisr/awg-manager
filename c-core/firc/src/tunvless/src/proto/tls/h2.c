/* A minimal HTTP/2 client: one stream on a connection, a body both ways. The gRPC transport's;
 * xhttp runs on the multi-stream engine (h2mux.c, xconn.c).
 *
 * gRPC is HTTP/2: the server expects the preface, frames and flow control, and without them the
 * connection just hangs.
 *
 * Deliberately NOT here, and why that is fine:
 *
 *   - multiplexing. Every VLESS connection gets its own TCP+TLS (Xray instead caches one gRPC
 *     connection per node);
 *   - HPACK on receive. Response headers are not parsed; only :status is extracted: a static
 *     index, or a literal with an indexed name as digits or Huffman code (a Go server sends
 *     502, 401, 301 this way, see status_huff in h2wire.h). Full HPACK needs a dynamic table, i.e. memory
 *     per connection;
 *   - PUSH_PROMISE. Disabled in SETTINGS, so it cannot arrive;
 *   - priorities and trailers. The first decides nothing, the second only closes the stream.
 *
 * Memory: one state per VLESS connection, and a loop thread holds hundreds of connections (MAX_CONNS in stack.c), so there is no frame buffer.
 * Records are read into a per-thread buffer, and only what cannot be parsed at once carries
 * over between calls: a split frame header (9 bytes), a control frame body (64) and the count
 * of unread body. A 16 KB frame buffer per connection would add up to megabytes.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <strings.h>
#include <sys/random.h>

#include "h2.h"
#include "h2wire.h"
#include "tls13.h"
#include "tscratch.h"

/* How the server last ended a stream, for h2_strerror. Per thread, like g_last_status. */
static __thread char g_reset_why[64];

struct wbuf { unsigned char *p; size_t n, cap; };

#define H2_MAX_BLOCK DEFAULT_MAX_FRAME

struct h2_scratch {
    unsigned char one[9 + DEFAULT_MAX_FRAME];
    unsigned char req_out[H2_PREFACE_LEN + 9 + H2_MAX_BLOCK];
    unsigned char rec[TLS13_MAX_PLAIN + sizeof(((struct h2 *)0)->pend)];
};

static struct h2_scratch *scratch(void) {
    return ts_get(TS_H2, sizeof(struct h2_scratch));
}

/* ---- frames ------------------------------------------------------------------ */
static int frame_out(struct h2 *h, unsigned char type, unsigned char flags, uint32_t sid,
                     const unsigned char *body, size_t n) {
    unsigned char hdr[9];
    hdr[0] = (unsigned char)(n >> 16); hdr[1] = (unsigned char)(n >> 8); hdr[2] = (unsigned char)n;
    hdr[3] = type;
    hdr[4] = flags;
    put32(hdr + 5, sid);
    /* Header and body in one write: split across TLS records they make a recognizable length
     * pattern (9 + N, 9 + N, …), the very thing Vision exists to remove. */
    if (n > DEFAULT_MAX_FRAME) return H2_ETOOBIG;
    struct h2_scratch *s = scratch();
    if (!s) return H2_EIO;
    memcpy(s->one, hdr, 9);
    if (n) memcpy(s->one + 9, body, n);
    return h->io.write(h->io.ctx, s->one, 9 + n);
}

/* HEADERS of the request. Pseudo-headers must come before the other fields (RFC 7540
 * §8.1.2.1). */
static int put_headers(struct h2 *h, struct wbuf *b, const struct h2_req *r) {
    size_t at = b->n;
    if (at + 9 > b->cap) return H2_ETOOBIG;
    size_t room = b->cap - at - 9;
    size_t n = h2w_headers(b->p + at + 9, room < H2_MAX_BLOCK ? room : H2_MAX_BLOCK, r);
    if (!n) return H2_ETOOBIG;

    /* END_STREAM as the caller asks. A long-lived stream has none: the request body is the
     * upstream channel and lives as long as the connection. A download GET has no body, and
     * the server waits for the close right here. END_HEADERS always: the headers are short and
     * need no CONTINUATION. */
    unsigned char *hdr = b->p + at;
    hdr[0] = (unsigned char)(n >> 16); hdr[1] = (unsigned char)(n >> 8);
    hdr[2] = (unsigned char)n;
    hdr[3] = FR_HEADERS;
    hdr[4] = (unsigned char)(FLAG_END_HEADERS | (r->end_stream ? FLAG_END_STREAM : 0));
    put32(hdr + 5, h->sid);
    b->n = at + 9 + n;
    return 0;
}

static int h2_start_req(struct h2 *h, const struct h2_io *io, const struct h2_req *r) {
    memset(h, 0, sizeof(*h));
    h->io = *io;
    h->sid = 1;                          /* a client's first stream: the first odd id */
    h->send_win = 65535;                 /* the default until the server's SETTINGS */
    h->send_win_conn = 65535;
    h->peer_init_win = 65535;            /* the same default; shifts count from it */

    struct h2_scratch *s = scratch();
    if (!s) return H2_EIO;
    struct wbuf b = { s->req_out, 0, sizeof s->req_out };
    b.n = h2w_preface(s->req_out, sizeof s->req_out, 1);

    int rc = put_headers(h, &b, r);
    if (rc) return rc;

    if (b.n > sizeof s->req_out) return H2_ETOOBIG;
    /* All in one write: preface, SETTINGS, WINDOW_UPDATE and the request leave together, as a
     * browser sends them; separately they would make a run of records of telltale lengths.
     *
     * The answer is NOT awaited. The server will send its SETTINGS and HEADERS, but waiting
     * for them would add a round trip before the first data byte, and HTTP/2 allows sending
     * data at once. The status shows on the first read. */
    h->started = 1;
    return h->io.write(h->io.ctx, s->req_out, b.n);
}

int h2_start(struct h2 *h, const struct h2_io *io, const char *authority,
             const char *path, const char *content_type, const char *referer) {
    const struct h2_field ref = { "referer", referer };
    const struct h2_req r = { .authority = authority, .path = path,
                              .content_type = content_type, .method = "POST",
                              .fields = referer ? &ref : NULL, .fields_n = referer ? 1 : 0 };
    return h2_start_req(h, io, &r);
}

static void c_wb(struct wbuf *b, const void *d, size_t n) {
    if (b->n + n <= b->cap) memcpy(b->p + b->n, d, n);
    b->n += n;
}
static void c_wb8(struct wbuf *b, unsigned v) {
    unsigned char c = (unsigned char)v;
    c_wb(b, &c, 1);
}
static void c_hp_int(struct wbuf *b, unsigned prefix, unsigned bits, uint32_t v) {
    uint32_t max = (1u << bits) - 1;
    if (v < max) { c_wb8(b, prefix | v); return; }
    c_wb8(b, prefix | max);
    v -= max;
    while (v >= 128) { c_wb8(b, (v & 0x7F) | 0x80); v >>= 7; }
    c_wb8(b, v);
}
static void c_hp_str(struct wbuf *b, const char *s, size_t n) {
    c_hp_int(b, 0x00, 7, (uint32_t)n);
    c_wb(b, s, n);
}
static void c_hp_field(struct wbuf *b, unsigned name_index, const char *value) {
    c_hp_int(b, 0x00, 4, name_index);
    c_hp_str(b, value, strlen(value));
}
static void c_hp_new(struct wbuf *b, const char *name, const char *value) {
    c_wb8(b, 0x00);
    c_hp_str(b, name, strlen(name));
    c_hp_str(b, value, strlen(value));
}

static void c_hp_noindex(struct wbuf *b, const char *name, const char *value, size_t vlen) {
    c_wb8(b, 0x10);
    c_hp_str(b, name, strlen(name));
    c_hp_str(b, value, vlen);
}

int h2_start_connect(struct h2 *h, const struct h2_io *io, const char *authority,
                     const char *proxy_auth, const char *user_agent, int end_stream) {
    memset(h, 0, sizeof(*h));
    h->io = *io;
    h->sid = 1;
    h->send_win = 65535;
    h->send_win_conn = 65535;
    h->peer_init_win = 65535;

    struct h2_scratch *s = scratch();
    if (!s) return H2_EIO;
    struct wbuf b = { s->req_out, 0, sizeof s->req_out };
    b.n = h2w_preface(s->req_out, sizeof s->req_out, 1);
    {
        unsigned char hb[2048];
        struct wbuf hp = { hb, 0, sizeof(hb) };
        /* RFC 7540 §8.3: CONNECT has ONLY :method and :authority pseudo-headers (:scheme and :path
         * MUST be omitted). Static table: 2 = :method, 1 = :authority, 58 = user-agent. */
        c_hp_field(&hp, 2, "CONNECT");
        c_hp_field(&hp, 1, authority ? authority : "");
        if (proxy_auth && proxy_auth[0]) c_hp_new(&hp, "proxy-authorization", proxy_auth);
        if (user_agent && user_agent[0]) {
            c_hp_field(&hp, 58, user_agent);
            if (!strncmp(user_agent, "Chrome/", 7)) {
                static const char npad[] = "0123456789abcdef0123456789abcdef";
                c_hp_noindex(&hp, "padding", npad, sizeof(npad) - 1);
            }
        }
        if (hp.n > sizeof(hb)) return H2_ETOOBIG;

        unsigned char hdr[9];
        h2w_frame_hdr(hdr, (uint32_t)hp.n, FR_HEADERS,
                      (unsigned char)(FLAG_END_HEADERS | (end_stream ? FLAG_END_STREAM : 0)), h->sid);
        c_wb(&b, hdr, 9);
        c_wb(&b, hb, hp.n);
    }
    if (b.n > sizeof s->req_out) return H2_ETOOBIG;
    h->started = 1;
    return h->io.write(h->io.ctx, s->req_out, b.n);
}

/* Refills the receive windows when enough has accumulated. Both levels: connection and stream
 * are counted apart, and forgetting one stalls at its limit. */
static int window_refill(struct h2 *h) {
    unsigned char wu[4];
    /* Each level is checked and refilled by ITS OWN count: the connection's is larger, since it
     * includes frames of closed streams. One number for both would refill the connection
     * window by less than was spent. */
    if (h->recv_credit >= WINDOW_REFILL) {
        put32(wu, (uint32_t)h->recv_credit);
        int rc = frame_out(h, FR_WINDOW_UPDATE, 0, h->sid, wu, 4);
        if (rc) return rc;
        h->recv_credit = 0;
    }
    if (h->recv_credit_conn >= WINDOW_REFILL) {
        put32(wu, (uint32_t)h->recv_credit_conn);
        int rc = frame_out(h, FR_WINDOW_UPDATE, 0, 0, wu, 4);
        if (rc) return rc;
        h->recv_credit_conn = 0;
    }
    return 0;
}


/* The name of an HTTP/2 error code (RFC 9113 §7), for the log: CANCEL (the server finished its
 * handler), FLOW_CONTROL_ERROR (we overran the window) and ENHANCE_YOUR_CALM (too many PINGs) are
 * different problems. */
static const char *h2_err_name(uint32_t code) {
    static const char *const n[] = {
        "NO_ERROR", "PROTOCOL_ERROR", "INTERNAL_ERROR", "FLOW_CONTROL_ERROR", "SETTINGS_TIMEOUT",
        "STREAM_CLOSED", "FRAME_SIZE_ERROR", "REFUSED_STREAM", "CANCEL", "COMPRESSION_ERROR",
        "CONNECT_ERROR", "ENHANCE_YOUR_CALM", "INADEQUATE_SECURITY", "HTTP_1_1_REQUIRED",
    };
    return code < sizeof(n) / sizeof(n[0]) ? n[code] : "unknown code";
}

/* Record how the stream ended and return H2_ERESET. Kept in the connection's state: other
 * connections of the same loop thread may be read before the code reaches the caller. */
static int h2_reset(struct h2 *h, const char *what, uint32_t code) {
    snprintf(h->why, sizeof(h->why), "%s %s", what, h2_err_name(code));
    return H2_ERESET;
}

/* Handles a control frame whose body is complete. */
static int ctl_handle(struct h2 *h) {
    switch (h->frame_type) {
        case FR_SETTINGS:
            if (h->frame_flags & FLAG_ACK) return 0;
            /* INITIAL_WINDOW_SIZE shifts the SEND window of the open stream by the difference
             * (RFC 7540 §6.9.2); without it we either waste the window or overrun it. */
            for (size_t i = 0; i + 6 <= h->ctl_n; i += 6) {
                unsigned id = ((unsigned)h->ctl[i] << 8) | h->ctl[i + 1];
                uint32_t v = get32(h->ctl + i + 2);
                /* The server's MAX_FRAME_SIZE is ignored on purpose: it cannot be below
                 * 16384, and we never send more, which is legal under any setting. Larger
                 * frames would gain nothing and need a larger send buffer. */
                if (id == 0x04) {
                    /* Only the STREAM window shifts; SETTINGS never change the connection
                     * window, only WINDOW_UPDATE does. The difference is from the PREVIOUS
                     * value, not from 65535: a repeated identical SETTINGS must not shift the
                     * window again. */
                    if (v > 0x7FFFFFFFu) return H2_ERESET;
                    int64_t w = (int64_t)h->send_win + ((int64_t)v - h->peer_init_win);
                    if (w > 0x7FFFFFFF || w < -0x7FFFFFFF) return H2_ERESET;
                    h->send_win = (int32_t)w;
                    h->peer_init_win = (int32_t)v;
                }
            }
            return frame_out(h, FR_SETTINGS, FLAG_ACK, 0, NULL, 0);

        case FR_PING:
            if (h->frame_flags & FLAG_ACK) return 0;
            /* Must be answered: the server sends PING as a keep-alive and takes silence for a
             * dead connection. */
            return frame_out(h, FR_PING, FLAG_ACK, 0, h->ctl, h->ctl_n);

        case FR_WINDOW_UPDATE: {
            if (h->ctl_n < 4) return 0;
            /* One for another stream opens nothing of ours: adding it to the connection window
             * would overrun the server's. */
            if (h->frame_sid != 0 && !h->frame_ours) return 0;
            int32_t inc = (int32_t)(get32(h->ctl) & 0x7FFFFFFF);
            /* The 2^31-1 limit is mandatory (RFC 7540 §6.9.1). Adding without the check is
             * signed overflow; in practice the window goes NEGATIVE for good, h2_write answers
             * H2_EWINDOW forever and uploads on this connection stall. One WINDOW_UPDATE of
             * 0x7FFFFFFF from the server is enough. The RFC makes exceeding the limit an
             * error, so the connection is dropped rather than the window clamped: a clamped
             * window would disagree with the server's count and stall anyway, for no visible
             * reason. */
            int64_t w = (int64_t)(h->frame_ours ? h->send_win : h->send_win_conn) + inc;
            if (w > 0x7FFFFFFF) return h2_reset(h, "window past 2^31-1,", 3);
            if (h->frame_ours) h->send_win = (int32_t)w;
            else h->send_win_conn = (int32_t)w;
            return 0;
        }

        case FR_RST_STREAM:
            /* A reset of another stream is not our end. */
            if (!h->frame_ours) return 0;
            return h2_reset(h, "RST_STREAM", h->ctl_n >= 4 ? get32(h->ctl) : 0);

        case FR_GOAWAY: {
            /* The end of the CONNECTION: no new streams after it. The current
             * stream is not necessarily over: last_stream_id says how far the server will go
             * (RFC 9113 §6.8). A Go server closes gracefully with GOAWAY(NO_ERROR, the highest
             * id), then PING and a second GOAWAY, and finishes the streams in progress. The
             * stream ends here only on an error code or when our id is past last_stream_id. */
            uint32_t last = h->ctl_n >= 4 ? get32(h->ctl) & 0x7FFFFFFFu : 0;
            uint32_t code = h->ctl_n >= 8 ? get32(h->ctl + 4) : 0;
            if (code != 0) return h2_reset(h, "GOAWAY", code);
            if (h->sid > last) {
                snprintf(h->why, sizeof(h->why), "GOAWAY: stream not accepted");
                return H2_ERESET;
            }
            return 0;
        }
    }
    return 0;
}

/* The last non-200 status, to name it in the error. Per thread: several threads run
 * connections at once, and a shared one would be overwritten. */
static __thread int g_last_status;

static void status_peek(struct h2 *h, const unsigned char *p, size_t n) {
    int s = h2w_status(p, n);
    h->status = s ? s : -1;
}

/* Stop parsing the record with rc. Data this call has already put in out is RETURNED and rc
 * postponed to the next call (pend_err): callers look at the code before got. */
#define H2_STOP(code) do { int e_ = (code); if (*got) { h->pend_err = e_; goto deliver; } return e_; } while (0)

static int h2_read_in(struct h2 *h, unsigned char *out, size_t cap, size_t *got) {
    *got = 0;
    if (!h->started) return H2_EPROTO;
    if (h->pend_err) {
        int e = h->pend_err;
        h->pend_err = 0;
        h->done = 1;                 /* the same end from now on */
        return e;
    }
    if (h->done) return H2_ERESET;
    /* The contract is checked BEFORE reading. A failure mid-frame (below) would drop the
     * record already read while the body counter stays, and the next frame would be read as a
     * continuation of the old one. Failing here touches neither the network nor the state. */
    if (cap < H2_MIN_READ_CAP) return H2_ETOOBIG;

    /* One buffer per thread, not per connection: 16 KB per connection would add up to
     * megabytes. */
    struct h2_scratch *s = scratch();
    if (!s) return H2_EIO;
    unsigned char *rec = s->rec;
    size_t avail = 0;
    if (h->pend_n) {
        memcpy(rec, h->pend, h->pend_n);
        avail = h->pend_n;
        h->pend_n = 0;
    }

    size_t r = 0;
    int rc = h->io.read(h->io.ctx, rec + avail, sizeof s->rec - avail, &r);
    if (rc) return rc;
    avail += r;

    size_t p = 0;
    while (p < avail) {
        if (h->frame_left) {
            if (h->pad_wait) {
                /* The pad length byte. Padding and priority must fit into the rest of the
                 * frame, or it is a protocol error (RFC 7540 §6.1), not a reason to read past
                 * the end. */
                h->pad_wait = 0;
                if ((uint32_t)rec[p] + h->skip_left > h->frame_left - 1) H2_STOP(H2_EPROTO);
                h->pad_left = rec[p];
                if (h->frame_type == FR_DATA) {
                    h->recv_credit_conn += 1;
                    if (h->frame_ours) h->recv_credit += 1;
                }
                p++;
                h->frame_left--;
            }
            if (h->skip_left) {
                /* HEADERS priority: five bytes we do not need. If not all arrived, the rest
                 * is skipped in the next record, and take below comes out zero. */
                size_t sk = h->skip_left < avail - p ? h->skip_left : avail - p;
                h->skip_left = (unsigned char)(h->skip_left - sk);
                p += sk;
                h->frame_left -= (uint32_t)sk;
            }
            size_t take = h->frame_left < avail - p ? h->frame_left : avail - p;
            /* The content is what precedes the padding; the padding is only skipped. When a
             * record boundary falls inside the padding, frame_left is already below pad_left:
             * no content is left, and the unsigned difference would wrap, putting the rest of
             * the padding into the body. */
            size_t body = h->frame_left > h->pad_left ? h->frame_left - h->pad_left : 0;
            size_t real = take < body ? take : body;
            if (h->frame_type == FR_DATA) {
                /* The CONNECTION window is always credited, even for a frame of another
                 * stream: the bytes came out of the shared window, and it is on us to return
                 * them. Padding counts too: the window pays for the whole frame (RFC 7540
                 * §6.9.1). */
                h->recv_credit_conn += (int32_t)take;
                if (h->frame_ours) {
                    if (*got + real > cap) H2_STOP(H2_ETOOBIG);
                    memcpy(out + *got, rec + p, real);
                    *got += real;
                    h->recv_credit += (int32_t)take;
                }
            } else if (h->frame_type == FR_HEADERS) {
                if (h->frame_ours && h->status == 0 && real) status_peek(h, rec + p, real);
            } else {
                /* A control frame: collect what fits of the body and drop the rest. Nothing
                 * that matters is lost except SETTINGS entries past the tenth; GOAWAY debug data
                 * and unknown frames are never looked at. */
                size_t room = sizeof(h->ctl) - h->ctl_n;
                size_t cp = take < room ? take : room;
                if (cp) memcpy(h->ctl + h->ctl_n, rec + p, cp);
                h->ctl_n = (unsigned char)(h->ctl_n + cp);
            }
            p += take;
            h->frame_left -= (uint32_t)take;
            if (h->frame_left == 0) {
                if (h->frame_type != FR_DATA && h->frame_type != FR_HEADERS) {
                    rc = ctl_handle(h);
                    if (rc) H2_STOP(rc);
                }
                if ((h->frame_flags & FLAG_END_STREAM) && h->frame_ours &&
                    (h->frame_type == FR_DATA || h->frame_type == FR_HEADERS))
                    h->done = 1;
                h->frame_type = 0xFF;
            }
            continue;
        }

        if (avail - p < 9) {
            /* The frame header is split by the record boundary; carry its bytes over. */
            h->pend_n = avail - p;
            memcpy(h->pend, rec + p, h->pend_n);
            break;
        }

        uint32_t len = ((uint32_t)rec[p] << 16) | ((uint32_t)rec[p + 1] << 8) | rec[p + 2];
        h->frame_type = rec[p + 3];
        h->frame_flags = rec[p + 4];
        uint32_t sid = get32(rec + p + 5) & 0x7FFFFFFF;
        /* "Ours" is only our stream. Frames of any other count toward the connection window
         * (the body parsing does that) and are dropped. */
        h->frame_ours = (sid == h->sid);
        h->frame_sid = sid;
        h->frame_left = len;
        h->ctl_n = 0;
        p += 9;
        h->pad_left = 0;
        h->pad_wait = 0;
        h->skip_left = 0;
        if (h->frame_type == FR_DATA || h->frame_type == FR_HEADERS) {
            h->pad_wait = (h->frame_flags & FLAG_PADDED) != 0;
            if (h->frame_type == FR_HEADERS && (h->frame_flags & FLAG_PRIORITY))
                h->skip_left = 5;
            if (h->pad_wait + h->skip_left > len) H2_STOP(H2_EPROTO);
        }

        /* A frame without a body is handled at once: the loop above would wait for bytes
         * that never come. */
        if (len == 0) {
            if (h->frame_type != FR_DATA && h->frame_type != FR_HEADERS) {
                rc = ctl_handle(h);
                if (rc) H2_STOP(rc);
            }
            if ((h->frame_flags & FLAG_END_STREAM) && h->frame_ours &&
                (h->frame_type == FR_DATA || h->frame_type == FR_HEADERS))
                h->done = 1;
            h->frame_type = 0xFF;
        }
    }

deliver:
    if (h->status > 0 && h->status != 200) { g_last_status = h->status; return H2_ESTATUS; }
    if (h->pend_err) return 0;       /* the stream is over: no point growing its window */
    rc = window_refill(h);
    if (rc) return rc;
    /* Zero bytes is a legal result: the record may have held only a PING or SETTINGS. An
     * error here would drop a working connection over a control frame. The caller must tell
     * "nothing to give" from "end of stream", which is why the end comes as a code (H2_ERESET
     * from the next call), not as zero. */
    return 0;
}

int h2_read(struct h2 *h, unsigned char *out, size_t cap, size_t *got) {
    int rc = h2_read_in(h, out, cap, got);
    if (rc == H2_ERESET)
        snprintf(g_reset_why, sizeof(g_reset_why), "%s", h->why[0] ? h->why : "end of stream");
    return rc;
}

/* Sends the data WHOLE or not at all.
 *
 * "Or nothing" is the only option without a buffer. Vision sits above us, and half a frame
 * sent breaks the stream for good. So either we keep the unsent rest (16 KB for each of 64
 * connections), or we send nothing and tell the caller.
 *
 * The latter also suits TCP: a closed window means the server cannot keep up, and the right
 * reaction is NOT to acknowledge the packet to the client. The client retransmits it as after
 * a loss, and no memory is needed. That is why there is no waiting for the window and no
 * reading here: both would mean someone must put already-read data somewhere. */
int h2_write(struct h2 *h, const unsigned char *d, size_t n) {
    if (!h->started) return H2_EPROTO;
    /* The comparison is SIGNED. send_win and send_win_conn are int32_t and may legally go
     * negative: a SETTINGS with INITIAL_WINDOW_SIZE below 65535 subtracts the difference from
     * the window already granted (RFC 7540 §6.9.2). Cast to size_t, -60000 would become
     * 1.8·10^19, the check would never fire, the frame would overrun the window, and the
     * server would answer RST_STREAM with FLOW_CONTROL_ERROR. */
    if (n > INT32_MAX) return H2_ETOOBIG;
    if ((int32_t)n > h->send_win || (int32_t)n > h->send_win_conn) return H2_EWINDOW;

    while (n) {
        size_t chunk = n > DEFAULT_MAX_FRAME ? DEFAULT_MAX_FRAME : n;
        int rc = frame_out(h, FR_DATA, 0, h->sid, d, chunk);
        if (rc) return rc;
        h->send_win -= (int32_t)chunk;
        h->send_win_conn -= (int32_t)chunk;
        d += chunk;
        n -= chunk;
    }
    return 0;
}

long h2_room(const struct h2 *h) {
    if (!h->started) return 0;
    int32_t w = h->send_win < h->send_win_conn ? h->send_win : h->send_win_conn;
    return w > 0 ? w : 0;
}

void h2_note_end(int rc, int status) {
    if (rc == H2_ESTATUS) g_last_status = status;
    if (rc == H2_ERESET) g_reset_why[0] = '\0';
}

const char *h2_strerror(int rc) {
    switch (rc) {
        case H2_EIO: return "HTTP/2 connection lost";
        case H2_EPROTO: return "unexpected HTTP/2 frame";
        /* The CODE is named, not hidden behind "not 200": 404, 403 and 502 mean "wrong path",
         * "not let in" and "nothing behind the server", three different problems to take to
         * the node's owner. */
        case H2_ESTATUS: {
            static __thread char st[64];
            if (g_last_status > 0) snprintf(st, sizeof st, "server answered %d instead of 200", g_last_status);
            else                   snprintf(st, sizeof st, "server answered with a status other than 200");
            return st;
        }
        case H2_ERESET: {
            static __thread char rs[112];
            if (g_reset_why[0]) snprintf(rs, sizeof rs, "stream closed by the server (%s)", g_reset_why);
            else                snprintf(rs, sizeof rs, "stream closed by the server (RST/GOAWAY)");
            return rs;
        }
        case H2_ETOOBIG: return "HTTP/2 frame too large";
        case H2_EWINDOW: return "HTTP/2 window closed";
        case H2_EFULL: return "no free HTTP/2 stream on the connection";
        case H2_EFLOW: return "HTTP/2 flow control window overrun by the server";
        default: return "unknown HTTP/2 error";
    }
}
