#include "firc/log.h"
#include "firc/rulesnap.h"

#include <stdlib.h>
#include <string.h>

static void group_snapshot_free(firc_group_snapshot_t *g)
{
    if (g == NULL) {
        return;
    }
    free(g->name);
    firc_matcher_free(g->matcher);
    firc_devsel_free(g->devices);
    free(g);
}

typedef struct entry_build {
    firc_group_snapshot_t *gs;
} entry_build_t;

static firc_err_t entry_begin(entry_build_t *b, firc_id_t id, const char *name,
                              const firc_devsel_spec_t *devices)
{
    memset(b, 0, sizeof(*b));
    firc_group_snapshot_t *gs = calloc(1, sizeof(*gs));
    if (gs == NULL) {
        return FIRC_ERR_NOMEM;
    }
    b->gs = gs;
    gs->id = id;
    gs->name = strdup(name != NULL ? name : "");
    gs->matcher = firc_matcher_new();
    if (gs->name == NULL || gs->matcher == NULL) {
        return FIRC_ERR_NOMEM;
    }
    if (devices != NULL) {
        return firc_devsel_compile((const char *const *)devices->allow, devices->n_allow,
                                   (const char *const *)devices->deny, devices->n_deny,
                                   &gs->devices);
    }
    return FIRC_OK;
}

static firc_err_t entry_add(entry_build_t *b, const char *type, const char *rule, bool enable,
                            bool borrow)
{
    if (!enable) {
        return FIRC_OK;
    }
    firc_err_t add = borrow ? firc_matcher_add_borrowed(b->gs->matcher, type, rule)
                            : firc_matcher_add(b->gs->matcher, type, rule);
    if (add == FIRC_ERR_INVAL) {
        return FIRC_OK; /* the rest of the group still routes */
    }
    return add == FIRC_OK ? FIRC_OK : FIRC_ERR_NOMEM;
}

static void entry_end(entry_build_t *b, firc_ruleset_snapshot_t *snap, firc_err_t err)
{
    if (err != FIRC_OK) {
        group_snapshot_free(b->gs);
        return;
    }
    snap->groups[snap->n_groups++] = b->gs;
}

static firc_err_t append_group(firc_ruleset_snapshot_t *snap, const firc_group_t *g)
{
    entry_build_t b;
    firc_err_t err = entry_begin(&b, g->id, g->name, &g->devices);
    if (err == FIRC_OK) { b.gs->block = firc_group_is_block(g) ? (int8_t)g->block : -1; }
    for (size_t j = 0; err == FIRC_OK && j < g->n_rules; j++) {
        const firc_rule_t *r = g->rules[j];
        err = entry_add(&b, r->type, r->rule, r->enable, false);
    }
    /* list text is borrowed from the arena; a list free/replace must release the snapshot first */
    const firc_sub_rules_t *lr = g->list != NULL ? &g->list->rules : NULL;
    for (size_t j = 0; err == FIRC_OK && lr != NULL && j < lr->n; j++) {
        err = entry_add(&b, firc_sub_rules_type(lr, j), firc_sub_rules_text(lr, j),
                        firc_sub_rules_enable(lr, j), true);
    }
    entry_end(&b, snap, err);
    return err;
}

const firc_group_snapshot_t *firc_ruleset_snapshot_owner(const firc_ruleset_snapshot_t *snap, const char *name,
                                                         const firc_ip_t *client, firc_devsel_policy_fn policy,
                                                         firc_devsel_device_fn device, void *ud,
                                                         const firc_group_snapshot_t **first)
{
    if (first != NULL) { *first = NULL; }
    if (snap == NULL || name == NULL) { return NULL; }
    for (size_t i = 0; i < snap->n_groups; i++) {
        const firc_group_snapshot_t *g = snap->groups[i];
        if (g == NULL || !firc_matcher_match(g->matcher, name)) { continue; }
        if (first != NULL && *first == NULL) { *first = g; }
        if (firc_devsel_allows(g->devices, client, policy, device, ud)) { return g; }
    }
    return NULL;
}

firc_ruleset_snapshot_t *firc_ruleset_snapshot_build_where(const firc_config_t *cfg,
                                                           firc_ruleset_keep_fn keep, void *ud)
{
    firc_ruleset_snapshot_t *snap = calloc(1, sizeof(*snap));
    if (snap == NULL) {
        return NULL;
    }
    snap->refs = 1;
    size_t cap = cfg->n_groups;
    if (cap == 0) {
        return snap;
    }
    snap->groups = calloc(cap, sizeof(firc_group_snapshot_t *));
    if (snap->groups == NULL) {
        free(snap);
        return NULL;
    }

    for (size_t i = 0; i < cfg->n_groups; i++) {
        const firc_group_t *g = cfg->groups[i];
        if (!g->enable || (keep != NULL && !keep(g, ud))) {
            continue; /* disabled groups can never emit an action */
        }
        if (append_group(snap, g) != FIRC_OK) {
            firc_ruleset_snapshot_free(snap);
            return NULL;
        }
    }
    /* provisional while an enabled group with a list has no body fetched yet */
    for (size_t i = 0; i < cfg->n_groups; i++) {
        const firc_group_t *g = cfg->groups[i];
        if (g->enable && (keep == NULL || keep(g, ud)) && g->list != NULL && !g->list->has_body_hash) {
            snap->provisional = true;
            break;
        }
    }
    return snap;
}

firc_ruleset_snapshot_t *firc_ruleset_snapshot_build(const firc_config_t *cfg)
{
    return firc_ruleset_snapshot_build_where(cfg, NULL, NULL);
}

firc_ruleset_snapshot_t *firc_ruleset_snapshot_ref(firc_ruleset_snapshot_t *snap)
{
    if (snap != NULL) {
        snap->refs++;
    }
    return snap;
}

void firc_ruleset_snapshot_free(firc_ruleset_snapshot_t *snap)
{
    if (snap == NULL) {
        return;
    }
    if (snap->refs > 1) {
        snap->refs--;
        return;
    }
    for (size_t i = 0; i < snap->n_groups; i++) {
        group_snapshot_free(snap->groups[i]);
    }
    free(snap->groups);
    free(snap);
}
