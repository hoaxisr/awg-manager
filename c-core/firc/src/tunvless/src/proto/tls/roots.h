/* Where the trusted roots come from. Details in roots.c. */
#ifndef STEER_ROOTS_H
#define STEER_ROOTS_H

/* PEM bundle for auth.roots of tls13_handshake_auth (proto/transport/trsec.c); NULL leaves the
 * choice to certverify's default. */
const char *tls_cert_roots(void);

/* --ca: use this bundle instead of the defaults. */
void tls_set_cert_roots(const char *path);

#endif
