#ifndef FIRC_ID_H
#define FIRC_ID_H

#include <stddef.h>
#include <stdbool.h>
#include <stdint.h>

#include "firc/err.h"

typedef struct firc_id {
    uint8_t b[4];
} firc_id_t;

#define FIRC_ID_STR_LEN 9 /* 8 hex chars + NUL */

typedef struct firc_id_set {
    uint32_t *slots; /* 0 = empty slot; the all-zero id is never stored */
    size_t n_slots;
    size_t len;
} firc_id_set_t;

/* True if id is in the set (always false for the zero id). */
bool firc_id_set_contains(const firc_id_set_t *set, firc_id_t id);

/* Adds id, growing as needed. FIRC_ERR_NOMEM if it cannot grow. */
firc_err_t firc_id_set_add(firc_id_set_t *set, firc_id_t id);

/* Releases the slots; the set is reusable afterwards. */
void firc_id_set_free(firc_id_set_t *set);

/* Requires exactly 8 hex chars (either case). */
firc_err_t firc_id_parse(const char *s, firc_id_t *out);

/* Writes lowercase hex; buf must be >= FIRC_ID_STR_LEN. */
void firc_id_format(firc_id_t id, char *buf);

bool firc_id_is_zero(firc_id_t id);
bool firc_id_equal(firc_id_t a, firc_id_t b);

/* Random non-zero ID from /dev/urandom; FIRC_ERR_SYS if none could be read. */
firc_err_t firc_id_random(firc_id_t *out);

typedef struct firc_id_pool {
    uint8_t bytes[1024];
    size_t at;
    size_t len;
} firc_id_pool_t;

/* Next random non-zero ID, refilled 256 at a time by one read; zero-initialise the pool before the first call.
 * FIRC_ERR_SYS if a refill could not be read. */
firc_err_t firc_id_pool_next(firc_id_pool_t *pool, firc_id_t *out);

#endif /* FIRC_ID_H */
