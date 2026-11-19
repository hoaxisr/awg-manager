#ifndef FIRC_RAND_H
#define FIRC_RAND_H

#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

/* Fills buf from /dev/urandom; FIRC_ERR_SYS if it cannot be opened or read fully. */
firc_err_t firc_random_bytes(uint8_t *buf, size_t len);

#endif
