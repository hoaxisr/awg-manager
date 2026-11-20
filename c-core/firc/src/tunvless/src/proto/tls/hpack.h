#ifndef STEER_HPACK_H
#define STEER_HPACK_H
#include <stddef.h>
#include <stdint.h>

#include "h2.h"

#define HPE_TABLE 4096
#define HPE_ENTS (HPE_TABLE / 32)

struct hpe_ent { uint16_t off, nl, vl; };

struct hpe {
    uint32_t max, min, size;
    uint16_t n;
    uint8_t pending;
    struct hpe_ent ent[HPE_ENTS];
    unsigned char data[HPE_TABLE];
};

void   hpe_init(struct hpe *e);
void   hpe_set_max(struct hpe *e, uint32_t v);
size_t hpe_bound(const struct h2_field *f, size_t n);
size_t hpe_block(struct hpe *e, const struct h2_field *f, size_t n, unsigned char *out, size_t cap);
size_t hpe_huff_len(const char *s, size_t n);
size_t hpe_huff(unsigned char *out, const char *s, size_t n);
int    hpack_huff_decode(const uint8_t *in, size_t n, uint8_t *out, size_t cap);

#endif
