#include <stdio.h>
#include <string.h>
#include "osrand.h"
#include "xhreq.h"

static const uint8_t huff_bits[256] = {
    13, 23, 28, 28, 28, 28, 28, 28, 28, 24, 30, 28, 28, 30, 28, 28,
    28, 28, 28, 28, 28, 28, 30, 28, 28, 28, 28, 28, 28, 28, 28, 28,
    6, 10, 10, 12, 13, 6, 8, 11, 10, 10, 8, 11, 8, 6, 6, 6,
    5, 5, 5, 6, 6, 6, 6, 6, 6, 6, 7, 8, 15, 6, 12, 10,
    13, 6, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
    7, 7, 7, 7, 7, 7, 7, 7, 8, 7, 8, 13, 19, 13, 14, 6,
    15, 5, 6, 5, 6, 5, 6, 6, 6, 5, 7, 7, 6, 6, 6, 5,
    6, 7, 6, 5, 5, 6, 7, 7, 7, 7, 7, 15, 11, 14, 13, 28,
    20, 22, 20, 20, 22, 22, 22, 23, 22, 23, 23, 23, 23, 23, 24, 23,
    24, 24, 22, 23, 24, 23, 23, 23, 23, 21, 22, 23, 22, 23, 23, 24,
    22, 21, 20, 22, 22, 23, 23, 21, 23, 22, 22, 24, 21, 22, 23, 23,
    21, 21, 22, 21, 23, 22, 23, 23, 20, 22, 22, 22, 23, 22, 22, 23,
    26, 26, 20, 19, 22, 23, 22, 25, 26, 26, 26, 27, 27, 26, 24, 25,
    19, 21, 26, 27, 27, 26, 27, 24, 21, 21, 26, 26, 28, 27, 27, 27,
    20, 24, 20, 21, 22, 21, 21, 23, 22, 22, 25, 25, 24, 24, 26, 23,
    26, 27, 26, 26, 27, 27, 27, 27, 27, 28, 27, 27, 27, 27, 27, 26,
};

static const char base62[] = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

size_t xh_huff_len(const char *s, size_t n) {
    uint64_t bits = 0;
    for (size_t i = 0; i < n; i++) bits += huff_bits[(unsigned char)s[i]];
    return (size_t)((bits + 7) / 8);
}

static int rand_from(const char *set, size_t m, char *out, size_t n) {
    unsigned char buf[256];
    unsigned limit = 256 - 256 % (unsigned)m;
    size_t i = 0;
    while (i < n) {
        if (os_getrandom(buf, sizeof buf, 0) != (ssize_t)sizeof buf) return -1;
        for (size_t j = 0; j < sizeof buf && i < n; j++)
            if (buf[j] < limit) out[i++] = set[buf[j] % m];
    }
    return 0;
}

static void uuid_id(char *out, size_t cap) {
    unsigned char r[16];
    if (os_getrandom(r, sizeof r, 0) != (ssize_t)sizeof r) {
        snprintf(out, cap, "00000000-0000-0000-0000-000000000000");
        return;
    }
    r[6] = (unsigned char)((r[6] & 0x0F) | 0x40);
    r[8] = (unsigned char)((r[8] & 0x3F) | 0x80);
    snprintf(out, cap,
             "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
             r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7],
             r[8], r[9], r[10], r[11], r[12], r[13], r[14], r[15]);
}

void xh_session_id(const struct xh_opts *o, char *out, size_t cap) {
    size_t m = strlen(o->table);
    if (!m || !o->id_len_to || !cap) { uuid_id(out, cap); return; }
    size_t n = o->id_len_from;
    if (o->id_len_to > o->id_len_from) {
        unsigned char r[2];
        if (os_getrandom(r, sizeof r, 0) != (ssize_t)sizeof r) { uuid_id(out, cap); return; }
        n += ((size_t)r[0] << 8 | r[1]) % ((size_t)o->id_len_to - o->id_len_from + 1);
    }
    if (n >= cap) n = cap - 1;
    if (rand_from(o->table, m, out, n)) { uuid_id(out, cap); return; }
    out[n] = '\0';
}

static size_t repeat_x(size_t len, char *out, size_t cap) {
    if (len >= cap) len = cap - 1;
    memset(out, 'X', len);
    out[len] = '\0';
    return len;
}

size_t xh_padding(const struct xh_opts *o, size_t len, char *out, size_t cap) {
    if (!cap) return 0;
    if (!len) { out[0] = '\0'; return 0; }
    if (!o->tokenish) return repeat_x(len, out, cap);
    size_t n = (len * 5 + 3) / 4;
    if (n >= cap) n = cap - 1;
    if (!n || rand_from(base62, sizeof base62 - 1, out, n)) return repeat_x(len, out, cap);
    char adjust = 'X';
    for (int iter = 0; iter < 150; iter++) {
        size_t h = xh_huff_len(out, n);
        if (h + 2 >= len && h <= len + 2) break;
        if (h < len) {
            if (n + 1 >= cap) break;
            out[n++] = adjust;
            adjust = adjust == 'X' ? 'Z' : 'X';
        } else {
            if (n <= 1) break;
            n--;
        }
    }
    out[n] = '\0';
    return n;
}
