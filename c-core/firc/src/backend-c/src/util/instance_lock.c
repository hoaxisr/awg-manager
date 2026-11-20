#include "firc/instance_lock.h"

#include <errno.h>
#include <fcntl.h>
#include <sys/file.h>
#include <unistd.h>

firc_err_t firc_instance_lock(const char *path, int *fd_out) {
    if (fd_out == NULL) { return FIRC_ERR_INVAL; }
    *fd_out = -1;
    if (path == NULL) { return FIRC_ERR_INVAL; }
    /* O_CLOEXEC: forked children (socat, tunvless) must not inherit the lock. */
    int fd = open(path, O_RDWR | O_CREAT | O_CLOEXEC, 0600);
    /* A read-only run dir must not stop the daemon: flock needs no writable descriptor. */
    if (fd < 0 && (errno == EROFS || errno == EACCES)) { fd = open(path, O_RDONLY | O_CLOEXEC); }
    if (fd < 0) { return firc_err_from_errno(errno); }
    if (flock(fd, LOCK_EX | LOCK_NB) != 0) {
        int e = errno;
        close(fd);
        return e == EWOULDBLOCK ? FIRC_ERR_EXIST : firc_err_from_errno(e);
    }
    *fd_out = fd;
    return FIRC_OK;
}

void firc_instance_unlock(int fd) {
    if (fd >= 0) { close(fd); }
}
