#include "firc/ifaddr.h"

#include <arpa/inet.h>
#include <linux/if.h>
#include <stdbool.h>
#include <stdio.h>
#include <ifaddrs.h>
#include <netinet/in.h>
#include <string.h>
#include <sys/socket.h>

int firc_iface_ipv4(const char *name, char *out, size_t cap) {
    struct ifaddrs *list = NULL;
    if (name == NULL || cap == 0 || getifaddrs(&list) != 0) { return -1; }
    int rc = -1;
    for (const struct ifaddrs *a = list; a != NULL; a = a->ifa_next) {
        if (a->ifa_addr != NULL && a->ifa_addr->sa_family == AF_INET && strcmp(a->ifa_name, name) == 0) {
            if (inet_ntop(AF_INET, &((const struct sockaddr_in *)(const void *)a->ifa_addr)->sin_addr, out,
                          (socklen_t)cap) != NULL) {
                rc = 0;
            }
            break;
        }
    }
    freeifaddrs(list);
    return rc;
}

size_t firc_ifnet4_list(firc_ifnet4_t *out, size_t cap) {
    struct ifaddrs *list = NULL;
    if (out == NULL || cap == 0 || getifaddrs(&list) != 0) { return 0; }
    size_t n = 0;
    for (const struct ifaddrs *a = list; a != NULL && n < cap; a = a->ifa_next) {
        if (a->ifa_addr == NULL || a->ifa_netmask == NULL || a->ifa_addr->sa_family != AF_INET ||
            (a->ifa_flags & IFF_LOOPBACK) != 0 || strlen(a->ifa_name) >= sizeof(out[n].name)) {
            continue;
        }
        uint32_t addr = ntohl(((const struct sockaddr_in *)(const void *)a->ifa_addr)->sin_addr.s_addr);
        uint32_t mask = ntohl(((const struct sockaddr_in *)(const void *)a->ifa_netmask)->sin_addr.s_addr);
        snprintf(out[n].name, sizeof(out[n].name), "%s", a->ifa_name);
        out[n].net = addr & mask;
        out[n].mask = mask;
        n++;
    }
    freeifaddrs(list);
    return n;
}

const char *firc_ifnet4_find(const firc_ifnet4_t *nets, size_t n, const char *ip) {
    struct in_addr in;
    if (nets == NULL || ip == NULL || inet_pton(AF_INET, ip, &in) != 1) { return NULL; }
    uint32_t v = ntohl(in.s_addr);
    for (size_t i = 0; i < n; i++) {
        if ((v & nets[i].mask) == nets[i].net) { return nets[i].name; }
    }
    return NULL;
}

static bool in_link(const char *name, char *const *link, size_t n_link) {
    for (size_t i = 0; i < n_link; i++) {
        if (strcmp(name, link[i]) == 0) { return true; }
    }
    return false;
}

void firc_ifnet4_link_first(firc_ifnet4_t *nets, size_t n, char *const *link, size_t n_link) {
    size_t front = 0;
    for (size_t i = 0; i < n; i++) {
        if (!in_link(nets[i].name, link, n_link)) { continue; }
        firc_ifnet4_t hit = nets[i];
        memmove(&nets[front + 1], &nets[front], (i - front) * sizeof(*nets));
        nets[front++] = hit;
    }
}
