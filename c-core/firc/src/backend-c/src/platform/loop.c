#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/loop.h"

#include <errno.h>
#include <limits.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/eventfd.h>
#include <sys/signalfd.h>
#include <sys/timerfd.h>
#include <unistd.h>

#include "firc/log.h"
#include "firc/queue.h"

#define FIRC_LOOP_MAX_EVENTS 64
#define FIRC_LOOP_POST_CAP 1024

typedef enum watch_kind {
    WATCH_FD = 0,
    WATCH_TIMER,
    WATCH_SIGNAL,
    WATCH_WAKEUP,
} watch_kind_t;

typedef struct watch {
    watch_kind_t kind;
    int fd;
    int id;
    bool oneshot;
    firc_fd_cb fd_cb;
    firc_timer_cb timer_cb;
    firc_signal_cb signal_cb;
    void *ud;
    bool dead;
    struct watch *next;
} watch_t;

typedef struct post_item {
    firc_post_cb cb;
    void *ud;
    firc_post_drop_cb drop;
} post_item_t;

struct firc_loop {
    int epfd;
    int wakeup_fd;
    _Atomic bool stopping;
    watch_t *watches;
    int next_timer_id;
    bool ids_wrapped;
    firc_queue_t *posts;
    watch_t wakeup_watch;
    int sigfd;
    /* A callback may delete a watch a later event in this batch still points at: unlink now, free after the batch. */
    bool dispatching;
    watch_t *graveyard;
};

static firc_err_t watch_register(firc_loop_t *loop, watch_t *w, uint32_t events)
{
    struct epoll_event ev;
    memset(&ev, 0, sizeof(ev));
    ev.events = events;
    ev.data.ptr = w;
    if (epoll_ctl(loop->epfd, EPOLL_CTL_ADD, w->fd, &ev) != 0) {
        return firc_err_from_errno(errno);
    }
    return FIRC_OK;
}

static void watch_link(firc_loop_t *loop, watch_t *w)
{
    w->next = loop->watches;
    loop->watches = w;
}

static void watch_unlink(firc_loop_t *loop, watch_t *w)
{
    watch_t **p = &loop->watches;
    while (*p != NULL) {
        if (*p == w) {
            *p = w->next;
            return;
        }
        p = &(*p)->next;
    }
}

static void watch_retire(firc_loop_t *loop, watch_t *w)
{
    watch_unlink(loop, w);
    if (!loop->dispatching) {
        free(w);
        return;
    }
    w->dead = true;
    w->next = loop->graveyard;
    loop->graveyard = w;
}

static void graveyard_clear(firc_loop_t *loop)
{
    while (loop->graveyard != NULL) {
        watch_t *w = loop->graveyard;
        loop->graveyard = w->next;
        free(w);
    }
}

firc_err_t firc_loop_create(firc_loop_t **out)
{
    firc_loop_t *loop = calloc(1, sizeof(*loop));
    if (loop == NULL) {
        return FIRC_ERR_NOMEM;
    }
    loop->epfd = -1;
    loop->wakeup_fd = -1;
    loop->sigfd = -1;
    loop->next_timer_id = 1;

    loop->epfd = epoll_create1(EPOLL_CLOEXEC);
    if (loop->epfd < 0) {
        goto fail_errno;
    }
    loop->wakeup_fd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
    if (loop->wakeup_fd < 0) {
        goto fail_errno;
    }
    loop->posts = firc_queue_create(FIRC_LOOP_POST_CAP, FIRC_QUEUE_REJECT);
    if (loop->posts == NULL) {
        errno = ENOMEM;
        goto fail_errno;
    }

    loop->wakeup_watch.kind = WATCH_WAKEUP;
    loop->wakeup_watch.fd = loop->wakeup_fd;
    firc_err_t err = watch_register(loop, &loop->wakeup_watch, EPOLLIN);
    if (err != FIRC_OK) {
        firc_loop_destroy(loop);
        return err;
    }

    *out = loop;
    return FIRC_OK;

fail_errno: {
        firc_err_t e = firc_err_from_errno(errno);
        firc_loop_destroy(loop);
        return e;
    }
}

void firc_loop_destroy(firc_loop_t *loop)
{
    if (loop == NULL) {
        return;
    }
    watch_t *w = loop->watches;
    while (w != NULL) {
        watch_t *next = w->next;
        if (w->kind == WATCH_TIMER || w->kind == WATCH_SIGNAL) {
            close(w->fd);
        }
        free(w);
        w = next;
    }
    graveyard_clear(loop);
    if (loop->posts != NULL) {
        /* Drain unexecuted posts through their drop so the poster's payload is released. */
        void *raw;
        while (firc_queue_try_pop(loop->posts, &raw) == FIRC_OK) {
            post_item_t *item = raw;
            if (item->drop != NULL) { item->drop(item->ud); }
            free(item);
        }
        firc_queue_destroy(loop->posts);
    }
    if (loop->wakeup_fd >= 0) {
        close(loop->wakeup_fd);
    }
    if (loop->epfd >= 0) {
        close(loop->epfd);
    }
    free(loop);
}

firc_err_t firc_loop_add_fd(firc_loop_t *loop, int fd, uint32_t events, firc_fd_cb cb,
                        void *ud)
{
    watch_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        return FIRC_ERR_NOMEM;
    }
    w->kind = WATCH_FD;
    w->fd = fd;
    w->fd_cb = cb;
    w->ud = ud;
    firc_err_t err = watch_register(loop, w, events);
    if (err != FIRC_OK) {
        free(w);
        return err;
    }
    watch_link(loop, w);
    return FIRC_OK;
}

static watch_t *find_fd_watch(firc_loop_t *loop, int fd)
{
    for (watch_t *w = loop->watches; w != NULL; w = w->next) {
        if (w->kind == WATCH_FD && w->fd == fd) {
            return w;
        }
    }
    return NULL;
}

firc_err_t firc_loop_mod_fd(firc_loop_t *loop, int fd, uint32_t events)
{
    watch_t *w = find_fd_watch(loop, fd);
    if (w == NULL) {
        return FIRC_ERR_NOENT;
    }
    struct epoll_event ev;
    memset(&ev, 0, sizeof(ev));
    ev.events = events;
    ev.data.ptr = w;
    if (epoll_ctl(loop->epfd, EPOLL_CTL_MOD, fd, &ev) != 0) {
        return firc_err_from_errno(errno);
    }
    return FIRC_OK;
}

firc_err_t firc_loop_del_fd(firc_loop_t *loop, int fd)
{
    watch_t *w = find_fd_watch(loop, fd);
    if (w == NULL) {
        return FIRC_ERR_NOENT;
    }
    /* Retire the watch whatever epoll_ctl says: a stale entry would shadow a later add on a recycled fd. */
    firc_err_t err = FIRC_OK;
    if (epoll_ctl(loop->epfd, EPOLL_CTL_DEL, fd, NULL) != 0) { err = firc_err_from_errno(errno); }
    watch_retire(loop, w);
    return err;
}

firc_err_t firc_loop_add_timer(firc_loop_t *loop, uint64_t initial_ms,
                           uint64_t interval_ms, firc_timer_cb cb, void *ud,
                           int *out_id)
{
    int tfd = timerfd_create(CLOCK_MONOTONIC, TFD_CLOEXEC | TFD_NONBLOCK);
    if (tfd < 0) {
        return firc_err_from_errno(errno);
    }
    struct itimerspec its;
    memset(&its, 0, sizeof(its));
    its.it_value.tv_sec = (time_t)(initial_ms / 1000);
    its.it_value.tv_nsec = (long)(initial_ms % 1000) * 1000000L;
    if (initial_ms == 0) {
        /* timerfd disarms on an all-zero it_value, so "now" is 1 ns. */
        its.it_value.tv_nsec = 1;
    }
    its.it_interval.tv_sec = (time_t)(interval_ms / 1000);
    its.it_interval.tv_nsec = (long)(interval_ms % 1000) * 1000000L;
    if (timerfd_settime(tfd, 0, &its, NULL) != 0) {
        firc_err_t e = firc_err_from_errno(errno);
        close(tfd);
        return e;
    }

    watch_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        close(tfd);
        return FIRC_ERR_NOMEM;
    }
    w->kind = WATCH_TIMER;
    w->fd = tfd;
    w->oneshot = interval_ms == 0;
    /* Ids start at 1 (0 means "no timer" to callers) and skip ids still in use after wrapping. */
    if (loop->next_timer_id >= INT_MAX) {
        loop->next_timer_id = 1;
        loop->ids_wrapped = true;
    }
    w->id = loop->next_timer_id++;
    while (loop->ids_wrapped) {
        bool taken = false;
        for (watch_t *o = loop->watches; o != NULL; o = o->next) {
            if (o->kind == WATCH_TIMER && o->id == w->id) {
                taken = true;
                break;
            }
        }
        if (!taken) { break; }
        if (loop->next_timer_id >= INT_MAX) { loop->next_timer_id = 1; }
        w->id = loop->next_timer_id++;
    }
    w->timer_cb = cb;
    w->ud = ud;
    firc_err_t err = watch_register(loop, w, EPOLLIN);
    if (err != FIRC_OK) {
        close(tfd);
        free(w);
        return err;
    }
    watch_link(loop, w);
    if (out_id != NULL) {
        *out_id = w->id;
    }
    return FIRC_OK;
}

firc_err_t firc_loop_del_timer(firc_loop_t *loop, int timer_id)
{
    for (watch_t *w = loop->watches; w != NULL; w = w->next) {
        if (w->kind == WATCH_TIMER && w->id == timer_id) {
            (void)epoll_ctl(loop->epfd, EPOLL_CTL_DEL, w->fd, NULL);
            close(w->fd);
            watch_retire(loop, w);
            return FIRC_OK;
        }
    }
    return FIRC_ERR_NOENT;
}

firc_err_t firc_loop_block_signals(const sigset_t *set)
{
    /* pthread_sigmask returns the error and leaves errno alone. */
    int e = pthread_sigmask(SIG_BLOCK, set, NULL);
    if (e != 0) { return firc_err_from_errno(e); }
    return FIRC_OK;
}

firc_err_t firc_loop_add_signals(firc_loop_t *loop, const sigset_t *set,
                             firc_signal_cb cb, void *ud)
{
    if (loop->sigfd >= 0) {
        return FIRC_ERR_EXIST;
    }
    /* Blocking here is too late alone: existing threads keep the signal unblocked (see block_signals). */
    firc_err_t berr = firc_loop_block_signals(set);
    if (berr != FIRC_OK) { return berr; }
    int sfd = signalfd(-1, set, SFD_CLOEXEC | SFD_NONBLOCK);
    if (sfd < 0) {
        return firc_err_from_errno(errno);
    }
    watch_t *w = calloc(1, sizeof(*w));
    if (w == NULL) {
        close(sfd);
        return FIRC_ERR_NOMEM;
    }
    w->kind = WATCH_SIGNAL;
    w->fd = sfd;
    w->signal_cb = cb;
    w->ud = ud;
    firc_err_t err = watch_register(loop, w, EPOLLIN);
    if (err != FIRC_OK) {
        close(sfd);
        free(w);
        return err;
    }
    watch_link(loop, w);
    loop->sigfd = sfd;
    return FIRC_OK;
}

static void wakeup(firc_loop_t *loop)
{
    uint64_t one = 1;
    ssize_t rc = write(loop->wakeup_fd, &one, sizeof(one));
    (void)rc; /* EAGAIN: a wakeup is already pending */
}

firc_err_t firc_loop_post_with_drop(firc_loop_t *loop, firc_post_cb cb, void *ud,
                                firc_post_drop_cb drop)
{
    post_item_t *item = malloc(sizeof(*item));
    if (item == NULL) {
        return FIRC_ERR_NOMEM;
    }
    item->cb = cb;
    item->ud = ud;
    item->drop = drop;
    /* A rejected push is not a drop: the caller still owns ud. */
    firc_err_t err = firc_queue_push(loop->posts, item, NULL);
    if (err != FIRC_OK) {
        free(item);
        return err;
    }
    wakeup(loop);
    return FIRC_OK;
}

firc_err_t firc_loop_post(firc_loop_t *loop, firc_post_cb cb, void *ud)
{
    return firc_loop_post_with_drop(loop, cb, ud, NULL);
}

void firc_loop_stop(firc_loop_t *loop)
{
    atomic_store(&loop->stopping, true);
    wakeup(loop);
}

static void handle_wakeup(firc_loop_t *loop)
{
    uint64_t counter;
    while (read(loop->wakeup_fd, &counter, sizeof(counter)) > 0) {
    }
    void *raw;
    while (firc_queue_try_pop(loop->posts, &raw) == FIRC_OK) {
        post_item_t *item = raw;
        item->cb(loop, item->ud);
        free(item);
    }
}

static void handle_timer(firc_loop_t *loop, watch_t *w)
{
    uint64_t expirations;
    if (read(w->fd, &expirations, sizeof(expirations)) !=
        (ssize_t)sizeof(expirations)) {
        return;
    }
    /* Read before the callback: it may delete this timer. */
    bool oneshot = w->oneshot;
    int id = w->id;
    w->timer_cb(loop, w->ud);
    /* Retire a fired one-shot: nothing disarms a timerfd by firing, so its fd would leak. */
    if (oneshot) { (void)firc_loop_del_timer(loop, id); }
}

static void handle_signal(firc_loop_t *loop, watch_t *w)
{
    struct signalfd_siginfo si;
    while (read(w->fd, &si, sizeof(si)) == (ssize_t)sizeof(si)) {
        w->signal_cb(loop, (int)si.ssi_signo, w->ud);
    }
}

firc_err_t firc_loop_run(firc_loop_t *loop)
{
    struct epoll_event events[FIRC_LOOP_MAX_EVENTS];

    while (!atomic_load(&loop->stopping)) {
        int n = epoll_wait(loop->epfd, events, FIRC_LOOP_MAX_EVENTS, -1);
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            return firc_err_from_errno(errno);
        }
        loop->dispatching = true;
        for (int i = 0; i < n; i++) {
            if (atomic_load(&loop->stopping)) {
                break;
            }
            watch_t *w = events[i].data.ptr;
            if (w->dead) { continue; } /* deleted by an earlier callback in this batch */
            switch (w->kind) {
            case WATCH_WAKEUP:
                handle_wakeup(loop);
                break;
            case WATCH_TIMER:
                handle_timer(loop, w);
                break;
            case WATCH_SIGNAL:
                handle_signal(loop, w);
                break;
            case WATCH_FD:
                w->fd_cb(loop, w->fd, events[i].events, w->ud);
                break;
            }
        }
        loop->dispatching = false;
        graveyard_clear(loop);
    }
    atomic_store(&loop->stopping, false);
    return FIRC_OK;
}
