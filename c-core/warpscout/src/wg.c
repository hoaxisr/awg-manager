/* awgm-warpscout: WireGuard initiator handshake + transport. */
#include "wg.h"
#include "wgcrypto.h"

#include <string.h>

const char WG_CONSTRUCTION[] = "Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s";
const char WG_IDENTIFIER[] = "WireGuard v1 zx2c4 Jason@zx2c4.com";
const char WG_LABEL_MAC1[] = "mac1----";

void wg_hash2(uint8_t out[32], const uint8_t *a, size_t alen, const uint8_t *b, size_t blen) {
    blake2s_ctx c;
    blake2s_init(&c, 32, NULL, 0);
    blake2s_update(&c, a, alen);
    blake2s_update(&c, b, blen);
    blake2s_final(&c, out);
}

int wg_identity_init(wg_identity *id, const uint8_t priv[32], const uint8_t peer_pub[32],
                     const uint8_t *psk) {
    memset(id, 0, sizeof(*id));
    memcpy(id->static_priv, priv, 32);
    x25519_clamp(id->static_priv);
    x25519_pub(id->static_pub, id->static_priv);
    memcpy(id->peer_pub, peer_pub, 32);
    if (psk) memcpy(id->psk, psk, 32);
    if (x25519_dh(id->ss, id->static_priv, id->peer_pub) != 0) return -1;
    wg_hash2(id->mac1_key, (const uint8_t *)WG_LABEL_MAC1, 8, id->peer_pub, 32);
    blake2s(id->c0, 32, NULL, 0, WG_CONSTRUCTION, strlen(WG_CONSTRUCTION));
    wg_hash2(id->h0, id->c0, 32, (const uint8_t *)WG_IDENTIFIER, strlen(WG_IDENTIFIER));
    wg_hash2(id->h0, id->h0, 32, id->peer_pub, 32);
    return 0;
}

int wg_create_initiation(const wg_identity *id, wg_hs *hs, uint32_t local_idx,
                         const uint8_t *e_priv, uint8_t out[WG_INIT_LEN]) {
    uint8_t k[32], dh[32], ts[12];
    memset(hs, 0, sizeof(*hs));
    memset(out, 0, WG_INIT_LEN);
    if (e_priv) memcpy(hs->e_priv, e_priv, 32);
    else if (os_random(hs->e_priv, 32) != 0) return -1;
    x25519_clamp(hs->e_priv);
    x25519_pub(hs->e_pub, hs->e_priv);
    hs->local_idx = local_idx;

    out[0] = WG_MSG_INIT;
    wg_st32(out + 4, local_idx);
    memcpy(hs->C, id->c0, 32);
    memcpy(hs->H, id->h0, 32);
    /* e */
    memcpy(out + 8, hs->e_pub, 32);
    wg_kdf(hs->C, NULL, NULL, hs->C, hs->e_pub, 32);
    wg_hash2(hs->H, hs->H, 32, hs->e_pub, 32);
    /* es */
    if (x25519_dh(dh, hs->e_priv, id->peer_pub) != 0) { wipe(dh, 32); return -1; }
    wg_kdf(hs->C, k, NULL, hs->C, dh, 32);
    /* s */
    aead_seal(out + 40, k, 0, id->static_pub, 32, hs->H, 32);
    wg_hash2(hs->H, hs->H, 32, out + 40, 48);
    /* ss */
    wg_kdf(hs->C, k, NULL, hs->C, id->ss, 32);
    /* timestamp */
    tai64n_now(ts);
    aead_seal(out + 88, k, 0, ts, 12, hs->H, 32);
    wg_hash2(hs->H, hs->H, 32, out + 88, 28);
    /* mac1 over msg[0:116]; mac2 stays zero (no cookie) */
    blake2s(out + 116, 16, id->mac1_key, 32, out, 116);
    wipe(k, 32);
    wipe(dh, 32);
    return 0;
}

int wg_consume_response(const wg_identity *id, wg_hs *hs, const uint8_t *msg, size_t len) {
    uint8_t C[32], H[32], dh[32], t[32], k[32];
    int rc = -1;
    if (len != WG_RESP_LEN || msg[0] != WG_MSG_RESP) return -1;
    if (wg_ld32(msg + 8) != hs->local_idx) return -1;
    const uint8_t *er = msg + 12;
    memcpy(C, hs->C, 32);
    memcpy(H, hs->H, 32);
    wg_kdf(C, NULL, NULL, C, er, 32);
    wg_hash2(H, H, 32, er, 32);
    if (x25519_dh(dh, hs->e_priv, er) != 0) goto out;       /* ee */
    wg_kdf(C, NULL, NULL, C, dh, 32);
    if (x25519_dh(dh, id->static_priv, er) != 0) goto out;  /* se */
    wg_kdf(C, NULL, NULL, C, dh, 32);
    wg_kdf(C, t, k, C, id->psk, 32);                        /* psk */
    wg_hash2(H, H, 32, t, 32);
    if (aead_open(NULL, k, 0, msg + 44, 16, H, 32) != 0) goto out;
    wg_hash2(H, H, 32, msg + 44, 16);
    wg_kdf(hs->send_key, hs->recv_key, NULL, C, NULL, 0);
    hs->remote_idx = wg_ld32(msg + 4);
    hs->send_ctr = 0;
    hs->established = 1;
    memcpy(hs->C, C, 32);
    memcpy(hs->H, H, 32);
    wipe(hs->e_priv, 32);
    rc = 0;
out:
    wipe(C, 32); wipe(dh, 32); wipe(t, 32); wipe(k, 32);
    return rc;
}

size_t wg_encrypt_transport(wg_hs *hs, const uint8_t *ip, size_t len, uint8_t *out) {
    uint8_t pt[2048];
    size_t plen = (len + 15) & ~(size_t)15;
    if (plen > sizeof(pt)) return 0;
    memcpy(pt, ip, len);
    memset(pt + len, 0, plen - len);
    out[0] = WG_MSG_DATA;
    out[1] = out[2] = out[3] = 0;
    wg_st32(out + 4, hs->remote_idx);
    for (int i = 0; i < 8; i++) out[8 + i] = (uint8_t)(hs->send_ctr >> (8 * i));
    aead_seal(out + 16, hs->send_key, hs->send_ctr, pt, plen, NULL, 0);
    hs->send_ctr++;
    return 16 + plen + 16;
}

int wg_decrypt_transport(const wg_hs *hs, const uint8_t *msg, size_t len, uint8_t *out) {
    uint64_t ctr = 0;
    if (len < 32 || msg[0] != WG_MSG_DATA) return -1;
    for (int i = 0; i < 8; i++) ctr |= (uint64_t)msg[8 + i] << (8 * i);
    if (aead_open(out, hs->recv_key, ctr, msg + 16, len - 16, NULL, 0) != 0) return -1;
    return (int)(len - 32);
}
