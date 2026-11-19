#include "firc/stable_fields.h"

#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/log.h"
#include "firc/mark.h"

static bool configured(const firc_config_t *cfg, const char *owner) {
    for (size_t i = 0; cfg != NULL && i < cfg->n_groups; i++) {
        if (cfg->groups[i] == NULL) { continue; }
        char id[FIRC_ID_STR_LEN];
        firc_id_format(cfg->groups[i]->id, id);
        if (strcmp(id, owner) == 0) { return true; }
    }
    return false;
}

static firc_err_t flush_dead(firc_ct_t *ct, uint32_t field) {
    if (ct == NULL) { return FIRC_OK; }
    size_t dropped = 0;
    return firc_ct_flush_by_mark(ct, firc_mark_group_value(field) | FIRC_MARK_HANDLED,
                                 FIRC_MARK_GROUP_MASK | FIRC_MARK_HANDLED, &dropped);
}

static bool seed(firc_rtnl_t *rtnl, const firc_fields_entry_t *e) {
    firc_err_t err = firc_rtnl_seed_mark_field(rtnl, e->owner, e->field);
    if (err != FIRC_OK) {
        FIRC_WARN("field %u of %s not kept: %s", (unsigned)e->field, e->owner, firc_err_str(err));
    }
    return err == FIRC_OK;
}

firc_stable_fields_adoption_t firc_stable_fields_adopt(firc_rtnl_t *rtnl, firc_ct_t *ct, const firc_config_t *cfg,
                                                       bool groups_known, firc_fields_entry_t *v, size_t *n) {
    firc_stable_fields_adoption_t r = {0};
    size_t out = 0;
    for (size_t i = 0; v != NULL && i < *n; i++) {
        firc_fields_entry_t e = v[i];
        bool keep = false;
        if (strncmp(e.owner, FIRC_MARK_TUNNEL_OWNER, sizeof(FIRC_MARK_TUNNEL_OWNER) - 1) == 0) {
            keep = seed(rtnl, &e);
            if (keep) { r.tunnels++; }
        } else if (configured(cfg, e.owner)) {
            keep = seed(rtnl, &e);
            if (keep) { r.kept++; }
        } else if (!groups_known) {
            keep = seed(rtnl, &e);
            if (keep) { r.reserved++; }
        } else {
            firc_err_t err = flush_dead(ct, e.field);
            if (err == FIRC_OK) {
                r.freed++;
                FIRC_INFO("field %u of group %s, no longer configured, is free again", (unsigned)e.field, e.owner);
            } else {
                keep = seed(rtnl, &e);
                if (keep) { r.reserved++; }
                FIRC_WARN("field %u of group %s, no longer configured, stays reserved: its flows were not "
                          "flushed (%s); the next start tries again",
                          (unsigned)e.field, e.owner, firc_err_str(err));
            }
        }
        if (keep) { v[out++] = e; }
    }
    *n = out;
    return r;
}

static void save_on_change(void *ud, const firc_rtnl_field_t *v, size_t n) {
    const firc_stable_fields_t *sf = ud;
    firc_err_t err = firc_fields_file_save(sf->path, v, n);
    if (err != FIRC_OK) {
        FIRC_WARN("mark fields not saved to %s: %s; the next start assigns them afresh", sf->path,
                  firc_err_str(err));
    }
}

bool firc_stable_fields_start(firc_stable_fields_t *sf, const char *path, firc_rtnl_t *rtnl, firc_ct_t *ct,
                              const firc_config_t *cfg, bool groups_known) {
    memset(sf, 0, sizeof(*sf));
    sf->path = path;
    firc_err_t err = firc_fields_file_load(path, &sf->v, &sf->n);
    if (err == FIRC_OK) {
        sf->loaded = true;
        firc_stable_fields_adoption_t r = firc_stable_fields_adopt(rtnl, ct, cfg, groups_known, sf->v, &sf->n);
        FIRC_INFO("mark fields from %s: %zu kept, %zu freed, %zu reserved, %zu for tunnels", path, r.kept, r.freed,
                  r.reserved, r.tunnels);
        firc_rtnl_fields_now(rtnl, save_on_change, sf);
    } else if (err == FIRC_ERR_NOENT) {
        FIRC_DEBUG("no mark field map at %s: fields are assigned afresh", path);
    } else if (err == FIRC_ERR_INVAL) {
        FIRC_WARN("%s is not a mark field map; removed, fields are assigned afresh", path);
        unlink(path);
    } else {
        FIRC_WARN("%s not read (%s); left in place, fields are assigned afresh this run", path, firc_err_str(err));
    }
    firc_rtnl_watch_mark_fields(rtnl, save_on_change, sf);
    return sf->loaded;
}

bool firc_stable_fields_kept(const firc_stable_fields_t *sf, const char *id, uint32_t field) {
    for (size_t i = 0; sf != NULL && id != NULL && i < sf->n; i++) {
        if (strcmp(sf->v[i].owner, id) == 0) { return firc_mark_group_value(sf->v[i].field) == field; }
    }
    return false;
}

void firc_stable_fields_release(firc_stable_fields_t *sf) {
    free(sf->v);
    sf->v = NULL;
    sf->n = 0;
}
