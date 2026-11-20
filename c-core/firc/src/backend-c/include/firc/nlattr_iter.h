/* Plain functions, not mnl_attr_for_each*: GCC 8.4 broke the pragma-wrapped macro loops. */
#ifndef FIRC_NLATTR_ITER_H
#define FIRC_NLATTR_ITER_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <linux/netlink.h>

typedef struct firc_nlattr_iter {
    const uint8_t *cursor;
    size_t remaining;
} firc_nlattr_iter_t;

/* Iterates attributes after an nlmsg payload header of extra_header_len bytes. */
bool firc_nlattr_iter_init_nlmsg(firc_nlattr_iter_t *it, const struct nlmsghdr *h,
                               size_t extra_header_len);

bool firc_nlattr_iter_init_nested(firc_nlattr_iter_t *it, const struct nlattr *nest);

/* Next structurally valid attribute; false at the end or on malformed input. *out is borrowed. */
bool firc_nlattr_iter_next(firc_nlattr_iter_t *it, const struct nlattr **out);

#endif /* FIRC_NLATTR_ITER_H */
