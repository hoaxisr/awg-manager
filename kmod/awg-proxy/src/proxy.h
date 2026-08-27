/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _AWG_PROXY_PROXY_H
#define _AWG_PROXY_PROXY_H

#include <linux/types.h>
#include <linux/in.h>
#include <linux/net.h>
#include <linux/kthread.h>
#include <linux/atomic.h>
#include <linux/spinlock.h>
#include <linux/skbuff.h>
#include <linux/wait.h>
#include <linux/netdevice.h>
#include <net/dst_cache.h>

#include "transform.h"

#define AWG_MAX_TUNNELS 16
#define AWG_BUF_SIZE    2048  /* per-packet buffer (MTU + headroom) */
#define AWG_RX_QUEUE_MAX 1024 /* max server->client skbs queued by encap_rcv */

/* AWG 3.1 random-trailer adaptive window: seeded to 500, grows to the largest
 * datagram observed (clamped to 1500), so handshake trailer sizes track the
 * connection's natural envelope. Mirrors amneziawg-go DefaultUdpWindow. */
#define AWG_DEFAULT_UDP_WINDOW 500
#define AWG_UDP_WINDOW_MAX     1500

/*
 * Per-tunnel UDP proxy instance.
 *
 * Architecture:
 *   WG kernel -> 127.0.0.1:listen_port -> [proxy] -> AWG server
 *   AWG server -> [proxy] -> 127.0.0.1:client_port -> WG kernel
 *
 * Two kernel threads per proxy:
 *   c2s_thread: reads from listen_sock, transforms WG->AWG, sends to remote_sock
 *   s2c_thread: drains rx_queue (fed by encap_rcv), transforms AWG->WG, sends to listen_sock
 *
 * Server->client receive uses a UDP-encap socket (setup_udp_tunnel_sock +
 * awg_encap_rcv) instead of kernel_recvmsg. This mirrors the native AWG/WG
 * kernel module (src/socket.c: encap_rcv=wg_receive) and is what keeps the
 * flow out of Keenetic's FASTNAT/PPE offload — a plain recvmsg socket gets a
 * forward-only offload entry latched while [UNREPLIED] and the server's
 * handshake reply is then dropped before delivery. The softirq encap_rcv
 * callback only enqueues; all sleeping work (cookie crypto, sendmsg) stays in
 * s2c_thread's process context.
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
	atomic_t udp_window;  /* AWG 3.1 random-trailer size envelope */

	/* Sockets */
	struct socket *listen_sock;     /* UDP, binds 127.0.0.1:0 (auto port) */
	struct socket *remote_sock;     /* UDP, connected to AWG server */

	/* Client address — protected by client_lock (written by c2s, read by
	 * s2c). Always sockaddr_in: the local WG client talks to us over the
	 * IPv4 loopback listen socket (127.0.0.1:auto) regardless of the
	 * remote endpoint's family, so this side never becomes IPv6. */
	struct sockaddr_in client_addr;
	spinlock_t client_lock;
	bool has_client;

	/* Worker threads */
	struct task_struct *c2s_thread;  /* client->server */
	struct task_struct *s2c_thread;  /* server->client */

	/* Server->client receive queue: awg_encap_rcv (softirq) enqueues skbs,
	 * s2c_thread drains in process context. */
	struct sk_buff_head rx_queue;
	wait_queue_head_t rx_wait;

	/* Client->server TX route cache (udp_tunnel_xmit_skb path). Accessed
	 * only from c2s_thread, but dst_cache uses this_cpu_ptr → callers must
	 * local_bh_disable() around get/set. */
	struct dst_cache tx_dst_cache;
	int bind_oif;   /* WAN egress ifindex from cfg.bind_iface; 0 = default route */

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

	/*
	 * Cookie-reply AAD translation state. The AWG server encrypts
	 * cookie_reply with AAD = MAC1_new (after our header substitution and
	 * MAC1 recompute), while the local vanilla-WG client decrypts with
	 * AAD = MAC1_old (the MAC1 it originally generated).
	 */
	u8 cookie_aead_key[32];
	u8 last_mac1_old[16];
	u8 last_mac1_new[16];
	bool have_last_mac1;
	bool has_cookie_key;    /* server_pub was set → cookie translation enabled */
	spinlock_t mac1_lock;

	/*
	 * Stashed decrypted cookie from the most recent cookie_reply, used to
	 * recompute MAC2 on subsequent handshakes. Without this, the server
	 * keeps responding with cookie_replies under load: client computes
	 * MAC2 over [01...||MAC1_old], server validates over [H1...||MAC1_new],
	 * mismatch -> VALID_MAC_BUT_NO_COOKIE -> another cookie_reply.
	 *
	 * Lifetime: COOKIE_TTL_NS (~120s, matches official AWG
	 * COOKIE_SECRET_MAX_AGE). On proxy restart we lose this and self-heal
	 * via one extra cookie_reply roundtrip.
	 */
	u8 latest_cookie[16];
	u64 latest_cookie_birthdate_ns;
	bool latest_cookie_valid;
	spinlock_t cookie_lock;
} __aligned(8);

#include "proxy_recv.h"

/*
 * Add a proxy from a procfs config line.
 * Creates sockets, starts threads.
 * Returns 0 on success.
 */
int awg_proxy_add(const char *config_line);

/*
 * Remove a proxy by remote endpoint (IPv4 or IPv6, matched by family).
 * Stops threads, closes sockets.
 * Returns 0 on success.
 */
int awg_proxy_del(const struct awg_endpoint_addr *addr, __be16 port);

/* Stop all proxies and free resources */
void awg_proxy_cleanup(void);

/* Format proxy list for procfs read */
int awg_proxy_list(char *buf, int buflen);

/*
 * Dummy net_device for the udp_tunnel_xmit_skb TX path. iptunnel_xmit derefs
 * skb->dev->tstats for stats, so egress skbs need a dev with allocated per-cpu
 * stats. Created at module init, destroyed at exit (after awg_proxy_cleanup).
 */
int awg_xmit_dev_create(void);
void awg_xmit_dev_destroy(void);

#endif /* _AWG_PROXY_PROXY_H */
