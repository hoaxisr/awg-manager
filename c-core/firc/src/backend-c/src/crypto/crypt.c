#include "firc/crypt.h"

#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/hash.h"

#define MD5_PREFIX "$1$"
#define SHA256_PREFIX "$5$"
#define SHA512_PREFIX "$6$"

#define SHA_ROUNDS_MIN 1000
#define SHA_ROUNDS_MAX 999999999
#define SHA_ROUNDS_DEFAULT 5000

#define MAX_PASSWORD_LEN 4096 /* bounds the pseq/sseq scratch buffers below */

static const char b64_alphabet[] =
    "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/* custom non-RFC4648 reordering, crypt(3) output encoding only */
static size_t base64_24bit(const uint8_t *src, size_t src_len, char *out) {
    size_t o = 0;
    size_t i = 0;
    while (i < src_len) {
        size_t remain = src_len - i;
        if (remain >= 3) {
            uint32_t v = ((uint32_t)src[i]) | ((uint32_t)src[i + 1] << 8) |
                        ((uint32_t)src[i + 2] << 16);
            out[o++] = b64_alphabet[v & 0x3f];
            out[o++] = b64_alphabet[(v >> 6) & 0x3f];
            out[o++] = b64_alphabet[(v >> 12) & 0x3f];
            out[o++] = b64_alphabet[(v >> 18) & 0x3f];
            i += 3;
        } else if (remain == 2) {
            uint32_t v = ((uint32_t)src[i]) | ((uint32_t)src[i + 1] << 8);
            out[o++] = b64_alphabet[v & 0x3f];
            out[o++] = b64_alphabet[(v >> 6) & 0x3f];
            out[o++] = b64_alphabet[(v >> 12) & 0x3f];
            i += 2;
        } else {
            uint32_t v = src[i];
            out[o++] = b64_alphabet[v & 0x3f];
            out[o++] = b64_alphabet[(v >> 6) & 0x3f];
            i += 1;
        }
    }
    return o;
}

#define MAX_SALT_TOKS 8
typedef struct tok {
    const char *p;
    size_t len;
} tok_t;

static size_t split_dollar(const char *s, size_t len, tok_t *toks, size_t max_toks) {
    size_t n = 0;
    size_t start = 0;
    for (size_t i = 0; i <= len && n < max_toks; i++) {
        if (i == len || s[i] == '$') {
            toks[n].p = s + start;
            toks[n].len = i - start;
            n++;
            start = i + 1;
        }
    }
    return n;
}

static firc_err_t extract_salt(const char *prefix, const char *salt, size_t max_len, tok_t *out) {
    size_t prefix_len = strlen(prefix);
    if (strncmp(salt, prefix, prefix_len) != 0) { return FIRC_ERR_INVAL; }
    tok_t toks[MAX_SALT_TOKS];
    size_t n = split_dollar(salt, strlen(salt), toks, MAX_SALT_TOKS);
    if (n < 3) { return FIRC_ERR_INVAL; }
    out->p = toks[2].p;
    out->len = toks[2].len > max_len ? max_len : toks[2].len;
    return FIRC_OK;
}

static firc_err_t extract_salt_and_rounds(const char *prefix, const char *salt, tok_t *out_salt,
                                        int *out_rounds, bool *out_custom) {
    size_t prefix_len = strlen(prefix);
    if (strncmp(salt, prefix, prefix_len) != 0) { return FIRC_ERR_INVAL; }
    tok_t toks[MAX_SALT_TOKS];
    size_t n = split_dollar(salt, strlen(salt), toks, MAX_SALT_TOKS);
    if (n < 3) { return FIRC_ERR_INVAL; }

    tok_t payload = toks[2];
    *out_custom = false;
    *out_rounds = SHA_ROUNDS_DEFAULT;
    if (payload.len > 7 && strncmp(payload.p, "rounds=", 7) == 0) {
        *out_custom = true;
        char buf[32];
        size_t digits = payload.len - 7;
        if (digits == 0 || digits >= sizeof(buf)) { return FIRC_ERR_INVAL; }
        memcpy(buf, payload.p + 7, digits);
        buf[digits] = '\0';
        char *end = NULL;
        long v = strtol(buf, &end, 10);
        if (end == buf || *end != '\0') { return FIRC_ERR_INVAL; }
        if (v < SHA_ROUNDS_MIN) { v = SHA_ROUNDS_MIN; }
        if (v > SHA_ROUNDS_MAX) { v = SHA_ROUNDS_MAX; }
        *out_rounds = (int)v;
        if (n < 4) { return FIRC_ERR_INVAL; }
        payload = toks[3];
    }
    out_salt->p = payload.p;
    out_salt->len = payload.len > 16 ? 16 : payload.len;
    return FIRC_OK;
}


static firc_err_t md5_crypt(const char *password, const char *salt, char *out, size_t out_len) {
    tok_t salt_tok;
    firc_err_t err = extract_salt(MD5_PREFIX, salt, 8, &salt_tok);
    if (err != FIRC_OK) { return err; }

    const uint8_t *key = (const uint8_t *)password;
    size_t key_len = strlen(password);
    if (key_len > MAX_PASSWORD_LEN) { return FIRC_ERR_LIMIT; }

    firc_md5_ctx_t alt;
    firc_md5_init(&alt);
    firc_md5_update(&alt, key, key_len);
    firc_md5_update(&alt, (const uint8_t *)salt_tok.p, salt_tok.len);
    firc_md5_update(&alt, key, key_len);
    uint8_t alt_sum[FIRC_MD5_DIGEST_LEN];
    firc_md5_final(&alt, alt_sum);

    firc_md5_ctx_t a;
    firc_md5_init(&a);
    firc_md5_update(&a, key, key_len);
    firc_md5_update(&a, (const uint8_t *)MD5_PREFIX, strlen(MD5_PREFIX));
    firc_md5_update(&a, (const uint8_t *)salt_tok.p, salt_tok.len);

    size_t i = key_len;
    for (; i > 16; i -= 16) {
        firc_md5_update(&a, alt_sum, FIRC_MD5_DIGEST_LEN);
    }
    firc_md5_update(&a, alt_sum, i);

    for (i = key_len; i > 0; i >>= 1) {
        if ((i & 1) == 0) {
            firc_md5_update(&a, key, 1);
        } else {
            uint8_t zero = 0;
            firc_md5_update(&a, &zero, 1);
        }
    }
    uint8_t csum[FIRC_MD5_DIGEST_LEN];
    firc_md5_final(&a, csum);

    for (i = 0; i < 1000; i++) {
        firc_md5_ctx_t c;
        firc_md5_init(&c);
        if ((i & 1) != 0) {
            firc_md5_update(&c, key, key_len);
        } else {
            firc_md5_update(&c, csum, FIRC_MD5_DIGEST_LEN);
        }
        if (i % 3 != 0) { firc_md5_update(&c, (const uint8_t *)salt_tok.p, salt_tok.len); }
        if (i % 7 != 0) { firc_md5_update(&c, key, key_len); }
        if ((i & 1) == 0) {
            firc_md5_update(&c, key, key_len);
        } else {
            firc_md5_update(&c, csum, FIRC_MD5_DIGEST_LEN);
        }
        firc_md5_final(&c, csum);
    }

    uint8_t reordered[16] = {
        csum[12], csum[6], csum[0], csum[13], csum[7], csum[1], csum[14], csum[8],
        csum[2],  csum[15], csum[9], csum[3], csum[5], csum[10], csum[4], csum[11],
    };

    size_t needed = strlen(MD5_PREFIX) + salt_tok.len + 1 + 22 + 1;
    if (out_len < needed) { return FIRC_ERR_LIMIT; }
    size_t o = 0;
    memcpy(out + o, MD5_PREFIX, strlen(MD5_PREFIX));
    o += strlen(MD5_PREFIX);
    memcpy(out + o, salt_tok.p, salt_tok.len);
    o += salt_tok.len;
    out[o++] = '$';
    o += base64_24bit(reordered, sizeof(reordered), out + o);
    out[o] = '\0';
    return FIRC_OK;
}


typedef union sha_ctx {
    firc_sha256_ctx_t s256;
    firc_sha512_ctx_t s512;
} sha_ctx_t;

typedef struct sha_ops {
    void (*init)(sha_ctx_t *ctx);
    void (*update)(sha_ctx_t *ctx, const uint8_t *data, size_t len);
    void (*final)(sha_ctx_t *ctx, uint8_t *out);
    size_t digest_size;
} sha_ops_t;

static void sha256_init_op(sha_ctx_t *ctx) { firc_sha256_init(&ctx->s256); }
static void sha256_update_op(sha_ctx_t *ctx, const uint8_t *data, size_t len) {
    firc_sha256_update(&ctx->s256, data, len);
}
static void sha256_final_op(sha_ctx_t *ctx, uint8_t *out) { firc_sha256_final(&ctx->s256, out); }

static void sha512_init_op(sha_ctx_t *ctx) { firc_sha512_init(&ctx->s512); }
static void sha512_update_op(sha_ctx_t *ctx, const uint8_t *data, size_t len) {
    firc_sha512_update(&ctx->s512, data, len);
}
static void sha512_final_op(sha_ctx_t *ctx, uint8_t *out) { firc_sha512_final(&ctx->s512, out); }

static const sha_ops_t SHA256_OPS = {sha256_init_op, sha256_update_op, sha256_final_op,
                                     FIRC_SHA256_DIGEST_LEN};
static const sha_ops_t SHA512_OPS = {sha512_init_op, sha512_update_op, sha512_final_op,
                                     FIRC_SHA512_DIGEST_LEN};

/* fixed index shuffle for base64_24bit input order */
static const uint8_t SHA256_REORDER[32] = {
    20, 10, 0,  11, 1,  21, 2,  22, 12, 23, 13, 3,  14, 4,  24, 5,
    25, 15, 26, 16, 6,  17, 7,  27, 8,  28, 18, 29, 19, 9,  30, 31,
};
static const uint8_t SHA512_REORDER[64] = {
    42, 21, 0,  1,  43, 22, 23, 2,  44, 45, 24, 3,  4,  46, 25, 26,
    5,  47, 48, 27, 6,  7,  49, 28, 29, 8,  50, 51, 30, 9,  10, 52,
    31, 32, 11, 53, 54, 33, 12, 13, 55, 34, 35, 14, 56, 57, 36, 15,
    16, 58, 37, 38, 17, 59, 60, 39, 18, 19, 61, 40, 41, 20, 62, 63,
};

static firc_err_t sha_crypt(const char *password, const char *salt, const char *prefix,
                          const sha_ops_t *ops, const uint8_t *reorder, char *out,
                          size_t out_len) {
    tok_t salt_tok;
    int rounds;
    bool custom_rounds;
    firc_err_t err = extract_salt_and_rounds(prefix, salt, &salt_tok, &rounds, &custom_rounds);
    if (err != FIRC_OK) { return err; }

    const uint8_t *key = (const uint8_t *)password;
    size_t key_len = strlen(password);
    if (key_len > MAX_PASSWORD_LEN) { return FIRC_ERR_LIMIT; }
    size_t digest_size = ops->digest_size;

    sha_ctx_t ctx;
    uint8_t alt_sum[FIRC_SHA512_DIGEST_LEN];
    ops->init(&ctx);
    ops->update(&ctx, key, key_len);
    ops->update(&ctx, (const uint8_t *)salt_tok.p, salt_tok.len);
    ops->update(&ctx, key, key_len);
    ops->final(&ctx, alt_sum);

    ops->init(&ctx);
    ops->update(&ctx, key, key_len);
    ops->update(&ctx, (const uint8_t *)salt_tok.p, salt_tok.len);
    size_t remaining = key_len;
    while (remaining > digest_size) {
        ops->update(&ctx, alt_sum, digest_size);
        remaining -= digest_size;
    }
    ops->update(&ctx, alt_sum, remaining);
    for (size_t i = key_len; i > 0; i >>= 1) {
        if ((i & 1) != 0) {
            ops->update(&ctx, alt_sum, digest_size);
        } else {
            ops->update(&ctx, key, key_len);
        }
    }
    uint8_t asum[FIRC_SHA512_DIGEST_LEN];
    ops->final(&ctx, asum);

    ops->init(&ctx);
    for (size_t i = 0; i < key_len; i++) {
        ops->update(&ctx, key, key_len);
    }
    uint8_t psum[FIRC_SHA512_DIGEST_LEN];
    ops->final(&ctx, psum);

    uint8_t *pseq = key_len ? malloc(key_len) : NULL;
    if (key_len && !pseq) { return FIRC_ERR_NOMEM; }
    remaining = key_len;
    size_t o = 0;
    while (remaining > digest_size) {
        memcpy(pseq + o, psum, digest_size);
        o += digest_size;
        remaining -= digest_size;
    }
    if (remaining > 0) { memcpy(pseq + o, psum, remaining); }

    ops->init(&ctx);
    int s_iters = 16 + asum[0];
    for (int i = 0; i < s_iters; i++) {
        ops->update(&ctx, (const uint8_t *)salt_tok.p, salt_tok.len);
    }
    uint8_t ssum[FIRC_SHA512_DIGEST_LEN];
    ops->final(&ctx, ssum);

    uint8_t *sseq = salt_tok.len ? malloc(salt_tok.len) : NULL;
    if (salt_tok.len && !sseq) {
        free(pseq);
        return FIRC_ERR_NOMEM;
    }
    remaining = salt_tok.len;
    o = 0;
    while (remaining > digest_size) {
        memcpy(sseq + o, ssum, digest_size);
        o += digest_size;
        remaining -= digest_size;
    }
    if (remaining > 0) { memcpy(sseq + o, ssum, remaining); }

    uint8_t csum[FIRC_SHA512_DIGEST_LEN];
    memcpy(csum, asum, digest_size);
    for (int i = 0; i < rounds; i++) {
        ops->init(&ctx);
        if ((i & 1) != 0) {
            ops->update(&ctx, pseq, key_len);
        } else {
            ops->update(&ctx, csum, digest_size);
        }
        if (i % 3 != 0) { ops->update(&ctx, sseq, salt_tok.len); }
        if (i % 7 != 0) { ops->update(&ctx, pseq, key_len); }
        if ((i & 1) != 0) {
            ops->update(&ctx, csum, digest_size);
        } else {
            ops->update(&ctx, pseq, key_len);
        }
        ops->final(&ctx, csum);
    }
    free(pseq);
    free(sseq);

    uint8_t reordered[FIRC_SHA512_DIGEST_LEN];
    for (size_t i = 0; i < digest_size; i++) {
        reordered[i] = csum[reorder[i]];
    }

    char rounds_buf[32] = {0};
    size_t rounds_buf_len = 0;
    if (custom_rounds) {
        rounds_buf_len = (size_t)snprintf(rounds_buf, sizeof(rounds_buf), "rounds=%d$", rounds);
    }
    size_t b64_len = firc_base64_encoded_len(digest_size); /* upper bound; actual may be shorter */
    size_t needed = strlen(prefix) + rounds_buf_len + salt_tok.len + 1 + b64_len + 1;
    if (out_len < needed) { return FIRC_ERR_LIMIT; }

    o = 0;
    memcpy(out + o, prefix, strlen(prefix));
    o += strlen(prefix);
    memcpy(out + o, rounds_buf, rounds_buf_len);
    o += rounds_buf_len;
    memcpy(out + o, salt_tok.p, salt_tok.len);
    o += salt_tok.len;
    out[o++] = '$';
    o += base64_24bit(reordered, digest_size, out + o);
    out[o] = '\0';
    return FIRC_OK;
}

firc_err_t firc_crypt_password(const char *password, const char *salt, char *out, size_t out_len) {
    if (strncmp(salt, MD5_PREFIX, strlen(MD5_PREFIX)) == 0) {
        return md5_crypt(password, salt, out, out_len);
    }
    if (strncmp(salt, SHA256_PREFIX, strlen(SHA256_PREFIX)) == 0) {
        return sha_crypt(password, salt, SHA256_PREFIX, &SHA256_OPS, SHA256_REORDER, out, out_len);
    }
    if (strncmp(salt, SHA512_PREFIX, strlen(SHA512_PREFIX)) == 0) {
        return sha_crypt(password, salt, SHA512_PREFIX, &SHA512_OPS, SHA512_REORDER, out, out_len);
    }
    return FIRC_ERR_INVAL;
}
