#ifndef FIRC_ROUTER_ID_H
#define FIRC_ROUTER_ID_H

#include <stdbool.h>

#include "firc/err.h"

typedef struct {
    char hwid[17];
    char release[64];
    char model[64];
} firc_router_id_t;

typedef firc_err_t (*firc_router_id_get_fn)(void *ud, const char *path, char **body);

/* The first 16 hex digits of sha256("firc-hwid\0" serial). */
void firc_router_id_from_serial(const char *serial, char out[17]);

/* The id stored at path wins over id->hwid; else a hwid already set is stored, or, when random_ok, a random one is
   made and stored (0600, parents 0700); else hwid stays empty. A failed write keeps the id for this run. */
void firc_router_id_settle(firc_router_id_t *id, const char *path, bool random_ok);

typedef struct firc_router_id_retry firc_router_id_retry_t;

/* Asks show version and show defaults on a thread of its own, up to tries rounds pause_ms apart, until both have
   answered and the latter with a serial; NULL when it cannot start. */
firc_router_id_retry_t *firc_router_id_retry_start(firc_router_id_get_fn get, void *ud, int tries, int pause_ms);

/* True once, when the thread has ended on a serial or out of tries, with what it gathered in out; once nothing is
   left to take, stops it and sets *r to NULL. Never waits on RCI; NULL-safe. */
bool firc_router_id_retry_poll(firc_router_id_retry_t **r, firc_router_id_t *out);

void firc_router_id_retry_hook_for_test(firc_router_id_retry_t *r, void (*between)(void *ud), void *ud); /* test seam */

/* True when the thread has ended, answered or out of tries. */
bool firc_router_id_retry_done(firc_router_id_retry_t *r);

/* Stops and joins the thread: a pause ends within 50 ms, an RCI call in flight runs to its timeout. NULL-safe. */
void firc_router_id_retry_stop(firc_router_id_retry_t *r);

/* Asks show version only, takes the id stored at path; the retry asks what is missing (show defaults only without
   a stored id), and meanwhile out->hwid stays empty (random when it cannot start). Returns the retry, or NULL. */
firc_router_id_retry_t *firc_router_id_begin(firc_router_id_get_fn get, void *ud, int tries, int pause_ms,
                                             const char *path, firc_router_id_t *out);

/* True when out holds the identity to apply now: the retry's late answer, or what it settles to once the retry gave
   up; *r is NULL from then on. Never waits on RCI; NULL-safe. */
bool firc_router_id_next(firc_router_id_retry_t **r, const char *path, firc_router_id_t *out);

#endif
