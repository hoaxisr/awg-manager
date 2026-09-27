/* SPDX-License-Identifier: GPL-2.0 */
/* Разбор строки /proc/awgm_relay/add (спека §3.3). Чистая функция. */
#ifndef AWGMR_CONFIG_H
#define AWGMR_CONFIG_H

#include "transform.h"

#define AWGMR_LINE_MAX 1024

struct awgmr_cfg {
	u16 listen_port;          /* 127.0.0.1:PORT, хостовый порядок */
	u8 target_ip[4];          /* сетевой порядок */
	u16 target_port;          /* хостовый порядок */
	const struct awgmr_transform *t;
	/* состояние трансформации; в нём может быть ключ — затирать при снятии */
	u8 tpriv[AWGMR_TPRIV_MAX] __attribute__((aligned(8)));
};

/* 0 или -EINVAL. line изменяется (токенизация на месте). */
int awgmr_config_parse(char *line, struct awgmr_cfg *cfg);
/* "127.0.0.1:PORT" -> порт; для del. */
int awgmr_parse_listen(const char *s, u16 *port);

#endif
