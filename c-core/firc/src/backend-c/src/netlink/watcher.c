#include "firc/netlink_watcher.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/nlattr_iter.h"
#include "firc/rtnl.h"

#include <errno.h>
#include <fcntl.h>
#include <libmnl/libmnl.h>
#include <linux/if.h>
#include <linux/rtnetlink.h>
#include <net/if.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>

#define FIRC_NL_WATCHER_RECVBUF 8192
#define FIRC_NL_WATCHER_REOPEN_FIRST_MS 100u
#define FIRC_NL_WATCHER_REOPEN_CAP_MS 5000u

#define FIRC_NL_WATCHER_ERROR_AFTER_MS (5u * 60u * 1000u)

struct firc_nl_watcher {
    struct mnl_socket *nl;
    firc_loop_t *loop;
    firc_nl_link_cb link_cb;
    void *link_ud;
    firc_nl_addr_cb addr_cb;
    void *addr_ud;
    firc_nl_lost_cb lost_cb;
    void *lost_ud;
    int reopen_timer;
    unsigned failures;
    uint32_t ticks_left;
    firc_logrep_t down;
    firc_lograte_t lost;
};

uint32_t firc_nl_watcher_reopen_delay_ms(unsigned failures) {
    uint32_t ms = FIRC_NL_WATCHER_REOPEN_FIRST_MS;
    for (unsigned i = 1; i < failures && ms < FIRC_NL_WATCHER_REOPEN_CAP_MS; i++) { ms *= 2; }
    return ms < FIRC_NL_WATCHER_REOPEN_CAP_MS ? ms : FIRC_NL_WATCHER_REOPEN_CAP_MS;
}

static void close_socket(firc_nl_watcher_t *w) {
    if (w->nl == NULL) { return; }
    firc_loop_del_fd(w->loop, mnl_socket_get_fd(w->nl));
    mnl_socket_close(w->nl);
    w->nl = NULL;
}

static void socket_failed(firc_nl_watcher_t *w);

bool firc_nl_watcher_link_event(const struct nlmsghdr *h, char *name, size_t cap, bool *up) {
    if (h->nlmsg_type != RTM_NEWLINK && h->nlmsg_type != RTM_DELLINK) { return false; }
    if (mnl_nlmsg_get_payload_len(h) < sizeof(struct ifinfomsg)) { return false; }
    const struct ifinfomsg *ifi = mnl_nlmsg_get_payload(h);
    *up = h->nlmsg_type == RTM_NEWLINK && (ifi->ifi_flags & IFF_UP) != 0;
    name[0] = '\0';
    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct ifinfomsg))) { return false; }
    while (firc_nlattr_iter_next(&it, &a)) {
        if (mnl_attr_get_type(a) == IFLA_IFNAME) { snprintf(name, cap, "%s", mnl_attr_get_str(a)); }
    }
    return name[0] != '\0';
}

static void handle_link_msg(firc_nl_watcher_t *w, const struct nlmsghdr *h) {
    if (!w->link_cb) { return; }
    char name[IFNAMSIZ];
    bool up = false;
    if (firc_nl_watcher_link_event(h, name, sizeof(name), &up)) { w->link_cb(name, up, w->link_ud); }
}

int firc_nl_watcher_addr_ifindex(const struct nlmsghdr *h) {
    if (h->nlmsg_type != RTM_NEWADDR && h->nlmsg_type != RTM_DELADDR) { return 0; }
    if (mnl_nlmsg_get_payload_len(h) < sizeof(struct ifaddrmsg)) { return 0; }
    const struct ifaddrmsg *ifa = mnl_nlmsg_get_payload(h);
    return (int)ifa->ifa_index;
}

static void handle_addr_msg(firc_nl_watcher_t *w, const struct nlmsghdr *h) {
    int ifindex = firc_nl_watcher_addr_ifindex(h);
    if (ifindex <= 0 || !w->addr_cb) { return; }
    char name[IFNAMSIZ] = {0};
    if (!if_indextoname((unsigned)ifindex, name)) { return; }
    w->addr_cb(name, w->addr_ud);
}

firc_nl_recv_verdict_t firc_nl_watcher_recv_verdict(int err) {
    switch (err) {
    case EAGAIN:
#if EWOULDBLOCK != EAGAIN
    case EWOULDBLOCK:
#endif
        return FIRC_NL_RECV_DRAINED;
    case EINTR:
        return FIRC_NL_RECV_RETRY;
    case ENOBUFS:
    case ENOSPC:
        /* Messages were dropped but the socket is fine: read on. */
        return FIRC_NL_RECV_LOST;
    default:
        return FIRC_NL_RECV_FATAL;
    }
}

/* False only when a device was dropped for want of room. */
static bool add_oif(int *out, size_t max, size_t *n, int oif) {
    if (oif <= 0) { return true; }
    for (size_t i = 0; i < *n; i++) {
        if (out[i] == oif) { return true; }
    }
    if (*n == max) { return false; }
    out[(*n)++] = oif;
    return true;
}

size_t firc_nl_watcher_default_route_oifs(const struct nlmsghdr *h, int *out, size_t max) {
    size_t n = 0;
    bool dropped = false;
    if (out == NULL || max == 0) { return 0; }
    if (h->nlmsg_type != RTM_NEWROUTE && h->nlmsg_type != RTM_DELROUTE) { return 0; }
    if (mnl_nlmsg_get_payload_len(h) < sizeof(struct rtmsg)) { return 0; }
    const struct rtmsg *rtm = mnl_nlmsg_get_payload(h);
    if (rtm->rtm_dst_len != 0 || rtm->rtm_type != RTN_UNICAST) { return 0; }
    if (rtm->rtm_protocol == FIRC_RTPROT) { return 0; }

    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct rtmsg))) { return 0; }
    while (firc_nlattr_iter_next(&it, &a)) {
        uint16_t t = mnl_attr_get_type(a);
        if (t == RTA_OIF && mnl_attr_get_payload_len(a) == 4) {
            dropped = !add_oif(out, max, &n, (int)mnl_attr_get_u32(a)) || dropped;
        } else if (t == RTA_MULTIPATH) {
            /* ECMP: one device per leg, none at the top level; name every leg. */
            const void *p = mnl_attr_get_payload(a);
            size_t left = mnl_attr_get_payload_len(a);
            const struct rtnexthop *nh = p;
            while (left >= sizeof(*nh) && nh->rtnh_len >= sizeof(*nh) && (size_t)nh->rtnh_len <= left) {
                dropped = !add_oif(out, max, &n, nh->rtnh_ifindex) || dropped;
                size_t step = (size_t)RTNH_ALIGN(nh->rtnh_len);
                if (step == 0 || step > left) { break; }
                left -= step;
                nh = (const struct rtnexthop *)(const void *)((const char *)nh + step);
            }
        }
    }
    if (dropped) {
        FIRC_WARN("a default route names more than %d devices; the rest are not looked at", (int)max);
    }
    return n;
}

static void handle_route_msg(firc_nl_watcher_t *w, const struct nlmsghdr *h) {
    if (!w->addr_cb) { return; }
    int oifs[FIRC_NL_WATCHER_MAX_NEXTHOPS];
    size_t n = firc_nl_watcher_default_route_oifs(h, oifs, sizeof(oifs) / sizeof(oifs[0]));
    for (size_t i = 0; i < n; i++) {
        char name[IFNAMSIZ] = {0};
        if (!if_indextoname((unsigned)oifs[i], name)) { continue; }
        w->addr_cb(name, w->addr_ud);
    }
}

bool firc_nl_watcher_handle_recv_error(firc_nl_watcher_t *w, int err) {
    switch (firc_nl_watcher_recv_verdict(err)) {
    case FIRC_NL_RECV_RETRY:
        return true;
    case FIRC_NL_RECV_LOST:
        firc_lograte_say(&w->lost, 60000, FIRC_LOG_WARN, NULL, "netlink watcher lost messages (%s); every interface is re-read",
                         err == ENOBUFS ? "its buffer overran" : "a message did not fit the read buffer");
        if (w->lost_cb) { w->lost_cb(w->lost_ud); }
        return true;
    case FIRC_NL_RECV_FATAL:
        firc_logrep_fail_for(&w->down, FIRC_NL_WATCHER_ERROR_AFTER_MS, NULL,
                             "netlink watcher recv error: %s; its socket is reopened", strerror(err));
        socket_failed(w);
        return false;
    case FIRC_NL_RECV_DRAINED:
    default:
        return false;
    }
}

static void on_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud) {
    (void)loop;
    (void)fd;
    (void)events;
    firc_nl_watcher_t *w = ud;
    _Alignas(struct nlmsghdr) uint8_t buf[FIRC_NL_WATCHER_RECVBUF];

    for (;;) {
        ssize_t ret = mnl_socket_recvfrom(w->nl, buf, sizeof(buf));
        if (ret < 0) {
            if (!firc_nl_watcher_handle_recv_error(w, errno)) { return; }
            continue;
        }
        if (ret == 0) { break; }
        w->failures = 0;

        int len = (int)ret;
        struct nlmsghdr *nh = (struct nlmsghdr *)buf;
        while (mnl_nlmsg_ok(nh, len)) {
            switch (nh->nlmsg_type) {
            case RTM_DELLINK:
            case RTM_NEWLINK:
                handle_link_msg(w, nh);
                break;
            case RTM_NEWADDR:
            case RTM_DELADDR:
                handle_addr_msg(w, nh);
                break;
            case RTM_NEWROUTE:
            case RTM_DELROUTE:
                handle_route_msg(w, nh);
                break;
            default:
                break;
            }
            nh = mnl_nlmsg_next(nh, &len);
        }
    }
}

static firc_err_t open_socket(firc_nl_watcher_t *w) {
    w->nl = mnl_socket_open(NETLINK_ROUTE);
    if (!w->nl) { return firc_err_from_errno(errno); }

    /* Room for route-multicast bursts; SO_RCVBUFFORCE passes rmem_max with CAP_NET_ADMIN. */
    int rcvbuf = 512 * 1024;
    int wfd = mnl_socket_get_fd(w->nl);
    bool got = false;
#ifdef SO_RCVBUFFORCE
    got = setsockopt(wfd, SOL_SOCKET, SO_RCVBUFFORCE, &rcvbuf, sizeof(rcvbuf)) == 0;
#endif
    if (!got && setsockopt(wfd, SOL_SOCKET, SO_RCVBUF, &rcvbuf, sizeof(rcvbuf)) != 0) {
        FIRC_DEBUG("netlink watcher keeps the default receive buffer: %s", strerror(errno));
    }

    unsigned int groups = RTMGRP_LINK | RTMGRP_IPV4_IFADDR | RTMGRP_IPV6_IFADDR | RTMGRP_IPV4_ROUTE |
                          RTMGRP_IPV6_ROUTE;
    firc_err_t err = FIRC_OK;
    int flags = 0;
    if (mnl_socket_bind(w->nl, groups, MNL_SOCKET_AUTOPID) < 0) {
        err = firc_err_from_errno(errno);
    } else if ((flags = fcntl(wfd, F_GETFL, 0)) < 0 || fcntl(wfd, F_SETFL, flags | O_NONBLOCK) < 0) {
        err = firc_err_from_errno(errno);
    } else {
        err = firc_loop_add_fd(w->loop, wfd, EPOLLIN, on_readable, w);
    }
    if (err != FIRC_OK) {
        mnl_socket_close(w->nl);
        w->nl = NULL;
    }
    return err;
}

static void wait_before_reopen(firc_nl_watcher_t *w) {
    w->failures++;
    w->ticks_left = firc_nl_watcher_reopen_delay_ms(w->failures) / FIRC_NL_WATCHER_REOPEN_FIRST_MS;
}

static void on_reopen_tick(firc_loop_t *loop, void *ud) {
    firc_nl_watcher_t *w = ud;
    if (--w->ticks_left > 0) { return; }
    firc_err_t err = open_socket(w);
    if (err != FIRC_OK) {
        firc_logrep_fail_for(&w->down, FIRC_NL_WATCHER_ERROR_AFTER_MS, NULL,
                             "netlink watcher socket not reopened yet: %s", firc_err_str(err));
        wait_before_reopen(w);
        return;
    }
    firc_loop_del_timer(loop, w->reopen_timer);
    w->reopen_timer = 0;
    (void)firc_logrep_ok(&w->down, NULL, "netlink watcher socket");
    if (w->lost_cb) { w->lost_cb(w->lost_ud); }
}

static void socket_failed(firc_nl_watcher_t *w) {
    close_socket(w);
    if (w->reopen_timer != 0) { return; }
    wait_before_reopen(w);
    if (firc_loop_add_timer(w->loop, FIRC_NL_WATCHER_REOPEN_FIRST_MS, FIRC_NL_WATCHER_REOPEN_FIRST_MS, on_reopen_tick,
                            w, &w->reopen_timer) != FIRC_OK) {
        w->reopen_timer = 0;
        FIRC_ERROR("netlink watcher socket cannot be reopened: interface changes go unseen until a restart");
    }
}

firc_err_t firc_nl_watcher_create(firc_loop_t *loop, firc_nl_link_cb link_cb, void *link_ud,
                                  firc_nl_addr_cb addr_cb, void *addr_ud, firc_nl_lost_cb lost_cb,
                                  void *lost_ud, firc_nl_watcher_t **out) {
    *out = NULL;
    firc_nl_watcher_t *w = calloc(1, sizeof(*w));
    if (!w) { return FIRC_ERR_NOMEM; }
    w->loop = loop;
    w->link_cb = link_cb;
    w->link_ud = link_ud;
    w->addr_cb = addr_cb;
    w->addr_ud = addr_ud;
    w->lost_cb = lost_cb;
    w->lost_ud = lost_ud;

    firc_err_t err = open_socket(w);
    if (err != FIRC_OK) {
        free(w);
        return err;
    }
    *out = w;
    return FIRC_OK;
}

int firc_nl_watcher_fd(const firc_nl_watcher_t *w) {
    return w == NULL || w->nl == NULL ? -1 : mnl_socket_get_fd(w->nl);
}

void firc_nl_watcher_destroy(firc_nl_watcher_t *w) {
    if (!w) { return; }
    close_socket(w);
    if (w->reopen_timer != 0) { firc_loop_del_timer(w->loop, w->reopen_timer); }
    free(w);
}
