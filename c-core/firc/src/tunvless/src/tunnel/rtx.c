/* Ring of unacknowledged bytes; see rtx.h. */
#include <stdlib.h>
#include <string.h>

#include "rtx.h"

int rtx_init(struct rtx *r, uint32_t cap) {
    r->buf = malloc(cap);
    r->cap = cap;
    r->len = 0;
    r->head = 0;
    return r->buf ? 0 : -1;
}

int rtx_grow(struct rtx *r, uint32_t cap) {
    if (cap <= r->cap) return -1;
    unsigned char *nb = malloc(cap);
    if (!nb) return -1;
    if (!r->buf) {
        r->buf = nb; r->cap = cap; r->len = 0; r->head = 0;
        return 0;
    }
    /* Unwrap: the old contents move to the start of the new buffer and head becomes 0. A plain
     * copy would be wrong: data that wrapped past the old end would no longer follow it, and the
     * next retransmit would send the wrong bytes. */
    uint32_t first = r->cap - r->head;
    if (first > r->len) first = r->len;
    memcpy(nb, r->buf + r->head, first);
    if (r->len > first) memcpy(nb + first, r->buf, r->len - first);
    free(r->buf);
    r->buf = nb;
    r->cap = cap;
    r->head = 0;
    return 0;
}

void rtx_done(struct rtx *r) {
    free(r->buf);
    r->buf = NULL;
    r->cap = r->len = r->head = 0;
}

uint32_t rtx_room(const struct rtx *r) {
    return r->cap - r->len;
}

void rtx_push(struct rtx *r, const unsigned char *p, uint32_t n) {
    if (n > rtx_room(r)) return;            /* the caller has checked; refuse rather
                                             * than corrupt the ring */
    uint32_t tail = r->head + r->len;
    if (tail >= r->cap) tail -= r->cap;     /* no %: cap need not be a power of two, and
                                             * tail is below 2 * cap */
    uint32_t first = r->cap - tail;
    if (first > n) first = n;
    memcpy(r->buf + tail, p, first);
    if (n > first) memcpy(r->buf, p + first, n - first);
    r->len += n;
}

uint32_t rtx_drop(struct rtx *r, uint32_t n) {
    if (n > r->len) n = r->len;
    r->head += n;
    if (r->head >= r->cap) r->head -= r->cap;
    r->len -= n;
    return n;
}

uint32_t rtx_peek(const struct rtx *r, uint32_t want, const unsigned char **p) {
    if (!r->len) { *p = NULL; return 0; }
    if (want > r->len) want = r->len;
    uint32_t contig = r->cap - r->head;
    if (want > contig) want = contig;
    *p = r->buf + r->head;
    return want;
}
