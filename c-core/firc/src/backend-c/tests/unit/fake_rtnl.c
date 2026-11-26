#include "fake_rtnl.h"

#include <assert.h>

#include "firc/nlattr_iter.h"

#include <errno.h>
#include <libmnl/libmnl.h>
#include <linux/fib_rules.h>
#include <linux/if.h>
#include <linux/rtnetlink.h>
#include <poll.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <linux/sockios.h>
#include <unistd.h>

#define FAKE_RTNL_MAX 256
#define FAKE_RTNL_MAX_RULES 32

typedef struct {
    int family;
    uint32_t mark, mask, table, priority;
} fake_rule_t;

typedef struct {
    int family;
    uint32_t table, priority;
    uint8_t rtm_type, protocol, scope, dst_len;
    uint8_t dst[16];
    uint32_t oif;
    uint8_t gw_len;
    uint8_t gw[16];
    bool multipath;
} fake_route_t;
#define FAKE_RTNL_MAX_ROUTES 32

typedef struct {
    int family;
    uint32_t ifindex;
    uint8_t scope, flags;
} fake_addr_t;
#define FAKE_RTNL_MAX_ADDRS 8

struct fake_rtnl {
    int fd;
    pthread_t th;
    pthread_mutex_t mu;
    fake_rtnl_msg_t msgs[FAKE_RTNL_MAX];
    size_t n;
    unsigned link_flags;
    uint32_t gw_oif;
    uint8_t gw[16];
    uint8_t gw_len;
    int next_error;
    uint16_t next_error_type;
    unsigned next_error_left;
    unsigned next_error_skip;
    size_t link_padding;
    size_t dump_padding;
    bool stray_reply;
    bool short_err_dump;
    fake_rule_t rules[FAKE_RTNL_MAX_RULES];
    size_t n_rules;
    fake_route_t routes[FAKE_RTNL_MAX_ROUTES];
    size_t n_routes;
    fake_addr_t addrs[FAKE_RTNL_MAX_ADDRS];
    size_t n_addrs;
    struct {
        uint8_t *data;
        size_t len;
    } dgrams[FAKE_RTNL_MAX_ROUTES + 4];
    size_t n_dgrams, next_dgram;
    fake_rtnl_record_fn on_record;
    void *on_record_ud;
};

static void record(fake_rtnl_t *f, const struct nlmsghdr *h) {
    fake_rtnl_msg_t m;
    memset(&m, 0, sizeof(m));
    m.type = h->nlmsg_type;
    m.flags = h->nlmsg_flags;
    m.suppress = -1;
    const uint8_t *hdr = mnl_nlmsg_get_payload(h);
    m.family = hdr[0];
    if (h->nlmsg_type == RTM_NEWROUTE || h->nlmsg_type == RTM_DELROUTE) {
        const struct rtmsg *rtm = (const struct rtmsg *)hdr;
        m.rtm_type = rtm->rtm_type;
        m.table = rtm->rtm_table;
        m.protocol = rtm->rtm_protocol;
        m.scope = rtm->rtm_scope;
        m.dst_len = rtm->rtm_dst_len;
    } else {
        const struct fib_rule_hdr *frh = (const struct fib_rule_hdr *)hdr;
        m.table = frh->table;
    }
    firc_nlattr_iter_t it;
    const struct nlattr *attr;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct rtmsg))) { return; }
    while (firc_nlattr_iter_next(&it, &attr)) {
        switch (mnl_attr_get_type(attr)) {
        case RTA_TABLE: m.table = mnl_attr_get_u32(attr); break;
        case RTA_PRIORITY: m.priority = mnl_attr_get_u32(attr); break;
        case RTA_OIF:
            if (h->nlmsg_type == RTM_NEWROUTE || h->nlmsg_type == RTM_DELROUTE) { m.oif = mnl_attr_get_u32(attr); }
            break;
        case FRA_FWMARK:
            if (h->nlmsg_type == RTM_NEWRULE || h->nlmsg_type == RTM_DELRULE) { m.mark = mnl_attr_get_u32(attr); }
            break;
        case FRA_FWMASK:
            if (h->nlmsg_type == RTM_NEWRULE || h->nlmsg_type == RTM_DELRULE) { m.mask = mnl_attr_get_u32(attr); }
            break;
        case FRA_IIFNAME:
            if (h->nlmsg_type == RTM_NEWRULE || h->nlmsg_type == RTM_DELRULE) {
                snprintf(m.iif, sizeof(m.iif), "%.*s", (int)mnl_attr_get_payload_len(attr),
                         (const char *)mnl_attr_get_payload(attr));
            }
            break;
        case FIRC_FRA_SUPPRESS_PREFIXLEN:
            if (h->nlmsg_type == RTM_NEWRULE || h->nlmsg_type == RTM_DELRULE) { m.suppress = (int32_t)mnl_attr_get_u32(attr); }
            break;
        case RTA_GATEWAY: {
            uint16_t len = mnl_attr_get_payload_len(attr);
            if (len == 4 || len == 16) {
                memcpy(m.gw, mnl_attr_get_payload(attr), len);
                m.gw_len = (uint8_t)len;
            }
            break;
        }
        case RTA_DST: {
            uint16_t len = mnl_attr_get_payload_len(attr);
            if ((h->nlmsg_type == RTM_NEWROUTE || h->nlmsg_type == RTM_DELROUTE) && (len == 4 || len == 16)) {
                memcpy(m.dst, mnl_attr_get_payload(attr), len);
            }
            break;
        }
        default: break;
        }
    }
    pthread_mutex_lock(&f->mu);
    if (f->n < FAKE_RTNL_MAX) { f->msgs[f->n++] = m; }
    fake_rtnl_record_fn fn = f->on_record;
    void *ud = f->on_record_ud;
    pthread_mutex_unlock(&f->mu);
    if (fn != NULL) { fn(&m, ud); }
}

void fake_rtnl_on_record(fake_rtnl_t *f, fake_rtnl_record_fn fn, void *ud) {
    pthread_mutex_lock(&f->mu);
    f->on_record = fn;
    f->on_record_ud = ud;
    pthread_mutex_unlock(&f->mu);
}

typedef struct {
    uint32_t table, priority, oif;
    uint8_t dst[16];
    uint8_t gw_len;
    uint8_t gw[16];
} req_route_t;

static void route_key(const struct nlmsghdr *h, req_route_t *q) {
    const struct rtmsg *rtm = mnl_nlmsg_get_payload(h);
    memset(q, 0, sizeof(*q));
    q->table = rtm->rtm_table;
    firc_nlattr_iter_t it;
    const struct nlattr *attr;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct rtmsg))) { return; }
    while (firc_nlattr_iter_next(&it, &attr)) {
        uint16_t t = mnl_attr_get_type(attr), len = mnl_attr_get_payload_len(attr);
        if (t == RTA_TABLE && len == 4) { q->table = mnl_attr_get_u32(attr); }
        if (t == RTA_PRIORITY && len == 4) { q->priority = mnl_attr_get_u32(attr); }
        if (t == RTA_OIF && len == 4) { q->oif = mnl_attr_get_u32(attr); }
        if (t == RTA_DST && (len == 4 || len == 16)) { memcpy(q->dst, mnl_attr_get_payload(attr), len); }
        if (t == RTA_GATEWAY && (len == 4 || len == 16)) {
            memcpy(q->gw, mnl_attr_get_payload(attr), len);
            q->gw_len = (uint8_t)len;
        }
    }
}

static bool is_reject(uint8_t rtm_type) {
    return rtm_type == RTN_UNREACHABLE || rtm_type == RTN_BLACKHOLE || rtm_type == RTN_PROHIBIT;
}

/* The device a route is reported on: IPv6 puts every reject route on lo, IPv4 none. */
static uint32_t effective_oif(const fake_route_t *rt) {
    if (rt->oif == 0 && rt->family == AF_INET6 && is_reject(rt->rtm_type)) { return 1; }
    return rt->oif;
}

/* A next hop the request names must match; one it leaves out is not compared. */
static bool nexthop_matches(const fake_route_t *rt, const req_route_t *q) {
    if (q->oif != 0 && q->oif != effective_oif(rt)) { return false; }
    if (q->gw_len != 0 && (q->gw_len != rt->gw_len || memcmp(q->gw, rt->gw, q->gw_len) != 0)) { return false; }
    return true;
}

static bool key_matches(const fake_route_t *rt, const struct rtmsg *rtm, const req_route_t *q) {
    size_t addr_len = rtm->rtm_family == AF_INET ? 4 : 16;
    return rt->family == rtm->rtm_family && rt->table == q->table && rt->priority == q->priority &&
           rt->dst_len == rtm->rtm_dst_len && (rt->dst_len == 0 || memcmp(rt->dst, q->dst, addr_len) == 0);
}

/* Whether a delete names this route: IPv4 compares type, protocol and scope too, IPv6 does not. */
static bool delete_names(const fake_route_t *rt, const struct rtmsg *rtm, const req_route_t *q) {
    if (!key_matches(rt, rtm, q) || !nexthop_matches(rt, q)) { return false; }
    if (rtm->rtm_family == AF_INET6) { return true; }
    return rt->protocol == rtm->rtm_protocol && rt->rtm_type == rtm->rtm_type && rt->scope == rtm->rtm_scope;
}

/* A delete that names a stored route takes it out of later dumps. */
static void forget_route(fake_rtnl_t *f, const struct nlmsghdr *h) {
    const struct rtmsg *rtm = mnl_nlmsg_get_payload(h);
    req_route_t q;
    route_key(h, &q);
    pthread_mutex_lock(&f->mu);
    for (size_t i = 0; i < f->n_routes; i++) {
        const fake_route_t *rt = &f->routes[i];
        if (!delete_names(rt, rtm, &q)) { continue; }
        memmove(&f->routes[i], &f->routes[i + 1], (f->n_routes - i - 1) * sizeof(*rt));
        f->n_routes--;
        break;
    }
    pthread_mutex_unlock(&f->mu);
}

/* An add, stored as the kernel would; returns EEXIST for an exclusive create at a held key. */
static int store_route(fake_rtnl_t *f, const struct nlmsghdr *h) {
    const struct rtmsg *rtm = mnl_nlmsg_get_payload(h);
    req_route_t q;
    route_key(h, &q);
    int code = 0;
    pthread_mutex_lock(&f->mu);
    fake_route_t *rt = NULL;
    for (size_t i = 0; i < f->n_routes; i++) {
        if (key_matches(&f->routes[i], rtm, &q)) {
            rt = &f->routes[i];
            break;
        }
    }
    if (rt != NULL && !(h->nlmsg_flags & NLM_F_REPLACE)) {
        code = EEXIST;
    } else {
        if (rt == NULL) {
            assert(f->n_routes < FAKE_RTNL_MAX_ROUTES);
            rt = &f->routes[f->n_routes++];
            memset(rt, 0, sizeof(*rt));
            rt->family = rtm->rtm_family;
            rt->table = q.table;
            rt->priority = q.priority;
            rt->dst_len = rtm->rtm_dst_len;
            memcpy(rt->dst, q.dst, 16);
        }
        rt->rtm_type = rtm->rtm_type;
        rt->protocol = rtm->rtm_protocol;
        rt->scope = rtm->rtm_scope;
        rt->oif = q.oif;
        rt->gw_len = q.gw_len;
        memcpy(rt->gw, q.gw, 16);
    }
    pthread_mutex_unlock(&f->mu);
    return code;
}

/* The kernel dumps tables by hash bucket (id & 255 v4, id & 63 v6), so a group's table comes first. */
static unsigned table_bucket(int family, uint32_t table) {
    return family == AF_INET ? (table & 255u) : (table & 63u);
}

static void put_route_msg(uint8_t *buf, uint32_t seq, const fake_route_t *rt) {
    struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
    nlh->nlmsg_type = RTM_NEWROUTE;
    nlh->nlmsg_flags = NLM_F_MULTI;
    nlh->nlmsg_seq = seq;
    struct rtmsg *rtm = mnl_nlmsg_put_extra_header(nlh, sizeof(*rtm));
    rtm->rtm_family = (uint8_t)rt->family;
    rtm->rtm_table = rt->table < 256 ? (uint8_t)rt->table : rt->family == AF_INET ? RT_TABLE_COMPAT : (uint8_t)rt->table;
    rtm->rtm_type = rt->rtm_type;
    rtm->rtm_protocol = rt->protocol;
    rtm->rtm_scope = rt->scope;
    rtm->rtm_dst_len = rt->dst_len;
    mnl_attr_put_u32(nlh, RTA_TABLE, rt->table);
    mnl_attr_put_u32(nlh, RTA_PRIORITY, rt->priority);
    if (rt->dst_len != 0) { mnl_attr_put(nlh, RTA_DST, rt->family == AF_INET ? 4 : 16, rt->dst); }
    uint32_t oif = effective_oif(rt);
    if (rt->multipath) {
        uint8_t nhbuf[128];
        memset(nhbuf, 0, sizeof(nhbuf));
        size_t off2 = 0;
        for (int k = 0; k < 2; k++) {
            uint32_t nh_oif = k == 0 ? oif + 100 : oif;
            const uint8_t *nh_gw = rt->gw;
            uint8_t decoy[16] = {0};
            if (k == 0) {
                memcpy(decoy, rt->gw, rt->gw_len);
                decoy[rt->gw_len - 1] = (uint8_t)(decoy[rt->gw_len - 1] ^ 0xff);
                nh_gw = decoy;
            }
            struct rtnexthop *nh = (struct rtnexthop *)(void *)(nhbuf + off2);
            nh->rtnh_ifindex = (int)nh_oif;
            size_t len = sizeof(*nh);
            if (rt->gw_len != 0) {
                struct nlattr *a = (struct nlattr *)(void *)(nhbuf + off2 + len);
                a->nla_type = RTA_GATEWAY;
                const size_t hdr = sizeof(struct nlattr);
                a->nla_len = (uint16_t)(hdr + rt->gw_len);
                memcpy(nhbuf + off2 + len + hdr, nh_gw, rt->gw_len);
                len += ((size_t)a->nla_len + 3u) & ~3u;
            }
            nh->rtnh_len = (unsigned short)len;
            off2 += len;
        }
        mnl_attr_put(nlh, RTA_MULTIPATH, (uint16_t)off2, nhbuf);
        return;
    }
    if (oif != 0) { mnl_attr_put_u32(nlh, RTA_OIF, oif); }
    if (rt->gw_len != 0) { mnl_attr_put(nlh, RTA_GATEWAY, rt->gw_len, rt->gw); }
}

static void reply(fake_rtnl_t *f, const void *buf, size_t len) {
    (void)send(f->fd, buf, len, MSG_NOSIGNAL);
}

static bool dump_running(const fake_rtnl_t *f) {
    return f->next_dgram < f->n_dgrams;
}

/* One datagram of the dump being built; sent later, in turn. */
static void enqueue(fake_rtnl_t *f, const void *buf, size_t len) {
    assert(f->n_dgrams < sizeof(f->dgrams) / sizeof(f->dgrams[0]));
    uint8_t *copy = malloc(len);
    assert(copy != NULL);
    memcpy(copy, buf, len);
    f->dgrams[f->n_dgrams].data = copy;
    f->dgrams[f->n_dgrams].len = len;
    f->n_dgrams++;
}

static void drop_dgrams(fake_rtnl_t *f) {
    for (size_t i = 0; i < f->n_dgrams; i++) { free(f->dgrams[i].data); }
    f->n_dgrams = 0;
    f->next_dgram = 0;
}

/* Bytes of what was sent to the daemon that it has not read yet. */
static int unread(const fake_rtnl_t *f) {
    int n = 0;
    if (ioctl(f->fd, SIOCOUTQ, &n) != 0) { return 0; }
    return n;
}

static void put_error(uint8_t *buf, const struct nlmsghdr *req, int err) {
    struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
    nlh->nlmsg_type = NLMSG_ERROR;
    nlh->nlmsg_seq = req->nlmsg_seq;
    struct nlmsgerr *e = mnl_nlmsg_put_extra_header(nlh, sizeof(*e));
    e->error = -err;
    e->msg = *req;
}

/* An added rule is reported in later dumps, as the kernel does, so mark fields stay distinct. */
static void store_rule(fake_rtnl_t *f, const struct nlmsghdr *h) {
    const struct fib_rule_hdr *frh = mnl_nlmsg_get_payload(h);
    fake_rule_t r = {.family = frh->family, .table = frh->table};
    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct fib_rule_hdr))) {
        while (firc_nlattr_iter_next(&it, &a)) {
            switch (mnl_attr_get_type(a)) {
            case FRA_TABLE: r.table = mnl_attr_get_u32(a); break;
            case FRA_PRIORITY: r.priority = mnl_attr_get_u32(a); break;
            case FRA_FWMARK: r.mark = mnl_attr_get_u32(a); break;
            case FRA_FWMASK: r.mask = mnl_attr_get_u32(a); break;
            default: break;
            }
        }
    }
    pthread_mutex_lock(&f->mu);
    if (f->n_rules < FAKE_RTNL_MAX_RULES) { f->rules[f->n_rules++] = r; }
    pthread_mutex_unlock(&f->mu);
}

/* A deleted rule stops being reported; firc's deletes name priority, table and mark. */
static void forget_rule(fake_rtnl_t *f, const struct nlmsghdr *h) {
    const struct fib_rule_hdr *frh = mnl_nlmsg_get_payload(h);
    fake_rule_t want = {.family = frh->family, .table = frh->table};
    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct fib_rule_hdr))) {
        while (firc_nlattr_iter_next(&it, &a)) {
            switch (mnl_attr_get_type(a)) {
            case FRA_TABLE: want.table = mnl_attr_get_u32(a); break;
            case FRA_PRIORITY: want.priority = mnl_attr_get_u32(a); break;
            case FRA_FWMARK: want.mark = mnl_attr_get_u32(a); break;
            case FRA_FWMASK: want.mask = mnl_attr_get_u32(a); break;
            default: break;
            }
        }
    }
    pthread_mutex_lock(&f->mu);
    for (size_t i = 0; i < f->n_rules; i++) {
        if (f->rules[i].family != want.family) { continue; }
        if (f->rules[i].table != want.table || f->rules[i].priority != want.priority) { continue; }
        if (f->rules[i].mark != want.mark || f->rules[i].mask != want.mask) { continue; }
        f->rules[i] = f->rules[--f->n_rules];
        break;
    }
    pthread_mutex_unlock(&f->mu);
}

static void answer(fake_rtnl_t *f, const struct nlmsghdr *req) {
    uint8_t buf[16384];
    memset(buf, 0, sizeof(buf));
    if (req->nlmsg_type == RTM_GETLINK && !(req->nlmsg_flags & NLM_F_DUMP)) {
        pthread_mutex_lock(&f->mu);
        int refused = f->next_error != 0 && f->next_error_type == RTM_GETLINK ? f->next_error : 0;
        if (refused != 0 && (f->next_error_left == 0 || --f->next_error_left == 0)) {
            f->next_error = 0;
            f->next_error_type = 0;
        }
        pthread_mutex_unlock(&f->mu);
        if (refused != 0) {
            put_error(buf, req, refused);
            reply(f, buf, ((struct nlmsghdr *)buf)->nlmsg_len);
            return;
        }
        const struct ifinfomsg *want = mnl_nlmsg_get_payload(req);
        struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
        nlh->nlmsg_type = RTM_NEWLINK;
        nlh->nlmsg_seq = req->nlmsg_seq;
        struct ifinfomsg *ifi = mnl_nlmsg_put_extra_header(nlh, sizeof(*ifi));
        ifi->ifi_family = AF_UNSPEC;
        ifi->ifi_index = want->ifi_index;
        pthread_mutex_lock(&f->mu);
        ifi->ifi_flags = f->link_flags;
        size_t pad = f->link_padding;
        pthread_mutex_unlock(&f->mu);
        if (pad > 0 && pad < sizeof(buf) - 256) {
            uint8_t *zeros = calloc(1, pad);
            if (zeros != NULL) {
                mnl_attr_put(nlh, 200, (uint16_t)pad, zeros);
                free(zeros);
            }
        }
        reply(f, buf, nlh->nlmsg_len);
        return;
    }
    if ((req->nlmsg_flags & NLM_F_DUMP) == NLM_F_DUMP) {
        size_t off = 0;
        if (dump_running(f)) {
            put_error(buf, req, EBUSY);
            reply(f, buf, ((struct nlmsghdr *)buf)->nlmsg_len);
            return;
        }
        pthread_mutex_lock(&f->mu);
        bool dump_armed = f->next_error != 0 && f->next_error_type == req->nlmsg_type;
        if (dump_armed && f->next_error_skip > 0) {
            f->next_error_skip--;
            dump_armed = false;
        }
        if (dump_armed) {
            int err = f->next_error;
            if (f->next_error_left == 0 || --f->next_error_left == 0) {
                f->next_error = 0;
                f->next_error_type = 0;
            }
            pthread_mutex_unlock(&f->mu);
            struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
            nlh->nlmsg_type = NLMSG_ERROR;
            nlh->nlmsg_seq = req->nlmsg_seq;
            struct nlmsgerr *e = mnl_nlmsg_put_extra_header(nlh, sizeof(*e));
            e->error = -err;
            e->msg = *req;
            reply(f, buf, nlh->nlmsg_len);
            return;
        }
        if (req->nlmsg_type == RTM_GETRULE) {
            const struct fib_rule_hdr *want = mnl_nlmsg_get_payload(req);
            for (size_t i = 0; i < f->n_rules; i++) {
                if (f->rules[i].family != want->family) { continue; }
                struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf + off);
                nlh->nlmsg_type = RTM_NEWRULE;
                nlh->nlmsg_flags = NLM_F_MULTI;
                nlh->nlmsg_seq = req->nlmsg_seq;
                struct fib_rule_hdr *frh = mnl_nlmsg_put_extra_header(nlh, sizeof(*frh));
                frh->family = (uint8_t)f->rules[i].family;
                frh->action = FR_ACT_TO_TBL;
                frh->table = f->rules[i].table < 256 ? (uint8_t)f->rules[i].table : RT_TABLE_UNSPEC;
                mnl_attr_put_u32(nlh, FRA_TABLE, f->rules[i].table);
                mnl_attr_put_u32(nlh, FRA_PRIORITY, f->rules[i].priority);
                if (f->rules[i].mask != 0) {
                    mnl_attr_put_u32(nlh, FRA_FWMARK, f->rules[i].mark);
                    mnl_attr_put_u32(nlh, FRA_FWMASK, f->rules[i].mask);
                }
                off += nlh->nlmsg_len;
            }
        }
        if (req->nlmsg_type == RTM_GETADDR) {
            const struct ifaddrmsg *want = mnl_nlmsg_get_payload(req);
            for (size_t i = 0; i < f->n_addrs; i++) {
                if (f->addrs[i].family != want->ifa_family) { continue; }
                struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf + off);
                nlh->nlmsg_type = RTM_NEWADDR;
                nlh->nlmsg_flags = NLM_F_MULTI;
                nlh->nlmsg_seq = req->nlmsg_seq;
                struct ifaddrmsg *ifa = mnl_nlmsg_put_extra_header(nlh, sizeof(*ifa));
                ifa->ifa_family = (uint8_t)f->addrs[i].family;
                ifa->ifa_prefixlen = 64;
                ifa->ifa_index = f->addrs[i].ifindex;
                ifa->ifa_scope = f->addrs[i].scope;
                ifa->ifa_flags = f->addrs[i].flags;
                off += nlh->nlmsg_len;
            }
        }
        const struct rtmsg *want_rt = mnl_nlmsg_get_payload(req);
        if (req->nlmsg_type == RTM_GETROUTE) {
            fake_route_t scripted;
            memset(&scripted, 0, sizeof(scripted));
            bool with_gw = f->gw_len > 0 && want_rt->rtm_family == (f->gw_len == 4 ? AF_INET : AF_INET6);
            if (with_gw) {
                scripted.family = want_rt->rtm_family;
                scripted.table = RT_TABLE_MAIN;
                scripted.rtm_type = RTN_UNICAST;
                scripted.protocol = RTPROT_BOOT;
                scripted.oif = f->gw_oif;
                scripted.gw_len = f->gw_len;
                memcpy(scripted.gw, f->gw, 16);
            }
            bool padded = false;
            for (unsigned bucket = 0; bucket < 256; bucket++) {
                for (size_t i = 0; i < f->n_routes + 1; i++) {
                    const fake_route_t *rt = i == 0 ? &scripted : &f->routes[i - 1];
                    if (i == 0 && !with_gw) { continue; }
                    if (rt->family != want_rt->rtm_family || table_bucket(rt->family, rt->table) != bucket) {
                        continue;
                    }
                    if (off > 0) {
                        enqueue(f, buf, off);
                        off = 0;
                        memset(buf, 0, sizeof(buf));
                    }
                    if (f->short_err_dump) {
                        f->short_err_dump = false;
                        struct nlmsghdr *bad = mnl_nlmsg_put_header(buf + off);
                        bad->nlmsg_type = NLMSG_ERROR;
                        bad->nlmsg_flags = NLM_F_MULTI;
                        bad->nlmsg_seq = req->nlmsg_seq;
                        enqueue(f, buf, off + bad->nlmsg_len);
                        off = 0;
                        memset(buf, 0, sizeof(buf));
                    }
                    put_route_msg(buf + off, req->nlmsg_seq, rt);
                    struct nlmsghdr *nlh = (struct nlmsghdr *)(buf + off);
                    if (!padded && f->dump_padding > 0 && f->dump_padding < sizeof(buf) - off - 512) {
                        uint8_t *zeros = calloc(1, f->dump_padding);
                        if (zeros != NULL) {
                            mnl_attr_put(nlh, 200, (uint16_t)f->dump_padding, zeros);
                            free(zeros);
                        }
                        padded = true;
                    }
                    off += nlh->nlmsg_len;
                }
            }
        }
        pthread_mutex_unlock(&f->mu);
        struct nlmsghdr *done = mnl_nlmsg_put_header(buf + off);
        done->nlmsg_type = NLMSG_DONE;
        done->nlmsg_flags = NLM_F_MULTI;
        done->nlmsg_seq = req->nlmsg_seq;
        mnl_nlmsg_put_extra_header(done, sizeof(int));
        enqueue(f, buf, off + done->nlmsg_len);
        return;
    }
    record(f, req);
    if (f->stray_reply) {
        f->stray_reply = false;
        uint8_t sbuf[128];
        memset(sbuf, 0, sizeof(sbuf));
        struct nlmsghdr *sh = mnl_nlmsg_put_header(sbuf);
        sh->nlmsg_type = NLMSG_ERROR;
        sh->nlmsg_seq = req->nlmsg_seq - 1000;
        struct nlmsgerr *se = mnl_nlmsg_put_extra_header(sh, sizeof(*se));
        se->error = 0;
        reply(f, sbuf, sh->nlmsg_len);
    }
    struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
    nlh->nlmsg_type = NLMSG_ERROR;
    nlh->nlmsg_seq = req->nlmsg_seq;
    struct nlmsgerr *e = mnl_nlmsg_put_extra_header(nlh, sizeof(*e));
    pthread_mutex_lock(&f->mu);
    bool armed = f->next_error != 0 && (f->next_error_type == 0 || f->next_error_type == req->nlmsg_type);
    if (armed && f->next_error_skip > 0) {
        f->next_error_skip--;
        armed = false;
    }
    e->error = armed ? -f->next_error : 0;
    if (armed && (f->next_error_left == 0 || --f->next_error_left == 0)) {
        f->next_error = 0;
        f->next_error_type = 0;
    }
    pthread_mutex_unlock(&f->mu);
    if (!armed && req->nlmsg_type == RTM_DELROUTE) { forget_route(f, req); }
    if (!armed && req->nlmsg_type == RTM_NEWROUTE) { e->error = -store_route(f, req); }
    if (!armed && req->nlmsg_type == RTM_DELRULE) { forget_rule(f, req); }
    if (!armed && req->nlmsg_type == RTM_NEWRULE) { store_rule(f, req); }
    e->msg = *req;
    reply(f, buf, nlh->nlmsg_len);
}

static void *kernel_main(void *ud) {
    fake_rtnl_t *f = ud;
    uint8_t buf[8192];
    for (;;) {
        if (dump_running(f) && unread(f) == 0) {
            reply(f, f->dgrams[f->next_dgram].data, f->dgrams[f->next_dgram].len);
            f->next_dgram++;
            if (!dump_running(f)) { drop_dgrams(f); }
        }
        struct pollfd pfd = {.fd = f->fd, .events = POLLIN};
        int r = poll(&pfd, 1, dump_running(f) ? 1 : -1);
        if (r < 0) { break; }
        if (r == 0) { continue; }
        ssize_t got = recv(f->fd, buf, sizeof(buf), 0);
        if (got <= 0) { break; }
        int len = (int)got;
        const struct nlmsghdr *h = (const struct nlmsghdr *)buf;
        while (mnl_nlmsg_ok(h, len)) {
            answer(f, h);
            h = mnl_nlmsg_next(h, &len);
        }
    }
    drop_dgrams(f);
    return NULL;
}

fake_rtnl_t *fake_rtnl_start(firc_rtnl_t **out) {
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_SEQPACKET, 0, sv) != 0) { return NULL; }
    fake_rtnl_t *f = calloc(1, sizeof(*f));
    if (f == NULL) {
        close(sv[0]);
        close(sv[1]);
        return NULL;
    }
    f->fd = sv[0];
    f->link_flags = IFF_UP;
    pthread_mutex_init(&f->mu, NULL);
    *out = firc_rtnl_open_fd(sv[1]);
    if (*out == NULL || pthread_create(&f->th, NULL, kernel_main, f) != 0) {
        firc_rtnl_close(*out);
        *out = NULL;
        close(sv[0]);
        free(f);
        return NULL;
    }
    return f;
}

void fake_rtnl_stop(fake_rtnl_t *f) {
    if (f == NULL) { return; }
    shutdown(f->fd, SHUT_RDWR);
    pthread_join(f->th, NULL);
    close(f->fd);
    pthread_mutex_destroy(&f->mu);
    free(f);
}

void fake_rtnl_set_link_flags(fake_rtnl_t *f, unsigned flags) {
    pthread_mutex_lock(&f->mu);
    f->link_flags = flags;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_set_gateway(fake_rtnl_t *f, uint32_t oif, const uint8_t *gw, uint8_t gw_len) {
    pthread_mutex_lock(&f->mu);
    f->gw_oif = oif;
    f->gw_len = gw_len;
    if (gw_len > 0) { memcpy(f->gw, gw, gw_len); }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_add_rule(fake_rtnl_t *f, int family, uint32_t mark, uint32_t mask, uint32_t table,
                        uint32_t priority) {
    pthread_mutex_lock(&f->mu);
    if (f->n_rules < FAKE_RTNL_MAX_RULES) {
        f->rules[f->n_rules++] = (fake_rule_t){family, mark, mask, table, priority};
    }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_add_route(fake_rtnl_t *f, int family, uint32_t table, uint8_t rtm_type, uint8_t protocol,
                         const uint8_t *dst, uint8_t dst_len, uint32_t priority) {
    pthread_mutex_lock(&f->mu);
    assert(f->n_routes < FAKE_RTNL_MAX_ROUTES);
    {
        fake_route_t *rt = &f->routes[f->n_routes++];
        memset(rt, 0, sizeof(*rt));
        rt->family = family;
        rt->table = table;
        rt->priority = priority;
        rt->rtm_type = rtm_type;
        rt->protocol = protocol;
        rt->scope = RT_SCOPE_UNIVERSE;
        rt->dst_len = dst_len;
        memcpy(rt->dst, dst, family == AF_INET ? 4 : 16);
    }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_clear_routes(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    f->n_routes = 0;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_scope_last_route(fake_rtnl_t *f, uint8_t scope) {
    pthread_mutex_lock(&f->mu);
    if (f->n_routes > 0) { f->routes[f->n_routes - 1].scope = scope; }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_route_via(fake_rtnl_t *f, uint32_t oif, const uint8_t *gw, uint8_t gw_len) {
    pthread_mutex_lock(&f->mu);
    if (f->n_routes > 0) {
        fake_route_t *rt = &f->routes[f->n_routes - 1];
        rt->oif = oif;
        rt->gw_len = gw_len;
        if (gw_len > 0) { memcpy(rt->gw, gw, gw_len); }
    }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_route_multipath(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    if (f->n_routes > 0) { f->routes[f->n_routes - 1].multipath = true; }
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_stray_reply(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    f->stray_reply = true;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_short_error_in_dump(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    f->short_err_dump = true;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_hangup(fake_rtnl_t *f) {
    shutdown(f->fd, SHUT_WR);
}

void fake_rtnl_set_dump_reply_padding(fake_rtnl_t *f, size_t bytes) {
    pthread_mutex_lock(&f->mu);
    f->dump_padding = bytes;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_set_link_reply_padding(fake_rtnl_t *f, size_t bytes) {
    pthread_mutex_lock(&f->mu);
    f->link_padding = bytes;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_fail_next_of(fake_rtnl_t *f, uint16_t type, int err) {
    pthread_mutex_lock(&f->mu);
    f->next_error = err;
    f->next_error_type = type;
    f->next_error_left = 1;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_fail_after_of(fake_rtnl_t *f, uint16_t type, int err, unsigned skip) {
    pthread_mutex_lock(&f->mu);
    f->next_error = err;
    f->next_error_type = type;
    f->next_error_left = 1;
    f->next_error_skip = skip;
    pthread_mutex_unlock(&f->mu);
}

bool fake_rtnl_failure_armed(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    bool armed = f->next_error != 0;
    pthread_mutex_unlock(&f->mu);
    return armed;
}

void fake_rtnl_fail_times(fake_rtnl_t *f, uint16_t type, int err, unsigned n) {
    pthread_mutex_lock(&f->mu);
    f->next_error = err;
    f->next_error_type = type;
    f->next_error_left = n;
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_fail_next(fake_rtnl_t *f, int err) {
    pthread_mutex_lock(&f->mu);
    f->next_error = err;
    f->next_error_type = 0;
    f->next_error_left = 1;
    pthread_mutex_unlock(&f->mu);
}

size_t fake_rtnl_messages(fake_rtnl_t *f, fake_rtnl_msg_t *out, size_t cap) {
    pthread_mutex_lock(&f->mu);
    size_t n = f->n;
    if (out != NULL) {
        size_t k = n < cap ? n : cap;
        memcpy(out, f->msgs, k * sizeof(*out));
    }
    pthread_mutex_unlock(&f->mu);
    return n;
}

size_t fake_rtnl_count(fake_rtnl_t *f, uint16_t type, uint8_t rtm_type) {
    size_t c = 0;
    pthread_mutex_lock(&f->mu);
    for (size_t i = 0; i < f->n; i++) {
        if (f->msgs[i].type == type && (rtm_type == 0 || f->msgs[i].rtm_type == rtm_type)) { c++; }
    }
    pthread_mutex_unlock(&f->mu);
    return c;
}

void fake_rtnl_add_addr(fake_rtnl_t *f, int family, uint32_t ifindex, uint8_t scope, uint8_t flags) {
    pthread_mutex_lock(&f->mu);
    assert(f->n_addrs < FAKE_RTNL_MAX_ADDRS);
    f->addrs[f->n_addrs++] = (fake_addr_t){family, ifindex, scope, flags};
    pthread_mutex_unlock(&f->mu);
}

void fake_rtnl_clear_addrs(fake_rtnl_t *f) {
    pthread_mutex_lock(&f->mu);
    f->n_addrs = 0;
    pthread_mutex_unlock(&f->mu);
}
