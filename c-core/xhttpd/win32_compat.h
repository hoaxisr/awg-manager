#ifndef AWGM_WIN32_COMPAT_H
#define AWGM_WIN32_COMPAT_H

#ifdef _WIN32

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <winsock2.h>
#include <ws2tcpip.h>
#include <process.h>
#include <io.h>
#include <fcntl.h>
#include <direct.h>
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <time.h>

#pragma comment(lib, "ws2_32.lib")

#ifndef SSIZE_T_DEFINED
typedef SSIZE_T ssize_t;
#define SSIZE_T_DEFINED
#endif

typedef int socklen_t;

#define poll WSAPoll

static inline int win32_close_fd(int fd) {
    if (fd < 0) return -1;
    if (closesocket((SOCKET)fd) == SOCKET_ERROR) {
        if (WSAGetLastError() == WSAENOTSOCK) {
            return _close(fd);
        }
        return -1;
    }
    return 0;
}

static inline ssize_t win32_write_fd(int fd, const void *buf, size_t len) {
    if (fd < 0) return -1;
    int r = send((SOCKET)fd, (const char *)buf, (int)len, 0);
    if (r == SOCKET_ERROR) {
        if (WSAGetLastError() == WSAENOTSOCK) {
            return _write(fd, buf, (unsigned int)len);
        }
        return -1;
    }
    return (ssize_t)r;
}

static inline ssize_t win32_read_fd(int fd, void *buf, size_t len) {
    if (fd < 0) return -1;
    int r = recv((SOCKET)fd, (char *)buf, (int)len, 0);
    if (r == SOCKET_ERROR) {
        if (WSAGetLastError() == WSAENOTSOCK) {
            return _read(fd, buf, (unsigned int)len);
        }
        return -1;
    }
    return (ssize_t)r;
}

#define close(s) win32_close_fd((int)(s))
#define read(s, b, l) win32_read_fd((int)(s), (b), (size_t)(l))
#define write(s, b, l) win32_write_fd((int)(s), (b), (size_t)(l))

#define SHUT_RDWR SD_BOTH
#define SHUT_RD SD_RECEIVE
#define SHUT_WR SD_SEND

#ifndef MSG_NOSIGNAL
#define MSG_NOSIGNAL 0
#endif

#ifndef MSG_DONTWAIT
#define MSG_DONTWAIT 0
#endif

#ifndef SOCK_CLOEXEC
#define SOCK_CLOEXEC 0
#endif
#ifndef SOCK_NONBLOCK
#define SOCK_NONBLOCK 0
#endif

#ifndef ETIMEDOUT
#define ETIMEDOUT WSAETIMEDOUT
#endif

#ifndef SIGHUP
#define SIGHUP 1
#endif
#ifndef SIGPIPE
#define SIGPIPE 13
#endif

#define strcasecmp _stricmp
#define strncasecmp _strnicmp
#define sleep(s) Sleep((DWORD)((s) * 1000))
#define usleep(us) Sleep((DWORD)((us) / 1000))

static inline int setenv(const char *name, const char *value, int overwrite) {
    (void)overwrite;
    return _putenv_s(name, value);
}
static inline int unsetenv(const char *name) {
    return _putenv_s(name, "");
}
#define fchmod(fd, mode) 0
#define truncate(p, s) 0

static inline void *memmem(const void *haystack, size_t haystacklen,
                           const void *needle, size_t needlelen) {
    if (!haystack || !needle || needlelen > haystacklen) return NULL;
    if (needlelen == 0) return (void *)haystack;
    const uint8_t *h = (const uint8_t *)haystack;
    const uint8_t *n = (const uint8_t *)needle;
    for (size_t i = 0; i <= haystacklen - needlelen; i++) {
        if (h[i] == n[0] && memcmp(h + i, n, needlelen) == 0) return (void *)(h + i);
    }
    return NULL;
}

#ifndef CLOCK_REALTIME
#define CLOCK_REALTIME 0
#endif
#ifndef CLOCK_MONOTONIC
#define CLOCK_MONOTONIC 1
#endif

static inline struct tm *localtime_r(const time_t *timer, struct tm *buf) {
    if (!timer || !buf) return NULL;
    if (localtime_s(buf, timer) == 0) return buf;
    return NULL;
}

static inline int clock_gettime(int clk_id, struct timespec *ts) {
    (void)clk_id;
    if (!ts) return -1;
    timespec_get(ts, TIME_UTC);
    return 0;
}

static inline char *strcasestr(const char *haystack, const char *needle) {
    if (!haystack || !needle) return NULL;
    if (!*needle) return (char *)haystack;
    size_t nlen = strlen(needle);
    for (; *haystack; haystack++) {
        if (_strnicmp(haystack, needle, nlen) == 0) {
            return (char *)haystack;
        }
    }
    return NULL;
}

static inline void init_winsock(void) {
    static int inited = 0;
    if (!inited) {
        WSADATA wsa;
        WSAStartup(MAKEWORD(2, 2), &wsa);
        inited = 1;
    }
}

static inline int socketpair_helper(int domain, int type, int protocol, int sv[2]) {
    (void)domain; (void)type; (void)protocol;
    init_winsock();
    SOCKET listener = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (listener == INVALID_SOCKET) return -1;

    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    addr.sin_port = 0;

    if (bind(listener, (struct sockaddr *)&addr, sizeof(addr)) != 0) {
        closesocket(listener);
        return -1;
    }
    int len = sizeof(addr);
    if (getsockname(listener, (struct sockaddr *)&addr, &len) != 0) {
        closesocket(listener);
        return -1;
    }
    if (listen(listener, 1) != 0) {
        closesocket(listener);
        return -1;
    }
    SOCKET c = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (c == INVALID_SOCKET) {
        closesocket(listener);
        return -1;
    }
    if (connect(c, (struct sockaddr *)&addr, sizeof(addr)) != 0) {
        closesocket(c);
        closesocket(listener);
        return -1;
    }
    SOCKET s = accept(listener, NULL, NULL);
    closesocket(listener);
    if (s == INVALID_SOCKET) {
        closesocket(c);
        return -1;
    }
    sv[0] = (int)s;
    sv[1] = (int)c;
    return 0;
}

#ifndef AF_UNIX
#define AF_UNIX 1
#endif
#define socketpair(d, t, p, sv) socketpair_helper(d, t, p, sv)
#define pipe(sv) socketpair_helper(0, 0, 0, sv)

/* Threading via SRWLOCK and CONDITION_VARIABLE */
typedef SRWLOCK pthread_mutex_t;
#define PTHREAD_MUTEX_INITIALIZER SRWLOCK_INIT

static inline int pthread_mutex_init(pthread_mutex_t *m, void *attr) {
    (void)attr;
    InitializeSRWLock(m);
    return 0;
}
static inline int pthread_mutex_destroy(pthread_mutex_t *m) {
    (void)m;
    return 0;
}
static inline int pthread_mutex_lock(pthread_mutex_t *m) {
    AcquireSRWLockExclusive(m);
    return 0;
}
static inline int pthread_mutex_unlock(pthread_mutex_t *m) {
    ReleaseSRWLockExclusive(m);
    return 0;
}
static inline int pthread_mutex_trylock(pthread_mutex_t *m) {
    return TryAcquireSRWLockExclusive(m) ? 0 : 16;
}

typedef struct {
    SRWLOCK lock;
    int is_write;
} pthread_rwlock_t;
#define PTHREAD_RWLOCK_INITIALIZER { SRWLOCK_INIT, 0 }

static inline int pthread_rwlock_init(pthread_rwlock_t *l, void *attr) {
    (void)attr;
    InitializeSRWLock(&l->lock);
    l->is_write = 0;
    return 0;
}
static inline int pthread_rwlock_destroy(pthread_rwlock_t *l) {
    (void)l;
    return 0;
}
static inline int pthread_rwlock_rdlock(pthread_rwlock_t *l) {
    AcquireSRWLockShared(&l->lock);
    return 0;
}
static inline int pthread_rwlock_wrlock(pthread_rwlock_t *l) {
    AcquireSRWLockExclusive(&l->lock);
    l->is_write = 1;
    return 0;
}
static inline int pthread_rwlock_unlock(pthread_rwlock_t *l) {
    if (l->is_write) {
        l->is_write = 0;
        ReleaseSRWLockExclusive(&l->lock);
    } else {
        ReleaseSRWLockShared(&l->lock);
    }
    return 0;
}

typedef CONDITION_VARIABLE pthread_cond_t;
#define PTHREAD_COND_INITIALIZER CONDITION_VARIABLE_INIT

static inline int pthread_cond_init(pthread_cond_t *c, void *attr) {
    (void)attr;
    InitializeConditionVariable(c);
    return 0;
}
static inline int pthread_cond_destroy(pthread_cond_t *c) {
    (void)c;
    return 0;
}
static inline int pthread_cond_signal(pthread_cond_t *c) {
    WakeConditionVariable(c);
    return 0;
}
static inline int pthread_cond_broadcast(pthread_cond_t *c) {
    WakeAllConditionVariable(c);
    return 0;
}
static inline int pthread_cond_wait(pthread_cond_t *c, pthread_mutex_t *m) {
    SleepConditionVariableSRW(c, m, INFINITE, 0);
    return 0;
}
static inline int pthread_cond_timedwait(pthread_cond_t *c, pthread_mutex_t *m, const struct timespec *ts) {
    struct timespec now;
    timespec_get(&now, TIME_UTC);
    int64_t diff_ms = (int64_t)(ts->tv_sec - now.tv_sec) * 1000 + (ts->tv_nsec - now.tv_nsec) / 1000000;
    if (diff_ms <= 0) diff_ms = 0;
    BOOL ok = SleepConditionVariableSRW(c, m, (DWORD)diff_ms, 0);
    if (!ok && GetLastError() == ERROR_TIMEOUT) {
        return ETIMEDOUT;
    }
    return 0;
}

typedef HANDLE pthread_t;
typedef struct {
    size_t stack_size;
    int detach;
} pthread_attr_t;

#define PTHREAD_CREATE_DETACHED 1

static inline int pthread_attr_init(pthread_attr_t *a) {
    a->stack_size = 0;
    a->detach = 0;
    return 0;
}
static inline int pthread_attr_destroy(pthread_attr_t *a) {
    (void)a;
    return 0;
}
static inline int pthread_attr_setstacksize(pthread_attr_t *a, size_t s) {
    a->stack_size = s;
    return 0;
}
static inline int pthread_attr_setdetachstate(pthread_attr_t *a, int d) {
    a->detach = d;
    return 0;
}

typedef struct {
    void *(*fn)(void *);
    void *arg;
    int detach;
} win_thread_context_t;

static inline DWORD WINAPI win_thread_entry(LPVOID p) {
    win_thread_context_t *wt = (win_thread_context_t *)p;
    void *(*fn)(void *) = wt->fn;
    void *arg = wt->arg;
    free(wt);
    fn(arg);
    return 0;
}

static inline int pthread_create(pthread_t *t, const pthread_attr_t *attr, void *(*start)(void *), void *arg) {
    win_thread_context_t *wt = (win_thread_context_t *)malloc(sizeof(win_thread_context_t));
    if (!wt) return -1;
    wt->fn = start;
    wt->arg = arg;
    wt->detach = attr ? attr->detach : 0;
    size_t stack = attr ? attr->stack_size : 0;
    HANDLE h = CreateThread(NULL, stack, win_thread_entry, wt, 0, NULL);
    if (!h) {
        free(wt);
        return -1;
    }
    if (wt->detach) {
        CloseHandle(h);
        if (t) *t = NULL;
    } else {
        if (t) *t = h;
    }
    return 0;
}

static inline int pthread_detach(pthread_t t) {
    if (t) CloseHandle(t);
    return 0;
}

static inline int pthread_join(pthread_t t, void **retval) {
    (void)retval;
    if (t) {
        WaitForSingleObject(t, INFINITE);
        CloseHandle(t);
    }
    return 0;
}

#endif /* _WIN32 */

#endif /* AWGM_WIN32_COMPAT_H */
