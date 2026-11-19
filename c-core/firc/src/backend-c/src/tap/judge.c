#include "firc/tap.h"

#include <string.h>

bool firc_tap_pool_from_fakeip(const firc_fakeip_t *f, firc_tap_pool_t *out) {
    if (f == NULL || out == NULL) { return false; }
    memset(out, 0, sizeof(*out));
    bool any = firc_fakeip_pool_prefix(f, FIRC_FAM_V4, &out->v4_base, &out->v4_bits);
    any = firc_fakeip_pool_prefix(f, FIRC_FAM_V6, &out->v6_base, &out->v6_bits) || any;
    return any;
}

/* name, lower case, into out; false if empty or if it does not fit. */
static bool fold_name(const char *name, char *out, size_t cap) {
    size_t n = 0;
    for (; name[n] != '\0'; n++) {
        if (n + 1 >= cap) { return false; }
        char ch = name[n];
        out[n] = ch >= 'A' && ch <= 'Z' ? (char)(ch - 'A' + 'a') : ch;
    }
    out[n] = '\0';
    return n > 0;
}

static bool judge_name(const firc_tap_ctx_t *c, const firc_ip_t *client, uint8_t how,
                       firc_tap_find_t *out) {
    const firc_group_snapshot_t *g =
        firc_ruleset_snapshot_owner(c->snap, out->name, client, c->policy, c->device, c->ud, NULL);
    if (g == NULL) { return false; }
    out->group = g;
    out->how = how;
    return true;
}

static bool judgeable(const firc_tap_ctx_t *c, const firc_ip_t *client, const firc_ip_t *dst,
                      firc_tap_find_t *out) {
    if (out == NULL) { return false; }
    memset(out, 0, sizeof(*out));
    if (c == NULL || c->snap == NULL || client == NULL || dst == NULL) { return false; }
    return firc_dns_pipeline_covers(c->pipeline, client);
}

bool firc_tap_judge_addr(const firc_tap_ctx_t *c, const firc_ip_t *client, const firc_ip_t *dst,
                         firc_tap_find_t *out) {
    if (!judgeable(c, client, dst, out)) { return false; }
    if (!firc_recall_by_real(c->recall, dst, out->name, sizeof(out->name), NULL) ||
        out->name[0] == '\0') {
        out->name[0] = '\0';
        return false;
    }
    if (judge_name(c, client, FIRC_BYPASS_BY_ADDR, out)) { return true; }
    memset(out, 0, sizeof(*out));
    return false;
}

bool firc_tap_judge_sni(const firc_tap_ctx_t *c, const firc_ip_t *client, const firc_ip_t *dst,
                        const char *sni, firc_tap_find_t *out) {
    if (!judgeable(c, client, dst, out)) { return false; }
    if (sni == NULL || !fold_name(sni, out->name, sizeof(out->name))) {
        out->name[0] = '\0';
        return false;
    }
    if (judge_name(c, client, FIRC_BYPASS_BY_SNI, out)) { return true; }
    memset(out, 0, sizeof(*out));
    return false;
}
