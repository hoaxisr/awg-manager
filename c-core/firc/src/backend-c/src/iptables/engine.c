#include "firc/iptables.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/xtables.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

enum { T_FILTER = 0, T_MANGLE = 1, T_NAT = 2, N_TABLES = 3 };

static const char *const k_tables[N_TABLES] = {"filter", "mangle", "nat"};
static const char *const k_builtin[N_TABLES][6] = {
    {"INPUT", "FORWARD", "OUTPUT", NULL},
    {"PREROUTING", "INPUT", "FORWARD", "OUTPUT", "POSTROUTING", NULL},
    {"PREROUTING", "INPUT", "OUTPUT", "POSTROUTING", NULL},
};

typedef struct reg {
    char *chain;
    bool patch;
    firc_xt_block_t block;
    firc_xt_patch_op_t *ops;
    size_t n_ops, cap_ops;
} reg_t;

typedef struct regs {
    reg_t *v;
    size_t n, cap;
} regs_t;

struct firc_ipt {
    firc_xt_t *xt;
    firc_ipt_proto_t fam;
    char *prefix;
    firc_cancel_t *cancel;
    regs_t staged[N_TABLES];
    regs_t written[N_TABLES];
    bool known[N_TABLES];
    firc_xt_info_t shape[N_TABLES];
    firc_logrep_t said_inval;
    firc_logrep_t said_uncovered;
    int refusal;
    const char *refused_table;
    char report[512];
    size_t report_len;
};

static int table_index(const char *name) {
    for (int i = 0; name != NULL && i < N_TABLES; i++) {
        if (strcmp(name, k_tables[i]) == 0) { return i; }
    }
    return -1;
}

static bool builtin(int ti, const char *chain) {
    for (size_t i = 0; k_builtin[ti][i] != NULL; i++) {
        if (strcmp(chain, k_builtin[ti][i]) == 0) { return true; }
    }
    return false;
}

static const char *fam_name(const firc_ipt_t *ipt) { return ipt->fam == FIRC_IPT_PROTO_IPV6 ? "ipv6" : "ipv4"; }

static size_t addr_bytes(const firc_ipt_t *ipt) { return ipt->fam == FIRC_IPT_PROTO_IPV6 ? 16 : 4; }

static void block_start(const firc_ipt_t *ipt, firc_xt_block_t *b) {
    firc_xt_block_init(b);
    b->mac_rev = firc_xt_mac_rev(ipt->xt);
}

static void reg_clear(reg_t *r) {
    free(r->chain);
    firc_xt_block_free(&r->block);
    for (size_t i = 0; i < r->n_ops; i++) { firc_ipt_rule_free(r->ops[i].rule); }
    free(r->ops);
    memset(r, 0, sizeof(*r));
}

static void regs_clear(regs_t *t) {
    for (size_t i = 0; i < t->n; i++) { reg_clear(&t->v[i]); }
    free(t->v);
    memset(t, 0, sizeof(*t));
}

firc_ipt_t *firc_ipt_new(firc_xt_t *xt, const char *chain_prefix) {
    if (xt == NULL || chain_prefix == NULL || chain_prefix[0] == '\0') { return NULL; }
    firc_ipt_t *ipt = calloc(1, sizeof(*ipt));
    if (ipt == NULL) { return NULL; }
    ipt->prefix = strdup(chain_prefix);
    if (ipt->prefix == NULL) {
        free(ipt);
        return NULL;
    }
    ipt->xt = xt;
    ipt->fam = firc_xt_proto(xt);
    return ipt;
}

void firc_ipt_free(firc_ipt_t *ipt) {
    if (ipt == NULL) { return; }
    for (int t = 0; t < N_TABLES; t++) {
        regs_clear(&ipt->staged[t]);
        regs_clear(&ipt->written[t]);
    }
    firc_xt_free(ipt->xt);
    free(ipt->prefix);
    free(ipt);
}

firc_ipt_proto_t firc_ipt_proto(const firc_ipt_t *ipt) { return ipt->fam; }
const char *firc_ipt_prefix(const firc_ipt_t *ipt) { return ipt->prefix; }
void firc_ipt_set_cancel(firc_ipt_t *ipt, firc_cancel_t *cancel) { ipt->cancel = cancel; }

void firc_ipt_discard(firc_ipt_t *ipt) {
    if (ipt == NULL) { return; }
    for (int t = 0; t < N_TABLES; t++) { regs_clear(&ipt->staged[t]); }
}

static reg_t *find_reg(const regs_t *t, const char *chain) {
    for (size_t i = 0; i < t->n; i++) {
        if (strcmp(t->v[i].chain, chain) == 0) { return &t->v[i]; }
    }
    return NULL;
}

static firc_err_t register_chain(firc_ipt_t *ipt, const char *table, const char *chain, bool patch) {
    int ti = table_index(table);
    if (ipt == NULL || ti < 0 || chain == NULL) { return FIRC_ERR_INVAL; }
    size_t plen = strlen(ipt->prefix);
    bool ok = patch ? builtin(ti, chain) : strncmp(chain, ipt->prefix, plen) == 0 && strlen(chain) <= 28;
    if (!ok) { return FIRC_ERR_INVAL; }
    regs_t *t = &ipt->staged[ti];
    reg_t *r = find_reg(t, chain);
    if (r != NULL && patch && r->patch) { return FIRC_OK; }
    if (r != NULL) {
        char *name = r->chain;
        r->chain = NULL;
        reg_clear(r);
        r->chain = name;
    } else {
        if (t->n == t->cap) {
            size_t cap = t->cap ? t->cap * 2 : 8;
            reg_t *grown = realloc(t->v, cap * sizeof(*grown));
            if (grown == NULL) { return FIRC_ERR_NOMEM; }
            t->v = grown;
            t->cap = cap;
        }
        r = &t->v[t->n];
        memset(r, 0, sizeof(*r));
        r->chain = strdup(chain);
        if (r->chain == NULL) { return FIRC_ERR_NOMEM; }
        t->n++;
    }
    r->patch = patch;
    block_start(ipt, &r->block);
    return FIRC_OK;
}

firc_err_t firc_ipt_register_chain_patch(firc_ipt_t *ipt, const char *table, const char *chain) {
    return register_chain(ipt, table, chain, true);
}
firc_err_t firc_ipt_register_chain_override(firc_ipt_t *ipt, const char *table, const char *chain) {
    return register_chain(ipt, table, chain, false);
}

static void say_refused(firc_ipt_t *ipt, const char *table, const char *chain, const firc_ipt_rule_t *rule) {
    char *words = firc_ipt_rule_string(rule);
    firc_logrep_fail(&ipt->said_inval, FIRC_LOG_WARN, NULL,
                     "x_tables %s %s: the rule \"%s\" in %s is outside what firc writes", fam_name(ipt), table,
                     words ? words : "", chain);
    free(words);
}

static firc_err_t add_op(reg_t *r, firc_ipt_option_t op, int num, const firc_ipt_rule_t *rule) {
    size_t kept = 0;
    for (size_t i = 0; i < r->n_ops; i++) {
        if (firc_ipt_rule_equal(r->ops[i].rule, rule)) {
            firc_ipt_rule_free(r->ops[i].rule);
            continue;
        }
        r->ops[kept++] = r->ops[i];
    }
    r->n_ops = kept;
    if (r->n_ops == r->cap_ops) {
        size_t cap = r->cap_ops ? r->cap_ops * 2 : 8;
        firc_xt_patch_op_t *grown = realloc(r->ops, cap * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        r->ops = grown;
        r->cap_ops = cap;
    }
    firc_ipt_rule_t *owned = firc_ipt_rule_clone(rule);
    if (owned == NULL) { return FIRC_ERR_NOMEM; }
    r->ops[r->n_ops++] = (firc_xt_patch_op_t){op, num, owned};
    return FIRC_OK;
}

static void find_rewrite(void *ud, uint32_t index, firc_xt_role_t role, const uint8_t *addr, const uint8_t *mask) {
    (void)index;
    (void)addr;
    (void)mask;
    if (role == FIRC_XT_ROLE_REWRITES) { *(bool *)ud = true; }
}

static firc_err_t rule_op(firc_ipt_t *ipt, const char *table, const char *chain, firc_ipt_option_t op, int num,
                          const char *const *args, size_t n_args) {
    int ti = table_index(table);
    reg_t *r = ipt == NULL || ti < 0 || chain == NULL ? NULL : find_reg(&ipt->staged[ti], chain);
    if (r == NULL) { return FIRC_ERR_STATE; }
    firc_ipt_rule_t *rule = firc_ipt_rule_new(args, n_args);
    if (rule == NULL) { return FIRC_ERR_NOMEM; }
    firc_err_t err;
    if (r->patch) {
        firc_xt_block_t check;
        block_start(ipt, &check);
        err = firc_xt_block_add_rule(&check, ipt->fam, rule);
        bool rewrites = false;
        if (err == FIRC_OK) { err = firc_xt_block_roles(&check, ipt->fam, find_rewrite, &rewrites); }
        if (err == FIRC_OK && rewrites) { err = FIRC_ERR_INVAL; }
        firc_xt_block_free(&check);
        if (err == FIRC_OK) { err = add_op(r, op, num, rule); }
    } else if (op == FIRC_IPT_OP_INSERT) {
        err = FIRC_ERR_INVAL;
    } else {
        err = firc_xt_block_add_rule(&r->block, ipt->fam, rule);
    }
    if (err == FIRC_ERR_INVAL) { say_refused(ipt, table, chain, rule); }
    firc_ipt_rule_free(rule);
    return err;
}

firc_err_t firc_ipt_append(firc_ipt_t *ipt, const char *table, const char *chain, const char *const *args, size_t n_args) {
    return rule_op(ipt, table, chain, FIRC_IPT_OP_APPEND, 0, args, n_args);
}
firc_err_t firc_ipt_insert(firc_ipt_t *ipt, const char *table, const char *chain, int rule_num, const char *const *args,
                           size_t n_args) {
    return rule_op(ipt, table, chain, FIRC_IPT_OP_INSERT, rule_num, args, n_args);
}

firc_err_t firc_ipt_append_dnat(firc_ipt_t *ipt, const char *table, const char *chain, const uint8_t *fake,
                                const uint8_t *real) {
    int ti = table_index(table);
    reg_t *r = ipt == NULL || ti < 0 || chain == NULL ? NULL : find_reg(&ipt->staged[ti], chain);
    if (r == NULL) { return FIRC_ERR_STATE; }
    if (r->patch) { return FIRC_ERR_INVAL; }
    return firc_xt_block_add_dnat(&r->block, ipt->fam, fake, real);
}

static bool reg_equal(const reg_t *a, const reg_t *b) {
    if (strcmp(a->chain, b->chain) != 0 || a->patch != b->patch) { return false; }
    if (!a->patch) { return firc_xt_block_equal(&a->block, &b->block); }
    if (a->n_ops != b->n_ops) { return false; }
    for (size_t i = 0; i < a->n_ops; i++) {
        if (a->ops[i].option != b->ops[i].option || a->ops[i].rule_num != b->ops[i].rule_num ||
            !firc_ipt_rule_equal(a->ops[i].rule, b->ops[i].rule)) {
            return false;
        }
    }
    return true;
}

static bool regs_equal(const regs_t *a, const regs_t *b) {
    if (a->n != b->n) { return false; }
    for (size_t i = 0; i < a->n; i++) {
        if (!reg_equal(&a->v[i], &b->v[i])) { return false; }
    }
    return true;
}

static firc_err_t write_regs(firc_ipt_t *ipt, int ti, const regs_t *t, bool full) {
    firc_xt_stage_chain_t *chains = calloc(t->n ? t->n : 1, sizeof(*chains));
    if (chains == NULL) { return FIRC_ERR_NOMEM; }
    size_t n = 0;
    for (size_t i = 0; i < t->n; i++) {
        const reg_t *r = &t->v[i];
        chains[n].name = r->chain;
        chains[n].kind = r->patch ? FIRC_XT_STAGE_PATCH : FIRC_XT_STAGE_OVERRIDE;
        if (r->patch) {
            chains[n].ops = r->ops;
            chains[n].n_ops = r->n_ops;
        } else {
            chains[n].block = &r->block;
        }
        n++;
    }
    firc_xt_stage_t stage = {chains, n, ipt->prefix, full};
    firc_err_t err = firc_xt_commit(ipt->xt, k_tables[ti], &stage, ipt->cancel);
    free(chains);
    return err;
}

typedef struct cover {
    uint8_t (*addr)[16];
    uint8_t (*mask)[16];
    size_t n, cap;
    bool all;
} cover_t;

static void cover_free(cover_t *c) {
    free(c->addr);
    free(c->mask);
    memset(c, 0, sizeof(*c));
}

typedef struct collect {
    cover_t *c;
    firc_xt_role_t want;
    firc_err_t err;
} collect_t;

static void collect_fn(void *ud, uint32_t index, firc_xt_role_t role, const uint8_t *addr, const uint8_t *mask) {
    collect_t *k = ud;
    (void)index;
    if (role != k->want || k->err != FIRC_OK) { return; }
    cover_t *c = k->c;
    if (c->n == c->cap) {
        size_t cap = c->cap ? c->cap * 2 : 64;
        uint8_t (*a)[16] = realloc(c->addr, cap * sizeof(*a));
        if (a == NULL) {
            k->err = FIRC_ERR_NOMEM;
            return;
        }
        c->addr = a;
        uint8_t (*m)[16] = realloc(c->mask, cap * sizeof(*m));
        if (m == NULL) {
            k->err = FIRC_ERR_NOMEM;
            return;
        }
        c->mask = m;
        c->cap = cap;
    }
    memcpy(c->addr[c->n], addr, 16);
    memcpy(c->mask[c->n], mask, 16);
    c->n++;
}

static const char *plain_jump(const firc_ipt_rule_t *r) {
    if (r == NULL || r->n_parts != 2) { return NULL; }
    return strcmp(r->parts[0], "-j") == 0 || strcmp(r->parts[0], "-g") == 0 ? r->parts[1] : NULL;
}

static bool reach(const regs_t *t, bool *reached, const char *chain) {
    for (size_t i = 0; i < t->n; i++) {
        const reg_t *r = &t->v[i];
        if (!reached[i] && !r->patch && strcmp(r->chain, chain) == 0) {
            reached[i] = true;
            return true;
        }
    }
    return false;
}

static void mark_reached(const regs_t *t, bool *reached) {
    for (size_t i = 0; i < t->n; i++) {
        const reg_t *r = &t->v[i];
        if (!r->patch || strcmp(r->chain, "PREROUTING") != 0) { continue; }
        for (size_t k = 0; k < r->n_ops; k++) {
            const char *to = plain_jump(r->ops[k].rule);
            if (to != NULL) { (void)reach(t, reached, to); }
        }
    }
    for (bool grew = true; grew;) {
        grew = false;
        for (size_t i = 0; i < t->n; i++) {
            if (!reached[i]) { continue; }
            const firc_xt_block_t *b = &t->v[i].block;
            for (size_t j = 0; j < b->n_jumps; j++) { grew = reach(t, reached, b->jumps[j].chain) || grew; }
        }
    }
}

static firc_err_t cover_of(const regs_t *t, firc_ipt_proto_t fam, firc_xt_role_t want, cover_t *out) {
    bool *reached = calloc(t->n ? t->n : 1, sizeof(*reached));
    if (reached == NULL) { return FIRC_ERR_NOMEM; }
    if (want == FIRC_XT_ROLE_MARKS) { mark_reached(t, reached); }
    collect_t k = {out, want, FIRC_OK};
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; i < t->n && err == FIRC_OK && k.err == FIRC_OK; i++) {
        const reg_t *r = &t->v[i];
        if (r->patch || (want == FIRC_XT_ROLE_MARKS && !reached[i])) { continue; }
        err = firc_xt_block_roles(&r->block, fam, collect_fn, &k);
    }
    free(reached);
    return err != FIRC_OK ? err : k.err;
}

static bool inside(const uint8_t *addr, const uint8_t *amask, const cover_t *m, size_t bytes) {
    if (m->all) { return true; }
    bool anywhere = true;
    for (size_t b = 0; b < bytes; b++) { anywhere = anywhere && amask[b] == 0; }
    if (anywhere) { return false; }
    for (size_t j = 0; j < m->n; j++) {
        bool hit = true;
        for (size_t b = 0; b < bytes && hit; b++) {
            uint8_t mm = m->mask[j][b];
            hit = (mm & (uint8_t)~amask[b]) == 0 && (addr[b] & mm) == (m->addr[j][b] & mm);
        }
        if (hit) { return true; }
    }
    return false;
}

static bool covered(const cover_t *d, const cover_t *m, size_t bytes) {
    if (d->all) { return m->all; }
    for (size_t i = 0; i < d->n; i++) {
        if (!inside(d->addr[i], d->mask[i], m, bytes)) { return false; }
    }
    return true;
}

typedef struct keep_ctx {
    const cover_t *a, *b;
    size_t bytes;
    bool *keep;
} keep_ctx_t;

static void keep_fn(void *ud, uint32_t index, firc_xt_role_t role, const uint8_t *addr, const uint8_t *mask) {
    keep_ctx_t *k = ud;
    k->keep[index] =
        role != FIRC_XT_ROLE_REWRITES || (inside(addr, mask, k->a, k->bytes) && inside(addr, mask, k->b, k->bytes));
}

static firc_err_t nat_part(const firc_ipt_t *ipt, const regs_t *nat, const cover_t *old_marks, const cover_t *new_marks,
                           regs_t *out) {
    out->v = calloc(nat->n ? nat->n : 1, sizeof(*out->v));
    if (out->v == NULL) { return FIRC_ERR_NOMEM; }
    out->cap = nat->n;
    for (size_t i = 0; i < nat->n; i++) {
        const reg_t *r = &nat->v[i];
        reg_t *o = &out->v[out->n++];
        o->chain = strdup(r->chain);
        o->patch = r->patch;
        firc_xt_block_init(&o->block);
        if (o->chain == NULL) { return FIRC_ERR_NOMEM; }
        for (size_t k = 0; r->patch && k < r->n_ops; k++) {
            firc_err_t err = add_op(o, r->ops[k].option, r->ops[k].rule_num, r->ops[k].rule);
            if (err != FIRC_OK) { return err; }
        }
        if (r->patch) { continue; }
        bool *keep = calloc(r->block.n ? r->block.n : 1, sizeof(*keep));
        if (keep == NULL) { return FIRC_ERR_NOMEM; }
        keep_ctx_t kc = {old_marks, new_marks, addr_bytes(ipt), keep};
        firc_err_t err = firc_xt_block_roles(&r->block, ipt->fam, keep_fn, &kc);
        if (err == FIRC_OK) { err = firc_xt_block_keep(&r->block, ipt->fam, keep, &o->block); }
        free(keep);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

typedef struct step {
    int table;
    bool part;
} step_t;

static const regs_t *after_commit(const firc_ipt_t *ipt, int t) {
    if (ipt->staged[t].n > 0) { return &ipt->staged[t]; }
    return ipt->known[t] ? &ipt->written[t] : NULL;
}

static firc_err_t unmarked_rewrites(const firc_ipt_t *ipt, size_t *lost) {
    *lost = 0;
    const regs_t *rewrites = after_commit(ipt, T_NAT), *marks = after_commit(ipt, T_MANGLE);
    if (rewrites == NULL) { return FIRC_OK; }
    cover_t mn, rn;
    memset(&mn, 0, sizeof(mn));
    memset(&rn, 0, sizeof(rn));
    firc_err_t err = marks != NULL ? cover_of(marks, ipt->fam, FIRC_XT_ROLE_MARKS, &mn) : FIRC_OK;
    if (err == FIRC_OK) { err = cover_of(rewrites, ipt->fam, FIRC_XT_ROLE_REWRITES, &rn); }
    for (size_t i = 0; err == FIRC_OK && i < rn.n; i++) {
        *lost += inside(rn.addr[i], rn.mask[i], &mn, addr_bytes(ipt)) ? 0u : 1u;
    }
    cover_free(&mn);
    cover_free(&rn);
    return err;
}

static firc_err_t order(firc_ipt_t *ipt, bool *add, bool *withdraw, regs_t *part) {
    size_t bytes = addr_bytes(ipt);
    cover_t mw, mn, rw, rn;
    memset(&mw, 0, sizeof(mw));
    memset(&mn, 0, sizeof(mn));
    memset(&rw, 0, sizeof(rw));
    memset(&rn, 0, sizeof(rn));
    const regs_t *marks = after_commit(ipt, T_MANGLE);
    firc_err_t err = marks != NULL ? cover_of(marks, ipt->fam, FIRC_XT_ROLE_MARKS, &mn) : FIRC_OK;
    if (err == FIRC_OK && ipt->known[T_MANGLE]) { err = cover_of(&ipt->written[T_MANGLE], ipt->fam, FIRC_XT_ROLE_MARKS, &mw); }
    if (err == FIRC_OK) { err = cover_of(&ipt->staged[T_NAT], ipt->fam, FIRC_XT_ROLE_REWRITES, &rn); }
    if (err == FIRC_OK) {
        if (ipt->known[T_NAT]) {
            err = cover_of(&ipt->written[T_NAT], ipt->fam, FIRC_XT_ROLE_REWRITES, &rw);
        } else {
            rw.all = true;
        }
    }
    if (err == FIRC_OK) {
        *add = !covered(&rn, &mw, bytes);
        *withdraw = !covered(&rw, &mn, bytes);
    }
    if (err == FIRC_OK && *add && *withdraw) { err = nat_part(ipt, &ipt->staged[T_NAT], &mw, &mn, part); }
    cover_free(&mw);
    cover_free(&mn);
    cover_free(&rw);
    cover_free(&rn);
    return err;
}

static firc_err_t plan(firc_ipt_t *ipt, const bool *due, step_t *steps, size_t *ns, regs_t *part) {
    *ns = 0;
    bool add = false, withdraw = false;
    if (due[T_MANGLE] || due[T_NAT]) {
        size_t lost = 0;
        firc_err_t err = unmarked_rewrites(ipt, &lost);
        if (err != FIRC_OK) { return err; }
        if (lost > 0) {
            firc_logrep_fail(&ipt->said_uncovered, FIRC_LOG_WARN, NULL,
                             "x_tables %s: %zu rewrites in nat would be left without their mark in mangle; not written",
                             fam_name(ipt), lost);
            return FIRC_ERR_INVAL;
        }
    }
    if ((due[T_FILTER] || due[T_MANGLE]) && due[T_NAT]) {
        firc_err_t err = order(ipt, &add, &withdraw, part);
        if (err != FIRC_OK) { return err; }
    }
    if (withdraw) { steps[(*ns)++] = (step_t){T_NAT, add}; }
    if (due[T_FILTER]) { steps[(*ns)++] = (step_t){T_FILTER, false}; }
    if (due[T_MANGLE]) { steps[(*ns)++] = (step_t){T_MANGLE, false}; }
    if (due[T_NAT] && (!withdraw || add)) { steps[(*ns)++] = (step_t){T_NAT, false}; }
    return FIRC_OK;
}

static void report_add(firc_ipt_t *ipt, const char *what, long long ms, firc_err_t err) {
    size_t room = sizeof(ipt->report) - ipt->report_len;
    if (room <= 1) { return; }
    const char *sep = ipt->report_len ? ", " : "";
    char *at = ipt->report + ipt->report_len;
    int n = ms < 0 ? snprintf(at, room, "%s%s skipped", sep, what)
                   : snprintf(at, room, "%s%s %lld ms%s", sep, what, ms, err == FIRC_OK ? "" : " failed");
    if (n > 0) { ipt->report_len += (size_t)n < room ? (size_t)n : room - 1; }
}

static long long ms_since(const struct timespec *t0) {
    struct timespec t1;
    clock_gettime(CLOCK_MONOTONIC, &t1);
    return (long long)((t1.tv_sec - t0->tv_sec) * 1000 + (t1.tv_nsec - t0->tv_nsec) / 1000000);
}

void firc_ipt_take_report(firc_ipt_t *ipt, char *out, size_t cap) {
    if (cap == 0) { return; }
    out[0] = '\0';
    if (ipt == NULL) { return; }
    snprintf(out, cap, "%s", ipt->report_len ? ipt->report : "nothing to write");
    ipt->report_len = 0;
    ipt->report[0] = '\0';
}

bool firc_ipt_tables_moved(firc_ipt_t *ipt) {
    if (ipt == NULL) { return false; }
    for (int t = 0; t < N_TABLES; t++) {
        if (ipt->known[t] && !firc_xt_shape_is(ipt->xt, k_tables[t], &ipt->shape[t])) { return true; }
    }
    return false;
}

static firc_err_t commit_kind(firc_ipt_t *ipt, bool full) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }
    ipt->refusal = 0;
    ipt->refused_table = NULL;
    if (firc_cancel_raised(ipt->cancel)) {
        firc_ipt_discard(ipt);
        return FIRC_ERR_CANCELED;
    }
    for (int t = 0; t < N_TABLES; t++) {
        if (ipt->known[t] && !firc_xt_shape_is(ipt->xt, k_tables[t], &ipt->shape[t])) {
            FIRC_DEBUG("x_tables %s %s: changed since firc wrote it, read again", fam_name(ipt), k_tables[t]);
            regs_clear(&ipt->written[t]);
            ipt->known[t] = false;
        }
    }
    bool due[N_TABLES];
    for (int t = 0; t < N_TABLES; t++) {
        bool staged = ipt->staged[t].n > 0;
        due[t] = staged && (full || !ipt->known[t] || !regs_equal(&ipt->staged[t], &ipt->written[t]));
        if (staged && !due[t]) {
            FIRC_DEBUG("netfilter %s: staged as last written, skipped", k_tables[t]);
            report_add(ipt, k_tables[t], -1, FIRC_OK);
        }
    }
    step_t steps[4];
    size_t ns = 0;
    regs_t part;
    memset(&part, 0, sizeof(part));
    firc_err_t err = plan(ipt, due, steps, &ns, &part);
    for (size_t s = 0; s < ns && err == FIRC_OK; s++) {
        if (firc_cancel_raised(ipt->cancel)) {
            err = FIRC_ERR_CANCELED;
            break;
        }
        int t = steps[s].table;
        struct timespec t0;
        clock_gettime(CLOCK_MONOTONIC, &t0);
        err = write_regs(ipt, t, steps[s].part ? &part : &ipt->staged[t], full);
        report_add(ipt, steps[s].part ? "nat (first part)" : k_tables[t], ms_since(&t0), err);
        if (err != FIRC_OK) {
            ipt->refusal = firc_xt_refusal(ipt->xt);
            ipt->refused_table = k_tables[t];
            break;
        }
        regs_clear(&ipt->written[t]);
        if (steps[s].part) {
            ipt->written[t] = part;
            memset(&part, 0, sizeof(part));
        } else {
            ipt->written[t] = ipt->staged[t];
            memset(&ipt->staged[t], 0, sizeof(ipt->staged[t]));
        }
        ipt->known[t] = true;
        ipt->shape[t] = firc_xt_left_shape(ipt->xt, k_tables[t]);
    }
    regs_clear(&part);
    firc_ipt_discard(ipt);
    if (err == FIRC_OK) {
        (void)firc_logrep_ok(&ipt->said_inval, NULL, "x_tables %s rules", fam_name(ipt));
        (void)firc_logrep_ok(&ipt->said_uncovered, NULL, "x_tables %s nat rewrites", fam_name(ipt));
    }
    return err;
}

firc_err_t firc_ipt_commit(firc_ipt_t *ipt) { return commit_kind(ipt, false); }
firc_err_t firc_ipt_commit_full(firc_ipt_t *ipt) { return commit_kind(ipt, true); }
int firc_ipt_refusal(const firc_ipt_t *ipt) { return ipt != NULL ? ipt->refusal : 0; }
const char *firc_ipt_refused_table(const firc_ipt_t *ipt) { return ipt != NULL ? ipt->refused_table : NULL; }

firc_err_t firc_ipt_probe_chain(firc_ipt_t *ipt, const char *table, const char *chain, firc_ipt_rule_t *const *rules,
                                size_t n) {
    int ti = table_index(table);
    if (ipt == NULL || ti < 0 || chain == NULL || strncmp(chain, ipt->prefix, strlen(ipt->prefix)) != 0 ||
        strlen(chain) > 28) {
        return FIRC_ERR_INVAL;
    }
    firc_xt_block_t b;
    block_start(ipt, &b);
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; i < n && err == FIRC_OK; i++) {
        err = firc_xt_block_add_rule(&b, ipt->fam, rules[i]);
        if (err == FIRC_ERR_INVAL) { say_refused(ipt, table, chain, rules[i]); }
    }
    if (err == FIRC_OK) {
        firc_xt_stage_chain_t sc = {chain, FIRC_XT_STAGE_OVERRIDE, NULL, 0, NULL, 0, &b};
        firc_xt_stage_t add = {&sc, 1, NULL, false};
        err = firc_xt_commit(ipt->xt, table, &add, NULL);
    }
    if (err == FIRC_OK) {
        firc_xt_stage_t drop = {NULL, 0, chain, false};
        firc_err_t gone = firc_xt_commit(ipt->xt, table, &drop, NULL);
        if (gone != FIRC_OK) {
            ipt->known[ti] = false;
            FIRC_WARN("the probe chain %s was not taken away (%s); the next netfilter pass drops it", chain,
                      firc_err_str(gone));
        }
    }
    firc_xt_block_free(&b);
    return err;
}

bool firc_ipt_has(const firc_ipt_t *ipt, const char *name) { return ipt != NULL && firc_xt_has(ipt->xt, name); }

firc_err_t firc_ipt_acl_denying(firc_ipt_t *ipt, const char *table, const char *in_chain, const char *out_chain,
                                struct firc_xt_acl *out) {
    if (ipt == NULL) { return FIRC_ERR_INVAL; }
    return firc_xt_acl_denying(ipt->xt, table, in_chain, out_chain, out);
}
firc_err_t firc_ipt_probe_capture(firc_ipt_t *ipt) { return ipt != NULL ? firc_xt_probe_capture(ipt->xt) : FIRC_ERR_INVAL; }
