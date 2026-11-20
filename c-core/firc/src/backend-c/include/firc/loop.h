#ifndef FIRC_LOOP_H
#define FIRC_LOOP_H

#include <signal.h>
#include <stdint.h>

#include "firc/err.h"

typedef struct firc_loop firc_loop_t;

typedef void (*firc_fd_cb)(firc_loop_t *loop, int fd, uint32_t events, void *ud);
typedef void (*firc_timer_cb)(firc_loop_t *loop, void *ud);
typedef void (*firc_signal_cb)(firc_loop_t *loop, int signo, void *ud);
typedef void (*firc_post_cb)(firc_loop_t *loop, void *ud);

firc_err_t firc_loop_create(firc_loop_t **out);
void firc_loop_destroy(firc_loop_t *loop);

/* Level-triggered; edge-trigger is not used. */
firc_err_t firc_loop_add_fd(firc_loop_t *loop, int fd, uint32_t events, firc_fd_cb cb,
                        void *ud);
firc_err_t firc_loop_mod_fd(firc_loop_t *loop, int fd, uint32_t events);
firc_err_t firc_loop_del_fd(firc_loop_t *loop, int fd);

/* One-shot timers are retired by the loop after the callback returns; initial_ms 0 fires at once. */
firc_err_t firc_loop_add_timer(firc_loop_t *loop, uint64_t initial_ms,
                           uint64_t interval_ms, firc_timer_cb cb, void *ud,
                           int *out_id);
/* FIRC_ERR_NOENT for an unknown or already-fired one-shot id; safe to call blindly. */
firc_err_t firc_loop_del_timer(firc_loop_t *loop, int timer_id);

/* Blocks `set` in the calling thread; call before the first pthread_create so threads inherit it. */
firc_err_t firc_loop_block_signals(const sigset_t *set);

firc_err_t firc_loop_add_signals(firc_loop_t *loop, const sigset_t *set,
                             firc_signal_cb cb, void *ud);

/* Thread-safe: runs cb(ud) on the loop thread; FIRC_ERR_LIMIT when the post queue is full. */
firc_err_t firc_loop_post(firc_loop_t *loop, firc_post_cb cb, void *ud);

/* Like post; a post still queued at loop destroy gets drop(ud) instead of cb. drop may be NULL. */
typedef void (*firc_post_drop_cb)(void *ud);
firc_err_t firc_loop_post_with_drop(firc_loop_t *loop, firc_post_cb cb, void *ud,
                                firc_post_drop_cb drop);

/* Runs until firc_loop_stop(); FIRC_OK on a clean stop. */
firc_err_t firc_loop_run(firc_loop_t *loop);

/* Thread-safe and signal-safe. */
void firc_loop_stop(firc_loop_t *loop);

#endif
