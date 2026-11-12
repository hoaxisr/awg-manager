/* Test-only WireGuard responder + round-trip test for awgm-warpscout.
 * Not linked into the production binary. */
#include "../src/wgcrypto.h"
#include "../src/wg.h"
#include "../src/netpkt.h"

#include <stdio.h>
#include <string.h>

typedef struct {
    uint8_t priv[32], pub[32];
    uint8_t C[32], H[32];
    uint8_t ei[32], si[32];
    uint32_t remote_idx, local_idx;
    uint8_t send_key[32], recv_key[32];
    uint64_t send_ctr;
} responder;

static int resp_consume_init(responder *r, const uint8_t *msg) {
    uint8_t k[32], dh[32], ts[12], mac1key[32], mac[16], m[WG_INIT_LEN];
    uint8_t c0[32];
    memcpy(m, msg, WG_INIT_LEN);
    m[1] = m[2] = m[3] = 0; /* reserved bytes are not covered (Xray/sing-box semantics) */
    if (m[0] != WG_MSG_INIT) return -1;
    wg_hash2(mac1key, (const uint8_t *)WG_LABEL_MAC1, 8, r->pub, 32);
    blake2s(mac, 16, mac1key, 32, m, 116);
    if (!ct_memeq(mac, m + 116, 16)) return -2;
    blake2s(c0, 32, NULL, 0, WG_CONSTRUCTION, strlen(WG_CONSTRUCTION));
    memcpy(r->C, c0, 32);
    wg_hash2(r->H, c0, 32, (const uint8_t *)WG_IDENTIFIER, strlen(WG_IDENTIFIER));
    wg_hash2(r->H, r->H, 32, r->pub, 32);
    memcpy(r->ei, m + 8, 32);
    wg_kdf(r->C, NULL, NULL, r->C, r->ei, 32);
    wg_hash2(r->H, r->H, 32, r->ei, 32);
    x25519_dh(dh, r->priv, r->ei);
    wg_kdf(r->C, k, NULL, r->C, dh, 32);
    if (aead_open(r->si, k, 0, m + 40, 48, r->H, 32) != 0) return -3;
    wg_hash2(r->H, r->H, 32, m + 40, 48);
    x25519_dh(dh, r->priv, r->si);
    wg_kdf(r->C, k, NULL, r->C, dh, 32);
    if (aead_open(ts, k, 0, m + 88, 28, r->H, 32) != 0) return -4;
    wg_hash2(r->H, r->H, 32, m + 88, 28);
    if (ts[0] != 0x40) return -5;
    r->remote_idx = wg_ld32(m + 4);
    return 0;
}

static void resp_create_response(responder *r, uint8_t out[WG_RESP_LEN], const uint8_t psk[32]) {
    uint8_t er_priv[32], er[32], dh[32], t[32], k[32], mac1key[32];
    memset(out, 0, WG_RESP_LEN);
    os_random(er_priv, 32);
    x25519_clamp(er_priv);
    x25519_pub(er, er_priv);
    r->local_idx = 0x11223344;
    out[0] = WG_MSG_RESP;
    wg_st32(out + 4, r->local_idx);
    wg_st32(out + 8, r->remote_idx);
    memcpy(out + 12, er, 32);
    wg_kdf(r->C, NULL, NULL, r->C, er, 32);
    wg_hash2(r->H, r->H, 32, er, 32);
    x25519_dh(dh, er_priv, r->ei);
    wg_kdf(r->C, NULL, NULL, r->C, dh, 32);
    x25519_dh(dh, er_priv, r->si);
    wg_kdf(r->C, NULL, NULL, r->C, dh, 32);
    wg_kdf(r->C, t, k, r->C, psk, 32);
    wg_hash2(r->H, r->H, 32, t, 32);
    aead_seal(out + 44, k, 0, NULL, 0, r->H, 32);
    wg_hash2(r->H, r->H, 32, out + 44, 16);
    wg_hash2(mac1key, (const uint8_t *)WG_LABEL_MAC1, 8, r->si, 32);
    blake2s(out + 60, 16, mac1key, 32, out, 60);
    /* responder: recv = T1 (initiator send), send = T2 */
    wg_kdf(r->recv_key, r->send_key, NULL, r->C, NULL, 0);
    out[1] = 0xaa; out[2] = 0xbb; out[3] = 0xcc; /* nonzero reserved on the wire */
}

static int roundtrip(const uint8_t *psk) {
    responder r;
    wg_identity id;
    wg_hs hs;
    uint8_t ipriv[32], init[WG_INIT_LEN], resp[WG_RESP_LEN], buf[256], pkt[128], dec[256];
    memset(&r, 0, sizeof(r));
    os_random(r.priv, 32);
    x25519_clamp(r.priv);
    x25519_pub(r.pub, r.priv);
    os_random(ipriv, 32);
    if (wg_identity_init(&id, ipriv, r.pub, psk) != 0) return 1;
    if (wg_create_initiation(&id, &hs, 0xdeadbeef, NULL, init) != 0) return 2;
    init[1] = 0x01; init[2] = 0x02; init[3] = 0x03; /* reserved after mac1 */
    int rc = resp_consume_init(&r, init);
    if (rc != 0) { fprintf(stderr, "responder rejected init: %d\n", rc); return 3; }
    if (memcmp(r.si, id.static_pub, 32) != 0) return 4;
    static const uint8_t zero[32] = {0};
    resp_create_response(&r, resp, psk ? psk : zero);
    resp[1] = resp[2] = resp[3] = 0; /* the scanner zeroes reserved before parsing */
    if (wg_consume_response(&id, &hs, resp, sizeof(resp)) != 0) return 5;
    if (memcmp(hs.send_key, r.recv_key, 32) || memcmp(hs.recv_key, r.send_key, 32)) return 6;
    /* initiator -> responder: ICMP echo */
    size_t n = pkt_icmp_echo(pkt, 0xAC100002, 0x01010101, 1, 77, 0, (const uint8_t *)"ping", 4);
    size_t m = wg_encrypt_transport(&hs, pkt, n, buf);
    if (m != 16 + ((n + 15) & ~15u) + 16) return 7;
    if (wg_ld32(buf + 4) != r.local_idx) return 8;
    {
        wg_hs rh;
        memset(&rh, 0, sizeof(rh));
        memcpy(rh.recv_key, r.recv_key, 32);
        int d = wg_decrypt_transport(&rh, buf, m, dec);
        pkt_info pi;
        if (d < (int)n || pkt_parse(dec, (size_t)d, &pi) != 0 || pi.icmp_id != 77) return 9;
        /* responder -> initiator */
        memcpy(rh.send_key, r.send_key, 32);
        rh.remote_idx = hs.local_idx;
        dec[20] = 0; /* echo reply (checksum now wrong; just bytes) */
        size_t m2 = wg_encrypt_transport(&rh, dec, (size_t)d, buf);
        int d2 = wg_decrypt_transport(&hs, buf, m2, pkt);
        if (d2 != d || memcmp(pkt, dec, (size_t)d) != 0) return 10;
        buf[20] ^= 0x80;
        if (wg_decrypt_transport(&hs, buf, m2, pkt) >= 0) return 11;
    }
    return 0;
}

int main(void) {
    int fails = crypto_selftest(1);
    int rc = roundtrip(NULL);
    fprintf(stderr, "test %-38s %s (%d)\n", "wg handshake round-trip (psk=0)", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    uint8_t psk[32];
    os_random(psk, 32);
    rc = roundtrip(psk);
    fprintf(stderr, "test %-38s %s (%d)\n", "wg handshake round-trip (psk)", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    for (int i = 0; i < 50 && !fails; i++) fails += roundtrip(NULL) != 0;
    fprintf(stderr, "test %-38s %s\n", "50x randomized round-trips", fails ? "FAIL" : "ok");
    fprintf(stderr, "tests: %s\n", fails ? "FAILED" : "all passed");
    return fails ? 1 : 0;
}
