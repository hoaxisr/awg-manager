/* Server certificate checks: the chain for security=tls, the proof for Reality.
 *
 * A separate file and not part of tls13.c on purpose: the TLS record layer knows nothing about
 * X.509, and one command proves it (`grep -n 'sc_chain\|sc_roots\|sc_cert'
 * src/proto/tls/tls13.c` prints nothing). Reality by design does not check the chain (it belongs
 * to someone else's camouflage site), so the record path must stay free of X.509. tls13.c calls
 * cert_verify_server_ex and cert_reality_check*, which call the primitives layer
 * (sc_chain_verify, sc_cert_verify_sig, sc_hmac, sc_mldsa65_verify).
 */
#ifndef STEER_CERTVERIFY_H
#define STEER_CERTVERIFY_H
#include <stddef.h>

#define CERTV_EPARSE   (-70)   /* malformed Certificate or CertificateVerify */
#define CERTV_ENOROOTS (-71)   /* the root store could not be read: nothing to check against */
#define CERTV_ECHAIN   (-72)   /* the chain does not reach a root, or the name does not match */
#define CERTV_ESIG     (-73)   /* bad CertificateVerify signature */
#define CERTV_EALG     (-74)   /* a CertificateVerify scheme we do not accept */

/* The root store used when the caller passes NULL or "". The path is a parameter, not
 * hardcoded: trsec.c passes what roots.c picked (--ca, or the tests' bundle via g_cert_roots). */
#define CERTV_DEFAULT_ROOTS "/etc/ssl/certs/ca-certificates.crt"

/* Authenticates the server by the TLS 1.3 rules (RFC 8446 §4.4.2 and §4.4.3).
 *
 * cert_body / cert_n     — the BODY of the Certificate message, without its 4-byte header;
 * cv_body  / cv_n        — the body of CertificateVerify;
 * transcript / thash_n   — Transcript-Hash of the messages up to and including Certificate,
 *                          i.e. exactly what the server signed;
 * host                   — the name the certificate must carry;
 * roots                  — path to the root store, or NULL/"" for the default.
 *
 * 0 — the server is authentic; otherwise one of the codes above. They mean DIFFERENT things:
 * "nothing to check against" is not "checked and failed", and the user needs to see which one
 * made the node unusable.
 */
int cert_verify_server(const unsigned char *cert_body, size_t cert_n,
                       const unsigned char *cv_body, size_t cv_n,
                       const unsigned char *transcript, size_t thash_n,
                       const char *host, const char *roots);

#define CERTV_EPIN     (-77)   /* the certificate matches no pinned fingerprint */

/* Node rules beyond the default "chain to the roots and the SNI name". They follow the client
 * side of Xray-core (transport/internet/tls/config.go, verifyPeerCert):
 *
 *   pcs — pinnedPeerCertSha256: SHA-256 (hex, comma-separated) of a whole certificate (DER). The
 *         LEAF matches: the server is accepted without checking chain, validity or name; the pin
 *         is the trust. An intermediate or root that is a CA matches: the chain is checked up to
 *         THAT certificate instead of the system roots. Nothing matches: rejected, the system
 *         roots are not consulted.
 *   pks — the same for sing-box (certificate_public_key_sha256): SHA-256 of the leaf's
 *         SubjectPublicKeyInfo. A match accepts the server like a matching pcs leaf.
 *   vcn — verifyPeerCertByName: comma-separated names to check the chain against INSTEAD of the
 *         SNI; any one of them will do.
 *   insecure — chain, name, validity and pins are not checked at all. The CertificateVerify
 *         signature still is (Go checks it too before calling its VerifyPeerCertificate): without
 *         it the peer does not even prove it holds the key of the certificate it sent. Only
 *         --insecure turns this on; a subscription alone does not.
 *
 * pcs and pks arrive normalized (sl_add_pins in sublink.c): 64 lowercase hex digits each,
 * comma-separated. */
struct cert_policy {
    const char *pcs, *pks, *vcn;
    int insecure;
};

/* cert_verify_server with node rules; pol == NULL — same as cert_verify_server. */
int cert_verify_server_ex(const unsigned char *cert_body, size_t cert_n,
                          const unsigned char *cv_body, size_t cv_n,
                          const unsigned char *transcript, size_t thash_n,
                          const char *host, const char *roots, const struct cert_policy *pol);

#define CERTV_ENOTREALITY (-75) /* the server did not prove it is Reality: it did not accept us */
#define CERTV_EPQ         (-76) /* accepted by Reality, but no valid ML-DSA-65 signature (pqv) */

/* Checks that the peer is THE Reality server.
 *
 * A server that accepts the client issues a temporary certificate with an Ed25519 key and puts
 * HMAC-SHA512(authkey, public key) into the signature field instead of a signature. Only the
 * holder of the static key can compute it: authkey derives from the secret shared with it. A
 * mismatch means we were NOT accepted and are talking to the camouflage site the server passed
 * us to. Without this check a failed Reality handshake looks like a success, and only the first
 * byte of the answer to a VLESS request would tell.
 *
 * No X.509 is needed: parsing is a few DER steps and the check is one HMAC, so Reality does not
 * depend on the X.509 code of security=tls.
 *
 * cert_body / cert_n — the body of the Certificate message, without its 4-byte header;
 * authkey            — 32 bytes from struct reality_state.
 */
int cert_reality_check(const unsigned char *cert_body, size_t cert_n,
                       const unsigned char *authkey);

/* The second half of the Reality proof: the ML-DSA-65 signature (`mldsa65Verify`, `pqv` in the
 * link).
 *
 * A server with mldsa65Seed puts into the only extension of the temporary certificate a
 * signature (3309 bytes) over HMAC-SHA512(authkey, ed25519_pub ‖ ClientHello ‖ ServerHello),
 * both messages whole, with the 4-byte handshake header, as sent and received (xtls/reality,
 * handshake_server_tls13.go). Call it ONLY after cert_reality_check: the ML-DSA signature means
 * nothing without the first half.
 *
 * pk is 1952 bytes. 0 — valid; CERTV_EPQ — no extension, wrong length or a bad signature;
 * CERTV_EPARSE — the certificate does not parse. */
int cert_reality_check_pq(const unsigned char *cert_body, size_t cert_n,
                          const unsigned char *authkey, const unsigned char *pk,
                          const unsigned char *ch, size_t ch_n,
                          const unsigned char *sh, size_t sh_n);

/* A readable explanation of a code; "" for 0. */
const char *cert_verify_strerror(int rc);

#endif
