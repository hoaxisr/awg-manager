#ifndef FIRC_KTCP_H
#define FIRC_KTCP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip_addr.h"
#include "firc/ipset_to_link.h"

#define FIRC_KTCP_DEVICES 100
#define FIRC_KTCP_NAME_CAP 16
#define FIRC_KTCP_MAP_MAX 65536
#define FIRC_KTCP_CMD_LINES 4096

typedef struct {
    uint8_t dev;
    uint8_t fake[4];
    uint8_t real[16];
    uint8_t real_len;
} firc_ktcp_pair_t;

typedef struct {
    firc_ktcp_pair_t *v;
    size_t n, cap;
} firc_ktcp_image_t;

firc_err_t firc_ktcp_image_add(firc_ktcp_image_t *im, uint8_t dev, const uint8_t fake[4], const firc_ip_t *real);

/* Sorts by (dev, fake) and keeps one pair per key; a later duplicate wins. */
void firc_ktcp_image_sort(firc_ktcp_image_t *im);

void firc_ktcp_image_free(firc_ktcp_image_t *im);

/* Calls fn(dev, fake, real) per pair that entered or changed, real NULL per pair that left; ordered by
   (dev, fake). Both images must be sorted. */
void firc_ktcp_image_diff(const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next,
                          void (*fn)(uint8_t dev, const uint8_t fake[4], const firc_ip_t *real, void *ud), void *ud);

/* Commands for tunvless's stdin, each ended by an empty line; counts[i] is the line count of command i. */
typedef struct {
    char *text;
    size_t len;
    size_t *counts;
    size_t n_cmds;
} firc_ktcp_cmds_t;

/* Appends dev's pairs of a sorted image as commands of at most FIRC_KTCP_CMD_LINES lines; the first is
   "mapall" when all is set, sent even with no pairs. On failure out is left as it was. */
firc_err_t firc_ktcp_map_cmds(const firc_ktcp_image_t *im, uint8_t dev, bool all, firc_ktcp_cmds_t *out);

/* Appends one delta line ("-" when real is NULL) to the last command, opening a "map" command when there
   is none or it is full; keep delta lines in a cmds of their own. On failure out is left as it was. */
firc_err_t firc_ktcp_line(firc_ktcp_cmds_t *out, const uint8_t fake[4], const firc_ip_t *real);

/* Appends "close" commands listing the v4 prefixes of p; nothing when there is none. On failure out is left
   as it was. */
firc_err_t firc_ktcp_close_cmd(const firc_nf_prefix_t *p, size_t n, firc_ktcp_cmds_t *out);

void firc_ktcp_cmds_free(firc_ktcp_cmds_t *c);

#endif
