#ifndef FIRC_CANCEL_H
#define FIRC_CANCEL_H

#include <stdbool.h>

#include "firc/err.h"

typedef struct firc_cancel firc_cancel_t;

/* NULL when memory runs out. */
firc_cancel_t *firc_cancel_new(void);
void firc_cancel_free(firc_cancel_t *c);

/* Thread-safe; idempotent while raised. NULL-safe. */
void firc_cancel_raise(firc_cancel_t *c);

/* Lowers the flag; call from the owning thread at the start of an attempt, not mid-attempt. */
void firc_cancel_clear(firc_cancel_t *c);

/* NULL-safe: an absent token is never raised. */
bool firc_cancel_raised(const firc_cancel_t *c);

#endif
