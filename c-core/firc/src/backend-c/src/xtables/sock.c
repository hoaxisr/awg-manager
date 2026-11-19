#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "xt_internal.h"

#include <errno.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

_Static_assert(sizeof(firc_xt_counter_t) == sizeof(xt_counters_t), "the counters cross the boundary as they are");

typedef struct real_kernel {
    int fd;
    int lock_fd;
} real_kernel_t;

static int level_of(firc_ipt_proto_t fam) { return fam == FIRC_IPT_PROTO_IPV6 ? XT_SOL_IPV6 : XT_SOL_IP; }

static void put_name(char *dst, const char *table) {
    memset(dst, 0, XT_TABLE_LEN);
    memcpy(dst, table, strnlen(table, XT_TABLE_LEN - 1));
}

static int real_get_info(void *ud, firc_ipt_proto_t fam, const char *table, firc_xt_info_t *out) {
    real_kernel_t *k = ud;
    xt_getinfo_t g;
    memset(&g, 0, sizeof(g));
    put_name(g.name, table);
    socklen_t len = sizeof(g);
    if (getsockopt(k->fd, level_of(fam), XT_SO_GET_INFO, &g, &len) != 0) { return errno; }
    out->valid_hooks = g.valid_hooks;
    memcpy(out->hook_entry, g.hook_entry, sizeof(out->hook_entry));
    memcpy(out->underflow, g.underflow, sizeof(out->underflow));
    out->num_entries = g.num_entries;
    out->size = g.size;
    return 0;
}

_Static_assert(FIRC_XT_HEADROOM >= XT_GET_ENTRIES_HDR_LEN && FIRC_XT_HEADROOM >= XT_REPLACE_HDR_LEN,
               "the headroom holds either header");

static int real_get_entries(void *ud, firc_ipt_proto_t fam, const char *table, uint8_t *blob, uint32_t size) {
    real_kernel_t *k = ud;
    uint8_t *buf = blob - XT_GET_ENTRIES_HDR_LEN;
    xt_get_entries_hdr_t h;
    memset(&h, 0, sizeof(h));
    put_name(h.name, table);
    h.size = size;
    memset(buf, 0, XT_GET_ENTRIES_HDR_LEN);
    memcpy(buf, &h, sizeof(h));
    socklen_t sl = (socklen_t)(XT_GET_ENTRIES_HDR_LEN + (size_t)size);
    return getsockopt(k->fd, level_of(fam), XT_SO_GET_ENTRIES, buf, &sl) != 0 ? errno : 0;
}

static int real_replace(void *ud, firc_ipt_proto_t fam, const char *table, const firc_xt_info_t *info,
                        uint8_t *blob, uint32_t num_counters, firc_xt_counter_t *old) {
    real_kernel_t *k = ud;
    size_t len = XT_REPLACE_HDR_LEN + (size_t)info->size;
    uint8_t *buf = blob - XT_REPLACE_HDR_LEN;
    xt_replace_hdr_t h;
    memset(&h, 0, sizeof(h));
    put_name(h.name, table);
    h.valid_hooks = info->valid_hooks;
    h.num_entries = info->num_entries;
    h.size = info->size;
    memcpy(h.hook_entry, info->hook_entry, sizeof(h.hook_entry));
    memcpy(h.underflow, info->underflow, sizeof(h.underflow));
    h.num_counters = num_counters;
    h.counters = (xt_counters_t *)(void *)old;
    memset(buf, 0, XT_REPLACE_HDR_LEN);
    memcpy(buf, &h, sizeof(h));
    return setsockopt(k->fd, level_of(fam), XT_SO_SET_REPLACE, buf, (socklen_t)len) != 0 ? errno : 0;
}

static int real_add_counters(void *ud, firc_ipt_proto_t fam, const char *table, const firc_xt_counter_t *c,
                             uint32_t n) {
    real_kernel_t *k = ud;
    size_t len = XT_COUNTERS_INFO_HDR_LEN + (size_t)n * sizeof(xt_counters_t);
    uint8_t *buf = calloc(1, len);
    if (buf == NULL) { return ENOMEM; }
    xt_counters_info_hdr_t h;
    memset(&h, 0, sizeof(h));
    put_name(h.name, table);
    h.num_counters = n;
    memcpy(buf, &h, sizeof(h));
    if (n > 0) { memcpy(buf + XT_COUNTERS_INFO_HDR_LEN, c, (size_t)n * sizeof(xt_counters_t)); }
    int err = setsockopt(k->fd, level_of(fam), XT_SO_SET_ADD_COUNTERS, buf, (socklen_t)len) != 0 ? errno : 0;
    free(buf);
    return err;
}

static int real_get_revision(void *ud, firc_ipt_proto_t fam, bool target, const char *name, uint8_t revision) {
    real_kernel_t *k = ud;
    xt_get_revision_t r;
    memset(&r, 0, sizeof(r));
    memcpy(r.name, name, strnlen(name, XT_NAME_LEN - 1));
    r.revision = revision;
    socklen_t len = sizeof(r);
    int opt = fam == FIRC_IPT_PROTO_IPV6 ? (target ? XT_SO_GET_REVISION_TARGET6 : XT_SO_GET_REVISION_MATCH6)
                                         : (target ? XT_SO_GET_REVISION_TARGET4 : XT_SO_GET_REVISION_MATCH4);
    return getsockopt(k->fd, level_of(fam), opt, &r, &len) < 0 ? errno : 0;
}

static long ms_between(const struct timespec *a, const struct timespec *b) {
    return (long)(b->tv_sec - a->tv_sec) * 1000L + (b->tv_nsec - a->tv_nsec) / 1000000L;
}

int firc_xt_sock_lock(int *fd, unsigned wait_ms) {
    *fd = -1;
    struct sockaddr_un a;
    memset(&a, 0, sizeof(a));
    a.sun_family = AF_UNIX;
    memcpy(a.sun_path + 1, "xtables", 7);
    socklen_t alen = (socklen_t)(offsetof(struct sockaddr_un, sun_path) + 8);
    int s = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (s < 0) { return errno; }
    struct timespec t0;
    clock_gettime(CLOCK_MONOTONIC, &t0);
    for (;;) {
        if (bind(s, (struct sockaddr *)&a, alen) == 0) {
            *fd = s;
            return 0;
        }
        int e = errno;
        struct timespec now;
        clock_gettime(CLOCK_MONOTONIC, &now);
        if (e != EADDRINUSE || ms_between(&t0, &now) >= (long)wait_ms) {
            close(s);
            return e == EADDRINUSE ? EAGAIN : e;
        }
        struct timespec pause = {0, 10L * 1000L * 1000L};
        nanosleep(&pause, NULL);
    }
}

void firc_xt_sock_unlock(int *fd) {
    if (*fd >= 0) { close(*fd); }
    *fd = -1;
}

static int real_lock(void *ud, unsigned wait_ms) { return firc_xt_sock_lock(&((real_kernel_t *)ud)->lock_fd, wait_ms); }
static void real_unlock(void *ud) { firc_xt_sock_unlock(&((real_kernel_t *)ud)->lock_fd); }

static void real_destroy(void *ud) {
    real_kernel_t *k = ud;
    firc_xt_sock_unlock(&k->lock_fd);
    close(k->fd);
    free(k);
}

static int real_set_index(void *ud, const char *name, uint16_t *index) {
    (void)ud;
    int fd = socket(AF_INET, SOCK_RAW | SOCK_CLOEXEC, IPPROTO_RAW);
    if (fd < 0) { return errno; }
    xt_ip_set_req_version_t v;
    memset(&v, 0, sizeof(v));
    v.op = XT_IP_SET_OP_VERSION;
    socklen_t len = sizeof(v);
    int err = getsockopt(fd, XT_SOL_IP, XT_SO_IP_SET, &v, &len) != 0 ? errno : 0;
    xt_ip_set_req_get_set_t g;
    memset(&g, 0, sizeof(g));
    g.op = XT_IP_SET_OP_GET_BYNAME;
    g.version = v.version;
    memcpy(g.set.name, name, strnlen(name, XT_IPSET_MAXNAMELEN - 1));
    if (err == 0) {
        len = sizeof(g);
        err = getsockopt(fd, XT_SOL_IP, XT_SO_IP_SET, &g, &len) != 0 ? errno : 0;
    }
    close(fd);
    if (err != 0) { return err; }
    if (g.set.index == XT_IPSET_INVALID_ID) { return ENOENT; }
    *index = g.set.index;
    return 0;
}

static const firc_xt_kernel_ops_t k_real_ops = {real_get_info,     real_get_entries, real_replace, real_add_counters,
                                                real_get_revision, real_lock,        real_unlock,  real_destroy,
                                                real_set_index};

firc_xt_t *firc_xt_real_new(firc_ipt_proto_t fam) {
    real_kernel_t *k = calloc(1, sizeof(*k));
    if (k == NULL) { return NULL; }
    k->lock_fd = -1;
    k->fd = socket(fam == FIRC_IPT_PROTO_IPV6 ? AF_INET6 : AF_INET, SOCK_RAW | SOCK_CLOEXEC, IPPROTO_RAW);
    if (k->fd < 0) {
        int e = errno;
        free(k);
        errno = e;
        return NULL;
    }
    firc_xt_t *xt = firc_xt_new(fam, &k_real_ops, k);
    if (xt == NULL) {
        close(k->fd);
        free(k);
        errno = ENOMEM;
    }
    return xt;
}
