#ifndef TUNVLESS_HY2NODE_H
#define TUNVLESS_HY2NODE_H
#include <stddef.h>
#include <stdint.h>

#include "hy2conn.h"
#include "vless.h"

#define HY2_ENOADDR (-60)
#define HY2_EPIN    (-61)

struct hy2_nodecfg {
    struct hy2c_cfg c;
    char ip[46];
    char sni[128];
    uint8_t pins[8 * 32];
    unsigned qp_ignored;
    unsigned qp_unparroted;
};

struct hy2_budget {
    size_t send_total, send_buf, brutal_span;
};

int hy2_nodecfg(const struct vless_node *n, unsigned addr_i, struct hy2_nodecfg *out);
extern int hy2_no_batch;
extern struct hy2_budget g_hy2_budget;
void hy2_budget_set(uint64_t mem_bytes);
void hy2_budget_init(const char *meminfo, char *line, size_t n);
extern uint32_t (*g_hy2_rand)(void);
struct qc_tls *hy2_tls(void);

#endif
