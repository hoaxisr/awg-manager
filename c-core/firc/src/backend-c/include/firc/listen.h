#ifndef FIRC_LISTEN_H
#define FIRC_LISTEN_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/socket.h>

/* Parses addr (IPv4/IPv6 literal, brackets ok) and port; empty_is_any makes "" and "[]" mean [::]. */
bool firc_listen_addr(const char *addr, uint16_t port, bool empty_is_any,
                      struct sockaddr_storage *sa, socklen_t *sa_len);

enum {
    /* IPV6_V6ONLY off (DNS proxy). */
    FIRC_LISTEN_DUAL_STACK = 1u << 0,
    /* IP_PKTINFO / IPV6_RECVPKTINFO (DNS proxy UDP). */
    FIRC_LISTEN_PKTINFO = 1u << 1,
};

#define FIRC_WEB_LISTEN 0u
#define FIRC_DNS_UDP_LISTEN (FIRC_LISTEN_DUAL_STACK | FIRC_LISTEN_PKTINFO)
#define FIRC_DNS_TCP_LISTEN FIRC_LISTEN_DUAL_STACK

/* Nonblocking close-on-exec socket bound to sa (SO_REUSEADDR, backlog 128 for streams); fd or -errno.
   [::] on a kernel without IPv6 binds 0.0.0.0 on the same port instead, with a WARN naming who (NULL: silent);
   on failure why (if not NULL) gets firc_listen_describe's line for the address last tried, naming the
   process that holds the port when /proc shows one. */
int firc_listen_open(const char *who, int type, const struct sockaddr_storage *sa, socklen_t sa_len,
                     unsigned opts, char *why, size_t why_cap);

typedef struct firc_listen_holder {
    long pid;
    char name[32];
} firc_listen_holder_t;

/* The first process /proc shows with a socket on port in type's tables (both families); false if none. */
bool firc_listen_holder(int type, uint16_t port, firc_listen_holder_t *out);

typedef struct firc_listen_seen {
    int err;
    bool held;
    firc_listen_holder_t holder;
} firc_listen_seen_t;

/* firc_listen_open remembering the last failure in seen: /proc is walked only when the errno differs from it. */
int firc_listen_open_seen(const char *who, int type, const struct sockaddr_storage *sa, socklen_t sa_len,
                          unsigned opts, char *why, size_t why_cap, firc_listen_seen_t *seen);

/* "<addr>:<port>/<udp|tcp>" of what fd is bound to; IPv6 in brackets. */
void firc_listen_name(int fd, int type, char *out, size_t cap);

/* "<addr>:<port>/<udp|tcp>: <reason>" for a listener that failed with errno err; IPv6 gets brackets.
   holder (may be NULL) names who has the port, said in place of the hint when err is EADDRINUSE. */
void firc_listen_describe(char *out, size_t cap, const char *addr, uint16_t port, int type, int err,
                          const char *holder);

#define FIRC_WEB_PORTS_MAX 6

/* WebUI ports to try in order: configured first, then the fallbacks other than it and skip; returns the count */
size_t firc_web_ports(uint16_t configured, uint16_t skip, uint16_t out[FIRC_WEB_PORTS_MAX]);

#endif
