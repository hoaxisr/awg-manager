#ifndef FIRC_LOG_H
#define FIRC_LOG_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef enum firc_log_level {
    FIRC_LOG_DEBUG = 0,
    FIRC_LOG_INFO,
    FIRC_LOG_WARN,
    FIRC_LOG_ERROR,
} firc_log_level_t;

/* True and sets *out only if s is a level name. */
bool firc_log_level_parse(const char *s, firc_log_level_t *out);

/* Redirects output (default fd 1). */
void firc_log_set_fd(int fd);
/* Lines below floor still reach the journal but are not written to the output. */
void firc_log_set_output_floor(firc_log_level_t floor);
/* Makes a pipe or socket fd non-blocking: a stalled reader drops lines instead of stalling the loop. */
void firc_log_nonblocking(void);

#if defined(__GNUC__) || defined(__clang__)
#define FIRC_PRINTF(fmt_idx, arg_idx) \
    __attribute__((format(printf, fmt_idx, arg_idx)))
#else
#define FIRC_PRINTF(fmt_idx, arg_idx)
#endif

void firc_log(firc_log_level_t level, const char *fmt, ...) FIRC_PRINTF(2, 3);
/* A line said for `source` (a tunnel device): the journal keeps it apart, the output prefixes "<source>: ". */
void firc_log_from(firc_log_level_t level, const char *source, const char *fmt, ...) FIRC_PRINTF(3, 4);

#define FIRC_DEBUG(...) firc_log(FIRC_LOG_DEBUG, __VA_ARGS__)
#define FIRC_INFO(...)  firc_log(FIRC_LOG_INFO, __VA_ARGS__)
#define FIRC_WARN(...)  firc_log(FIRC_LOG_WARN, __VA_ARGS__)
#define FIRC_ERROR(...) firc_log(FIRC_LOG_ERROR, __VA_ARGS__)

#endif
