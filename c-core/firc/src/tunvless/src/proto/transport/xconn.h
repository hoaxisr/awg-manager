#ifndef STEER_XCONN_H
#define STEER_XCONN_H
#include <stddef.h>
#include <stdint.h>

#include "dialer.h"
#include "transport.h"
#include "h2mux.h"

#define XC_EPING   (-62)
#define XC_EGOAWAY (-63)
#define XC_EREFUSED (-64)
#define XC_IDLE    1

#define XC_CHUNK           16384
#define XC_FREE_KEEP       16
#define XC_OUT_HIGH        (256 * 1024)
#define XC_OUT_LOW         (64 * 1024)
#define XC_CTL_MAX         (256 * 1024)
#define XC_CTL_HOLD        (128 * 1024)
#define XC_CTL_RESUME      (64 * 1024)
#define XC_PEND_MAX        (512 * 1024)
#define XC_PEND_STREAM     (64 * 1024)
#define XC_IDLE_MS         300000
#define XC_ORPHAN_MS       30000
#define XC_PING_TIMEOUT_MS 15000
#define XC_KEEPALIVE_MS    45000
#define XC_MSS             1460
#define XC_READ_MAX        (192 * 1024)
#define XC_ENTRIES         512
#define XC_READ_UNIT       8192
#define XC_RX_BUDGET       (16u * 1024 * 1024)
#define XC_RX_HARD         (32u * 1024 * 1024)
#define XC_RX_RESUME       (28u * 1024 * 1024)
#define XC_STALL_MS        30000
#define XC_STALL_NEXT_MS   1000

#define XS_NOWNER UINT16_MAX
#define XC_MARKS  1024

struct xc_wake { uint16_t owner; uint8_t what; int rc; };

struct xconn_scratch {
    unsigned char rbuf[XC_CHUNK];
    unsigned char seal[TLS13_MAX_REC + 5];
    struct xc_wake wq[2 * XC_ENTRIES];
    unsigned wq_n;
    uint32_t mark[XC_MARKS], mark_ep;
};

enum { XC_DIALING, XC_LIVE, XC_DEAD };


struct xc_st {
    struct xc_q rx, pend;
    uint64_t half_at;
    uint32_t sid;
    int code;
    int status;
    uint16_t owner, h2slot;
    uint8_t used, ended, local_end, seen, post, starved, win_wait, up_cur, up_last, reset, pend_end, withheld;
    uint32_t disc;
    uint64_t wu_at, read_ms;
    struct h2m_in in;
};

struct xconn {
    struct xc_ref ref;
    int state, rc, done;
    uint8_t shared, retired;
    struct tr_node node;
    struct tr_link link;
    struct h2m h2;
    uint64_t last_rx_ms, ping_at_ms, idle_since_ms;
    int64_t keepalive_ms;

    uint64_t now_ms;
    struct xconn *prev, *next;
    struct xc_st *st;
    uint16_t st_cap, nstreams, cut_n;
    uint16_t map[H2M_STREAMS];
    struct xc_chunk *stage;
    size_t stage_pay;
    struct xc_q out;
    size_t q_pay, pend_n, q_ctl, stage_ctl;
    int64_t held_n;
    uint8_t in_dial, listed, oom, discard, pend_due, nown_due, paused;
    uint16_t pend_at;
    uint32_t refused;
    uint64_t pt_q, pt_s, out_due;
    unsigned char pre[128];
    size_t pre_n;
};

struct xc_host { void (*wake)(void *arg, uint16_t owner, unsigned what, int rc); void *arg; };

void xc_set_host(const struct xc_host *h);
void xc_pend_set(uint32_t limit);
void xc_budget_set(uint32_t soft, uint32_t hard, uint32_t resume);
int64_t xc_held(void);
uint32_t xc_pend_limit(void);
size_t xc_queued(void);
unsigned xc_free_n(void);

struct xconn *xconn_new(const struct tr_node *n, int64_t keepalive_s, int shared);
void xconn_discard(struct xconn *x);
int  xconn_dial(struct xconn *x, int timeout_s);
int  xconn_dial2(struct xconn *a, struct xconn *b, int timeout_s);
int  xconn_start(struct xconn *x);
int  xconn_adopt(struct xconn *x, uint64_t now_ms);
int  xconn_io(struct xconn *x, uint32_t ev, uint64_t now_ms);
int  xconn_flush(struct xconn *x);
int  xconn_due(struct xconn *x, uint64_t now_ms);
int  xconn_wants_out(const struct xconn *x);
int  xconn_wants_in(const struct xconn *x);
int  xconn_tick(struct xconn *x, uint64_t now_ms);
void xconn_fail(struct xconn *x, int err);
unsigned xconn_streams(const struct xconn *x);
void xconn_free(struct xconn *x);

int  xs_open(struct xconn *x, const struct h2_req *r, uint16_t owner, struct xs *s);
long xs_room(const struct xs *s);
long xc_post_room(const struct xconn *x, size_t n);
int  xc_slot_free(const struct xconn *x);
size_t xc_pend_free(void);
int  xs_send(const struct xs *s, const unsigned char *d, size_t n, int end);
int  xs_send_q(const struct xs *s, struct xc_q *q, size_t n, int end);
int  xs_read(const struct xs *s, unsigned char *out, size_t cap, size_t *got);
int  xs_status(const struct xs *s);
int  xs_ready(const struct xs *s);
int  xs_failed(const struct xs *s);
void xs_close(struct xs *s);
int  xs_wait(const struct xs *s, int timeout_ms);

int    xc_q_push(struct xc_q *q, const unsigned char *d, size_t n);
size_t xc_q_peek(const struct xc_q *q, unsigned char *out, size_t cap);
int    xc_q_copy(struct xc_q *dst, const struct xc_q *src, size_t n);
size_t xc_q_pop(struct xc_q *q, unsigned char *out, size_t cap);
void   xc_q_drop(struct xc_q *q);
size_t xc_q_skip(struct xc_q *q, size_t n);

#endif
