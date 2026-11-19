/* A minimal HTTP/2 client, one stream at a time. Why it exists and its limits: h2.c. */
#ifndef STEER_H2_H
#define STEER_H2_H
#include <stdint.h>
#include <stddef.h>

#define H2_EIO      (-50)
#define H2_EPROTO   (-51)   /* a frame that cannot occur here */
#define H2_ESTATUS  (-52)   /* the server did not answer 200 */
#define H2_ERESET   (-53)   /* RST_STREAM, GOAWAY, end of stream, or a flow-control error */
#define H2_ETOOBIG  (-54)
#define H2_EWINDOW  (-55)   /* the send window is closed */
#define H2_EFULL    (-60)
#define H2_EFLOW    (-61)

/* The I/O below us. Not direct tls13_* calls: the transport may run without TLS
 * (security=none), and HTTP/2 does not care. */
struct h2_io {
    void *ctx;
    int (*write)(void *ctx, const unsigned char *d, size_t n);
    int (*read)(void *ctx, unsigned char *d, size_t cap, size_t *got);
};

/* Kept SMALL on purpose: one per gRPC connection, and a loop thread holds hundreds of
 * connections. There is no frame buffer: records are read into a
 * per-thread buffer, and only what cannot be parsed at once carries over between calls: a split
 * frame header, a control frame body and the count of unread body. A 16 KB frame buffer per
 * connection would add up to megabytes. */
struct h2 {
    struct h2_io io;
    int started;
    int status;                 /* 200; 0 — not known yet; -1 — could not parse */
    int done;                   /* END_STREAM from the server */

    /* A frame header cut off by the end of the previous record. */
    unsigned char pend[9];
    size_t pend_n;

    /* The current frame: body bytes left, type and flags. */
    uint32_t frame_left;
    unsigned char frame_type;
    unsigned char frame_flags;
    int frame_ours;             /* the frame belongs to our stream */
    uint32_t frame_sid;         /* the frame's stream: 0 the connection, sid ours */
    /* An end (RST_STREAM, GOAWAY, a parse failure) found by h2_read after it had already
     * gathered data in the same call. The data is returned (code 0) and this code by the next
     * call: callers look at the code before got, and Xray sends a response's last data, the
     * closing HEADERS and RST_STREAM(NO_ERROR) in one TLS record. 0: none. */
    int pend_err;
    char why[56];               /* how the server ended the stream: "RST_STREAM CANCEL" ... */
    /* Framing inside DATA and HEADERS bodies that is not data (RFC 7540 §6.1, §6.2):
     * pad_wait — the pad length byte is still ahead (PADDED); skip_left — HEADERS priority
     * bytes left to skip (PRIORITY); pad_left — padding length at the end of the frame. They
     * carry over between calls: a TLS record boundary can fall anywhere. */
    unsigned char pad_wait;
    unsigned char pad_left;
    unsigned char skip_left;

    /* The body of a control frame is collected here: WINDOW_UPDATE, SETTINGS, PING and RST
     * must be seen WHOLE to act on them, and TLS record and HTTP/2 frame boundaries do not line
     * up. They are all short: 64 bytes hold ten settings. */
    unsigned char ctl[64];
    unsigned char ctl_n;

    /* Flow control. Our receive window is refilled as we read; the SEND window belongs to the
     * server, and overrunning it earns a RST_STREAM.
     *
     * Bytes read since the last WINDOW_UPDATE. TWO counters, because there are two receive
     * windows: the stream's and the connection's (RFC 7540 §6.9.1).
     *
     * The stream counter gets only what came on our stream.
     *
     * The connection counter gets EVERYTHING, including frames of other streams: the bytes came
     * out of the shared window whoever they were for, and it is on us to return them. Otherwise
     * the window announced to the server shrinks toward zero and the server eventually goes
     * silent. */
    int32_t recv_credit;
    int32_t recv_credit_conn;
    int32_t send_win;           /* the stream window the server gave */
    int32_t send_win_conn;      /* the connection window */
    /* The previous SETTINGS_INITIAL_WINDOW_SIZE. RFC 7540 §6.9.2 shifts the window by the
     * difference from the PREVIOUS value, not from 65535; otherwise a server sending the same
     * SETTINGS twice would shift it twice. Starts at 65535, set by h2_start_req. */
    int32_t peer_init_win;

    /* Our stream id: one stream per connection, so no windows need scheduling between streams.
     * Frames of any other stream are recognized by id and dropped (their DATA still counts
     * toward the connection window); see the frame header parsing in h2_read. */
    uint32_t sid;
};

/* The browser looks of request headers: xhttp (h2w_go_fields in h2wire.c) and the Upgrade
 * request of ws and httpupgrade (proto/transport/trupgrade.c). One place for both, so the
 * versions cannot differ between transports.
 *
 * THE VERSIONS ARE FIXED, NOT DERIVED FROM THE CLOCK. Xray derives them from the date
 * (common/utils/browser.go), but a router with a wrong clock (common without an RTC battery)
 * would then claim a browser from the future or from years ago, which stands out more than a
 * slightly old version. Update them with the rest of the fingerprint when the ClientHello is
 * updated. */
#define UA_CHROME_MAJOR "149"
#define UA_CHROME \
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " \
    "Chrome/" UA_CHROME_MAJOR ".0.0.0 Safari/537.36"
#define UA_CH_CHROME \
    "\"Google Chrome\";v=\"" UA_CHROME_MAJOR "\", \"Chromium\";v=\"" UA_CHROME_MAJOR \
    "\", \"Not)A;Brand\";v=\"24\""
#define UA_EDGE UA_CHROME "Edg/" UA_CHROME_MAJOR ".0.0.0"
#define UA_CH_EDGE \
    "\"Microsoft Edge\";v=\"" UA_CHROME_MAJOR "\", \"Chromium\";v=\"" UA_CHROME_MAJOR \
    "\", \"Not)A;Brand\";v=\"24\""
#define UA_FIREFOX \
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:153.0) Gecko/20100101 Firefox/153.0"
#define UA_SAFARI \
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) " \
    "Version/26.6 Safari/605.1.15"
#define UA_CURL "curl/8.20.0"

/* The room a h2_read caller must give. A TLS record carries up to 16384 bytes, all of which
 * may be DATA body, plus a carried-over header fragment. A smaller buffer would mean
 * H2_ETOOBIG on a perfectly legal frame. */
#define H2_MIN_READ_CAP (16384 + 16)

/* Opens a stream with an HTTP/2 CONNECT request (RFC 7540 §8.3): :method = CONNECT and
 * :authority = authority, without :scheme or :path. */
int h2_start_connect(struct h2 *h, const struct h2_io *io, const char *authority,
                     const char *proxy_auth, const char *user_agent, int end_stream);

/* Opens a stream with the gRPC headers: preface, SETTINGS, WINDOW_UPDATE, HEADERS. content_type
 * and referer may be NULL; gRPC sends no referer. */
int h2_start(struct h2 *h, const struct h2_io *io, const char *authority,
             const char *path, const char *content_type, const char *referer);

struct h2_field { const char *name; const char *value; };

enum h2_look { H2L_CHROME, H2L_EDGE, H2L_FIREFOX, H2L_SAFARI, H2L_CURL, H2L_GOLANG, H2L_NONE };

struct h2_req {
    const char *authority, *path, *content_type;
    const char *method;
    const struct h2_field *fields;
    size_t fields_n;
    int end_stream, look;
    int64_t body_len;
};

/* Sends the data in DATA frames, all or nothing (H2_EWINDOW); see h2.c. */
int h2_write(struct h2 *h, const unsigned char *d, size_t n);
/* The largest n h2_write takes now: the smaller of the stream and connection windows, 0 if
 * either is closed (or negative, RFC 7540 §6.9.2). */
long h2_room(const struct h2 *h);

/* Reads the response body. cap must be at least H2_MIN_READ_CAP, so that one call returns
 * everything a record brought. 0 with *got == 0 is legal: the record held only control
 * frames. */
int h2_read(struct h2 *h, unsigned char *out, size_t cap, size_t *got);

const char *h2_strerror(int rc);
void h2_note_end(int rc, int status);

#endif
