#ifndef FIRC_TAP_H
#define FIRC_TAP_H

#include <stdbool.h>

#include "firc/devices.h"
#include "firc/dnspipeline.h"
#include "firc/events.h"
#include "firc/fakeip.h"
#include "firc/recall.h"
#include "firc/rulesnap.h"

/* Not a firc_fakeip_t: building one generates and persists a ULA prefix, which a diagnostic must never do. */
typedef struct {
    firc_ip_t v4_base;
    uint8_t v4_bits;
    firc_ip_t v6_base;
    uint8_t v6_bits;
} firc_tap_pool_t;

/* Fills from an existing pool; false if it has no prefixes to give. */
bool firc_tap_pool_from_fakeip(const firc_fakeip_t *f, firc_tap_pool_t *out);

/* Loop thread only, like the pipeline and recall it reads. */
typedef struct firc_tap_ctx {
    const firc_ruleset_snapshot_t *snap;
    const firc_tap_pool_t *pool;
    const firc_dns_pipeline_t *pipeline; /* NULL: nobody covered */
    const firc_recall_t *recall;         /* NULL: address path finds nothing */
    firc_devsel_policy_fn policy;
    firc_devsel_device_fn device;
    void *ud;
} firc_tap_ctx_t;

typedef struct firc_tap_find {
    const firc_group_snapshot_t *group; /* NULL: not a bypass */
    char name[256];                     /* folded to lower case */
    uint8_t how;                        /* firc_bypass_how_t */
} firc_tap_find_t;

/* A new connection client->dst: a bypass when the client is covered, the recall knows dst's name,
 * and some group matching that name now admits the client (the first such group is reported). */
bool firc_tap_judge_addr(const firc_tap_ctx_t *c, const firc_ip_t *client, const firc_ip_t *dst,
                         firc_tap_find_t *out);
/* The same, from a ClientHello's SNI; used only when the address path misses. */
bool firc_tap_judge_sni(const firc_tap_ctx_t *c, const firc_ip_t *client, const firc_ip_t *dst,
                        const char *sni, firc_tap_find_t *out);

#endif /* FIRC_TAP_H */
