#include "firc/hash.h"

#include <stdbool.h>
#include <string.h>

static const char std_alphabet[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
static const char url_alphabet[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

size_t firc_base64_encoded_len(size_t in_len) {
    return ((in_len + 2) / 3) * 4;
}

size_t firc_base64url_encoded_len(size_t in_len) {
    size_t full = in_len / 3;
    size_t rem = in_len % 3;
    size_t len = full * 4;
    if (rem == 1) { len += 2; }
    if (rem == 2) { len += 3; }
    return len;
}

static void encode_core(const uint8_t *data, size_t len, const char *alphabet, bool pad,
                        char *out) {
    size_t i = 0;
    size_t o = 0;
    while (i + 3 <= len) {
        uint32_t v = ((uint32_t)data[i] << 16) | ((uint32_t)data[i + 1] << 8) | data[i + 2];
        out[o++] = alphabet[(v >> 18) & 0x3f];
        out[o++] = alphabet[(v >> 12) & 0x3f];
        out[o++] = alphabet[(v >> 6) & 0x3f];
        out[o++] = alphabet[v & 0x3f];
        i += 3;
    }
    size_t rem = len - i;
    if (rem == 1) {
        uint32_t v = (uint32_t)data[i] << 16;
        out[o++] = alphabet[(v >> 18) & 0x3f];
        out[o++] = alphabet[(v >> 12) & 0x3f];
        if (pad) {
            out[o++] = '=';
            out[o++] = '=';
        }
    } else if (rem == 2) {
        uint32_t v = ((uint32_t)data[i] << 16) | ((uint32_t)data[i + 1] << 8);
        out[o++] = alphabet[(v >> 18) & 0x3f];
        out[o++] = alphabet[(v >> 12) & 0x3f];
        out[o++] = alphabet[(v >> 6) & 0x3f];
        if (pad) {
            out[o++] = '=';
        }
    }
    out[o] = '\0';
}

void firc_base64_encode(const uint8_t *data, size_t len, char *out) {
    encode_core(data, len, std_alphabet, true, out);
}

void firc_base64url_encode(const uint8_t *data, size_t len, char *out) {
    encode_core(data, len, url_alphabet, false, out);
}

static int decode_char(char c, const char *alphabet, uint8_t *val) {
    for (uint8_t i = 0; i < 64; i++) {
        if (alphabet[i] == c) {
            *val = i;
            return 0;
        }
    }
    return -1;
}

static int decode_core(const char *in, size_t in_len, const char *alphabet, uint8_t *out,
                       size_t out_cap, size_t *out_len) {
    while (in_len > 0 && in[in_len - 1] == '=') {
        in_len--;
    }
    size_t o = 0;
    size_t i = 0;
    uint32_t acc = 0;
    int bits = 0;
    for (; i < in_len; i++) {
        uint8_t v;
        if (decode_char(in[i], alphabet, &v) != 0) { return -1; }
        acc = (acc << 6) | v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (o == out_cap) { return -1; }
            out[o++] = (uint8_t)((acc >> bits) & 0xff);
        }
    }
    *out_len = o;
    return 0;
}

int firc_base64_decode(const char *in, size_t in_len, uint8_t *out, size_t out_cap,
                       size_t *out_len) {
    return decode_core(in, in_len, std_alphabet, out, out_cap, out_len);
}

int firc_base64url_decode(const char *in, size_t in_len, uint8_t *out, size_t out_cap,
                          size_t *out_len) {
    return decode_core(in, in_len, url_alphabet, out, out_cap, out_len);
}
