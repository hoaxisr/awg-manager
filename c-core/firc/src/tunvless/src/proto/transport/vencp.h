/* Parsing of a VLESS node's `encryption` string:
 * mlkem768x25519plus.<mode>.<rtt>.<padding>.<key>[.<key>...]
 *
 * Strings only, no cryptography, so this is a header of static functions: both the subscription
 * parser (sub.c and sublink.c, built into `make test` without the crypto library) and the
 * encryption itself (trvenc.c) include it. The rule is what Xray-core does when it builds the
 * config (infra/conf/vless.go, VLessOutboundConfig.Build) and the client
 * (proxy/vless/outbound/outbound.go, New):
 *
 *     mlkem768x25519plus . native|xorpub|random . 1rtt|0rtt . [padding .]... key [. key]...
 *
 *   - mode: native — records are sent as is; xorpub — the handshake public keys (X25519, ML-KEM
 *     ciphertext) are masked with a keystream so they look like random bytes; random — the
 *     headers of all records are masked too, so the whole stream is indistinguishable from noise;
 *   - 0rtt lets the client resume with a ticket the server issued (no new key exchange); 1rtt is
 *     always a full handshake. A client that declares 0rtt may still not use a ticket;
 *   - padding — tokens shorter than 20 characters like "100-111-1111" (probability, from, to),
 *     alternating length, gap, length...; the server hands them to the client in the link.
 *     Without them Xray's default applies. The first length must be at least 100-35-35:
 *     always applied, and at least 35 bytes;
 *   - key — a token of 20 characters or more, base64url without padding: 32 bytes (X25519, the
 *     relay's public key) or 1184 (ML-KEM-768 encapsulation key). Relays form a chain, in order.
 *
 * A token of 20 characters or more that does not decode to 32 or 1184 bytes makes the string
 * invalid: Xray rejects such a config ("unsupported encryption") rather than skipping the token. */
#ifndef STEER_VENCP_H
#define STEER_VENCP_H
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#define VENC_MAX_KEYS 6
#define VENC_MAX_PAD  8            /* padding (length, gap) pairs */
#define VENC_PREFIX "mlkem768x25519plus"

struct venc_cfg {
    uint8_t xor_mode;              /* 0 native, 1 xorpub, 2 random */
    uint8_t zero_rtt;              /* 0rtt: the client may use a ticket */
    uint8_t nkeys;
    uint8_t key_kind[VENC_MAX_KEYS];             /* 32, or 0 for ML-KEM (1184), by token length */
    const char *key[VENC_MAX_KEYS];              /* token start in the source string */
    uint16_t key_len[VENC_MAX_KEYS];             /* token length in characters */
    /* Padding: triples (probability 0..100, from, to). lens[i] — length, gaps[i] — gap in ms. */
    uint8_t npad_lens, npad_gaps;
    uint16_t lens[VENC_MAX_PAD][3];
    uint16_t gaps[VENC_MAX_PAD][3];
};

/* The decoded length of unpadded base64url, or -1 for a bad character or length. */
static inline int vencp_b64url_len(const char *s, size_t n) {
    for (size_t i = 0; i < n; i++) {
        char c = s[i];
        if (!((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
              c == '-' || c == '_')) return -1;
    }
    if (n % 4 == 1) return -1;
    return (int)(n * 6 / 8);
}

/* One "p-a-b" triple; 0 — parsed. As in Xray: at least three parts, decimal numbers. */
static inline int vencp_triple(const char *s, size_t n, uint16_t out[3]) {
    size_t p = 0;
    for (int k = 0; k < 3; k++) {
        if (p >= n || s[p] < '0' || s[p] > '9') return -1;
        unsigned v = 0;
        while (p < n && s[p] >= '0' && s[p] <= '9') { v = v * 10 + (unsigned)(s[p] - '0'); p++; if (v > 65535) return -1; }
        out[k] = (uint16_t)v;
        if (k < 2) { if (p >= n || s[p] != '-') return -1; p++; }
    }
    /* Xray ignores anything after the third part ("-..."): it takes the first three fields. */
    return 0;
}

/* 0 — valid; otherwise -1 and *why (a short reason, a static string). why may be NULL. */
static inline int vencp_parse(const char *s, struct venc_cfg *c, const char **why) {
    memset(c, 0, sizeof(*c));
    const char *w = "";
    size_t pl = strlen(VENC_PREFIX);
    if (strncmp(s, VENC_PREFIX, pl) != 0 || s[pl] != '.') { w = "encryption: not mlkem768x25519plus"; goto bad; }
    const char *p = s + pl + 1;
    if (!strncmp(p, "native.", 7)) c->xor_mode = 0;
    else if (!strncmp(p, "xorpub.", 7)) c->xor_mode = 1;
    else if (!strncmp(p, "random.", 7)) c->xor_mode = 2;
    else { w = "encryption: mode is not native/xorpub/random"; goto bad; }
    p += 7;
    if (!strncmp(p, "1rtt.", 5)) c->zero_rtt = 0;
    else if (!strncmp(p, "0rtt.", 5)) c->zero_rtt = 1;
    else { w = "encryption: not 1rtt/0rtt"; goto bad; }
    p += 5;
    int tok_i = 0;
    while (*p) {
        const char *e = strchr(p, '.');
        size_t n = e ? (size_t)(e - p) : strlen(p);
        if (n == 0) { w = "encryption: empty token"; goto bad; }
        if (n < 20) {
            /* Padding. Keys only come after it: Xray takes as padding exactly the leading tokens
             * shorter than 20 characters, and a key cannot be shorter than 20. */
            if (c->nkeys) { w = "encryption: padding after a key"; goto bad; }
            uint16_t t3[3];
            if (vencp_triple(p, n, t3) != 0) { w = "encryption: invalid padding"; goto bad; }
            if (tok_i % 2 == 0) {
                if (c->npad_lens >= VENC_MAX_PAD) { w = "encryption: too many padding tokens"; goto bad; }
                memcpy(c->lens[c->npad_lens++], t3, sizeof t3);
                if (c->npad_lens == 1 && (t3[0] < 100 || t3[1] < 18 + 17 || t3[2] < 18 + 17)) {
                    w = "encryption: first padding is below 100-35-35"; goto bad;
                }
            } else {
                if (c->npad_gaps >= VENC_MAX_PAD) { w = "encryption: too many padding tokens"; goto bad; }
                memcpy(c->gaps[c->npad_gaps++], t3, sizeof t3);
            }
            tok_i++;
        } else {
            int bl = vencp_b64url_len(p, n);
            if (bl != 32 && bl != 1184) { w = "encryption: key is neither 32 nor 1184 bytes"; goto bad; }
            if (c->nkeys >= VENC_MAX_KEYS) { w = "encryption: too many keys"; goto bad; }
            c->key[c->nkeys] = p;
            c->key_len[c->nkeys] = (uint16_t)n;
            c->key_kind[c->nkeys] = bl == 32 ? 32 : 0;
            c->nkeys++;
        }
        if (!e) break;
        p = e + 1;
    }
    if (!c->nkeys) { w = "encryption: no key"; goto bad; }
    return 0;
bad:
    if (why) *why = w;
    return -1;
}

#endif
