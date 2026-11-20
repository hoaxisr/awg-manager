#ifndef FIRC_TUNADDR_H
#define FIRC_TUNADDR_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef struct {
    char name[16];
    int family;
    uint8_t addr[16];
} firc_tun_ifaddr_t;

/* Tunnel n's device address: v4 "203.0.113.<1+n>/32", v6 2001:db8:: followed by the four IPv4 bytes. */
void firc_tun_addr(int n, char v4[20], uint8_t v6[16]);

/* AF_INET or AF_INET6 when an interface other than own holds tunnel n's address of that family, out
   getting its name; 0 for none; -1 when the interface addresses cannot be read. */
int firc_tun_addr_holder(int n, const char *own, char out[16]);

/* The holder check reads v instead of the system's addresses; NULL reads the system's again. */
void firc_tun_addr_set_ifaddrs_for_test(const firc_tun_ifaddr_t *v, size_t n);

/* Makes the holder check fail its read until the next set_ifaddrs_for_test. */
void firc_tun_addr_fail_ifaddrs_for_test(bool fail);

#endif
