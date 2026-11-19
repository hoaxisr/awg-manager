#ifndef FIRC_PTR_H
#define FIRC_PTR_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip_addr.h"
#include "firc/loop.h"

typedef enum { FIRC_PTR_PENDING, FIRC_PTR_NAME, FIRC_PTR_NONE } firc_ptr_state_t;

typedef struct firc_ptr firc_ptr_t;

/* A PTR cache resolving on `loop`; a query unanswered in timeout_ms ends in NONE. NULL on failure. */
firc_ptr_t *firc_ptr_new(firc_loop_t *loop, uint32_t timeout_ms);

/* Removes its socket and timers from the loop, then frees. NULL is a no-op. */
void firc_ptr_free(firc_ptr_t *p);

/* The cached state of ip; an unknown or expired address is asked of upstream:port once and is PENDING.
 * On NAME, out holds the lower-case target without the trailing dot; *sent (nullable) says a query went out.
 * Loop thread only. */
firc_ptr_state_t firc_ptr_get(firc_ptr_t *p, const firc_ip_t *ip, const char *upstream, uint16_t port,
                              int64_t now_ms, char *out, size_t cap, bool *sent);

/* As firc_ptr_get, but never sends: an unknown address is PENDING. */
firc_ptr_state_t firc_ptr_peek(const firc_ptr_t *p, const firc_ip_t *ip, int64_t now_ms, char *out, size_t cap);

/* The reverse name of ip (in-addr.arpa or nibble ip6.arpa), no trailing dot; false if it does not fit. */
bool firc_ptr_name_for(const firc_ip_t *ip, char *out, size_t cap);

#endif
