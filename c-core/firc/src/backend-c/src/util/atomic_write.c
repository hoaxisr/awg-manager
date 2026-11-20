#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/atomic_write.h"

#include <errno.h>
#include <fcntl.h>
#include <libgen.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <unistd.h>

int firc_mkstemp_cloexec(char *tmpl)
{
    return mkostemp(tmpl, O_CLOEXEC);
}

firc_err_t firc_atomic_write(const char *path, const char *buf, size_t len)
{
    /* mkstemp, not a pid-based name: open(O_CREAT) without O_EXCL would follow a planted symlink */
    char tmp_path[4096];
    int n = snprintf(tmp_path, sizeof(tmp_path), "%s.tmp.XXXXXX", path);
    if (n < 0 || (size_t)n >= sizeof(tmp_path)) {
        return FIRC_ERR_INVAL;
    }

    int fd = firc_mkstemp_cloexec(tmp_path);
    if (fd < 0) {
        return firc_err_from_errno(errno);
    }
    /* belt-and-braces: mkstemp already creates at 0600; rename doesn't carry a mode over on its own */
    firc_err_t err = FIRC_OK;
    if (fchmod(fd, 0600) != 0) {
        err = firc_err_from_errno(errno);
        close(fd);
        unlink(tmp_path);
        return err;
    }
    size_t written = 0;
    while (written < len) {
        ssize_t rc = write(fd, buf + written, len - written);
        if (rc < 0) {
            if (errno == EINTR) {
                continue;
            }
            err = firc_err_from_errno(errno);
            close(fd);
            unlink(tmp_path);
            return err;
        }
        written += (size_t)rc;
    }
    if (fsync(fd) != 0 || close(fd) != 0) {
        err = firc_err_from_errno(errno);
        unlink(tmp_path);
        return err;
    }

    if (rename(tmp_path, path) != 0) {
        err = firc_err_from_errno(errno);
        unlink(tmp_path);
        return err;
    }

    /* fsync the directory so the rename is durable */
    char dir_buf[4096];
    snprintf(dir_buf, sizeof(dir_buf), "%s", path);
    const char *dir = dirname(dir_buf);
    int dfd = open(dir, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    if (dfd >= 0) {
        (void)fsync(dfd);
        close(dfd);
    }
    return FIRC_OK;
}
