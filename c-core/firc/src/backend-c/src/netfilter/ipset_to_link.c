#include "firc/ipset_to_link.h"
#include "firc/models.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/mark.h"

#include <linux/rtnetlink.h>
#include <netinet/in.h>
#include "firc/pool_reject.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>
#include <sys/socket.h>

typedef struct family_state {
    bool rule_added;
    bool fallback_added;
    firc_iface_route_t route;
} family_state_t;

struct firc_ipset_to_link {
    char *chain_name;
    char *ifaces[FIRC_IPSET_TO_LINK_IFACES_MAX];
    size_t n_ifaces;
    size_t active;
    bool blocked;
    bool reply_added[FIRC_IPSET_TO_LINK_IFACES_MAX];
    firc_ipt_t *ipt4;
    firc_ipt_t *ipt6;
    firc_rtnl_t *rtnl;
    const firc_fakeip_t *pool; /* loop thread only */
    const firc_fakeip_snapshot_t *(*snap)(void *ud);
    void *snap_ud;
    firc_tunnel_dev_fn tunnel_dev;
    void *tunnel_ud;
    char *group_id;
    firc_ipv4_subnet_t *sub4;
    size_t n_sub4;
    firc_ipv6_subnet_t *sub6;
    size_t n_sub6;
    firc_nf_print_t bare_print[2];
    firc_nf_devices_t dev;
    bool pool_rejected;
    uint32_t start_idx;

    bool enabled;
    uint32_t mark;       /* what the MARK rule writes: group field + handled bit */
    uint32_t mark_field; /* 1..FIRC_MARK_MAX_GROUPS; 0 until enable */
    uint32_t table;
    family_state_t v4, v6;
    bool v6_known, v6_carries;
    firc_logrep_t link_missing;
};

firc_ipset_to_link_t *firc_ipset_to_link_new(const char *chain_name, const char *iface_name,
                                         firc_ipt_t *ipt4, firc_ipt_t *ipt6,
                                         firc_rtnl_t *rtnl, uint32_t start_idx,
                                         const firc_fakeip_t *pool, const char *group_id,
                                         const firc_fakeip_snapshot_t *(*snap)(void *ud),
                                         void *snap_ud) {
    firc_ipset_to_link_t *l = calloc(1, sizeof(*l));
    if (!l) { return NULL; }
    l->chain_name = strdup(chain_name);
    l->ifaces[0] = strdup(iface_name);
    l->n_ifaces = 1;
    if (!l->chain_name || !l->ifaces[0]) {
        free(l->chain_name);
        free(l->ifaces[0]);
        free(l);
        return NULL;
    }
    l->ipt4 = ipt4;
    l->ipt6 = ipt6;
    l->rtnl = rtnl;
    l->pool = pool;
    l->snap = snap;
    l->snap_ud = snap_ud;
    l->group_id = group_id ? strdup(group_id) : NULL;
    if (group_id && !l->group_id) {
        free(l->chain_name);
        free(l->ifaces[0]);
        free(l);
        return NULL;
    }
    l->start_idx = start_idx;
    return l;
}

static const char *active_name(const firc_ipset_to_link_t *l) { return l->ifaces[l->active]; }

bool firc_iface_is_sink(const char *iface) {
    return iface != NULL && (strcmp(iface, FIRC_IPSET_TO_LINK_BLACKHOLE) == 0 || strcmp(iface, FIRC_IFACE_BLOCK) == 0);
}

static bool is_blackhole(const firc_ipset_to_link_t *l) { return l->n_ifaces == 1 && firc_iface_is_sink(l->ifaces[0]); }

void firc_ipset_to_link_free(firc_ipset_to_link_t *l) {
    if (!l) { return; }
    free(l->chain_name);
    for (size_t i = 0; i < l->n_ifaces; i++) { free(l->ifaces[i]); }
    free(l->group_id);
    free(l->sub4);
    free(l->sub6);
    firc_nf_devices_clear(&l->dev);
    free(l);
}

typedef struct {
    firc_ipt_t *ipt;
    const char *chain;
    const char *mark_str;
    const char *jump; /* the devices chain, for a selector group; else NULL */
    unsigned want_family;
    firc_err_t err;
} chunk_emit_t;

static void emit_chunk_rule(void *ud, const char *group_id, unsigned family,
                            const firc_ip_t *base, uint8_t prefix) {
    chunk_emit_t *e = ud;
    (void)group_id;
    if (family != e->want_family || e->err != FIRC_OK) { return; }

    char cidr[64];
    char addr[INET6_ADDRSTRLEN];
    if (inet_ntop(base->len == 4 ? AF_INET : AF_INET6, base->b, addr, sizeof(addr)) == NULL) {
        e->err = FIRC_ERR_INVAL;
        return;
    }
    if (snprintf(cidr, sizeof(cidr), "%s/%u", addr, prefix) >= (int)sizeof(cidr)) {
        e->err = FIRC_ERR_INVAL;
        return;
    }

    if (e->jump != NULL) {
        const char *j[] = {"-d", cidr, "-j", e->jump};
        e->err = firc_ipt_append(e->ipt, "mangle", e->chain, j, 4);
        return;
    }

    const char *mk[] = {"-d", cidr, "-j", "MARK", "--set-xmark", e->mark_str};
    e->err = firc_ipt_append(e->ipt, "mangle", e->chain, mk, 6);
    if (e->err != FIRC_OK) { return; }
    /* Only firc's bits reach ctmark. */
    const char *cm[] = {"-d", cidr, "-j", "CONNMARK", "--save-mark", "--nfmask", "0x40ff0000", "--ctmask", "0x40ff0000"};
    e->err = firc_ipt_append(e->ipt, "mangle", e->chain, cm, 9);
}

firc_err_t firc_ipset_to_link_devices_chain_of(const char *chain, char *out, size_t cap) {
    int n = snprintf(out, cap, "%s%s", chain, FIRC_DEVICES_CHAIN_SUFFIX);
    return n < 0 || (size_t)n >= cap ? FIRC_ERR_INVAL : FIRC_OK;
}

/* Match parts, none for a /0; false for the other family. */
static bool source_match(const firc_nf_source_t *s, bool is6, char *text, size_t cap, const char **parts,
                         size_t *n) {
    *n = 0;
    switch (s->kind) {
    case FIRC_NF_SRC_ADDR: {
        if ((s->family == 6) != is6) { return false; }
        if (s->prefix == 0) { return true; }
        char a[INET6_ADDRSTRLEN];
        if (inet_ntop(is6 ? AF_INET6 : AF_INET, s->addr, a, sizeof(a)) == NULL) { return false; }
        snprintf(text, cap, "%s/%u", a, s->prefix);
        parts[(*n)++] = "-s";
        parts[(*n)++] = text;
        return true;
    }
    case FIRC_NF_SRC_MAC:
        snprintf(text, cap, "%02X:%02X:%02X:%02X:%02X:%02X", s->mac[0], s->mac[1], s->mac[2], s->mac[3],
                 s->mac[4], s->mac[5]);
        parts[(*n)++] = "-m";
        parts[(*n)++] = "mac";
        parts[(*n)++] = "--mac-source";
        parts[(*n)++] = text;
        return true;
    case FIRC_NF_SRC_MARK:
        /* Every bit but firc's: an earlier firc chain may have written ours. */
        snprintf(text, cap, "0x%x/0x%x", s->mark & FIRC_MARK_POLICY_MASK, FIRC_MARK_POLICY_MASK);
        parts[(*n)++] = "-m";
        parts[(*n)++] = "mark";
        parts[(*n)++] = "--mark";
        parts[(*n)++] = text;
        return true;
    default:
        return false;
    }
}

/* Deny RETURNs, allow MARKs, then CONNMARK only on packets this chain marked. */
static firc_err_t emit_devices_chain(firc_ipt_t *ipt, const char *dchain, const char *mark_str,
                                     const firc_nf_devices_t *dev) {
    bool is6 = firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6;
    firc_err_t err = firc_ipt_register_chain_override(ipt, "mangle", dchain);
    if (err != FIRC_OK) { return err; }
    char text[64];
    const char *r[12];
    size_t n = 0;
    for (size_t i = 0; i < dev->n_deny; i++) {
        if (!source_match(&dev->deny[i], is6, text, sizeof(text), r, &n)) { continue; }
        r[n++] = "-j";
        r[n++] = "RETURN";
        err = firc_ipt_append(ipt, "mangle", dchain, r, n);
        if (err != FIRC_OK) { return err; }
    }
    if (dev->allow_all) {
        const char *all[] = {"-j", "MARK", "--set-xmark", mark_str};
        err = firc_ipt_append(ipt, "mangle", dchain, all, 4);
        if (err != FIRC_OK) { return err; }
    } else {
        for (size_t i = 0; i < dev->n_allow; i++) {
            if (!source_match(&dev->allow[i], is6, text, sizeof(text), r, &n)) { continue; }
            r[n++] = "-j";
            r[n++] = "MARK";
            r[n++] = "--set-xmark";
            r[n++] = mark_str;
            err = firc_ipt_append(ipt, "mangle", dchain, r, n);
            if (err != FIRC_OK) { return err; }
        }
    }
    const char *cm[] = {"-m", "mark", "--mark", mark_str, "-j", "CONNMARK", "--save-mark",
                        "--nfmask", "0x40ff0000", "--ctmask", "0x40ff0000"};
    return firc_ipt_append(ipt, "mangle", dchain, cm, 11);
}

static firc_err_t open_egress(firc_ipt_t *ipt, const char *chain) {
    firc_err_t err = firc_ipt_register_chain_override(ipt, "filter", chain);
    if (err != FIRC_OK) { return err; }
    char handled[32];
    snprintf(handled, sizeof(handled), "0x0/0x%x", FIRC_MARK_HANDLED);
    const char *unmarked[] = {"-m", "mark", "--mark", handled, "-j", "RETURN"};
    err = firc_ipt_append(ipt, "filter", chain, unmarked, 6);
    if (err != FIRC_OK) { return err; }
    const char *jump[] = {"-j", chain};
    return firc_ipt_insert(ipt, "filter", "FORWARD", 1, jump, 2);
}

static firc_err_t append_guard(firc_ipt_t *ipt, const char *iface, const char *mark_str) {
    char chain[128];
    int n = snprintf(chain, sizeof(chain), "%s%s", firc_ipt_prefix(ipt), FIRC_EGRESS_CHAIN_SUFFIX);
    if (n < 0 || (size_t)n >= sizeof(chain)) { return FIRC_ERR_INVAL; }
    const char *guard[] = {"!", "-i", iface, "!", "-o", iface, "-m", "mark", "--mark", mark_str,
                           "-m", "conntrack", "--ctdir", "ORIGINAL", "-j", "DROP"};
    const char *const *rule = iface != NULL ? guard : guard + 6;
    size_t len = iface != NULL ? 16 : 10;
    firc_err_t err = firc_ipt_append(ipt, "filter", chain, rule, len);
    if (err != FIRC_ERR_STATE) { return err; }
    err = open_egress(ipt, chain);
    return err != FIRC_OK ? err : firc_ipt_append(ipt, "filter", chain, rule, len);
}

static firc_err_t append_guard_list(firc_ipt_t *ipt, const char *chain_name, const char *const *ifaces, size_t n,
                                    const char *mark_str) {
    char echain[128], chain[128];
    int w = snprintf(echain, sizeof(echain), "%s%s", chain_name, FIRC_EGRESS_LIST_SUFFIX);
    int v = snprintf(chain, sizeof(chain), "%s%s", firc_ipt_prefix(ipt), FIRC_EGRESS_CHAIN_SUFFIX);
    if (w < 0 || (size_t)w >= sizeof(echain) || v < 0 || (size_t)v >= sizeof(chain)) { return FIRC_ERR_INVAL; }
    firc_err_t err = firc_ipt_register_chain_override(ipt, "filter", echain);
    for (size_t i = 0; err == FIRC_OK && i < n; i++) {
        const char *in[] = {"-i", ifaces[i], "-j", "RETURN"};
        const char *out[] = {"-o", ifaces[i], "-j", "RETURN"};
        err = firc_ipt_append(ipt, "filter", echain, in, 4);
        if (err == FIRC_OK) { err = firc_ipt_append(ipt, "filter", echain, out, 4); }
    }
    const char *drop[] = {"-j", "DROP"};
    if (err == FIRC_OK) { err = firc_ipt_append(ipt, "filter", echain, drop, 2); }
    if (err != FIRC_OK) { return err; }
    const char *jump[] = {"-m", "mark", "--mark", mark_str, "-m", "conntrack", "--ctdir", "ORIGINAL", "-j", echain};
    err = firc_ipt_append(ipt, "filter", chain, jump, 10);
    if (err != FIRC_ERR_STATE) { return err; }
    err = open_egress(ipt, chain);
    return err != FIRC_OK ? err : firc_ipt_append(ipt, "filter", chain, jump, 10);
}

firc_err_t firc_ipset_to_link_build_rules_list(firc_ipt_t *ipt, const char *chain_name, const char *const *ifaces,
                                               size_t n, uint32_t mark, uint32_t mark_field,
                                               const firc_fakeip_snapshot_t *snap, const char *group_id,
                                               const firc_nf_devices_t *dev) {
    if (!ipt) { return FIRC_OK; }
    if (n == 0 || n > FIRC_IPSET_TO_LINK_IFACES_MAX) { return FIRC_ERR_INVAL; }
    bool with_dev = dev != NULL && dev->active;
    char dchain[128];
    if (with_dev && firc_ipset_to_link_devices_chain_of(chain_name, dchain, sizeof(dchain)) != FIRC_OK) {
        return FIRC_ERR_INVAL;
    }

    firc_err_t err = firc_ipt_register_chain_override(ipt, "filter", chain_name);
    if (err != FIRC_OK) { return err; }

    /* Masked on both sides: bits outside FIRC_MARK_WRITE_MASK are not ours. */
    char mark_str[32], match_str[32];
    snprintf(mark_str, sizeof(mark_str), "0x%x/0x%x", mark, FIRC_MARK_WRITE_MASK);
    snprintf(match_str, sizeof(match_str), "0x%x/0x%x", firc_mark_group_value(mark_field),
             FIRC_MARK_GROUP_MASK);

    bool blackhole = n == 1 && firc_iface_is_sink(ifaces[0]);
    /* FORWARD sees the real address, so match the mark, not the chunk. */
    for (size_t i = 0; !blackhole && i < n; i++) {
        const char *acc[] = {"-o", ifaces[i], "-m", "mark", "--mark", match_str, "-j", "ACCEPT"};
        err = firc_ipt_append(ipt, "filter", chain_name, acc, 8);
        if (err != FIRC_OK) { return err; }
    }
    const char *fwd_args[] = {"-j", chain_name};
    err = firc_ipt_append(ipt, "filter", "FORWARD", fwd_args, 2);
    if (err != FIRC_OK) { return err; }

    err = n > 1 ? append_guard_list(ipt, chain_name, ifaces, n, mark_str)
                : append_guard(ipt, blackhole ? NULL : ifaces[0], mark_str);
    if (err != FIRC_OK) { return err; }

    err = firc_ipt_register_chain_override(ipt, "mangle", chain_name);
    if (err != FIRC_OK) { return err; }
    const char *mangle1[] = {"-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN"};
    err = firc_ipt_append(ipt, "mangle", chain_name, mangle1, 6);
    if (err != FIRC_OK) { return err; }
    const char *pre_args[] = {"-j", chain_name};
    err = firc_ipt_append(ipt, "mangle", "PREROUTING", pre_args, 2);
    if (err != FIRC_OK) { return err; }

    err = firc_ipt_register_chain_override(ipt, "nat", chain_name);
    if (err != FIRC_OK) { return err; }
    /* POSTROUTING sees the real address: match the mark and the output interface. */
    for (size_t i = 0; !blackhole && i < n; i++) {
        const char *nat1[] = {"-o", ifaces[i], "-m", "mark", "--mark", match_str, "-j", "MASQUERADE"};
        err = firc_ipt_append(ipt, "nat", chain_name, nat1, 8);
        if (err != FIRC_OK) { return err; }
    }
    const char *post_args[] = {"-j", chain_name};
    err = firc_ipt_append(ipt, "nat", "POSTROUTING", post_args, 2);
    if (err != FIRC_OK) { return err; }

    if (snap != NULL && group_id != NULL) {
        chunk_emit_t e = {
            .ipt = ipt,
            .chain = chain_name,
            .mark_str = mark_str,
            .jump = with_dev ? dchain : NULL,
            .want_family = firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6 ? FIRC_FAM_V6 : FIRC_FAM_V4,
            .err = FIRC_OK,
        };
        /* Snapshot only: the loop thread frees live chunks without a lock. */
        firc_fakeip_snapshot_walk_chunks(snap, group_id, emit_chunk_rule, &e);
        if (e.err != FIRC_OK) { return e.err; }
    }

    if (with_dev) { return emit_devices_chain(ipt, dchain, mark_str, dev); }
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_build_rules_dev(firc_ipt_t *ipt, const char *chain_name, const char *iface_name,
                                              uint32_t mark, uint32_t mark_field,
                                              const firc_fakeip_snapshot_t *snap, const char *group_id,
                                              const firc_nf_devices_t *dev) {
    return firc_ipset_to_link_build_rules_list(ipt, chain_name, &iface_name, 1, mark, mark_field, snap, group_id,
                                               dev);
}

firc_err_t firc_ipset_to_link_build_rules(firc_ipt_t *ipt, const char *chain_name, const char *iface_name,
                                          uint32_t mark, uint32_t mark_field,
                                          const firc_fakeip_snapshot_t *snap, const char *group_id) {
    return firc_ipset_to_link_build_rules_dev(ipt, chain_name, iface_name, mark, mark_field, snap, group_id,
                                              NULL);
}

static firc_err_t build_iptables_rules(firc_ipset_to_link_t *l, firc_ipt_t *ipt) {
    firc_err_t err = firc_ipset_to_link_build_rules_list(ipt, l->chain_name, (const char *const *)l->ifaces,
                                                         l->n_ifaces, l->mark, l->mark_field,
                                                         l->snap ? l->snap(l->snap_ud) : NULL, l->group_id,
                                                         &l->dev);
    if (err != FIRC_OK) { return err; }
    return firc_ipset_to_link_build_subnet_rules_dev(ipt, l->chain_name, l->mark, l->sub4, l->n_sub4,
                                                     l->sub6, l->n_sub6, &l->dev);
}

static firc_err_t bare_of(const firc_ipv4_subnet_t *sub4, size_t n_sub4, const firc_ipv6_subnet_t *sub6,
                          size_t n_sub6, uint8_t family, firc_nf_prefixes_t *out);

firc_err_t firc_ipset_to_link_set_subnets(firc_ipset_to_link_t *l, const firc_ipv4_subnet_t *v4,
                                          size_t n4, const firc_ipv6_subnet_t *v6, size_t n6,
                                          bool *changed) {
    if (!l) { return FIRC_ERR_INVAL; }
    bool same = n4 == l->n_sub4 && n6 == l->n_sub6 &&
                (n4 == 0 || memcmp(v4, l->sub4, n4 * sizeof(*v4)) == 0) &&
                (n6 == 0 || memcmp(v6, l->sub6, n6 * sizeof(*v6)) == 0);
    if (changed) { *changed = !same; }
    if (same) { return FIRC_OK; }
    firc_ipv4_subnet_t *c4 = n4 ? malloc(n4 * sizeof(*c4)) : NULL;
    firc_ipv6_subnet_t *c6 = n6 ? malloc(n6 * sizeof(*c6)) : NULL;
    if ((n4 && !c4) || (n6 && !c6)) {
        free(c4);
        free(c6);
        return FIRC_ERR_NOMEM;
    }
    if (n4) { memcpy(c4, v4, n4 * sizeof(*c4)); }
    if (n6) { memcpy(c6, v6, n6 * sizeof(*c6)); }
    firc_nf_print_t prints[2];
    for (size_t f = 0; f < 2; f++) {
        firc_nf_prefixes_t bare = {NULL, 0};
        if (bare_of(c4, n4, c6, n6, f == 0 ? 4 : 6, &bare) != FIRC_OK) {
            free(c4);
            free(c6);
            return FIRC_ERR_NOMEM;
        }
        prints[f] = firc_nf_prefixes_print(&bare);
        firc_nf_prefixes_clear(&bare);
    }
    l->bare_print[0] = prints[0];
    l->bare_print[1] = prints[1];
    free(l->sub4);
    free(l->sub6);
    l->sub4 = c4;
    l->n_sub4 = n4;
    l->sub6 = c6;
    l->n_sub6 = n6;
    return FIRC_OK;
}

static void mask_prefix(uint8_t *addr, unsigned cidr, size_t len) {
    for (size_t i = 0; i < len; i++) {
        unsigned bit = 8u * (unsigned)i;
        unsigned keep = cidr >= bit + 8u ? 8u : cidr > bit ? cidr - bit : 0u;
        addr[i] &= (uint8_t)(0xffu << (8u - keep));
    }
}

static int prefix_cmp(const void *a, const void *b) {
    const firc_nf_prefix_t *x = a, *y = b;
    if (x->family != y->family) { return x->family < y->family ? -1 : 1; }
    if (x->cidr != y->cidr) { return x->cidr < y->cidr ? -1 : 1; }
    return memcmp(x->addr, y->addr, sizeof(x->addr));
}

static void put_prefix(firc_nf_prefix_t *p, uint8_t family, const uint8_t *addr, size_t len, uint8_t cidr,
                       uint8_t max) {
    p->family = family;
    p->cidr = cidr < max ? cidr : max;
    memcpy(p->addr, addr, len);
    mask_prefix(p->addr, p->cidr, len);
}

firc_err_t firc_nf_prefixes_of(const firc_ipv4_subnet_t *v4, size_t n4, const firc_ipv6_subnet_t *v6, size_t n6,
                               firc_nf_prefixes_t *out) {
    out->v = NULL;
    out->n = 0;
    size_t n = n4 + n6;
    if (n == 0) { return FIRC_OK; }
    firc_nf_prefix_t *v = calloc(n, sizeof(*v));
    if (v == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n4; i++) { put_prefix(&v[i], 4, v4[i].addr, 4, v4[i].cidr, 32); }
    for (size_t i = 0; i < n6; i++) { put_prefix(&v[n4 + i], 6, v6[i].addr, 16, v6[i].cidr, 128); }
    qsort(v, n, sizeof(*v), prefix_cmp);
    out->v = v;
    out->n = n;
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_prefixes(const firc_ipset_to_link_t *l, firc_nf_prefixes_t *out) {
    return firc_nf_prefixes_of(l->sub4, l->n_sub4, l->sub6, l->n_sub6, out);
}

bool firc_subnet4_is_bare(const firc_ipv4_subnet_t *s) {
    return s->proto == 0 && s->cidr != 0;
}

bool firc_subnet6_is_bare(const firc_ipv6_subnet_t *s) {
    return s->proto == 0 && s->cidr != 0;
}

static _Atomic size_t g_bare_builds;

size_t firc_ipset_to_link_bare_builds_for_test(void) { return g_bare_builds; }

firc_nf_print_t firc_ipset_to_link_bare_print(const firc_ipset_to_link_t *l, uint8_t family) {
    return family == 4 ? l->bare_print[0] : family == 6 ? l->bare_print[1] : (firc_nf_print_t){0, 0};
}

static firc_err_t bare_of(const firc_ipv4_subnet_t *sub4, size_t n_sub4, const firc_ipv6_subnet_t *sub6,
                          size_t n_sub6, uint8_t family, firc_nf_prefixes_t *out) {
    out->v = NULL;
    out->n = 0;
    if (family != 4 && family != 6) { return FIRC_ERR_INVAL; }
    size_t k = 0;
    if (family == 4) {
        for (size_t i = 0; i < n_sub4; i++) { k += firc_subnet4_is_bare(&sub4[i]); }
    } else {
        for (size_t i = 0; i < n_sub6; i++) { k += firc_subnet6_is_bare(&sub6[i]); }
    }
    firc_nf_prefix_t *v = k ? calloc(k, sizeof(*v)) : NULL;
    if (k && v == NULL) { return FIRC_ERR_NOMEM; }
    firc_nf_prefixes_t bare = {.v = v, .n = 0};
    if (family == 4) {
        for (size_t i = 0; i < n_sub4; i++) {
            if (!firc_subnet4_is_bare(&sub4[i])) { continue; }
            put_prefix(&v[bare.n++], 4, sub4[i].addr, 4, sub4[i].cidr, 32);
        }
    } else {
        for (size_t i = 0; i < n_sub6; i++) {
            if (!firc_subnet6_is_bare(&sub6[i])) { continue; }
            put_prefix(&v[bare.n++], 6, sub6[i].addr, 16, sub6[i].cidr, 128);
        }
    }
    if (bare.n > 1) { qsort(v, bare.n, sizeof(*v), prefix_cmp); }
    size_t u = 0;
    for (size_t i = 0; i < bare.n; i++) {
        if (u == 0 || prefix_cmp(&v[u - 1], &v[i]) != 0) { v[u++] = v[i]; }
    }
    out->v = v;
    out->n = u;
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_bare(const firc_ipset_to_link_t *l, uint8_t family, firc_nf_prefixes_t *out) {
    g_bare_builds++;
    return bare_of(l->sub4, l->n_sub4, l->sub6, l->n_sub6, family, out);
}

firc_nf_print_t firc_nf_prefixes_print(const firc_nf_prefixes_t *p) {
    uint64_t h = 0xcbf29ce484222325ULL;
    for (size_t i = 0; i < p->n; i++) {
        size_t len = p->v[i].family == 6 ? 16 : 4;
        h = (h ^ p->v[i].cidr) * 0x100000001b3ULL;
        for (size_t j = 0; j < len; j++) { h = (h ^ p->v[i].addr[j]) * 0x100000001b3ULL; }
    }
    return (firc_nf_print_t){.n = p->n, .hash = h};
}

firc_err_t firc_ipset_to_link_set_name_of(const char *chain, uint8_t family, bool scratch, char *out, size_t cap) {
    if (family != 4 && family != 6) { return FIRC_ERR_INVAL; }
    size_t lim = cap < FIRC_SET_NAME_MAX ? cap : FIRC_SET_NAME_MAX;
    int n = snprintf(out, cap, "%s%c%u", chain, scratch ? 'T' : 'S', (unsigned)family);
    return n < 0 || (size_t)n >= lim ? FIRC_ERR_INVAL : FIRC_OK;
}

const char *firc_ipset_to_link_chain(const firc_ipset_to_link_t *l) {
    return l->chain_name;
}

static bool prefix_covered(const firc_nf_prefix_t *w, const firc_nf_prefixes_t *now, const bool *present) {
    size_t len = w->family == 6 ? 16 : 4;
    for (unsigned c = 0; c <= w->cidr; c++) {
        if (!present[c]) { continue; }
        firc_nf_prefix_t key = {.family = w->family, .cidr = (uint8_t)c};
        memcpy(key.addr, w->addr, len);
        mask_prefix(key.addr, c, len);
        if (bsearch(&key, now->v, now->n, sizeof(*now->v), prefix_cmp) != NULL) { return true; }
    }
    return false;
}

static void mark_present(const firc_nf_prefixes_t *now, bool present[2][129]) {
    for (size_t i = 0; i < now->n; i++) { present[now->v[i].family == 6][now->v[i].cidr] = true; }
}

bool firc_nf_prefixes_narrow(const firc_nf_prefixes_t *was, const firc_nf_prefixes_t *now) {
    bool present[2][129] = {{false}};
    mark_present(now, present);
    for (size_t i = 0; i < was->n; i++) {
        if (!prefix_covered(&was->v[i], now, present[was->v[i].family == 6])) { return true; }
    }
    return false;
}

firc_err_t firc_nf_prefixes_lost(const firc_nf_prefixes_t *was, const firc_nf_prefixes_t *now, firc_nf_prefixes_t *out) {
    out->v = NULL;
    out->n = 0;
    if (was->n == 0) { return FIRC_OK; }
    firc_nf_prefix_t *v = malloc(was->n * sizeof(*v));
    if (v == NULL) { return FIRC_ERR_NOMEM; }
    bool present[2][129] = {{false}};
    mark_present(now, present);
    size_t n = 0;
    for (size_t i = 0; i < was->n; i++) {
        if (!prefix_covered(&was->v[i], now, present[was->v[i].family == 6])) { v[n++] = was->v[i]; }
    }
    if (n == 0) {
        free(v);
        return FIRC_OK;
    }
    out->v = v;
    out->n = n;
    return FIRC_OK;
}

firc_err_t firc_nf_prefixes_join(firc_nf_prefixes_t *to, const firc_nf_prefix_t *p, size_t n, uint8_t family) {
    size_t m = 0;
    for (size_t i = 0; i < n; i++) { m += p[i].family == family; }
    if (m == 0) { return FIRC_OK; }
    firc_nf_prefix_t *add = malloc(m * sizeof(*add));
    firc_nf_prefix_t *out = malloc((to->n + m) * sizeof(*out));
    if (add == NULL || out == NULL) {
        free(add);
        free(out);
        return FIRC_ERR_NOMEM;
    }
    m = 0;
    for (size_t i = 0; i < n; i++) {
        if (p[i].family == family) { add[m++] = p[i]; }
    }
    qsort(add, m, sizeof(*add), prefix_cmp);
    size_t i = 0, j = 0, k = 0;
    while (i < to->n || j < m) {
        const firc_nf_prefix_t *next = j >= m || (i < to->n && prefix_cmp(&to->v[i], &add[j]) <= 0) ? &to->v[i++] : &add[j++];
        if (k == 0 || prefix_cmp(&out[k - 1], next) != 0) { out[k++] = *next; }
    }
    free(add);
    free(to->v);
    to->v = out;
    to->n = k;
    return FIRC_OK;
}

void firc_nf_prefixes_clear(firc_nf_prefixes_t *p) {
    if (p == NULL) { return; }
    free(p->v);
    p->v = NULL;
    p->n = 0;
}

void firc_nf_devices_clear(firc_nf_devices_t *d) {
    if (d == NULL) { return; }
    free(d->deny);
    free(d->allow);
    memset(d, 0, sizeof(*d));
}

/* Only the fields of the source's kind, never memcmp: the padding is garbage. */
static bool source_equal(const firc_nf_source_t *a, const firc_nf_source_t *b) {
    if (a->kind != b->kind) { return false; }
    switch (a->kind) {
    case FIRC_NF_SRC_ADDR:
        return a->family == b->family && a->prefix == b->prefix &&
               memcmp(a->addr, b->addr, a->family == 6 ? 16 : 4) == 0;
    case FIRC_NF_SRC_MAC:
        return memcmp(a->mac, b->mac, sizeof(a->mac)) == 0;
    case FIRC_NF_SRC_MARK:
        return ((a->mark ^ b->mark) & FIRC_MARK_POLICY_MASK) == 0;
    default:
        return true;
    }
}

static bool sources_equal(const firc_nf_source_t *a, const firc_nf_source_t *b, size_t n) {
    for (size_t i = 0; i < n; i++) {
        if (!source_equal(&a[i], &b[i])) { return false; }
    }
    return true;
}

firc_err_t firc_nf_devices_push(firc_nf_devices_t *d, bool deny, const firc_nf_source_t *s) {
    firc_nf_source_t **v = deny ? &d->deny : &d->allow;
    size_t *n = deny ? &d->n_deny : &d->n_allow;
    size_t *cap = deny ? &d->cap_deny : &d->cap_allow;
    for (size_t i = 0; i < *n; i++) {
        if (source_equal(&(*v)[i], s)) { return FIRC_OK; }
    }
    if (*n >= *cap) {
        size_t room = *n != 0 ? *n * 2 : 4;
        firc_nf_source_t *grown = realloc(*v, room * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        *v = grown;
        *cap = room;
    }
    (*v)[(*n)++] = *s;
    return FIRC_OK;
}

bool firc_nf_devices_equal(const firc_nf_devices_t *a, const firc_nf_devices_t *b) {
    return a->active == b->active && a->allow_all == b->allow_all && a->n_deny == b->n_deny &&
           a->n_allow == b->n_allow && sources_equal(a->deny, b->deny, a->n_deny) &&
           sources_equal(a->allow, b->allow, a->n_allow);
}

static bool source_in(const firc_nf_source_t *s, const firc_nf_source_t *v, size_t n) {
    for (size_t i = 0; i < n; i++) {
        if (source_equal(s, &v[i])) { return true; }
    }
    return false;
}

/* An inactive chain counts as marking every device. */
bool firc_nf_devices_narrows(const firc_nf_devices_t *was, const firc_nf_devices_t *now) {
    if (was->active && !was->allow_all && was->n_allow == 0) { return false; }
    bool was_all = !was->active || was->allow_all;
    bool now_all = !now->active || now->allow_all;
    size_t was_deny = was->active ? was->n_deny : 0;
    size_t now_deny = now->active ? now->n_deny : 0;
    for (size_t i = 0; i < now_deny; i++) {
        if (!source_in(&now->deny[i], was->deny, was_deny)) { return true; }
    }
    if (now_all) { return false; }
    if (was_all) { return true; }
    for (size_t i = 0; i < was->n_allow; i++) {
        if (!source_in(&was->allow[i], now->allow, now->n_allow)) { return true; }
    }
    return false;
}

static firc_err_t sources_copy(const firc_nf_source_t *src, size_t n, firc_nf_source_t **out) {
    *out = NULL;
    if (n == 0) { return FIRC_OK; }
    *out = malloc(n * sizeof(**out));
    if (*out == NULL) { return FIRC_ERR_NOMEM; }
    memcpy(*out, src, n * sizeof(**out));
    return FIRC_OK;
}

firc_err_t firc_nf_devices_copy(firc_nf_devices_t *dst, const firc_nf_devices_t *src) {
    firc_nf_devices_t c = {.active = src->active, .allow_all = src->allow_all, .n_deny = src->n_deny,
                           .n_allow = src->n_allow, .cap_deny = src->n_deny, .cap_allow = src->n_allow};
    firc_err_t err = sources_copy(src->deny, src->n_deny, &c.deny);
    if (err == FIRC_OK) { err = sources_copy(src->allow, src->n_allow, &c.allow); }
    if (err != FIRC_OK) {
        firc_nf_devices_clear(&c);
        memset(dst, 0, sizeof(*dst));
        return err;
    }
    *dst = c;
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_set_devices(firc_ipset_to_link_t *l, const firc_nf_devices_t *d, bool *changed) {
    if (l == NULL || d == NULL) { return FIRC_ERR_INVAL; }
    bool same = firc_nf_devices_equal(&l->dev, d);
    if (changed) { *changed = !same; }
    if (same) { return FIRC_OK; }
    firc_nf_devices_t c;
    firc_err_t err = firc_nf_devices_copy(&c, d);
    if (err != FIRC_OK) { return err; }
    firc_nf_devices_clear(&l->dev);
    l->dev = c;
    return FIRC_OK;
}

const firc_nf_devices_t *firc_ipset_to_link_devices(const firc_ipset_to_link_t *l) { return &l->dev; }

static const char *proto_name(uint8_t proto) {
    return proto == IPPROTO_TCP ? "tcp" : proto == IPPROTO_UDP ? "udp" : NULL;
}

/* Original direction and not yet handled. */
static firc_err_t emit_subnet_rule(firc_ipt_t *ipt, const char *chain, const char *const *dst, size_t nd,
                                   const char *const *set, size_t ns, uint8_t proto, const char *ports,
                                   const char *mark_str, const char *jump) {
    char handled[32];
    snprintf(handled, sizeof(handled), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_HANDLED);
    const char *pn = proto_name(proto);
    const char *head[8];
    size_t nh = 0;
    for (size_t i = 0; i < nd; i++) { head[nh++] = dst[i]; }
    if (pn != NULL) { head[nh++] = "-p"; head[nh++] = pn; }
    const char *pm[4];
    size_t np = 0;
    if (pn != NULL && ports[0] != '\0') {
        bool list = strchr(ports, ',') != NULL;
        pm[np++] = "-m";
        pm[np++] = list ? "multiport" : pn;
        pm[np++] = list ? "--dports" : "--dport";
        pm[np++] = ports;
    }
    const char *mk[32], *cm[32];
    size_t nm = 0, nc = 0;
    for (size_t i = 0; i < nh; i++) { mk[nm++] = head[i]; cm[nc++] = head[i]; }
    const char *dir[] = {"-m", "conntrack", "--ctdir", "ORIGINAL"};
    for (size_t i = 0; i < 4; i++) { mk[nm++] = dir[i]; cm[nc++] = dir[i]; }
    const char *unm[] = {"-m", "mark", "!", "--mark", handled};
    for (size_t i = 0; i < 5; i++) { mk[nm++] = unm[i]; }
    for (size_t i = 0; i < ns; i++) { mk[nm++] = set[i]; cm[nc++] = set[i]; }
    for (size_t i = 0; i < np; i++) { mk[nm++] = pm[i]; cm[nc++] = pm[i]; }
    if (jump != NULL) {
        mk[nm++] = "-j";
        mk[nm++] = jump;
        return firc_ipt_append(ipt, "mangle", chain, mk, nm);
    }
    const char *tgt[] = {"-j", "MARK", "--set-xmark", mark_str};
    for (size_t i = 0; i < 4; i++) { mk[nm++] = tgt[i]; }
    firc_err_t err = firc_ipt_append(ipt, "mangle", chain, mk, nm);
    if (err != FIRC_OK) { return err; }
    const char *save[] = {"-j", "CONNMARK", "--save-mark", "--nfmask", "0x40ff0000", "--ctmask", "0x40ff0000"};
    for (size_t i = 0; i < 7; i++) { cm[nc++] = save[i]; }
    return firc_ipt_append(ipt, "mangle", chain, cm, nc);
}

firc_err_t firc_ipset_to_link_build_subnet_rules_dev(firc_ipt_t *ipt, const char *chain_name, uint32_t mark,
                                                     const firc_ipv4_subnet_t *v4, size_t n4,
                                                     const firc_ipv6_subnet_t *v6, size_t n6,
                                                     const firc_nf_devices_t *dev) {
    if (!ipt) { return FIRC_OK; }
    bool with_dev = dev != NULL && dev->active;
    char dchain[128];
    if (with_dev && firc_ipset_to_link_devices_chain_of(chain_name, dchain, sizeof(dchain)) != FIRC_OK) {
        return FIRC_ERR_INVAL;
    }
    char mark_str[32];
    snprintf(mark_str, sizeof(mark_str), "0x%x/0x%x", mark, FIRC_MARK_WRITE_MASK);
    bool is6 = firc_ipt_proto(ipt) == FIRC_IPT_PROTO_IPV6;
    size_t n = is6 ? n6 : n4;
    bool any_bare = false;
    for (size_t i = 0; i < n; i++) {
        /* Ports without a protocol would mark every port. */
        if ((is6 ? v6[i].proto : v4[i].proto) == 0 && (is6 ? v6[i].ports : v4[i].ports)[0] != '\0') {
            return FIRC_ERR_INVAL;
        }
        any_bare = any_bare || (is6 ? firc_subnet6_is_bare(&v6[i]) : firc_subnet4_is_bare(&v4[i]));
    }
    const char *jump = with_dev ? dchain : NULL;
    if (any_bare) {
        char set[FIRC_SET_NAME_MAX];
        firc_err_t err = firc_ipset_to_link_set_name_of(chain_name, is6 ? 6 : 4, false, set, sizeof(set));
        if (err != FIRC_OK) { return err; }
        const char *dst[] = {"-m", "set", "--match-set", set, "dst"};
        err = emit_subnet_rule(ipt, chain_name, NULL, 0, dst, 5, 0, "", mark_str, jump);
        if (err != FIRC_OK) { return err; }
    }
    char addr[INET6_ADDRSTRLEN], cidr[64];
    for (size_t i = 0; i < n; i++) {
        if (is6 ? firc_subnet6_is_bare(&v6[i]) : firc_subnet4_is_bare(&v4[i])) { continue; }
        const void *raw = is6 ? (const void *)v6[i].addr : (const void *)v4[i].addr;
        unsigned prefix = is6 ? v6[i].cidr : v4[i].cidr;
        if (inet_ntop(is6 ? AF_INET6 : AF_INET, raw, addr, sizeof(addr)) == NULL) {
            return FIRC_ERR_INVAL;
        }
        snprintf(cidr, sizeof(cidr), "%s/%u", addr, prefix);
        const char *dst[] = {"-d", cidr};
        firc_err_t err = emit_subnet_rule(ipt, chain_name, dst, prefix != 0 ? 2 : 0, NULL, 0,
                                          is6 ? v6[i].proto : v4[i].proto, is6 ? v6[i].ports : v4[i].ports,
                                          mark_str, jump);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_build_subnet_rules(firc_ipt_t *ipt, const char *chain_name,
                                                 uint32_t mark, const firc_ipv4_subnet_t *v4,
                                                 size_t n4, const firc_ipv6_subnet_t *v6,
                                                 size_t n6) {
    return firc_ipset_to_link_build_subnet_rules_dev(ipt, chain_name, mark, v4, n4, v6, n6, NULL);
}

firc_err_t firc_ipset_to_link_stage_for_test(firc_ipset_to_link_t *l) {
    if (!l) { return FIRC_ERR_INVAL; }
    firc_err_t e4 = build_iptables_rules(l, l->ipt4);
    firc_err_t e6 = build_iptables_rules(l, l->ipt6);
    return e4 != FIRC_OK ? e4 : e6;
}

firc_err_t firc_ipset_to_link_prepare_iptables(firc_ipset_to_link_t *l) {
    if (!l || !l->enabled) { return FIRC_OK; }

    firc_err_t e4 = build_iptables_rules(l, l->ipt4);
    firc_err_t e6 = build_iptables_rules(l, l->ipt6);
    return e4 != FIRC_OK ? e4 : e6;
}

static firc_err_t insert_ip_rule(firc_ipset_to_link_t *l, const char **failed) {
    /* Reject routes before the rule: a marked packet must never take the group's default. */
    if (l->pool != NULL) {
        l->pool_rejected = true; /* set first: rollback removes a half-installed pair */
        firc_err_t err = firc_pool_reject_install(l->rtnl, l->pool, l->table,
                                                  FIRC_POOL_REJECT_METRIC_GROUP);
        if (err != FIRC_OK) { *failed = "pool reject route"; return err; }
    } else {
        FIRC_WARN("no address pool for chain %s: its table cannot reject pool traffic",
                  l->chain_name);
    }
    /* The reply twin goes in before the rule it guards. */
    if (l->ipt4 && !is_blackhole(l)) {
        uint32_t value = firc_mark_group_value(l->mark_field);
        for (size_t i = 0; i < l->n_ifaces; i++) {
            (void)firc_rtnl_rule_del_reply(l->rtnl, AF_INET, value, FIRC_MARK_GROUP_MASK, l->ifaces[i], RT_TABLE_MAIN,
                                           FIRC_RULE_PRIORITY_REPLY);
            firc_err_t rerr = firc_rtnl_rule_add_reply(l->rtnl, AF_INET, value, FIRC_MARK_GROUP_MASK, l->ifaces[i],
                                                       RT_TABLE_MAIN, FIRC_RULE_PRIORITY_REPLY);
            if (rerr != FIRC_OK) {
                *failed = "ip rule";
                return rerr;
            }
            l->reply_added[i] = true;
        }
    }
    if (l->ipt4) {
        (void)firc_rtnl_rule_del(l->rtnl, AF_INET, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        firc_err_t err = firc_rtnl_rule_add(l->rtnl, AF_INET, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        if (err != FIRC_OK) { *failed = "ip rule"; return err; }
        l->v4.rule_added = true;
    }
    if (l->ipt6) {
        (void)firc_rtnl_rule_del(l->rtnl, AF_INET6, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        firc_err_t err = firc_rtnl_rule_add(l->rtnl, AF_INET6, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        if (err != FIRC_OK) { *failed = "ip rule"; return err; }
        l->v6.rule_added = true;
    }

    return FIRC_OK;
}

static firc_err_t delete_ip_rule(firc_ipset_to_link_t *l) {
    firc_err_t first_err = FIRC_OK;
    /* Rules out before reject routes, or a marked packet takes the default. */
    if (l->v4.rule_added) {
        firc_err_t err = firc_rtnl_rule_del(l->rtnl, AF_INET, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->v4.rule_added = false;
    }
    for (size_t i = 0; i < l->n_ifaces; i++) {
        if (!l->reply_added[i]) { continue; }
        firc_err_t err = firc_rtnl_rule_del_reply(l->rtnl, AF_INET, firc_mark_group_value(l->mark_field),
                                                  FIRC_MARK_GROUP_MASK, l->ifaces[i], RT_TABLE_MAIN,
                                                  FIRC_RULE_PRIORITY_REPLY);
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->reply_added[i] = false;
    }
    if (l->v6.rule_added) {
        firc_err_t err = firc_rtnl_rule_del(l->rtnl, AF_INET6, firc_mark_group_value(l->mark_field), FIRC_MARK_GROUP_MASK, l->table, firc_rule_priority_for_field(l->mark_field));
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->v6.rule_added = false;
    }
    if (l->pool_rejected && l->pool != NULL) {
        firc_err_t err = firc_pool_reject_remove(l->rtnl, l->pool, l->table,
                                                 FIRC_POOL_REJECT_METRIC_GROUP);
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->pool_rejected = false;
    }
    return first_err;
}

firc_err_t firc_iface_route_update(firc_rtnl_t *r, int family, uint32_t table, const char *iface_name,
                                  int ifindex, bool point_to_point, firc_iface_route_t *st) {
    bool has_gw = false;
    uint8_t gw[16] = {0};
    uint8_t gw_len = 0;

    if (!point_to_point) {
        bool found;
        bool gatewayless = false;
        firc_err_t err =
            firc_rtnl_gateway_for_iface2(r, family, ifindex, &found, gw, &gw_len, &gatewayless);
        if (err == FIRC_OK && !found && gatewayless) {
            /* On-link default: drop the learnt gateway, or a dead one stays forever. */
            FIRC_DEBUG("%s has a default route with no gateway; the group's default follows it", iface_name);
        } else if (err != FIRC_OK || !found) {
            /* No default found is not "no gateway": keep the last one rather than write a dev-only route. */
            if (err != FIRC_OK) {
                FIRC_WARN("gateway lookup failed for %s: %s; keeping the gateway last written", iface_name,
                          firc_err_str(err));
            } else if (st->has_gw) {
                FIRC_DEBUG("no default route on %s yet; keeping the gateway last written", iface_name);
            }
            if (st->has_gw) {
                has_gw = true;
                gw_len = st->gw_len;
                memcpy(gw, st->gw, sizeof(gw));
            }
        } else {
            has_gw = true;
        }
    }

    /* Always rewritten with NLM_F_REPLACE: link-down purges device routes behind our back. */
    bool unavailable = false;
    firc_err_t err = firc_rtnl_route_add_iface(r, family, table, FIRC_IFACE_ROUTE_METRIC, ifindex,
                                               has_gw ? gw : NULL, gw_len, &unavailable);
    if (err != FIRC_OK) { return err; }
    if (unavailable) {
        FIRC_DEBUG("interface %s is down or not ready for this IP family, skipping route", iface_name);
        st->present = false;
        return FIRC_OK;
    }

    st->present = true;
    st->has_gw = has_gw;
    st->gw_len = gw_len;
    memcpy(st->gw, gw, sizeof(st->gw));
    return FIRC_OK;
}

firc_err_t firc_iface_route_remove(firc_rtnl_t *r, int family, uint32_t table, const char *iface_name,
                                  firc_iface_route_t *st) {
    if (!st->present) { return FIRC_OK; }
    firc_link_info_t li;
    bool found;
    firc_rtnl_link_by_name(r, iface_name, &li, &found);
    int ifindex = found ? li.ifindex : 0;
    st->present = false;
    return firc_rtnl_route_del_iface(r, family, table, FIRC_IFACE_ROUTE_METRIC, ifindex,
                                     st->has_gw ? st->gw : NULL, st->gw_len);
}

static firc_err_t update_iface_route(firc_ipset_to_link_t *l, int family, int ifindex,
                                   bool point_to_point, family_state_t *fs) {
    return firc_iface_route_update(l->rtnl, family, l->table, active_name(l), ifindex, point_to_point,
                                   &fs->route);
}

static void keep_first(firc_err_t *first, firc_err_t err) {
    if (*first == FIRC_OK) { *first = err; }
}

static uint8_t fallback_type(int family) { return family == AF_INET6 ? RTN_UNREACHABLE : RTN_BLACKHOLE; }

static firc_err_t update_v6_route(firc_ipset_to_link_t *l, const firc_link_info_t *li) {
    bool carries = false;
    firc_err_t err = firc_rtnl_iface_carries_v6(l->rtnl, li->ifindex, &carries);
    if (err != FIRC_OK) { return err; }
    err = carries ? update_iface_route(l, AF_INET6, li->ifindex, li->point_to_point, &l->v6)
                  : firc_iface_route_remove(l->rtnl, AF_INET6, l->table, active_name(l), &l->v6.route);
    if (err != FIRC_OK) { return err; }
    if (carries != l->v6_carries || !l->v6_known) {
        if (!carries) {
            FIRC_DEBUG("%s carries no IPv6: its group's names get no IPv6 address", active_name(l));
        } else if (l->v6_known) {
            FIRC_DEBUG("%s carries IPv6 again: its group's names get IPv6 addresses", active_name(l));
        }
        l->v6_known = true;
        l->v6_carries = carries;
    }
    return FIRC_OK;
}

static firc_err_t update_iface_routes(firc_ipset_to_link_t *l, const firc_link_info_t *li) {
    firc_err_t first = FIRC_OK;
    if (l->ipt4) { keep_first(&first, update_iface_route(l, AF_INET, li->ifindex, li->point_to_point, &l->v4)); }
    if (l->ipt6) { keep_first(&first, update_v6_route(l, li)); }
    return first;
}

static firc_err_t add_fallback(firc_ipset_to_link_t *l, int family, family_state_t *fs) {
    firc_err_t err = firc_rtnl_route_add_reject(l->rtnl, family, l->table, 20, fallback_type(family));
    if (err == FIRC_OK) { fs->fallback_added = true; }
    return err;
}

static bool say_missing(firc_ipset_to_link_t *l, bool found, const firc_link_info_t *li) {
    if (!found) {
        bool ours = l->tunnel_dev != NULL && l->tunnel_dev(l->tunnel_ud, active_name(l));
        firc_log_level_t level = ours ? FIRC_LOG_INFO : FIRC_LOG_WARN;
        firc_logrep_fail(&l->link_missing, level, NULL, "interface %s not found, it can be caught later",
                         active_name(l));
        return true;
    }
    if (!li->up) {
        firc_logrep_fail(&l->link_missing, FIRC_LOG_WARN, NULL, "interface %s is down", active_name(l));
        return true;
    }
    return false;
}

static firc_err_t insert_iface_route(firc_ipset_to_link_t *l) {
    firc_link_info_t li;
    bool found;
    firc_err_t err = firc_rtnl_link_by_name(l->rtnl, active_name(l), &li, &found);
    if (err != FIRC_OK) { return err; }
    if (say_missing(l, found, &li)) { return FIRC_OK; }
    (void)firc_logrep_ok(&l->link_missing, NULL, "interface %s", active_name(l));
    return update_iface_routes(l, &li);
}

static firc_err_t insert_ip_route(firc_ipset_to_link_t *l) {
    firc_err_t first = FIRC_OK;
    if (l->ipt4) { keep_first(&first, add_fallback(l, AF_INET, &l->v4)); }
    if (l->ipt6) { keep_first(&first, add_fallback(l, AF_INET6, &l->v6)); }
    if (is_blackhole(l) || l->blocked) { return first; }
    keep_first(&first, insert_iface_route(l));
    return first;
}

static firc_err_t delete_ip_route(firc_ipset_to_link_t *l) {
    firc_err_t first_err = FIRC_OK;

    firc_err_t e4 = firc_iface_route_remove(l->rtnl, AF_INET, l->table, active_name(l), &l->v4.route);
    if (e4 != FIRC_OK && first_err == FIRC_OK) { first_err = e4; }
    firc_err_t e6 = firc_iface_route_remove(l->rtnl, AF_INET6, l->table, active_name(l), &l->v6.route);
    if (e6 != FIRC_OK && first_err == FIRC_OK) { first_err = e6; }
    if (l->v4.fallback_added) {
        firc_err_t err = firc_rtnl_route_del_reject(l->rtnl, AF_INET, l->table, 20, fallback_type(AF_INET));
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->v4.fallback_added = false;
    }
    if (l->v6.fallback_added) {
        firc_err_t err = firc_rtnl_route_del_reject(l->rtnl, AF_INET6, l->table, 20, fallback_type(AF_INET6));
        if (err != FIRC_OK && first_err == FIRC_OK) { first_err = err; }
        l->v6.fallback_added = false;
    }
    return first_err;
}

/* A zero group value would match every unmarked flow on the router. */
void firc_ipset_to_link_flush_mark(firc_ct_t *ct, uint32_t group_value, const char *who) {
    if (ct == NULL || group_value == 0) { return; }
    size_t dropped = 0;
    /* Handled bit in value and mask: the field alone would flush firmware-policy flows. */
    firc_err_t err = firc_ct_flush_by_mark(ct, group_value | FIRC_MARK_HANDLED,
                                           FIRC_MARK_GROUP_MASK | FIRC_MARK_HANDLED, &dropped);
    if (dropped > 0) {
        FIRC_DEBUG("dropped %zu live flow(s) of %s", dropped, who);
    }
    if (err != FIRC_OK) {
        FIRC_WARN("could not drop all of %s's live flows: %s; any left keep their old route "
                  "until they end",
                  who, firc_err_str(err));
    }
}

uint32_t firc_ipset_to_link_mark_field(const firc_ipset_to_link_t *l) {
    return l == NULL ? 0 : l->mark_field;
}

bool firc_ipset_to_link_has_iface_route(const firc_ipset_to_link_t *l, int family) {
    if (l == NULL) { return false; }
    return family == AF_INET6 ? l->v6.route.present : l->v4.route.present;
}

static firc_err_t teardown(firc_ipset_to_link_t *l) {
    /* Rule first, then routes, so the rule never points at an emptying table. */
    firc_err_t e2 = delete_ip_rule(l);
    firc_err_t e1 = delete_ip_route(l);
    if (e1 != FIRC_OK) { return e1; }
    return e2;
}

firc_err_t firc_ipset_to_link_enable(firc_ipset_to_link_t *l, const char **step) {
    if (l->enabled) { return FIRC_OK; }
    const char *failed = NULL;

    uint32_t idx;
    firc_err_t err = firc_rtnl_alloc_mark_table(l->rtnl, l->start_idx, &idx);
    if (err != FIRC_OK) {
        if (step) { *step = "routing table"; }
        return err;
    }
    l->table = idx;
    uint32_t field = 0;
    /* By group id, so a re-created group keeps the field its live flows carry. */
    firc_err_t ferr = firc_rtnl_alloc_mark_field_for(l->rtnl, l->group_id, &field);
    if (ferr != FIRC_OK) {
        if (step) { *step = "mark field"; }
        return ferr;
    }
    l->mark_field = field;
    if (!firc_mark_for_field(field, &l->mark)) {
        if (step) { *step = "mark field"; }
        return FIRC_ERR_LIMIT;
    }

    /* Routes before the rule that points at them. */
    err = insert_ip_route(l);
    if (err != FIRC_OK) { failed = "route"; }
    if (err == FIRC_OK) { err = insert_ip_rule(l, &failed); }

    if (err != FIRC_OK) {
        teardown(l);
        if (step) { *step = failed; }
        return err;
    }

    l->enabled = true;
    FIRC_DEBUG("using ip table and mark 0x%x for chain %s", l->mark, l->chain_name);
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_disable(firc_ipset_to_link_t *l) {
    if (!l->enabled) { return FIRC_OK; }
    l->enabled = false;
    return teardown(l);
}

static bool holds_reply(const firc_ipset_to_link_t *l, const char *iif) {
    for (size_t i = 0; i < l->n_ifaces; i++) {
        if (l->reply_added[i] && strcmp(l->ifaces[i], iif) == 0) { return true; }
    }
    return false;
}

firc_err_t firc_ipset_to_link_release(firc_ipset_to_link_t *l, const firc_ipset_to_link_t *newer) {
    if (newer != NULL && newer->mark_field == l->mark_field) {
        for (size_t i = 0; i < l->n_ifaces; i++) {
            if (l->reply_added[i] && holds_reply(newer, l->ifaces[i])) { l->reply_added[i] = false; }
        }
    }
    firc_err_t err = firc_ipset_to_link_disable(l);
    firc_ipset_to_link_free(l);
    return err;
}

firc_err_t firc_ipset_to_link_set_ifaces(firc_ipset_to_link_t *l, const char *const *names, size_t n, size_t active,
                                         bool blocked) {
    if (l == NULL || l->enabled || n == 0 || n > FIRC_IPSET_TO_LINK_IFACES_MAX || active >= n) {
        return FIRC_ERR_INVAL;
    }
    char *copy[FIRC_IPSET_TO_LINK_IFACES_MAX] = {NULL};
    for (size_t i = 0; i < n; i++) {
        copy[i] = strdup(names[i]);
        if (copy[i] == NULL) {
            for (size_t k = 0; k < i; k++) { free(copy[k]); }
            return FIRC_ERR_NOMEM;
        }
    }
    for (size_t i = 0; i < l->n_ifaces; i++) { free(l->ifaces[i]); }
    memcpy(l->ifaces, copy, sizeof(copy));
    l->n_ifaces = n;
    l->active = active;
    l->blocked = blocked;
    return FIRC_OK;
}

firc_err_t firc_ipset_to_link_set_active(firc_ipset_to_link_t *l, size_t to, bool blocked) {
    if (l == NULL || to >= l->n_ifaces) { return FIRC_ERR_INVAL; }
    if (!l->enabled) {
        l->active = to;
        l->blocked = blocked;
        return FIRC_OK;
    }
    firc_err_t first = FIRC_OK;
    if (to != l->active || blocked) {
        keep_first(&first, firc_iface_route_remove(l->rtnl, AF_INET, l->table, active_name(l), &l->v4.route));
        keep_first(&first, firc_iface_route_remove(l->rtnl, AF_INET6, l->table, active_name(l), &l->v6.route));
        memset(&l->v4.route, 0, sizeof(l->v4.route));
        memset(&l->v6.route, 0, sizeof(l->v6.route));
        l->v6_known = false;
        firc_logrep_reset(&l->link_missing);
    }
    l->active = to;
    l->blocked = blocked;
    if (!blocked && !is_blackhole(l)) { keep_first(&first, insert_iface_route(l)); }
    return first;
}

size_t firc_ipset_to_link_active(const firc_ipset_to_link_t *l, bool *blocked) {
    if (blocked != NULL) { *blocked = l->blocked; }
    return l->active;
}

const char *firc_ipset_to_link_active_iface(const firc_ipset_to_link_t *l) { return active_name(l); }

firc_err_t firc_ipset_to_link_on_link_up(firc_ipset_to_link_t *l) {
    if (!l->enabled || l->blocked) { return FIRC_OK; }
    return insert_ip_route(l);
}

void firc_ipset_to_link_set_tunnel_dev(firc_ipset_to_link_t *l, firc_tunnel_dev_fn fn, void *ud) {
    l->tunnel_dev = fn;
    l->tunnel_ud = ud;
}

void firc_ipset_to_link_remind(firc_ipset_to_link_t *l) {
    if (!l->enabled || l->blocked || l->link_missing.count == 0 || is_blackhole(l)) { return; }
    firc_link_info_t li;
    bool found;
    if (firc_rtnl_link_by_name(l->rtnl, active_name(l), &li, &found) != FIRC_OK) { return; }
    if (found && li.up) {
        (void)firc_logrep_ok(&l->link_missing, NULL, "interface %s", active_name(l));
    } else if (firc_logrep_due(&l->link_missing, UINT64_MAX)) {
        (void)say_missing(l, found, &li);
    }
}

firc_err_t firc_ipset_to_link_on_addr_change(firc_ipset_to_link_t *l) {
    if (!l->enabled || l->blocked || is_blackhole(l)) { return FIRC_OK; }

    firc_link_info_t li;
    bool found;
    firc_err_t err = firc_rtnl_link_by_name(l->rtnl, active_name(l), &li, &found);
    if (err != FIRC_OK) { return err; }
    if (!found) { return FIRC_OK; }
    /* A down link's routes would be refused; link-up puts them back. */
    if (!li.up) { return FIRC_OK; }

    return update_iface_routes(l, &li);
}

