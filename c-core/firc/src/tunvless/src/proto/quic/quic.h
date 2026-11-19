/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#ifndef TUNVLESS_QUIC_H
#define TUNVLESS_QUIC_H
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>

#include "portpick.h"

#define QC_EINVAL    (-1)
#define QC_ENOMEM    (-2)
#define QC_ESOCK     (-3)
#define QC_ETLS      (-4)
#define QC_EQUIC     (-5)
#define QC_EAGAIN    (-6)
#define QC_ECLOSED   (-7)
#define QC_ETOOBIG   (-8)
#define QC_ENOSTREAM (-9)

#define QC_CLOSE_LOCAL     0
#define QC_CLOSE_PEER      1
#define QC_CLOSE_IDLE      2
#define QC_CLOSE_HANDSHAKE 3
#define QC_CLOSE_ERROR     4

#define QC_FILTER_RX_MAX 65536

#define QC_CC_BBR    0
#define QC_CC_RENO   1
#define QC_CC_BRUTAL 2

#define QC_HOP_LOCAL  1u
#define QC_HOP_REMOTE 2u
#define QC_HOP_LINGER_MIN_MS 2000

#define QC_WAIT_CWND   0
#define QC_WAIT_CREDIT 1
#define QC_WAIT_BUDGET 2

struct qc;
struct qc_tls;

struct qc_filter {
    size_t (*tx)(void *u, uint8_t *dst, const uint8_t *src, size_t n);
    size_t (*rx)(void *u, uint8_t *dst, const uint8_t *src, size_t n);
    void *user;
    size_t overhead;
};

struct qc_ops {
    void (*on_handshake)(void *u);
    void (*on_stream_data)(void *u, int64_t sid, const uint8_t *d, size_t n, int fin);
    void (*on_stream_writable)(void *u, int64_t sid);
    void (*on_streams_open)(void *u);
    void (*on_stream_peer_abort)(void *u, int64_t sid, int reset, uint64_t rx_err, int stopped, uint64_t tx_err);
    void (*on_stream_close)(void *u, int64_t sid, int rx_reset, uint64_t rx_err, int tx_stopped, uint64_t tx_err);
    void (*on_datagram)(void *u, const uint8_t *d, size_t n);
    void (*on_closed)(void *u, int reason, int app, uint64_t code, const char *why);
};

struct qc_cfg {
    const char *host;
    uint16_t port;
    const char *sni;
    const char *alpn;
    int insecure;
    const uint8_t *pin_sha256;
    size_t pin_n;
    const char *verify_name;
    const uint8_t *ca_pem;
    size_t ca_pem_n;
    const char *ca_file;
    struct qc_tls *tls;
    int (*prepare)(int fd);
    int prefer_chacha;
    int no_batch;
    size_t datagram_max;
    unsigned idle_ms, keepalive_ms, handshake_ms;
    uint64_t max_data, max_stream_data, max_streams;
    uint64_t max_data_win, max_stream_win;
    int no_pmtud;
    int cc;
    size_t send_buf;
    size_t send_total;
    size_t brutal_span;
    int defer_flush;
    const struct qc_filter *filter;
    unsigned hop_mode, hop_min_s, hop_max_s;
    size_t hop_n;
    struct port_range hop_ports[PORT_RANGES_MAX];
    uint32_t (*rand32)(void);
};

struct qc_stats {
    uint64_t rtt_us, cwnd, in_flight, pkt_sent, pkt_lost, pkt_recv, bytes_sent, bytes_recv;
    uint64_t dg_tx, dg_rx, dg_dropped, tx_calls, rx_calls;
    int handshake_done;
    unsigned cipher;
    int gso, gro;
    unsigned rcvbuf, sndbuf;
    int cc;
    uint64_t pacing_bps;
    uint32_t ack_q16;
    int no_loss_comp;
    uint64_t hops, hop_fails;
    uint16_t port;
    uint64_t min_rtt_us, bytes_acked, pay_tx, pay_rx, bbr_bw;
    const char *bbr_state;
    uint64_t cc_sent, cc_lost;
};

struct qc_tls *qc_tls_new(int insecure, const uint8_t *ca_pem, size_t ca_pem_n, const char *ca_file);
void qc_tls_free(struct qc_tls *t);

int  qc_open(const struct qc_cfg *cfg, const struct qc_ops *ops, void *user, struct qc **out);
void qc_close(struct qc *q, uint64_t app_err);
void qc_free(struct qc *q);

int  qc_fd(const struct qc *q);
int  qc_fd_extra(const struct qc *q);
int  qc_timeout_ms(struct qc *q);
uint64_t qc_deadline_ns(struct qc *q);
int  qc_on_readable(struct qc *q);
int  qc_on_timer(struct qc *q);
void qc_flush(struct qc *q);
void qc_keepalive(struct qc *q, unsigned ms);
uint64_t qc_keepalive_ns(const struct qc *q);
uint64_t qc_rx_last_ns(const struct qc *q);
uint64_t qc_tx_wait_ns(const struct qc *q);
uint64_t qc_now_ns(const struct qc *q);
void qc_ping(struct qc *q);

int  qc_handshake_done(const struct qc *q);
int  qc_stream_open(struct qc *q, int64_t *sid);
int  qc_stream_open_uni(struct qc *q, int64_t *sid);
ssize_t qc_stream_send(struct qc *q, int64_t sid, const uint8_t *d, size_t n, int fin);
size_t  qc_stream_room(const struct qc *q, int64_t sid);
size_t  qc_stream_unsent(const struct qc *q, int64_t sid);
size_t  qc_stream_queued(const struct qc *q, int64_t sid);
size_t  qc_send_total(const struct qc *q);
size_t  qc_send_buf(const struct qc *q);
int  qc_stream_stop(struct qc *q, int64_t sid, uint64_t app_err);
int  qc_stream_reset(struct qc *q, int64_t sid, uint64_t app_err);
void qc_stream_consumed(struct qc *q, int64_t sid, size_t n);
void qc_conn_consumed(struct qc *q, size_t n);
int  qc_datagram_send(struct qc *q, const uint8_t *d, size_t n);
size_t qc_datagram_max(const struct qc *q);
unsigned qc_datagram_slots(const struct qc *q);
void qc_stats_get(struct qc *q, struct qc_stats *st);
void qc_wait_get(const struct qc *q, uint64_t now_ns, uint64_t ns[3]);
uint64_t qc_hop_fails(const struct qc *q);
uint16_t qc_sock_port(const struct qc *q);
uint64_t qc_opened_ns(const struct qc *q);
int qc_cc_brutal(struct qc *q, uint64_t bytes_per_s, int no_loss_comp);

#ifdef QC_WITH_SERVER
struct qc_srv_cfg {
    const char *bind_host;
    uint16_t port;
    const char *alpn;
    const uint8_t *cert_der;
    size_t cert_n;
    const uint8_t *key_der;
    size_t key_n;
    const char *chain_pem;
    size_t chain_pem_n;
    int no_batch;
    size_t datagram_max;
    unsigned idle_ms;
    uint64_t max_data, max_stream_data, max_streams;
    size_t send_buf;
    const struct qc_filter *filter;
    int follow_peer;
};
void qc_probe_stats(const struct qc *q, uint64_t *steps, uint64_t *ops);
size_t qc_send_held(const struct qc *q);
void qc_nested_stats(unsigned long *count, size_t *max_bytes);
void qc_work_stats(struct qc *q, unsigned *rx_max, size_t *flush_max);
unsigned long qc_clock_reads(void);
int qc_stream_reset_write(struct qc *q, int64_t sid, uint64_t app_err);
size_t qc_close_reason_len(const struct qc *q);
int qc_listen(const struct qc_srv_cfg *cfg, const struct qc_ops *ops, void *user, struct qc **out);
uint16_t qc_local_port(const struct qc *q);
uint64_t qc_srtt_ns(const struct qc *q);
struct qc_bbr_view {
    uint64_t longterm, bw_lo, inflight_lo, loss_round_delivered;
    unsigned probe_up_rounds;
    int too_high, loss_in_round, full_bw, startup, drain, down;
};
int qc_bbr_view(const struct qc *q, struct qc_bbr_view *v);
int qc_bbr_poke(struct qc *q, uint64_t longterm, int loss_in_round, uint64_t loss_round_delivered, int drain);
int qc_bbr_poke_probe(struct qc *q);
int qc_bbr_blind(struct qc *q);
int qc_cc_brutal_now(struct qc *q, uint64_t bytes_per_s, int no_loss_comp);
void qc_hop_now(struct qc *q);
void qc_tally_add(struct qc *q, uint64_t sent, uint64_t lost);
void qc_clock_set(struct qc *q, uint64_t ns);
uint64_t qc_hop_due_ns(const struct qc *q);
uint64_t qc_linger_end_ns(const struct qc *q);
uint64_t qc_hop_linger_ns(uint64_t srtt_ns, uint64_t interval_ns);
void qc_path_ports(const struct qc *q, uint16_t *ng_local, uint16_t *ng_remote);
uint16_t qc_peer_port(const struct qc *srv);
void qc_sock_ports(const struct qc *q, uint16_t *cur_local, uint16_t *pre_local, int *pre_fd);
#endif

#endif
