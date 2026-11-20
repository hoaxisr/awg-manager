#ifndef TUNVLESS_FMSD_H
#define TUNVLESS_FMSD_H
#include <stddef.h>
#include <stdint.h>
#include "fmask.h"

enum { SD_PAD = 0x40, SD_MARK = 0x41, SD_GRIDS = 288 };

struct sd_layout { uint8_t marker, pool_n, pool[44], hint[64], cls[256]; };
struct sd_set { size_t n; const struct sd_layout *lay; uint16_t order[256]; };
struct sd_base { const uint32_t *off; const uint8_t (*g)[4]; };

const struct sd_base *sd_base(void);
int sd_layout_make(struct sd_layout *l, int ascii, const char *pattern);
int sd_order(const unsigned char *pw, size_t pw_n, uint16_t order[SD_GRIDS], int64_t *seed);
const struct sd_set *sd_tables(const struct fm_sd *c);

struct sd_rng { unsigned char key[32], buf[256]; uint32_t ctr; uint16_t pos; };
int sd_rng_init(struct sd_rng *r);
unsigned sd_rng_below(struct sd_rng *r, unsigned n);

struct sd_enc { const struct sd_set *s; struct sd_rng rng; unsigned chance; size_t ti; };
int sd_enc_init(struct sd_enc *e, const struct sd_set *s, uint32_t pmin, uint32_t pmax);
size_t sd_enc_bound(const struct sd_enc *e, size_t n);
size_t sd_encode(struct sd_enc *e, const unsigned char *in, size_t n, unsigned char *out);

struct sd_dec { const struct sd_set *s; size_t gi; uint32_t bits; unsigned nbits; };
size_t sd_unpack(struct sd_dec *d, unsigned char *buf, size_t n);

#endif
