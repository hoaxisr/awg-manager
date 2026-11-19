#ifndef FIRC_QUEUE_H
#define FIRC_QUEUE_H

#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

typedef enum firc_queue_policy {
    FIRC_QUEUE_REJECT = 0,
    FIRC_QUEUE_DROP_OLDEST,
} firc_queue_policy_t;

typedef struct firc_queue firc_queue_t;

/* Bounded MPMC queue; REJECT fails a full push with FIRC_ERR_LIMIT, DROP_OLDEST evicts. NULL on OOM. */
firc_queue_t *firc_queue_create(size_t capacity, firc_queue_policy_t policy);
void firc_queue_destroy(firc_queue_t *q);

/* Non-blocking. A full DROP_OLDEST queue hands the evicted element to *evicted (may be NULL). */
firc_err_t firc_queue_push(firc_queue_t *q, void *item, void **evicted);

/* Non-blocking pop: FIRC_ERR_AGAIN when empty and open. */
firc_err_t firc_queue_try_pop(firc_queue_t *q, void **item);

/* timeout_ms < 0 waits forever; FIRC_ERR_TIMEOUT on expiry, FIRC_ERR_CLOSED once closed and drained. */
firc_err_t firc_queue_pop(firc_queue_t *q, void **item, int timeout_ms);

/* Wakes blocked consumers; later pushes fail with FIRC_ERR_CLOSED. Does not free queued payloads. */
void firc_queue_close(firc_queue_t *q);

size_t firc_queue_len(firc_queue_t *q);
size_t firc_queue_capacity(const firc_queue_t *q);
uint64_t firc_queue_dropped(firc_queue_t *q);

#endif
