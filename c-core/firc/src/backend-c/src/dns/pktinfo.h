#ifndef FIRC_DNS_PKTINFO_H
#define FIRC_DNS_PKTINFO_H

#include <netinet/in.h>
#include <stdbool.h>
#include <stddef.h>
#include <sys/socket.h>

typedef struct {
    int family;
    struct in_addr dst4;
    struct in6_addr dst6;
    int ifindex;
} firc_pktinfo_t;

typedef union {
    struct cmsghdr hdr;
    unsigned char b[CMSG_SPACE(sizeof(struct in6_addr) + sizeof(unsigned int))];
} firc_pktinfo_cmsg_t;

bool firc_pktinfo_read(const struct cmsghdr *cm, firc_pktinfo_t *out);

size_t firc_pktinfo_write(const firc_pktinfo_t *pi, firc_pktinfo_cmsg_t *c);

#endif
