#include "firc/autolistrules.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>

#include "firc/ifacename.h"
#include "firc/mark.h"

#define AL_PORT "443"

#define AL_HELLO_PACKETS "2:128"
#define AL_SYN_PACKETS "1:"

#define AL_LENGTH "100:"

#define AL_RECORD_HEX "|1603|"
#define AL_SEARCH_FROM_V4 "40"
#define AL_SEARCH_TO_V4 "84"
#define AL_SEARCH_FROM_V6 "60"
#define AL_SEARCH_TO_V6 "104"

#define AL_LIMIT_RATE "500/sec"
#define AL_LIMIT_BURST "500"

static firc_err_t al_chain(const char *chain_prefix, char *out, size_t cap) {
    if (chain_prefix == NULL) { return FIRC_ERR_INVAL; }
    int n = snprintf(out, cap, "%s%s", chain_prefix, FIRC_AUTOLIST_CHAIN_SUFFIX);
    return (n < 0 || (size_t)n >= cap) ? FIRC_ERR_INVAL : FIRC_OK;
}

firc_err_t firc_autolist_rules_build(const firc_fakeip_t *pool, const char *const *lan, size_t n_lan,
                                     firc_tap_rule_fn fn, void *ud) {
    if (pool == NULL || fn == NULL) { return FIRC_ERR_INVAL; }
    if (lan == NULL || n_lan == 0) { return FIRC_ERR_INVAL; }
    for (size_t li = 0; li < n_lan; li++) {
        if (!firc_is_interface_name(lan[li])) { return FIRC_ERR_INVAL; }
    }

    char handled[32];
    snprintf(handled, sizeof(handled), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_HANDLED);

    char group_hello[8], group_reset[8], group_answered[8], group_fin[8], group_close_rst[8], group_syn[8], group_new[8];
    snprintf(group_hello, sizeof(group_hello), "%u", (unsigned)FIRC_AUTOLIST_GROUP_HELLO);
    snprintf(group_reset, sizeof(group_reset), "%u", (unsigned)FIRC_AUTOLIST_GROUP_RESET);
    snprintf(group_answered, sizeof(group_answered), "%u", (unsigned)FIRC_AUTOLIST_GROUP_ANSWERED);
    snprintf(group_fin, sizeof(group_fin), "%u", (unsigned)FIRC_AUTOLIST_GROUP_FIN);
    snprintf(group_close_rst, sizeof(group_close_rst), "%u", (unsigned)FIRC_AUTOLIST_GROUP_CLOSE_RST);
    snprintf(group_syn, sizeof(group_syn), "%u", (unsigned)FIRC_AUTOLIST_GROUP_SYN);
    snprintf(group_new, sizeof(group_new), "%u", (unsigned)FIRC_AUTOLIST_GROUP_NEW);

    static const unsigned families[2] = {FIRC_FAM_V4, FIRC_FAM_V6};
    static const int af[2] = {AF_INET, AF_INET6};

    for (size_t li = 0; li < n_lan; li++) {
        for (size_t i = 0; i < 2; i++) {
            firc_ip_t base;
            uint8_t prefix = 0;
            if (!firc_fakeip_pool_prefix(pool, families[i], &base, &prefix)) { continue; }
            if (base.len != (families[i] == FIRC_FAM_V4 ? 4 : 16)) { continue; }

            char addr[INET6_ADDRSTRLEN], cidr[INET6_ADDRSTRLEN + 8];
            if (inet_ntop(af[i], base.b, addr, sizeof(addr)) == NULL) { continue; }
            if (snprintf(cidr, sizeof(cidr), "%s/%u", addr, (unsigned)prefix) >= (int)sizeof(cidr)) { continue; }

            const bool v4 = families[i] == FIRC_FAM_V4;

            const char *hello_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-p", "tcp",
                "--dport", AL_PORT,
                "--tcp-flags", "SYN,ACK", "ACK",
                "-m", "length", "--length", AL_LENGTH,
                "-m", "connbytes", "--connbytes", AL_HELLO_PACKETS,
                "--connbytes-dir", "original", "--connbytes-mode", "packets",
                "-m", "string", "--algo", "bm", "--hex-string", AL_RECORD_HEX,
                "--from", v4 ? AL_SEARCH_FROM_V4 : AL_SEARCH_FROM_V6,
                "--to", v4 ? AL_SEARCH_TO_V4 : AL_SEARCH_TO_V6,
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_hello, "--nflog-threshold", "1",
            };
            fn(ud, families[i], hello_args, sizeof(hello_args) / sizeof(hello_args[0]));

            const char *reset_args[] = {
                "-o", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-p", "tcp",
                "--sport", AL_PORT,
                "--tcp-flags", "RST", "RST",
                "-m", "connbytes", "--connbytes", FIRC_AUTOLIST_RESET_BYTES,
                "--connbytes-dir", "reply", "--connbytes-mode", "bytes",
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_reset, "--nflog-threshold", "1",
            };
            fn(ud, families[i], reset_args, sizeof(reset_args) / sizeof(reset_args[0]));

            const char *answered_args[] = {
                "-o", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-p", "tcp",
                "--sport", AL_PORT,
                "-m", "connbytes", "--connbytes", FIRC_AUTOLIST_ANSWERED_BYTES,
                "--connbytes-dir", "reply", "--connbytes-mode", "bytes",
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_answered, "--nflog-threshold", "1",
            };
            fn(ud, families[i], answered_args, sizeof(answered_args) / sizeof(answered_args[0]));

            const char *fin_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-p", "tcp",
                "--dport", AL_PORT,
                "--tcp-flags", "FIN", "FIN",
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_fin, "--nflog-threshold", "1",
            };
            fn(ud, families[i], fin_args, sizeof(fin_args) / sizeof(fin_args[0]));

            const char *close_rst_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-p", "tcp",
                "--dport", AL_PORT,
                "--tcp-flags", "RST,ACK", "RST,ACK",
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_close_rst, "--nflog-threshold", "1",
            };
            fn(ud, families[i], close_rst_args, sizeof(close_rst_args) / sizeof(close_rst_args[0]));

            const char *syn_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-p", "tcp",
                "--dport", AL_PORT,
                "--tcp-flags", "SYN,ACK", "SYN",
                "-m", "connbytes", "--connbytes", AL_SYN_PACKETS,
                "--connbytes-dir", "original", "--connbytes-mode", "packets",
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_syn, "--nflog-threshold", "1",
            };
            fn(ud, families[i], syn_args, sizeof(syn_args) / sizeof(syn_args[0]));

            const char *new_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "--ctstate", "NEW", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-m", "limit", "--limit", AL_LIMIT_RATE, "--limit-burst", AL_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_new, "--nflog-threshold", "1",
            };
            fn(ud, families[i], new_args, sizeof(new_args) / sizeof(new_args[0]));
        }
    }
    return FIRC_OK;
}

typedef struct {
    unsigned want;
    firc_ipt_t *ipt;
    const char *chain;
    firc_err_t err;
    size_t found;
} install_ctx_t;

static void install_one(void *ud, unsigned family, const char *const *args, size_t n_args) {
    install_ctx_t *c = ud;
    if (family != c->want || c->err != FIRC_OK) { return; }
    c->found++;
    c->err = firc_ipt_append(c->ipt, "mangle", c->chain, args, n_args);
}

static void install_noop(void *ud, unsigned family, const char *const *args, size_t n_args) {
    (void)ud;
    (void)family;
    (void)args;
    (void)n_args;
}

firc_err_t firc_autolist_rules_install(firc_ipt_t *ipt, const char *chain_prefix, const firc_fakeip_t *pool,
                                       const char *const *lan, size_t n_lan) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }
    char chain[128];
    firc_err_t err = al_chain(chain_prefix, chain, sizeof(chain));
    if (err != FIRC_OK) { return err; }

    err = firc_autolist_rules_build(pool, lan, n_lan, install_noop, NULL);
    if (err != FIRC_OK) { return err; }

    err = firc_ipt_register_chain_override(ipt, "mangle", chain);
    if (err != FIRC_OK) { return err; }

    install_ctx_t ctx = {firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6 ? FIRC_FAM_V6 : FIRC_FAM_V4, ipt, chain,
                         FIRC_OK, 0};
    err = firc_autolist_rules_build(pool, lan, n_lan, install_one, &ctx);
    if (err != FIRC_OK) { return err; }
    if (ctx.err != FIRC_OK) { return ctx.err; }
    if (ctx.found == 0) { return FIRC_ERR_INVAL; }

    err = firc_ipt_register_chain_patch(ipt, "mangle", "FORWARD");
    if (err != FIRC_OK) { return err; }

    const char *jump[] = {"-j", chain};
    return firc_ipt_insert(ipt, "mangle", "FORWARD", 1, jump, 2);
}

typedef struct {
    unsigned want;
    firc_ipt_rule_t **rules;
    size_t found, cap;
    firc_err_t err;
} probe_ctx_t;

static void probe_one(void *ud, unsigned family, const char *const *args, size_t n_args) {
    probe_ctx_t *c = ud;
    if (family != c->want || c->err != FIRC_OK) { return; }
    if (c->found == c->cap) {
        size_t cap = c->cap ? c->cap * 2 : 8;
        firc_ipt_rule_t **grown = realloc(c->rules, cap * sizeof(*grown));
        if (grown == NULL) {
            c->err = FIRC_ERR_NOMEM;
            return;
        }
        c->rules = grown;
        c->cap = cap;
    }
    c->rules[c->found] = firc_ipt_rule_new(args, n_args);
    if (c->rules[c->found] == NULL) {
        c->err = FIRC_ERR_NOMEM;
        return;
    }
    c->found++;
}

firc_err_t firc_autolist_rules_supported(firc_ipt_t *ipt, const char *chain_prefix, const firc_fakeip_t *pool,
                                         const char *const *lan, size_t n_lan) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }

    char chain[128], probe[160];
    firc_err_t err = al_chain(chain_prefix, chain, sizeof(chain));
    if (err != FIRC_OK) { return err; }
    int pn = snprintf(probe, sizeof(probe), "%s%s", chain, FIRC_AUTOLIST_PROBE_SUFFIX);
    if (pn < 0 || (size_t)pn >= sizeof(probe)) { return FIRC_ERR_INVAL; }

    probe_ctx_t ctx = {firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6 ? FIRC_FAM_V6 : FIRC_FAM_V4, NULL, 0, 0,
                       FIRC_OK};
    err = firc_autolist_rules_build(pool, lan, n_lan, probe_one, &ctx);
    if (err == FIRC_OK) { err = ctx.err; }
    if (err == FIRC_OK && ctx.found > 0) {
        err = firc_ipt_probe_chain(ipt, "mangle", probe, ctx.rules, ctx.found);
        if (err == FIRC_ERR_IO) { err = FIRC_ERR_NOSYS; }
    }
    for (size_t i = 0; i < ctx.found; i++) { firc_ipt_rule_free(ctx.rules[i]); }
    free(ctx.rules);
    return err;
}
