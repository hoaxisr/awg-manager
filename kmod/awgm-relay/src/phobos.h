/* SPDX-License-Identifier: GPL-2.0 */
/*
 * Протокол Phobos (клиент, версия 1): кодирование пакета, кадры STUN/RTP,
 * служебка STUN. Чистые функции без состояния ядра — собираются и в модуле,
 * и в хост-тестах. Независимая реализация по описанию провода (спека §3.7),
 * не порт кода wg-obfuscator (GPL-3, §3.8).
 */
#ifndef AWGMR_PHOBOS_H
#define AWGMR_PHOBOS_H

#ifdef __KERNEL__
#include <linux/types.h>
#include <linux/errno.h>
#include <linux/string.h>
#else
#include <stdint.h>
#include <errno.h>
#include <string.h>
typedef uint8_t u8;
typedef uint16_t u16;
typedef uint32_t u32;
#endif

#define AWGMR_KEY_MAX        255
#define AWGMR_PAD_TOTAL_MAX  1024
#define AWGMR_PAD_HS_MAX     512
#define AWGMR_STUN_HDR       24
#define AWGMR_RTP_HDR        12
#define AWGMR_FRAME_MAX      AWGMR_STUN_HDR
#define AWGMR_STUN_REQ_LEN   28   /* 20 + FINGERPRINT 8 */
#define AWGMR_STUN_OK_LEN    40   /* 20 + XOR-MAPPED-ADDRESS 12 + FINGERPRINT 8 */

enum awgmr_mask {
	AWGMR_MASK_NONE = 0,
	AWGMR_MASK_STUN,
	AWGMR_MASK_MEDIA,
};

/* Источник случайности: в ядре — prandom_u32, в тестах — детерминированный. */
struct awgmr_rng {
	u32 (*next)(void *ctx);
	void *ctx;
};

struct awgmr_phobos_cfg {
	u8 key[AWGMR_KEY_MAX];
	int key_len;         /* 1..AWGMR_KEY_MAX, эффективный ключ */
	int max_dummy;       /* 0..1024 */
	int obf_bytes;       /* эффективный obfuscate-bytes, 0 = весь пакет */
	enum awgmr_mask mask;
};

struct awgmr_rtp_state {
	u16 seq;
	u32 ts;
	u32 ssrc;
	u16 ts_step;
	u8 pt;
};

/* Что пришло от сервера после снятия кадра. */
enum awgmr_in {
	AWGMR_IN_DATA,      /* закодированный пакет: buf[off .. off+plen) */
	AWGMR_IN_BIND_REQ,  /* Binding Request сервера — ответить Binding Success */
	AWGMR_IN_BIND_OK,   /* Binding Success сервера — поглотить */
	AWGMR_IN_DROP,      /* не разобралось — поглотить со счётчиком */
};

void awgmr_crc8_init(void);
void awgmr_keystream_xor(u8 *buf, int n, const u8 *key, int key_len);
u32 awgmr_crc32(const u8 *p, int n);

/* encode: пакет WG в buf[0..len), cap — место в буфере от buf.
 * Возвращает новую длину или -EINVAL. */
int awgmr_encode(const struct awgmr_phobos_cfg *cfg, u8 *buf, int len, int cap,
		 const struct awgmr_rng *rng);
/* decode: возвращает длину пакета WG или -EINVAL. */
int awgmr_decode(const struct awgmr_phobos_cfg *cfg, u8 *buf, int len);

int awgmr_stun_frame(u8 *hdr, int payload_len, const struct awgmr_rng *rng);
void awgmr_rtp_init(struct awgmr_rtp_state *st, const struct awgmr_rng *rng);
int awgmr_rtp_frame(u8 *hdr, struct awgmr_rtp_state *st);

enum awgmr_in awgmr_unframe(enum awgmr_mask mask, const u8 *buf, int len,
			    int *off, int *plen);

int awgmr_stun_binding_request(u8 *out, const struct awgmr_rng *rng);
/* addr, port — адрес отправителя запроса в сетевом порядке байт. */
int awgmr_stun_binding_success(u8 *out, const u8 *req, int req_len,
			       const u8 addr[4], const u8 port[2]);

#endif
