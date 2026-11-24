#include "firc/dnat.h"
#include "firc/cfrange.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

typedef struct {
    firc_ipt_t *ipt;
    const char *chain;
    uint8_t want_len; /* 4 or 16 */
    unsigned family;
    bool covered_pass;
    const firc_fakeip_snapshot_t *snap;
    firc_err_t err;
    firc_dnat_routed_fn routed; /* NULL: every group */
    void *routed_ud;
} emit_ctx_t;

static bool to_text(const firc_ip_t *a, char *out, size_t out_len) {
    return inet_ntop(a->len == 4 ? AF_INET : AF_INET6, a->b, out, (socklen_t)out_len) != NULL;
}

static void emit_mapping(void *ud, const char *group_id, unsigned family, const firc_ip_t *fake,
                         const firc_ip_t *real, unsigned kind) {
    emit_ctx_t *e = ud;
    (void)family;
    if (e->err != FIRC_OK || fake == NULL || fake->len != e->want_len) { return; }
    if (e->routed != NULL && (group_id == NULL || !e->routed(group_id, e->routed_ud))) { return; }
    if (real == NULL || real->len != e->want_len) { return; }
    bool covered = kind == FIRC_FAKEIP_KIND_CF && firc_cf_contains(real);
    if (covered != e->covered_pass) { return; }
    e->err = firc_ipt_append_dnat(e->ipt, "nat", e->chain, fake->b, real->b);
}

static void emit_range(void *ud, const char *group_id, unsigned family, const firc_ip_t *base, uint8_t prefix) {
    emit_ctx_t *e = ud;
    if (e->err != FIRC_OK || family != e->family || base == NULL || base->len != e->want_len) { return; }
    if (e->routed != NULL && !e->routed(group_id, e->routed_ud)) { return; }
    firc_ip_t target;
    if (!firc_fakeip_snapshot_cf_target(e->snap, group_id, family, &target) || target.len != e->want_len) { return; }
    char addr[INET6_ADDRSTRLEN], cidr[INET6_ADDRSTRLEN + 8], to[INET6_ADDRSTRLEN];
    if (!to_text(base, addr, sizeof(addr)) || !to_text(&target, to, sizeof(to)) ||
        snprintf(cidr, sizeof(cidr), "%s/%u", addr, prefix) >= (int)sizeof(cidr)) {
        e->err = FIRC_ERR_INVAL;
        return;
    }
    const char *tcp[] = {"-d", cidr, "-p", "tcp", "-m", "multiport", "--dports", "80,443", "-m", "mark",
                         "--mark", "0x40000000/0x40000000", "-j", "DNAT", "--to-destination", to};
    const char *udp[] = {"-d", cidr, "-p", "udp", "--dport", "443", "-m", "mark", "--mark",
                         "0x40000000/0x40000000", "-j", "DNAT", "--to-destination", to};
    e->err = firc_ipt_append(e->ipt, "nat", e->chain, tcp, 16);
    if (e->err == FIRC_OK) { e->err = firc_ipt_append(e->ipt, "nat", e->chain, udp, 14); }
}

firc_err_t firc_dnat_build_rules(firc_ipt_t *ipt, const char *chain_prefix,
                                 const firc_fakeip_snapshot_t *snap,
                                 firc_dnat_routed_fn routed, void *routed_ud) {
    if (ipt == NULL || chain_prefix == NULL) { return FIRC_ERR_INVAL; }

    char chain[128];
    if (snprintf(chain, sizeof(chain), "%s%s", chain_prefix, FIRC_DNAT_CHAIN_SUFFIX) >=
        (int)sizeof(chain)) {
        return FIRC_ERR_INVAL;
    }

    /* Override, not patch: a pass emits the whole chain, and patching it is quadratic. */
    firc_err_t err = firc_ipt_register_chain_override(ipt, "nat", chain);
    if (err != FIRC_OK) { return err; }

    bool v6 = firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6;
    unsigned family = v6 ? FIRC_FAM_V6 : FIRC_FAM_V4;
    uint8_t want_len = v6 ? 16 : 4;

    /* Without a pool the chain is staged empty for a later pass to fill. */
    if (snap != NULL) {
        firc_ip_t base;
        uint8_t prefix = 0;
        if (firc_fakeip_snapshot_pool_prefix(snap, family, &base, &prefix)) {
            char addr[INET6_ADDRSTRLEN], cidr[INET6_ADDRSTRLEN + 8];
            if (!to_text(&base, addr, sizeof(addr)) ||
                snprintf(cidr, sizeof(cidr), "%s/%u", addr, prefix) >= (int)sizeof(cidr)) {
                return FIRC_ERR_INVAL;
            }
            const char *jump[] = {"-d", cidr, "-j", chain};
            err = firc_ipt_append(ipt, "nat", "PREROUTING", jump, 4);
            if (err != FIRC_OK) { return err; }
        }

        emit_ctx_t e = {.ipt = ipt, .chain = chain, .want_len = want_len, .err = FIRC_OK,
                        .family = family, .snap = snap, .routed = routed, .routed_ud = routed_ud};
        firc_fakeip_snapshot_walk_kind(snap, emit_mapping, &e);
        if (e.err == FIRC_OK) { firc_fakeip_snapshot_walk_cf_chunks(snap, emit_range, &e); }
        e.covered_pass = true;
        if (e.err == FIRC_OK) { firc_fakeip_snapshot_walk_kind(snap, emit_mapping, &e); }
        if (e.err != FIRC_OK) { return e.err; }
    }
    return FIRC_OK;
}
