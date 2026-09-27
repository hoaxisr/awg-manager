// SPDX-License-Identifier: GPL-2.0
#include "config.h"

int awgmr_parse_uint(const char *s, int max, int *out)
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
	if (awgmr_parse_uint(s, 65535, &p) || p == 0)
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

#define AWGMR_TOKENS_MAX 16

/*
 * Позиционные: 127.0.0.1:PORT и A.B.C.D:PORT; затем key=value в любом
 * порядке. transform= выбирает трансформацию, остальные ключи — её (parse).
 */
int awgmr_config_parse(char *line, struct awgmr_cfg *cfg)
{
	char *p = line, *tok, *toks[AWGMR_TOKENS_MAX];
	int n = 0, i;

	memset(cfg, 0, sizeof(*cfg));
	tok = next_tok(&p);
	if (!tok || awgmr_parse_listen(tok, &cfg->listen_port))
		return -EINVAL;
	tok = next_tok(&p);
	if (!tok || parse_ip4_port(tok, cfg->target_ip, &cfg->target_port))
		return -EINVAL;
	while ((tok = next_tok(&p))) {
		char *val = strchr(tok, '=');

		if (!val || n == AWGMR_TOKENS_MAX)
			return -EINVAL;
		*val = '\0';
		if (!strcmp(tok, "transform")) {
			if (cfg->t)
				return -EINVAL;
			cfg->t = awgmr_transform_find(val + 1);
			if (!cfg->t)
				return -EINVAL;
			continue;
		}
		toks[n++] = tok;
	}
	if (!cfg->t || cfg->t->priv_size > AWGMR_TPRIV_MAX ||
	    cfg->t->headroom > AWGMR_HEADROOM_MAX)
		return -EINVAL;
	for (i = 0; i < n; i++)
		if (cfg->t->parse(cfg->tpriv, toks[i], toks[i] + strlen(toks[i]) + 1))
			return -EINVAL;
	return cfg->t->ready(cfg->tpriv);
}
