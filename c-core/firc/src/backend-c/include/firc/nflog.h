#ifndef FIRC_NFLOG_H
#define FIRC_NFLOG_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* *out borrows the NFULA_PAYLOAD inside one netlink message; false if malformed or absent. */
bool firc_nflog_payload(const void *buf, size_t len, const uint8_t **out, size_t *out_len);

/* Calls cb once per packet; non-zero at the first message it could not step past. */
size_t firc_nflog_walk(const uint8_t *buf, size_t len,
                       void (*cb)(const uint8_t *pkt, size_t len, void *ud), void *ud);

#endif /* FIRC_NFLOG_H */
