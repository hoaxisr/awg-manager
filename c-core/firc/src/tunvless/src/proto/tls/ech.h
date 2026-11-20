/* Encrypted Client Hello (RFC 9849, formerly draft-ietf-tls-esni), client side, for security=tls.
 *
 * The ClientHello carries the real server name (SNI) in the clear. With ECH the wire carries a
 * ClientHelloOuter with a harmless name (public_name of the ECHConfig, usually the ECH provider's),
 * and the real ClientHelloInner sits in one of its extensions, encrypted to the server's public key
 * (HPKE). A server with the private key decrypts Inner and runs the handshake on it; anyone else
 * sees only Outer. The client learns that the server accepted ECH from the last eight bytes of
 * ServerHello.random; that check is in tls13.c, this file only builds the Hello.
 *
 * Here: parsing the ECHConfigList (ech_pick), HPKE (ech_hpke_seal: DHKEM(X25519, HKDF-SHA256),
 * HKDF-SHA256, AES-128-GCM or ChaCha20-Poly1305 — what ECH servers offer in practice: Cloudflare,
 * Go crypto/tls, BoringSSL) and building the Outer/Inner pair from a finished ClientHello
 * (ech_wrap). reality.c builds the Hello with the real SNI and that Hello becomes Inner; ECH only
 * wraps the finished bytes, so the Hello's shape does not depend on ECH and the frozen fingerprints
 * (tests/hellofreeze.c) do not change.
 *
 * Not supported: compressing Inner with ech_outer_extensions (Inner goes in full: a larger Hello,
 * but simpler and accepted by any server), HelloRetryRequest with ECH, fetching the ECHConfigList
 * from DNS (HTTPS record): the list comes from the node's link (`ech=`). GREASE ECH needs nothing
 * here: the Hello builder always sends it, and ech_wrap puts the real extension in its place. */
#ifndef STEER_ECH_H
#define STEER_ECH_H
#include <stddef.h>
#include <stdint.h>

#define ECH_EPARSE    (-90)   /* malformed ECHConfigList or ClientHello */
#define ECH_ENOCONFIG (-91)   /* no usable entry (version, KEM, cipher suite, extensions) */
#define ECH_ECRYPTO   (-92)
#define ECH_ETOOBIG   (-93)   /* the result does not fit the buffer */

/* The full ClientHelloInner handshake message (with its 4-byte header), for the transcript when
 * the server accepts ECH. A Hello with ML-KEM in key_share is about 1.8 KB; the rest is headroom
 * for the extension and the name. */
#define ECH_INNER_MAX 3072

struct ech_cfg {
    uint8_t  config_id;
    uint16_t kdf_id, aead_id;       /* HKDF-SHA256 (1); AES-128-GCM (1) or ChaCha20-Poly1305 (3) */
    uint8_t  pk[32];                /* the server's X25519 public key */
    uint8_t  max_name;              /* maximum_name_length: Inner is padded to this name length */
    char     public_name[256];      /* SNI of the outer Hello */
    uint8_t  raw[1024];             /* the whole ECHConfig (version, length, body): in HPKE info */
    size_t   raw_n;
};

struct ech_state {
    uint8_t random[32];             /* Inner's random: input of the acceptance check */
    size_t  inner_n;
    uint8_t inner[ECH_INNER_MAX];
};

/* base64 (standard or URL-safe alphabet; `=`, spaces and line breaks are skipped) to bytes;
 * returns the length or -1. */
int ech_b64_decode(const char *in, uint8_t *out, size_t cap);

/* Picks the first ECHConfig of version 0xfe0d with KEM X25519, a suite of HKDF-SHA256 and
 * AES-128-GCM or ChaCha20-Poly1305, a valid public_name and no mandatory extensions (high bit of
 * the type). 0 — picked. */
int ech_pick(const uint8_t *list, size_t n, struct ech_cfg *out);

/* HPKE base mode: encrypts pt to cfg->pk. info = "tls ech\0" ‖ ECHConfig (RFC 9849, 6.1).
 * eph is the ephemeral private key (for known-answer tests) or NULL for a random one.
 * enc gets 32 bytes (the encapsulated key), ct gets pt_n + 16 bytes. */
int ech_hpke_seal(const struct ech_cfg *cfg, const uint8_t *eph,
                  const uint8_t *aad, size_t aad_n, const uint8_t *pt, size_t pt_n,
                  uint8_t enc[32], uint8_t *ct);

/* From a finished ClientHello with the real SNI (a whole TLS record, with its 5-byte header) builds
 * the ClientHelloOuter record in out and keeps Inner in st. 0 — done. */
int ech_wrap(const struct ech_cfg *cfg, const uint8_t *hello, size_t hello_n,
             uint8_t *out, size_t cap, size_t *out_n, struct ech_state *st);

#endif
