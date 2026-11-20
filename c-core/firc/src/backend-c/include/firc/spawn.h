#ifndef FIRC_SPAWN_H
#define FIRC_SPAWN_H

#include "firc/err.h"

/* Runs `path arg` detached (own session, stdio on /dev/null); FIRC_OK once exec'd, else the exec errno. */
firc_err_t firc_spawn_detached(const char *path, const char *arg);

#define FIRC_SPAWN_MAX_ARGS 16

/* Runs path with argv (NULL-terminated, argv[0] included, at most FIRC_SPAWN_MAX_ARGS) detached like
 * firc_spawn_detached; FIRC_OK once exec'd, else the exec errno; FIRC_ERR_INVAL for too many. */
firc_err_t firc_spawn_argv(const char *path, char *const argv[]);

#endif
