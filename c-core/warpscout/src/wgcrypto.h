/* awgm-warpscout: crypto primitives used by the WireGuard handshake.
 * BLAKE2s (RFC 7693) is implemented here; X25519 and ChaCha20/Poly1305
 * come from vendored Monocypher (CC0 / BSD-2). */
#ifndef WS_WGCRYPTO_H
#define WS_WGCRYPTO_H

#include <stddef.h>
#include <stdint.h>

#define BLAKE2S_OUTBYTES 32
#define BLAKE2S_BLOCKBYTES 64

typedef struct {
    uint32_t h[8];
    uint32_t t[2];
    uint32_t f[2];
    uint8_t buf[BLAKE2S_BLOCKBYTES];
    size_t buflen;
    size_t outlen;
} blake2s_ctx;

int blake2s_init(blake2s_ctx *c, size_t outlen, const void *key, size_t keylen);
void blake2s_update(blake2s_ctx *c, const void *in, size_t inlen);
void blake2s_final(blake2s_ctx *c, void *out);
void blake2s(void *out, size_t outlen, const void *key, size_t keylen,
             const void *in, size_t inlen);

/* HMAC-BLAKE2s-256 (RFC 2104 over BLAKE2s, block size 64). */
void hmac_blake2s(uint8_t out[32], const uint8_t *key, size_t keylen,
                  const uint8_t *in, size_t inlen);

/* WireGuard KDF_n(key, input) -> n outputs of 32 bytes (n = 1..3).
 * Any of t1..t3 may be NULL. */
void wg_kdf(uint8_t *t1, uint8_t *t2, uint8_t *t3, const uint8_t key[32],
            const uint8_t *in, size_t inlen);

/* ChaCha20-Poly1305 IETF AEAD (RFC 8439). WireGuard nonce:
 * 4 zero bytes || 64-bit little-endian counter. */
void aead_seal(uint8_t *out /* len+16 */, const uint8_t key[32], uint64_t counter,
               const uint8_t *pt, size_t len, const uint8_t *ad, size_t adlen);
int aead_open(uint8_t *out /* len-16 */, const uint8_t key[32], uint64_t counter,
              const uint8_t *ct, size_t len, const uint8_t *ad, size_t adlen);
/* Same, but with an explicit 12-byte nonce (for RFC vectors). */
void aead_seal_n(uint8_t *out, const uint8_t key[32], const uint8_t nonce[12],
                 const uint8_t *pt, size_t len, const uint8_t *ad, size_t adlen);
int aead_open_n(uint8_t *out, const uint8_t key[32], const uint8_t nonce[12],
                const uint8_t *ct, size_t len, const uint8_t *ad, size_t adlen);

void x25519_pub(uint8_t pub[32], const uint8_t priv[32]);
/* Returns 0 on success, -1 if the shared secret is all-zero. */
int x25519_dh(uint8_t out[32], const uint8_t priv[32], const uint8_t pub[32]);
void x25519_clamp(uint8_t k[32]);

int ct_memeq(const void *a, const void *b, size_t n);
void wipe(void *p, size_t n);

/* OS randomness; returns 0 on success. */
int os_random(void *buf, size_t n);

/* TAI64N timestamp (12 bytes); strictly increasing within the process. */
void tai64n_now(uint8_t out[12]);

/* base64 (standard alphabet). decode returns length or -1. */
int b64_decode(const char *in, uint8_t *out, size_t outcap);
size_t b64_encode(const uint8_t *in, size_t n, char *out);

int hex_decode(const char *in, uint8_t *out, size_t outcap);

int crypto_selftest(int verbose);

#endif
