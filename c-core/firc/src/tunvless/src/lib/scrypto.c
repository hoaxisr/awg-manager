/* The cryptographic primitives layer on wolfCrypt. Why the layer exists and why contexts are
 * shaped as they are: see scrypto.h.
 *
 * This is the only file that sees wolfSSL. The library's options are in
 * build/wolfssl/user_settings.h, and this file must be compiled with the same ones
 * (-DWOLFSSL_USER_SETTINGS and -I to build/wolfssl, from the .cflags file build.sh writes next to
 * the library), or struct sizes here and in the library disagree. The _Static_assert checks
 * below catch only header storage that is too small; against a library built with other
 * options the only guard is that build.sh writes the library and its .cflags together.
 *
 * Rule for edits: no function returns wolfSSL codes, only SC_*. Library codes mean nothing to
 * the caller, and replacing the library must not change any branch on the caller's side. */
#define _GNU_SOURCE
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include <stdlib.h>
#include <unistd.h>
#include <pthread.h>
#include <time.h>
#include <arpa/inet.h>

#include <wolfssl/wolfcrypt/settings.h>

#if defined(__mips__) && !defined(WOLFSSL_USE_ALIGN)
#error "wolfSSL on MIPS needs WOLFSSL_USE_ALIGN"
#endif
#include <wolfssl/version.h>
#include <wolfssl/ssl.h>
#include <wolfssl/wolfcrypt/sha256.h>
#include <wolfssl/wolfcrypt/sha512.h>
#include <wolfssl/wolfcrypt/sha.h>
#include <wolfssl/wolfcrypt/md5.h>
#include <wolfssl/wolfcrypt/sha3.h>
#include <wolfssl/wolfcrypt/blake2.h>
#include <wolfssl/wolfcrypt/hmac.h>
#include <wolfssl/wolfcrypt/aes.h>
#include <wolfssl/wolfcrypt/chacha.h>
#include <wolfssl/wolfcrypt/poly1305.h>
#include <wolfssl/wolfcrypt/chacha20_poly1305.h>
#include <wolfssl/wolfcrypt/curve25519.h>
#include <wolfssl/wolfcrypt/rsa.h>
#include <wolfssl/wolfcrypt/ecc.h>
#include <wolfssl/wolfcrypt/asn_public.h>
#include <wolfssl/wolfcrypt/hash.h>
#include <wolfssl/wolfcrypt/memory.h>
#include <wolfssl/wolfcrypt/wc_mlkem.h>
#include <wolfssl/wolfcrypt/wc_mldsa.h>

#include "scrypto.h"
#include "blake3.h"

/* ---- storage against the real sizes -------------------------------------------------------
 *
 * Header storage is a byte array aligned to 16 that holds a wolfSSL object. Checked at build
 * time, not at run time: storage that is too small is not an error to return but a write past
 * the field, and it must show up when building for the architecture where it would happen. */
_Static_assert(sizeof(wc_Sha256) <= SC_HASH_CTX_SIZE && _Alignof(wc_Sha256) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Sha256");
_Static_assert(sizeof(wc_Sha512) <= SC_HASH_CTX_SIZE && _Alignof(wc_Sha512) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Sha512");
_Static_assert(sizeof(wc_Sha384) <= SC_HASH_CTX_SIZE && _Alignof(wc_Sha384) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Sha384");
_Static_assert(sizeof(Aes) <= SC_AEAD_CTX_SIZE && _Alignof(Aes) <= 16, "SC_AEAD_CTX_SIZE is too small for Aes");
_Static_assert(sizeof(Aes) <= SC_AESCTR_CTX_SIZE, "SC_AESCTR_CTX_SIZE is too small for Aes");
/* SHA-1, SHA-224 and MD5 (scrypto.h). In wolfSSL wc_Sha224 is the same type as wc_Sha256. */
_Static_assert(sizeof(wc_Sha) <= SC_HASH_CTX_SIZE && _Alignof(wc_Sha) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Sha");
_Static_assert(sizeof(wc_Sha224) <= SC_HASH_CTX_SIZE && _Alignof(wc_Sha224) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Sha224");
_Static_assert(sizeof(wc_Md5) <= SC_HASH_CTX_SIZE && _Alignof(wc_Md5) <= 16, "SC_HASH_CTX_SIZE is too small for wc_Md5");

/* ChaCha20-Poly1305: the ChaCha key plus a working Poly1305 (its key differs for every record,
 * RFC 8439 §2.6), side by side in one storage. */
struct chachapoly {
    ChaCha   chacha;
    Poly1305 poly;
};
_Static_assert(sizeof(struct chachapoly) <= SC_AEAD_CTX_SIZE && _Alignof(struct chachapoly) <= 16,
               "SC_AEAD_CTX_SIZE is too small for ChaCha+Poly1305");

/* ---- hashes --------------------------------------------------------------------------------- */

size_t sc_hash_len(enum sc_hash h) {
    switch (h) {
        case SC_SHA256: return 32;
        case SC_SHA384: return 48;
        case SC_SHA512: return 64;
        case SC_SHA1:   return 20;
        case SC_SHA224: return 28;
        case SC_MD5:    return 16;
    }
    return 0;
}

/* wolfSSL hash types: WC_SHA256 and friends for HMAC and HKDF, enum wc_HashType for PSS and
 * OIDs. */
static int wc_type(enum sc_hash h) {
    switch (h) {
        case SC_SHA256: return WC_SHA256;
        case SC_SHA384: return WC_SHA384;
        case SC_SHA512: return WC_SHA512;
        case SC_SHA1:   return WC_SHA;
        case SC_SHA224: return WC_SHA224;
        case SC_MD5:    return WC_MD5;
    }
    return -1;
}
static enum wc_HashType wc_htype(enum sc_hash h) {
    switch (h) {
        case SC_SHA256: return WC_HASH_TYPE_SHA256;
        case SC_SHA384: return WC_HASH_TYPE_SHA384;
        case SC_SHA512: return WC_HASH_TYPE_SHA512;
        /* SHA-1, SHA-224 and MD5 never verify signatures: they are not passed to chain or PSS
         * checks, so a SHA-1 signature is rejected. */
        case SC_SHA1: case SC_SHA224: case SC_MD5: break;
    }
    return WC_HASH_TYPE_NONE;
}

int sc_hash_init(struct sc_hash_ctx *c, enum sc_hash h) {
    int rc = -1;
    c->alg = 0;
    switch (h) {
        case SC_SHA256: rc = wc_InitSha256_ex((wc_Sha256 *)c->st, NULL, INVALID_DEVID); break;
        case SC_SHA384: rc = wc_InitSha384_ex((wc_Sha384 *)c->st, NULL, INVALID_DEVID); break;
        case SC_SHA512: rc = wc_InitSha512_ex((wc_Sha512 *)c->st, NULL, INVALID_DEVID); break;
        case SC_SHA1:   rc = wc_InitSha_ex((wc_Sha *)c->st, NULL, INVALID_DEVID); break;
        case SC_SHA224: rc = wc_InitSha224_ex((wc_Sha224 *)c->st, NULL, INVALID_DEVID); break;
        case SC_MD5:    rc = wc_InitMd5_ex((wc_Md5 *)c->st, NULL, INVALID_DEVID); break;
        default: return SC_EINVAL;
    }
    if (rc != 0) return SC_ECRYPTO;
    c->alg = (int)h;
    return 0;
}

int sc_hash_update(struct sc_hash_ctx *c, const void *d, size_t n) {
    int rc;
    if (n > UINT32_MAX) return SC_EINVAL;
    switch (c->alg) {
        case SC_SHA256: rc = wc_Sha256Update((wc_Sha256 *)c->st, d, (word32)n); break;
        case SC_SHA384: rc = wc_Sha384Update((wc_Sha384 *)c->st, d, (word32)n); break;
        case SC_SHA512: rc = wc_Sha512Update((wc_Sha512 *)c->st, d, (word32)n); break;
        case SC_SHA1:   rc = wc_ShaUpdate((wc_Sha *)c->st, d, (word32)n); break;
        case SC_SHA224: rc = wc_Sha224Update((wc_Sha224 *)c->st, d, (word32)n); break;
        case SC_MD5:    rc = wc_Md5Update((wc_Md5 *)c->st, d, (word32)n); break;
        default: return SC_EINVAL;
    }
    return rc == 0 ? 0 : SC_ECRYPTO;
}

int sc_hash_final(struct sc_hash_ctx *c, unsigned char *out) {
    int rc;
    switch (c->alg) {
        case SC_SHA256: rc = wc_Sha256Final((wc_Sha256 *)c->st, out); break;
        case SC_SHA384: rc = wc_Sha384Final((wc_Sha384 *)c->st, out); break;
        case SC_SHA512: rc = wc_Sha512Final((wc_Sha512 *)c->st, out); break;
        case SC_SHA1:   rc = wc_ShaFinal((wc_Sha *)c->st, out); break;
        case SC_SHA224: rc = wc_Sha224Final((wc_Sha224 *)c->st, out); break;
        case SC_MD5:    rc = wc_Md5Final((wc_Md5 *)c->st, out); break;
        default: return SC_EINVAL;
    }
    return rc == 0 ? 0 : SC_ECRYPTO;
}

/* Copy with the library's function, not memcpy: a wolfSSL hash context may hold pointers (the
 * schedule cache with WOLFSSL_SMALL_STACK_CACHE, device data). A byte copy would share them
 * between two contexts, and freeing both would free one twice. */
int sc_hash_clone(struct sc_hash_ctx *dst, const struct sc_hash_ctx *src) {
    int rc;
    dst->alg = 0;
    switch (src->alg) {
        case SC_SHA256:
            rc = wc_InitSha256_ex((wc_Sha256 *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_Sha256Copy((wc_Sha256 *)src->st, (wc_Sha256 *)dst->st);
            break;
        case SC_SHA384:
            rc = wc_InitSha384_ex((wc_Sha384 *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_Sha384Copy((wc_Sha384 *)src->st, (wc_Sha384 *)dst->st);
            break;
        case SC_SHA512:
            rc = wc_InitSha512_ex((wc_Sha512 *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_Sha512Copy((wc_Sha512 *)src->st, (wc_Sha512 *)dst->st);
            break;
        case SC_SHA1:
            rc = wc_InitSha_ex((wc_Sha *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_ShaCopy((wc_Sha *)src->st, (wc_Sha *)dst->st);
            break;
        case SC_SHA224:
            rc = wc_InitSha224_ex((wc_Sha224 *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_Sha224Copy((wc_Sha224 *)src->st, (wc_Sha224 *)dst->st);
            break;
        case SC_MD5:
            rc = wc_InitMd5_ex((wc_Md5 *)dst->st, NULL, INVALID_DEVID);
            if (rc == 0) rc = wc_Md5Copy((wc_Md5 *)src->st, (wc_Md5 *)dst->st);
            break;
        default: return SC_EINVAL;
    }
    if (rc != 0) return SC_ECRYPTO;
    dst->alg = src->alg;
    return 0;
}

void sc_hash_free(struct sc_hash_ctx *c) {
    switch (c->alg) {
        case SC_SHA256: wc_Sha256Free((wc_Sha256 *)c->st); break;
        case SC_SHA384: wc_Sha384Free((wc_Sha384 *)c->st); break;
        case SC_SHA512: wc_Sha512Free((wc_Sha512 *)c->st); break;
        case SC_SHA1:   wc_ShaFree((wc_Sha *)c->st); break;
        case SC_SHA224: wc_Sha224Free((wc_Sha224 *)c->st); break;
        case SC_MD5:    wc_Md5Free((wc_Md5 *)c->st); break;
        default: return;
    }
    c->alg = 0;
}

int sc_hash(enum sc_hash h, const void *d, size_t n, unsigned char *out) {
    struct sc_hash_ctx c;
    int rc = sc_hash_init(&c, h);
    if (rc) return rc;
    rc = sc_hash_update(&c, d, n);
    if (rc == 0) rc = sc_hash_final(&c, out);
    sc_hash_free(&c);
    return rc;
}

/* ---- HMAC and HKDF -------------------------------------------------------------------------- */

int sc_hmac2(enum sc_hash h, const void *key, size_t key_n,
             const void *msg, size_t n, const void *msg2, size_t n2, unsigned char *out) {
    int t = wc_type(h);
    if (t < 0 || key_n > UINT32_MAX || n > UINT32_MAX || n2 > UINT32_MAX) return SC_EINVAL;
    /* wolfSSL's Hmac is over half a kilobyte (two hash states and two pads), so it goes on the
     * heap: connector threads, whose stacks are small, call HMAC. */
    Hmac *m = malloc(sizeof(*m));
    if (!m) return SC_ENOMEM;
    /* An empty key is legal (HKDF-Extract without salt in RFC 5869 is HMAC with a zero key, but
     * a caller may pass an empty one), and wolfSSL refuses NULL even with zero length. */
    static const unsigned char nokey[1];
    int rc = wc_HmacInit(m, NULL, INVALID_DEVID);
    if (rc == 0) rc = wc_HmacSetKey(m, t, key_n ? key : nokey, (word32)key_n);
    if (rc == 0 && n) rc = wc_HmacUpdate(m, msg, (word32)n);
    if (rc == 0 && n2) rc = wc_HmacUpdate(m, msg2, (word32)n2);
    if (rc == 0) rc = wc_HmacFinal(m, out);
    wc_HmacFree(m);
    wc_ForceZero(m, sizeof(*m));
    free(m);
    return rc == 0 ? 0 : SC_ECRYPTO;
}

int sc_hmac(enum sc_hash h, const void *key, size_t key_n,
            const void *msg, size_t n, unsigned char *out) {
    return sc_hmac2(h, key, key_n, msg, n, NULL, 0, out);
}

/* HKDF (RFC 5869) with wolfSSL's functions. An empty ikm or info is passed as a non-NULL
 * pointer, since wolfSSL may refuse NULL. A NULL salt of zero length is replaced with
 * hash-length zeros, as the RFC requires, and HMAC with an empty key gives the same. expand's
 * out must not overlap prk: the library writes out block by block, reading prk for each. */
int sc_hkdf_extract(enum sc_hash h, const void *salt, size_t salt_n,
                    const void *ikm, size_t ikm_n, unsigned char *prk) {
    static const unsigned char empty[1];
    int t = wc_type(h);
    if (t < 0 || salt_n > UINT32_MAX || ikm_n > UINT32_MAX) return SC_EINVAL;
    return wc_HKDF_Extract(t, salt_n ? salt : NULL, (word32)salt_n, ikm_n ? ikm : empty,
                           (word32)ikm_n, prk) == 0 ? 0 : SC_ECRYPTO;
}

int sc_hkdf_expand(enum sc_hash h, const void *prk, size_t prk_n,
                   const void *info, size_t info_n, unsigned char *out, size_t out_n) {
    static const unsigned char empty[1];
    int t = wc_type(h);
    if (t < 0 || prk_n > UINT32_MAX || info_n > UINT32_MAX || out_n > 255 * sc_hash_len(h))
        return SC_EINVAL;
    return wc_HKDF_Expand(t, prk, (word32)prk_n, info_n ? info : empty, (word32)info_n,
                          out, (word32)out_n) == 0 ? 0 : SC_ECRYPTO;
}

int sc_hkdf(enum sc_hash h, const void *salt, size_t salt_n, const void *ikm, size_t ikm_n,
            const void *info, size_t info_n, unsigned char *out, size_t out_n) {
    unsigned char prk[SC_HASH_MAX];
    int rc = sc_hkdf_extract(h, salt, salt_n, ikm, ikm_n, prk);
    if (rc == 0) rc = sc_hkdf_expand(h, prk, sc_hash_len(h), info, info_n, out, out_n);
    wc_ForceZero(prk, sizeof(prk));
    return rc;
}

int sc_pbkdf2_sha256(const void *pass, size_t pass_n, const void *salt, size_t salt_n,
                     int iter, unsigned char *out, size_t out_n) {
    if (iter < 1 || !out || !out_n || pass_n > UINT32_MAX || salt_n > UINT32_MAX) return SC_EINVAL;
    Hmac *m = malloc(sizeof(*m));
    if (!m) return SC_ENOMEM;
    static const unsigned char nokey[1];
    int rc = wc_HmacInit(m, NULL, INVALID_DEVID);
    if (rc != 0) { free(m); return SC_ECRYPTO; }
    uint32_t blk = 1;
    size_t pos = 0;
    unsigned char u[32], t[32];
    while (pos < out_n) {
        unsigned char be4[4] = {
            (unsigned char)(blk >> 24), (unsigned char)(blk >> 16),
            (unsigned char)(blk >> 8),  (unsigned char)blk
        };
        rc = wc_HmacSetKey(m, WC_SHA256, pass_n ? pass : nokey, (word32)pass_n);
        if (rc == 0 && salt_n) rc = wc_HmacUpdate(m, salt, (word32)salt_n);
        if (rc == 0) rc = wc_HmacUpdate(m, be4, 4);
        if (rc == 0) rc = wc_HmacFinal(m, u);
        if (rc != 0) break;
        memcpy(t, u, 32);
        for (int i = 1; i < iter; i++) {
            rc = wc_HmacSetKey(m, WC_SHA256, pass_n ? pass : nokey, (word32)pass_n);
            if (rc == 0) rc = wc_HmacUpdate(m, u, 32);
            if (rc == 0) rc = wc_HmacFinal(m, u);
            if (rc != 0) break;
            for (int j = 0; j < 32; j++) t[j] ^= u[j];
        }
        if (rc != 0) break;
        size_t take = out_n - pos < 32 ? out_n - pos : 32;
        memcpy(out + pos, t, take);
        pos += take;
        blk++;
    }
    wc_ForceZero(u, sizeof(u));
    wc_ForceZero(t, sizeof(t));
    wc_HmacFree(m);
    wc_ForceZero(m, sizeof(*m));
    free(m);
    return rc == 0 ? 0 : SC_ECRYPTO;
}

/* ---- AEAD ----------------------------------------------------------------------------------- */

size_t sc_aead_key_len(enum sc_aead_alg a) {
    switch (a) {
        case SC_AES128_GCM: return 16;
        case SC_AES256_GCM: return 32;
        case SC_CHACHA20_POLY1305: return 32;
    }
    return 0;
}

int sc_aead_setkey(struct sc_aead *k, enum sc_aead_alg a, const unsigned char *key) {
    size_t kn = sc_aead_key_len(a);
    k->alg = 0;
    if (!kn) return SC_EINVAL;
    if (a == SC_CHACHA20_POLY1305) {
        struct chachapoly *cp = (struct chachapoly *)k->st;
        memset(cp, 0, sizeof(*cp));
        if (wc_Chacha_SetKey(&cp->chacha, key, (word32)kn) != 0) return SC_ECRYPTO;
    } else {
        Aes *aes = (Aes *)k->st;
        if (wc_AesInit(aes, NULL, INVALID_DEVID) != 0) return SC_ECRYPTO;
        if (wc_AesGcmSetKey(aes, key, (word32)kn) != 0) { wc_AesFree(aes); return SC_ECRYPTO; }
    }
    k->alg = (int)a;
    return 0;
}

void sc_aead_free(struct sc_aead *k) {
    if (!k->alg) return;
    if (k->alg == SC_CHACHA20_POLY1305) {
        wc_ForceZero(k->st, sizeof(struct chachapoly));
    } else {
        wc_AesFree((Aes *)k->st);
        wc_ForceZero(k->st, sizeof(Aes));
    }
    k->alg = 0;
}

int sc_aead_seal(struct sc_aead *k, const unsigned char nonce[12],
                 const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                 unsigned char tag[16]) {
    int rc;
    if (n > UINT32_MAX || aad_n > UINT32_MAX) return SC_EINVAL;
    if (k->alg == SC_CHACHA20_POLY1305) {
        struct chachapoly *cp = (struct chachapoly *)k->st;
        rc = wc_ChaCha20Poly1305_Encrypt_ex(&cp->chacha, &cp->poly, buf, buf, (word32)n, nonce,
                                            tag, aad, (word32)aad_n);
    } else if (k->alg == SC_AES128_GCM || k->alg == SC_AES256_GCM) {
        rc = wc_AesGcmEncrypt((Aes *)k->st, buf, buf, (word32)n, nonce, 12, tag, 16,
                              aad, (word32)aad_n);
    } else {
        return SC_EINVAL;
    }
    return rc == 0 ? 0 : SC_ECRYPTO;
}

int sc_aead_open(struct sc_aead *k, const unsigned char nonce[12],
                 const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                 const unsigned char tag[16]) {
    int rc;
    if (n > UINT32_MAX || aad_n > UINT32_MAX) return SC_EINVAL;
    if (k->alg == SC_CHACHA20_POLY1305) {
        struct chachapoly *cp = (struct chachapoly *)k->st;
        rc = wc_ChaCha20Poly1305_Decrypt_ex(&cp->chacha, &cp->poly, buf, buf, (word32)n, nonce,
                                            tag, aad, (word32)aad_n);
        if (rc == WC_NO_ERR_TRACE(MAC_CMP_FAILED_E)) return SC_EAUTH;
    } else if (k->alg == SC_AES128_GCM || k->alg == SC_AES256_GCM) {
        rc = wc_AesGcmDecrypt((Aes *)k->st, buf, buf, (word32)n, nonce, 12, tag, 16,
                              aad, (word32)aad_n);
        if (rc == WC_NO_ERR_TRACE(AES_GCM_AUTH_E)) return SC_EAUTH;
    } else {
        return SC_EINVAL;
    }
    return rc == 0 ? 0 : SC_ECRYPTO;
}

static inline uint32_t hcc_rotl32(uint32_t v, int c) { return (v << c) | (v >> (32 - c)); }
static inline uint32_t hcc_le32(const unsigned char *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static inline void hcc_put_le32(unsigned char *p, uint32_t v) {
    p[0] = (unsigned char)v; p[1] = (unsigned char)(v >> 8);
    p[2] = (unsigned char)(v >> 16); p[3] = (unsigned char)(v >> 24);
}
#define HCC_QR(a, b, c, d) do { \
    (a) += (b); (d) ^= (a); (d) = hcc_rotl32((d), 16); \
    (c) += (d); (b) ^= (c); (b) = hcc_rotl32((b), 12); \
    (a) += (b); (d) ^= (a); (d) = hcc_rotl32((d), 8);  \
    (c) += (d); (b) ^= (c); (b) = hcc_rotl32((b), 7);  \
} while (0)

static void hchacha20(const unsigned char key[32], const unsigned char nonce16[16],
                      unsigned char subkey[32]) {
    uint32_t x[16] = {
        0x61707865u, 0x3320646eu, 0x79622d32u, 0x6b206574u,
        hcc_le32(key + 0),  hcc_le32(key + 4),  hcc_le32(key + 8),  hcc_le32(key + 12),
        hcc_le32(key + 16), hcc_le32(key + 20), hcc_le32(key + 24), hcc_le32(key + 28),
        hcc_le32(nonce16 + 0), hcc_le32(nonce16 + 4), hcc_le32(nonce16 + 8), hcc_le32(nonce16 + 12)
    };
    for (int i = 0; i < 10; i++) {
        HCC_QR(x[0], x[4], x[8],  x[12]);
        HCC_QR(x[1], x[5], x[9],  x[13]);
        HCC_QR(x[2], x[6], x[10], x[14]);
        HCC_QR(x[3], x[7], x[11], x[15]);
        HCC_QR(x[0], x[5], x[10], x[15]);
        HCC_QR(x[1], x[6], x[11], x[12]);
        HCC_QR(x[2], x[7], x[8],  x[13]);
        HCC_QR(x[3], x[4], x[9],  x[14]);
    }
    for (int i = 0; i < 4; i++) hcc_put_le32(subkey + i * 4, x[i]);
    for (int i = 0; i < 4; i++) hcc_put_le32(subkey + 16 + i * 4, x[12 + i]);
    wc_ForceZero(x, sizeof(x));
}

int sc_xchacha20_poly1305_seal(const unsigned char key[32], const unsigned char nonce[24],
                               const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                               unsigned char tag[16]) {
    unsigned char subkey[32], subnonce[12] = {0};
    hchacha20(key, nonce, subkey);
    memcpy(subnonce + 4, nonce + 16, 8);
    struct sc_aead a;
    int rc = sc_aead_setkey(&a, SC_CHACHA20_POLY1305, subkey);
    if (rc == 0) rc = sc_aead_seal(&a, subnonce, aad, aad_n, buf, n, tag);
    sc_aead_free(&a);
    wc_ForceZero(subkey, sizeof(subkey));
    return rc;
}

int sc_xchacha20_poly1305_open(const unsigned char key[32], const unsigned char nonce[24],
                               const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                               const unsigned char tag[16]) {
    unsigned char subkey[32], subnonce[12] = {0};
    hchacha20(key, nonce, subkey);
    memcpy(subnonce + 4, nonce + 16, 8);
    struct sc_aead a;
    int rc = sc_aead_setkey(&a, SC_CHACHA20_POLY1305, subkey);
    if (rc == 0) rc = sc_aead_open(&a, subnonce, aad, aad_n, buf, n, tag);
    sc_aead_free(&a);
    wc_ForceZero(subkey, sizeof(subkey));
    return rc;
}

int sc_chacha20_hp(const unsigned char key[32], const unsigned char sample[16],
                   unsigned char mask[5]) {
    if (!key || !sample || !mask) return SC_EINVAL;
    uint32_t init[16] = {
        0x61707865u, 0x3320646eu, 0x79622d32u, 0x6b206574u,
        hcc_le32(key + 0),     hcc_le32(key + 4),     hcc_le32(key + 8),     hcc_le32(key + 12),
        hcc_le32(key + 16),    hcc_le32(key + 20),    hcc_le32(key + 24),    hcc_le32(key + 28),
        hcc_le32(sample + 0),  hcc_le32(sample + 4),  hcc_le32(sample + 8),  hcc_le32(sample + 12)
    };
    uint32_t x[16];
    memcpy(x, init, sizeof(x));
    for (int i = 0; i < 10; i++) {
        HCC_QR(x[0], x[4], x[8],  x[12]);
        HCC_QR(x[1], x[5], x[9],  x[13]);
        HCC_QR(x[2], x[6], x[10], x[14]);
        HCC_QR(x[3], x[7], x[11], x[15]);
        HCC_QR(x[0], x[5], x[10], x[15]);
        HCC_QR(x[1], x[6], x[11], x[12]);
        HCC_QR(x[2], x[7], x[8],  x[13]);
        HCC_QR(x[3], x[4], x[9],  x[14]);
    }
    unsigned char blk[8];
    hcc_put_le32(blk + 0, x[0] + init[0]);
    hcc_put_le32(blk + 4, x[1] + init[1]);
    memcpy(mask, blk, 5);
    wc_ForceZero(x, sizeof(x));
    wc_ForceZero(init, sizeof(init));
    return 0;
}

/* ---- BLAKE2b-256 (RFC 7693) ----------------------------------------------------------------- */

static const uint64_t b2b_iv[8] = {
    0x6a09e667f3bcc908ULL, 0xbb67ae8584caa73bULL,
    0x3c6ef372fe94f82bULL, 0xa54ff53a5f1d36f1ULL,
    0x510e527fade682d1ULL, 0x9b05688c2b3e6c1fULL,
    0x1f83d9abfb41bd6bULL, 0x5be0cd19137e2179ULL
};

static const uint8_t b2b_sigma[12][16] = {
    {  0,  1,  2,  3,  4,  5,  6,  7,  8,  9, 10, 11, 12, 13, 14, 15 },
    { 14, 10,  4,  8,  9, 15, 13,  6,  1, 12,  0,  2, 11,  7,  5,  3 },
    { 11,  8, 12,  0,  5,  2, 15, 13, 10, 14,  3,  6,  7,  1,  9,  4 },
    {  7,  9,  3,  1, 13, 12, 11, 14,  2,  6,  5, 10,  4,  0, 15,  8 },
    {  9,  0,  5,  7,  2,  4, 10, 15, 14,  1, 11, 12,  6,  8,  3, 13 },
    {  2, 12,  6, 10,  0, 11,  8,  3,  4, 13,  7,  5, 15, 14,  1,  9 },
    { 12,  5,  1, 15, 14, 13,  4, 10,  0,  7,  6,  3,  9,  2,  8, 11 },
    { 13, 11,  7, 14, 12,  1,  3,  9,  5,  0, 15,  4,  8,  6,  2, 10 },
    {  6, 15, 14,  9, 11,  3,  0,  8, 12,  2, 13,  7,  1,  4, 10,  5 },
    { 10,  2,  8,  4,  7,  6,  1,  5, 15, 11,  9, 14,  3, 12, 13,  0 },
    {  0,  1,  2,  3,  4,  5,  6,  7,  8,  9, 10, 11, 12, 13, 14, 15 },
    { 14, 10,  4,  8,  9, 15, 13,  6,  1, 12,  0,  2, 11,  7,  5,  3 }
};

static inline uint64_t b2b_rotr64(uint64_t w, int c) { return (w >> c) | (w << (64 - c)); }
static inline uint64_t b2b_le64(const unsigned char *p) {
    return (uint64_t)hcc_le32(p) | ((uint64_t)hcc_le32(p + 4) << 32);
}

#define B2B_G(v, a, b, c, d, x, y) do { \
    (v)[a] = (v)[a] + (v)[b] + (x); (v)[d] = b2b_rotr64((v)[d] ^ (v)[a], 32); \
    (v)[c] = (v)[c] + (v)[d];       (v)[b] = b2b_rotr64((v)[b] ^ (v)[c], 24); \
    (v)[a] = (v)[a] + (v)[b] + (y); (v)[d] = b2b_rotr64((v)[d] ^ (v)[a], 16); \
    (v)[c] = (v)[c] + (v)[d];       (v)[b] = b2b_rotr64((v)[b] ^ (v)[c], 63); \
} while (0)

static void b2b_compress(uint64_t h[8], const unsigned char block[128], uint64_t t0, int last) {
    uint64_t v[16], m[16];
    for (int i = 0; i < 16; i++) m[i] = b2b_le64(block + i * 8);
    for (int i = 0; i < 8; i++)  v[i] = h[i];
    for (int i = 0; i < 8; i++)  v[i + 8] = b2b_iv[i];
    v[12] ^= t0;
    if (last) v[14] = ~v[14];
    for (int r = 0; r < 12; r++) {
        const uint8_t *s = b2b_sigma[r];
        B2B_G(v, 0, 4,  8, 12, m[s[ 0]], m[s[ 1]]);
        B2B_G(v, 1, 5,  9, 13, m[s[ 2]], m[s[ 3]]);
        B2B_G(v, 2, 6, 10, 14, m[s[ 4]], m[s[ 5]]);
        B2B_G(v, 3, 7, 11, 15, m[s[ 6]], m[s[ 7]]);
        B2B_G(v, 0, 5, 10, 15, m[s[ 8]], m[s[ 9]]);
        B2B_G(v, 1, 6, 11, 12, m[s[10]], m[s[11]]);
        B2B_G(v, 2, 7,  8, 13, m[s[12]], m[s[13]]);
        B2B_G(v, 3, 4,  9, 14, m[s[14]], m[s[15]]);
    }
    for (int i = 0; i < 8; i++) h[i] ^= v[i] ^ v[i + 8];
}

void sc_blake2b_256(unsigned char out[32], const void *in, size_t n) {
    uint64_t h[8];
    for (int i = 0; i < 8; i++) h[i] = b2b_iv[i];
    h[0] ^= 0x01010020ULL;
    const unsigned char *p = (const unsigned char *)in;
    uint64_t t0 = 0;
    while (n > 128) {
        t0 += 128;
        b2b_compress(h, p, t0, 0);
        p += 128;
        n -= 128;
    }
    unsigned char blk[128] = {0};
    if (n > 0) memcpy(blk, p, n);
    t0 += (uint64_t)n;
    b2b_compress(h, blk, t0, 1);
    for (int i = 0; i < 4; i++) {
        hcc_put_le32(out + i * 8,     (uint32_t)h[i]);
        hcc_put_le32(out + i * 8 + 4, (uint32_t)(h[i] >> 32));
    }
    wc_ForceZero(blk, sizeof(blk));
    wc_ForceZero(h, sizeof(h));
}

/* ---- AES-256-CTR ---------------------------------------------------------------------------- */

int sc_aesctr_init(struct sc_aesctr *c, const unsigned char key[32], const unsigned char iv[16]) {
    Aes *aes = (Aes *)c->st;
    c->ready = 0;
    if (wc_AesInit(aes, NULL, INVALID_DEVID) != 0) return SC_ECRYPTO;
    /* CTR always uses the encryption key: the keystream is the same in both directions. */
    if (wc_AesCtrSetKey(aes, key, 32, iv, AES_ENCRYPTION) != 0) { wc_AesFree(aes); return SC_ECRYPTO; }
    c->ready = 1;
    return 0;
}

int sc_aesctr_xor(struct sc_aesctr *c, const unsigned char *in, unsigned char *out, size_t n) {
    if (!c->ready || n > UINT32_MAX) return SC_EINVAL;
    if (!n) return 0;
    return wc_AesCtrEncrypt((Aes *)c->st, out, in, (word32)n) == 0 ? 0 : SC_ECRYPTO;
}

void sc_aesctr_free(struct sc_aesctr *c) {
    if (!c->ready) return;
    wc_AesFree((Aes *)c->st);
    wc_ForceZero(c->st, sizeof(Aes));
    c->ready = 0;
}

/* ---- AES, one block ------------------------------------------------------------------------- */

/* Aes is nearly a kilobyte, so it goes on the heap (see sc_hmac2). A direct block
 * (WOLFSSL_AES_DIRECT in user_settings.h) has no mode and no IV. */
int sc_aes_block(const unsigned char *key, size_t key_n, int decrypt,
                 const unsigned char in[16], unsigned char out[16]) {
    if (key_n != 16 && key_n != 32) return SC_EINVAL;
    Aes *aes = malloc(sizeof(*aes));
    if (!aes) return SC_ENOMEM;
    int rc = wc_AesInit(aes, NULL, INVALID_DEVID);
    if (rc == 0) {
        rc = wc_AesSetKey(aes, key, (word32)key_n, NULL, decrypt ? AES_DECRYPTION : AES_ENCRYPTION);
        if (rc == 0) rc = decrypt ? wc_AesDecryptDirect(aes, out, in) : wc_AesEncryptDirect(aes, out, in);
        wc_AesFree(aes);
    }
    wc_ForceZero(aes, sizeof(*aes));
    free(aes);
    return rc == 0 ? 0 : SC_ECRYPTO;
}

/* ---- X25519 --------------------------------------------------------------------------------- */

/* wolfSSL accepts only a clamped scalar (and refuses any other), while the X25519 function of
 * RFC 7748 clamps it itself, so a copy is clamped. For properly generated keys (reality.c clamps
 * its own) this changes nothing. */
static void clamp(unsigned char s[32], const unsigned char in[32]) {
    memcpy(s, in, 32);
    s[0] &= 248;
    s[31] &= 127;
    s[31] |= 64;
}

static int all_zero(const unsigned char *p, size_t n) {
    unsigned char acc = 0;
    for (size_t i = 0; i < n; i++) acc |= p[i];
    return acc == 0;
}

int sc_x25519(unsigned char out[32], const unsigned char scalar[32], const unsigned char point[32]) {
    unsigned char s[32];
    clamp(s, scalar);
    int rc = wc_curve25519_generic(32, out, 32, s, 32, point);
    wc_ForceZero(s, sizeof(s));
    if (rc != 0) return SC_ECRYPTO;
    /* A small-order point gives an all-zero secret known to anyone (RFC 7748 §6.1). */
    if (all_zero(out, 32)) return SC_ECRYPTO;
    return 0;
}

int sc_x25519_base(unsigned char pub[32], const unsigned char scalar[32]) {
    unsigned char s[32];
    clamp(s, scalar);
    int rc = wc_curve25519_make_pub(32, pub, 32, s);
    wc_ForceZero(s, sizeof(s));
    return rc == 0 ? 0 : SC_ECRYPTO;
}

int sc_p256_pub(const unsigned char seed[32], unsigned char pub[65]) {
    ecc_key *key = malloc(sizeof(*key));
    if (!key) return SC_ENOMEM;
    if (wc_ecc_init(key) != 0) { free(key); return SC_ECRYPTO; }
    word32 n = 65;
    int rc = SC_EINVAL;
    if (wc_ecc_import_private_key_ex(seed, 32, NULL, 0, key, ECC_SECP256R1) == 0 &&
        wc_ecc_make_pub(key, NULL) == 0)
        rc = wc_ecc_export_x963(key, pub, &n) == 0 && n == 65 ? 0 : SC_ECRYPTO;
    wc_ecc_free(key);
    wc_ForceZero(key, sizeof(*key));
    free(key);
    return rc;
}

void sc_wipe(void *p, size_t n) { wc_ForceZero(p, (word32)n); }

int sc_p256_on_curve(const unsigned char pub[65]) {
    ecc_key *key = malloc(sizeof(*key));
    if (!key) return SC_ENOMEM;
    if (wc_ecc_init(key) != 0) { free(key); return SC_ECRYPTO; }
    int rc = wc_ecc_import_x963_ex(pub, 65, key, ECC_SECP256R1) == 0 && wc_ecc_check_key(key) == 0
                 ? 0 : SC_EINVAL;
    wc_ecc_free(key);
    free(key);
    return rc;
}

/* ---- signatures ----------------------------------------------------------------------------- */

/* Compare without early exit. Signatures and digests here are not secret, but this file should
 * not get into the habit of comparing cryptographic values with memcmp. */
static int ct_equal(const unsigned char *a, const unsigned char *b, size_t n) {
    unsigned char d = 0;
    for (size_t i = 0; i < n; i++) d |= (unsigned char)(a[i] ^ b[i]);
    return d == 0;
}

/* ---- ML-KEM-768 ----------------------------------------------------------------------------- */

/* The key lives for the call only, on the heap (wc_MlKemKey_New), not by value in the caller's
 * struct, so its size does not bloat struct tls13. The cost is one malloc per handshake, nothing
 * next to ML-KEM itself. */
static MlKemKey *kem_new(void) {
    return wc_MlKemKey_New(WC_ML_KEM_768, NULL, INVALID_DEVID);
}

int sc_mlkem768_keygen(unsigned char ek[SC_MLKEM768_EK], unsigned char dk[SC_MLKEM768_DK],
                       const unsigned char seed[SC_MLKEM768_SEED]) {
    MlKemKey *k = kem_new();
    if (!k) return SC_ENOMEM;
    int rc = SC_ECRYPTO;
    if (wc_MlKemKey_MakeKeyWithRandom(k, seed, SC_MLKEM768_SEED) == 0 &&
        wc_MlKemKey_EncodePublicKey(k, ek, SC_MLKEM768_EK) == 0 &&
        wc_MlKemKey_EncodePrivateKey(k, dk, SC_MLKEM768_DK) == 0)
        rc = 0;
    wc_MlKemKey_Delete(k, &k);
    return rc;
}

int sc_mlkem768_ek_check(const unsigned char ek[SC_MLKEM768_EK]) {
    MlKemKey *k = kem_new();
    if (!k) return SC_ENOMEM;
    unsigned char back[SC_MLKEM768_EK];
    int rc = SC_EPARSE;
    /* wolfSSL's DecodePublicKey checks the coefficients (< q), but the check is confirmed by a
     * round trip through encoding: that is exactly how Go checks (FIPS 203, 7.2), and a mismatch
     * would mean accepting a key the server rejects. */
    if (wc_MlKemKey_DecodePublicKey(k, ek, SC_MLKEM768_EK) == 0 &&
        wc_MlKemKey_EncodePublicKey(k, back, sizeof back) == 0 &&
        ct_equal(ek, back, sizeof back))
        rc = 0;
    wc_MlKemKey_Delete(k, &k);
    return rc;
}

int sc_mlkem768_encaps(unsigned char ct[SC_MLKEM768_CT], unsigned char ss[SC_MLKEM768_SS],
                       const unsigned char ek[SC_MLKEM768_EK], const unsigned char rnd[SC_MLKEM768_RND]) {
    MlKemKey *k = kem_new();
    if (!k) return SC_ENOMEM;
    int rc = SC_EPARSE;
    unsigned char back[SC_MLKEM768_EK];
    if (wc_MlKemKey_DecodePublicKey(k, ek, SC_MLKEM768_EK) == 0 &&
        wc_MlKemKey_EncodePublicKey(k, back, sizeof back) == 0 && ct_equal(ek, back, sizeof back))
        rc = wc_MlKemKey_EncapsulateWithRandom(k, ct, ss, rnd, SC_MLKEM768_RND) == 0 ? 0 : SC_ECRYPTO;
    wc_MlKemKey_Delete(k, &k);
    return rc;
}

int sc_mlkem768_decaps(unsigned char ss[SC_MLKEM768_SS], const unsigned char dk[SC_MLKEM768_DK],
                       const unsigned char ct[SC_MLKEM768_CT]) {
    MlKemKey *k = kem_new();
    if (!k) return SC_ENOMEM;
    int rc = SC_EPARSE;
    if (wc_MlKemKey_DecodePrivateKey(k, dk, SC_MLKEM768_DK) == 0)
        rc = wc_MlKemKey_Decapsulate(k, ss, ct, SC_MLKEM768_CT) == 0 ? 0 : SC_ECRYPTO;
    wc_MlKemKey_Delete(k, &k);
    return rc;
}

/* ---- ML-DSA-65: verification only ----------------------------------------------------------- */

int sc_mldsa65_verify(const unsigned char pk[SC_MLDSA65_PK], const unsigned char *msg, size_t msg_n,
                      const unsigned char *sig, size_t sig_n) {
    if (sig_n != SC_MLDSA65_SIG) return SC_ESIG;
    wc_MlDsaKey *k = wc_MlDsaKey_New(NULL, INVALID_DEVID);
    if (!k) return SC_ENOMEM;
    int rc = SC_EPARSE, res = 0;
    if (wc_MlDsaKey_SetParams(k, WC_ML_DSA_65) == 0 &&
        wc_MlDsaKey_ImportPubRaw(k, pk, SC_MLDSA65_PK) == 0) {
        int vr = wc_MlDsaKey_VerifyCtx(k, sig, (word32)sig_n, NULL, 0, msg, (word32)msg_n, &res);
        rc = (vr == 0 && res == 1) ? 0 : SC_ESIG;
    }
    wc_MlDsaKey_Delete(k, &k);
    return rc;
}


static int mgf_of(enum sc_hash h) {
    switch (h) {
        case SC_SHA256: return WC_MGF1SHA256;
        case SC_SHA384: return WC_MGF1SHA384;
        case SC_SHA512: return WC_MGF1SHA512;
        default: break;
    }
    return WC_MGF1NONE;
}

static int verify_rsa(const unsigned char *spki, word32 spki_n, enum sc_sig_alg alg,
                      enum sc_hash h, const unsigned char *digest, size_t dn,
                      const unsigned char *sig, size_t sig_n) {
    RsaKey *key = malloc(sizeof(*key));
    /* The public operation's output is as long as the modulus; 1024 bytes allow keys up to 8192
     * bits. */
    unsigned char *out = malloc(1024);
    int rc = SC_ENOMEM;
    if (!key || !out) goto done;
    rc = SC_EPARSE;
    if (wc_InitRsaKey(key, NULL) != 0) { free(key); key = NULL; goto done; }
    word32 idx = 0;
    if (wc_RsaPublicKeyDecode(spki, &idx, key, spki_n) != 0) goto done_key;
    rc = SC_ESIG;
    if (alg == SC_SIG_RSA_PSS) {
        int n = wc_RsaPSS_Verify_ex(sig, (word32)sig_n, out, 1024, wc_htype(h), mgf_of(h),
                                    RSA_PSS_SALT_LEN_DISCOVER, key);
        if (n > 0 && wc_RsaPSS_CheckPadding_ex2(digest, (word32)dn, out, (word32)n, wc_htype(h),
                                                RSA_PSS_SALT_LEN_DISCOVER,
                                                wc_RsaEncryptSize(key) * 8, NULL) == 0)
            rc = 0;
    } else {
        /* PKCS#1 v1.5: the public operation must yield exactly the DigestInfo with our hash's
         * OID. Build the expected one and compare it whole, rather than look for the digest at
         * the tail. */
        unsigned char want[SC_HASH_MAX + 32];
        int n = wc_RsaSSL_Verify(sig, (word32)sig_n, out, 1024, key);
        word32 wn = wc_EncodeSignature(want, digest, (word32)dn, wc_HashGetOID(wc_htype(h)));
        if (n > 0 && wn > 0 && (word32)n == wn && ct_equal(out, want, wn)) rc = 0;
    }
done_key:
    wc_FreeRsaKey(key);
done:
    free(key);
    free(out);
    return rc;
}

static int verify_ecdsa(const unsigned char *spki, word32 spki_n,
                        const unsigned char *digest, size_t dn,
                        const unsigned char *sig, size_t sig_n) {
    ecc_key *key = malloc(sizeof(*key));
    int rc = SC_ENOMEM, ok = 0;
    if (!key) return rc;
    rc = SC_EPARSE;
    if (wc_ecc_init(key) != 0) { free(key); return rc; }
    word32 idx = 0;
    if (wc_EccPublicKeyDecode(spki, &idx, key, spki_n) == 0) {
        rc = SC_ESIG;
        if (wc_ecc_verify_hash(sig, (word32)sig_n, digest, (word32)dn, &ok, key) == 0 && ok == 1)
            rc = 0;
    }
    wc_ecc_free(key);
    free(key);
    return rc;
}

int sc_cert_verify_sig(const unsigned char *cert_der, size_t cert_n,
                       enum sc_sig_alg alg, enum sc_hash h,
                       const unsigned char *digest, size_t digest_n,
                       const unsigned char *sig, size_t sig_n) {
    if (!cert_der || !digest || !sig || cert_n > UINT32_MAX || sig_n > 4096 ||
        digest_n != sc_hash_len(h) || !digest_n)
        return SC_EINVAL;
    /* The key comes from the certificate's SubjectPublicKeyInfo: there is no need to parse the
     * whole certificate, and both key decoders understand SPKI. Keys over 8192 bits are not
     * supported: no node or root has one. */
    word32 spki_n = 2048;
    unsigned char *spki = malloc(spki_n);
    if (!spki) return SC_ENOMEM;
    int rc;
    if (wc_GetSubjectPubKeyInfoDerFromCert(cert_der, (word32)cert_n, spki, &spki_n) != 0)
        rc = SC_EPARSE;
    else if (alg == SC_SIG_ECDSA)
        rc = verify_ecdsa(spki, spki_n, digest, digest_n, sig, sig_n);
    else if (alg == SC_SIG_RSA_PSS || alg == SC_SIG_RSA_PKCS1)
        rc = verify_rsa(spki, spki_n, alg, h, digest, digest_n, sig, sig_n);
    else
        rc = SC_EINVAL;
    free(spki);
    return rc;
}

/* ---- root store and chain ----------------------------------------------------------------
 *
 * wolfSSL_X509_verify_cert builds and checks the chain; the roots are held by the CertManager
 * inside WOLFSSL_X509_STORE. A bare CertManager does not do: it checks ONE certificate against
 * what it already holds, and the intermediates come from the server and are not trusted. Adding
 * them would either pollute the shared store for good or mean building the path ourselves, with
 * our own checks of the CA flag, keyCertSign, path length and name constraints: a reimplementation
 * of what the library has and has already fixed (name constraints in CVE-2026-89133).
 * verify_cert does all of it, including the name (SAN, IP).
 *
 * The cost is one check at a time: verify_cert adds the intermediates to the store's CertManager
 * as temporary and removes ALL temporary ones when done, so two concurrent checks on one store
 * would interfere (x509_str.c says so). Hence the mutex, taken only by chain checks that miss the
 * verified-chain cache (certverify.c), while a store per thread would cost a hundred KB of roots
 * per connector. */
struct sc_roots {
    WOLFSSL_X509_STORE *store;
    pthread_mutex_t mu;
};

static pthread_once_t g_init_once = PTHREAD_ONCE_INIT;
static int g_init_rc = -1;
static void lib_init(void) { g_init_rc = wolfSSL_Init() == WOLFSSL_SUCCESS ? 0 : -1; }

int sc_roots_load(struct sc_roots **out, const unsigned char *pem, size_t n) {
    *out = NULL;
    if (!pem || !n || n > INT32_MAX) return SC_EINVAL;
    pthread_once(&g_init_once, lib_init);
    if (g_init_rc != 0) return SC_ECRYPTO;
    struct sc_roots *r = calloc(1, sizeof(*r));
    if (!r) return SC_ENOMEM;
    r->store = wolfSSL_X509_STORE_new();
    if (!r->store) { free(r); return SC_ENOMEM; }
    pthread_mutex_init(&r->mu, NULL);
    /* IGNORE_ERR: an entry that does not parse is skipped, the rest load (see scrypto.h).
     * wolfSSL does not report how many roots loaded; "none" shows as a failure of the call. */
    int rc = wolfSSL_CertManagerLoadCABuffer_ex(r->store->cm, pem, (long)n, WOLFSSL_FILETYPE_PEM,
                                                0, WOLFSSL_LOAD_FLAG_IGNORE_ERR);
    if (rc != WOLFSSL_SUCCESS) { sc_roots_free(r); return SC_EPARSE; }
    *out = r;
    return 0;
}

/* A store of ONE DER certificate: a CA pinned by fingerprint (Xray's pinnedPeerCertSha256, when
 * the fingerprint matches an intermediate or the root of the chain). Unlike sc_roots_load, the
 * entry must parse: a certificate that cannot be a CA (no CA flag, an algorithm not in the
 * build) fails with SC_EPARSE, and the caller treats it as "the fingerprint found no CA", just
 * as Xray does (its verifyChain pins only when cert.IsCA). */
int sc_roots_load_der(struct sc_roots **out, const unsigned char *der, size_t n) {
    *out = NULL;
    if (!der || !n || n > INT32_MAX) return SC_EINVAL;
    pthread_once(&g_init_once, lib_init);
    if (g_init_rc != 0) return SC_ECRYPTO;
    struct sc_roots *r = calloc(1, sizeof(*r));
    if (!r) return SC_ENOMEM;
    r->store = wolfSSL_X509_STORE_new();
    if (!r->store) { free(r); return SC_ENOMEM; }
    pthread_mutex_init(&r->mu, NULL);
    int rc = wolfSSL_CertManagerLoadCABuffer(r->store->cm, der, (long)n, WOLFSSL_FILETYPE_ASN1);
    if (rc != WOLFSSL_SUCCESS) { sc_roots_free(r); return SC_EPARSE; }
    *out = r;
    return 0;
}

void sc_roots_free(struct sc_roots *r) {
    if (!r) return;
    wolfSSL_X509_STORE_free(r->store);
    pthread_mutex_destroy(&r->mu);
    free(r);
}

static int asn1_epoch(const WOLFSSL_ASN1_TIME *t, int64_t *out) {
    struct tm tm;
    if (!t || wolfSSL_ASN1_TIME_to_tm(t, &tm) != 1) return -1;
    int64_t y = (int64_t)tm.tm_year + 1900, m = tm.tm_mon + 1;
    if (m <= 2) y--;
    int64_t era = (y >= 0 ? y : y - 399) / 400;
    int64_t yoe = y - era * 400;
    int64_t doy = (153 * (m > 2 ? m - 3 : m + 9) + 2) / 5 + tm.tm_mday - 1;
    int64_t doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    int64_t days = era * 146097 + doe - 719468;
    *out = days * 86400 + tm.tm_hour * 3600 + tm.tm_min * 60 + tm.tm_sec;
    return 0;
}

static void chain_window(WOLFSSL_X509_STORE_CTX *ctx, int64_t *nb, int64_t *na) {
    WOLF_STACK_OF(WOLFSSL_X509) *path = wolfSSL_X509_STORE_CTX_get_chain(ctx);
    int n = path ? wolfSSL_sk_X509_num(path) : 0;
    int64_t lo = INT64_MIN, hi = INT64_MAX;
    for (int i = 0; i < n; i++) {
        WOLFSSL_X509 *x = wolfSSL_sk_X509_value(path, i);
        int64_t b, a;
        if (!x || asn1_epoch(wolfSSL_X509_get_notBefore(x), &b) != 0 ||
            asn1_epoch(wolfSSL_X509_get_notAfter(x), &a) != 0) {
            *nb = INT64_MAX;
            *na = INT64_MIN;
            return;
        }
        if (b > lo) lo = b;
        if (a < hi) hi = a;
    }
    if (n <= 0) { lo = INT64_MAX; hi = INT64_MIN; }
    *nb = lo;
    *na = hi;
}

int sc_cert_window(const unsigned char *der, size_t n, int64_t *nb, int64_t *na) {
    if (!der || !n || n > INT32_MAX || !nb || !na) return SC_EINVAL;
    pthread_once(&g_init_once, lib_init);
    if (g_init_rc != 0) return SC_ECRYPTO;
    WOLFSSL_X509 *x = wolfSSL_X509_d2i(NULL, der, (int)n);
    if (!x) return SC_EPARSE;
    int64_t b, a;
    int rc = asn1_epoch(wolfSSL_X509_get_notBefore(x), &b) == 0 &&
             asn1_epoch(wolfSSL_X509_get_notAfter(x), &a) == 0 ? 0 : SC_EPARSE;
    wolfSSL_X509_free(x);
    if (rc == 0) { *nb = b; *na = a; }
    return rc;
}

int sc_chain_verify(struct sc_roots *r, const unsigned char *const *der, const size_t *der_n,
                    size_t count, const char *host) {
    return sc_chain_verify_ex(r, der, der_n, count, host, NULL, NULL);
}

int sc_chain_verify_ex(struct sc_roots *r, const unsigned char *const *der, const size_t *der_n,
                       size_t count, const char *host, int64_t *nb, int64_t *na) {
    if (!r || !der || !der_n || !count || !host || !host[0]) return SC_EINVAL;
    for (size_t i = 0; i < count; i++) if (!der[i] || !der_n[i] || der_n[i] > INT32_MAX) return SC_EINVAL;

    WOLFSSL_X509 *leaf = wolfSSL_X509_d2i(NULL, der[0], (int)der_n[0]);
    if (!leaf) return SC_EPARSE;
    int rc = SC_ENOMEM;
    WOLF_STACK_OF(WOLFSSL_X509) *sk = wolfSSL_sk_X509_new_null();
    WOLFSSL_X509_STORE_CTX *ctx = wolfSSL_X509_STORE_CTX_new();
    if (!sk || !ctx) goto out;
    /* An intermediate that does not parse is skipped rather than failing the check: chains often
     * come with extras, and an extra certificate with an unknown algorithm decides nothing. If it
     * was needed, no path to a root is built, and the chain fails honestly. */
    for (size_t i = 1; i < count; i++) {
        WOLFSSL_X509 *x = wolfSSL_X509_d2i(NULL, der[i], (int)der_n[i]);
        if (x && wolfSSL_sk_X509_push(sk, x) <= 0) wolfSSL_X509_free(x);
    }

    pthread_mutex_lock(&r->mu);
    rc = SC_ECHAIN;
    if (wolfSSL_X509_STORE_CTX_init(ctx, r->store, leaf, sk) == WOLFSSL_SUCCESS) {
        WOLFSSL_X509_VERIFY_PARAM *param = wolfSSL_X509_STORE_CTX_get0_param(ctx);
        /* An address is matched against SAN IP, a name against SAN DNS (and CN when there is
         * no SAN). */
        unsigned char ip[16];
        int is_ip = inet_pton(AF_INET, host, ip) == 1 || inet_pton(AF_INET6, host, ip) == 1;
        int set = param && (is_ip ? wolfSSL_X509_VERIFY_PARAM_set1_ip_asc(param, host)
                                  : wolfSSL_X509_VERIFY_PARAM_set1_host(param, host, strlen(host)));
        if (set == WOLFSSL_SUCCESS && wolfSSL_X509_verify_cert(ctx) == WOLFSSL_SUCCESS) {
            rc = 0;
            if (nb && na) chain_window(ctx, nb, na);
        }
    }
    pthread_mutex_unlock(&r->mu);
out:
    if (ctx) wolfSSL_X509_STORE_CTX_free(ctx);
    if (sk) wolfSSL_sk_X509_pop_free(sk, wolfSSL_X509_free);
    wolfSSL_X509_free(leaf);
    return rc;
}

void sc_blake2b256(const uint8_t *in, size_t n, uint8_t out[32]) {
    Blake2b b;
    (void)wc_InitBlake2b(&b, 32);
    (void)wc_Blake2bUpdate(&b, in, (word32)n);
    (void)wc_Blake2bFinal(&b, out, 32);
}

/* ---- BLAKE3 --------------------------------------------------------------------------------- */

void sc_blake3_hash(unsigned char out[32], const void *in, size_t n) { b3_hash(out, in, n); }

void sc_blake3_derive_key(unsigned char *out, size_t out_n, const void *ctx, size_t ctx_n,
                          const void *material, size_t material_n) {
    b3_derive_key(out, out_n, ctx, ctx_n, material, material_n);
}
