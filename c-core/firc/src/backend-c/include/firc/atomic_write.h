#ifndef FIRC_ATOMIC_WRITE_H
#define FIRC_ATOMIC_WRITE_H

#include <stddef.h>

#include "firc/err.h"

/* mkstemp whose fd is close-on-exec from the start. */
int firc_mkstemp_cloexec(char *tmpl);

/* Writes buf to path through a 0600 temp file, fsync and rename, then fsyncs the directory. */
firc_err_t firc_atomic_write(const char *path, const char *buf, size_t len);

#endif
