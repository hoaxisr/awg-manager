#ifndef FIRC_HASH_H
#define FIRC_HASH_H

#include <stddef.h>
#include <stdint.h>

#define FIRC_MD5_DIGEST_LEN 16
#define FIRC_SHA256_DIGEST_LEN 32
#define FIRC_SHA512_DIGEST_LEN 64

typedef struct firc_md5_ctx {
    uint32_t state[4];
    uint64_t total_len;
    uint8_t buf[64];
    size_t buf_len;
} firc_md5_ctx_t;

void firc_md5_init(firc_md5_ctx_t *ctx);
void firc_md5_update(firc_md5_ctx_t *ctx, const uint8_t *data, size_t len);
void firc_md5_final(firc_md5_ctx_t *ctx, uint8_t out[FIRC_MD5_DIGEST_LEN]);
void firc_md5(const uint8_t *data, size_t len, uint8_t out[FIRC_MD5_DIGEST_LEN]);

typedef struct firc_sha256_ctx {
    uint32_t state[8];
    uint64_t total_len;
    uint8_t buf[64];
    size_t buf_len;
} firc_sha256_ctx_t;

void firc_sha256_init(firc_sha256_ctx_t *ctx);
void firc_sha256_update(firc_sha256_ctx_t *ctx, const uint8_t *data, size_t len);
void firc_sha256_final(firc_sha256_ctx_t *ctx, uint8_t out[FIRC_SHA256_DIGEST_LEN]);
void firc_sha256(const uint8_t *data, size_t len, uint8_t out[FIRC_SHA256_DIGEST_LEN]);

typedef struct firc_sha512_ctx {
    uint64_t state[8];
    uint64_t total_len; /* bytes */
    uint8_t buf[128];
    size_t buf_len;
} firc_sha512_ctx_t;

void firc_sha512_init(firc_sha512_ctx_t *ctx);
void firc_sha512_update(firc_sha512_ctx_t *ctx, const uint8_t *data, size_t len);
void firc_sha512_final(firc_sha512_ctx_t *ctx, uint8_t out[FIRC_SHA512_DIGEST_LEN]);
void firc_sha512(const uint8_t *data, size_t len, uint8_t out[FIRC_SHA512_DIGEST_LEN]);

void firc_hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *msg,
                    size_t msg_len, uint8_t out[FIRC_SHA256_DIGEST_LEN]);

/* constant-time compare */
int firc_hash_equal(const uint8_t *a, const uint8_t *b, size_t len);

/* out needs firc_base64_encoded_len(len)+1 bytes, NUL included */
size_t firc_base64_encoded_len(size_t in_len);
void firc_base64_encode(const uint8_t *data, size_t len, char *out);

/* out needs firc_base64url_encoded_len(len)+1 bytes */
size_t firc_base64url_encoded_len(size_t in_len);
void firc_base64url_encode(const uint8_t *data, size_t len, char *out);

/* writes at most out_cap bytes; returns 0 on success, -1 on malformed input or overflow */
int firc_base64url_decode(const char *in, size_t in_len, uint8_t *out, size_t out_cap,
                          size_t *out_len);

/* same contract as firc_base64url_decode */
int firc_base64_decode(const char *in, size_t in_len, uint8_t *out, size_t out_cap,
                       size_t *out_len);

#endif /* FIRC_HASH_H */
