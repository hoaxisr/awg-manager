#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier): syscall() */
#include "firc/taprules.h"

#include <arpa/inet.h>
#include <net/if.h>

#include "firc/ifacename.h"
#include "firc/kmod.h"
#include "firc/log.h"
#include "firc/mark.h"
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/utsname.h>
#include <unistd.h>

/* A rule covering every port would copy every connection the router forwards. */
#define TAP_PORT "443"

/* Wide on purpose: helloes bench at positions 2-8 here, not just 2:3. */
#define TAP_CONNBYTES "2:8"

/* IP total length, not frame size: ACK/SYN stay under 100 bytes, a ClientHello does not. */
#define TAP_LENGTH "100:"

/* Handshake record header; window.c tells a ClientHello from an encrypted Finished (same 0x16) by a later byte. */
#define TAP_RECORD_HEX "|1603|"
/* IP header + a 20-60 byte TCP header, per family since they differ in length. */
#define TAP_SEARCH_FROM_V4 "40"
#define TAP_SEARCH_TO_V4 "84"
#define TAP_SEARCH_FROM_V6 "60"
#define TAP_SEARCH_TO_V6 "104"

#define TAP_LIMIT_RATE "500/sec"
#define TAP_LIMIT_BURST "500"

_Static_assert(FIRC_TAP_LIMIT_PER_SEC == 500, "TAP_LIMIT_RATE/TAP_LIMIT_BURST are 500, by hand");

static firc_err_t tap_chain(const char *chain_prefix, char *out, size_t cap) {
    if (chain_prefix == NULL) { return FIRC_ERR_INVAL; }
    int n = snprintf(out, cap, "%s%s", chain_prefix, FIRC_TAP_CHAIN_SUFFIX);
    return (n < 0 || (size_t)n >= cap) ? FIRC_ERR_INVAL : FIRC_OK;
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

/* Finds out whether firc_tap_rules_build would refuse lan/n_lan before staging anything. */
static void install_noop(void *ud, unsigned family, const char *const *args, size_t n_args) {
    (void)ud;
    (void)family;
    (void)args;
    (void)n_args;
}

firc_err_t firc_tap_rules_install(firc_ipt_t *ipt, const char *chain_prefix,
                                  const firc_fakeip_t *pool, const char *const *lan,
                                  size_t n_lan) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }
    char chain[128];
    firc_err_t err = tap_chain(chain_prefix, chain, sizeof(chain));
    if (err != FIRC_OK) { return err; }

    /* Validated here so a bad name or n_lan == 0 registers nothing. */
    err = firc_tap_rules_build(pool, lan, n_lan, install_noop, NULL);
    if (err != FIRC_OK) { return err; }

    /* Override, not patch: a second start lands on exactly the rules the builder built. */
    err = firc_ipt_register_chain_override(ipt, "mangle", chain);
    if (err != FIRC_OK) { return err; }

    install_ctx_t ctx = {firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6 ? FIRC_FAM_V6 : FIRC_FAM_V4,
                         ipt, chain, FIRC_OK, 0};
    err = firc_tap_rules_build(pool, lan, n_lan, install_one, &ctx);
    if (err != FIRC_OK) { return err; }
    if (ctx.err != FIRC_OK) { return ctx.err; }
    if (ctx.found == 0) { return FIRC_ERR_INVAL; }

    /* FORWARD belongs to the firmware, so it is patched here, never overridden. */
    err = firc_ipt_register_chain_patch(ipt, "mangle", "FORWARD");
    if (err != FIRC_OK) { return err; }

    /* First, so the capture sees the packet whatever the rules after it do with it. */
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

firc_err_t firc_tap_rules_supported(firc_ipt_t *ipt, const char *chain_prefix,
                                    const firc_fakeip_t *pool, const char *const *lan,
                                    size_t n_lan) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }

    char chain[128], probe[160];
    firc_err_t err = tap_chain(chain_prefix, chain, sizeof(chain));
    if (err != FIRC_OK) { return err; }
    int pn = snprintf(probe, sizeof(probe), "%s%s", chain, FIRC_TAP_PROBE_SUFFIX);
    if (pn < 0 || (size_t)pn >= sizeof(probe)) { return FIRC_ERR_INVAL; }

    probe_ctx_t ctx = {firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6 ? FIRC_FAM_V6 : FIRC_FAM_V4, NULL, 0, 0, FIRC_OK};
    err = firc_tap_rules_build(pool, lan, n_lan, probe_one, &ctx);
    if (err == FIRC_OK) { err = ctx.err; }
    if (err == FIRC_OK && ctx.found > 0) {
        err = firc_ipt_probe_chain(ipt, "mangle", probe, ctx.rules, ctx.found);
        if (err == FIRC_ERR_IO) { err = FIRC_ERR_NOSYS; }
    }
    for (size_t i = 0; i < ctx.found; i++) { firc_ipt_rule_free(ctx.rules[i]); }
    free(ctx.rules);
    return err;
}

firc_err_t firc_tap_rules_build(const firc_fakeip_t *pool, const char *const *lan, size_t n_lan,
                                firc_tap_rule_fn fn, void *ud) {
    if (pool == NULL || fn == NULL) { return FIRC_ERR_INVAL; }
    if (lan == NULL || n_lan == 0) { return FIRC_ERR_INVAL; }
    for (size_t li = 0; li < n_lan; li++) {
        if (!firc_is_interface_name(lan[li])) { return FIRC_ERR_INVAL; }
    }

    /* The handled bit, not the group field: the firmware's policy mark fills the group field too. */
    char handled[32];
    snprintf(handled, sizeof(handled), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_HANDLED);

    char group_hello[8], group_new[8];
    snprintf(group_hello, sizeof(group_hello), "%u", (unsigned)FIRC_TAP_GROUP_HELLO);
    snprintf(group_new, sizeof(group_new), "%u", (unsigned)FIRC_TAP_GROUP_NEW);

    static const unsigned families[2] = {FIRC_FAM_V4, FIRC_FAM_V6};
    static const int af[2] = {AF_INET, AF_INET6};

    for (size_t li = 0; li < n_lan; li++) {
        for (size_t i = 0; i < 2; i++) {
            firc_ip_t base;
            uint8_t prefix = 0;
            if (!firc_fakeip_pool_prefix(pool, families[i], &base, &prefix)) { continue; }
            /* No prefix for this family: no rule, since the exclusion below has nothing to exclude. */
            if (base.len != (families[i] == FIRC_FAM_V4 ? 4 : 16)) { continue; }

            char addr[INET6_ADDRSTRLEN], cidr[INET6_ADDRSTRLEN + 8];
            if (inet_ntop(af[i], base.b, addr, sizeof(addr)) == NULL) { continue; }
            if (snprintf(cidr, sizeof(cidr), "%s/%u", addr, (unsigned)prefix) >=
                (int)sizeof(cidr)) {
                continue;
            }

            const bool v4 = families[i] == FIRC_FAM_V4;

            /* Excluded by the ORIGINAL destination, not -d: DNAT has already restored dst by mangle/FORWARD. */
            const char *new_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "--ctstate", "NEW", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-m", "limit", "--limit", TAP_LIMIT_RATE, "--limit-burst", TAP_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_new, "--nflog-threshold", "1",
            };
            fn(ud, families[i], new_args, sizeof(new_args) / sizeof(new_args[0]));

            /* --nflog-range does nothing on 1.4.21; the socket's copy mode truncates instead. */
            const char *hello_args[] = {
                "-i", lan[li],
                "-m", "conntrack", "!", "--ctorigdst", cidr,
                "-m", "mark", "!", "--mark", handled,
                "-p", "tcp",
                "--dport", TAP_PORT,
                "--tcp-flags", "SYN,ACK", "ACK",
                "-m", "length", "--length", TAP_LENGTH,
                "-m", "connbytes", "--connbytes", TAP_CONNBYTES,
                "--connbytes-dir", "original", "--connbytes-mode", "packets",
                "-m", "string", "--algo", "bm", "--hex-string", TAP_RECORD_HEX,
                "--from", v4 ? TAP_SEARCH_FROM_V4 : TAP_SEARCH_FROM_V6,
                "--to", v4 ? TAP_SEARCH_TO_V4 : TAP_SEARCH_TO_V6,
                "-m", "limit", "--limit", TAP_LIMIT_RATE, "--limit-burst", TAP_LIMIT_BURST,
                "-j", "NFLOG", "--nflog-group", group_hello, "--nflog-threshold", "1",
            };
            fn(ud, families[i], hello_args, sizeof(hello_args) / sizeof(hello_args[0]));
        }
    }
    return FIRC_OK;
}

size_t firc_tap_load_modules(const char *dir) {
    /* nfnetlink_log first: xt_NFLOG depends on it and init_module does not resolve dependencies. */
    static const char *const mods[] = {"nfnetlink_log", "xt_NFLOG",  "xt_conntrack",
                                       "xt_mark",       "xt_limit",  "xt_string",
                                       "xt_length",     "xt_connbytes"};
    return firc_kmod_load(dir, mods, sizeof(mods) / sizeof(mods[0]), "the capture");
}
