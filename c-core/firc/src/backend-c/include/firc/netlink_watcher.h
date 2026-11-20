#ifndef FIRC_NETLINK_WATCHER_H
#define FIRC_NETLINK_WATCHER_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/loop.h"

typedef struct firc_nl_watcher firc_nl_watcher_t;

typedef void (*firc_nl_link_cb)(const char *iface_name, bool up, void *ud);
typedef void (*firc_nl_addr_cb)(const char *iface_name, void *ud);
/* Messages were lost: the listener must re-read the state it cares about. */
typedef void (*firc_nl_lost_cb)(void *ud);

typedef enum {
    FIRC_NL_RECV_DRAINED,
    FIRC_NL_RECV_RETRY,
    FIRC_NL_RECV_LOST,
    FIRC_NL_RECV_FATAL,
} firc_nl_recv_verdict_t;

firc_nl_recv_verdict_t firc_nl_watcher_recv_verdict(int err);

/* Acts on the verdict, a fatal one by reopening the socket and then telling lost_cb; true when reading should go on. */
bool firc_nl_watcher_handle_recv_error(firc_nl_watcher_t *w, int err);

/* Wait before a reopen after `failures` failures since the last read: 100 ms, doubling, at most 5 s. */
uint32_t firc_nl_watcher_reopen_delay_ms(unsigned failures);

#define FIRC_NL_WATCHER_MAX_NEXTHOPS 8

/* Interfaces a default-route message names (every ECMP leg); 0 for other routes and firc's own. */
struct nlmsghdr;
size_t firc_nl_watcher_default_route_oifs(const struct nlmsghdr *h, int *out, size_t max);

/* The interface an address message (added or removed) is about; 0 for any other message. */
int firc_nl_watcher_addr_ifindex(const struct nlmsghdr *h);

/* Name and state of a link message: NEWLINK is up when IFF_UP is set; DELLINK is always down; false for any other
 * message or one without a name. */
bool firc_nl_watcher_link_event(const struct nlmsghdr *h, char *name, size_t cap, bool *up);

/* Watches link, address and default-route events; links dispatch up and down. loop borrowed; NULL callbacks ignore
 * that event class. */
firc_err_t firc_nl_watcher_create(firc_loop_t *loop, firc_nl_link_cb link_cb, void *link_ud,
                                  firc_nl_addr_cb addr_cb, void *addr_ud, firc_nl_lost_cb lost_cb,
                                  void *lost_ud, firc_nl_watcher_t **out);
void firc_nl_watcher_destroy(firc_nl_watcher_t *w);

/* The netlink socket (for tests), or -1. */
int firc_nl_watcher_fd(const firc_nl_watcher_t *w);

#endif /* FIRC_NETLINK_WATCHER_H */
