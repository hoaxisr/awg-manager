/* Encrypted Client Hello, client side; see ech.h.
 *
 * Own HPKE rather than wolfCrypt's: base mode with DHKEM(X25519) is two HKDFs and one AEAD, all
 * already in the primitives layer (scrypto.h). Turning on HAVE_HPKE in the bundled wolfSSL for a
 * hundred lines would change the library's layout and size. This implementation is checked
 * against Go crypto/hpke (tests/echmatch.c) and a real ECH server, Xray-core on Go crypto/tls
 * (tests/ech.sh): both decrypt what it seals. */
#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>

#include "scrypto.h"
#include "reality.h"
#include "ech.h"

static unsigned be16(const uint8_t *p) { return ((unsigned)p[0] << 8) | p[1]; }
static size_t be24(const uint8_t *p) { return ((size_t)p[0] << 16) | ((size_t)p[1] << 8) | p[2]; }

int ech_b64_decode(const char *in, uint8_t *out, size_t cap) {
    uint32_t acc = 0;
    int bits = 0;
    size_t o = 0;
    for (; *in; in++) {
        int c = (unsigned char)*in, v;
        if (c >= 'A' && c <= 'Z') v = c - 'A';
        else if (c >= 'a' && c <= 'z') v = c - 'a' + 26;
        else if (c >= '0' && c <= '9') v = c - '0' + 52;
        else if (c == '+' || c == '-') v = 62;
        else if (c == '/' || c == '_') v = 63;
        else if (c == '=' || c == '\n' || c == '\r' || c == ' ') continue;
        else return -1;
        acc = ((acc << 6) | (uint32_t)v) & 0xFFFFFFu;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (o >= cap) return -1;
            out[o++] = (uint8_t)((acc >> bits) & 0xFF);
        }
    }
    return (int)o;
}

/* ---- ECHConfigList ---------------------------------------------------------------------------
 *
 *   ECHConfigList  = u16 length, then ECHConfigs back to back
 *   ECHConfig      = u16 version (0xfe0d), u16 length, contents
 *   contents       = u8 config_id, u16 kem_id, u16 length + public key,
 *                    u16 length + pairs (u16 kdf_id, u16 aead_id), u8 maximum_name_length,
 *                    u8 length + public_name, u16 length + extensions (u16 type, u16 length + data)
 */
static int name_ok(const char *s, size_t n) {
    if (n == 0 || n > 253) return 0;
    for (size_t i = 0; i < n; i++) {
        char c = s[i];
        int ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.';
        if (!ok) return 0;
    }
    return s[0] != '.' && s[0] != '-' && s[n - 1] != '-';
}

int ech_pick(const uint8_t *list, size_t n, struct ech_cfg *out) {
    if (n < 2 || (size_t)be16(list) + 2 != n) return ECH_EPARSE;
    size_t p = 2;
    while (p + 4 <= n) {
        unsigned ver = be16(list + p);
        size_t len = be16(list + p + 2);
        if (p + 4 + len > n) return ECH_EPARSE;
        const uint8_t *c = list + p + 4;
        const uint8_t *raw = list + p;
        size_t rawn = 4 + len;
        p += rawn;
        if (ver != 0xfe0d) continue;

        /* A structural error rejects the whole list; an entry we cannot use is skipped. */
        size_t q = 0;
        if (len < 1 + 2 + 2) return ECH_EPARSE;
        uint8_t id = c[q++];
        unsigned kem = be16(c + q); q += 2;
        size_t pkn = be16(c + q); q += 2;
        if (q + pkn + 2 > len) return ECH_EPARSE;
        const uint8_t *pk = c + q; q += pkn;
        size_t sn = be16(c + q); q += 2;
        if (q + sn + 1 + 1 > len || (sn & 3)) return ECH_EPARSE;
        const uint8_t *suites = c + q; q += sn;
        uint8_t maxn = c[q++];
        size_t pnn = c[q++];
        if (q + pnn + 2 > len) return ECH_EPARSE;
        const char *pn = (const char *)(c + q); q += pnn;
        size_t xn = be16(c + q); q += 2;
        if (q + xn != len) return ECH_EPARSE;
        int mandatory = 0;
        for (size_t x = 0; x < xn;) {
            if (x + 4 > xn) return ECH_EPARSE;
            unsigned xt = be16(c + q + x);
            size_t xl = be16(c + q + x + 2);
            if (x + 4 + xl > xn) return ECH_EPARSE;
            if (xt & 0x8000u) mandatory = 1;              /* a mandatory extension we do not know */
            x += 4 + xl;
        }
        if (mandatory || kem != 0x0020 || pkn != 32 || !name_ok(pn, pnn) || rawn > sizeof out->raw) continue;
        for (size_t s = 0; s < sn; s += 4) {
            unsigned kdf = be16(suites + s), aead = be16(suites + s + 2);
            if (kdf != 0x0001 || (aead != 0x0001 && aead != 0x0003)) continue;
            memset(out, 0, sizeof *out);
            out->config_id = id;
            out->kdf_id = (uint16_t)kdf;
            out->aead_id = (uint16_t)aead;
            memcpy(out->pk, pk, 32);
            out->max_name = maxn;
            memcpy(out->public_name, pn, pnn);
            memcpy(out->raw, raw, rawn);
            out->raw_n = rawn;
            return 0;
        }
    }
    return ECH_ENOCONFIG;
}

/* ---- HPKE (RFC 9180), base mode ------------------------------------------------------------ */

/* Buffer for "HPKE-v1" ‖ suite_id ‖ label ‖ ikm. The longest ikm is info:
 * "tls ech\0" ‖ ECHConfig, at most 1032 bytes. */
#define HB 1200

static int labeled_extract(const uint8_t *suite, size_t sn, const uint8_t *salt, size_t saltn,
                           const char *label, const uint8_t *ikm, size_t ikmn, uint8_t out[32]) {
    uint8_t b[HB];
    size_t ln = strlen(label), i = 0;
    if (7 + sn + ln + ikmn > sizeof b) return -1;
    memcpy(b, "HPKE-v1", 7); i = 7;
    memcpy(b + i, suite, sn); i += sn;
    memcpy(b + i, label, ln); i += ln;
    if (ikmn) memcpy(b + i, ikm, ikmn);
    i += ikmn;
    return sc_hkdf_extract(SC_SHA256, salt, saltn, b, i, out);
}

static int labeled_expand(const uint8_t prk[32], const uint8_t *suite, size_t sn, const char *label,
                          const uint8_t *info, size_t infon, size_t L, uint8_t *out) {
    uint8_t b[HB];
    size_t ln = strlen(label), i = 0;
    if (2 + 7 + sn + ln + infon > sizeof b) return -1;
    b[i++] = (uint8_t)(L >> 8);
    b[i++] = (uint8_t)L;
    memcpy(b + i, "HPKE-v1", 7); i += 7;
    memcpy(b + i, suite, sn); i += sn;
    memcpy(b + i, label, ln); i += ln;
    if (infon) memcpy(b + i, info, infon);
    i += infon;
    return sc_hkdf_expand(SC_SHA256, prk, 32, b, i, out, L);
}

int ech_hpke_seal(const struct ech_cfg *cfg, const uint8_t *eph,
                  const uint8_t *aad, size_t aad_n, const uint8_t *pt, size_t pt_n,
                  uint8_t enc[32], uint8_t *ct) {
    uint8_t sk[32], dh[32], prk[32], shared[32], secret[32], key[32], nonce[12];
    if (eph) memcpy(sk, eph, 32);
    else if (xc_random(sk, 32) != 0) return ECH_ECRYPTO;
    if (sc_x25519_base(enc, sk) != 0 || sc_x25519(dh, sk, cfg->pk) != 0) return ECH_ECRYPTO;

    /* DHKEM(X25519, HKDF-SHA256): suite_id = "KEM" ‖ kem_id. */
    static const uint8_t KEM[5] = { 'K', 'E', 'M', 0x00, 0x20 };
    uint8_t kctx[64];
    memcpy(kctx, enc, 32);
    memcpy(kctx + 32, cfg->pk, 32);
    if (labeled_extract(KEM, 5, NULL, 0, "eae_prk", dh, 32, prk) != 0 ||
        labeled_expand(prk, KEM, 5, "shared_secret", kctx, 64, 32, shared) != 0) return ECH_ECRYPTO;

    /* KeySchedule(base): suite_id = "HPKE" ‖ kem_id ‖ kdf_id ‖ aead_id. */
    uint8_t suite[10] = { 'H', 'P', 'K', 'E', 0x00, 0x20, (uint8_t)(cfg->kdf_id >> 8), (uint8_t)cfg->kdf_id,
                          (uint8_t)(cfg->aead_id >> 8), (uint8_t)cfg->aead_id };
    uint8_t info[8 + 1024];
    if (8 + cfg->raw_n > sizeof info) return ECH_ETOOBIG;
    memcpy(info, "tls ech", 7);
    info[7] = 0;
    memcpy(info + 8, cfg->raw, cfg->raw_n);
    uint8_t ksc[1 + 32 + 32];
    ksc[0] = 0;                                              /* mode: base */
    if (labeled_extract(suite, 10, NULL, 0, "psk_id_hash", NULL, 0, ksc + 1) != 0 ||
        labeled_extract(suite, 10, NULL, 0, "info_hash", info, 8 + cfg->raw_n, ksc + 33) != 0 ||
        labeled_extract(suite, 10, shared, 32, "secret", NULL, 0, secret) != 0) return ECH_ECRYPTO;
    enum sc_aead_alg alg = cfg->aead_id == 0x0003 ? SC_CHACHA20_POLY1305 : SC_AES128_GCM;
    size_t kn = sc_aead_key_len(alg);
    if (labeled_expand(secret, suite, 10, "key", ksc, sizeof ksc, kn, key) != 0 ||
        labeled_expand(secret, suite, 10, "base_nonce", ksc, sizeof ksc, 12, nonce) != 0) return ECH_ECRYPTO;

    /* The context's first message: seq = 0, so nonce = base_nonce. */
    struct sc_aead k;
    if (sc_aead_setkey(&k, alg, key) != 0) return ECH_ECRYPTO;
    if (pt_n) memcpy(ct, pt, pt_n);
    int rc = sc_aead_seal(&k, nonce, aad, aad_n, ct, pt_n, ct + pt_n);
    sc_aead_free(&k);
    memset(sk, 0, sizeof sk);
    memset(key, 0, sizeof key);
    return rc == 0 ? 0 : ECH_ECRYPTO;
}

/* ---- building Outer/Inner ----------------------------------------------------------------- */

struct wb {
    uint8_t *p;
    size_t n, cap;
    int bad;
};
static void w_put(struct wb *w, const void *d, size_t n) {
    if (w->bad || w->n + n > w->cap) { w->bad = 1; return; }
    if (n) memcpy(w->p + w->n, d, n);
    w->n += n;
}
static void w_u8(struct wb *w, unsigned v) { uint8_t b = (uint8_t)v; w_put(w, &b, 1); }
static void w_u16(struct wb *w, unsigned v) { uint8_t b[2] = { (uint8_t)(v >> 8), (uint8_t)v }; w_put(w, b, 2); }
static void w_u24(struct wb *w, size_t v) { uint8_t b[3] = { (uint8_t)(v >> 16), (uint8_t)(v >> 8), (uint8_t)v }; w_put(w, b, 3); }

#define EXT_ECH 0xfe0d

int ech_wrap(const struct ech_cfg *cfg, const uint8_t *hello, size_t hello_n,
             uint8_t *out, size_t cap, size_t *out_n, struct ech_state *st) {
    /* ---- parse the finished Hello ---- */
    if (hello_n < 5 + 4 + 35 || hello[0] != 0x16 || hello[5] != 0x01) return ECH_EPARSE;
    if ((size_t)be16(hello + 3) + 5 != hello_n || be24(hello + 6) + 9 != hello_n) return ECH_EPARSE;
    const uint8_t *b = hello + 9;
    size_t bn = hello_n - 9, p = 34;
    size_t sid_n = b[p];
    size_t sid_off = p + 1;
    p = sid_off + sid_n;
    if (p + 2 > bn) return ECH_EPARSE;
    p += 2 + be16(b + p);
    if (p + 1 > bn) return ECH_EPARSE;
    p += 1 + b[p];
    size_t pre_end = p;                                     /* the extensions length is here */
    if (p + 2 > bn) return ECH_EPARSE;
    size_t ext_raw_n = be16(b + p), ext_off = p + 2;
    if (ext_off + ext_raw_n != bn) return ECH_EPARSE;
    /* Copy the extensions without ECH: the Chrome-like Hello carries a GREASE ECH extension (0xfe0d
     * with a random payload), and it makes way for the real one, since two extensions of one type
     * are not allowed. */
    uint8_t ex[ECH_INNER_MAX], exi[ECH_INNER_MAX];         /* extensions of Outer and Inner */
    size_t ex_n = 0, exi_n = 0, sni_len = 0;
    int have_sni = 0;
    for (size_t q = ext_off; q + 4 <= bn;) {
        unsigned t = be16(b + q);
        size_t l = be16(b + q + 2);
        if (q + 4 + l > bn) return ECH_EPARSE;
        /* server_name data: list length(2), name type(1), name length(2), name. */
        if (t == 0 && l >= 5) { sni_len = be16(b + q + 5 + 2); have_sni = 1; }
        if (t != EXT_ECH) {
            if (ex_n + 4 + l > sizeof ex || exi_n + 4 + l > sizeof exi) return ECH_ETOOBIG;
            memcpy(ex + ex_n, b + q, 4 + l);
            ex_n += 4 + l;
            if (t == 0x002b && l >= 1) {
                /* Inner's supported_versions: TLS 1.3 and above only (RFC 9849, 6.1); Go and
                 * BoringSSL reject an Inner that offers 1.2. GREASE values stay as in the Hello. */
                size_t ln = b[q + 4], keep = 0;
                if (1 + ln != l || (ln & 1)) return ECH_EPARSE;
                exi[exi_n++] = b[q]; exi[exi_n++] = b[q + 1];       /* type */
                size_t len_at = exi_n;
                exi_n += 3;                                          /* both lengths: below */
                for (size_t v = 0; v < ln; v += 2) {
                    unsigned ver = be16(b + q + 5 + v);
                    int grease = (ver & 0x0F0F) == 0x0A0A && (ver & 0xFF) == (ver >> 8);
                    if (grease || ver >= 0x0304) { exi[exi_n++] = b[q + 5 + v]; exi[exi_n++] = b[q + 6 + v]; keep += 2; }
                }
                exi[len_at] = (uint8_t)((keep + 1) >> 8);
                exi[len_at + 1] = (uint8_t)(keep + 1);
                exi[len_at + 2] = (uint8_t)keep;
            } else {
                memcpy(exi + exi_n, b + q, 4 + l);
                exi_n += 4 + l;
            }
        }
        q += 4 + l;
    }
    if (!have_sni) return ECH_EPARSE;

    /* ---- Inner: the full message for the transcript (the Hello + an inner ECH extension) ---- */
    struct wb w = { st->inner, 0, sizeof st->inner, 0 };
    w_u8(&w, 0x01);
    w_u24(&w, pre_end + 2 + exi_n + 5);
    w_put(&w, b, pre_end);
    w_u16(&w, exi_n + 5);
    w_put(&w, exi, exi_n);
    w_u16(&w, EXT_ECH); w_u16(&w, 1); w_u8(&w, 1);          /* type 1: inner */
    if (w.bad) return ECH_ETOOBIG;
    st->inner_n = w.n;
    memcpy(st->random, b + 2, 32);

    /* ---- EncodedClientHelloInner: the same without legacy_session_id (the server restores it
     * from Outer), plus padding ---- */
    uint8_t enc_in[ECH_INNER_MAX + 64];
    struct wb e = { enc_in, 0, sizeof enc_in - 64, 0 };
    w_put(&e, b, 34);
    w_u8(&e, 0);
    w_put(&e, b + sid_off + sid_n, pre_end - (sid_off + sid_n));
    w_u16(&e, exi_n + 5);
    w_put(&e, exi, exi_n);
    w_u16(&e, EXT_ECH); w_u16(&e, 1); w_u8(&e, 1);
    if (e.bad) return ECH_ETOOBIG;
    /* Padding (RFC 9849, 6.1.3): the name up to maximum_name_length, then the whole up to a
     * multiple of 32. */
    size_t pad = cfg->max_name > sni_len ? cfg->max_name - sni_len : 0;
    pad += 31 - ((e.n + pad - 1) % 32);
    if (e.n + pad > sizeof enc_in) return ECH_ETOOBIG;
    memset(enc_in + e.n, 0, pad);
    size_t enc_n = e.n + pad, payload_n = enc_n + 16;

    /* ---- Outer ---- */
    uint8_t eph[32], enc_key[32];
    if (xc_random(eph, 32) != 0 || sc_x25519_base(enc_key, eph) != 0) return ECH_ECRYPTO;
    uint8_t orand[32];
    if (xc_random(orand, 32) != 0) return ECH_ECRYPTO;
    const size_t pn = strlen(cfg->public_name);
    const size_t ech_ext_n = 1 + 2 + 2 + 1 + 2 + 32 + 2 + payload_n;       /* extension data */
    /* Outer's extensions: the original ones, the old SNI replaced by the new one, plus ECH. */
    size_t old_sni_total = 0;
    for (size_t q = 0; q + 4 <= ex_n;) {
        size_t l = be16(ex + q + 2);
        if (be16(ex + q) == 0) old_sni_total = 4 + l;
        q += 4 + l;
    }
    const size_t new_sni_total = 4 + 2 + 1 + 2 + pn;
    const size_t o_ext_n = ex_n - old_sni_total + new_sni_total + 4 + ech_ext_n;
    const size_t o_body_n = 2 + 32 + 1 + sid_n + (pre_end - (sid_off + sid_n)) + 2 + o_ext_n;
    if (o_ext_n > 0xFFFF || 5 + 4 + o_body_n > cap) return ECH_ETOOBIG;

    struct wb o = { out, 0, cap, 0 };
    w_u8(&o, 0x16); w_u16(&o, 0x0301); w_u16(&o, 4 + o_body_n);
    w_u8(&o, 0x01); w_u24(&o, o_body_n);
    const size_t body0 = o.n;
    w_put(&o, b, 2);                                        /* version */
    w_put(&o, orand, 32);                                   /* Outer has its own random */
    w_u8(&o, sid_n);
    w_put(&o, b + sid_off, sid_n);                          /* the same session_id in both */
    w_put(&o, b + sid_off + sid_n, pre_end - (sid_off + sid_n));
    w_u16(&o, o_ext_n);
    for (size_t q = 0; q + 4 <= ex_n;) {
        size_t l = be16(ex + q + 2);
        if (be16(ex + q) == 0) {
            w_u16(&o, 0); w_u16(&o, 2 + 1 + 2 + pn);
            w_u16(&o, 1 + 2 + pn); w_u8(&o, 0); w_u16(&o, pn);
            w_put(&o, cfg->public_name, pn);
        } else {
            w_put(&o, ex + q, 4 + l);
        }
        q += 4 + l;
    }
    w_u16(&o, EXT_ECH); w_u16(&o, ech_ext_n);
    w_u8(&o, 0);                                            /* outer */
    w_u16(&o, cfg->kdf_id); w_u16(&o, cfg->aead_id);
    w_u8(&o, cfg->config_id);
    w_u16(&o, 32); w_put(&o, enc_key, 32);
    w_u16(&o, payload_n);
    const size_t payload_off = o.n;
    uint8_t zero[64] = { 0 };
    for (size_t z = payload_n; z > 0;) { size_t t = z < sizeof zero ? z : sizeof zero; w_put(&o, zero, t); z -= t; }
    if (o.bad || o.n != 5 + 4 + o_body_n) return ECH_ETOOBIG;

    /* AAD is Outer's message body with the payload zeroed; Inner is what gets sealed. */
    uint8_t ct[ECH_INNER_MAX + 64 + 16], enc_check[32];
    int rc = ech_hpke_seal(cfg, eph, out + body0, o_body_n, enc_in, enc_n, enc_check, ct);
    if (rc != 0) return rc;
    if (memcmp(enc_check, enc_key, 32) != 0) return ECH_ECRYPTO;
    memcpy(out + payload_off, ct, payload_n);
    *out_n = o.n;
    memset(eph, 0, sizeof eph);
    return 0;
}
