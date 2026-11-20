/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>

#include <wolfssl/ssl.h>
#include <wolfssl/quic.h>
#include <wolfssl/error-ssl.h>
#include <wolfssl/wolfcrypt/sha256.h>
#include <ngtcp2/ngtcp2.h>
#include <ngtcp2/ngtcp2_crypto.h>
#include <ngtcp2/ngtcp2_crypto_wolfssl.h>

#include "qcssl.h"
#include "certverify.h"

#define QCSSL_ROOTS_MAX (1024 * 1024)
#define QCSSL_IDX 1

static const char SUITES_AES[] = "TLS13-AES128-GCM-SHA256:TLS13-AES256-GCM-SHA384:TLS13-CHACHA20-POLY1305-SHA256";
static const char SUITES_CHACHA[] = "TLS13-CHACHA20-POLY1305-SHA256:TLS13-AES128-GCM-SHA256:TLS13-AES256-GCM-SHA384";

struct qcssl_state {
    uint8_t *pins;
    size_t pin_n;
    const char *why;
};

static unsigned char *read_file(const char *path, size_t *n) {
    FILE *f = fopen(path, "rb");
    if (!f) return NULL;
    unsigned char *buf = malloc(QCSSL_ROOTS_MAX + 1);
    if (!buf) { fclose(f); return NULL; }
    size_t got = fread(buf, 1, QCSSL_ROOTS_MAX + 1, f);
    fclose(f);
    if (got == 0 || got > QCSSL_ROOTS_MAX) { free(buf); return NULL; }
    *n = got;
    return buf;
}

void *qcssl_ctx_client(int load_roots, const uint8_t *ca_pem, size_t ca_pem_n, const char *ca_file) {
    WOLFSSL_CTX *ctx = wolfSSL_CTX_new(wolfTLSv1_3_client_method());
    if (!ctx) return NULL;
    if (ngtcp2_crypto_wolfssl_configure_client_context(ctx) != 0) goto fail;
    if (!load_roots) return ctx;
    unsigned char *own = NULL;
    if (!ca_pem) {
        size_t n = 0;
        own = read_file(ca_file && ca_file[0] ? ca_file : CERTV_DEFAULT_ROOTS, &n);
        if (!own) goto fail;
        ca_pem = own;
        ca_pem_n = n;
    }
    int rc = wolfSSL_CTX_load_verify_buffer(ctx, ca_pem, (long)ca_pem_n, WOLFSSL_FILETYPE_PEM);
    free(own);
    if (rc != WOLFSSL_SUCCESS) goto fail;
    return ctx;
fail:
    wolfSSL_CTX_free(ctx);
    return NULL;
}

#ifdef QC_WITH_SERVER
void *qcssl_ctx_server(const uint8_t *cert_der, size_t cert_n, const char *chain_pem, size_t chain_pem_n,
                       const uint8_t *key_der, size_t key_n) {
    WOLFSSL_CTX *ctx = wolfSSL_CTX_new(wolfTLSv1_3_server_method());
    if (!ctx) return NULL;
    int crt = chain_pem
        ? wolfSSL_CTX_use_certificate_chain_buffer(ctx, (const unsigned char *)chain_pem, (long)chain_pem_n)
        : wolfSSL_CTX_use_certificate_buffer(ctx, cert_der, (long)cert_n, WOLFSSL_FILETYPE_ASN1);
    if (ngtcp2_crypto_wolfssl_configure_server_context(ctx) != 0 || crt != WOLFSSL_SUCCESS ||
        wolfSSL_CTX_use_PrivateKey_buffer(ctx, key_der, (long)key_n, WOLFSSL_FILETYPE_ASN1) != WOLFSSL_SUCCESS ||
        wolfSSL_CTX_UseClientSuites(ctx) != 0) {
        wolfSSL_CTX_free(ctx);
        return NULL;
    }
    return ctx;
}
#endif

void qcssl_ctx_free(void *ctx) {
    if (ctx) wolfSSL_CTX_free(ctx);
}

static int pin_match(const struct qcssl_state *v, const unsigned char *der, int len) {
    uint8_t h[32];
    if (!der || len <= 0 || wc_Sha256Hash(der, (word32)len, h) != 0) return 0;
    for (size_t i = 0; i < v->pin_n; i++)
        if (memcmp(h, v->pins + 32 * i, 32) == 0) return 1;
    return 0;
}

static int verify_cb(int ok, WOLFSSL_X509_STORE_CTX *st) {
    WOLFSSL *ssl = wolfSSL_X509_STORE_CTX_get_ex_data(st, wolfSSL_get_ex_data_X509_STORE_CTX_idx());
    struct qcssl_state *v = ssl ? wolfSSL_get_ex_data(ssl, QCSSL_IDX) : NULL;
    if (!v) return 0;
    if (v->pin_n) {
        if (wolfSSL_X509_STORE_CTX_get_error_depth(st) != 0) return 1;
        WOLFSSL_X509 *x = wolfSSL_X509_STORE_CTX_get_current_cert(st);
        int len = 0;
        const unsigned char *der = x ? wolfSSL_X509_get_der(x, &len) : NULL;
        if (pin_match(v, der, len)) return 1;
        v->why = "certificate pin mismatch";
        return 0;
    }
    if (ok) return 1;
    int err = wolfSSL_X509_STORE_CTX_get_error(st);
    if (err == DOMAIN_NAME_MISMATCH || err == IPADDR_MISMATCH)
        v->why = "certificate name mismatch";
    else
        v->why = "certificate chain not trusted";
    return 0;
}

static int is_ip(const char *s) {
    unsigned char ip[16];
    return inet_pton(AF_INET, s, ip) == 1 || inet_pton(AF_INET6, s, ip) == 1;
}

void *qcssl_new(void *ctx, void *conn_ref, const char *sni, const char *alpn, int server, int prefer_chacha,
                const struct qcssl_check *chk) {
    WOLFSSL *ssl = wolfSSL_new(ctx);
    if (!ssl) return NULL;
    struct qcssl_state *v = calloc(1, sizeof *v);
    if (!v || wolfSSL_set_ex_data(ssl, QCSSL_IDX, v) != WOLFSSL_SUCCESS) {
        free(v);
        wolfSSL_free(ssl);
        return NULL;
    }
    wolfSSL_set_app_data(ssl, conn_ref);
    if (server) wolfSSL_set_accept_state(ssl); else wolfSSL_set_connect_state(ssl);
    if (alpn && alpn[0] &&
        wolfSSL_UseALPN(ssl, (char *)alpn, (unsigned)strlen(alpn), WOLFSSL_ALPN_FAILED_ON_MISMATCH) != WOLFSSL_SUCCESS)
        goto fail;
    if (server) return ssl;
    if (wolfSSL_set_cipher_list(ssl, prefer_chacha ? SUITES_CHACHA : SUITES_AES) != WOLFSSL_SUCCESS) goto fail;
    if (sni && sni[0] && !is_ip(sni) &&
        wolfSSL_UseSNI(ssl, WOLFSSL_SNI_HOST_NAME, sni, (unsigned short)strlen(sni)) != WOLFSSL_SUCCESS)
        goto fail;
    if (chk && chk->pin_n) {
        v->pins = malloc(chk->pin_n * 32);
        if (!v->pins) goto fail;
        memcpy(v->pins, chk->pins, chk->pin_n * 32);
        v->pin_n = chk->pin_n;
        wolfSSL_set_verify(ssl, WOLFSSL_VERIFY_PEER, verify_cb);
        return ssl;
    }
    if (!chk || chk->insecure) {
        wolfSSL_set_verify(ssl, WOLFSSL_VERIFY_NONE, NULL);
        return ssl;
    }
    wolfSSL_set_verify(ssl, WOLFSSL_VERIFY_PEER, verify_cb);
    if (chk->name && chk->name[0]) {
        int rc = is_ip(chk->name) ? wolfSSL_check_ip_address(ssl, chk->name)
                                  : wolfSSL_check_domain_name(ssl, chk->name);
        if (rc != WOLFSSL_SUCCESS) goto fail;
    }
    return ssl;
fail:
    qcssl_free(ssl);
    return NULL;
}

void qcssl_free(void *ssl) {
    if (!ssl) return;
    struct qcssl_state *v = wolfSSL_get_ex_data(ssl, QCSSL_IDX);
    if (v) {
        free(v->pins);
        free(v);
    }
    wolfSSL_free(ssl);
}

const char *qcssl_why(void *ssl) {
    struct qcssl_state *v = ssl ? wolfSSL_get_ex_data(ssl, QCSSL_IDX) : NULL;
    return v ? v->why : NULL;
}

int qcssl_alpn_check(void *ssl, const char *alpn) {
    struct qcssl_state *v = wolfSSL_get_ex_data(ssl, QCSSL_IDX);
    char *got = NULL;
    unsigned short n = 0;
    if (!alpn || !alpn[0]) return 0;
    if (wolfSSL_ALPN_GetProtocol(ssl, &got, &n) == WOLFSSL_SUCCESS && got && n == strlen(alpn) &&
        memcmp(got, alpn, n) == 0)
        return 0;
    if (v) v->why = "server chose no matching alpn";
    return -1;
}

unsigned qcssl_cipher(void *ssl) {
    return ssl ? (unsigned)wolfSSL_get_current_cipher_suite(ssl) & 0xffffu : 0;
}
