#ifndef TUNVLESS_NGCC_H
#define TUNVLESS_NGCC_H
#include <stdint.h>

#include <ngtcp2/ngtcp2.h>

struct ngcc_tally {
    uint64_t sent, lost;
};

int ngcc_brutal(ngtcp2_conn *conn, uint64_t bps, int no_comp, ngtcp2_tstamp ts);
uint32_t ngcc_brutal_ack_q16(const ngtcp2_conn *conn);
int ngcc_brutal_no_comp(const ngtcp2_conn *conn);
uint64_t ngcc_pacing_bps(const ngtcp2_conn *conn);
uint64_t ngcc_delivered(const ngtcp2_conn *conn);
const char *ngcc_bbr_state(const ngtcp2_conn *conn, uint64_t *bw);
int ngcc_initcwnd(ngtcp2_conn *conn, uint64_t packets, ngtcp2_tstamp ts);
int ngcc_bbr_blind(ngtcp2_conn *conn);
#ifdef QC_WITH_SERVER
struct ngcc_bbr_view {
    uint64_t longterm, bw_lo, inflight_lo, loss_round_delivered;
    unsigned probe_up_rounds;
    int too_high, loss_in_round, full_bw, startup, drain, down;
};
int ngcc_bbr_view(const ngtcp2_conn *conn, struct ngcc_bbr_view *v);
int ngcc_bbr_poke(ngtcp2_conn *conn, uint64_t longterm, int loss_in_round, uint64_t loss_round_delivered, int drain);
int ngcc_bbr_poke_probe(ngtcp2_conn *conn);
#endif

#endif
