/* TLS 1.3 handshake and record layer for a ClientHello built by reality.c.
 *
 * Reality is real TLS 1.3 with one twist in authentication: the client proves itself with the
 * authenticator in session_id, and a server that recognised it sends a temporary certificate
 * with an HMAC under the authenticator key in the signature field (certverify.h). The rest is
 * plain RFC 8446: ServerHello brings the server's key_share, traffic keys come from the shared
 * secret, records use AEAD.
 *
 * No chain is checked for Reality: a server that did not recognise us hands the connection to
 * the third-party site it hides behind, whose genuine certificate proves nothing about the
 * server. That is why there is no library TLS stack here: it would insist on chain checks and
 * build its own ClientHello, while ours must look like a browser's (reality.c). Only primitives
 * are needed, through the scrypto layer.
 *
 * Only the needed part of the handshake exists: parse ServerHello, derive keys, check Finished
 * (plus the certificate check for security=tls). Resumption, client certificates,
 * HelloRetryRequest and post-handshake messages are not supported: no node needs them, and
 * each would be code that never runs and so is never tested.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <poll.h>

#include "scrypto.h"
#include "certverify.h"
#include "certzip.h"
#include "tls13.h"
#include "fmlink.h"
#include "transport.h"

/* From reality.c: the same X25519, with the peer key from ServerHello. Shared rather than
 * copied, so clamping and byte order cannot diverge between two copies. */
int x25519_shared_ext(const unsigned char priv[32], const unsigned char peer[32],
                      unsigned char out[32]);

/* ---- HKDF-Expand-Label, RFC 8446 §7.1 -------------------------------------- */
/* The label has a strict format: output length, "tls13 " + label, context. One wrong byte
 * gives keys that differ from the server's, which shows up as failing decryption after the
 * handshake. */
static int expand_label(enum sc_hash md,
                        const unsigned char *secret, size_t secret_n,
                        const char *label,
                        const unsigned char *ctx, size_t ctx_n,
                        unsigned char *out, size_t out_n) {
    unsigned char info[512];
    size_t i = 0;
    size_t llen = strlen(label);
    if (6 + llen > 255 || ctx_n > 255 || 4 + 6 + llen + ctx_n > sizeof(info)) return -1;
    info[i++] = (unsigned char)(out_n >> 8);
    info[i++] = (unsigned char)out_n;
    info[i++] = (unsigned char)(6 + llen);
    memcpy(info + i, "tls13 ", 6); i += 6;
    memcpy(info + i, label, llen); i += llen;
    info[i++] = (unsigned char)ctx_n;
    if (ctx_n) { memcpy(info + i, ctx, ctx_n); i += ctx_n; }
    return sc_hkdf_expand(md, secret, secret_n, info, i, out, out_n);
}

static int derive_secret(enum sc_hash md, const unsigned char *secret,
                         const char *label, const unsigned char *thash, size_t hash_n,
                         unsigned char *out) {
    return expand_label(md, secret, hash_n, label, thash, hash_n, out, hash_n);
}

/* ---- transcript ------------------------------------------------------------ */
/* The hash of all handshake messages in order. It enters every key derivation, so any
 * divergence from the server (an extra byte, a missing message) breaks the keys and is caught
 * as a bad Finished.
 *
 * Hash failures are not returned: an initialised SHA-2 context has nothing that can fail (no
 * allocation, no device), and if it ever does, the hash comes out wrong and the handshake fails
 * with TLS13_EFINISHED rather than passing with foreign keys. A failed init leaves the context
 * unset (alg == 0), and every later call on it fails too. */
static void tr_init(struct tls13 *t) {
    sc_hash_init(&t->tr, SC_SHA256);
    sc_hash_init(&t->tr384, SC_SHA384);
}
static void tr_add(struct tls13 *t, const unsigned char *d, size_t n) {
    sc_hash_update(&t->tr, d, n);
    sc_hash_update(&t->tr384, d, n);
}
/* The transcript hash with the algorithm the server chose, on a copy of the context: the
 * transcript goes on after each key derivation. */
static void tr_hash(const struct tls13 *t, unsigned char *out) {
    struct sc_hash_ctx c;
    if (sc_hash_clone(&c, t->hash_n == 48 ? &t->tr384 : &t->tr) != 0) {
        memset(out, 0, t->hash_n == 48 ? 48 : 32);
        return;
    }
    sc_hash_final(&c, out);
    sc_hash_free(&c);
}

/* ---- reading records ------------------------------------------------------- */
/* Reading exactly n bytes from the link, never past them, is for the handshake only, which is
 * synchronous by nature. */
static int read_full(struct tls13 *t, unsigned char *buf, size_t n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = t->fm ? fm_recv(t->fm, t->fd, buf + got, n - got, 0, 0) : read(t->fd, buf + got, n - got);
        if (r == 0) return TLS13_ECLOSED;
        if (r < 0) {
            if (errno == EINTR) continue;
            if (t->fm && errno == EPROTO) return TR_EMASK;
            /* The handshake socket is blocking with SO_RCVTIMEO, so EAGAIN here means only
             * that the node did not answer in time: not an I/O error (see TLS13_ETIMEOUT). */
            if (errno == EAGAIN || errno == EWOULDBLOCK) return TLS13_ETIMEOUT;
            return TLS13_EIO;
        }
        got += (size_t)r;
    }
    return 0;
}

/* Records come from the connection buffer. The socket is read in LARGE chunks: one read()
 * takes everything pending (up to 16 KB), and records are served from the buffer without new
 * calls.
 *
 * The data stream passes may_wait = 0: poll first, never block, and leave an incomplete record
 * in the buffer so the loop moves on to other connections. The handshake, which must wait,
 * reads the link directly (read_record_fd). */
/* The remainder moves to the start of the buffer only when the free tail runs short: with
 * medium records (4-8 KB, common with Xray) an unconditional move would memmove kilobytes on
 * every read. The threshold lets one read take a useful chunk rather than a hundred bytes at a
 * time. */
#define RBUF_MIN_FILL 4096

static int rbuf_fill(struct tls13 *t, int may_wait) {
    /* Compact first, then check for overflow: a full buffer with a moved start is not an
     * overflow but room to reclaim. */
    if (t->rbuf_off && sizeof(t->rbuf) - t->rbuf_n < RBUF_MIN_FILL) {
        if (t->rbuf_n > t->rbuf_off)
            memmove(t->rbuf, t->rbuf + t->rbuf_off, t->rbuf_n - t->rbuf_off);
        t->rbuf_n -= t->rbuf_off;
        t->rbuf_off = 0;
    }
    if (t->rbuf_n >= sizeof(t->rbuf)) return TLS13_ETOOBIG;

    if (!may_wait && !fm_pending(t->fm)) {
        struct pollfd p = { .fd = t->fd, .events = POLLIN };
        int pr = poll(&p, 1, 0);
        if (pr <= 0 || !(p.revents & POLLIN)) return TLS13_EAGAIN;
    }
    unsigned char *at = t->rbuf + t->rbuf_n;
    size_t room = sizeof(t->rbuf) - t->rbuf_n;
    ssize_t r = t->fm ? fm_recv(t->fm, t->fd, at, room, may_wait ? 0 : MSG_DONTWAIT, 0) : read(t->fd, at, room);
    if (r == 0) return TLS13_ECLOSED;
    if (r < 0) {
        if (errno == EINTR) return 0;
        if (t->fm && errno == EPROTO) return TR_EMASK;
        if (errno == EAGAIN || errno == EWOULDBLOCK) return TLS13_EAGAIN;
        return TLS13_EIO;
    }
    t->rbuf_n += (size_t)r;
    return 0;
}

/* For the handshake: it waits, and it reads exactly one record at a time, so nothing past its
 * last record leaves the socket before the connection buffer takes over. */
static int read_record_fd(struct tls13 *t, unsigned char *type, unsigned char *body, size_t cap,
                          size_t *body_n) {
    unsigned char h[5];
    int rc = read_full(t, h, 5);
    if (rc) return rc;
    size_t len = ((size_t)h[3] << 8) | h[4];
    if (len > cap) return TLS13_ETOOBIG;
    rc = read_full(t, body, len);
    if (rc) return rc;
    *type = h[0];
    *body_n = len;
    return 0;
}

static int read_record(struct tls13 *t, unsigned char *type, unsigned char **body,
                       size_t *body_n, int may_wait) {
    for (int guard = 0; guard < 64; guard++) {
        size_t have = t->rbuf_n - t->rbuf_off;
        if (have >= 5) {
            const unsigned char *h = t->rbuf + t->rbuf_off;
            size_t len = ((size_t)h[3] << 8) | h[4];
            if (len > TLS13_MAX_REC) return TLS13_EBADREC;
            if (have >= 5 + len) {
                *type = h[0];
                *body = t->rbuf + t->rbuf_off + 5;
                *body_n = len;
                t->rbuf_off += 5 + len;
                return 0;
            }
        }
        int rc = rbuf_fill(t, may_wait);
        if (rc) return rc;
    }
    return TLS13_EAGAIN;
}

static int send_all(struct tls13 *t, const unsigned char *d, size_t n) {
    if (t->fm) {
        ssize_t w = fm_send(t->fm, t->fd, d, n, 0);
        return w < 0 ? (int)w : 0;
    }
    size_t sent = 0;
    while (sent < n) {
        ssize_t w = write(t->fd, d + sent, n - sent);
        if (w <= 0) {
            if (w < 0 && errno == EINTR) continue;
            return TLS13_EIO;
        }
        sent += (size_t)w;
    }
    return 0;
}

/* ---- AEAD ------------------------------------------------------------------ */
/* TLS 1.3 nonce: iv XOR the right-aligned record number. Each direction has its own counter,
 * NEVER reset: a reset repeats a nonce, which voids AEAD entirely. */
static void aead_nonce(const unsigned char iv[12], uint64_t seq, unsigned char out[12]) {
    memcpy(out, iv, 12);
    for (int i = 0; i < 8; i++)
        out[11 - i] ^= (unsigned char)(seq >> (8 * i));
}

/* Called once per direction when the traffic keys are ready; every record then uses the
 * ready context. */
int tls13_keys_setup(struct tls13_keys *k) {
    if (k->ctx_ready) return 0;
    enum sc_aead_alg a;
    /* The key length is checked against the algorithm: the caller fills key_n, and a mismatch
     * would mean an AES-256 key expanded from sixteen bytes plus garbage. */
    switch (k->aead) {
        case TLS13_AEAD_AES128: a = SC_AES128_GCM; break;
        case TLS13_AEAD_AES256: a = SC_AES256_GCM; break;
        case TLS13_AEAD_CHACHA: a = SC_CHACHA20_POLY1305; break;
        default: return TLS13_ECRYPTO;
    }
    if (k->key_n != sc_aead_key_len(a)) return TLS13_ECRYPTO;
    if (sc_aead_setkey(&k->ctx, a, k->key) != 0) return TLS13_ECRYPTO;
    k->ctx_ready = 1;
    return 0;
}

void tls13_keys_free(struct tls13_keys *k) {
    sc_wipe(k->key, sizeof k->key);
    sc_wipe(k->iv, sizeof k->iv);
    if (!k->ctx_ready) return;
    sc_aead_free(&k->ctx);
    k->ctx_ready = 0;
}

/* Safe on a ZEROED struct and when called twice; this is a contract: tr_link_close calls it on
 * every TLS link, including one whose handshake never started. It relies on a zero algorithm or
 * ready flag meaning "empty" for every layer context (sc_hash_free and sc_aead_free then do
 * nothing), and on freeing setting it back to zero. */
void tls13_free(struct tls13 *t) {
    tls13_keys_free(&t->rd);
    tls13_keys_free(&t->wr);
    sc_hash_free(&t->tr);
    sc_hash_free(&t->tr384);
    t->ready = 0;
}

int tls13_aead_open(struct tls13_keys *k, uint64_t seq,
                     const unsigned char *aad, size_t aad_n,
                     unsigned char *buf, size_t n) {
    if (n < 16) return TLS13_EBADREC;
    if (!k->ctx_ready) return TLS13_ESTATE;
    unsigned char nonce[12];
    aead_nonce(k->iv, seq, nonce);
    size_t ct = n - 16;
    /* The tag is COPIED, not read from the same buffer. Decryption runs in place, and the tag
     * sits right after the ciphertext, where an implementation may write while finishing the
     * last partial block. tests/scryptomatch.c checks that wolfCrypt does not, at record sizes
     * up to 16401 bytes, but a 16-byte copy is cheaper than depending on library internals. */
    unsigned char tag[16];
    memcpy(tag, buf + ct, 16);
    int rc = sc_aead_open(&k->ctx, nonce, aad, aad_n, buf, ct, tag);
    return rc == 0 ? 0 : TLS13_EAUTH;
}

int tls13_aead_seal(struct tls13_keys *k, uint64_t seq,
                     const unsigned char *aad, size_t aad_n,
                     unsigned char *buf, size_t n, unsigned char *tag) {
    if (!k->ctx_ready) return TLS13_ESTATE;
    unsigned char nonce[12];
    aead_nonce(k->iv, seq, nonce);
    int rc = sc_aead_seal(&k->ctx, nonce, aad, aad_n, buf, n, tag);
    return rc == 0 ? 0 : TLS13_ECRYPTO;
}

/* The same two operations with a one-time context, for the HANDSHAKE: it handles three or four
 * records per connection, so the key setup cost does not matter, and no context needs freeing
 * on a dozen error paths. Persistent contexts are for the data stream only. */
static int aead_open_once(const struct tls13_keys *src, uint64_t seq,
                          const unsigned char *aad, size_t aad_n,
                          unsigned char *buf, size_t n) {
    struct tls13_keys k = *src;
    k.ctx_ready = 0;
    int rc = tls13_keys_setup(&k);
    if (rc == 0) rc = tls13_aead_open(&k, seq, aad, aad_n, buf, n);
    tls13_keys_free(&k);
    return rc;
}

static int aead_seal_once(const struct tls13_keys *src, uint64_t seq,
                          const unsigned char *aad, size_t aad_n,
                          unsigned char *buf, size_t n, unsigned char *tag) {
    struct tls13_keys k = *src;
    k.ctx_ready = 0;
    int rc = tls13_keys_setup(&k);
    if (rc == 0) rc = tls13_aead_seal(&k, seq, aad, aad_n, buf, n, tag);
    tls13_keys_free(&k);
    return rc;
}

/* ---- handshake ------------------------------------------------------------- */

/* The verification failure is kept PER THREAD, not in struct tls13: the caller turns it into
 * text from the error code alone (transport_strerror), after the link is closed. The thread
 * outlives the close, and connectors in different threads do not overwrite each other's
 * reasons. */
static __thread char g_verify_reason[96];

const char *tls13_verify_reason(void) { return g_verify_reason; }

static __thread unsigned g_server_group;

unsigned tls13_server_group(void) { return g_server_group; }

static const unsigned char HRR_RANDOM[32] = {
    0xCF, 0x21, 0xAD, 0x74, 0xE5, 0x9A, 0x61, 0x11, 0xBE, 0x1D, 0x8C, 0x02, 0x1E, 0x65, 0xB8, 0x91,
    0xC2, 0xA2, 0x11, 0x16, 0x7A, 0xBB, 0x8C, 0x5E, 0x07, 0x9E, 0x09, 0xE2, 0xC8, 0xA8, 0x33, 0x9C,
};

static size_t be24(const unsigned char *b) { return (size_t)b[0] << 16 | (size_t)b[1] << 8 | b[2]; }

static int cert_has_dc(const unsigned char *b, size_t n) {
    if (n < 4 || 1 + (size_t)b[0] + 3 > n) return 0;
    size_t p = 1 + b[0];
    size_t end = p + 3 + be24(b + p);
    if (end > n) return 0;
    for (p += 3; p + 3 <= end;) {
        p += 3 + be24(b + p);
        if (p + 2 > end) return 0;
        size_t xe = p + 2 + ((size_t)b[p] << 8 | b[p + 1]);
        if (xe > end) return 0;
        for (p += 2; p + 4 <= xe; p += 4 + ((size_t)b[p + 2] << 8 | b[p + 3]))
            if (((unsigned)b[p] << 8 | b[p + 1]) == 0x0022) return 1;
        p = xe;
    }
    return 0;
}

struct hs_secrets {
    unsigned char early[48], derived[48], hs_secret[48];
    unsigned char ecdhe[SC_MLKEM768_SS + 32];
    unsigned char c_hs[48], s_hs[48], fkey[48], cfkey[48];
    unsigned char master[48], c_ap[48], s_ap[48];
    struct tls13_keys c_hk, s_hk;
};

struct hs_bufs {
    unsigned char hs[40960];
    unsigned char cert[40960];
};

/* Takes the ClientHello record already sent (for the transcript) and our ephemeral private key.
 *
 * auth->host != NULL turns on the certificate check (security=tls); auth->reality_key, the
 * Reality check. Without either, Certificate and CertificateVerify still enter the transcript
 * but are not parsed.
 *
 * Returns with the traffic keys ready. */
static int handshake_run(struct tls13 *t, int fd,
                         const unsigned char *client_hello, size_t hello_n,
                         const unsigned char *our_priv,
                         const struct tls13_auth *auth, struct hs_secrets *k,
                         struct hs_bufs *b) {
    /* Computed once, so the checks in the parsing loop cannot drift apart. */
    const char *host = auth ? auth->host : NULL;
    const unsigned char *rkey = auth ? auth->reality_key : NULL;
    const int want_cert = (host != NULL) || (rkey != NULL);
    struct fm_link *fm = t->fm;
    memset(t, 0, sizeof(*t));
    t->fd = fd;
    t->fm = fm;
    g_server_group = 0;
    /* md and H are set after ServerHello: they depend on the cipher suite. */
    enum sc_hash md = SC_SHA256;
    size_t H = 0;

    tr_init(t);
    /* The transcript takes the handshake message without the record header. */
    tr_add(t, client_hello + 5, hello_n - 5);

    unsigned char rec[TLS13_MAX_REC];
    unsigned char type;
    size_t n;

    /* ServerHello. */
    int rc = read_record_fd(t, &type, rec, sizeof(rec), &n);
    if (rc) return rc;
    if (type != 0x16 || n < 44 || rec[0] != 0x02) return TLS13_EBADREC;
    if (!memcmp(rec + 6, HRR_RANDOM, 32)) return TLS13_EHRR;
    tr_add(t, rec, n);

    /* The server's key_share is among the extensions. It is found by type 0x33, not by
     * offset: the session_id length and the extension set vary between servers.
     * HS_HDR = 4: message type(1) + length(3). */
    const size_t HS_HDR = 4;
    unsigned char server_pub[32];
    /* X25519MLKEM768 hybrid: the server answers with the ML-KEM ciphertext (1088) and its
     * X25519 half (32). */
    unsigned char kem_ct[SC_MLKEM768_CT];
    int have_pub = 0, have_kem = 0;
    {
        size_t p = HS_HDR + 2 + 32;             /* header + version + random */
        if (p >= n) return TLS13_EBADREC;
        size_t sid_n = rec[p++];
        p += sid_n;
        p += 2;                                 /* cipher_suite */
        p += 1;                                 /* compression */
        if (p + 2 > n) return TLS13_EBADREC;
        size_t exts_n = ((size_t)rec[p] << 8) | rec[p + 1];
        p += 2;
        size_t end = p + exts_n;
        if (end > n) return TLS13_EBADREC;
        while (p + 4 <= end) {
            unsigned etype = ((unsigned)rec[p] << 8) | rec[p + 1];
            size_t elen = ((size_t)rec[p + 2] << 8) | rec[p + 3];
            p += 4;
            if (p + elen > end) break;
            if (etype == 0x0033 && elen >= 4) {
                /* group(2) + length(2) + key. The group is read, not guessed from the length
                 * (X25519: 32 bytes, hybrid: 1120): taking a ciphertext for a public key would
                 * go on with a wrong secret and fail as an AEAD error. */
                unsigned grp = ((unsigned)rec[p] << 8) | rec[p + 1];
                size_t klen = ((size_t)rec[p + 2] << 8) | rec[p + 3];
                if (4 + klen > elen) return TLS13_EBADREC;
                if (grp == 0x001D && klen == 32) {
                    memcpy(server_pub, rec + p + 4, 32);
                    have_pub = 1;
                } else if (grp == 0x11EC && klen == SC_MLKEM768_CT + 32 && auth && auth->mlkem_dk) {
                    memcpy(kem_ct, rec + p + 4, SC_MLKEM768_CT);
                    memcpy(server_pub, rec + p + 4 + SC_MLKEM768_CT, 32);
                    have_pub = have_kem = 1;
                } else if (grp != 0x001D && grp != 0x11EC) {
                    g_server_group = grp;
                    return TLS13_EGROUP;
                } else return TLS13_ENOKEYSHARE;
            }
            p += elen;
        }
    }
    if (!have_pub) return TLS13_ENOKEYSHARE;
    /* A copy of ServerHello for the ML-DSA check, which signs it too: rec is reused for the
     * following records. Made only when the check is on. */
    unsigned char sh_copy[TLS13_SH_KEEP];
    size_t sh_copy_n = 0;
    if (auth && auth->mldsa_pk) {
        if (n > sizeof(sh_copy)) return TLS13_ETOOBIG;
        memcpy(sh_copy, rec, n);
        sh_copy_n = n;
    }

    /* The cipher suite from ServerHello sets the AEAD and the hash. */
    {
        size_t p = HS_HDR + 2 + 32;
        size_t sid_n = rec[p++];
        p += sid_n;
        unsigned suite = ((unsigned)rec[p] << 8) | rec[p + 1];
        switch (suite) {
            case 0x1301: t->rd.aead = t->wr.aead = TLS13_AEAD_AES128;
                         t->rd.key_n = t->wr.key_n = 16; t->hash_n = 32; break;
            case 0x1302: t->rd.aead = t->wr.aead = TLS13_AEAD_AES256;
                         t->rd.key_n = t->wr.key_n = 32; t->hash_n = 48; break;
            case 0x1303: t->rd.aead = t->wr.aead = TLS13_AEAD_CHACHA;
                         t->rd.key_n = t->wr.key_n = 32; t->hash_n = 32; break;
            default: return TLS13_EBADSUITE;
        }
    }
    H = t->hash_n;
    md = H == 48 ? SC_SHA384 : SC_SHA256;

    /* ECH: did the server accept the inner Hello (RFC 9849, 7.2)? The confirmation is the last
     * eight bytes of ServerHello.random: HKDF-Expand-Label(HKDF-Extract(0, Inner random),
     * "ech accept confirmation", Hash(Inner ‖ ServerHello with those eight bytes zeroed), 8).
     * Accepted: from now on the transcript is built from Inner, not the Outer that was sent
     * (both keys and Finished use it). Not accepted: the handshake stops (TLS13_EECH). The
     * comparison has no early exit: the value is not secret, but the habit is cheaper than an
     * exception. */
    if (auth && auth->ech) {
        struct sc_hash_ctx ic;
        unsigned char ch[48], prk[48], conf[8], zero8[8] = { 0 };
        if (n < 38) return TLS13_EBADREC;
        if (sc_hash_init(&ic, md) != 0) return TLS13_ECRYPTO;
        sc_hash_update(&ic, auth->ech->inner, auth->ech->inner_n);
        sc_hash_update(&ic, rec, 30);
        sc_hash_update(&ic, zero8, 8);
        sc_hash_update(&ic, rec + 38, n - 38);
        int hr = sc_hash_final(&ic, ch);
        sc_hash_free(&ic);
        if (hr != 0 || sc_hkdf_extract(md, NULL, 0, auth->ech->random, 32, prk) != 0 ||
            expand_label(md, prk, H, "ech accept confirmation", ch, H, conf, 8) != 0)
            return TLS13_ECRYPTO;
        unsigned char diff = 0;
        for (int i = 0; i < 8; i++) diff |= (unsigned char)(conf[i] ^ rec[30 + i]);
        if (diff) return TLS13_EECH;
        /* Restart the transcript: Inner, then the real ServerHello. */
        sc_hash_free(&t->tr);
        sc_hash_free(&t->tr384);
        tr_init(t);
        tr_add(t, auth->ech->inner, auth->ech->inner_n);
        tr_add(t, rec, n);
    }

    /* Key schedule, RFC 8446 §7.1. Every step is required, in strict order:
     * early -> handshake -> master, with derive-secret between them. */
    unsigned char zeros[48] = {0};
    unsigned char empty_hash[48];
    if (sc_hash(md, zeros, 0, empty_hash) != 0) return TLS13_ECRYPTO;

    if (sc_hkdf_extract(md, NULL, 0, zeros, H, k->early) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->early, H, "derived", empty_hash, H, k->derived, H) != 0) return TLS13_ECRYPTO;

    /* The ECDHE input of the schedule is the secret with the server's EPHEMERAL key from
     * ServerHello, not the one computed in reality.c. Mixing them up passes ServerHello and then
     * fails every AEAD (TLS13_EAUTH):
     *
     *   Reality secret = our ephemeral x the server's static key (pbk from the link);
     *                    used ONLY for the authenticator in session_id;
     *   TLS secret     = our ephemeral x the server's ephemeral key (key_share in ServerHello);
     *                    the whole key schedule rests on it.
     *
     * The server serving us VLESS still runs ordinary TLS 1.3 with its ephemeral key, or the
     * stream would not look like real HTTPS.
     *
     * The hybrid secret is mlkem_ss ‖ x25519_ss (draft-ietf-tls-ecdhe-mlkem: ML-KEM first for
     * X25519MLKEM768, the same order as Go and BoringSSL). Plain X25519 is just 32 bytes. */
    size_t ecdhe_n = 32;
    if (have_kem) {
        if (getenv("STEER_PQ_TRACE")) fprintf(stderr, "tunvless[debug] pq: server chose X25519MLKEM768\n");
        if (sc_mlkem768_decaps(k->ecdhe, auth->mlkem_dk, kem_ct) != 0) return TLS13_ECRYPTO;
        if (x25519_shared_ext(our_priv, server_pub, k->ecdhe + SC_MLKEM768_SS) != 0) return TLS13_ECRYPTO;
        ecdhe_n = sizeof(k->ecdhe);
    } else if (x25519_shared_ext(our_priv, server_pub, k->ecdhe) != 0) return TLS13_ECRYPTO;
    if (sc_hkdf_extract(md, k->derived, H, k->ecdhe, ecdhe_n, k->hs_secret) != 0)
        return TLS13_ECRYPTO;

    unsigned char th[48];
    tr_hash(t, th);
    if (derive_secret(md, k->hs_secret, "c hs traffic", th, H, k->c_hs) != 0) return TLS13_ECRYPTO;
    if (derive_secret(md, k->hs_secret, "s hs traffic", th, H, k->s_hs) != 0) return TLS13_ECRYPTO;

    k->c_hk = t->wr;
    k->s_hk = t->rd;
    if (expand_label(md, k->c_hs, H, "key", NULL, 0, k->c_hk.key, k->c_hk.key_n) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->c_hs, H, "iv", NULL, 0, k->c_hk.iv, 12) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->s_hs, H, "key", NULL, 0, k->s_hk.key, k->s_hk.key_n) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->s_hs, H, "iv", NULL, 0, k->s_hk.iv, 12) != 0) return TLS13_ECRYPTO;

    /* Encrypted server messages follow. Read up to Finished, adding each to the transcript:
     * Certificate and CertificateVerify must be in the hash even when not checked, or
     * Finished will not match. */
    uint64_t s_seq = 0;
    int got_finished = 0;
    unsigned char server_finished[48];

    /* Handshake messages are assembled ACROSS RECORD BOUNDARIES. A message continued in the
     * next record (long certificate chains: Certificates of 8273 bytes occur) must not be
     * lost, or the transcript diverges, our Finished fails and the server closes the
     * connection, which looks like "the node no longer recognises the client" even with a
     * valid authenticator.
     *
     * One buffer per running handshake. */
    unsigned char *hsbuf = b->hs;
    size_t hs_have = 0;

    /* Messages the authentication needs. Copied, not kept by pointer: hsbuf shifts as it is
     * parsed, and by check time the old place would hold the next message. The chain can be
     * eight kilobytes or more, so the buffer is as large as hsbuf. */
    unsigned char *certbuf = b->cert;
    size_t cert_n = 0;
    unsigned char cv_buf[1024];
    size_t cv_n = 0;
    unsigned char cv_transcript[48];
    size_t cv_thash_n = 0;
    g_verify_reason[0] = '\0';

    /* Up to 64 records: a long certificate chain arrives in several. */
    for (int guard = 0; guard < 64 && !got_finished; guard++) {
        rc = read_record_fd(t, &type, rec, sizeof(rec), &n);
        if (rc) return rc;
        if (type == 0x14) continue;             /* ChangeCipherSpec: ignored in 1.3 */
        if (type != 0x17) return TLS13_EBADREC;

        unsigned char aad[5] = { 0x17, 0x03, 0x03,
                                 (unsigned char)(n >> 8), (unsigned char)n };
        rc = aead_open_once(&k->s_hk, s_seq++, aad, 5, rec, n);
        if (rc) return rc;
        size_t pt = n - 16;
        /* The last non-zero byte is the real record type (RFC 8446 §5.4). */
        while (pt > 0 && rec[pt - 1] == 0) pt--;
        if (pt == 0) return TLS13_EBADREC;
        unsigned char inner = rec[--pt];
        if (inner != 0x16) continue;            /* not handshake: skip */

        /* Append to what is left from earlier records. */
        if (hs_have + pt > sizeof b->hs) return TLS13_ETOOBIG;
        memcpy(hsbuf + hs_have, rec, pt);
        hs_have += pt;

        /* The buffer may hold several messages, the last one incomplete. */
        size_t p = 0;
        while (p + 4 <= hs_have) {
            unsigned char msg = hsbuf[p];
            size_t mlen = ((size_t)hsbuf[p + 1] << 16) | ((size_t)hsbuf[p + 2] << 8) | hsbuf[p + 3];
            if (p + 4 + mlen > hs_have) break;  /* the rest comes in the next record */
            if (msg == 0x08) {                  /* EncryptedExtensions */
                /* Only ALPN is extracted: no other extension affects us, and parsing more
                 * untrusted bytes is more room for bugs. Body: list length(2), then
                 * type(2) + length(2) + body per extension; ALPN holds list length(2),
                 * length(1), name. */
                const unsigned char *e = hsbuf + p + 4;
                if (mlen >= 2) {
                    size_t total = ((size_t)e[0] << 8) | e[1];
                    if (total + 2 <= mlen) {
                        size_t q = 2;
                        while (q + 4 <= total + 2) {
                            unsigned etype = ((unsigned)e[q] << 8) | e[q + 1];
                            size_t ebody = ((size_t)e[q + 2] << 8) | e[q + 3];
                            if (q + 4 + ebody > total + 2) break;
                            if (etype == 0x0010 && ebody >= 4) {
                                size_t pl = e[q + 6];
                                if (pl && pl < sizeof(t->alpn) && 3 + pl <= ebody) {
                                    memcpy(t->alpn, e + q + 7, pl);
                                    t->alpn[pl] = '\0';
                                }
                            }
                            q += 4 + ebody;
                        }
                    }
                }
            }
            if (rkey && msg == 0x19) {          /* CompressedCertificate (RFC 8879) */
                /* A Reality server that RECOGNISED the client answers with its temporary
                 * certificate, uncompressed. A compressed one comes from the camouflage site
                 * instead, which is exactly "key not recognised"; compression is beside the point
                 * for the user. */
                snprintf(g_verify_reason, sizeof(g_verify_reason), "%s",
                         cert_verify_strerror(CERTV_ENOTREALITY));
                return TLS13_ECERT;
            }
            if (want_cert && (msg == 0x0B || msg == 0x19) && cert_n != 0) {
                snprintf(g_verify_reason, sizeof(g_verify_reason),
                         "the server sent its certificate twice");
                return TLS13_ECERT;
            }
            if (host && msg == 0x19) {
                const unsigned char *c = hsbuf + p + 4;
                if (mlen < 8) {
                    snprintf(g_verify_reason, sizeof(g_verify_reason),
                             "cannot decompress the server certificate");
                    return TLS13_ECERT;
                }
                unsigned alg = ((unsigned)c[0] << 8) | c[1];
                if (alg >= 32 || !(auth->cert_algs & (1u << alg))) {
                    snprintf(g_verify_reason, sizeof(g_verify_reason),
                             "server sent a compressed certificate without being asked");
                    return TLS13_ECERT;
                }
                size_t declared = ((size_t)c[2] << 16) | ((size_t)c[3] << 8) | c[4];
                size_t zn = ((size_t)c[5] << 16) | ((size_t)c[6] << 8) | c[7];
                if (declared == 0 || 8 + zn != mlen ||
                    certzip_inflate((int)alg, c + 8, zn, declared, certbuf, sizeof b->cert) != 0) {
                    snprintf(g_verify_reason, sizeof(g_verify_reason),
                             "cannot decompress the server certificate");
                    return TLS13_ECERT;
                }
                cert_n = declared;
            }
            if (want_cert && msg == 0x0B) {   /* Certificate */
                if (mlen > sizeof b->cert) return TLS13_ETOOBIG;
                memcpy(certbuf, hsbuf + p + 4, mlen);
                cert_n = mlen;
            }
            if (host && msg == 0x0F && cv_n != 0) {
                snprintf(g_verify_reason, sizeof(g_verify_reason),
                         "the server sent CertificateVerify twice");
                return TLS13_ECERT;
            }
            if (host && msg == 0x0F) {     /* CertificateVerify */
                /* The hash is taken HERE, before tr_add of this message: the server signed
                 * the transcript up to and including Certificate, and a hash cannot be
                 * rolled back later. */
                if (mlen > sizeof(cv_buf)) return TLS13_ETOOBIG;
                tr_hash(t, cv_transcript);
                cv_thash_n = H;
                memcpy(cv_buf, hsbuf + p + 4, mlen);
                cv_n = mlen;
            }
            if (msg == 0x14) {                  /* Finished */
                /* Checked BEFORE it enters the transcript: the server computed it over the
                 * hash of the preceding messages. */
                unsigned char hash[48], want[48];
                tr_hash(t, hash);
                if (expand_label(md, k->s_hs, H, "finished", NULL, 0, k->fkey, H) != 0)
                    return TLS13_ECRYPTO;
                if (sc_hmac(md, k->fkey, H, hash, H, want) != 0) return TLS13_ECRYPTO;
                if (mlen != H || memcmp(hsbuf + p + 4, want, H) != 0) return TLS13_EFINISHED;
                memcpy(server_finished, want, H);
                got_finished = 1;
            }
            tr_add(t, hsbuf + p, 4 + mlen);
            p += 4 + mlen;
        }
        /* Drop what was parsed; move the incomplete rest to the start. */
        if (p) {
            if (hs_have > p) memmove(hsbuf, hsbuf + p, hs_have - p);
            hs_have -= p;
        }
    }
    if (!got_finished) return TLS13_EFINISHED;

    /* Authentication comes AFTER the server's Finished and BEFORE ours.
     *
     * After: Finished proves we share one transcript with the server, so the part signed by
     * CertificateVerify was not swapped on the way. A signature over a transcript we are not
     * yet sure of proves nothing.
     *
     * Before: our Finished is the first thing sent under the session keys, and there is no
     * reason to send it to a peer that has not proved itself. */
    /* Reality: the proof is the signature field of the temporary certificate; the chain plays
     * no part. It runs before the security=tls branch, not instead of it: no setting has both
     * proofs, but the order in code must be unambiguous. */
    if (rkey) {
        if (!cert_n) {
            snprintf(g_verify_reason, sizeof(g_verify_reason), "server sent no certificate");
            return TLS13_ECERT;
        }
        int rrc = cert_reality_check(certbuf, cert_n, rkey);
        if (rrc == 0 && auth->mldsa_pk)
            rrc = cert_reality_check_pq(certbuf, cert_n, rkey, auth->mldsa_pk,
                                        client_hello + 5, hello_n - 5, sh_copy, sh_copy_n);
        if (rrc != 0) {
            snprintf(g_verify_reason, sizeof(g_verify_reason), "%s", cert_verify_strerror(rrc));
            return TLS13_ECERT;
        }
    }

    if (host) {
        if (!cert_n || !cv_n) {
            /* The handshake completed without proof. Without a PSK (we offer none) a TLS 1.3
             * server must send both, so only a broken or hostile server gets here. Named apart
             * from "the certificate does not verify". */
            snprintf(g_verify_reason, sizeof(g_verify_reason),
                     "server sent no %s", cert_n ? "signature" : "certificate");
            return TLS13_ECERT;
        }
        if (cert_has_dc(certbuf, cert_n)) {
            snprintf(g_verify_reason, sizeof(g_verify_reason),
                     "the server sent a delegated credential, which is not supported");
            return TLS13_ECERT;
        }
        int vrc = cert_verify_server_ex(certbuf, cert_n, cv_buf, cv_n,
                                        cv_transcript, cv_thash_n, host, auth->roots, auth->policy);
        if (vrc != 0) {
            snprintf(g_verify_reason, sizeof(g_verify_reason), "%s",
                     cert_verify_strerror(vrc));
            return TLS13_ECERT;
        }
    }

    /* Our Finished covers the transcript including the server's Finished. */
    unsigned char th2[48], chash[48], cfin[48];
    tr_hash(t, th2);
    if (expand_label(md, k->c_hs, H, "finished", NULL, 0, k->cfkey, H) != 0) return TLS13_ECRYPTO;
    memcpy(chash, th2, H);
    if (sc_hmac(md, k->cfkey, H, chash, H, cfin) != 0) return TLS13_ECRYPTO;

    /* ChangeCipherSpec (middlebox compatibility, as the browser does) and the encrypted Finished
     * leave in one write, as Go and uTLS flush the client's second flight. */
    {
        unsigned char pt[96];
        size_t pl = 0;
        pt[pl++] = 0x14;
        pt[pl++] = 0; pt[pl++] = 0; pt[pl++] = (unsigned char)H;
        memcpy(pt + pl, cfin, H); pl += H;
        pt[pl++] = 0x16;                        /* inner type */

        unsigned char flight[6 + 160] = { 0x14, 0x03, 0x03, 0x00, 0x01, 0x01 };
        unsigned char *out = flight + 6;
        size_t total = pl + 16;
        out[0] = 0x17; out[1] = 0x03; out[2] = 0x03;
        out[3] = (unsigned char)(total >> 8); out[4] = (unsigned char)total;
        memcpy(out + 5, pt, pl);
        if (aead_seal_once(&k->c_hk, 0, out, 5, out + 5, pl, out + 5 + pl) != 0) return TLS13_ECRYPTO;
        rc = send_all(t, flight, 6 + 5 + total);
        if (rc) return rc;
    }

    /* Application traffic keys: from the master secret and the full transcript. */
    if (expand_label(md, k->hs_secret, H, "derived", empty_hash, H, k->derived, H) != 0)
        return TLS13_ECRYPTO;
    if (sc_hkdf_extract(md, k->derived, H, zeros, H, k->master) != 0) return TLS13_ECRYPTO;
    tr_hash(t, th2);
    if (derive_secret(md, k->master, "c ap traffic", th2, H, k->c_ap) != 0) return TLS13_ECRYPTO;
    if (derive_secret(md, k->master, "s ap traffic", th2, H, k->s_ap) != 0) return TLS13_ECRYPTO;

    if (expand_label(md, k->c_ap, H, "key", NULL, 0, t->wr.key, t->wr.key_n) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->c_ap, H, "iv", NULL, 0, t->wr.iv, 12) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->s_ap, H, "key", NULL, 0, t->rd.key, t->rd.key_n) != 0) return TLS13_ECRYPTO;
    if (expand_label(md, k->s_ap, H, "iv", NULL, 0, t->rd.iv, 12) != 0) return TLS13_ECRYPTO;
    /* Traffic keys never change from here, so the cipher contexts are set up once; the stream
     * then runs without a single setkey. */
    if (tls13_keys_setup(&t->wr) != 0 || tls13_keys_setup(&t->rd) != 0) return TLS13_ECRYPTO;
    t->wr_seq = t->rd_seq = 0;
    t->ready = 1;
    return 0;
}

static int handshake(struct tls13 *t, int fd,
                     const unsigned char *client_hello, size_t hello_n,
                     const unsigned char *our_priv,
                     const struct tls13_auth *auth) {
    struct hs_bufs *b = malloc(sizeof *b);
    if (!b) return TLS13_EIO;
    struct hs_secrets k;
    int rc = handshake_run(t, fd, client_hello, hello_n, our_priv, auth, &k, b);
    sc_wipe(&k, sizeof k);
    sc_wipe(b, sizeof *b);
    free(b);
    return rc;
}

int tls13_handshake(struct tls13 *t, int fd,
                    const unsigned char *client_hello, size_t hello_n,
                    const unsigned char *our_priv) {
    return handshake(t, fd, client_hello, hello_n, our_priv, NULL);
}

int tls13_handshake_auth(struct tls13 *t, int fd,
                         const unsigned char *client_hello, size_t hello_n,
                         const unsigned char *our_priv,
                         const struct tls13_auth *auth) {
    /* An empty name with the certificate check on is a caller bug, not "nothing to check": a
     * check without a name would pass any valid certificate in the world. */
    if (auth && auth->host && !auth->host[0]) return TLS13_ECERT;
    return handshake(t, fd, client_hello, hello_n, our_priv, auth);
}

int tls13_has_record(const struct tls13 *t) {
    size_t have = t->rbuf_n - t->rbuf_off;
    if (have < 5) return 0;
    const unsigned char *h = t->rbuf + t->rbuf_off;
    size_t len = ((size_t)h[3] << 8) | h[4];
    return have >= 5 + len;
}

size_t tls13_buffered(const struct tls13 *t) {
    return t->rbuf_n - t->rbuf_off;
}

size_t tls13_take_pending(struct tls13 *t, unsigned char *out, size_t cap) {
    size_t have = t->rbuf_n - t->rbuf_off;
    if (!have) return 0;
    if (have > cap) have = cap;
    memcpy(out, t->rbuf + t->rbuf_off, have);
    t->rbuf_off += have;
    return have;
}

/* ---- data ------------------------------------------------------------------ */

int tls13_seal(struct tls13 *t, const unsigned char *d, size_t n, unsigned char *out, size_t cap, size_t *out_n) {
    if (!t->ready) return TLS13_ESTATE;
    size_t need = 0;
    uint64_t seq = t->wr_seq;
    for (size_t left = n; left; seq++) {
        size_t c = tls13_plain_cap_at(seq);
        size_t piece = left > c ? c : left;
        need += piece + 22;
        left -= piece;
    }
    if (need > cap) return TLS13_ETOOBIG;
    size_t o = 0;
    while (n) {
        size_t lim = tls13_plain_cap(t);
        size_t chunk = n > lim ? lim : n;
        size_t total = chunk + 1 + 16;          /* + inner type + tag */
        unsigned char *r = out + o;
        r[0] = 0x17; r[1] = 0x03; r[2] = 0x03;
        r[3] = (unsigned char)(total >> 8); r[4] = (unsigned char)total;
        memmove(r + 5, d, chunk);
        r[5 + chunk] = 0x17;                    /* inner type: application_data */
        if (tls13_aead_seal(&t->wr, t->wr_seq++, r, 5, r + 5, chunk + 1, r + 5 + chunk + 1) != 0)
            return TLS13_ECRYPTO;
        o += 5 + total;
        d += chunk;
        n -= chunk;
    }
    *out_n = o;
    return 0;
}

int tls13_write(struct tls13 *t, const unsigned char *data, size_t n) {
    if (!t->ready) return TLS13_ESTATE;
    unsigned char out[TLS13_MAX_REC + 5];
    while (n) {
        size_t lim = tls13_plain_cap(t);
        size_t chunk = n > lim ? lim : n;
        size_t want = 0;
        int rc = tls13_seal(t, data, chunk, out, sizeof out, &want);
        if (rc) return rc;
        rc = send_all(t, out, want);
        if (rc) return rc;
        data += chunk;
        n -= chunk;
    }
    return 0;
}

/* Read EXACTLY ONE record. It may carry no data: then zero bytes with code 0, a success.
 *
 * "Exactly one" matters. Records without data are common here (ChangeCipherSpec,
 * NewSessionTicket, Vision's empty records), and the socket is blocking with SO_RCVTIMEO: waiting
 * on for data after one stalls a healthy connection until the timeout and then fails it as an
 * I/O error, cutting a download at a random point. Waiting for the socket to become readable is
 * the caller's job: it has poll; this layer has none and must not.
 *
 * Shared by both reads: the plaintext stays WHERE IT IS, in the connection buffer. Only
 * tls13_read copies, because its caller wants its own buffer. */
static int read_one(struct tls13 *t, const unsigned char **body, size_t *body_n) {
    if (!t->ready) return TLS13_ESTATE;
    *body = NULL;
    *body_n = 0;

    /* The record is decrypted IN PLACE in the connection buffer: another copy would move an
     * extra 16 KB through memory per record. */
    unsigned char *rec = NULL;
    unsigned char type;
    size_t n = 0;
    int rc = read_record(t, &type, &rec, &n, 0);
    /* No whole record yet means "nothing for now", not a failure: the caller comes back. */
    if (rc == TLS13_EAGAIN) return 0;
    if (rc) return rc;
    if (type == 0x14) return 0;                /* ChangeCipherSpec: meaningless in 1.3 */
    if (type != 0x17) return TLS13_EBADREC;

    unsigned char aad[5] = { 0x17, 0x03, 0x03,
                             (unsigned char)(n >> 8), (unsigned char)n };
    rc = tls13_aead_open(&t->rd, t->rd_seq++, aad, 5, rec, n);
    if (rc) return rc;
    size_t pt = n - 16;
    while (pt > 0 && rec[pt - 1] == 0) pt--;
    if (pt == 0) return TLS13_EBADREC;
    unsigned char inner = rec[--pt];

    /* NewSessionTicket and other post-handshake messages arrive as handshake records and are
     * skipped, not treated as errors: resumption is not supported, and failing on them would
     * kill connections soon after they open. */
    if (inner == 0x16) return 0;
    if (inner == 0x15) return TLS13_ECLOSED;   /* alert */
    if (inner != 0x17) return 0;
    if (pt == 0) return 0;                     /* empty record: legitimate Vision padding */

    *body = rec;
    *body_n = pt;
    return 0;
}

int tls13_read(struct tls13 *t, unsigned char *out, size_t cap, size_t *got) {
    const unsigned char *body = NULL;
    size_t n = 0;
    int rc = read_one(t, &body, &n);
    *got = 0;
    if (rc || !n) return rc;
    if (n > cap) return TLS13_ETOOBIG;
    memcpy(out, body, n);
    *got = n;
    return 0;
}

int tls13_read_ref(struct tls13 *t, const unsigned char **body, size_t *body_n) {
    return read_one(t, body, body_n);
}
