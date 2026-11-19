#include "ngtcp2_conn.h"
#include "ngtcp2_cc.h"
#include "ngtcp2_bbr.h"

#include "ngcc.h"
#include "qcbrutal.h"

#if NGTCP2_VERSION_NUM != 0x011a00
#error "ngcc.c is written against the internals of ngtcp2 1.26.0"
#endif

struct ngcc_brutal {
    ngtcp2_cc cc;
    struct qcb b;
};

_Static_assert(sizeof(struct ngcc_brutal) <= sizeof(ngtcp2_cc_bbr), "Brutal state outgrows the controller union");
_Static_assert(_Alignof(struct ngcc_brutal) <= _Alignof(ngtcp2_cc_bbr), "Brutal state needs a stricter alignment");

static struct ngcc_brutal *br_of(ngtcp2_cc *cc) { return ngtcp2_struct_of(cc, struct ngcc_brutal, cc); }

static struct ngcc_tally *tally_of(ngtcp2_cc *cc) { return ngtcp2_struct_of(cc, ngtcp2_conn, cc)->user_data; }

static void count_sent(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt) {
    (void)cstat;
    (void)pkt;
    tally_of(cc)->sent++;
}

static void count_lost(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt, ngtcp2_tstamp ts) {
    (void)cstat;
    (void)pkt;
    (void)ts;
    tally_of(cc)->lost++;
}

static void br_out(struct ngcc_brutal *br, ngtcp2_conn_stat *cstat, ngtcp2_tstamp ts) {
    struct qcb_out o;
    int have_rtt = cstat->first_rtt_sample_ts != UINT64_MAX && cstat->smoothed_rtt >= NGTCP2_MICROSECONDS;
    qcb_rate(&br->b, ts);
    qcb_out(&br->b, have_rtt, cstat->smoothed_rtt, cstat->max_tx_udp_payload_size, &o);
    cstat->cwnd = o.cwnd + 1;
    cstat->pacing_interval_m = o.interval_m;
    cstat->send_quantum = o.quantum;
}

static void on_pkt_acked(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt, ngtcp2_tstamp ts) {
    (void)cstat;
    (void)pkt;
    qcb_acked(&br_of(cc)->b, ts);
}

static void on_pkt_lost(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt, ngtcp2_tstamp ts) {
    (void)cstat;
    (void)pkt;
    tally_of(cc)->lost++;
    qcb_lost(&br_of(cc)->b, ts);
}

static void congestion_event(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, ngtcp2_tstamp sent_ts,
                             const ngtcp2_cc_ack *ack, ngtcp2_tstamp ts) {
    (void)sent_ts;
    (void)ack;
    br_out(br_of(cc), cstat, ts);
}

static void on_ack_recv(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_ack *ack, ngtcp2_tstamp ts) {
    (void)ack;
    br_out(br_of(cc), cstat, ts);
}

static void br_reset(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, ngtcp2_tstamp ts) {
    struct ngcc_brutal *br = br_of(cc);
    qcb_init(&br->b, br->b.bps, br->b.no_comp);
    br_out(br, cstat, ts);
}

int ngcc_brutal(ngtcp2_conn *conn, uint64_t bps, int no_comp, ngtcp2_tstamp ts) {
    if (!conn || !bps) return -1;
    struct ngcc_brutal *br = br_of(&conn->cc);
    br->cc = (ngtcp2_cc){
        .log = &conn->log,
        .on_pkt_acked = on_pkt_acked,
        .on_pkt_lost = on_pkt_lost,
        .on_pkt_sent = count_sent,
        .congestion_event = congestion_event,
        .on_ack_recv = on_ack_recv,
        .reset = br_reset,
    };
    qcb_init(&br->b, bps, no_comp);
    conn->tx.pacing.next_ts = UINT64_MAX;
    conn->tx.pacing.compensation = 0;
    br_out(br, &conn->cstat, ts);
    return 0;
}

uint32_t ngcc_brutal_ack_q16(const ngtcp2_conn *conn) {
    if (!conn || conn->cc.on_pkt_acked != on_pkt_acked) return 0;
    return ((const struct ngcc_brutal *)&conn->cc)->b.ack_q16;
}

int ngcc_brutal_no_comp(const ngtcp2_conn *conn) {
    if (!conn || conn->cc.on_pkt_acked != on_pkt_acked) return 0;
    return ((const struct ngcc_brutal *)&conn->cc)->b.no_comp;
}

uint64_t ngcc_pacing_bps(const ngtcp2_conn *conn) {
    if (!conn || !conn->cstat.pacing_interval_m) return 0;
    return (NGTCP2_SECONDS << 10) / conn->cstat.pacing_interval_m;
}

uint64_t ngcc_delivered(const ngtcp2_conn *conn) { return conn ? conn->rst.delivered : 0; }

static const char *const g_bbr_names[] = { "none", "startup", "drain", "probe_bw_down", "probe_bw_cruise",
                                           "probe_bw_refill", "probe_bw_up", "probe_rtt" };
_Static_assert(sizeof g_bbr_names / sizeof g_bbr_names[0] == NGTCP2_BBR_STATE_PROBE_RTT + 1, "a BBR state without a name");

const char *ngcc_bbr_state(const ngtcp2_conn *conn, uint64_t *bw) {
    if (!conn || conn->cc_algo != NGTCP2_CC_ALGO_BBR || conn->cc.on_pkt_acked) return NULL;
    *bw = conn->bbr.bw;
    return g_bbr_names[conn->bbr.state];
}

int ngcc_initcwnd(ngtcp2_conn *conn, uint64_t packets, ngtcp2_tstamp ts) {
    if (!conn || conn->cc.on_pkt_acked == on_pkt_acked) return -1;
    conn->cstat.cwnd = packets * conn->cstat.max_tx_udp_payload_size;
    if (conn->cc_algo == NGTCP2_CC_ALGO_BBR)
        ngtcp2_cc_bbr_init(&conn->bbr, &conn->log, &conn->cstat, &conn->rst, ts, &conn->pcg);
    else if (conn->cc_algo == NGTCP2_CC_ALGO_RENO) {
        ngtcp2_cc_reno_init(&conn->reno, &conn->log, &conn->cstat);
        conn->cc.on_pkt_sent = count_sent;
        conn->cc.on_pkt_lost = count_lost;
    } else return -1;
    return 0;
}

static ngtcp2_cc_on_pkt_lost g_bbr_lost;
static ngtcp2_cc_on_ack_recv g_bbr_ack;
static ngtcp2_cc_on_pkt_sent g_bbr_sent;

static void blind_lost(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt, ngtcp2_tstamp ts) {
    ngtcp2_cc_bbr *bbr = ngtcp2_struct_of(cc, ngtcp2_cc_bbr, cc);
    int probe = bbr->is_bw_probe_sample;
    bbr->is_bw_probe_sample = 0;
    tally_of(cc)->lost++;
    __atomic_load_n(&g_bbr_lost, __ATOMIC_RELAXED)(cc, cstat, pkt, ts);
    bbr->is_bw_probe_sample = probe;
}

static void blind_ack(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_ack *ack, ngtcp2_tstamp ts) {
    ngtcp2_cc_bbr *bbr = ngtcp2_struct_of(cc, ngtcp2_cc_bbr, cc);
    int loss = bbr->is_loss_in_round;
    ngtcp2_tstamp cs = bbr->cycle_stamp;
    bbr->is_loss_in_round = 0;
    __atomic_load_n(&g_bbr_ack, __ATOMIC_RELAXED)(cc, cstat, ack, ts);
    int down = bbr->cycle_stamp != cs;
    if (!ack->bytes_delivered || (!bbr->loss_round_start && !down)) bbr->is_loss_in_round = loss;
    bbr->inflight_longterm = UINT64_MAX;
}

static void blind_sent(ngtcp2_cc *cc, ngtcp2_conn_stat *cstat, const ngtcp2_cc_pkt *pkt) {
    tally_of(cc)->sent++;
    __atomic_load_n(&g_bbr_sent, __ATOMIC_RELAXED)(cc, cstat, pkt);
}

int ngcc_bbr_blind(ngtcp2_conn *conn) {
    if (!conn || conn->cc_algo != NGTCP2_CC_ALGO_BBR || conn->cc.on_pkt_acked || conn->cc.on_ack_recv == blind_ack) return -1;
    __atomic_store_n(&g_bbr_lost, conn->cc.on_pkt_lost, __ATOMIC_RELAXED);
    __atomic_store_n(&g_bbr_ack, conn->cc.on_ack_recv, __ATOMIC_RELAXED);
    __atomic_store_n(&g_bbr_sent, conn->cc.on_pkt_sent, __ATOMIC_RELAXED);
    conn->cc.on_pkt_lost = blind_lost;
    conn->cc.on_pkt_sent = blind_sent;
    conn->cc.on_ack_recv = blind_ack;
    return 0;
}

#ifdef QC_WITH_SERVER
int ngcc_bbr_view(const ngtcp2_conn *conn, struct ngcc_bbr_view *v) {
    if (!conn || conn->cc_algo != NGTCP2_CC_ALGO_BBR || conn->cc.on_pkt_acked) return -1;
    const ngtcp2_cc_bbr *b = &conn->bbr;
    v->longterm = b->inflight_longterm;
    v->bw_lo = b->bw_shortterm;
    v->inflight_lo = b->inflight_shortterm;
    v->loss_round_delivered = b->loss_round_delivered;
    v->probe_up_rounds = (unsigned)b->bw_probe_up_rounds;
    v->too_high = b->prev_probe_too_high;
    v->loss_in_round = b->is_loss_in_round;
    v->full_bw = b->full_bw_reached;
    v->startup = b->state == NGTCP2_BBR_STATE_STARTUP;
    v->drain = b->state == NGTCP2_BBR_STATE_DRAIN;
    v->down = b->state == NGTCP2_BBR_STATE_PROBE_BW_DOWN;
    return conn->cc.on_ack_recv == blind_ack;
}

int ngcc_bbr_poke(ngtcp2_conn *conn, uint64_t longterm, int loss_in_round, uint64_t loss_round_delivered, int drain) {
    if (!conn || conn->cc_algo != NGTCP2_CC_ALGO_BBR || conn->cc.on_pkt_acked) return -1;
    conn->bbr.inflight_longterm = longterm;
    conn->bbr.is_loss_in_round = loss_in_round;
    conn->bbr.loss_round_delivered = loss_round_delivered;
    if (drain) conn->bbr.state = NGTCP2_BBR_STATE_DRAIN;
    return 0;
}

int ngcc_bbr_poke_probe(ngtcp2_conn *conn) {
    if (!conn || conn->cc_algo != NGTCP2_CC_ALGO_BBR || conn->cc.on_pkt_acked) return -1;
    conn->bbr.is_bw_probe_sample = 1;
    return 0;
}
#endif
