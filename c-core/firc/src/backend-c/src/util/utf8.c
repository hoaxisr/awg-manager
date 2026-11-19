#include "firc/utf8.h"

bool firc_utf8_valid(const char *s)
{
    const unsigned char *p = (const unsigned char *)s;
    while (*p != '\0') {
        unsigned char c = *p;
        if (c < 0x80) {
            p++;
            continue;
        }
        int n;
        unsigned char lo = 0x80, hi = 0xbf;
        if (c >= 0xc2 && c <= 0xdf) {
            n = 1;
        } else if (c >= 0xe0 && c <= 0xef) {
            n = 2;
            if (c == 0xe0) { lo = 0xa0; }
            if (c == 0xed) { hi = 0x9f; }
        } else if (c >= 0xf0 && c <= 0xf4) {
            n = 3;
            if (c == 0xf0) { lo = 0x90; }
            if (c == 0xf4) { hi = 0x8f; }
        } else {
            return false;
        }
        if (p[1] < lo || p[1] > hi) { return false; }
        for (int i = 2; i <= n; i++) {
            if (p[i] < 0x80 || p[i] > 0xbf) { return false; }
        }
        p += n + 1;
    }
    return true;
}

bool firc_text_has_control(const char *s)
{
    for (const unsigned char *p = (const unsigned char *)s; *p != '\0'; p++) {
        if (*p < 0x20 || *p == 0x7f || (p[0] == 0xc2 && p[1] >= 0x80 && p[1] <= 0x9f)) { return true; }
    }
    return false;
}

int firc_utf8_clamp(const char *s, int max_bytes) {
    int n = 0;
    while (n < max_bytes && s[n] != '\0') { n++; } /* cppcheck arrayIndexThenCheck */
    if (s[n] == '\0') { return n; }
    while (n > 0 && ((unsigned char)s[n] & 0xc0) == 0x80) { n--; }
    return n;
}
