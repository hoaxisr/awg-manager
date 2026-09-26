// SPDX-License-Identifier: GPL-2.0
/*
 * awgm_relay — экземпляры kernel-релея (спека §3.2).
 *
 *   WG -> 127.0.0.1:listen -> [c2s: encode + кадр] -> udp_tunnel_xmit -> сервер
 *   сервер -> encap_rcv (softirq, только очередь) -> [s2c: кадр + decode] -> WG
 *
 * Каркас (сокеты, encap, xmit, teardown) скопирован из kmod/awg-proxy/src/proxy.c
 * сознательно, без общего кода (§3.6). Приём ответа сервера — строго через
 * encap_rcv: обычный recvmsg на remote-сокете даёт Keenetic FASTNAT/PPE
 * забрать поток в offload и терять ответ (proxy.h awg-proxy, §5).
 */
#include <linux/kernel.h>
#include <linux/slab.h>
#include <linux/kthread.h>
#include <linux/mutex.h>
#include <linux/net.h>
#include <linux/socket.h>
#include <linux/random.h>
#include <linux/delay.h>
#include <linux/udp.h>
#include <linux/ip.h>
#include <linux/netdevice.h>
#include <net/sock.h>
#include <net/udp.h>
#include <net/udp_tunnel.h>
#include <net/route.h>
#include <net/ip.h>

#include "relay.h"

static struct awgmr_relay relays[AWGMR_MAX_RELAYS];
static DEFINE_MUTEX(relay_mutex);
static struct net_device *xmit_dev;

static u32 krng_next(void *ctx) { return prandom_u32(); }
static const struct awgmr_rng krng = { .next = krng_next };

/* ---- dummy netdev: udp_tunnel_xmit_skb трогает skb->dev->tstats ---- */

static void xmit_dev_setup(struct net_device *dev) { }

int awgmr_xmit_dev_create(void)
{
	xmit_dev = alloc_netdev(0, "awgmrelay", NET_NAME_UNKNOWN, xmit_dev_setup);
	if (!xmit_dev)
		return -ENOMEM;
	xmit_dev->tstats = netdev_alloc_pcpu_stats(struct pcpu_sw_netstats);
	if (!xmit_dev->tstats) {
		free_netdev(xmit_dev);
		xmit_dev = NULL;
		return -ENOMEM;
	}
	return 0;
}

void awgmr_xmit_dev_destroy(void)
{
	if (!xmit_dev)
		return;
	free_percpu(xmit_dev->tstats);
	free_netdev(xmit_dev);
	xmit_dev = NULL;
}

/*
 * Отправка серверу через udp_tunnel_xmit_skb (как send4 в WG). Зовётся из
 * трёх process-контекстов — c2s, s2c (Binding Success), work (keepalive);
 * допущение awg-proxy «только c2s» здесь снято: dst_cache per-cpu под
 * local_bh_disable безопасен из любого process-контекста.
 */
static int relay_send_remote(struct awgmr_relay *r, const u8 *buf, int len)
{
	int headroom = sizeof(struct iphdr) + sizeof(struct udphdr) + MAX_HEADER;
	struct sock *sk = r->remote_sock->sk;
	__be16 sport = inet_sk(sk)->inet_sport;
	__be32 daddr;
	struct flowi4 fl = {};
	struct sk_buff *skb;
	struct rtable *rt;

	memcpy(&daddr, r->cfg.target_ip, 4);
	fl.daddr = daddr;
	fl.fl4_dport = htons(r->cfg.target_port);
	fl.fl4_sport = sport;
	fl.flowi4_proto = IPPROTO_UDP;

	skb = alloc_skb(len + headroom, GFP_KERNEL);
	if (!skb)
		return -ENOMEM;
	skb_reserve(skb, headroom);
	memcpy(skb_put(skb, len), buf, len);

	local_bh_disable();
	rt = dst_cache_get_ip4(&r->tx_dst_cache, &fl.saddr);
	if (!rt) {
		rt = ip_route_output_flow(&init_net, &fl, sk);
		if (IS_ERR(rt)) {
			long err = PTR_ERR(rt);

			local_bh_enable();
			kfree_skb(skb);
			pr_warn_ratelimited("awgm_relay: no route to %pI4: %ld\n", &daddr, err);
			return (int)err;
		}
		dst_cache_set_ip4(&r->tx_dst_cache, &rt->dst, fl.saddr);
	}
	skb->dev = xmit_dev;
	skb->mark = 0;
	skb->ignore_df = 1;
	/* 4.9: 12 аргументов; rt и skb поглощаются на любом пути. */
	udp_tunnel_xmit_skb(rt, sk, skb, fl.saddr, daddr, 0,
			    ip4_dst_hoplimit(&rt->dst), 0, sport,
			    htons(r->cfg.target_port), false, false);
	local_bh_enable();

	atomic_inc(&r->tx_pkts);
	atomic64_add(len, &r->tx_bytes);
	return 0;
}

static void relay_send_client(struct awgmr_relay *r, u8 *buf, int len)
{
	struct sockaddr_in to;
	struct msghdr msg = {};
	struct kvec iov = { .iov_base = buf, .iov_len = len };

	spin_lock(&r->client_lock);
	if (!r->has_client) {
		spin_unlock(&r->client_lock);
		return; /* WG ещё ничего не слал — отвечать некуда */
	}
	to = r->client_addr;
	spin_unlock(&r->client_lock);
	msg.msg_name = &to;
	msg.msg_namelen = sizeof(to);
	kernel_sendmsg(r->listen_sock, &msg, &iov, 1, len);
}

/* ---- c2s ---- */

static int c2s_thread_fn(void *data)
{
	struct awgmr_relay *r = data;
	u8 *payload = r->c2s_buf + AWGMR_FRAME_MAX;

	while (!kthread_should_stop()) {
		struct msghdr msg = {};
		struct kvec iov = { .iov_base = payload, .iov_len = AWGMR_PKT_MAX };
		struct sockaddr_in from;
		int n, out, hdr = 0;

		msg.msg_name = &from;
		msg.msg_namelen = sizeof(from);
		n = kernel_recvmsg(r->listen_sock, &msg, &iov, 1, AWGMR_PKT_MAX, 0);
		if (kthread_should_stop())
			break;
		if (n < 0) {
			if (n == -ERESTARTSYS || n == -EINTR || n == -ESHUTDOWN ||
			    n == -EBADF || n == -EPIPE)
				break;
			msleep(10);
			continue;
		}
		if (msg.msg_flags & MSG_TRUNC) {
			atomic_inc(&r->trunc);
			continue;
		}
		if (n < 4) {
			cond_resched();
			continue;
		}

		spin_lock(&r->client_lock);
		r->client_addr = from;
		r->has_client = true;
		spin_unlock(&r->client_lock);

		if (payload[0] == 2)
			WRITE_ONCE(r->hs_done, true);   /* сервер инициировал — ответ WG */
		if (payload[0] == 1 && r->cfg.phobos.mask != AWGMR_MASK_NONE) {
			u8 req[AWGMR_STUN_REQ_LEN];

			relay_send_remote(r, req, awgmr_stun_binding_request(req, &krng));
		}

		out = awgmr_encode(&r->cfg.phobos, payload, n, AWGMR_PKT_MAX, &krng);
		if (out < 0) {
			atomic_inc(&r->parse_err);
			continue;
		}
		if (r->cfg.phobos.mask == AWGMR_MASK_STUN)
			hdr = awgmr_stun_frame(payload - AWGMR_STUN_HDR, out, &krng);
		else if (r->cfg.phobos.mask == AWGMR_MASK_MEDIA)
			hdr = awgmr_rtp_frame(payload - AWGMR_RTP_HDR, &r->rtp);
		relay_send_remote(r, payload - hdr, out + hdr);
		/*
		 * Ядро Keenetic без вытеснения (PREEMPT_NONE): при непрерывном входе
		 * поток не доходит до точки планирования, softirq ушёл в ksoftirqd,
		 * а тот не получает CPU — backlog loopback (netdev_max_backlog) молча
		 * переполняется. F472: без уступки 135 885 drops за 10 с, с ней 0.
		 */
		cond_resched();
	}
	return 0;
}

/* ---- s2c ---- */

static int encap_rcv(struct sock *sk, struct sk_buff *skb)
{
	struct awgmr_relay *r = rcu_dereference_sk_user_data(sk);
	const struct udphdr *uh;
	__be32 target;

	if (unlikely(!r) || !READ_ONCE(r->active))
		goto drop;
	if (unlikely(!pskb_may_pull(skb, sizeof(struct udphdr))))
		goto drop;
	uh = (const struct udphdr *)skb->data;
	memcpy(&target, r->cfg.target_ip, 4);
	if (ip_hdr(skb)->saddr != target || uh->source != htons(r->cfg.target_port))
		goto drop;
	__skb_pull(skb, sizeof(struct udphdr));
	if (skb_queue_len(&r->rx_queue) >= AWGMR_RXQ_MAX) {
		atomic_inc(&r->rxq_drop);
		goto drop;
	}
	skb_queue_tail(&r->rx_queue, skb);
	wake_up(&r->rx_wait);
	return 0;
drop:
	kfree_skb(skb);
	return 0;
}

static int s2c_thread_fn(void *data)
{
	struct awgmr_relay *r = data;
	u8 *buf = r->s2c_buf;

	while (!kthread_should_stop()) {
		struct sk_buff *skb;
		int n, off = 0, plen = 0, wg;

		wait_event_interruptible(r->rx_wait,
			!skb_queue_empty(&r->rx_queue) || kthread_should_stop());
		if (kthread_should_stop())
			break;
		skb = skb_dequeue(&r->rx_queue);
		if (!skb)
			continue;
		n = skb->len;
		if (n <= 0 || n > AWGMR_PKT_MAX || skb_copy_bits(skb, 0, buf, n) < 0) {
			atomic_inc(&r->trunc);
			kfree_skb(skb);
			continue;
		}
		kfree_skb(skb);
		atomic_inc(&r->rx_pkts);
		atomic64_add(n, &r->rx_bytes);

		switch (awgmr_unframe(r->cfg.phobos.mask, buf, n, &off, &plen)) {
		case AWGMR_IN_DATA:
			wg = awgmr_decode(&r->cfg.phobos, buf + off, plen);
			if (wg < 0) {
				atomic_inc(&r->parse_err);
				break;
			}
			if (buf[off] == 2)
				WRITE_ONCE(r->hs_done, true);
			relay_send_client(r, buf + off, wg);
			break;
		case AWGMR_IN_BIND_REQ: {
			u8 resp[AWGMR_STUN_OK_LEN];
			u8 port[2] = { r->cfg.target_port >> 8, r->cfg.target_port & 0xFF };
			int rl = awgmr_stun_binding_success(resp, buf, n, r->cfg.target_ip, port);

			if (rl > 0)
				relay_send_remote(r, resp, rl);
			break;
		}
		case AWGMR_IN_BIND_OK:
			break;
		case AWGMR_IN_DROP:
			atomic_inc(&r->parse_err);
			break;
		}
		cond_resched(); /* F472, см. c2s */
	}
	return 0;
}

/* ---- keepalive STUN: work взводится в add и перевзводит сам себя (§3.2) ---- */

static void timer_fn(struct work_struct *w)
{
	struct awgmr_relay *r = container_of(to_delayed_work(w), struct awgmr_relay, timer);

	if (!READ_ONCE(r->active))
		return;
	if (READ_ONCE(r->hs_done)) {
		u8 req[AWGMR_STUN_REQ_LEN];

		relay_send_remote(r, req, awgmr_stun_binding_request(req, &krng));
	}
	schedule_delayed_work(&r->timer, r->timer_period);
}

/* ---- жизненный цикл ---- */

static int create_listen_socket(struct awgmr_relay *r)
{
	struct sockaddr_in a = {};
	int ret;

	ret = sock_create_kern(&init_net, AF_INET, SOCK_DGRAM, IPPROTO_UDP, &r->listen_sock);
	if (ret)
		return ret;
	a.sin_family = AF_INET;
	a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	a.sin_port = htons(r->cfg.listen_port);
	return kernel_bind(r->listen_sock, (struct sockaddr *)&a, sizeof(a));
}

/* Без connect(): на части ядер он шлёт 0-байтовый пробник — отпечаток
 * (история awg-proxy v1.1.6). Адрес назначения — в каждом xmit. */
static int create_remote_socket(struct awgmr_relay *r)
{
	struct sockaddr_in a = {};
	struct udp_tunnel_sock_cfg tcfg = {
		.sk_user_data = r,
		.encap_type = 1,
		.encap_rcv = encap_rcv,
	};
	int pmtu = IP_PMTUDISC_DONT;
	int ret;

	ret = sock_create_kern(&init_net, AF_INET, SOCK_DGRAM, IPPROTO_UDP, &r->remote_sock);
	if (ret)
		return ret;
	kernel_setsockopt(r->remote_sock, IPPROTO_IP, IP_MTU_DISCOVER, (char *)&pmtu, sizeof(pmtu));
	a.sin_family = AF_INET;
	a.sin_addr.s_addr = htonl(INADDR_ANY);
	ret = kernel_bind(r->remote_sock, (struct sockaddr *)&a, sizeof(a));
	if (ret)
		return ret;
	setup_udp_tunnel_sock(&init_net, r->remote_sock, &tcfg);
	return 0;
}

/* Порядок §3.2: active=false → RCU-отключение encap → потоки → work →
 * очередь → сокеты. Годится и для недостроенного экземпляра (откат add). */
static void relay_stop(struct awgmr_relay *r)
{
	WRITE_ONCE(r->active, false);
	if (r->remote_sock && r->remote_sock->sk) {
		struct sock *sk = r->remote_sock->sk;

		udp_sk(sk)->encap_type = 0;
		WRITE_ONCE(udp_sk(sk)->encap_rcv, NULL);
		rcu_assign_sk_user_data(sk, NULL);
		synchronize_net();
	}
	if (r->listen_sock)
		kernel_sock_shutdown(r->listen_sock, SHUT_RDWR);
	if (r->remote_sock)
		kernel_sock_shutdown(r->remote_sock, SHUT_RDWR);
	if (r->c2s_thread)
		kthread_stop(r->c2s_thread);
	if (r->s2c_thread)
		kthread_stop(r->s2c_thread);
	r->c2s_thread = r->s2c_thread = NULL;
	if (r->timer_period)
		cancel_delayed_work_sync(&r->timer);
	skb_queue_purge(&r->rx_queue);
	if (r->listen_sock)
		sock_release(r->listen_sock);
	if (r->remote_sock)
		sock_release(r->remote_sock);
	r->listen_sock = r->remote_sock = NULL;
	dst_cache_destroy(&r->tx_dst_cache);
	kfree(r->c2s_buf);
	kfree(r->s2c_buf);
	r->c2s_buf = r->s2c_buf = NULL;
	memzero_explicit(&r->cfg, sizeof(r->cfg)); /* ключ */
}

int awgmr_relay_add(char *line)
{
	struct awgmr_cfg cfg;
	struct awgmr_relay *r = NULL;
	int i, ret;

	ret = awgmr_config_parse(line, &cfg);
	if (ret)
		return ret;

	mutex_lock(&relay_mutex);
	for (i = 0; i < AWGMR_MAX_RELAYS; i++) {
		if (relays[i].active && relays[i].cfg.listen_port == cfg.listen_port) {
			ret = -EEXIST;
			goto out;
		}
	}
	for (i = 0; i < AWGMR_MAX_RELAYS; i++) {
		if (!relays[i].active) {
			r = &relays[i];
			break;
		}
	}
	if (!r) {
		ret = -ENOSPC;
		goto out;
	}

	memset(r, 0, sizeof(*r));
	r->cfg = cfg;
	spin_lock_init(&r->client_lock);
	skb_queue_head_init(&r->rx_queue);
	init_waitqueue_head(&r->rx_wait);
	INIT_DELAYED_WORK(&r->timer, timer_fn);
	if (cfg.phobos.mask == AWGMR_MASK_STUN)
		r->timer_period = 10 * HZ;
	else if (cfg.phobos.mask == AWGMR_MASK_MEDIA)
		r->timer_period = 5 * HZ;
	if (cfg.phobos.mask == AWGMR_MASK_MEDIA)
		awgmr_rtp_init(&r->rtp, &krng);

	/* Буферы здесь, не в потоке: иначе отказ kmalloc — слот-зомби (§3.2). */
	r->c2s_buf = kmalloc(AWGMR_FRAME_MAX + AWGMR_PKT_MAX, GFP_KERNEL);
	r->s2c_buf = kmalloc(AWGMR_PKT_MAX, GFP_KERNEL);
	if (!r->c2s_buf || !r->s2c_buf) {
		ret = -ENOMEM;
		goto fail;
	}
	ret = dst_cache_init(&r->tx_dst_cache, GFP_KERNEL);
	if (ret)
		goto fail;
	ret = create_listen_socket(r);
	if (ret)
		goto fail;
	ret = create_remote_socket(r);
	if (ret)
		goto fail;

	r->active = true;
	r->c2s_thread = kthread_run(c2s_thread_fn, r, "awgmr_c2s/%u", cfg.listen_port);
	if (IS_ERR(r->c2s_thread)) {
		ret = PTR_ERR(r->c2s_thread);
		r->c2s_thread = NULL;
		goto fail;
	}
	r->s2c_thread = kthread_run(s2c_thread_fn, r, "awgmr_s2c/%u", cfg.listen_port);
	if (IS_ERR(r->s2c_thread)) {
		ret = PTR_ERR(r->s2c_thread);
		r->s2c_thread = NULL;
		goto fail;
	}
	if (r->timer_period)
		schedule_delayed_work(&r->timer, r->timer_period);
	pr_info("awgm_relay: 127.0.0.1:%u -> %pI4:%u\n", cfg.listen_port,
		cfg.target_ip, cfg.target_port);
	mutex_unlock(&relay_mutex);
	memzero_explicit(&cfg, sizeof(cfg));
	return 0;

fail:
	relay_stop(r);
out:
	mutex_unlock(&relay_mutex);
	memzero_explicit(&cfg, sizeof(cfg));
	return ret;
}

int awgmr_relay_del(u16 listen_port)
{
	int i, ret = -ENOENT;

	mutex_lock(&relay_mutex);
	for (i = 0; i < AWGMR_MAX_RELAYS; i++) {
		if (relays[i].active && relays[i].cfg.listen_port == listen_port) {
			relay_stop(&relays[i]);
			ret = 0;
			break;
		}
	}
	mutex_unlock(&relay_mutex);
	return ret;
}

void awgmr_relay_cleanup(void)
{
	int i;

	mutex_lock(&relay_mutex);
	for (i = 0; i < AWGMR_MAX_RELAYS; i++)
		if (relays[i].active)
			relay_stop(&relays[i]);
	mutex_unlock(&relay_mutex);
}

/* Строка на экземпляр, ключ не выводится (§3.3). */
int awgmr_relay_list(char *buf, int buflen)
{
	int i, len = 0;

	mutex_lock(&relay_mutex);
	for (i = 0; i < AWGMR_MAX_RELAYS && len < buflen - 256; i++) {
		struct awgmr_relay *r = &relays[i];
		static const char *const masks[] = { "none", "stun", "media" };

		if (!r->active)
			continue;
		len += snprintf(buf + len, buflen - len,
			"127.0.0.1:%u %pI4:%u transform=phobos masking=%s rx=%lld tx=%lld rx_pkt=%d tx_pkt=%d parse_err=%d rxq_drop=%d trunc=%d\n",
			r->cfg.listen_port, r->cfg.target_ip, r->cfg.target_port,
			masks[r->cfg.phobos.mask],
			(long long)atomic64_read(&r->rx_bytes),
			(long long)atomic64_read(&r->tx_bytes),
			atomic_read(&r->rx_pkts), atomic_read(&r->tx_pkts),
			atomic_read(&r->parse_err), atomic_read(&r->rxq_drop),
			atomic_read(&r->trunc));
	}
	mutex_unlock(&relay_mutex);
	return len;
}
