// SPDX-License-Identifier: GPL-2.0
#include "config.h"

static int parse_uint(const char *s, int max, int *out)
{
	long v = 0;

	if (!*s)
		return -EINVAL;
	for (; *s; s++) {
		if (*s < '0' || *s > '9')
			return -EINVAL;
		v = v * 10 + (*s - '0');
		if (v > max)
			return -EINVAL;
	}
	*out = (int)v;
	return 0;
}

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

/* "A.B.C.D:PORT" */
static int parse_ip4_port(const char *s, u8 ip[4], u16 *port)
{
	int i, v, p;

	for (i = 0; i < 4; i++) {
		v = 0;
		if (*s < '0' || *s > '9')
			return -EINVAL;
		while (*s >= '0' && *s <= '9') {
			v = v * 10 + (*s++ - '0');
			if (v > 255)
				return -EINVAL;
		}
		ip[i] = (u8)v;
		if (*s++ != (i < 3 ? '.' : ':'))
			return -EINVAL;
	}
	if (parse_uint(s, 65535, &p) || p == 0)
		return -EINVAL;
	*port = (u16)p;
	return 0;
}

int awgmr_parse_listen(const char *s, u16 *port)
{
	u8 ip[4];

	if (parse_ip4_port(s, ip, port))
		return -EINVAL;
	if (ip[0] != 127 || ip[1] || ip[2] || ip[3] != 1)
		return -EINVAL;
	return 0;
}

static char *next_tok(char **p)
{
	char *s = *p, *t;

	while (*s == ' ')
		s++;
	if (!*s)
		return NULL;
	t = s;
	while (*s && *s != ' ')
		s++;
	if (*s)
		*s++ = '\0';
	*p = s;
	return t;
}

int awgmr_config_parse(char *line, struct awgmr_cfg *cfg)
{
	char *p = line, *tok;
	int have_transform = 0, have_key = 0, have_mask = 0, v, i;

	memset(cfg, 0, sizeof(*cfg));
	tok = next_tok(&p);
	if (!tok || awgmr_parse_listen(tok, &cfg->listen_port))
		return -EINVAL;
	tok = next_tok(&p);
	if (!tok || parse_ip4_port(tok, cfg->target_ip, &cfg->target_port))
		return -EINVAL;
	while ((tok = next_tok(&p))) {
		char *val = strchr(tok, '=');

		if (!val)
			return -EINVAL;
		*val++ = '\0';
		if (!strcmp(tok, "transform")) {
			if (strcmp(val, "phobos"))
				return -EINVAL;
			have_transform = 1;
		} else if (!strcmp(tok, "key")) {
			int n = (int)strlen(val);

			if (n == 0 || n % 2 || n / 2 > AWGMR_KEY_MAX)
				return -EINVAL;
			for (i = 0; i < n / 2; i++) {
				int hi = hexval(val[2 * i]), lo = hexval(val[2 * i + 1]);

				if (hi < 0 || lo < 0)
					return -EINVAL;
				cfg->phobos.key[i] = (u8)(hi << 4 | lo);
			}
			cfg->phobos.key_len = n / 2;
			have_key = 1;
		} else if (!strcmp(tok, "masking")) {
			if (!strcmp(val, "none"))
				cfg->phobos.mask = AWGMR_MASK_NONE;
			else if (!strcmp(val, "stun"))
				cfg->phobos.mask = AWGMR_MASK_STUN;
			else if (!strcmp(val, "media"))
				cfg->phobos.mask = AWGMR_MASK_MEDIA;
			else
				return -EINVAL;
			have_mask = 1;
		} else if (!strcmp(tok, "max-dummy")) {
			if (parse_uint(val, AWGMR_PAD_TOTAL_MAX, &v))
				return -EINVAL;
			cfg->phobos.max_dummy = v;
		} else if (!strcmp(tok, "obfuscate-bytes")) {
			if (parse_uint(val, 65535, &v))
				return -EINVAL;
			cfg->phobos.obf_bytes = v;
		} else {
			return -EINVAL; /* незнакомый ключ — громко, не молча */
		}
	}
	return (have_transform && have_key && have_mask) ? 0 : -EINVAL;
}
