#ifndef FIRC_TEST_FAKE_RTNL_H
#define FIRC_TEST_FAKE_RTNL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/rtnl.h"

typedef struct fake_rtnl_msg {
    uint16_t type;
    uint8_t family;
    uint8_t rtm_type;
    uint32_t table;
    uint32_t priority;
    uint32_t oif;
    uint8_t gw_len;
    uint8_t gw[16];
    uint32_t mark, mask;
    int32_t suppress;
    char iif[16];
    uint8_t protocol;
    uint8_t scope;
    uint16_t flags;
    uint8_t dst_len;
    uint8_t dst[16];
} fake_rtnl_msg_t;

typedef struct fake_rtnl fake_rtnl_t;

fake_rtnl_t *fake_rtnl_start(firc_rtnl_t **out);
void fake_rtnl_stop(fake_rtnl_t *f);

void fake_rtnl_set_link_flags(fake_rtnl_t *f, unsigned flags);
void fake_rtnl_set_link_reply_padding(fake_rtnl_t *f, size_t bytes);
void fake_rtnl_set_dump_reply_padding(fake_rtnl_t *f, size_t bytes);
void fake_rtnl_set_gateway(fake_rtnl_t *f, uint32_t oif, const uint8_t *gw, uint8_t gw_len);
/* An ip rule the kernel reports in RTM_GETRULE dumps */
void fake_rtnl_add_rule(fake_rtnl_t *f, int family, uint32_t mark, uint32_t mask, uint32_t table,
                        uint32_t priority);
/* A route the kernel reports in RTM_GETROUTE dumps of `family`. dst is 4 or 16 bytes; dst_len in bits */
void fake_rtnl_add_route(fake_rtnl_t *f, int family, uint32_t table, uint8_t rtm_type, uint8_t protocol,
                         const uint8_t *dst, uint8_t dst_len, uint32_t priority);
void fake_rtnl_add_addr(fake_rtnl_t *f, int family, uint32_t ifindex, uint8_t scope, uint8_t flags);
void fake_rtnl_clear_addrs(fake_rtnl_t *f);
void fake_rtnl_clear_routes(fake_rtnl_t *f);
void fake_rtnl_scope_last_route(fake_rtnl_t *f, uint8_t scope);
void fake_rtnl_route_via(fake_rtnl_t *f, uint32_t oif, const uint8_t *gw, uint8_t gw_len);
void fake_rtnl_route_multipath(fake_rtnl_t *f);
void fake_rtnl_stray_reply(fake_rtnl_t *f);
void fake_rtnl_short_error_in_dump(fake_rtnl_t *f);
void fake_rtnl_hangup(fake_rtnl_t *f);
void fake_rtnl_fail_next(fake_rtnl_t *f, int err);
void fake_rtnl_fail_next_of(fake_rtnl_t *f, uint16_t type, int err);
void fake_rtnl_fail_after_of(fake_rtnl_t *f, uint16_t type, int err, unsigned skip);
bool fake_rtnl_failure_armed(fake_rtnl_t *f);
void fake_rtnl_fail_times(fake_rtnl_t *f, uint16_t type, int err, unsigned n);

typedef void (*fake_rtnl_record_fn)(const fake_rtnl_msg_t *m, void *ud);
void fake_rtnl_on_record(fake_rtnl_t *f, fake_rtnl_record_fn fn, void *ud);

size_t fake_rtnl_messages(fake_rtnl_t *f, fake_rtnl_msg_t *out, size_t cap);
size_t fake_rtnl_count(fake_rtnl_t *f, uint16_t type, uint8_t rtm_type);

#endif
