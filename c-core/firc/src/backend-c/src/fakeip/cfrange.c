#include "firc/cfrange.h"

#include <stddef.h>
#include <string.h>

typedef struct cf_prefix {
    uint8_t b[16];
    uint8_t bits;
} cf_prefix_t;

static const cf_prefix_t V4[] = {
    {{173, 245, 48, 0}, 20}, {{103, 21, 244, 0}, 22}, {{103, 22, 200, 0}, 22}, {{103, 31, 4, 0}, 22},
    {{141, 101, 64, 0}, 18}, {{108, 162, 192, 0}, 18}, {{190, 93, 240, 0}, 20}, {{188, 114, 96, 0}, 20},
    {{197, 234, 240, 0}, 22}, {{198, 41, 128, 0}, 17}, {{162, 158, 0, 0}, 15}, {{104, 16, 0, 0}, 13},
    {{104, 24, 0, 0}, 14}, {{172, 64, 0, 0}, 13}, {{131, 0, 72, 0}, 22},
};

static const cf_prefix_t V6[] = {
    {{0x24, 0x00, 0xcb, 0x00}, 32}, {{0x26, 0x06, 0x47, 0x00}, 32}, {{0x28, 0x03, 0xf8, 0x00}, 32},
    {{0x24, 0x05, 0xb5, 0x00}, 32}, {{0x24, 0x05, 0x81, 0x00}, 32}, {{0x2a, 0x06, 0x98, 0xc0}, 29},
    {{0x2c, 0x0f, 0xf2, 0x48}, 32},
};

static bool within(const firc_ip_t *ip, const cf_prefix_t *p) {
    uint8_t whole = p->bits / 8;
    uint8_t rest = p->bits % 8;
    if (memcmp(ip->b, p->b, whole) != 0) {
        return false;
    }
    if (rest == 0) {
        return true;
    }
    uint8_t mask = (uint8_t)(0xFFu << (8 - rest));
    return (ip->b[whole] & mask) == (p->b[whole] & mask);
}

bool firc_cf_contains(const firc_ip_t *ip) {
    if (ip == NULL || (ip->len != 4 && ip->len != 16)) {
        return false;
    }
    const cf_prefix_t *table = ip->len == 4 ? V4 : V6;
    size_t n = ip->len == 4 ? sizeof(V4) / sizeof(V4[0]) : sizeof(V6) / sizeof(V6[0]);
    for (size_t i = 0; i < n; i++) {
        if (within(ip, &table[i])) {
            return true;
        }
    }
    return false;
}
