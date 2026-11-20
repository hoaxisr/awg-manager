/* Configuring the TUN device without iproute2. Details in ifcfg.c. */
#ifndef TUNVLESS_IFCFG_H
#define TUNVLESS_IFCFG_H
#include <stdint.h>

/* "a.b.c.d" or "a.b.c.d/len" (no length — /32). 0, or -1 when the text is not an IPv4 prefix.
 * The address is in network order and keeps its host bits. */
int ifcfg_parse_cidr(const char *s, uint32_t *addr, int *prefix);

/* Address, link up and transmit queue length of dev: the equivalent of `ip addr replace`,
 * `ip link set up` and `ip link set txqueuelen 4096`. Every failed step is reported on stderr;
 * returns how many failed (0 — all done). */
int ifcfg_bring_up(const char *dev, uint32_t addr, int prefix);

/* Route dst/prefix into dev (replace if it exists) in routing table `table` (0 — main). 0, or a
 * negative errno. */
int ifcfg_route_add(const char *dev, uint32_t dst, int prefix, uint32_t table);

/* The route the kernel takes to dst right now: gateway (0 — on the link) and device index. 0; 1 —
 * dst is an address of this machine (no route needed); or a negative errno. */
int ifcfg_route_get(uint32_t dst, uint32_t *gw, unsigned *oif);

/* Host route dst/32 through gw (0 — on the link) and device oif, in table (0 — main): add — only
 * if the table has no route for dst/32 yet (-EEXIST otherwise) — or remove. 0, or a negative
 * errno. */
int ifcfg_route_via(uint32_t dst, uint32_t gw, unsigned oif, uint32_t table);
int ifcfg_route_unvia(uint32_t dst, uint32_t gw, unsigned oif, uint32_t table);

void ifcfg_addr6_of(uint32_t v4_be, unsigned char out[16]);

int ifcfg_addr6_set(int ifindex, const unsigned char addr[16], int on);

#endif
