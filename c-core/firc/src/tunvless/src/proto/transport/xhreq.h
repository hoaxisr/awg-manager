#ifndef TUNVLESS_XHREQ_H
#define TUNVLESS_XHREQ_H
#include <stddef.h>
#include "xhopts.h"

void xh_session_id(const struct xh_opts *o, char *out, size_t cap);
size_t xh_padding(const struct xh_opts *o, size_t len, char *out, size_t cap);
size_t xh_huff_len(const char *s, size_t n);

#endif
