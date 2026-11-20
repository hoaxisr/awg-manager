#include "firc/nlattr_iter.h"

#define FIRC_NL_ALIGNTO 4u

static size_t align_len(size_t len) {
    return (len + FIRC_NL_ALIGNTO - 1u) & ~(FIRC_NL_ALIGNTO - 1u);
}

static void iter_init(firc_nlattr_iter_t *it, const void *payload, size_t len) {
    it->cursor = payload;
    it->remaining = len;
}

bool firc_nlattr_iter_init_nlmsg(firc_nlattr_iter_t *it, const struct nlmsghdr *h,
                               size_t extra_header_len) {
    if (!it || !h) { return false; }

    size_t offset = align_len(sizeof(struct nlmsghdr)) + align_len(extra_header_len);
    if ((size_t)h->nlmsg_len < offset) {
        iter_init(it, NULL, 0);
        return false;
    }

    const uint8_t *payload = (const uint8_t *)h + offset;
    iter_init(it, payload, (size_t)h->nlmsg_len - offset);
    return true;
}

bool firc_nlattr_iter_init_nested(firc_nlattr_iter_t *it, const struct nlattr *nest) {
    size_t header_len = align_len(sizeof(struct nlattr));
    if (!it || !nest || (size_t)nest->nla_len < header_len) {
        if (it) { iter_init(it, NULL, 0); }
        return false;
    }

    iter_init(it, (const uint8_t *)nest + header_len, (size_t)nest->nla_len - header_len);
    return true;
}

bool firc_nlattr_iter_next(firc_nlattr_iter_t *it, const struct nlattr **out) {
    if (!it || !out) { return false; }
    *out = NULL;

    size_t header_len = align_len(sizeof(struct nlattr));
    if (!it->cursor || it->remaining < header_len) {
        it->remaining = 0;
        return false;
    }

    const struct nlattr *attr = (const struct nlattr *)it->cursor;
    size_t attr_len = attr->nla_len;
    if (attr_len < header_len || attr_len > it->remaining) {
        it->remaining = 0;
        return false;
    }

    *out = attr;

    size_t step = align_len(attr_len);
    if (step >= it->remaining) {
        it->cursor += it->remaining;
        it->remaining = 0;
    } else {
        it->cursor += step;
        it->remaining -= step;
    }
    return true;
}
