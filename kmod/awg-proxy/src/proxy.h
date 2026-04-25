/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _AWG_PROXY_PROXY_H
#define _AWG_PROXY_PROXY_H

#include <linux/types.h>
#include <linux/in.h>
#include <linux/net.h>
#include <linux/kthread.h>
#include <linux/atomic.h>
#include <linux/spinlock.h>

#include "transform.h"

#define AWG_MAX_TUNNELS 16
#define AWG_BUF_SIZE    2048  /* per-packet buffer (MTU + headroom) */

/*
 * Per-tunnel UDP proxy instance.
 *
 * Architecture:
 *   WG kernel -> 127.0.0.1:listen_port -> [proxy] -> AWG server
 *   AWG server -> [proxy] -> 127.0.0.1:client_port -> WG kernel
 *
 * Two kernel threads per proxy:
 *   c2s_thread: reads from listen_sock, transforms WG->AWG, sends to remote_sock
 *   s2c_thread: reads from remote_sock, transforms AWG->WG, sends to listen_sock
 */
/*
 * Forced 8-byte alignment: atomic64_t below requires 8-aligned address
 * on 32-bit MIPS (ll/sc-pair ops trap on misalignment). Putting it
 * first only aligns proxies[0]; without an explicit struct-level
 * aligned(8), proxies[1..15] depend on sizeof(struct awg_proxy) being
 * a multiple of 8, which is fragile across field reorders.
 */
struct awg_proxy {
	/*
	 * Stats — atomic64_t MUST be first for 8-byte alignment on 32-bit
	 * MIPS. Without this, atomic64 ops cause unaligned access panic.
	 */
	atomic64_t rx_bytes;  /* bytes received from server */
	atomic64_t tx_bytes;  /* bytes sent to server */
	atomic_t rx_packets;  /* packets from server */
	atomic_t tx_packets;  /* packets to server */

	/* Sockets */
	struct socket *listen_sock;     /* UDP, binds 127.0.0.1:0 (auto port) */
	struct socket *remote_sock;     /* UDP, connected to AWG server */

	/* Client address — protected by client_lock (written by c2s, read by s2c) */
	struct sockaddr_in client_addr;
	spinlock_t client_lock;
	bool has_client;

	/* Worker threads */
	struct task_struct *c2s_thread;  /* client->server */
	struct task_struct *s2c_thread;  /* server->client */

	/* AWG configuration (parsed from procfs) */
	awg_config_t cfg;

	/* CPS counter — only accessed from c2s_thread, no locking needed */
	u32 cps_counter;

	/* Headroom for outbound transform (max of s1,s2,s3,s4) */
	int headroom;

	/* Assigned listen port (host byte order, for /proc output) */
	u16 listen_port;

	/* Active flag */
	bool active;
} __aligned(8);

/*
 * Add a proxy from a procfs config line.
 * Creates sockets, starts threads.
 * Returns 0 on success.
 */
int awg_proxy_add(const char *config_line);

/*
 * Remove a proxy by remote endpoint.
 * Stops threads, closes sockets.
 * Returns 0 on success.
 */
int awg_proxy_del(__be32 ip, __be16 port);

/* Stop all proxies and free resources */
void awg_proxy_cleanup(void);

/* Format proxy list for procfs read */
int awg_proxy_list(char *buf, int buflen);

#endif /* _AWG_PROXY_PROXY_H */
