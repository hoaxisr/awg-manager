#ifndef STEER_SOCKMARK_H
#define STEER_SOCKMARK_H
#include <stdint.h>

/* SO_MARK on sockets to the node, set before connect (--mark): policy routing can then keep them
 * out of the tunnel. 0: no mark. A socket the mark cannot be set on is not used. */
void transport_set_sock_mark(uint32_t mark);

/* SO_BINDTODEVICE on sockets to the node (--bind-dev); NULL or "": none. */
void transport_set_bind_dev(const char *ifname);

/* Marks and binds a socket to the node before its connect. 0, or -1 when the mark or the bind
 * is refused: the socket must not be used. */
int tr_sock_prepare(int fd);

#endif
