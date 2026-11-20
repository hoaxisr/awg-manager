#include "firc/mss.h"

#include <stdio.h>

firc_err_t firc_mss_build_rules(firc_ipt_t *ipt, const char *chain_prefix, const char *const *devs, size_t n) {
    if (ipt == NULL || chain_prefix == NULL || (n > 0 && devs == NULL)) { return FIRC_ERR_INVAL; }
    if (n == 0) { return FIRC_OK; }

    char chain[128];
    if (snprintf(chain, sizeof(chain), "%s%s", chain_prefix, FIRC_MSS_CHAIN_SUFFIX) >= (int)sizeof(chain)) {
        return FIRC_ERR_INVAL;
    }
    firc_err_t err = firc_ipt_register_chain_override(ipt, "mangle", chain);
    if (err != FIRC_OK) { return err; }

    for (size_t i = 0; i < n; i++) {
        const char *rule[] = {"-o", devs[i], "-p", "tcp", "-m", "tcp", "--tcp-flags", "SYN,RST", "SYN",
                              "-j", "TCPMSS", "--clamp-mss-to-pmtu"};
        err = firc_ipt_append(ipt, "mangle", chain, rule, 12);
        if (err != FIRC_OK) { return err; }
    }

    err = firc_ipt_register_chain_patch(ipt, "mangle", "FORWARD");
    if (err != FIRC_OK) { return err; }
    const char *jump[] = {"-j", chain};
    return firc_ipt_insert(ipt, "mangle", "FORWARD", 1, jump, 2);
}
