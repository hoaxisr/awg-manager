#include "firc/cancel.h"

#include <stdatomic.h>
#include <stdlib.h>

struct firc_cancel {
    atomic_bool raised;
};

firc_cancel_t *firc_cancel_new(void) {
    firc_cancel_t *c = calloc(1, sizeof(*c));
    if (!c) { return NULL; }
    atomic_init(&c->raised, false);
    return c;
}

void firc_cancel_free(firc_cancel_t *c) { free(c); }

void firc_cancel_raise(firc_cancel_t *c) {
    if (c) { atomic_store(&c->raised, true); }
}

void firc_cancel_clear(firc_cancel_t *c) {
    if (c) { atomic_store(&c->raised, false); }
}

bool firc_cancel_raised(const firc_cancel_t *c) {
    if (!c) { return false; }
    return atomic_load(&((firc_cancel_t *)c)->raised);
}
