#define _GNU_SOURCE
#include <errno.h>
#include <net/if.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

#include "sockmark.h"

static uint32_t g_sock_mark;
static char g_bind_dev[IFNAMSIZ];
static int g_mark_told, g_bind_told;

void transport_set_sock_mark(uint32_t mark) {
    g_sock_mark = mark;
}

void transport_set_bind_dev(const char *ifname) {
    snprintf(g_bind_dev, sizeof(g_bind_dev), "%s", ifname ? ifname : "");
}

int tr_sock_prepare(int fd) {
    if (g_sock_mark &&
        setsockopt(fd, SOL_SOCKET, SO_MARK, &g_sock_mark, sizeof(g_sock_mark)) != 0) {
        if (!__atomic_exchange_n(&g_mark_told, 1, __ATOMIC_RELAXED)) {
            fprintf(stderr, "tunvless[warn]: cannot set mark 0x%08x on a socket to the node "
                            "(%s) — not connecting: unmarked, it would go into the tunnel\n",
                    g_sock_mark, strerror(errno));
        }
        return -1;
    }
    if (g_bind_dev[0] &&
        setsockopt(fd, SOL_SOCKET, SO_BINDTODEVICE, g_bind_dev, (socklen_t)strlen(g_bind_dev) + 1) != 0) {
        if (!__atomic_exchange_n(&g_bind_told, 1, __ATOMIC_RELAXED)) {
            fprintf(stderr, "tunvless[warn]: cannot bind a socket for the node to %s (%s) — not "
                            "connecting: unbound, it would go into the tunnel\n",
                    g_bind_dev, strerror(errno));
        }
        return -1;
    }
    return 0;
}
