#include "firc/tunaddr.h"

#include <ifaddrs.h>
#include <netinet/in.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

static const uint8_t R[3] = {203, 0, 113};

static const firc_tun_ifaddr_t *g_test;
static size_t g_test_n;
static bool g_fail;

void firc_tun_addr(int n, char v4[20], uint8_t v6[16])
{
    uint8_t last = (uint8_t)(1 + n);
    snprintf(v4, 20, "%u.%u.%u.%u/32", R[0], R[1], R[2], last);
    static const uint8_t head[12] = {0x20, 0x01, 0x0d, 0xb8};
    memcpy(v6, head, sizeof head);
    memcpy(v6 + 12, R, sizeof R);
    v6[15] = last;
}

static bool held(const char *name, int family, const uint8_t *addr, const char *own, const uint8_t *v4,
                 const uint8_t *v6)
{
    if (strcmp(name, own) == 0) {
        return false;
    }
    return (family == AF_INET && memcmp(addr, v4, 4) == 0) || (family == AF_INET6 && memcmp(addr, v6, 16) == 0);
}

int firc_tun_addr_holder(int n, const char *own, char out[16])
{
    char text[20];
    uint8_t v6[16];
    firc_tun_addr(n, text, v6);
    const uint8_t *v4 = v6 + 12;
    if (g_fail) {
        return -1;
    }
    if (g_test != NULL) {
        for (size_t i = 0; i < g_test_n; i++) {
            if (held(g_test[i].name, g_test[i].family, g_test[i].addr, own, v4, v6)) {
                snprintf(out, 16, "%s", g_test[i].name);
                return g_test[i].family;
            }
        }
        return 0;
    }
    struct ifaddrs *list = NULL;
    if (getifaddrs(&list) != 0) {
        return -1;
    }
    int found = 0;
    for (const struct ifaddrs *a = list; a != NULL && found == 0; a = a->ifa_next) {
        if (a->ifa_addr == NULL) {
            continue;
        }
        const uint8_t *addr = NULL;
        if (a->ifa_addr->sa_family == AF_INET) {
            addr = (const uint8_t *)&((const struct sockaddr_in *)(const void *)a->ifa_addr)->sin_addr;
        } else if (a->ifa_addr->sa_family == AF_INET6) {
            addr = ((const struct sockaddr_in6 *)(const void *)a->ifa_addr)->sin6_addr.s6_addr;
        }
        if (addr != NULL && held(a->ifa_name, a->ifa_addr->sa_family, addr, own, v4, v6)) {
            snprintf(out, 16, "%s", a->ifa_name);
            found = a->ifa_addr->sa_family;
        }
    }
    freeifaddrs(list);
    return found;
}

void firc_tun_addr_set_ifaddrs_for_test(const firc_tun_ifaddr_t *v, size_t n)
{
    g_test = v;
    g_test_n = n;
    g_fail = false;
}

void firc_tun_addr_fail_ifaddrs_for_test(bool fail)
{
    g_fail = fail;
}
