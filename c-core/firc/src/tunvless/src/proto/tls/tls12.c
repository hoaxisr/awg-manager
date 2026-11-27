/* TLS 1.2 client fallback for security=tls nodes that do not speak TLS 1.3.
 *
 * Uses the already-linked wolfSSL library (built with NO_OLD_TLS, so SSLv3/TLS 1.0/1.1 stay off)
 * over custom WOLFSSL_USER_IO callbacks (supporting both raw sockets and finalmask chains).
 * Certificate verification delegates to cert_verify_chain() in certverify.c so TLS 1.2 shares the
 * exact same root store, verified-chain cache, hostname/IP/vcn rules and pcs/pks pin policy as
 * TLS 1.3 without parsing the CA bundle again.
 */
#define _GNU_SOURCE
#include "tls12.h"
#include "tls13.h"

#ifdef STEER_TLS12_STUB
/* Stub for unit tests that link trsec.c without tls13.c / certverify.c / wolfSSL. */
int tls12_handshake(struct tls13 *t, int fd, const char *host, const char *alpn,
                    const char *roots, const struct cert_policy *pol, int record_fragment) {
    (void)t; (void)fd; (void)host; (void)alpn; (void)roots; (void)pol; (void)record_fragment;
    return TLS13_ENOKEYSHARE;
}
#else

#include <arpa/inet.h>
#include <errno.h>
#include <limits.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>

#include <wolfssl/ssl.h>
#include <wolfssl/error-ssl.h>

#include "certverify.h"
#include "fmlink.h"
#include "transport.h"

/* Modern TLS 1.2 AEAD cipher suites only (ECDHE forward secrecy; no CBC, no static RSA).
 * Prefer AES-GCM on 64-bit targets with AES acceleration and ChaCha20-Poly1305 on 32-bit routers
 * (matching reality.c and qcssl.c). */
#if defined(__x86_64__) || defined(__aarch64__)
static const char TLS12_SUITES[] =
    "ECDHE-ECDSA-AES128-GCM-SHA256:"
    "ECDHE-RSA-AES128-GCM-SHA256:"
    "ECDHE-ECDSA-CHACHA20-POLY1305:"
    "ECDHE-RSA-CHACHA20-POLY1305:"
    "ECDHE-ECDSA-AES256-GCM-SHA384:"
    "ECDHE-RSA-AES256-GCM-SHA384";
#else
static const char TLS12_SUITES[] =
    "ECDHE-ECDSA-CHACHA20-POLY1305:"
    "ECDHE-RSA-CHACHA20-POLY1305:"
    "ECDHE-ECDSA-AES128-GCM-SHA256:"
    "ECDHE-RSA-AES128-GCM-SHA256:"
    "ECDHE-ECDSA-AES256-GCM-SHA384:"
    "ECDHE-RSA-AES256-GCM-SHA384";
#endif

/* Default ALPN list when the transport requires no specific protocol (e.g. tcp / trojan). */
static const char TLS12_DEFAULT_ALPN[] = "h2,http/1.1";

struct tls12 {
    WOLFSSL_CTX *ctx;
    WOLFSSL *ssl;
    int fd;
    struct fm_link *fm;
    int in_hs;
    int io_err;
    int record_fragment;
    int ch_sent;
    /* Handshake-only certificate check inputs; cleared once wolfSSL_connect returns. */
    const char *host;
    const char *roots;
    const struct cert_policy *pol;
    int verified;
    int verify_rc;
    /* Optional output capture buffer used only during tls12_seal(). */
    unsigned char *seal_buf;
    size_t seal_cap;
    size_t seal_n;
};

static pthread_once_t g_tls12_init_once = PTHREAD_ONCE_INIT;
static int g_tls12_init_rc = -1;

static void tls12_lib_init(void) {
    g_tls12_init_rc = wolfSSL_Init() == WOLFSSL_SUCCESS ? 0 : -1;
}

/* Matches reality.c:sni_is_ip() — RFC 6066 §3 forbids bare, bracketed, or scoped IP literals in SNI. */
static int is_ip_literal(const char *s) {
    unsigned char ip[16];
    if (inet_pton(AF_INET, s, ip) == 1 || inet_pton(AF_INET6, s, ip) == 1) return 1;
    size_t len = strlen(s);
    const char *start = s;
    if (len >= 2 && s[0] == '[' && s[len - 1] == ']') {
        start++;
        len -= 2;
    }
    const char *pct = memchr(start, '%', len);
    if (pct && pct > start) len = (size_t)(pct - start);
    if (start == s && !pct) return 0;
    if (len == 0 || len >= 64) return 0;
    char tmp[64];
    memcpy(tmp, start, len);
    tmp[len] = '\0';
    return inet_pton(AF_INET, tmp, ip) == 1 || inet_pton(AF_INET6, tmp, ip) == 1;
}

static int raw_send_all(struct tls12 *s, const unsigned char *buf, size_t n) {
    if (s->fm) {
        ssize_t w = fm_send(s->fm, s->fd, buf, n, 0);
        if (w < 0) {
            s->io_err = (int)w;
            return -1;
        }
        return 0;
    }
    size_t sent = 0;
    while (sent < n) {
        ssize_t w = send(s->fd, buf + sent, n - sent, MSG_NOSIGNAL);
        if (w < 0 && errno == EINTR) continue;
        if (w <= 0) {
            if (w < 0 && s->in_hs && (errno == EAGAIN || errno == EWOULDBLOCK))
                s->io_err = TLS13_ETIMEOUT;
            else
                s->io_err = TLS13_EIO;
            return -1;
        }
        sent += (size_t)w;
    }
    return 0;
}

static int send_fragmented_hello(struct tls12 *s, const unsigned char *buf, size_t n) {
    size_t payload_n = n - 5;
    size_t split = payload_n / 2;
    size_t hlen = s->host ? strlen(s->host) : 0;
    if (hlen >= 4) {
        const unsigned char *pos = memmem(buf + 5, payload_n, s->host, hlen);
        if (pos) split = (size_t)(pos - (buf + 5)) + hlen / 2;
    }
    if (split < 1) split = 1;
    if (split >= payload_n) split = payload_n - 1;
    size_t part2 = payload_n - split;

    unsigned char *frag = malloc(n + 5);
    if (!frag) {
        s->io_err = TLS13_EIO;
        return -1;
    }
    memcpy(frag, buf, 3);
    frag[3] = (unsigned char)(split >> 8);
    frag[4] = (unsigned char)(split & 0xFF);
    memcpy(frag + 5, buf + 5, split);
    memcpy(frag + 5 + split, buf, 3);
    frag[5 + split + 3] = (unsigned char)(part2 >> 8);
    frag[5 + split + 4] = (unsigned char)(part2 & 0xFF);
    memcpy(frag + 10 + split, buf + 5 + split, part2);

    size_t f1 = 5 + split;
    int rc = raw_send_all(s, frag, f1);
    if (!rc) rc = raw_send_all(s, frag + f1, 5 + part2);
    free(frag);
    return rc;
}

static int tls12_io_recv(WOLFSSL *ssl, char *buf, int sz, void *ctx) {
    (void)ssl;
    struct tls12 *s = ctx;
    if (!s || sz <= 0) return WOLFSSL_CBIO_ERR_GENERAL;

    int flags = s->in_hs ? 0 : MSG_DONTWAIT;
    for (;;) {
        ssize_t r = s->fm ? fm_recv(s->fm, s->fd, buf, (size_t)sz, flags, 0)
                          : recv(s->fd, buf, (size_t)sz, flags);
        if (r > 0) return (int)r;
        if (r == 0) {
            s->io_err = TLS13_ECLOSED;
            return WOLFSSL_CBIO_ERR_CONN_CLOSE;
        }
        if (errno == EINTR) continue;
        if (s->fm && errno == EPROTO) {
            s->io_err = TR_EMASK;
            return WOLFSSL_CBIO_ERR_GENERAL;
        }
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            if (s->in_hs) {
                s->io_err = TLS13_ETIMEOUT;
                return WOLFSSL_CBIO_ERR_TIMEOUT;
            }
            return WOLFSSL_CBIO_ERR_WANT_READ;
        }
        s->io_err = TLS13_EIO;
        return WOLFSSL_CBIO_ERR_GENERAL;
    }
}

static int tls12_io_send(WOLFSSL *ssl, char *buf, int sz, void *ctx) {
    (void)ssl;
    struct tls12 *s = ctx;
    if (!s || sz < 0) return WOLFSSL_CBIO_ERR_GENERAL;
    if (sz == 0) return 0;

    if (s->seal_buf) {
        if ((size_t)sz > s->seal_cap - s->seal_n) {
            s->io_err = TLS13_ETOOBIG;
            return WOLFSSL_CBIO_ERR_GENERAL;
        }
        memcpy(s->seal_buf + s->seal_n, buf, (size_t)sz);
        s->seal_n += (size_t)sz;
        return sz;
    }

    const unsigned char *u = (const unsigned char *)buf;
    if (s->record_fragment && s->in_hs && !s->ch_sent &&
        (size_t)sz > 5 + 16 && u[0] == 0x16 && u[5] == 0x01 &&
        (((size_t)u[3] << 8) | u[4]) == (size_t)sz - 5) {
        s->ch_sent = 1;
        if (send_fragmented_hello(s, u, (size_t)sz) != 0)
            return WOLFSSL_CBIO_ERR_GENERAL;
        return sz;
    }
    if (s->in_hs) s->ch_sent = 1;
    if (raw_send_all(s, u, (size_t)sz) != 0)
        return WOLFSSL_CBIO_ERR_GENERAL;
    return sz;
}

static int tls12_verify_cb(int preverify, WOLFSSL_X509_STORE_CTX *store) {
    (void)preverify;
    WOLFSSL *ssl = wolfSSL_X509_STORE_CTX_get_ex_data(store, wolfSSL_get_ex_data_X509_STORE_CTX_idx());
    struct tls12 *s = ssl ? wolfSSL_get_app_data(ssl) : NULL;
    if (!s) return 0;

    /* Defer validation until wolfSSL finishes walking intermediates and invokes us on the leaf
     * (error_depth == 0), where store->certs[0..totalCerts-1] holds the complete wire chain. */
    if (wolfSSL_X509_STORE_CTX_get_error_depth(store) > 0) return 1;

    s->verified = 0;
    if (s->pol && s->pol->insecure) {
        s->verified = 1;
        return 1;
    }
    if (!store->certs || store->totalCerts <= 0) {
        s->verify_rc = CERTV_EPARSE;
        return 0;
    }
    size_t count = (size_t)store->totalCerts;
    if (count > 16) count = 16;
    const unsigned char *der[16];
    size_t der_n[16];
    for (size_t i = 0; i < count; i++) {
        if (!store->certs[i].buffer || store->certs[i].length == 0) {
            s->verify_rc = CERTV_EPARSE;
            return 0;
        }
        der[i] = store->certs[i].buffer;
        der_n[i] = (size_t)store->certs[i].length;
    }

    int vrc = cert_verify_chain(der, der_n, count, s->host, s->roots, s->pol);
    if (vrc != 0) {
        s->verify_rc = vrc;
        return 0;
    }
    s->verified = 1;
    return 1;
}

static int map_connect_error(const struct tls12 *s, int ret) {
    if (s->verify_rc != 0) {
        tls13_set_verify_reason(cert_verify_strerror(s->verify_rc));
        return TLS13_ECERT;
    }
    if (s->io_err != 0) return s->io_err;

    int err = wolfSSL_get_error(s->ssl, ret);
    switch (err) {
        case VERIFY_CERT_ERROR:
        case ASN_BEFORE_DATE_E:
        case ASN_AFTER_DATE_E:
        case ASN_NO_SIGNER_E:
        case ASN_SELF_SIGNED_E:
        case NOT_CA_ERROR:
        case MAX_CHAIN_ERROR:
        case KEYUSE_SIGNATURE_E:
        case KEYUSE_ENCIPHER_E:
        case EXTKEYUSE_AUTH_E:
        case DOMAIN_NAME_MISMATCH:
        case IPADDR_MISMATCH:
        case NO_PEER_CERT:
        case NO_PEER_KEY:
            tls13_set_verify_reason(cert_verify_strerror(CERTV_ECHAIN));
            return TLS13_ECERT;
        case ASN_PARSE_E:
        case ASN_VERSION_E:
            tls13_set_verify_reason(cert_verify_strerror(CERTV_EPARSE));
            return TLS13_ECERT;
        case ASN_SIG_CONFIRM_E:
        case VERIFY_SIGN_ERROR:
        case SIG_VERIFY_E:
        case RSA_PAD_E:
        case BAD_PADDING_E:
        case PSS_SALTLEN_E:
            tls13_set_verify_reason(cert_verify_strerror(CERTV_ESIG));
            return TLS13_ECERT;
        case VERIFY_FINISHED_ERROR:
            return TLS13_EFINISHED;
        case MATCH_SUITE_ERROR:
        case UNSUPPORTED_SUITE:
        case WOLFSSL_ERROR_SYSCALL:
            /* With OPENSSL_EXTRA wolfSSL_get_error() maps MATCH_SUITE_ERROR to
             * WOLFSSL_ERROR_SYSCALL; socket I/O errors are already captured in s->io_err above. */
            return TLS13_EBADSUITE;
        case FATAL_ERROR: {
            WOLFSSL_ALERT_HISTORY ah;
            memset(&ah, 0, sizeof(ah));
            if (wolfSSL_get_alert_history(s->ssl, &ah) == WOLFSSL_SUCCESS) {
                if (ah.last_rx.code == 40) return TLS13_EBADSUITE; /* handshake_failure */
                if (ah.last_rx.code == 0) return TLS13_ECLOSED;    /* close_notify */
            }
            return TLS13_EBADREC;
        }
        case SOCKET_PEER_CLOSED_E:
        case WOLFSSL_ERROR_ZERO_RETURN:
            return TLS13_ECLOSED;
        default:
            return TLS13_EBADREC;
    }
}

int tls12_handshake(struct tls13 *t, int fd, const char *host, const char *alpn,
                    const char *roots, const struct cert_policy *pol, int record_fragment) {
    if (!t || fd < 0 || !host || !host[0]) {
        tls13_set_verify_reason("server sent no hostname to verify");
        return TLS13_ECERT;
    }
    tls13_set_verify_reason("");

    pthread_once(&g_tls12_init_once, tls12_lib_init);
    if (g_tls12_init_rc != 0) return TLS13_ECRYPTO;

    struct fm_link *fm = t->fm;
    memset(t, 0, sizeof(*t));
    t->fd = fd;
    t->fm = fm;

    struct tls12 *s = calloc(1, sizeof(*s));
    if (!s) return TLS13_EIO;
    s->fd = fd;
    s->fm = fm;
    s->in_hs = 1;
    s->record_fragment = record_fragment;
    s->host = host;
    s->roots = roots;
    s->pol = pol;

    s->ctx = wolfSSL_CTX_new(wolfTLSv1_2_client_method());
    if (!s->ctx) {
        tls12_free(s);
        return TLS13_ECRYPTO;
    }
    wolfSSL_CTX_SetIORecv(s->ctx, tls12_io_recv);
    wolfSSL_CTX_SetIOSend(s->ctx, tls12_io_send);

    s->ssl = wolfSSL_new(s->ctx);
    if (!s->ssl) {
        tls12_free(s);
        return TLS13_ECRYPTO;
    }
    wolfSSL_set_app_data(s->ssl, s);
    wolfSSL_SetIOReadCtx(s->ssl, s);
    wolfSSL_SetIOWriteCtx(s->ssl, s);
    /* No CA roots are loaded into s->ctx; all trust/pin/hostname/insecure enforcement runs once
     * on the leaf in tls12_verify_cb() via cert_verify_chain(). Using WOLFSSL_VERIFY_NONE skips
     * wolfSSL's intermediate AddCA() rejections (matching TLS 1.3 leaf-pin and spare-intermediate
     * handling) while WOLFSSL_ALWAYS_VERIFY_CB still invokes tls12_verify_cb() once on the leaf,
     * aborts with VERIFY_CERT_ERROR on return 0, enforces NO_PEER_CERT + post-connect s->verified,
     * and still authenticates the ServerKeyExchange signature against the leaf public key. */
    wolfSSL_set_verify(s->ssl, WOLFSSL_VERIFY_NONE, tls12_verify_cb);

    if (wolfSSL_set_cipher_list(s->ssl, TLS12_SUITES) != WOLFSSL_SUCCESS) {
        tls12_free(s);
        return TLS13_ECRYPTO;
    }
    if (!is_ip_literal(host)) {
        size_t hlen = strlen(host);
        if (hlen > 255) {
            tls13_set_verify_reason("server hostname is too long");
            tls12_free(s);
            return TLS13_ECERT;
        }
        if (wolfSSL_UseSNI(s->ssl, WOLFSSL_SNI_HOST_NAME, host, (unsigned short)hlen) != WOLFSSL_SUCCESS) {
            tls12_free(s);
            return TLS13_ECRYPTO;
        }
    }

    const char *alpn_list = (alpn && alpn[0]) ? alpn : TLS12_DEFAULT_ALPN;
    char alpn_buf[64];
    size_t alen = strlen(alpn_list);
    if (alen > 0 && alen < sizeof(alpn_buf)) {
        memcpy(alpn_buf, alpn_list, alen + 1);
        if (wolfSSL_UseALPN(s->ssl, alpn_buf, (unsigned int)alen, WOLFSSL_ALPN_CONTINUE_ON_MISMATCH) != WOLFSSL_SUCCESS) {
            tls12_free(s);
            return TLS13_ECRYPTO;
        }
    }

    int ret = wolfSSL_connect(s->ssl);
    s->host = NULL;
    s->roots = NULL;
    s->pol = NULL;
    if (ret != WOLFSSL_SUCCESS) {
        int rc = map_connect_error(s, ret);
        tls12_free(s);
        return rc;
    }
    if (!s->verified) {
        tls13_set_verify_reason("server sent no certificate");
        tls12_free(s);
        return TLS13_ECERT;
    }

    char *proto = NULL;
    unsigned short plen = 0;
    if (wolfSSL_ALPN_GetProtocol(s->ssl, &proto, &plen) == WOLFSSL_SUCCESS &&
        proto && plen > 0 && plen < sizeof(t->alpn)) {
        memcpy(t->alpn, proto, plen);
        t->alpn[plen] = '\0';
    }

    s->in_hs = 0;
    t->t12 = s;
    t->ready = 1;
    return 0;
}

int tls12_write(void *p, const unsigned char *data, size_t n) {
    struct tls12 *s = p;
    if (!s || !s->ssl) return TLS13_ESTATE;
    size_t off = 0;
    while (off < n) {
        size_t chunk = n - off;
        if (chunk > TLS13_MAX_PLAIN) chunk = TLS13_MAX_PLAIN;
        s->io_err = 0;
        int r = wolfSSL_write(s->ssl, data + off, (int)chunk);
        if (r > 0) {
            off += (size_t)r;
            continue;
        }
        return s->io_err ? s->io_err : TLS13_EIO;
    }
    return 0;
}

int tls12_seal(void *p, const unsigned char *d, size_t n,
               unsigned char *out, size_t cap, size_t *out_n) {
    struct tls12 *s = p;
    if (!s || !s->ssl) return TLS13_ESTATE;
    if (n == 0) {
        *out_n = 0;
        return 0;
    }
    /* Upper bound per TLS 1.2 AEAD record: 5 (hdr) + 8 (explicit IV) + 16 (tag) = 29 bytes. */
    size_t recs = (n - 1) / TLS13_MAX_PLAIN + 1;
    if (n > cap || recs > (cap - n) / 29) return TLS13_ETOOBIG;
    s->seal_buf = out;
    s->seal_cap = cap;
    s->seal_n = 0;
    int rc = 0;
    size_t off = 0;
    while (off < n) {
        size_t chunk = n - off;
        if (chunk > TLS13_MAX_PLAIN) chunk = TLS13_MAX_PLAIN;
        s->io_err = 0;
        int r = wolfSSL_write(s->ssl, d + off, (int)chunk);
        if (r > 0) {
            off += (size_t)r;
            continue;
        }
        rc = s->io_err ? s->io_err : TLS13_ECRYPTO;
        break;
    }
    size_t produced = s->seal_n;
    s->seal_buf = NULL;
    s->seal_cap = 0;
    s->seal_n = 0;
    if (rc) return rc;
    *out_n = produced;
    return 0;
}

int tls12_read(void *p, unsigned char *out, size_t cap, size_t *got) {
    struct tls12 *s = p;
    *got = 0;
    if (!s || !s->ssl) return TLS13_ESTATE;
    if (!cap) return 0;
    int want = cap > (size_t)INT_MAX ? INT_MAX : (int)cap;
    s->io_err = 0;
    int r = wolfSSL_read(s->ssl, out, want);
    if (r > 0) {
        *got = (size_t)r;
        return 0;
    }
    int err = wolfSSL_get_error(s->ssl, r);
    if (err == WOLFSSL_ERROR_WANT_READ || err == WOLFSSL_ERROR_WANT_WRITE)
        return 0;
    if (r == 0 || err == WOLFSSL_ERROR_ZERO_RETURN)
        return TLS13_ECLOSED;
    if (s->io_err) return s->io_err;
    return TLS13_EIO;
}

int tls12_has_record(const void *p) {
    const struct tls12 *s = p;
    return s && s->ssl && wolfSSL_pending(s->ssl) > 0;
}

size_t tls12_pending(const void *p) {
    const struct tls12 *s = p;
    if (!s || !s->ssl) return 0;
    int n = wolfSSL_pending(s->ssl);
    return n > 0 ? (size_t)n : 0;
}

size_t tls12_take_pending(void *p, unsigned char *out, size_t cap) {
    struct tls12 *s = p;
    if (!s || !s->ssl || !out || !cap) return 0;
    int pend = wolfSSL_pending(s->ssl);
    if (pend <= 0) return 0;
    size_t want_sz = (size_t)pend < cap ? (size_t)pend : cap;
    int want = want_sz > (size_t)INT_MAX ? INT_MAX : (int)want_sz;
    s->io_err = 0;
    int r = wolfSSL_read(s->ssl, out, want);
    return r > 0 ? (size_t)r : 0;
}

void tls12_free(void *p) {
    struct tls12 *s = p;
    if (!s) return;
    if (s->ssl) wolfSSL_free(s->ssl);
    if (s->ctx) wolfSSL_CTX_free(s->ctx);
    free(s);
}

#endif /* STEER_TLS12_STUB */
