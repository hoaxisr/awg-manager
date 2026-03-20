/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _AWG_BLAKE2S_H
#define _AWG_BLAKE2S_H

#include <linux/types.h>

/* BLAKE2s-256 (unkeyed, 32-byte output) */
void blake2s_256(const void *data, size_t len, u8 out[32]);

/* BLAKE2s-128 MAC (keyed, 16-byte output) */
void blake2s_128mac(const u8 key[32], const void *data, size_t len, u8 out[16]);

/* Derive MAC1 key: BLAKE2s-256("mac1----" || pubkey) */
void compute_mac1_key(const u8 pubkey[32], u8 mac1key[32]);

/* Recompute MAC1 in handshake init (148 bytes). MAC1 at [116:132], covers [0:116]. */
void recompute_mac1(u8 *buf, const u8 mac1key[32]);

/* Recompute MAC1 in handshake response (92 bytes). MAC1 at [60:76], covers [0:60]. */
void recompute_mac1_response(u8 *buf, const u8 mac1key[32]);

#endif
