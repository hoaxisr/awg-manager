#ifndef SUSANIN_STATE_H
#define SUSANIN_STATE_H

#include <time.h>

typedef struct {
    char addr[64];
    time_t expire;
} state_entry;

typedef struct {
    state_entry *v;
    int n;
    int cap;
} state_set;

typedef struct {
    state_set test_tcp, ok_tcp, watch_tcp, cooldown_tcp;
    state_set test_udp, ok_udp, watch_udp, cooldown_udp;
} susanin_state;

void state_init(susanin_state *s);
void state_free(susanin_state *s);
void state_expire(state_set *st, time_t now);
int state_has(const state_set *st, const char *addr, time_t now);
int state_add(state_set *st, const char *addr, time_t now, int ttl, int refresh);
int state_remove(state_set *st, const char *addr);
time_t state_at(const state_set *st, const char *addr, time_t now);

/* Persistence: persist test/ok/cooldown entries across daemon restarts. */
int state_save(const char *path, const susanin_state *s);
int state_load(const char *path, susanin_state *s);

#define st_test(st, udp) ((udp) ? &(st)->test_udp : &(st)->test_tcp)
#define st_ok(st, udp)   ((udp) ? &(st)->ok_udp   : &(st)->ok_tcp)
#define st_watch(st, udp)((udp) ? &(st)->watch_udp: &(st)->watch_tcp)
#define st_cool(st, udp) ((udp) ? &(st)->cooldown_udp : &(st)->cooldown_tcp)

#endif
