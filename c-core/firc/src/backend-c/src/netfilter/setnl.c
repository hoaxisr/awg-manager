#include "firc/setnl.h"

#include <arpa/inet.h>
#include <errno.h>
#include <libmnl/libmnl.h>
#include <linux/netfilter/nfnetlink.h>
#include <linux/netlink.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

#include "firc/log.h"
#include "firc/nlattr_iter.h"
#include "setnl_abi.h"

#define SETNL_DATAGRAM 32768
#define SETNL_RECV 32768
#define SETNL_MSG_MAX 256
#define SETNL_ROUND_MS 2000
#define SETNL_MIN_SIZE 1024u

struct firc_setnl {
    int fd;
    uint32_t seq;
    bool probed;
    firc_setnl_caps_t caps;
    uint8_t *out;
    uint8_t *in;
};

typedef void (*msg_fn)(const struct nlmsghdr *h, void *ud);

firc_setnl_t *firc_setnl_new_fd(int fd) {
    firc_setnl_t *s = calloc(1, sizeof(*s));
    if (s != NULL) {
        s->out = malloc(SETNL_DATAGRAM);
        s->in = malloc(SETNL_RECV);
    }
    if (s == NULL || s->out == NULL || s->in == NULL) {
        if (s != NULL) {
            free(s->out);
            free(s->in);
            free(s);
        }
        close(fd);
        return NULL;
    }
    s->fd = fd;
    return s;
}

firc_err_t firc_setnl_new(firc_setnl_t **out) {
    if (out == NULL) { return FIRC_ERR_INVAL; }
    *out = NULL;
    int fd = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_NETFILTER);
    if (fd < 0) { return firc_err_from_errno(errno); }
    struct sockaddr_nl sa;
    memset(&sa, 0, sizeof(sa));
    sa.nl_family = AF_NETLINK;
    if (bind(fd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        int e = errno;
        close(fd);
        return firc_err_from_errno(e);
    }
    *out = firc_setnl_new_fd(fd);
    return *out != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
}

void firc_setnl_free(firc_setnl_t *s) {
    if (s == NULL) { return; }
    close(s->fd);
    free(s->out);
    free(s->in);
    free(s);
}

static int64_t now_ms(void) {
    struct timespec ts = {0, 0};
    if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) { return 0; }
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static bool name_ok(const char *name) {
    return name != NULL && name[0] != '\0' && strlen(name) < SETNL_MAXNAMELEN;
}

static struct nlmsghdr *begin(uint8_t *buf, uint8_t cmd, uint16_t flags) {
    memset(buf, 0, SETNL_MSG_MAX);
    struct nlmsghdr *nlh = mnl_nlmsg_put_header(buf);
    nlh->nlmsg_type = (uint16_t)((SETNL_SUBSYS_IPSET << 8u) | (unsigned)cmd);
    nlh->nlmsg_flags = (uint16_t)(NLM_F_REQUEST | flags);
    struct nfgenmsg *g = mnl_nlmsg_put_extra_header(nlh, sizeof(*g));
    g->nfgen_family = AF_INET;
    g->version = NFNETLINK_V0;
    g->res_id = 0;
    mnl_attr_put_u8(nlh, SETNL_ATTR_PROTOCOL, SETNL_PROTOCOL);
    return nlh;
}

static void put_be32(struct nlmsghdr *nlh, uint16_t type, uint32_t v) {
    uint32_t be = htonl(v);
    mnl_attr_put(nlh, (uint16_t)((unsigned)type | SETNL_F_NET_BYTEORDER), sizeof(be), &be);
}

static firc_err_t from_code(int code) {
    switch (code) {
    case 0: return FIRC_OK;
    case EBUSY:
    case SETNL_ERR_BUSY:
    case SETNL_ERR_REFERENCED: return FIRC_ERR_AGAIN;
    case SETNL_ERR_FIND_TYPE: return FIRC_ERR_NOSYS;
    default: return code < 4096 ? firc_err_from_errno(code) : FIRC_ERR_PROTO;
    }
}

static firc_err_t round_trip(firc_setnl_t *s, size_t len, uint32_t first, uint32_t last, bool dump, int *code,
                             msg_fn fn, void *ud) {
    *code = 0;
    ssize_t sent = send(s->fd, s->out, len, 0);
    if (sent < 0) { return firc_err_from_errno(errno); }
    if ((size_t)sent != len) { return FIRC_ERR_IO; }
    const int64_t deadline = now_ms() + SETNL_ROUND_MS;
    for (;;) {
        int64_t wait = deadline - now_ms();
        if (wait <= 0) { return FIRC_ERR_TIMEOUT; }
        if (wait > SETNL_ROUND_MS) { wait = SETNL_ROUND_MS; }
        struct pollfd pfd = {.fd = s->fd, .events = POLLIN};
        int pr = poll(&pfd, 1, (int)wait);
        if (pr == 0) { return FIRC_ERR_TIMEOUT; }
        if (pr < 0) {
            if (errno == EINTR) { continue; }
            return firc_err_from_errno(errno);
        }
        ssize_t got = recv(s->fd, s->in, SETNL_RECV, MSG_TRUNC | MSG_DONTWAIT);
        if (got < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) { continue; }
            if (errno == ENOBUFS) { return FIRC_ERR_IO; }
            return firc_err_from_errno(errno);
        }
        if (got == 0) { return FIRC_ERR_IO; }
        if ((size_t)got > SETNL_RECV) { return FIRC_ERR_PROTO; }
        int left = (int)got;
        const struct nlmsghdr *h = (const struct nlmsghdr *)s->in;
        while (mnl_nlmsg_ok(h, left)) {
            if (h->nlmsg_seq - first <= last - first) {
                if (h->nlmsg_type == NLMSG_ERROR) {
                    if (mnl_nlmsg_get_payload_len(h) < sizeof(struct nlmsgerr)) { return FIRC_ERR_PROTO; }
                    const struct nlmsgerr *e = mnl_nlmsg_get_payload(h);
                    if (e->error != 0 && *code == 0) { *code = e->error < 0 ? -e->error : e->error; }
                    if (dump || h->nlmsg_seq == last) { return FIRC_OK; }
                } else if (h->nlmsg_type == NLMSG_DONE) {
                    if (dump) { return FIRC_OK; }
                } else if (fn != NULL) {
                    fn(h, ud);
                }
            }
            h = mnl_nlmsg_next(h, &left);
        }
    }
}

static firc_err_t request(firc_setnl_t *s, int *code, msg_fn fn, void *ud) {
    struct nlmsghdr *nlh = (struct nlmsghdr *)s->out;
    nlh->nlmsg_seq = ++s->seq;
    bool dump = (nlh->nlmsg_flags & NLM_F_DUMP) == NLM_F_DUMP;
    return round_trip(s, nlh->nlmsg_len, nlh->nlmsg_seq, nlh->nlmsg_seq, dump, code, fn, ud);
}

static bool is_cmd(const struct nlmsghdr *h, uint8_t cmd) {
    return h->nlmsg_type == (uint16_t)((SETNL_SUBSYS_IPSET << 8u) | (unsigned)cmd);
}

typedef struct {
    bool got;
    uint8_t rev;
} type_reply_t;

static void on_type(const struct nlmsghdr *h, void *ud) {
    type_reply_t *t = ud;
    if (!is_cmd(h, SETNL_CMD_TYPE)) { return; }
    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct nfgenmsg))) { return; }
    while (firc_nlattr_iter_next(&it, &a)) {
        if (mnl_attr_get_type(a) == SETNL_ATTR_REVISION && mnl_attr_get_payload_len(a) == 1) {
            t->rev = mnl_attr_get_u8(a);
            t->got = true;
        }
    }
}

firc_err_t firc_setnl_probe(firc_setnl_t *s, firc_setnl_caps_t *out) {
    if (s == NULL) { return FIRC_ERR_INVAL; }
    firc_setnl_caps_t caps = {false, 0, 0};
    int code = 0;
    begin(s->out, SETNL_CMD_PROTOCOL, NLM_F_ACK);
    firc_err_t err = request(s, &code, NULL, NULL);
    if (err != FIRC_OK) { return err; }
    if (code == EINVAL) { return FIRC_ERR_NOSYS; }
    if (code != 0) { return from_code(code); }
    const uint8_t families[2] = {SETNL_NFPROTO_IPV4, SETNL_NFPROTO_IPV6};
    uint8_t revs[2] = {0, 0};
    bool has = true;
    for (size_t i = 0; i < 2 && has; i++) {
        struct nlmsghdr *nlh = begin(s->out, SETNL_CMD_TYPE, NLM_F_ACK);
        mnl_attr_put_strz(nlh, SETNL_ATTR_TYPENAME, "hash:net");
        mnl_attr_put_u8(nlh, SETNL_ATTR_FAMILY, families[i]);
        type_reply_t t = {false, 0};
        err = request(s, &code, on_type, &t);
        if (err != FIRC_OK) { return err; }
        if (code == SETNL_ERR_FIND_TYPE) {
            has = false;
        } else if (code != 0) {
            return from_code(code);
        } else if (!t.got) {
            return FIRC_ERR_PROTO;
        }
        revs[i] = t.rev;
    }
    if (has) {
        caps.has_type = true;
        caps.rev4 = revs[0];
        caps.rev6 = revs[1];
    }
    s->caps = caps;
    s->probed = true;
    if (out != NULL) { *out = caps; }
    return FIRC_OK;
}

static firc_err_t destroy_one(firc_setnl_t *s, const char *name) {
    struct nlmsghdr *nlh = begin(s->out, SETNL_CMD_DESTROY, NLM_F_ACK);
    mnl_attr_put_strz(nlh, SETNL_ATTR_SETNAME, name);
    int code = 0;
    firc_err_t err = request(s, &code, NULL, NULL);
    if (err != FIRC_OK) { return err; }
    return code == ENOENT ? FIRC_OK : from_code(code);
}

firc_err_t firc_setnl_destroy(firc_setnl_t *s, const char *name) {
    if (s == NULL || !name_ok(name)) { return FIRC_ERR_INVAL; }
    return destroy_one(s, name);
}

static firc_err_t two_names(firc_setnl_t *s, uint8_t cmd, const char *from, const char *to, int *code) {
    struct nlmsghdr *nlh = begin(s->out, cmd, NLM_F_ACK);
    mnl_attr_put_strz(nlh, SETNL_ATTR_SETNAME, from);
    mnl_attr_put_strz(nlh, SETNL_ATTR_SETNAME2, to);
    return request(s, code, NULL, NULL);
}

static firc_err_t create(firc_setnl_t *s, const char *name, uint8_t family, size_t n) {
    uint32_t maxelem = n > SETNL_MIN_SIZE ? (uint32_t)n : SETNL_MIN_SIZE;
    uint32_t want = n / 2 > SETNL_MIN_SIZE ? (uint32_t)(n / 2) : SETNL_MIN_SIZE;
    uint32_t hashsize = SETNL_MIN_SIZE;
    while (hashsize < want) { hashsize <<= 1u; }
    struct nlmsghdr *nlh = begin(s->out, SETNL_CMD_CREATE, NLM_F_ACK | NLM_F_EXCL);
    mnl_attr_put_strz(nlh, SETNL_ATTR_SETNAME, name);
    mnl_attr_put_strz(nlh, SETNL_ATTR_TYPENAME, "hash:net");
    mnl_attr_put_u8(nlh, SETNL_ATTR_REVISION, family == 4 ? s->caps.rev4 : s->caps.rev6);
    mnl_attr_put_u8(nlh, SETNL_ATTR_FAMILY, family == 4 ? SETNL_NFPROTO_IPV4 : SETNL_NFPROTO_IPV6);
    struct nlattr *data = mnl_attr_nest_start(nlh, SETNL_ATTR_DATA);
    put_be32(nlh, SETNL_ATTR_HASHSIZE, hashsize);
    put_be32(nlh, SETNL_ATTR_MAXELEM, maxelem);
    mnl_attr_nest_end(nlh, data);
    int code = 0;
    firc_err_t err = request(s, &code, NULL, NULL);
    return err != FIRC_OK ? err : from_code(code);
}

static firc_err_t flush_adds(firc_setnl_t *s, size_t len, uint32_t first, struct nlmsghdr *last) {
    last->nlmsg_flags = (uint16_t)(last->nlmsg_flags | NLM_F_ACK);
    int code = 0;
    firc_err_t err = round_trip(s, len, first, last->nlmsg_seq, false, &code, NULL, NULL);
    return err != FIRC_OK ? err : from_code(code);
}

static firc_err_t add_all(firc_setnl_t *s, const char *name, uint8_t family, const firc_nf_prefix_t *v, size_t n) {
    const size_t alen = family == 4 ? 4u : 16u;
    const uint16_t addr_type =
        (uint16_t)((family == 4 ? SETNL_ATTR_IPADDR_IPV4 : SETNL_ATTR_IPADDR_IPV6) | SETNL_F_NET_BYTEORDER);
    size_t off = 0;
    uint32_t first = 0;
    struct nlmsghdr *last = NULL;
    for (size_t i = 0; i < n; i++) {
        if (off + SETNL_MSG_MAX > SETNL_DATAGRAM) {
            firc_err_t err = flush_adds(s, off, first, last);
            if (err != FIRC_OK) { return err; }
            off = 0;
        }
        struct nlmsghdr *nlh = begin(s->out + off, SETNL_CMD_ADD, 0);
        nlh->nlmsg_seq = ++s->seq;
        if (off == 0) { first = nlh->nlmsg_seq; }
        mnl_attr_put_strz(nlh, SETNL_ATTR_SETNAME, name);
        struct nlattr *data = mnl_attr_nest_start(nlh, SETNL_ATTR_DATA);
        struct nlattr *ip = mnl_attr_nest_start(nlh, SETNL_ATTR_IP);
        mnl_attr_put(nlh, addr_type, alen, v[i].addr);
        mnl_attr_nest_end(nlh, ip);
        mnl_attr_put_u8(nlh, SETNL_ATTR_CIDR, v[i].cidr);
        mnl_attr_nest_end(nlh, data);
        off += nlh->nlmsg_len;
        last = nlh;
    }
    return off > 0 ? flush_adds(s, off, first, last) : FIRC_OK;
}

static firc_err_t swap_in(firc_setnl_t *s, const char *live, const char *scratch) {
    int code = 0;
    firc_err_t err = two_names(s, SETNL_CMD_SWAP, scratch, live, &code);
    if (err != FIRC_OK) { return err; }
    if (code == SETNL_ERR_EXIST_SETNAME2) {
        err = two_names(s, SETNL_CMD_RENAME, scratch, live, &code);
        return err != FIRC_OK ? err : from_code(code);
    }
    if (code != 0) { return from_code(code); }
    err = destroy_one(s, scratch);
    if (err != FIRC_OK) {
        FIRC_WARN("ip_set %s is in place; the old members left in %s stay until the next pass: %s", live, scratch,
                  firc_err_str(err));
    }
    return FIRC_OK;
}

firc_err_t firc_setnl_replace(firc_setnl_t *s, const char *live, const char *scratch, uint8_t family,
                              const firc_nf_prefix_t *v, size_t n) {
    if (s == NULL || !name_ok(live) || !name_ok(scratch) || strcmp(live, scratch) == 0 ||
        (family != 4 && family != 6) || (n > 0 && v == NULL) || ((n >> 16) >> 16) != 0) {
        return FIRC_ERR_INVAL;
    }
    const uint8_t most = family == 4 ? 32 : 128;
    for (size_t i = 0; i < n; i++) {
        if (v[i].family != family || v[i].cidr == 0 || v[i].cidr > most) { return FIRC_ERR_INVAL; }
    }
    if (!s->probed) {
        firc_err_t err = firc_setnl_probe(s, NULL);
        if (err != FIRC_OK) { return err; }
    }
    if (!s->caps.has_type) { return FIRC_ERR_NOSYS; }
    firc_err_t err = destroy_one(s, scratch);
    if (err != FIRC_OK) { return err; }
    err = create(s, scratch, family, n);
    if (err == FIRC_OK) { err = add_all(s, scratch, family, v, n); }
    if (err == FIRC_OK) { err = swap_in(s, live, scratch); }
    if (err != FIRC_OK) { (void)destroy_one(s, scratch); }
    return err;
}

typedef struct {
    firc_setnl_name_fn fn;
    void *ud;
} names_ctx_t;

static void on_name(const struct nlmsghdr *h, void *ud) {
    names_ctx_t *c = ud;
    if (!is_cmd(h, SETNL_CMD_LIST)) { return; }
    firc_nlattr_iter_t it;
    const struct nlattr *a;
    if (!firc_nlattr_iter_init_nlmsg(&it, h, sizeof(struct nfgenmsg))) { return; }
    while (firc_nlattr_iter_next(&it, &a)) {
        if (mnl_attr_get_type(a) != SETNL_ATTR_SETNAME) { continue; }
        const char *p = mnl_attr_get_payload(a);
        size_t len = mnl_attr_get_payload_len(a);
        if (strnlen(p, len) < len) { c->fn(p, c->ud); }
    }
}

firc_err_t firc_setnl_names(firc_setnl_t *s, firc_setnl_name_fn fn, void *ud) {
    if (s == NULL || fn == NULL) { return FIRC_ERR_INVAL; }
    struct nlmsghdr *nlh = begin(s->out, SETNL_CMD_LIST, NLM_F_DUMP);
    put_be32(nlh, SETNL_ATTR_FLAGS, SETNL_FLAG_LIST_SETNAME);
    names_ctx_t c = {fn, ud};
    int code = 0;
    firc_err_t err = request(s, &code, on_name, &c);
    return err != FIRC_OK ? err : from_code(code);
}

typedef struct {
    char (*v)[SETNL_MAXNAMELEN];
    size_t n, cap;
    bool no_memory;
} name_list_t;

static void collect(const char *name, void *ud) {
    name_list_t *l = ud;
    if (l->no_memory || strlen(name) >= SETNL_MAXNAMELEN) { return; }
    if (l->n == l->cap) {
        size_t cap = l->cap ? l->cap * 2 : 16;
        char(*v)[SETNL_MAXNAMELEN] = realloc(l->v, cap * sizeof(*v));
        if (v == NULL) {
            l->no_memory = true;
            return;
        }
        l->v = v;
        l->cap = cap;
    }
    memcpy(l->v[l->n++], name, strlen(name) + 1);
}

static bool kept(const char *name, const char *const *keep, size_t n_keep) {
    for (size_t i = 0; i < n_keep; i++) {
        if (keep[i] != NULL && strcmp(keep[i], name) == 0) { return true; }
    }
    return false;
}

firc_err_t firc_setnl_sweep(firc_setnl_t *s, const char *prefix, const char *const *keep, size_t n_keep,
                            size_t *destroyed) {
    if (destroyed != NULL) { *destroyed = 0; }
    if (s == NULL || prefix == NULL || prefix[0] == '\0' || (n_keep > 0 && keep == NULL)) { return FIRC_ERR_INVAL; }
    name_list_t l = {NULL, 0, 0, false};
    firc_err_t err = firc_setnl_names(s, collect, &l);
    if (err == FIRC_OK && l.no_memory) { err = FIRC_ERR_NOMEM; }
    const size_t plen = strlen(prefix);
    firc_err_t rc = err;
    for (size_t i = 0; i < l.n && err == FIRC_OK; i++) {
        if (strncmp(l.v[i], prefix, plen) != 0 || kept(l.v[i], keep, n_keep)) { continue; }
        firc_err_t e = destroy_one(s, l.v[i]);
        if (e == FIRC_ERR_AGAIN) {
            rc = FIRC_ERR_AGAIN;
        } else if (e != FIRC_OK) {
            rc = e;
            break;
        } else if (destroyed != NULL) {
            (*destroyed)++;
        }
    }
    free(l.v);
    return rc;
}
