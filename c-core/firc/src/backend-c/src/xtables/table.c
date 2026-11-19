#include "xt_internal.h"

#include <stdio.h>
#include <stdlib.h>

static bool jumps(const firc_xt_entry_t *e) { return e->jump[0] != '\0' || e->points_at != FIRC_XT_NONE; }

static uint32_t userspace(const uint8_t *ext, uint32_t size) {
    uint32_t keep = size;
    if (memcmp(ext + 2, "limit", 6) == 0 && ext[31] == 0) { keep = XT_MATCH_HDR_LEN + XT_LIMIT_USERSPACE; }
    if (memcmp(ext + 2, "string", 7) == 0 && ext[31] == 1) { keep = XT_MATCH_HDR_LEN + XT_STRING_USERSPACE; }
    return keep < size ? keep : size;
}

static bool same_but(const uint8_t *a, const uint8_t *b, uint32_t from, uint32_t to, uint32_t skip) {
    if (skip < from || skip >= to || to - skip < 2u) { return memcmp(a + from, b + from, to - from) == 0; }
    return memcmp(a + from, b + from, skip - from) == 0 && memcmp(a + skip + 2u, b + skip + 2u, to - skip - 2u) == 0;
}

bool firc_xt_entry_same(firc_ipt_proto_t fam, const firc_xt_entry_t *a, const firc_xt_entry_t *b) {
    if (a->len != b->len || a->points_at != b->points_at || strcmp(a->jump, b->jump) != 0) { return false; }
    uint32_t skip = a->set != 0 ? a->set_at : FIRC_XT_NONE;
    uint32_t eh = firc_xt_ehdr(fam), toff = firc_xt_rd16(a->bytes + firc_xt_at_toff(fam));
    if (toff != firc_xt_rd16(b->bytes + firc_xt_at_toff(fam)) || toff < eh || toff > a->len) { return false; }
    if (memcmp(a->bytes, b->bytes, eh) != 0) { return false; }
    for (uint32_t m = eh; m < toff;) {
        uint32_t ms = firc_xt_rd16(a->bytes + m);
        if (ms < XT_MATCH_HDR_LEN || ms > toff - m || ms != firc_xt_rd16(b->bytes + m)) { return false; }
        if (!same_but(a->bytes, b->bytes, m, m + userspace(a->bytes + m, ms), skip)) { return false; }
        m += ms;
    }
    if (!jumps(a)) { return memcmp(a->bytes + toff, b->bytes + toff, a->len - toff) == 0; }
    uint32_t v = toff + 32u;
    if (v + 4u > a->len) { return false; }
    return memcmp(a->bytes + toff, b->bytes + toff, 32u) == 0 &&
           memcmp(a->bytes + v + 4u, b->bytes + v + 4u, a->len - v - 4u) == 0;
}

static firc_err_t insert_rule(firc_xt_chain_t *c, size_t at, firc_xt_entry_t *e) {
    if (c->n_rules == c->cap_rules) {
        size_t cap = c->cap_rules ? c->cap_rules * 2 : 8;
        firc_xt_entry_t *grown = realloc(c->rules, cap * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        c->rules = grown;
        c->cap_rules = cap;
    }
    memmove(&c->rules[at + 1], &c->rules[at], (c->n_rules - at) * sizeof(*c->rules));
    c->rules[at] = *e;
    c->n_rules++;
    memset(e, 0, sizeof(*e));
    return FIRC_OK;
}

static void cut_rule(firc_xt_chain_t *c, size_t at) {
    firc_xt_entry_clear(&c->rules[at]);
    memmove(&c->rules[at], &c->rules[at + 1], (c->n_rules - at - 1) * sizeof(*c->rules));
    c->n_rules--;
}

uint16_t firc_xt_table_set(firc_xt_table_t *t, const char *name) {
    for (size_t i = 0; i < t->n_sets; i++) {
        if (strcmp(t->sets[i].name, name) == 0) { return (uint16_t)(i + 1); }
    }
    if (t->n_sets == UINT16_MAX) { return 0; }
    if (t->n_sets == t->cap_sets) {
        size_t cap = t->cap_sets ? t->cap_sets * 2 : 4;
        firc_xt_tset_t *grown = realloc(t->sets, cap * sizeof(*grown));
        if (grown == NULL) { return 0; }
        t->sets = grown;
        t->cap_sets = cap;
    }
    firc_xt_tset_t *s = &t->sets[t->n_sets++];
    memset(s, 0, sizeof(*s));
    snprintf(s->name, sizeof(s->name), "%s", name);
    return (uint16_t)t->n_sets;
}

static firc_err_t encode_one(firc_xt_table_t *t, const char *chain, const firc_ipt_rule_t *r, firc_xt_entry_t *e,
                             uint32_t *exts, char *why, size_t why_len) {
    char set[FIRC_XT_NAME_LEN];
    firc_err_t err = firc_xt_encode(t->fam, t->mac_rev, r, e, exts, set);
    if (err == FIRC_ERR_INVAL) {
        char *words = firc_ipt_rule_string(r);
        snprintf(why, why_len, "the rule \"%s\" is outside what firc writes", words != NULL ? words : "");
        free(words);
        return err;
    }
    if (err == FIRC_OK && set[0] != '\0' && (e->set = firc_xt_table_set(t, set)) == 0) {
        firc_xt_entry_clear(e);
        return FIRC_ERR_NOMEM;
    }
    if (err != FIRC_OK || e->jump[0] == '\0') { return err; }
    const firc_xt_chain_t *to = firc_xt_find_chain(t, e->jump);
    if (to != NULL && to->hook >= 0) {
        snprintf(why, why_len, "a rule in %s jumps to %s, a built-in chain", chain, e->jump);
        firc_xt_entry_clear(e);
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

static firc_err_t chain_for(firc_xt_table_t *t, const char *name, firc_xt_chain_t **c) {
    *c = firc_xt_find_chain(t, name);
    return *c != NULL ? FIRC_OK : firc_xt_add_user_chain(t, name, c);
}

static void clear_entries(firc_xt_entry_t *e, size_t n) {
    for (size_t i = 0; i < n; i++) { firc_xt_entry_clear(&e[i]); }
}

static firc_err_t replace_rules(firc_xt_table_t *t, const char *name, firc_xt_entry_t *fresh, size_t n) {
    firc_xt_chain_t *c = NULL;
    firc_err_t err = chain_for(t, name, &c);
    if (err != FIRC_OK) {
        clear_entries(fresh, n);
        free(fresh);
        return err;
    }
    for (size_t i = 0; i < n && i < c->n_rules; i++) {
        if (firc_xt_entry_same(t->fam, &fresh[i], &c->rules[i])) {
            uint16_t set = fresh[i].set, set_at = fresh[i].set_at;
            firc_xt_entry_clear(&fresh[i]);
            fresh[i] = c->rules[i];
            fresh[i].set = set;
            fresh[i].set_at = set_at;
            c->rules[i].owned = false;
        }
    }
    clear_entries(c->rules, c->n_rules);
    free(c->rules);
    c->rules = fresh;
    c->n_rules = n;
    c->cap_rules = n ? n : 1;
    return FIRC_OK;
}

static firc_err_t do_override(firc_xt_table_t *t, const firc_xt_stage_chain_t *sc, uint32_t *exts, char *why,
                              size_t why_len) {
    size_t n = sc->n_rules;
    firc_xt_entry_t *fresh = calloc(n ? n : 1, sizeof(*fresh));
    if (fresh == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n; i++) {
        firc_err_t err = encode_one(t, sc->name, sc->rules[i], &fresh[i], exts, why, why_len);
        if (err != FIRC_OK) {
            clear_entries(fresh, i);
            free(fresh);
            return err;
        }
    }
    return replace_rules(t, sc->name, fresh, n);
}

static firc_err_t override_from_block(firc_xt_table_t *t, const firc_xt_stage_chain_t *sc, uint32_t *exts, char *why,
                                      size_t why_len) {
    const firc_xt_block_t *b = sc->block;
    if (b->n > 0 && b->fam != t->fam) {
        snprintf(why, why_len, "the block for %s holds another family's rules", sc->name);
        return FIRC_ERR_INVAL;
    }
    if ((b->exts & FIRC_XT_EXT_MAC) && b->first_mac_rev != t->mac_rev) {
        snprintf(why, why_len, "the block for %s holds mac revision %u, the kernel takes %u", sc->name, b->first_mac_rev,
                 t->mac_rev);
        return FIRC_ERR_INVAL;
    }
    for (size_t j = 0; j < b->n_jumps; j++) {
        const firc_xt_chain_t *to = firc_xt_find_chain(t, b->jumps[j].chain);
        if (to != NULL && to->hook >= 0) {
            snprintf(why, why_len, "a rule in %s jumps to %s, a built-in chain", sc->name, b->jumps[j].chain);
            return FIRC_ERR_INVAL;
        }
    }
    firc_xt_entry_t *fresh = calloc(b->n ? b->n : 1, sizeof(*fresh));
    if (fresh == NULL) { return FIRC_ERR_NOMEM; }
    size_t off = 0, jump = 0, set = 0;
    uint32_t eh = firc_xt_ehdr(t->fam);
    uint32_t i = 0;
    for (; i < b->n; i++) {
        firc_xt_entry_t *e = &fresh[i];
        firc_xt_entry_clear(e);
        if (b->len - off < eh) { break; }
        e->bytes = b->bytes + off;
        e->len = firc_xt_rd16(e->bytes + firc_xt_at_next(t->fam));
        if (e->len < eh || e->len > b->len - off) { break; }
        if (jump < b->n_jumps && b->jumps[jump].entry == i) {
            snprintf(e->jump, sizeof(e->jump), "%s", b->jumps[jump].chain);
            jump++;
        }
        if (set < b->n_sets && b->sets[set].entry == i) {
            if (b->sets[set].at < eh + XT_MATCH_HDR_LEN || b->sets[set].at + 2u > firc_xt_rd16(e->bytes + firc_xt_at_toff(t->fam))) {
                break;
            }
            e->set_at = (uint16_t)b->sets[set].at;
            e->set = firc_xt_table_set(t, b->sets[set].name);
            if (e->set == 0) {
                free(fresh);
                return FIRC_ERR_NOMEM;
            }
            set++;
        }
        off += e->len;
    }
    if (i != b->n || off != b->len || jump != b->n_jumps || set != b->n_sets) {
        free(fresh);
        snprintf(why, why_len, "the block for %s does not hold the entries it counts", sc->name);
        return FIRC_ERR_INVAL;
    }
    *exts |= b->exts;
    return replace_rules(t, sc->name, fresh, b->n);
}

static firc_err_t do_patch(firc_xt_table_t *t, const firc_xt_stage_chain_t *sc, uint32_t *exts, char *why,
                           size_t why_len) {
    firc_xt_chain_t *c = NULL;
    firc_err_t err = chain_for(t, sc->name, &c);
    if (err != FIRC_OK) { return err; }
    for (size_t k = 0; k < sc->n_ops; k++) {
        const firc_xt_patch_op_t *op = &sc->ops[k];
        firc_xt_entry_t e;
        err = encode_one(t, sc->name, op->rule, &e, exts, why, why_len);
        if (err != FIRC_OK) { return err; }
        if (e.set != 0) {
            snprintf(why, why_len, "a patch of %s carries a set match", c->name);
            firc_xt_entry_clear(&e);
            return FIRC_ERR_INVAL;
        }
        size_t count = 0;
        for (size_t r = 0; r < c->n_rules; r++) { count += firc_xt_entry_same(t->fam, &c->rules[r], &e) ? 1u : 0u; }
        for (; count > 1; count--) {
            size_t r = 0;
            while (!firc_xt_entry_same(t->fam, &c->rules[r], &e)) { r++; }
            cut_rule(c, r);
        }
        size_t at = 0;
        while (count == 1 && !firc_xt_entry_same(t->fam, &c->rules[at], &e)) { at++; }
        switch (op->option) {
        case FIRC_IPT_OP_APPEND:
        case FIRC_IPT_OP_INSERT:
            if (count == 1) {
                c->rules[at].staged = true;
                break;
            }
            if (op->option == FIRC_IPT_OP_INSERT && (op->rule_num < 1 || (size_t)op->rule_num - 1 > c->n_rules)) {
                snprintf(why, why_len, "an insert at %d into %s, which has %zu rules", op->rule_num, c->name, c->n_rules);
                err = FIRC_ERR_INVAL;
                break;
            }
            e.staged = true;
            err = insert_rule(c, op->option == FIRC_IPT_OP_INSERT ? (size_t)op->rule_num - 1 : c->n_rules, &e);
            break;
        default:
            snprintf(why, why_len, "a patch of %s carries an op firc does not write", c->name);
            err = FIRC_ERR_INVAL;
            break;
        }
        firc_xt_entry_clear(&e);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static bool overridden(const firc_xt_stage_t *s, const char *name) {
    for (size_t i = 0; i < s->n_chains; i++) {
        if (s->chains[i].kind == FIRC_XT_STAGE_OVERRIDE && strcmp(s->chains[i].name, name) == 0) { return true; }
    }
    return false;
}

static void do_sweep(firc_xt_table_t *t, const firc_xt_stage_t *s) {
    const char *prefix = s->sweep_prefix;
    size_t plen = strlen(prefix);
    for (size_t i = t->n_chains; i-- > 0;) {
        firc_xt_chain_t *c = &t->chains[i];
        if (c->hook < 0 && strncmp(c->name, prefix, plen) == 0 && !overridden(s, c->name)) { (void)firc_xt_drop_chain(t, c); }
    }
    for (size_t i = 0; i < t->n_chains; i++) {
        firc_xt_chain_t *c = &t->chains[i];
        if (c->hook < 0 && strncmp(c->name, prefix, plen) == 0) { continue; }
        for (size_t r = c->n_rules; r-- > 0;) {
            const firc_xt_entry_t *e = &c->rules[r];
            if (e->jump[0] != '\0' && strncmp(e->jump, prefix, plen) == 0 && !e->staged) { cut_rule(c, r); }
        }
    }
}

static void cut_our_jumps(firc_xt_table_t *t, const char *prefix) {
    size_t plen = strlen(prefix);
    for (size_t i = 0; i < t->n_chains; i++) {
        firc_xt_chain_t *c = &t->chains[i];
        if (c->hook < 0) { continue; }
        for (size_t r = c->n_rules; r-- > 0;) {
            if (c->rules[r].jump[0] != '\0' && strncmp(c->rules[r].jump, prefix, plen) == 0) { cut_rule(c, r); }
        }
    }
}

firc_err_t firc_xt_merge(firc_xt_table_t *t, const firc_xt_stage_t *stage, uint32_t *exts, char *why, size_t why_len) {
    why[0] = '\0';
    bool sweep = stage->sweep_prefix != NULL && stage->sweep_prefix[0] != '\0';
    if (sweep && stage->relocate_jumps) { cut_our_jumps(t, stage->sweep_prefix); }
    for (size_t i = 0; i < stage->n_chains; i++) {
        const firc_xt_stage_chain_t *sc = &stage->chains[i];
        firc_err_t err = FIRC_OK;
        if (sc->kind == FIRC_XT_STAGE_OVERRIDE) {
            err = sc->block != NULL ? override_from_block(t, sc, exts, why, why_len) : do_override(t, sc, exts, why, why_len);
        } else if (sc->kind == FIRC_XT_STAGE_PATCH) {
            err = do_patch(t, sc, exts, why, why_len);
        } else {
            snprintf(why, why_len, "%s is staged in a way firc does not write", sc->name);
            err = FIRC_ERR_INVAL;
        }
        if (err != FIRC_OK) { return err; }
    }
    if (sweep) { do_sweep(t, stage); }
    return FIRC_OK;
}

bool firc_xt_blob_equal(const firc_xt_info_t *ai, const uint8_t *a, const firc_xt_info_t *bi, const uint8_t *b) {
    if (ai->valid_hooks != bi->valid_hooks || ai->num_entries != bi->num_entries || ai->size != bi->size) { return false; }
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (!(ai->valid_hooks & (1u << h))) { continue; }
        if (ai->hook_entry[h] != bi->hook_entry[h] || ai->underflow[h] != bi->underflow[h]) { return false; }
    }
    return memcmp(a, b, ai->size) == 0;
}

firc_err_t firc_xt_carry_counters(const int32_t *old_index, uint32_t n_new, const firc_xt_counter_t *old,
                                  uint32_t n_old, firc_xt_counter_t **out) {
    *out = calloc(n_new ? n_new : 1, sizeof(**out));
    if (*out == NULL) { return FIRC_ERR_NOMEM; }
    for (uint32_t i = 0; i < n_new; i++) {
        if (old_index[i] >= 0 && (uint32_t)old_index[i] < n_old) { (*out)[i] = old[old_index[i]]; }
    }
    return FIRC_OK;
}
