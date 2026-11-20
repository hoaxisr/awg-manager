#ifndef FIRC_SUBNET_H
#define FIRC_SUBNET_H

#include <stdint.h>

/* a single port fills one of xt_multiport's slots, a range two; written in iptables form, "53,1000:2000" */
#define FIRC_PORTS_MAX_SLOTS 15
#define FIRC_PORTS_STR_MAX 192

typedef struct firc_ipv4_subnet {
    uint8_t addr[4];
    uint8_t cidr;
    uint8_t proto;              /* 0 (any), IPPROTO_TCP or IPPROTO_UDP */
    char ports[FIRC_PORTS_STR_MAX]; /* "" (all) or the comma list above; meaningful only with proto */
} firc_ipv4_subnet_t;

typedef struct firc_ipv6_subnet {
    uint8_t addr[16];
    uint8_t cidr;
    uint8_t proto;
    char ports[FIRC_PORTS_STR_MAX];
} firc_ipv6_subnet_t;

#endif /* FIRC_SUBNET_H */
