#ifndef FIRC_LIFECYCLE_H
#define FIRC_LIFECYCLE_H

#include <stddef.h>

#include "firc/err.h"

typedef void (*firc_lc_destroy_fn)(void *ctx);

typedef struct firc_lc_entry {
    firc_lc_destroy_fn destroy;
    void *ctx;
    const char *name;
} firc_lc_entry_t;

typedef struct firc_lifecycle {
    firc_lc_entry_t *entries;
    size_t len;
    size_t cap;
} firc_lifecycle_t;

void firc_lc_init(firc_lifecycle_t *lc);

/* Registers a destructor for an object just created. */
firc_err_t firc_lc_push(firc_lifecycle_t *lc, firc_lc_destroy_fn destroy, void *ctx,
                    const char *name);

/* Failure path: runs destructors in reverse order and frees tracking. */
void firc_lc_fail(firc_lifecycle_t *lc);

/* Success path: drops tracking without running destructors. */
void firc_lc_commit(firc_lifecycle_t *lc);

#endif
