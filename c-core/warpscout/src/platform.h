/* awgm-warpscout: tiny portability layer (POSIX / Winsock2). */
#ifndef WS_PLATFORM_H
#define WS_PLATFORM_H

#include <stdint.h>

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <io.h>
#include <fcntl.h>

typedef SOCKET sock_t;
typedef WSAPOLLFD ws_pollfd;
#define WS_BAD_SOCK INVALID_SOCKET
#define ws_poll(fds, n, ms) WSAPoll((fds), (ULONG)(n), (ms))
#define ws_closesock(s) closesocket(s)
static inline int ws_sockerr(void) { return WSAGetLastError(); }
static inline int ws_would_block(int e) { return e == WSAEWOULDBLOCK; }
static inline int ws_intr(int e) { return e == WSAEINTR; }
static inline int ws_set_nonblock(sock_t s) {
    u_long one = 1;
    return ioctlsocket(s, FIONBIO, &one) == 0 ? 0 : -1;
}
static inline int ws_net_init(void) {
    WSADATA w;
    return WSAStartup(MAKEWORD(2, 2), &w) == 0 ? 0 : -1;
}
static inline uint64_t ws_now_ms(void) {
    static LARGE_INTEGER f;
    LARGE_INTEGER c;
    if (!f.QuadPart) QueryPerformanceFrequency(&f);
    QueryPerformanceCounter(&c);
    return (uint64_t)((double)c.QuadPart * 1000.0 / (double)f.QuadPart);
}
static inline void ws_sleep_ms(int ms) { Sleep((DWORD)ms); }
#else
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <poll.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <time.h>

typedef int sock_t;
typedef struct pollfd ws_pollfd;
#define WS_BAD_SOCK (-1)
#define ws_poll(fds, n, ms) poll((fds), (nfds_t)(n), (ms))
#define ws_closesock(s) close(s)
static inline int ws_sockerr(void) { return errno; }
static inline int ws_would_block(int e) { return e == EAGAIN || e == EWOULDBLOCK; }
static inline int ws_intr(int e) { return e == EINTR; }
static inline int ws_set_nonblock(sock_t s) {
    int fl = fcntl(s, F_GETFL, 0);
    return (fl >= 0 && fcntl(s, F_SETFL, fl | O_NONBLOCK) == 0) ? 0 : -1;
}
static inline int ws_net_init(void) { return 0; }
static inline uint64_t ws_now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000ULL + (uint64_t)ts.tv_nsec / 1000000ULL;
}
static inline void ws_sleep_ms(int ms) {
    struct timespec ts = {ms / 1000, (long)(ms % 1000) * 1000000L};
    nanosleep(&ts, NULL);
}
#endif

#endif
