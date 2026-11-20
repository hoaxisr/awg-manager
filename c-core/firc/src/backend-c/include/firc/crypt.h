#ifndef FIRC_CRYPT_H
#define FIRC_CRYPT_H

#include <stddef.h>

#include "firc/err.h"

/* crypt(3)-style hash of password with the algorithm/salt/rounds encoded in salt; out needs >= 128 bytes */
firc_err_t firc_crypt_password(const char *password, const char *salt, char *out, size_t out_len);

#endif /* FIRC_CRYPT_H */
