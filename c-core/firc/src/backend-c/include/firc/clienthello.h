#ifndef FIRC_CLIENTHELLO_H
#define FIRC_CLIENTHELLO_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* Copies the first SNI host name, lowercased, into `out`; false if none, malformed, or it does not fit `cap`. */
bool firc_tls_client_hello_sni(const uint8_t *buf, size_t len, char *out, size_t cap);

#endif /* FIRC_CLIENTHELLO_H */
