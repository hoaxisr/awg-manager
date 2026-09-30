#define _GNU_SOURCE
#include "log.h"

#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

static int g_level = SL_INFO;

void slog_init(const char *level)
{
    if (!level)
        return;
    if (!strcmp(level, "quiet")) g_level = SL_QUIET;
    else if (!strcmp(level, "error")) g_level = SL_ERROR;
    else if (!strcmp(level, "info")) g_level = SL_INFO;
    else if (!strcmp(level, "debug")) g_level = SL_DEBUG;
    else if (!strcmp(level, "trace")) g_level = SL_TRACE;
    else g_level = SL_INFO;
}

int slog_enabled(int lvl)
{
    return lvl <= g_level;
}

void slogf(int lvl, const char *fmt, ...)
{
    va_list ap;
    const char *tag;
    char ts[16];
    time_t now;
    struct tm tmv;
    if (!slog_enabled(lvl))
        return;
    if (lvl <= SL_ERROR) tag = "ERR";
    else if (lvl <= SL_WARN) tag = "WARN";
    else if (lvl <= SL_INFO) tag = "INFO";
    else tag = "DBG";
    now = time(NULL);
    localtime_r(&now, &tmv);
    strftime(ts, sizeof(ts), "%H:%M:%S", &tmv);
    fprintf(stdout, "%s %s: ", ts, tag);
    va_start(ap, fmt);
    vfprintf(stdout, fmt, ap);
    va_end(ap);
    fputc('\n', stdout);
    fflush(stdout);
}
