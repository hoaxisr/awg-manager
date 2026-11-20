#ifndef QCBRUTAL_H
#define QCBRUTAL_H

#include <stddef.h>
#include <stdint.h>

#define QCB_SLOTS 5
#define QCB_MIN_SAMPLES 50
#define QCB_MIN_ACK_Q16 52429
#define QCB_CWND_NO_RTT 10240
#define QCB_BPS_MAX (1ull << 37)
#define QCB_SRTT_MAX_US 60000000ull

struct qcb_slot {
    int64_t s;
    uint32_t acked, lost;
};

struct qcb {
    uint64_t bps;
    uint32_t ack_q16;
    uint8_t no_comp, dirty;
    struct qcb_slot slot[QCB_SLOTS];
};

struct qcb_out {
    uint64_t cwnd, interval_m;
    size_t quantum;
};

void qcb_init(struct qcb *b, uint64_t bps, int no_comp);
void qcb_acked(struct qcb *b, uint64_t ts_ns);
void qcb_lost(struct qcb *b, uint64_t ts_ns);
void qcb_rate(struct qcb *b, uint64_t ts_ns);
void qcb_out(const struct qcb *b, int have_rtt, uint64_t srtt_ns, size_t mds, struct qcb_out *o);

#endif
