#ifndef FIRC_BYTEBUF_H
#define FIRC_BYTEBUF_H

#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

#define FIRC_BYTEBUF_MAX_DEFAULT ((size_t)64 * 1024 * 1024) /* 64 MiB */

typedef struct firc_bytebuf {
    uint8_t *data;
    size_t len;
    size_t cap;
    size_t max_cap; /* hard limit; 0 means FIRC_BYTEBUF_MAX_DEFAULT */
} firc_bytebuf_t;

void firc_bytebuf_init(firc_bytebuf_t *b);
void firc_bytebuf_init_bounded(firc_bytebuf_t *b, size_t max_cap);
void firc_bytebuf_free(firc_bytebuf_t *b);

/* FIRC_ERR_LIMIT if growth would exceed max_cap; FIRC_ERR_NOMEM on allocation failure. */
firc_err_t firc_bytebuf_append(firc_bytebuf_t *b, const void *data, size_t len);
firc_err_t firc_bytebuf_append_str(firc_bytebuf_t *b, const char *s);
firc_err_t firc_bytebuf_append_byte(firc_bytebuf_t *b, uint8_t byte);

#endif
