#ifndef GORAND_H
#define GORAND_H

#include <stdint.h>

struct go_rand {
    int tap, feed;
    uint64_t vec[607];
};

void go_rand_seed(struct go_rand *r, int64_t seed);
int64_t go_rand_int63(struct go_rand *r);
uint32_t go_rand_u32(struct go_rand *r);
int32_t go_rand_int31n(struct go_rand *r, int32_t n);
void go_rand_shuffle_u16(struct go_rand *r, uint16_t *v, int n);

#endif
