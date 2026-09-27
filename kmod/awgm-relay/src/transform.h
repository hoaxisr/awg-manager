/* SPDX-License-Identifier: GPL-2.0 */
/*
 * Таблица трансформаций датаграмм (UDP). Каркас relay.c протокола не знает:
 * сокеты, потоки, очередь и teardown общие, а что делать с пакетом — решает
 * трансформация, выбранная ключом transform=<name> строки add. Новый
 * протокол — свой файл с struct awgmr_transform и строка в реестре
 * (transform.c). Потоковые протоколы (TCP) сюда не ложатся: у них другая
 * модель (поток, соединение на клиента) — отдельный тип трансформации.
 */
#ifndef AWGMR_TRANSFORM_H
#define AWGMR_TRANSFORM_H

#include "common.h"

#define AWGMR_TPRIV_MAX     384  /* состояние трансформации на экземпляр */
#define AWGMR_HEADROOM_MAX  64   /* сколько трансформация может дописать перед пакетом */

/* Что каркас даёт трансформации. */
struct awgmr_tctx {
	void *priv;                     /* состояние трансформации экземпляра */
	const struct awgmr_rng *rng;
	/* служебка серверу (process-контекст: c2s, s2c, work) */
	int (*send_remote)(void *relay, const u8 *buf, int len);
	void *relay;
	u8 peer_ip[4];                  /* адрес сервера, сетевой порядок */
	u8 peer_port[2];                /* порт сервера, сетевой порядок */
};

struct awgmr_transform {
	const char *name;
	/* один раз при загрузке модуля (таблицы и т.п.); может быть NULL */
	void (*global_init)(void);
	int priv_size;                  /* ≤ AWGMR_TPRIV_MAX */
	int headroom;                   /* ≤ AWGMR_HEADROOM_MAX */
	/* свой ключ строки add; 0 или -EINVAL (незнакомый ключ — тоже отказ) */
	int (*parse)(void *priv, const char *key, const char *val);
	/* 0 — обязательные ключи на месте */
	int (*ready)(const void *priv);
	/* после add, до потоков; может быть NULL */
	void (*init)(struct awgmr_tctx *t);
	/* период on_timer, 0 — таймера нет */
	unsigned int (*timer_ms)(const void *priv);
	/*
	 * c2s: пакет клиента в payload[0..len), перед ним свободно
	 * AWGMR_HEADROOM_MAX байт, после — до cap. Возвращает длину кадра,
	 * начинающегося в *start; <0 — дроп.
	 */
	int (*encode)(struct awgmr_tctx *t, u8 *payload, int len, int cap, u8 **start);
	/* s2c: >0 — пакет клиенту в buf[*off..], 0 — поглощено, <0 — дроп */
	int (*decode)(struct awgmr_tctx *t, u8 *buf, int len, int *off);
	/* может быть NULL */
	void (*on_timer)(struct awgmr_tctx *t);
	/* параметры для /proc list (без секретов); длина или <0 */
	int (*describe)(const void *priv, char *buf, int len);
};

const struct awgmr_transform *awgmr_transform_find(const char *name);
/* global_init всех трансформаций реестра — из module_init. */
void awgmr_transforms_init(void);

/* Помощник разбора чисел для трансформаций: 0..max, иначе -EINVAL. */
int awgmr_parse_uint(const char *s, int max, int *out);

#endif
