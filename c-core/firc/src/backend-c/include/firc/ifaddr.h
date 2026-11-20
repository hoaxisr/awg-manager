#ifndef FIRC_IFADDR_H
#define FIRC_IFADDR_H

#include <stddef.h>
#include <stdint.h>

/* Writes the first IPv4 address of interface name as text; 0, or -1 when it has none. */
int firc_iface_ipv4(const char *name, char *out, size_t cap);

typedef struct {
    char name[16];
    uint32_t net, mask;
} firc_ifnet4_t;

/* Every IPv4 net of a non-loopback interface, host order, up to cap; the count. */
size_t firc_ifnet4_list(firc_ifnet4_t *out, size_t cap);

/* Name of the first net holding dotted IPv4 ip; NULL for none, an empty, IPv6 or malformed ip. */
const char *firc_ifnet4_find(const firc_ifnet4_t *nets, size_t n, const char *ip);

/* Stable reorder putting the nets of link's interfaces first. */
void firc_ifnet4_link_first(firc_ifnet4_t *nets, size_t n, char *const *link, size_t n_link);

#endif
