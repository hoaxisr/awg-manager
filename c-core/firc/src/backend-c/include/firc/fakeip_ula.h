#ifndef FIRC_FAKEIP_ULA_H
#define FIRC_FAKEIP_ULA_H

#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip_addr.h"

/* Reads the prefix from `path`, generating and persisting one if absent or unusable; FIRC_ERR_SYS on failure. */
firc_err_t firc_fakeip_ula_load(const char *path, firc_ip_t *base_out, uint8_t *prefix_out);

#endif /* FIRC_FAKEIP_ULA_H */
