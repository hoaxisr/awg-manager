#ifndef SUSANIN_LOG_H
#define SUSANIN_LOG_H

enum { SL_QUIET = 0, SL_ERROR = 1, SL_WARN = 2, SL_INFO = 3, SL_DEBUG = 4, SL_TRACE = 5 };

void slog_init(const char *level);
int  slog_enabled(int lvl);
void slogf(int lvl, const char *fmt, ...);

#endif
