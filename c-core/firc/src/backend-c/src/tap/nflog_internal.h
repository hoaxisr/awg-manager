#ifndef FIRC_TAP_NFLOG_INTERNAL_H
#define FIRC_TAP_NFLOG_INTERNAL_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"

/* Classifies a failed recv; the fake-kernel test socket cannot produce ENOBUFS itself. */
firc_err_t firc_nflog_recv_error(int errnum);

/* True for the kernel's own datagrams (port id 0) and non-netlink test addresses. */
bool firc_nflog_from_the_kernel(const void *addr, size_t addr_len);

#endif /* FIRC_TAP_NFLOG_INTERNAL_H */
