#ifndef FIRC_FAKEIP_ADDR_H
#define FIRC_FAKEIP_ADDR_H

#include <stdbool.h>
#include <stdint.h>

typedef struct firc_ip {
    uint8_t b[16];
    uint8_t len; /* 4 or 16 */
} firc_ip_t;

/* a += delta, wrapping silently; use firc_ip_add_shifted to detect overflow. */
void firc_ip_add(firc_ip_t *a, uint64_t delta);

/* a += count << shift_bits, wrapping; returns false and leaves a unchanged on overflow. */
bool firc_ip_add_shifted(firc_ip_t *a, uint64_t count, uint8_t shift_bits);

/* True when every bit below prefix_len is zero (false if prefix_len exceeds the address width). */
bool firc_ip_aligned(const firc_ip_t *a, uint8_t prefix_len);

/* Parses "address/prefix"; returns false and leaves outputs untouched on failure. */
bool firc_ip_parse_cidr(const char *text, firc_ip_t *out, uint8_t *prefix_out);

#endif /* FIRC_FAKEIP_ADDR_H */
