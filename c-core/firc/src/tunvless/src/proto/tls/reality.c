/* Reality handshake.
 *
 * Plain TLS proves the server by its certificate. Reality cannot: the certificate the server
 * sends belongs to a real third-party site, which the server proxies for anyone who does not
 * prove themselves. Checking the chain is pointless; the proof works differently:
 *
 *   1. the server has a static X25519 pair; the client knows the public half (pbk);
 *   2. the client generates an ephemeral pair and puts the public half in the ClientHello
 *      key_share, exactly where real TLS 1.3 has it;
 *   3. from its private key and pbk the client computes a shared secret and from it a short
 *      authenticator, hidden in the 32 bytes of session_id;
 *   4. the server computes the same secret with its private key and checks it. On a match it
 *      serves VLESS; otherwise it proxies to the real site.
 *
 * Two consequences shape the code below.
 *
 * The ClientHello must be INDISTINGUISHABLE from a browser's, not merely similar: the set and
 * order of extensions, the cipher list, GREASE values. Any deviation makes us an atypical
 * client, which is a signal by itself even with a valid authenticator. So the Hello is built
 * here by hand: a library TLS stack would send its own extension order.
 *
 * And failure looks like success: the server answers no error, it serves the real site, and the
 * handshake completes. Only the HMAC in the server's temporary certificate (checked by tls13.c,
 * see certverify.h) or the VLESS answer tells.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <errno.h>
#include "osrand.h"
#include <stdlib.h>
#if defined(__aarch64__)
#include <sys/auxv.h>
#endif

#include "scrypto.h"
#include "reality.h"

/* base64url, unpadded as pbk comes in a link; '=' and line breaks are skipped. */
static int b64url_decode(const char *in, unsigned char *out, size_t out_n) {
    static const char *A = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    size_t o = 0;
    /* Unsigned and masked: the accumulator keeps shifting in 6-bit groups, and a signed int
     * would overflow (undefined behaviour) after a few characters. */
    unsigned acc = 0;
    int bits = 0;
    for (const char *p = in; *p; p++) {
        const char *q = strchr(A, *p);
        if (!q) {
            if (*p == '=' || *p == '\n' || *p == '\r') continue;
            /* Links sometimes carry the standard alphabet instead of the url-safe one. */
            if (*p == '+') q = A + 62;
            else if (*p == '/') q = A + 63;
            else return -1;
        }
        acc = ((acc << 6) | (unsigned)(q - A)) & 0x3FFFFFu;   /* 22 bits are enough */
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (o >= out_n) return -1;
            out[o++] = (unsigned char)((acc >> bits) & 0xFF);
        }
    }
    return (int)o;
}

static int hex_decode(const char *in, unsigned char *out, size_t out_n) {
    size_t o = 0;
    for (const char *p = in; p[0] && p[1]; p += 2) {
        if (o >= out_n) return -1;
        int hi = p[0] <= '9' ? p[0] - '0' : (p[0] | 32) - 'a' + 10;
        int lo = p[1] <= '9' ? p[1] - '0' : (p[1] | 32) - 'a' + 10;
        if (hi < 0 || hi > 15 || lo < 0 || lo > 15) return -1;
        out[o++] = (unsigned char)((hi << 4) | lo);
    }
    return (int)o;
}

/* Random bytes straight from the kernel. No DRBG of our own: getrandom(2) is what it would be
 * seeded from anyway, and an extra layer adds code exactly where a bug escapes every test. */
static int fill_random(unsigned char *buf, size_t n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = os_getrandom(buf + got, n - got, 0);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        got += (size_t)r;
    }
    return 0;
}

/* Whether the CPU has AES instructions.
 *
 * The cipher order in the ClientHello depends on it, and both orders are Chrome's: Chrome asks
 * the same question (EVP_has_aes_hardware) and reorders the same way, so this repeats the
 * browser's behaviour on this hardware.
 *
 * x86 asks CPUID, aarch64 asks the kernel (AT_HWCAP): trying the instruction would raise SIGILL
 * where it is missing. MIPS has no such instructions at all.
 *
 * STEER_CIPHER=aes|chacha overrides the answer, to check a speed claim on the spot;
 * tests/hellofreeze.c pins the order with it. */
static int cpu_has_aes(void) {
    const char *env = getenv("STEER_CIPHER");
    if (env && !strcmp(env, "aes")) return 1;
    if (env && !strcmp(env, "chacha")) return 0;
#if defined(__x86_64__) || defined(__i386__)
    unsigned a = 1, b = 0, c = 0, d = 0;
    __asm__ volatile("cpuid" : "=a"(a), "=b"(b), "=c"(c), "=d"(d) : "a"(1), "c"(0));
    return (c & (1u << 25)) != 0;                 /* AESNI */
#elif defined(__aarch64__)
    return (getauxval(AT_HWCAP) & (1ul << 3)) != 0;      /* HWCAP_AES */
#else
    /* Everything else, including 32-bit ARM with crypto extensions. What counts is whether
     * OUR build uses the instructions: our wolfSSL has a hardware AES path only for x86_64
     * (AES-NI) and aarch64 (ARMv8 Crypto), see build/wolfssl/user_settings.h. On armv7 AES is
     * table-based and slow, so preferring it would pick the worse cipher. */
    return 0;
#endif
}

/* ---- X25519 --------------------------------------------------------------- */
/* Through the primitives layer (wolfCrypt).
 *
 * The random scalar is drawn HERE through fill_random, not inside the layer: tests/hellofreeze.c
 * replaces os_getrandom with a macro before including this file and compares the Hello byte for
 * byte with a frozen copy; a key generated in another file would escape the substitution.
 * The layer clamps every scalar itself (RFC 7748); clamping here as well keeps st->priv, which
 * tls13.c later uses with the server's ephemeral key, equal to the scalar actually used. */
static int x25519_keypair(unsigned char priv[32], unsigned char pub[32]) {
    if (fill_random(priv, 32) != 0) return -1;
    priv[0] &= 248;
    priv[31] &= 127;
    priv[31] |= 64;
    return sc_x25519_base(pub, priv) == 0 ? 0 : -1;
}

int x25519_shared_ext(const unsigned char priv[32], const unsigned char peer[32],
                      unsigned char out[32]);

static int x25519_shared(const unsigned char priv[32], const unsigned char peer[32],
                         unsigned char out[32]) {
    return sc_x25519(out, priv, peer) == 0 ? 0 : -1;
}

/* ---- ClientHello ---------------------------------------------------------- */
struct buf {
    unsigned char *p;
    size_t len, cap;
};

static void put(struct buf *b, const void *d, size_t n) {
    if (b->len + n > b->cap) { b->len = b->cap + 1; return; }   /* overflow shows to the caller */
    /* An empty extension comes as (NULL, 0) (extended_master_secret, session_ticket below), and
     * memcpy from NULL is undefined even with zero length. */
    if (!n) return;
    memcpy(b->p + b->len, d, n);
    b->len += n;
}
static void put8(struct buf *b, unsigned v) { unsigned char c = (unsigned char)v; put(b, &c, 1); }
static void put16(struct buf *b, unsigned v) { unsigned char c[2] = { (unsigned char)(v >> 8), (unsigned char)v }; put(b, c, 2); }

static void ext(struct buf *b, unsigned type, const void *body, size_t n) {
    put16(b, type);
    put16(b, (unsigned)n);
    put(b, body, n);
}

static size_t sni_body(unsigned char *o, size_t cap, const char *sni) {
    size_t sni_len = strlen(sni);
    struct buf sb = { o, 0, cap };
    put16(&sb, (unsigned)(sni_len + 3));
    put8(&sb, 0);
    put16(&sb, (unsigned)sni_len);
    put(&sb, sni, sni_len);
    return sb.len;
}

static size_t alpn_body(unsigned char *o, size_t cap, const struct reality_carrier *car) {
    struct buf ab = { o, 0, cap };
    if (car && car->alpn_http11) {
        put16(&ab, 9);
        put8(&ab, 8); put(&ab, "http/1.1", 8);
    } else {
        put16(&ab, 12);
        put8(&ab, 2); put(&ab, "h2", 2);
        put8(&ab, 8); put(&ab, "http/1.1", 8);
    }
    return ab.len;
}

static size_t cert_algs_body(unsigned char *o, size_t cap, int fp) {
    struct buf cb = { o, 0, cap };
    unsigned algs = reality_cert_algs(fp), n = 0;
    for (unsigned a = 0; a < 32; a++) n += algs >> a & 1u;
    put8(&cb, 2 * n);
    for (unsigned a = 0; a < 32; a++)
        if (algs >> a & 1u) put16(&cb, a);
    return cb.len;
}

static int chrome_body(struct buf *b, size_t *xl, const struct reality_cfg *cfg,
                       struct reality_state *st, const struct reality_carrier *car) {
    /* GREASE values (RFC 8701): one for each place Chrome puts one, fresh per connection;
     * constant values would be a fingerprint themselves.
     *
     * Values are 0x0a0a + n*0x1010, sixteen of them from 0x0a0a to 0xfafa. The two extension
     * types must differ: equal ones would repeat an extension, which TLS forbids. */
    unsigned char gr[5];
    if (fill_random(gr, sizeof(gr)) != 0) return REALITY_ECRYPTO;
    unsigned g_cipher  = 0x0A0Au + (unsigned)(gr[0] & 15) * 0x1010u;
    unsigned g_group   = 0x0A0Au + (unsigned)(gr[1] & 15) * 0x1010u;
    unsigned g_version = 0x0A0Au + (unsigned)(gr[2] & 15) * 0x1010u;
    unsigned g_ext_a   = 0x0A0Au + (unsigned)(gr[3] & 15) * 0x1010u;
    unsigned g_ext_b   = 0x0A0Au + (unsigned)((gr[4] & 15) ^ (((gr[4] & 15) == (gr[3] & 15)) ? 1 : 0)) * 0x1010u;

    /* Chrome's cipher suites in Chrome's order, GREASE first; checked against a capture. The
     * server picks: Go's crypto/tls takes the first suite of ITS preference order
     * (AES_128_GCM_SHA256, CHACHA20, AES_256_GCM_SHA384) that the client offered. tls13.c
     * serves all three TLS 1.3 suites. */
    static const unsigned suites_aes[] = {
        0x1301, 0x1302, 0x1303, 0xC02B, 0xC02F, 0xC02C, 0xC030,
        0xCCA9, 0xCCA8, 0xC013, 0xC014, 0x009C, 0x009D, 0x002F, 0x0035,
    };
    /* The same list in BoringSSL's order for a CPU WITHOUT AES instructions: ChaCha20 above
     * AES-GCM in both 1.3 and 1.2. Chrome does exactly this (EVP_has_aes_hardware in
     * ssl_cipher.cc), so the fingerprint stays a browser's: the order changes, not the set.
     *
     * It matters because Go's crypto/tls looks at OUR order: aesgcmPreferred() checks whether
     * AES-GCM is first in the client's list, and if not, uses defaultCipherSuitesTLS13NoAES,
     * i.e. ChaCha20. On a MIPS 24Kc router AES-128-GCM runs at 1.7 MB/s against 11.6 MB/s for
     * ChaCha20-Poly1305; with AES the tunnel was capped at 1.2 MB/s on a 48 Mbit/s link, 91% of
     * the time in decryption. */
    static const unsigned suites_chacha[] = {
        0x1303, 0x1301, 0x1302, 0xCCA9, 0xCCA8, 0xC02B, 0xC02F, 0xC02C, 0xC030,
        0xC013, 0xC014, 0x009C, 0x009D, 0x002F, 0x0035,
    };
    const unsigned *suites = cpu_has_aes() ? suites_aes : suites_chacha;
    size_t nsuites = sizeof(suites_aes) / sizeof(suites_aes[0]);
    put16(b, (unsigned)(nsuites + 1) * 2);
    put16(b, g_cipher);
    for (size_t i = 0; i < nsuites; i++) put16(b, suites[i]);

    put8(b, 1); put8(b, 0);  /* compression: null */

    *xl = b->len;
    put16(b, 0);

    /* The set, contents and order of extensions follow Chrome, checked against a capture of
     * its Hello to the same server (tests/hello-diff.py).
     *
     * The Reality authenticator signs the WHOLE ClientHello, and Reality's point is to look
     * like a browser. An openssl-like Hello (3 suites, 10 extensions, no GREASE, ECH or ALPN,
     * plus encrypt_then_mac) stopped working on every node at once when servers got pickier,
     * while sing-box worked with the same keys. Reality has no negative answer: it silently
     * proxies an unrecognised client to the camouflage site, so a Hello it rejects looks
     * exactly like a wrong key (the handshake completes, the certificate check fails).
     *
     * So any change here is verified by a capture next to the browser reference, not by
     * reasoning about what "should work". */

    /* Extensions go into a table and are written later: since version 110 Chrome SHUFFLES
     * their order on every connection, keeping the first and last in place. A fixed order
     * would be a fingerprint. */
    struct pend { unsigned type; const unsigned char *body; size_t n; };
    struct pend px[20];
    size_t pn = 0;

    unsigned char b_pq[REALITY_MLKEM_SHARE];
    unsigned char b_sni[300], b_alpn[16], b_cc[1 + 2 * 32], b_ech[220], b_alps[8], b_reneg[1];
    unsigned char b_ocsp[5], b_vers[8], b_sigs[20], b_grp[12], b_pskm[2];
    unsigned char b_ks[64 + REALITY_MLKEM_SHARE];
    unsigned char b_ecpf[2], b_last[1];

    /* First: an empty GREASE extension. */
    px[pn].type = g_ext_a; px[pn].body = NULL; px[pn].n = 0; pn++;

    /* server_name: list(2) + type(1) + length(2) + name.
     *
     * NO NAME MEANS NO EXTENSION, not an empty name. Reality checks the name against its
     * `serverNames`, where an empty string is legal: the server then expects a ClientHello
     * without this extension, exactly as Xray sends it with an empty `serverName`. An
     * extension with a zero-length name is something no browser or client builds, and the
     * server's check does not recognise it. Subscriptions do declare nodes without `sni`. */
    if (cfg->sni[0]) {
        px[pn].type = 0x0000; px[pn].body = b_sni; px[pn].n = sni_body(b_sni, sizeof(b_sni), cfg->sni); pn++;
    }

    /* ALPN: ALWAYS "h2, http/1.1" as the browser sends it, whatever the node's transport
     * (http/1.1 alone with car->alpn_http11, see reality.h). A server that wants h2 picks it;
     * Reality, having recognised us, picks nothing (NextProtos is nil in Xray) and sends empty
     * EncryptedExtensions. */
    px[pn].type = 0x0010; px[pn].body = b_alpn; px[pn].n = alpn_body(b_alpn, sizeof(b_alpn), car); pn++;

    px[pn].type = 0x001B; px[pn].body = b_cc;
    px[pn].n = cert_algs_body(b_cc, sizeof b_cc, REALITY_FP_CHROME); pn++;

    /* encrypted_client_hello: PADDING, not real ECH.
     *
     * Chrome without an ECH config sends exactly this: a well-formed extension filled with
     * random bytes (GREASE-ECH in uTLS). Real ECH has nothing to negotiate here; what matters
     * is the same shape and size, or the Hello stands out by one missing 218-byte extension.
     * (Real ECH for security=tls wraps the finished Hello instead, see ech.h.)
     *
     * Layout: type(1)=0 outer, kdf(2)=HKDF-SHA256, aead(2)=AES-128-GCM, config id(1),
     * enc length(2)=32 and enc, payload length(2)=176 and payload:
     * 1+2+2+1+2+32+2+176 = 218 bytes, as in the reference. */
    {
        unsigned char noise[209];
        if (fill_random(noise, sizeof(noise)) != 0) return REALITY_ECRYPTO;
        /* A carrier fills exactly the 176 bytes that become the ECH payload. It does so HERE,
         * not after assembly: the padding is part of the bytes session_id later signs, and
         * `noise + 33` is literally the future payload, not an offset computed by hand. */
        if (car && car->fill_ech &&
            car->fill_ech(car->ctx, noise + 33, 176, st->shared) != 0)
            return REALITY_ECRYPTO;
        struct buf eb = { b_ech, 0, sizeof(b_ech) };
        put8(&eb, 0x00);
        put16(&eb, 0x0001);
        put16(&eb, 0x0001);
        put8(&eb, noise[0]);
        put16(&eb, 32);
        put(&eb, noise + 1, 32);
        put16(&eb, 176);
        put(&eb, noise + 33, 176);
        px[pn].type = 0xFE0D; px[pn].body = b_ech; px[pn].n = eb.len; pn++;
    }

    /* application_settings (ALPS), new codepoint 0x44cd: a list of one "h2". */
    if (!(car && car->no_alps)) {           /* no_alps: see reality.h */
      struct buf ab = { b_alps, 0, sizeof(b_alps) };
      put16(&ab, 3); put8(&ab, 2); put(&ab, "h2", 2);
      px[pn].type = 0x44CD; px[pn].body = b_alps; px[pn].n = ab.len; pn++; }

    b_reneg[0] = 0;
    px[pn].type = 0xFF01; px[pn].body = b_reneg; px[pn].n = 1; pn++;

    px[pn].type = 0x0017; px[pn].body = NULL; px[pn].n = 0; pn++;   /* extended_master_secret */
    px[pn].type = 0x0023; px[pn].body = NULL; px[pn].n = 0; pn++;   /* session_ticket */

    /* status_request: OCSP, empty lists. */
    { struct buf ob = { b_ocsp, 0, sizeof(b_ocsp) };
      put8(&ob, 1); put16(&ob, 0); put16(&ob, 0);
      px[pn].type = 0x0005; px[pn].body = b_ocsp; px[pn].n = ob.len; pn++; }

    /* supported_versions: GREASE, 1.3, 1.2.
     *
     * TLS 1.2 is listed because the browser lists it. This handshake cannot serve it, but
     * Reality is strictly 1.3; should a server pick 1.2, ServerHello has no key_share and the
     * handshake fails with ENOKEYSHARE, an error rather than silence. */
    { struct buf vb = { b_vers, 0, sizeof(b_vers) };
      put8(&vb, 6); put16(&vb, g_version); put16(&vb, 0x0304); put16(&vb, 0x0303);
      px[pn].type = 0x002B; px[pn].body = b_vers; px[pn].n = vb.len; pn++; }

    px[pn].type = 0x0012; px[pn].body = NULL; px[pn].n = 0; pn++;   /* signed_cert_timestamp */

    /* signature_algorithms: eight, in Chrome's order. */
    { static const unsigned sigs[] = { 0x0403, 0x0804, 0x0401, 0x0503,
                                       0x0805, 0x0501, 0x0806, 0x0601 };
      struct buf sb = { b_sigs, 0, sizeof(b_sigs) };
      put16(&sb, (unsigned)(sizeof(sigs) / sizeof(sigs[0])) * 2);
      for (size_t i = 0; i < sizeof(sigs) / sizeof(sigs[0]); i++) put16(&sb, sigs[i]);
      px[pn].type = 0x000D; px[pn].body = b_sigs; px[pn].n = sb.len; pn++; }

    /* key_share: a GREASE group with one zero byte, then (as in current Chrome) the
     * post-quantum share, then our X25519 half. The GREASE group is the same as in
     * supported_groups, as the browser does.
     *
     * With car->pq alone the post-quantum share is RANDOM bytes: no exchange runs on it, and
     * the peer ignores it. To an observer it is indistinguishable from a real key (which looks
     * like noise too), and it gives the Hello a browser's size (see pq in reality.h). */
    { struct buf kb = { b_ks, 0, sizeof(b_ks) };
      int want_pq = (car && car->pq) || cfg->pq;
      unsigned body = 2 + 2 + 1;                       /* GREASE */
      if (want_pq) body += 2 + 2 + REALITY_MLKEM_SHARE;
      body += 2 + 2 + 32;                              /* x25519 */
      put16(&kb, body);
      put16(&kb, g_group); put16(&kb, 1); put8(&kb, 0);
      if (want_pq) {
          put16(&kb, REALITY_GROUP_MLKEM); put16(&kb, REALITY_MLKEM_SHARE);
          if (cfg->pq) {
              /* The real hybrid: an ML-KEM-768 key (1184) and the same X25519 half as the
               * separate X25519 share below (BoringSSL and Go use one ephemeral key for both).
               * Field order per draft-ietf-tls-ecdhe-mlkem: ML-KEM first. The seed (d‖z) comes
               * from the same random source as the rest of the Hello, so hellofreeze.c
               * substitutes it too. */
              unsigned char seed[SC_MLKEM768_SEED];
              int krc = fill_random(seed, sizeof seed) != 0 ||
                        sc_mlkem768_keygen(b_pq, st->mlkem_dk, seed) != 0;
              sc_wipe(seed, sizeof seed);
              if (krc) return REALITY_ECRYPTO;
              memcpy(b_pq + SC_MLKEM768_EK, st->pub, 32);
              st->pq = 1;
          } else if (fill_random(b_pq, REALITY_MLKEM_SHARE) != 0) return REALITY_ECRYPTO;
          put(&kb, b_pq, REALITY_MLKEM_SHARE);
      }
      put16(&kb, 0x001D); put16(&kb, 32); put(&kb, st->pub, 32);
      px[pn].type = 0x0033; px[pn].body = b_ks; px[pn].n = kb.len; pn++; }

    /* supported_groups: GREASE, post-quantum (if offered), X25519, secp256r1, secp384r1: exactly
     * Chrome's set. Browsers offer no FFDHE groups (0x0100..0x0104). */
    { struct buf gb = { b_grp, 0, sizeof(b_grp) };
      int want_pq = (car && car->pq) || cfg->pq;
      put16(&gb, want_pq ? 10 : 8);
      put16(&gb, g_group);
      if (want_pq) put16(&gb, REALITY_GROUP_MLKEM);
      put16(&gb, 0x001D); put16(&gb, 0x0017); put16(&gb, 0x0018);
      px[pn].type = 0x000A; px[pn].body = b_grp; px[pn].n = gb.len; pn++; }

    b_pskm[0] = 1; b_pskm[1] = 1;
    px[pn].type = 0x002D; px[pn].body = b_pskm; px[pn].n = 2; pn++;  /* psk_key_exchange_modes */

    b_ecpf[0] = 1; b_ecpf[1] = 0;
    px[pn].type = 0x000B; px[pn].body = b_ecpf; px[pn].n = 2; pn++;  /* ec_point_formats */

    /* Last: the second GREASE extension, with one zero byte. */
    b_last[0] = 0;
    px[pn].type = g_ext_b; px[pn].body = b_last; px[pn].n = 1; pn++;

    /* Fisher-Yates shuffle of all but the first and last. */
    if (pn > 3) {
        unsigned char sh[20];
        if (fill_random(sh, sizeof(sh)) != 0) return REALITY_ECRYPTO;
        for (size_t i = pn - 2; i > 1; i--) {
            size_t j = 1 + (size_t)sh[i] % i;
            struct pend t = px[i]; px[i] = px[j]; px[j] = t;
        }
    }
    for (size_t i = 0; i < pn; i++) ext(b, px[i].type, px[i].body, px[i].n);
    return 0;
}

static int firefox_body(struct buf *b, size_t *xl, const struct reality_cfg *cfg,
                        struct reality_state *st, const struct reality_carrier *car) {
    static const unsigned suites[] = {
        0x1301, 0x1303, 0x1302, 0xC02B, 0xC02F, 0xCCA9, 0xCCA8, 0xC02C, 0xC030,
        0xC00A, 0xC009, 0xC013, 0xC014, 0x009C, 0x009D, 0x002F, 0x0035,
    };
    static const unsigned groups[] = {
        REALITY_GROUP_MLKEM, 0x001D, 0x0017, 0x0018, 0x0019, 0x0100, 0x0101,
    };
    static const unsigned dcs[] = { 0x0403, 0x0503, 0x0603, 0x0203 };
    static const unsigned sigs[] = {
        0x0403, 0x0503, 0x0603, 0x0804, 0x0805, 0x0806, 0x0401, 0x0501, 0x0601, 0x0203, 0x0201,
    };
    unsigned char ks[2 + 4 + REALITY_MLKEM_SHARE + 4 + 32 + 4 + 65];
    unsigned char sni[300], alpn[16], grp[2 + 2 * 7], dc[2 + 2 * 4], sg[2 + 2 * 11], cc[1 + 2 * 32];
    unsigned char ech[1 + 2 + 2 + 1 + 2 + 32 + 2 + 239], noise[1 + 32 + 239 + 1];
    unsigned char seed[SC_MLKEM768_SEED], p256[32], p256_pub[65];

    put16(b, (unsigned)(sizeof suites / sizeof suites[0]) * 2);
    for (size_t i = 0; i < sizeof suites / sizeof suites[0]; i++) put16(b, suites[i]);
    put8(b, 1); put8(b, 0);
    *xl = b->len;
    put16(b, 0);

    if (cfg->sni[0]) ext(b, 0x0000, sni, sni_body(sni, sizeof sni, cfg->sni));
    ext(b, 0x0017, NULL, 0);
    ext(b, 0xFF01, "\x00", 1);
    {
        struct buf gb = { grp, 0, sizeof grp };
        put16(&gb, (unsigned)(sizeof groups / sizeof groups[0]) * 2);
        for (size_t i = 0; i < sizeof groups / sizeof groups[0]; i++) put16(&gb, groups[i]);
        ext(b, 0x000A, grp, gb.len);
    }
    ext(b, 0x000B, "\x01\x00", 2);
    ext(b, 0x0010, alpn, alpn_body(alpn, sizeof alpn, car));
    ext(b, 0x0005, "\x01\x00\x00\x00\x00", 5);
    {
        struct buf db = { dc, 0, sizeof dc };
        put16(&db, (unsigned)(sizeof dcs / sizeof dcs[0]) * 2);
        for (size_t i = 0; i < sizeof dcs / sizeof dcs[0]; i++) put16(&db, dcs[i]);
        ext(b, 0x0022, dc, db.len);
    }
    ext(b, 0x0012, NULL, 0);
    {
        unsigned char *ek = ks + 2 + 4;
        int krc = fill_random(seed, sizeof seed) != 0 || sc_mlkem768_keygen(ek, st->mlkem_dk, seed) != 0;
        sc_wipe(seed, sizeof seed);
        if (krc) return REALITY_ECRYPTO;
        memcpy(ek + SC_MLKEM768_EK, st->pub, 32);
        st->pq = 1;
        int tries = 0;
        do {
            if (++tries > 4 || fill_random(p256, sizeof p256) != 0) return REALITY_ECRYPTO;
        } while (sc_p256_pub(p256, p256_pub) != 0);
        sc_wipe(p256, sizeof p256);
        struct buf kb = { ks, 0, sizeof ks };
        put16(&kb, (unsigned)(sizeof ks - 2));
        put16(&kb, REALITY_GROUP_MLKEM); put16(&kb, REALITY_MLKEM_SHARE);
        kb.len += REALITY_MLKEM_SHARE;
        put16(&kb, 0x001D); put16(&kb, 32); put(&kb, st->pub, 32);
        put16(&kb, 0x0017); put16(&kb, 65); put(&kb, p256_pub, 65);
        ext(b, 0x0033, ks, kb.len);
    }
    ext(b, 0x002B, "\x04\x03\x04\x03\x03", 5);
    {
        struct buf sb = { sg, 0, sizeof sg };
        put16(&sb, (unsigned)(sizeof sigs / sizeof sigs[0]) * 2);
        for (size_t i = 0; i < sizeof sigs / sizeof sigs[0]; i++) put16(&sb, sigs[i]);
        ext(b, 0x000D, sg, sb.len);
    }
    ext(b, 0x001C, "\x40\x01", 2);
    ext(b, 0x001B, cc, cert_algs_body(cc, sizeof cc, REALITY_FP_FIREFOX));
    {
        if (fill_random(noise, sizeof noise) != 0) return REALITY_ECRYPTO;
        if (car && car->fill_ech && car->fill_ech(car->ctx, noise + 33, 239, st->shared) != 0)
            return REALITY_ECRYPTO;
        struct buf eb = { ech, 0, sizeof ech };
        put8(&eb, 0x00);
        put16(&eb, 0x0001);
        put16(&eb, (noise[sizeof noise - 1] & 1) ? 0x0003 : 0x0001);
        put8(&eb, noise[0]);
        put16(&eb, 32);
        put(&eb, noise + 1, 32);
        put16(&eb, 239);
        put(&eb, noise + 33, 239);
        ext(b, 0xFE0D, ech, eb.len);
    }
    return 0;
}

/* The Hello without a carrier; tests/hellofreeze.c pins its bytes. */
int reality_build_hello(const struct reality_cfg *cfg, struct reality_state *st,
                        unsigned char *out, size_t out_n, size_t *out_len) {
    return reality_build_hello_carry(cfg, st, NULL, out, out_n, out_len);
}

int reality_build_hello_carry(const struct reality_cfg *cfg, struct reality_state *st,
                              const struct reality_carrier *car,
                              unsigned char *out, size_t out_n, size_t *out_len) {
    unsigned char pbk[32], sid[16];
    int sid_n = 0;
    if (!cfg->plain) {
        int pbk_n = b64url_decode(cfg->pbk, pbk, sizeof(pbk));
        if (pbk_n != 32) return REALITY_EBADKEY;
        sid_n = cfg->sid[0] ? hex_decode(cfg->sid, sid, sizeof(sid)) : 0;
        if (sid_n < 0) return REALITY_EBADKEY;
    }

    st->pq = 0;
    if (car && car->priv) {
        memcpy(st->priv, car->priv, 32);
        memcpy(st->pub, car->pub, 32);
    } else if (x25519_keypair(st->priv, st->pub) != 0) return REALITY_ECRYPTO;
    /* The secret shared with the server's STATIC key serves only the authenticator (or a
     * carrier's callbacks). Plain TLS has none: there is only the ephemeral exchange with the
     * server's key_share, which tls13.c computes from ServerHello. */
    if (!cfg->plain && x25519_shared(st->priv, pbk, st->shared) != 0) return REALITY_ECRYPTO;

    /* The authenticator is computed AFTER the Hello is built (below, where it is written in
     * place), because it signs the whole ClientHello. Here only the plaintext: 16 meaningful
     * bytes and 16 zeros for the tag. */
    unsigned char sess[32] = {0};
    if (cfg->plain) {
        /* A browser sends 32 random bytes in legacy_session_id, a leftover of TLS 1.2
         * resumption. TLS 1.3 does not use them (the server echoes them back), but a modern
         * client never leaves the field empty, and a zero one would be a fingerprint. */
        if (fill_random(sess, sizeof(sess)) != 0) return REALITY_ECRYPTO;
    } else {
        /* Reality client version, as Xray's core.Version_{x,y,z}; a server checks it only with
         * minClientVer/maxClientVer set, but it is part of the signed 16 bytes, so it must be
         * plausible. Bytes 4..7 are Unix time: a server with maxTimeDiff set rejects a client
         * whose clock is off. */
        sess[0] = 26; sess[1] = 9; sess[2] = 8; sess[3] = 0;
        uint32_t now = (uint32_t)time(NULL);
        sess[4] = (unsigned char)(now >> 24);
        sess[5] = (unsigned char)(now >> 16);
        sess[6] = (unsigned char)(now >> 8);
        sess[7] = (unsigned char)now;
        if (sid_n > 0) memcpy(sess + 8, sid, (size_t)(sid_n > 8 ? 8 : sid_n));
    }
    memcpy(st->session_id, sess, 32);

    /* ---- the Hello itself ---- */
    struct buf b = { out, 0, out_n };
    unsigned char rnd[32];
    if (fill_random(rnd, sizeof(rnd)) != 0) return REALITY_ECRYPTO;

    /* The record header is filled in at the end, when the length is known. */
    size_t rec_at = b.len;
    put8(&b, 0x16);            /* handshake */
    /* Record version 0x0301, as browsers and openssl send it (checked by capture; RFC 8446
     * §5.1 allows it for an initial ClientHello). Do not "fix" it to 0x0303. */
    put16(&b, 0x0301);
    size_t rec_len_at = b.len;
    put16(&b, 0);

    size_t hs_at = b.len;
    put8(&b, 0x01);            /* ClientHello */
    size_t hs_len_at = b.len;
    put8(&b, 0); put16(&b, 0); /* 24-bit length */

    put16(&b, 0x0303);         /* legacy_version TLS 1.2, as TLS 1.3 requires */
    put(&b, rnd, 32);
    put8(&b, 32);
    put(&b, st->session_id, 32);

    size_t exts_len_at = 0;
    int brc = reality_fp_of(cfg->fp) == REALITY_FP_FIREFOX
                  ? firefox_body(&b, &exts_len_at, cfg, st, car)
                  : chrome_body(&b, &exts_len_at, cfg, st, car);
    if (brc) return brc;

    if (b.len > b.cap) return REALITY_ETOOBIG;

    /* Back-fill the lengths. */
    size_t exts_len = b.len - exts_len_at - 2;
    out[exts_len_at] = (unsigned char)(exts_len >> 8);
    out[exts_len_at + 1] = (unsigned char)exts_len;
    size_t hs_len = b.len - hs_at - 4;
    out[hs_len_at] = (unsigned char)(hs_len >> 16);
    out[hs_len_at + 1] = (unsigned char)(hs_len >> 8);
    out[hs_len_at + 2] = (unsigned char)hs_len;
    size_t rec_len = b.len - rec_at - 5;
    out[rec_len_at] = (unsigned char)(rec_len >> 8);
    out[rec_len_at + 1] = (unsigned char)rec_len;

    *out_len = b.len;

    /* ---- Reality authenticator ------------------------------------------------
     *
     * The format comes from Xray's implementation; it cannot be guessed (salt = SNI,
     * nonce = own pub and an empty AAD give a handshake the server answers with its camouflage
     * site).
     *
     *   key   = HKDF-SHA256(ikm = ECDH(our private key, server pbk),
     *                       salt = Random[0..20), info = "REALITY")
     *   nonce = Random[20..32)
     *   data  = the first 16 bytes of session_id (version, time, short id)
     *   AAD   = the WHOLE ClientHello as a handshake message, zeros in place of session_id
     *
     * The AAD is why this happens here: until the Hello is built there is nothing to sign. The
     * server repeats the computation with its private key and checks the tag. */
    /* Plain TLS has nothing to sign: its session_id is random and already in the Hello. */
    if (cfg->plain) return 0;

    {
        const unsigned char *raw = out + 5;              /* handshake message, no record header */
        const unsigned char *random = raw + 4 + 2;       /* after type + len24 + version */
        unsigned char *sid_at = out + 5 + 4 + 2 + 32 + 1;

        /* The plaintext: the 16 meaningful bytes of session_id. Encryption runs in place at
         * sid_at; the zeros belong only in the AAD. */
        unsigned char plain[16];
        memcpy(plain, sid_at, 16);

        /* The AAD is the Hello with ZEROS in place of session_id: Xray zeroes it before
         * signing (`copy(hello.Raw[39:], hello.SessionId)` with a freshly zeroed SessionId),
         * and the server does the same; 39 = 4+2+32+1 here too. With session_id filled in, the
         * tag does not match and the server silently answers with its camouflage site, which
         * looks exactly like a wrong key. Zeroed in a copy: the Hello itself goes out signed. */
        unsigned char aad[4096];
        size_t aad_n = b.len - 5;
        if (aad_n > sizeof(aad)) return REALITY_ETOOBIG;
        memcpy(aad, raw, aad_n);
        memset(aad + (4 + 2 + 32 + 1), 0, 32);

        /* A carrier signs the same bytes its own way: the handshake message with session_id
         * zeroed, exactly what the peer checks against. The Reality authenticator is then not
         * computed at all: its place is taken. */
        if (car && car->fill_sid) {
            if (car->fill_sid(car->ctx, sid_at, aad, aad_n, st->shared) != 0)
                return REALITY_ECRYPTO;
            memcpy(st->session_id, sid_at, 32);
            return 0;
        }

        /* The key goes to st rather than a local: the server signs its certificate with it,
         * and checking that signature is the only way it proves itself to us (tls13.c). */
        unsigned char *authkey = st->authkey;
        if (sc_hkdf(SC_SHA256, random, 20, st->shared, 32,
                    (const unsigned char *)"REALITY", 7, authkey, 32) != 0)
            return REALITY_ECRYPTO;

        /* AES-256-GCM with a one-time key: one encryption per handshake. */
        unsigned char tag[16];
        struct sc_aead gcm;
        memcpy(sid_at, plain, 16);
        int crc = sc_aead_setkey(&gcm, SC_AES256_GCM, authkey);
        if (crc == 0) crc = sc_aead_seal(&gcm, random + 20, aad, aad_n, sid_at, 16, tag);
        sc_aead_free(&gcm);
        if (crc != 0) return REALITY_ECRYPTO;
        memcpy(sid_at + 16, tag, 16);
        memcpy(st->session_id, sid_at, 32);
    }
    return 0;
}

/* The same exchange for tls13.c, which needs the secret with the server's EPHEMERAL key from
 * ServerHello; reality.c computes the one with its static key. Two values, one primitive. */
int x25519_shared_ext(const unsigned char priv[32], const unsigned char peer[32],
                      unsigned char out[32]) {
    return x25519_shared(priv, peer, out);
}

/* ---- primitives for other files --------------------------------------------
 *
 * Wrappers over this file's statics rather than copies, so each decision lives in one place.
 * It matters most for cpu_has_aes: the Hello's cipher order (the fingerprint) depends on it,
 * and a mismatch between the cipher we announce and the one we encrypt with would cost a
 * sixfold slowdown on MIPS. */
int xc_random(unsigned char *out, size_t n) { return fill_random(out, n); }
int xc_cpu_has_aes(void) { return cpu_has_aes(); }
int xc_b64url_decode(const char *in, unsigned char *out, size_t out_n) {
    return b64url_decode(in, out, out_n);
}

int xc_x25519_keypair(unsigned char priv[32], unsigned char pub[32]) {
    return x25519_keypair(priv, pub);
}

/* The public half of a GIVEN private key. An unclamped key is clamped by the layer (RFC 7748),
 * as wireguard-go and Go do, so the result matches theirs. */
int xc_x25519_public(const unsigned char priv[32], unsigned char pub[32]) {
    return sc_x25519_base(pub, priv) == 0 ? 0 : -1;
}
