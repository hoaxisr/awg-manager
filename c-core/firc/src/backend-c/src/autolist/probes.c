#include "firc/autolistprobes.h"

#include <stdlib.h>
#include <string.h>

enum { AL_NOTE = 0, AL_RECHECK = 1, AL_USER = 2 };

#define AL_INFLIGHT 8
#define AL_INFLIGHT_SECS 120
#define AL_SHOWN_SECS 3600

typedef struct {
    char host[FIRC_AUTOLIST_NAME_MAX];
    uint32_t notes;
    uint8_t kind;
    bool by_domain;
    uint64_t born;
    uint64_t order;
    bool used;
} al_q_t;

typedef struct {
    char host[FIRC_AUTOLIST_NAME_MAX];
    firc_autolist_result_t result;
    int64_t at;
    int64_t until;
    int64_t shown_until;
    uint64_t touch;
    bool used;
} al_c_t;

struct firc_al_probes {
    al_q_t q[FIRC_AUTOLIST_PROBES_QUEUE];
    al_c_t c[FIRC_AUTOLIST_PROBE_CACHE];
    char inflight[AL_INFLIGHT][FIRC_AUTOLIST_NAME_MAX];
    int64_t inflight_at[AL_INFLIGHT];
    bool inflight_used[AL_INFLIGHT];
    int64_t starts[FIRC_AUTOLIST_PROBES_PER_HOUR];
    uint64_t seq;
};

static bool al_ok(const char *h) {
    return h != NULL && h[0] != '\0' && strnlen(h, FIRC_AUTOLIST_NAME_MAX) < FIRC_AUTOLIST_NAME_MAX;
}

static uint32_t al_used(const firc_al_probes_t *p, int64_t now) {
    uint32_t n = 0;
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_PER_HOUR; i++) {
        if (p->starts[i] != 0 && p->starts[i] > now - 3600) { n++; }
    }
    return n;
}

static al_q_t *al_q_find(firc_al_probes_t *p, const char *h) {
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_QUEUE; i++) {
        if (p->q[i].used && strcmp(p->q[i].host, h) == 0) { return &p->q[i]; }
    }
    return NULL;
}

static const al_c_t *al_c_find(const firc_al_probes_t *p, const char *h) {
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBE_CACHE; i++) {
        if (p->c[i].used && strcmp(p->c[i].host, h) == 0) { return &p->c[i]; }
    }
    return NULL;
}

static al_c_t *al_c_find_mut(firc_al_probes_t *p, const char *h) {
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBE_CACHE; i++) {
        if (p->c[i].used && strcmp(p->c[i].host, h) == 0) { return &p->c[i]; }
    }
    return NULL;
}

static bool al_slot_live(const firc_al_probes_t *p, size_t i, int64_t now) {
    return p->inflight_used[i] && p->inflight_at[i] > now - AL_INFLIGHT_SECS;
}

static bool al_inflight(const firc_al_probes_t *p, const char *h, int64_t now) {
    for (size_t i = 0; i < AL_INFLIGHT; i++) {
        if (al_slot_live(p, i, now) && strcmp(p->inflight[i], h) == 0) { return true; }
    }
    return false;
}

static al_q_t *al_q_slot(firc_al_probes_t *p, uint8_t kind) {
    al_q_t *note = NULL, *recheck = NULL;
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_QUEUE; i++) {
        al_q_t *e = &p->q[i];
        if (!e->used) { return e; }
        if (e->kind == AL_NOTE && (note == NULL || e->born < note->born)) { note = e; }
        if (e->kind == AL_RECHECK && (recheck == NULL || e->born < recheck->born)) { recheck = e; }
    }
    if (note != NULL) { return note; }
    return kind == AL_USER ? recheck : NULL;
}

static firc_al_ask_t al_add(firc_al_probes_t *p, const char *h, uint8_t kind) {
    al_q_t *e = al_q_find(p, h);
    if (e == NULL) {
        e = al_q_slot(p, kind);
        if (e == NULL) { return FIRC_AL_ASK_FULL; }
        memset(e, 0, sizeof(*e));
        memcpy(e->host, h, strlen(h) + 1);
        e->used = true;
        e->born = ++p->seq;
        e->order = e->born;
        e->kind = kind;
    } else if (kind > e->kind) {
        e->kind = kind;
        e->order = ++p->seq;
    }
    if (kind == AL_NOTE && e->notes < UINT32_MAX) { e->notes++; }
    return FIRC_AL_ASK_QUEUED;
}

firc_al_probes_t *firc_al_probes_new(void) { return calloc(1, sizeof(firc_al_probes_t)); }

void firc_al_probes_free(firc_al_probes_t *p) { free(p); }

firc_al_ask_t firc_al_probes_nominate(firc_al_probes_t *p, const char *host, bool by_domain, int64_t now) {
    if (p == NULL || !al_ok(host)) { return FIRC_AL_ASK_BUSY; }
    const al_c_t *c = al_c_find(p, host);
    if (c != NULL && c->until > now) { return FIRC_AL_ASK_CACHED; }
    if (al_inflight(p, host, now)) { return FIRC_AL_ASK_QUEUED; }
    firc_al_ask_t ask = al_add(p, host, AL_NOTE);
    al_q_t *e = ask == FIRC_AL_ASK_QUEUED ? al_q_find(p, host) : NULL;
    if (e != NULL) { e->by_domain = by_domain; }
    return ask;
}

firc_al_ask_t firc_al_probes_request(firc_al_probes_t *p, const char *host, int64_t now) {
    if (p == NULL || !al_ok(host)) { return FIRC_AL_ASK_BUSY; }
    const al_c_t *c = al_c_find(p, host);
    if (al_inflight(p, host, now) || (c != NULL && c->at > now - FIRC_AUTOLIST_PROBE_HOST_SECS)) {
        return FIRC_AL_ASK_BUSY;
    }
    if (al_used(p, now) >= FIRC_AUTOLIST_PROBES_PER_HOUR) { return FIRC_AL_ASK_FULL; }
    return al_add(p, host, AL_USER);
}

firc_al_ask_t firc_al_probes_recheck(firc_al_probes_t *p, const char *host, int64_t now) {
    if (p == NULL || !al_ok(host) || al_inflight(p, host, now)) { return FIRC_AL_ASK_BUSY; }
    return al_add(p, host, AL_RECHECK);
}

static bool al_before(const al_q_t *a, const al_q_t *b) {
    if (a->kind != b->kind) { return a->kind > b->kind; }
    if (a->kind == AL_NOTE) {
        if (a->notes != b->notes) { return a->notes > b->notes; }
        return a->born < b->born;
    }
    return a->order < b->order;
}

bool firc_al_probes_next(firc_al_probes_t *p, int64_t now, char *host, size_t cap, bool allow_notes, bool *recheck,
                         bool *by_domain) {
    if (p == NULL || host == NULL) { return false; }
    if (al_used(p, now) >= FIRC_AUTOLIST_PROBES_PER_HOUR) { return false; }
    al_q_t *best = NULL;
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_QUEUE; i++) {
        if (p->q[i].used && (allow_notes || p->q[i].kind != AL_NOTE) && (best == NULL || al_before(&p->q[i], best))) {
            best = &p->q[i];
        }
    }
    if (best == NULL) { return false; }
    size_t n = strlen(best->host);
    if (n >= cap) { return false; }
    size_t slot = 0;
    while (slot < AL_INFLIGHT && al_slot_live(p, slot, now)) { slot++; }
    if (slot == AL_INFLIGHT) { return false; }
    size_t ring = 0;
    for (size_t i = 1; i < FIRC_AUTOLIST_PROBES_PER_HOUR; i++) {
        if (p->starts[i] < p->starts[ring]) { ring = i; }
    }
    p->starts[ring] = now == 0 ? 1 : now;
    memcpy(host, best->host, n + 1);
    memcpy(p->inflight[slot], best->host, n + 1);
    p->inflight_used[slot] = true;
    p->inflight_at[slot] = now;
    if (recheck != NULL) { *recheck = best->kind == AL_RECHECK; }
    if (by_domain != NULL) { *by_domain = best->by_domain; }
    best->used = false;
    return true;
}

void firc_al_probes_done(firc_al_probes_t *p, const char *host, const firc_autolist_result_t *r, int64_t now) {
    if (p == NULL || !al_ok(host) || r == NULL) { return; }
    for (size_t i = 0; i < AL_INFLIGHT; i++) {
        if (p->inflight_used[i] && strcmp(p->inflight[i], host) == 0) { p->inflight_used[i] = false; }
    }
    al_c_t *c = al_c_find_mut(p, host);
    if (c == NULL) {
        for (size_t i = 0; i < FIRC_AUTOLIST_PROBE_CACHE; i++) {
            if (!p->c[i].used) {
                c = &p->c[i];
                break;
            }
            if (c == NULL || p->c[i].touch < c->touch) { c = &p->c[i]; }
        }
    }
    memset(c, 0, sizeof(*c));
    memcpy(c->host, host, strlen(host) + 1);
    c->result = *r;
    c->at = now;
    c->touch = ++p->seq;
    c->used = true;
    if (r->verdict == FIRC_AUTOLIST_REACHABLE) {
        c->until = now + FIRC_AUTOLIST_CACHE_OPEN_SECS;
    } else if (r->verdict == FIRC_AUTOLIST_UNKNOWN) {
        c->until = now + FIRC_AUTOLIST_CACHE_UNKNOWN_SECS;
    } else {
        c->until = now;
    }
    c->shown_until = c->until > now + AL_SHOWN_SECS ? c->until : now + AL_SHOWN_SECS;
}

const firc_autolist_result_t *firc_al_probes_result(const firc_al_probes_t *p, const char *host, int64_t now,
                                                    int64_t *at) {
    if (p == NULL || !al_ok(host)) { return NULL; }
    const al_c_t *c = al_c_find(p, host);
    if (c == NULL || c->shown_until <= now) { return NULL; }
    if (at != NULL) { *at = c->at; }
    return &c->result;
}

void firc_al_probes_counts(const firc_al_probes_t *p, int64_t now, uint32_t *used_hour, uint32_t *queued) {
    if (p == NULL) { return; }
    if (used_hour != NULL) { *used_hour = al_used(p, now); }
    if (queued != NULL) {
        uint32_t n = 0;
        for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_QUEUE; i++) {
            if (p->q[i].used) { n++; }
        }
        *queued = n;
    }
}

size_t firc_al_probes_results(const firc_al_probes_t *p, int64_t now, firc_al_probe_seen_t *out, size_t cap) {
    if (p == NULL || out == NULL) { return 0; }
    size_t n = 0;
    uint64_t last = UINT64_MAX;
    while (n < cap) {
        const al_c_t *best = NULL;
        for (size_t i = 0; i < FIRC_AUTOLIST_PROBE_CACHE; i++) {
            const al_c_t *c = &p->c[i];
            if (!c->used || c->shown_until <= now || c->touch >= last) { continue; }
            if (best == NULL || c->touch > best->touch) { best = c; }
        }
        if (best == NULL) { break; }
        memcpy(out[n].host, best->host, sizeof(out[n].host));
        out[n].result = best->result;
        out[n].at = best->at;
        last = best->touch;
        n++;
    }
    return n;
}

bool firc_al_probes_rechecking(const firc_al_probes_t *p) {
    if (p == NULL) { return false; }
    for (size_t i = 0; i < FIRC_AUTOLIST_PROBES_QUEUE; i++) {
        if (p->q[i].used && p->q[i].kind == AL_RECHECK) { return true; }
    }
    return false;
}
