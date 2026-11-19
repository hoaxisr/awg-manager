#include "xt_internal.h"

#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#include "firc/atomic_write.h"
#include "firc/log.h"
#include "firc/logrep.h"

struct firc_xt {
    firc_ipt_proto_t fam;
    const firc_xt_kernel_ops_t *ops;
    void *ud;
    uint32_t missing;
    uint8_t mac_rev;
    firc_logrep_t failing[4];
    firc_xt_info_t left[4];
    char *dump_dir;
    int refusal;
};

firc_xt_t *firc_xt_new(firc_ipt_proto_t fam, const firc_xt_kernel_ops_t *ops, void *ud) {
    if (ops == NULL) { return NULL; }
    firc_xt_t *xt = calloc(1, sizeof(*xt));
    if (xt == NULL) { return NULL; }
    xt->fam = fam;
    xt->ops = ops;
    xt->ud = ud;
    return xt;
}

void firc_xt_free(firc_xt_t *xt) {
    if (xt == NULL) { return; }
    if (xt->ops->destroy != NULL) { xt->ops->destroy(xt->ud); }
    free(xt->dump_dir);
    free(xt);
}

firc_ipt_proto_t firc_xt_proto(const firc_xt_t *xt) { return xt->fam; }

uint8_t firc_xt_mac_rev(const firc_xt_t *xt) { return xt->mac_rev; }

bool firc_xt_has(const firc_xt_t *xt, const char *name) {
    for (size_t i = 0; i < firc_xt_n_exts; i++) {
        if (strcmp(firc_xt_exts[i].name, name) != 0) { continue; }
        bool written = xt->fam == FIRC_IPT_PROTO_IPV4 || (firc_xt_exts[i].bit & FIRC_XT_EXT_V4_ONLY) == 0;
        return written && (xt->missing & firc_xt_exts[i].bit) == 0;
    }
    return false;
}

firc_err_t firc_xt_set_dump_dir(firc_xt_t *xt, const char *dir) {
    free(xt->dump_dir);
    xt->dump_dir = NULL;
    if (dir == NULL) { return FIRC_OK; }
    xt->dump_dir = strdup(dir);
    return xt->dump_dir != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
}

static const char *fam_name(const firc_xt_t *xt) { return xt->fam == FIRC_IPT_PROTO_IPV6 ? "ipv6" : "ipv4"; }

static const char *errname(int e, char *buf, size_t cap) {
    switch (e) {
    case EAGAIN: return "EAGAIN";
    case EINVAL: return "EINVAL";
    case ENOENT: return "ENOENT";
    case ENOMEM: return "ENOMEM";
    case ELOOP: return "ELOOP";
    case EPERM: return "EPERM";
    case EACCES: return "EACCES";
    case EPROTONOSUPPORT: return "EPROTONOSUPPORT";
    case ENOPROTOOPT: return "ENOPROTOOPT";
    case EFAULT: return "EFAULT";
    case EBUSY: return "EBUSY";
    default:
        snprintf(buf, cap, "errno %d", e);
        return buf;
    }
}

typedef struct times {
    uint64_t t0, read, parse, merge, serialise, compare;
} times_t;

static unsigned long long us(uint64_t a, uint64_t b) { return (unsigned long long)(b - a); }

static uint64_t now_us(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000u + (uint64_t)ts.tv_nsec / 1000u;
}

static int table_slot(const char *table) {
    if (strcmp(table, "filter") == 0) { return 0; }
    if (strcmp(table, "mangle") == 0) { return 1; }
    if (strcmp(table, "nat") == 0) { return 2; }
    return 3;
}

static firc_err_t failed(firc_xt_t *xt, const char *table, firc_err_t err, const char *fmt, ...) FIRC_PRINTF(4, 5);

static firc_err_t failed(firc_xt_t *xt, const char *table, firc_err_t err, const char *fmt, ...) {
    char msg[600];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(msg, sizeof(msg), fmt, ap);
    va_end(ap);
    firc_logrep_fail(&xt->failing[table_slot(table)], FIRC_LOG_WARN, NULL, "%s", msg);
    return err;
}

static int ask(firc_xt_t *xt, const firc_xt_ext_desc_t *x, uint8_t *rev) {
    *rev = xt->fam == FIRC_IPT_PROTO_IPV6 ? x->rev6 : x->rev4;
    if (x->bit != FIRC_XT_EXT_MAC) { return xt->ops->get_revision(xt->ud, xt->fam, x->target, x->name, *rev); }
    *rev = 1;
    int e = xt->ops->get_revision(xt->ud, xt->fam, false, x->name, 1);
    if (e == 0) {
        xt->mac_rev = 1;
        return 0;
    }
    if (e != ENOENT && e != EPROTONOSUPPORT) { return e; }
    *rev = 0;
    return xt->ops->get_revision(xt->ud, xt->fam, false, x->name, 0);
}

static firc_err_t probe_set(firc_xt_t *xt, bool capture) {
    firc_err_t worst = FIRC_OK;
    bool blind = false;
    for (size_t i = 0; i < firc_xt_n_exts; i++) {
        const firc_xt_ext_desc_t *x = &firc_xt_exts[i];
        if (x->capture != capture) { continue; }
        uint8_t rev;
        int e = ask(xt, x, &rev);
        if (e == 0 && !blind) {
            xt->missing &= ~x->bit;
            continue;
        }
        xt->missing |= x->bit;
        if (e == 0) { continue; }
        char b[24];
        if (e == ENOENT || e == EPROTONOSUPPORT) {
            FIRC_ERROR("x_tables: no %s %s %s revision %u (%s): %s rules cannot be written", fam_name(xt),
                       x->target ? "target" : "match", x->name, rev, errname(e, b, sizeof(b)), x->name);
            if (worst == FIRC_OK) { worst = FIRC_ERR_NOSYS; }
        } else {
            FIRC_ERROR("x_tables: cannot probe %s %s %s revision %u (%s)", fam_name(xt), x->target ? "target" : "match",
                       x->name, rev, errname(e, b, sizeof(b)));
            worst = FIRC_ERR_IO;
            blind = true;
        }
    }
    return worst;
}

firc_err_t firc_xt_probe(firc_xt_t *xt) {
    xt->mac_rev = 0;
    return probe_set(xt, false);
}

firc_err_t firc_xt_probe_capture(firc_xt_t *xt) { return probe_set(xt, true); }

static void dump_refused(const firc_xt_t *xt, const char *table, const firc_xt_info_t *info, const uint8_t *blob) {
    if (xt->dump_dir == NULL) { return; }
    char path[512];
    int n = snprintf(path, sizeof(path), "%s/refused-%s-%s.bin", xt->dump_dir, fam_name(xt), table);
    if (n < 0 || (size_t)n >= sizeof(path)) { return; }
    size_t len = XT_REPLACE_HDR_LEN + (size_t)info->size;
    char *buf = calloc(1, len);
    if (buf == NULL) { return; }
    xt_replace_hdr_t h;
    memset(&h, 0, sizeof(h));
    memcpy(h.name, table, strnlen(table, XT_TABLE_LEN - 1));
    h.valid_hooks = info->valid_hooks;
    h.num_entries = info->num_entries;
    h.size = info->size;
    memcpy(h.hook_entry, info->hook_entry, sizeof(h.hook_entry));
    memcpy(h.underflow, info->underflow, sizeof(h.underflow));
    memcpy(buf, &h, sizeof(h));
    memcpy(buf + XT_REPLACE_HDR_LEN, blob, info->size);
    if (firc_atomic_write(path, buf, len) != FIRC_OK) { FIRC_DEBUG("x_tables: %s not written", path); }
    free(buf);
}

static bool same_shape(const firc_xt_info_t *a, const firc_xt_info_t *b) {
    if (a->size != b->size || a->num_entries != b->num_entries || a->valid_hooks != b->valid_hooks) { return false; }
    for (int h = 0; h < FIRC_XT_NUMHOOKS; h++) {
        if (!(a->valid_hooks & (1u << h))) { continue; }
        if (a->hook_entry[h] != b->hook_entry[h] || a->underflow[h] != b->underflow[h]) { return false; }
    }
    return true;
}

static const firc_xt_ext_desc_t *first_missing(uint32_t bits) {
    for (size_t i = 0; i < firc_xt_n_exts; i++) {
        if (bits & firc_xt_exts[i].bit) { return &firc_xt_exts[i]; }
    }
    return NULL;
}

typedef struct work {
    uint8_t *read;
    uint8_t *written;
    int32_t *old_index;
    firc_xt_counter_t *old;
    firc_xt_counter_t *carried;
    firc_xt_table_t view;
} work_t;

static void work_free(work_t *w) {
    firc_xt_table_clear(&w->view);
    free(w->read);
    free(w->written);
    free(w->old_index);
    free(w->old);
    free(w->carried);
}

static firc_err_t write_locked(firc_xt_t *xt, const char *table, const firc_xt_info_t *read_info,
                               const firc_xt_info_t *new_info, work_t *w, const times_t *tm, int *refused);

static firc_err_t stage_failed(firc_xt_t *xt, firc_err_t err, const char *what, const char *table) {
    return failed(xt, table, err, "x_tables %s %s: %s (%s)", fam_name(xt), table, what, firc_err_str(err));
}

static firc_err_t commit_table(firc_xt_t *xt, const char *table, const firc_xt_stage_t *stage,
                               firc_cancel_t *cancel, work_t *w) {
    char eb[24];
    const char *fam = fam_name(xt);
    times_t tm = {now_us(), 0, 0, 0, 0, 0};
    firc_xt_info_t info;
    int e = xt->ops->get_info(xt->ud, xt->fam, table, &info);
    if (e != 0) { return failed(xt, table, FIRC_ERR_IO, "x_tables cannot read %s %s (%s)", fam, table, errname(e, eb, sizeof(eb))); }
    w->read = malloc(FIRC_XT_HEADROOM + (info.size ? info.size : 1));
    if (w->read == NULL) { return stage_failed(xt, FIRC_ERR_NOMEM, "no memory for the read", table); }
    uint8_t *read = w->read + FIRC_XT_HEADROOM;
    e = xt->ops->get_entries(xt->ud, xt->fam, table, read, info.size);
    if (e == EAGAIN) {
        FIRC_DEBUG("x_tables %s %s changed while it was read, starting over", fam, table);
        return FIRC_ERR_AGAIN;
    }
    if (e != 0) { return failed(xt, table, FIRC_ERR_IO, "x_tables cannot read %s %s (%s)", fam, table, errname(e, eb, sizeof(eb))); }
    tm.read = now_us();
    const char *bad = NULL;
    firc_err_t err = firc_xt_parse(xt->fam, &info, read, &w->view, &bad);
    if (err == FIRC_ERR_PROTO) {
        return failed(xt, table, err, "x_tables %s %s: the table read back is malformed: %s", fam, table, bad);
    }
    if (err != FIRC_OK) { return stage_failed(xt, err, "the table read back cannot be parsed", table); }
    tm.parse = now_us();
    w->view.mac_rev = xt->mac_rev;
    uint32_t exts = 0;
    char why[512];
    err = firc_xt_merge(&w->view, stage, &exts, why, sizeof(why));
    if (err == FIRC_ERR_INVAL) { return failed(xt, table, err, "x_tables %s %s: %s", fam, table, why); }
    if (err != FIRC_OK) { return stage_failed(xt, err, "the staging cannot be merged", table); }
    const firc_xt_ext_desc_t *gone = first_missing(exts & xt->missing);
    if (gone != NULL) {
        return failed(xt, table, FIRC_ERR_IO, "x_tables %s %s needs %s %s revision %u, which this kernel lacks", fam, table,
                      gone->target ? "target" : "match", gone->name,
                      xt->fam == FIRC_IPT_PROTO_IPV6 ? gone->rev6 : gone->rev4);
    }
    for (size_t i = 0; i < w->view.n_sets; i++) {
        firc_xt_tset_t *s = &w->view.sets[i];
        e = xt->ops->set_index(xt->ud, s->name, &s->index);
        if (e != 0) {
            xt->refusal = e == ENOENT || e == ENOPROTOOPT ? ENOENT : e;
            return failed(xt, table, FIRC_ERR_IO, "x_tables %s %s: the set %s cannot be found in the kernel (%s)", fam,
                          table, s->name, errname(e, eb, sizeof(eb)));
        }
        s->known = true;
    }
    tm.merge = now_us();
    firc_xt_info_t ni;
    err = firc_xt_serialise_room(&w->view, FIRC_XT_HEADROOM, &w->written, &ni, &w->old_index, why, sizeof(why));
    if (err == FIRC_ERR_IO) { return failed(xt, table, err, "x_tables %s %s: %s", fam, table, why); }
    if (err != FIRC_OK) { return stage_failed(xt, err, "the table cannot be written out", table); }
    tm.serialise = now_us();
    bool same = firc_xt_blob_equal(&ni, w->written + FIRC_XT_HEADROOM, &info, read);
    tm.compare = now_us();
    if (same) {
        FIRC_DEBUG("x_tables %s %s: unchanged, read %llu us, parse %llu us, merge %llu us, serialise %llu us, "
                   "compare %llu us",
                   fam, table, us(tm.t0, tm.read), us(tm.read, tm.parse), us(tm.parse, tm.merge),
                   us(tm.merge, tm.serialise), us(tm.serialise, tm.compare));
        (void)firc_logrep_ok(&xt->failing[table_slot(table)], NULL, "x_tables %s %s", fam, table);
        xt->left[table_slot(table)] = info;
        return FIRC_OK;
    }
    if (firc_cancel_raised(cancel)) { return FIRC_ERR_CANCELED; }
    e = xt->ops->lock(xt->ud, FIRC_XT_LOCK_WAIT_MS);
    if (e == EAGAIN) {
        FIRC_DEBUG("x_tables %s %s: another writer holds the xtables lock, starting over", fam, table);
        return FIRC_ERR_AGAIN;
    }
    if (e != 0) {
        return failed(xt, table, FIRC_ERR_IO, "x_tables %s %s: the xtables lock cannot be taken (%s)", fam, table,
                      errname(e, eb, sizeof(eb)));
    }
    int refused = 0;
    err = write_locked(xt, table, &info, &ni, w, &tm, &refused);
    xt->ops->unlock(xt->ud);
    if (refused != 0) {
        xt->refusal = refused;
        dump_refused(xt, table, &ni, w->written + FIRC_XT_HEADROOM);
        return failed(xt, table, FIRC_ERR_IO, "x_tables refused %s %s (%s), %u entries, %u KB", fam, table,
                      errname(refused, eb, sizeof(eb)), ni.num_entries, (ni.size + 1023u) / 1024u);
    }
    if (err == FIRC_ERR_NOMEM) { return stage_failed(xt, err, "no memory for the old counters", table); }
    if (err == FIRC_OK) { xt->left[table_slot(table)] = ni; }
    return err;
}

static firc_err_t write_locked(firc_xt_t *xt, const char *table, const firc_xt_info_t *read_info,
                               const firc_xt_info_t *new_info, work_t *w, const times_t *tm, int *refused) {
    char eb[24];
    const char *fam = fam_name(xt);
    firc_xt_info_t info = *read_info, ni = *new_info;
    firc_xt_info_t again;
    int e = xt->ops->get_info(xt->ud, xt->fam, table, &again);
    if (e != 0 || !same_shape(&again, &info)) {
        FIRC_DEBUG("x_tables %s %s changed since it was read, starting over", fam, table);
        return FIRC_ERR_AGAIN;
    }
    w->old = calloc(info.num_entries ? info.num_entries : 1, sizeof(*w->old));
    if (w->old == NULL) { return FIRC_ERR_NOMEM; }
    e = xt->ops->replace(xt->ud, xt->fam, table, &ni, w->written + FIRC_XT_HEADROOM, info.num_entries, w->old);
    if (e == EAGAIN) {
        FIRC_DEBUG("x_tables %s %s lost a race with another writer, starting over", fam, table);
        return FIRC_ERR_AGAIN;
    }
    if (e != 0) {
        *refused = e;
        return FIRC_ERR_IO;
    }
    uint64_t t3 = now_us();
    if (firc_xt_carry_counters(w->old_index, ni.num_entries, w->old, info.num_entries, &w->carried) == FIRC_OK) {
        e = xt->ops->add_counters(xt->ud, xt->fam, table, w->carried, ni.num_entries);
        if (e != 0) { FIRC_DEBUG("x_tables %s %s: counters not carried (%s)", fam, table, errname(e, eb, sizeof(eb))); }
    }
    uint64_t t4 = now_us();
    FIRC_DEBUG("x_tables %s %s: read %llu us, parse %llu us, merge %llu us, serialise %llu us, compare %llu us, "
               "write %llu us, counters %llu us, %u entries, %u bytes",
               fam, table, us(tm->t0, tm->read), us(tm->read, tm->parse), us(tm->parse, tm->merge),
               us(tm->merge, tm->serialise), us(tm->serialise, tm->compare), us(tm->compare, t3), us(t3, t4),
               ni.num_entries, ni.size);
    (void)firc_logrep_ok(&xt->failing[table_slot(table)], NULL, "x_tables %s %s", fam, table);
    return FIRC_OK;
}

firc_err_t firc_xt_commit(firc_xt_t *xt, const char *table, const firc_xt_stage_t *stage, firc_cancel_t *cancel) {
    if (xt == NULL || table == NULL || stage == NULL) { return FIRC_ERR_INVAL; }
    xt->refusal = 0;
    if (firc_cancel_raised(cancel)) { return FIRC_ERR_CANCELED; }
    memset(&xt->left[table_slot(table)], 0, sizeof(xt->left[0]));
    work_t w;
    memset(&w, 0, sizeof(w));
    firc_err_t err = commit_table(xt, table, stage, cancel, &w);
    work_free(&w);
    return err;
}

int firc_xt_refusal(const firc_xt_t *xt) { return xt->refusal; }

firc_xt_info_t firc_xt_left_shape(const firc_xt_t *xt, const char *table) { return xt->left[table_slot(table)]; }

bool firc_xt_shape_is(firc_xt_t *xt, const char *table, const firc_xt_info_t *want) {
    firc_xt_info_t now;
    return xt->ops->get_info(xt->ud, xt->fam, table, &now) == 0 && same_shape(&now, want);
}

firc_err_t firc_xt_acl_denying(firc_xt_t *xt, const char *table, const char *in_chain, const char *out_chain,
                               firc_xt_acl_t *out) {
    if (out != NULL) { memset(out, 0, sizeof(*out)); }
    if (xt == NULL || table == NULL || in_chain == NULL || out_chain == NULL || out == NULL ||
        xt->fam != FIRC_IPT_PROTO_IPV4) {
        return FIRC_ERR_INVAL;
    }
    firc_xt_info_t info;
    if (xt->ops->get_info(xt->ud, xt->fam, table, &info) != 0) { return FIRC_ERR_IO; }
    uint8_t *raw = malloc(FIRC_XT_HEADROOM + (info.size ? info.size : 1));
    if (raw == NULL) { return FIRC_ERR_NOMEM; }
    int e = xt->ops->get_entries(xt->ud, xt->fam, table, raw + FIRC_XT_HEADROOM, info.size);
    firc_err_t err = e == EAGAIN ? FIRC_ERR_AGAIN : e != 0 ? FIRC_ERR_IO : FIRC_OK;
    firc_xt_table_t view;
    memset(&view, 0, sizeof(view));
    const char *bad = NULL;
    if (err == FIRC_OK) { err = firc_xt_parse(xt->fam, &info, raw + FIRC_XT_HEADROOM, &view, &bad); }
    if (err == FIRC_OK) { err = firc_xt_acl_judge(&view, in_chain, out_chain, out); }
    if (err != FIRC_OK) { memset(out, 0, sizeof(*out)); }
    firc_xt_table_clear(&view);
    free(raw);
    return err;
}
