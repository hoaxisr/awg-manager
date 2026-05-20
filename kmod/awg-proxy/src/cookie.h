/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _AWG_PROXY_COOKIE_H
#define _AWG_PROXY_COOKIE_H

#include <linux/types.h>

struct crypto_aead;

/*
 * Allocate / free the XChaCha20-Poly1305 AEAD context used for cookie_reply
 * translation. One per slot, set up at slot creation when has_server_pub.
 */
int  awg_cookie_aead_create(struct crypto_aead **out);
void awg_cookie_aead_destroy(struct crypto_aead *aead);

/*
 * XChaCha20-Poly1305 ops.
 *
 * Decrypt: ct_with_tag holds the ciphertext + 16-byte Poly1305 tag on input
 * (length = ct_with_tag_len). On success the buffer contains the plaintext
 * in its first (ct_with_tag_len - 16) bytes; remaining 16 bytes are
 * undefined. Returns 0 on success, -EBADMSG on tag mismatch, -EINVAL on
 * bad input.
 *
 * Encrypt: pt_out_buf holds plaintext on input. Buffer must have room for
 * pt_len + 16 bytes (ciphertext + tag). On success the buffer holds
 * ciphertext || tag in its first pt_len + 16 bytes. Returns 0 on success.
 */
int awg_xchacha20p1305_decrypt(struct crypto_aead *aead, const u8 key[32],
			       const u8 nonce_24[24],
			       const u8 *aad, size_t aad_len,
			       u8 *ct_with_tag, size_t ct_with_tag_len);

int awg_xchacha20p1305_encrypt(struct crypto_aead *aead, const u8 key[32],
			       const u8 nonce_24[24],
			       const u8 *aad, size_t aad_len,
			       u8 *pt_out_buf, size_t pt_len);

#endif /* _AWG_PROXY_COOKIE_H */
