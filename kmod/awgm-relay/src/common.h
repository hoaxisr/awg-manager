/* SPDX-License-Identifier: GPL-2.0 */
/* Общие типы модуля: собираются и в ядре, и в хост-тестах. */
#ifndef AWGMR_COMMON_H
#define AWGMR_COMMON_H

#ifdef __KERNEL__
#include <linux/types.h>
#include <linux/errno.h>
#include <linux/string.h>
#include <linux/kernel.h>
#include <linux/compiler.h>
#else
#include <stdint.h>
#include <stdbool.h>
#include <stdio.h>
#include <errno.h>
#include <string.h>
typedef uint8_t u8;
typedef uint16_t u16;
typedef uint32_t u32;
#define READ_ONCE(x) (*(volatile __typeof__(x) *)&(x))
#define WRITE_ONCE(x, v) (*(volatile __typeof__(x) *)&(x) = (v))
#endif

/* Источник случайности: в ядре — prandom_u32, в тестах — детерминированный. */
struct awgmr_rng {
	u32 (*next)(void *ctx);
	void *ctx;
};

#endif
