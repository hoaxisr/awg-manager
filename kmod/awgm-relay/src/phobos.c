// SPDX-License-Identifier: GPL-2.0
/* Протокол Phobos — см. phobos.h и спеку §3.7. */
#include "phobos.h"

static u8 crc8_tab[256];
static u32 crc32_tab[256];

static const u8 stun_magic[4] = { 0x21, 0x12, 0xA4, 0x42 };

#define STUN_DATA_IND   0x0115
#define STUN_BIND_REQ   0x0001
#define STUN_BIND_OK    0x0101
#define STUN_ATTR_DATA  0x0013
#define STUN_ATTR_XMA   0x0020
#define STUN_ATTR_FP    0x8028
#define STUN_FP_XOR     0x5354554Eu

static const u16 rtp_steps[] = { 1500, 3000, 3600, 3750, 6000 };

static void put16(u8 *p, u16 v) { p[0] = v >> 8; p[1] = v & 0xFF; }
static void put32(u8 *p, u32 v)
{
	p[0] = v >> 24; p[1] = (v >> 16) & 0xFF; p[2] = (v >> 8) & 0xFF; p[3] = v & 0xFF;
}
static u16 get16(const u8 *p) { return (u16)(p[0] << 8 | p[1]); }

void awgmr_crc8_init(void)
{
	int i, b;

	for (i = 0; i < 256; i++) {
		u8 c = (u8)i;
		u32 d = (u32)i;

		for (b = 0; b < 8; b++)
			c = (c & 1) ? (u8)((c >> 1) ^ 0x8C) : (u8)(c >> 1);
		crc8_tab[i] = c;
		for (b = 0; b < 8; b++)
			d = (d & 1) ? (d >> 1) ^ 0xEDB88320u : d >> 1;
		crc32_tab[i] = d;
	}
}

void awgmr_keystream_xor(u8 *buf, int n, const u8 *key, int key_len)
{
	u8 adj[AWGMR_KEY_MAX];
	u8 base = (u8)(n + key_len);
	u8 c = 0;
	int i, j = 0;

	for (i = 0; i < key_len; i++)
		adj[i] = (u8)(key[i] + base);
	for (i = 0; i < n; i++) {
		c = crc8_tab[c ^ adj[j]];
		buf[i] ^= c;
		if (++j == key_len)
			j = 0;
	}
}

u32 awgmr_crc32(const u8 *p, int n)
{
	u32 c = 0xFFFFFFFFu;
	int i;

	for (i = 0; i < n; i++)
		c = crc32_tab[(c ^ p[i]) & 0xFF] ^ (c >> 8);
	return ~c;
}

static int imin(int a, int b) { return a < b ? a : b; }

int awgmr_encode(const struct awgmr_phobos_cfg *cfg, u8 *buf, int len, int cap,
		 const struct awgmr_rng *rng)
{
	int n = cfg->obf_bytes;
	int partial = n > 0 && n < len;
	int d = 0, i;
	u8 type, r;

	if (len < 4 || buf[0] < 1 || buf[0] > 4)
		return -EINVAL;
	type = buf[0];
	r = (u8)(1 + rng->next(rng->ctx) % 255);
	buf[0] ^= r;
	buf[1] = r;
	if (!partial && len < AWGMR_PAD_TOTAL_MAX) {
		int maxd = AWGMR_PAD_TOTAL_MAX - len;

		if (n > 0)
			maxd = imin(maxd, n - len + 1);
		if (type == 1 || type == 2)
			d = (int)(rng->next(rng->ctx) % (u32)imin(maxd, AWGMR_PAD_HS_MAX));
		else if (cfg->max_dummy > 0)
			d = (int)(rng->next(rng->ctx) % (u32)imin(maxd, cfg->max_dummy));
	}
	if (len + d > cap)
		return -EINVAL;
	buf[2] = d & 0xFF;
	buf[3] = d >> 8;
	for (i = 0; i < d; i++)
		buf[len + i] = (u8)rng->next(rng->ctx);
	len += d;
	awgmr_keystream_xor(buf, partial ? n : len, cfg->key, cfg->key_len);
	return len;
}

int awgmr_decode(const struct awgmr_phobos_cfg *cfg, u8 *buf, int len)
{
	int n = cfg->obf_bytes;
	int d, out;

	if (len < 4)
		return -EINVAL;
	awgmr_keystream_xor(buf, (n > 0 && n < len) ? n : len, cfg->key, cfg->key_len);
	if (buf[0] >= 1 && buf[0] <= 4 && !(buf[1] | buf[2] | buf[3]))
		return -EINVAL; /* версия 0 — не поддерживаем (§3.5) */
	buf[0] ^= buf[1];
	d = buf[2] | buf[3] << 8;
	buf[1] = buf[2] = buf[3] = 0;
	out = len - d;
	if (out < 4 || out > len)
		return -EINVAL;
	return out;
}

int awgmr_stun_frame(u8 *hdr, int payload_len, const struct awgmr_rng *rng)
{
	int i;

	put16(hdr, STUN_DATA_IND);
	put16(hdr + 2, 0);
	memcpy(hdr + 4, stun_magic, 4);
	for (i = 0; i < 12; i++)
		hdr[8 + i] = (u8)rng->next(rng->ctx);
	put16(hdr + 20, STUN_ATTR_DATA);
	put16(hdr + 22, (u16)payload_len);
	return AWGMR_STUN_HDR;
}

void awgmr_rtp_init(struct awgmr_rtp_state *st, const struct awgmr_rng *rng)
{
	st->seq = (u16)rng->next(rng->ctx);
	st->ts = rng->next(rng->ctx);
	do {
		st->ssrc = rng->next(rng->ctx);
	} while (!st->ssrc);
	st->pt = (u8)(96 + rng->next(rng->ctx) % 32);
	st->ts_step = rtp_steps[rng->next(rng->ctx) % (sizeof(rtp_steps) / sizeof(rtp_steps[0]))];
}

int awgmr_rtp_frame(u8 *hdr, struct awgmr_rtp_state *st)
{
	hdr[0] = 0x80;
	hdr[1] = 0x80 | (st->pt & 0x7F);
	put16(hdr + 2, st->seq);
	put32(hdr + 4, st->ts);
	put32(hdr + 8, st->ssrc);
	st->seq++;
	st->ts += st->ts_step;
	return AWGMR_RTP_HDR;
}

static enum awgmr_in stun_classify(const u8 *buf, int len, int *off, int *plen)
{
	u16 type = get16(buf);

	if (type == STUN_DATA_IND) {
		int dl;

		if (len < AWGMR_STUN_HDR || get16(buf + 20) != STUN_ATTR_DATA)
			return AWGMR_IN_DROP;
		dl = get16(buf + 22);
		if (AWGMR_STUN_HDR + dl > len)
			return AWGMR_IN_DROP;
		*off = AWGMR_STUN_HDR;
		*plen = dl;
		return AWGMR_IN_DATA;
	}
	if (len < 20)
		return AWGMR_IN_DROP; /* txid за границей — не читаем */
	if (type == STUN_BIND_REQ)
		return AWGMR_IN_BIND_REQ;
	if (type == STUN_BIND_OK)
		return AWGMR_IN_BIND_OK;
	return AWGMR_IN_DROP;
}

enum awgmr_in awgmr_unframe(enum awgmr_mask mask, const u8 *buf, int len,
			    int *off, int *plen)
{
	int is_stun = len >= 8 && !memcmp(buf + 4, stun_magic, 4);

	switch (mask) {
	case AWGMR_MASK_NONE:
		*off = 0;
		*plen = len;
		return AWGMR_IN_DATA;
	case AWGMR_MASK_STUN:
		return is_stun ? stun_classify(buf, len, off, plen) : AWGMR_IN_DROP;
	case AWGMR_MASK_MEDIA:
		if (is_stun)
			return stun_classify(buf, len, off, plen);
		if (len < AWGMR_RTP_HDR + 4 || (buf[0] & 0xC0) != 0x80)
			return AWGMR_IN_DROP;
		*off = AWGMR_RTP_HDR;
		*plen = len - AWGMR_RTP_HDR;
		return AWGMR_IN_DATA;
	}
	return AWGMR_IN_DROP;
}

/* FINGERPRINT по RFC 5389: длина в заголовке уже включает атрибут. */
static int stun_finish(u8 *out, int body_len)
{
	u32 fp;

	put16(out + 2, (u16)(body_len + 8));
	put16(out + 20 + body_len, STUN_ATTR_FP);
	put16(out + 22 + body_len, 4);
	fp = awgmr_crc32(out, 20 + body_len) ^ STUN_FP_XOR;
	put32(out + 24 + body_len, fp);
	return 20 + body_len + 8;
}

int awgmr_stun_binding_request(u8 *out, const struct awgmr_rng *rng)
{
	int i;

	put16(out, STUN_BIND_REQ);
	memcpy(out + 4, stun_magic, 4);
	for (i = 0; i < 12; i++)
		out[8 + i] = (u8)rng->next(rng->ctx);
	return stun_finish(out, 0);
}

int awgmr_stun_binding_success(u8 *out, const u8 *req, int req_len,
			       const u8 addr[4], const u8 port[2])
{
	u8 *a = out + 20;
	int i;

	if (req_len < 20)
		return -EINVAL;
	put16(out, STUN_BIND_OK);
	memcpy(out + 4, stun_magic, 4);
	memcpy(out + 8, req + 8, 12);
	put16(a, STUN_ATTR_XMA);
	put16(a + 2, 8);
	a[4] = 0;
	a[5] = 0x01;
	a[6] = port[0] ^ stun_magic[0];
	a[7] = port[1] ^ stun_magic[1];
	for (i = 0; i < 4; i++)
		a[8 + i] = addr[i] ^ stun_magic[i];
	return stun_finish(out, 12);
}
