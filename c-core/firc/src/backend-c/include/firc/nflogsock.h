#ifndef FIRC_NFLOGSOCK_H
#define FIRC_NFLOGSOCK_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/nflog.h"

typedef struct firc_nflog firc_nflog_t;

#define FIRC_NFLOG_MAX_RANGE 4096u

/* The privacy control: deliberately smaller than the ceiling above. */
#define FIRC_TAP_COPY_RANGE 2048u

#define FIRC_NFLOG_BUF (8u * 1024u)

/* Opens NETLINK_NETFILTER, binds group, asks for copy_range bytes of each
 * packet (1..FIRC_NFLOG_MAX_RANGE). NULL on a bad range or a bind the kernel refuses. */
firc_nflog_t *firc_nflog_open(uint16_t group, uint16_t copy_range);

/* The same over an already-open socket, for tests; takes ownership of fd either way. */
firc_nflog_t *firc_nflog_open_fd(int fd, uint16_t group, uint16_t copy_range);

int firc_nflog_fd(const firc_nflog_t *n);

/* FIRC_ERR_LIMIT: packets were lost or too big; cb is still called for whatever did arrive. */
firc_err_t firc_nflog_read(firc_nflog_t *n, void (*cb)(const uint8_t *pkt, size_t len, void *ud),
                           void *ud);

void firc_nflog_close(firc_nflog_t *n);

#endif /* FIRC_NFLOGSOCK_H */
