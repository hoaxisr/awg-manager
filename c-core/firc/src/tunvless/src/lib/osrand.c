/* Kernel randomness for kernels without getrandom(2).
 *
 * getrandom(2) appeared in Linux 3.17, while Entware's mips and mipsel targets support kernels
 * from 3.4. There the call fails with ENOSYS, and with it every Reality key, TLS handshake,
 * Vision padding and WebSocket mask. os_getrandom keeps the getrandom contract and reads
 * /dev/urandom instead once the kernel has said it does not know the call.
 *
 * The system call is made directly rather than through the libc wrapper, so the build does not
 * depend on the libc being new enough to have one: a toolchain whose kernel headers predate the
 * call (no SYS_getrandom) simply always reads /dev/urandom.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <unistd.h>
#include <sys/syscall.h>

#include "osrand.h"

#ifdef SYS_getrandom
static int g_nosys;                 /* getrandom answered ENOSYS once: do not ask again */
#endif
static int g_urandom = -1;
static pthread_once_t g_urandom_once = PTHREAD_ONCE_INIT;

static void urandom_open(void) {
    g_urandom = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
}

ssize_t os_getrandom(void *buf, size_t n, unsigned int flags) {
#ifdef SYS_getrandom
    if (!__atomic_load_n(&g_nosys, __ATOMIC_RELAXED)) {
        long r = syscall(SYS_getrandom, buf, n, flags);
        if (r >= 0 || errno != ENOSYS) return (ssize_t)r;
        __atomic_store_n(&g_nosys, 1, __ATOMIC_RELAXED);
    }
#else
    (void)flags;
#endif
    pthread_once(&g_urandom_once, urandom_open);
    if (g_urandom < 0) { errno = ENOSYS; return -1; }
    return read(g_urandom, buf, n);
}

int os_rand_seed(unsigned char *out, unsigned int n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = os_getrandom(out + got, n - got, 0);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (r == 0) return -1;
        got += (size_t)r;
    }
    return 0;
}
