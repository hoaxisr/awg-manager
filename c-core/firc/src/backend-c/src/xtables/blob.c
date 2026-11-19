#include "xt_internal.h"

#include <stdio.h>
#include <stdlib.h>

static const char *const k_hook_names[FIRC_XT_NUMHOOKS] = {"PREROUTING", "INPUT", "FORWARD", "OUTPUT", "POSTROUTING"};

#define BAD(text) do { *why = (text); err = FIRC_ERR_PROTO; goto fail; } while (0)

void firc_xt_entry_clear(firc_xt_entry_t *e) {
    if (e->owned) { free(e->bytes); }
    memset(e, 0, sizeof(*e));
    e->old_index = -1;
    e->old_off = FIRC_XT_NONE;
    e->points_at = FIRC_XT_NONE;
}

static void chain_clear(firc_xt_chain_t *c) {
    firc_xt_entry_clear(&c->head);
    for (size_t r = 0; r < c->n_rules; r++) { firc_xt_entry_clear(&c->rules[r]); }
    free(c->rules);
    c->rules = NULL;
    c->n_rules = 0;
    c->cap_rules = 0;
    firc_xt_entry_clear(&c->tail);
}

void firc_xt_table_clear(firc_xt_table_t *t) {
    for (size_t i = 0; i < t->n_chains; i++) { chain_clear(&t->chains[i]); }
    free(t->chains);
    t->chains = NULL;
    t->n_chains = 0;
    t->cap_chains = 0;
    firc_xt_entry_clear(&t->end);
    free(t->sets);
    t->sets = NULL;
    t->n_sets = 0;
    t->cap_sets = 0;
}

firc_xt_chain_t *firc_xt_find_chain(firc_xt_table_t *t, const char *name) {
    for (size_t i = 0; i < t->n_chains; i++) {
        if (strcmp(t->chains[i].name, name) == 0) { return &t->chains[i]; }
    }
    return NULL;
}

static bool name_taken(const firc_xt_table_t *t, size_t upto, const char *name) {
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (strcmp(k_hook_names[h], name) == 0) { return true; }
    }
    for (size_t i = 0; i < upto; i++) {
        if (strcmp(t->chains[i].name, name) == 0) { return true; }
    }
    return false;
}

static const uint8_t *target_of(firc_ipt_proto_t fam, const uint8_t *e) { return e + firc_xt_rd16(e + firc_xt_at_toff(fam)); }

static bool named(const uint8_t *ext, const char *name) { return strncmp((const char *)ext + 2, name, XT_NAME_LEN) == 0; }

static void clear_tail(uint8_t *ext) {
    char *name = (char *)ext + 2;
    size_t n = strnlen(name, XT_NAME_LEN);
    memset(name + n, 0, XT_NAME_LEN - n);
}

static int find_index(const uint32_t *offs, uint32_t n, uint32_t off) {
    uint32_t lo = 0, hi = n;
    while (lo < hi) {
        uint32_t mid = lo + (hi - lo) / 2;
        if (offs[mid] == off) { return (int)mid; }
        if (offs[mid] < off) { lo = mid + 1; } else { hi = mid; }
    }
    return -1;
}

typedef struct start {
    uint32_t off;
    int hook;
    uint32_t index;
} start_t;

static int start_cmp(const void *a, const void *b) {
    const start_t *x = a, *y = b;
    return (x->off > y->off) - (x->off < y->off);
}

static bool is_return_tail(firc_ipt_proto_t fam, const uint8_t *e) {
    uint32_t eh = firc_xt_ehdr(fam);
    if (firc_xt_rd16(e + firc_xt_at_toff(fam)) != eh) { return false; }
    const uint8_t *t = e + eh;
    return t[2] == '\0' && (int32_t)firc_xt_rd32(t + 32) == XT_VERDICT_RETURN;
}

static void view_entry(firc_ipt_proto_t fam, uint8_t *blob, const uint32_t *offs, uint32_t i, firc_xt_entry_t *out) {
    uint8_t *e = blob + offs[i];
    out->bytes = e;
    out->len = firc_xt_rd16(e + firc_xt_at_next(fam));
    out->owned = false;
    out->old_index = (int32_t)i;
    out->old_off = offs[i];
    out->points_at = FIRC_XT_NONE;
    out->jump[0] = '\0';
    out->staged = false;
    out->set = 0;
    out->set_at = 0;
}

static uint32_t first_off(const firc_xt_chain_t *c) { return c->n_rules > 0 ? c->rules[0].old_off : c->tail.old_off; }

static void resolve(firc_ipt_proto_t fam, firc_xt_table_t *t, firc_xt_entry_t *e) {
    if (e->bytes == NULL) { return; }
    const uint8_t *tg = e->bytes + firc_xt_rd16(e->bytes + firc_xt_at_toff(fam));
    if (tg[2] != '\0') { return; }
    int32_t v = (int32_t)firc_xt_rd32(tg + 32);
    if (v < 0) { return; }
    for (size_t i = 0; i < t->n_chains; i++) {
        if (first_off(&t->chains[i]) == (uint32_t)v) {
            snprintf(e->jump, sizeof(e->jump), "%s", t->chains[i].name);
            return;
        }
    }
    e->points_at = (uint32_t)v;
}

firc_err_t firc_xt_parse(firc_ipt_proto_t fam, const firc_xt_info_t *info, uint8_t *blob, firc_xt_table_t *out,
                         const char **why) {
    memset(out, 0, sizeof(*out));
    out->fam = fam;
    out->valid_hooks = info->valid_hooks;
    firc_xt_entry_clear(&out->end);
    *why = NULL;
    const uint32_t eh = firc_xt_ehdr(fam), at_t = firc_xt_at_toff(fam), at_n = firc_xt_at_next(fam);
    const uint32_t size = info->size;
    firc_err_t err = FIRC_ERR_PROTO;
    start_t *starts = NULL;
    uint32_t *offs = malloc(((size_t)size / (eh + XT_MATCH_HDR_LEN) + 1u) * sizeof(uint32_t));
    if (offs == NULL) { return FIRC_ERR_NOMEM; }
    uint32_t n = 0;
    if ((info->valid_hooks & ~0x1fu) != 0) { BAD("valid_hooks names a hook that does not exist"); }
    for (uint32_t off = 0; off < size;) {
        if (size - off < eh + XT_MATCH_HDR_LEN) { BAD("an entry runs past the end of the table"); }
        uint8_t *e = blob + off;
        uint16_t toff = firc_xt_rd16(e + at_t), next = firc_xt_rd16(e + at_n);
        if (next < eh + XT_MATCH_HDR_LEN || next % 8u != 0 || next > size - off) { BAD("an entry's next_offset is out of bounds"); }
        if (toff < eh || (uint32_t)toff + XT_MATCH_HDR_LEN > next) { BAD("an entry's target_offset is out of bounds"); }
        uint32_t m = eh;
        while (m < toff) {
            uint16_t ms = firc_xt_rd16(e + m);
            if (ms < XT_MATCH_HDR_LEN || ms % 8u != 0 || ms > toff - m) { BAD("a match's size is out of bounds"); }
            if (strnlen((const char *)e + m + 2, XT_NAME_LEN) == XT_NAME_LEN) { BAD("a match name has no end"); }
            m += ms;
        }
        const uint8_t *tg = e + toff;
        uint16_t ts = firc_xt_rd16(tg);
        if (ts < XT_MATCH_HDR_LEN || ts % 8u != 0 || ts > next - toff) { BAD("a target's size is out of bounds"); }
        if (strnlen((const char *)tg + 2, XT_NAME_LEN) == XT_NAME_LEN) { BAD("a target name has no end"); }
        if (tg[2] == '\0') {
            if (ts != XT_STANDARD_TARGET_LEN) { BAD("a standard target has the wrong size"); }
            if ((int32_t)firc_xt_rd32(tg + 32) < XT_VERDICT_RETURN) { BAD("a standard target has an unknown verdict"); }
        } else if (named(tg, "ERROR")) {
            if (ts != XT_ERROR_TARGET_LEN) { BAD("an ERROR target has the wrong size"); }
            if (strnlen((const char *)tg + 32, XT_ERRORNAME_LEN) == XT_ERRORNAME_LEN) { BAD("an ERROR target's name has no end"); }
        }
        offs[n++] = off;
        off += next;
    }
    if (n == 0 || n != info->num_entries) { BAD("num_entries does not match the entries"); }
    if (!named(target_of(fam, blob + offs[n - 1]), "ERROR")) { BAD("the last entry is not an ERROR target"); }
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (!(info->valid_hooks & (1u << h))) { continue; }
        int a = find_index(offs, n, info->hook_entry[h]), b = find_index(offs, n, info->underflow[h]);
        if (a < 0 || b < a) { BAD("a hook entry or underflow is not on an entry"); }
    }
    for (uint32_t i = 0; i < n; i++) {
        const uint8_t *tg = target_of(fam, blob + offs[i]);
        if (tg[2] != '\0') { continue; }
        int32_t v = (int32_t)firc_xt_rd32(tg + 32);
        if (v >= 0 && find_index(offs, n, (uint32_t)v) < 0) { BAD("a jump lands inside an entry"); }
    }
    for (uint32_t i = 0; i < n; i++) {
        uint8_t *e = blob + offs[i];
        uint16_t toff = firc_xt_rd16(e + at_t);
        firc_xt_wr32(e + firc_xt_at_comefrom(fam), 0);
        memset(e + firc_xt_at_counters(fam), 0, sizeof(xt_counters_t));
        for (uint32_t m = eh; m < toff; m += firc_xt_rd16(e + m)) { clear_tail(e + m); }
        clear_tail(e + toff);
    }
    starts = malloc(((size_t)n + FIRC_XT_NUMHOOKS) * sizeof(*starts));
    if (starts == NULL) { err = FIRC_ERR_NOMEM; goto fail; }
    size_t ns = 0;
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (!(info->valid_hooks & (1u << h))) { continue; }
        starts[ns++] = (start_t){info->hook_entry[h], h, (uint32_t)find_index(offs, n, info->hook_entry[h])};
    }
    for (uint32_t i = 0; i + 1 < n; i++) {
        if (named(target_of(fam, blob + offs[i]), "ERROR")) { starts[ns++] = (start_t){offs[i], -1, i}; }
    }
    qsort(starts, ns, sizeof(*starts), start_cmp);
    if (ns == 0 || starts[0].off != 0) { BAD("entries come before the first chain"); }
    for (size_t k = 0; k + 1 < ns; k++) {
        if (starts[k].off == starts[k + 1].off) { BAD("two chains start at one entry"); }
    }
    out->chains = calloc(ns, sizeof(*out->chains));
    if (out->chains == NULL) { err = FIRC_ERR_NOMEM; goto fail; }
    out->cap_chains = ns;
    for (size_t k = 0; k < ns; k++) {
        uint32_t a = starts[k].index, b = k + 1 < ns ? starts[k + 1].index : n - 1;
        firc_xt_chain_t *c = &out->chains[out->n_chains++];
        firc_xt_entry_clear(&c->head);
        firc_xt_entry_clear(&c->tail);
        c->hook = starts[k].hook;
        uint32_t ra, rb;
        if (c->hook >= 0) {
            snprintf(c->name, sizeof(c->name), "%s", k_hook_names[c->hook]);
            int u = find_index(offs, n, info->underflow[c->hook]);
            if (b == a || (uint32_t)u != b - 1) { BAD("a built-in chain does not end at its policy"); }
            ra = a;
            rb = b - 1;
        } else {
            const uint8_t *tg = target_of(fam, blob + offs[a]);
            size_t len = strnlen((const char *)tg + 32, XT_ERRORNAME_LEN);
            if (len == 0 || len >= FIRC_XT_NAME_LEN) { BAD("a user chain has no usable name"); }
            snprintf(c->name, sizeof(c->name), "%s", (const char *)tg + 32);
            if (name_taken(out, out->n_chains - 1, c->name)) { BAD("two chains share a name"); }
            if (b - a < 2 || !is_return_tail(fam, blob + offs[b - 1])) { BAD("a user chain does not end in RETURN"); }
            view_entry(fam, blob, offs, a, &c->head);
            ra = a + 1;
            rb = b - 1;
        }
        c->n_rules = rb - ra;
        c->cap_rules = c->n_rules;
        if (c->n_rules > 0) {
            c->rules = calloc(c->n_rules, sizeof(*c->rules));
            if (c->rules == NULL) { c->n_rules = 0; err = FIRC_ERR_NOMEM; goto fail; }
            for (uint32_t r = ra; r < rb; r++) {
                view_entry(fam, blob, offs, r, &c->rules[r - ra]);
            }
        }
        view_entry(fam, blob, offs, rb, &c->tail);
    }
    view_entry(fam, blob, offs, n - 1, &out->end);
    for (size_t k = 0; k < out->n_chains; k++) {
        firc_xt_chain_t *c = &out->chains[k];
        for (size_t r = 0; r < c->n_rules; r++) { resolve(fam, out, &c->rules[r]); }
        resolve(fam, out, &c->tail);
    }
    out->n_read = n;
    free(offs);
    free(starts);
    return FIRC_OK;
fail:
    free(offs);
    free(starts);
    firc_xt_table_clear(out);
    return err;
}

typedef struct slot {
    const firc_xt_entry_t *e;
    const char *chain;
    uint32_t off;
} slot_t;

typedef struct moved {
    uint32_t old_off, new_off;
} moved_t;

static int moved_cmp(const void *a, const void *b) {
    const moved_t *x = a, *y = b;
    return (x->old_off > y->old_off) - (x->old_off < y->old_off);
}

firc_err_t firc_xt_serialise(const firc_xt_table_t *t, uint8_t **blob, firc_xt_info_t *info, int32_t **old_index,
                             char *why, size_t why_len) {
    return firc_xt_serialise_room(t, 0, blob, info, old_index, why, why_len);
}

firc_err_t firc_xt_serialise_room(const firc_xt_table_t *t, size_t room, uint8_t **blob, firc_xt_info_t *info,
                                  int32_t **old_index, char *why, size_t why_len) {
    *blob = NULL;
    *old_index = NULL;
    why[0] = '\0';
    memset(info, 0, sizeof(*info));
    info->valid_hooks = t->valid_hooks;
    size_t nc = t->n_chains, total = 1;
    for (size_t i = 0; i < nc; i++) { total += t->chains[i].n_rules + 2; }
    size_t *order = malloc((nc + 1) * sizeof(*order));
    uint32_t *start = malloc((nc + 1) * sizeof(*start));
    uint32_t *tail_at = malloc((nc + 1) * sizeof(*tail_at));
    slot_t *slots = malloc(total * sizeof(*slots));
    moved_t *mv = malloc(total * sizeof(*mv));
    uint8_t *base = NULL, *out = NULL;
    int32_t *oi = NULL;
    firc_err_t err = FIRC_ERR_NOMEM;
    if (order == NULL || start == NULL || tail_at == NULL || slots == NULL || mv == NULL) { goto done; }
    size_t k = 0;
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        for (size_t i = 0; i < nc; i++) { if (t->chains[i].hook == h) { order[k++] = i; } }
    }
    for (size_t i = 0; i < nc; i++) { if (t->chains[i].hook < 0) { order[k++] = i; } }
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (!(t->valid_hooks & (1u << h))) { continue; }
        size_t i = 0;
        while (i < nc && t->chains[i].hook != h) { i++; }
        if (i == nc) {
            snprintf(why, why_len, "the table has no %s chain", k_hook_names[h]);
            err = FIRC_ERR_IO;
            goto done;
        }
    }
    uint64_t size = 0;
    size_t n = 0;
    for (size_t j = 0; j < k; j++) {
        const firc_xt_chain_t *c = &t->chains[order[j]];
        if (c->hook < 0) { slots[n++] = (slot_t){&c->head, c->name, (uint32_t)size}; size += c->head.len; }
        start[order[j]] = (uint32_t)size;
        for (size_t r = 0; r < c->n_rules; r++) { slots[n++] = (slot_t){&c->rules[r], c->name, (uint32_t)size}; size += c->rules[r].len; }
        tail_at[order[j]] = (uint32_t)size;
        slots[n++] = (slot_t){&c->tail, c->name, (uint32_t)size};
        size += c->tail.len;
    }
    slots[n++] = (slot_t){&t->end, "", (uint32_t)size};
    size += t->end.len;
    if (size > UINT32_MAX) { err = FIRC_ERR_LIMIT; goto done; }
    base = malloc(room + (size ? (size_t)size : 1));
    out = base != NULL ? base + room : NULL;
    oi = malloc(n * sizeof(*oi));
    if (out == NULL || oi == NULL) { goto done; }
    size_t nm = 0;
    err = FIRC_ERR_IO;
    for (size_t i = 0; i < n; i++) {
        const firc_xt_entry_t *e = slots[i].e;
        memcpy(out + slots[i].off, e->bytes, e->len);
        if (e->set != 0) {
            const firc_xt_tset_t *s = e->set <= t->n_sets ? &t->sets[e->set - 1] : NULL;
            if (s == NULL || !s->known) {
                snprintf(why, why_len, "a rule in %s names the set %s, whose index is not known", slots[i].chain,
                         s != NULL ? s->name : "?");
                goto done;
            }
            firc_xt_wr16(out + slots[i].off + e->set_at, s->index);
        }
        oi[i] = slots[i].e->old_index;
        if (slots[i].e->old_off != FIRC_XT_NONE) { mv[nm++] = (moved_t){slots[i].e->old_off, slots[i].off}; }
    }
    qsort(mv, nm, sizeof(*mv), moved_cmp);
    err = FIRC_ERR_IO;
    for (size_t i = 0; i < n; i++) {
        const firc_xt_entry_t *e = slots[i].e;
        if (e->jump[0] == '\0' && e->points_at == FIRC_XT_NONE) { continue; }
        uint32_t v = 0;
        if (e->jump[0] != '\0') {
            size_t c = 0;
            while (c < nc && strcmp(t->chains[c].name, e->jump) != 0) { c++; }
            if (c == nc) {
                snprintf(why, why_len, "a rule in %s jumps to %s, which the table does not have", slots[i].chain, e->jump);
                goto done;
            }
            v = start[c];
        } else {
            moved_t key = {e->points_at, 0};
            const moved_t *hit = bsearch(&key, mv, nm, sizeof(*mv), moved_cmp);
            if (hit == NULL) {
                snprintf(why, why_len, "a rule in %s jumps into the middle of a chain this write changed", slots[i].chain);
                goto done;
            }
            v = hit->new_off;
        }
        uint16_t toff = firc_xt_rd16(e->bytes + firc_xt_at_toff(t->fam));
        firc_xt_wr32(out + slots[i].off + toff + 32, v);
    }
    for (size_t j = 0; j < k; j++) {
        const firc_xt_chain_t *c = &t->chains[order[j]];
        if (c->hook < 0) { continue; }
        info->hook_entry[c->hook] = start[order[j]];
        info->underflow[c->hook] = tail_at[order[j]];
    }
    info->num_entries = (uint32_t)n;
    info->size = (uint32_t)size;
    *blob = base;
    *old_index = oi;
    base = NULL;
    oi = NULL;
    err = FIRC_OK;
done:
    free(order);
    free(start);
    free(tail_at);
    free(slots);
    free(mv);
    free(base);
    free(oi);
    return err;
}

static firc_err_t fresh_entry(firc_ipt_proto_t fam, uint32_t tsize, firc_xt_entry_t *e) {
    uint32_t eh = firc_xt_ehdr(fam);
    firc_xt_entry_clear(e);
    e->len = eh + tsize;
    e->owned = true;
    e->bytes = calloc(1, e->len);
    if (e->bytes == NULL) { return FIRC_ERR_NOMEM; }
    firc_xt_wr16(e->bytes + firc_xt_at_toff(fam), (uint16_t)eh);
    firc_xt_wr16(e->bytes + firc_xt_at_next(fam), (uint16_t)e->len);
    firc_xt_wr16(e->bytes + eh, (uint16_t)tsize);
    return FIRC_OK;
}

firc_err_t firc_xt_add_user_chain(firc_xt_table_t *t, const char *name, firc_xt_chain_t **out) {
    size_t len = strlen(name);
    if (len == 0 || len > 28 || name_taken(t, t->n_chains, name)) { return FIRC_ERR_INVAL; }
    if (t->n_chains == t->cap_chains) {
        size_t cap = t->cap_chains ? t->cap_chains * 2 : 8;
        firc_xt_chain_t *grown = realloc(t->chains, cap * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        t->chains = grown;
        t->cap_chains = cap;
    }
    size_t at = t->n_chains;
    for (size_t i = 0; i < t->n_chains; i++) {
        if (t->chains[i].hook < 0 && strcmp(t->chains[i].name, name) >= 0) { at = i; break; }
    }
    firc_xt_chain_t c;
    memset(&c, 0, sizeof(c));
    firc_xt_entry_clear(&c.head);
    firc_xt_entry_clear(&c.tail);
    snprintf(c.name, sizeof(c.name), "%s", name);
    c.hook = -1;
    uint32_t eh = firc_xt_ehdr(t->fam);
    if (fresh_entry(t->fam, XT_ERROR_TARGET_LEN, &c.head) != FIRC_OK ||
        fresh_entry(t->fam, XT_STANDARD_TARGET_LEN, &c.tail) != FIRC_OK) {
        chain_clear(&c);
        return FIRC_ERR_NOMEM;
    }
    memcpy(c.head.bytes + eh + 2, "ERROR", 5);
    memcpy(c.head.bytes + eh + 32, name, len);
    firc_xt_wr32(c.tail.bytes + eh + 32, (uint32_t)XT_VERDICT_RETURN);
    memmove(&t->chains[at + 1], &t->chains[at], (t->n_chains - at) * sizeof(*t->chains));
    t->chains[at] = c;
    t->n_chains++;
    *out = &t->chains[at];
    return FIRC_OK;
}

firc_err_t firc_xt_drop_chain(firc_xt_table_t *t, firc_xt_chain_t *c) {
    if (c->hook >= 0) { return FIRC_ERR_INVAL; }
    size_t at = (size_t)(c - t->chains);
    chain_clear(c);
    memmove(&t->chains[at], &t->chains[at + 1], (t->n_chains - at - 1) * sizeof(*t->chains));
    t->n_chains--;
    return FIRC_OK;
}
