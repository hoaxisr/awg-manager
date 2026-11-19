/* Configuring the TUN device: address, link up, queue length and routes.
 *
 * Routers do not always have iproute2 (Entware ships it as a separate package, and busybox `ip` is
 * not always built in), so the requests go to the kernel directly: rtnetlink for the address and
 * the routes, ioctl for the link flags and the queue length. Every request here exists since Linux
 * 2.6, well under Entware's 3.4 floor.
 *
 * The device needs an address although the stack does not use it (it reads packets and opens a
 * flow per connection): the kernel treats a route into a device without an address as unusable
 * for locally generated packets.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <fcntl.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <net/if.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <linux/netlink.h>
#include <linux/rtnetlink.h>

#include "ifcfg.h"

#define LOG_W "tunvless[warn] tunnel: "
#define LOG_I "tunvless[info] tunnel: "

/* Transmit queue of the device. The name is misleading for TUN: it is the queue of packets the
 * KERNEL hands to us. At the default 500, whatever of a burst exceeds 500 packets while the loop
 * is busy with crypto is dropped by the kernel (counted as tx_dropped of the device); 4096 holds
 * eight times as much and costs no memory up front — the queue holds what is in it. */
#define TUN_TXQLEN 4096

int ifcfg_parse_cidr(const char *s, uint32_t *addr, int *prefix) {
    char buf[32];
    const char *slash = strchr(s, '/');
    size_t n = slash ? (size_t)(slash - s) : strlen(s);
    if (n == 0 || n >= sizeof(buf)) return -1;
    memcpy(buf, s, n);
    buf[n] = '\0';
    struct in_addr a;
    if (inet_pton(AF_INET, buf, &a) != 1) return -1;
    int len = 32;
    if (slash) {
        char *end;
        long v = strtol(slash + 1, &end, 10);
        if (end == slash + 1 || *end || v < 0 || v > 32) return -1;
        len = (int)v;
    }
    *addr = a.s_addr;
    *prefix = len;
    return 0;
}

/* ---- rtnetlink: one request, one acknowledgement ---------------------------------------- */

struct nlreq {
    struct nlmsghdr h;
    union {
        struct ifaddrmsg ifa;
        struct rtmsg rt;
    } u;
    char attrs[64];
};

/* Takes the whole request, not its header: an attribute written past &q->h is out of bounds of the
 * object GCC sees (-Wstringop-overflow). */
static void nl_attr(struct nlreq *q, unsigned short type, const void *d, size_t n) {
    struct nlmsghdr *h = &q->h;
    struct rtattr *a = (struct rtattr *)((char *)q + NLMSG_ALIGN(h->nlmsg_len));
    a->rta_type = type;
    a->rta_len = (unsigned short)RTA_LENGTH(n);
    memcpy(RTA_DATA(a), d, n);
    h->nlmsg_len = NLMSG_ALIGN(h->nlmsg_len) + RTA_ALIGN(a->rta_len);
}

/* Send the request. With resp == NULL (a change) wait for its acknowledgement; otherwise (a
 * query) copy the answer into resp. 0 or a negative errno. */
static int nl_talk(struct nlmsghdr *h, struct nlmsghdr *resp, size_t resp_cap) {
    int fd = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_ROUTE);
    if (fd < 0) return -errno;
    struct sockaddr_nl sa = { .nl_family = AF_NETLINK };
    h->nlmsg_flags |= NLM_F_REQUEST | (resp ? 0 : NLM_F_ACK);
    h->nlmsg_seq = 1;
    int rc;
    if (sendto(fd, h, h->nlmsg_len, 0, (struct sockaddr *)&sa, sizeof(sa)) < 0) {
        rc = -errno;
        close(fd);
        return rc;
    }
    union { struct nlmsghdr h; char b[4096]; } r;
    for (;;) {
        ssize_t n = recv(fd, &r, sizeof(r), 0);
        if (n < 0) {
            if (errno == EINTR) continue;
            rc = -errno;
            break;
        }
        if (n == 0) { rc = -EIO; break; }
        int len = (int)n;
        for (struct nlmsghdr *m = &r.h; NLMSG_OK(m, len); m = NLMSG_NEXT(m, len)) {
            if (m->nlmsg_type == NLMSG_ERROR) {
                const struct nlmsgerr *e = NLMSG_DATA(m);
                close(fd);
                return e->error ? e->error : (resp ? -ENOENT : 0);
            }
            if (resp && m->nlmsg_type != NLMSG_DONE && m->nlmsg_type != NLMSG_NOOP) {
                if (m->nlmsg_len > resp_cap) { close(fd); return -EMSGSIZE; }
                memcpy(resp, m, m->nlmsg_len);
                close(fd);
                return 0;
            }
        }
    }
    close(fd);
    return rc;
}

static int addr_replace(int ifindex, uint32_t addr, int prefix) {
    struct nlreq q;
    memset(&q, 0, sizeof(q));
    q.h.nlmsg_len = NLMSG_LENGTH(sizeof(q.u.ifa));
    q.h.nlmsg_type = RTM_NEWADDR;
    q.h.nlmsg_flags = NLM_F_CREATE | NLM_F_REPLACE;
    q.u.ifa.ifa_family = AF_INET;
    q.u.ifa.ifa_prefixlen = (unsigned char)prefix;
    q.u.ifa.ifa_scope = RT_SCOPE_UNIVERSE;
    q.u.ifa.ifa_index = (unsigned)ifindex;
    nl_attr(&q, IFA_LOCAL, &addr, sizeof(addr));
    nl_attr(&q, IFA_ADDRESS, &addr, sizeof(addr));
    return nl_talk(&q.h, NULL, 0);
}

enum { RT_ADD, RT_ADD_EXCL, RT_DEL };

/* dst/prefix through gw (0 — directly on the link) and device oif, in table: add (replacing one
 * that exists), add only if there is none (-EEXIST otherwise), or remove. */
static int route_req(int op, uint32_t dst, int prefix, uint32_t gw, unsigned oif, uint32_t table) {
    struct nlreq q;
    memset(&q, 0, sizeof(q));
    q.h.nlmsg_len = NLMSG_LENGTH(sizeof(q.u.rt));
    q.h.nlmsg_type = op == RT_DEL ? RTM_DELROUTE : RTM_NEWROUTE;
    q.h.nlmsg_flags = op == RT_DEL ? 0 : op == RT_ADD_EXCL ? NLM_F_CREATE | NLM_F_EXCL
                                                          : NLM_F_CREATE | NLM_F_REPLACE;
    q.u.rt.rtm_family = AF_INET;
    q.u.rt.rtm_dst_len = (unsigned char)prefix;
    q.u.rt.rtm_protocol = RTPROT_BOOT;
    q.u.rt.rtm_scope = gw ? RT_SCOPE_UNIVERSE : RT_SCOPE_LINK;
    q.u.rt.rtm_type = RTN_UNICAST;
    if (table == 0) table = RT_TABLE_MAIN;
    q.u.rt.rtm_table = table < 256 ? (unsigned char)table : RT_TABLE_UNSPEC;
    if (table >= 256) nl_attr(&q, RTA_TABLE, &table, sizeof(table));
    if (prefix > 0) {
        uint32_t net = prefix == 32 ? dst : dst & htonl(~0u << (32 - prefix));
        nl_attr(&q, RTA_DST, &net, sizeof(net));
    }
    if (gw) nl_attr(&q, RTA_GATEWAY, &gw, sizeof(gw));
    uint32_t o = oif;
    nl_attr(&q, RTA_OIF, &o, sizeof(o));
    return nl_talk(&q.h, NULL, 0);
}

int ifcfg_route_add(const char *dev, uint32_t dst, int prefix, uint32_t table) {
    unsigned ifindex = if_nametoindex(dev);
    if (!ifindex) return -errno;
    return route_req(RT_ADD, dst, prefix, 0, ifindex, table);
}

int ifcfg_route_get(uint32_t dst, uint32_t *gw, unsigned *oif) {
    struct nlreq q;
    memset(&q, 0, sizeof(q));
    q.h.nlmsg_len = NLMSG_LENGTH(sizeof(q.u.rt));
    q.h.nlmsg_type = RTM_GETROUTE;
    q.u.rt.rtm_family = AF_INET;
    q.u.rt.rtm_dst_len = 32;
    nl_attr(&q, RTA_DST, &dst, sizeof(dst));
    union { struct nlmsghdr h; char b[1024]; } r;
    int rc = nl_talk(&q.h, &r.h, sizeof(r));
    if (rc != 0) return rc;
    if (r.h.nlmsg_type != RTM_NEWROUTE) return -ENOENT;
    struct rtmsg *rt = NLMSG_DATA(&r.h);
    /* An address of this machine: the local table answers before any route of ours could. */
    if (rt->rtm_type == RTN_LOCAL) return 1;
    int len = (int)RTM_PAYLOAD(&r.h);
    *gw = 0;
    *oif = 0;
    for (struct rtattr *a = RTM_RTA(rt); RTA_OK(a, len); a = RTA_NEXT(a, len)) {
        if (a->rta_type == RTA_GATEWAY && RTA_PAYLOAD(a) == 4) memcpy(gw, RTA_DATA(a), 4);
        if (a->rta_type == RTA_OIF && RTA_PAYLOAD(a) == 4) memcpy(oif, RTA_DATA(a), 4);
    }
    return *oif ? 0 : -ENETUNREACH;
}

int ifcfg_route_via(uint32_t dst, uint32_t gw, unsigned oif, uint32_t table) {
    return route_req(RT_ADD_EXCL, dst, 32, gw, oif, table);
}

int ifcfg_route_unvia(uint32_t dst, uint32_t gw, unsigned oif, uint32_t table) {
    return route_req(RT_DEL, dst, 32, gw, oif, table);
}

/* ---- the device ----------------------------------------------------------------------- */

int ifcfg_bring_up(const char *dev, uint32_t addr, int prefix) {
    int failed = 0;
    char astr[INET_ADDRSTRLEN];
    inet_ntop(AF_INET, &addr, astr, sizeof(astr));

    unsigned ifindex = if_nametoindex(dev);
    int rc = ifindex ? addr_replace((int)ifindex, addr, prefix) : -errno;
    if (rc != 0) {
        fprintf(stderr, LOG_W "%s: address %s/%d not set (%s) — the kernel will not route "
                        "locally generated packets into this device\n",
                dev, astr, prefix, strerror(-rc));
        failed++;
    }

    int s = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    struct ifreq ifr;
    memset(&ifr, 0, sizeof(ifr));
    snprintf(ifr.ifr_name, sizeof(ifr.ifr_name), "%s", dev);
    if (s < 0 || ioctl(s, SIOCGIFFLAGS, &ifr) != 0 ||
        (ifr.ifr_flags |= IFF_UP, ioctl(s, SIOCSIFFLAGS, &ifr) != 0)) {
        fprintf(stderr, LOG_W "%s: link not brought up (%s) — no traffic will pass\n",
                dev, strerror(errno));
        failed++;
    }
    /* Informational: traffic passes with the kernel's queue, only bursts lose more. */
    memset(&ifr, 0, sizeof(ifr));
    snprintf(ifr.ifr_name, sizeof(ifr.ifr_name), "%s", dev);
    ifr.ifr_qlen = TUN_TXQLEN;
    if (s < 0 || ioctl(s, SIOCSIFTXQLEN, &ifr) != 0)
        fprintf(stderr, LOG_I "%s: queue length left at the kernel default (asked %d: %s)\n",
                dev, TUN_TXQLEN, strerror(errno));
    if (s >= 0) close(s);
    return failed;
}

void ifcfg_addr6_of(uint32_t v4_be, unsigned char out[16]) {
    static const unsigned char doc[4] = { 0x20, 0x01, 0x0d, 0xb8 };
    memset(out, 0, 16);
    memcpy(out, doc, 4);
    memcpy(out + 12, &v4_be, 4);
}

static int ipv6_enable(const char *dev) {
    char path[96], c = 0;
    snprintf(path, sizeof path, "/proc/sys/net/ipv6/conf/%s/disable_ipv6", dev);
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) return -errno;
    ssize_t n = read(fd, &c, 1);
    close(fd);
    if (n != 1 || c != '1') return 0;
    fd = open(path, O_WRONLY | O_CLOEXEC);
    if (fd < 0) return -errno;
    int rc = write(fd, "0\n", 2) == 2 ? 0 : -errno;
    close(fd);
    return rc;
}

#ifndef IFA_F_NOPREFIXROUTE
#define IFA_F_NOPREFIXROUTE 0x200
#endif
/* IFA_FLAGS, missing from the SDK's 3.x headers; the routers run 4.9. */
enum { ATTR_IFA_FLAGS = 8 };

int ifcfg_addr6_set(int ifindex, const unsigned char addr[16], int on) {
    if (on) {
        char dev[IF_NAMESIZE];
        if (!if_indextoname((unsigned)ifindex, dev)) return -errno;
        int rc = ipv6_enable(dev);
        if (rc) return rc;
    }
    struct nlreq q;
    memset(&q, 0, sizeof(q));
    q.h.nlmsg_len = NLMSG_LENGTH(sizeof(q.u.ifa));
    q.h.nlmsg_type = on ? RTM_NEWADDR : RTM_DELADDR;
    q.h.nlmsg_flags = on ? NLM_F_CREATE | NLM_F_REPLACE : 0;
    q.u.ifa.ifa_family = AF_INET6;
    q.u.ifa.ifa_prefixlen = 128;
    q.u.ifa.ifa_flags = on ? IFA_F_NODAD : 0;
    q.u.ifa.ifa_scope = RT_SCOPE_UNIVERSE;
    q.u.ifa.ifa_index = (unsigned)ifindex;
    nl_attr(&q, IFA_LOCAL, addr, 16);
    nl_attr(&q, IFA_ADDRESS, addr, 16);
    if (on) {
        /* ndm logs a backtrace for every main-table route on a device it did not create; the address needs none. */
        uint32_t flags = IFA_F_NODAD | IFA_F_NOPREFIXROUTE;
        nl_attr(&q, ATTR_IFA_FLAGS, &flags, sizeof(flags));
    }
    int rc = nl_talk(&q.h, NULL, 0);
    if (!on && (rc == -ENOENT || rc == -EADDRNOTAVAIL)) return 0;
    return rc;
}
