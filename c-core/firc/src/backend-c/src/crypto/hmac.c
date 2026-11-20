#include "firc/hash.h"

#include <string.h>

#define SHA256_BLOCK_SIZE 64

void firc_hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *msg,
                    size_t msg_len, uint8_t out[FIRC_SHA256_DIGEST_LEN]) {
    uint8_t key_block[SHA256_BLOCK_SIZE] = {0};
    if (key_len > SHA256_BLOCK_SIZE) {
        firc_sha256(key, key_len, key_block);
    } else {
        memcpy(key_block, key, key_len);
    }

    uint8_t ipad[SHA256_BLOCK_SIZE];
    uint8_t opad[SHA256_BLOCK_SIZE];
    for (size_t i = 0; i < SHA256_BLOCK_SIZE; i++) {
        ipad[i] = key_block[i] ^ 0x36;
        opad[i] = key_block[i] ^ 0x5c;
    }

    firc_sha256_ctx_t ctx;
    firc_sha256_init(&ctx);
    firc_sha256_update(&ctx, ipad, sizeof(ipad));
    firc_sha256_update(&ctx, msg, msg_len);
    uint8_t inner[FIRC_SHA256_DIGEST_LEN];
    firc_sha256_final(&ctx, inner);

    firc_sha256_init(&ctx);
    firc_sha256_update(&ctx, opad, sizeof(opad));
    firc_sha256_update(&ctx, inner, sizeof(inner));
    firc_sha256_final(&ctx, out);
}

int firc_hash_equal(const uint8_t *a, const uint8_t *b, size_t len) {
    uint8_t diff = 0;
    for (size_t i = 0; i < len; i++) {
        diff |= (uint8_t)(a[i] ^ b[i]);
    }
    return diff == 0;
}
