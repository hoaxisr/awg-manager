#include "firc/lifecycle.h"

#include <stdlib.h>

#include "firc/log.h"

void firc_lc_init(firc_lifecycle_t *lc)
{
    lc->entries = NULL;
    lc->len = 0;
    lc->cap = 0;
}

firc_err_t firc_lc_push(firc_lifecycle_t *lc, firc_lc_destroy_fn destroy, void *ctx,
                    const char *name)
{
    if (lc->len == lc->cap) {
        size_t cap = lc->cap == 0 ? 8 : lc->cap * 2;
        firc_lc_entry_t *entries =
            realloc(lc->entries, cap * sizeof(firc_lc_entry_t));
        if (entries == NULL) {
            return FIRC_ERR_NOMEM;
        }
        lc->entries = entries;
        lc->cap = cap;
    }
    lc->entries[lc->len].destroy = destroy;
    lc->entries[lc->len].ctx = ctx;
    lc->entries[lc->len].name = name;
    lc->len++;
    return FIRC_OK;
}

void firc_lc_fail(firc_lifecycle_t *lc)
{
    while (lc->len > 0) {
        lc->len--;
        firc_lc_entry_t *e = &lc->entries[lc->len];
        FIRC_DEBUG("lifecycle: unwinding %s", e->name ? e->name : "?");
        e->destroy(e->ctx);
    }
    free(lc->entries);
    firc_lc_init(lc);
}

void firc_lc_commit(firc_lifecycle_t *lc)
{
    free(lc->entries);
    firc_lc_init(lc);
}
