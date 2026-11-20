/* Kernel randomness that also works where getrandom(2) does not exist. Details in osrand.c. */
#ifndef TUNVLESS_OSRAND_H
#define TUNVLESS_OSRAND_H
#include <stddef.h>
#include <sys/types.h>

/* getrandom(2) contract: bytes written (possibly fewer than n) or -1 with errno set. Falls back
 * to /dev/urandom when the kernel does not have the system call. */
ssize_t os_getrandom(void *buf, size_t n, unsigned int flags);

/* Fill out completely: 0, or -1 when no source of randomness is available. Also wolfSSL's seed
 * source (CUSTOM_RAND_GENERATE_SEED in build/wolfssl/user_settings.h). */
int os_rand_seed(unsigned char *out, unsigned int n);

#endif
