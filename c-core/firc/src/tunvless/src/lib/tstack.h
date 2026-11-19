#ifndef TUNVLESS_TSTACK_H
#define TUNVLESS_TSTACK_H
#include <link.h>
#include <pthread.h>
#include <stddef.h>
#include <unistd.h>

#define TSTACK_SLACK (16 * 1024)

static int tstack_tls_one(struct dl_phdr_info *i, size_t n, void *ud) {
    (void)n;
    for (ElfW(Half) k = 0; k < i->dlpi_phnum; k++)
        if (i->dlpi_phdr[k].p_type == PT_TLS)
            *(size_t *)ud += i->dlpi_phdr[k].p_memsz + i->dlpi_phdr[k].p_align;
    return 0;
}

static inline size_t tstack_tls(void) {
    size_t s = 0;
    dl_iterate_phdr(tstack_tls_one, &s);
    return s;
}

static inline int tstack_set(pthread_attr_t *a, size_t usable) {
    long pg = sysconf(_SC_PAGESIZE);
    size_t p = pg > 0 ? (size_t)pg : 4096;
    size_t s = usable + tstack_tls() + TSTACK_SLACK;
    return pthread_attr_setstacksize(a, (s + p - 1) / p * p);
}

#endif
