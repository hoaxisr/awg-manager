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
	struct sockaddr_in addr;
	int ret;

	ret = sock_create_kern(&init_net, AF_INET, SOCK_DGRAM, IPPROTO_UDP,
			       sock);
	if (ret)
		return ret;

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

	memset(&addr, 0, sizeof(addr));
	addr.sin_family = AF_INET;
	addr.sin_addr.s_addr = ip;
	addr.sin_port = port;

	ret = kernel_connect(*sock, (struct sockaddr *)&addr, sizeof(addr), 0);
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
 * Send CPS packets before handshake init.
 * Uses cps_generate_all from cps.c (reference's structured approach).
 */
static void send_cps_packets(struct awg_proxy *proxy)
{
	u8 (*bufs)[1500];
	int lens[5];
	int count, i;

	bufs = kmalloc(5 * 1500, GFP_KERNEL);
	if (!bufs)
		return;

	count = cps_generate_all(proxy->cfg.cps, &proxy->cps_counter,
				 bufs, lens);
	for (i = 0; i < count; i++) {
		if (lens[i] > 0)
			proxy_sendmsg(proxy->remote_sock, bufs[i], lens[i],
				      NULL);
	}
	kfree(bufs);
}

/*
 * Send junk packets before handshake init.
 * Uses generate_junk from transform.c (reference's approach).
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
		if (sizes[i] <= 0 || sizes[i] > 1500)
			continue;
		get_random_bytes(junk, sizes[i]);
		proxy_sendmsg(proxy->remote_sock, junk, sizes[i], NULL);
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
		u64 rand_val;

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

		/* Get random value for H range selection */
		get_random_bytes(&rand_val, sizeof(rand_val));

		/* Transform WG -> AWG (handles all message types) */
		out = transform_outbound(raw_buf, headroom, n,
					 &proxy->cfg, rand_val,
					 &out_len, &sendJunk);

		/* Send CPS + junk before handshake init if needed */
		if (sendJunk) {
			send_cps_packets(proxy);
			send_junk_packets(proxy);
		}

		/* Send transformed packet to remote AWG server */
		if (proxy_sendmsg(proxy->remote_sock, out, out_len,
				  NULL) >= 0) {
			atomic_inc(&proxy->tx_packets);
			atomic64_add(out_len, &proxy->tx_bytes);
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
	p->cps_counter = 0;
	p->headroom = compute_headroom(&p->cfg);
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
