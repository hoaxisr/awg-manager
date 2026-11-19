/* Reality handshake: a ClientHello indistinguishable from a browser's, with an authenticator in
 * session_id. The mechanics are explained in reality.c. */
#ifndef STEER_REALITY_H
#define STEER_REALITY_H
#include <stdint.h>
#include <stddef.h>
#include <strings.h>
#include "certzip.h"

#define REALITY_EBADKEY (-2)   /* pbk or sid does not parse */
#define REALITY_ECRYPTO (-3)   /* a primitive or the random source failed */
#define REALITY_ETOOBIG (-4)   /* the Hello does not fit the buffer */

/* X25519MLKEM768: the post-quantum exchange current Chrome offers FIRST. Its share on the wire is
 * 1184 bytes of ML-KEM plus 32 bytes of X25519. */
#define REALITY_GROUP_MLKEM 0x11EC
#define REALITY_MLKEM_SHARE 1216

enum { REALITY_FP_CHROME = 0, REALITY_FP_FIREFOX = 1 };

static inline int reality_fp_of(const char *fp) {
    return fp && !strcasecmp(fp, "firefox") ? REALITY_FP_FIREFOX : REALITY_FP_CHROME;
}

/* The certificate compressions a Hello of this fingerprint offers, as 1u << algorithm; the Hello
 * writes its compress_certificate list from it and the handshake accepts only these. */
static inline unsigned reality_cert_algs(int fp) {
    return fp == REALITY_FP_FIREFOX
               ? (1u << CERTZIP_ZLIB) | (1u << CERTZIP_BROTLI) | (1u << CERTZIP_ZSTD)
               : (1u << CERTZIP_BROTLI);
}

struct reality_cfg {
    const char *sni;   /* SNI: the camouflage domain (Reality) or the server name (plain);
                        * "" sends no server_name extension at all */
    const char *pbk;   /* server public key, base64url */
    const char *sid;   /* short id, hex; may be empty */
    const char *fp;    /* browser fingerprint: Firefox 148 or Chrome, by reality_fp_of */
    /* The ALPN protocol the transport needs, or NULL. Not read by the builder: the Hello always
     * offers "h2, http/1.1" like Chrome, or http/1.1 alone via reality_carrier.alpn_http11. */
    const char *alpn;

    /* PLAIN TLS, NO REALITY (security=tls).
     *
     * The same ClientHello (Chrome's or Firefox's look by fp, the post-quantum share; GREASE
     * only in Chrome's) without the authenticator: session_id is random bytes, as a browser's,
     * and pbk is not needed. There is no separate builder because the browser look must live in
     * ONE place: two copies drift apart, and the symptom is not a build error but a node that
     * stops working for no visible reason.
     *
     * An explicit flag rather than "pbk is empty": a typo in the key must not silently turn
     * Reality into plain TLS, a downgrade that looks like a working node. */
    int plain;

    /* A REAL X25519MLKEM768 exchange, like uTLS HelloChrome_131 and later and Go 1.24+ (which
     * Xray-core uses).
     *
     * Unlike carrier.pq (1216 bytes of random noise, for size only), this is a real ML-KEM-768
     * key (sc_mlkem768_keygen) plus an X25519 half that is the same key as the separate X25519
     * share. A Reality server on Go >= 1.24 picks the hybrid when offered; ServerHello then
     * carries the ciphertext (1088) and its X25519 half (32), and the key schedule secret is
     * mlkem_ss ‖ x25519_ss (draft-ietf-tls-ecdhe-mlkem: ML-KEM first). The ML-KEM private key
     * goes to reality_state.mlkem_dk and on to tls13_auth.mlkem_dk. Such a server rejects noise
     * in place of the key (Go validates it), so carrier.pq cannot replace this. */
    int pq;
};

struct reality_state {
    unsigned char priv[32];        /* our ephemeral private key */
    unsigned char pub[32];         /* its public half, sent in key_share */
    unsigned char shared[32];      /* secret shared with the server's static key (pbk) */
    unsigned char session_id[32];  /* sent as legacy_session_id: the authenticator, or random
                                    * bytes when plain */

    /* Authenticator key: HKDF-SHA256(ikm = shared secret, salt = Random[0..20),
     * info = "REALITY"). The server SIGNS its temporary certificate with the same key, which is
     * how it proves itself to us (tls13_handshake_auth; reality.go in Xray). So it lives until
     * the end of the handshake. Not set when plain or when a carrier fills session_id. */
    unsigned char authkey[32];

    /* ML-KEM-768 private key (FIPS 203, 2400 bytes) to decapsulate the server's answer; set
     * only with cfg.pq. Like priv, it lives on the caller's stack until the handshake ends. */
    unsigned char mlkem_dk[2400];
    int pq;
};

int reality_build_hello(const struct reality_cfg *cfg, struct reality_state *st,
                        unsigned char *out, size_t out_n, size_t *out_len);

/* Variations of the same ClientHello for callers that need a different payload or ALPN.
 *
 * They are optional fields of the one builder, not a second builder: the browser fingerprint
 * must live in ONE place, or two copies drift apart, and the symptom is not an error but a
 * Reality server silently answering with its camouflage site.
 *
 * The Hello with car == NULL is pinned byte for byte by tests/hellofreeze.c; a field left at
 * zero does not change it.
 *
 * Callback order matters to both sides: fill_ech first (the padding is part of the signed
 * bytes), then fill_sid (it signs the whole Hello with session_id zeroed). The peer checks in
 * the same order. */
struct reality_carrier {
    /* A ready ephemeral pair instead of a generated one, for a caller that needs the shared
     * secret BEFORE the Hello is built (to seal data into the ECH padding). */
    const unsigned char *priv;      /* 32 bytes, or NULL */
    const unsigned char *pub;       /* 32 bytes, required with priv */
    /* Offer X25519MLKEM768 in key_share, filled with random noise instead of a key.
     *
     * Current Chrome's ClientHello is about 1760 bytes and goes out in TWO segments because of
     * the 1216-byte post-quantum share. A Hello of about 540 bytes in one segment stands out by
     * size, by segment count and by supported_groups: "Chrome that offers no post-quantum
     * exchange" is an outdated Chrome.
     *
     * Only for a peer that never picks the hybrid; the VLESS client uses reality_cfg.pq. */
    int pq;
    /* Offer ONLY http/1.1 in ALPN instead of the usual "h2, http/1.1"; used by ws and
     * httpupgrade, as Xray does (its UConn.WebsocketHandshakeContext over uTLS).
     *
     * With h2 offered first, a WebSocket endpoint (e.g. behind Cloudflare) picks h2, and our
     * HTTP/1.1 Upgrade is garbage to it: the node sent an HTTP/2 preface before our request and
     * closed the connection. WebSocket over HTTP/2 exists (RFC 8441), but it is a separate
     * protocol for the same result. */
    int alpn_http11;
    /* Do not send application_settings (ALPS, 0x44cd).
     *
     * The extension says the client is ready to send application settings. A server that picks
     * h2 and accepts ALPS (Google's, e.g. dns.google) then expects the client's own
     * EncryptedExtensions with its ALPS block before the client Finished. A browser sends it,
     * tls13.c does not, so a completed handshake ended in a fatal unexpected_message alert (10)
     * on the first read, before any HTTP/2 frame. Cloudflare and Quad9 do not accept ALPS.
     * Without the extension the server expects nothing. */
    int no_alps;
    /* Fill the ECH padding (176 bytes). NULL leaves random noise, as a browser without an ECH
     * config sends. */
    int (*fill_ech)(void *ctx, unsigned char *ech, size_t ech_n,
                    const unsigned char shared[32]);
    /* Fill the 32 bytes of session_id instead of the Reality authenticator. hs is the handshake
     * message with session_id ALREADY ZEROED: exactly the bytes the peer can rebuild. */
    int (*fill_sid)(void *ctx, unsigned char sid[32],
                    const unsigned char *hs, size_t hs_n,
                    const unsigned char shared[32]);
    void *ctx;
};

int reality_build_hello_carry(const struct reality_cfg *cfg, struct reality_state *st,
                              const struct reality_carrier *car,
                              unsigned char *out, size_t out_n, size_t *out_len);

/* Primitives of reality.c for tls13.c, ech.c, trsec.c and trvenc.c (VLESS encryption). They are
 * wrappers, not copies; see their definitions in reality.c. */
int xc_random(unsigned char *out, size_t n);
/* base64url (padded or not) to bytes; returns the length or -1. For node keys (pqv, VLESS
 * encryption keys). */
int xc_b64url_decode(const char *in, unsigned char *out, size_t out_n);
int xc_cpu_has_aes(void);
int xc_x25519_keypair(unsigned char priv[32], unsigned char pub[32]);
int xc_x25519_public(const unsigned char priv[32], unsigned char pub[32]);
int x25519_shared_ext(const unsigned char priv[32], const unsigned char peer[32],
                      unsigned char out[32]);

#endif
