#include "firc/ifchoose.h"

#include <stdio.h>
#include <string.h>

const char *firc_if_state_name(firc_if_state_t s) {
    switch (s) {
    case FIRC_IF_ALIVE: return "alive";
    case FIRC_IF_DEAD: return "dead";
    case FIRC_IF_UNKNOWN: break;
    }
    return "unknown";
}

void firc_if_track_init(firc_if_track_t *t, int64_t now_ms) {
    memset(t, 0, sizeof(*t));
    t->state = FIRC_IF_UNKNOWN;
    t->since_ms = now_ms;
    t->clean_since_ms = -1;
}

static firc_if_step_t enter(firc_if_track_t *t, firc_if_state_t s, int64_t now_ms) {
    if (t->state == s) { return FIRC_IF_KEEP; }
    t->state = s;
    t->since_ms = now_ms;
    return FIRC_IF_CHANGED;
}

static void note(firc_if_track_t *t, const char *error) {
    snprintf(t->error, sizeof(t->error), "%s", error != NULL ? error : "");
}

firc_if_step_t firc_if_track_check(firc_if_track_t *t, bool passed, bool control_failed, const char *error,
                                   int64_t now_ms) {
    if (passed) {
        t->suspect = false;
        t->error[0] = '\0';
        if (t->clean_since_ms < 0) { t->clean_since_ms = now_ms; }
        return enter(t, FIRC_IF_ALIVE, now_ms);
    }
    if (control_failed) { return FIRC_IF_KEEP; }
    note(t, error);
    t->clean_since_ms = -1;
    if (t->state == FIRC_IF_DEAD) { return FIRC_IF_KEEP; }
    if (!t->suspect) {
        t->suspect = true;
        return FIRC_IF_CONFIRM;
    }
    t->suspect = false;
    return enter(t, FIRC_IF_DEAD, now_ms);
}

firc_if_step_t firc_if_track_link(firc_if_track_t *t, bool up, int64_t now_ms) {
    if (up) { return FIRC_IF_KEEP; }
    t->suspect = false;
    t->clean_since_ms = -1;
    note(t, "the link is down or gone");
    return enter(t, FIRC_IF_DEAD, now_ms);
}

firc_if_step_t firc_if_track_tunnel(firc_if_track_t *t, bool up, int64_t now_ms) {
    if (up) {
        t->error[0] = '\0';
        if (t->state != FIRC_IF_ALIVE) { t->clean_since_ms = now_ms; }
        return enter(t, FIRC_IF_ALIVE, now_ms);
    }
    t->clean_since_ms = -1;
    note(t, "the tunnel carries no traffic");
    return enter(t, FIRC_IF_DEAD, now_ms);
}

static bool held(const firc_if_track_t *t, int64_t now_ms) {
    return t->state == FIRC_IF_ALIVE && t->clean_since_ms >= 0 && now_ms - t->clean_since_ms >= FIRC_IFCHOOSE_HOLD_MS;
}

firc_ifchoice_t firc_ifchoose(const firc_if_track_t *const *e, size_t n, size_t active, int64_t now_ms) {
    firc_ifchoice_t c = {active < n ? active : 0, false};
    if (n == 0) { return c; }
    size_t first_alive = n, first_unknown = n;
    for (size_t i = 0; i < n; i++) {
        if (e[i]->state == FIRC_IF_ALIVE && first_alive == n) { first_alive = i; }
        if (e[i]->state == FIRC_IF_UNKNOWN && first_unknown == n) { first_unknown = i; }
    }
    if (first_alive == n && first_unknown == n) {
        c.all_dead = true;
        return c;
    }
    if (e[c.active]->state == FIRC_IF_DEAD) {
        c.active = first_alive < n ? first_alive : first_unknown;
        return c;
    }
    for (size_t i = 0; i < c.active; i++) {
        if (held(e[i], now_ms)) {
            c.active = i;
            break;
        }
    }
    return c;
}

bool firc_ifchoose_first_alive(const firc_if_track_t *const *e, size_t n) {
    return n > 0 && e[0]->state == FIRC_IF_ALIVE;
}
