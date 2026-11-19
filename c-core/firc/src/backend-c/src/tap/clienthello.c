#include "firc/clienthello.h"

#include <string.h>

/* Bounds-checked cursor: once bad is set, every further read returns 0. */
typedef struct {
    const uint8_t *p;
    size_t left;
    bool bad;
} cur_t;

static uint8_t u8(cur_t *c) {
    if (c->bad || c->left < 1) {
        c->bad = true;
        return 0;
    }
    c->left--;
    return *c->p++;
}

static uint16_t u16(cur_t *c) {
    uint16_t hi = u8(c);
    uint16_t lo = u8(c);
    return (uint16_t)((hi << 8) | lo);
}

static uint32_t u24(cur_t *c) {
    uint32_t a = u8(c), b = u8(c), d = u8(c);
    return (a << 16) | (b << 8) | d;
}

static void skip(cur_t *c, size_t n) {
    if (c->bad || c->left < n) {
        c->bad = true;
        return;
    }
    c->p += n;
    c->left -= n;
}

/* Narrows to the next n bytes; the outer cursor advances past them either way. */
static cur_t enter(cur_t *outer, size_t n) {
    cur_t in = {outer->p, n, outer->bad};
    if (outer->bad || outer->left < n) {
        outer->bad = true;
        in.bad = true;
        in.left = 0;
        return in;
    }
    outer->p += n;
    outer->left -= n;
    return in;
}

/* Clamps instead of refusing: NFLOG truncates every capture, so a longer declared length is normal here. */
static cur_t enter_what_arrived(cur_t *outer, size_t n) {
    if (n > outer->left) { n = outer->left; }
    return enter(outer, n);
}

/* rules/match.c's host-name grammar minus wildcards, since this name is matched against rules too. */
static bool is_a_host_name(const char *s, size_t n) {
    if (n == 0 || n > 253) { return false; }
    if (s[0] == '.' || s[n - 1] == '.') { return false; }
    size_t label = 0;
    for (size_t i = 0; i < n; i++) {
        unsigned char ch = (unsigned char)s[i];
        if (ch == '.') {
            if (label == 0) { return false; } /* empty label */
            label = 0;
            continue;
        }
        bool ok = (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
                  (ch >= '0' && ch <= '9') || ch == '-' || ch == '_';
        if (!ok) { return false; }
        if (++label > 63) { return false; }
    }
    return true;
}

bool firc_tls_client_hello_sni(const uint8_t *buf, size_t len, char *out, size_t cap) {
    if (buf == NULL || out == NULL || cap == 0) { return false; }
    cur_t rec = {buf, len, false};

    if (u8(&rec) != 0x16) { return false; } /* handshake record */
    skip(&rec, 2);                          /* record version: not ours to judge */
    uint16_t rec_len = u16(&rec);
    cur_t hs = enter_what_arrived(&rec, rec_len);

    if (u8(&hs) != 0x01) { return false; } /* client_hello */
    uint32_t hs_len = u24(&hs);
    cur_t ch = enter_what_arrived(&hs, hs_len);

    skip(&ch, 2);                 /* client_version */
    skip(&ch, 32);                /* random */
    skip(&ch, u8(&ch));           /* session_id */
    skip(&ch, u16(&ch));          /* cipher_suites */
    skip(&ch, u8(&ch));           /* compression_methods */
    if (ch.bad) { return false; }
    if (ch.left == 0) { return false; } /* no extensions: no SNI */

    cur_t exts = enter_what_arrived(&ch, u16(&ch));
    while (!exts.bad && exts.left > 0) {
        uint16_t type = u16(&exts);
        cur_t ext = enter(&exts, u16(&exts));
        if (exts.bad) { return false; }
        if (type != 0x0000) { continue; } /* not server_name */

        cur_t list = enter(&ext, u16(&ext));
        /* Only the first host_name is read; RFC 6066 allows one of each type. */
        while (!list.bad && list.left > 0) {
            uint8_t name_type = u8(&list);
            uint16_t name_len = u16(&list);
            if (list.bad) { return false; }
            if (name_type != 0x00) { /* not host_name */
                skip(&list, name_len);
                continue;
            }
            if (name_len >= cap) { return false; }
            const uint8_t *name = list.p;
            skip(&list, name_len);
            if (list.bad) { return false; }
            size_t n = name_len;
            /* Strips one trailing root dot, which OpenSSL sends but a query never does. */
            if (n > 1 && name[n - 1] == '.') { n--; }
            if (!is_a_host_name((const char *)name, n)) { return false; }
            /* Folded to lower case: every other name in firc reaches the matchers folded. */
            for (size_t i = 0; i < n; i++) {
                char c = (char)name[i];
                out[i] = (c >= 'A' && c <= 'Z') ? (char)(c - 'A' + 'a') : (char)c;
            }
            out[n] = '\0';
            return true;
        }
        return false; /* a server_name extension with no host_name in it */
    }
    return false;
}
