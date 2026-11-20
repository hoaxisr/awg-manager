/* TLS 1.3 records for Reality and security=tls; why not a library TLS stack is explained in
 * tls13.c. Crypto goes through src/lib/scrypto.h: this header has no crypto-library types, so
 * `make test` programs built without the library include it too (h2match, tunnelmatch...). */
#ifndef STEER_TLS13_H
#define STEER_TLS13_H
#include <stdint.h>
#include <stddef.h>
#include "scrypto.h"

#define TLS13_MAX_REC   16640          /* 2^14 + 256: RFC 8446 limit for a protected record */
#define TLS13_MAX_PLAIN 16384
#define TLS13_RAMP_STEP 1186
/* ServerHello bytes kept for the ML-DSA check (about 1200 with the hybrid). */
#define TLS13_SH_KEEP   2048

#define TLS13_EIO          (-10)
#define TLS13_ECLOSED      (-11)
#define TLS13_EBADREC      (-12)
#define TLS13_ETOOBIG      (-13)
#define TLS13_EAUTH        (-14)   /* AEAD failed: our keys diverged from the server's */
#define TLS13_ECRYPTO      (-15)
#define TLS13_ENOKEYSHARE  (-16)   /* ServerHello without key_share: not TLS 1.3 */
#define TLS13_EBADSUITE    (-17)
#define TLS13_EFINISHED    (-18)   /* Finished mismatch: wrong transcript or keys */
#define TLS13_ESTATE       (-19)
/* No whole record in the socket yet. NOT an error: the read refused to block, because blocking
 * in the middle of a record stalls the whole loop, not one connection. */
#define TLS13_EAGAIN       (-20)

/* The node did not answer in time.
 *
 * Kept apart from TLS13_EIO on purpose: the two have different causes and call for different
 * action. The handshake reads a blocking socket with SO_RCVTIMEO (sock_ready in trdial.c), and
 * when the timeout expires read returns EAGAIN. Silence means OUR packets do not reach the node
 * (SNI filtering, the node is down, packet loss), so the cause is outside, not in the record
 * parser. A provider dropping the ClientHello by its SNI looks exactly like this: a healthy TCP
 * connection, a live node, and no answer. */
#define TLS13_ETIMEOUT     (-21)
/* The server did not prove its identity: chain, name or signature. Unlike EFINISHED (diverged
 * keys), the handshake is mathematically sound but the peer is the wrong one. The exact reason
 * comes from certverify.h through tls13_verify_reason(). */
#define TLS13_ECERT        (-22)
/* The Hello went out with ECH (Encrypted Client Hello) and the server did not accept it: no
 * confirmation in ServerHello.random. The handshake stops at once, before any certificate work:
 * the server answered the outer Hello, so it does not talk to us as a client of the real name
 * (the ECH key in the link is stale, or the server does not know ECH). Going on would reveal the
 * name in the clear, which ECH exists to prevent. */
#define TLS13_EECH         (-23)
#define TLS13_EGROUP       (-24)
#define TLS13_EHRR         (-25)

enum tls13_aead { TLS13_AEAD_AES128, TLS13_AEAD_AES256, TLS13_AEAD_CHACHA };

struct tls13_keys {
    enum tls13_aead aead;
    size_t key_n;
    unsigned char key[32];
    unsigned char iv[12];
    /* Cipher context, set up ONCE per connection: setkey for GCM expands the key schedule and
     * builds the GHASH table, a fixed cost per call whatever the record size, which dominates
     * with small records (about 300 bytes with Vision's direct copy). */
    int ctx_ready;
    struct sc_aead ctx;
};
/* WARNING: do not copy this struct by value after tls13_keys_setup. A copy is two instances of
 * one key state and two sc_aead_free calls; with a context that holds a pointer (as mbedtls's
 * GCM did) that is a double free. wolfCrypt behind scrypto keeps the whole context inside the
 * struct, but the layer cannot promise the library never adds a pointer. Pass a pointer; for a
 * second instance, set it up from the key again. */

struct fm_link;

struct tls13 {
    int fd;
    struct fm_link *fm;
    int ready;
    /* Read buffer: take everything the socket has in one call and assemble records from here.
     * Without it each record cost several system calls and a wait for the rest of it
     * (16,000 reads a second of 600 bytes, 80% of loop time inside reads). The price is 16 KB
     * per connection, 1 MB at 64 connections. */
    unsigned char rbuf[TLS13_MAX_REC + 8];
    size_t rbuf_n;      /* bytes held */
    size_t rbuf_off;    /* bytes of them already parsed */
    /* The ALPN protocol the server chose, from EncryptedExtensions; "" if it sent none (as a
     * Reality server that accepted us does).
     *
     * transport.c checks it against what the transport needs (h2 for grpc and xhttp, http/1.1
     * for ws and httpupgrade). With another protocol the handshake works but no data flows;
     * without this field the symptom is "the node connects and stays silent", which cannot be
     * told from a closed port. */
    char alpn[16];
    struct tls13_keys rd, wr;
    /* Record counters. NEVER reset: a reset repeats a nonce, which voids AEAD entirely. */
    uint64_t rd_seq, wr_seq;
    /* TWO transcripts at once, SHA-256 and SHA-384.
     *
     * The key schedule hash depends on the cipher suite, known only from ServerHello, after the
     * ClientHello must already be hashed. Keeping both and dropping one is cheaper than
     * buffering the handshake messages. Chrome's list offers AES_256_GCM_SHA384, and the list
     * must match the browser's, so that suite must be served too. */
    struct sc_hash_ctx tr;
    struct sc_hash_ctx tr384;
    size_t hash_n;                  /* 32 or 48, known after ServerHello */
};

/* client_hello: the record ALREADY sent to the server, 5-byte record header included (for the
 * transcript).
 * shared_secret: despite the name, our ephemeral X25519 PRIVATE key (reality_state.priv). The
 * TLS secret needs the server's ephemeral key, which only arrives in ServerHello. */
int tls13_handshake(struct tls13 *t, int fd,
                    const unsigned char *client_hello, size_t hello_n,
                    const unsigned char *shared_secret);

/* How the server proves itself. reality_key and host are both optional and could in principle
 * be combined, but a real node uses exactly one of them.
 *
 * A TLS 1.3 handshake alone does not prove WHO is on the other end: Finished verifies for
 * anyone who ran the key exchange, a man in the middle included. Only the certificate shows
 * it, and it does so differently for the two kinds of node.
 *
 * reality_key: the 32-byte authkey from struct reality_state. A Reality server that recognised
 *   the client puts an HMAC-SHA512 under this key in the signature field of its temporary
 *   certificate; only the holder of the static key can compute it. A mismatch means we were
 *   not recognised and the camouflage site answered. Checking the chain is pointless here: the
 *   certificate is real, but someone else's.
 *
 * host: the name the certificate must contain (also sent as SNI). This is security=tls: there
 *   is no static key, and the proof is the chain to a root plus the CertificateVerify signature
 *   over the transcript.
 *
 * roots: path to the root store, NULL or "" for the default. Only with host. */
struct cert_policy;
/* ECH: the inner ClientHello (ech.h, ech_state) for the acceptance check and the transcript.
 * A type of its own rather than ech_state, so tls13.c does not depend on ech.c; trsec.c fills
 * it. */
struct tls13_ech {
    const unsigned char *inner;     /* ClientHelloInner handshake message with its 4-byte header */
    size_t inner_n;
    const unsigned char *random;    /* 32 bytes: the inner random */
};
struct tls13_auth {
    const unsigned char *reality_key;
    const char *host;
    const char *roots;
    /* ML-KEM-768 private key (2400 bytes) when the ClientHello offered a real X25519MLKEM768
     * hybrid (reality_cfg.pq). NULL: no hybrid offered, and a ServerHello choosing it is
     * rejected. */
    const unsigned char *mlkem_dk;
    /* ML-DSA-65 public key (1952 bytes) from the node's mldsa65Verify (`pqv` in a link), or
     * NULL. With it the Reality certificate must carry an ML-DSA-65 signature over
     * HMAC-SHA512(authkey, pub ‖ ClientHello ‖ ServerHello); without one, or with a wrong one,
     * the node is not recognised. Only with reality_key. */
    const unsigned char *mldsa_pk;
    /* Certificate rules when host != NULL (certverify.h): pins, names, insecure. NULL: the
     * default, chain to the roots and the name host. */
    const struct cert_policy *policy;
    /* Set when the Hello passed to tls13_handshake_auth is an ECH ClientHelloOuter; NULL: no
     * ECH. */
    const struct tls13_ech *ech;
    unsigned cert_algs;
};

/* tls13_handshake plus the server authentication described by auth. */
int tls13_handshake_auth(struct tls13 *t, int fd,
                         const unsigned char *client_hello, size_t hello_n,
                         const unsigned char *shared_secret,
                         const struct tls13_auth *auth);

/* Why the server failed to prove itself, as a string, after TLS13_ECERT; "" if it did not
 * fail. Per thread: several connectors handshake at once. */
const char *tls13_verify_reason(void);

unsigned tls13_server_group(void);

/* Whether a WHOLE record, ready for decryption, is in the buffer.
 *
 * Readiness is asked of the kernel (epoll), but the data may already be with US: one socket
 * read brings up to 16 KB, often several records. Parsing one and leaving makes the next wait
 * for a socket event that may never come; the TAIL of a response then stalls until the
 * client's retransmission timeout ("the page almost loaded and froze"). */
int tls13_has_record(const struct tls13 *t);

/* Bytes read from the socket and not yet parsed. For direct-copy mode, where there are no
 * records and a "whole record" means nothing. */
size_t tls13_buffered(const struct tls13 *t);

/* Take the bytes already read from the socket but not yet parsed as records.
 *
 * Needed in one case: the server switched to direct copy, and what follows in the socket is no
 * longer records. Part of that raw stream may already be in our buffer; reading the socket
 * directly without taking it loses a piece and desynchronises us from the server (Vision nodes
 * then deliver zero bytes). */
size_t tls13_take_pending(struct tls13 *t, unsigned char *out, size_t cap);

int tls13_seal(struct tls13 *t, const unsigned char *d, size_t n, unsigned char *out, size_t cap, size_t *out_n);

/* The most plaintext the next application record may carry: 1186 per record sent, 16384 from the 14th. */
static inline size_t tls13_plain_cap_at(uint64_t seq) {
    return seq >= 13 ? (size_t)TLS13_MAX_PLAIN : (size_t)TLS13_RAMP_STEP * (size_t)(seq + 1);
}
static inline size_t tls13_plain_cap(const struct tls13 *t) { return tls13_plain_cap_at(t->wr_seq); }
int tls13_write(struct tls13 *t, const unsigned char *data, size_t n);
int tls13_read(struct tls13 *t, unsigned char *out, size_t cap, size_t *got);

/* The same read WITHOUT A COPY: returns a pointer into the connection buffer, where the record
 * is decrypted in place. All downloaded traffic passes here, and a copy costs an extra pass over
 * memory per record, which is noticeable on a router's slow memory.
 *
 * The one hard rule for the caller: use the data BEFORE the next read on this connection. The
 * next read may move the unread bytes to the start of the buffer, and the old pointer then shows
 * other bytes; breaking the rule looks like corrupt data in the middle of a file. */
int tls13_read_ref(struct tls13 *t, const unsigned char **body, size_t *body_n);

/* Free the cipher and transcript contexts; this wipes the expanded cipher keys (key[] and iv[]
 * in struct tls13_keys are left as they are). Required on every close: thousands of connections
 * pass in an hour, and a key schedule not wiped stays in memory until the struct is reused.
 * Harmless on a zeroed struct and when called twice. */
void tls13_free(struct tls13 *t);

/* ---- AEAD apart from TLS records --------------------------------------------
 *
 * Record encryption without the record format: two ciphers, the nonce derived from the record
 * number, in-place operation. Two lessons from measurements:
 *
 *   - the cipher context is set up ONCE per direction (tls13_keys_setup), not per record:
 *     setkey expands the key schedule and builds the GHASH table, a fixed cost whatever the
 *     record size, which dominates with small records;
 *   - on decryption the tag is COPIED before the call (inside tls13_aead_open): decryption runs
 *     in place, the tag sits right after the ciphertext, and there is no reason to depend on
 *     whether the implementation touches it while finishing the last partial block
 *     (tests/scryptomatch.c checks in-place sizes up to 16401 bytes).
 *
 * seq is the record number the nonce is derived from. It must be unique per key, or AEAD
 * protects nothing. */
int  tls13_keys_setup(struct tls13_keys *k);
void tls13_keys_free(struct tls13_keys *k);
int  tls13_aead_seal(struct tls13_keys *k, uint64_t seq,
                     const unsigned char *aad, size_t aad_n,
                     unsigned char *buf, size_t n, unsigned char *tag);
int  tls13_aead_open(struct tls13_keys *k, uint64_t seq,
                     const unsigned char *aad, size_t aad_n,
                     unsigned char *buf, size_t n);

#endif
