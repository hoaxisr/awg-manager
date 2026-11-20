#include "qcbrutal.h"

#include <string.h>

#define NS_PER_SEC 1000000000ull

void qcb_init(struct qcb *b, uint64_t bps, int no_comp) {
    memset(b, 0, sizeof *b);
    b->bps = bps > QCB_BPS_MAX ? QCB_BPS_MAX : bps ? bps : 1;
    b->ack_q16 = 65536;
    b->no_comp = no_comp != 0;
}

static struct qcb_slot *slot_at(struct qcb *b, uint64_t ts_ns) {
    int64_t s = (int64_t)(ts_ns / NS_PER_SEC);
    struct qcb_slot *sl = &b->slot[s % QCB_SLOTS];
    if (sl->s != s) {
        sl->s = s;
        sl->acked = 0;
        sl->lost = 0;
    }
    b->dirty = 1;
    return sl;
}

void qcb_acked(struct qcb *b, uint64_t ts_ns) { slot_at(b, ts_ns)->acked++; }

void qcb_lost(struct qcb *b, uint64_t ts_ns) { slot_at(b, ts_ns)->lost++; }

void qcb_rate(struct qcb *b, uint64_t ts_ns) {
    if (!b->dirty) return;
    b->dirty = 0;
    b->ack_q16 = 65536;
    if (b->no_comp) return;
    int64_t now = (int64_t)(ts_ns / NS_PER_SEC);
    uint64_t acked = 0, lost = 0;
    for (int i = 0; i < QCB_SLOTS; i++) {
        if (b->slot[i].s < now - (QCB_SLOTS - 1)) continue;
        acked += b->slot[i].acked;
        lost += b->slot[i].lost;
    }
    if (acked + lost < QCB_MIN_SAMPLES) return;
    uint64_t q = acked * 65536 / (acked + lost);
    b->ack_q16 = q < QCB_MIN_ACK_Q16 ? QCB_MIN_ACK_Q16 : (uint32_t)q;
}

void qcb_out(const struct qcb *b, int have_rtt, uint64_t srtt_ns, size_t mds, struct qcb_out *o) {
    uint64_t rate = b->bps * 65536 / b->ack_q16;
    uint64_t srtt_us = srtt_ns / 1000;
    if (srtt_us > QCB_SRTT_MAX_US) srtt_us = QCB_SRTT_MAX_US;
    uint64_t cwnd = QCB_CWND_NO_RTT;
    if (have_rtt) {
        cwnd = rate * srtt_us / 500000;
        if (cwnd < mds) cwnd = mds;
    }
    uint64_t interval = (NS_PER_SEC << 10) / rate;
    uint64_t quantum = rate * 4 / 1000;
    if (quantum < 10ull * mds) quantum = 10ull * mds;
    if (quantum > 65536) quantum = 65536;
    o->cwnd = cwnd;
    o->interval_m = interval;
    o->quantum = (size_t)quantum;
}
