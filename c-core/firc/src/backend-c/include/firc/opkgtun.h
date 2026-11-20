#ifndef FIRC_OPKGTUN_H
#define FIRC_OPKGTUN_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

#define FIRC_OPKGTUN_LIST_PATH "/rci/show/rc/interface"
#define FIRC_OPKGTUN_NS_PATH "/rci/show/rc/ip/name-server"
#define FIRC_OPKGTUN_ROUTE_PATH "/rci/show/rc/ip/route"
#define FIRC_OPKGTUN_MAX_WANTS 32
#define FIRC_OPKGTUN_DESC_MAX 256

typedef struct {
    firc_err_t (*get)(void *ud, const char *path, char **body);
    firc_err_t (*post)(void *ud, const char *body, long timeout_s, char **out);
    bool (*dev_exists)(void *ud, const char *dev);
    firc_err_t (*unpersist)(void *ud, const char *dev);
    bool (*addr_holder)(void *ud, int n, const char *own, char out[16]);
    int64_t (*now_ms)(void *ud);
    bool (*sleep_ms)(void *ud, int ms);
    const char *intent_path;
    atomic_bool *stop;
    void *ud;
    atomic_bool *cut;
} firc_opkgtun_io_t;

typedef enum { FIRC_OPKG_PENDING, FIRC_OPKG_READY, FIRC_OPKG_FAILED, FIRC_OPKG_FOREIGN } firc_opkg_state_t;

typedef enum { FIRC_OPKG_POL_OFF, FIRC_OPKG_POL_ON, FIRC_OPKG_POL_FAILED } firc_opkg_pol_t;

typedef struct {
    char name[16];
    char device[16];
    int n;
    char mark[24];
    char desc[85];
    bool present;
    uint32_t recheck;
    bool policies;
    bool route;
    bool keep;
    uint32_t checked;
} firc_opkgtun_want_t;

typedef struct {
    char name[16];
    firc_opkg_state_t state;
    bool saved;
    bool removing;
    uint32_t rechecked;
    char error[160];
    firc_opkg_pol_t policies;
    char pol_error[160];
    bool route;
    uint32_t route_reads;
    uint32_t route_adds;
} firc_opkgtun_seen_t;

typedef enum { FIRC_OPKG_RC_ABSENT, FIRC_OPKG_RC_OURS, FIRC_OPKG_RC_UNMARKED, FIRC_OPKG_RC_FOREIGN } firc_opkg_rc_kind_t;

typedef struct {
    firc_opkg_rc_kind_t kind;
    char mark[24];
    char description[FIRC_OPKGTUN_DESC_MAX + 1];
    bool public_level;
    char address[16];
    char mask[16];
    bool up;
    bool global;
} firc_opkgtun_rc_t;

typedef enum { FIRC_OPKG_ITEM_APPLIED, FIRC_OPKG_ITEM_PARTIAL, FIRC_OPKG_ITEM_REFUSED } firc_opkg_item_t;

typedef struct firc_opkgtun firc_opkgtun_t;

typedef struct {
    atomic_bool stop;
    atomic_bool cut;
    char base_url[128];
} firc_opkgtun_rci_t;

/* Binds io to RCI at base_url, the system's devices and addresses and a monotonic clock; io->stop is rci's own
   flag, which no other RCI caller shares, and every call and sleep of io ends on it. */
void firc_opkgtun_rci_io(firc_opkgtun_rci_t *rci, const char *base_url, const char *intent_path,
                         firc_opkgtun_io_t *io);

/* Starts the worker thread on io (copied; io->stop is raised by firc_opkgtun_stop). NULL when out of memory. A
   host build starts no thread. */
firc_opkgtun_t *firc_opkgtun_start(const firc_opkgtun_io_t *io);

/* Replaces the wanted set: present records to keep configured, absent ones to remove. A set over
   FIRC_OPKGTUN_MAX_WANTS, or naming a record twice, is warned and the last set stays. A change of only policies,
   route, keep or checked keeps the record's state; keep leaves a route line as found and adds none. No save starts
   while a route added, or found after a start with unsaved changes, is not yet checked. */
void firc_opkgtun_want(firc_opkgtun_t *m, const firc_opkgtun_want_t *w, size_t n);

/* Queues a removal of the default route via name ahead of every other job, and cuts a save in flight. */
void firc_opkgtun_trip(firc_opkgtun_t *m, const char *name);

/* Queues a sweep of firc's records no want names; the wants are read when it runs. */
void firc_opkgtun_sweep_now(firc_opkgtun_t *m);

/* Copies the state of every want into out; returns how many, at most cap. */
size_t firc_opkgtun_seen(firc_opkgtun_t *m, firc_opkgtun_seen_t *out, size_t cap);

/* Bit N set for each OpkgTun<N> a sweep removed since the last call; clears them. */
uint32_t firc_opkgtun_swept(firc_opkgtun_t *m);

/* Readable whenever the seen state or the swept set changed; drained by firc_opkgtun_seen. */
int firc_opkgtun_fd(const firc_opkgtun_t *m);

/* Raises io->stop, joins the worker and frees m; returns within about a second. NULL-safe. */
void firc_opkgtun_stop(firc_opkgtun_t *m);

/* One pass with no retry: removes every record of firc's no keep names, then saves when anything is unsaved.
   FIRC_ERR_STATE when the records went but the save failed; *unsaved (nullable) whenever a save failed. */
firc_err_t firc_opkgtun_sweep(const firc_opkgtun_io_t *io, const firc_opkgtun_want_t *keep, size_t n_keep,
                              size_t *removed, bool *unsaved);

/* Classifies name in a GET of FIRC_OPKGTUN_LIST_PATH; FIRC_ERR_PROTO when the body is not a good list. */
firc_err_t firc_opkgtun_classify(const char *list_json, const char *name, firc_opkgtun_rc_t *out);

/* True when description carries firc's mark. */
bool firc_opkgtun_ours(const char *description);

/* Reads a POST answer into one outcome per item and the first error's message; the item count, or -1. */
int firc_opkgtun_items(const char *answer, firc_opkg_item_t *out, size_t cap, char *error, size_t error_cap);

/* "firc:<id> <desc>", or "firc:<id>" for an empty desc; at most 84 bytes, so never cut. NULL for an id over 15
   bytes, a desc over 63, or no memory; caller frees. */
char *firc_opkgtun_description(const char *id, const char *desc);

/* Lets a host build start the worker thread, for tests. */
void firc_opkgtun_reach_for_test(bool on);

/* True while the worker waits with nothing due. */
bool firc_opkgtun_idle_for_test(firc_opkgtun_t *m);

#endif
