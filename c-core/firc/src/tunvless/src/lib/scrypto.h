/* The cryptographic primitives layer: everything the protocols need from the crypto library.
 *
 * Protocol code (src/proto) does not call the crypto library itself: it calls these functions,
 * with wolfCrypt behind them. The library is visible in exactly one file, scrypto.c, so
 * replacing it means editing one file under an unchanged header.
 *
 * No library types here, and that is a promise: tls13.h includes this header, and through it so
 * do unit tests built without the crypto library (neither its headers nor its objects). Such a
 * test supplies its own sc_* functions where it needs them.
 *
 * Contexts are opaque and of fixed size, not allocated by the library:
 *
 *   - struct tls13 and struct tls13_keys hold contexts by value and themselves live inside
 *     connections, in the tunnel's static tables and on the handshake stack. Allocating per key
 *     would add a malloc per direction, an out-of-memory path in a dozen places and a leak at
 *     every missed free. Storage inside the struct cannot be lost: it has no pointer;
 *   - the storage size is a constant of this header, and scrypto.c checks it against the real
 *     sizeof of the wolfSSL type at compile time (_Static_assert). If a new wolfSSL version or a
 *     new option in build/wolfssl/user_settings.h grows the struct, the build fails instead of
 *     corrupting the next field at run time;
 *   - the alignment (16 bytes) is declared on the storage itself, so it holds wherever a context
 *     lives, the stack included.
 *
 * Threads: each context belongs to one thread; functions without a context (sc_hash, sc_hmac,
 * sc_hkdf_*, sc_x25519*) are safe from any. The root store (struct sc_roots) is shared by the
 * process; chain verification serializes internally.
 *
 * Return codes: 0 on success, a negative code from the list below on failure. Callers map them
 * to their own (TLS13_ECRYPTO, CERTV_* ...): the layer need not know what the protocol calls a
 * failure. */
#ifndef STEER_SCRYPTO_H
#define STEER_SCRYPTO_H
#include <stddef.h>
#include <stdint.h>

#define SC_EINVAL   (-1)    /* bad argument: length, algorithm, key not set up */
#define SC_EAUTH    (-2)    /* AEAD tag mismatch: data altered or wrong key */
#define SC_ECRYPTO  (-3)    /* the library failed where it should not */
#define SC_EPARSE   (-4)    /* a certificate or key does not parse */
#define SC_ECHAIN   (-5)    /* no chain to the roots, expired, or a wrong name */
#define SC_ESIG     (-6)    /* bad signature */
#define SC_ENOMEM   (-7)

/* Context storage alignment: what AES-NI and the ARMv8 Crypto extension need for round keys. */
#define SC_ALIGN _Alignas(16)

/* ---- hashes ------------------------------------------------------------------------------- */

/* SHA-1, SHA-224 and MD5 exist for wire formats of other protocols, not for our own
 * cryptography; nothing in src uses them, only tests/scryptomatch.c. */
enum sc_hash { SC_SHA256 = 1, SC_SHA384 = 2, SC_SHA512 = 3, SC_SHA1 = 4, SC_SHA224 = 5, SC_MD5 = 6 };
#define SC_HASH_MAX 64
/* Output length: 32, 48, 64, 20, 28 or 16; 0 for an unknown algorithm. */
size_t sc_hash_len(enum sc_hash h);

/* Storage size: wolfSSL's SHA-512 (also used for SHA-384) is 224-232 bytes depending on
 * acceleration; the margin is for library updates, checked in scrypto.c. */
#define SC_HASH_CTX_SIZE 320
struct sc_hash_ctx {
    int alg;                                   /* enum sc_hash; 0 if not set up */
    SC_ALIGN unsigned char st[SC_HASH_CTX_SIZE];
};

/* Streaming hash. clone serves the TLS 1.3 transcript: a hash is taken at each key schedule
 * step while the transcript goes on. final does not free the context: afterwards it is set up
 * anew, as after init (as in wolfSSL). free is required exactly once per init or clone. */
int  sc_hash_init(struct sc_hash_ctx *c, enum sc_hash h);
int  sc_hash_update(struct sc_hash_ctx *c, const void *d, size_t n);
int  sc_hash_final(struct sc_hash_ctx *c, unsigned char *out);
int  sc_hash_clone(struct sc_hash_ctx *dst, const struct sc_hash_ctx *src);
void sc_hash_free(struct sc_hash_ctx *c);
int  sc_hash(enum sc_hash h, const void *d, size_t n, unsigned char *out);

/* One-call HMAC (RFC 2104). Two message pieces in a row: joining them in a caller's buffer
 * would cost a copy and a length limit.
 * msg2 may be NULL when n2 == 0. */
int sc_hmac(enum sc_hash h, const void *key, size_t key_n,
            const void *msg, size_t n, unsigned char *out);
int sc_hmac2(enum sc_hash h, const void *key, size_t key_n,
             const void *msg, size_t n, const void *msg2, size_t n2, unsigned char *out);

/* HKDF (RFC 5869). extract writes sc_hash_len(h) bytes; an empty salt is hash-length zeros, as
 * in the RFC. */
int sc_hkdf_extract(enum sc_hash h, const void *salt, size_t salt_n,
                    const void *ikm, size_t ikm_n, unsigned char *prk);
int sc_hkdf_expand(enum sc_hash h, const void *prk, size_t prk_n,
                   const void *info, size_t info_n, unsigned char *out, size_t out_n);
int sc_hkdf(enum sc_hash h, const void *salt, size_t salt_n, const void *ikm, size_t ikm_n,
            const void *info, size_t info_n, unsigned char *out, size_t out_n);

/* PBKDF2-HMAC-SHA256 (RFC 8018 §5.2). */
int sc_pbkdf2_sha256(const void *pass, size_t pass_n, const void *salt, size_t salt_n,
                     int iter, unsigned char *out, size_t out_n);

/* ---- AEAD --------------------------------------------------------------------------------- */

enum sc_aead_alg { SC_AES128_GCM = 1, SC_AES256_GCM = 2, SC_CHACHA20_POLY1305 = 3 };

/* The key is set up once (sc_aead_setkey), not per record: for AES-GCM that is the key schedule
 * and the GHASH table, fixed work that dominates on small records (Vision sends about 300
 * bytes). ChaCha20-Poly1305 has nothing to expand and stores the key itself. wolfSSL's AES
 * context (Aes) is 864-896 bytes; the margin is for library updates. */
#define SC_AEAD_CTX_SIZE 1152
struct sc_aead {
    int alg;                                   /* enum sc_aead_alg; 0 if no key */
    SC_ALIGN unsigned char st[SC_AEAD_CTX_SIZE];
};
/* Key length of the algorithm: 16 or 32. The nonce is always 12 bytes, the tag always 16. */
size_t sc_aead_key_len(enum sc_aead_alg a);
int    sc_aead_setkey(struct sc_aead *k, enum sc_aead_alg a, const unsigned char *key);
void   sc_aead_free(struct sc_aead *k);
/* In place: buf is encrypted (decrypted) where it lies. The tag has its own pointer and open
 * only reads it: in TLS the tag follows the ciphertext, and the caller copies it rather than
 * depend on whether the implementation touches neighbouring bytes on the last block. */
int sc_aead_seal(struct sc_aead *k, const unsigned char nonce[12],
                 const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                 unsigned char tag[16]);
int sc_aead_open(struct sc_aead *k, const unsigned char nonce[12],
                 const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                 const unsigned char tag[16]);

/* XChaCha20-Poly1305 (draft-irtf-cfrg-xchacha): 32-byte key, 24-byte nonce, 16-byte tag. */
int sc_xchacha20_poly1305_seal(const unsigned char key[32], const unsigned char nonce[24],
                               const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                               unsigned char tag[16]);
int sc_xchacha20_poly1305_open(const unsigned char key[32], const unsigned char nonce[24],
                               const void *aad, size_t aad_n, unsigned char *buf, size_t n,
                               const unsigned char tag[16]);

/* ChaCha20 QUIC header protection (RFC 9001 §5.4.4): sample[0..3] is LE32 counter,
 * sample[4..15] is 12-byte nonce, outputs 5-byte mask. */
int sc_chacha20_hp(const unsigned char key[32], const unsigned char sample[16],
                   unsigned char mask[5]);

/* BLAKE2b-256 (RFC 7693): unkeyed 32-byte digest (used by Hysteria2 Salamander obfuscation). */
void sc_blake2b_256(unsigned char out[32], const void *in, size_t n);

/* ---- AES-256-CTR -------------------------------------------------------------------------- */

/* A continuous keystream (VLESS encryption's xorpub/random, NewCTR in trvenc.c). The context
 * remembers the position inside a block, so consecutive pieces of any length give the same
 * keystream as one large piece. The counter is a 128-bit big-endian number. */
#define SC_AESCTR_CTX_SIZE 1152
struct sc_aesctr {
    int ready;
    SC_ALIGN unsigned char st[SC_AESCTR_CTX_SIZE];
};
int  sc_aesctr_init(struct sc_aesctr *c, const unsigned char key[32], const unsigned char iv[16]);
int  sc_aesctr_xor(struct sc_aesctr *c, const unsigned char *in, unsigned char *out, size_t n);
void sc_aesctr_free(struct sc_aesctr *c);

/* ---- AES, one block (ECB) ----------------------------------------------------------------- */

/* One AES-128 or AES-256 block (key_n 16 or 32), encrypted or decrypted, for protocols that
 * hide a short header in one block. No context: the key is expanded on each call, which is
 * cheap at one block per connection or datagram, while storage per connection would cost a
 * kilobyte. Nothing in src uses it, only tests/scryptomatch.c. */
int sc_aes_block(const unsigned char *key, size_t key_n, int decrypt,
                 const unsigned char in[16], unsigned char out[16]);

/* ---- X25519 (RFC 7748) -------------------------------------------------------------------- */

/* The X25519 function of RFC 7748 §5: the scalar is clamped inside (on a copy), as in Go,
 * WireGuard and the RFC itself, so the RFC vectors pass as they are. An all-zero shared secret
 * (a small-order peer point) fails with SC_ECRYPTO: such a secret is known to everyone. */
int sc_x25519(unsigned char out[32], const unsigned char scalar[32], const unsigned char point[32]);
/* The public half: X25519(scalar, 9). */
int sc_x25519_base(unsigned char pub[32], const unsigned char scalar[32]);
int sc_p256_pub(const unsigned char seed[32], unsigned char pub[65]);
/* Zeroes n bytes at p in a way the compiler does not drop. */
void sc_wipe(void *p, size_t n);
/* 0 when pub is an uncompressed SEC1 point on P-256, else an error. */
int sc_p256_on_curve(const unsigned char pub[65]);

/* ---- ML-KEM-768 and ML-DSA-65 (FIPS 203 and 204) ------------------------------------------ */

/* The post-quantum part of parity with Xray-core: half of the X25519MLKEM768 hybrid in TLS 1.3
 * (the Chrome 131+ ClientHello, the REALITY server's answer), the "mlkem768x25519plus" exchange
 * of VLESS encryption and the ML-DSA-65 signature check of the REALITY certificate
 * (`mldsa65Verify`, `pqv` in the link).
 *
 * Keys are bytes, not opaque contexts. A wolfSSL ML-KEM key takes several kilobytes and expands a
 * matrix; holding one by value in struct tls13 (like AEAD) would bloat every connection for an
 * operation done once per handshake. So each function sets the key up on the heap for the call
 * and frees it; the caller keeps only the encoded decapsulation key (SC_MLKEM768_DK). Byte
 * layout per FIPS 203: ek as in Go (crypto/mlkem.EncapsulationKey768.Bytes), ct 1088 bytes,
 * ss 32.
 *
 * The caller supplies the randomness, so the Hello of the freeze test (tests/hellofreeze.c)
 * stays reproducible: it replaces the one source of random bytes, not library internals. */
#define SC_MLKEM768_EK  1184
#define SC_MLKEM768_DK  2400
#define SC_MLKEM768_CT  1088
#define SC_MLKEM768_SS  32
#define SC_MLKEM768_SEED 64     /* d || z, FIPS 203 ML-KEM.KeyGen_internal */
#define SC_MLKEM768_RND 32      /* m, FIPS 203 ML-KEM.Encaps_internal */

/* A key pair from seed. dk is the encoded private key (for sc_mlkem768_decaps). */
int sc_mlkem768_keygen(unsigned char ek[SC_MLKEM768_EK], unsigned char dk[SC_MLKEM768_DK],
                       const unsigned char seed[SC_MLKEM768_SEED]);
/* Checks a key from the peer (FIPS 203, 7.2): every coefficient below q. Go rejects other keys
 * in NewEncapsulationKey768, and so do we. SC_EPARSE if it is not a valid key. */
int sc_mlkem768_ek_check(const unsigned char ek[SC_MLKEM768_EK]);
int sc_mlkem768_encaps(unsigned char ct[SC_MLKEM768_CT], unsigned char ss[SC_MLKEM768_SS],
                       const unsigned char ek[SC_MLKEM768_EK], const unsigned char rnd[SC_MLKEM768_RND]);
/* Decapsulation. ML-KEM does not reject a bad ciphertext (implicit rejection): it returns a
 * pseudorandom secret, and the AEAD detects the mismatch. */
int sc_mlkem768_decaps(unsigned char ss[SC_MLKEM768_SS], const unsigned char dk[SC_MLKEM768_DK],
                       const unsigned char ct[SC_MLKEM768_CT]);

#define SC_MLDSA65_PK   1952
#define SC_MLDSA65_SIG  3309
/* Verifies an ML-DSA-65 signature with an empty context (as mldsa65.Verify(pk, msg, nil, sig)
 * in circl). 0 if valid, SC_ESIG if not, SC_EPARSE on a bad key or length. */
int sc_mldsa65_verify(const unsigned char pk[SC_MLDSA65_PK], const unsigned char *msg, size_t msg_n,
                      const unsigned char *sig, size_t sig_n);

/* ---- BLAKE3 (VLESS encryption) ------------------------------------------------------------ */

/* BLAKE3, needed only by VLESS encryption: Xray uses it to derive the AEAD keys (DeriveKey with
 * a string context) and the xorpub/random keystream keys, and to hash the relay keys. wolfSSL has
 * none, so it is our own port of the reference implementation (src/lib/blake3.h). Any input
 * length works: the NewAEAD context (a 1216-byte public key) spans more than one chunk and goes
 * through the tree. tests/b3match.c checks it against lukechampine.com/blake3, the library
 * Xray-core uses. */
void sc_blake3_hash(unsigned char out[32], const void *in, size_t n);
/* blake3.DeriveKey(out, context, material) of lukechampine.com/blake3: the derive_key mode, the
 * context is arbitrary bytes. out_n is at most 64. */
void sc_blake3_derive_key(unsigned char *out, size_t out_n, const void *ctx, size_t ctx_n,
                          const void *material, size_t material_n);

void sc_blake2b256(const uint8_t *in, size_t n, uint8_t out[32]);

/* ---- signatures and certificates ---------------------------------------------------------- */

enum sc_sig_alg { SC_SIG_RSA_PKCS1 = 1, SC_SIG_RSA_PSS = 2, SC_SIG_ECDSA = 3 };

/* Verifies a signature over a ready digest with the key of a certificate (DER).
 *
 * PSS: any salt length, MGF1 with the same hash (RFC 8446 §4.2.3). ECDSA: a DER (r, s)
 * signature, curve P-256 or P-384 from the certificate. PKCS#1 v1.5: DigestInfo with the hash
 * OID. The certificate is parsed only for its key: its authenticity is sc_chain_verify's job. */
int sc_cert_verify_sig(const unsigned char *cert_der, size_t cert_n,
                       enum sc_sig_alg alg, enum sc_hash h,
                       const unsigned char *digest, size_t digest_n,
                       const unsigned char *sig, size_t sig_n);

/* The root store: parsed once per process from PEM text (a CA bundle), kept until exit. Entries
 * that do not parse (an algorithm not in the build, an expired root) are skipped: demanding a
 * perfect parse would lose every root over one exotic entry. No root at all: SC_EPARSE. */
struct sc_roots;
int  sc_roots_load(struct sc_roots **out, const unsigned char *pem, size_t n);
/* A store of one DER certificate (a pinned CA); SC_EPARSE if it cannot serve as a CA. */
int  sc_roots_load_der(struct sc_roots **out, const unsigned char *der, size_t n);
void sc_roots_free(struct sc_roots *r);

/* Verifies the server's chain: der[0] is the leaf, then intermediates in any order, extra ones
 * allowed. Checks the path to a root of the store (signatures, CA and keyCertSign on
 * intermediates, path length limit), each certificate's validity now, and host in the leaf
 * (SAN; an address by SAN IP). 0 if the server is genuine; SC_ECHAIN if not, one reason for all
 * ("does not chain to the roots or not issued for this name", as certverify.c puts it);
 * SC_EPARSE if the leaf does not parse. The _ex form, on 0 with nb and na set, gives the latest
 * notBefore and the earliest notAfter of the verified path in UTC seconds (empty if a date does not
 * convert); on failure it leaves them untouched. */
int sc_chain_verify(struct sc_roots *r, const unsigned char *const *der, const size_t *der_n,
                    size_t count, const char *host);
int sc_chain_verify_ex(struct sc_roots *r, const unsigned char *const *der, const size_t *der_n,
                       size_t count, const char *host, int64_t *nb, int64_t *na);
/* One DER certificate's notBefore and notAfter in UTC seconds. 0, or SC_EPARSE if it does not parse
 * or a date does not convert, SC_EINVAL for bad arguments, SC_ECRYPTO if the library does not start;
 * on any failure the outputs are untouched. */
int sc_cert_window(const unsigned char *der, size_t n, int64_t *nb, int64_t *na);

#endif
