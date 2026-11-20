#ifndef FIRC_STABLE_FIELDS_H
#define FIRC_STABLE_FIELDS_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/conntrack.h"
#include "firc/fields_file.h"
#include "firc/models.h"
#include "firc/rtnl.h"

typedef struct {
    size_t kept;
    size_t freed;
    size_t reserved;
    size_t tunnels;
} firc_stable_fields_adoption_t;

typedef struct {
    const char *path;
    bool loaded;
    firc_fields_entry_t *v;
    size_t n;
} firc_stable_fields_t;

/* Seeds the ids cfg names and every tunnel uplink entry; flushes by mark and frees every other entry, or
 * reserves it when the flush fails or !groups_known. Compacts v to the entries seeded. ct may be NULL. */
firc_stable_fields_adoption_t firc_stable_fields_adopt(firc_rtnl_t *rtnl, firc_ct_t *ct, const firc_config_t *cfg,
                                                       bool groups_known, firc_fields_entry_t *v, size_t *n);

/* Loads path, adopts it, saves the result once and saves at every later change; true when the map was
 * loaded. Only a malformed file is removed. sf must outlive rtnl's use of it. */
bool firc_stable_fields_start(firc_stable_fields_t *sf, const char *path, firc_rtnl_t *rtnl, firc_ct_t *ct,
                              const firc_config_t *cfg, bool groups_known);

/* True when the loaded map gave id the field it holds now (field as firc_mark_group_value). */
bool firc_stable_fields_kept(const firc_stable_fields_t *sf, const char *id, uint32_t field);

/* Frees the loaded entries; loaded and the on-change save stay. */
void firc_stable_fields_release(firc_stable_fields_t *sf);

#endif /* FIRC_STABLE_FIELDS_H */
