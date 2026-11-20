/* Adapted from steer (github.com/splify2/steer, 70d11eb), GPL-3.0; changed 2026-11. */
#ifndef TUNVLESS_QCSSL_H
#define TUNVLESS_QCSSL_H
#include <stddef.h>
#include <stdint.h>

struct qcssl_check {
    const uint8_t *pins;
    size_t pin_n;
    int insecure;
    const char *name;
};

void *qcssl_ctx_client(int load_roots, const uint8_t *ca_pem, size_t ca_pem_n, const char *ca_file);
#ifdef QC_WITH_SERVER
void *qcssl_ctx_server(const uint8_t *cert_der, size_t cert_n, const char *chain_pem, size_t chain_pem_n,
                       const uint8_t *key_der, size_t key_n);
#endif
void  qcssl_ctx_free(void *ctx);

void *qcssl_new(void *ctx, void *conn_ref, const char *sni, const char *alpn, int server, int prefer_chacha,
                const struct qcssl_check *chk);
void  qcssl_free(void *ssl);
const char *qcssl_why(void *ssl);
int   qcssl_alpn_check(void *ssl, const char *alpn);
unsigned qcssl_cipher(void *ssl);

#endif
