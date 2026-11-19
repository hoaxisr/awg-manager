#include "firc/autolistcand.h"

#include <stdlib.h>
#include <string.h>

typedef struct {
    char domain[FIRC_AUTOLIST_CAND_NAME];
    int64_t until;
    bool used;
} al_dismissed_t;

struct firc_al_cands {
    firc_al_cand_t cand[FIRC_AUTOLIST_CANDIDATES];
    uint64_t seq[FIRC_AUTOLIST_CANDIDATES];
    bool used[FIRC_AUTOLIST_CANDIDATES];
    uint64_t next_seq;
    al_dismissed_t dismissed[FIRC_AUTOLIST_CANDIDATES];
    size_t dismissed_next;
};

static bool al_fits(const char *s) { return s != NULL && strnlen(s, FIRC_AUTOLIST_CAND_NAME) < FIRC_AUTOLIST_CAND_NAME; }

static bool al_ip_eq(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

firc_al_cands_t *firc_al_cands_new(void) { return calloc(1, sizeof(firc_al_cands_t)); }

void firc_al_cands_free(firc_al_cands_t *c) { free(c); }

static int al_index(const firc_al_cands_t *c, const char *domain) {
    for (int i = 0; i < FIRC_AUTOLIST_CANDIDATES; i++) {
        if (c->used[i] && strcmp(c->cand[i].domain, domain) == 0) { return i; }
    }
    return -1;
}

static bool al_blocked(firc_al_cands_t *c, const char *domain, int64_t now) {
    for (int i = 0; i < FIRC_AUTOLIST_CANDIDATES; i++) {
        al_dismissed_t *d = &c->dismissed[i];
        if (!d->used || strcmp(d->domain, domain) != 0) { continue; }
        if (now < d->until) { return true; }
        d->used = false;
    }
    return false;
}

static int al_slot(const firc_al_cands_t *c) {
    int oldest = 0;
    for (int i = 0; i < FIRC_AUTOLIST_CANDIDATES; i++) {
        if (!c->used[i]) { return i; }
        if (c->seq[i] < c->seq[oldest]) { oldest = i; }
    }
    return oldest;
}

firc_al_cand_t *firc_al_cands_note(firc_al_cands_t *c, const char *domain, const char *host, firc_al_why_t why,
                                   uint32_t fails, const firc_ip_t *client, int64_t now) {
    if (c == NULL || !al_fits(domain) || !al_fits(host)) { return NULL; }
    if (al_blocked(c, domain, now)) { return NULL; }
    int i = al_index(c, domain);
    if (i < 0) {
        i = al_slot(c);
        memset(&c->cand[i], 0, sizeof(c->cand[i]));
        c->used[i] = true;
        strcpy(c->cand[i].domain, domain);
        c->cand[i].state = FIRC_AL_CAND;
    }
    firc_al_cand_t *e = &c->cand[i];
    strcpy(e->host, host);
    e->why = (uint8_t)why;
    e->fails = fails;
    e->last_at = now;
    c->seq[i] = ++c->next_seq;
    if (client != NULL && e->n_clients < FIRC_AUTOLIST_CAND_CLIENTS) {
        bool seen = false;
        for (uint8_t k = 0; k < e->n_clients; k++) { seen = seen || al_ip_eq(&e->clients[k], client); }
        if (!seen) { e->clients[e->n_clients++] = *client; }
    }
    return e;
}

firc_al_cand_t *firc_al_cands_find(firc_al_cands_t *c, const char *domain) {
    if (c == NULL || domain == NULL) { return NULL; }
    int i = al_index(c, domain);
    return i < 0 ? NULL : &c->cand[i];
}

bool firc_al_cands_dismiss(firc_al_cands_t *c, const char *domain, int64_t now) {
    if (c == NULL || !al_fits(domain)) { return false; }
    int i = al_index(c, domain);
    if (i < 0) { return false; }
    c->used[i] = false;
    al_dismissed_t *d = &c->dismissed[c->dismissed_next];
    c->dismissed_next = (c->dismissed_next + 1) % FIRC_AUTOLIST_CANDIDATES;
    strcpy(d->domain, domain);
    d->until = now + FIRC_AUTOLIST_DISMISS_SECS;
    d->used = true;
    return true;
}

bool firc_al_cands_forget(firc_al_cands_t *c, const char *domain) {
    if (c == NULL || !al_fits(domain)) { return false; }
    int i = al_index(c, domain);
    if (i < 0) { return false; }
    c->used[i] = false;
    return true;
}

size_t firc_al_cands_list(const firc_al_cands_t *c, const firc_al_cand_t **out, size_t cap) {
    if (c == NULL || out == NULL) { return 0; }
    int order[FIRC_AUTOLIST_CANDIDATES];
    size_t n = 0;
    for (int i = 0; i < FIRC_AUTOLIST_CANDIDATES; i++) {
        if (!c->used[i]) { continue; }
        size_t k = n++;
        while (k > 0 && c->seq[order[k - 1]] < c->seq[i]) {
            order[k] = order[k - 1];
            k--;
        }
        order[k] = i;
    }
    if (n > cap) { n = cap; }
    for (size_t k = 0; k < n; k++) { out[k] = &c->cand[order[k]]; }
    return n;
}
