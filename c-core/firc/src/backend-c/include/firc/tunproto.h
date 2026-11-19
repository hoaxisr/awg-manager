#ifndef FIRC_TUNPROTO_H
#define FIRC_TUNPROTO_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/log.h"

#define FIRC_TUN_LINE_MAX 1024

typedef struct {
    char buf[FIRC_TUN_LINE_MAX];
    size_t len;
    bool dropping;
} firc_tun_lines_t;

typedef void (*firc_tun_line_fn)(const char *line, size_t len, void *ud);

/* Splits a byte stream into lines; returns how many oversize lines were dropped in this call. */
size_t firc_tun_lines_feed(firc_tun_lines_t *l, const char *data, size_t n, firc_tun_line_fn fn, void *ud);

/* Level of a tunvless stderr line from its leading tunvless[<level>] tag, INFO without one;
   *skip is where the text after the tag starts, 0 when there is none. */
firc_log_level_t firc_tun_err_level(const char *line, size_t len, size_t *skip);

typedef enum {
    FIRC_TEV_READY,
    FIRC_TEV_ACTIVE,
    FIRC_TEV_NODE_DOWN,
    FIRC_TEV_NODE_UP,
    FIRC_TEV_NO_NODE,
    FIRC_TEV_NODES,
    FIRC_TEV_PINS_FULL,
    FIRC_TEV_MAP,
    FIRC_TEV_CLOSE,
    FIRC_TEV_MISS,
    FIRC_TEV_BYTES,
    FIRC_TEV_INBOUND,
    FIRC_TEV_IBYTES,
    FIRC_TEV_INBOUNDS,
    FIRC_TEV_BAD
} firc_tev_kind_t;

/* index and active_index are tunvless's node numbers, -1 when absent; pos and active_pos are the
   positions in the list last given for the tunnel, filled by tunsup, -1 when unknown. For a map ack,
   tunsup fills expect with the line count sent and sets drift when count differs from it. */
typedef struct {
    firc_tev_kind_t kind;
    char dev[16];
    char node[128];
    char why[160];
    int retry_s;
    int count;
    int index;
    int pos;
    char active[8][128];
    int active_index[8];
    int active_pos[8];
    bool active_v6[8];
    size_t n_active;
    uint16_t tcp_port;
    uint16_t ib_port;
    char istate[16];
    uint32_t rx, tx;
    uint8_t fake[4];
    int expect;
    bool drift;
} firc_tev_t;

/* Parses one line; kind is FIRC_TEV_BAD when it is malformed. Never reads past len. */
void firc_tun_event_parse(const char *line, size_t len, firc_tev_t *out);

#endif
