#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/kmod.h"

#include <errno.h>
#include <fcntl.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/utsname.h>
#include <unistd.h>

#include "firc/log.h"

/* init_module, not finit_module: the mipsel target's 3.4 kernel headers may lack the second. */
static bool load_module_file(int fd, int *err_out) {
    struct stat st;
    if (fstat(fd, &st) != 0 || st.st_size <= 0 || st.st_size > (off_t)(4 * 1024 * 1024)) {
        *err_out = EINVAL;
        return false;
    }
    size_t len = (size_t)st.st_size;
    char *image = malloc(len);
    if (image == NULL) {
        *err_out = ENOMEM;
        return false;
    }
    size_t got = 0;
    while (got < len) {
        ssize_t n = read(fd, image + got, len - got);
        if (n <= 0) { break; }
        got += (size_t)n;
    }
    bool ok = false;
    if (got != len) {
        *err_out = EIO;
    } else if (syscall(SYS_init_module, image, len, "") == 0) {
        ok = true;
    } else {
        *err_out = errno;
    }
    free(image);
    return ok;
}

size_t firc_kmod_load(const char *dir, const char *const *mods, size_t n, const char *why) {
    char base[320];
    if (dir == NULL) {
        struct utsname u;
        if (uname(&u) != 0) { return 0; }
        snprintf(base, sizeof(base), "/lib/modules/%s", u.release);
        dir = base;
    }
    size_t loaded = 0;
    for (size_t i = 0; i < n; i++) {
        char path[400];
        if (snprintf(path, sizeof(path), "%s/%s.ko", dir, mods[i]) >= (int)sizeof(path)) { continue; }
        int fd = open(path, O_RDONLY | O_CLOEXEC);
        if (fd < 0) { continue; }
        int err = 0;
        bool ok = load_module_file(fd, &err);
        close(fd);
        if (ok) {
            loaded++;
            FIRC_INFO("loaded kernel module %s for %s", mods[i], why);
        } else if (err != EEXIST) {
            FIRC_WARN("kernel module %s would not load (%s); %s may need it", mods[i], strerror(err), why);
        }
    }
    return loaded;
}

size_t firc_kmod_load_netfilter(const char *dir) {
    static const char *const mods[] = {"xt_multiport", "xt_addrtype", "xt_mac",    "xt_mark",
                                       "xt_connmark",  "xt_conntrack", "xt_tcpudp", "ip_set",
                                       "ip_set_hash_net", "xt_set"};
    return firc_kmod_load(dir, mods, sizeof(mods) / sizeof(mods[0]), "firc's rules");
}
