/* SPDX-License-Identifier: GPL-2.0 */
#ifndef AWGMR_RELAY_H
#define AWGMR_RELAY_H

#include <linux/types.h>
#include <linux/atomic.h>
#include <linux/spinlock.h>
#include <linux/skbuff.h>
#include <linux/wait.h>
#include <linux/workqueue.h>
#include <linux/in.h>
#include <net/dst_cache.h>

#include "config.h"

#define AWGMR_MAX_RELAYS  16    /* как AWG_MAX_TUNNELS у awg-proxy */
#define AWGMR_PKT_MAX     2048  /* датаграмма WG/сервера; длиннее — дроп */
#define AWGMR_RXQ_MAX     1024  /* очередь s2c, как AWG_RX_QUEUE_MAX */

/*
 * Экземпляр = туннель (спека §3.2). Каркас — из kmod/awg-proxy (proxy.c),
 * сознательно скопирован, общего кода нет (§3.6).
 * aligned(8): atomic64_t на 32-битном MIPS требует 8-байтного адреса.
 */
struct awgmr_relay {
	atomic64_t rx_bytes;
	atomic64_t tx_bytes;
	atomic_t rx_pkts;
	atomic_t tx_pkts;
	atomic_t parse_err;
	atomic_t rxq_drop;
	atomic_t trunc;

	struct awgmr_cfg cfg;

	struct socket *listen_sock;   /* 127.0.0.1:cfg.listen_port */
	struct socket *remote_sock;   /* encap-UDP к target */

	struct sockaddr_in client_addr; /* порт WG, учится с c2s */
	spinlock_t client_lock;
	bool has_client;

	struct task_struct *c2s_thread;
	struct task_struct *s2c_thread;
	u8 *c2s_buf;                  /* AWGMR_FRAME_MAX + AWGMR_PKT_MAX */
	u8 *s2c_buf;                  /* AWGMR_PKT_MAX */

	struct sk_buff_head rx_queue;
	wait_queue_head_t rx_wait;

	struct dst_cache tx_dst_cache;

	struct delayed_work timer;    /* STUN keepalive, process-контекст */
	unsigned long timer_period;   /* jiffies; 0 = таймера нет */
	bool hs_done;                 /* был type 2 в любом направлении */

	struct awgmr_rtp_state rtp;   /* только c2s */

	bool active;
} __aligned(8);

int awgmr_relay_add(char *line);
int awgmr_relay_del(u16 listen_port);
void awgmr_relay_cleanup(void);
int awgmr_relay_list(char *buf, int buflen);
int awgmr_xmit_dev_create(void);
void awgmr_xmit_dev_destroy(void);

#endif
