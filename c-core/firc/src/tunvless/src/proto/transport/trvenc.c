/* VLESS encryption (mlkem768x25519plus), client side: an X25519 + ML-KEM-768 handshake and AEAD
 * records over a ready transport. A byte-exact port of Xray-core proxy/vless/encryption
 * (client.go, common.go, xor.go).
 *
 * WHERE IT SITS. Between the transport (tcp/ws/grpc/xhttp... over none/tls/reality) and the
 * VLESS header: in Xray it is `conn = encryption.Handshake(conn)` before the request is written,
 * and the request, the response and the Vision frames all travel inside this layer's records.
 * Same here: transport_open calls tr_venc_open after opening the transport, and transport_write
 * and transport_read then go through this file. The transport itself knows nothing of
 * encryption.
 *
 * ON THE WIRE (1-RTT).
 *   client: iv(16) ‖ relay... ‖ AEAD(length 1232) ‖ AEAD(ML-KEM ek ‖ X25519 pub) ‖ padding;
 *   server: AEAD(ML-KEM ct ‖ X25519 pub) ‖ AEAD(ticket) ‖ AEAD(padding length) ‖ padding;
 * then records: 23 3 3 <length 2> ‖ AEAD(data, aad = header), up to 8192 data bytes per record.
 *
 * KEYS. The relay is a chain of "pre-shared" exchanges with keys of the server known in advance
 * (nfsKey); they give the handshake AEAD. A second, ephemeral exchange (pfsKey = ML-KEM ss ‖
 * X25519 ss) gives forward secrecy. The record keys are blake3.DeriveKey(context, pfsKey ‖
 * nfsKey), and each direction's context is the message that created it (the two differ in
 * length, so the keys differ). The record cipher is AES-256-GCM with hardware AES, otherwise
 * ChaCha20-Poly1305: the client chooses, the server tries both on the first message.
 *
 * MODES. xorpub masks the X25519 key and the ML-KEM ciphertext in the relay with an AES-CTR
 * keystream (key: DeriveKey("VLESS", relay key)). random also masks the five-byte record headers
 * both ways (Xray's XorConn): the stream looks random.
 *
 * 0-RTT. The server issues a ticket (16 bytes, the first two its lifetime in seconds); a client
 * that declared 0rtt resumes with it on the next connection without ML-KEM, sending iv ‖ relay ‖
 * AEAD(ticket) together with the first record. Tickets live in a process-wide table (key: hash of
 * the encryption string). An expired ticket, or one the server forgot, gets noise back: the
 * ticket is dropped and the connection fails with TR_EVENC0RTT (the next one does a full
 * handshake), as in Xray ("new handshake needed").
 *
 * DOES NOT BLOCK THE LOOP. The handshake runs in a connector thread and waits with poll; records
 * are read in the tunnel loop without waiting: no whole record yet means "0 bytes" (a valid
 * outcome, as with HTTP/2).
 *
 * WRITE ROLLBACK. A transport over HTTP/2 may return H2_EWINDOW ("window closed, nothing sent"),
 * and the caller repeats THE SAME bytes. So the write state (nonce counter, header keystream)
 * advances only after a successful send; otherwise the retry would carry a wrong nonce and the
 * server would close the stream. */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <time.h>
#include <poll.h>
#include <pthread.h>

#include "transport.h"
#include "reality.h"
#include "vencp.h"
#include "tscratch.h"

#define REC_MAX_DATA 8192              /* data per sent record (Xray: no extra copy at the peer) */
#define REC_MAX_LEN  16640             /* max record body length accepted on receive */
#define REC_MIN_LEN  17
#define PROTO_MAX_CHUNK 65535          /* max 16-bit chunk length in SS-2022 / VMess / AnyTLS */
/* Raw input: a whole record of the largest accepted size plus room for one transport read,
 * which needs TRANSPORT_MIN_READ_CAP free (raw_fill). */
#define RBUF_CAP     (32 + PROTO_MAX_CHUNK + (16384 + 16) + 32)

static const unsigned char MAX_NONCE[12] = { 255,255,255,255,255,255,255,255,255,255,255,255 };

/* The reason of the last failure as text (per thread), like tls13_verify_reason: one code covers
 * a class, but the user needs words ("server answered with noise" and "wrong relay key" call for
 * different fixes). */
static __thread char g_reason[128];
const char *tr_venc_reason(void) { return g_reason; }
static int fail(int rc, const char *why) {
    snprintf(g_reason, sizeof g_reason, "%s", why);
    return rc;
}

/* ---- AEAD and keystream -------------------------------------------------------------------- */

struct v_aead {
    struct sc_aead k;
    unsigned char nonce[12];       /* the last one used (like Xray's AEAD.Nonce) */
    int ok;
};

static void nonce_inc(unsigned char n[12]) {
    for (int i = 11; i >= 0; i--)
        if (++n[i] != 0) break;
}

static void va_free(struct v_aead *a) {
    if (a->ok) sc_aead_free(&a->k);
    a->ok = 0;
}

/* NewAEAD(ctx, key, useAES): the key is blake3.DeriveKey(k, string(ctx), key). */
static int va_init(struct v_aead *a, const void *ctx, size_t ctx_n, const void *key, size_t key_n,
                   int use_aes) {
    unsigned char k[32];
    va_free(a);
    sc_blake3_derive_key(k, 32, ctx, ctx_n, key, key_n);
    memset(a->nonce, 0, 12);
    int rc = sc_aead_setkey(&a->k, use_aes ? SC_AES256_GCM : SC_CHACHA20_POLY1305, k);
    memset(k, 0, sizeof k);
    a->ok = rc == 0;
    return rc;
}

/* Seal/Open like Xray's AEAD wrapper: with nonce == NULL the counter grows BEFORE use, so the
 * first record goes with nonce 1; Xray passes MaxNonce explicitly (the server's relay answer). */
static int va_seal(struct v_aead *a, const unsigned char *fixed, const void *aad, size_t aad_n,
                   unsigned char *buf, size_t n, unsigned char tag[16]) {
    unsigned char nn[12];
    if (fixed) memcpy(nn, fixed, 12); else { nonce_inc(a->nonce); memcpy(nn, a->nonce, 12); }
    return sc_aead_seal(&a->k, nn, aad, aad_n, buf, n, tag);
}
static int va_open(struct v_aead *a, const unsigned char *fixed, const void *aad, size_t aad_n,
                   unsigned char *buf, size_t n, const unsigned char tag[16]) {
    unsigned char nn[12];
    if (fixed) memcpy(nn, fixed, 12); else { nonce_inc(a->nonce); memcpy(nn, a->nonce, 12); }
    return sc_aead_open(&a->k, nn, aad, aad_n, buf, n, tag);
}

/* NewCTR(key, iv): AES-256-CTR, the key is blake3.DeriveKey(k, "VLESS", key) ("so the key is not
 * used as is"), iv is the whole initial counter block. */
static int ctr_new(struct sc_aesctr *c, const void *key, size_t key_n, const unsigned char iv[16]) {
    unsigned char k[32];
    sc_blake3_derive_key(k, 32, "VLESS", 5, key, key_n);
    int rc = sc_aesctr_init(c, k, iv);
    memset(k, 0, sizeof k);
    return rc;
}

/* ---- 0-RTT tickets ------------------------------------------------------------------------- */

/* A process-wide table: connections to one node share one ticket. Key: blake3 of the encryption
 * string (the string itself, which holds keys, is not stored). Few entries: few nodes in a
 * subscription use 0-RTT. */
#define TICKETS 16
static struct ticket {
    int used;
    unsigned char id[32];
    time_t expire;
    unsigned char pfs[64];
    unsigned char ticket[16];
} g_tk[TICKETS];
static pthread_mutex_t g_tk_mu = PTHREAD_MUTEX_INITIALIZER;
static unsigned g_tk_next;

static int tk_get(const unsigned char id[32], unsigned char pfs[64], unsigned char tk[16]) {
    int ok = 0;
    pthread_mutex_lock(&g_tk_mu);
    for (int i = 0; i < TICKETS; i++)
        if (g_tk[i].used && !memcmp(g_tk[i].id, id, 32)) {
            if (time(NULL) < g_tk[i].expire) {
                memcpy(pfs, g_tk[i].pfs, 64);
                memcpy(tk, g_tk[i].ticket, 16);
                ok = 1;
            }
            break;
        }
    pthread_mutex_unlock(&g_tk_mu);
    return ok;
}

static void tk_put(const unsigned char id[32], const unsigned char pfs[64], const unsigned char tk[16],
                   unsigned seconds) {
    pthread_mutex_lock(&g_tk_mu);
    int slot = -1;
    for (int i = 0; i < TICKETS; i++)
        if (g_tk[i].used && !memcmp(g_tk[i].id, id, 32)) { slot = i; break; }
    if (slot < 0) {
        for (int i = 0; i < TICKETS; i++) if (!g_tk[i].used) { slot = i; break; }
        if (slot < 0) slot = (int)(g_tk_next++ % TICKETS);
    }
    g_tk[slot].used = 1;
    memcpy(g_tk[slot].id, id, 32);
    g_tk[slot].expire = time(NULL) + (time_t)seconds;
    memcpy(g_tk[slot].pfs, pfs, 64);
    memcpy(g_tk[slot].ticket, tk, 16);
    pthread_mutex_unlock(&g_tk_mu);
}

/* The server rejected the ticket: expire it only if it is still the one we used (another
 * connection may have got a new one by now). */
static void tk_expire(const unsigned char id[32], const unsigned char pfs[64]) {
    pthread_mutex_lock(&g_tk_mu);
    for (int i = 0; i < TICKETS; i++)
        if (g_tk[i].used && !memcmp(g_tk[i].id, id, 32) && !memcmp(g_tk[i].pfs, pfs, 64))
            g_tk[i].expire = 0;
    pthread_mutex_unlock(&g_tk_mu);
}

/* ---- connection state ---------------------------------------------------------------------- */

enum {
    RS_PAD = 0, RS_RAND, RS_REC,
    RS_SS_SALT, RS_SS2022_HDR, RS_SS_PAYLOAD, RS_SS_REC,
    RS_VMESS_HDR_LEN, RS_VMESS_HDR, RS_VMESS_REC,
    RS_MIERU_META, RS_MIERU_PAYLOAD,
    RS_ANYTLS_FRAME
};

struct venc {
    int use_aes, xor2, zero_rtt_try;
    int ss_mode;                           /* 0: VLESS enc, 1: SS AEAD, 2: SS 2022, 3: VMess AEAD, 4: Mieru TCP, 5: AnyTLS */
    enum sc_aead_alg ss_alg;
    size_t ss_klen;
    unsigned char ss_psk[32];
    unsigned char ss_tx_salt[32];
    int ss_first_wr;
    /* VMess AEAD state (ss_mode == 3). */
    unsigned char vm_cmd_key[16];
    unsigned char vm_req_iv[16], vm_req_key[16];
    unsigned char vm_resp_iv[16], vm_resp_key[16];
    unsigned char vm_resp_hdr;
    unsigned char vm_sec;                  /* 3: AES-128-GCM, 4: ChaCha20-Poly1305, 5: None */
    uint8_t vm_is_udp;
    uint32_t vm_tx_count, vm_rx_count;
    /* Mieru TCP state (ss_mode == 4). */
    unsigned char mieru_key[32];
    unsigned char mieru_tx_nonce[24], mieru_rx_nonce[24];
    uint32_t mieru_sid, mieru_tx_seq, mieru_rx_seq;
    int mieru_tx_first, mieru_rx_first, mieru_closing;
    uint16_t mieru_pay_len;
    uint8_t mieru_pre_len, mieru_suf_len;
    unsigned char united[96];
    struct v_aead wr, rd;
    unsigned char tk_id[32];               /* ticket table key: blake3 of the encryption string */
    unsigned char tk_pfs[64];              /* pfsKey of the ticket in use (for tk_expire) */
    /* Receive. */
    int rs;                                /* RS_PAD, RS_RAND, RS_REC, RS_SS_* */
    size_t peer_pad;                       /* RS_PAD: server padding length with the tag */
    unsigned char *padbuf;                 /* padding is collected whole: AEAD checks it */
    size_t padbuf_n;
    unsigned char *rbuf;                   /* raw input */
    size_t rlen, rcap;
    size_t pl_off, pl_len;                 /* decrypted, not yet returned (inside rbuf) */
    size_t pl_rec;                         /* bytes of rbuf taken by this plaintext's record */
    int hdr_done;                          /* the current record's header is unmasked */
    int first_rec;                         /* 0-RTT: the first record not yet received */
    /* random mode keystream (XorConn): the next record's header mask is computed ahead, so a
     * write rollback does not move the keystream. */
    struct sc_aesctr out_ctr, in_ctr;
    int have_out_ctr, have_in_ctr;
    unsigned char out_mask[5];
    /* Send. */
    unsigned char *prewrite;               /* 0-RTT: iv ‖ relay ‖ ticket, sent with record 1 */
    size_t prewrite_n;
};

static void enc_free(struct venc *e) {
    if (!e) return;
    va_free(&e->wr);
    va_free(&e->rd);
    if (e->have_out_ctr) sc_aesctr_free(&e->out_ctr);
    if (e->have_in_ctr) sc_aesctr_free(&e->in_ctr);
    memset(e->united, 0, sizeof e->united);
    memset(e->ss_psk, 0, sizeof e->ss_psk);
    free(e->rbuf);
    free(e->padbuf);
    free(e->prewrite);
    free(e);
}

static void out_mask_next(struct venc *e) {
    if (!e->xor2) return;
    static const unsigned char z[5] = {0};
    sc_aesctr_xor(&e->out_ctr, z, e->out_mask, 5);
}

/* ---- raw transport I/O ---------------------------------------------------------------------- */

static int64_t now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* Read raw input into rbuf until it holds need bytes. block != 0 (handshake): wait with poll up
 * to deadline. Otherwise 0 means "not enough yet" (e->rlen < need); negative: transport failure. */
static int raw_fill(struct transport *t, struct venc *e, size_t need, int block, int64_t deadline) {
    while (e->rlen < need) {
        if (e->rcap - e->rlen < TRANSPORT_MIN_READ_CAP) return TR_EVENC;   /* need too big: a bug */
        size_t got = 0;
        int rc = t->fr->read(t, e->rbuf + e->rlen, e->rcap - e->rlen, &got);
        if (rc) return rc;
        e->rlen += got;
        if (got) continue;
        if (!block) return 0;
        if (transport_has_data(t)) continue;
        int64_t left = deadline - now_ms();
        if (left <= 0) return fail(TR_EVENC, "the server did not answer the handshake");
        struct pollfd p = { .fd = transport_fd(t), .events = POLLIN };
        int pr = poll(&p, 1, left > 1000 ? 1000 : (int)left);
        if (pr < 0 && errno != EINTR) return TR_EIO;
    }
    return 0;
}

/* Unmask the next record's header as soon as its five bytes are in the input: right after the
 * previous record is returned, not at the next read. tr_venc_pending must know whether a WHOLE
 * record is in the input, and a masked header has no readable length. Otherwise a record that
 * arrived in one segment with the previous one would sit untouched while the socket is silent,
 * i.e. forever (found in runs against Xray: one 1 MB exchange in ten stalled at the end). */
static void hdr_prepare(struct venc *e) {
    if (e->hdr_done || e->rs != RS_REC || e->rlen < 5) return;
    if (e->xor2) sc_aesctr_xor(&e->in_ctr, e->rbuf, e->rbuf, 5);
    e->hdr_done = 1;
}

static void rb_drop(struct venc *e, size_t n) {
    if (n >= e->rlen) { e->rlen = 0; return; }
    memmove(e->rbuf, e->rbuf + n, e->rlen - n);
    e->rlen -= n;
}

static void sleep_ms(unsigned ms) {
    if (!ms) return;
    struct timespec ts = { .tv_sec = ms / 1000, .tv_nsec = (long)(ms % 1000) * 1000000L };
    nanosleep(&ts, NULL);
}

/* crypto.RandBetween(from, to): [from, to); a difference of 0 or 1 gives from. */
static int64_t rand_between(int64_t from, int64_t to) {
    if (from > to) { int64_t x = from; from = to; to = x; }
    int64_t d = to - from;
    if (d == 0 || d == 1) return from;
    uint64_t r = 0;
    if (xc_random((unsigned char *)&r, sizeof r) != 0) return from;
    return from + (int64_t)(r % (uint64_t)d);
}

/* ---- handshake ------------------------------------------------------------------------------ */

static const uint16_t DEF_LENS[2][3] = { { 100, 111, 1111 }, { 50, 0, 3333 } };
static const uint16_t DEF_GAPS[1][3] = { { 75, 0, 111 } };

/* CreatPadding: padding chunk lengths and the pauses between writes. */
static size_t make_padding(const struct venc_cfg *c, size_t lens[VENC_MAX_PAD], unsigned gaps[VENC_MAX_PAD],
                           size_t *nl, size_t *ng) {
    const uint16_t (*pl)[3] = c->npad_lens ? (const uint16_t (*)[3])c->lens : DEF_LENS;
    size_t npl = c->npad_lens ? c->npad_lens : 2;
    const uint16_t (*pg)[3] = c->npad_lens ? (const uint16_t (*)[3])c->gaps : DEF_GAPS;
    size_t npg = c->npad_lens ? c->npad_gaps : 1;
    size_t total = 0;
    for (size_t i = 0; i < npl; i++) {
        size_t l = 0;
        if (pl[i][0] >= rand_between(0, 100)) l = (size_t)rand_between(pl[i][1], pl[i][2]);
        lens[i] = l;
        total += l;
    }
    for (size_t i = 0; i < npg; i++) {
        unsigned g = 0;
        if (pg[i][0] >= rand_between(0, 100)) g = (unsigned)rand_between(pg[i][1], pg[i][2]);
        gaps[i] = g;
    }
    *nl = npl;
    *ng = npg;
    return total;
}

static int b64url_key(const char *s, size_t n, unsigned char *out, size_t cap) {
    char tmp[1700];
    if (n >= sizeof tmp) return -1;
    memcpy(tmp, s, n);
    tmp[n] = '\0';
    return xc_b64url_decode(tmp, out, cap);
}

static void ss_nonce_inc(unsigned char n[12]) {
    for (int i = 0; i < 12; i++)
        if (++n[i] != 0) break;
}

static int ss_derive_subkey(const struct venc *e, const unsigned char *salt, unsigned char *subkey) {
    if (e->ss_mode == 2) {
        unsigned char mat[64];
        memcpy(mat, e->ss_psk, e->ss_klen);
        memcpy(mat + e->ss_klen, salt, e->ss_klen);
        sc_blake3_derive_key(subkey, e->ss_klen, "shadowsocks 2022 session subkey", 31, mat, e->ss_klen * 2);
        memset(mat, 0, sizeof(mat));
        return 0;
    }
    return sc_hkdf(SC_SHA1, salt, e->ss_klen, e->ss_psk, e->ss_klen, "ss-subkey", 9, subkey, e->ss_klen);
}

static int ss_open(struct transport *t, const struct tr_node *node) {
    const char *u = node->uuid ? node->uuid : "";
    const char *colon = strchr(u, ':');
    if (!colon) return fail(TR_EVENC, "ss credentials must be method:password");
    size_t mlen = (size_t)(colon - u);
    const char *pass = colon + 1;

    int ss_mode = 0;
    enum sc_aead_alg alg = SC_AES256_GCM;
    size_t klen = 32;
    if (mlen == 11 && !strncmp(u, "aes-128-gcm", 11)) {
        ss_mode = 1; alg = SC_AES128_GCM; klen = 16;
    } else if (mlen == 11 && !strncmp(u, "aes-256-gcm", 11)) {
        ss_mode = 1; alg = SC_AES256_GCM; klen = 32;
    } else if ((mlen == 22 && !strncmp(u, "chacha20-ietf-poly1305", 22)) ||
               (mlen == 17 && !strncmp(u, "chacha20-poly1305", 17))) {
        ss_mode = 1; alg = SC_CHACHA20_POLY1305; klen = 32;
    } else if (mlen == 23 && !strncmp(u, "2022-blake3-aes-128-gcm", 23)) {
        ss_mode = 2; alg = SC_AES128_GCM; klen = 16;
    } else if (mlen == 23 && !strncmp(u, "2022-blake3-aes-256-gcm", 23)) {
        ss_mode = 2; alg = SC_AES256_GCM; klen = 32;
    } else if (mlen == 29 && !strncmp(u, "2022-blake3-chacha20-poly1305", 29)) {
        ss_mode = 2; alg = SC_CHACHA20_POLY1305; klen = 32;
    } else {
        return fail(TR_EVENC, "unsupported Shadowsocks cipher");
    }

    struct venc *e = calloc(1, sizeof(*e));
    if (!e) return TR_EIO;
    e->rcap = RBUF_CAP;
    e->rbuf = malloc(e->rcap);
    if (!e->rbuf) { enc_free(e); return TR_EIO; }
    e->ss_mode = ss_mode;
    e->ss_alg = alg;
    e->ss_klen = klen;

    if (ss_mode == 2) {
        int dec = b64url_key(pass, strlen(pass), e->ss_psk, sizeof(e->ss_psk));
        if (dec != (int)klen) { enc_free(e); return fail(TR_EVENC, "invalid SS-2022 base64 PSK length"); }
    } else {
        size_t plen = strlen(pass), gen = 0;
        unsigned char prev[16];
        while (gen < klen) {
            struct sc_hash_ctx hc;
            if (sc_hash_init(&hc, SC_MD5) != 0) { enc_free(e); return TR_EIO; }
            if (gen > 0) sc_hash_update(&hc, prev, 16);
            if (plen > 0) sc_hash_update(&hc, pass, plen);
            sc_hash_final(&hc, prev);
            sc_hash_free(&hc);
            size_t cp = klen - gen < 16 ? klen - gen : 16;
            memcpy(e->ss_psk + gen, prev, cp);
            gen += cp;
        }
    }

    if (xc_random(e->ss_tx_salt, klen) != 0) { enc_free(e); return TR_EIO; }
    unsigned char subkey[32];
    if (ss_derive_subkey(e, e->ss_tx_salt, subkey) != 0 ||
        sc_aead_setkey(&e->wr.k, alg, subkey) != 0) {
        memset(subkey, 0, sizeof(subkey));
        enc_free(e);
        return TR_EIO;
    }
    memset(subkey, 0, sizeof(subkey));
    e->wr.ok = 1;
    memset(e->wr.nonce, 0, 12);
    e->prewrite = malloc(klen);
    if (!e->prewrite) { enc_free(e); return TR_EIO; }
    memcpy(e->prewrite, e->ss_tx_salt, klen);
    e->prewrite_n = klen;
    e->ss_first_wr = (ss_mode == 2 ? 1 : 0);
    e->rs = RS_SS_SALT;
    t->enc = e;
    return 0;
}

/* ---- VMess AEAD (ss_mode == 3) and Mieru v3 TCP (ss_mode == 4) ----------------------------- */

static int hex_nibble(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static int parse_uuid16(const char *s, unsigned char out[16]) {
    if (!s) return -1;
    int bi = 0;
    for (int i = 0; s[i] && bi < 16; ) {
        if (s[i] == '-') { i++; continue; }
        int hi = hex_nibble(s[i]);
        int lo = s[i + 1] ? hex_nibble(s[i + 1]) : -1;
        if (hi < 0 || lo < 0) return -1;
        out[bi++] = (unsigned char)((hi << 4) | lo);
        i += 2;
    }
    return bi == 16 ? 0 : -1;
}

static uint32_t crc32_ieee(const unsigned char *d, size_t n) {
    uint32_t c = 0xFFFFFFFFu;
    for (size_t i = 0; i < n; i++) {
        c ^= d[i];
        for (int b = 0; b < 8; b++)
            c = (c >> 1) ^ (0xEDB88320u & (-(c & 1u)));
    }
    return ~c;
}

static uint32_t fnv1a32(const unsigned char *d, size_t n) {
    uint32_t h = 0x811c9dc5u;
    for (size_t i = 0; i < n; i++) {
        h ^= d[i];
        h *= 0x01000193u;
    }
    return h;
}

/* VMess AEAD nested HMAC-SHA256 KDF (xray-core/proxy/vmess/aead/kdf.go). */
static void vmess_kdf_hash(int depth,
                           const void *p0, size_t n0,
                           const void *p1, size_t n1,
                           const void *p2, size_t n2,
                           const unsigned char *msg, size_t msg_n,
                           unsigned char out[32]) {
    if (depth <= 0) {
        sc_hash(SC_SHA256, msg, msg_n, out);
        return;
    }
    const unsigned char *k = (const unsigned char *)"VMess AEAD KDF";
    size_t kn = 14;
    if (depth == 2) { k = p0; kn = n0; }
    else if (depth == 3) { k = p1; kn = n1; }
    else if (depth == 4) { k = p2; kn = n2; }

    unsigned char ipad[64], opad[64], inner_in[384], inner_h[32], outer_in[96];
    memset(ipad, 0x36, 64);
    memset(opad, 0x5c, 64);
    for (size_t i = 0; i < kn && i < 64; i++) {
        ipad[i] ^= k[i];
        opad[i] ^= k[i];
    }
    memcpy(inner_in, ipad, 64);
    if (msg_n > sizeof(inner_in) - 64) msg_n = sizeof(inner_in) - 64;
    memcpy(inner_in + 64, msg, msg_n);
    vmess_kdf_hash(depth - 1, p0, n0, p1, n1, p2, n2, inner_in, 64 + msg_n, inner_h);

    memcpy(outer_in, opad, 64);
    memcpy(outer_in + 64, inner_h, 32);
    vmess_kdf_hash(depth - 1, p0, n0, p1, n1, p2, n2, outer_in, 96, out);
}

static void vmess_kdf(const unsigned char key[16], int np,
                      const void *p0, size_t n0,
                      const void *p1, size_t n1,
                      const void *p2, size_t n2,
                      unsigned char out[32]) {
    vmess_kdf_hash(np + 1, p0, n0, p1, n1, p2, n2, key, 16, out);
}

static int vmess_open(struct transport *t, const struct tr_node *node) {
    const char *u = node->uuid ? node->uuid : "";
    const char *colon = strchr(u, ':');
    const char *uuid_str = colon ? colon + 1 : u;
    char sec[32] = "auto";
    if (colon && (size_t)(colon - u) < sizeof(sec)) {
        memcpy(sec, u, (size_t)(colon - u));
        sec[colon - u] = '\0';
    } else if (node->encryption && node->encryption[0] && strcmp(node->encryption, "none") != 0) {
        snprintf(sec, sizeof(sec), "%s", node->encryption);
    }

    unsigned char user[16];
    if (parse_uuid16(uuid_str, user) != 0)
        return fail(TR_EVENC, "invalid VMess UUID");

    struct venc *e = calloc(1, sizeof(*e));
    if (!e) return TR_EIO;
    e->rcap = RBUF_CAP;
    e->rbuf = malloc(e->rcap);
    if (!e->rbuf) { enc_free(e); return TR_EIO; }
    e->ss_mode = 3;

    /* cmdKey = MD5(UUID || "c48619fe-8f02-49e0-b9e9-edf763e17e21") */
    static const char vmess_magic[] = "c48619fe-8f02-49e0-b9e9-edf763e17e21";
    unsigned char cmd_in[16 + 36];
    memcpy(cmd_in, user, 16);
    memcpy(cmd_in + 16, vmess_magic, 36);
    sc_hash(SC_MD5, cmd_in, sizeof(cmd_in), e->vm_cmd_key);

    if (!strcasecmp(sec, "chacha20-poly1305") || !strcasecmp(sec, "chacha20-ietf-poly1305"))
        e->vm_sec = 4;
    else if (!strcasecmp(sec, "none") || !strcasecmp(sec, "zero"))
        e->vm_sec = 5;
    else
        e->vm_sec = 3; /* AES-128-GCM */

    if (xc_random(e->vm_req_iv, 16) != 0 ||
        xc_random(e->vm_req_key, 16) != 0 ||
        xc_random(&e->vm_resp_hdr, 1) != 0) {
        enc_free(e);
        return TR_EIO;
    }
    unsigned char h32[32];
    sc_hash(SC_SHA256, e->vm_req_iv, 16, h32);
    memcpy(e->vm_resp_iv, h32, 16);
    sc_hash(SC_SHA256, e->vm_req_key, 16, h32);
    memcpy(e->vm_resp_key, h32, 16);

    if (e->vm_sec == 3) {
        if (sc_aead_setkey(&e->wr.k, SC_AES128_GCM, e->vm_req_key) != 0 ||
            sc_aead_setkey(&e->rd.k, SC_AES128_GCM, e->vm_resp_key) != 0) {
            enc_free(e);
            return TR_EIO;
        }
        e->wr.ok = e->rd.ok = 1;
    } else if (e->vm_sec == 4) {
        unsigned char k_wr[32], k_rd[32];
        sc_hash(SC_MD5, e->vm_req_key, 16, k_wr);
        sc_hash(SC_MD5, k_wr, 16, k_wr + 16);
        sc_hash(SC_MD5, e->vm_resp_key, 16, k_rd);
        sc_hash(SC_MD5, k_rd, 16, k_rd + 16);
        if (sc_aead_setkey(&e->wr.k, SC_CHACHA20_POLY1305, k_wr) != 0 ||
            sc_aead_setkey(&e->rd.k, SC_CHACHA20_POLY1305, k_rd) != 0) {
            enc_free(e);
            return TR_EIO;
        }
        e->wr.ok = e->rd.ok = 1;
    }

    e->vm_tx_count = 0;
    e->vm_rx_count = 0;
    e->ss_first_wr = 1;
    e->rs = RS_VMESS_HDR_LEN;
    t->enc = e;
    return 0;
}

static void mieru_nonce_inc(unsigned char n[24]) {
    for (int i = 23; i >= 0; i--)
        if (++n[i] != 0) break;
}

static int mieru_open(struct transport *t, const struct tr_node *node) {
    const char *u = node->uuid ? node->uuid : "";
    const char *colon = strchr(u, ':');
    if (!colon || colon == u || !colon[1])
        return fail(TR_EVENC, "Mieru credentials must be username:password");
    size_t ulen = (size_t)(colon - u);
    const char *pass = colon + 1;
    size_t plen = strlen(pass);

    struct venc *e = calloc(1, sizeof(*e));
    if (!e) return TR_EIO;
    e->rcap = RBUF_CAP;
    e->rbuf = malloc(e->rcap);
    if (!e->rbuf) { enc_free(e); return TR_EIO; }
    e->ss_mode = 4;

    /* hashedPass = SHA256(password || 0x00 || username) */
    unsigned char hashed_pass[32];
    struct sc_hash_ctx hc;
    if (sc_hash_init(&hc, SC_SHA256) != 0) { enc_free(e); return TR_EIO; }
    sc_hash_update(&hc, pass, plen);
    unsigned char z = 0;
    sc_hash_update(&hc, &z, 1);
    sc_hash_update(&hc, u, ulen);
    sc_hash_final(&hc, hashed_pass);
    sc_hash_free(&hc);

    /* 2-minute rounded time salt (keygen.go). */
    int64_t now_s = (int64_t)time(NULL);
    int64_t rem = (now_s + 60) % 120;
    if (rem < 0) rem += 120;
    uint64_t rounded = (uint64_t)((now_s + 60) - rem);
    unsigned char tb[8], salt[32];
    for (int i = 0; i < 8; i++) tb[i] = (unsigned char)(rounded >> (56 - 8 * i));
    sc_hash(SC_SHA256, tb, 8, salt);

    if (sc_pbkdf2_sha256(hashed_pass, 32, salt, 32, 64, e->mieru_key, 32) != 0) {
        enc_free(e);
        return TR_EIO;
    }

    /* Initial 24-byte TX nonce with 4-byte user hint at [20..23]: SHA256(user || nonce[0..15])[0..3]. */
    if (xc_random(e->mieru_tx_nonce, 24) != 0 ||
        xc_random((unsigned char *)&e->mieru_sid, 4) != 0) {
        enc_free(e);
        return TR_EIO;
    }
    if (e->mieru_sid == 0) e->mieru_sid = 1;
    unsigned char hint[32];
    if (sc_hash_init(&hc, SC_SHA256) != 0) { enc_free(e); return TR_EIO; }
    sc_hash_update(&hc, u, ulen);
    sc_hash_update(&hc, e->mieru_tx_nonce, 16);
    sc_hash_final(&hc, hint);
    sc_hash_free(&hc);
    memcpy(e->mieru_tx_nonce + 20, hint, 4);

    e->mieru_tx_first = 1;
    e->mieru_rx_first = 1;
    e->mieru_tx_seq = 0;
    e->mieru_rx_seq = 0;
    e->rs = RS_MIERU_META;
    t->enc = e;
    return 0;
}

static int anytls_open(struct transport *t, const struct tr_node *node) {
    const char *pass = node->uuid ? node->uuid : "";
    size_t plen = strlen(pass);
    if (!plen) return fail(TR_EVENC, "AnyTLS password is empty");

    struct venc *e = calloc(1, sizeof(*e));
    if (!e) return TR_EIO;
    e->rcap = RBUF_CAP;
    e->rbuf = malloc(e->rcap);
    if (!e->rbuf) { enc_free(e); return TR_EIO; }
    e->ss_mode = 5;

    static const char settings[] = "v=2\nclient=tunvless\npadding-md5=04cfa2939c724322dc67282362a8f718";
    const size_t slen = sizeof(settings) - 1;
    e->prewrite_n = 34 + 7 + slen + 7;
    e->prewrite = calloc(1, e->prewrite_n);
    if (!e->prewrite) { enc_free(e); return TR_EIO; }
    sc_hash(SC_SHA256, pass, plen, e->prewrite);
    e->prewrite[32] = 0;
    e->prewrite[33] = 0;
    size_t off = 34;
    e->prewrite[off] = 4; /* cmdSettings */
    e->prewrite[off + 5] = (unsigned char)(slen >> 8);
    e->prewrite[off + 6] = (unsigned char)slen;
    memcpy(e->prewrite + off + 7, settings, slen);
    off += 7 + slen;
    e->prewrite[off] = 1; /* cmdSyn */
    e->prewrite[off + 4] = 1; /* sid = 1 */
    e->rs = RS_ANYTLS_FRAME;
    t->enc = e;
    return 0;
}

int tr_venc_open(struct transport *t, const struct tr_node *node, int timeout_s) {
    if (node->proto && !strcmp(node->proto, "ss")) return ss_open(t, node);
    if (node->proto && !strcmp(node->proto, "vmess")) return vmess_open(t, node);
    if (node->proto && !strcmp(node->proto, "mieru")) return mieru_open(t, node);
    if (node->proto && !strcmp(node->proto, "anytls")) return anytls_open(t, node);
    struct venc_cfg c;
    const char *why = "";
    if (vencp_parse(node->encryption, &c, &why) != 0) return fail(TR_EVENC, why);

    struct venc *e = calloc(1, sizeof(*e));
    unsigned char (*keys)[1184] = malloc(sizeof(unsigned char[VENC_MAX_KEYS][1184]));
    unsigned char *hello = NULL;
    int rc = TR_EVENC;
    unsigned char nfs[32], iv[16];
    struct v_aead nfs_a;
    memset(&nfs_a, 0, sizeof nfs_a);
    unsigned char mlk_dk[SC_MLKEM768_DK], xpriv[32], xpub[32];
    struct sc_aesctr tmpctr, last;
    int have_tmp = 0, have_last = 0;
    memset(&tmpctr, 0, sizeof tmpctr);
    memset(&last, 0, sizeof last);
    if (!e || !keys) { rc = TR_EIO; goto out; }
    e->rcap = RBUF_CAP;
    e->rbuf = malloc(e->rcap);
    if (!e->rbuf) { rc = TR_EIO; goto out; }
    e->use_aes = xc_cpu_has_aes();
    e->xor2 = c.xor_mode == 2;
    int64_t deadline = now_ms() + (timeout_s > 0 ? timeout_s : 10) * 1000;

    /* Relay keys and their hashes. */
    size_t klen[VENC_MAX_KEYS], relays_len = 0;
    unsigned char h32[VENC_MAX_KEYS][32];
    for (unsigned j = 0; j < c.nkeys; j++) {
        int kl = b64url_key(c.key[j], c.key_len[j], keys[j], 1184);
        if (kl != 32 && kl != 1184) { rc = fail(TR_EVENC, "a key in the encryption string does not parse"); goto out; }
        klen[j] = (size_t)kl;
        sc_blake3_hash(h32[j], keys[j], klen[j]);
        relays_len += kl == 32 ? 32 + 32 : 1088 + 32;
    }
    relays_len -= 32;
    const size_t head_n = 16 + relays_len;

    /* Ticket id: the hash of the whole string. */
    sc_blake3_hash(e->tk_id, node->encryption, strlen(node->encryption));
    unsigned char t_pfs[64], t_tk[16] = {0};
    const int zrtt = c.zero_rtt && tk_get(e->tk_id, t_pfs, t_tk);
    if (getenv("STEER_VENC_TRACE"))
        fprintf(stderr, "tunvless[debug] venc: %s, mode %s, %u keys, cipher %s\n",
                zrtt ? "0-RTT with ticket" : "full handshake",
                c.xor_mode == 2 ? "random" : c.xor_mode == 1 ? "xorpub" : "native", c.nkeys,
                e->use_aes ? "AES-256-GCM" : "ChaCha20-Poly1305");

    const size_t pfs_n = 18 + 1184 + 32 + 16;              /* 1250 */
    size_t plens[VENC_MAX_PAD], nl = 0, ng = 0;
    unsigned pgaps[VENC_MAX_PAD];
    size_t pad_n = 0;
    if (!zrtt) pad_n = make_padding(&c, plens, pgaps, &nl, &ng);
    size_t hello_n = head_n + (zrtt ? 18 + 32 : pfs_n + pad_n);
    hello = calloc(1, hello_n + 1);
    if (!hello) { rc = TR_EIO; goto out; }

    if (xc_random(iv, 16) != 0) { rc = TR_EIO; goto out; }
    memcpy(hello, iv, 16);
    unsigned char *rel = hello + 16;
    for (unsigned j = 0; j < c.nkeys; j++) {
        size_t index = 32;
        if (klen[j] == 32) {
            if (xc_x25519_keypair(xpriv, xpub) != 0) { rc = TR_EIO; goto out; }
            memcpy(rel, xpub, 32);
            if (x25519_shared_ext(xpriv, keys[j], nfs) != 0) { rc = fail(TR_EVENC, "invalid X25519 relay key"); goto out; }
        } else {
            unsigned char rnd[SC_MLKEM768_RND];
            if (xc_random(rnd, sizeof rnd) != 0) { rc = TR_EIO; goto out; }
            if (sc_mlkem768_encaps(rel, nfs, keys[j], rnd) != 0) { rc = fail(TR_EVENC, "invalid ML-KEM relay key"); goto out; }
            index = 1088;
        }
        if (c.xor_mode > 0) {           /* mask with a relay key keystream: looks random */
            if (ctr_new(&tmpctr, keys[j], klen[j], iv) != 0) { rc = TR_EIO; goto out; }
            have_tmp = 1;
            sc_aesctr_xor(&tmpctr, rel, rel, index);
            sc_aesctr_free(&tmpctr);
            have_tmp = 0;
        }
        if (have_last) sc_aesctr_xor(&last, rel, rel, 32);       /* "so relays cannot be swapped" */
        if (j == (unsigned)c.nkeys - 1u) { if (have_last) { sc_aesctr_free(&last); have_last = 0; } break; }
        if (have_last) sc_aesctr_free(&last);
        if (ctr_new(&last, nfs, 32, iv) != 0) { have_last = 0; rc = TR_EIO; goto out; }
        have_last = 1;
        memcpy(rel + index, h32[j + 1], 32);
        sc_aesctr_xor(&last, rel + index, rel + index, 32);
        rel += index + 32;
    }
    if (va_init(&nfs_a, iv, 16, nfs, 32, e->use_aes) != 0) { rc = TR_EIO; goto out; }

    if (zrtt) {
        /* Resume with the ticket: united = the ticket's pfsKey ‖ nfsKey (per connection). */
        memcpy(e->united, t_pfs, 64);
        memcpy(e->united + 64, nfs, 32);
        unsigned char *p = hello + head_n;
        p[0] = 0; p[1] = 32;
        va_seal(&nfs_a, NULL, NULL, 0, p, 2, p + 2);
        memcpy(p + 18, t_tk, 16);
        va_seal(&nfs_a, NULL, NULL, 0, p + 18, 16, p + 34);
        e->prewrite_n = head_n + 18 + 32;
        e->prewrite = malloc(e->prewrite_n);
        if (!e->prewrite) { rc = TR_EIO; goto out; }
        memcpy(e->prewrite, hello, e->prewrite_n);
        if (va_init(&e->wr, p + 18, 32, e->united, 96, e->use_aes) != 0) { rc = TR_EIO; goto out; }
        e->rs = RS_RAND;
        e->first_rec = 1;
        e->zero_rtt_try = 1;
        memcpy(e->tk_pfs, t_pfs, 64);
        if (e->xor2) {
            if (ctr_new(&e->out_ctr, e->united, 96, iv) != 0) { rc = TR_EIO; goto out; }
            e->have_out_ctr = 1;
            out_mask_next(e);
        }
        rc = 0;
        goto out;
    }

    /* Full handshake. */
    unsigned char *pfs = hello + head_n;
    pfs[0] = (unsigned char)((pfs_n - 18) >> 8); pfs[1] = (unsigned char)(pfs_n - 18);
    va_seal(&nfs_a, NULL, NULL, 0, pfs, 2, pfs + 2);
    unsigned char seed[SC_MLKEM768_SEED];
    if (xc_random(seed, sizeof seed) != 0 || xc_x25519_keypair(xpriv, xpub) != 0) { rc = TR_EIO; goto out; }
    unsigned char pfs_pub[1184 + 32];
    if (sc_mlkem768_keygen(pfs_pub, mlk_dk, seed) != 0) { rc = TR_EIO; goto out; }
    memcpy(pfs_pub + 1184, xpub, 32);
    memcpy(pfs + 18, pfs_pub, sizeof pfs_pub);
    va_seal(&nfs_a, NULL, NULL, 0, pfs + 18, sizeof pfs_pub, pfs + 18 + sizeof pfs_pub);

    unsigned char *pad = hello + head_n + pfs_n;
    if (pad_n) {
        pad[0] = (unsigned char)((pad_n - 18) >> 8); pad[1] = (unsigned char)(pad_n - 18);
        va_seal(&nfs_a, NULL, NULL, 0, pad, 2, pad + 2);
        va_seal(&nfs_a, NULL, NULL, 0, pad + 18, pad_n - 34, pad + pad_n - 16);
    }

    /* Send in chunks with pauses: a varying traffic pattern until VLESS takes over. */
    {
        size_t off = 0;
        size_t first = head_n + pfs_n + (nl ? plens[0] : 0);
        for (size_t i = 0; i < nl; i++) {
            size_t l = i == 0 ? first : plens[i];
            if (l) {
                if (off + l > hello_n) { rc = TR_EVENC; goto out; }
                int wr = t->fr->write(t, hello + off, l);
                if (wr) { rc = wr; goto out; }
                off += l;
            }
            if (i < ng) sleep_ms(pgaps[i]);
        }
        if (off != hello_n) { rc = fail(TR_EVENC, "padding chunks do not add up"); goto out; }
    }

    /* Server answer: AEAD(ML-KEM ct ‖ X25519 pub) under the relay key; the record keys derive
     * from pfs + nfs. */
    if ((rc = raw_fill(t, e, 1088 + 32 + 16, 1, deadline)) != 0) goto out;
    if (va_open(&nfs_a, MAX_NONCE, NULL, 0, e->rbuf, 1088 + 32, e->rbuf + 1088 + 32) != 0) {
        rc = fail(TR_EVENC, "cannot decrypt the server answer (wrong relay key?)");
        goto out;
    }
    unsigned char pfs_key[64], srv_pub[1120];
    memcpy(srv_pub, e->rbuf, 1120);
    if (sc_mlkem768_decaps(pfs_key, mlk_dk, srv_pub) != 0 || x25519_shared_ext(xpriv, srv_pub + 1088, pfs_key + 32) != 0) {
        rc = fail(TR_EVENC, "key exchange failed");
        goto out;
    }
    rb_drop(e, 1088 + 32 + 16);
    memcpy(e->united, pfs_key, 64);
    memcpy(e->united + 64, nfs, 32);
    if (va_init(&e->wr, pfs_pub, sizeof pfs_pub, e->united, 96, e->use_aes) != 0 ||
        va_init(&e->rd, srv_pub, sizeof srv_pub, e->united, 96, e->use_aes) != 0) { rc = TR_EIO; goto out; }

    /* Ticket: 16 bytes under AEAD, the first two its lifetime in seconds (0: no ticket). */
    if ((rc = raw_fill(t, e, 32, 1, deadline)) != 0) goto out;
    unsigned char tkt[16];
    memcpy(tkt, e->rbuf, 16);
    if (va_open(&e->rd, NULL, NULL, 0, tkt, 16, e->rbuf + 16) != 0) { rc = fail(TR_EVENCAUTH, "server ticket failed authentication"); goto out; }
    rb_drop(e, 32);
    unsigned secs = ((unsigned)tkt[0] << 8) | tkt[1];
    if (c.zero_rtt && secs > 0) tk_put(e->tk_id, pfs_key, tkt, secs);

    /* The server's padding length; the padding itself is read on the first read (the server
     * sends it without hurry, and our request may go out meanwhile). */
    if ((rc = raw_fill(t, e, 18, 1, deadline)) != 0) goto out;
    unsigned char lb[2];
    memcpy(lb, e->rbuf, 2);
    if (va_open(&e->rd, NULL, NULL, 0, lb, 2, e->rbuf + 2) != 0) { rc = fail(TR_EVENCAUTH, "server padding length failed authentication"); goto out; }
    rb_drop(e, 18);
    e->peer_pad = ((size_t)lb[0] << 8) | lb[1];
    if (e->peer_pad < 16) { rc = fail(TR_EVENC, "server padding is shorter than the AEAD tag"); goto out; }
    e->rs = RS_PAD;
    if (e->xor2) {
        if (ctr_new(&e->out_ctr, e->united, 96, iv) != 0) { rc = TR_EIO; goto out; }
        e->have_out_ctr = 1;
        if (ctr_new(&e->in_ctr, e->united, 96, tkt) != 0) { rc = TR_EIO; goto out; }
        e->have_in_ctr = 1;
        out_mask_next(e);
    }
    rc = 0;

out:
    if (have_tmp) sc_aesctr_free(&tmpctr);
    if (have_last) sc_aesctr_free(&last);
    memset(mlk_dk, 0, sizeof mlk_dk);
    memset(xpriv, 0, sizeof xpriv);
    memset(nfs, 0, sizeof nfs);
    va_free(&nfs_a);
    free(keys);
    free(hello);
    if (rc == 0) { t->enc = e; return 0; }
    enc_free(e);
    return rc;
}

/* ---- records -------------------------------------------------------------------------------- */

struct venc_scratch { unsigned char obuf[512 + REC_MAX_DATA + 64]; };

static struct venc_scratch *scratch(void) {
    return ts_get(TS_VENC, sizeof(struct venc_scratch));
}

static int ss_write(struct transport *t, struct venc *e, const unsigned char *d, size_t n) {
    struct venc_scratch *s = scratch();
    if (!s) return TR_EIO;
    unsigned char *obuf = s->obuf;
    if (e->ss_first_wr && n > 0 && n <= REC_MAX_DATA) {
        size_t alen = 0;
        if (d[0] == 0x01 && n >= 7) alen = 7;
        else if (d[0] == 0x03 && n >= 2 && n >= (size_t)4 + d[1]) alen = (size_t)4 + d[1];
        else if (d[0] == 0x04 && n >= 19) alen = 19;
        if (alen > 0) {
            unsigned char n0[12], n1[12], n2[12];
            memcpy(n0, e->wr.nonce, 12);
            memcpy(n1, n0, 12); ss_nonce_inc(n1);
            memcpy(n2, n1, 12); ss_nonce_inc(n2);

            size_t off = 0;
            if (e->prewrite) {
                memcpy(obuf, e->prewrite, e->prewrite_n);
                off += e->prewrite_n;
            }
            uint64_t ts = (uint64_t)time(NULL);
            uint16_t vlen = (uint16_t)(n + 2);
            unsigned char *fh = obuf + off;
            fh[0] = 0x00;
            for (int i = 0; i < 8; i++) fh[1 + i] = (unsigned char)(ts >> (56 - 8 * i));
            fh[9] = (unsigned char)(vlen >> 8);
            fh[10] = (unsigned char)vlen;
            if (sc_aead_seal(&e->wr.k, n0, NULL, 0, fh, 11, fh + 11) != 0) return TR_EVENCAUTH;
            off += 27;

            unsigned char *vh = obuf + off;
            memcpy(vh, d, alen);
            vh[alen] = 0;
            vh[alen + 1] = 0;
            if (n > alen) memcpy(vh + alen + 2, d + alen, n - alen);
            if (sc_aead_seal(&e->wr.k, n1, NULL, 0, vh, vlen, vh + vlen) != 0) return TR_EVENCAUTH;
            off += (size_t)vlen + 16;

            int rc = t->fr->write(t, obuf, off);
            if (rc) return rc;
            if (e->prewrite) { free(e->prewrite); e->prewrite = NULL; e->prewrite_n = 0; }
            memcpy(e->wr.nonce, n2, 12);
            e->ss_first_wr = 0;
            return 0;
        }
    }

    size_t done = 0;
    while (done < n) {
        size_t ch = n - done > REC_MAX_DATA ? REC_MAX_DATA : n - done;
        unsigned char n0[12], n1[12], n2[12];
        memcpy(n0, e->wr.nonce, 12);
        memcpy(n1, n0, 12); ss_nonce_inc(n1);
        memcpy(n2, n1, 12); ss_nonce_inc(n2);

        size_t off = 0;
        if (e->prewrite) {
            memcpy(obuf, e->prewrite, e->prewrite_n);
            off += e->prewrite_n;
        }
        unsigned char *lh = obuf + off;
        lh[0] = (unsigned char)(ch >> 8);
        lh[1] = (unsigned char)ch;
        if (sc_aead_seal(&e->wr.k, n0, NULL, 0, lh, 2, lh + 2) != 0) return TR_EVENCAUTH;
        off += 18;
        unsigned char *pl = obuf + off;
        memcpy(pl, d + done, ch);
        if (sc_aead_seal(&e->wr.k, n1, NULL, 0, pl, ch, pl + ch) != 0) return TR_EVENCAUTH;
        off += ch + 16;

        int rc = t->fr->write(t, obuf, off);
        if (rc) return done ? TR_EIO : rc;
        if (e->prewrite) { free(e->prewrite); e->prewrite = NULL; e->prewrite_n = 0; }
        memcpy(e->wr.nonce, n2, 12);
        e->ss_first_wr = 0;
        done += ch;
    }
    return 0;
}

static int ss_read(struct transport *t, struct venc *e, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    if (!cap) return 0;
    for (;;) {
        if (e->pl_len) {
            size_t k = e->pl_len < cap ? e->pl_len : cap;
            memcpy(d, e->rbuf + e->pl_off, k);
            e->pl_off += k;
            e->pl_len -= k;
            *got = k;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; }
            return 0;
        }
        if (e->rs == RS_SS_SALT) {
            int rc = raw_fill(t, e, e->ss_klen, 0, 0);
            if (rc) return rc;
            if (e->rlen < e->ss_klen) return 0;
            unsigned char subkey[32];
            if (ss_derive_subkey(e, e->rbuf, subkey) != 0 ||
                sc_aead_setkey(&e->rd.k, e->ss_alg, subkey) != 0) {
                memset(subkey, 0, sizeof(subkey));
                return TR_EIO;
            }
            memset(subkey, 0, sizeof(subkey));
            e->rd.ok = 1;
            memset(e->rd.nonce, 0, 12);
            rb_drop(e, e->ss_klen);
            e->rs = (e->ss_mode == 2) ? RS_SS2022_HDR : RS_SS_REC;
            continue;
        }
        if (e->rs == RS_SS2022_HDR) {
            size_t hlen = 11 + e->ss_klen;
            size_t need = hlen + 16;
            int rc = raw_fill(t, e, need, 0, 0);
            if (rc) return rc;
            if (e->rlen < need) return 0;
            if (sc_aead_open(&e->rd.k, e->rd.nonce, NULL, 0, e->rbuf, hlen, e->rbuf + hlen) != 0)
                return fail(TR_EVENCAUTH, "SS-2022 response header failed authentication");
            ss_nonce_inc(e->rd.nonce);
            if (e->rbuf[0] != 0x01 || memcmp(e->rbuf + 9, e->ss_tx_salt, e->ss_klen) != 0)
                return fail(TR_EVENCAUTH, "SS-2022 response salt mismatch");
            e->peer_pad = ((size_t)e->rbuf[9 + e->ss_klen] << 8) | e->rbuf[10 + e->ss_klen];
            rb_drop(e, need);
            e->rs = RS_SS_PAYLOAD;
            continue;
        }
        if (e->rs == RS_SS_PAYLOAD) {
            size_t need = e->peer_pad + 16;
            int rc = raw_fill(t, e, need, 0, 0);
            if (rc) return rc;
            if (e->rlen < need) return 0;
            if (sc_aead_open(&e->rd.k, e->rd.nonce, NULL, 0, e->rbuf, e->peer_pad, e->rbuf + e->peer_pad) != 0)
                return fail(TR_EVENCAUTH, "SS-2022 first payload failed authentication");
            ss_nonce_inc(e->rd.nonce);
            e->pl_off = 0;
            e->pl_len = e->peer_pad;
            e->pl_rec = need;
            e->rs = RS_SS_REC;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; continue; }
            continue;
        }
        /* RS_SS_REC: 2-byte length + 16-byte tag, then payload + 16-byte tag. */
        int rc = raw_fill(t, e, 18, 0, 0);
        if (rc) return rc;
        if (e->rlen < 18) return 0;
        if (!e->hdr_done) {
            if (sc_aead_open(&e->rd.k, e->rd.nonce, NULL, 0, e->rbuf, 2, e->rbuf + 2) != 0)
                return fail(TR_EVENCAUTH, "Shadowsocks chunk length failed authentication");
            ss_nonce_inc(e->rd.nonce);
            if (e->ss_mode == 2)
                e->peer_pad = ((size_t)e->rbuf[0] << 8) | e->rbuf[1];
            else
                e->peer_pad = (((size_t)e->rbuf[0] & 0x3Fu) << 8) | e->rbuf[1];
            e->hdr_done = 1;
        }
        size_t need = 18 + e->peer_pad + 16;
        rc = raw_fill(t, e, need, 0, 0);
        if (rc) return rc;
        if (e->rlen < need) return 0;
        if (sc_aead_open(&e->rd.k, e->rd.nonce, NULL, 0, e->rbuf + 18, e->peer_pad, e->rbuf + 18 + e->peer_pad) != 0)
            return fail(TR_EVENCAUTH, "Shadowsocks chunk payload failed authentication");
        ss_nonce_inc(e->rd.nonce);
        e->pl_off = 18;
        e->pl_len = e->peer_pad;
        e->pl_rec = need;
        if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; continue; }
    }
}

static int vmess_aead_hdr_seal(const unsigned char key16[16], const unsigned char iv12[12],
                               const void *aad, size_t aad_n,
                               const unsigned char *in, size_t n, unsigned char *out) {
    struct sc_aead a;
    if (sc_aead_setkey(&a, SC_AES128_GCM, key16) != 0) return -1;
    memcpy(out, in, n);
    int rc = sc_aead_seal(&a, iv12, aad, aad_n, out, n, out + n);
    sc_aead_free(&a);
    return rc;
}

static int vmess_aead_hdr_open(const unsigned char key16[16], const unsigned char iv12[12],
                               const void *aad, size_t aad_n,
                               unsigned char *buf, size_t n, const unsigned char tag[16]) {
    struct sc_aead a;
    if (sc_aead_setkey(&a, SC_AES128_GCM, key16) != 0) return -1;
    int rc = sc_aead_open(&a, iv12, aad, aad_n, buf, n, tag);
    sc_aead_free(&a);
    return rc;
}

static int vmess_write(struct transport *t, struct venc *e, const unsigned char *d, size_t n) {
    struct venc_scratch *s = scratch();
    if (!s) return TR_EIO;
    unsigned char *obuf = s->obuf;
    size_t hdr_off = 0;

    if (e->ss_first_wr) {
        uint8_t raw_atyp = n > 0 ? d[0] : 0;
        int is_udp = (raw_atyp & 0x80) != 0;
        e->vm_is_udp = (uint8_t)is_udp;
        uint8_t atyp = raw_atyp & 0x7Fu;
        size_t alen = 0, addr_len = 0;
        const unsigned char *addr_ptr = NULL;
        unsigned char port_hi = 0, port_lo = 0, vm_atyp = 0;

        if (atyp == 0x01 && n >= 7) {
            alen = 7; vm_atyp = 1; addr_ptr = d + 1; addr_len = 4;
            port_hi = d[5]; port_lo = d[6];
        } else if (atyp == 0x03 && n >= 2 && n >= (size_t)4 + d[1]) {
            alen = (size_t)4 + d[1]; vm_atyp = 2; addr_ptr = d + 1; addr_len = (size_t)1 + d[1];
            port_hi = d[2 + d[1]]; port_lo = d[3 + d[1]];
        } else if (atyp == 0x04 && n >= 19) {
            alen = 19; vm_atyp = 3; addr_ptr = d + 1; addr_len = 16;
            port_hi = d[17]; port_lo = d[18];
        } else {
            return fail(TR_EVENC, "VMess first write missing target address");
        }

        unsigned char hdr[320];
        unsigned char rnd_b = 0;
        xc_random(&rnd_b, 1);
        size_t pad_len = (size_t)(rnd_b & 0x0Fu);

        hdr[0] = 1; /* version 1 */
        memcpy(hdr + 1, e->vm_req_iv, 16);
        memcpy(hdr + 17, e->vm_req_key, 16);
        hdr[33] = e->vm_resp_hdr;
        /* OptionChunkStream (0x01) for AEAD or UDP; SecurityType_NONE (5) over TCP uses OptionNone (0x00) raw stream */
        hdr[34] = (e->vm_sec == 5 && !is_udp) ? 0x00 : 0x01;
        hdr[35] = (unsigned char)((pad_len << 4) | (e->vm_sec & 0x0Fu));
        hdr[36] = 0;
        hdr[37] = is_udp ? 0x02 : 0x01;
        hdr[38] = port_hi;
        hdr[39] = port_lo;
        hdr[40] = vm_atyp;
        memcpy(hdr + 41, addr_ptr, addr_len);
        size_t hpos = 41 + addr_len;
        if (pad_len > 0) {
            xc_random(hdr + hpos, pad_len);
            hpos += pad_len;
        }
        uint32_t fnv = fnv1a32(hdr, hpos);
        hdr[hpos]     = (unsigned char)(fnv >> 24);
        hdr[hpos + 1] = (unsigned char)(fnv >> 16);
        hdr[hpos + 2] = (unsigned char)(fnv >> 8);
        hdr[hpos + 3] = (unsigned char)fnv;
        hpos += 4;

        /* 16-byte AuthID */
        unsigned char auth_plain[16], auth_id[16], kdf_k[32], kdf_iv[32], conn_nonce[8];
        uint64_t ts = (uint64_t)time(NULL);
        for (int i = 0; i < 8; i++) auth_plain[i] = (unsigned char)(ts >> (56 - 8 * i));
        xc_random(auth_plain + 8, 4);
        uint32_t crc = crc32_ieee(auth_plain, 12);
        auth_plain[12] = (unsigned char)(crc >> 24);
        auth_plain[13] = (unsigned char)(crc >> 16);
        auth_plain[14] = (unsigned char)(crc >> 8);
        auth_plain[15] = (unsigned char)crc;
        vmess_kdf(e->vm_cmd_key, 1, "AES Auth ID Encryption", 22, NULL, 0, NULL, 0, kdf_k);
        if (sc_aes_block(kdf_k, 16, 0, auth_plain, auth_id) != 0) return TR_EIO;

        xc_random(conn_nonce, 8);
        memcpy(obuf, auth_id, 16);

        unsigned char len_be[2] = { (unsigned char)(hpos >> 8), (unsigned char)hpos };
        vmess_kdf(e->vm_cmd_key, 3, "VMess Header AEAD Key_Length", 28, auth_id, 16, conn_nonce, 8, kdf_k);
        vmess_kdf(e->vm_cmd_key, 3, "VMess Header AEAD Nonce_Length", 30, auth_id, 16, conn_nonce, 8, kdf_iv);
        if (vmess_aead_hdr_seal(kdf_k, kdf_iv, auth_id, 16, len_be, 2, obuf + 16) != 0) return TR_EVENCAUTH;

        memcpy(obuf + 34, conn_nonce, 8);
        vmess_kdf(e->vm_cmd_key, 3, "VMess Header AEAD Key", 21, auth_id, 16, conn_nonce, 8, kdf_k);
        vmess_kdf(e->vm_cmd_key, 3, "VMess Header AEAD Nonce", 23, auth_id, 16, conn_nonce, 8, kdf_iv);
        if (vmess_aead_hdr_seal(kdf_k, kdf_iv, auth_id, 16, hdr, hpos, obuf + 42) != 0) return TR_EVENCAUTH;

        hdr_off = 42 + hpos + 16;
        d += alen;
        n -= alen;
        if (n == 0) {
            int rc = t->fr->write(t, obuf, hdr_off);
            if (rc) return rc;
            e->ss_first_wr = 0;
            return 0;
        }
    }

    size_t done = 0;
    while (done < n) {
        size_t ch = n - done > REC_MAX_DATA ? REC_MAX_DATA : n - done;
        size_t off = hdr_off;
        uint32_t next_cnt = e->vm_tx_count;

        if (e->vm_sec == 5 && !e->vm_is_udp) {
            memcpy(obuf + off, d + done, ch);
            off += ch;
        } else if (e->vm_sec == 5) {
            obuf[off]     = (unsigned char)(ch >> 8);
            obuf[off + 1] = (unsigned char)ch;
            memcpy(obuf + off + 2, d + done, ch);
            off += 2 + ch;
        } else {
            if (next_cnt > 0xFFFFu)
                return fail(TR_EVENC, "VMess AEAD TX nonce limit reached");
            uint16_t clen = (uint16_t)(ch + 16);
            obuf[off]     = (unsigned char)(clen >> 8);
            obuf[off + 1] = (unsigned char)clen;
            unsigned char nonce[12];
            nonce[0] = (unsigned char)(next_cnt >> 8);
            nonce[1] = (unsigned char)next_cnt;
            memcpy(nonce + 2, e->vm_req_iv + 2, 10);
            memcpy(obuf + off + 2, d + done, ch);
            if (sc_aead_seal(&e->wr.k, nonce, NULL, 0, obuf + off + 2, ch, obuf + off + 2 + ch) != 0)
                return TR_EVENCAUTH;
            off += 2 + (size_t)clen;
            next_cnt++;
        }

        int rc = t->fr->write(t, obuf, off);
        if (rc) return done ? TR_EIO : rc;
        e->ss_first_wr = 0;
        hdr_off = 0;
        e->vm_tx_count = next_cnt;
        done += ch;
    }
    return 0;
}

static int vmess_read(struct transport *t, struct venc *e, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    if (!cap) return 0;
    for (;;) {
        if (e->pl_len) {
            size_t k = e->pl_len < cap ? e->pl_len : cap;
            memcpy(d, e->rbuf + e->pl_off, k);
            e->pl_off += k;
            e->pl_len -= k;
            *got = k;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; }
            return 0;
        }
        if (e->rs == RS_VMESS_HDR_LEN) {
            int rc = raw_fill(t, e, 18, 0, 0);
            if (rc) return rc;
            if (e->rlen < 18) return 0;
            unsigned char kdf_k[32], kdf_iv[32];
            vmess_kdf(e->vm_resp_key, 1, "AEAD Resp Header Len Key", 24, NULL, 0, NULL, 0, kdf_k);
            vmess_kdf(e->vm_resp_iv,  1, "AEAD Resp Header Len IV",  23, NULL, 0, NULL, 0, kdf_iv);
            if (vmess_aead_hdr_open(kdf_k, kdf_iv, NULL, 0, e->rbuf, 2, e->rbuf + 2) != 0)
                return fail(TR_EVENCAUTH, "VMess response header length failed authentication");
            e->peer_pad = ((size_t)e->rbuf[0] << 8) | e->rbuf[1];
            if (e->peer_pad < 4 || e->peer_pad > 1024)
                return fail(TR_EVENC, "invalid VMess response header length");
            rb_drop(e, 18);
            e->rs = RS_VMESS_HDR;
            continue;
        }
        if (e->rs == RS_VMESS_HDR) {
            size_t need = e->peer_pad + 16;
            int rc = raw_fill(t, e, need, 0, 0);
            if (rc) return rc;
            if (e->rlen < need) return 0;
            unsigned char kdf_k[32], kdf_iv[32];
            vmess_kdf(e->vm_resp_key, 1, "AEAD Resp Header Key", 20, NULL, 0, NULL, 0, kdf_k);
            vmess_kdf(e->vm_resp_iv,  1, "AEAD Resp Header IV",  19, NULL, 0, NULL, 0, kdf_iv);
            if (vmess_aead_hdr_open(kdf_k, kdf_iv, NULL, 0, e->rbuf, e->peer_pad, e->rbuf + e->peer_pad) != 0)
                return fail(TR_EVENCAUTH, "VMess response header failed authentication");
            if (e->rbuf[0] != e->vm_resp_hdr)
                return fail(TR_EVENCAUTH, "VMess response header tag mismatch");
            rb_drop(e, need);
            e->rs = RS_VMESS_REC;
            continue;
        }
        /* RS_VMESS_REC */
        if (e->vm_sec == 5 && !e->vm_is_udp) {
            if (!e->rlen) {
                int rc = raw_fill(t, e, 1, 0, 0);
                if (rc) return rc;
                if (!e->rlen) return 0;
            }
            size_t k = e->rlen < cap ? e->rlen : cap;
            memcpy(d, e->rbuf, k);
            rb_drop(e, k);
            *got = k;
            return 0;
        }
        int rc = raw_fill(t, e, 2, 0, 0);
        if (rc) return rc;
        if (e->rlen < 2) return 0;
        size_t clen = ((size_t)e->rbuf[0] << 8) | e->rbuf[1];
        if (clen == 0) {
            rb_drop(e, 2);
            return TR_ECLOSED;
        }
        if (clen > PROTO_MAX_CHUNK) return fail(TR_EVENC, "VMess chunk exceeds maximum size");
        size_t need = 2 + clen;
        rc = raw_fill(t, e, need, 0, 0);
        if (rc) return rc;
        if (e->rlen < need) return 0;

        if (e->vm_sec == 5) {
            e->pl_off = 2;
            e->pl_len = clen;
            e->pl_rec = need;
            continue;
        }
        if (clen < 16) return fail(TR_EVENC, "VMess chunk shorter than AEAD tag");
        if (e->vm_rx_count > 0xFFFFu)
            return fail(TR_EVENC, "VMess AEAD RX nonce limit reached");
        unsigned char nonce[12];
        nonce[0] = (unsigned char)(e->vm_rx_count >> 8);
        nonce[1] = (unsigned char)e->vm_rx_count;
        memcpy(nonce + 2, e->vm_resp_iv + 2, 10);
        if (sc_aead_open(&e->rd.k, nonce, NULL, 0, e->rbuf + 2, clen - 16, e->rbuf + 2 + clen - 16) != 0)
            return fail(TR_EVENCAUTH, "VMess chunk payload failed authentication");
        e->vm_rx_count++;
        if (clen == 16) {
            rb_drop(e, need);
            return TR_ECLOSED;
        }
        e->pl_off = 2;
        e->pl_len = clen - 16;
        e->pl_rec = need;
    }
}

static int mieru_write(struct transport *t, struct venc *e, const unsigned char *d, size_t n) {
    struct venc_scratch *s = scratch();
    if (!s) return TR_EIO;
    unsigned char *obuf = s->obuf;
    size_t done = 0;

    if (e->mieru_tx_first) {
        size_t ch = n > 1024 ? 1024 : n;
        unsigned char meta[32] = {0};
        uint32_t ts_min = (uint32_t)(time(NULL) / 60);
        meta[0] = 2; /* openSessionRequest */
        meta[2] = (unsigned char)(ts_min >> 24);
        meta[3] = (unsigned char)(ts_min >> 16);
        meta[4] = (unsigned char)(ts_min >> 8);
        meta[5] = (unsigned char)ts_min;
        meta[6] = (unsigned char)(e->mieru_sid >> 24);
        meta[7] = (unsigned char)(e->mieru_sid >> 16);
        meta[8] = (unsigned char)(e->mieru_sid >> 8);
        meta[9] = (unsigned char)e->mieru_sid;
        meta[15] = (unsigned char)(ch >> 8);
        meta[16] = (unsigned char)ch;

        unsigned char n0[24], n1[24], n2[24];
        memcpy(n0, e->mieru_tx_nonce, 24);
        memcpy(n1, n0, 24); mieru_nonce_inc(n1);
        memcpy(n2, n1, 24); mieru_nonce_inc(n2);

        memcpy(obuf, n0, 24);
        memcpy(obuf + 24, meta, 32);
        if (sc_xchacha20_poly1305_seal(e->mieru_key, n0, NULL, 0, obuf + 24, 32, obuf + 56) != 0)
            return TR_EVENCAUTH;
        size_t off = 72;
        if (ch > 0) {
            memcpy(obuf + off, d, ch);
            if (sc_xchacha20_poly1305_seal(e->mieru_key, n1, NULL, 0, obuf + off, ch, obuf + off + ch) != 0)
                return TR_EVENCAUTH;
            off += ch + 16;
        }
        int rc = t->fr->write(t, obuf, off);
        if (rc) return rc;
        memcpy(e->mieru_tx_nonce, ch > 0 ? n2 : n1, 24);
        e->mieru_tx_first = 0;
        e->mieru_tx_seq = 1;
        done += ch;
    }

    while (done < n) {
        size_t ch = n - done > 4096 ? 4096 : n - done;
        unsigned char meta[32] = {0};
        uint32_t ts_min = (uint32_t)(time(NULL) / 60);
        meta[0] = 6; /* dataClientToServer */
        meta[2] = (unsigned char)(ts_min >> 24);
        meta[3] = (unsigned char)(ts_min >> 16);
        meta[4] = (unsigned char)(ts_min >> 8);
        meta[5] = (unsigned char)ts_min;
        meta[6] = (unsigned char)(e->mieru_sid >> 24);
        meta[7] = (unsigned char)(e->mieru_sid >> 16);
        meta[8] = (unsigned char)(e->mieru_sid >> 8);
        meta[9] = (unsigned char)e->mieru_sid;
        meta[10] = (unsigned char)(e->mieru_tx_seq >> 24);
        meta[11] = (unsigned char)(e->mieru_tx_seq >> 16);
        meta[12] = (unsigned char)(e->mieru_tx_seq >> 8);
        meta[13] = (unsigned char)e->mieru_tx_seq;
        meta[14] = (unsigned char)(e->mieru_rx_seq >> 24);
        meta[15] = (unsigned char)(e->mieru_rx_seq >> 16);
        meta[16] = (unsigned char)(e->mieru_rx_seq >> 8);
        meta[17] = (unsigned char)e->mieru_rx_seq;
        meta[18] = (unsigned char)(4096 >> 8);
        meta[19] = (unsigned char)(4096 & 0xFF);
        meta[22] = (unsigned char)(ch >> 8);
        meta[23] = (unsigned char)ch;

        unsigned char n0[24], n1[24], n2[24];
        memcpy(n0, e->mieru_tx_nonce, 24);
        memcpy(n1, n0, 24); mieru_nonce_inc(n1);
        memcpy(n2, n1, 24); mieru_nonce_inc(n2);

        memcpy(obuf, meta, 32);
        if (sc_xchacha20_poly1305_seal(e->mieru_key, n0, NULL, 0, obuf, 32, obuf + 32) != 0)
            return TR_EVENCAUTH;
        memcpy(obuf + 48, d + done, ch);
        if (sc_xchacha20_poly1305_seal(e->mieru_key, n1, NULL, 0, obuf + 48, ch, obuf + 48 + ch) != 0)
            return TR_EVENCAUTH;
        int rc = t->fr->write(t, obuf, 48 + ch + 16);
        if (rc) return done ? TR_EIO : rc;
        memcpy(e->mieru_tx_nonce, n2, 24);
        e->mieru_tx_seq++;
        done += ch;
    }
    return 0;
}

static int mieru_read(struct transport *t, struct venc *e, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    if (!cap) return 0;
    for (;;) {
        if (e->pl_len) {
            size_t k = e->pl_len < cap ? e->pl_len : cap;
            memcpy(d, e->rbuf + e->pl_off, k);
            e->pl_off += k;
            e->pl_len -= k;
            *got = k;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; }
            return 0;
        }
        if (e->rs == RS_MIERU_META) {
            size_t mneed = e->mieru_rx_first ? 72 : 48;
            int rc = raw_fill(t, e, mneed, 0, 0);
            if (rc) return rc;
            if (e->rlen < mneed) return 0;
            unsigned char *meta;
            if (e->mieru_rx_first) {
                memcpy(e->mieru_rx_nonce, e->rbuf, 24);
                meta = e->rbuf + 24;
            } else {
                meta = e->rbuf;
            }
            if (sc_xchacha20_poly1305_open(e->mieru_key, e->mieru_rx_nonce, NULL, 0, meta, 32, meta + 32) != 0)
                return fail(TR_EVENCAUTH, "Mieru metadata failed authentication");
            mieru_nonce_inc(e->mieru_rx_nonce);
            e->mieru_rx_first = 0;

            uint8_t ptype = meta[0];
            uint32_t seq = ((uint32_t)meta[10] << 24) | ((uint32_t)meta[11] << 16) |
                           ((uint32_t)meta[12] << 8) | meta[13];
            if (seq >= e->mieru_rx_seq) e->mieru_rx_seq = seq + 1;

            if (ptype == 3 || ptype == 4 || ptype == 5) {
                if (ptype == 3 && meta[14] != 0)
                    return fail(TR_EVENC, "Mieru server refused session");
                e->mieru_pre_len = 0;
                e->mieru_pay_len = (uint16_t)(((uint16_t)meta[15] << 8) | meta[16]);
                e->mieru_suf_len = meta[17];
                e->mieru_closing = (ptype == 4 || ptype == 5);
            } else if (ptype == 7 || ptype == 9) {
                e->mieru_pre_len = meta[21];
                e->mieru_pay_len = (uint16_t)(((uint16_t)meta[22] << 8) | meta[23]);
                e->mieru_suf_len = meta[24];
                e->mieru_closing = 0;
            } else if (ptype == 0 || ptype == 1) {
                rb_drop(e, mneed);
                return TR_ECLOSED;
            } else {
                return fail(TR_EVENC, "unexpected Mieru segment protocol");
            }
            if (e->mieru_pay_len > 4096)
                return fail(TR_EVENC, "Mieru segment payload exceeds maximum size");
            e->peer_pad = (size_t)e->mieru_pre_len +
                          (e->mieru_pay_len ? (size_t)e->mieru_pay_len + 16 : 0) +
                          (size_t)e->mieru_suf_len;
            rb_drop(e, mneed);
            if (e->peer_pad == 0) {
                if (e->mieru_closing) return TR_ECLOSED;
                continue;
            }
            e->rs = RS_MIERU_PAYLOAD;
            continue;
        }
        /* RS_MIERU_PAYLOAD */
        int rc = raw_fill(t, e, e->peer_pad, 0, 0);
        if (rc) return rc;
        if (e->rlen < e->peer_pad) return 0;
        if (e->mieru_pay_len > 0) {
            unsigned char *pl = e->rbuf + e->mieru_pre_len;
            if (sc_xchacha20_poly1305_open(e->mieru_key, e->mieru_rx_nonce, NULL, 0,
                                           pl, e->mieru_pay_len, pl + e->mieru_pay_len) != 0)
                return fail(TR_EVENCAUTH, "Mieru payload failed authentication");
            mieru_nonce_inc(e->mieru_rx_nonce);
            e->pl_off = e->mieru_pre_len;
            e->pl_len = e->mieru_pay_len;
            e->pl_rec = e->peer_pad;
            e->rs = RS_MIERU_META;
            continue;
        }
        rb_drop(e, e->peer_pad);
        e->peer_pad = 0;
        e->rs = RS_MIERU_META;
        if (e->mieru_closing) return TR_ECLOSED;
    }
}

static int anytls_write(struct transport *t, struct venc *e, const unsigned char *d, size_t n) {
    struct venc_scratch *s = scratch();
    if (!s) return TR_EIO;
    unsigned char *obuf = s->obuf;
    if (n == 0 && e->prewrite) {
        int rc = t->fr->write(t, e->prewrite, e->prewrite_n);
        if (rc) return rc;
        free(e->prewrite); e->prewrite = NULL; e->prewrite_n = 0;
        return 0;
    }
    size_t done = 0;
    while (done < n) {
        size_t ch = n - done > REC_MAX_DATA ? REC_MAX_DATA : n - done;
        size_t off = 0;
        if (e->prewrite) {
            memcpy(obuf, e->prewrite, e->prewrite_n);
            off += e->prewrite_n;
        }
        obuf[off]     = 2; /* cmdPsh */
        obuf[off + 1] = 0;
        obuf[off + 2] = 0;
        obuf[off + 3] = 0;
        obuf[off + 4] = 1; /* sid = 1 */
        obuf[off + 5] = (unsigned char)(ch >> 8);
        obuf[off + 6] = (unsigned char)ch;
        memcpy(obuf + off + 7, d + done, ch);
        int rc = t->fr->write(t, obuf, off + 7 + ch);
        if (rc) return done ? TR_EIO : rc;
        if (e->prewrite) { free(e->prewrite); e->prewrite = NULL; e->prewrite_n = 0; }
        done += ch;
    }
    return 0;
}

static int anytls_read(struct transport *t, struct venc *e, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    if (!cap) return 0;
    for (;;) {
        if (e->pl_len) {
            size_t k = e->pl_len < cap ? e->pl_len : cap;
            memcpy(d, e->rbuf + e->pl_off, k);
            e->pl_off += k;
            e->pl_len -= k;
            *got = k;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; }
            return 0;
        }
        int rc = raw_fill(t, e, 7, 0, 0);
        if (rc) return rc;
        if (e->rlen < 7) return 0;
        uint8_t cmd = e->rbuf[0];
        size_t flen = ((size_t)e->rbuf[5] << 8) | e->rbuf[6];
        if (flen > PROTO_MAX_CHUNK) return fail(TR_EVENC, "AnyTLS frame exceeds maximum size");
        size_t need = 7 + flen;
        rc = raw_fill(t, e, need, 0, 0);
        if (rc) return rc;
        if (e->rlen < need) return 0;

        if (cmd == 2) { /* cmdPsh */
            if (flen == 0) { rb_drop(e, 7); continue; }
            e->pl_off = 7;
            e->pl_len = flen;
            e->pl_rec = need;
            continue;
        }
        if (cmd == 3) { /* cmdFin */
            rb_drop(e, need);
            return TR_ECLOSED;
        }
        if (cmd == 8) { /* cmdHeartRequest -> cmdHeartResponse (9) */
            unsigned char hr[7] = { 9, e->rbuf[1], e->rbuf[2], e->rbuf[3], e->rbuf[4], 0, 0 };
            (void)t->fr->write(t, hr, 7);
            rb_drop(e, need);
            continue;
        }
        if (cmd == 7) { /* cmdSynAck */
            if (flen > 0) {
                rb_drop(e, need);
                return fail(TR_EVENC, "AnyTLS server rejected stream");
            }
            rb_drop(e, need);
            continue;
        }
        if (cmd == 0 || cmd == 6 || cmd == 9 || cmd == 10) {
            rb_drop(e, need);
            continue;
        }
        if (cmd == 5) { /* cmdAlert */
            rb_drop(e, need);
            return fail(TR_EVENCAUTH, "AnyTLS server alert");
        }
        return fail(TR_EVENC, "unexpected AnyTLS frame command");
    }
}

int tr_venc_write(struct transport *t, const unsigned char *d, size_t n) {
    struct venc *e = t->enc;
    if (e->ss_mode == 1 || e->ss_mode == 2) return ss_write(t, e, d, n);
    if (e->ss_mode == 3) return vmess_write(t, e, d, n);
    if (e->ss_mode == 4) return mieru_write(t, e, d, n);
    if (e->ss_mode == 5) return anytls_write(t, e, d, n);
    struct venc_scratch *s = scratch();
    if (!s) return TR_EIO;
    unsigned char *obuf = s->obuf;
    size_t done = 0;
    while (done < n) {
        size_t ch = n - done > REC_MAX_DATA ? REC_MAX_DATA : n - done;
        /* nonce and key are tentative: committed only after the send. */
        const int rekey = !memcmp(e->wr.nonce, MAX_NONCE, 12);
        unsigned char nn[12];
        memcpy(nn, e->wr.nonce, 12);
        nonce_inc(nn);
        unsigned char hdr[5] = { 23, 3, 3, (unsigned char)((ch + 16) >> 8), (unsigned char)(ch + 16) };
        memcpy(obuf, hdr, 5);
        memcpy(obuf + 5, d + done, ch);
        if (sc_aead_seal(&e->wr.k, nn, hdr, 5, obuf + 5, ch, obuf + 5 + ch) != 0) return TR_EVENCAUTH;
        size_t rec_n = 5 + ch + 16;
        unsigned char *send = obuf;
        size_t send_n = rec_n;
        unsigned char *big = NULL;
        if (e->prewrite) {                  /* iv ‖ relay ‖ ticket go with the first record */
            big = malloc(e->prewrite_n + rec_n);
            if (!big) return TR_EIO;
            memcpy(big, e->prewrite, e->prewrite_n);
            memcpy(big + e->prewrite_n, obuf, rec_n);
            send = big;
            send_n = e->prewrite_n + rec_n;
        }
        if (e->xor2) for (int i = 0; i < 5; i++) send[send_n - rec_n + (size_t)i] ^= e->out_mask[i];
        int rc = t->fr->write(t, send, send_n);
        if (rc) {
            free(big);
            /* send holds a masked header, but the state has not moved and obuf is rebuilt on
             * the retry, so nothing is spoiled. After a partial send (done > 0) a retry of the
             * same bytes would repeat the chunks already sent, so that is a hard failure. */
            return done ? TR_EIO : rc;
        }
        free(big);
        /* Sent: commit. */
        if (e->prewrite) { free(e->prewrite); e->prewrite = NULL; e->prewrite_n = 0; }
        memcpy(e->wr.nonce, nn, 12);
        out_mask_next(e);
        if (rekey) {
            /* A record whose nonce wrapped past the maximum is the last under this key: the
             * next key derives from the record itself (context: header ‖ ciphertext ‖ tag, the
             * header unmasked). */
            unsigned char plainrec[5 + REC_MAX_DATA + 16];
            memcpy(plainrec, obuf, rec_n);
            memcpy(plainrec, hdr, 5);
            if (va_init(&e->wr, plainrec, rec_n, e->united, 96, e->use_aes) != 0) return TR_EIO;
        }
        done += ch;
    }
    return 0;
}

/* Parse a record header: the body length, or 0 if it is not an Xray header (DecodeHeader). */
static int hdr_len(const unsigned char h[5]) {
    if (h[0] != 23 || h[1] != 3 || h[2] != 3) return 0;
    int l = (h[3] << 8) | h[4];
    return (l < REC_MIN_LEN || l > REC_MAX_LEN) ? 0 : l;
}

int tr_venc_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    struct venc *e = t->enc;
    if (e->ss_mode == 1 || e->ss_mode == 2) return ss_read(t, e, d, cap, got);
    if (e->ss_mode == 3) return vmess_read(t, e, d, cap, got);
    if (e->ss_mode == 4) return mieru_read(t, e, d, cap, got);
    if (e->ss_mode == 5) return anytls_read(t, e, d, cap, got);
    *got = 0;
    if (!cap) return 0;
    for (;;) {
        /* 1. Decrypted data not yet returned. */
        if (e->pl_len) {
            size_t k = e->pl_len < cap ? e->pl_len : cap;
            memcpy(d, e->rbuf + e->pl_off, k);
            e->pl_off += k;
            e->pl_len -= k;
            *got = k;
            if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; hdr_prepare(e); }
            return 0;
        }
        /* 2. Preambles: the server's random 16 bytes (0-RTT) or its padding (1-RTT). */
        if (e->rs == RS_RAND) {
            int rc = raw_fill(t, e, 16, 0, 0);
            if (rc) return rc;
            if (e->rlen < 16) return 0;
            if (va_init(&e->rd, e->rbuf, 16, e->united, 96, e->use_aes) != 0) return TR_EIO;
            if (e->xor2) {
                if (ctr_new(&e->in_ctr, e->united, 96, e->rbuf) != 0) return TR_EIO;
                e->have_in_ctr = 1;
            }
            rb_drop(e, 16);
            e->rs = RS_REC;
            continue;
        }
        if (e->rs == RS_PAD) {
            if (!e->padbuf) { e->padbuf = malloc(e->peer_pad); e->padbuf_n = 0; if (!e->padbuf) return TR_EIO; }
            while (e->padbuf_n < e->peer_pad) {
                if (!e->rlen) {
                    int rc = raw_fill(t, e, 1, 0, 0);
                    if (rc) return rc;
                    if (!e->rlen) return 0;
                }
                size_t take = e->peer_pad - e->padbuf_n;
                if (take > e->rlen) take = e->rlen;
                memcpy(e->padbuf + e->padbuf_n, e->rbuf, take);
                e->padbuf_n += take;
                rb_drop(e, take);
            }
            if (va_open(&e->rd, NULL, NULL, 0, e->padbuf, e->peer_pad - 16, e->padbuf + e->peer_pad - 16) != 0)
                return fail(TR_EVENCAUTH, "server padding failed authentication");
            free(e->padbuf);
            e->padbuf = NULL;
            e->rs = RS_REC;
            continue;
        }
        /* 3. A record: header, body, decryption. */
        int rc = raw_fill(t, e, 5, 0, 0);
        if (rc) return rc;
        if (e->rlen < 5) return 0;
        hdr_prepare(e);
        int l = hdr_len(e->rbuf);
        if (!l) {
            if (e->first_rec && e->zero_rtt_try) {
                tk_expire(e->tk_id, e->tk_pfs);
                return fail(TR_EVENC0RTT, "0-RTT ticket rejected, next connect: full handshake");
            }
            return fail(TR_EVENC, "invalid record header");
        }
        rc = raw_fill(t, e, 5 + (size_t)l, 0, 0);
        if (rc) return rc;
        if (e->rlen < 5 + (size_t)l) return 0;
        unsigned char hdr[5];
        memcpy(hdr, e->rbuf, 5);
        const int rekey = !memcmp(e->rd.nonce, MAX_NONCE, 12);
        unsigned char nn[12];
        memcpy(nn, e->rd.nonce, 12);
        nonce_inc(nn);
        /* A record whose nonce wrapped past the maximum is the last under this key: the next key
         * derives from the record itself (header ‖ ciphertext ‖ tag, header unmasked). Copy it
         * before opening: the body is decrypted in place. */
        unsigned char *rk = NULL;
        if (rekey) {
            rk = malloc(5 + (size_t)l);
            if (!rk) return TR_EIO;
            memcpy(rk, e->rbuf, 5 + (size_t)l);
        }
        if (sc_aead_open(&e->rd.k, nn, hdr, 5, e->rbuf + 5, (size_t)l - 16, e->rbuf + 5 + l - 16) != 0) {
            free(rk);
            return fail(TR_EVENCAUTH, "cannot decrypt a record (keys out of sync)");
        }
        memcpy(e->rd.nonce, nn, 12);
        e->first_rec = 0;
        if (rk) {
            int krc = va_init(&e->rd, rk, 5 + (size_t)l, e->united, 96, e->use_aes);
            free(rk);
            if (krc != 0) return TR_EIO;
        }
        e->pl_off = 5;
        e->pl_len = (size_t)l - 16;
        e->pl_rec = 5 + (size_t)l;
        if (!e->pl_len) { rb_drop(e, e->pl_rec); e->pl_rec = 0; e->hdr_done = 0; hdr_prepare(e); continue; }
    }
}

int tr_venc_pending(const struct transport *t) {
    const struct venc *e = t->enc;
    if (!e) return 0;
    if (e->pl_len) return 1;
    if (e->ss_mode == 1 || e->ss_mode == 2) {
        if (e->rs == RS_SS_REC && e->hdr_done && e->rlen >= 18 + e->peer_pad + 16) return 1;
        if (e->rs == RS_SS_REC && !e->hdr_done && e->rlen >= 18) return 1;
        if (e->rs == RS_SS_PAYLOAD && e->rlen >= e->peer_pad + 16) return 1;
        return 0;
    }
    if (e->ss_mode == 3) {
        if (e->rs == RS_VMESS_HDR_LEN && e->rlen >= 18) return 1;
        if (e->rs == RS_VMESS_HDR && e->rlen >= e->peer_pad + 16) return 1;
        if (e->rs == RS_VMESS_REC && e->vm_sec == 5 && !e->vm_is_udp && e->rlen > 0) return 1;
        if (e->rs == RS_VMESS_REC && e->rlen >= 2) {
            size_t clen = ((size_t)e->rbuf[0] << 8) | e->rbuf[1];
            if (e->rlen >= 2 + clen) return 1;
        }
        return 0;
    }
    if (e->ss_mode == 4) {
        if (e->rs == RS_MIERU_META && e->rlen >= (e->mieru_rx_first ? 72u : 48u)) return 1;
        if (e->rs == RS_MIERU_PAYLOAD && e->rlen >= e->peer_pad) return 1;
        return 0;
    }
    if (e->ss_mode == 5) {
        if (e->rlen >= 7) {
            size_t flen = ((size_t)e->rbuf[5] << 8) | e->rbuf[6];
            if (e->rlen >= 7 + flen) return 1;
        }
        return 0;
    }
    /* A whole record is already in the input: no socket event will come for it. */
    if (e->rs == RS_REC && e->rlen >= 5 && e->hdr_done) {
        int l = hdr_len(e->rbuf);
        return l && e->rlen >= 5 + (size_t)l;
    }
    return 0;
}

void tr_venc_close(struct transport *t) {
    enc_free(t->enc);
    t->enc = NULL;
}

void tr_venc_test_set_vmess_counts(struct transport *t, uint32_t tx_cnt, uint32_t rx_cnt) {
    struct venc *e = t ? t->enc : NULL;
    if (!e) return;
    e->vm_tx_count = tx_cnt;
    e->vm_rx_count = rx_cnt;
}
