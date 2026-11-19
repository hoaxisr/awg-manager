#include "firc/pool_reject.h"
#include "firc/mark.h"

#include <arpa/inet.h>
#include <stdio.h>

#include <sys/socket.h>

static const unsigned k_families[2] = {FIRC_FAM_V4, FIRC_FAM_V6};
static const int k_af[2] = {AF_INET, AF_INET6};

void firc_pool_reject_plan(const firc_fakeip_t *pool, uint32_t table, uint32_t metric,
                           firc_pool_reject_plan_fn fn, void *ud) {
    if (pool == NULL || fn == NULL) { return; }
    for (size_t i = 0; i < 2; i++) {
        firc_ip_t base;
        uint8_t prefix = 0;
        if (!firc_fakeip_pool_prefix(pool, k_families[i], &base, &prefix)) { continue; }
        fn(ud, k_af[i], table, metric, &base, prefix);
    }
}

static firc_err_t each_family(firc_rtnl_t *r, const firc_fakeip_t *pool, uint32_t table,
                              uint32_t metric, bool add) {
    if (r == NULL || pool == NULL) { return FIRC_ERR_INVAL; }

    firc_err_t first = FIRC_OK;
    for (size_t i = 0; i < 2; i++) {
        firc_ip_t base;
        uint8_t prefix = 0;
        if (!firc_fakeip_pool_prefix(pool, k_families[i], &base, &prefix)) {
            continue;
        }
        firc_err_t err = add ? firc_rtnl_route_add_unreachable(r, k_af[i], table, metric, base.b,
                                                               prefix)
                             : firc_rtnl_route_del_unreachable(r, k_af[i], table, metric, base.b,
                                                               prefix);
        /* Both families are tried even when the first fails. */
        if (err != FIRC_OK && first == FIRC_OK) { first = err; }
    }
    return first;
}

firc_err_t firc_pool_reject_install(firc_rtnl_t *r, const firc_fakeip_t *pool, uint32_t table,
                                    uint32_t metric) {
    return each_family(r, pool, table, metric, true);
}

firc_err_t firc_pool_reject_remove(firc_rtnl_t *r, const firc_fakeip_t *pool, uint32_t table,
                                   uint32_t metric) {
    return each_family(r, pool, table, metric, false);
}

firc_err_t firc_pool_reject_build_rules(firc_ipt_t *ipt, const char *chain_prefix, const firc_ip_t *base,
                                        uint8_t prefix_len) {
    if (ipt == NULL || chain_prefix == NULL || base == NULL) { return FIRC_ERR_INVAL; }
    bool v6 = firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6;
    if (base->len != (v6 ? 16 : 4)) { return FIRC_ERR_INVAL; }
    char chain[128], addr[INET6_ADDRSTRLEN], cidr[INET6_ADDRSTRLEN + 8];
    if (snprintf(chain, sizeof(chain), "%s%s", chain_prefix, FIRC_POOL_REJECT_CHAIN_SUFFIX) >= (int)sizeof(chain)) {
        return FIRC_ERR_INVAL;
    }
    if (inet_ntop(v6 ? AF_INET6 : AF_INET, base->b, addr, sizeof(addr)) == NULL ||
        snprintf(cidr, sizeof(cidr), "%s/%u", addr, prefix_len) >= (int)sizeof(cidr)) {
        return FIRC_ERR_INVAL;
    }
    firc_err_t err = firc_ipt_register_chain_override(ipt, "filter", chain);
    if (err != FIRC_OK) { return err; }
    char handled[32], fieldless[32];
    snprintf(handled, sizeof(handled), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_HANDLED);
    snprintf(fieldless, sizeof(fieldless), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_WRITE_MASK);
    const char *tcp[] = {"-d", cidr, "-p", "tcp", "-j", "REJECT", "--reject-with", "tcp-reset"};
    err = firc_ipt_append(ipt, "filter", chain, tcp, 8);
    if (err != FIRC_OK) { return err; }
    const char *rest[] = {"-d", cidr, "-j", "REJECT", "--reject-with", v6 ? "icmp6-addr-unreachable" : "icmp-host-unreachable"};
    err = firc_ipt_append(ipt, "filter", chain, rest, 6);
    if (err != FIRC_OK) { return err; }
    const char *foreign[] = {"-m", "connmark", "--mark", fieldless, "-j", "RETURN"};
    err = firc_ipt_append(ipt, "filter", chain, foreign, 6);
    if (err != FIRC_OK) { return err; }
    const char *unmarked[] = {"-m", "conntrack", "--ctdir", "ORIGINAL", "-m", "connmark", "--mark", handled,
                              "-m", "mark", "!", "--mark", handled, "-j", "DROP"};
    err = firc_ipt_append(ipt, "filter", chain, unmarked, 15);
    if (err != FIRC_OK) { return err; }
    const char *jump[] = {"-j", chain};
    return firc_ipt_insert(ipt, "filter", "FORWARD", 1, jump, 2);
}
