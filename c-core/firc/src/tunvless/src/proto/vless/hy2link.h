#ifndef STEER_HY2LINK_H
#define STEER_HY2LINK_H
#include <stddef.h>
#include <stdint.h>
#include "vless.h"

extern const char HY2_BAD_QP[];

#define HY2_NUL "NUL byte in auth, obfs or obfs-password"
#define HY2_SECRET_MAX 510
#define HY2_LONG "auth, obfs or obfs-password longer than 510 bytes"

#define HY2_CC_AUTO  0
#define HY2_CC_BBR   1
#define HY2_CC_RENO  2
#define HY2_CC_FORCE 3
#define HY2_HOP_LOCAL  1u
#define HY2_HOP_REMOTE 2u
#define HY2_HOP_ONCE   4u
#define HY2_HOP_IPS    8u
#define HY2_HOP_EXPLICIT 0x80u
#define HY2_QP_IGN_PROFILE 1u
#define HY2_QP_IGN_DEBUG   2u
#define HY2_QP_IGN_CHROME  4u
#define HY2_QP_IGN_GSO     8u
#define HY2_QP_IGN_STREAMS 16u
#define HY2_QP_IGN_SRESET  32u

struct hy2_qp {
    uint8_t cc, no_loss_comp, no_pmtud, no_parrot;
    uint64_t up_bps, down_bps;
    uint64_t init_stream_win, max_stream_win, init_conn_win, max_conn_win;
    unsigned idle_s, keepalive_s;
    unsigned ignored;
};

struct hy2_in {
    char auth[HY2_SECRET_MAX + 2];
    char obfs[16];
    char obfs_pw[HY2_SECRET_MAX + 2];
    char ports[HY2_PORTS_CAP];
    char why[96];
    uint16_t first, hop_from, hop_to;
    uint8_t hop_mode;
    uint32_t up_mbps, down_mbps;
    const char *qp;
    size_t qp_n;
};

unsigned long hy2_digits(const char *v, size_t vlen, unsigned long max);
uint32_t hy2_mbps(uint64_t bytes_per_s);
const char *hy2_qp_intern(const char *json, size_t n);
void hy2_in_fail(struct hy2_in *h, const char *why);
void hy2_in_secret(struct hy2_in *h, char *dst, const char *v, size_t n, int nul);
void hy2_in_ports(struct hy2_in *h, const char *spec, size_t n, const char *what);
void hy2_apply(struct vless_node *n, const struct hy2_in *h);

int hy2_link_parse(const char *url, struct vless_node *n);
int hy2_node_usable(struct vless_node *n);
int hy2_qp_parse(const char *json, size_t n, struct hy2_qp *out, char *why, size_t why_n);
void hy2_node_qp(const struct vless_node *n, struct hy2_qp *out);
const char *hy2_interval(const char *t, uint16_t *from, uint16_t *to);
int hy2_ports_canon(const char *spec, size_t n, char *out, size_t cap, uint16_t *first);

#endif
