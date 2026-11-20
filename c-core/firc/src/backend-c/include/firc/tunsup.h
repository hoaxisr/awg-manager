#ifndef FIRC_TUNSUP_H
#define FIRC_TUNSUP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>

#include "firc/err.h"
#include "firc/loop.h"
#include "firc/tunnels.h"
#include "firc/tunproto.h"

typedef enum {
    FIRC_TUN_ST_OFF,
    FIRC_TUN_ST_STARTING,
    FIRC_TUN_ST_UP,
    FIRC_TUN_ST_NO_NODE,
    FIRC_TUN_ST_BACKOFF,
    FIRC_TUN_ST_BAD_CONFIG,
    FIRC_TUN_ST_WAITING,
    FIRC_TUN_ST_UPLINK_DOWN,
    FIRC_TUN_ST_ROUTER
} firc_tun_status_t;

typedef struct {
    uint16_t port;
    char state[16];
    char why[96];
    uint64_t rx, tx;
} firc_tun_ib_state_t;

typedef struct {
    size_t n_active;
    int64_t since_ms;
    firc_tun_status_t status;
    int backoff_s;
    int last_exit;
    bool uplink_ok;
    uint16_t tcp_port;
    char id[16];
    char device[16];
    char why[160];
    char active[8][128];
    firc_tun_ib_state_t ib[FIRC_TUN_MAX_INBOUNDS];
    size_t n_ib;
} firc_tun_state_t;

typedef struct firc_tunsup firc_tunsup_t;

/* ev is the child's event, or NULL when the supervisor itself changed the status (start, exit, stop). */
typedef void (*firc_tun_event_fn)(const firc_tun_state_t *st, const firc_tev_t *ev, void *ud);

/* Loop thread only, like every call below; children are spawned from this thread. */
firc_tunsup_t *firc_tunsup_new(firc_loop_t *loop, const char *binary, firc_tun_event_fn on_event, void *ud);

/* Starts, restarts and stops children to match want; marks[i] is the socket mark of want->t[i], 0 for none. */
firc_err_t firc_tunsup_apply(firc_tunsup_t *s, const firc_tunnels_t *want, const uint32_t *marks);

/* Copies up to cap states of the configured tunnels; returns how many there are. */
size_t firc_tunsup_states(const firc_tunsup_t *s, firc_tun_state_t *out, size_t cap);

/* SIGTERM to every child, SIGKILL after grace_ms; returns with every child reaped, each tunnel reported OFF. */
void firc_tunsup_stop(firc_tunsup_t *s, int grace_ms);

void firc_tunsup_free(firc_tunsup_t *s);

typedef void (*firc_tun_reap_fn)(const char *id, const char *device, pid_t pid, void *ud);

/* fn runs once child pid of tunnel id, which ran on device, has been reaped, removed tunnels included, before any
   respawn. */
void firc_tunsup_set_on_reap(firc_tunsup_t *s, firc_tun_reap_fn fn, void *ud);

typedef enum { FIRC_TUN_GATE_GO, FIRC_TUN_GATE_WAIT } firc_tun_gate_t;

/* Asked before every spawn of tunnel id on device (index n); WAIT holds the spawn until firc_tunsup_gate_open(gen). */
typedef firc_tun_gate_t (*firc_tun_gate_fn)(const char *id, const char *device, int n, uint32_t gen, void *ud);

/* Every spawn asks fn first; gens come from one counter across the supervisor, so no two waits share one. */
void firc_tunsup_set_gate(firc_tunsup_t *s, firc_tun_gate_fn fn, void *ud);

/* Spawns tunnel id held by the gate under gen, past the gate this once; nothing when the slot changed since. */
void firc_tunsup_gate_open(firc_tunsup_t *s, const char *id, uint32_t gen);

/* True while a child that runs on device is not reaped, retired tunnels included. */
bool firc_tunsup_holds_device(const firc_tunsup_t *s, const char *device);

/* Sets the pool base and prefix length passed as --tcp-pool to kernel-TCP tunnels; len 0 passes none. */
void firc_tunsup_set_tcp_pool(firc_tunsup_t *s, const uint8_t base[4], uint8_t len);

typedef enum { FIRC_TUN_CMD_MAP, FIRC_TUN_CMD_CLOSE, FIRC_TUN_CMD_INBOUNDS, FIRC_TUN_CMD_WAKE } firc_tun_cmd_t;

/* Queues text for tunnel id's ready child, written at once when nothing is pending; expect holds the ack
   count of each MAP command (checked), ignored for CLOSE. INBOUNDS and WAKE take the lines only: the verb
   and the closing empty line are added. FIRC_ERR_STATE when no ready child holds stdin. */
firc_err_t firc_tunsup_command(firc_tunsup_t *s, const char *id, firc_tun_cmd_t kind, const char *text, size_t len,
                               const size_t *expect, size_t n_expect);

/* True while tunnel id has a child that reported ready, is not terminating and holds stdin. */
bool firc_tunsup_ready(const firc_tunsup_t *s, const char *id);

/* Sets the uplink_ok the states of tunnel id report. */
void firc_tunsup_set_uplink_ok(firc_tunsup_t *s, const char *id, bool ok);

/* True while a child of tunnel id started with mark is not reaped, retired tunnels included. */
bool firc_tunsup_holds(const firc_tunsup_t *s, const char *id, uint32_t mark);

/* Restarts the child of tunnel id even when nothing changed; FIRC_ERR_NOENT unknown, FIRC_ERR_STATE disabled. */
firc_err_t firc_tunsup_restart(firc_tunsup_t *s, const char *id);

/* Restarts every live child that reported no_node in this run and has more than 5 s of its pause left, 30 s after
   its last such restart at the soonest, doubling to 300 s, from 30 s again once a node was held 30 s; a child held
   back by that floor alone is restarted once when it ends, unless it got a node or restarted meanwhile; returns how
   many it restarted. */
size_t firc_tunsup_restart_starved(firc_tunsup_t *s);

/* The network changed: runs firc_tunsup_restart_starved 2 s after the last of a burst of calls. */
void firc_tunsup_net_changed(firc_tunsup_t *s);

/* A counter raised each time a child is (re)started or sent new nodes; started() and updated() are its
   value at id's last start and last node update, 0 never. */
uint64_t firc_tunsup_start_seq(const firc_tunsup_t *s);
uint64_t firc_tunsup_started(const firc_tunsup_t *s, const char *id);
uint64_t firc_tunsup_updated(const firc_tunsup_t *s, const char *id);

void firc_tunsup_set_second_ms_for_test(firc_tunsup_t *s, int ms); /* test seam: length of a backoff second */

#endif
