/* Server certificate checks; why this is a separate file: certverify.h.
 *
 * WHAT IS CHECKED. security=tls has nothing but the certificate. Reality proves the server with
 * an authenticator only the holder of the static key can compute; plain TLS has no such key, and
 * the only proof is the chain to a root plus the CertificateVerify signature over the transcript.
 * Skipping either half checks nothing: a chain without the signature proves only that someone
 * once got a certificate for this name, a signature without the chain only that the peer holds
 * the key of the certificate it sent.
 *
 * WHAT IS NOT. No OCSP and no revocation lists: a router has neither the means nor the time to
 * fetch them, and a silent imitation of a check is worse than an honest absence. The library
 * checks the validity period of every chain certificate against the current time
 * (sc_chain_verify); a cached chain is held to the same dates on every use.
 *
 * The X.509 work is behind the primitives layer (src/lib/scrypto.h): wolfSSL builds and checks
 * the path to a root, the CA flags and the name; wolfCrypt checks the CertificateVerify
 * signature. What stays here is TLS 1.3, not X.509: parsing Certificate and CertificateVerify,
 * the prefixed string the server signs, and the algorithm chosen by the message's code.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <stdint.h>
#include <time.h>

#include "scrypto.h"
#include "certverify.h"

/* ---- root store ----------------------------------------------------------------------
 *
 * Parsed ONCE per process, from the path of the first call; later paths are ignored. The
 * ca-bundle file is 182 KB and about 150 certificates; parsing it per connection would cost half
 * a second and a third of a megabyte on EVERY node attempt. Hence pthread_once: connector
 * threads (stack.c) run in parallel, and two simultaneous first calls would otherwise parse the
 * store twice, the second on top of the first.
 *
 * Never freed, on purpose: the store lives as long as the process. */
static struct sc_roots *g_roots;
static int g_roots_rc = CERTV_ENOROOTS;
static pthread_once_t g_roots_once = PTHREAD_ONCE_INIT;
static const char *g_roots_path;

static void roots_load(void) {
    const char *path = (g_roots_path && g_roots_path[0]) ? g_roots_path : CERTV_DEFAULT_ROOTS;

    FILE *f = fopen(path, "rb");
    if (!f) return;
    if (fseek(f, 0, SEEK_END) != 0) { fclose(f); return; }
    long sz = ftell(f);
    /* The path comes from the configuration, and pointing it at, say, /dev/zero must not eat
     * the router's memory. ca-bundle is 182 KB; 8 MB is enough for any sane store. */
    if (sz <= 0 || sz > 8 * 1024 * 1024) { fclose(f); return; }
    rewind(f);

    /* +1 for a NUL: PEM parsing does not need it, but it keeps the text safe to read as a
     * string. */
    unsigned char *buf = malloc((size_t)sz + 1);
    if (!buf) { fclose(f); return; }
    size_t got = fread(buf, 1, (size_t)sz, f);
    fclose(f);
    buf[got] = '\0';

    /* The layer skips entries that do not parse: stores carry roots with algorithms our wolfSSL
     * build lacks (and expired ones), and demanding a perfect parse would lose all roots over
     * one exotic entry. It fails only when NOTHING parses. */
    int rc = sc_roots_load(&g_roots, buf, got);
    free(buf);
    if (rc != 0) return;
    g_roots_rc = 0;
}

/* ---- Certificate message (RFC 8446 §4.4.2) ------------------------------------------
 *
 * Body: the request context (1 length byte and the bytes), then a list with a 3-byte length of
 * entries "3-byte length + DER", each followed by extensions with a 2-byte length. Extensions
 * are not read: at most they carry signed_certificate_timestamp, which does not affect the
 * verdict.
 */
/* How many chain certificates are checked. Real chains have two to four. Anything past sixteen
 * is dropped rather than failing the check: if the needed intermediate was seventeenth, no path
 * to a root is built, and the chain fails honestly. */
#define CHAIN_MAX 16

/* Splits the message into DER pieces. The library parses the certificates (sc_chain_verify): a
 * leaf that does not parse fails the check, an intermediate is skipped (chains often come with
 * extras, and an unknown algorithm in a spare certificate decides nothing). */
static int parse_chain(const unsigned char *b, size_t n, const unsigned char **der,
                       size_t *der_n, size_t *count) {
    *count = 0;
    if (n < 1) return CERTV_EPARSE;
    size_t p = 1 + b[0];                       /* certificate_request_context */
    if (p + 3 > n) return CERTV_EPARSE;
    size_t list = ((size_t)b[p] << 16) | ((size_t)b[p + 1] << 8) | b[p + 2];
    p += 3;
    if (p + list != n) return CERTV_EPARSE;

    size_t end = p + list;
    while (p + 3 <= end) {
        size_t clen = ((size_t)b[p] << 16) | ((size_t)b[p + 1] << 8) | b[p + 2];
        p += 3;
        if (clen == 0 || p + clen > end) return CERTV_EPARSE;
        if (*count < CHAIN_MAX) {
            der[*count] = b + p;
            der_n[*count] = clen;
            (*count)++;
        }
        p += clen;
        if (p + 2 > end) break;
        size_t elen = ((size_t)b[p] << 8) | b[p + 1];
        p += 2;
        if (p + elen > end) return CERTV_EPARSE;
        p += elen;
    }
    return *count ? 0 : CERTV_EPARSE;
}

/* ---- CertificateVerify signature (RFC 8446 §4.4.3) -----------------------------------
 *
 * The server signs not the transcript bytes but a prefixed string: 64 spaces, "TLS 1.3, server
 * CertificateVerify", a zero byte and the transcript hash. The prefix keeps a signature from
 * being moved between roles and protocol versions.
 */
static const char CV_LABEL[] = "TLS 1.3, server CertificateVerify";

/* The signature scheme from its two-byte code: ECDSA and RSA-PSS. rsa_pkcs1_* are refused even
 * though signature_algorithms offers them (reality.c, as Chrome does): TLS 1.3 forbids them for
 * CertificateVerify (RFC 8446 §4.4.3), they serve only for signatures INSIDE certificates. A
 * server that picks a scheme outside this list gets its own reason (CERTV_EALG), not "bad
 * signature".
 *
 * rsa_pss_pss_* (0x0809..0x080B) pass here although we do not offer them. */
static int sig_alg(unsigned code, enum sc_hash *md, enum sc_sig_alg *alg) {
    switch (code) {
        case 0x0403: *md = SC_SHA256; *alg = SC_SIG_ECDSA; return 0;  /* ecdsa_secp256r1 */
        case 0x0503: *md = SC_SHA384; *alg = SC_SIG_ECDSA; return 0;  /* ecdsa_secp384r1 */
        case 0x0603: *md = SC_SHA512; *alg = SC_SIG_ECDSA; return 0;  /* ecdsa_secp521r1 */
        case 0x0804: *md = SC_SHA256; *alg = SC_SIG_RSA_PSS; return 0;  /* rsa_pss_rsae */
        case 0x0805: *md = SC_SHA384; *alg = SC_SIG_RSA_PSS; return 0;
        case 0x0806: *md = SC_SHA512; *alg = SC_SIG_RSA_PSS; return 0;
        case 0x0809: *md = SC_SHA256; *alg = SC_SIG_RSA_PSS; return 0;  /* rsa_pss_pss */
        case 0x080A: *md = SC_SHA384; *alg = SC_SIG_RSA_PSS; return 0;
        case 0x080B: *md = SC_SHA512; *alg = SC_SIG_RSA_PSS; return 0;
        default: return CERTV_EALG;
    }
}

static int check_signature(const unsigned char *leaf, size_t leaf_n,
                           const unsigned char *cv, size_t cv_n,
                           const unsigned char *transcript, size_t thash_n) {
    if (cv_n < 4) return CERTV_EPARSE;
    unsigned code = ((unsigned)cv[0] << 8) | cv[1];
    size_t sig_n = ((size_t)cv[2] << 8) | cv[3];
    if (4 + sig_n != cv_n) return CERTV_EPARSE;

    enum sc_hash mdt;
    enum sc_sig_alg alg;
    int rc = sig_alg(code, &mdt, &alg);
    if (rc) return rc;
    size_t hn = sc_hash_len(mdt);

    /* The transcript hash length and the signature's hash length are DIFFERENT things. The
     * transcript is hashed with the CIPHER SUITE's hash (RFC 8446 §4.4.1): 48 bytes for
     * TLS_AES_256_GCM_SHA384. The signature uses the scheme from CertificateVerify, and the
     * server may pick rsa_pss_rsae_sha256, 32 bytes. www.microsoft.com does exactly that
     * (SHA-384 suite, SHA-256 signature), so the two must not be required to match. */
    if (thash_n == 0 || thash_n > 64) return CERTV_EPARSE;

    /* 64 + 33 + 1 + 64 = 162: room for the longest transcript hash. */
    unsigned char content[176];
    size_t cn = 0;
    memset(content, 0x20, 64); cn = 64;
    memcpy(content + cn, CV_LABEL, sizeof(CV_LABEL) - 1); cn += sizeof(CV_LABEL) - 1;
    content[cn++] = 0x00;
    memcpy(content + cn, transcript, thash_n); cn += thash_n;

    unsigned char digest[64];
    if (sc_hash(mdt, content, cn, digest) != 0) return CERTV_ESIG;

    /* The layer accepts a PSS salt of ANY length. RFC 8446 wants it as long as the hash, but
     * some servers (and middleboxes that re-sign the stream) use another length; rejecting them
     * would declare a node broken where the signature is valid. A key of the wrong kind (an
     * ECDSA signature with an RSA key) is "bad signature" too. */
    return sc_cert_verify_sig(leaf, leaf_n, alg, mdt, digest, hn, cv + 4, sig_n) == 0
               ? 0 : CERTV_ESIG;
}

int cert_verify_server(const unsigned char *cert_body, size_t cert_n,
                       const unsigned char *cv_body, size_t cv_n,
                       const unsigned char *transcript, size_t thash_n,
                       const char *host, const char *roots) {
    return cert_verify_server_ex(cert_body, cert_n, cv_body, cv_n, transcript, thash_n, host, roots, NULL);
}

static int der_next(const unsigned char **p, const unsigned char *end,
                    unsigned char *tag, const unsigned char **val, size_t *val_n);

/* Whether fingerprint h is in the list (64 lowercase hex digits per entry, comma-separated). */
static int pin_listed(const char *list, const unsigned char h[32]) {
    static const char HEX[] = "0123456789abcdef";
    char want[64];
    if (!list) return 0;
    for (int i = 0; i < 32; i++) { want[2 * i] = HEX[h[i] >> 4]; want[2 * i + 1] = HEX[h[i] & 15]; }
    for (const char *p = list; *p;) {
        const char *e = strchr(p, ',');
        size_t n = e ? (size_t)(e - p) : strlen(p);
        if (n == 64 && !memcmp(p, want, 64)) return 1;
        if (!e) break;
        p = e + 1;
    }
    return 0;
}

/* The certificate's whole SubjectPublicKeyInfo, header included (what sing-box hashes).
 * tbsCertificate ::= [0] version (optional), serial, signature, issuer, validity, subject, spki:
 * the seventh field, or the sixth without a version. A real walk: issuer and subject vary in
 * length. */
static int spki_of(const unsigned char *der, size_t n, const unsigned char **spki, size_t *spki_n) {
    const unsigned char *p = der, *end = der + n, *v;
    unsigned char tag;
    size_t vn;
    if (der_next(&p, end, &tag, &v, &vn) != 0 || tag != 0x30) return -1;   /* Certificate */
    const unsigned char *ip = v, *iend = v + vn;
    if (der_next(&ip, iend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* tbs */
    const unsigned char *tp = v, *tend = v + vn;
    const unsigned char *start;
    if (tp < tend && *tp == 0xA0 && der_next(&tp, tend, &tag, &v, &vn) != 0) return -1;
    for (int i = 0; i < 5; i++)                   /* serial, signature, issuer, validity, subject */
        if (der_next(&tp, tend, &tag, &v, &vn) != 0) return -1;
    start = tp;
    if (der_next(&tp, tend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;
    *spki = start;
    *spki_n = (size_t)(tp - start);
    return 0;
}

/* The chain against ONE store and a list of names; any name will do (verifyPeerCertByName). */
static int chain_by_names(struct sc_roots *roots, const unsigned char *const *der, const size_t *der_n,
                          size_t count, const char *names, int64_t *nb, int64_t *na) {
    int rc = CERTV_ECHAIN;
    for (const char *p = names; *p;) {
        const char *e = strchr(p, ',');
        size_t n = e ? (size_t)(e - p) : strlen(p);
        char one[256];
        if (n && n < sizeof one) {
            memcpy(one, p, n);
            one[n] = '\0';
            int vr = sc_chain_verify_ex(roots, der, der_n, count, one, nb, na);
            if (vr == 0) return 0;
            if (vr == SC_EPARSE) rc = CERTV_EPARSE;
        }
        if (!e) break;
        p = e + 1;
    }
    return rc;
}

#define CC_SLOTS 64
#define CC_TTL_MS 3600000u
#define CC_JITTER_MS 2000u
#define CC_STRIPES 8

struct cc_ent {
    unsigned char key[32];
    int64_t nb, na;
    uint64_t born, used;
};

static struct cc_ent g_cc[CC_SLOTS];
static pthread_mutex_t g_cc_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_mutex_t g_cc_miss[CC_STRIPES] = {
    PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER,
    PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER,
};
static int64_t g_cc_wall_s;
static uint64_t g_cc_mono_ms;
static unsigned long g_cc_checked;

static int64_t cc_wall(void) {
    return g_cc_wall_s ? g_cc_wall_s : (int64_t)time(NULL);
}

static uint64_t cc_mono(void) {
    if (g_cc_mono_ms) return g_cc_mono_ms;
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    uint64_t ms = (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
    return ms ? ms : 1;
}

static uint64_t cc_ttl(const unsigned char key[32]) {
    return CC_TTL_MS - (uint64_t)key[0] * CC_JITTER_MS;
}

static int cc_str(struct sc_hash_ctx *h, const char *s) {
    size_t n = s ? strlen(s) : 0;
    if (n > 0xffff) return -1;
    unsigned char l[2] = { (unsigned char)(n >> 8), (unsigned char)n };
    if (sc_hash_update(h, l, 2) != 0) return -1;
    return n && sc_hash_update(h, s, n) != 0 ? -1 : 0;
}

static int cc_key(int anchor, const char *names, const struct cert_policy *pol, const unsigned char *const *der,
                  const size_t *der_n, size_t count, unsigned char out[32]) {
    static const char label[] = "tunvless chain cache 1";
    struct sc_hash_ctx h;
    if (count > 255 || sc_hash_init(&h, SC_SHA256) != 0) return -1;
    unsigned char a = (unsigned char)(anchor ? 1 : 0), c = (unsigned char)count;
    int rc = sc_hash_update(&h, label, sizeof label) != 0 || sc_hash_update(&h, &a, 1) != 0 ||
             cc_str(&h, names) != 0 || cc_str(&h, pol ? pol->pcs : NULL) != 0 ||
             cc_str(&h, pol ? pol->pks : NULL) != 0 || sc_hash_update(&h, &c, 1) != 0;
    for (size_t i = 0; !rc && i < count; i++) {
        if (der_n[i] > 0xffffff) { rc = 1; break; }
        unsigned char l[3] = { (unsigned char)(der_n[i] >> 16), (unsigned char)(der_n[i] >> 8),
                               (unsigned char)der_n[i] };
        rc = sc_hash_update(&h, l, 3) != 0 || sc_hash_update(&h, der[i], der_n[i]) != 0;
    }
    if (!rc) rc = sc_hash_final(&h, out) != 0;
    sc_hash_free(&h);
    return rc ? -1 : 0;
}

static int cc_get(const unsigned char key[32]) {
    int64_t wall = cc_wall();
    uint64_t mono = cc_mono(), ttl = cc_ttl(key);
    int hit = 0;
    pthread_mutex_lock(&g_cc_mu);
    for (int i = 0; i < CC_SLOTS; i++) {
        struct cc_ent *e = &g_cc[i];
        if (!e->born || memcmp(e->key, key, 32) != 0) continue;
        if (e->nb <= wall && wall < e->na && mono >= e->born && mono - e->born < ttl) {
            e->used = mono;
            hit = 1;
        }
        break;
    }
    pthread_mutex_unlock(&g_cc_mu);
    return hit;
}

static void cc_put(const unsigned char key[32], int64_t nb, int64_t na) {
    if (nb >= na) return;
    uint64_t mono = cc_mono();
    pthread_mutex_lock(&g_cc_mu);
    struct cc_ent *v = NULL;
    for (int i = 0; i < CC_SLOTS && !v; i++)
        if (g_cc[i].born && !memcmp(g_cc[i].key, key, 32)) v = &g_cc[i];
    for (int i = 0; i < CC_SLOTS && !v; i++)
        if (!g_cc[i].born) v = &g_cc[i];
    if (!v) {
        v = &g_cc[0];
        for (int i = 1; i < CC_SLOTS; i++)
            if (g_cc[i].used < v->used) v = &g_cc[i];
    }
    memcpy(v->key, key, 32);
    v->nb = nb;
    v->na = na;
    v->born = v->used = mono;
    pthread_mutex_unlock(&g_cc_mu);
}

static void cc_trace(const char *names, int rc, int hit, int cached, int64_t na) {
    if (!getenv("STEER_CHAIN_TRACE")) return;
    if (hit) fprintf(stderr, "tunvless[debug] chain: %s from the cache\n", names);
    else if (rc) fprintf(stderr, "tunvless[debug] chain: %s refused\n", names);
    else if (cached) fprintf(stderr, "tunvless[debug] chain: %s verified, cached until %lld\n", names, (long long)na);
    else fprintf(stderr, "tunvless[debug] chain: %s verified, not cached\n", names);
}

static int pin_listed(const char *list, const unsigned char h[32]);

static int cc_check(struct sc_roots *store, const unsigned char *pinned_der, size_t pinned_n,
                    const unsigned char *const *der, const size_t *der_n, size_t count, const char *names,
                    const struct cert_policy *pol, int64_t *nb, int64_t *na) {
    struct sc_roots *ca = NULL;
    const unsigned char *ca_der = pinned_der;
    size_t ca_n = pinned_n;
    if (pinned_der) {
        if (sc_roots_load_der(&ca, pinned_der, pinned_n) != 0) ca = NULL;
        int after = 0;
        for (size_t i = 1; i < count && !ca; i++) {
            unsigned char h[32];
            if (!after) { after = der[i] == pinned_der; continue; }
            if (sc_hash(SC_SHA256, der[i], der_n[i], h) == 0 && pin_listed(pol ? pol->pcs : NULL, h)) {
                if (sc_roots_load_der(&ca, der[i], der_n[i]) == 0) { ca_der = der[i]; ca_n = der_n[i]; }
                else ca = NULL;
            }
        }
        if (!ca) return CERTV_EPIN;
        store = ca;
    }
    int rc = chain_by_names(store, der, der_n, count, names, nb, na);
    if (ca) {
        int64_t cb = INT64_MAX, ce = INT64_MIN;
        if (rc == 0) {
            sc_cert_window(ca_der, ca_n, &cb, &ce);
            if (cb > *nb) *nb = cb;
            if (ce < *na) *na = ce;
        }
        sc_roots_free(ca);
    }
    pthread_mutex_lock(&g_cc_mu);
    g_cc_checked++;
    pthread_mutex_unlock(&g_cc_mu);
    return rc;
}

static int cc_chain(struct sc_roots *store_or_null, const unsigned char *pinned_der, size_t pinned_n,
                    const unsigned char *const *der, const size_t *der_n, size_t count, const char *names,
                    const struct cert_policy *pol, const char *roots) {
    struct sc_roots *store = store_or_null;
    if (!pinned_der && !store) {
        g_roots_path = roots;
        pthread_once(&g_roots_once, roots_load);
        if (g_roots_rc != 0) return CERTV_ENOROOTS;
        store = g_roots;
    }
    unsigned char key[32];
    int64_t nb = INT64_MAX, na = INT64_MIN;
    if (cc_key(pinned_der != NULL, names, pol, der, der_n, count, key) != 0) {
        int rc = cc_check(store, pinned_der, pinned_n, der, der_n, count, names, pol, &nb, &na);
        cc_trace(names, rc, 0, 0, 0);
        return rc;
    }
    if (cc_get(key)) {
        cc_trace(names, 0, 1, 0, 0);
        return 0;
    }
    pthread_mutex_t *stripe = &g_cc_miss[key[0] & (CC_STRIPES - 1)];
    pthread_mutex_lock(stripe);
    if (cc_get(key)) {
        pthread_mutex_unlock(stripe);
        cc_trace(names, 0, 1, 0, 0);
        return 0;
    }
    int rc = cc_check(store, pinned_der, pinned_n, der, der_n, count, names, pol, &nb, &na);
    if (!rc) cc_put(key, nb, na);
    pthread_mutex_unlock(stripe);
    cc_trace(names, rc, 0, !rc && nb < na, na);
    return rc;
}

int cert_verify_server_ex(const unsigned char *cert_body, size_t cert_n,
                          const unsigned char *cv_body, size_t cv_n,
                          const unsigned char *transcript, size_t thash_n,
                          const char *host, const char *roots, const struct cert_policy *pol) {
    if (!cert_body || !cv_body || !host || !host[0]) return CERTV_EPARSE;

    const unsigned char *der[CHAIN_MAX];
    size_t der_n[CHAIN_MAX], count = 0;
    int rc = parse_chain(cert_body, cert_n, der, der_n, &count);
    if (rc) return rc;

    /* insecure: only the signature is checked (see certverify.h). */
    if (pol && pol->insecure)
        return check_signature(der[0], der_n[0], cv_body, cv_n, transcript, thash_n);

    const int have_pin = pol && ((pol->pcs && pol->pcs[0]) || (pol->pks && pol->pks[0]));
    const char *names = pol && pol->vcn && pol->vcn[0] ? pol->vcn : host;
    size_t pinned = 0;
    int leaf_pinned = 0;

    if (have_pin) {
        unsigned char h[32];
        /* The leaf, by certificate and by key (sing-box). A match: the pin is the trust. */
        if (sc_hash(SC_SHA256, der[0], der_n[0], h) == 0 && pin_listed(pol->pcs, h)) leaf_pinned = 1;
        if (!leaf_pinned && pol->pks && pol->pks[0]) {
            const unsigned char *sp;
            size_t sn;
            if (spki_of(der[0], der_n[0], &sp, &sn) == 0 && sc_hash(SC_SHA256, sp, sn, h) == 0 &&
                pin_listed(pol->pks, h))
                leaf_pinned = 1;
        }
        if (!leaf_pinned) {
            /* An intermediate or a root: Xray uses it as the root store if it is a CA
             * (verifyChain). A non-CA does not load into the store: same as no match. */
            for (size_t i = 1; i < count && !pinned; i++)
                if (sc_hash(SC_SHA256, der[i], der_n[i], h) == 0 && pin_listed(pol->pcs, h)) pinned = i;
            if (!pinned) return CERTV_EPIN;
        }
    }

    if (!leaf_pinned) {
        /* The name is checked HERE, with the chain: a separate check would be a second SAN
         * parser, and it would drift from the library's. */
        rc = cc_chain(NULL, pinned ? der[pinned] : NULL, pinned ? der_n[pinned] : 0, der, der_n, count, names,
                      pol, roots);
        if (rc) return rc;
    }
    return check_signature(der[0], der_n[0], cv_body, cv_n, transcript, thash_n);
}

/* ---- Reality: the server proves itself to us ----------------------------------------
 *
 * The mechanism is described in certverify.h. The parsing here is our OWN on purpose: the
 * Reality certificate is signed with an Ed25519 key, and our wolfSSL build has no Ed25519 (it
 * would serve nothing else; see build/wolfssl/user_settings.h). Parsing the whole certificate
 * for two fields would be extra code and a dependency on Ed25519 support. Exactly two fields are
 * needed, and both sit at predictable places in the DER.
 */

/* One DER step: tag, length, value. Returns 0, moves *p past the value and puts the value's
 * start and length into *val and *val_n. A certificate that fits a handshake message never has a
 * length field longer than four bytes. */
static int der_next(const unsigned char **p, const unsigned char *end,
                    unsigned char *tag, const unsigned char **val, size_t *val_n) {
    if (*p + 2 > end) return -1;
    *tag = *(*p)++;
    size_t n = *(*p)++;
    if (n & 0x80) {
        size_t k = n & 0x7F;
        if (k == 0 || k > 4 || *p + k > end) return -1;
        n = 0;
        while (k--) n = (n << 8) | *(*p)++;
    }
    if ((size_t)(end - *p) < n) return -1;
    *val = *p;
    *val_n = n;
    *p += n;
    return 0;
}

/* The Ed25519 public key from SubjectPublicKeyInfo.
 *
 * For Ed25519 this structure has ONE possible form, since the algorithm has no parameters and
 * the key is always 32 bytes:
 *
 *     30 2A            SEQUENCE (44 bytes)
 *        30 05         SEQUENCE (algorithm)
 *           06 03 2B 65 70   OID 1.3.101.112 (id-Ed25519)
 *        03 21 00      BIT STRING, 33 bytes, zero unused bits
 *        <32 bytes>
 *
 * So the key is found by this byte sequence rather than by walking the seven TBS fields: the
 * form has no variants, and a walk would be longer, with more places to go wrong. A certificate
 * that is not Ed25519 lacks the sequence, which is exactly the answer needed: not Reality. */
static const unsigned char *find_ed25519_pub(const unsigned char *b, size_t n) {
    static const unsigned char SPKI[] = {
        0x30, 0x2A, 0x30, 0x05, 0x06, 0x03, 0x2B, 0x65, 0x70, 0x03, 0x21, 0x00
    };
    if (n < sizeof(SPKI) + 32) return NULL;
    for (size_t i = 0; i + sizeof(SPKI) + 32 <= n; i++)
        if (memcmp(b + i, SPKI, sizeof(SPKI)) == 0) return b + i + sizeof(SPKI);
    return NULL;
}

/* The signature field is the last element of the certificate's outer SEQUENCE:
 *     Certificate ::= SEQUENCE { tbsCertificate, signatureAlgorithm, signatureValue }
 * A real walk here: tbsCertificate varies in length, so there is no pattern to search for. */
static int find_signature(const unsigned char *der, size_t n,
                          const unsigned char **sig, size_t *sig_n) {
    const unsigned char *p = der, *end = der + n, *v;
    unsigned char tag;
    size_t vn;
    if (der_next(&p, end, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* Certificate */
    const unsigned char *ip = v, *iend = v + vn;
    if (der_next(&ip, iend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* tbs */
    if (der_next(&ip, iend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* algid */
    if (der_next(&ip, iend, &tag, &v, &vn) != 0 || tag != 0x03) return -1;  /* BIT STRING */
    if (vn < 2 || v[0] != 0) return -1;      /* no unused bits allowed */
    *sig = v + 1;
    *sig_n = vn - 1;
    return 0;
}

int cert_reality_check(const unsigned char *cert_body, size_t cert_n,
                       const unsigned char *authkey) {
    if (!cert_body || !authkey) return CERTV_EPARSE;

    /* The first certificate of the list is the one. Both the key and the signature are read
     * from its RAW DER bytes. */
    if (cert_n < 1) return CERTV_EPARSE;
    size_t p = 1 + cert_body[0];
    if (p + 3 > cert_n) return CERTV_EPARSE;
    size_t list = ((size_t)cert_body[p] << 16) | ((size_t)cert_body[p + 1] << 8) | cert_body[p + 2];
    p += 3;
    if (p + 3 > cert_n || list < 3) return CERTV_EPARSE;
    size_t clen = ((size_t)cert_body[p] << 16) | ((size_t)cert_body[p + 1] << 8) | cert_body[p + 2];
    p += 3;
    if (clen == 0 || p + clen > cert_n) return CERTV_EPARSE;
    const unsigned char *der = cert_body + p;

    const unsigned char *pub = find_ed25519_pub(der, clen);
    if (!pub) return CERTV_ENOTREALITY;      /* not Ed25519: the camouflage site */

    const unsigned char *sig;
    size_t sig_n;
    if (find_signature(der, clen, &sig, &sig_n) != 0) return CERTV_EPARSE;

    unsigned char want[64];
    if (sc_hmac(SC_SHA512, authkey, 32, pub, 32, want) != 0) return CERTV_ESIG;

    /* Constant-time compare. A leak here reveals nothing (both sides see the bytes anyway),
     * but comparing secret-derived data with memcmp is a habit not to start in this file. */
    if (sig_n != sizeof(want)) return CERTV_ENOTREALITY;
    unsigned char diff = 0;
    for (size_t i = 0; i < sizeof(want); i++) diff |= (unsigned char)(want[i] ^ sig[i]);
    return diff ? CERTV_ENOTREALITY : 0;
}

/* The value of the first X.509 extension (extnValue, the OCTET STRING contents) of a Reality
 * temporary certificate with ML-DSA. A real walk: Certificate → tbsCertificate → [3] extensions
 * → SEQUENCE → the first Extension { OID, [BOOLEAN critical], OCTET STRING }. Go reads the same:
 * Extensions[0].Value. */
static int find_first_ext_value(const unsigned char *der, size_t n,
                                const unsigned char **val, size_t *val_n) {
    const unsigned char *p = der, *end = der + n, *v;
    unsigned char tag;
    size_t vn;
    if (der_next(&p, end, &tag, &v, &vn) != 0 || tag != 0x30) return -1;   /* Certificate */
    const unsigned char *ip = v, *iend = v + vn;
    if (der_next(&ip, iend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* tbs */
    const unsigned char *tp = v, *tend = v + vn, *ev = NULL;
    size_t evn = 0;
    while (tp < tend) {
        if (der_next(&tp, tend, &tag, &v, &vn) != 0) return -1;
        if (tag == 0xA3) { ev = v; evn = vn; break; }                       /* [3] extensions */
    }
    if (!ev) return -1;
    const unsigned char *sp = ev;
    if (der_next(&sp, ev + evn, &tag, &v, &vn) != 0 || tag != 0x30) return -1;  /* Extensions */
    const unsigned char *xp = v, *xend = v + vn;
    if (der_next(&xp, xend, &tag, &v, &vn) != 0 || tag != 0x30) return -1;   /* Extension #0 */
    const unsigned char *fp = v, *fend = v + vn;
    if (der_next(&fp, fend, &tag, &v, &vn) != 0 || tag != 0x06) return -1;   /* extnID */
    if (der_next(&fp, fend, &tag, &v, &vn) != 0) return -1;
    if (tag == 0x01 && der_next(&fp, fend, &tag, &v, &vn) != 0) return -1;   /* critical */
    if (tag != 0x04) return -1;                                              /* extnValue */
    *val = v;
    *val_n = vn;
    return 0;
}

int cert_reality_check_pq(const unsigned char *cert_body, size_t cert_n,
                          const unsigned char *authkey, const unsigned char *pk,
                          const unsigned char *ch, size_t ch_n,
                          const unsigned char *sh, size_t sh_n) {
    if (!cert_body || !authkey || !pk || !ch || !sh || cert_n < 1) return CERTV_EPARSE;
    size_t p = 1 + cert_body[0];
    if (p + 6 > cert_n) return CERTV_EPARSE;
    p += 3;
    size_t clen = ((size_t)cert_body[p] << 16) | ((size_t)cert_body[p + 1] << 8) | cert_body[p + 2];
    p += 3;
    if (clen == 0 || p + clen > cert_n) return CERTV_EPARSE;
    const unsigned char *der = cert_body + p;
    const unsigned char *epub = find_ed25519_pub(der, clen);
    if (!epub) return CERTV_EPARSE;

    const unsigned char *sig;
    size_t sig_n;
    if (find_first_ext_value(der, clen, &sig, &sig_n) != 0 || sig_n != SC_MLDSA65_SIG) return CERTV_EPQ;

    /* HMAC over pub ‖ ClientHello ‖ ServerHello. sc_hmac2 takes two pieces, not three, so they
     * are joined on the heap: 32 + about 1.7 KB + about 1.2 KB. */
    size_t tot = 32 + ch_n + sh_n;
    unsigned char *msg = malloc(tot);
    if (!msg) return CERTV_EPQ;
    memcpy(msg, epub, 32);
    memcpy(msg + 32, ch, ch_n);
    memcpy(msg + 32 + ch_n, sh, sh_n);
    unsigned char mac[64];
    int rc = sc_hmac(SC_SHA512, authkey, 32, msg, tot, mac);
    free(msg);
    if (rc != 0) return CERTV_EPQ;
    return sc_mldsa65_verify(pk, mac, sizeof mac, sig, sig_n) == 0 ? 0 : CERTV_EPQ;
}

const char *cert_verify_strerror(int rc) {
    switch (rc) {
        case 0:              return "";
        case CERTV_EPARSE:   return "cannot parse the server certificate";
        case CERTV_ENOROOTS: return "no root store (install the ca-bundle package or use --ca)";
        case CERTV_ECHAIN:   return "certificate does not chain to a root or names another host";
        case CERTV_ESIG:     return "bad server signature";
        case CERTV_EPIN:     return "certificate matches no pinned fingerprint (pcs/pks)";
        case CERTV_EALG:     return "server signed with an algorithm we did not offer";
        /* About the key, not the server: the node is alive and answers, it just did not
         * recognise us. Almost always mismatched pbk/sid or someone else's subscription. */
        case CERTV_ENOTREALITY: return "node did not accept the key (the camouflage site answered)";
        case CERTV_EPQ:      return "server's ML-DSA-65 signature does not match the node's pqv";
        default:             return "certificate check failed";
    }
}
