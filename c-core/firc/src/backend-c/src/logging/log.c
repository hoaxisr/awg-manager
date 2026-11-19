#include "firc/log.h"

#include "firc/events.h"

#include <fcntl.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <sys/stat.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define FIRC_LOG_LINE_MAX 1024

static _Atomic int g_fd = STDOUT_FILENO;
static _Atomic int g_output_floor = FIRC_LOG_DEBUG;

void firc_log_set_fd(int fd)
{
    atomic_store_explicit(&g_fd, fd, memory_order_relaxed);
}

void firc_log_set_output_floor(firc_log_level_t floor)
{
    atomic_store_explicit(&g_output_floor, (int)floor, memory_order_relaxed);
}

void firc_log_nonblocking(void)
{
    int fd = atomic_load_explicit(&g_fd, memory_order_relaxed);
    struct stat st;
    if (fstat(fd, &st) != 0 || !(S_ISFIFO(st.st_mode) || S_ISSOCK(st.st_mode))) { return; }
    int fl = fcntl(fd, F_GETFL);
    if (fl >= 0) { (void)fcntl(fd, F_SETFL, fl | O_NONBLOCK); }
}

bool firc_log_level_parse(const char *s, firc_log_level_t *out)
{
    if (s == NULL) {
        return false;
    }
    static const struct {
        const char *name;
        firc_log_level_t level;
    } levels[] = {
        {"debug", FIRC_LOG_DEBUG},
        {"info", FIRC_LOG_INFO},
        {"warn", FIRC_LOG_WARN},
        {"error", FIRC_LOG_ERROR},
    };
    for (size_t i = 0; i < sizeof(levels) / sizeof(levels[0]); i++) {
        if (strcmp(s, levels[i].name) == 0) {
            *out = levels[i].level;
            return true;
        }
    }
    return false;
}

static const char *level_tag(firc_log_level_t level)
{
    switch (level) {
    case FIRC_LOG_DEBUG: return "DBG";
    case FIRC_LOG_INFO:  return "INF";
    case FIRC_LOG_WARN:  return "WRN";
    case FIRC_LOG_ERROR: return "ERR";
    }
    return "ERR";
}

FIRC_PRINTF(3, 0) static void vlog(firc_log_level_t level, const char *source, const char *fmt, va_list ap)
{
    char buf[FIRC_LOG_LINE_MAX];
    struct timespec ts;
    struct tm tm;
    clock_gettime(CLOCK_REALTIME, &ts);
    gmtime_r(&ts.tv_sec, &tm);

    int off = snprintf(buf, sizeof(buf),
                       "%04d-%02d-%02dT%02d:%02d:%02dZ %s ",
                       tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday,
                       tm.tm_hour, tm.tm_min, tm.tm_sec, level_tag(level));
    if (off < 0 || (size_t)off >= sizeof(buf)) {
        return;
    }
    if (source != NULL && source[0] != '\0') {
        int p = snprintf(buf + off, sizeof(buf) - (size_t)off, "%s: ", source);
        if (p < 0 || (size_t)(off + p) >= sizeof(buf)) {
            return;
        }
        off += p;
    }

    /* NOLINTNEXTLINE(clang-analyzer-valist.Uninitialized) — the caller started ap */
    int n = vsnprintf(buf + off, sizeof(buf) - (size_t)off, fmt, ap);
    if (n < 0) {
        return;
    }
    size_t len = (size_t)off + (size_t)n;
    if (len >= sizeof(buf)) {
        len = sizeof(buf) - 1;
    }
    for (size_t i = (size_t)off; i < len; i++) {
        unsigned char c = (unsigned char)buf[i];
        if (c < 0x20 || c == 0x7f) { buf[i] = '?'; }
    }
    /* Before the write, text only: the UI renders its own timestamp and level. */
    firc_event_put_log(level, (int64_t)ts.tv_sec, source, buf + off);
    if ((int)level < atomic_load_explicit(&g_output_floor, memory_order_relaxed)) {
        return;
    }

    buf[len] = '\n';
    len += 1;

    /* One write keeps concurrent lines whole; a line dropped on EAGAIN or EPIPE is acceptable. */
    ssize_t rc = write(atomic_load_explicit(&g_fd, memory_order_relaxed),
                       buf, len);
    (void)rc;
}

void firc_log(firc_log_level_t level, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    vlog(level, NULL, fmt, ap);
    va_end(ap);
}

void firc_log_from(firc_log_level_t level, const char *source, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    vlog(level, source, fmt, ap);
    va_end(ap);
}
