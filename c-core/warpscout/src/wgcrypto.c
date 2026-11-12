/* awgm-warpscout crypto glue. BLAKE2s written from RFC 7693. */
#include "wgcrypto.h"
#include "../vendor/monocypher.h"

#include <string.h>
#include <stdio.h>
#include <time.h>

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <bcrypt.h>
#else
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#endif

/* ---------------- BLAKE2s ---------------- */

static const uint32_t b2s_iv[8] = {
    0x6A09E667UL, 0xBB67AE85UL, 0x3C6EF372UL, 0xA54FF53AUL,
    0x510E527FUL, 0x9B05688CUL, 0x1F83D9ABUL, 0x5BE0CD19UL};

static const uint8_t b2s_sigma[10][16] = {
    {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
    {14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
    {11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4},
    {7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
    {9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13},
    {2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
    {12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11},
    {13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
    {6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5},
    {10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0}};

static inline uint32_t rotr32(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }
static inline uint32_t ld32le(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static inline void st32le(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)v; p[1] = (uint8_t)(v >> 8); p[2] = (uint8_t)(v >> 16); p[3] = (uint8_t)(v >> 24);
}

#define B2S_G(a, b, c, d, x, y)          \
    do {                                 \
        v[a] = v[a] + v[b] + (x);        \
        v[d] = rotr32(v[d] ^ v[a], 16);  \
        v[c] = v[c] + v[d];              \
        v[b] = rotr32(v[b] ^ v[c], 12);  \
        v[a] = v[a] + v[b] + (y);        \
        v[d] = rotr32(v[d] ^ v[a], 8);   \
        v[c] = v[c] + v[d];              \
        v[b] = rotr32(v[b] ^ v[c], 7);   \
    } while (0)

static void b2s_compress(blake2s_ctx *c, int last) {
    uint32_t v[16], m[16];
    int i;
    for (i = 0; i < 8; i++) { v[i] = c->h[i]; v[i + 8] = b2s_iv[i]; }
    v[12] ^= c->t[0];
    v[13] ^= c->t[1];
    if (last) v[14] = ~v[14];
    for (i = 0; i < 16; i++) m[i] = ld32le(c->buf + 4 * i);
    for (i = 0; i < 10; i++) {
        const uint8_t *s = b2s_sigma[i];
        B2S_G(0, 4, 8, 12, m[s[0]], m[s[1]]);
        B2S_G(1, 5, 9, 13, m[s[2]], m[s[3]]);
        B2S_G(2, 6, 10, 14, m[s[4]], m[s[5]]);
        B2S_G(3, 7, 11, 15, m[s[6]], m[s[7]]);
        B2S_G(0, 5, 10, 15, m[s[8]], m[s[9]]);
        B2S_G(1, 6, 11, 12, m[s[10]], m[s[11]]);
        B2S_G(2, 7, 8, 13, m[s[12]], m[s[13]]);
        B2S_G(3, 4, 9, 14, m[s[14]], m[s[15]]);
    }
    for (i = 0; i < 8; i++) c->h[i] ^= v[i] ^ v[i + 8];
}

int blake2s_init(blake2s_ctx *c, size_t outlen, const void *key, size_t keylen) {
    if (outlen == 0 || outlen > 32 || keylen > 32) return -1;
    memset(c, 0, sizeof(*c));
    for (int i = 0; i < 8; i++) c->h[i] = b2s_iv[i];
    c->h[0] ^= 0x01010000UL ^ ((uint32_t)keylen << 8) ^ (uint32_t)outlen;
    c->outlen = outlen;
    if (keylen > 0) {
        blake2s_update(c, key, keylen);
        c->buflen = 64; /* key block is padded to a full block */
    }
    return 0;
}

void blake2s_update(blake2s_ctx *c, const void *in, size_t inlen) {
    const uint8_t *p = (const uint8_t *)in;
    for (size_t i = 0; i < inlen; i++) {
        if (c->buflen == 64) {
            c->t[0] += 64;
            if (c->t[0] < 64) c->t[1]++;
            b2s_compress(c, 0);
            c->buflen = 0;
        }
        c->buf[c->buflen++] = p[i];
    }
}

void blake2s_final(blake2s_ctx *c, void *out) {
    uint8_t full[32];
    c->t[0] += (uint32_t)c->buflen;
    if (c->t[0] < c->buflen) c->t[1]++;
    while (c->buflen < 64) c->buf[c->buflen++] = 0;
    b2s_compress(c, 1);
    for (int i = 0; i < 8; i++) st32le(full + 4 * i, c->h[i]);
    memcpy(out, full, c->outlen);
    wipe(full, sizeof(full));
    wipe(c, sizeof(*c));
}

void blake2s(void *out, size_t outlen, const void *key, size_t keylen,
             const void *in, size_t inlen) {
    blake2s_ctx c;
    blake2s_init(&c, outlen, key, keylen);
    blake2s_update(&c, in, inlen);
    blake2s_final(&c, out);
}

void hmac_blake2s(uint8_t out[32], const uint8_t *key, size_t keylen,
                  const uint8_t *in, size_t inlen) {
    uint8_t k[64], pad[64], inner[32];
    blake2s_ctx c;
    memset(k, 0, sizeof(k));
    if (keylen > 64) blake2s(k, 32, NULL, 0, key, keylen);
    else memcpy(k, key, keylen);
    for (int i = 0; i < 64; i++) pad[i] = k[i] ^ 0x36;
    blake2s_init(&c, 32, NULL, 0);
    blake2s_update(&c, pad, 64);
    blake2s_update(&c, in, inlen);
    blake2s_final(&c, inner);
    for (int i = 0; i < 64; i++) pad[i] = k[i] ^ 0x5c;
    blake2s_init(&c, 32, NULL, 0);
    blake2s_update(&c, pad, 64);
    blake2s_update(&c, inner, 32);
    blake2s_final(&c, out);
    wipe(k, sizeof(k));
    wipe(pad, sizeof(pad));
    wipe(inner, sizeof(inner));
}

void wg_kdf(uint8_t *t1, uint8_t *t2, uint8_t *t3, const uint8_t key[32],
            const uint8_t *in, size_t inlen) {
    uint8_t t0[32], o1[32], o2[33], o3[32];
    uint8_t one = 1;
    hmac_blake2s(t0, key, 32, in, inlen);
    hmac_blake2s(o1, t0, 32, &one, 1);
    if (t1) memcpy(t1, o1, 32);
    if (t2 || t3) {
        memcpy(o2, o1, 32);
        o2[32] = 2;
        hmac_blake2s(o2, t0, 32, o2, 33);
        if (t2) memcpy(t2, o2, 32);
        if (t3) {
            o2[32] = 3;
            hmac_blake2s(o3, t0, 32, o2, 33);
            memcpy(t3, o3, 32);
        }
    }
    wipe(t0, 32); wipe(o1, 32); wipe(o2, 33); wipe(o3, 32);
}

/* ---------------- AEAD ---------------- */

static void poly_tag(uint8_t tag[16], const uint8_t key[32], const uint8_t nonce[12],
                     const uint8_t *ad, size_t adlen, const uint8_t *ct, size_t len) {
    static const uint8_t zero[16] = {0};
    uint8_t polykey[64] = {0};
    uint8_t lens[16];
    crypto_poly1305_ctx pc;
    crypto_chacha20_ietf(polykey, polykey, 64, key, nonce, 0);
    crypto_poly1305_init(&pc, polykey);
    crypto_poly1305_update(&pc, ad, adlen);
    crypto_poly1305_update(&pc, zero, (16 - (adlen & 15)) & 15);
    crypto_poly1305_update(&pc, ct, len);
    crypto_poly1305_update(&pc, zero, (16 - (len & 15)) & 15);
    for (int i = 0; i < 8; i++) {
        lens[i] = (uint8_t)((uint64_t)adlen >> (8 * i));
        lens[8 + i] = (uint8_t)((uint64_t)len >> (8 * i));
    }
    crypto_poly1305_update(&pc, lens, 16);
    crypto_poly1305_final(&pc, tag);
    wipe(polykey, sizeof(polykey));
}

void aead_seal_n(uint8_t *out, const uint8_t key[32], const uint8_t nonce[12],
                 const uint8_t *pt, size_t len, const uint8_t *ad, size_t adlen) {
    if (len) crypto_chacha20_ietf(out, pt, len, key, nonce, 1);
    poly_tag(out + len, key, nonce, ad, adlen, out, len);
}

int aead_open_n(uint8_t *out, const uint8_t key[32], const uint8_t nonce[12],
                const uint8_t *ct, size_t len, const uint8_t *ad, size_t adlen) {
    uint8_t tag[16];
    if (len < 16) return -1;
    len -= 16;
    poly_tag(tag, key, nonce, ad, adlen, ct, len);
    if (crypto_verify16(tag, ct + len) != 0) return -1;
    if (len) crypto_chacha20_ietf(out, ct, len, key, nonce, 1);
    return 0;
}

static void wg_nonce(uint8_t n[12], uint64_t ctr) {
    memset(n, 0, 4);
    for (int i = 0; i < 8; i++) n[4 + i] = (uint8_t)(ctr >> (8 * i));
}

void aead_seal(uint8_t *out, const uint8_t key[32], uint64_t counter,
               const uint8_t *pt, size_t len, const uint8_t *ad, size_t adlen) {
    uint8_t n[12];
    wg_nonce(n, counter);
    aead_seal_n(out, key, n, pt, len, ad, adlen);
}

int aead_open(uint8_t *out, const uint8_t key[32], uint64_t counter,
              const uint8_t *ct, size_t len, const uint8_t *ad, size_t adlen) {
    uint8_t n[12];
    wg_nonce(n, counter);
    return aead_open_n(out, key, n, ct, len, ad, adlen);
}

/* ---------------- X25519 ---------------- */

void x25519_clamp(uint8_t k[32]) {
    k[0] &= 248;
    k[31] &= 127;
    k[31] |= 64;
}

void x25519_pub(uint8_t pub[32], const uint8_t priv[32]) {
    crypto_x25519_public_key(pub, priv);
}

int x25519_dh(uint8_t out[32], const uint8_t priv[32], const uint8_t pub[32]) {
    static const uint8_t zero[32] = {0};
    crypto_x25519(out, priv, pub);
    return crypto_verify32(out, zero) == 0 ? -1 : 0;
}

int ct_memeq(const void *a, const void *b, size_t n) {
    const volatile uint8_t *x = (const volatile uint8_t *)a, *y = (const volatile uint8_t *)b;
    uint8_t d = 0;
    for (size_t i = 0; i < n; i++) d |= x[i] ^ y[i];
    return d == 0;
}

void wipe(void *p, size_t n) { crypto_wipe(p, n); }

/* ---------------- randomness / time ---------------- */

int os_random(void *buf, size_t n) {
#ifdef _WIN32
    return BCryptGenRandom(NULL, (PUCHAR)buf, (ULONG)n, BCRYPT_USE_SYSTEM_PREFERRED_RNG) == 0 ? 0 : -1;
#else
    static int fd = -1;
    uint8_t *p = (uint8_t *)buf;
    if (fd < 0) {
        fd = open("/dev/urandom", O_RDONLY);
        if (fd < 0) return -1;
    }
    while (n > 0) {
        ssize_t r = read(fd, p, n);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (r == 0) return -1;
        p += r;
        n -= (size_t)r;
    }
    return 0;
#endif
}

void tai64n_now(uint8_t out[12]) {
    static uint64_t last_s;
    static uint32_t last_ns;
    uint64_t s;
    uint32_t ns;
#ifdef _WIN32
    FILETIME ft;
    GetSystemTimePreciseAsFileTime(&ft);
    uint64_t t = ((uint64_t)ft.dwHighDateTime << 32) | ft.dwLowDateTime; /* 100ns since 1601 */
    t -= 116444736000000000ULL;
    s = t / 10000000ULL;
    ns = (uint32_t)((t % 10000000ULL) * 100);
#else
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    s = (uint64_t)ts.tv_sec;
    ns = (uint32_t)ts.tv_nsec;
#endif
    s += 0x400000000000000aULL;
    if (s < last_s || (s == last_s && ns <= last_ns)) {
        s = last_s;
        ns = last_ns + 1;
        if (ns >= 1000000000U) { ns = 0; s++; }
    }
    last_s = s;
    last_ns = ns;
    for (int i = 0; i < 8; i++) out[i] = (uint8_t)(s >> (56 - 8 * i));
    for (int i = 0; i < 4; i++) out[8 + i] = (uint8_t)(ns >> (24 - 8 * i));
}

/* ---------------- encoding ---------------- */

static const char b64tab[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static int b64val(int c) {
    if (c >= 'A' && c <= 'Z') return c - 'A';
    if (c >= 'a' && c <= 'z') return c - 'a' + 26;
    if (c >= '0' && c <= '9') return c - '0' + 52;
    if (c == '+' || c == '-') return 62;
    if (c == '/' || c == '_') return 63;
    return -1;
}

int b64_decode(const char *in, uint8_t *out, size_t outcap) {
    uint32_t acc = 0;
    int bits = 0;
    size_t n = 0;
    for (; *in; in++) {
        int c = (unsigned char)*in;
        if (c == '=' ) break;
        if (c == ' ' || c == '\n' || c == '\r' || c == '\t') continue;
        int v = b64val(c);
        if (v < 0) return -1;
        acc = (acc << 6) | (uint32_t)v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (n >= outcap) return -1;
            out[n++] = (uint8_t)(acc >> bits);
        }
    }
    return (int)n;
}

size_t b64_encode(const uint8_t *in, size_t n, char *out) {
    size_t o = 0, i;
    for (i = 0; i + 2 < n; i += 3) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8) | in[i + 2];
        out[o++] = b64tab[(v >> 18) & 63]; out[o++] = b64tab[(v >> 12) & 63];
        out[o++] = b64tab[(v >> 6) & 63];  out[o++] = b64tab[v & 63];
    }
    if (i < n) {
        uint32_t v = (uint32_t)in[i] << 16;
        if (i + 1 < n) v |= (uint32_t)in[i + 1] << 8;
        out[o++] = b64tab[(v >> 18) & 63]; out[o++] = b64tab[(v >> 12) & 63];
        out[o++] = (i + 1 < n) ? b64tab[(v >> 6) & 63] : '=';
        out[o++] = '=';
    }
    out[o] = 0;
    return o;
}

static int hexval(int c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

int hex_decode(const char *in, uint8_t *out, size_t outcap) {
    size_t n = 0;
    if (in[0] == '0' && (in[1] == 'x' || in[1] == 'X')) in += 2;
    while (*in) {
        int c = (unsigned char)*in;
        if (c == ' ' || c == ':' || c == '\n' || c == '\r' || c == '\t') { in++; continue; }
        int hi = hexval(c);
        int lo = in[1] ? hexval((unsigned char)in[1]) : -1;
        if (hi < 0 || lo < 0) return -1;
        if (n >= outcap) return -1;
        out[n++] = (uint8_t)((hi << 4) | lo);
        in += 2;
    }
    return (int)n;
}

/* ---------------- self test (RFC vectors) ---------------- */

#include "selftest_vectors.h"

static int check(const char *name, const uint8_t *got, const char *want_hex, int verbose) {
    uint8_t want[512];
    int n = hex_decode(want_hex, want, sizeof(want));
    int ok = n > 0 && memcmp(got, want, (size_t)n) == 0;
    if (verbose || !ok) fprintf(stderr, "selftest %-34s %s\n", name, ok ? "ok" : "FAIL");
    return ok ? 0 : 1;
}

int crypto_selftest(int verbose) {
    int fails = 0;
    uint8_t out[512], buf[512], key[64], nonce[12], ad[64];
    int n, kn, an;

    /* BLAKE2s-256("abc"), RFC 7693 Appendix B */
    blake2s(out, 32, NULL, 0, "abc", 3);
    fails += check("blake2s-256(abc) rfc7693", out, VEC_B2S_ABC, verbose);
    /* BLAKE2s keyed KAT: key=00..1f, in=empty and in=00..3f */
    for (int i = 0; i < 64; i++) { key[i] = (uint8_t)i; buf[i] = (uint8_t)i; }
    blake2s(out, 32, key, 32, buf, 0);
    fails += check("blake2s keyed kat[0]", out, VEC_B2S_KEYED_0, verbose);
    blake2s(out, 32, key, 32, buf, 64);
    fails += check("blake2s keyed kat[64]", out, VEC_B2S_KEYED_64, verbose);
    {   /* streaming over multiple blocks */
        static uint8_t big[1000];
        blake2s_ctx c;
        for (int i = 0; i < 1000; i++) big[i] = (uint8_t)(i * 7);
        blake2s_init(&c, 32, NULL, 0);
        blake2s_update(&c, big, 333);
        blake2s_update(&c, big + 333, 667);
        blake2s_final(&c, out);
        fails += check("blake2s-256(1000 bytes)", out, VEC_B2S_1000, verbose);
        blake2s(out, 16, key, 32, big, 116);
        fails += check("blake2s-128 keyed (mac1 shape)", out, VEC_B2S128_KEYED, verbose);
    }
    /* HMAC-BLAKE2s and WireGuard KDF (vectors from Go x/crypto) */
    hmac_blake2s(out, key, 32, (const uint8_t *)"wireguard", 9);
    fails += check("hmac-blake2s", out, VEC_HMAC, verbose);
    {
        uint8_t t1[32], t2[32], t3[32];
        wg_kdf(t1, t2, t3, key, buf, 32);
        fails += check("wg kdf t1", t1, VEC_KDF_T1, verbose);
        fails += check("wg kdf t2", t2, VEC_KDF_T2, verbose);
        fails += check("wg kdf t3", t3, VEC_KDF_T3, verbose);
    }

    /* ChaCha20-Poly1305, RFC 8439 section 2.8.2 */
    kn = hex_decode(VEC_AEAD_KEY, key, sizeof(key));
    hex_decode(VEC_AEAD_NONCE, nonce, sizeof(nonce));
    an = hex_decode(VEC_AEAD_AD, ad, sizeof(ad));
    n = (int)strlen(VEC_AEAD_PT);
    (void)kn;
    aead_seal_n(out, key, nonce, (const uint8_t *)VEC_AEAD_PT, (size_t)n, ad, (size_t)an);
    fails += check("chacha20poly1305 seal rfc8439", out, VEC_AEAD_CT, verbose);
    fails += check("chacha20poly1305 tag rfc8439", out + n, VEC_AEAD_TAG, verbose);
    if (aead_open_n(buf, key, nonce, out, (size_t)n + 16, ad, (size_t)an) != 0 ||
        memcmp(buf, VEC_AEAD_PT, (size_t)n) != 0) {
        fprintf(stderr, "selftest chacha20poly1305 open FAIL\n");
        fails++;
    }
    out[3] ^= 1;
    if (aead_open_n(buf, key, nonce, out, (size_t)n + 16, ad, (size_t)an) == 0) {
        fprintf(stderr, "selftest chacha20poly1305 forgery accepted FAIL\n");
        fails++;
    }
    /* WireGuard nonce layout check: counter=0x0102030405060708, empty pt */
    aead_seal(out, key, 0x0102030405060708ULL, NULL, 0, ad, (size_t)an);
    fails += check("aead wg-nonce empty", out, VEC_AEAD_WG_EMPTY, verbose);

    /* X25519, RFC 7748 section 6.1 */
    {
        uint8_t a[32], b[32], ap[32], bp[32], s1[32], s2[32];
        hex_decode(VEC_X_A_PRIV, a, 32);
        hex_decode(VEC_X_B_PRIV, b, 32);
        x25519_pub(ap, a);
        x25519_pub(bp, b);
        fails += check("x25519 alice pub rfc7748", ap, VEC_X_A_PUB, verbose);
        fails += check("x25519 bob pub rfc7748", bp, VEC_X_B_PUB, verbose);
        x25519_dh(s1, a, bp);
        x25519_dh(s2, b, ap);
        fails += check("x25519 shared (a) rfc7748", s1, VEC_X_SHARED, verbose);
        fails += check("x25519 shared (b) rfc7748", s2, VEC_X_SHARED, verbose);
    }
    {
        uint8_t t[12], t2[12];
        tai64n_now(t);
        tai64n_now(t2);
        if (memcmp(t2, t, 12) <= 0 || t[0] != 0x40) {
            fprintf(stderr, "selftest tai64n monotonic FAIL\n");
            fails++;
        } else if (verbose) fprintf(stderr, "selftest %-34s ok\n", "tai64n monotonic");
    }
    return fails;
}
