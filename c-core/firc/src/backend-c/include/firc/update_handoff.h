#ifndef FIRC_UPDATE_HANDOFF_H
#define FIRC_UPDATE_HANDOFF_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>

#include "firc/err.h"

typedef enum {
    FIRC_UPDATE_MARKER_NONE,
    FIRC_UPDATE_MARKER_NO_PID,
    FIRC_UPDATE_MARKER_ALIVE,
    FIRC_UPDATE_MARKER_STALE
} firc_update_marker_t;

/* Reads <dir>/installing: line 1 "<tag> <epoch>", line 2 "<pid> <starttime>". NO_PID while line 2 is absent or
 * unfinished, ALIVE when its pid has that /proc start time, STALE otherwise. tag/epoch out may be NULL. */
firc_update_marker_t firc_update_marker_read(const char *dir, char *tag, size_t cap, int64_t *epoch);

#define FIRC_UPDATE_LOG_MAX 4096

typedef struct firc_update_result {
    char version[64];
    int opkg_exit;
    int64_t finished_at;
} firc_update_result_t;

/* Reads <dir>/result, exactly "<tag> <int> <epoch>\n"; false when absent or malformed. */
bool firc_update_result_read(const char *dir, firc_update_result_t *out);

/* Reads the tail of <dir>/opkg.log that fits cap, from a line start; "" and 0 when absent. */
size_t firc_update_log_read(const char *dir, char *buf, size_t cap);

/* /proc/<pid>/stat field 22 (after the last ")"). */
bool firc_proc_starttime(pid_t pid, uint64_t *out);

/* Removes <dir>/result, writes <dir>/installing ("<tag> <epoch>\n", atomic), spawns `sh` with the fixed text and
 * argv {"firc-update", dir, opkg, ipk, tag}; on a spawn failure removes the marker and returns the error. */
firc_err_t firc_update_handoff(const char *dir, const char *sh, const char *opkg, const char *ipk, const char *tag,
                               int64_t epoch);

#endif
