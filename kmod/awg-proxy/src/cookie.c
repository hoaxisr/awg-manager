// SPDX-License-Identifier: GPL-2.0
/*
 * Self-contained XChaCha20-Poly1305 AEAD for cookie_reply AAD translation.
 *
 * Replaces the kernel crypto API dependency (crypto_alloc_aead) which is
 * unavailable on Keenetic's kernel 4.9-ndm (no CONFIG_CRYPTO_CHACHA20POLY1305).
 *
 * Implements RFC 8439 (ChaCha20-Poly1305) + XChaCha20 extension (draft-irtf-
 * cfrg-xchacha) in pure C with no arch-specific assembly. All 32-bit safe
 * (Donna32 Poly1305) for MIPS targets.
 *
 * Only used for cookie_reply translation: 16-byte plaintext, 16-byte AAD.
 */

#include <linux/kernel.h>
#include <linux/string.h>
#include <linux/errno.h>
#include <asm/unaligned.h>

#include "cookie.h"

/* ---- ChaCha20 core (RFC 8439 §2.3) ---- */

static inline u32 rotl32(u32 v, int n)
{
	return (v << n) | (v >> (32 - n));
}

#define QR(a, b, c, d) do {		\
	a += b; d ^= a; d = rotl32(d, 16);	\
	c += d; b ^= c; b = rotl32(b, 12);	\
	a += b; d ^= a; d = rotl32(d, 8);	\
	c += d; b ^= c; b = rotl32(b, 7);	\
} while (0)

static const u32 chacha_constants[4] = {
	0x61707865, 0x3320646e, 0x79622d32, 0x6b206574
};

static void chacha20_block(u32 out[16], const u32 in[16])
{
	int i;
	u32 x[16];

	memcpy(x, in, 64);
	for (i = 0; i < 10; i++) {
		QR(x[0], x[4], x[ 8], x[12]);
		QR(x[1], x[5], x[ 9], x[13]);
		QR(x[2], x[6], x[10], x[14]);
		QR(x[3], x[7], x[11], x[15]);
		QR(x[0], x[5], x[10], x[15]);
		QR(x[1], x[6], x[11], x[12]);
		QR(x[2], x[7], x[ 8], x[13]);
		QR(x[3], x[4], x[ 9], x[14]);
	}
	for (i = 0; i < 16; i++)
		out[i] = x[i] + in[i];
}

/* HChaCha20: XChaCha subkey derivation (draft-irtf-cfrg-xchacha §2.2) */
static void hchacha20(u8 subkey[32], const u8 key[32], const u8 nonce[16])
{
	u32 x[16];
	int i;

	x[0] = chacha_constants[0];
	x[1] = chacha_constants[1];
	x[2] = chacha_constants[2];
	x[3] = chacha_constants[3];
	for (i = 0; i < 8; i++)
		x[4 + i] = get_unaligned_le32(key + i * 4);
	for (i = 0; i < 4; i++)
		x[12 + i] = get_unaligned_le32(nonce + i * 4);

	for (i = 0; i < 10; i++) {
		QR(x[0], x[4], x[ 8], x[12]);
		QR(x[1], x[5], x[ 9], x[13]);
		QR(x[2], x[6], x[10], x[14]);
		QR(x[3], x[7], x[11], x[15]);
		QR(x[0], x[5], x[10], x[15]);
		QR(x[1], x[6], x[11], x[12]);
		QR(x[2], x[7], x[ 8], x[13]);
		QR(x[3], x[4], x[ 9], x[14]);
	}

	/* Output words 0-3 and 12-15 */
	put_unaligned_le32(x[0],  subkey);
	put_unaligned_le32(x[1],  subkey + 4);
	put_unaligned_le32(x[2],  subkey + 8);
	put_unaligned_le32(x[3],  subkey + 12);
	put_unaligned_le32(x[12], subkey + 16);
	put_unaligned_le32(x[13], subkey + 20);
	put_unaligned_le32(x[14], subkey + 24);
	put_unaligned_le32(x[15], subkey + 28);

	memzero_explicit(x, sizeof(x));
}

/* Generate ChaCha20 keystream and XOR with data (public: see cookie.h).
 * counter starts at the given value. Handles multi-block spans. Used both for
 * the XChaCha20-Poly1305 cookie AEAD below and AmneziaWG 3.0+ header protection
 * (transform.c), so there is one vetted ChaCha20 core, not two. */
void awg_chacha20_xor(const u8 key[32], const u8 nonce[12], u32 counter,
		      u8 *data, size_t len)
{
	u32 state[16], block[16];
	u8 keystream[64];
	size_t i, off = 0;

	state[0] = chacha_constants[0];
	state[1] = chacha_constants[1];
	state[2] = chacha_constants[2];
	state[3] = chacha_constants[3];
	for (i = 0; i < 8; i++)
		state[4 + i] = get_unaligned_le32(key + i * 4);
	state[13] = get_unaligned_le32(nonce);
	state[14] = get_unaligned_le32(nonce + 4);
	state[15] = get_unaligned_le32(nonce + 8);

	while (off < len) {
		size_t take = len - off;

		if (take > 64)
			take = 64;
		state[12] = counter++;
		chacha20_block(block, state);
		for (i = 0; i < 16; i++)
			put_unaligned_le32(block[i], keystream + i * 4);
		for (i = 0; i < take; i++)
			data[off + i] ^= keystream[i];
		off += take;
	}

	memzero_explicit(state, sizeof(state));
	memzero_explicit(block, sizeof(block));
	memzero_explicit(keystream, sizeof(keystream));
}

/* ---- Poly1305 (Donna32, RFC 8439 §2.5) ---- */

struct poly1305_state {
	u32 r[5];   /* clamped key */
	u32 h[5];   /* accumulator */
	u32 pad[4]; /* final addition */
};

static void poly1305_init(struct poly1305_state *st, const u8 key[32])
{
	st->r[0] = (get_unaligned_le32(key +  0))       & 0x3ffffff;
	st->r[1] = (get_unaligned_le32(key +  3) >> 2)  & 0x3ffff03;
	st->r[2] = (get_unaligned_le32(key +  6) >> 4)  & 0x3ffc0ff;
	st->r[3] = (get_unaligned_le32(key +  9) >> 6)  & 0x3f03fff;
	st->r[4] = (get_unaligned_le32(key + 12) >> 8)  & 0x00fffff;

	st->h[0] = st->h[1] = st->h[2] = st->h[3] = st->h[4] = 0;

	st->pad[0] = get_unaligned_le32(key + 16);
	st->pad[1] = get_unaligned_le32(key + 20);
	st->pad[2] = get_unaligned_le32(key + 24);
	st->pad[3] = get_unaligned_le32(key + 28);
}

static void poly1305_blocks(struct poly1305_state *st, const u8 *data,
			     size_t len, u32 hibit)
{
	u32 r0 = st->r[0], r1 = st->r[1], r2 = st->r[2];
	u32 r3 = st->r[3], r4 = st->r[4];
	u32 s1 = r1 * 5, s2 = r2 * 5, s3 = r3 * 5, s4 = r4 * 5;
	u32 h0 = st->h[0], h1 = st->h[1], h2 = st->h[2];
	u32 h3 = st->h[3], h4 = st->h[4];

	while (len >= 16) {
		u64 d0, d1, d2, d3, d4;
		u32 c;

		h0 += get_unaligned_le32(data +  0) & 0x3ffffff;
		h1 += (get_unaligned_le32(data +  3) >> 2) & 0x3ffffff;
		h2 += (get_unaligned_le32(data +  6) >> 4) & 0x3ffffff;
		h3 += (get_unaligned_le32(data +  9) >> 6) & 0x3ffffff;
		h4 += (get_unaligned_le32(data + 12) >> 8) | hibit;

		d0 = (u64)h0*r0 + (u64)h1*s4 + (u64)h2*s3 + (u64)h3*s2 + (u64)h4*s1;
		d1 = (u64)h0*r1 + (u64)h1*r0 + (u64)h2*s4 + (u64)h3*s3 + (u64)h4*s2;
		d2 = (u64)h0*r2 + (u64)h1*r1 + (u64)h2*r0 + (u64)h3*s4 + (u64)h4*s3;
		d3 = (u64)h0*r3 + (u64)h1*r2 + (u64)h2*r1 + (u64)h3*r0 + (u64)h4*s4;
		d4 = (u64)h0*r4 + (u64)h1*r3 + (u64)h2*r2 + (u64)h3*r1 + (u64)h4*r0;

		c = (u32)(d0 >> 26); h0 = (u32)d0 & 0x3ffffff; d1 += c;
		c = (u32)(d1 >> 26); h1 = (u32)d1 & 0x3ffffff; d2 += c;
		c = (u32)(d2 >> 26); h2 = (u32)d2 & 0x3ffffff; d3 += c;
		c = (u32)(d3 >> 26); h3 = (u32)d3 & 0x3ffffff; d4 += c;
		c = (u32)(d4 >> 26); h4 = (u32)d4 & 0x3ffffff; h0 += c * 5;
		c = h0 >> 26; h0 &= 0x3ffffff; h1 += c;

		data += 16;
		len -= 16;
	}

	st->h[0] = h0; st->h[1] = h1; st->h[2] = h2;
	st->h[3] = h3; st->h[4] = h4;
}

static void poly1305_final(struct poly1305_state *st, u8 tag[16])
{
	u32 h0 = st->h[0], h1 = st->h[1], h2 = st->h[2];
	u32 h3 = st->h[3], h4 = st->h[4];
	u32 c, g0, g1, g2, g3, g4, mask;
	u64 f;

	c = h1 >> 26; h1 &= 0x3ffffff; h2 += c;
	c = h2 >> 26; h2 &= 0x3ffffff; h3 += c;
	c = h3 >> 26; h3 &= 0x3ffffff; h4 += c;
	c = h4 >> 26; h4 &= 0x3ffffff; h0 += c * 5;
	c = h0 >> 26; h0 &= 0x3ffffff; h1 += c;

	g0 = h0 + 5; c = g0 >> 26; g0 &= 0x3ffffff;
	g1 = h1 + c; c = g1 >> 26; g1 &= 0x3ffffff;
	g2 = h2 + c; c = g2 >> 26; g2 &= 0x3ffffff;
	g3 = h3 + c; c = g3 >> 26; g3 &= 0x3ffffff;
	g4 = h4 + c - (1 << 26);

	mask = (g4 >> ((sizeof(u32) * 8) - 1)) - 1;
	g0 &= mask; g1 &= mask; g2 &= mask; g3 &= mask; g4 &= mask;
	mask = ~mask;
	h0 = (h0 & mask) | g0; h1 = (h1 & mask) | g1;
	h2 = (h2 & mask) | g2; h3 = (h3 & mask) | g3;
	h4 = (h4 & mask) | g4;

	h0 |= h1 << 26;
	h1 = (h1 >> 6) | (h2 << 20);
	h2 = (h2 >> 12) | (h3 << 14);
	h3 = (h3 >> 18) | (h4 << 8);

	f = (u64)h0 + st->pad[0];            h0 = (u32)f;
	f = (u64)h1 + st->pad[1] + (f >> 32); h1 = (u32)f;
	f = (u64)h2 + st->pad[2] + (f >> 32); h2 = (u32)f;
	f = (u64)h3 + st->pad[3] + (f >> 32); h3 = (u32)f;

	put_unaligned_le32(h0, tag);
	put_unaligned_le32(h1, tag + 4);
	put_unaligned_le32(h2, tag + 8);
	put_unaligned_le32(h3, tag + 12);

	memzero_explicit(st, sizeof(*st));
}

/* Process a partial block (< 16 bytes) with padding byte 0x01. */
static void poly1305_process_partial(struct poly1305_state *st,
				      const u8 *data, size_t len)
{
	u8 block[16];

	if (len == 0)
		return;
	memset(block, 0, 16);
	memcpy(block, data, len);
	block[len] = 1;
	poly1305_blocks(st, block, 16, 0); /* hibit=0 for partial */
	memzero_explicit(block, sizeof(block));
}

/* ---- AEAD (RFC 8439 §2.8 + XChaCha extension) ---- */

static void pad16(struct poly1305_state *st, size_t len)
{
	static const u8 zeros[16] = {0};
	size_t rem = len & 0xf;

	if (rem)
		poly1305_process_partial(st, zeros, 16 - rem);
}

/*
 * XChaCha20-Poly1305 encrypt in-place.
 * pt_buf must have room for pt_len + 16 bytes (tag appended).
 */
int awg_xchacha20p1305_encrypt(const u8 key[32], const u8 nonce[24],
			       const u8 *aad, size_t aad_len,
			       u8 *pt_buf, size_t pt_len)
{
	u8 subkey[32], subnonce[12];
	u8 poly_key_block[64];
	u32 poly_key_state[16];
	struct poly1305_state poly;
	u8 len_block[16];
	u8 tag[16];
	int i;

	/* XChaCha: derive subkey */
	hchacha20(subkey, key, nonce);
	memset(subnonce, 0, 4);
	memcpy(subnonce + 4, nonce + 16, 8);

	/* Generate Poly1305 key from block 0 */
	memset(poly_key_block, 0, 64);
	{
		u32 state[16];

		state[0] = chacha_constants[0];
		state[1] = chacha_constants[1];
		state[2] = chacha_constants[2];
		state[3] = chacha_constants[3];
		for (i = 0; i < 8; i++)
			state[4 + i] = get_unaligned_le32(subkey + i * 4);
		state[12] = 0; /* counter = 0 */
		state[13] = get_unaligned_le32(subnonce);
		state[14] = get_unaligned_le32(subnonce + 4);
		state[15] = get_unaligned_le32(subnonce + 8);
		chacha20_block(poly_key_state, state);
		for (i = 0; i < 16; i++)
			put_unaligned_le32(poly_key_state[i],
					   poly_key_block + i * 4);
		memzero_explicit(state, sizeof(state));
	}

	/* Encrypt plaintext with counter starting at 1 */
	awg_chacha20_xor(subkey, subnonce, 1, pt_buf, pt_len);

	/* Compute tag: Poly1305(poly_key, pad16(AAD) || pad16(ct) || le64(aad_len) || le64(ct_len)) */
	poly1305_init(&poly, poly_key_block);

	if (aad_len >= 16)
		poly1305_blocks(&poly, aad, aad_len & ~0xfUL, 1 << 24);
	if (aad_len & 0xf)
		poly1305_process_partial(&poly, aad + (aad_len & ~0xfUL), aad_len & 0xf);
	pad16(&poly, aad_len);

	if (pt_len >= 16)
		poly1305_blocks(&poly, pt_buf, pt_len & ~0xfUL, 1 << 24);
	if (pt_len & 0xf)
		poly1305_process_partial(&poly, pt_buf + (pt_len & ~0xfUL), pt_len & 0xf);
	pad16(&poly, pt_len);

	memset(len_block, 0, 16);
	put_unaligned_le64(aad_len, len_block);
	put_unaligned_le64(pt_len, len_block + 8);
	poly1305_blocks(&poly, len_block, 16, 1 << 24);

	poly1305_final(&poly, tag);
	memcpy(pt_buf + pt_len, tag, 16);

	memzero_explicit(subkey, sizeof(subkey));
	memzero_explicit(poly_key_block, sizeof(poly_key_block));
	memzero_explicit(poly_key_state, sizeof(poly_key_state));
	return 0;
}

/*
 * XChaCha20-Poly1305 decrypt in-place.
 * ct_with_tag has ct_len bytes (includes 16-byte tag at end).
 * On success, plaintext is in ct_with_tag[0..ct_len-16], returns 0.
 * On tag mismatch, returns -EBADMSG.
 */
int awg_xchacha20p1305_decrypt(const u8 key[32], const u8 nonce[24],
			       const u8 *aad, size_t aad_len,
			       u8 *ct_with_tag, size_t ct_with_tag_len)
{
	u8 subkey[32], subnonce[12];
	u8 poly_key_block[64];
	u32 poly_key_state[16];
	struct poly1305_state poly;
	u8 len_block[16];
	u8 tag[16];
	size_t ct_len;
	int i, ret;

	if (ct_with_tag_len < 16)
		return -EINVAL;

	ct_len = ct_with_tag_len - 16;

	/* XChaCha: derive subkey */
	hchacha20(subkey, key, nonce);
	memset(subnonce, 0, 4);
	memcpy(subnonce + 4, nonce + 16, 8);

	/* Generate Poly1305 key from block 0 */
	memset(poly_key_block, 0, 64);
	{
		u32 state[16];

		state[0] = chacha_constants[0];
		state[1] = chacha_constants[1];
		state[2] = chacha_constants[2];
		state[3] = chacha_constants[3];
		for (i = 0; i < 8; i++)
			state[4 + i] = get_unaligned_le32(subkey + i * 4);
		state[12] = 0;
		state[13] = get_unaligned_le32(subnonce);
		state[14] = get_unaligned_le32(subnonce + 4);
		state[15] = get_unaligned_le32(subnonce + 8);
		chacha20_block(poly_key_state, state);
		for (i = 0; i < 16; i++)
			put_unaligned_le32(poly_key_state[i],
					   poly_key_block + i * 4);
		memzero_explicit(state, sizeof(state));
	}

	/* Verify tag over ciphertext (before decrypting) */
	poly1305_init(&poly, poly_key_block);

	if (aad_len >= 16)
		poly1305_blocks(&poly, aad, aad_len & ~0xfUL, 1 << 24);
	if (aad_len & 0xf)
		poly1305_process_partial(&poly, aad + (aad_len & ~0xfUL), aad_len & 0xf);
	pad16(&poly, aad_len);

	if (ct_len >= 16)
		poly1305_blocks(&poly, ct_with_tag, ct_len & ~0xfUL, 1 << 24);
	if (ct_len & 0xf)
		poly1305_process_partial(&poly, ct_with_tag + (ct_len & ~0xfUL), ct_len & 0xf);
	pad16(&poly, ct_len);

	memset(len_block, 0, 16);
	put_unaligned_le64(aad_len, len_block);
	put_unaligned_le64(ct_len, len_block + 8);
	poly1305_blocks(&poly, len_block, 16, 1 << 24);

	poly1305_final(&poly, tag);

	ret = memcmp(tag, ct_with_tag + ct_len, 16) ? -EBADMSG : 0;

	if (!ret)
		awg_chacha20_xor(subkey, subnonce, 1, ct_with_tag, ct_len);

	memzero_explicit(subkey, sizeof(subkey));
	memzero_explicit(poly_key_block, sizeof(poly_key_block));
	memzero_explicit(poly_key_state, sizeof(poly_key_state));
	memzero_explicit(tag, sizeof(tag));
	return ret;
}
