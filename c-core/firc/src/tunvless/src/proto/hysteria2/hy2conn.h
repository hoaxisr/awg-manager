/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#ifndef TUNVLESS_HY2CONN_H
#define TUNVLESS_HY2CONN_H
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>

#include "quic.h"

#define HY2C_CLOSE_AUTH 100

#define HY2C_END_CLEAN 0
#define HY2C_END_RESET 1
#define HY2C_END_LINK  2

#define HY2C_ESTOPPED (-16)

#define HY2C_LEFT_MS 30000

struct hy2c;

struct hy2c_cfg {
    struct qc_cfg qc;
    const char *auth;
    const char *obfs_password;
    uint64_t rx_bps;
    uint64_t up_bps;
    int cc;
    int no_loss_comp;
    unsigned udp_sessions;
};

struct hy2c_ops {
    void (*on_ready)(void *u, uint64_t peer_rx_bps, int udp);
    void (*on_stream_data)(void *u, int64_t sid, const uint8_t *d, size_t n, int fin);
    void (*on_stream_writable)(void *u, int64_t sid);
    void (*on_streams_open)(void *u);
    void (*on_stream_refused)(void *u, int64_t sid, const char *msg);
    void (*on_stream_close)(void *u, int64_t sid, int how);
    void (*on_udp)(void *u, uint32_t session, const char *addr, size_t addr_n, const uint8_t *d, size_t n);
    void (*on_closed)(void *u, int reason, const char *why);
};

int  hy2c_open(const struct hy2c_cfg *cfg, const struct hy2c_ops *ops, void *user, struct hy2c **out);
void hy2c_close(struct hy2c *c);
void hy2c_free(struct hy2c *c);

int  hy2c_fd(const struct hy2c *c);
int  hy2c_fd_extra(const struct hy2c *c);
int  hy2c_timeout_ms(struct hy2c *c);
uint64_t hy2c_deadline_ns(struct hy2c *c);
int  hy2c_on_readable(struct hy2c *c);
int  hy2c_on_timer(struct hy2c *c);
void hy2c_flush(struct hy2c *c);

int  hy2c_ready(const struct hy2c *c);
int  hy2c_closed(const struct hy2c *c);
int  hy2c_cc(const struct hy2c *c, uint64_t *tx_bps);
void hy2c_stats(struct hy2c *c, struct qc_stats *st);
uint64_t hy2c_clock(const struct hy2c *c, uint64_t now_ns);
void hy2c_wait(const struct hy2c *c, uint64_t now_ns, uint64_t ns[3]);
uint64_t hy2c_hop_fails(const struct hy2c *c);
uint16_t hy2c_sock_port(const struct hy2c *c);

int  hy2c_tcp_open(struct hy2c *c, const char *host, uint16_t port, int64_t *sid);
ssize_t hy2c_tcp_send(struct hy2c *c, int64_t sid, const uint8_t *d, size_t n);
size_t  hy2c_tcp_room(const struct hy2c *c, int64_t sid);
int  hy2c_tcp_finish(struct hy2c *c, int64_t sid);
void hy2c_tcp_consumed(struct hy2c *c, int64_t sid, size_t n);
void hy2c_conn_consumed(struct hy2c *c, size_t n);
void hy2c_tcp_close(struct hy2c *c, int64_t sid);

uint32_t hy2c_udp_session(struct hy2c *c);
int  hy2c_udp_send(struct hy2c *c, uint32_t session, const char *host, uint16_t port, const uint8_t *d, size_t n);
void hy2c_udp_end(struct hy2c *c, uint32_t session);

#ifdef QC_WITH_SERVER
struct hy2c_probe { uint64_t steps, ops, rasm_bytes, rasm_peak, drain_n, left_n; };
void hy2c_probe_get(const struct hy2c *c, struct hy2c_probe *p);
void hy2c_udp_next_set(struct hy2c *c, uint32_t id);
size_t hy2c_tcp_unsent(const struct hy2c *c, int64_t sid);
void hy2c_clock_set(struct hy2c *c, uint64_t ns);
void hy2c_hop_now(struct hy2c *c);
void hy2c_tally_add(struct hy2c *c, uint64_t sent, uint64_t lost);
uint64_t hy2c_opened_ns(const struct hy2c *c);
uint64_t hy2c_live_wake_ns(const struct hy2c *c);
uint64_t hy2c_drain_due_ns(const struct hy2c *c);
extern void (*hy2c_credit_hook)(void *u, int64_t sid, uint64_t delivered, uint64_t returned);
#endif

#endif
