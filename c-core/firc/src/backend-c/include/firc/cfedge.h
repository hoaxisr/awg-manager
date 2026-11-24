#ifndef FIRC_CFEDGE_H
#define FIRC_CFEDGE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip.h"
#include "firc/fakeip_addr.h"
#include "firc/loop.h"

typedef struct firc_cftable firc_cftable_t;

typedef struct {
    firc_ip_t addr;
    bool ok;
    double median_s;
    char name[254];
} firc_cfprobe_t;

firc_cftable_t *firc_cftable_new(void);
void firc_cftable_free(firc_cftable_t *t);

bool firc_cftable_target(const firc_cftable_t *t, const char *gid, unsigned fam, firc_fakeip_cf_cand_t *out);

bool firc_cftable_due(const firc_cftable_t *t, const char *gid, unsigned fam, int64_t now);

int firc_cftable_apply(firc_cftable_t *t, const char *gid, unsigned fam, const firc_cfprobe_t *r, size_t n,
                       int64_t now);

bool firc_cftable_recheck(firc_cftable_t *t, const char *gid);

void firc_cftable_forget(firc_cftable_t *t, const char *gid);

#define FIRC_CFPROBE_TRIES 3
#define FIRC_CFPROBE_TIMEOUT_S 3
#define FIRC_CFPROBE_MAX 4
#define FIRC_CF_TICK_MS 30000

typedef bool (*firc_cfprobe_fn)(void *ud, const char *host, const firc_ip_t *addr, uint32_t mark, double *secs);

bool firc_cfprobe_tls(void *ud, const char *host, const firc_ip_t *addr, uint32_t mark, double *secs);

void firc_cfprobe_run(firc_cfprobe_fn probe, void *ud, const firc_fakeip_cf_cand_t *c, size_t n, uint32_t mark,
                      firc_cfprobe_t *out);

typedef struct firc_cfjobs firc_cfjobs_t;

typedef void (*firc_cfjobs_done_fn)(void *ud, const char *gid, unsigned fam, uint64_t tag, const firc_cfprobe_t *r,
                                    size_t n);

firc_cfjobs_t *firc_cfjobs_new(firc_loop_t *loop, firc_cfjobs_done_fn done, void *ud);
void firc_cfjobs_free(firc_cfjobs_t *j);

void firc_cfjobs_set_probe(firc_cfjobs_t *j, firc_cfprobe_fn probe, void *ud);

bool firc_cfjobs_busy(const firc_cfjobs_t *j, const char *gid, unsigned fam);

firc_err_t firc_cfjobs_start(firc_cfjobs_t *j, const char *gid, unsigned fam, uint64_t tag, uint32_t mark,
                             const firc_fakeip_cf_cand_t *c, size_t n);

#endif
