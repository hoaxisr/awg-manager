#include "firc/fakeip_addr.h"

#include <arpa/inet.h>
#include <string.h>
#include <sys/socket.h>

void firc_ip_add(firc_ip_t *a, uint64_t delta) {
    /* Wire order: byte 0 is most significant; the carry walks backwards. */
    for (int i = (int)a->len - 1; i >= 0 && delta != 0; i--) {
        uint64_t sum = (uint64_t)a->b[i] + (delta & 0xFFu);
        a->b[i] = (uint8_t)sum;
        delta = (delta >> 8) + (sum >> 8);
    }
}

bool firc_ip_add_shifted(firc_ip_t *a, uint64_t count, uint8_t shift_bits) {
    /* count << shift_bits can pass 64 bits: a byte shift places the addend, 0-7 leftover bits widen it to 9 bytes. */
    uint8_t byte_shift = (uint8_t)(shift_bits / 8);
    uint8_t bit_shift = (uint8_t)(shift_bits % 8);

    uint8_t addend[9] = {0};
    for (uint8_t i = 0; i < 8; i++) {
        unsigned limb = (unsigned)((count >> (i * 8)) & 0xFFu) << bit_shift;
        addend[i] = (uint8_t)(addend[i] | (uint8_t)limb);
        addend[i + 1] = (uint8_t)(limb >> 8);
    }

    firc_ip_t out = *a;
    unsigned carry = 0;
    for (uint8_t i = 0; i < sizeof(addend) || carry != 0; i++) {
        int idx = (int)out.len - 1 - (int)byte_shift - (int)i;
        if (idx < 0) { /* carry or addend left above the address: overflow */
            if (carry != 0) { return false; }
            for (size_t j = i; j < sizeof(addend); j++) {
                if (addend[j] != 0) { return false; }
            }
            break;
        }
        unsigned sum =
            (unsigned)out.b[idx] + (i < sizeof(addend) ? addend[i] : 0u) + carry;
        out.b[idx] = (uint8_t)sum;
        carry = sum >> 8;
    }
    *a = out;
    return true;
}

bool firc_ip_aligned(const firc_ip_t *a, uint8_t prefix_len) {
    uint8_t bits = (uint8_t)(a->len * 8);
    if (prefix_len > bits) { return false; }

    uint8_t whole = (uint8_t)(prefix_len / 8);
    uint8_t rest = (uint8_t)(prefix_len % 8);
    if (rest != 0) {
        /* The byte the prefix ends inside: its low (8 - rest) bits are host. */
        if ((a->b[whole] & (uint8_t)(0xFFu >> rest)) != 0) { return false; }
        whole++;
    }
    for (uint8_t i = whole; i < a->len; i++) {
        if (a->b[i] != 0) { return false; }
    }
    return true;
}

bool firc_ip_parse_cidr(const char *text, firc_ip_t *out, uint8_t *prefix_out) {
    if (text == NULL || out == NULL || prefix_out == NULL) { return false; }

    const char *slash = strchr(text, '/');
    if (slash == NULL || slash == text) { return false; }

    size_t addr_len = (size_t)(slash - text);
    char addr[46]; /* the longest textual IPv6 address is 45 characters */
    if (addr_len >= sizeof(addr)) { return false; }
    memcpy(addr, text, addr_len);
    addr[addr_len] = '\0';

    /* Family comes from the text: a colon means v6. */
    firc_ip_t parsed = {{0}, 0};
    if (strchr(addr, ':') != NULL) {
        if (inet_pton(AF_INET6, addr, parsed.b) != 1) { return false; }
        parsed.len = 16;
    } else {
        if (inet_pton(AF_INET, addr, parsed.b) != 1) { return false; }
        parsed.len = 4;
    }

    /* Parsed by hand: strtoul would accept "+15", "0x0f", or a wrapping '-'. */
    const char *p = slash + 1;
    if (*p == '\0') { return false; }
    unsigned prefix = 0;
    for (; *p != '\0'; p++) {
        if (*p < '0' || *p > '9') { return false; }
        prefix = prefix * 10u + (unsigned)(*p - '0');
        if (prefix > 128u) { return false; }
    }
    if (prefix > (unsigned)(parsed.len * 8)) { return false; }

    *out = parsed;
    *prefix_out = (uint8_t)prefix;
    return true;
}
