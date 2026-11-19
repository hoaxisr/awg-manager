#include "firc/nflog.h"

#include <linux/netfilter/nfnetlink.h>
#include <linux/netfilter/nfnetlink_log.h>
#include <linux/netlink.h>
#include <string.h>

#include "firc/nlattr_iter.h"

#define NFGEN_LEN sizeof(struct nfgenmsg)

#define ATTR_HDR ((size_t)4) /* NLA_HDRLEN, without its negative ~3 mask */

size_t firc_nflog_walk(const uint8_t *buf, size_t len,
                       void (*cb)(const uint8_t *pkt, size_t len, void *ud), void *ud) {
    if (buf == NULL || cb == NULL) { return 1; }
    size_t left = len;
    size_t at = 0;
    while (left >= NLMSG_HDRLEN) {
        const struct nlmsghdr *h = (const struct nlmsghdr *)(buf + at);
        size_t mlen = h->nlmsg_len;
        /* A message reaching past what arrived belongs to the datagram before it in the buffer. */
        if (mlen < NLMSG_HDRLEN || mlen > left) { return 1; }
        const uint8_t *pkt = NULL;
        size_t pkt_len = 0;
        if (firc_nflog_payload(h, mlen, &pkt, &pkt_len)) { cb(pkt, pkt_len, ud); }
        /* The kernel pads each message to four bytes; a tail too short for that is the end. */
        size_t step = (mlen + 3u) & ~(size_t)3u;
        if (step >= left) { return step > left ? 1 : 0; }
        at += step;
        left -= step;
    }
    return left > 0 ? 1 : 0;
}

bool firc_nflog_payload(const void *buf, size_t len, const uint8_t **out, size_t *out_len) {
    if (!buf || !out || !out_len) { return false; }
    *out = NULL;
    *out_len = 0;

    if (len < NLMSG_HDRLEN) { return false; }

    const struct nlmsghdr *h = buf;
    if ((size_t)h->nlmsg_len > len) { return false; }

    if ((h->nlmsg_type >> 8) != NFNL_SUBSYS_ULOG) { return false; }
    if ((h->nlmsg_type & 0xff) != NFULNL_MSG_PACKET) { return false; }

    firc_nlattr_iter_t it;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, NFGEN_LEN)) { return false; }

    const struct nlattr *a = NULL;
    while (firc_nlattr_iter_next(&it, &a)) {
        /* Compared whole: nfnetlink_log sets no nested/byte-order flag on a payload attribute. */
        if (a->nla_type != NFULA_PAYLOAD) { continue; }
        size_t n = (size_t)a->nla_len - ATTR_HDR;
        if (n == 0) { return false; }
        *out = (const uint8_t *)a + ATTR_HDR;
        *out_len = n;
        return true;
    }
    return false;
}
