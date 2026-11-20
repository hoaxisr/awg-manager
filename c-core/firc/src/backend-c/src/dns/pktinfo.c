#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "pktinfo.h"

#include <string.h>

_Static_assert(sizeof(firc_pktinfo_cmsg_t) >= CMSG_SPACE(sizeof(struct in6_pktinfo)),
               "room for the larger of the two pktinfo messages");

bool firc_pktinfo_read(const struct cmsghdr *cm, firc_pktinfo_t *out) {
    if (cm->cmsg_level == IPPROTO_IP && cm->cmsg_type == IP_PKTINFO &&
        cm->cmsg_len >= CMSG_LEN(sizeof(struct in_pktinfo))) {
        struct in_pktinfo pi;
        memcpy(&pi, CMSG_DATA(cm), sizeof(pi));
        memset(out, 0, sizeof(*out));
        out->family = AF_INET;
        out->dst4 = pi.ipi_addr;
        out->ifindex = pi.ipi_ifindex;
        return true;
    }
    if (cm->cmsg_level == IPPROTO_IPV6 && cm->cmsg_type == IPV6_PKTINFO &&
        cm->cmsg_len >= CMSG_LEN(sizeof(struct in6_pktinfo))) {
        struct in6_pktinfo pi;
        memcpy(&pi, CMSG_DATA(cm), sizeof(pi));
        memset(out, 0, sizeof(*out));
        out->family = AF_INET6;
        out->dst6 = pi.ipi6_addr;
        out->ifindex = (int)pi.ipi6_ifindex;
        return true;
    }
    return false;
}

size_t firc_pktinfo_write(const firc_pktinfo_t *pi, firc_pktinfo_cmsg_t *c) {
    memset(c, 0, sizeof(*c));
    if (pi->family == AF_INET) {
        c->hdr.cmsg_level = IPPROTO_IP;
        c->hdr.cmsg_type = IP_PKTINFO;
        c->hdr.cmsg_len = CMSG_LEN(sizeof(struct in_pktinfo));
        struct in_pktinfo v;
        memset(&v, 0, sizeof(v));
        v.ipi_spec_dst = pi->dst4;
        v.ipi_ifindex = pi->ifindex;
        memcpy(CMSG_DATA(&c->hdr), &v, sizeof(v));
        return CMSG_SPACE(sizeof(struct in_pktinfo));
    }
    c->hdr.cmsg_level = IPPROTO_IPV6;
    c->hdr.cmsg_type = IPV6_PKTINFO;
    c->hdr.cmsg_len = CMSG_LEN(sizeof(struct in6_pktinfo));
    struct in6_pktinfo v;
    memset(&v, 0, sizeof(v));
    v.ipi6_addr = pi->dst6;
    v.ipi6_ifindex = (unsigned)pi->ifindex;
    memcpy(CMSG_DATA(&c->hdr), &v, sizeof(v));
    return CMSG_SPACE(sizeof(struct in6_pktinfo));
}
