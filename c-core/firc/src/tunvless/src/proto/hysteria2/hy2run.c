#define _GNU_SOURCE
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/epoll.h>
#include <sys/timerfd.h>

#include "hy2run.h"

static uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec;
}

static void arm(int tfd, uint64_t *armed, uint64_t d) {
    if (d == *armed) return;
    struct itimerspec it;
    memset(&it, 0, sizeof it);
    if (d != UINT64_MAX) {
        it.it_value.tv_sec = (time_t)(d / 1000000000u);
        it.it_value.tv_nsec = (long)(d % 1000000000u);
    }
    timerfd_settime(tfd, d != UINT64_MAX ? TFD_TIMER_ABSTIME : 0, &it, NULL);
    *armed = d;
}

int hy2_run(struct hy2c *c, uint64_t until_ns, hy2_step_fn step, void *u, struct hy2_run_stats *st) {
    if (!c) return HY2_RUN_CLOSED;
    int ep = epoll_create1(EPOLL_CLOEXEC);
    int tfd = timerfd_create(CLOCK_MONOTONIC, TFD_NONBLOCK | TFD_CLOEXEC);
    int rc = QC_ESOCK, closed = hy2c_closed(c);
    struct epoll_event sev = { .events = EPOLLIN, .data.u32 = 0 }, tev = { .events = EPOLLIN, .data.u32 = 1 };
    if (ep < 0 || tfd < 0 || epoll_ctl(ep, EPOLL_CTL_ADD, tfd, &tev) != 0) goto out;
    if (!closed && epoll_ctl(ep, EPOLL_CTL_ADD, hy2c_fd(c), &sev) != 0) goto out;
    uint64_t armed = UINT64_MAX;
    for (;;) {
        uint64_t now = now_ns(), d = closed ? UINT64_MAX : hy2c_deadline_ns(c);
        int tmo = -1;
        if (closed || d <= now || until_ns <= now) {
            tmo = 0;
        } else {
            if (st && d != UINT64_MAX) {
                st->sleeps++;
                if (d - now < 1000000u) st->short_sleeps++;
            }
            arm(tfd, &armed, d < until_ns ? d : until_ns);
        }
        struct epoll_event ev[2];
        int n = epoll_wait(ep, ev, 2, tmo);
        for (int i = 0; i < n; i++) {
            if (ev[i].data.u32 == 1) {
                uint64_t x;
                if (read(tfd, &x, sizeof x) < 0) {}
                armed = 0;
            } else if (!closed) {
                (void)hy2c_on_readable(c);
            }
        }
        if (!closed && hy2c_deadline_ns(c) <= now_ns()) (void)hy2c_on_timer(c);
        if (!closed && hy2c_closed(c)) {
            closed = 1;
            epoll_ctl(ep, EPOLL_CTL_DEL, hy2c_fd(c), NULL);
        }
        int r = step(u);
        hy2c_flush(c);
        if (r) { rc = r; break; }
        if (closed) { rc = HY2_RUN_CLOSED; break; }
        if (now_ns() >= until_ns) { rc = HY2_RUN_TIMEOUT; break; }
    }
out:
    if (tfd >= 0) close(tfd);
    if (ep >= 0) close(ep);
    return rc;
}
