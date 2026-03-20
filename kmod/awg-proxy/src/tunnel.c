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

/*
 * Parse hex string into binary buffer.
 * Returns number of bytes written, or -1 on error.
 */
static int parse_hex(const char *hex, u8 *out, int maxlen)
{
	int i = 0;
	int hi, lo;

	while (hex[0] && hex[1] && i < maxlen) {
		hi = hex_to_bin(hex[0]);
		lo = hex_to_bin(hex[1]);
		if (hi < 0 || lo < 0)
			return -1;
		out[i++] = (hi << 4) | lo;
		hex += 2;
	}
	return i;
}

/*
 * Parse config line format:
 *   IP:PORT H1=min-max H2=min-max H3=min-max H4=min-max
 *           S1=N S2=N S3=N S4=N Jc=N Jmin=N Jmax=N
 *           PUB_SERVER=hex PUB_CLIENT=hex
 *           I1="template" I2="template" ...
 *
 * All params after IP:PORT are optional (defaults to identity = standard WG).
 * Fills the provided awg_config_t; calls config_compute() at the end.
 * Returns 0 on success, negative errno on error.
 */
int awg_config_parse(const char *config_line, awg_config_t *cfg)
{
	char ip_str[64];
	__be32 ip;
	__be16 port;
	int port_int;
	const char *p;
	char *colon;
	int i;

	/* Parse IP:PORT */
	p = config_line;
	while (*p == ' ' || *p == '\t')
		p++;

	i = 0;
	while (*p && *p != ' ' && *p != '\t' && i < (int)sizeof(ip_str) - 1)
		ip_str[i++] = *p++;
	ip_str[i] = '\0';

	colon = strrchr(ip_str, ':');
	if (!colon)
		return -EINVAL;
	*colon = '\0';

	ip = in_aton(ip_str);
	if (kstrtoint(colon + 1, 10, &port_int) ||
	    port_int <= 0 || port_int > 65535)
		return -EINVAL;
	port = htons(port_int);

	/* Initialize with identity defaults (standard WG, no transformation) */
	memset(cfg, 0, sizeof(*cfg));
	cfg->remote_ip = ip;
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

		/* Parse known keys */
		if (strcmp(key, "H1") == 0) {
			if (sscanf(val, "%u-%u",
				   &cfg->h1.min, &cfg->h1.max) == 1)
				cfg->h1.max = cfg->h1.min;
		} else if (strcmp(key, "H2") == 0) {
			if (sscanf(val, "%u-%u",
				   &cfg->h2.min, &cfg->h2.max) == 1)
				cfg->h2.max = cfg->h2.min;
		} else if (strcmp(key, "H3") == 0) {
			if (sscanf(val, "%u-%u",
				   &cfg->h3.min, &cfg->h3.max) == 1)
				cfg->h3.max = cfg->h3.min;
		} else if (strcmp(key, "H4") == 0) {
			if (sscanf(val, "%u-%u",
				   &cfg->h4.min, &cfg->h4.max) == 1)
				cfg->h4.max = cfg->h4.min;
		} else if (strcmp(key, "S1") == 0) {
			kstrtoint(val, 10, &cfg->s1);
		} else if (strcmp(key, "S2") == 0) {
			kstrtoint(val, 10, &cfg->s2);
		} else if (strcmp(key, "S3") == 0) {
			kstrtoint(val, 10, &cfg->s3);
		} else if (strcmp(key, "S4") == 0) {
			kstrtoint(val, 10, &cfg->s4);
		} else if (strcmp(key, "Jc") == 0) {
			kstrtoint(val, 10, &cfg->jc);
		} else if (strcmp(key, "Jmin") == 0) {
			kstrtoint(val, 10, &cfg->jmin);
		} else if (strcmp(key, "Jmax") == 0) {
			kstrtoint(val, 10, &cfg->jmax);
		} else if (strcmp(key, "PUB_SERVER") == 0) {
			parse_hex(val, cfg->server_pub, 32);
		} else if (strcmp(key, "PUB_CLIENT") == 0) {
			parse_hex(val, cfg->client_pub, 32);
		} else if (strcmp(key, "BIND") == 0) {
			strscpy(cfg->bind_iface, val, sizeof(cfg->bind_iface));
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
