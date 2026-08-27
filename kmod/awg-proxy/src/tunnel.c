// SPDX-License-Identifier: GPL-2.0
/*
 * AWG Proxy — tunnel configuration parsing.
 * Parses config lines from procfs into awg_config_t structs.
 * Calls config_compute() to precompute derived fields and cps_parse()
 * to parse CPS templates into structured segments.
 */

#include <linux/kernel.h>
#include <linux/slab.h>
#include <linux/inet.h>

#include "tunnel.h"
#include "blake2s.h"
#include "cps.h"

static int parse_hex_exact(const char *hex, u8 *out, int len)
{
	int i, hi, lo;

	for (i = 0; i < len; i++) {
		if (!hex[0] || !hex[1])
			return -EINVAL;
		hi = hex_to_bin(hex[0]);
		lo = hex_to_bin(hex[1]);
		if (hi < 0 || lo < 0)
			return -EINVAL;
		out[i] = (hi << 4) | lo;
		hex += 2;
	}

	return *hex ? -EINVAL : 0;
}

static int hrange_overlaps(const hrange_t *a, const hrange_t *b)
{
	return a->min <= b->max && b->min <= a->max;
}

/*
 * Parse an endpoint token — see tunnel.h for the accepted forms.
 *
 * The IPv4 branch is byte-identical to the historical parse (strrchr(':'),
 * in_aton on whatever is left of the colon, kstrtoint on the port), so
 * every input that used to be accepted still is, with the single deliberate
 * exception of bare-IPv6-without-brackets: that used to be silently
 * mis-parsed by in_aton into a bogus IPv4 slot and now fails loudly.
 */
int awg_endpoint_parse(const char *str, struct awg_endpoint_addr *addr,
		       __be16 *port)
{
	char buf[64];
	const char *port_str;
	int port_int;

	memset(addr, 0, sizeof(*addr));

	if (strscpy(buf, str, sizeof(buf)) < 0)
		return -EINVAL;

	if (buf[0] == '[') {
		/* Bracketed IPv6: "[2001:db8::1]:51820" */
		char *close = strchr(buf, ']');

		if (!close || close == buf + 1 || close[1] != ':')
			return -EINVAL;
		*close = '\0';
		if (in6_pton(buf + 1, -1, addr->ip6.s6_addr, -1, NULL) != 1) {
			pr_warn("awg_proxy: invalid IPv6 endpoint address\n");
			return -EINVAL;
		}
		addr->family = AF_INET6;
		port_str = close + 2;
	} else {
		char *colon = strrchr(buf, ':');

		if (!colon)
			return -EINVAL;
		if (strchr(buf, ':') != colon) {
			pr_warn("awg_proxy: IPv6 endpoint must be written as [addr]:port\n");
			return -EINVAL;
		}
		*colon = '\0';
		addr->family = AF_INET;
		addr->ip4 = in_aton(buf);
		port_str = colon + 1;
	}

	if (kstrtoint(port_str, 10, &port_int) ||
	    port_int <= 0 || port_int > 65535)
		return -EINVAL;
	*port = htons(port_int);
	return 0;
}

/*
 * Parse config line format:
 *   ENDPOINT H1=min-max H2=min-max H3=min-max H4=min-max
 *           S1=N S2=N S3=N S4=N Jc=N Jmin=N Jmax=N
 *           PUB_SERVER=hex PUB_CLIENT=hex
 *           I1="template" I2="template" ...
 *
 * ENDPOINT is "A.B.C.D:PORT" or "[IPV6]:PORT" — see awg_endpoint_parse.
 * All params after ENDPOINT are optional (defaults to identity = standard WG).
 * Fills the provided awg_config_t; calls config_compute() at the end.
 * Returns 0 on success, negative errno on error.
 */
int awg_config_parse(const char *config_line, awg_config_t *cfg)
{
	char ip_str[64];
	struct awg_endpoint_addr addr;
	__be16 port;
	const char *p;
	int i, ret;

	/* Parse ENDPOINT ("IP:PORT" or "[v6]:PORT") */
	p = config_line;
	while (*p == ' ' || *p == '\t')
		p++;

	i = 0;
	while (*p && *p != ' ' && *p != '\t' && i < (int)sizeof(ip_str) - 1)
		ip_str[i++] = *p++;
	ip_str[i] = '\0';

	ret = awg_endpoint_parse(ip_str, &addr, &port);
	if (ret)
		return ret;

	/* Initialize with identity defaults (standard WG, no transformation) */
	memset(cfg, 0, sizeof(*cfg));
	cfg->remote_addr = addr;
	cfg->remote_port = port;
	cfg->h1.min = 1; cfg->h1.max = 1;
	cfg->h2.min = 2; cfg->h2.max = 2;
	cfg->h3.min = 3; cfg->h3.max = 3;
	cfg->h4.min = 4; cfg->h4.max = 4;

	/* Parse remaining key=value params */
	while (*p) {
		char key[32];
		char *val;
		int ki = 0, vi = 0;
		int bad = 0;

		val = kmalloc(4096, GFP_KERNEL);
		if (!val)
			break;

		while (*p == ' ' || *p == '\t')
			p++;
		if (!*p) {
			kfree(val);
			break;
		}

		/* Read key */
		while (*p && *p != '=' && ki < (int)sizeof(key) - 1)
			key[ki++] = *p++;
		key[ki] = '\0';
		if (*p != '=') {
			kfree(val);
			break;
		}
		p++;

		/* Read value (may be quoted) */
		if (*p == '"') {
			p++;
			while (*p && *p != '"' && vi < 4095)
				val[vi++] = *p++;
			if (*p == '"')
				p++;
		} else {
			while (*p && *p != ' ' && *p != '\t' && vi < 4095)
				val[vi++] = *p++;
		}
		val[vi] = '\0';

		/* Parse known keys. A malformed value (kstrtoint error, or an
		 * H range that is neither "N" nor "N-M") is fail-closed: set
		 * bad and reject the whole config below, rather than silently
		 * leaving the field at its zero/default and disabling the
		 * obfuscation parameter the user asked for. */
		if (strcmp(key, "H1") == 0) {
			int r = sscanf(val, "%u-%u", &cfg->h1.min, &cfg->h1.max);

			if (r == 1)
				cfg->h1.max = cfg->h1.min;
			else if (r != 2)
				bad = 1;
		} else if (strcmp(key, "H2") == 0) {
			int r = sscanf(val, "%u-%u", &cfg->h2.min, &cfg->h2.max);

			if (r == 1)
				cfg->h2.max = cfg->h2.min;
			else if (r != 2)
				bad = 1;
		} else if (strcmp(key, "H3") == 0) {
			int r = sscanf(val, "%u-%u", &cfg->h3.min, &cfg->h3.max);

			if (r == 1)
				cfg->h3.max = cfg->h3.min;
			else if (r != 2)
				bad = 1;
		} else if (strcmp(key, "H4") == 0) {
			int r = sscanf(val, "%u-%u", &cfg->h4.min, &cfg->h4.max);

			if (r == 1)
				cfg->h4.max = cfg->h4.min;
			else if (r != 2)
				bad = 1;
		} else if (strcmp(key, "S1") == 0) {
			bad = kstrtoint(val, 10, &cfg->s1) != 0;
		} else if (strcmp(key, "S2") == 0) {
			bad = kstrtoint(val, 10, &cfg->s2) != 0;
		} else if (strcmp(key, "S3") == 0) {
			bad = kstrtoint(val, 10, &cfg->s3) != 0;
		} else if (strcmp(key, "S4") == 0) {
			bad = kstrtoint(val, 10, &cfg->s4) != 0;
		} else if (strcmp(key, "Jc") == 0) {
			bad = kstrtoint(val, 10, &cfg->jc) != 0;
		} else if (strcmp(key, "Jmin") == 0) {
			bad = kstrtoint(val, 10, &cfg->jmin) != 0;
		} else if (strcmp(key, "Jmax") == 0) {
			bad = kstrtoint(val, 10, &cfg->jmax) != 0;
		} else if (strcmp(key, "PUB_SERVER") == 0) {
			if (parse_hex_exact(val, cfg->server_pub, 32)) {
				pr_warn("awg_proxy: invalid PUB_SERVER\n");
				kfree(val);
				goto out_invalid;
			}
		} else if (strcmp(key, "PUB_CLIENT") == 0) {
			if (parse_hex_exact(val, cfg->client_pub, 32)) {
				pr_warn("awg_proxy: invalid PUB_CLIENT\n");
				kfree(val);
				goto out_invalid;
			}
		} else if (strcmp(key, "BIND") == 0) {
			strscpy(cfg->bind_iface, val, sizeof(cfg->bind_iface));
		} else if (strcmp(key, "HP_KEY") == 0) {
			if (parse_hex_exact(val, cfg->hp_key, 32)) {
				pr_warn("awg_proxy: invalid HP_KEY\n");
				kfree(val);
				goto out_invalid;
			}
			cfg->hp_key_set = 1;
		} else if (strcmp(key, "RT") == 0) {
			bad = kstrtoint(val, 10, &cfg->random_trailers) != 0;
		} else if (key[0] == 'I' && key[1] >= '1' && key[1] <= '5' &&
			   key[2] == '\0') {
			int idx = key[1] - '1';
			cps_template_t *tmpl;

			tmpl = kmalloc(sizeof(*tmpl), GFP_KERNEL);
			if (tmpl) {
				if (cps_parse(val, tmpl) == 0) {
					cfg->cps[idx] = tmpl;
				} else {
					kfree(tmpl);
					pr_warn("awg_proxy: failed to parse %s\n",
						key);
				}
			}
		}
		kfree(val);

		if (bad) {
			pr_warn("awg_proxy: invalid value for %s\n", key);
			goto out_invalid;
		}
	}

	/* Validate config ranges */
	if (cfg->s1 < 0 || cfg->s2 < 0 || cfg->s3 < 0 || cfg->s4 < 0 ||
	    cfg->s1 + WG_INIT_SIZE > 1500 ||
	    cfg->s2 + WG_RESP_SIZE > 1500 ||
	    cfg->s3 + WG_COOKIE_SIZE > 1500 ||
	    cfg->s4 > 1024) {
		pr_warn("awg_proxy: S1-S4 out of range\n");
		goto out_invalid;
	}
	/* Header protection uses the first AWG_HP_MIN_PADDING padding bytes as the
	 * ChaCha20 nonce, so every message class must carry at least that much. */
	if (cfg->hp_key_set &&
	    (cfg->s1 < AWG_HP_MIN_PADDING || cfg->s2 < AWG_HP_MIN_PADDING ||
	     cfg->s3 < AWG_HP_MIN_PADDING || cfg->s4 < AWG_HP_MIN_PADDING)) {
		pr_warn("awg_proxy: S1-S4 must be >= %d with header protection\n",
			AWG_HP_MIN_PADDING);
		goto out_invalid;
	}
	if (cfg->jc < 0 || cfg->jc > AWG_MAX_JC) {
		pr_warn("awg_proxy: Jc out of range (%d)\n", cfg->jc);
		goto out_invalid;
	}
	if (cfg->jmin < 0 || cfg->jmin > 1500 ||
	    cfg->jmax < 0 || cfg->jmax > 1500) {
		pr_warn("awg_proxy: Jmin/Jmax out of range\n");
		goto out_invalid;
	}
	if (cfg->h1.min > cfg->h1.max || cfg->h2.min > cfg->h2.max ||
	    cfg->h3.min > cfg->h3.max || cfg->h4.min > cfg->h4.max) {
		pr_warn("awg_proxy: H range min > max\n");
		goto out_invalid;
	}
	if (hrange_overlaps(&cfg->h1, &cfg->h2) ||
	    hrange_overlaps(&cfg->h1, &cfg->h3) ||
	    hrange_overlaps(&cfg->h1, &cfg->h4) ||
	    hrange_overlaps(&cfg->h2, &cfg->h3) ||
	    hrange_overlaps(&cfg->h2, &cfg->h4) ||
	    hrange_overlaps(&cfg->h3, &cfg->h4)) {
		pr_warn("awg_proxy: H ranges must not overlap\n");
		goto out_invalid;
	}

	/* Compute derived fields (MAC1 keys, totals, fast-path flags) */
	config_compute(cfg);

	return 0;

out_invalid:
	awg_config_free(cfg);
	return -EINVAL;
}

void awg_config_free(awg_config_t *cfg)
{
	int i;

	for (i = 0; i < 5; i++) {
		kfree(cfg->cps[i]);
		cfg->cps[i] = NULL;
	}
}
