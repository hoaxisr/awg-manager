#include "firc/bytebuf.h"

#include <stdlib.h>
#include <string.h>

void firc_bytebuf_init(firc_bytebuf_t *b) {
    firc_bytebuf_init_bounded(b, 0);
}

void firc_bytebuf_init_bounded(firc_bytebuf_t *b, size_t max_cap) {
    b->data = NULL;
    b->len = 0;
    b->cap = 0;
    b->max_cap = max_cap ? max_cap : FIRC_BYTEBUF_MAX_DEFAULT;
}

void firc_bytebuf_free(firc_bytebuf_t *b) {
    if (!b) { return; }
    free(b->data);
    b->data = NULL;
    b->len = 0;
    b->cap = 0;
}

firc_err_t firc_bytebuf_append(firc_bytebuf_t *b, const void *data, size_t len) {
    if (len == 0) { return FIRC_OK; }

    if (len > b->max_cap || b->len > b->max_cap - len) { return FIRC_ERR_LIMIT; }

    if (b->len + len > b->cap) {
        size_t newcap = b->cap == 0 ? 4096 : b->cap;
        while (newcap < b->len + len) {
            if (newcap > b->max_cap / 2) {
                newcap = b->max_cap;
                break;
            }
            newcap *= 2;
        }
        if (newcap > b->max_cap) { newcap = b->max_cap; }
        uint8_t *tmp = realloc(b->data, newcap);
        if (!tmp) { return FIRC_ERR_NOMEM; }
        b->data = tmp;
        b->cap = newcap;
    }

    memcpy(b->data + b->len, data, len);
    b->len += len;
    return FIRC_OK;
}

firc_err_t firc_bytebuf_append_str(firc_bytebuf_t *b, const char *s) {
    return firc_bytebuf_append(b, s, strlen(s));
}

firc_err_t firc_bytebuf_append_byte(firc_bytebuf_t *b, uint8_t byte) {
    return firc_bytebuf_append(b, &byte, 1);
}
