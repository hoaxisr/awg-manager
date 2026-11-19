/* The tunnel stack: packets from the TUN device into flows to a node through a dialer (dialer.h).
 * What it does and does not do: see the top of stack.c. */
#ifndef STEER_STACK_H
#define STEER_STACK_H
#include <stdint.h>
#include "dialer.h"

/* The device the stack creates and brings up. */
struct tun_cfg {
    const char *dev;    /* name as asked; the kernel's limit is 15 characters */
    uint32_t addr;      /* IPv4 address of the device, network order */
    int prefix;         /* its prefix length */
    int tcp_port;
    uint32_t tcp_pool_be;
    int tcp_pool_len;
};

/* The device is up, has its address and is ready to carry traffic: called once, from the
 * stack_run thread, before the loop threads start. The caller adds its routes here. */
typedef void (*stack_ready_fn)(void *arg, const char *dev);

/* Create the TUN device tc, bring it up (ifcfg.c) and carry its traffic through dialer d until
 * the queues end. ready may be NULL.
 *
 * Always returns 1: the loop has no successful exit (see the end of stack_run). */
int stack_run(const struct tun_cfg *tc, const struct dialer *d, stack_ready_fn ready, void *arg);

/* The set of active nodes changed: the stack resets (RST to the client) every connection whose node
 * is no longer active (dialer_ops.stale), so applications reconnect at once instead of waiting for
 * their own timeout on a hung connection. Callable from any thread; each loop thread notices on its
 * next pass (within a second). */
void stack_nodes_changed(void);

int stack_tcp_port(void);

/* Wakes every loop to reset its TUN connections and waits for them, at most wait_ms. Any thread. */
void stack_stop(int wait_ms);

#endif
