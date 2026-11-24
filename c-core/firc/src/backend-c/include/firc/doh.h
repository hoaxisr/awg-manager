#ifndef FIRC_DOH_H
#define FIRC_DOH_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip.h"
#include "firc/loop.h"

#define FIRC_DOH_MAX_URLS 64
#define FIRC_DOH_DETACH_MS 10000u

typedef struct firc_doh firc_doh_t;

typedef void (*firc_doh_done_fn)(void *ud, const uint8_t *resp, size_t len, bool ok);

typedef firc_err_t (*firc_doh_query_fn)(void *ud, uint16_t url_id, uint32_t mark, const uint8_t *query, size_t len,
                                        uint32_t timeout_ms, firc_doh_done_fn fn, void *fn_ud, uint64_t *ticket);

typedef struct firc_doh_ops {
    firc_doh_query_fn query;
    void (*cancel)(void *ud, uint64_t ticket);
    void *ud;
} firc_doh_ops_t;

firc_doh_t *firc_doh_new(firc_loop_t *loop);
firc_err_t firc_doh_start(firc_doh_t *d);
void firc_doh_free(firc_doh_t *d);

/* Before start. The client never connects to an address in this pool; its prefixes are copied. */
void firc_doh_set_pool(firc_doh_t *d, const firc_fakeip_t *pool);
/* Test seam, before start. */
void firc_doh_allow_for_test(firc_doh_t *d, bool http, bool loopback);

uint16_t firc_doh_intern(firc_doh_t *d, const char *url);
const char *firc_doh_url(const firc_doh_t *d, uint16_t id);

firc_err_t firc_doh_query(firc_doh_t *d, uint16_t url_id, uint32_t mark, const uint8_t *query, size_t len,
                          uint32_t timeout_ms, firc_doh_done_fn fn, void *ud, uint64_t *ticket);
void firc_doh_cancel(firc_doh_t *d, uint64_t ticket);
void firc_doh_forget_mark(firc_doh_t *d, uint32_t mark);

firc_doh_ops_t firc_doh_ops(firc_doh_t *d);

#endif
