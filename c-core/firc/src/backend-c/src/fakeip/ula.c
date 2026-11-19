#include "firc/fakeip_ula.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/atomic_write.h"
#include "firc/log.h"
#include "firc/rand.h"

#define ULA_PREFIX_LEN 48

/* fd00::/8 is locally-assigned; fc00::/8 is reserved. */
static bool is_local_ula_48(const firc_ip_t *a, uint8_t prefix_len) {
    if (a->len != 16 || prefix_len != ULA_PREFIX_LEN) { return false; }
    if (a->b[0] != 0xfd) { return false; }
    return firc_ip_aligned(a, prefix_len);
}

/* Absent vs unusable must stay distinct: a single access() can't tell, the file can appear in between. */
typedef enum {
    PREFIX_OK,
    PREFIX_ABSENT, /* no file */
    PREFIX_BAD,    /* present but unusable */
    PREFIX_ERROR,  /* could not read it */
} prefix_read_t;

static prefix_read_t read_prefix_state(const char *path, firc_ip_t *out, uint8_t *prefix_out);

static prefix_read_t read_prefix_state(const char *path, firc_ip_t *out, uint8_t *prefix_out) {
    FILE *f = fopen(path, "re");
    if (f == NULL) {
        /* Only ENOENT means absent; any other failure isn't license to delete. */
        return errno == ENOENT ? PREFIX_ABSENT : PREFIX_ERROR;
    }
    char line[128];
    char *got = fgets(line, sizeof(line), f);
    bool read_failed = got == NULL && ferror(f) != 0;
    fclose(f);
    if (got == NULL) { return read_failed ? PREFIX_ERROR : PREFIX_BAD; }

    size_t n = strlen(line);
    while (n > 0 && (line[n - 1] == '\n' || line[n - 1] == '\r' || line[n - 1] == ' ' ||
                     line[n - 1] == '\t')) {
        line[--n] = '\0';
    }

    firc_ip_t base;
    uint8_t plen;
    if (!firc_ip_parse_cidr(line, &base, &plen)) { return PREFIX_BAD; }
    if (!is_local_ula_48(&base, plen)) { return PREFIX_BAD; }
    *out = base;
    *prefix_out = plen;
    return PREFIX_OK;
}

/* replace: false = exclusive create, true = overwrite an unusable file. */
static firc_err_t write_prefix(const char *path, const firc_ip_t *base, bool replace) {
    char text[64];
    if (inet_ntop(AF_INET6, base->b, text, sizeof(text)) == NULL) { return FIRC_ERR_SYS; }

    /* mkstemp: unique name + O_EXCL, immune to races and planted symlinks. */
    char tmp[512];
    if (snprintf(tmp, sizeof(tmp), "%s.tmpXXXXXX", path) >= (int)sizeof(tmp)) {
        return FIRC_ERR_INVAL;
    }
    int fd = firc_mkstemp_cloexec(tmp);
    if (fd < 0) { return FIRC_ERR_SYS; }
    if (fchmod(fd, 0600) != 0) {
        close(fd);
        unlink(tmp);
        return FIRC_ERR_SYS;
    }

    char line[80];
    int n = snprintf(line, sizeof(line), "%s/%d\n", text, ULA_PREFIX_LEN);
    if (n < 0 || n >= (int)sizeof(line)) {
        close(fd);
        unlink(tmp);
        return FIRC_ERR_INVAL;
    }

    firc_err_t err = FIRC_OK;
    ssize_t off = 0;
    while (off < n) {
        ssize_t w = write(fd, line + off, (size_t)(n - off));
        if (w < 0) {
            if (errno == EINTR) { continue; }
            err = FIRC_ERR_SYS;
            break;
        }
        off += w;
    }
    /* fsync before link, or a crash leaves the name pointing at unwritten data. */
    if (err == FIRC_OK && fsync(fd) != 0) { err = FIRC_ERR_SYS; }
    if (close(fd) != 0 && err == FIRC_OK) { err = FIRC_ERR_SYS; }
    if (err != FIRC_OK) {
        unlink(tmp);
        return err;
    }

    /* link, not rename: rename would let two racing creators both "succeed"; EEXIST instead names a winner. */
    if (replace) { (void)unlink(path); }
    if (link(tmp, path) != 0) {
        int e = errno;
        unlink(tmp);
        return e == EEXIST ? FIRC_ERR_EXIST : FIRC_ERR_SYS;
    }
    unlink(tmp);

    char dir[512];
    snprintf(dir, sizeof(dir), "%s", path);
    char *slash = strrchr(dir, '/');
    int dfd = open(slash != NULL ? (*slash = '\0', dir) : ".", O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    if (dfd >= 0) {
        (void)fsync(dfd);
        close(dfd);
    }
    return FIRC_OK;
}

firc_err_t firc_fakeip_ula_load(const char *path, firc_ip_t *base_out, uint8_t *prefix_out) {
    if (path == NULL || base_out == NULL || prefix_out == NULL) { return FIRC_ERR_INVAL; }

    /* Loops: the file can appear/disappear as other instances race it; each turn re-reads first. */
    bool shouted = false;
    for (int attempt = 0; attempt < 8; attempt++) {
        prefix_read_t state = read_prefix_state(path, base_out, prefix_out);
        if (state == PREFIX_OK) { return FIRC_OK; }
        if (state == PREFIX_ERROR) {
            continue;
        }

        bool replacing = state == PREFIX_BAD;
        if (replacing && !shouted) {
            FIRC_WARN("unusable IPv6 pool prefix in %s: generating a new one, "
                       "clients holding a fake IPv6 address will lose it",
                       path);
            shouted = true;
        }

        firc_ip_t base;
        memset(&base, 0, sizeof(base));
        base.len = 16;
        /* 0xfd + 40 random bits (RFC 4193 Global ID); bytes 6..15 stay zero. */
        uint8_t gid[5];
        if (firc_random_bytes(gid, sizeof(gid)) != FIRC_OK) { return FIRC_ERR_SYS; }
        base.b[0] = 0xfd;
        memcpy(&base.b[1], gid, sizeof(gid));

        firc_err_t err = write_prefix(path, &base, replacing);
        if (err == FIRC_OK) {
            *base_out = base;
            *prefix_out = ULA_PREFIX_LEN;
            return FIRC_OK;
        }
        if (err != FIRC_ERR_EXIST) { return err; }
    }
    return FIRC_ERR_SYS;
}
