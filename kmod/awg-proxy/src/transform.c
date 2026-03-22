// SPDX-License-Identifier: GPL-2.0
/*
 * AWG transform logic.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 * Adapted: fastrand → get_random_bytes.
 */

#include <linux/kernel.h>
#include <linux/string.h>
#include <linux/random.h>
#include <asm/byteorder.h>

#include "transform.h"
#include "blake2s.h"

void config_compute(awg_config_t *cfg)
{
	static const u8 z[32] = {0};
	int tmin;

	cfg->has_server_pub = memcmp(cfg->server_pub, z, 32) != 0;
	cfg->has_client_pub = memcmp(cfg->client_pub, z, 32) != 0;

	compute_mac1_key(cfg->server_pub, cfg->mac1key_server);
	compute_mac1_key(cfg->client_pub, cfg->mac1key_client);

	cfg->h4_fixed = cfg->h4.min;
	cfg->h4_noop = (cfg->h4.min == WG_TRANSPORT_DATA &&
			cfg->h4.max == WG_TRANSPORT_DATA && cfg->s4 == 0);
	cfg->init_total = cfg->s1 + WG_INIT_SIZE;
	cfg->resp_total = cfg->s2 + WG_RESP_SIZE;
	cfg->cookie_total = cfg->s3 + WG_COOKIE_SIZE;

	tmin = cfg->s4 + WG_TRANSPORT_MIN;
	cfg->transport_size_ambiguous =
		(cfg->init_total >= tmin) ||
		(cfg->resp_total >= tmin) ||
		(cfg->cookie_total >= tmin);
}

static inline u32 read32_le(const u8 *p)
{
	__le32 v;

	memcpy(&v, p, 4);
	return le32_to_cpu(v);
}

static inline void write32_le(u8 *p, u32 v)
{
	__le32 le = cpu_to_le32(v);

	memcpy(p, &le, 4);
}

u8 *transform_outbound(u8 *buf, int dataoff, int n,
		       const awg_config_t *cfg, u64 rand_val,
		       int *out_len, int *sendJunk)
{
	u8 *data = buf + dataoff;
	u32 msgType;

	*sendJunk = 0;
	if (n < 4) {
		*out_len = n;
		return data;
	}

	msgType = read32_le(data);

	if (msgType == WG_HANDSHAKE_INIT && n == WG_INIT_SIZE) {
		write32_le(data, hrange_pick(&cfg->h1, rand_val));
		if (cfg->has_server_pub)
			recompute_mac1(data, cfg->mac1key_server);
		*sendJunk = (cfg->jc > 0);
		if (cfg->s1 > 0) {
			if (WARN_ON_ONCE(dataoff < cfg->s1)) {
				*out_len = n;
				return data;
			}
			get_random_bytes(data - cfg->s1, cfg->s1);
			*out_len = cfg->s1 + n;
			return data - cfg->s1;
		}
		*out_len = n;
		return data;
	}

	if (msgType == WG_HANDSHAKE_RESPONSE && n == WG_RESP_SIZE) {
		write32_le(data, hrange_pick(&cfg->h2, rand_val));
		if (cfg->s2 > 0) {
			if (WARN_ON_ONCE(dataoff < cfg->s2)) {
				*out_len = n;
				return data;
			}
			get_random_bytes(data - cfg->s2, cfg->s2);
			*out_len = cfg->s2 + n;
			return data - cfg->s2;
		}
		*out_len = n;
		return data;
	}

	if (msgType == WG_COOKIE_REPLY && n == WG_COOKIE_SIZE) {
		write32_le(data, hrange_pick(&cfg->h3, rand_val));
		if (cfg->s3 > 0) {
			if (WARN_ON_ONCE(dataoff < cfg->s3)) {
				*out_len = n;
				return data;
			}
			get_random_bytes(data - cfg->s3, cfg->s3);
			*out_len = cfg->s3 + n;
			return data - cfg->s3;
		}
		*out_len = n;
		return data;
	}

	if (msgType == WG_TRANSPORT_DATA && n >= WG_TRANSPORT_MIN) {
		if (cfg->h4_noop) {
			*out_len = n;
			return data;
		}
		if (cfg->h4.min == cfg->h4.max)
			write32_le(data, cfg->h4_fixed);
		else
			write32_le(data, hrange_pick(&cfg->h4, rand_val));
		if (cfg->s4 > 0 && dataoff >= cfg->s4) {
			*out_len = cfg->s4 + n;
			return buf + dataoff - cfg->s4;
		}
		*out_len = n;
		return data;
	}

	/* Unknown, pass through */
	*out_len = n;
	return data;
}

u8 *transform_inbound(u8 *buf, int n, const awg_config_t *cfg, int *out_len)
{
	if (n < 4)
		return NULL;

	/* Fast path: identity transport */
	if (cfg->h4_noop) {
		if (read32_le(buf) == WG_TRANSPORT_DATA &&
		    n >= WG_TRANSPORT_MIN) {
			*out_len = n;
			return buf;
		}
	}

	/* Size-based dispatch: handshake first, transport last */
	if (n == cfg->init_total) {
		u32 h = read32_le(buf + cfg->s1);

		if (hrange_contains(&cfg->h1, h)) {
			write32_le(buf + cfg->s1, WG_HANDSHAKE_INIT);
			*out_len = n - cfg->s1;
			return buf + cfg->s1;
		}
	}

	if (n == cfg->resp_total) {
		u32 h = read32_le(buf + cfg->s2);

		if (hrange_contains(&cfg->h2, h)) {
			write32_le(buf + cfg->s2, WG_HANDSHAKE_RESPONSE);
			if (cfg->has_client_pub)
				recompute_mac1_response(buf + cfg->s2,
							cfg->mac1key_client);
			*out_len = n - cfg->s2;
			return buf + cfg->s2;
		}
	}

	if (n == cfg->cookie_total) {
		u32 h = read32_le(buf + cfg->s3);

		if (hrange_contains(&cfg->h3, h)) {
			write32_le(buf + cfg->s3, WG_COOKIE_REPLY);
			*out_len = n - cfg->s3;
			return buf + cfg->s3;
		}
	}

	/* Transport data: variable size, checked last */
	if (n >= cfg->s4 + WG_TRANSPORT_MIN) {
		u32 h = read32_le(buf + cfg->s4);

		if (hrange_contains(&cfg->h4, h)) {
			write32_le(buf + cfg->s4, WG_TRANSPORT_DATA);
			*out_len = n - cfg->s4;
			return buf + cfg->s4;
		}
	}

	return NULL;
}

int generate_junk(const awg_config_t *cfg, u8 *junk_buf,
		  int *sizes, int max_sizes)
{
	int i, jc, jmin, jmax, span;
	u32 r;

	if (cfg->jc <= 0 || cfg->jmax <= 0)
		return 0;

	jc = cfg->jc;
	if (jc > max_sizes)
		jc = max_sizes;

	jmin = cfg->jmin > 0 ? cfg->jmin : 1;
	jmax = cfg->jmax >= jmin ? cfg->jmax : jmin;
	span = jmax - jmin + 1;

	for (i = 0; i < jc; i++) {
		if (span > 1) {
			get_random_bytes(&r, sizeof(r));
			sizes[i] = jmin + (int)(r % span);
		} else {
			sizes[i] = jmin;
		}
	}
	return jc;
}
