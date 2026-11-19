#ifndef FIRC_INSTANCE_LOCK_H
#define FIRC_INSTANCE_LOCK_H

#include "firc/err.h"

/* Takes a flock on path (0600); FIRC_ERR_EXIST if held elsewhere. *fd_out is the held fd, -1 on failure. */
firc_err_t firc_instance_lock(const char *path, int *fd_out);
/* Releases the lock. */
void firc_instance_unlock(int fd);

#endif
