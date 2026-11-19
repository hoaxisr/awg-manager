#ifndef STEER_H2MUX_H
#define STEER_H2MUX_H
#include <stddef.h>
#include <stdint.h>

#include "h2.h"
#include "h2wire.h"
#include "hpack.h"

#define H2M_STREAMS 256
#define H2M_BLOCK_MAX 32768
#define H2M_SEND_SCRATCH 524288

struct h2mux_scratch {
    struct h2w_list list;
    unsigned char frame[9 + DEFAULT_MAX_FRAME];
    unsigned char block[H2M_BLOCK_MAX];
};

#define H2M_CTL 1u
#define H2M_PAY 2u
#define H2M_END 4u

struct h2m_in { uint32_t sid; int32_t avail, unsent, wh; };

struct h2m_cb {
    void *ctx;
    int  (*out)(void *ctx, const unsigned char *d, size_t n, unsigned flags);
    void (*data)(void *ctx, uint16_t slot, const unsigned char *d, size_t n);
    void (*status)(void *ctx, uint16_t slot, int status);
    void (*end)(void *ctx, uint16_t slot, int code, const struct h2m_in *in);
    void (*goaway)(void *ctx, uint32_t last_sid, uint32_t code);
    void (*ping_ack)(void *ctx);
    void (*room)(void *ctx, uint16_t slot);
};

struct h2m_stream {
    uint32_t sid;
    int32_t send_win;
    struct h2m_in in;
    uint8_t state;
};

struct h2m {
    struct h2m_cb cb;
    struct h2m_stream *st;
    uint16_t st_cap;
    uint16_t open_n;
    uint32_t next_sid;
    int32_t send_win_conn;
    struct h2m_in in_conn;
    int32_t peer_init_win;
    uint32_t peer_max_streams, peer_hdr_list;
    uint8_t peer_max_set, peer_settings_seen, peer_hdr_set;
    uint8_t goaway;
    uint32_t goaway_last;
    int dead;

    unsigned char hdr[9];
    uint8_t hdr_n;
    uint32_t f_len, f_left;
    uint32_t f_sid;
    uint16_t f_slot;
    uint8_t f_type, f_flags;
    uint8_t pad_wait, pad_left, skip_left;
    uint32_t f_drop, f_pad;
    uint8_t peek, paid, grew, hold;
    unsigned char ctl[64];
    uint8_t ctl_n;

    uint8_t cont;
    uint8_t cont_end;
    uint32_t cont_sid;

    unsigned char ping_op[8];
    uint8_t ping_out;

    uint32_t peer_max_frame;
    uint64_t rng;
    uint32_t read_before_sid, resets_pending;
    uint8_t rst_ping_block, up_end;
    uint32_t up_sid, up_left, up_frame;
    struct hpe enc;
};

uint32_t h2m_in_add(struct h2m_in *f, uint32_t n);
int  h2m_start(struct h2m *m, const struct h2m_cb *cb, uint64_t seed);
int  h2m_feed(struct h2m *m, const unsigned char *d, size_t n);
int  h2m_open(struct h2m *m, const struct h2_req *r, uint16_t *slot);
long h2m_room(const struct h2m *m, uint16_t slot);
long h2m_conn_room(const struct h2m *m);
int  h2m_send(struct h2m *m, uint16_t slot, const unsigned char *d, size_t n, int end);
int  h2m_send_begin(struct h2m *m, uint16_t slot, size_t n, int end);
int  h2m_send_put(struct h2m *m, const unsigned char *d, size_t k);
int  h2m_reset(struct h2m *m, uint16_t slot, uint32_t code);
void h2m_consumed(struct h2m *m, uint16_t slot, struct h2m_in *ended, uint32_t n, int give);
uint32_t h2m_stream_flush(struct h2m *m, uint16_t slot, struct h2m_in *ended);
void h2m_hold(struct h2m *m, int on);
void h2m_conn_refund(struct h2m *m, uint32_t n);
int  h2m_ping(struct h2m *m, const unsigned char op[8]);
uint32_t h2m_cap(const struct h2m *m);
uint32_t h2m_header_budget(const struct h2m *m, uint32_t server_max);
unsigned h2m_open_n(const struct h2m *m);
void h2m_free(struct h2m *m);

#endif
