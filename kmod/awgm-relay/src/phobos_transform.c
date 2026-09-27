// SPDX-License-Identifier: GPL-2.0
/*
 * Трансформация Phobos: кодирование пакета WG, кадр STUN/RTP, служебка STUN
 * (Binding Request перед handshake и keepalive, ответ на Binding Request
 * сервера). Правила — спека §3.5, формат провода — §3.7.
 */
#include "phobos_transform.h"

/* Состояние обязано влезть в буфер экземпляра (AWGMR_TPRIV_MAX). */
typedef char awgmr_phobos_priv_fits[(sizeof(struct awgmr_phobos_priv) <= AWGMR_TPRIV_MAX) ? 1 : -1];

static int hexval(char c)
{
	if (c >= '0' && c <= '9')
		return c - '0';
	if (c >= 'a' && c <= 'f')
		return c - 'a' + 10;
	if (c >= 'A' && c <= 'F')
		return c - 'A' + 10;
	return -1;
}

static int phobos_parse(void *priv, const char *key, const char *val)
{
	struct awgmr_phobos_priv *p = priv;
	int v, i;

	if (!strcmp(key, "key")) {
		int n = (int)strlen(val);

		if (n == 0 || n % 2 || n / 2 > AWGMR_KEY_MAX)
			return -EINVAL;
		for (i = 0; i < n / 2; i++) {
			int hi = hexval(val[2 * i]), lo = hexval(val[2 * i + 1]);

			if (hi < 0 || lo < 0)
				return -EINVAL;
			p->cfg.key[i] = (u8)(hi << 4 | lo);
		}
		p->cfg.key_len = n / 2;
		p->have_key = true;
	} else if (!strcmp(key, "masking")) {
		if (!strcmp(val, "none"))
			p->cfg.mask = AWGMR_MASK_NONE;
		else if (!strcmp(val, "stun"))
			p->cfg.mask = AWGMR_MASK_STUN;
		else if (!strcmp(val, "media"))
			p->cfg.mask = AWGMR_MASK_MEDIA;
		else
			return -EINVAL;
		p->have_mask = true;
	} else if (!strcmp(key, "max-dummy")) {
		if (awgmr_parse_uint(val, AWGMR_PAD_TOTAL_MAX, &v))
			return -EINVAL;
		p->cfg.max_dummy = v;
	} else if (!strcmp(key, "obfuscate-bytes")) {
		if (awgmr_parse_uint(val, 65535, &v))
			return -EINVAL;
		p->cfg.obf_bytes = v;
	} else {
		return -EINVAL; /* незнакомый ключ — громко, не молча */
	}
	return 0;
}

static int phobos_ready(const void *priv)
{
	const struct awgmr_phobos_priv *p = priv;

	return (p->have_key && p->have_mask) ? 0 : -EINVAL;
}

static void phobos_init(struct awgmr_tctx *t)
{
	struct awgmr_phobos_priv *p = t->priv;

	p->hs_done = false;
	if (p->cfg.mask == AWGMR_MASK_MEDIA)
		awgmr_rtp_init(&p->rtp, t->rng);
}

static unsigned int phobos_timer_ms(const void *priv)
{
	const struct awgmr_phobos_priv *p = priv;

	switch (p->cfg.mask) {
	case AWGMR_MASK_STUN:
		return 10000;
	case AWGMR_MASK_MEDIA:
		return 5000;
	default:
		return 0;
	}
}

static void send_binding_request(struct awgmr_tctx *t)
{
	u8 req[AWGMR_STUN_REQ_LEN];

	t->send_remote(t->relay, req, awgmr_stun_binding_request(req, t->rng));
}

static int phobos_encode(struct awgmr_tctx *t, u8 *payload, int len, int cap, u8 **start)
{
	struct awgmr_phobos_priv *p = t->priv;
	int out, hdr = 0;

	if (len >= 1 && payload[0] == 2)
		WRITE_ONCE(p->hs_done, true);   /* сервер инициировал — ответ WG */
	if (len >= 1 && payload[0] == 1 && p->cfg.mask != AWGMR_MASK_NONE)
		send_binding_request(t);
	out = awgmr_encode(&p->cfg, payload, len, cap, t->rng);
	if (out < 0)
		return out;
	if (p->cfg.mask == AWGMR_MASK_STUN)
		hdr = awgmr_stun_frame(payload - AWGMR_STUN_HDR, out, t->rng);
	else if (p->cfg.mask == AWGMR_MASK_MEDIA)
		hdr = awgmr_rtp_frame(payload - AWGMR_RTP_HDR, &p->rtp);
	*start = payload - hdr;
	return out + hdr;
}

static int phobos_decode(struct awgmr_tctx *t, u8 *buf, int len, int *off)
{
	struct awgmr_phobos_priv *p = t->priv;
	int plen = 0, wg;

	switch (awgmr_unframe(p->cfg.mask, buf, len, off, &plen)) {
	case AWGMR_IN_DATA:
		wg = awgmr_decode(&p->cfg, buf + *off, plen);
		if (wg < 0)
			return -EINVAL;
		if (buf[*off] == 2)
			WRITE_ONCE(p->hs_done, true);
		return wg;
	case AWGMR_IN_BIND_REQ: {
		u8 resp[AWGMR_STUN_OK_LEN];
		int rl = awgmr_stun_binding_success(resp, buf, len, t->peer_ip, t->peer_port);

		if (rl > 0)
			t->send_remote(t->relay, resp, rl);
		return 0;
	}
	case AWGMR_IN_BIND_OK:
		return 0;
	case AWGMR_IN_DROP:
	default:
		return -EINVAL;
	}
}

/* keepalive: только после рукопожатия (спека §3.5) */
static void phobos_on_timer(struct awgmr_tctx *t)
{
	struct awgmr_phobos_priv *p = t->priv;

	if (READ_ONCE(p->hs_done))
		send_binding_request(t);
}

static int phobos_describe(const void *priv, char *buf, int len)
{
	static const char *const masks[] = { "none", "stun", "media" };
	const struct awgmr_phobos_priv *p = priv;

	return snprintf(buf, len, "masking=%s", masks[p->cfg.mask]);
}

const struct awgmr_transform awgmr_phobos_transform = {
	.name = "phobos",
	.global_init = awgmr_crc8_init,
	.priv_size = sizeof(struct awgmr_phobos_priv),
	.headroom = AWGMR_FRAME_MAX,
	.parse = phobos_parse,
	.ready = phobos_ready,
	.init = phobos_init,
	.timer_ms = phobos_timer_ms,
	.encode = phobos_encode,
	.decode = phobos_decode,
	.on_timer = phobos_on_timer,
	.describe = phobos_describe,
};
