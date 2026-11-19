#ifndef FIRC_STALE_MARKS_H
#define FIRC_STALE_MARKS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/conntrack.h"
#include "firc/err.h"
#include "firc/fakeip.h"
#include "firc/models.h"

typedef struct {
    const char *id;
    uint32_t field; /* 0: not in the kernel */
    const firc_ct_chunk_t *subnets;
    size_t n_subnets;
    bool inexact;
    bool field_kept;
} firc_stale_group_t;

/* False only on allocation failure, and then the caller must not sweep. Caller frees *out.
 * A list not loaded yet counts as a /0 in both families, only when field_kept (the field map says so). */
bool firc_stale_group_subnets(const firc_group_t *g, uint32_t field, bool field_kept, firc_ct_chunk_t **out,
                              size_t *out_n);

/* Without fields_loaded every flow carrying the handled bit goes. Unless pool_state_trusted, a flow to
 * the pool stays only on a field a group holds with field_kept. */
firc_err_t firc_stale_marks_sweep(firc_ct_t *ct, const firc_fakeip_t *pool, bool pool_state_trusted,
                                  bool fields_loaded, const firc_stale_group_t *groups, size_t n_groups,
                                  uint32_t mask, size_t *dropped);

/* Judges only the flows carrying field, against a trusted pool and every group's chunks and subnets. */
firc_err_t firc_stale_field_sweep(firc_ct_t *ct, const firc_fakeip_t *pool, const firc_stale_group_t *groups,
                                  size_t n_groups, uint32_t mask, uint32_t field, size_t *dropped);

#endif /* FIRC_STALE_MARKS_H */
