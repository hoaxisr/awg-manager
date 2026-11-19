#include "firc/update_handoff.h"

#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include "firc/atomic_write.h"
#include "firc/spawn.h"

static const char handoff_sh[] =
    "cd \"$1\" || exit 1\n"
    ": > opkg.log\n"
    "printf '%s %s\\n' \"$$\" \"$(sed -e 's/.*) //' /proc/$$/stat | cut -d' ' -f20)\" >> installing\n"
    "rc=$({ { \"$2\" install \"$3\" 2>&1 3>&-; echo $? >&3; } |\n"
    "  { while IFS= read -r l || [ -n \"$l\" ]; do printf '%s\\n' \"$l\" >> opkg.log; printf '%s\\n' \"$l\"; done 3>&-; } |\n"
    "  { logger -t firc-update || cat; } > /dev/null; } 3>&1)\n"
    "printf '%s %s %s\\n' \"$4\" \"${rc:-255}\" \"$(date +%s)\" > result.part && mv -f result.part result\n"
    "rm -f -- \"$3\" installing\n";

static size_t read_small(const char *path, char *buf, size_t cap, int *err)
{
    *err = 0;
    buf[0] = '\0';
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        *err = errno;
        return 0;
    }
    size_t n = 0;
    while (n + 1 < cap) {
        ssize_t r = read(fd, buf + n, cap - 1 - n);
        if (r < 0 && errno == EINTR) { continue; }
        if (r < 0) {
            *err = errno;
            break;
        }
        if (r == 0) { break; }
        n += (size_t)r;
    }
    close(fd);
    buf[n] = '\0';
    return n;
}

static const char *word(const char *p, char *out, size_t cap)
{
    size_t n = 0;
    while (p[n] > ' ' && p[n] != 0x7f) { n++; }
    if (n == 0 || n >= cap) { return NULL; }
    memcpy(out, p, n);
    out[n] = '\0';
    return p + n;
}

static const char *number(const char *p, uint64_t max, uint64_t *out)
{
    uint64_t v = 0;
    size_t n = 0;
    while (p[n] >= '0' && p[n] <= '9') {
        uint64_t d = (uint64_t)(p[n] - '0');
        if (v > (max - d) / 10) { return NULL; }
        v = v * 10 + d;
        n++;
    }
    if (n == 0) { return NULL; }
    *out = v;
    return p + n;
}

static const char *expect(const char *p, char c)
{
    return p != NULL && *p == c ? p + 1 : NULL;
}

bool firc_proc_starttime(pid_t pid, uint64_t *out)
{
    if (pid <= 0 || out == NULL) { return false; }
    char path[64], buf[1024];
    snprintf(path, sizeof(path), "/proc/%ld/stat", (long)pid);
    int err = 0;
    size_t n = read_small(path, buf, sizeof(buf), &err);
    if (err != 0 || n == 0) { return false; }
    const char *p = strrchr(buf, ')');
    if (p == NULL) { return false; }
    for (int i = 0; i < 20; i++) {
        p = strchr(p, ' ');
        if (p == NULL) { return false; }
        p++;
    }
    return number(p, UINT64_MAX, out) != NULL;
}

firc_update_marker_t firc_update_marker_read(const char *dir, char *tag, size_t cap, int64_t *epoch)
{
    if (tag != NULL && cap > 0) { tag[0] = '\0'; }
    if (epoch != NULL) { *epoch = 0; }
    char path[PATH_MAX], buf[256];
    if (dir == NULL || snprintf(path, sizeof(path), "%s/installing", dir) >= (int)sizeof(path)) {
        return FIRC_UPDATE_MARKER_STALE;
    }
    int err = 0;
    read_small(path, buf, sizeof(buf), &err);
    if (err == ENOENT) { return FIRC_UPDATE_MARKER_NONE; }
    if (err != 0) { return FIRC_UPDATE_MARKER_STALE; }

    char t[64];
    uint64_t e = 0;
    const char *p = expect(word(buf, t, sizeof(t)), ' ');
    p = p != NULL ? expect(number(p, INT64_MAX, &e), '\n') : NULL;
    if (p == NULL) { return FIRC_UPDATE_MARKER_STALE; }
    if (tag != NULL && cap > 0) { snprintf(tag, cap, "%s", t); }
    if (epoch != NULL) { *epoch = (int64_t)e; }

    if (strchr(p, '\n') == NULL) { return FIRC_UPDATE_MARKER_NO_PID; }
    uint64_t pid = 0, start = 0, now = 0;
    p = expect(number(p, INT_MAX, &pid), ' ');
    p = p != NULL ? expect(number(p, UINT64_MAX, &start), '\n') : NULL;
    if (p == NULL) { return FIRC_UPDATE_MARKER_STALE; }
    if (!firc_proc_starttime((pid_t)pid, &now) || now != start) { return FIRC_UPDATE_MARKER_STALE; }
    return FIRC_UPDATE_MARKER_ALIVE;
}

bool firc_update_result_read(const char *dir, firc_update_result_t *out)
{
    if (dir == NULL || out == NULL) { return false; }
    char path[PATH_MAX], buf[160];
    if (snprintf(path, sizeof(path), "%s/result", dir) >= (int)sizeof(path)) { return false; }
    int err = 0;
    read_small(path, buf, sizeof(buf), &err);
    if (err != 0) { return false; }
    firc_update_result_t r;
    uint64_t code = 0, at = 0;
    const char *p = expect(word(buf, r.version, sizeof(r.version)), ' ');
    p = p != NULL ? expect(number(p, 255, &code), ' ') : NULL;
    p = p != NULL ? expect(number(p, INT64_MAX, &at), '\n') : NULL;
    if (p == NULL || *p != '\0') { return false; }
    r.opkg_exit = (int)code;
    r.finished_at = (int64_t)at;
    *out = r;
    return true;
}

size_t firc_update_log_read(const char *dir, char *buf, size_t cap)
{
    buf[0] = '\0';
    char path[PATH_MAX];
    if (snprintf(path, sizeof(path), "%s/opkg.log", dir) >= (int)sizeof(path)) { return 0; }
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) { return 0; }
    off_t size = lseek(fd, 0, SEEK_END);
    off_t from = size > (off_t)(cap - 1) ? size - (off_t)(cap - 1) : 0;
    size_t n = 0;
    char before = '\n';
    if (from > 0 && pread(fd, &before, 1, from - 1) != 1) { before = '\0'; }
    if (size > 0 && lseek(fd, from, SEEK_SET) == from) {
        while (n + 1 < cap) {
            ssize_t r = read(fd, buf + n, cap - 1 - n);
            if (r < 0 && errno == EINTR) { continue; }
            if (r <= 0) { break; }
            n += (size_t)r;
        }
    }
    close(fd);
    buf[n] = '\0';
    if (before == '\n') { return n; }
    char *nl = memchr(buf, '\n', n);
    size_t skip = nl == NULL ? n : (size_t)(nl + 1 - buf);
    memmove(buf, buf + skip, n - skip + 1);
    return n - skip;
}

firc_err_t firc_update_handoff(const char *dir, const char *sh, const char *opkg, const char *ipk, const char *tag,
                               int64_t epoch)
{
    if (dir == NULL || sh == NULL || opkg == NULL || ipk == NULL || tag == NULL) { return FIRC_ERR_INVAL; }
    char result[PATH_MAX], marker[PATH_MAX], line[128];
    if (snprintf(result, sizeof(result), "%s/result", dir) >= (int)sizeof(result) ||
        snprintf(marker, sizeof(marker), "%s/installing", dir) >= (int)sizeof(marker)) {
        return FIRC_ERR_INVAL;
    }
    int n = snprintf(line, sizeof(line), "%s %lld\n", tag, (long long)epoch);
    if (n < 0 || (size_t)n >= sizeof(line)) { return FIRC_ERR_INVAL; }
    if (unlink(result) != 0 && errno != ENOENT) { return FIRC_ERR_IO; }
    if (firc_atomic_write(marker, line, (size_t)n) != FIRC_OK) { return FIRC_ERR_IO; }
    char *const argv[] = {(char *)sh, "-c", (char *)handoff_sh, "firc-update", (char *)dir,
                          (char *)opkg, (char *)ipk, (char *)tag, NULL};
    firc_err_t err = firc_spawn_argv(sh, argv);
    if (err != FIRC_OK) { unlink(marker); }
    return err;
}
