#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/rci_token.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#include "firc/atomic_write.h"
#include "firc/log.h"

#define NDMC_OUT_MAX (64 * 1024)
#define MAX_IDS 32
#define STOP_SLICE_MS 100

static struct {
    pthread_mutex_t mu;
    char path[256];
    char ndmc[256];
    bool on;
    bool loaded;
    char token[FIRC_KN_TOKEN_MAX];
    time_t last_issue;
    bool issued;
} g = {.mu = PTHREAD_MUTEX_INITIALIZER};

bool firc_kn_token_release_ok(const char *release)
{
    if (release == NULL || !isdigit((unsigned char)release[0])) { return false; }
    char *end = NULL;
    long major = strtol(release, &end, 10);
    if (*end != '.' || !isdigit((unsigned char)end[1])) { return false; }
    long minor = strtol(end + 1, NULL, 10);
    return major > 5 || (major == 5 && minor >= 2);
}

static void strip_ansi(const char *in, char *out, size_t cap)
{
    size_t o = 0;
    for (const char *p = in; *p != '\0' && o + 1 < cap; p++) {
        if (p[0] == '\x1b' && p[1] == '[' && p[2] == 'K') {
            p += 2;
            continue;
        }
        out[o++] = *p;
    }
    out[o] = '\0';
}

static char *trim(char *s)
{
    while (isspace((unsigned char)*s)) { s++; }
    size_t n = strlen(s);
    while (n > 0 && isspace((unsigned char)s[n - 1])) { s[--n] = '\0'; }
    return s;
}

static char *dup_clean(const char *out)
{
    size_t cap = strlen(out) + 1;
    char *buf = calloc(cap, 1);
    if (buf != NULL) { strip_ansi(out, buf, cap); }
    return buf;
}

static char *next_line(char **cur)
{
    char *line = *cur;
    if (line == NULL) { return NULL; }
    char *nl = strchr(line, '\n');
    if (nl != NULL) { *nl = '\0'; }
    *cur = nl != NULL ? nl + 1 : NULL;
    return line;
}

bool firc_kn_token_parse_generated(const char *out, char *id, size_t id_cap, char *tok, size_t tok_cap)
{
    id[0] = '\0';
    tok[0] = '\0';
    char *buf = dup_clean(out);
    if (buf == NULL) { return false; }
    size_t len = 0;
    bool in = false;
    bool ok = true;
    char *cur = buf;
    for (char *line; ok && (line = next_line(&cur)) != NULL;) {
        char *s = trim(line);
        char *colon = strchr(s, ':');
        if (in && (colon != NULL || *s == '\0')) { break; }
        if (!in) {
            if (colon == NULL) { continue; }
            *colon = '\0';
            char *k = trim(s);
            s = trim(colon + 1);
            if (strcmp(k, "id") == 0) { snprintf(id, id_cap, "%s", s); }
            if (strcmp(k, "value") != 0) { continue; }
            in = true;
        }
        size_t n = strlen(s);
        ok = len + n < tok_cap;
        if (ok) {
            memcpy(tok + len, s, n);
            len += n;
            tok[len] = '\0';
        }
    }
    free(buf);
    for (size_t i = 0; ok && i < len; i++) { ok = isalnum((unsigned char)tok[i]) != 0; }
    if (!ok || len < 32) {
        id[0] = '\0';
        tok[0] = '\0';
        return false;
    }
    return true;
}

size_t firc_kn_token_ids(const char *out, const char *user_data, char ids[][16], size_t cap)
{
    char *buf = dup_clean(out);
    if (buf == NULL) { return 0; }
    size_t n = 0;
    char id[16] = "";
    char *cur = buf;
    for (char *line; n < cap && (line = next_line(&cur)) != NULL;) {
        char *colon = strchr(line, ':');
        if (colon == NULL) { continue; }
        *colon = '\0';
        char *k = trim(line);
        char *v = trim(colon + 1);
        if (strcmp(k, "id") == 0) {
            snprintf(id, sizeof id, "%s", v);
        } else if (strcmp(k, "user-data") == 0 && strcmp(v, user_data) == 0 && id[0] != '\0') {
            snprintf(ids[n++], 16, "%s", id);
        }
    }
    free(buf);
    return n;
}

bool firc_kn_token_refusal(long status, const char *detail)
{
    if ((status != 401 && status != 403) || detail == NULL) { return false; }
    return strstr(detail, "0x2312") != NULL || strstr(detail, "0x1218") != NULL;
}

static int spawn_ndmc(const char *ndmc, const char *cmd, int out_fd, int err_fd, pid_t *pid)
{
    posix_spawn_file_actions_t fa;
    posix_spawnattr_t attr;
    int rc = posix_spawn_file_actions_init(&fa);
    if (rc != 0) { return rc; }
    rc = posix_spawnattr_init(&attr);
    if (rc != 0) {
        posix_spawn_file_actions_destroy(&fa);
        return rc;
    }
    sigset_t all, none;
    sigfillset(&all);
    sigemptyset(&none);
    char *argv[] = {(char *)ndmc, "-c", (char *)cmd, NULL};
    char *envp[] = {"PATH=/sbin:/usr/sbin:/bin:/usr/bin", "LD_LIBRARY_PATH=/lib:/usr/lib", NULL};
    if ((rc = posix_spawn_file_actions_addopen(&fa, STDIN_FILENO, "/dev/null", O_RDONLY, 0)) == 0 &&
        (rc = posix_spawn_file_actions_adddup2(&fa, out_fd, STDOUT_FILENO)) == 0 &&
        (rc = posix_spawn_file_actions_adddup2(&fa, err_fd, STDERR_FILENO)) == 0 &&
        (rc = posix_spawnattr_setsigdefault(&attr, &all)) == 0 && (rc = posix_spawnattr_setsigmask(&attr, &none)) == 0 &&
        (rc = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETSIGDEF | POSIX_SPAWN_SETSIGMASK)) == 0) {
        rc = posix_spawn(pid, ndmc, &fa, &attr, argv, envp);
    }
    posix_spawnattr_destroy(&attr);
    posix_spawn_file_actions_destroy(&fa);
    return rc;
}

static long ms_left(const struct timespec *start)
{
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return FIRC_KN_NDMC_TIMEOUT_MS -
           ((now.tv_sec - start->tv_sec) * 1000L + (now.tv_nsec - start->tv_nsec) / 1000000L);
}

static bool stopped(const atomic_bool *stop)
{
    return stop != NULL && atomic_load(stop);
}

static bool reap(pid_t pid, const struct timespec *start, const atomic_bool *stop, int *status)
{
    for (;;) {
        pid_t r = waitpid(pid, status, WNOHANG);
        if (r == pid) { return true; }
        if (r < 0 && errno != EINTR) { return false; }
        if (ms_left(start) <= 0 || stopped(stop)) {
            kill(pid, SIGKILL);
            while (waitpid(pid, status, 0) < 0 && errno == EINTR) {}
            return false;
        }
        struct timespec nap = {0, 10 * 1000000L};
        nanosleep(&nap, NULL);
    }
}

static firc_err_t ndmc_run(const char *ndmc, const char *cmd, const atomic_bool *stop, char **out)
{
    *out = NULL;
    char *buf = malloc(NDMC_OUT_MAX + 1);
    if (buf == NULL) { return FIRC_ERR_NOMEM; }
    int pfd[2], efd[2];
    if (pipe2(pfd, O_CLOEXEC) != 0) {
        free(buf);
        return FIRC_ERR_IO;
    }
    if (pipe2(efd, O_CLOEXEC) != 0) {
        close(pfd[0]);
        close(pfd[1]);
        free(buf);
        return FIRC_ERR_IO;
    }
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    pid_t pid;
    int rc = spawn_ndmc(ndmc, cmd, pfd[1], efd[1], &pid);
    close(pfd[1]);
    close(efd[1]);
    if (rc != 0) {
        close(pfd[0]);
        close(efd[0]);
        free(buf);
        return FIRC_ERR_IO;
    }

    char ebuf[512];
    size_t len = 0, elen = 0;
    struct pollfd p[2] = {{.fd = pfd[0], .events = POLLIN}, {.fd = efd[0], .events = POLLIN}};
    while (p[0].fd >= 0 || p[1].fd >= 0) {
        long left = ms_left(&start);
        if (left <= 0 || stopped(stop)) { break; }
        if (stop != NULL && left > STOP_SLICE_MS) { left = STOP_SLICE_MS; }
        int pr = poll(p, 2, (int)left);
        if (pr == 0 && stop != NULL) { continue; }
        if (pr < 0 && errno == EINTR) { continue; }
        if (pr <= 0) { break; }
        if (p[0].revents != 0) {
            ssize_t n = read(pfd[0], buf + len, NDMC_OUT_MAX - len);
            if (n < 0 && errno == EINTR) { continue; }
            if (n <= 0 || (len += (size_t)n) == NDMC_OUT_MAX) { p[0].fd = -1; }
        }
        if (p[1].revents != 0) {
            ssize_t n = read(efd[0], ebuf + elen, sizeof ebuf - 1 - elen);
            if (n < 0 && errno == EINTR) { continue; }
            if (n <= 0 || (elen += (size_t)n) == sizeof ebuf - 1) { p[1].fd = -1; }
        }
    }
    close(pfd[0]);
    close(efd[0]);
    buf[len] = '\0';
    ebuf[elen] = '\0';
    int status = 0;
    if (!reap(pid, &start, stop, &status)) {
        free(buf);
        if (stopped(stop)) { return FIRC_ERR_CANCELED; }
        FIRC_WARN("rci token: ndmc \"%s\" gave no answer in %d ms", cmd, FIRC_KN_NDMC_TIMEOUT_MS);
        return FIRC_ERR_TIMEOUT;
    }
    if (strstr(buf, "no such command") != NULL) {
        free(buf);
        return FIRC_ERR_NOSYS;
    }
    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        char *clean = dup_clean(elen > 0 ? ebuf : buf);
        char none[1] = "";
        char *line = clean != NULL ? trim(clean) : none;
        line[strcspn(line, "\r\n")] = '\0';
        if (WIFEXITED(status)) {
            FIRC_WARN("rci token: ndmc \"%s\" failed (exit %d): %s", cmd, WEXITSTATUS(status), line);
        } else {
            FIRC_WARN("rci token: ndmc \"%s\" failed (signal %d): %s", cmd, WTERMSIG(status), line);
        }
        free(clean);
        free(buf);
        return FIRC_ERR_UPSTREAM;
    }
    *out = buf;
    return FIRC_OK;
}

static firc_err_t delete_ours(const char *ndmc, const char *keep, const atomic_bool *stop)
{
    char *out = NULL;
    firc_err_t err = ndmc_run(ndmc, "show authentication token", stop, &out);
    if (err != FIRC_OK) { return err; }
    char ids[MAX_IDS][16];
    size_t n = firc_kn_token_ids(out, FIRC_KN_TOKEN_USER_DATA, ids, MAX_IDS);
    free(out);
    for (size_t i = 0; i < n; i++) {
        if (keep != NULL && strcmp(ids[i], keep) == 0) { continue; }
        char cmd[64];
        snprintf(cmd, sizeof cmd, "authentication token delete %.15s", ids[i]);
        err = ndmc_run(ndmc, cmd, stop, &out);
        free(out);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static void make_parents(const char *path)
{
    char p[sizeof g.path];
    snprintf(p, sizeof p, "%s", path);
    char *slash = strrchr(p, '/');
    if (slash == NULL || slash == p) { return; }
    *slash = '\0';
    for (char *q = p + 1; *q != '\0'; q++) {
        if (*q == '/') {
            *q = '\0';
            (void)mkdir(p, 0700);
            *q = '/';
        }
    }
    (void)mkdir(p, 0700);
}

static bool store(const char *tok)
{
    make_parents(g.path);
    char line[FIRC_KN_TOKEN_MAX + 1];
    int n = snprintf(line, sizeof line, "%s\n", tok);
    firc_err_t err = firc_atomic_write(g.path, line, (size_t)n);
    if (err != FIRC_OK) {
        FIRC_WARN("rci token: %s not written (%s); the token is deleted again, firc goes without one", g.path,
                  firc_err_str(err));
    }
    return err == FIRC_OK;
}

static bool issue(const atomic_bool *stop)
{
    g.token[0] = '\0';
    g.last_issue = time(NULL);
    g.issued = true;
    char *out = NULL;
    firc_err_t err = ndmc_run(g.ndmc, "authentication token generate " FIRC_KN_TOKEN_USER_DATA, stop, &out);
    if (err != FIRC_OK) { return err != FIRC_ERR_CANCELED; }
    char id[16], tok[FIRC_KN_TOKEN_MAX];
    bool ok = firc_kn_token_parse_generated(out, id, sizeof id, tok, sizeof tok);
    free(out);
    if (!ok) { FIRC_WARN("rci token: ndmc issued a token firc cannot read; firc's tokens are deleted"); }
    if (!ok || !store(tok)) {
        (void)delete_ours(g.ndmc, NULL, stop);
        return !stopped(stop);
    }
    if (id[0] != '\0') { (void)delete_ours(g.ndmc, id, stop); }
    snprintf(g.token, sizeof g.token, "%s", tok);
    FIRC_INFO("rci token: issued");
    return true;
}

static bool pause_over(void)
{
    return !g.issued || time(NULL) - g.last_issue >= FIRC_KN_TOKEN_REISSUE_SECONDS;
}

void firc_kn_token_setup(const char *path, const char *ndmc)
{
    pthread_mutex_lock(&g.mu);
    snprintf(g.path, sizeof g.path, "%s", path);
    snprintf(g.ndmc, sizeof g.ndmc, "%s", ndmc);
    g.on = g.path[0] != '\0' && access(g.path, F_OK) == 0;
    g.loaded = false;
    g.issued = false;
    g.token[0] = '\0';
    pthread_mutex_unlock(&g.mu);
}

void firc_kn_token_set_release(const char *release)
{
    pthread_mutex_lock(&g.mu);
    if (g.path[0] != '\0' && firc_kn_token_release_ok(release)) { g.on = true; }
    pthread_mutex_unlock(&g.mu);
}

static void load(void)
{
    g.loaded = true;
    char buf[FIRC_KN_TOKEN_MAX + 2] = "";
    int fd = open(g.path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) { return; }
    ssize_t n = read(fd, buf, sizeof buf - 1);
    close(fd);
    buf[n > 0 ? n : 0] = '\0';
    snprintf(g.token, sizeof g.token, "%s", trim(buf));
}

static bool lock(const atomic_bool *stop)
{
    if (stop == NULL) {
        pthread_mutex_lock(&g.mu);
        return true;
    }
    while (!stopped(stop)) {
        struct timespec until;
        clock_gettime(CLOCK_REALTIME, &until);
        until.tv_nsec += STOP_SLICE_MS * 1000000L;
        if (until.tv_nsec >= 1000000000L) {
            until.tv_sec++;
            until.tv_nsec -= 1000000000L;
        }
        int rc = pthread_mutex_timedlock(&g.mu, &until);
        if (rc == 0 && !stopped(stop)) { return true; }
        if (rc == 0) { pthread_mutex_unlock(&g.mu); }
    }
    return false;
}

bool firc_kn_token_current(char out[FIRC_KN_TOKEN_MAX], const atomic_bool *stop)
{
    out[0] = '\0';
    if (!lock(stop)) { return false; }
    bool ok = true;
    if (g.on) {
        if (!g.loaded) { load(); }
        if (g.token[0] == '\0' && pause_over()) { ok = issue(stop); }
        snprintf(out, FIRC_KN_TOKEN_MAX, "%s", g.token);
    }
    pthread_mutex_unlock(&g.mu);
    return ok;
}

bool firc_kn_token_refused(const char *stale, char out[FIRC_KN_TOKEN_MAX], const atomic_bool *stop)
{
    out[0] = '\0';
    if (!lock(stop)) { return false; }
    bool ok = true;
    g.on = g.path[0] != '\0';
    if (g.on) {
        if (!g.loaded) { load(); }
        if (strcmp(g.token, stale) == 0) {
            g.token[0] = '\0';
            if (pause_over()) { ok = issue(stop); }
        }
        snprintf(out, FIRC_KN_TOKEN_MAX, "%s", g.token);
    }
    pthread_mutex_unlock(&g.mu);
    return ok;
}

static bool release_old(const char *release)
{
    return release != NULL && isdigit((unsigned char)release[0]) && strchr(release, '.') != NULL &&
           !firc_kn_token_release_ok(release);
}

firc_err_t firc_kn_token_revoke(const char *path, const char *ndmc, const char *release)
{
    bool had_file = access(path, F_OK) == 0;
    if (!had_file && release_old(release)) { return FIRC_OK; }
    firc_err_t err = delete_ours(ndmc, NULL, NULL);
    if (err != FIRC_OK && err != FIRC_ERR_NOSYS) { return err; }
    if (had_file && unlink(path) != 0 && errno != ENOENT) { return FIRC_ERR_IO; }
    return FIRC_OK;
}
