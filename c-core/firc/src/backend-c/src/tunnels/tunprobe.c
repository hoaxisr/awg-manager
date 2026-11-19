#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/tunprobe.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include "firc/tunproto.h"

extern char **environ;

struct firc_tunprobe {
    firc_loop_t *loop;
    pid_t pid;
    int in_fd, out_fd;
    char *in_buf;
    size_t in_len, in_off;
    int timer;
    firc_tun_lines_t lines;
    firc_tunprobe_row_t *rows;
    size_t n;
    firc_tunprobe_done_fn done;
    void *ud;
};

int64_t firc_tunprobe_deadline_ms(size_t n)
{
    return ((int64_t)n * 6 + 5) * 1000;
}

static void close_in(firc_tunprobe_t *p)
{
    if (p->in_fd >= 0) {
        firc_loop_del_fd(p->loop, p->in_fd);
        close(p->in_fd);
        p->in_fd = -1;
    }
    free(p->in_buf);
    p->in_buf = NULL;
}

static void release(firc_tunprobe_t *p)
{
    close_in(p);
    if (p->out_fd >= 0) {
        firc_loop_del_fd(p->loop, p->out_fd);
        close(p->out_fd);
        p->out_fd = -1;
    }
    if (p->timer != 0) {
        firc_loop_del_timer(p->loop, p->timer);
        p->timer = 0;
    }
    if (p->pid > 0) {
        kill(p->pid, SIGKILL);
        while (waitpid(p->pid, NULL, 0) < 0 && errno == EINTR) {
        }
        p->pid = 0;
    }
}

static void destroy(firc_tunprobe_t *p)
{
    release(p);
    free(p->rows);
    free(p);
}

static void finish(firc_tunprobe_t *p)
{
    release(p);
    p->done(p->rows, p->n, p->ud);
    free(p->rows);
    free(p);
}

static bool parse_ms(const char *s, const char *label, int *out)
{
    const char *at = strstr(s, label);
    if (at == NULL) {
        return false;
    }
    char *end = NULL;
    long v = strtol(at + strlen(label), &end, 10);
    if (end == at + strlen(label) || v < 0 || v > 3600000) {
        return false;
    }
    *out = (int)v;
    return true;
}

static void on_line(const char *line, size_t len, void *ud)
{
    firc_tunprobe_t *p = ud;
    char buf[FIRC_TUN_LINE_MAX + 1];
    memcpy(buf, line, len);
    buf[len] = 0;
    char *f[4] = {buf, NULL, NULL, NULL};
    for (int k = 1; k < 4; k++) {
        char *tab = strchr(f[k - 1], '\t');
        if (tab == NULL) {
            return;
        }
        *tab = 0;
        f[k] = tab + 1;
    }
    char *end = NULL;
    unsigned long i = strtoul(f[0], &end, 10);
    if (end == f[0] || *end != 0 || i >= p->n) {
        return;
    }
    firc_tunprobe_row_t *r = &p->rows[i];
    memset(r, 0, sizeof *r);
    r->have = true;
    r->handshake_ms = -1;
    r->first_byte_ms = -1;
    if (strcmp(f[2], "ok") == 0) {
        r->ok = parse_ms(f[3], "handshake ", &r->handshake_ms) && parse_ms(f[3], "first byte ", &r->first_byte_ms);
        if (!r->ok) {
            snprintf(r->why, sizeof r->why, "unreadable answer");
        }
        return;
    }
    size_t o = 0;
    for (const char *s = f[3]; *s != 0 && o + 1 < sizeof r->why; s++) {
        unsigned char ch = (unsigned char)*s;
        r->why[o++] = ch < 0x20 || ch == 0x7f ? ' ' : (char)ch;
    }
    r->why[o] = 0;
}

static void out_cb(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)events;
    firc_tunprobe_t *p = ud;
    char buf[4096];
    for (;;) {
        ssize_t n = read(fd, buf, sizeof buf);
        if (n > 0) {
            (void)firc_tun_lines_feed(&p->lines, buf, (size_t)n, on_line, p);
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && errno == EAGAIN) {
            return;
        }
        finish(p);
        return;
    }
}

static void in_cb(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)events;
    firc_tunprobe_t *p = ud;
    while (p->in_off < p->in_len) {
        ssize_t n = write(fd, p->in_buf + p->in_off, p->in_len - p->in_off);
        if (n > 0) {
            p->in_off += (size_t)n;
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && errno == EAGAIN) {
            return;
        }
        break;
    }
    close_in(p);
}

static void deadline_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    firc_tunprobe_t *p = ud;
    p->timer = 0;
    finish(p);
}

static char *join(char *const *links, size_t n, size_t *len)
{
    size_t total = 1;
    for (size_t i = 0; i < n; i++) {
        total += strlen(links[i]) + 1;
    }
    char *buf = malloc(total);
    if (buf == NULL) {
        return NULL;
    }
    size_t off = 0;
    for (size_t i = 0; i < n; i++) {
        size_t l = strlen(links[i]);
        memcpy(buf + off, links[i], l);
        off += l;
        buf[off++] = '\n';
    }
    buf[off++] = '\n';
    *len = off;
    return buf;
}

static int set_nonblock(int fd)
{
    int fl = fcntl(fd, F_GETFL, 0);
    return fl < 0 ? -1 : fcntl(fd, F_SETFL, fl | O_NONBLOCK);
}

static int spawn_child(const firc_tunprobe_opts_t *o, pid_t *pid, int *in_fd, int *out_fd)
{
    char timeout[16];
    char mark[16];
    snprintf(timeout, sizeof timeout, "%d", o->timeout_s);
    snprintf(mark, sizeof mark, "0x%x", (unsigned)o->mark);
    const char *argv[16];
    size_t a = 0;
    argv[a++] = o->binary;
    argv[a++] = "--probe";
    argv[a++] = "-t";
    argv[a++] = timeout;
    if (o->mark != 0) {
        argv[a++] = "-m";
        argv[a++] = mark;
    }
    if (o->insecure) {
        argv[a++] = "--insecure";
    }
    if (o->ca != NULL && o->ca[0] != 0) {
        argv[a++] = "--ca";
        argv[a++] = o->ca;
    }
    if (o->fingerprint != NULL && o->fingerprint[0] != 0) {
        argv[a++] = "--fp";
        argv[a++] = o->fingerprint;
    }
    argv[a++] = "-";
    argv[a] = NULL;

    int in[2] = {-1, -1};
    int out[2] = {-1, -1};
    int null_fd = open("/dev/null", O_WRONLY | O_CLOEXEC);
    int rc = 0;
    if (null_fd < 0 || pipe2(in, O_CLOEXEC) != 0 || pipe2(out, O_CLOEXEC) != 0) {
        rc = errno;
    }
    if (rc == 0 && (set_nonblock(in[1]) != 0 || set_nonblock(out[0]) != 0)) {
        rc = errno;
    }
    posix_spawn_file_actions_t fa;
    posix_spawnattr_t attr;
    bool fa_ok = false;
    bool attr_ok = false;
    if (rc == 0) {
        rc = posix_spawn_file_actions_init(&fa);
        fa_ok = rc == 0;
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, in[0], STDIN_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, out[1], STDOUT_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, null_fd, STDERR_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawnattr_init(&attr);
        attr_ok = rc == 0;
    }
    sigset_t all;
    sigset_t none;
    sigfillset(&all);
    sigemptyset(&none);
    if (rc == 0) {
        rc = posix_spawnattr_setsigdefault(&attr, &all);
    }
    if (rc == 0) {
        rc = posix_spawnattr_setsigmask(&attr, &none);
    }
    if (rc == 0) {
        rc = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETSIGDEF | POSIX_SPAWN_SETSIGMASK);
    }
    if (rc == 0) {
        rc = posix_spawnp(pid, o->binary, &fa, &attr, (char *const *)argv, environ);
    }
    if (attr_ok) {
        posix_spawnattr_destroy(&attr);
    }
    if (fa_ok) {
        posix_spawn_file_actions_destroy(&fa);
    }
    int ends[] = {in[0], out[1], null_fd};
    for (size_t i = 0; i < sizeof ends / sizeof ends[0]; i++) {
        if (ends[i] >= 0) {
            close(ends[i]);
        }
    }
    if (rc != 0) {
        if (in[1] >= 0) {
            close(in[1]);
        }
        if (out[0] >= 0) {
            close(out[0]);
        }
        return rc;
    }
    *in_fd = in[1];
    *out_fd = out[0];
    return 0;
}

firc_err_t firc_tunprobe_start(firc_loop_t *loop, const firc_tunprobe_opts_t *o, char *const *links, size_t n,
                               firc_tunprobe_done_fn done, void *ud, firc_tunprobe_t **out)
{
    *out = NULL;
    firc_tunprobe_t *p = calloc(1, sizeof *p);
    if (p == NULL) {
        return FIRC_ERR_NOMEM;
    }
    p->loop = loop;
    p->in_fd = -1;
    p->out_fd = -1;
    p->n = n;
    p->done = done;
    p->ud = ud;
    p->rows = calloc(n + 1, sizeof *p->rows);
    p->in_buf = join(links, n, &p->in_len);
    if (p->rows == NULL || p->in_buf == NULL) {
        destroy(p);
        return FIRC_ERR_NOMEM;
    }
    int rc = spawn_child(o, &p->pid, &p->in_fd, &p->out_fd);
    if (rc != 0) {
        p->pid = 0;
        destroy(p);
        return firc_err_from_errno(rc);
    }
    if (firc_loop_add_fd(loop, p->out_fd, EPOLLIN, out_cb, p) != FIRC_OK ||
        firc_loop_add_fd(loop, p->in_fd, EPOLLOUT, in_cb, p) != FIRC_OK ||
        firc_loop_add_timer(loop, (uint64_t)o->kill_after_ms, 0, deadline_cb, p, &p->timer) != FIRC_OK) {
        p->timer = 0;
        destroy(p);
        return FIRC_ERR_SYS;
    }
    *out = p;
    return FIRC_OK;
}

void firc_tunprobe_cancel(firc_tunprobe_t *p)
{
    if (p != NULL) {
        destroy(p);
    }
}
