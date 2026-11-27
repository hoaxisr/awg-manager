/* TLS 1.2 client fallback for security=tls nodes that do not support TLS 1.3.
 *
 * Kept behind an opaque pointer (struct tls13.t12) with no wolfSSL types in this header, matching
 * scrypto.h / tls13.h so callers and unit tests never see wolfSSL headers.
 */
#ifndef STEER_TLS12_H
#define STEER_TLS12_H
#include <stddef.h>

struct tls13;
struct cert_policy;

/* Runs a TLS 1.2 client handshake over the already-connected fd (preserving t->fm).
 * Verifies the server certificate chain via cert_verify_chain() (shared root store, chain cache,
 * hostname/vcn, pcs/pks pins, or insecure). On success populates t->t12, t->alpn and t->ready.
 * On failure frees all wolfSSL state and leaves t->t12 == NULL, t->ready == 0. */
int tls12_handshake(struct tls13 *t, int fd, const char *host, const char *alpn,
                    const char *roots, const struct cert_policy *pol, int record_fragment);

int tls12_write(void *p, const unsigned char *data, size_t n);
int tls12_read(void *p, unsigned char *out, size_t cap, size_t *got);
int tls12_seal(void *p, const unsigned char *d, size_t n,
               unsigned char *out, size_t cap, size_t *out_n);
int tls12_has_record(const void *p);
size_t tls12_pending(const void *p);
size_t tls12_take_pending(void *p, unsigned char *out, size_t cap);
void tls12_free(void *p);

#endif
