#include "firc/netfilter_cleaner.h"

#include <stdbool.h>
#include <time.h>

#include "firc/pool_reject.h"

static firc_err_t clean_one(firc_ipt_t *ipt, const firc_fakeip_t *pool, unsigned family) {
    if (ipt == NULL) { return FIRC_OK; }
    firc_ipt_discard(ipt);
    firc_err_t err = firc_netfilter_register_base_chains(ipt, NULL);
    firc_ip_t base;
    uint8_t len = 0;
    if (err == FIRC_OK && pool != NULL && firc_fakeip_pool_prefix(pool, family, &base, &len)) {
        err = firc_pool_reject_build_rules(ipt, firc_ipt_prefix(ipt), &base, len);
    }
    if (err != FIRC_OK) {
        firc_ipt_discard(ipt);
        return err;
    }
    return firc_ipt_commit_full(ipt);
}

firc_err_t firc_netfilter_clean(firc_ipt_t *ipt4, firc_ipt_t *ipt6, const firc_fakeip_t *pool) {
    firc_err_t e1 = clean_one(ipt4, pool, FIRC_FAM_V4);
    firc_err_t e2 = clean_one(ipt6, pool, FIRC_FAM_V6);
    return e1 != FIRC_OK ? e1 : e2;
}

firc_err_t firc_netfilter_register_base_chains(firc_ipt_t *ipt4, firc_ipt_t *ipt6) {
    static const struct {
        const char *table;
        const char *chain;
    } base[] = {
        {"filter", "FORWARD"},
        {"mangle", "PREROUTING"},
        {"nat", "PREROUTING"},
        {"nat", "POSTROUTING"},
    };

    firc_ipt_t *ipts[2] = {ipt4, ipt6};
    for (size_t i = 0; i < 2; i++) {
        if (!ipts[i]) { continue; }
        for (size_t b = 0; b < sizeof(base) / sizeof(base[0]); b++) {
            firc_err_t err = firc_ipt_register_chain_patch(ipts[i], base[b].table, base[b].chain);
            if (err != FIRC_OK) { return err; }
        }
    }
    return FIRC_OK;
}

#define RETRY_ATTEMPTS 3

firc_err_t firc_netfilter_retry_races(firc_err_t (*step)(void *ud), void *ud) {
    firc_err_t err = FIRC_ERR_AGAIN;
    for (int attempt = 0; attempt < RETRY_ATTEMPTS && (err == FIRC_ERR_AGAIN || err == FIRC_ERR_CANCELED);
         attempt++) {
        if (attempt > 0) {
            struct timespec pause = {0, 200 * 1000 * 1000};
            nanosleep(&pause, NULL);
        }
        err = step(ud);
    }
    return err;
}

typedef struct {
    firc_ipt_t *ipt4, *ipt6;
    const firc_fakeip_t *pool;
} clean_args_t;

static firc_err_t clean_step(void *ud) {
    const clean_args_t *a = ud;
    return firc_netfilter_clean(a->ipt4, a->ipt6, a->pool);
}

firc_err_t firc_netfilter_clean_retrying(firc_ipt_t *ipt4, firc_ipt_t *ipt6, const firc_fakeip_t *pool) {
    clean_args_t a = {ipt4, ipt6, pool};
    return firc_netfilter_retry_races(clean_step, &a);
}
