/* awgm-warpscout: WireGuard initiator (Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s). */
#ifndef WS_WG_H
#define WS_WG_H

#include <stddef.h>
#include <stdint.h>

#define WG_MSG_INIT 1
#define WG_MSG_RESP 2
#define WG_MSG_COOKIE 3
#define WG_MSG_DATA 4
#define WG_INIT_LEN 148
#define WG_RESP_LEN 92
#define WG_DATA_HDR 16

extern const char WG_CONSTRUCTION[];
extern const char WG_IDENTIFIER[];
extern const char WG_LABEL_MAC1[];

typedef struct {
    uint8_t static_priv[32];
    uint8_t static_pub[32];
    uint8_t peer_pub[32];
    uint8_t psk[32];
    uint8_t ss[32];        /* DH(static_priv, peer_pub), precomputed */
    uint8_t mac1_key[32];  /* HASH(LABEL_MAC1 || peer_pub) */
    uint8_t c0[32];        /* HASH(CONSTRUCTION) */
    uint8_t h0[32];        /* HASH(HASH(C0 || IDENTIFIER) || peer_pub) */
} wg_identity;

typedef struct {
    uint8_t e_priv[32];
    uint8_t e_pub[32];
    uint8_t C[32];
    uint8_t H[32];
    uint32_t local_idx;
    uint32_t remote_idx;
    uint8_t send_key[32];
    uint8_t recv_key[32];
    uint64_t send_ctr;
    int established;
} wg_hs;

void wg_hash2(uint8_t out[32], const uint8_t *a, size_t alen, const uint8_t *b, size_t blen);

int wg_identity_init(wg_identity *id, const uint8_t priv[32], const uint8_t peer_pub[32],
                     const uint8_t *psk /* NULL = zeros */);

/* Builds a 148-byte initiation (reserved bytes zero, mac2 zero).
 * e_priv may be NULL (random). Returns 0 on success. */
int wg_create_initiation(const wg_identity *id, wg_hs *hs, uint32_t local_idx,
                         const uint8_t *e_priv, uint8_t out[WG_INIT_LEN]);

/* Validates a 92-byte response, derives transport keys. Reserved bytes
 * 1..3 of the response are ignored. Returns 0 on success. */
int wg_consume_response(const wg_identity *id, wg_hs *hs, const uint8_t *msg, size_t len);

/* Encrypts an IP packet into a type-4 message (padded to 16).
 * out must have room for 16 + len + 15 + 16. Returns message length. */
size_t wg_encrypt_transport(wg_hs *hs, const uint8_t *ip, size_t len, uint8_t *out);

/* Decrypts a type-4 message with the given keys. Returns plaintext length
 * (possibly 0 for keepalive) or -1. */
int wg_decrypt_transport(const wg_hs *hs, const uint8_t *msg, size_t len, uint8_t *out);

static inline uint32_t wg_ld32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static inline void wg_st32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)v; p[1] = (uint8_t)(v >> 8); p[2] = (uint8_t)(v >> 16); p[3] = (uint8_t)(v >> 24);
}

#endif
