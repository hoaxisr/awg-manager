#ifndef FIRC_CONNTRACK_H
#define FIRC_CONNTRACK_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

typedef struct firc_ct firc_ct_t;

/* NULL when conntrack is unavailable. Not thread-safe: flush from the loop thread only. */
firc_ct_t *firc_ct_open(void);
/* Test seam: takes ownership of an already-connected socket. */
firc_ct_t *firc_ct_open_fd(int fd);
/* Test seam: clock in ms for the flush budget; NULL means CLOCK_MONOTONIC. */
void firc_ct_set_clock(firc_ct_t *ct, int64_t (*clock_ms)(void));
void firc_ct_close(firc_ct_t *ct);

/* Each flush is bounded to ~1.5 s; FIRC_ERR_AGAIN means it stopped early and is not retried. */

/* Deletes entries with (ct->mark & mask) == value; *deleted (optional) counts confirmed deletes. */
firc_err_t firc_ct_flush_by_mark(firc_ct_t *ct, uint32_t value, uint32_t mask, size_t *deleted);

/* Deletes entries with the handled bit and a nonzero (ct->mark & field_mask). */
firc_err_t firc_ct_flush_handled_with_field(firc_ct_t *ct, uint32_t field_mask, size_t *deleted);

/* Deletes entries whose reply source is in a pool prefix; a prefix with len 0 is skipped. */
firc_err_t firc_ct_flush_pool_replies(firc_ct_t *ct, const uint8_t v4[4], uint8_t v4_len,
                                      const uint8_t v6[16], uint8_t v6_len, size_t *deleted);

/* A prefix a group reaches and the mark field it holds now. */
typedef struct {
    uint8_t family;
    uint8_t base[16];
    uint8_t prefix;
    bool is_subnet;
    uint32_t field; /* shifted: firc_mark_group_value(n) */
    bool inexact;
} firc_ct_chunk_t;

/* Deletes entries whose group field is left over from a previous run; call at startup after groups are enabled. */
firc_err_t firc_ct_flush_stale_group_marks(firc_ct_t *ct, const uint8_t v4[4], uint8_t v4_len,
                                           const uint8_t v6[16], uint8_t v6_len,
                                           const firc_ct_chunk_t *chunks, size_t n_chunks,
                                           uint32_t mask, size_t *deleted);
/* As firc_ct_flush_stale_group_marks, judging only the flows that carry field (shifted). */
firc_err_t firc_ct_flush_stale_field(firc_ct_t *ct, const uint8_t v4[4], uint8_t v4_len, const uint8_t v6[16],
                                     uint8_t v6_len, const firc_ct_chunk_t *chunks, size_t n_chunks, uint32_t mask,
                                     uint32_t field, size_t *deleted);
/* As above for a pool state that is not trusted: inside the pool only a field held[n] (n = field number,
 * NULL = none held) is left alone. */
firc_err_t firc_ct_flush_stale_marks_outside_pool(firc_ct_t *ct, const uint8_t v4[4], uint8_t v4_len,
                                                  const uint8_t v6[16], uint8_t v6_len,
                                                  const firc_ct_chunk_t *chunks, size_t n_chunks,
                                                  uint32_t mask, const bool *held, size_t *deleted);

#endif /* FIRC_CONNTRACK_H */
