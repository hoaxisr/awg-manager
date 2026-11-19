#ifndef FIRC_TAPWINDOW_H
#define FIRC_TAPWINDOW_H

#include <stdbool.h>

#include "firc/nflogsock.h"
#include "firc/tap.h"
#include "firc/tapreport.h"

/* One socket per NFLOG group, so the drain knows which rule a packet came from. */
typedef struct firc_tap_drain {
    firc_tap_ctx_t ctx;
    firc_tap_report_t *report;
    int which;            /* 0: new-connection socket, 1: hello socket */
    int64_t (*now)(void); /* wall clock, injectable for tests; NULL: time() */
} firc_tap_drain_t;

/* Anything but FIRC_OK/FIRC_ERR_AGAIN back means the reader itself is broken. */
firc_err_t firc_tap_drain(firc_tap_drain_t *w, firc_nflog_t *n, size_t *out_events);

#endif /* FIRC_TAPWINDOW_H */
