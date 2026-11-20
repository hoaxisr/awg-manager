/* Where the trusted roots for security=tls come from.
 *
 * certverify.c reads ONE PEM bundle per process. Which file that is: --ca on the command line,
 * then SSL_CERT_FILE, then the first bundle that exists among the usual places — Entware's
 * ca-bundle package first, then the common Linux distributions. Nothing found leaves the
 * path to certverify's own default, which then fails with "no root store" for security=tls nodes
 * (Reality and none do not need roots).
 */
#define _GNU_SOURCE
#include <stdlib.h>
#include <unistd.h>
#include <pthread.h>

#include "roots.h"

/* Test seam and --ca: set before the first TLS connection. Tests include this file to reach
 * it directly (tests/vlessmatch.c). */
static const char *g_cert_roots;

static const char *const DEFAULT_BUNDLES[] = {
    "/opt/etc/ssl/certs/ca-certificates.crt",  /* Entware ca-bundle */
    "/opt/etc/ssl/cert.pem",                   /* Entware ca-bundle, link to the above */
    "/etc/ssl/certs/ca-certificates.crt",      /* Debian, Ubuntu, Alpine, Arch, OpenWrt */
    "/etc/pki/tls/certs/ca-bundle.crt",        /* Fedora, RHEL */
    "/etc/ssl/ca-bundle.pem",                  /* openSUSE */
    "/etc/ssl/cert.pem",
    NULL,
};

static const char *g_default;
static pthread_once_t g_default_once = PTHREAD_ONCE_INIT;

static void default_pick(void) {
    const char *env = getenv("SSL_CERT_FILE");
    if (env && env[0] && access(env, R_OK) == 0) { g_default = env; return; }
    for (size_t i = 0; DEFAULT_BUNDLES[i]; i++)
        if (access(DEFAULT_BUNDLES[i], R_OK) == 0) { g_default = DEFAULT_BUNDLES[i]; return; }
}

void tls_set_cert_roots(const char *path) { g_cert_roots = path && path[0] ? path : NULL; }

const char *tls_cert_roots(void) {
    if (g_cert_roots) return g_cert_roots;
    pthread_once(&g_default_once, default_pick);
    return g_default;
}
