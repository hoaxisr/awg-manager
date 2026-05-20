// SPDX-License-Identifier: GPL-2.0
/*
 * XChaCha20-Poly1305 AEAD helpers for cookie_reply AAD translation.
 *
 * Composed from kernel-exported primitives:
 *   - hchacha_block() (lib/chacha.c) for HChaCha20 subkey derivation
 *   - "rfc7539(chacha20,poly1305)" AEAD template for the ChaCha20-Poly1305
 *     stage
 *
 * Used by proxy.c's s2c thread to translate the AEAD on incoming cookie_reply
 * packets so the local vanilla-WG client can decrypt with its stored MAC1
 * (computed pre-recompute) instead of the server's MAC1 (post-recompute).
 */

#include <linux/kernel.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/crypto.h>
#include <linux/scatterlist.h>
#include <crypto/aead.h>
#include <crypto/chacha.h>

#include "cookie.h"

/*
 * Derive the XChaCha20 subkey: subkey = HChaCha20(key, nonce[:16]).
 * The remaining 8 bytes of the 24-byte XChaCha20 nonce become the lower
 * 8 bytes of the inner ChaCha20-Poly1305's 12-byte IV (top 4 zero).
 */
static void hchacha20_subkey(u8 subkey[32], const u8 key[32],
			     const u8 nonce_16[16])
{
	u32 state[16];
	u32 out_words[8];

	chacha_init_consts(state);
	memcpy(&state[4], key, 32);
	memcpy(&state[12], nonce_16, 16);

	hchacha_block(state, out_words, 20);

	memcpy(subkey, out_words, 32);
	memzero_explicit(state, sizeof(state));
	memzero_explicit(out_words, sizeof(out_words));
}

int awg_cookie_aead_create(struct crypto_aead **out)
{
	struct crypto_aead *aead;
	int ret;

	aead = crypto_alloc_aead("rfc7539(chacha20,poly1305)", 0,
				 CRYPTO_ALG_ASYNC);
	if (IS_ERR(aead))
		return PTR_ERR(aead);

	ret = crypto_aead_setauthsize(aead, 16);
	if (ret) {
		crypto_free_aead(aead);
		return ret;
	}

	*out = aead;
	return 0;
}

void awg_cookie_aead_destroy(struct crypto_aead *aead)
{
	if (aead && !IS_ERR(aead))
		crypto_free_aead(aead);
}

/*
 * AEAD operation helper. dir=0 → decrypt, dir=1 → encrypt.
 * Buffer layout for crypto_aead is [AAD || payload]. We build a temp buffer,
 * run the op, then copy payload back to the caller.
 *
 * For decrypt: payload_in = ciphertext || tag (16 bytes), payload_out = plaintext.
 * For encrypt: payload_in = plaintext, payload_out = ciphertext || tag.
 */
static int xchacha20p1305_op(int dir, struct crypto_aead *aead,
			     const u8 key[32], const u8 nonce_24[24],
			     const u8 *aad, size_t aad_len,
			     u8 *payload, size_t payload_len)
{
	u8 subkey[32];
	u8 iv[12];
	struct aead_request *req;
	struct scatterlist sg;
	u8 *combined;
	size_t total;
	int ret;

	hchacha20_subkey(subkey, key, nonce_24);

	memset(iv, 0, 4);
	memcpy(iv + 4, nonce_24 + 16, 8);

	total = aad_len + payload_len + (dir ? 16 : 0);
	combined = kmalloc(total, GFP_KERNEL);
	if (!combined) {
		ret = -ENOMEM;
		goto out_zeroize;
	}
	memcpy(combined, aad, aad_len);
	memcpy(combined + aad_len, payload, payload_len);

	ret = crypto_aead_setkey(aead, subkey, 32);
	if (ret)
		goto out_free;

	req = aead_request_alloc(aead, GFP_KERNEL);
	if (!req) {
		ret = -ENOMEM;
		goto out_free;
	}

	sg_init_one(&sg, combined, total);
	aead_request_set_callback(req, 0, NULL, NULL);
	aead_request_set_crypt(req, &sg, &sg, payload_len, iv);
	aead_request_set_ad(req, aad_len);

	if (dir)
		ret = crypto_aead_encrypt(req);
	else
		ret = crypto_aead_decrypt(req);

	aead_request_free(req);

	if (ret == 0) {
		size_t out_len = dir ? (payload_len + 16) : (payload_len - 16);
		memcpy(payload, combined + aad_len, out_len);
	}

out_free:
	memzero_explicit(combined, total);
	kfree(combined);
out_zeroize:
	memzero_explicit(subkey, sizeof(subkey));
	memzero_explicit(iv, sizeof(iv));
	return ret;
}

int awg_xchacha20p1305_decrypt(struct crypto_aead *aead, const u8 key[32],
			       const u8 nonce_24[24],
			       const u8 *aad, size_t aad_len,
			       u8 *ct_with_tag, size_t ct_with_tag_len)
{
	/* ct_with_tag_len includes ciphertext + 16-byte tag */
	if (ct_with_tag_len < 16)
		return -EINVAL;
	return xchacha20p1305_op(0, aead, key, nonce_24, aad, aad_len,
				 ct_with_tag, ct_with_tag_len);
}

int awg_xchacha20p1305_encrypt(struct crypto_aead *aead, const u8 key[32],
			       const u8 nonce_24[24],
			       const u8 *aad, size_t aad_len,
			       u8 *pt_out_buf, size_t pt_len)
{
	/* pt_out_buf must have room for pt_len + 16 bytes (tag) */
	return xchacha20p1305_op(1, aead, key, nonce_24, aad, aad_len,
				 pt_out_buf, pt_len);
}
