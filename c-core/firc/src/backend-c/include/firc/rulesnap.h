#ifndef FIRC_RULESNAP_H
#define FIRC_RULESNAP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/id.h"
#include "firc/match.h"
#include "firc/devices.h"
#include "firc/models.h"

typedef struct firc_group_snapshot {
    firc_id_t id;
    char *name; /* owned copy, for logging only */
    firc_matcher_t *matcher;
    firc_devsel_t *devices; /* owned */
    int8_t block;           /* -1 not a block group, else the firc_block_answer_t */
} firc_group_snapshot_t;

typedef struct firc_ruleset_snapshot {
    firc_group_snapshot_t **groups;
    size_t n_groups;
    size_t refs;       /* loop-thread only; the last release frees the snapshot */
    bool provisional;  /* an enabled group with a list has no body fetched yet */
} firc_ruleset_snapshot_t;

/* The first group, in snapshot order, whose rules match name and whose selector admits client (NULL: any);
 * *first (nullable) gets the first group whose rules match, admitting or not. */
const firc_group_snapshot_t *firc_ruleset_snapshot_owner(const firc_ruleset_snapshot_t *snap, const char *name,
                                                         const firc_ip_t *client, firc_devsel_policy_fn policy,
                                                         firc_devsel_device_fn device, void *ud,
                                                         const firc_group_snapshot_t **first);

/* Builds a fresh snapshot: one entry per enabled group, in config order; NULL only on OOM. */
firc_ruleset_snapshot_t *firc_ruleset_snapshot_build(const firc_config_t *cfg);
/* Whether an enabled group is included in the DNS view. */
typedef bool (*firc_ruleset_keep_fn)(const firc_group_t *g, void *ud);
/* Like firc_ruleset_snapshot_build, limited to groups keep accepts (NULL: every enabled group). */
firc_ruleset_snapshot_t *firc_ruleset_snapshot_build_where(const firc_config_t *cfg,
                                                           firc_ruleset_keep_fn keep, void *ud);
/* Another holder of the same snapshot; returns snap. */
firc_ruleset_snapshot_t *firc_ruleset_snapshot_ref(firc_ruleset_snapshot_t *snap);
/* Releases one holder's share; frees the snapshot when it was the last. */
void firc_ruleset_snapshot_free(firc_ruleset_snapshot_t *snap);

#endif /* FIRC_RULESNAP_H */
