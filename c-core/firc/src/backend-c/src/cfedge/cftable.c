#include "firc/cfedge.h"

#include <stdlib.h>
#include <string.h>

#include "firc/fakeip.h"
#include "firc/id.h"

#define FAMS 2
#define OK_EVERY_S 600
#define FAIL_EVERY_S 30
#define REPLACE_RATIO 0.8
#define DROP_AFTER 2

typedef struct fam_state {
    bool has;
    firc_fakeip_cf_cand_t target;
    double median_s;
    unsigned fails;
    int64_t next_due;
} fam_state_t;

typedef struct entry {
    struct entry *next;
    char gid[FIRC_ID_STR_LEN];
    fam_state_t fam[FAMS];
} entry_t;

struct firc_cftable {
    entry_t *head;
};

static bool ip_eq(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

static entry_t *find(const firc_cftable_t *t, const char *gid) {
    for (entry_t *e = t->head; e; e = e->next) {
        if (strncmp(e->gid, gid, sizeof(e->gid)) == 0) { return e; }
    }
    return NULL;
}

firc_cftable_t *firc_cftable_new(void) {
    return calloc(1, sizeof(firc_cftable_t));
}

void firc_cftable_free(firc_cftable_t *t) {
    if (!t) { return; }
    while (t->head) {
        entry_t *e = t->head;
        t->head = e->next;
        free(e);
    }
    free(t);
}

bool firc_cftable_target(const firc_cftable_t *t, const char *gid, unsigned fam, firc_fakeip_cf_cand_t *out) {
    const entry_t *e = fam < FAMS ? find(t, gid) : NULL;
    if (!e || !e->fam[fam].has) { return false; }
    *out = e->fam[fam].target;
    return true;
}

bool firc_cftable_due(const firc_cftable_t *t, const char *gid, unsigned fam, int64_t now) {
    if (fam >= FAMS) { return false; }
    const entry_t *e = find(t, gid);
    return !e || now >= e->fam[fam].next_due;
}

static entry_t *get(firc_cftable_t *t, const char *gid) {
    entry_t *e = find(t, gid);
    if (e) { return e; }
    e = calloc(1, sizeof(*e));
    if (!e) { return NULL; }
    strncpy(e->gid, gid, sizeof(e->gid) - 1);
    e->next = t->head;
    t->head = e;
    return e;
}

static const firc_cfprobe_t *fastest_ok(const firc_cfprobe_t *r, size_t n) {
    const firc_cfprobe_t *best = NULL;
    for (size_t i = 0; i < n; i++) {
        if (r[i].ok && (!best || r[i].median_s < best->median_s)) { best = &r[i]; }
    }
    return best;
}

static void adopt(fam_state_t *s, const firc_cfprobe_t *p) {
    s->has = true;
    s->target.addr = p->addr;
    memcpy(s->target.name, p->name, sizeof(s->target.name));
    s->median_s = p->median_s;
    s->fails = 0;
}

int firc_cftable_apply(firc_cftable_t *t, const char *gid, unsigned fam, const firc_cfprobe_t *r, size_t n,
                       int64_t now) {
    if (fam >= FAMS) { return 0; }
    entry_t *e = get(t, gid);
    if (!e) { return 0; }
    fam_state_t *s = &e->fam[fam];
    const firc_cfprobe_t *best = fastest_ok(r, n);
    int rc = 0;
    bool failed = false;

    if (!s->has) {
        if (best) {
            adopt(s, best);
            rc = 1;
        } else {
            failed = true;
        }
    } else {
        const firc_cfprobe_t *cur = NULL;
        for (size_t i = 0; i < n; i++) {
            if (ip_eq(&r[i].addr, &s->target.addr)) {
                cur = &r[i];
                break;
            }
        }
        if (cur && cur->ok) {
            s->fails = 0;
            s->median_s = cur->median_s;
        } else if (cur) {
            s->fails++;
            failed = true;
        }
        bool dead = s->fails >= DROP_AFTER;
        if (best && !(cur && cur->ok && ip_eq(&best->addr, &s->target.addr)) &&
            (dead || best->median_s <= REPLACE_RATIO * s->median_s)) {
            adopt(s, best);
            rc = 1;
        } else if (dead) {
            s->has = false;
            s->fails = 0;
            rc = -1;
        }
    }
    s->next_due = now + (failed ? FAIL_EVERY_S : OK_EVERY_S);
    return rc;
}

bool firc_cftable_recheck(firc_cftable_t *t, const char *gid) {
    entry_t *e = find(t, gid);
    if (!e) { return false; }
    for (unsigned f = 0; f < FAMS; f++) { e->fam[f].next_due = 0; }
    return true;
}

void firc_cftable_forget(firc_cftable_t *t, const char *gid) {
    for (entry_t **pp = &t->head; *pp; pp = &(*pp)->next) {
        if (strncmp((*pp)->gid, gid, sizeof((*pp)->gid)) == 0) {
            entry_t *e = *pp;
            *pp = e->next;
            free(e);
            return;
        }
    }
}
