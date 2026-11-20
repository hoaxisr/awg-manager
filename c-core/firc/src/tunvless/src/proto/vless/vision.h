/* XTLS-Vision: padded frames over VLESS. The frame format is described in vision.c. */
#ifndef STEER_VISION_H
#define STEER_VISION_H
#include <stddef.h>
#include <stdint.h>

#define VISION_CMD_CONTINUE 0
#define VISION_CMD_END      1
#define VISION_CMD_DIRECT   2

#define VISION_FILTER_N 8
#define VISION_BUF      8192
#define VISION_PLAIN    1
#define VISION_EFIT   (-3)

/* Never returned: a short read is not an error, the start of the stream is collected in
 * rx_pre. The value stays reserved so that -1 never gets another meaning. */
#define VISION_EAGAIN (-1)
#define VISION_EPROTO (-2)

struct vision {
    unsigned char uuid[16];
    /* The UUID goes only in the first frame: it marks the start of the stream, and repeating
     * it would hand an observer a fixed byte sequence. */
    int need_uuid;
    int recv_uuid_seen;      /* the UUID of the server's first frame is consumed */
    int recv_done;           /* padding is over: the rest of the stream is passed as is */
    /* The server sent direct: it switches to direct copy and writes the destination's stream
     * to the socket as is, not inside its own TLS records. Our TLS ends in the downstream
     * direction, and the reader must know, or it parses foreign records as its own.
     * Xray enables this when it sees TLS 1.3 inside the tunnel (XtlsFilterTls), so on almost
     * any https connection. */
    int recv_direct;

    /* Parsing is streaming: a frame may arrive in several TLS records, and holding whole
     * frames would cost a 128 KB buffer per connection. Only counters carry over between
     * calls. */
    unsigned char rx_hdr[5];
    unsigned char rx_hdr_n;
    /* The start of the server's stream: the 16-byte UUID and the first 5-byte header. It is
     * collected here for the same reason as rx_hdr: a TLS record need not hold it whole, and
     * the caller has no buffer for a partial one. Losing these bytes shifts the UUID compare,
     * the stream is taken as unwrapped and frame headers reach the client as data. */
    unsigned char rx_pre[21];
    unsigned char rx_pre_n;
    uint32_t rx_data_left;   /* payload bytes left in the current frame */
    uint32_t rx_pad_left;    /* padding bytes left in the current frame */
    int rx_end_after;        /* the current frame's command is end or direct */
    int rx_direct_after;

    unsigned long sent_frames;

    int filter_left;
    int32_t sh_left;
    uint16_t cipher;
    uint8_t is_tls, tls12_or_above, enable_xtls;
    uint8_t is_padding;
    uint8_t tx_switch;
};

void vision_init(struct vision *v, const unsigned char uuid[16]);
int vision_wrap(struct vision *v, const unsigned char *d, size_t n, int may_direct,
                unsigned char *out, size_t cap, size_t *out_n);
int vision_unwrap(struct vision *v, const unsigned char *in, size_t n,
                  size_t *consumed, const unsigned char **payload, size_t *payload_n);
int vision_filter(struct vision *v, const unsigned char *b, size_t n);
static inline int vision_filtering(const struct vision *v) { return v->filter_left > 0; }
#endif
