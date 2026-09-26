/* Хост-тесты протокола Phobos: векторы спеки §3.7, круг, структура, разбор. */
#include <assert.h>
#include <stdio.h>
#include "../src/phobos.h"

static u32 xs_state;
static u32 xs_next(void *ctx) { (void)ctx; u32 x = xs_state; x ^= x << 13; x ^= x >> 17; x ^= x << 5; return xs_state = x; }
static const struct awgmr_rng rng = { xs_next, NULL };
static u32 zero_next(void *ctx) { (void)ctx; return 0; }
static const struct awgmr_rng rng0 = { zero_next, NULL };

static const char KEY[] = "benchkey-0123456789";
static struct awgmr_phobos_cfg cfg(int md, int nb, enum awgmr_mask m)
{
	struct awgmr_phobos_cfg c = { .key_len = sizeof(KEY) - 1, .max_dummy = md, .obf_bytes = nb, .mask = m };
	memcpy(c.key, KEY, sizeof(KEY) - 1);
	return c;
}

static void test_vectors(void)
{
	static const u8 ks16[] = { 0xb3,0x9e,0x41,0x49,0x76,0xb6,0xa1,0x43,0x7f,0x80,0x36,0x87,0x09,0xc4,0xb2,0xc9 };
	static const u8 ksk[] = { 0xf8,0x4e,0xa1,0x09 };
	static const u8 enc32[] = { 0xf6,0xa5,0x20,0x4c,0xef,0x3d,0x4b,0x52,0x6b,0x36,0x13,0xad,0x87,0xba,0xfa,0xdc,0x3d,0xb5,0xf8,0x57,0x02,0x9b,0x08,0x9b,0xc6,0x60,0x90,0x6e,0x63,0x42,0x23,0xa5,0x93,0xe9,0x86 };
	static const u8 p16[] = { 0x8c,0xa5,0x41,0x49,0x7a,0xb9,0xb3,0x56,0x67,0x9b,0x28,0xa6,0x2d,0xe3,0x98,0xe4 };
	struct awgmr_phobos_cfg c = cfg(0, 0, AWGMR_MASK_NONE);
	u8 b[128];
	int i, n;

	memset(b, 0, 16);
	awgmr_keystream_xor(b, 16, (const u8 *)KEY, sizeof(KEY) - 1);
	assert(!memcmp(b, ks16, 16));
	memset(b, 0, 4);
	awgmr_keystream_xor(b, 4, (const u8 *)"k", 1);
	assert(!memcmp(b, ksk, 4));

	memcpy(b, enc32, sizeof(enc32));
	n = awgmr_decode(&c, b, sizeof(enc32));
	assert(n == 32 && b[0] == 4 && !b[1] && !b[2] && !b[3]);
	for (i = 4; i < 32; i++)
		assert(b[i] == i);

	c = cfg(0, 16, AWGMR_MASK_NONE);
	memcpy(b, p16, 16);
	for (i = 16; i < 100; i++)
		b[i] = (u8)(3 * i);
	n = awgmr_decode(&c, b, 100);
	assert(n == 100 && b[0] == 4 && !b[1] && !b[2] && !b[3]);
	for (i = 4; i < 100; i++)
		assert(b[i] == (u8)(3 * i));
}

static void test_roundtrip_and_structure(void)
{
	static const int lens[] = { 4, 32, 92, 148, 1000, 1023, 1024, 1420 };
	static const int mds[] = { 0, 4, 1024 };
	static const int nbs[] = { 0, 16, 2000 };
	u8 orig[2048], b[2048];
	int li, mi, ni, t, k, n, m;

	xs_state = 0xC0FFEE;
	for (li = 0; li < 8; li++) for (mi = 0; mi < 3; mi++) for (ni = 0; ni < 3; ni++) for (t = 1; t <= 4; t++) for (k = 0; k < 50; k++) {
		struct awgmr_phobos_cfg c = cfg(mds[mi], nbs[ni], AWGMR_MASK_NONE);
		int len = lens[li], x;

		memset(orig, 0, sizeof(orig));
		orig[0] = (u8)t;
		for (x = 4; x < len; x++)
			orig[x] = (u8)xs_next(NULL);
		memcpy(b, orig, len);
		n = awgmr_encode(&c, b, len, sizeof(b), &rng);
		assert(n >= len && n <= (len < 1024 ? 1024 : len) && n <= len + 1024);
		m = awgmr_decode(&c, b, n);
		assert(m == len && !memcmp(b, orig, len));
	}
	/* r из [1,255]: при ГПСЧ = 0 r = 1, не 0 (§3.7 п.2) */
	{
		struct awgmr_phobos_cfg c = cfg(0, 0, AWGMR_MASK_NONE);
		memset(b, 0, 8); b[0] = 4;
		n = awgmr_encode(&c, b, 8, sizeof(b), &rng0);
		awgmr_keystream_xor(b, n, c.key, c.key_len);
		assert(b[1] == 1 && b[0] == (4 ^ 1));
	}
	/* частичная обфускация: паддинга нет, хвост не тронут */
	{
		struct awgmr_phobos_cfg c = cfg(1024, 16, AWGMR_MASK_NONE);
		memset(b, 0xAB, 100); b[0] = 4; b[1] = b[2] = b[3] = 0;
		n = awgmr_encode(&c, b, 100, sizeof(b), &rng);
		assert(n == 100 && b[50] == 0xAB);
	}
	/* нехватка места — отказ, не запись за буфер */
	{
		struct awgmr_phobos_cfg c = cfg(0, 0, AWGMR_MASK_NONE);
		memset(b, 0, 148); b[0] = 1;
		xs_state = 7;
		for (k = 0; k < 200; k++) {
			memset(b, 0, 148); b[0] = 1;
			n = awgmr_encode(&c, b, 148, 150, &rng);
			assert(n == -EINVAL || n <= 150);
		}
	}
}

static void test_decode_rejects(void)
{
	struct awgmr_phobos_cfg c = cfg(0, 0, AWGMR_MASK_NONE);
	u8 b[64];

	assert(awgmr_decode(&c, b, 3) == -EINVAL);
	/* версия 0: после снятия гаммы — чистый WG → ошибка */
	memset(b, 0, 32); b[0] = 4;
	awgmr_keystream_xor(b, 32, c.key, c.key_len);
	assert(awgmr_decode(&c, b, 32) == -EINVAL);
	/* паддинг длиннее пакета */
	memset(b, 0, 32); b[0] = 4 ^ 9; b[1] = 9; b[2] = 40;
	awgmr_keystream_xor(b, 32, c.key, c.key_len);
	assert(awgmr_decode(&c, b, 32) == -EINVAL);
}

static void test_framing(void)
{
	u8 b[256], out[64];
	int off, pl, n;
	struct awgmr_rtp_state st;

	xs_state = 99;
	n = awgmr_stun_frame(b, 100, &rng);
	assert(n == 24 && b[0] == 0x01 && b[1] == 0x15 && !b[2] && !b[3]);
	assert(b[4] == 0x21 && b[7] == 0x42 && b[20] == 0x00 && b[21] == 0x13 && b[22] == 0 && b[23] == 100);
	assert(awgmr_unframe(AWGMR_MASK_STUN, b, 124, &off, &pl) == AWGMR_IN_DATA && off == 24 && pl == 100);
	assert(awgmr_unframe(AWGMR_MASK_STUN, b, 123, &off, &pl) == AWGMR_IN_DROP);
	assert(awgmr_unframe(AWGMR_MASK_STUN, b, 130, &off, &pl) == AWGMR_IN_DATA && pl == 100); /* хвост игнор */

	awgmr_rtp_init(&st, &rng);
	assert(st.ssrc && st.pt >= 96 && st.pt <= 127);
	{
		u16 s0 = st.seq; u32 t0 = st.ts;
		n = awgmr_rtp_frame(b, &st);
		assert(n == 12 && b[0] == 0x80 && b[1] == (0x80 | st.pt));
		assert(((b[2] << 8) | b[3]) == s0 && st.seq == (u16)(s0 + 1) && st.ts == t0 + st.ts_step);
	}
	assert(awgmr_unframe(AWGMR_MASK_MEDIA, b, 40, &off, &pl) == AWGMR_IN_DATA && off == 12 && pl == 28);
	assert(awgmr_unframe(AWGMR_MASK_MEDIA, b, 15, &off, &pl) == AWGMR_IN_DROP);

	n = awgmr_stun_binding_request(b, &rng);
	assert(n == 28 && b[0] == 0 && b[1] == 1 && b[2] == 0 && b[3] == 8);
	assert(b[20] == 0x80 && b[21] == 0x28);
	{
		u32 fp = (u32)b[24] << 24 | b[25] << 16 | b[26] << 8 | b[27];
		assert(fp == (awgmr_crc32(b, 20) ^ 0x5354554Eu));
	}
	assert(awgmr_unframe(AWGMR_MASK_MEDIA, b, n, &off, &pl) == AWGMR_IN_BIND_REQ);
	assert(awgmr_unframe(AWGMR_MASK_STUN, b, 19, &off, &pl) == AWGMR_IN_DROP); /* короткий — без чтения txid */
	{
		static const u8 ip[4] = { 176, 109, 110, 182 }, port[2] = { 0xCA, 0x6C };
		n = awgmr_stun_binding_success(out, b, 28, ip, port);
		assert(n == 40 && out[0] == 0x01 && out[1] == 0x01 && out[3] == 20);
		assert(!memcmp(out + 8, b + 8, 12));
		assert(out[26] == (0xCA ^ 0x21) && out[28] == (176 ^ 0x21) && out[31] == (182 ^ 0x42));
		assert(awgmr_unframe(AWGMR_MASK_STUN, out, n, &off, &pl) == AWGMR_IN_BIND_OK);
		assert(awgmr_stun_binding_success(out, b, 19, ip, port) == -EINVAL);
	}
	/* CRC-32 = zlib: crc32("123456789") = 0xCBF43926 */
	assert(awgmr_crc32((const u8 *)"123456789", 9) == 0xCBF43926u);
}

int main(void)
{
	awgmr_crc8_init();
	test_vectors();
	test_roundtrip_and_structure();
	test_decode_rejects();
	test_framing();
	printf("test_phobos: OK\n");
	return 0;
}
