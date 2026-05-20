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
#include <net/sock.h>

#include "proxy.h"
#include "transform.h"
#include "cps.h"
#include "cookie.h"
#include "blake2s.h"

static struct awg_proxy proxies[AWG_MAX_TUNNELS];
static DEFINE_MUTEX(proxy_mutex);

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
		/* Inter-packet jitter to match reference's natural workqueue
		 * scheduling cadence. Without this, our kthread blasts all
		 * handshake-cycle packets back-to-back in <1ms — a server-side
		 * fingerprint (sub-millisecond burst of 4+ UDP packets from one
		 * source). It also gives Linux's IP-ID hash bucket a chance to
		 * advance with `delta = prandom_u32_max(now - old)`, avoiding
		 * a perfect +1 ID sequence that screams "bot/proxy" to any WAF. */
		usleep_range(1500, 2500);
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
		/* Inter-packet jitter — see send_cps_packets() for rationale. */
		usleep_range(1500, 2500);
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
		int n, out_len, sendJunk;
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
		if (n < 0) {
			if (n == -ERESTARTSYS || kthread_should_stop())
				break;
			msleep(10);
			continue;
		}
		if (n < 4)
			continue;

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

		/*
		 * Cookie-reply AAD prep: capture vanilla WG's MAC1 BEFORE
		 * transform substitutes it (sits at payload[116..132] for init,
		 * payload[60..76] for response). We commit the (old, new) pair
		 * atomically AFTER transform so an s2c reader can never observe
		 * a mismatched pair from two different handshake cycles.
		 *
		 * Only relevant when has_server_pub (i.e. proxy will recompute
		 * MAC1).
		 */
		if (proxy->cookie_aead) {
			u8 first_byte = payload[0];
			if (first_byte == WG_HANDSHAKE_INIT &&
			    n == WG_INIT_SIZE) {
				memcpy(captured_mac1_old, payload + 116, 16);
				mac1_capture_pending = true;
			} else if (first_byte == WG_HANDSHAKE_RESPONSE &&
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
					 &out_len, &sendJunk, &msgType);

		/* Commit MAC1 pair atomically (both values from the SAME
		 * handshake cycle). */
		if (mac1_capture_pending && proxy->cookie_aead) {
			int s_prefix = (msgType == WG_HANDSHAKE_INIT)
				? proxy->cfg.s1 :
				(msgType == WG_HANDSHAKE_RESPONSE)
				? proxy->cfg.s2 : -1;
			int mac1_off = (msgType == WG_HANDSHAKE_INIT) ? 116
				: (msgType == WG_HANDSHAKE_RESPONSE) ? 60 : -1;
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

		/* Send CPS + junk before handshake init if needed */
		if (sendJunk) {
			/*
			 * Re-seed CPS counter to a fresh random value at the
			 * start of each handshake-init cycle — matches the
			 * reference's `atomic_set(&peer->jp_packet_counter,
			 * get_random_u32())` (src/send.c:43).
			 *
			 * Without this, our counter would start at 0 and grow
			 * monotonically across the tunnel lifetime, producing
			 * a trivially-predictable byte sequence in <c>-tokens
			 * of every handshake's CPS packets (DPI fingerprint).
			 */
			get_random_bytes(&proxy->cps_counter,
					 sizeof(proxy->cps_counter));
			send_cps_packets(proxy);
			send_junk_packets(proxy);
		}

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
		if (n < 0) {
			if (n == -ERESTARTSYS || kthread_should_stop())
				break;
			msleep(10);
			continue;
		}
		if (n < 4)
			continue;

		atomic_inc(&proxy->rx_packets);
		atomic64_add(n, &proxy->rx_bytes);

		/* Transform inbound AWG -> WG */
		out = transform_inbound(buf, n, &proxy->cfg, &out_len);

		/*
		 * Cookie-reply AAD translation.
		 *
		 * Server encrypted the cookie's AEAD with AAD = MAC1 it received
		 * (= MAC1_new, our recomputed). Vanilla WG client will decrypt
		 * with AAD = its stored last_mac1_sent (= MAC1_old). Without
		 * translation, AEAD verification fails on the client side, the
		 * cookie is silently dropped, and subsequent handshakes carry
		 * MAC2=zeros — which the server (under_load) refuses.
		 *
		 * Layout of the forwarded cookie_reply (out[0..64]):
		 *   [0..4]   type = WG_COOKIE_REPLY (already restored)
		 *   [4..8]   receiver_index
		 *   [8..32]  nonce (24 bytes, XChaCha20)
		 *   [32..64] encrypted_cookie (16 ciphertext + 16 Poly1305 tag)
		 */
		if (out && out_len == WG_COOKIE_SIZE &&
		    out[0] == WG_COOKIE_REPLY &&
		    proxy->cookie_aead &&
		    READ_ONCE(proxy->have_last_mac1)) {
			u8 mac1_old[16], mac1_new[16];
			u8 ct_buf[32];
			int dret;

			spin_lock(&proxy->mac1_lock);
			memcpy(mac1_old, proxy->last_mac1_old, 16);
			memcpy(mac1_new, proxy->last_mac1_new, 16);
			spin_unlock(&proxy->mac1_lock);

			memcpy(ct_buf, out + 32, 32);
			dret = awg_xchacha20p1305_decrypt(
				proxy->cookie_aead,
				proxy->cookie_decryption_key,
				out + 8 /* 24-byte nonce */,
				mac1_new, 16,
				ct_buf, 32);
			if (dret == 0) {
				/* ct_buf[0..16] holds the 16-byte cookie now.
				 * Re-encrypt with MAC1_old AAD; output fills
				 * ct_buf[0..32] (16 ct + 16 tag). */
				dret = awg_xchacha20p1305_encrypt(
					proxy->cookie_aead,
					proxy->cookie_decryption_key,
					out + 8, mac1_old, 16,
					ct_buf, 16);
				if (dret == 0)
					memcpy(out + 32, ct_buf, 32);
			}
			/* Any failure: leave the cookie_reply unchanged. */
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
	p->cps_counter = 0;
	p->have_last_mac1 = false;
	p->cookie_aead = NULL;
	p->headroom = compute_headroom(&p->cfg);

	/*
	 * Cookie translation setup. Only relevant when we recompute MAC1 (i.e.
	 * has_server_pub). Failure here is non-fatal: cookie path just won't be
	 * translatable, server's cookie_reply will be silently rejected by the
	 * vanilla WG client as today.
	 */
	if (p->cfg.has_server_pub) {
		compute_cookie_key(p->cfg.server_pub, p->cookie_decryption_key);
		ret = awg_cookie_aead_create(&p->cookie_aead);
		if (ret) {
			pr_warn("awg_proxy: cookie AEAD setup failed: %d "
				"(cookie translation disabled for this slot)\n",
				ret);
			p->cookie_aead = NULL;
			/* not fatal — proceed */
			ret = 0;
		}
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
	 * Removed in v1.1.6. To mitigate the ARP-queue race the inter-packet
	 * jitter in send_cps_packets()/send_junk_packets() now spaces
	 * handshake-cycle packets by ~2ms each, which is enough for the
	 * gateway resolution to complete on a normal first-cycle.
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
	if (p->cookie_aead) {
		awg_cookie_aead_destroy(p->cookie_aead);
		p->cookie_aead = NULL;
	}
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
	if (p->cookie_aead) {
		awg_cookie_aead_destroy(p->cookie_aead);
		p->cookie_aead = NULL;
	}

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
