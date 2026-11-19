#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/listen.h"

#include <arpa/inet.h>
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/log.h"

bool firc_listen_addr(const char *addr, uint16_t port, bool empty_is_any,
                      struct sockaddr_storage *sa, socklen_t *sa_len)
{
    char buf[128];
    snprintf(buf, sizeof(buf), "%s", addr != NULL ? addr : "");
    char *host = buf;
    size_t n = strlen(host);
    if (n >= 2 && host[0] == '[' && host[n - 1] == ']') {
        host[n - 1] = '\0';
        host++;
    }

    memset(sa, 0, sizeof(*sa));
    if (strchr(host, ':') != NULL || (empty_is_any && host[0] == '\0')) {
        struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)sa;
        s6->sin6_family = AF_INET6;
        s6->sin6_port = htons(port);
        if (host[0] == '\0') {
            s6->sin6_addr = in6addr_any;
        } else if (inet_pton(AF_INET6, host, &s6->sin6_addr) != 1) {
            return false;
        }
        *sa_len = sizeof(*s6);
        return true;
    }
    struct sockaddr_in *s4 = (struct sockaddr_in *)sa;
    s4->sin_family = AF_INET;
    s4->sin_port = htons(port);
    if (inet_pton(AF_INET, host, &s4->sin_addr) != 1) { return false; }
    *sa_len = sizeof(*s4);
    return true;
}

static const char *proto_name(int type)
{
    return type == SOCK_DGRAM ? "udp" : "tcp";
}

static bool any6(const struct sockaddr_storage *sa)
{
    const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)sa;
    return sa->ss_family == AF_INET6 && memcmp(&s6->sin6_addr, &in6addr_any, sizeof(in6addr_any)) == 0;
}

static int open_one(int type, const struct sockaddr_storage *sa, socklen_t sa_len, unsigned opts);

static size_t port_inodes(const char *table, uint16_t port, unsigned long *out, size_t n, size_t cap)
{
    FILE *f = fopen(table, "re");
    if (f == NULL) { return n; }
    char line[512];
    if (fgets(line, sizeof(line), f) == NULL) {
        fclose(f);
        return n;
    }
    while (n < cap && fgets(line, sizeof(line), f) != NULL) {
        unsigned local = 0;
        unsigned long inode = 0;
        if (sscanf(line, " %*u: %*[0-9A-Fa-f]:%x %*[0-9A-Fa-f]:%*x %*x %*x:%*x %*x:%*x %*x %*u %*u %lu", &local,
                   &inode) == 2 &&
            local == port && inode != 0) {
            out[n++] = inode;
        }
    }
    fclose(f);
    return n;
}

static bool pid_holds(const char *pid, const unsigned long *inodes, size_t n)
{
    char path[300];
    snprintf(path, sizeof(path), "/proc/%s/fd", pid);
    DIR *d = opendir(path);
    if (d == NULL) { return false; }
    bool found = false;
    struct dirent *e;
    while (!found && (e = readdir(d)) != NULL) {
        char target[64];
        ssize_t len = readlinkat(dirfd(d), e->d_name, target, sizeof(target) - 1);
        if (len <= 0) { continue; }
        target[len] = '\0';
        unsigned long inode = 0;
        char end = 0;
        if (sscanf(target, "socket:[%lu%c", &inode, &end) != 2 || end != ']') { continue; }
        for (size_t i = 0; i < n && !found; i++) { found = inodes[i] == inode; }
    }
    closedir(d);
    return found;
}

bool firc_listen_holder(int type, uint16_t port, firc_listen_holder_t *out)
{
    unsigned long inodes[32];
    size_t n = 0;
    n = port_inodes(type == SOCK_DGRAM ? "/proc/net/udp" : "/proc/net/tcp", port, inodes, n, 32);
    n = port_inodes(type == SOCK_DGRAM ? "/proc/net/udp6" : "/proc/net/tcp6", port, inodes, n, 32);
    if (n == 0) { return false; }
    DIR *d = opendir("/proc");
    if (d == NULL) { return false; }
    bool found = false;
    struct dirent *e;
    while (!found && (e = readdir(d)) != NULL) {
        if (e->d_name[0] < '0' || e->d_name[0] > '9' || strspn(e->d_name, "0123456789") != strlen(e->d_name)) {
            continue;
        }
        if (!pid_holds(e->d_name, inodes, n)) { continue; }
        char path[300];
        char comm[sizeof(out->name)] = "";
        snprintf(path, sizeof(path), "/proc/%s/comm", e->d_name);
        FILE *f = fopen(path, "re");
        if (f != NULL) {
            if (fgets(comm, sizeof(comm), f) == NULL) { comm[0] = '\0'; }
            fclose(f);
        }
        comm[strcspn(comm, "\n")] = '\0';
        snprintf(out->name, sizeof(out->name), "%s", comm[0] != '\0' ? comm : "?");
        out->pid = strtol(e->d_name, NULL, 10);
        found = true;
    }
    closedir(d);
    return found;
}

static void describe_sa(char *out, size_t cap, const struct sockaddr_storage *sa, int type, int err,
                        firc_listen_seen_t *seen)
{
    char host[INET6_ADDRSTRLEN] = "";
    uint16_t port = 0;
    if (sa->ss_family == AF_INET6) {
        const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)sa;
        inet_ntop(AF_INET6, &s6->sin6_addr, host, sizeof(host));
        port = ntohs(s6->sin6_port);
    } else {
        const struct sockaddr_in *s4 = (const struct sockaddr_in *)sa;
        inet_ntop(AF_INET, &s4->sin_addr, host, sizeof(host));
        port = ntohs(s4->sin_port);
    }
    if (seen->err != err) {
        seen->err = err;
        seen->held = err == EADDRINUSE && firc_listen_holder(type, port, &seen->holder);
    }
    char holder[64];
    if (seen->held) { snprintf(holder, sizeof(holder), "%s (pid %ld)", seen->holder.name, seen->holder.pid); }
    firc_listen_describe(out, cap, host, port, type, err, seen->held ? holder : NULL);
}

static int open_any(const char *who, int type, const struct sockaddr_storage *sa, socklen_t sa_len,
                    unsigned opts, struct sockaddr_storage *tried)
{
    *tried = *sa;
    int fd = open_one(type, sa, sa_len, opts);
    if (fd != -EAFNOSUPPORT || !any6(sa)) { return fd; }
    uint16_t port = ntohs(((const struct sockaddr_in6 *)sa)->sin6_port);
    struct sockaddr_storage v4;
    memset(&v4, 0, sizeof(v4));
    struct sockaddr_in *s4 = (struct sockaddr_in *)&v4;
    s4->sin_family = AF_INET;
    s4->sin_port = htons(port);
    s4->sin_addr.s_addr = htonl(INADDR_ANY);
    fd = open_one(type, &v4, sizeof(*s4), opts);
    if (fd >= 0 && who != NULL) {
        FIRC_WARN("%s: IPv6 is not available on this router; listening on 0.0.0.0:%u/%s instead of [::]:%u", who,
                  port, proto_name(type), port);
    }
    *tried = v4;
    return fd;
}

int firc_listen_open_seen(const char *who, int type, const struct sockaddr_storage *sa, socklen_t sa_len,
                          unsigned opts, char *why, size_t why_cap, firc_listen_seen_t *seen)
{
    struct sockaddr_storage tried;
    int fd = open_any(who, type, sa, sa_len, opts, &tried);
    if (fd >= 0) {
        memset(seen, 0, sizeof(*seen));
    } else if (why != NULL) {
        describe_sa(why, why_cap, &tried, type, -fd, seen);
    }
    return fd;
}

int firc_listen_open(const char *who, int type, const struct sockaddr_storage *sa, socklen_t sa_len,
                     unsigned opts, char *why, size_t why_cap)
{
    firc_listen_seen_t seen = {0};
    return firc_listen_open_seen(who, type, sa, sa_len, opts, why, why_cap, &seen);
}

void firc_listen_name(int fd, int type, char *out, size_t cap)
{
    struct sockaddr_storage sa;
    socklen_t len = sizeof(sa);
    memset(&sa, 0, sizeof(sa));
    if (getsockname(fd, (struct sockaddr *)&sa, &len) != 0) {
        snprintf(out, cap, "?/%s", proto_name(type));
        return;
    }
    char host[INET6_ADDRSTRLEN] = "";
    uint16_t port;
    if (sa.ss_family == AF_INET6) {
        const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)&sa;
        inet_ntop(AF_INET6, &s6->sin6_addr, host, sizeof(host));
        port = ntohs(s6->sin6_port);
        snprintf(out, cap, "[%s]:%u/%s", host, port, proto_name(type));
    } else {
        const struct sockaddr_in *s4 = (const struct sockaddr_in *)&sa;
        inet_ntop(AF_INET, &s4->sin_addr, host, sizeof(host));
        port = ntohs(s4->sin_port);
        snprintf(out, cap, "%s:%u/%s", host, port, proto_name(type));
    }
}

void firc_listen_describe(char *out, size_t cap, const char *addr, uint16_t port, int type, int err,
                          const char *holder)
{
    const char *a = addr != NULL ? addr : "";
    bool bare6 = strchr(a, ':') != NULL && a[0] != '[';
    char reason[128];
    switch (err) {
    case EADDRINUSE:
        if (holder != NULL) {
            snprintf(reason, sizeof(reason), "address already in use by %s", holder);
        } else {
            snprintf(reason, sizeof(reason), "address already in use (is another program using this port?)");
        }
        break;
    case EADDRNOTAVAIL:
        snprintf(reason, sizeof(reason), "address not available (not an address of this router)");
        break;
    case EAFNOSUPPORT:
        snprintf(reason, sizeof(reason), "address family not supported (is IPv6 off on this router?)");
        break;
    default:
        snprintf(reason, sizeof(reason), "%s", strerror(err));
        if (reason[0] >= 'A' && reason[0] <= 'Z') { reason[0] = (char)(reason[0] - 'A' + 'a'); }
        break;
    }
    snprintf(out, cap, "%s%s%s:%u/%s: %s", bare6 ? "[" : "", a, bare6 ? "]" : "", port, proto_name(type), reason);
}

static int open_one(int type, const struct sockaddr_storage *sa, socklen_t sa_len, unsigned opts)
{
    int fd = socket(sa->ss_family, type | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd < 0) { return -errno; }
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    if ((opts & FIRC_LISTEN_DUAL_STACK) != 0 && sa->ss_family == AF_INET6) {
        int zero = 0;
        setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &zero, sizeof(zero));
    }
    if ((opts & FIRC_LISTEN_PKTINFO) != 0) {
        if (sa->ss_family == AF_INET6) {
            setsockopt(fd, IPPROTO_IPV6, IPV6_RECVPKTINFO, &one, sizeof(one));
        }
        setsockopt(fd, IPPROTO_IP, IP_PKTINFO, &one, sizeof(one));
    }
    if (bind(fd, (const struct sockaddr *)sa, sa_len) != 0 ||
        (type == SOCK_STREAM && listen(fd, 128) != 0)) {
        int e = errno;
        close(fd);
        return -e;
    }
    return fd;
}

size_t firc_web_ports(uint16_t configured, uint16_t skip, uint16_t out[FIRC_WEB_PORTS_MAX]) {
    static const uint16_t fallback[] = {666, 999, 1666, 2666, 9999};
    size_t n = 0;
    out[n++] = configured;
    for (size_t i = 0; i < sizeof(fallback) / sizeof(fallback[0]); i++) {
        if (fallback[i] != configured && fallback[i] != skip) { out[n++] = fallback[i]; }
    }
    return n;
}
