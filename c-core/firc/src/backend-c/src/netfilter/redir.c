#include "firc/redir.h"

#include <stdio.h>

#include "firc/mark.h"

firc_err_t firc_redir_build_rules(firc_ipt_t *ipt4, const char *chain_prefix, const firc_redir_group_t *g, size_t n,
                                  const char (*skip)[16], size_t n_skip) {
    if (ipt4 == NULL || chain_prefix == NULL || (n > 0 && g == NULL) ||
        (n_skip > 0 && skip == NULL)) { return FIRC_ERR_INVAL; }
    if (n == 0) { return FIRC_OK; }

    char chain[128];
    if (snprintf(chain, sizeof(chain), "%s%s", chain_prefix, FIRC_REDIR_CHAIN_SUFFIX) >= (int)sizeof(chain)) {
        return FIRC_ERR_INVAL;
    }
    firc_err_t err = firc_ipt_register_chain_override(ipt4, "nat", chain);
    if (err != FIRC_OK) { return err; }

    for (size_t i = 0; i < n_skip; i++) {
        const char *ret[] = {"-i", skip[i], "-j", "RETURN"};
        err = firc_ipt_append(ipt4, "nat", chain, ret, 4);
        if (err != FIRC_OK) { return err; }
    }

    for (size_t i = 0; i < n; i++) {
        char mark[32], port[8];
        snprintf(mark, sizeof(mark), "0x%x/0x%x", g[i].mark, FIRC_MARK_WRITE_MASK);
        snprintf(port, sizeof(port), "%u", (unsigned)g[i].port);
        const char *rule[] = {"-p", "tcp", "-m", "mark", "--mark", mark, "-j", "REDIRECT", "--to-ports", port};
        err = firc_ipt_append(ipt4, "nat", chain, rule, 10);
        if (err != FIRC_OK) { return err; }
    }

    err = firc_ipt_register_chain_patch(ipt4, "nat", "PREROUTING");
    if (err != FIRC_OK) { return err; }
    char handled[32];
    snprintf(handled, sizeof(handled), "0x%x/0x%x", FIRC_MARK_HANDLED, FIRC_MARK_HANDLED);
    const char *jump[] = {"-p", "tcp", "-m", "mark", "--mark", handled, "-m", "addrtype", "!", "--dst-type", "LOCAL",
                          "-j", chain};
    return firc_ipt_insert(ipt4, "nat", "PREROUTING", 1, jump, 13);
}

