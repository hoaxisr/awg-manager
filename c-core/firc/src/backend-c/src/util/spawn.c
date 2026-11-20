#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/spawn.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

extern char **environ;

firc_err_t firc_spawn_detached(const char *path, const char *arg)
{
    if (path == NULL || arg == NULL) { return FIRC_ERR_INVAL; }
    return firc_spawn_argv(path, (char *const[]){(char *)path, (char *)arg, NULL});
}

firc_err_t firc_spawn_argv(const char *path, char *const argv[])
{
    if (path == NULL || argv == NULL) { return FIRC_ERR_INVAL; }
    size_t argc = 0;
    while (argc <= FIRC_SPAWN_MAX_ARGS && argv[argc] != NULL) { argc++; }
    if (argc == 0 || argc > FIRC_SPAWN_MAX_ARGS) { return FIRC_ERR_INVAL; }

    /* Compute everything before fork: the child may call only async-signal-safe functions (sysconf is not one). */
    long max_fd = sysconf(_SC_OPEN_MAX);
    if (max_fd < 0 || max_fd > 65536) { max_fd = 65536; }
    static const int reset[] = {SIGHUP, SIGINT, SIGQUIT, SIGTERM, SIGPIPE,
                                SIGCHLD, SIGALRM, SIGUSR1, SIGUSR2};
    struct sigaction dfl;
    memset(&dfl, 0, sizeof(dfl));
    dfl.sa_handler = SIG_DFL;
    sigemptyset(&dfl.sa_mask);
    sigset_t none;
    sigemptyset(&none);

    /* O_CLOEXEC via pipe2 in one call: a concurrent fork must not leak the write end. */
    int pfd[2];
    if (pipe2(pfd, O_CLOEXEC) != 0) { return firc_err_from_errno(errno); }

    pid_t pid = fork();
    if (pid < 0) {
        int e = errno;
        close(pfd[0]);
        close(pfd[1]);
        return firc_err_from_errno(e);
    }
    if (pid == 0) {
        close(pfd[0]);
        if (setsid() < 0) { _exit(127); }
        pid_t grandchild = fork();
        if (grandchild != 0) { _exit(grandchild < 0 ? 127 : 0); }
        for (size_t i = 0; i < sizeof(reset) / sizeof(reset[0]); i++) {
            (void)sigaction(reset[i], &dfl, NULL);
        }
        (void)sigprocmask(SIG_SETMASK, &none, NULL);
        int null = open("/dev/null", O_RDWR);
        if (null >= 0) {
            (void)dup2(null, 0);
            (void)dup2(null, 1);
            (void)dup2(null, 2);
        }
        for (long fd = 3; fd < max_fd; fd++) {
            if (fd != pfd[1]) { (void)close((int)fd); }
        }
        execve(path, argv, environ);
        int e = errno;
        if (write(pfd[1], &e, sizeof(e)) < 0) { _exit(126); }
        _exit(127);
    }

    close(pfd[1]);
    int child_errno = 0;
    ssize_t n;
    do {
        n = read(pfd[0], &child_errno, sizeof(child_errno));
    } while (n < 0 && errno == EINTR);
    close(pfd[0]);
    int status = 0;
    pid_t w;
    do {
        w = waitpid(pid, &status, 0);
    } while (w < 0 && errno == EINTR);
    if (n == (ssize_t)sizeof(child_errno)) { return firc_err_from_errno(child_errno); }
    if (w < 0) { return firc_err_from_errno(errno); }
    return WIFEXITED(status) && WEXITSTATUS(status) == 0 ? FIRC_OK : FIRC_ERR_SYS;
}
