/* BLAKE3 for VLESS encryption: the hash and key derivation (derive_key), portable C, no library.
 *
 * VLESS encryption in Xray-core (proxy/vless/encryption) is built on BLAKE3: it derives every
 * AEAD key (blake3.DeriveKey(k, string(ctx), key) in NewAEAD), the xorpub/random keystream key
 * (DeriveKey with the context "VLESS") and the relay key hash (blake3.Sum256). The server needs
 * the very same BLAKE3, and wolfSSL 5.9.4 has none (only BLAKE2). Another library for two
 * functions is not worth it: the algorithm is forty lines of compression plus the chunk tree.
 *
 * A header of static functions rather than a .c: the public sc_blake3_* are defined in
 * scrypto.c (they belong to the primitives layer), and tests/b3match.c includes this header
 * directly to check BLAKE3 without wolfSSL.
 *
 * The whole input at once, no streaming interface: every VLESS encryption input (keys, iv,
 * ciphertext, public key) is known in full before the call. The input is not limited to one
 * chunk, though: the NewAEAD context is the 1216-byte pfsPublicKey (2 chunks of 1024), and the
 * ML-KEM public key hash (1184 bytes) is 2 chunks as well. So the tree (parent nodes, the stack
 * of chaining values) is implemented in full. tests/b3match.c checks it against
 * lukechampine.com/blake3, the library Xray-core uses, at the block, chunk and tree depth
 * boundaries.
 *
 * Speed does not matter: about ten calls per handshake, none per data record (a record key is
 * derived once per key change, every 2^96 records, see trvenc.c). Hence portable C, no SIMD. */
#ifndef STEER_BLAKE3_H
#define STEER_BLAKE3_H
#include <stdint.h>
#include <string.h>

#define B3_CHUNK_START 1u
#define B3_CHUNK_END 2u
#define B3_PARENT 4u
#define B3_ROOT 8u
#define B3_DERIVE_CONTEXT 32u
#define B3_DERIVE_MATERIAL 64u

static const uint32_t B3_IV[8] = {
    0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au,
    0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u,
};

static const uint8_t B3_PERM[16] = { 2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8 };

static inline uint32_t rotr(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }

static inline void g(uint32_t *s, int a, int b, int c, int d, uint32_t mx, uint32_t my) {
    s[a] = s[a] + s[b] + mx; s[d] = rotr(s[d] ^ s[a], 16);
    s[c] = s[c] + s[d];      s[b] = rotr(s[b] ^ s[c], 12);
    s[a] = s[a] + s[b] + my; s[d] = rotr(s[d] ^ s[a], 8);
    s[c] = s[c] + s[d];      s[b] = rotr(s[b] ^ s[c], 7);
}

static void round_fn(uint32_t *s, const uint32_t *m) {
    g(s, 0, 4, 8, 12, m[0], m[1]);   g(s, 1, 5, 9, 13, m[2], m[3]);
    g(s, 2, 6, 10, 14, m[4], m[5]);  g(s, 3, 7, 11, 15, m[6], m[7]);
    g(s, 0, 5, 10, 15, m[8], m[9]);  g(s, 1, 6, 11, 12, m[10], m[11]);
    g(s, 2, 7, 8, 13, m[12], m[13]); g(s, 3, 4, 9, 14, m[14], m[15]);
}

/* Compresses one block. out gets all 16 state words after the final XOR: the first 8 are the
 * new chaining value, all 16 are the root's extendable output. */
static void compress(const uint32_t cv[8], const uint8_t blk[64], uint8_t blen, uint64_t ctr,
                     uint32_t flags, uint32_t out[16]) {
    uint32_t m[16], s[16];
    for (int i = 0; i < 16; i++)
        m[i] = (uint32_t)blk[4 * i] | (uint32_t)blk[4 * i + 1] << 8 |
               (uint32_t)blk[4 * i + 2] << 16 | (uint32_t)blk[4 * i + 3] << 24;
    memcpy(s, cv, 32);
    memcpy(s + 8, B3_IV, 16);
    s[12] = (uint32_t)ctr; s[13] = (uint32_t)(ctr >> 32); s[14] = blen; s[15] = flags;
    for (int r = 0; r < 7; r++) {
        round_fn(s, m);
        if (r < 6) {
            uint32_t t[16];
            for (int i = 0; i < 16; i++) t[i] = m[B3_PERM[i]];
            memcpy(m, t, sizeof m);
        }
    }
    for (int i = 0; i < 8; i++) { s[i] ^= s[i + 8]; s[i + 8] ^= cv[i]; }
    memcpy(out, s, 64);
}

/* A deferred compression: the last node (the last chunk's block or a parent) is compressed
 * twice, as a chaining value for the node above and with the ROOT flag for the output, so the
 * inputs are kept rather than the result. */
struct b3_out { uint32_t cv[8]; uint8_t blk[64]; uint8_t blen; uint64_t ctr; uint32_t flags; };

static void out_cv(const struct b3_out *o, uint32_t cv[8]) {
    uint32_t w[16];
    compress(o->cv, o->blk, o->blen, o->ctr, o->flags, w);
    memcpy(cv, w, 32);
}

static void put_words(uint8_t *dst, const uint32_t *w, int n) {
    for (int i = 0; i < n; i++) {
        dst[4 * i] = (uint8_t)w[i]; dst[4 * i + 1] = (uint8_t)(w[i] >> 8);
        dst[4 * i + 2] = (uint8_t)(w[i] >> 16); dst[4 * i + 3] = (uint8_t)(w[i] >> 24);
    }
}

static void out_root(const struct b3_out *o, uint8_t *out, size_t n) {
    uint64_t blockno = 0;
    while (n) {
        uint32_t w[16];
        uint8_t buf[64];
        compress(o->cv, o->blk, o->blen, blockno++, o->flags | B3_ROOT, w);
        put_words(buf, w, 16);
        size_t take = n < 64 ? n : 64;
        memcpy(out, buf, take);
        out += take; n -= take;
    }
}

/* One chunk (up to 1024 bytes), except the compression of its last block, which is returned as
 * a b3_out. */
static struct b3_out chunk_out(const uint32_t key[8], const uint8_t *in, size_t n, uint64_t ctr,
                               uint32_t flags) {
    uint32_t cv[8];
    memcpy(cv, key, 32);
    uint32_t start = B3_CHUNK_START;
    while (n > 64) {
        uint32_t w[16];
        compress(cv, in, 64, ctr, flags | start, w);
        memcpy(cv, w, 32);
        in += 64; n -= 64; start = 0;
    }
    struct b3_out o;
    memcpy(o.cv, cv, 32);
    memset(o.blk, 0, 64);
    if (n) memcpy(o.blk, in, n);
    o.blen = (uint8_t)n; o.ctr = ctr; o.flags = flags | start | B3_CHUNK_END;
    return o;
}

static struct b3_out parent_out(const uint32_t key[8], const uint32_t l[8], const uint32_t r[8],
                                uint32_t flags) {
    struct b3_out o;
    memcpy(o.cv, key, 32);
    put_words(o.blk, l, 8);
    put_words(o.blk + 32, r, 8);
    o.blen = 64; o.ctr = 0; o.flags = flags | B3_PARENT;
    return o;
}

/* Hashes the whole input with key and flags (0 or B3_DERIVE_*) into out_n bytes. */
static void b3_run(const uint32_t key[8], uint32_t flags, const uint8_t *in, size_t n,
                   uint8_t *out, size_t out_n) {
    uint32_t stack[54][8];
    int depth = 0;
    uint64_t chunks = 0;
    /* Every chunk but the last is folded into the stack; the last one stays deferred. */
    while (n > 1024) {
        struct b3_out c = chunk_out(key, in, 1024, chunks, flags);
        uint32_t cv[8];
        out_cv(&c, cv);
        in += 1024; n -= 1024;
        chunks++;
        for (uint64_t t = chunks; (t & 1) == 0; t >>= 1) {
            struct b3_out p = parent_out(key, stack[--depth], cv, flags);
            out_cv(&p, cv);
        }
        memcpy(stack[depth++], cv, 32);
    }
    struct b3_out o = chunk_out(key, in, n, chunks, flags);
    while (depth) {
        uint32_t cv[8];
        out_cv(&o, cv);
        o = parent_out(key, stack[--depth], cv, flags);
    }
    out_root(&o, out, out_n);
}

static void b3_hash(unsigned char out[32], const void *in, size_t n) {
    b3_run(B3_IV, 0, in, n, out, 32);
}

static void b3_derive_key(unsigned char *out, size_t out_n, const void *ctx, size_t ctx_n,
                          const void *material, size_t material_n) {
    uint8_t ck[32];
    uint32_t key[8];
    b3_run(B3_IV, B3_DERIVE_CONTEXT, ctx, ctx_n, ck, 32);
    for (int i = 0; i < 8; i++)
        key[i] = (uint32_t)ck[4 * i] | (uint32_t)ck[4 * i + 1] << 8 |
                 (uint32_t)ck[4 * i + 2] << 16 | (uint32_t)ck[4 * i + 3] << 24;
    b3_run(key, B3_DERIVE_MATERIAL, material, material_n, out, out_n);
    memset(ck, 0, sizeof ck);
    memset(key, 0, sizeof key);
}

#endif
