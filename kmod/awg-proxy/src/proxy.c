// SPDX-License-Identifier: GPL-2.0
/*
 * AWG Proxy - Kernel UDP proxy for WG<->AWG transformation.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 * Adapted: userspace sockets → kernel sockets, pthreads → kthreads,
 *          batch I/O → single-packet, fastrand → get_random_bytes.
 *
 * Each proxy instance creates two kernel UDP sockets and two threads:
 *   listen_sock  - binds to 127.0.0.1:auto, receives from WG
 *   remote_sock  - UDP-encap socket to AWG server (sends, and receives via
 *                  awg_encap_rcv instead of recvmsg — keeps the flow out of
 *                  Keenetic FASTNAT/PPE offload; see awg_encap_rcv)
 *   c2s_thread   - WG->AWG: recvmsg(listen) -> transform -> sendmsg(remote)
 *   s2c_thread   - AWG->WG: drain rx_queue -> transform -> sendmsg(listen)
 */

#include <linux/kernel.h>
#include <linux/slab.h>
#include <linux/kthread.h>
#include <linux/mutex.h>
#include <linux/net.h>
#include <linux/in.h>
#include <linux/in6.h>
#include <linux/socket.h>
#include <linux/random.h>
#include <linux/delay.h>
#include <linux/ktime.h>
#include <linux/skbuff.h>
#include <linux/udp.h>
#include <linux/netdevice.h>
#include <linux/version.h>
#include <net/sock.h>
#include <net/inet_sock.h>
#include <net/udp.h>
#include <net/udp_tunnel.h>
#include <net/route.h>
#include <net/ip.h>
#include <net/dst_cache.h>
#if IS_ENABLED(CONFIG_IPV6)
#include <linux/ipv6.h>
#include <net/ipv6.h>
#include <net/ip6_route.h>
#include <net/addrconf.h>	/* ipv6_stub */
#endif

#include "proxy.h"
#include "transform.h"
#include "cps.h"
#include "cookie.h"
#include "blake2s.h"

/*
 * Cookie TTL. Matches official AWG COOKIE_SECRET_MAX_AGE (120s). Past this
 * the client also expires its cookie and falls back to MAC2=zeros, so the
 * proxy and client desync gracefully (one extra cookie_reply roundtrip).
 */
#define AWG_COOKIE_TTL_NS (120ULL * NSEC_PER_SEC)

/*
 * IPv6 TX route lookup — which ipv6_stub member exists?
 *
 * Commit 6c8991f41546 ("net: ipv6_stub: use ip6_dst_lookup_flow instead of
 * ip6_dst_lookup", v5.5) RENAMED the stub member ipv6_dst_lookup ->
 * ipv6_dst_lookup_flow (new signature). Careful: the December-2019 stable
 * backports (4.4.207/4.9.207/4.14.160/4.19.91) only added the
 * ip6_dst_lookup_flow FUNCTION; the ipv6_stub member itself was renamed
 * later, in the May-2020 rounds: 4.4.224, 4.9.224, 4.14.181, 4.19.119
 * (plus 5.3.18 and 5.4.5, where both landed together). The version ranges
 * below are taken verbatim from wireguard-linux-compat's compat.h
 * detection of the stub member. Vendor kernels (Keenetic -ndm-*) may carry
 * the backport without the matching SUBLEVEL — if the build fails on this,
 * override from the make command line with
 * ccflags-y+=-DAWG_HAVE_IPV6_DST_LOOKUP_FLOW=0|1.
 */
#ifndef AWG_HAVE_IPV6_DST_LOOKUP_FLOW
#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 5, 0) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(5, 5, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(5, 4, 5)) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(5, 4, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(5, 3, 18)) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(4, 20, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(4, 19, 119)) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(4, 15, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(4, 14, 181)) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(4, 10, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(4, 9, 224)) || \
    (LINUX_VERSION_CODE < KERNEL_VERSION(4, 5, 0) && LINUX_VERSION_CODE >= KERNEL_VERSION(4, 4, 224))
#define AWG_HAVE_IPV6_DST_LOOKUP_FLOW 1
#else
#define AWG_HAVE_IPV6_DST_LOOKUP_FLOW 0
#endif
#endif

static struct awg_proxy proxies[AWG_MAX_TUNNELS];
static DEFINE_MUTEX(proxy_mutex);

/*
 * Render an endpoint for logs / /proc list rows: "a.b.c.d:port" for IPv4
 * (byte-identical to the historical "%pI4:%d" output), "[v6...]:port" for
 * IPv6 (%pI6c — compressed lowercase, same shape Go's net.JoinHostPort
 * produces, so the userspace list parser round-trips it).
 */
#define AWG_EP_STRLEN 64
static void awg_endpoint_fmt(const struct awg_endpoint_addr *addr,
			     __be16 port, char *buf, size_t len)
{
	if (addr->family == AF_INET6)
		snprintf(buf, len, "[%pI6c]:%d", &addr->ip6, ntohs(port));
	else
		snprintf(buf, len, "%pI4:%d", &addr->ip4, ntohs(port));
}

/*
 * Dummy net_device for the udp_tunnel_xmit_skb TX path. iptunnel_xmit
 * (ip_tunnel_core.c) unconditionally derefs skb->dev->tstats for stats; a bare
 * alloc_skb has dev==NULL (panic) and a real WAN dev (ppp0/eth) leaves the
 * tstats union unset (corruption). So egress skbs point at this owned dev,
 * which exists ONLY to carry an allocated per-cpu tstats. Never registered.
 */
static struct net_device *awg_xmit_dev;

static void awg_xmit_dev_setup(struct net_device *dev)
{
	/* Unregistered, no xmit/ops — nothing to set up. */
}

int awg_xmit_dev_create(void)
{
	awg_xmit_dev = alloc_netdev(0, "awgproxy", NET_NAME_UNKNOWN,
				    awg_xmit_dev_setup);
	if (!awg_xmit_dev)
		return -ENOMEM;

	awg_xmit_dev->tstats = netdev_alloc_pcpu_stats(struct pcpu_sw_netstats);
	if (!awg_xmit_dev->tstats) {
		free_netdev(awg_xmit_dev);
		awg_xmit_dev = NULL;
		return -ENOMEM;
	}
	return 0;
}

void awg_xmit_dev_destroy(void)
{
	if (!awg_xmit_dev)
		return;
	free_percpu(awg_xmit_dev->tstats);
	free_netdev(awg_xmit_dev);
	awg_xmit_dev = NULL;
}

static inline bool cookie_expired(u64 birthdate_ns)
{
	u64 now = ktime_to_ns(ktime_get_boottime());

	return now < birthdate_ns || now - birthdate_ns > AWG_COOKIE_TTL_NS;
}

/* ---- socket helpers ---- */

/*
 * Create a UDP socket bound to 127.0.0.1:0 (kernel-assigned port).
 * Returns 0 on success, fills *sock and *port.
 */
static int create_listen_socket(struct socket **sock, u16 *port)
{
	struct sockaddr_in addr;
	int addrlen = sizeof(addr);
	int ret;

	ret = sock_create_kern(&init_net, AF_INET, SOCK_DGRAM, IPPROTO_UDP,
			       sock);
	if (ret)
		return ret;

	memset(&addr, 0, sizeof(addr));
	addr.sin_family = AF_INET;
	addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	addr.sin_port = 0; /* auto-assign */

	ret = kernel_bind(*sock, (struct sockaddr *)&addr, sizeof(addr));
	if (ret) {
		sock_release(*sock);
		*sock = NULL;
		return ret;
	}

	/* Read assigned port */
	ret = kernel_getsockname(*sock, (struct sockaddr *)&addr, &addrlen);
	if (ret) {
		sock_release(*sock);
		*sock = NULL;
		return ret;
	}

	*port = ntohs(addr.sin_port);
	return 0;
}

/*
 * Create a UDP socket facing the remote AWG server, in the endpoint's
 * address family (AF_INET or AF_INET6). If bind_iface is non-empty, bind
 * the socket to that network interface via SO_BINDTODEVICE (WAN binding /
 * "connect via").
 */
static int create_remote_socket(struct socket **sock, const awg_config_t *cfg)
{
	const char *bind_iface = cfg->bind_iface;
	int family = cfg->remote_addr.family;
	int ret;

	/* destination is passed per-sendmsg, not via connect — see below */

	ret = sock_create_kern(&init_net, family, SOCK_DGRAM, IPPROTO_UDP,
			       sock);
	if (ret)
		return ret;

	if (family == AF_INET6) {
		/* No mapped-IPv4 ambiguity on this socket: the endpoint is a
		 * genuine IPv6 address and the RX filter compares in6_addr. */
		int on = 1;
		(void)kernel_setsockopt(*sock, SOL_IPV6, IPV6_V6ONLY,
					(char *)&on, sizeof(on));

		/* IPv6 analog of the IPv4 PMTU opt-out below. There is no DF
		 * bit in IPv6 (fragmentation is source-only), but this stops
		 * the socket from honoring cached path-MTU on sends, matching
		 * the reference's skb->ignore_df = 1 behavior for send6. */
		{
			int pmtu = IPV6_PMTUDISC_DONT;
			(void)kernel_setsockopt(*sock, SOL_IPV6,
						IPV6_MTU_DISCOVER,
						(char *)&pmtu, sizeof(pmtu));
		}
	} else {
		/* Disable Path-MTU-Discovery / Don't-Fragment bit on outbound
		 * packets — mirrors amneziawg-linux-kernel-module's
		 * `skb->ignore_df = 1` (src/socket.c). Standard UDP sockets set
		 * DF=1 by default, which makes some middleboxes drop AWG
		 * handshakes (especially with DNS-shaped CPS payloads, where
		 * DF=1 looks like DNS-amplification probes). Reference sends
		 * with DF=0 and works against the same servers we fail on. */
		int pmtu = IP_PMTUDISC_DONT;
		(void)kernel_setsockopt(*sock, IPPROTO_IP, IP_MTU_DISCOVER,
					(char *)&pmtu, sizeof(pmtu));
	}

	/* Bind to specific WAN interface if requested */
	if (bind_iface && bind_iface[0]) {
		ret = kernel_setsockopt(*sock, SOL_SOCKET, SO_BINDTODEVICE,
					bind_iface, strlen(bind_iface) + 1);
		if (ret) {
			pr_err("awg_proxy: SO_BINDTODEVICE(%s) failed: %d\n",
			       bind_iface, ret);
			sock_release(*sock);
			*sock = NULL;
			return ret;
		}
		pr_info("awg_proxy: socket bound to %s\n", bind_iface);
	}

	/* Bind to any local port so recvmsg has a port to listen on.
	 * We intentionally do NOT call kernel_connect() — that triggers a
	 * 0-byte UDP "probe" to the destination on some kernels (visible on
	 * the wire as a malformed first packet from our source, a known
	 * server-side fingerprint for proxy/scanner traffic). Instead, we
	 * pass the destination addr explicitly in every send. */
	if (family == AF_INET6) {
		struct sockaddr_in6 local = {};

		local.sin6_family = AF_INET6;
		/* sin6_addr already zeroed = in6addr_any */
		local.sin6_port = 0;
		ret = kernel_bind(*sock, (struct sockaddr *)&local,
				  sizeof(local));
	} else {
		struct sockaddr_in local = {};

		local.sin_family = AF_INET;
		local.sin_addr.s_addr = htonl(INADDR_ANY);
		local.sin_port = 0;
		ret = kernel_bind(*sock, (struct sockaddr *)&local,
				  sizeof(local));
	}
	if (ret) {
		sock_release(*sock);
		*sock = NULL;
		return ret;
	}

	return 0;
}

/* ---- send helpers ---- */

static int proxy_sendmsg(struct socket *sock, u8 *buf, int len,
			 struct sockaddr_in *addr)
{
	struct msghdr msg = {};
	struct kvec iov = { .iov_base = buf, .iov_len = len };

	if (addr) {
		msg.msg_name = addr;
		msg.msg_namelen = sizeof(*addr);
	}

	return kernel_sendmsg(sock, &msg, &iov, 1, len);
}

/*
 * Send a payload to the AWG server via the udp_tunnel xmit path, mirroring
 * native AWG/WG send4 (src/socket.c). This is the SEND half of the udp_tunnel
 * conversion: a plain kernel_sendmsg egress is what trips Keenetic FASTNAT/PPE
 * into forward-only-offloading the UNREPLIED flow and dropping the handshake
 * reply; udp_tunnel_xmit_skb egress is treated as tunnel traffic, like the
 * immune native AWG. ds = IP TOS/DSCP for this datagram.
 *
 * Called only from c2s_thread (process context). alloc_skb + memcpy happen
 * BEFORE local_bh_disable() — dst_cache uses this_cpu_ptr and the route/cache/
 * xmit block must run with BH disabled (matches send4's rcu_read_lock_bh).
 * Returns 0 on success; on route/alloc failure the skb is freed here.
 */
static int proxy_tunnel_xmit4(struct awg_proxy *p, const u8 *buf, int len,
			      u8 ds)
{
	int headroom = sizeof(struct iphdr) + sizeof(struct udphdr) + MAX_HEADER;
	struct sock *sk = p->remote_sock->sk;
	__be16 sport = inet_sk(sk)->inet_sport;
	struct flowi4 fl = {
		.daddr = p->cfg.remote_addr.ip4,
		.fl4_dport = p->cfg.remote_port,
		.fl4_sport = sport,
		.flowi4_oif = p->bind_oif,
		.flowi4_mark = 0,
		.flowi4_proto = IPPROTO_UDP,
	};
	struct sk_buff *skb;
	struct rtable *rt;

	skb = alloc_skb(len + headroom, GFP_KERNEL);
	if (!skb)
		return -ENOMEM;
	skb_reserve(skb, headroom);
	memcpy(skb_put(skb, len), buf, len);

	local_bh_disable();

	rt = dst_cache_get_ip4(&p->tx_dst_cache, &fl.saddr);
	if (!rt) {
		rt = ip_route_output_flow(&init_net, &fl, sk);
		if (IS_ERR(rt)) {
			long err = PTR_ERR(rt);

			local_bh_enable();
			kfree_skb(skb);
			pr_warn_ratelimited("awg_proxy: no route to %pI4: %ld\n",
					    &p->cfg.remote_addr.ip4, err);
			return (int)err;
		}
		dst_cache_set_ip4(&p->tx_dst_cache, &rt->dst, fl.saddr);
	}

	skb->dev = awg_xmit_dev;
	skb->mark = 0;
	skb->ignore_df = 1;

	/* 4.9: udp_tunnel_xmit_skb takes 12 args. It consumes both rt and skb
	 * on every path (skb_dst_set + ip_local_out) — do NOT ip_rt_put or touch
	 * skb after this call. */
	udp_tunnel_xmit_skb(rt, sk, skb, fl.saddr, p->cfg.remote_addr.ip4, ds,
			    ip4_dst_hoplimit(&rt->dst), 0, sport,
			    p->cfg.remote_port, false, false);

	local_bh_enable();
	return 0;
}

#if IS_ENABLED(CONFIG_IPV6)
/*
 * IPv6 twin of proxy_tunnel_xmit4, mirroring native AWG/WG send6
 * (src/socket.c). Same structure: alloc+copy in process context, then
 * dst_cache lookup / route / udp_tunnel6_xmit_skb under BH-off. Route
 * lookup goes through ipv6_stub so the module keeps loading on kernels
 * where IPv6 is a module (or absent — then family=AF_INET6 never gets
 * this far because sock_create_kern(AF_INET6) already failed).
 *
 * Differences from v4, all matching send6: no DF/frag-off flags (no DF bit
 * in IPv6), flowlabel 0, hoplimit from the route, and the trailing nocheck
 * arg is false because the IPv6 UDP checksum is MANDATORY (RFC 2460) —
 * udp_tunnel6_xmit_skb computes it.
 */
static int proxy_tunnel_xmit6(struct awg_proxy *p, const u8 *buf, int len,
			      u8 ds)
{
	int headroom = sizeof(struct ipv6hdr) + sizeof(struct udphdr) +
		       MAX_HEADER;
	struct sock *sk = p->remote_sock->sk;
	__be16 sport = inet_sk(sk)->inet_sport;
	struct flowi6 fl = {
		.daddr = p->cfg.remote_addr.ip6,
		.fl6_dport = p->cfg.remote_port,
		.fl6_sport = sport,
		.flowi6_oif = p->bind_oif,
		.flowi6_mark = 0,
		.flowi6_proto = IPPROTO_UDP,
	};
	struct sk_buff *skb;
	struct dst_entry *dst;

	skb = alloc_skb(len + headroom, GFP_KERNEL);
	if (!skb)
		return -ENOMEM;
	skb_reserve(skb, headroom);
	memcpy(skb_put(skb, len), buf, len);

	local_bh_disable();

	dst = dst_cache_get_ip6(&p->tx_dst_cache, &fl.saddr);
	if (!dst) {
		long err;

#if AWG_HAVE_IPV6_DST_LOOKUP_FLOW
		dst = ipv6_stub->ipv6_dst_lookup_flow(&init_net, sk, &fl,
						      NULL);
		err = IS_ERR(dst) ? PTR_ERR(dst) : 0;
#else
		err = ipv6_stub->ipv6_dst_lookup(&init_net, sk, &dst, &fl);
#endif
		if (err) {
			local_bh_enable();
			kfree_skb(skb);
			pr_warn_ratelimited("awg_proxy: no route to %pI6c: %ld\n",
					    &p->cfg.remote_addr.ip6, err);
			return (int)err;
		}
		dst_cache_set_ip6(&p->tx_dst_cache, dst, &fl.saddr);
	}

	skb->dev = awg_xmit_dev;
	skb->mark = 0;
	skb->ignore_df = 1;

	/* 4.9: udp_tunnel6_xmit_skb takes 12 args and consumes both dst and
	 * skb on every path — do NOT dst_release or touch skb after this. */
	udp_tunnel6_xmit_skb(dst, sk, skb, skb->dev, &fl.saddr, &fl.daddr,
			     ds, ip6_dst_hoplimit(dst), 0, sport,
			     p->cfg.remote_port, false);

	local_bh_enable();
	return 0;
}
#else /* !CONFIG_IPV6 */
static int proxy_tunnel_xmit6(struct awg_proxy *p, const u8 *buf, int len,
			      u8 ds)
{
	/* Unreachable: awg_proxy_add rejects AF_INET6 endpoints on kernels
	 * built without IPv6. Kept so the dispatcher links unconditionally. */
	return -EAFNOSUPPORT;
}
#endif /* CONFIG_IPV6 */

/* Family dispatcher — every sender goes through here. */
static int proxy_tunnel_xmit(struct awg_proxy *p, const u8 *buf, int len, u8 ds)
{
	if (p->cfg.remote_addr.family == AF_INET6)
		return proxy_tunnel_xmit6(p, buf, len, ds);
	return proxy_tunnel_xmit4(p, buf, len, ds);
}

/*
 * Send CPS packets before handshake init.
 *
 * Counter handling mirrors amneziawg-linux-kernel-module's wg_packet_send_handshake_initiation
 * (src/send.c): the caller (c2s_thread_fn) re-seeds proxy->cps_counter to a
 * fresh random value at the start of each handshake cycle, and we increment
 * it ONLY after a successful socket send — never at generation time.
 *
 * Pre-compute the per-slot counter array first so cps_generate_all can stay
 * pure (no shared-state mutation). Counter advance per packet sent: matches
 * reference's atomic_inc-after-send.
 */
static void send_cps_packets(struct awg_proxy *proxy)
{
	u8 (*bufs)[1500];
	int lens[5];
	u32 counters[5];
	int count, i, slot;

	bufs = kmalloc(5 * 1500, GFP_KERNEL);
	if (!bufs)
		return;

	/* Counters[k] = current counter + k, one per non-null template. */
	for (i = 0; i < 5; i++)
		counters[i] = proxy->cps_counter + i;

	count = cps_generate_all(proxy->cfg.cps, counters, bufs, lens);

	for (slot = 0; slot < count; slot++) {
		int sret;

		if (lens[slot] <= 0)
			continue;
		sret = proxy_tunnel_xmit(proxy, bufs[slot], lens[slot], 0);
		if (sret >= 0)
			proxy->cps_counter++;
		/*
		 * No inter-packet delay: the reference (amneziawg src/send.c)
		 * emits the whole handshake-init cycle (I1-I5, Jc junk, init)
		 * back-to-back with no sleep, so a sub-ms burst is exactly what
		 * native AWG traffic looks like — spacing it out by ~2ms made us
		 * the outlier on the wire, not the crowd. The cold-neighbour
		 * arp_queue (unres_qlen_bytes=64KB, ~30+ small packets) dwarfs
		 * any sane burst, and a rare first-cycle drop is self-healed by
		 * the WG handshake retry.
		 */
	}
	kfree(bufs);
}

/*
 * Send junk packets before handshake init.
 *
 * Each junk datagram gets a freshly randomised IP TOS (DSCP) — mirrors the
 * amneziawg-linux-kernel-module reference (src/send.c:68-69):
 *     get_random_bytes(&ds, 1);
 *     wg_socket_send_buffer_to_peer(peer, buffer, junk_packet_size, ds, 0);
 *
 * Without per-packet DSCP randomisation, every junk UDP datagram from our
 * source goes out with TOS=0, producing a trivially-fingerprintable burst
 * (N back-to-back UDP packets, identical TOS) that distinguishes us from
 * amneziawg-go traffic on the wire.
 */
static void send_junk_packets(struct awg_proxy *proxy)
{
	u8 *junk;
	int sizes[128]; /* jc max */
	int count, i;

	junk = kmalloc(1500, GFP_KERNEL);
	if (!junk)
		return;

	count = generate_junk(&proxy->cfg, junk, sizes, AWG_MAX_JC);
	for (i = 0; i < count; i++) {
		u8 ds;

		if (sizes[i] <= 0 || sizes[i] > 1500)
			continue;
		get_random_bytes(junk, sizes[i]);

		/* Random per-packet IP TOS (DSCP), passed straight to the xmit
		 * helper — mirrors amneziawg src/send.c per-junk DSCP. No more
		 * IP_TOS setsockopt dance (it was racy on the shared socket). */
		get_random_bytes(&ds, 1);
		proxy_tunnel_xmit(proxy, junk, sizes[i], ds);
		/* No inter-packet delay — burst like the reference; see
		 * send_cps_packets() for rationale. */
	}

	kfree(junk);
}

/* ---- worker threads ---- */

/*
 * AWG 3.1 adaptive window: record the largest real datagram seen on the slot
 * (clamped to AWG_UDP_WINDOW_MAX) so handshake trailer sizes track the traffic
 * envelope. Observe pre-trailer sizes only — observing our own trailered
 * handshakes would make the window run away. Racy by design: a slightly stale
 * window only perturbs a cosmetic trailer length.
 */
static inline void udp_window_observe(struct awg_proxy *proxy, int size)
{
	if (size > AWG_UDP_WINDOW_MAX)
		size = AWG_UDP_WINDOW_MAX;
	if (size > atomic_read(&proxy->udp_window))
		atomic_set(&proxy->udp_window, size);
}

/*
 * Client-to-server thread: reads WG packets from listen_sock,
 * transforms to AWG via transform_outbound(), sends to remote_sock.
 *
 * Buffer layout: [headroom][payload...]
 * recvmsg writes at buf + headroom, transform may shift left into headroom.
 *
 * Key behavior from reference:
 *   - Always update client address (not just first packet)
 *   - Single transform_outbound() call handles all message types
 *   - sendJunk flag triggers CPS + junk before the packet
 */
static int c2s_thread_fn(void *data)
{
	struct awg_proxy *proxy = data;
	u8 *raw_buf;
	int headroom = proxy->headroom;
	int bufsize = AWG_BUF_SIZE;

	raw_buf = kmalloc(headroom + bufsize, GFP_KERNEL);
	if (!raw_buf) {
		pr_err("awg_proxy: c2s: failed to allocate buffer\n");
		return -ENOMEM;
	}

	while (!kthread_should_stop()) {
		struct msghdr msg = {};
		struct kvec iov;
		struct sockaddr_in from;
		u8 *payload, *out;
		int n, out_len, sendCps, sendJunk;
		u32 msgType;
		u64 rand_val;
		u8 captured_mac1_old[16];
		bool mac1_capture_pending = false;

		/* Receive from listen socket (WG sends here) */
		payload = raw_buf + headroom;
		memset(&msg, 0, sizeof(msg));
		msg.msg_name = &from;
		msg.msg_namelen = sizeof(from);
		iov.iov_base = payload;
		iov.iov_len = bufsize;

		n = kernel_recvmsg(proxy->listen_sock, &msg, &iov, 1,
				   bufsize, 0);
		switch (awg_classify_recv(n, kthread_should_stop())) {
		case AWG_RECV_BREAK:
			goto out;
		case AWG_RECV_RETRY_SLEEP:
			msleep(10);
			continue;
		case AWG_RECV_RETRY_YIELD:
			cond_resched();
			continue;
		case AWG_RECV_PROCESS:
			break;
		}

		/* Always update client address (reference behavior) */
		spin_lock(&proxy->client_lock);
		if (!proxy->has_client ||
		    memcmp(&proxy->client_addr, &from, sizeof(from)) != 0) {
			memcpy(&proxy->client_addr, &from, sizeof(from));
			if (!proxy->has_client) {
				WRITE_ONCE(proxy->has_client, true);
				spin_unlock(&proxy->client_lock);
				pr_info("awg_proxy: client at 127.0.0.1:%u\n",
					ntohs(from.sin_port));
			} else {
				spin_unlock(&proxy->client_lock);
			}
		} else {
			spin_unlock(&proxy->client_lock);
		}

		if (proxy->has_cookie_key) {
			if (payload[0] == WG_HANDSHAKE_INIT &&
			    n == WG_INIT_SIZE) {
				memcpy(captured_mac1_old, payload + 116, 16);
				mac1_capture_pending = true;
			} else if (payload[0] == WG_HANDSHAKE_RESPONSE &&
				   n == WG_RESP_SIZE) {
				memcpy(captured_mac1_old, payload + 60, 16);
				mac1_capture_pending = true;
			}
		}

		/* Get random value for H range selection */
		get_random_bytes(&rand_val, sizeof(rand_val));

		/* Transform WG -> AWG (handles all message types) */
		out = transform_outbound(raw_buf, headroom, n,
					 &proxy->cfg, rand_val,
					 &out_len, &sendCps, &sendJunk, &msgType);

		if (mac1_capture_pending && proxy->has_cookie_key) {
			int s_prefix = -1;
			int mac1_off = -1;

			if (msgType == WG_HANDSHAKE_INIT) {
				s_prefix = proxy->cfg.s1;
				mac1_off = 116;
			} else if (msgType == WG_HANDSHAKE_RESPONSE) {
				s_prefix = proxy->cfg.s2;
				mac1_off = 60;
			}

			if (s_prefix >= 0 && mac1_off >= 0 &&
			    out_len >= s_prefix + mac1_off + 16) {
				spin_lock(&proxy->mac1_lock);
				memcpy(proxy->last_mac1_old,
				       captured_mac1_old, 16);
				memcpy(proxy->last_mac1_new,
				       out + s_prefix + mac1_off, 16);
				WRITE_ONCE(proxy->have_last_mac1, true);
				spin_unlock(&proxy->mac1_lock);
			}
		}

		/*
		 * Recompute MAC2 if the client already had a cookie. Server
		 * validates MAC2 over the bytes it receives (cookie.c:142-143
		 * in amneziawg-linux-kernel-module), so the client's MAC2
		 * computed over [01...||MAC1_old] is stale after we rewrote
		 * msg_type and recomputed MAC1. Without this, the server stays
		 * stuck on VALID_MAC_BUT_NO_COOKIE under load and keeps
		 * responding with cookie_replies — handshakes loop.
		 */
		if (msgType == WG_HANDSHAKE_INIT ||
		    msgType == WG_HANDSHAKE_RESPONSE) {
			int s_prefix = (msgType == WG_HANDSHAKE_INIT) ?
				proxy->cfg.s1 : proxy->cfg.s2;
			u8 cookie_copy[16];
			bool have_cookie = false;

			spin_lock(&proxy->cookie_lock);
			if (proxy->latest_cookie_valid &&
			    !cookie_expired(proxy->latest_cookie_birthdate_ns)) {
				memcpy(cookie_copy, proxy->latest_cookie, 16);
				have_cookie = true;
			}
			spin_unlock(&proxy->cookie_lock);

			if (have_cookie && out_len >= s_prefix + n)
				recompute_mac2_if_present(out + s_prefix, n,
							  msgType, cookie_copy);
			memzero_explicit(cookie_copy, sizeof(cookie_copy));
		}

		/*
		 * AWG 3.0 header protection: encrypt the WG header LAST, after
		 * MAC1/MAC2 are final — for handshakes HP wraps the whole message
		 * including the MACs, so it must run after they are rewritten. The
		 * nonce is the padding prefix (out[0..12)), so s_prefix >= 12
		 * (enforced at config parse). Transport encrypts only the 16-byte
		 * header. Ingress decrypt lives inside transform_inbound.
		 */
		if (proxy->cfg.hp_key_set &&
		    msgType >= WG_HANDSHAKE_INIT && msgType <= WG_TRANSPORT_DATA) {
			int s_prefix = out_len - n;

			if (s_prefix >= AWG_HP_MIN_PADDING)
				hp_crypt(&proxy->cfg, out, s_prefix, n, msgType);
		}

		/*
		 * AWG 3.1 random trailer: append cleartext bytes to handshake
		 * datagrams so their fixed size stops being a fingerprint. Observe
		 * the pre-trailer size first (transport packets set the envelope),
		 * then draw a trailer for handshakes only — transport padding would
		 * have to live inside the AEAD, which a keyless proxy cannot do.
		 */
		udp_window_observe(proxy, out_len);
		if (proxy->cfg.random_trailers &&
		    (msgType == WG_HANDSHAKE_INIT ||
		     msgType == WG_HANDSHAKE_RESPONSE ||
		     msgType == WG_COOKIE_REPLY)) {
			int tailroom = (raw_buf + headroom + bufsize) - (out + out_len);
			int tlen = awg_trailer_len(atomic_read(&proxy->udp_window),
						   out_len);

			if (tlen > tailroom)
				tlen = tailroom;
			if (tlen > 0) {
				get_random_bytes(out + out_len, tlen);
				out_len += tlen;
			}
		}

		/*
		 * Send I1-I5 CPS packets before the handshake init whenever any
		 * template is configured — independent of Jc, matching the
		 * reference (src/send.c: the ispec loop is unconditional; only
		 * the Jc junk loop is gated by jc && jmax). Re-seed the CPS
		 * counter to a fresh random value at the start of each cycle —
		 * matches `atomic_set(&peer->jp_packet_counter, get_random_u32())`
		 * (src/send.c:45). Without it the counter would grow
		 * monotonically across the tunnel lifetime, a DPI fingerprint in
		 * the <c>-tokens.
		 */
		if (sendCps) {
			get_random_bytes(&proxy->cps_counter,
					 sizeof(proxy->cps_counter));
			send_cps_packets(proxy);
		}
		if (sendJunk)
			send_junk_packets(proxy);

		/* Send transformed packet to remote AWG server via the
		 * udp_tunnel xmit path (see proxy_tunnel_xmit).
		 *
		 * Handshake init/response go out with IP TOS = AWG_HANDSHAKE_DSCP
		 * (passed as the ds arg, no more setsockopt) to mirror
		 * amneziawg-linux-kernel-module: without it some middleboxes
		 * silently drop handshakes; pcap diff confirmed kernel-AWG init
		 * uses TOS=0x88 and gets a response while TOS=0 got none.
		 *
		 * Log ratelimited negative returns to correlate handshake
		 * retries with route/alloc failures on flaky links. */
		{
			u8 ds = (msgType == WG_HANDSHAKE_INIT ||
				 msgType == WG_HANDSHAKE_RESPONSE) ?
					AWG_HANDSHAKE_DSCP : 0;
			int sret = proxy_tunnel_xmit(proxy, out, out_len, ds);

			if (sret < 0) {
				char ep[AWG_EP_STRLEN];

				awg_endpoint_fmt(&proxy->cfg.remote_addr,
						 proxy->cfg.remote_port,
						 ep, sizeof(ep));
				pr_warn_ratelimited("awg_proxy: send to %s failed: %d\n",
						    ep, sret);
			} else {
				atomic_inc(&proxy->tx_packets);
				atomic64_add(out_len, &proxy->tx_bytes);
			}
		}
	}

out:
	kfree(raw_buf);
	return 0;
}

/*
 * UDP-encap receive callback — runs in softirq from the udp_rcv path.
 *
 * Mirrors native AWG/WG wg_receive (src/socket.c: encap_rcv=wg_receive). The
 * server->client flow terminating on a udp_tunnel/encap socket is what keeps
 * it out of Keenetic's FASTNAT/PPE offload: a plain recvmsg socket gets a
 * forward-only offload entry latched while the conntrack is still [UNREPLIED]
 * (our I1+junk handshake burst trips the offload heuristic before the reply),
 * and the server's handshake response is then dropped before delivery.
 *
 * Softirq context: no sleeping, no GFP_KERNEL. Strip the UDP header (matches
 * native prepare_skb_header's skb_pull of sizeof(udphdr)), enqueue, and wake
 * s2c_thread — all transform/cookie/sendmsg work stays in process context.
 * Returns 0: skb consumed.
 */
static int awg_encap_rcv(struct sock *sk, struct sk_buff *skb)
{
	struct awg_proxy *proxy = rcu_dereference_sk_user_data(sk);
	const struct udphdr *uh;

	if (unlikely(!proxy) || !READ_ONCE(proxy->active))
		goto drop;

	if (unlikely(!pskb_may_pull(skb, sizeof(struct udphdr))))
		goto drop;

	/* Accept only the configured server — see awg_src_matches
	 * (endpoint.h) for the rationale. The remote socket is per-family
	 * (and IPV6_V6ONLY on v6), so an AF_INET slot only ever sees IPv4
	 * headers here and an AF_INET6 slot only IPv6 ones — branch on the
	 * configured family and read the matching header. */
	uh = (const struct udphdr *)skb->data;
	if (proxy->cfg.remote_addr.family == AF_INET6) {
#if IS_ENABLED(CONFIG_IPV6)
		if (!awg_src_matches(&proxy->cfg.remote_addr,
				     proxy->cfg.remote_port, AF_INET6,
				     &ipv6_hdr(skb)->saddr, uh->source))
			goto drop;
#else
		goto drop;	/* v6 slot cannot exist on a no-IPv6 kernel */
#endif
	} else {
		if (!awg_src_matches(&proxy->cfg.remote_addr,
				     proxy->cfg.remote_port, AF_INET,
				     &ip_hdr(skb)->saddr, uh->source))
			goto drop;
	}

	__skb_pull(skb, sizeof(struct udphdr));

	/* Bound the backlog — a stalled drain must not OOM. AWG tolerates loss:
	 * WG retransmits handshakes, transport data is best-effort. */
	if (skb_queue_len(&proxy->rx_queue) >= AWG_RX_QUEUE_MAX)
		goto drop;

	skb_queue_tail(&proxy->rx_queue, skb);  /* own lock — softirq-safe */
	wake_up(&proxy->rx_wait);
	return 0;
drop:
	kfree_skb(skb);
	return 0;
}

/*
 * Server-to-client thread: drains rx_queue (fed by awg_encap_rcv),
 * transforms AWG->WG via transform_inbound(), sends to listen_sock -> WG.
 *
 * transform_inbound() returns NULL for junk/CPS packets (drop silently).
 */
static int s2c_thread_fn(void *data)
{
	struct awg_proxy *proxy = data;
	u8 *buf;

	buf = kmalloc(AWG_BUF_SIZE, GFP_KERNEL);
	if (!buf) {
		pr_err("awg_proxy: s2c: failed to allocate buffer\n");
		return -ENOMEM;
	}

	while (!kthread_should_stop()) {
		struct sk_buff *skb;
		u8 *out;
		int n, out_len;

		wait_event_interruptible(proxy->rx_wait,
			!skb_queue_empty(&proxy->rx_queue) ||
			kthread_should_stop());
		if (kthread_should_stop())
			goto out;

		skb = skb_dequeue(&proxy->rx_queue);
		if (!skb)
			continue;

		n = skb->len;
		if (n <= 0 || n > AWG_BUF_SIZE ||
		    skb_copy_bits(skb, 0, buf, n) < 0) {
			kfree_skb(skb);
			continue;
		}
		kfree_skb(skb);

		atomic_inc(&proxy->rx_packets);
		atomic64_add(n, &proxy->rx_bytes);
		/* Feed the adaptive window from real inbound datagram sizes. */
		udp_window_observe(proxy, n);

		/* Transform inbound AWG -> WG */
		out = transform_inbound(buf, n, &proxy->cfg, &out_len);
		if (out && out_len == WG_COOKIE_SIZE &&
		    out[0] == WG_COOKIE_REPLY &&
		    proxy->has_cookie_key &&
		    READ_ONCE(proxy->have_last_mac1)) {
			u8 mac1_old[16], mac1_new[16];
			u8 cookie_buf[32];
			int ret;

			spin_lock(&proxy->mac1_lock);
			memcpy(mac1_old, proxy->last_mac1_old, 16);
			memcpy(mac1_new, proxy->last_mac1_new, 16);
			spin_unlock(&proxy->mac1_lock);

			memcpy(cookie_buf, out + 32, 32);
			ret = awg_xchacha20p1305_decrypt(
				proxy->cookie_aead_key,
				out + 8, mac1_new, 16, cookie_buf, 32);
			if (!ret) {
				/*
				 * Stash the decrypted 16-byte cookie for future
				 * MAC2 recompute on outbound handshakes. The
				 * vanilla-WG client will receive the same
				 * cookie after we re-encrypt below, so MAC2
				 * keys on both ends stay in sync until TTL.
				 */
				spin_lock(&proxy->cookie_lock);
				memcpy(proxy->latest_cookie, cookie_buf, 16);
				proxy->latest_cookie_birthdate_ns =
					ktime_to_ns(ktime_get_boottime());
				proxy->latest_cookie_valid = true;
				spin_unlock(&proxy->cookie_lock);

				/*
				 * Same (key, nonce) reused with a different AAD
				 * to satisfy the vanilla-WG client. Plaintext is
				 * the same cookie, so ciphertext is identical
				 * (ChaCha20 is deterministic) and only Poly1305
				 * tag changes. No new leak — proxy is in the
				 * same trust domain as the local client.
				 */
				ret = awg_xchacha20p1305_encrypt(
					proxy->cookie_aead_key,
					out + 8, mac1_old, 16, cookie_buf, 16);
				if (!ret)
					memcpy(out + 32, cookie_buf, 32);
				else
					pr_warn_ratelimited("awg_proxy: cookie_reply re-encrypt failed: %d\n",
							    ret);
			} else {
				pr_warn_ratelimited("awg_proxy: cookie_reply decrypt failed: %d (forwarded as-is)\n",
						    ret);
			}
		}
		if (!out)
			continue; /* junk/CPS — drop silently */

		/* Forward to WG client */
		if (READ_ONCE(proxy->has_client)) {
			struct sockaddr_in addr;

			spin_lock(&proxy->client_lock);
			addr = proxy->client_addr;
			spin_unlock(&proxy->client_lock);
			proxy_sendmsg(proxy->listen_sock, out, out_len,
				      &addr);
		}
	}

out:
	kfree(buf);
	return 0;
}

/* ---- proxy lifecycle ---- */

/* Compute headroom needed: max(s1, s2, s3, s4), minimum 64 */
static int compute_headroom(const awg_config_t *cfg)
{
	int h = cfg->s1;

	if (cfg->s2 > h)
		h = cfg->s2;
	if (cfg->s3 > h)
		h = cfg->s3;
	if (cfg->s4 > h)
		h = cfg->s4;
	if (h < 64)
		h = 64;
	return h;
}

/* Forward declaration — defined in tunnel.c */
int awg_config_parse(const char *config_line, awg_config_t *cfg);
void awg_config_free(awg_config_t *cfg);

int awg_proxy_add(const char *config_line)
{
	struct awg_proxy *p = NULL;
	awg_config_t tmp;
	int i, ret;

	/* Parse config into temporary struct */
	ret = awg_config_parse(config_line, &tmp);
	if (ret)
		return ret;

	/* Fail v6 endpoints loudly on kernels built without IPv6 instead of
	 * a cryptic sock_create_kern error later. */
	if (tmp.remote_addr.family == AF_INET6 &&
	    !IS_ENABLED(CONFIG_IPV6)) {
		pr_warn("awg_proxy: IPv6 endpoint but kernel lacks CONFIG_IPV6\n");
		awg_config_free(&tmp);
		return -EAFNOSUPPORT;
	}

	mutex_lock(&proxy_mutex);

	/* Check duplicate */
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		if (proxies[i].active &&
		    awg_endpoint_addr_equal(&proxies[i].cfg.remote_addr,
					    &tmp.remote_addr) &&
		    proxies[i].cfg.remote_port == tmp.remote_port) {
			ret = -EEXIST;
			goto out_free;
		}
	}

	/* Find free slot */
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		if (!proxies[i].active) {
			p = &proxies[i];
			break;
		}
	}
	if (!p) {
		ret = -ENOSPC;
		goto out_free;
	}

	/* Initialize proxy.
	 * Move config from tmp to p. After memcpy, CPS pointers are
	 * shared; zero tmp's so only p->cfg owns them. */
	memset(p, 0, sizeof(*p));
	memcpy(&p->cfg, &tmp, sizeof(tmp));
	memset(tmp.cps, 0, sizeof(tmp.cps)); /* prevent double-free */
	spin_lock_init(&p->client_lock);
	spin_lock_init(&p->mac1_lock);
	spin_lock_init(&p->cookie_lock);
	skb_queue_head_init(&p->rx_queue);
	init_waitqueue_head(&p->rx_wait);

	ret = dst_cache_init(&p->tx_dst_cache, GFP_KERNEL);
	if (ret) {
		pr_err("awg_proxy: dst_cache_init failed: %d\n", ret);
		goto out_cleanup;
	}

	/* Resolve WAN egress ifindex for the udp_tunnel_xmit route lookup.
	 * Cached once; a WAN flap that changes ifindex needs a tunnel restart
	 * (nwg does this on WAN change). 0 = let routing pick the default. */
	p->bind_oif = 0;
	if (p->cfg.bind_iface[0]) {
		struct net_device *dev = dev_get_by_name(&init_net,
							 p->cfg.bind_iface);
		if (dev) {
			p->bind_oif = dev->ifindex;
			dev_put(dev);
		}
	}

	p->cps_counter = 0;
	p->have_last_mac1 = false;
	p->latest_cookie_valid = false;
	p->has_cookie_key = false;
	p->headroom = compute_headroom(&p->cfg);

	if (p->cfg.has_server_pub) {
		compute_cookie_key(p->cfg.server_pub, p->cookie_aead_key);
		p->has_cookie_key = true;
	}
	atomic64_set(&p->rx_bytes, 0);
	atomic64_set(&p->tx_bytes, 0);
	atomic_set(&p->rx_packets, 0);
	atomic_set(&p->tx_packets, 0);
	atomic_set(&p->udp_window, AWG_DEFAULT_UDP_WINDOW);

	/* Create listen socket (127.0.0.1:auto) */
	ret = create_listen_socket(&p->listen_sock, &p->listen_port);
	if (ret) {
		pr_err("awg_proxy: failed to create listen socket: %d\n", ret);
		goto out_cleanup;
	}

	/* Create remote socket (facing the AWG server, per-family) */
	ret = create_remote_socket(&p->remote_sock, &p->cfg);
	if (ret) {
		pr_err("awg_proxy: failed to create remote socket: %d\n", ret);
		goto out_cleanup;
	}

	/* Turn remote_sock into a UDP-encap socket: server replies now go to
	 * awg_encap_rcv (softirq) instead of the recv queue. This is what keeps
	 * the flow out of Keenetic FASTNAT/PPE offload — see awg_encap_rcv. */
	{
		struct udp_tunnel_sock_cfg tcfg = {
			.sk_user_data = p,
			.encap_type   = 1,
			.encap_rcv    = awg_encap_rcv,
		};
		setup_udp_tunnel_sock(&init_net, p->remote_sock, &tcfg);
	}

	/*
	 * Previously we sent a 0-byte UDP "probe" here to pre-warm the
	 * ARP/neighbour cache so the first handshake burst wouldn't be
	 * starved by arp_queue overflow. That probe turned out to be a
	 * server-side fingerprint: any DPI/WAF flags a 0-byte UDP datagram
	 * as the very first packet from a source as malformed/scanner
	 * traffic, then accumulates that flag toward eventual blacklist.
	 *
	 * Removed in v1.1.6. The handshake-cycle packets now go out as a
	 * back-to-back burst (matching the reference, which never spaces
	 * them). The shared WAN-gateway neighbour is virtually always warm;
	 * even cold, its arp_queue (unres_qlen_bytes=64KB, ~30+ small
	 * packets) dwarfs any sane burst, and a rare first-cycle drop is
	 * self-healed by the WG handshake retry.
	 */

	p->active = true;

	/* Start worker threads. Thread names keep the historical
	 * "%pI4"-based comm for IPv4 (byte-identical, TASK_COMM_LEN
	 * truncation applies as before); IPv6 uses the compressed %pI6c. */
	if (p->cfg.remote_addr.family == AF_INET6)
		p->c2s_thread = kthread_run(c2s_thread_fn, p, "awg_c2s_%pI6c",
					    &p->cfg.remote_addr.ip6);
	else
		p->c2s_thread = kthread_run(c2s_thread_fn, p, "awg_c2s_%pI4",
					    &p->cfg.remote_addr.ip4);
	if (IS_ERR(p->c2s_thread)) {
		ret = PTR_ERR(p->c2s_thread);
		p->c2s_thread = NULL;
		pr_err("awg_proxy: failed to start c2s thread: %d\n", ret);
		goto out_cleanup;
	}

	if (p->cfg.remote_addr.family == AF_INET6)
		p->s2c_thread = kthread_run(s2c_thread_fn, p, "awg_s2c_%pI6c",
					    &p->cfg.remote_addr.ip6);
	else
		p->s2c_thread = kthread_run(s2c_thread_fn, p, "awg_s2c_%pI4",
					    &p->cfg.remote_addr.ip4);
	if (IS_ERR(p->s2c_thread)) {
		ret = PTR_ERR(p->s2c_thread);
		p->s2c_thread = NULL;
		pr_err("awg_proxy: failed to start s2c thread: %d\n", ret);
		goto out_cleanup;
	}

	{
		char ep[AWG_EP_STRLEN];

		awg_endpoint_fmt(&p->cfg.remote_addr, p->cfg.remote_port,
				 ep, sizeof(ep));
		pr_info("awg_proxy: added %s -> 127.0.0.1:%u (headroom=%d)\n",
			ep, p->listen_port, p->headroom);
	}

	mutex_unlock(&proxy_mutex);
	return 0;

out_cleanup:
	/* Stop encap dispatch before teardown (encap may already be enabled).
	 * Same RCU discipline as proxy_stop: wait out in-flight callbacks
	 * before the queue purge / slot reuse below. */
	if (p->remote_sock && p->remote_sock->sk) {
		struct sock *sk = p->remote_sock->sk;

		udp_sk(sk)->encap_type = 0;
		WRITE_ONCE(udp_sk(sk)->encap_rcv, NULL);
		rcu_assign_sk_user_data(sk, NULL);
		synchronize_net();
	}
	/* Shutdown sockets first to unblock threads in kernel_recvmsg */
	if (p->listen_sock)
		kernel_sock_shutdown(p->listen_sock, SHUT_RDWR);
	if (p->remote_sock)
		kernel_sock_shutdown(p->remote_sock, SHUT_RDWR);
	/* Now safe to stop threads */
	if (p->c2s_thread) {
		kthread_stop(p->c2s_thread);
		p->c2s_thread = NULL;
	}
	if (p->s2c_thread) {
		kthread_stop(p->s2c_thread);
		p->s2c_thread = NULL;
	}
	/* Release sockets after threads are done */
	if (p->listen_sock) {
		sock_release(p->listen_sock);
		p->listen_sock = NULL;
	}
	if (p->remote_sock) {
		sock_release(p->remote_sock);
		p->remote_sock = NULL;
	}
	skb_queue_purge(&p->rx_queue);
	dst_cache_destroy(&p->tx_dst_cache);  /* NULL-guarded if init never ran */
	memzero_explicit(p->cookie_aead_key,
			 sizeof(p->cookie_aead_key));
	memzero_explicit(p->latest_cookie, sizeof(p->latest_cookie));
	p->latest_cookie_valid = false;
	p->has_cookie_key = false;
	p->active = false;
	awg_config_free(&p->cfg);
out_free:
	if (!p || !p->active)
		awg_config_free(&tmp);
	mutex_unlock(&proxy_mutex);
	return ret;
}

/*
 * Stop a proxy: signal threads to stop, close sockets (unblocks recvmsg),
 * wait for thread exit, free resources.
 */
static void proxy_stop(struct awg_proxy *p)
{
	p->active = false;

	/* Stop encap dispatch first: with encap_type cleared udp_rcv no longer
	 * calls awg_encap_rcv, so no new skbs get queued during teardown.
	 *
	 * The plain stores are not enough on SMP: udp_queue_rcv_skb on another
	 * CPU may have read encap_rcv before the store and still be executing
	 * the callback (the RX path holds only the RCU read lock — since
	 * SOCK_RCU_FREE sockets there is no refcount pinning it). Clear
	 * sk_user_data and synchronize_net() so every in-flight callback has
	 * finished before we purge rx_queue below and before this slot can be
	 * memset by a subsequent awg_proxy_add — otherwise a late
	 * skb_queue_tail runs on a reinitialized spinlock (list corruption) or
	 * leaks the skb past the purge. Mirrors native WG wg_socket_reinit. */
	if (p->remote_sock && p->remote_sock->sk) {
		struct sock *sk = p->remote_sock->sk;

		udp_sk(sk)->encap_type = 0;
		WRITE_ONCE(udp_sk(sk)->encap_rcv, NULL);
		rcu_assign_sk_user_data(sk, NULL);
		synchronize_net();
	}

	/* Closing sockets unblocks kernel_recvmsg in c2s; kthread_stop wakes
	 * s2c out of wait_event (condition includes kthread_should_stop). */
	if (p->listen_sock)
		kernel_sock_shutdown(p->listen_sock, SHUT_RDWR);
	if (p->remote_sock)
		kernel_sock_shutdown(p->remote_sock, SHUT_RDWR);

	if (p->c2s_thread) {
		kthread_stop(p->c2s_thread);
		p->c2s_thread = NULL;
	}
	if (p->s2c_thread) {
		kthread_stop(p->s2c_thread);
		p->s2c_thread = NULL;
	}

	if (p->listen_sock) {
		sock_release(p->listen_sock);
		p->listen_sock = NULL;
	}
	if (p->remote_sock) {
		sock_release(p->remote_sock);
		p->remote_sock = NULL;
	}

	/* Free any skbs the encap callback left queued. */
	skb_queue_purge(&p->rx_queue);

	/* Safe now: c2s_thread (sole tx_dst_cache user) is stopped. */
	dst_cache_destroy(&p->tx_dst_cache);

	memzero_explicit(p->cookie_aead_key,
			 sizeof(p->cookie_aead_key));
	memzero_explicit(p->latest_cookie, sizeof(p->latest_cookie));
	p->latest_cookie_valid = false;
	p->has_cookie_key = false;

	awg_config_free(&p->cfg);
}

int awg_proxy_del(const struct awg_endpoint_addr *addr, __be16 port)
{
	int i, ret = -ENOENT;

	mutex_lock(&proxy_mutex);
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		char ep[AWG_EP_STRLEN];

		if (!proxies[i].active)
			continue;
		if (!awg_endpoint_addr_equal(&proxies[i].cfg.remote_addr,
					     addr) ||
		    proxies[i].cfg.remote_port != port)
			continue;

		awg_endpoint_fmt(addr, port, ep, sizeof(ep));
		pr_info("awg_proxy: removing %s\n", ep);
		proxy_stop(&proxies[i]);
		ret = 0;
		break;
	}
	mutex_unlock(&proxy_mutex);
	return ret;
}

void awg_proxy_cleanup(void)
{
	int i;

	mutex_lock(&proxy_mutex);
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		if (proxies[i].active)
			proxy_stop(&proxies[i]);
	}
	mutex_unlock(&proxy_mutex);
}

/*
 * Format proxy list for procfs read.
 * Output: "ENDPOINT listen=127.0.0.1:PORT rx=BYTES tx=BYTES rx_pkt=N tx_pkt=N\n"
 * ENDPOINT is "a.b.c.d:port" for IPv4 rows (unchanged) and "[v6...]:port"
 * (%pI6c, RFC 5952 compressed) for IPv6 rows — the same shape userspace
 * writes to /proc add/del, so readers can match rows by string prefix.
 */
int awg_proxy_list(char *buf, int buflen)
{
	int i, len = 0;

	mutex_lock(&proxy_mutex);
	/* 192-byte slack: a worst-case IPv6 row (bracketed full-form address
	 * plus grown 64-bit byte counters) is longer than the old 128. */
	for (i = 0; i < AWG_MAX_TUNNELS && len < buflen - 192; i++) {
		struct awg_proxy *p = &proxies[i];
		char ep[AWG_EP_STRLEN];

		if (!p->active)
			continue;

		awg_endpoint_fmt(&p->cfg.remote_addr, p->cfg.remote_port,
				 ep, sizeof(ep));
		len += snprintf(buf + len, buflen - len,
			"%s listen=127.0.0.1:%u "
			"rx=%lld tx=%lld rx_pkt=%d tx_pkt=%d\n",
			ep,
			p->listen_port,
			(long long)atomic64_read(&p->rx_bytes),
			(long long)atomic64_read(&p->tx_bytes),
			atomic_read(&p->rx_packets),
			atomic_read(&p->tx_packets));
	}
	mutex_unlock(&proxy_mutex);

	if (len == 0)
		len = snprintf(buf, buflen, "(no active proxies)\n");
	return len;
}
