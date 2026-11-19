#ifndef TUNVLESS_IBHS_H
#define TUNVLESS_IBHS_H
#include "../ctlinb.h"
#include <stddef.h>
#include <stdint.h>

#define IBHS_OUT 256

enum ibhs_rc { IBHS_MORE, IBHS_REPLY, IBHS_CONNECT, IBHS_UDP, IBHS_FAIL };
enum ibhs_end { IBHS_END_OK, IBHS_END_DIAL, IBHS_END_GONE, IBHS_END_FULL };

struct ibhs {
    unsigned char buf[600];
    size_t len;
    int phase;
    int http;
    int need_auth;
    char host[256];
    uint32_t ip;
    uint16_t port;
};

void ibhs_init(struct ibhs *h, const struct ib_cfg *c);

/* Feeds bytes; out (IBHS_OUT bytes) gets what to send back, out_n its length. CONNECT and UDP
 * may carry replies of earlier phases in out. ip is in host order.
 * CONNECT, UDP and FAIL end the handshake: nothing more is fed. */
enum ibhs_rc ibhs_feed(struct ibhs *h, const struct ib_cfg *c, const unsigned char *p, size_t n,
                       unsigned char *out, size_t *out_n) __attribute__((nonnull));

/* The final reply for a finished dial; returns its length, 0 if cap is too small. */
size_t ibhs_done(int http, enum ibhs_end end, unsigned char *out, size_t cap);

/* Early data that followed the request. */
size_t ibhs_rest(const struct ibhs *h, const unsigned char **p);

#endif
