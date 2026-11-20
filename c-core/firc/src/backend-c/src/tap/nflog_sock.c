#include "firc/nflogsock.h"

#include <arpa/inet.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

#include <linux/netfilter/nfnetlink.h>
#include <linux/netfilter/nfnetlink_log.h>
#include <linux/netlink.h>

#include "firc/log.h"
#include "nflog_internal.h"

#define ATTR_HDR ((size_t)4) /* NLA_HDRLEN, without its negative ~3 mask */
#define NFLOG_CFG ((uint16_t)((NFNL_SUBSYS_ULOG << 8) | NFULNL_MSG_CONFIG))

#define NFLOG_BUF FIRC_NFLOG_BUF
_Static_assert(NFLOG_BUF >= FIRC_NFLOG_MAX_RANGE + 1024u,
               "the read buffer must hold a packet of the largest copy range");

#define NFLOG_QTHRESH 1u
#define NFLOG_TIMEOUT 1u /* hundredths of a second */

#ifndef FIRC_NFLOG_ACK_MS
#define FIRC_NFLOG_ACK_MS 2000 /* a kernel answers a bind in microseconds */
#endif

struct firc_nflog {
    int fd;
    uint32_t seq;
    _Alignas(4) uint8_t buf[NFLOG_BUF];
};

static size_t align4(size_t n) { return (n + 3u) & ~(size_t)3u; }

static void put_attr(uint8_t *msg, size_t *at, uint16_t type, const void *val, size_t len) {
    struct nlattr *a = (struct nlattr *)(msg + *at);
    a->nla_type = type;
    a->nla_len = (uint16_t)(ATTR_HDR + len);
    memcpy(msg + *at + ATTR_HDR, val, len);
    *at += align4(a->nla_len);
}

/* Reads until the kernel answers this seq; FIRC_OK only on a yes, errno carries a refusal. */
static firc_err_t wait_ack(firc_nflog_t *n, uint32_t seq) {
    for (;;) {
        ssize_t r = recv(n->fd, n->buf, sizeof(n->buf), 0);
        if (r < 0) { return firc_err_from_errno(errno); }
        size_t left = (size_t)r;
        size_t at = 0;
        while (left >= NLMSG_HDRLEN) {
            const struct nlmsghdr *h = (const struct nlmsghdr *)(n->buf + at);
            size_t mlen = h->nlmsg_len;
            if (mlen < NLMSG_HDRLEN || mlen > left) {
                errno = EBADMSG;
                return FIRC_ERR_PROTO;
            }
            if (h->nlmsg_type == NLMSG_ERROR && h->nlmsg_seq == seq) {
                if (mlen < NLMSG_HDRLEN + sizeof(struct nlmsgerr)) {
                    errno = EBADMSG;
                    return FIRC_ERR_PROTO;
                }
                const struct nlmsgerr *e =
                    (const struct nlmsgerr *)(n->buf + at + NLMSG_HDRLEN);
                if (e->error == 0) { return FIRC_OK; }
                errno = -e->error;
                return firc_err_from_errno(-e->error);
            }
            size_t step = align4(mlen);
            if (step >= left) { break; }
            at += step;
            left -= step;
        }
    }
}

/* One message: BIND creates the instance and the attributes after it configure it; a second message would be -EBUSY. */
static firc_err_t bind_the_group(firc_nflog_t *n, uint16_t group, uint16_t copy_range) {
    /* A union, not a cast: an unaligned word is a kernel fixup at best on mipsel. */
    union {
        uint8_t b[128];
        struct nlmsghdr h;
    } u;
    uint8_t *msg = u.b;
    memset(msg, 0, sizeof(u.b));

    struct nlmsghdr *h = &u.h;
    h->nlmsg_type = NFLOG_CFG;
    h->nlmsg_flags = NLM_F_REQUEST | NLM_F_ACK;
    h->nlmsg_seq = ++n->seq;

    size_t at = NLMSG_HDRLEN;
    struct nfgenmsg *g = (struct nfgenmsg *)(msg + at);
    g->nfgen_family = AF_UNSPEC; /* the group is in res_id; the family is not read */
    g->version = NFNETLINK_V0;
    g->res_id = htons(group);
    at += align4(sizeof(*g));

    struct nfulnl_msg_config_cmd cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.command = NFULNL_CFG_CMD_BIND;
    put_attr(msg, &at, NFULA_CFG_CMD, &cmd, sizeof(cmd));

    struct nfulnl_msg_config_mode mode;
    memset(&mode, 0, sizeof(mode));
    mode.copy_mode = NFULNL_COPY_PACKET;
    mode.copy_range = htonl(copy_range);
    put_attr(msg, &at, NFULA_CFG_MODE, &mode, sizeof(mode));

    uint32_t qthresh = htonl(NFLOG_QTHRESH);
    put_attr(msg, &at, NFULA_CFG_QTHRESH, &qthresh, sizeof(qthresh));
    uint32_t timeout = htonl(NFLOG_TIMEOUT);
    put_attr(msg, &at, NFULA_CFG_TIMEOUT, &timeout, sizeof(timeout));

    h->nlmsg_len = (uint32_t)at;
    if (send(n->fd, msg, at, 0) != (ssize_t)at) { return FIRC_ERR_IO; }
    return wait_ack(n, h->nlmsg_seq);
}

firc_nflog_t *firc_nflog_open_fd(int fd, uint16_t group, uint16_t copy_range) {
    if (copy_range == 0 || copy_range > FIRC_NFLOG_MAX_RANGE) {
        FIRC_WARN("nflog: a copy range of %u bytes is not one this reader can hold (1..%u)",
                   (unsigned)copy_range, (unsigned)FIRC_NFLOG_MAX_RANGE);
        close(fd);
        return NULL;
    }
    firc_nflog_t *n = calloc(1, sizeof(*n));
    if (n == NULL) {
        close(fd);
        return NULL;
    }
    n->fd = fd;

    struct timeval tv = {FIRC_NFLOG_ACK_MS / 1000,
                         (suseconds_t)(FIRC_NFLOG_ACK_MS % 1000) * 1000};
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));

    firc_err_t e = bind_the_group(n, group, copy_range);
    if (e != FIRC_OK) {
        /* A timeout is not a refusal: say so, since it is a different fault to chase. */
        if (e == FIRC_ERR_AGAIN) {
            FIRC_WARN("nflog: the kernel did not answer the bind of group %u within %d ms",
                       (unsigned)group, FIRC_NFLOG_ACK_MS);
        } else {
            FIRC_WARN("nflog: the kernel refused group %u: %s", (unsigned)group,
                       strerror(errno));
        }
        firc_nflog_close(n);
        return NULL;
    }

    tv.tv_sec = 0;
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    return n;
}

firc_nflog_t *firc_nflog_open(uint16_t group, uint16_t copy_range) {
    int fd = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_NETFILTER);
    if (fd < 0) {
        FIRC_WARN("nflog: no netfilter netlink socket: %s", strerror(errno));
        return NULL;
    }
    int rcvbuf = 1024 * 1024;
    bool got = false;
#ifdef SO_RCVBUFFORCE
    got = setsockopt(fd, SOL_SOCKET, SO_RCVBUFFORCE, &rcvbuf, sizeof(rcvbuf)) == 0;
#endif
    if (!got) { (void)setsockopt(fd, SOL_SOCKET, SO_RCVBUF, &rcvbuf, sizeof(rcvbuf)); }

    struct sockaddr_nl sa;
    memset(&sa, 0, sizeof(sa));
    sa.nl_family = AF_NETLINK;
    if (bind(fd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        FIRC_WARN("nflog: cannot bind the netlink socket: %s", strerror(errno));
        close(fd);
        return NULL;
    }
    return firc_nflog_open_fd(fd, group, copy_range);
}

int firc_nflog_fd(const firc_nflog_t *n) { return n ? n->fd : -1; }

firc_err_t firc_nflog_recv_error(int errnum) {
    if (errnum == EAGAIN || errnum == EWOULDBLOCK || errnum == EINTR) { return FIRC_ERR_AGAIN; }
    if (errnum == ENOBUFS) { return FIRC_ERR_LIMIT; }
    return FIRC_ERR_IO;
}

bool firc_nflog_from_the_kernel(const void *addr, size_t addr_len) {
    if (addr == NULL || addr_len < sizeof(struct sockaddr_nl)) { return true; }
    const struct sockaddr_nl *nl = addr;
    if (nl->nl_family != AF_NETLINK) { return true; }
    return nl->nl_pid == 0;
}

firc_err_t firc_nflog_read(firc_nflog_t *n, void (*cb)(const uint8_t *pkt, size_t len, void *ud),
                           void *ud) {
    if (n == NULL || cb == NULL) { return FIRC_ERR_INVAL; }
    /* recvfrom with MSG_TRUNC: who sent it decides whether it is read, and the kernel reports the real size. */
    struct sockaddr_nl from;
    socklen_t from_len = sizeof(from);
    memset(&from, 0, sizeof(from));
    ssize_t r = recvfrom(n->fd, n->buf, sizeof(n->buf), MSG_DONTWAIT | MSG_TRUNC,
                         (struct sockaddr *)&from, &from_len);
    if (r < 0) { return firc_nflog_recv_error(errno); }
    if (!firc_nflog_from_the_kernel(&from, from_len)) {
        FIRC_WARN("nflog: a datagram from port %u, which is not the kernel -- ignored",
                  (unsigned)from.nl_pid);
        return FIRC_ERR_PROTO;
    }

    size_t have = (size_t)r;
    bool lost = false;
    if (have > sizeof(n->buf)) {
        have = sizeof(n->buf);
        lost = true;
    }
    size_t unwalkable = firc_nflog_walk(n->buf, have, cb, ud);
    if (lost || unwalkable > 0) { return FIRC_ERR_LIMIT; }
    return FIRC_OK;
}

void firc_nflog_close(firc_nflog_t *n) {
    if (n == NULL) { return; }
    if (n->fd >= 0) { close(n->fd); }
    free(n);
}
