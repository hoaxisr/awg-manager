/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _AWG_PROXY_COOKIE_H
#define _AWG_PROXY_COOKIE_H

#include <linux/types.h>

int awg_xchacha20p1305_decrypt(const u8 key[32], const u8 nonce[24],
			       const u8 *aad, size_t aad_len,
			       u8 *ct_with_tag, size_t ct_with_tag_len);

int awg_xchacha20p1305_encrypt(const u8 key[32], const u8 nonce[24],
			       const u8 *aad, size_t aad_len,
			       u8 *pt_out_buf, size_t pt_len);

/* Raw IETF/RFC-8439 ChaCha20 keystream XOR (in place), block counter starting
 * at `counter`. Public entry point for AmneziaWG 3.0+ header protection, which
 * XORs the WireGuard header with header_protection_key (used directly as the
 * key) and nonce = the first 12 on-wire junk-padding bytes, counter 0. Handles
 * multi-block spans (up to the largest handshake, 148 bytes = 3 blocks).
 */
void awg_chacha20_xor(const u8 key[32], const u8 nonce[12], u32 counter,
		      u8 *data, size_t len);

#endif /* _AWG_PROXY_COOKIE_H */
