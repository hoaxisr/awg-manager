#ifndef TUNVLESS_PORTPICK_H
#define TUNVLESS_PORTPICK_H
#include <stddef.h>
#include <stdint.h>

#define PORT_RANGES_MAX 32

struct port_range { uint16_t lo, hi; };

size_t port_ranges(const char *ports, struct port_range *out, size_t cap);
uint16_t port_pick(const struct port_range *r, size_t n, uint32_t x);

#endif
