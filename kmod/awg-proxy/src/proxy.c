// SPDX-License-Identifier: GPL-2.0
/*
 * AWG Proxy - Kernel UDP proxy for WG<->AWG transformation.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 * Adapted: userspace sockets → kernel sockets, pthreads → kthreads,
 *          batch I/O → single-packet, fastrand → get_random_bytes.
 *
 * Each proxy instance creates two kernel UDP sockets and two threads:
 *   listen_sock  - binds to 127.0.0.1:auto, receives from WG
 *   remote_sock  - connected to AWG server, sends/receives AWG packets
 *   c2s_thread   - WG->AWG: recvmsg(listen) -> transform -> sendmsg(remote)
 *   s2c_thread   - AWG->WG: recvmsg(remote) -> transform -> sendmsg(listen)
 */

#include <linux/kernel.h>
#include <linux/slab.h>
#include <linux/kthread.h>
#include <linux/mutex.h>
#include <linux/net.h>
#include <linux/in.h>
#include <linux/socket.h>
#include <linux/random.h>
#include <linux/delay.h>
#include <linux/ktime.h>
#include <net/sock.h>

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

static struct awg_proxy proxies[AWG_MAX_TUNNELS];
static DEFINE_MUTEX(proxy_mutex);

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
 * Create a UDP socket connected to the remote AWG server.
 * If bind_iface is non-empty, bind the socket to that network interface
 * via SO_BINDTODEVICE before connecting (WAN binding / "connect via").
 */
static int create_remote_socket(struct socket **sock, __be32 ip, __be16 port,
				const char *bind_iface)
{
	int ret;

	(void)ip; (void)port;  /* destination passed per-sendmsg, not via connect */

	ret = sock_create_kern(&init_net, AF_INET, SOCK_DGRAM, IPPROTO_UDP,
			       sock);
	if (ret)
		return ret;

	/* Disable Path-MTU-Discovery / Don't-Fragment bit on outbound packets
	 * — mirrors amneziawg-linux-kernel-module's `skb->ignore_df = 1`
	 * (src/socket.c). Standard UDP sockets set DF=1 by default, which makes
	 * some middleboxes drop AWG handshakes (especially with DNS-shaped CPS
	 * payloads, where DF=1 looks like DNS-amplification probes). Reference
	 * sends with DF=0 and works against the same servers we fail on. */
	{
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
	 * pass the destination addr explicitly in every sendmsg below. */
	{
		struct sockaddr_in local = {};
		local.sin_family = AF_INET;
		local.sin_addr.s_addr = htonl(INADDR_ANY);
		local.sin_port = 0;
		ret = kernel_bind(*sock, (struct sockaddr *)&local,
				  sizeof(local));
		if (ret) {
			sock_release(*sock);
			*sock = NULL;
			return ret;
		}
	}

	return 0;
}

/* Fill a sockaddr_in from the proxy's stored server endpoint. */
static inline void proxy_server_addr(struct sockaddr_in *out,
				     const struct awg_proxy *proxy)
{
	memset(out, 0, sizeof(*out));
	out->sin_family = AF_INET;
	out->sin_addr.s_addr = proxy->cfg.remote_ip;
	out->sin_port = proxy->cfg.remote_port;
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
	struct sockaddr_in dst;
	int count, i, slot;

	bufs = kmalloc(5 * 1500, GFP_KERNEL);
	if (!bufs)
		return;

	proxy_server_addr(&dst, proxy);

	/* Counters[k] = current counter + k, one per non-null template. */
	for (i = 0; i < 5; i++)
		counters[i] = proxy->cps_counter + i;

	count = cps_generate_all(proxy->cfg.cps, counters, bufs, lens);

	for (slot = 0; slot < count; slot++) {
		int sret;

		if (lens[slot] <= 0)
			continue;
		sret = proxy_sendmsg(proxy->remote_sock, bufs[slot], lens[slot],
				     &dst);
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
	struct sockaddr_in dst;
	int count, i;

	junk = kmalloc(1500, GFP_KERNEL);
	if (!junk)
		return;

	proxy_server_addr(&dst, proxy);

	count = generate_junk(&proxy->cfg, junk, sizes, AWG_MAX_JC);
	for (i = 0; i < count; i++) {
		u8 ds;
		int tos;

		if (sizes[i] <= 0 || sizes[i] > 1500)
			continue;
		get_random_bytes(junk, sizes[i]);

		/* Random per-packet IP TOS. setsockopt expects an int. */
		get_random_bytes(&ds, 1);
		tos = ds;
		(void)kernel_setsockopt(proxy->remote_sock, IPPROTO_IP, IP_TOS,
					(char *)&tos, sizeof(tos));

		proxy_sendmsg(proxy->remote_sock, junk, sizes[i], &dst);
		/* No inter-packet delay — burst like the reference; see
		 * send_cps_packets() for rationale. */
	}

	/* Restore default TOS=0 so the subsequent handshake init / steady-state
	 * traffic doesn't inherit the last junk DSCP value. */
	{
		int zero_tos = 0;

		(void)kernel_setsockopt(proxy->remote_sock, IPPROTO_IP, IP_TOS,
					(char *)&zero_tos, sizeof(zero_tos));
	}

	kfree(junk);
}

/* ---- worker threads ---- */

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

		/* Send transformed packet to remote AWG server.
		 *
		 * For handshake init/response, set IP TOS = AWG_HANDSHAKE_DSCP
		 * to mirror amneziawg-linux-kernel-module: without this, some
		 * middleboxes on the path to the AWG server silently drop
		 * handshake packets, leaving local WG retrying forever while
		 * the server side stays mute. Confirmed empirically by pcap
		 * diff: kernel-AWG init goes out with TOS=0x88 and gets a
		 * response; our previous TOS=0 sends got no reply at all.
		 *
		 * Capture and log negative returns so we can confirm in the
		 * field whether handshake retries correlate with -ENOBUFS
		 * (ARP queue overflow) or transient errors. The log is
		 * ratelimited to keep dmesg usable on flaky links. */
		{
			int sret;
			int is_handshake = (msgType == WG_HANDSHAKE_INIT ||
					    msgType == WG_HANDSHAKE_RESPONSE);
			int tos;
			struct sockaddr_in dst;

			proxy_server_addr(&dst, proxy);

			if (is_handshake) {
				tos = AWG_HANDSHAKE_DSCP;
				(void)kernel_setsockopt(proxy->remote_sock,
							IPPROTO_IP, IP_TOS,
							(char *)&tos,
							sizeof(tos));
			}

			sret = proxy_sendmsg(proxy->remote_sock, out,
					     out_len, &dst);

			if (is_handshake) {
				tos = 0;
				(void)kernel_setsockopt(proxy->remote_sock,
							IPPROTO_IP, IP_TOS,
							(char *)&tos,
							sizeof(tos));
			}

			if (sret < 0) {
				pr_warn_ratelimited("awg_proxy: send to %pI4:%d failed: %d\n",
						    &proxy->cfg.remote_ip,
						    ntohs(proxy->cfg.remote_port),
						    sret);
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
 * Server-to-client thread: reads AWG packets from remote_sock,
 * transforms to WG via transform_inbound(), sends to listen_sock -> WG.
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
		struct msghdr msg = {};
		struct kvec iov;
		u8 *out;
		int n, out_len;

		iov.iov_base = buf;
		iov.iov_len = AWG_BUF_SIZE;

		n = kernel_recvmsg(proxy->remote_sock, &msg, &iov, 1,
				   AWG_BUF_SIZE, 0);
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

		atomic_inc(&proxy->rx_packets);
		atomic64_add(n, &proxy->rx_bytes);

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

	mutex_lock(&proxy_mutex);

	/* Check duplicate */
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		if (proxies[i].active &&
		    proxies[i].cfg.remote_ip == tmp.remote_ip &&
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

	/* Create listen socket (127.0.0.1:auto) */
	ret = create_listen_socket(&p->listen_sock, &p->listen_port);
	if (ret) {
		pr_err("awg_proxy: failed to create listen socket: %d\n", ret);
		goto out_cleanup;
	}

	/* Create remote socket (connected to AWG server) */
	ret = create_remote_socket(&p->remote_sock, p->cfg.remote_ip,
				   p->cfg.remote_port, p->cfg.bind_iface);
	if (ret) {
		pr_err("awg_proxy: failed to create remote socket: %d\n", ret);
		goto out_cleanup;
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

	/* Start worker threads */
	p->c2s_thread = kthread_run(c2s_thread_fn, p,
				    "awg_c2s_%pI4", &p->cfg.remote_ip);
	if (IS_ERR(p->c2s_thread)) {
		ret = PTR_ERR(p->c2s_thread);
		p->c2s_thread = NULL;
		pr_err("awg_proxy: failed to start c2s thread: %d\n", ret);
		goto out_cleanup;
	}

	p->s2c_thread = kthread_run(s2c_thread_fn, p,
				    "awg_s2c_%pI4", &p->cfg.remote_ip);
	if (IS_ERR(p->s2c_thread)) {
		ret = PTR_ERR(p->s2c_thread);
		p->s2c_thread = NULL;
		pr_err("awg_proxy: failed to start s2c thread: %d\n", ret);
		goto out_cleanup;
	}

	pr_info("awg_proxy: added %pI4:%d -> 127.0.0.1:%u (headroom=%d)\n",
		&p->cfg.remote_ip, ntohs(p->cfg.remote_port),
		p->listen_port, p->headroom);

	mutex_unlock(&proxy_mutex);
	return 0;

out_cleanup:
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

	/* Closing sockets unblocks kernel_recvmsg in the threads */
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
	memzero_explicit(p->cookie_aead_key,
			 sizeof(p->cookie_aead_key));
	memzero_explicit(p->latest_cookie, sizeof(p->latest_cookie));
	p->latest_cookie_valid = false;
	p->has_cookie_key = false;

	awg_config_free(&p->cfg);
}

int awg_proxy_del(__be32 ip, __be16 port)
{
	int i, ret = -ENOENT;

	mutex_lock(&proxy_mutex);
	for (i = 0; i < AWG_MAX_TUNNELS; i++) {
		if (!proxies[i].active)
			continue;
		if (proxies[i].cfg.remote_ip != ip ||
		    proxies[i].cfg.remote_port != port)
			continue;

		pr_info("awg_proxy: removing %pI4:%d\n", &ip, ntohs(port));
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
 * Output: "REMOTE_IP:REMOTE_PORT listen=127.0.0.1:PORT rx=BYTES tx=BYTES rx_pkt=N tx_pkt=N\n"
 */
int awg_proxy_list(char *buf, int buflen)
{
	int i, len = 0;

	mutex_lock(&proxy_mutex);
	for (i = 0; i < AWG_MAX_TUNNELS && len < buflen - 128; i++) {
		struct awg_proxy *p = &proxies[i];

		if (!p->active)
			continue;

		len += snprintf(buf + len, buflen - len,
			"%pI4:%d listen=127.0.0.1:%u "
			"rx=%lld tx=%lld rx_pkt=%d tx_pkt=%d\n",
			&p->cfg.remote_ip,
			ntohs(p->cfg.remote_port),
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
