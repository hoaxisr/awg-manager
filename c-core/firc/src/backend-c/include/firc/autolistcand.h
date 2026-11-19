#ifndef FIRC_AUTOLISTCAND_H
#define FIRC_AUTOLISTCAND_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip.h"

#define FIRC_AUTOLIST_CANDIDATES 64
#define FIRC_AUTOLIST_DISMISS_SECS 86400
#define FIRC_AUTOLIST_CAND_CLIENTS 4
#define FIRC_AUTOLIST_CAND_NAME 254

typedef enum {
    FIRC_AL_WHY_NO_ANSWER = 1,
    FIRC_AL_WHY_RESET = 2,
    FIRC_AL_WHY_FREEZE = 3,
    FIRC_AL_WHY_NO_CONNECT = 4,
} firc_al_why_t;

typedef enum {
    FIRC_AL_CAND = 0,
    FIRC_AL_CHECKING,
    FIRC_AL_ADDED,
    FIRC_AL_OFF,
} firc_al_state_t;

typedef struct firc_al_cand {
    char domain[FIRC_AUTOLIST_CAND_NAME];
    char host[FIRC_AUTOLIST_CAND_NAME];
    uint8_t why;
    uint8_t state;
    uint8_t notes;
    bool by_domain;
    uint32_t fails;
    firc_ip_t clients[FIRC_AUTOLIST_CAND_CLIENTS];
    uint8_t n_clients;
    int64_t last_at;
} firc_al_cand_t;

typedef struct firc_al_cands firc_al_cands_t;

/* One allocation; nothing is allocated afterwards. */
firc_al_cands_t *firc_al_cands_new(void);
void firc_al_cands_free(firc_al_cands_t *c);

/* Add or refresh a candidate; NULL when dismissed within a day or a name does not fit. */
firc_al_cand_t *firc_al_cands_note(firc_al_cands_t *c, const char *domain, const char *host, firc_al_why_t why,
                                   uint32_t fails, const firc_ip_t *client, int64_t now);
firc_al_cand_t *firc_al_cands_find(firc_al_cands_t *c, const char *domain);

/* Drop a candidate and keep it out for a day; false when it was not listed. */
bool firc_al_cands_dismiss(firc_al_cands_t *c, const char *domain, int64_t now);

/* Drop a candidate without keeping it out; false when it was not listed. */
bool firc_al_cands_forget(firc_al_cands_t *c, const char *domain);

/* Candidates newest first, at most cap; returns how many were written. */
size_t firc_al_cands_list(const firc_al_cands_t *c, const firc_al_cand_t **out, size_t cap);

#endif
