#include "xt_internal.h"

static bool standard_permit(const firc_xt_entry_t *e) {
    const uint8_t *tg = e->bytes + firc_xt_rd16(e->bytes + firc_xt_at_toff(FIRC_IPT_PROTO_IPV4));
    if (tg[2] != '\0') { return false; }
    int32_t v = (int32_t)firc_xt_rd32(tg + 32);
    return v == XT_VERDICT_ACCEPT || v == XT_VERDICT_RETURN;
}

static bool chain_permits(firc_xt_table_t *t, const char *name) {
    firc_xt_chain_t *c = firc_xt_find_chain(t, name);
    if (c == NULL || c->hook >= 0) { return false; }
    for (size_t i = 0; i < c->n_rules; i++) {
        if (!standard_permit(&c->rules[i])) { return false; }
    }
    return true;
}

static bool exact_iface(const firc_xt_entry_t *e, bool egress, char name[16]) {
    xt_ipt_entry_t h;
    memcpy(&h, e->bytes, sizeof(h));
    const char *iface = egress ? h.ip.outiface : h.ip.iniface;
    const uint8_t *mask = egress ? h.ip.outiface_mask : h.ip.iniface_mask;
    size_t n = strnlen(iface, sizeof(h.ip.iniface));
    if (n == 0 || n >= sizeof(h.ip.iniface) || (h.ip.invflags & (egress ? XT_INV_VIA_OUT : XT_INV_VIA_IN)) != 0 ||
        mask[n] != 0xff) {
        return false;
    }
    memcpy(name, iface, n);
    name[n] = '\0';
    return true;
}

static bool listed(const firc_xt_acl_t *a, const char *name) {
    for (size_t i = 0; i < a->n; i++) {
        if (strcmp(a->iface[i], name) == 0) { return true; }
    }
    return false;
}

static firc_err_t judge_chain(firc_xt_table_t *t, const char *chain, bool egress, firc_xt_acl_t *out) {
    firc_xt_chain_t *c = firc_xt_find_chain(t, chain);
    if (c == NULL) { return FIRC_OK; }
    for (size_t i = 0; i < c->n_rules; i++) {
        const firc_xt_entry_t *e = &c->rules[i];
        bool permits = e->jump[0] != '\0' ? chain_permits(t, e->jump) : standard_permit(e);
        if (permits) { continue; }
        char name[16] = {0};
        if (!exact_iface(e, egress, name)) {
            out->all = true;
            continue;
        }
        if (listed(out, name)) { continue; }
        if (out->n == FIRC_XT_ACL_MAX) { return FIRC_ERR_LIMIT; }
        memcpy(out->iface[out->n++], name, sizeof(name));
    }
    return FIRC_OK;
}

firc_err_t firc_xt_acl_judge(firc_xt_table_t *t, const char *in_chain, const char *out_chain, firc_xt_acl_t *out) {
    memset(out, 0, sizeof(*out));
    firc_err_t err = judge_chain(t, in_chain, false, out);
    return err == FIRC_OK ? judge_chain(t, out_chain, true, out) : err;
}
