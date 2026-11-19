#ifndef FIRC_IFCHOOSE_H
#define FIRC_IFCHOOSE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define FIRC_IFCHOOSE_CONFIRM_MS 3000
#define FIRC_IFCHOOSE_HOLD_MS 120000
#define FIRC_IFCHOOSE_ALL_DEAD_ERROR_MS 300000
#define FIRC_IF_ERROR_MAX 160

typedef enum { FIRC_IF_UNKNOWN = 0, FIRC_IF_ALIVE, FIRC_IF_DEAD } firc_if_state_t;

typedef struct firc_if_track {
    firc_if_state_t state;
    int64_t since_ms;
    int64_t clean_since_ms;
    bool suspect;
    char error[FIRC_IF_ERROR_MAX];
} firc_if_track_t;

typedef enum { FIRC_IF_KEEP, FIRC_IF_CONFIRM, FIRC_IF_CHANGED } firc_if_step_t;

typedef struct firc_ifchoice {
    size_t active;
    bool all_dead;
} firc_ifchoice_t;

typedef struct firc_if_view {
    firc_if_state_t state;
    int64_t since_unix;
    char error[FIRC_IF_ERROR_MAX];
} firc_if_view_t;

const char *firc_if_state_name(firc_if_state_t s);
void firc_if_track_init(firc_if_track_t *t, int64_t now_ms);

/* A check's result; with control_failed a failure concludes nothing.
 * CONFIRM asks for a second check in FIRC_IFCHOOSE_CONFIRM_MS. */
firc_if_step_t firc_if_track_check(firc_if_track_t *t, bool passed, bool control_failed, const char *error,
                                   int64_t now_ms);

/* A link gone or not IFF_UP is dead at once; a link coming up changes nothing until a check passes. */
firc_if_step_t firc_if_track_link(firc_if_track_t *t, bool up, int64_t now_ms);

/* A tunnel's status: UP is alive, anything else is dead. */
firc_if_step_t firc_if_track_tunnel(firc_if_track_t *t, bool up, int64_t now_ms);

/* The entry a group's table points at, given the one it points at now; all_dead keeps active. */
firc_ifchoice_t firc_ifchoose(const firc_if_track_t *const *e, size_t n, size_t active, int64_t now_ms);

/* Whether the move-up button may move the group to its first entry now. */
bool firc_ifchoose_first_alive(const firc_if_track_t *const *e, size_t n);

#endif
